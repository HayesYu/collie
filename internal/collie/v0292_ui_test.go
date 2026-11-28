package collie

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"
	"time"
)

func TestSuperSayPickSilentReceiptAndQuote(t *testing.T) {
	t.Parallel()
	sp, db, call := newPendingTestEnv(t)

	// 1) Collie emits an --opt decision card with --file attachment.
	recReply := call("POST", "/api/super/reply", map[string]any{
		"text":    "旧回滚脚本怎么处理？",
		"options": []string{"移到 archive 留档", "直接删掉"},
		"files":   []string{"~/code/shop-api/bench-pagination.html"},
	})
	if recReply.Code != 200 {
		t.Fatalf("POST /api/super/reply status=%d body=%s", recReply.Code, recReply.Body.String())
	}
	var replyOut struct {
		Entry string `json:"entry"`
	}
	if err := json.Unmarshal(recReply.Body.Bytes(), &replyOut); err != nil {
		t.Fatalf("unmarshal reply: %v", err)
	}
	optID := replyOut.Entry
	if optID == "" {
		t.Fatal("expected non-empty opt entry id")
	}

	getSnap := func() superView {
		rec := call("GET", "/api/super", nil)
		if rec.Code != 200 {
			t.Fatalf("GET /api/super status=%d", rec.Code)
		}
		var snap superView
		if err := json.Unmarshal(rec.Body.Bytes(), &snap); err != nil {
			t.Fatalf("unmarshal snap: %v", err)
		}
		return snap
	}

	// Verify pending contains optID and files persisted.
	snap := getSnap()
	if len(snap.Pending) != 1 || snap.Pending[0].ID != optID {
		t.Fatalf("expected pending=[%s], got %+v", optID, snap.Pending)
	}
	if len(snap.Entries) != 1 || len(snap.Entries[0].Files) != 1 || snap.Entries[0].Files[0] != "~/code/shop-api/bench-pagination.html" {
		t.Fatalf("expected files persisted on entry, got %+v", snap.Entries)
	}

	// 2) Pure option click via POST /api/super/say with pick: true
	rec := call("POST", "/api/super/say", map[string]any{
		"text":          "移到 archive 留档",
		"re":            optID,
		"pick":          true,
		"client_msg_id": "pick-cmsg-1",
	})
	if rec.Code != 200 {
		t.Fatalf("POST /api/super/say pick=true status=%d body=%s", rec.Code, rec.Body.String())
	}
	var res map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &res); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	if res["picked"] != true || res["entry"] != optID {
		t.Fatalf("expected picked=true entry=%s, got %+v", optID, res)
	}

	// Must NOT add a new user bubble; must stamp target.Pick in memory & SQLite, and resolve pending.
	snap = getSnap()
	if len(snap.Entries) != 1 {
		t.Fatalf("pick=true must not append a user bubble, got %d entries", len(snap.Entries))
	}
	if snap.Entries[0].Pick == nil || snap.Entries[0].Pick.State != "picked" || snap.Entries[0].Pick.Text != "移到 archive 留档" {
		t.Fatalf("expected target.Pick state=picked, got %+v", snap.Entries[0].Pick)
	}
	if len(snap.Pending) != 0 {
		t.Fatalf("expected pending resolved after pick=true, got %+v", snap.Pending)
	}

	// Reload from SQLite to confirm Pick & Files survive restart.
	sp.flush()
	reloaded, err := loadState(db, sp.dir)
	if err != nil {
		t.Fatalf("loadState: %v", err)
	}
	if len(reloaded.Entries) != 1 || reloaded.Entries[0].Pick == nil || reloaded.Entries[0].Pick.State != "picked" || reloaded.Entries[0].Pick.Text != "移到 archive 留档" {
		t.Fatalf("Pick did not persist to SQLite: %+v", reloaded.Entries)
	}
	if len(reloaded.Entries[0].Files) != 1 || reloaded.Entries[0].Files[0] != "~/code/shop-api/bench-pagination.html" {
		t.Fatalf("Files did not persist to SQLite: %+v", reloaded.Entries)
	}

	// 3) Collie replies referencing the trigger produced by pick=true -> quiet quote strip "你选择了" / "「…」"
	trigID, _ := res["trigger"].(string)
	if trigID == "" {
		t.Fatal("expected trigger id from pick=true")
	}
	recFollow := call("POST", "/api/super/reply", map[string]any{
		"text": "好，我把这 3 个脚本归档到 archive/。",
		"re":   trigID,
	})
	if recFollow.Code != 200 {
		t.Fatalf("follow-up reply status=%d body=%s", recFollow.Code, recFollow.Body.String())
	}
	snap = getSnap()
	if len(snap.Entries) != 2 {
		t.Fatalf("expected 2 entries after collie follow-up, got %d", len(snap.Entries))
	}
	follow := snap.Entries[1]
	if follow.Re == nil || follow.Re.Entry != optID || follow.Re.Who != "你选择了" || follow.Re.Text != "「移到 archive 留档」" {
		t.Fatalf("unexpected follow-up quote: %+v", follow.Re)
	}

	// 4) Duplicate pick with same client_msg_id must dedup cleanly.
	recDup := call("POST", "/api/super/say", map[string]any{
		"text":          "移到 archive 留档",
		"re":            optID,
		"pick":          true,
		"client_msg_id": "pick-cmsg-1",
	})
	if recDup.Code != 200 {
		t.Fatalf("dup pick status=%d", recDup.Code)
	}
	if len(getSnap().Entries) != 2 {
		t.Fatalf("dup pick must not create extra entries")
	}

	// 5) Free-text custom quote ("补充一句") on a new decision card creates a user bubble & marks state=custom.
	rec2Card := call("POST", "/api/super/reply", map[string]any{
		"text":    "需要顺便开 PR 吗？",
		"options": []string{"直接开 PR", "先不用"},
	})
	var out2 struct {
		Entry string `json:"entry"`
	}
	_ = json.Unmarshal(rec2Card.Body.Bytes(), &out2)
	opt2 := out2.Entry
	rec2 := call("POST", "/api/super/say", map[string]any{
		"text": "先建草稿 PR，顺便补上压测 CSV",
		"re":   opt2,
	})
	if rec2.Code != 200 {
		t.Fatalf("custom quote status=%d body=%s", rec2.Code, rec2.Body.String())
	}
	snap = getSnap()
	if len(snap.Entries) != 4 {
		t.Fatalf("expected custom reply to emit user bubble (4 entries total), got %d", len(snap.Entries))
	}
	var card2 *Entry
	for _, e := range snap.Entries {
		if e.ID == opt2 {
			card2 = e
		}
	}
	if card2 == nil || card2.Pick == nil || card2.Pick.State != "custom" || card2.Pick.Text != "先建草稿 PR，顺便补上压测 CSV" {
		t.Fatalf("expected card2 Pick state=custom, got %+v", card2)
	}

	// 6) Dismiss stamps state=dismissed.
	rec3Card := call("POST", "/api/super/reply", map[string]any{
		"text":    "要顺手清理缓存吗？",
		"options": []string{"清理", "保留"},
	})
	var out3 struct {
		Entry string `json:"entry"`
	}
	_ = json.Unmarshal(rec3Card.Body.Bytes(), &out3)
	opt3 := out3.Entry
	recDismiss := call("POST", "/api/super/dismiss", map[string]any{"id": opt3})
	if recDismiss.Code != 200 {
		t.Fatalf("dismiss status=%d", recDismiss.Code)
	}
	snap = getSnap()
	var card3 *Entry
	for _, e := range snap.Entries {
		if e.ID == opt3 {
			card3 = e
		}
	}
	if card3 == nil || card3.Pick == nil || card3.Pick.State != "dismissed" {
		t.Fatalf("expected card3 Pick state=dismissed, got %+v", card3)
	}
}

func TestSuperNotebookAPI(t *testing.T) {
	t.Parallel()
	sp, _, call := newPendingTestEnv(t)

	// Seed memory/tasks/*.md
	tasksDir := filepath.Join(sp.dir, "router", "memory", "tasks")
	if err := os.MkdirAll(tasksDir, 0o755); err != nil {
		t.Fatalf("mkdir tasks: %v", err)
	}
	task1 := "# v1.4 发布与分页修复\n状态：等待压测报告确认\n- [x] 修复排序\n- [ ] 确认压测结果\n"
	task2 := "# 暗色模式对比度核查\n进行中：移动端检查中\n"
	if err := os.WriteFile(filepath.Join(tasksDir, "v1.4-release.md"), []byte(task1), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(tasksDir, "dark-mode.md"), []byte(task2), 0o644); err != nil {
		t.Fatal(err)
	}

	// Seed local deliverable files (HTML with <title> + CSV)
	outDir := filepath.Join(sp.dir, "artifacts")
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		t.Fatal(err)
	}
	htmlPath := filepath.Join(outDir, "bench-report.html")
	csvPath := filepath.Join(outDir, "orders-benchmark.csv")
	if err := os.WriteFile(htmlPath, []byte("<!doctype html><html><head><title>200 轮分页压测对比</title></head><body>ok</body></html>"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(csvPath, []byte("route,p50,p99\n/orders,7.9,28.5\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	// Emit Collie reply referencing both files + an upload path (uploads must be excluded)
	recRep := call("POST", "/api/super/reply", map[string]any{
		"text":  "压测完成，表格在 `" + csvPath + "`，截图在 `/tmp/.collie/uploads/20261127/foo.png`。",
		"files": []string{htmlPath},
	})
	if recRep.Code != 200 {
		t.Fatalf("reply status=%d body=%s", recRep.Code, recRep.Body.String())
	}

	// Add 2 timers (out of order) + 1 disabled timer directly to sp.st.Timers
	now := time.Now()
	sp.mu.Lock()
	sp.st.Timers = append(sp.st.Timers,
		&Timer{ID: "tm_late", Spec: "daily 20:00", Text: "晚间复盘", Enabled: true, NextRun: now.Add(3 * time.Hour)},
		&Timer{ID: "tm_soon", Spec: "in 30m", Text: "核对压测结果", Enabled: true, NextRun: now.Add(30 * time.Minute)},
		&Timer{ID: "tm_off", Spec: "daily 09:00", Text: "已关闭提醒", Enabled: false, NextRun: now.Add(10 * time.Minute)},
	)
	sp.mu.Unlock()

	rec := call("GET", "/api/super/notebook", nil)
	if rec.Code != 200 {
		t.Fatalf("GET /api/super/notebook status=%d body=%s", rec.Code, rec.Body.String())
	}
	var nb notebookView
	if err := json.Unmarshal(rec.Body.Bytes(), &nb); err != nil {
		t.Fatalf("unmarshal notebook: %v", err)
	}
	if nb.Date != time.Now().Format("2006-01-02") {
		t.Fatalf("unexpected notebook date: %q", nb.Date)
	}
	if len(nb.Tasks) != 2 {
		t.Fatalf("expected 2 tasks from memory/tasks/*.md, got %+v", nb.Tasks)
	}
	for _, tk := range nb.Tasks {
		if !strings.HasPrefix(tk.Path, "tasks/") {
			t.Fatalf("expected memory-relative Path tasks/..., got %+v", tk)
		}
		if !strings.HasPrefix(tk.FilePath, "~/router/memory/tasks/") {
			t.Fatalf("expected FilePath normalized with ~/, got %+v", tk)
		}
	}
	if len(nb.Artifacts) != 2 {
		t.Fatalf("expected 2 artifacts (excluding uploads), got %+v", nb.Artifacts)
	}
	var gotHTML, gotCSV bool
	for _, a := range nb.Artifacts {
		if a.Name == "bench-report.html" {
			gotHTML = true
			if a.Title != "200 轮分页压测对比" || a.Ext != "html" || a.Path != "~/artifacts/bench-report.html" {
				t.Fatalf("expected HTML title peeked and ~/ path normalized, got %+v", a)
			}
		}
		if a.Name == "orders-benchmark.csv" {
			gotCSV = true
			if a.Ext != "csv" || a.Path != "~/artifacts/orders-benchmark.csv" {
				t.Fatalf("expected csv ext and ~/ path normalized, got %+v", a)
			}
		}
	}
	if !gotHTML || !gotCSV {
		t.Fatalf("missing expected artifacts: %+v", nb.Artifacts)
	}
	if len(nb.Timers) != 2 || nb.Timers[0].ID != "tm_soon" || nb.Timers[1].ID != "tm_late" {
		t.Fatalf("expected sorted enabled timers [tm_soon, tm_late], got %+v", nb.Timers)
	}
}

func TestSuperNotebookTaskFilePathReadableByAPIFile(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name           string
		separateHome   bool
		wantPathPrefix string
	}{
		{name: "default_under_home", separateHome: false, wantPathPrefix: "~/.collie/router/memory/tasks/"},
		{name: "custom_collie_home_outside_home", separateHome: true, wantPathPrefix: "/"},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			userHome := t.TempDir()
			collieDir := filepath.Join(userHome, ".collie")
			if tc.separateHome {
				collieDir = filepath.Join(t.TempDir(), "isolated-data")
			}
			if err := os.MkdirAll(collieDir, 0o700); err != nil {
				t.Fatal(err)
			}
			db, err := openDB(filepath.Join(collieDir, "herd.db"))
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { db.Close() })
			st, err := loadState(db, collieDir)
			if err != nil {
				t.Fatal(err)
			}
			srv := &Server{
				token: "test-tok",
				home:  userHome,
				web:   fstest.MapFS{"index.html": {Data: []byte("ok")}, "app.js": {Data: []byte("")}, "app.css": {Data: []byte("")}},
			}
			sp := &Super{s: srv, db: db, dir: collieDir, st: st, subs: map[chan struct{}]bool{}}
			srv.sup = sp
			handler := srv.Routes()

			tasksDir := filepath.Join(sp.memDir(), "tasks")
			if err := os.MkdirAll(tasksDir, 0o755); err != nil {
				t.Fatal(err)
			}
			wantBody := "# 无 Worker 排期任务\n- **状态**：待人工评审\n- [ ] 核对字段边界\n"
			if err := os.WriteFile(filepath.Join(tasksDir, "review-plan.md"), []byte(wantBody), 0o600); err != nil {
				t.Fatal(err)
			}
			// Symlink escaping tasksDir must be ignored by superNotebook.
			outsidePath := filepath.Join(t.TempDir(), "escape.md")
			if err := os.WriteFile(outsidePath, []byte("# Secret Outside\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			_ = os.Symlink(outsidePath, filepath.Join(tasksDir, "escape-link.md"))

			get := func(target string) *httptest.ResponseRecorder {
				req := httptest.NewRequest(http.MethodGet, target, nil)
				req.Header.Set("Authorization", "Bearer test-tok")
				rec := httptest.NewRecorder()
				handler.ServeHTTP(rec, req)
				return rec
			}

			recNb := get("/api/super/notebook")
			if recNb.Code != 200 {
				t.Fatalf("GET /api/super/notebook = %d: %s", recNb.Code, recNb.Body.String())
			}
			var nb notebookView
			if err := json.Unmarshal(recNb.Body.Bytes(), &nb); err != nil {
				t.Fatalf("unmarshal notebook: %v", err)
			}
			if len(nb.Tasks) != 1 {
				t.Fatalf("expected exactly 1 safe task (excluding symlink escape), got %+v", nb.Tasks)
			}
			tk := nb.Tasks[0]
			if tk.Path != "tasks/review-plan.md" {
				t.Fatalf("tk.Path = %q, want tasks/review-plan.md", tk.Path)
			}
			if !strings.HasPrefix(tk.FilePath, tc.wantPathPrefix) || !strings.HasSuffix(tk.FilePath, "/router/memory/tasks/review-plan.md") {
				t.Fatalf("unexpected tk.FilePath = %q (want prefix %q)", tk.FilePath, tc.wantPathPrefix)
			}

			recFile := get("/api/file?path=" + url.QueryEscape(tk.FilePath))
			if recFile.Code != 200 {
				t.Fatalf("GET /api/file?path=%s = %d: %s", tk.FilePath, recFile.Code, recFile.Body.String())
			}
			if gotKind := recFile.Header().Get("X-File-Kind"); gotKind != "text" {
				t.Fatalf("X-File-Kind = %q, want text", gotKind)
			}
			if recFile.Body.String() != wantBody {
				t.Fatalf("GET /api/file body mismatch:\ngot:  %q\nwant: %q", recFile.Body.String(), wantBody)
			}
		})
	}
}

func TestWebV0292UIContractsAndBehavior(t *testing.T) {
	t.Parallel()
	if _, err := exec.LookPath("node"); err != nil {
		t.Skip("node not installed")
	}
	script := `
const fs = require("fs");
const vm = require("vm");
const assert = require("assert");

const htmlSrc = fs.readFileSync("web/index.html", "utf8");
const cssSrc = fs.readFileSync("web/app.css", "utf8");
const appSrc = fs.readFileSync("web/app.js", "utf8");

// ─── 1. Static HTML & CSS contracts for v0.2.92 ───
assert.ok(htmlSrc.includes('id="open-notes-btn"') && htmlSrc.includes('id="notes-pane"'), "index.html must include floating notebook trigger and panel");
assert.ok(!htmlSrc.includes('class="np-foot"') && !htmlSrc.includes("tasks/"), "floating notebook must have ZERO footer text or tasks/ leak");
assert.ok(htmlSrc.includes('id="fv-mode-seg"') && htmlSrc.includes('id="rv-mode-seg"'), "file viewers must provide table/raw mode toggle");
assert.ok(htmlSrc.includes('id="fv-sandbox-tag"') && htmlSrc.includes('id="fv-widen"'), "file drawer must provide HTML sandbox badge and widen toggle");

assert.ok(cssSrc.includes("--head-h: 60px"), "CSS must unify --head-h to 60px for 0px top-bar alignment");
assert.ok(!cssSrc.includes(".collie-top::after") && cssSrc.includes("container-type: inline-size"), "seamless top bar must omit bottom hairline/veil and use container query");
assert.ok(cssSrc.includes("@container collie-top"), "CSS must adapt #crew capsules via @container collie-top");
assert.ok(cssSrc.includes(".dispatch-pill.is-override"), "CSS must keep default dispatch pill borderless ghost and add capsule only on .is-override");
assert.ok(cssSrc.includes(".notepane") && cssSrc.includes("@media (min-width: 1360px)"), "CSS must implement floating white-paper notebook with 1360px wide breakpoint");
assert.ok(cssSrc.includes(".reply-receipt") && cssSrc.includes("::details-content"), "CSS must style collapsible decision receipt with ::details-content");
assert.ok(cssSrc.includes(".csv-table") && cssSrc.includes("border-collapse: separate"), "CSS must render sticky CSV table with border-collapse: separate");
assert.ok(cssSrc.includes(".fv-html-sandbox"), "CSS must style HTML preview sandbox iframe");

// ─── 2. Load web/app.js in sandboxed VM ───
function makeClassList() {
  const set = new Set();
  return {
    add: (...cs) => cs.forEach((c) => c && set.add(c)),
    remove: (...cs) => cs.forEach((c) => set.delete(c)),
    toggle: (c, f) => { const on = f === undefined ? !set.has(c) : !!f; if (on) set.add(c); else set.delete(c); return on; },
    contains: (c) => set.has(c),
    toString: () => [...set].join(" "),
  };
}
function makeEl(id = "") {
  const listeners = new Map();
  const attrs = new Map();
  const el = {
    id, tagName: "DIV", value: "", textContent: "", innerHTML: "",
    hidden: false, disabled: false, checked: false, open: false,
    className: "", title: "", style: { setProperty() {}, removeProperty() {} },
    dataset: {}, classList: makeClassList(), children: [], selectedOptions: [], options: [],
    scrollTop: 0, scrollHeight: 200, clientHeight: 200, offsetWidth: 400, offsetHeight: 98, offsetParent: {}, isConnected: true,
    _qs: {}, _qsa: {},
    addEventListener(type, fn) { if (!listeners.has(type)) listeners.set(type, []); listeners.get(type).push(fn); },
    removeEventListener(type, fn) { const l = listeners.get(type); if (!l) return; const i = l.indexOf(fn); if (i >= 0) l.splice(i, 1); },
    dispatch(type, ev = {}) {
      const e = { type, target: el, currentTarget: el, defaultPrevented: false, preventDefault() { this.defaultPrevented = true; }, stopPropagation() {}, composedPath: () => [el], ...ev };
      for (const fn of [...(listeners.get(type) || [])]) fn(e);
      return e;
    },
    querySelector(sel) { if (el._qs[sel]) return el._qs[sel]; const c = makeEl(id + ":" + sel); el._qs[sel] = c; return c; },
    querySelectorAll(sel) { return el._qsa[sel] || []; },
    setAttribute(k, v) { attrs.set(k, String(v)); },
    getAttribute(k) { return attrs.get(k) ?? ""; },
    removeAttribute(k) { attrs.delete(k); },
    hasAttribute(k) { return attrs.has(k); },
    closest() { return null; }, contains() { return false; },
    append() {}, remove() {}, after() {}, focus() {}, click() {}, scrollIntoView() {},
    showModal() { el.open = true; }, close() { el.open = false; },
    getBoundingClientRect() { return { top: 0, bottom: 40, left: 0, right: 300, width: 300, height: 40 }; },
  };
  return el;
}
const bySel = new Map();
function getEl(sel) {
  if (!bySel.has(sel)) {
    const el = makeEl(sel);
    el.parentElement = makeEl(sel + ":parent");
    bySel.set(sel, el);
  }
  return bySel.get(sel);
}
for (const [formSel, inputSel] of [["#super-composer", "#super-input"], ["#composer", "#input"], ["#x-composer", "#x-input"], ["#td-composer", "#td-input"]]) {
  const f = getEl(formSel);
  f._qs["textarea"] = getEl(inputSel);
  f._qs[".send"] = formSel === "#td-composer" ? getEl("#td-send") : makeEl(formSel + " .send");
  f._qs[".files"] = makeEl(formSel + " .files");
  f._qs[".attach"] = makeEl(formSel + " .attach");
  f._qs['input[type="file"]'] = makeEl(formSel + " input[type=file]");
}
getEl("#re-draft")._qs[".re-t"] = makeEl("#re-draft .re-t");
getEl("#re-draft")._qs["button"] = makeEl("#re-draft button");

const storage = new Map();
const fetchLog = [];
const doc = {
  documentElement: makeEl("html"),
  body: makeEl("body"),
  hidden: false,
  hasFocus: () => true,
  createElement: (tag) => makeEl(tag),
  getElementById: (id) => getEl("#" + id),
  querySelector: (sel) => (sel === "dialog[open]" ? null : getEl(sel)),
  querySelectorAll: () => [],
  addEventListener() {},
  removeEventListener() {},
};
const ctx = {
  console, Math, Date, JSON, Set, Map, RegExp, Error, Promise, URL, URLSearchParams, Object, Array, Number, String, Boolean,
  setTimeout: () => 1, clearTimeout() {}, setInterval: () => 1, clearInterval() {}, queueMicrotask,
  requestAnimationFrame: (fn) => { fn(Date.now()); return 1; },
  cancelAnimationFrame() {},
  document: doc,
  location: { hash: "#/collie", origin: "http://127.0.0.1:7788", href: "http://127.0.0.1:7788/#/collie", pathname: "/", search: "" },
  history: { state: null, pushState() {}, replaceState() {}, back() {} },
  navigator: { platform: "MacIntel", clipboard: { writeText: async () => {} } },
  isSecureContext: true, innerWidth: 1440, innerHeight: 900,
  visualViewport: { height: 900, offsetTop: 0, addEventListener() {} },
  CSS: { escape: (s) => String(s), supports: () => true },
  HTMLDialogElement: { prototype: {} },
  Blob, Uint8Array, TextEncoder, atob, AbortController,
  crypto: { randomUUID: () => "v0292-uuid" },
  performance: { now: () => 0 },
  Event: class Event { constructor(t) { this.type = t; } },
  CustomEvent: class CustomEvent { constructor(t, d) { this.type = t; this.detail = d?.detail; } },
  ResizeObserver: class { observe() {} disconnect() {} },
  MutationObserver: class { observe() {} disconnect() {} },
  IntersectionObserver: class { observe() {} disconnect() {} },
  EventSource: class { addEventListener() {} close() {} },
  Notification: class { static permission = "default"; static async requestPermission() { return "default"; } },
  localStorage: { getItem: (k) => (storage.has(k) ? storage.get(k) : null), setItem: (k, v) => storage.set(k, String(v)), removeItem: (k) => storage.delete(k) },
  matchMedia: () => ({ matches: false, addEventListener() {} }),
  getComputedStyle: () => ({ getPropertyValue: () => "" }),
  addEventListener() {}, removeEventListener() {}, dispatchEvent() {},
  fetch: async (url, init = {}) => {
    const body = init.body ? JSON.parse(init.body) : null;
    fetchLog.push({ url: String(url), method: init.method || "GET", body });
    if (String(url).includes("/api/super/say")) {
      return { ok: true, status: 200, json: async () => ({ ok: true, entry: body?.pick ? body.re : "e200", trigger: "t200", picked: !!body?.pick }) };
    }
    return { ok: true, status: 200, json: async () => ({ ok: true }) };
  },
};
ctx.window = ctx;
ctx.globalThis = ctx;

const marker = "  boot();\n})();";
const instrumented = appSrc.replace(
  marker,
  "  globalThis.__test = { parseDelimitedTable, renderDelimitedStage, HTML_SANDBOX_ATTR, CSV_PAGE_SIZE, CSV_MAX_PARSE_ROWS, replyOptsMarkup, renderEntryArtifacts, handleQuickOptionClick, submitInlineDecision, renderNotebook, syncNotebookVisibility, setNotesExpanded, setNotebookData: (d) => { notebookData = d; }, setView: (v) => { view = v; }, setSup: (s) => { sup = normSup(s); }, getPendingSays: () => pendingSays };\n})();"
);
vm.runInNewContext(instrumented, ctx, { filename: "web/app.js" });
const T = ctx.__test;

// 2a) HTML sandbox contract: must NOT allow-same-origin
assert.ok(T.HTML_SANDBOX_ATTR.includes("allow-scripts"), "HTML preview must allow scripts");
assert.ok(!T.HTML_SANDBOX_ATTR.includes("allow-same-origin"), "HTML preview sandbox must NEVER include allow-same-origin");

// 2b) RFC 4180 CSV & TSV parser edge cases
const bomCsv = "\uFEFFname,note,score\r\n\"Alice, Jr.\",\"says \"\"hi\"\"\nline2\",98.5\r\nBob,plain\r\n";
const pCsv = T.parseDelimitedTable(bomCsv, ",");
assert.deepEqual(pCsv.headers, ["name", "note", "score"]);
assert.equal(pCsv.rows.length, 2);
assert.equal(pCsv.rows[0][0], "Alice, Jr.");
assert.equal(pCsv.rows[0][1], "says \"hi\"\nline2");
assert.equal(pCsv.rows[0][2], "98.5");
assert.deepEqual(pCsv.rows[1], ["Bob", "plain", ""], "ragged rows must pad to colCount");
assert.equal(pCsv.capped, false);

const tsv = "colA\tcolB\n1\t\"a\tb\"\n";
const pTsv = T.parseDelimitedTable(tsv, "\t");
assert.deepEqual(pTsv.headers, ["colA", "colB"]);
assert.equal(pTsv.rows[0][1], "a\tb");

// Large CSV row cap
const hugeLines = ["a,b"];
for (let i = 0; i < T.CSV_MAX_PARSE_ROWS + 50; i++) hugeLines.push(i + ",v");
const pHuge = T.parseDelimitedTable(hugeLines.join("\n"), ",");
assert.equal(pHuge.rows.length, T.CSV_MAX_PARSE_ROWS);
assert.equal(pHuge.capped, true);

// 2c) CSV renderDelimitedStage: table mode (sticky + pagination + tabindex="0") vs raw mode
const bigCsv = ["route,p50,p99"];
for (let i = 1; i <= T.CSV_PAGE_SIZE + 5; i++) bigCsv.push("/api/r" + i + "," + i + ",99.9");
const curCsv = { reqPath: "bench.csv", path: "bench.csv", rawText: bigCsv.join("\n"), csvDelimiter: ",", viewMode: "table", csvPage: 0 };
const fvBody = makeEl("#fv-body");
T.renderDelimitedStage("fv", curCsv, fvBody, "");
assert.ok(fvBody.classList.contains("is-csv"), "table mode must add .is-csv");
assert.ok(fvBody.innerHTML.includes('tabindex="0"') && fvBody.innerHTML.includes('class="csv-table"'), "CSV table must include keyboard-scrollable wrapper");
assert.ok(fvBody.innerHTML.includes("第 1 / 2 页"), "CSV > 100 rows must show pagination");
assert.ok(fvBody.innerHTML.includes('class="is-num"'), "numeric cells must right-align with .is-num");
curCsv.viewMode = "raw";
T.renderDelimitedStage("fv", curCsv, fvBody, "");
assert.ok(!fvBody.classList.contains("is-csv"), "raw mode must remove .is-csv");
assert.ok(fvBody.innerHTML.includes("/api/r1,1,99.9"), "raw mode must display exact raw CSV text");

// 2d) Floating notebook compact <-> expanded with stable DOM (.np-fold) & <1360px / drawer auto-yield
T.setView("collie");
T.setNotebookData({
  date: "2026-11-27",
  tasks: [
    { id: "t1", title: "任务 1", note: "详情 1", tone: "live" },
    { id: "t2", title: "任务 2", note: "详情 2", tone: "attn" },
    { id: "t3", title: "任务 3", note: "详情 3", tone: "live" },
    { id: "t4", title: "任务 4", note: "详情 4", tone: "idle" },
  ],
  artifacts: [
    { path: "~/a.html", name: "a.html", title: "报告 A", ext: "html", at: "2026-11-27T10:00:00Z" },
    { path: "~/b.csv", name: "b.csv", title: "数据 B", ext: "csv", at: "2026-11-27T10:05:00Z" },
    { path: "~/c.md", name: "c.md", title: "笔记 C", ext: "md", at: "2026-11-27T10:10:00Z" },
  ],
  timers: [
    { id: "tm1", spec: "daily 18:30", text: "定时 1", next_run: "2026-11-27T18:30:00+08:00" },
    { id: "tm2", spec: "daily 20:00", text: "定时 2", next_run: "2026-11-27T20:00:00+08:00" },
  ],
});
ctx.innerWidth = 1440;
T.syncNotebookVisibility();
assert.equal(getEl("#notes-pane").classList.contains("is-hidden"), false, "notebook must default open at >= 1360px");
assert.equal(getEl("#notes-pane").classList.contains("is-expanded"), false, "notebook must start in compact mode on wide desktop");
assert.equal(getEl("#np-tasks-more").hidden, false);
assert.equal(getEl("#np-tasks-more").textContent, "+1");
assert.equal(getEl("#np-files-more").textContent, "+1");
assert.equal(getEl("#np-timers-more").textContent, "+1");
assert.ok(getEl("#np-tasks-list").innerHTML.includes("np-fold-more") && getEl("#np-tasks-list").innerHTML.includes("任务 4"), "overflow tasks live inside stable .np-fold-more wrapper");
assert.ok(getEl("#np-files-list").innerHTML.includes("np-fold-more") && getEl("#np-files-list").innerHTML.includes("笔记 C"), "overflow artifacts live inside stable .np-fold-more wrapper");
assert.ok(getEl("#np-timers-list").innerHTML.includes("np-fold-more") && getEl("#np-timers-list").innerHTML.includes("定时 2"), "overflow timers live inside stable .np-fold-more wrapper");
T.setNotesExpanded(true);
assert.equal(getEl("#notes-pane").classList.contains("is-expanded"), true, "setNotesExpanded(true) toggles .is-expanded smoothly");
T.setNotesExpanded(false);
assert.equal(getEl("#notes-pane").classList.contains("is-expanded"), false);

// Auto-yield at 1280px (< 1360px)
ctx.innerWidth = 1280;
T.syncNotebookVisibility();
assert.equal(getEl("#notes-pane").classList.contains("is-hidden"), true, "notebook must auto-yield at < 1360px");

// Auto-yield when file drawer opens at 1440px, restore on close
ctx.innerWidth = 1440;
T.syncNotebookVisibility();
assert.equal(getEl("#notes-pane").classList.contains("is-hidden"), false);
getEl("#app").classList.add("has-file");
T.syncNotebookVisibility();
assert.equal(getEl("#notes-pane").classList.contains("is-hidden"), true, "notebook must yield when file drawer opens");
getEl("#app").classList.remove("has-file");
T.syncNotebookVisibility();
assert.equal(getEl("#notes-pane").classList.contains("is-hidden"), false, "notebook must restore when file drawer closes");

// 2e) Decision card: pure option click sends pick=true without creating user bubble; renders collapsible receipt
(async () => {
  const optEntry = { id: "e10", role: "collie", text: "要直接开 PR 吗？", at: "2026-11-27T10:00:00Z", options: ["直接开 PR", "先不用"] };
  T.setSup({ entries: [optEntry], pending: [{ id: "e10", options: optEntry.options }], workers: [] });
  const grp = makeEl("opt-grp");
  grp.dataset.entry = "e10";
  const btn = makeEl("opt-btn");
  btn.dataset.opt = "直接开 PR";
  grp._qsa = { button: [btn] };
  btn.closest = (sel) => (sel.includes(".reply-opt[data-opt]") ? btn : sel === ".reply-opts[data-entry]" ? grp : null);
  await T.handleQuickOptionClick({ target: btn });

  const sayReq = fetchLog.find((r) => r.url.includes("/api/super/say"));
  assert.ok(sayReq, "option click must call /api/super/say");
  assert.equal(sayReq.body.pick, true, "pure option click must set pick: true");
  assert.equal(sayReq.body.re, "e10");
  assert.equal(sayReq.body.text, "直接开 PR");
  assert.equal(T.getPendingSays().length, 0, "pure option click must NOT add an optimistic user bubble");

  // Receipt markup after pick
  const receiptHtml = T.replyOptsMarkup(
    { ...optEntry, pick: { state: "picked", text: "直接开 PR", at: "2026-11-27T10:01:00Z" } },
    0,
    [optEntry],
    new Set(),
  );
  assert.ok(receiptHtml.includes('<details class="reply-receipt"'), "picked decision card must collapse to collapsible <details class=reply-receipt>");
  assert.ok(receiptHtml.includes("已选：") && receiptHtml.includes("直接开 PR"), "receipt summary must show chosen option");

  // Deliverable snapshot card for HTML/CSV files
  const snapHtml = T.renderEntryArtifacts({ id: "e11", role: "collie", text: "见 ~/code/shop-api/bench-pagination.html", files: ["~/code/shop-api/docs/orders-benchmark.csv"] });
  assert.ok(snapHtml.includes("snap-card") && snapHtml.includes("bench-pagination.html") && snapHtml.includes("orders-benchmark.csv"), "HTML/CSV deliverables must render light snapshot cards");
})().catch((e) => {
  console.error(e);
  process.exit(1);
});
`
	cmd := exec.Command("node", "-e", script)
	cmd.Dir = filepath.Join("..", "..")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("v0.2.92 UI checks failed: %v\n%s", err, string(out))
	}
}
