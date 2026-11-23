package collie

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"testing/fstest"
	"time"
)

func newPendingTestEnv(t *testing.T) (*Super, *sql.DB, func(method, path string, body any) *httptest.ResponseRecorder) {
	t.Helper()
	dir := t.TempDir()
	db, err := openDB(filepath.Join(dir, "herd.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	st, err := loadState(db, dir)
	if err != nil {
		t.Fatal(err)
	}
	srv := &Server{token: "test-tok", home: dir, web: fstest.MapFS{}}
	sp := &Super{s: srv, db: db, dir: dir, st: st, subs: map[chan struct{}]bool{}}
	srv.sup = sp
	mux := http.NewServeMux()
	srv.superRoutes(mux)

	call := func(method, path string, body any) *httptest.ResponseRecorder {
		var rd *bytes.Reader
		if body != nil {
			b, _ := json.Marshal(body)
			rd = bytes.NewReader(b)
		} else {
			rd = bytes.NewReader(nil)
		}
		req := httptest.NewRequest(method, path, rd)
		req.Header.Set("Authorization", "Bearer test-tok")
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		return rec
	}
	return sp, db, call
}

func pendingIDs(t *testing.T, call func(string, string, any) *httptest.ResponseRecorder) []string {
	t.Helper()
	rec := call("GET", "/api/super", nil)
	if rec.Code != 200 {
		t.Fatalf("GET /api/super = %d: %s", rec.Code, rec.Body.String())
	}
	var snap superView
	if err := json.Unmarshal(rec.Body.Bytes(), &snap); err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, p := range snap.Pending {
		ids = append(ids, p.ID)
	}
	return ids
}

func TestPendingScopeAndFourResolutionRules(t *testing.T) {
	sp, _, call := newPendingTestEnv(t)

	// 0 items initially; plain Collie replies and other threads never count.
	if got := pendingIDs(t, call); len(got) != 0 {
		t.Fatalf("initial pending = %v, want empty", got)
	}
	sp.Reply(mainThread, "普通进展汇报，没有选项按钮", "", nil)
	sp.Reply("github:org/repo#9", "别的线程里的按钮不计入主会话提醒", "", []string{"合并", "关闭"})
	if got := pendingIDs(t, call); len(got) != 0 {
		t.Fatalf("non-option or non-main pending = %v, want empty", got)
	}

	// Rule ① / ②: tapping an option button or quoting (re == e.ID) resolves it,
	// even if another Collie message arrived in between.
	e1, _ := sp.Reply(mainThread, "发布说明写好了，要直接开 PR 吗？", "", []string{"直接开 PR", "先不用"})
	sp.Reply(mainThread, "顺便报个进度：分页测试跑完一半了。", "", nil)
	if got := pendingIDs(t, call); !slices.Equal(got, []string{e1.ID}) {
		t.Fatalf("pending before option click = %v, want [%s]", got, e1.ID)
	}
	if rec := call("POST", "/api/super/say", map[string]any{"text": "直接开 PR", "re": e1.ID}); rec.Code != 200 {
		t.Fatalf("say status = %d", rec.Code)
	}
	if got := pendingIDs(t, call); len(got) != 0 {
		t.Fatalf("pending after option/quote reply = %v, want empty", got)
	}

	// Rule ③a: a plain user message right after the option message resolves it,
	// even when worker/system event lines sit between them.
	e2, _ := sp.Reply(mainThread, "要重跑全量算子还是复用打分表？", "", []string{"复用打分表", "重跑算子"})
	sp.mu.Lock()
	sp.addEntryLocked("event", mainThread, "df-check 完成了一轮", "df-check", "done")
	sp.addEntryLocked("event", mainThread, "Collie 换了一个新会话，记忆已交接，点此翻看之前的对话", "h:1", "info")
	sp.mu.Unlock()
	if got := pendingIDs(t, call); !slices.Equal(got, []string{e2.ID}) {
		t.Fatalf("pending after events = %v, want [%s]", got, e2.ID)
	}
	if rec := call("POST", "/api/super/say", map[string]any{"text": "直接复用现有表就行，别重跑"}); rec.Code != 200 {
		t.Fatalf("say status = %d", rec.Code)
	}
	if got := pendingIDs(t, call); len(got) != 0 {
		t.Fatalf("pending after direct plain reply = %v, want empty", got)
	}

	// Rule ③b: if another Collie reply arrives before the user speaks, a plain
	// unquoted user message does NOT guess/resolve the earlier option message.
	otherCollie, _ := sp.Reply(mainThread, "另一件事：日志清理做完了", "", nil)
	e3, _ := sp.Reply(mainThread, "旧归档目录要一并删掉吗？", "", []string{"保留归档", "直接删掉"})
	// Quoting a DIFFERENT Collie message right after e3 also must not resolve e3.
	if rec := call("POST", "/api/super/say", map[string]any{"text": "日志那块收到", "re": otherCollie.ID}); rec.Code != 200 {
		t.Fatalf("say quoting other = %d", rec.Code)
	}
	if got := pendingIDs(t, call); !slices.Equal(got, []string{e3.ID}) {
		t.Fatalf("pending after quoting another entry = %v, want [%s]", got, e3.ID)
	}
	// Now Collie speaks again before the human answers e3.
	sp.Reply(mainThread, "收到，另外回归测试也全绿了。", "", nil)
	if rec := call("POST", "/api/super/say", map[string]any{"text": "太好了，辛苦"}); rec.Code != 200 {
		t.Fatalf("plain say after intervening collie = %d", rec.Code)
	}
	if got := pendingIDs(t, call); !slices.Equal(got, []string{e3.ID}) {
		t.Fatalf("e3 should stay pending when Collie spoke in between, got %v", got)
	}

	// Two consecutive option messages in the same turn: an unquoted reply only
	// resolves the latest one (right above the user's message), keeping earlier ones safe.
	e4, _ := sp.Reply(mainThread, "问题 A：要不要升级依赖？", "", []string{"升", "不升"})
	e5, _ := sp.Reply(mainThread, "问题 B：要不要顺手发版？", "", []string{"发版", "先不发"})
	if got := pendingIDs(t, call); !slices.Equal(got, []string{e3.ID, e4.ID, e5.ID}) {
		t.Fatalf("pending before plain reply to consecutive = %v", got)
	}
	call("POST", "/api/super/say", map[string]any{"text": "先不发"})
	if got := pendingIDs(t, call); !slices.Equal(got, []string{e3.ID, e4.ID}) {
		t.Fatalf("plain reply should resolve e5 only, got %v", got)
	}

	// Quoting e3 many turns later resolves e3 (Rule ②).
	call("POST", "/api/super/say", map[string]any{"text": "保留归档", "re": e3.ID})
	if got := pendingIDs(t, call); !slices.Equal(got, []string{e4.ID}) {
		t.Fatalf("quoting e3 later should leave only e4, got %v", got)
	}

	// Rule ④: dismissing e4 via POST /api/super/dismiss resolves it and is idempotent.
	if rec := call("POST", "/api/super/dismiss", map[string]any{"id": ""}); rec.Code != 400 {
		t.Fatalf("empty dismiss status = %d, want 400", rec.Code)
	}
	for range 2 {
		if rec := call("POST", "/api/super/dismiss", map[string]any{"id": e4.ID}); rec.Code != 200 {
			t.Fatalf("dismiss status = %d: %s", rec.Code, rec.Body.String())
		}
	}
	if got := pendingIDs(t, call); len(got) != 0 {
		t.Fatalf("pending after dismiss = %v, want empty", got)
	}
}

func TestPendingDismissPersistenceAgeAndTrimmedQuote(t *testing.T) {
	dir := t.TempDir()
	db, err := openDB(filepath.Join(dir, "herd.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	st, err := loadState(db, dir)
	if err != nil {
		t.Fatal(err)
	}
	srv := &Server{token: "tok", home: dir, web: fstest.MapFS{}}
	sp := &Super{s: srv, db: db, dir: dir, st: st, subs: map[chan struct{}]bool{}}
	srv.sup = sp

	// Stale unanswered option older than pendingMaxAge does not surface.
	sp.mu.Lock()
	stale := &Entry{
		ID: sp.st.nextID("e"), Role: "collie", Thread: mainThread,
		Text: "两周前的陈年问题", At: time.Now().Add(-8 * 24 * time.Hour),
		Options: []string{"继续", "算了"},
	}
	sp.st.Entries = append(sp.st.Entries, stale)
	sp.changed()
	sp.mu.Unlock()

	eKeep, _ := sp.Reply(mainThread, "**标题**：看 [升级指南](https://example.com/doc) 里的 `config.yaml`，要继续吗？\n```sh\nrm -rf tmp\n```", "", []string{"继续", "取消"})
	eDismiss, _ := sp.Reply(mainThread, "要清理旧镜像吗？", "", []string{"清理", "保留"})

	// Subscribe to SSE wakeup channel to verify Dismiss broadcasts immediately.
	ch, unsub := sp.subscribe()
	defer unsub()
	sp.Dismiss(eDismiss.ID)
	select {
	case <-ch:
	default:
		t.Fatal("Dismiss did not notify subscribers")
	}

	// Reload from SQLite (simulating hub restart) without an extra periodic tick:
	// Dismiss flushes immediately, so eDismiss stays dismissed after restart.
	reloaded, err := loadState(db, dir)
	if err != nil {
		t.Fatal(err)
	}
	sp2 := &Super{s: srv, db: db, dir: dir, st: reloaded, subs: map[chan struct{}]bool{}}
	srv.sup = sp2

	snap := srv.superState(t.Context())
	if len(snap.Pending) != 1 || snap.Pending[0].ID != eKeep.ID {
		t.Fatalf("pending after restart = %+v, want only %s", snap.Pending, eKeep.ID)
	}
	wantSum := "标题：看 升级指南 里的 config.yaml，要继续吗？"
	if snap.Pending[0].Summary != wantSum {
		t.Fatalf("summary = %q, want %q", snap.Pending[0].Summary, wantSum)
	}

	// Verify collieQuoteLocked still resolves quotes for an older entry trimmed
	// from in-memory st.Entries (e.g. after scrolling back via /api/super/earlier).
	oldTarget, _ := sp2.Reply(mainThread, "很早以前的一条待确认消息", "", []string{"确认"})
	for i := 0; i < 610; i++ {
		sp2.mu.Lock()
		sp2.addEntryLocked("event", mainThread, fmt.Sprintf("worker-%d 完成了一轮", i), "w", "done")
		sp2.mu.Unlock()
	}
	sp2.flush() // saves to SQLite and trims st.Entries to 600
	sp2.mu.Lock()
	inMem := false
	for _, x := range sp2.st.Entries {
		if x.ID == oldTarget.ID {
			inMem = true
		}
	}
	q := sp2.collieQuoteLocked(oldTarget.ID)
	sp2.mu.Unlock()
	if inMem {
		t.Fatal("expected oldTarget to be trimmed from in-memory st.Entries")
	}
	if q == nil || q.Entry != oldTarget.ID || !strings.Contains(q.Text, "很早以前") {
		t.Fatalf("collieQuoteLocked fallback from SQLite failed: %+v", q)
	}
}

func TestReplyCheckPersistencePendingAndFourResolutions(t *testing.T) {
	sp, db, call := newPendingTestEnv(t)

	// Non-main thread --check does not enter chat:main pending.
	if rec := call("POST", "/api/super/reply", map[string]any{
		"thread": "github:org/repo#12",
		"text":   "这条 PR 线程里的待办不进主会话提醒",
		"check":  true,
	}); rec.Code != 200 {
		t.Fatalf("reply status = %d", rec.Code)
	}
	if got := pendingIDs(t, call); len(got) != 0 {
		t.Fatalf("non-main --check pending = %v, want empty", got)
	}

	// Rule ①: --check without options enters pending; clicking「已看」calls /api/super/dismiss.
	rec1 := call("POST", "/api/super/reply", map[string]any{
		"text":  "飞书扫出 2 条需要你本人回复的单聊待办，详见清单。",
		"check": true,
	})
	if rec1.Code != 200 {
		t.Fatalf("reply --check status = %d: %s", rec1.Code, rec1.Body.String())
	}
	var r1 struct {
		Entry string `json:"entry"`
	}
	json.Unmarshal(rec1.Body.Bytes(), &r1)
	if got := pendingIDs(t, call); !slices.Equal(got, []string{r1.Entry}) {
		t.Fatalf("pending after --check = %v, want [%s]", got, r1.Entry)
	}
	if rec := call("POST", "/api/super/dismiss", map[string]any{"id": r1.Entry}); rec.Code != 200 {
		t.Fatalf("dismiss --check status = %d", rec.Code)
	}
	if got := pendingIDs(t, call); len(got) != 0 {
		t.Fatalf("pending after dismiss = %v, want empty", got)
	}

	// Rule ② & ③b: intervening Collie reply preserves --check until quoted (re == e.ID).
	cQuote, _ := sp.Reply(mainThread, "日报里发现昨天延迟抖动了两次，你空了看一眼原因是什么？", "", nil, true)
	sp.Reply(mainThread, "顺便说下：定时巡检照常跑完了。", "", nil)
	call("POST", "/api/super/say", map[string]any{"text": "巡检收到"})
	if got := pendingIDs(t, call); !slices.Equal(got, []string{cQuote.ID}) {
		t.Fatalf("cQuote should stay pending across intervening Collie reply, got %v", got)
	}
	call("POST", "/api/super/say", map[string]any{"text": "那是昨天机房切流，不用管", "re": cQuote.ID})
	if got := pendingIDs(t, call); len(got) != 0 {
		t.Fatalf("quoting cQuote should resolve it, got %v", got)
	}

	// Rule ③a: direct unquoted user reply (even with worker event in between) resolves --check.
	cDirect, _ := sp.Reply(mainThread, "这个开放式问题想听你的想法：下周重点先排哪块？", "", nil, true)
	sp.mu.Lock()
	sp.addEntryLocked("event", mainThread, "worker-a 完成了一轮", "worker-a", "done")
	sp.mu.Unlock()
	if got := pendingIDs(t, call); !slices.Equal(got, []string{cDirect.ID}) {
		t.Fatalf("cDirect pending = %v, want [%s]", got, cDirect.ID)
	}
	call("POST", "/api/super/say", map[string]any{"text": "先做稳定性专项"})
	if got := pendingIDs(t, call); len(got) != 0 {
		t.Fatalf("direct reply should resolve cDirect, got %v", got)
	}

	// Persistence across SQLite saveState/loadState and /api/super/earlier, plus 7-day cutoff.
	sp.mu.Lock()
	sp.st.Entries = append(sp.st.Entries, &Entry{
		ID: sp.st.nextID("e"), Role: "collie", Thread: mainThread,
		Text: "八天前的陈年 --check", At: time.Now().Add(-8 * 24 * time.Hour), Check: true,
	})
	sp.changed()
	sp.mu.Unlock()

	cKeep, _ := sp.Reply(mainThread, "验收报告放好了，请过目。", "", nil, true)
	cIgnore, _ := sp.Reply(mainThread, "这条点 × 忽略。", "", nil, true)
	// Rule ④: × in pending bar calls Dismiss.
	sp.Dismiss(cIgnore.ID)
	sp.flush()

	reloaded, err := loadState(db, sp.dir)
	if err != nil {
		t.Fatal(err)
	}
	srv2 := &Server{token: "test-tok", home: sp.dir, web: fstest.MapFS{}}
	sp2 := &Super{s: srv2, db: db, dir: sp.dir, st: reloaded, subs: map[chan struct{}]bool{}}
	srv2.sup = sp2

	snap := srv2.superState(t.Context())
	if len(snap.Pending) != 1 || snap.Pending[0].ID != cKeep.ID || !snap.Pending[0].Check {
		t.Fatalf("reloaded pending = %+v, want only %s (check=true)", snap.Pending, cKeep.ID)
	}
	var foundCheck bool
	for _, e := range snap.Entries {
		if e.ID == cKeep.ID && e.Check {
			foundCheck = true
		}
	}
	if !foundCheck {
		t.Fatalf("reloaded entries missing Check=true for %s", cKeep.ID)
	}

	earlierRec := call("GET", "/api/super/earlier?before="+cIgnore.ID, nil)
	var earlierOut struct {
		Entries []*Entry `json:"entries"`
	}
	if err := json.Unmarshal(earlierRec.Body.Bytes(), &earlierOut); err != nil {
		t.Fatal(err)
	}
	var earlierHasCheck bool
	for _, e := range earlierOut.Entries {
		if e.ID == cKeep.ID && e.Check {
			earlierHasCheck = true
		}
	}
	if !earlierHasCheck {
		t.Fatalf("/api/super/earlier did not preserve Check=true for %s", cKeep.ID)
	}
}
