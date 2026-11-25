package collie

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestWorkerReportQuoteUsesActualReportAndTimelineEntry(t *testing.T) {
	dir := t.TempDir()
	db, err := openDB(filepath.Join(dir, "herd.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	sp := &Super{
		db:   db,
		dir:  dir,
		st:   defaultState(dir),
		subs: map[chan struct{}]bool{},
	}
	sp.st.Workers["data-sync"] = &Worker{
		Name:   "data-sync",
		Kind:   "codex",
		Thread: mainThread,
		Task:   "初始任务 brief：请跑通 20261124 全量质检并核验底库（不该出现在回报引用里）",
	}
	eSpawn := sp.addEntryLocked("event", mainThread, "派出 data-sync（codex）：初始任务 brief", "data-sync", "spawn")

	reportBody := "BLOCKED: 需要在目标库注册空表 orders_20261125\n当前进度：50,243 行计算已落盘，等待建表后回写验收。"
	if err := sp.Report("data-sync", reportBody); err != nil {
		t.Fatalf("Report err = %v", err)
	}

	if len(sp.st.Triggers) != 1 {
		t.Fatalf("triggers = %d, want 1", len(sp.st.Triggers))
	}
	trig := sp.st.Triggers[0]
	eReport := sp.st.Entries[len(sp.st.Entries)-1]
	if trig.Entry != eReport.ID {
		t.Fatalf("trigger.Entry = %q, want report entry %q (spawn was %q)", trig.Entry, eReport.ID, eSpawn.ID)
	}

	// Simulate another worker reporting right after so the quote bar is displayed in chat.
	sp.addEntryLocked("event", mainThread, "other-worker 回报：另一路试跑完成", "other-worker", "done")

	reply, warn := sp.Reply(mainThread, "data-sync 阶段卡在建表权限，等你确认。", trig.ID, nil)
	if warn != "" {
		t.Fatalf("unexpected reply warning: %q", warn)
	}
	if reply.Re == nil {
		t.Fatalf("reply.Re is nil")
	}
	if reply.Re.Entry != eReport.ID {
		t.Fatalf("reply.Re.Entry = %q, want %q", reply.Re.Entry, eReport.ID)
	}
	if reply.Re.Worker != "data-sync" || reply.Re.Who != "data-sync" {
		t.Fatalf("reply.Re worker/who = %q/%q, want data-sync", reply.Re.Worker, reply.Re.Who)
	}
	if !strings.HasPrefix(reply.Re.Text, "BLOCKED: 需要在目标库注册空表") {
		t.Fatalf("reply.Re.Text = %q, want actual report body", reply.Re.Text)
	}
	if strings.Contains(reply.Re.Text, "初始任务 brief") || strings.Contains(reply.Re.Text, "report from") {
		t.Fatalf("reply.Re.Text leaked brief or raw header: %q", reply.Re.Text)
	}

	// Verify persistence through SQLite reload.
	sp.flush()
	reloaded, err := loadState(db, dir)
	if err != nil {
		t.Fatalf("loadState: %v", err)
	}
	if got := reloaded.trigger(trig.ID); got == nil || got.Entry != eReport.ID {
		t.Fatalf("reloaded trigger = %+v, want Entry=%q", got, eReport.ID)
	}
	var savedReply *Entry
	for _, e := range reloaded.Entries {
		if e.ID == reply.ID {
			savedReply = e
			break
		}
	}
	if savedReply == nil || savedReply.Re == nil || savedReply.Re.Entry != eReport.ID || !strings.HasPrefix(savedReply.Re.Text, "BLOCKED:") {
		t.Fatalf("reloaded reply quote corrupted: %+v", savedReply)
	}
}

func TestLegacyWorkerReportTriggerAndOtherWorkerQuotes(t *testing.T) {
	now := time.Now()
	sp := &Super{
		st: &State{
			Workers: map[string]*Worker{
				"rn": {Name: "rn", Kind: "codex", Task: "初始 spawn brief 不应替代事件正文", Thread: mainThread},
			},
			Entries: []*Entry{
				{ID: "e10", Role: "event", Thread: mainThread, Ref: "rn", Tone: "spawn", Text: "派出 rn（codex）：初始 spawn brief", At: now.Add(-5 * time.Minute)},
				{ID: "e11", Role: "event", Thread: mainThread, Ref: "rn", Tone: "done", Text: "rn 回报：BLOCKED: 缺少发布权限", At: now.Add(-2 * time.Minute)},
				{ID: "e12", Role: "event", Thread: mainThread, Ref: "rn", Tone: "done", Text: "rn 完成了一轮", At: now.Add(-1 * time.Minute)},
				{ID: "e13", Role: "event", Thread: mainThread, Ref: "rn", Tone: "blocked", Text: "rn 等你确认", At: now.Add(-50 * time.Second)},
				{ID: "e14", Role: "event", Thread: mainThread, Ref: "rn", Tone: "error", Text: "rn 出错了：Rate limit exceeded", At: now.Add(-30 * time.Second)},
				{ID: "e15", Role: "event", Thread: mainThread, Ref: "rn", Tone: "error", Text: "rn 启动失败：directory not found", At: now.Add(-20 * time.Second)},
			},
			Triggers: []*Trigger{
				// Legacy report trigger without t.Entry field.
				{ID: "t11", Source: "worker", Thread: mainThread, From: "rn", Ref: "rn", Text: "report from rn:\nBLOCKED: 缺少发布权限", At: now.Add(-2 * time.Minute)},
				// Finished-turn trigger whose paired event e12 is hidden chatter -> should anchor to visible event (e11/e10), not e12.
				{ID: "t12", Source: "worker", Thread: mainThread, From: "rn", Ref: "rn", Text: "worker rn finished a turn. Last message:\n已完成全部回归测试，12/12 通过", At: now.Add(-1 * time.Minute)},
				// Blocked dialog trigger whose paired event e13 is hidden chatter.
				{ID: "t13", Source: "worker", Thread: mainThread, From: "rn", Ref: "rn", Text: "worker rn is BLOCKED on a dialog in its terminal (the human also sees it as a card in chat). Screen:\nRun go test ./...\nIf the answer follows from what the human asked", At: now.Add(-50 * time.Second)},
				// Agent error trigger linked to e14.
				{ID: "t14", Source: "worker", Thread: mainThread, From: "rn", Ref: "rn", Entry: "e14", Text: "worker rn stopped on an error from its agent, not because it was done:\nRate limit exceeded\nLast message before it:\nrunning tests", At: now.Add(-30 * time.Second)},
				// Start failure trigger linked to e15.
				{ID: "t15", Source: "worker", Thread: mainThread, From: "rn", Ref: "rn", Entry: "e15", Text: "worker rn failed to start: directory not found", At: now.Add(-20 * time.Second)},
				// Watchdog trigger (no timeline entry of its own).
				{ID: "t16", Source: "system", Thread: mainThread, From: "watchdog", Ref: "rn", Text: "watchdog: worker rn has shown no visible progress for 12 min while 'working'. Inspect with `collie show rn`; send guidance.", At: now.Add(-10 * time.Second)},
			},
		},
	}

	// 1. Legacy report trigger without Entry field still resolves report text & report entry e11.
	q11 := sp.quoteLocked("t11")
	if q11 == nil || q11.Entry != "e11" || q11.Text != "BLOCKED: 缺少发布权限" {
		t.Fatalf("t11 quote = %+v, want Entry=e11 Text='BLOCKED: 缺少发布权限'", q11)
	}

	// 2. Finished turn trigger uses actual last message and never targets hidden chatter e12.
	q12 := sp.quoteLocked("t12")
	if q12 == nil || q12.Text != "已完成全部回归测试，12/12 通过" || q12.Entry == "e12" || q12.Entry == "" {
		t.Fatalf("t12 quote = %+v, want actual summary & visible anchor (not e12)", q12)
	}

	// 3. Blocked dialog trigger strips router instructions and never targets hidden chatter e13.
	q13 := sp.quoteLocked("t13")
	if q13 == nil || q13.Text != "等你确认：Run go test ./..." || q13.Entry == "e13" || q13.Entry == "" {
		t.Fatalf("t13 quote = %+v, want clean blocked text & visible anchor (not e13)", q13)
	}

	// 4. Agent error trigger uses clean error message and links directly to e14.
	q14 := sp.quoteLocked("t14")
	if q14 == nil || q14.Entry != "e14" || q14.Text != "出错了：Rate limit exceeded" {
		t.Fatalf("t14 quote = %+v, want Entry=e14 error summary", q14)
	}

	// 5. Start failure trigger uses clean start-failure message and links to e15.
	q15 := sp.quoteLocked("t15")
	if q15 == nil || q15.Entry != "e15" || q15.Text != "启动失败：directory not found" {
		t.Fatalf("t15 quote = %+v, want Entry=e15 start-failure summary", q15)
	}

	// 6. Watchdog trigger strips internal command hint and anchors to visible timeline row.
	q16 := sp.quoteLocked("t16")
	if q16 == nil || !strings.Contains(q16.Text, "no visible progress for 12 min") || strings.Contains(q16.Text, "Inspect with") || q16.Entry == "" {
		t.Fatalf("t16 quote = %+v, want clean watchdog summary & visible anchor", q16)
	}

	// 7. Quoting a visible timeline event directly by entry ID strips redundant "<name> 回报：" prefix.
	qe11 := sp.quoteLocked("e11")
	if qe11 == nil || qe11.Entry != "e11" || qe11.Who != "rn" || qe11.Text != "BLOCKED: 缺少发布权限" {
		t.Fatalf("e11 direct quote = %+v, want Entry=e11 Who=rn Text='BLOCKED: 缺少发布权限'", qe11)
	}
}

func TestQuoteFallbackFromSQLiteAndEarlierTimelineJump(t *testing.T) {
	dir := t.TempDir()
	db, err := openDB(filepath.Join(dir, "herd.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	sp := &Super{
		db:   db,
		dir:  dir,
		st:   defaultState(dir),
		subs: map[chan struct{}]bool{},
	}
	sp.st.Workers["archiver"] = &Worker{Name: "archiver", Kind: "claude", Thread: mainThread, Task: "旧任务 brief"}
	if err := sp.Report("archiver", "旧回报摘要：归档校验完成 100%"); err != nil {
		t.Fatal(err)
	}
	oldTrigID := sp.st.Triggers[0].ID
	oldReportEntryID := sp.st.Entries[len(sp.st.Entries)-1].ID

	// Push >620 newer entries so oldTrigID and oldReportEntryID are trimmed out of memory into SQLite.
	for i := 0; i < 620; i++ {
		sp.addEntryLocked("collie", mainThread, fmt.Sprintf("msg %d", i), "", "")
	}
	sp.flush()

	// quoteLocked must still find the trimmed trigger & entry in SQLite.
	q := sp.quoteLocked(oldTrigID)
	if q == nil || q.Entry != oldReportEntryID || q.Text != "旧回报摘要：归档校验完成 100%" {
		t.Fatalf("SQLite fallback quoteLocked(%s) = %+v, want Entry=%s", oldTrigID, q, oldReportEntryID)
	}

	// /api/super/earlier?before=<first>&until=<oldReportEntryID> must return the timeline event in one hop.
	srv := &Server{sup: sp}
	firstInLatest200 := sp.st.Entries[len(sp.st.Entries)-200].ID
	req := httptest.NewRequest(http.MethodGet, fmt.Sprintf("/api/super/earlier?before=%s&until=%s", firstInLatest200, oldReportEntryID), nil)
	rec := httptest.NewRecorder()
	srv.superEarlier(rec, req)
	if rec.Code != 200 {
		t.Fatalf("superEarlier status = %d: %s", rec.Code, rec.Body.String())
	}
	var payload struct {
		Entries []*Entry `json:"entries"`
		More    bool     `json:"more"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode superEarlier: %v", err)
	}
	found := false
	for _, e := range payload.Entries {
		if e.ID == oldReportEntryID && e.Role == "event" {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("superEarlier did not include target timeline event %s (got %d entries)", oldReportEntryID, len(payload.Entries))
	}
}
