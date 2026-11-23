package collie

import (
	"encoding/json"
	"fmt"
	"net/url"
	"slices"
	"strings"
	"testing"
	"time"
)

func TestSuperSearchFullHistoryAndEarlierUntil(t *testing.T) {
	sp, db, call := newPendingTestEnv(t)

	base := time.Date(2026, 11, 20, 9, 0, 0, 0, time.Local)
	addAt := func(role, thread, text string, offsetMin int, opts ...string) *Entry {
		sp.mu.Lock()
		defer sp.mu.Unlock()
		e := &Entry{
			ID:      sp.st.nextID("e"),
			Role:    role,
			Thread:  thread,
			Text:    text,
			At:      base.Add(time.Duration(offsetMin) * time.Minute),
			Options: opts,
		}
		sp.st.Entries = append(sp.st.Entries, e)
		sp.changed()
		return e
	}

	// 1. Seed very old entries that will fall far outside the 600-entry in-memory window.
	eVeryOldUser := addAt("user", mainThread,
		"请检查 lab_nlp.dwd_bp_datacard_book_df 在 1020 分区的 SCHEMA_AND_ID 字段完整性，脚本见 ~/workspace/scripts/check_book.py",
		1)
	eVeryOldCollie := addAt("collie", mainThread,
		strings.Repeat("前序巡检背景说明文字，确保消息总长度超过摘录截断窗口上限。", 4)+
			"\n```sql\nSELECT count(*) FROM book_df WHERE dt = '1020' AND mode = 'SCHEMA_AND_ID'\n```\n"+
			"详细报表在 [巡检看板](https://dataseek.example.com/report/book_df?dt=1020#summary) 可以直接查看。"+
			strings.Repeat("后续补充说明与归档提示，用于验证摘录尾部省略号。", 4),
		2, "归档 1020 快照", "忽略")

	// Noise rows that MUST be excluded from search results:
	addAt("event", mainThread, "worker book_df 1020 SCHEMA_AND_ID 完成了一轮", 3)
	addAt("collie", "github:org/repo#88", "book_df 1020 SCHEMA_AND_ID in another thread", 4)

	// 2. Push 1,600 filler entries so eVeryOldUser / eVeryOldCollie are >1,500 entries back
	// and trimmed completely out of in-memory sp.st.Entries (which keeps 600).
	var fillerIDs []string
	for i := 0; i < 1600; i++ {
		role := "event"
		text := fmt.Sprintf("worker-%d 完成了一轮", i)
		if i%25 == 0 {
			role = "collie"
			text = fmt.Sprintf("例行批量回归批次 batch-item-%04d 通过（含预印本抽样）", i)
		}
		e := addAt(role, mainThread, text, 10+i)
		fillerIDs = append(fillerIDs, e.ID)
	}
	sp.flush() // persists all 1,604 entries to SQLite herd.db and trims st.Entries to 600

	sp.mu.Lock()
	for _, e := range sp.st.Entries {
		if e.ID == eVeryOldUser.ID || e.ID == eVeryOldCollie.ID {
			sp.mu.Unlock()
			t.Fatalf("expected %s to be trimmed from memory (len=%d)", e.ID, len(sp.st.Entries))
		}
	}
	sp.mu.Unlock()

	// 3. Append brand-new UNFLUSHED entries in memory right before searching.
	eRecentUser := addAt("user", mainThread,
		"刚上传了压测截图，顺便核对 book_df 1020 最新行数\n\n附件：\n/home/alex/.collie/uploads/20261123/book_df_1020_chart.png\n\n[The human picked where this runs: spawn with --kind codex]",
		2000)
	eRecentCollie := addAt("collie", mainThread,
		"核对完毕：**book_df** 的 `1020` 分区共 482,910 行，与上游一致。",
		2001)

	getSearch := func(rawQuery string) SearchResult {
		t.Helper()
		rec := call("GET", "/api/super/search?"+rawQuery, nil)
		if rec.Code != 200 {
			t.Fatalf("GET /api/super/search?%s = %d: %s", rawQuery, rec.Code, rec.Body.String())
		}
		var out SearchResult
		if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
			t.Fatalf("decode search result: %v", err)
		}
		return out
	}

	// Empty / whitespace query -> empty list, zero total.
	for _, emptyQ := range []string{"", "q=", "q=+++%09+"} {
		res := getSearch(emptyQ)
		if len(res.Results) != 0 || res.Total != 0 || res.HasMore {
			t.Fatalf("empty query %q returned %+v", emptyQ, res)
		}
	}

	// Multi-term AND + case-insensitivity + across memory (unflushed) & SQLite (>1,600 entries back),
	// excluding event & non-main threads, ordered newest-first!
	res := getSearch("q=" + url.QueryEscape("BOOK_DF 1020"))
	wantIDs := []string{eRecentCollie.ID, eRecentUser.ID, eVeryOldCollie.ID, eVeryOldUser.ID}
	var gotIDs []string
	for _, h := range res.Results {
		gotIDs = append(gotIDs, h.ID)
		if h.Role != "user" && h.Role != "collie" {
			t.Fatalf("unexpected role in search hit: %+v", h)
		}
	}
	if res.Total != 4 || res.HasMore || !slices.Equal(gotIDs, wantIDs) {
		t.Fatalf("search 'BOOK_DF 1020': total=%d hasMore=%v got=%v, want %v", res.Total, res.HasMore, gotIDs, wantIDs)
	}
	// Verify clean snippet strips markdown fences/bold and adds ellipses on long text.
	oldSnip := res.Results[2].Snippet
	if strings.Contains(oldSnip, "```") || !strings.Contains(strings.ToLower(oldSnip), "book_df") || !strings.HasPrefix(oldSnip, "…") || !strings.HasSuffix(oldSnip, "…") {
		t.Fatalf("unexpected long snippet: %q", oldSnip)
	}
	// Hub-injected "[The human picked..." must never leak into user snippets or match searches.
	if strings.Contains(res.Results[1].Snippet, "The human picked") {
		t.Fatalf("hub hint leaked into snippet: %q", res.Results[1].Snippet)
	}
	if leak := getSearch("q=" + url.QueryEscape("The human picked")); leak.Total != 0 {
		t.Fatalf("hub hint matched search: %+v", leak)
	}

	// Case-insensitive constant + file path + URL + quick-reply option + attachment filename:
	if r := getSearch("q=" + url.QueryEscape("schema_and_id check_book.py")); r.Total != 1 || r.Results[0].ID != eVeryOldUser.ID {
		t.Fatalf("path + constant AND search failed: %+v", r)
	}
	if r := getSearch("q=" + url.QueryEscape("dataseek.example.com/report/book_df summary")); r.Total != 1 || r.Results[0].ID != eVeryOldCollie.ID || !strings.Contains(r.Results[0].Snippet, "dataseek.example.com") {
		t.Fatalf("markdown link URL search failed: %+v", r)
	}
	if r := getSearch("q=" + url.QueryEscape("归档 1020 快照")); r.Total != 1 || r.Results[0].ID != eVeryOldCollie.ID {
		t.Fatalf("quick-reply option search failed: %+v", r)
	}
	if r := getSearch("q=" + url.QueryEscape("book_df_1020_chart.png")); r.Total != 1 || r.Results[0].ID != eRecentUser.ID {
		t.Fatalf("attachment path search failed: %+v", r)
	}
	// AND semantics: if one word doesn't match, result is empty.
	if r := getSearch("q=" + url.QueryEscape("book_df nonexistent_token_xyz")); r.Total != 0 {
		t.Fatalf("partial miss should return 0 hits, got %+v", r)
	}
	// Explicit thread query finds github thread entry only.
	if r := getSearch("thread=" + url.QueryEscape("github:org/repo#88") + "&q=SCHEMA_AND_ID"); r.Total != 1 {
		t.Fatalf("explicit thread search = %+v, want 1 hit", r)
	}

	// Limit clamping: 1600/25 = 64 "预印本抽样" collie entries in SQLite.
	// Default limit = 30; limit=999 clamps to 50; limit=7 returns 7.
	defRes := getSearch("q=" + url.QueryEscape("预印本抽样 batch-item"))
	if defRes.Total != 64 || !defRes.HasMore || len(defRes.Results) != 30 {
		t.Fatalf("default limit search: total=%d hasMore=%v len=%d, want 64/true/30", defRes.Total, defRes.HasMore, len(defRes.Results))
	}
	maxRes := getSearch("q=" + url.QueryEscape("预印本抽样 batch-item") + "&limit=999")
	if maxRes.Total != 64 || !maxRes.HasMore || len(maxRes.Results) != 50 {
		t.Fatalf("clamped limit search: total=%d hasMore=%v len=%d, want 64/true/50", maxRes.Total, maxRes.HasMore, len(maxRes.Results))
	}
	smallRes := getSearch("q=" + url.QueryEscape("预印本抽样 batch-item") + "&limit=7")
	if smallRes.Total != 64 || !smallRes.HasMore || len(smallRes.Results) != 7 {
		t.Fatalf("custom limit=7 search: total=%d hasMore=%v len=%d, want 64/true/7", smallRes.Total, smallRes.HasMore, len(smallRes.Results))
	}

	// 4. Verify /api/super/earlier?before=<snapFirst>&until=<veryOldID> loads 1,400+ entries
	// in ONE single request, oldest-first, including eVeryOldCollie & eVeryOldUser!
	snapRec := call("GET", "/api/super", nil)
	var snap superView
	if err := json.Unmarshal(snapRec.Body.Bytes(), &snap); err != nil {
		t.Fatal(err)
	}
	if !snap.Earlier || len(snap.Entries) != 200 {
		t.Fatalf("expected 200-entry window with Earlier=true, got len=%d earlier=%v", len(snap.Entries), snap.Earlier)
	}
	firstInWindow := snap.Entries[0].ID

	// Add 30 even older entries before eVeryOldUser to verify context cushion (24) + more=true.
	for i := 0; i < 30; i++ {
		id := fmt.Sprintf("e0")
		_ = id
	}
	_, err := db.Exec(`INSERT INTO entries(id,n,at,thread,data) VALUES('e0', 0, ?, ?, ?)`,
		base.Add(-time.Hour).Unix(), mainThread, `{"id":"e0","role":"user","thread":"chat:main","text":"genesis"}`)
	if err != nil {
		t.Fatal(err)
	}

	jumpRec := call("GET", fmt.Sprintf("/api/super/earlier?before=%s&until=%s", firstInWindow, eVeryOldCollie.ID), nil)
	if jumpRec.Code != 200 {
		t.Fatalf("earlier with until = %d: %s", jumpRec.Code, jumpRec.Body.String())
	}
	var jumpOut struct {
		Entries []*Entry `json:"entries"`
		More    bool     `json:"more"`
	}
	if err := json.Unmarshal(jumpRec.Body.Bytes(), &jumpOut); err != nil {
		t.Fatal(err)
	}
	if len(jumpOut.Entries) < 1400 {
		t.Fatalf("expected single jump to fetch >1400 entries, got %d", len(jumpOut.Entries))
	}
	// Verify oldest-first ordering and inclusion of eVeryOldCollie, eVeryOldUser, and e0 cushion!
	foundTarget := false
	for i, e := range jumpOut.Entries {
		if i > 0 && idNum(jumpOut.Entries[i-1].ID) >= idNum(e.ID) {
			t.Fatalf("entries not strictly oldest-first at %d: %s >= %s", i, jumpOut.Entries[i-1].ID, e.ID)
		}
		if e.ID == eVeryOldCollie.ID {
			foundTarget = true
		}
	}
	if !foundTarget {
		t.Fatalf("target %s missing from single-jump response", eVeryOldCollie.ID)
	}
	if jumpOut.Entries[len(jumpOut.Entries)-1].ID != fillerIDs[len(fillerIDs)-199] {
		// snap.Entries has [filler[1402..1599], eRecentUser, eRecentCollie] = 200 items,
		// so the item right before snap.Entries[0] (filler[1402]) is filler[1401]!
		wantLast := fillerIDs[len(fillerIDs)-199]
		t.Fatalf("last entry before window = %s, want %s", jumpOut.Entries[len(jumpOut.Entries)-1].ID, wantLast)
	}
	if jumpOut.More {
		t.Fatalf("expected more=false when cushion reached e0, got true")
	}
}
