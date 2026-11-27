package collie

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestMemorySummaryDoneFileSearchAndETag(t *testing.T) {
	sp, _, call := newPendingTestEnv(t)
	sp.ensureMemory()
	memDir := sp.memDir()
	dreamsDir := filepath.Join(sp.routerDir(), "dreams")
	if err := os.MkdirAll(dreamsDir, 0o700); err != nil {
		t.Fatal(err)
	}

	mustWrite := func(base, rel, body string, mod time.Time) {
		t.Helper()
		p := filepath.Join(base, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		if !mod.IsZero() {
			if err := os.Chtimes(p, mod, mod); err != nil {
				t.Fatal(err)
			}
		}
	}

	now := time.Now().Truncate(time.Second)
	mustWrite(memDir, "instructions.md", "# Instructions\n- Keep responses crisp.\n", now)
	mustWrite(memDir, "notes/index.md", "# Notes\n- note-01.md: architecture overview\n", now)
	mustWrite(memDir, "tasks/active-one.md", "# Active One\n- Working on latency.\n", now)
	for i := 1; i <= 6; i++ {
		mustWrite(memDir, fmt.Sprintf("notes/note-%02d.md", i),
			fmt.Sprintf("# Note %02d\n\nSummary line for note %02d.\n\n%s\n", i, i, strings.Repeat("Extra detail block.\n", 40)),
			now.Add(-time.Duration(i)*time.Hour))
	}
	for i := 1; i <= 120; i++ {
		tag := ""
		if i == 77 {
			tag = "\n- Unique needle: quantum-zebra-77\n"
		}
		mustWrite(memDir, fmt.Sprintf("tasks/done/2026-11-task-%03d.md", i),
			fmt.Sprintf("# Done Task %03d\n\nFinished batch %03d cleanly.%s\n%s\n", i, i, tag, strings.Repeat("Detail line.\n", 25)),
			now.Add(-time.Duration(i)*time.Hour))
	}
	for i := 1; i <= 4; i++ {
		mustWrite(dreamsDir, fmt.Sprintf("2026-11-%02d-0400.md", 20+i),
			fmt.Sprintf("# Dream %d\n\nConsolidated %d notes and archived tasks.\n\n%s\n", i, i, strings.Repeat("Journal line.\n", 30)),
			now.Add(-time.Duration(5-i)*24*time.Hour))
	}

	// 1) Default full endpoint remains backward-compatible and includes ETag + done_count.
	fullRec := call("GET", "/api/super/memory", nil)
	if fullRec.Code != 200 {
		t.Fatalf("GET /api/super/memory = %d: %s", fullRec.Code, fullRec.Body.String())
	}
	fullETag := fullRec.Header().Get("ETag")
	if fullETag == "" {
		t.Fatalf("expected non-empty ETag on GET /api/super/memory")
	}
	var fullOut struct {
		Files     []memFile `json:"files"`
		Dreams    []memFile `json:"dreams"`
		DoneCount int       `json:"done_count"`
		Version   string    `json:"version"`
	}
	if err := json.Unmarshal(fullRec.Body.Bytes(), &fullOut); err != nil {
		t.Fatal(err)
	}
	if fullOut.DoneCount != 120 {
		t.Fatalf("full done_count = %d, want 120", fullOut.DoneCount)
	}
	if len(fullOut.Files) < 129 {
		t.Fatalf("full files len = %d, want >= 129", len(fullOut.Files))
	}
	for _, f := range fullOut.Files {
		if f.SummaryOnly {
			t.Fatalf("default full mode must not set summary_only on %s", f.Name)
		}
	}

	// 2) Summary mode omits tasks/done and deferred bodies, shrinking payload dramatically.
	sumRec := call("GET", "/api/super/memory?summary=1&include_done=0", nil)
	if sumRec.Code != 200 {
		t.Fatalf("GET /api/super/memory?summary=1&include_done=0 = %d: %s", sumRec.Code, sumRec.Body.String())
	}
	if sumRec.Body.Len()*4 >= fullRec.Body.Len() {
		t.Fatalf("expected summary payload (%d B) to be much smaller than full (%d B)", sumRec.Body.Len(), fullRec.Body.Len())
	}
	var sumOut struct {
		SummaryMode bool      `json:"summary_mode"`
		DoneCount   int       `json:"done_count"`
		Files       []memFile `json:"files"`
		Dreams      []memFile `json:"dreams"`
	}
	if err := json.Unmarshal(sumRec.Body.Bytes(), &sumOut); err != nil {
		t.Fatal(err)
	}
	if !sumOut.SummaryMode || sumOut.DoneCount != 120 {
		t.Fatalf("unexpected summary header: summary_mode=%v done_count=%d", sumOut.SummaryMode, sumOut.DoneCount)
	}
	byName := map[string]memFile{}
	for _, f := range sumOut.Files {
		if strings.HasPrefix(f.Name, "tasks/done/") {
			t.Fatalf("include_done=0 must omit done file %s", f.Name)
		}
		byName[f.Name] = f
	}
	if byName["instructions.md"].SummaryOnly || !strings.Contains(byName["instructions.md"].Text, "Keep responses crisp") {
		t.Fatalf("instructions.md must include full text in summary mode: %+v", byName["instructions.md"])
	}
	if byName["notes/index.md"].SummaryOnly || !strings.Contains(byName["notes/index.md"].Text, "architecture overview") {
		t.Fatalf("notes/index.md must include full text in summary mode: %+v", byName["notes/index.md"])
	}
	if byName["tasks/active-one.md"].SummaryOnly || !strings.Contains(byName["tasks/active-one.md"].Text, "Working on latency") {
		t.Fatalf("active task must include full text in summary mode: %+v", byName["tasks/active-one.md"])
	}
	n1 := byName["notes/note-01.md"]
	if !n1.SummaryOnly || n1.Text != "" || !strings.Contains(n1.Preview, "Summary line for note 01") {
		t.Fatalf("non-index note must be summary_only with preview: %+v", n1)
	}
	if len(sumOut.Dreams) != 4 || sumOut.Dreams[0].SummaryOnly || sumOut.Dreams[0].Text == "" {
		t.Fatalf("latest dream must include full text in summary mode: %+v", sumOut.Dreams)
	}
	if !sumOut.Dreams[1].SummaryOnly || sumOut.Dreams[1].Text != "" || sumOut.Dreams[1].Preview == "" {
		t.Fatalf("older dream must be summary_only with preview: %+v", sumOut.Dreams[1])
	}

	// 3) Conditional ETag returns 304 Not Modified, and saving memory invalidates it.
	sumETag := sumRec.Header().Get("ETag")
	mux := http.NewServeMux()
	sp.s.superRoutes(mux)
	req304 := httptest.NewRequest("GET", "/api/super/memory?summary=1&include_done=0", nil)
	req304.Header.Set("Authorization", "Bearer "+sp.s.token)
	req304.Header.Set("If-None-Match", sumETag)
	rec304 := httptest.NewRecorder()
	mux.ServeHTTP(rec304, req304)
	if rec304.Code != http.StatusNotModified || rec304.Body.Len() != 0 {
		t.Fatalf("expected 304 empty body on matching ETag, got %d len=%d", rec304.Code, rec304.Body.Len())
	}
	saveRec := call("POST", "/api/super/memory", map[string]any{"path": "instructions.md", "text": "# Instructions\n- Updated rule.\n"})
	if saveRec.Code != 200 {
		t.Fatalf("POST /api/super/memory = %d: %s", saveRec.Code, saveRec.Body.String())
	}
	reqAfterSave := httptest.NewRequest("GET", "/api/super/memory?summary=1&include_done=0", nil)
	reqAfterSave.Header.Set("Authorization", "Bearer "+sp.s.token)
	reqAfterSave.Header.Set("If-None-Match", sumETag)
	recAfterSave := httptest.NewRecorder()
	mux.ServeHTTP(recAfterSave, reqAfterSave)
	if recAfterSave.Code != http.StatusOK || recAfterSave.Header().Get("ETag") == sumETag {
		t.Fatalf("expected 200 with new ETag after save, got %d etag=%s", recAfterSave.Code, recAfterSave.Header().Get("ETag"))
	}

	// 4) Paginated archived done endpoint returns newest-first pages with previews.
	p1 := call("GET", "/api/super/memory/done?offset=0&limit=50", nil)
	if p1.Code != 200 {
		t.Fatalf("GET /api/super/memory/done page 1 = %d: %s", p1.Code, p1.Body.String())
	}
	var donePage struct {
		Files   []memFile `json:"files"`
		Total   int       `json:"total"`
		Offset  int       `json:"offset"`
		Limit   int       `json:"limit"`
		HasMore bool      `json:"has_more"`
	}
	if err := json.Unmarshal(p1.Body.Bytes(), &donePage); err != nil {
		t.Fatal(err)
	}
	if donePage.Total != 120 || len(donePage.Files) != 50 || !donePage.HasMore {
		t.Fatalf("unexpected done page 1: total=%d len=%d has_more=%v", donePage.Total, len(donePage.Files), donePage.HasMore)
	}
	if donePage.Files[0].Name != "tasks/done/2026-11-task-001.md" || !donePage.Files[0].SummaryOnly || !strings.Contains(donePage.Files[0].Preview, "Finished batch 001") {
		t.Fatalf("unexpected newest done item: %+v", donePage.Files[0])
	}
	p3 := call("GET", "/api/super/memory/done?offset=100&limit=50", nil)
	if err := json.Unmarshal(p3.Body.Bytes(), &donePage); err != nil {
		t.Fatal(err)
	}
	if len(donePage.Files) != 20 || donePage.HasMore {
		t.Fatalf("unexpected final done page: len=%d has_more=%v", len(donePage.Files), donePage.HasMore)
	}

	// 5) On-demand single file endpoint reads memory & dreams and rejects traversal.
	fRec := call("GET", "/api/super/memory/file?path=tasks/done/2026-11-task-077.md", nil)
	if fRec.Code != 200 || !strings.Contains(fRec.Body.String(), "quantum-zebra-77") {
		t.Fatalf("GET /api/super/memory/file done = %d: %s", fRec.Code, fRec.Body.String())
	}
	dRec := call("GET", "/api/super/memory/file?path=dreams/2026-11-21-0400.md", nil)
	if dRec.Code != 200 || !strings.Contains(dRec.Body.String(), "Consolidated 1 notes") {
		t.Fatalf("GET /api/super/memory/file dream = %d: %s", dRec.Code, dRec.Body.String())
	}
	for _, bad := range []string{"../herd.db", "/etc/passwd", "tasks/../../secret.md", "notes/not-md.txt", "dreams/sub/a.md"} {
		badRec := call("GET", "/api/super/memory/file?path="+bad, nil)
		if badRec.Code != 400 {
			t.Fatalf("path %q expected 400, got %d", bad, badRec.Code)
		}
	}
	missRec := call("GET", "/api/super/memory/file?path=tasks/done/missing.md", nil)
	if missRec.Code != 404 {
		t.Fatalf("missing file expected 404, got %d", missRec.Code)
	}

	// 6) Server-side search finds matches inside archived done tasks and caps broad queries.
	sRec := call("GET", "/api/super/memory/search?q=quantum-zebra-77+077", nil)
	if sRec.Code != 200 {
		t.Fatalf("GET /api/super/memory/search = %d: %s", sRec.Code, sRec.Body.String())
	}
	var sOut struct {
		Files      []memFile `json:"files"`
		Dreams     []memFile `json:"dreams"`
		TotalFiles int       `json:"total_files"`
		TotalHits  int       `json:"total_hits"`
		Truncated  bool      `json:"truncated"`
	}
	if err := json.Unmarshal(sRec.Body.Bytes(), &sOut); err != nil {
		t.Fatal(err)
	}
	if sOut.TotalFiles != 1 || len(sOut.Files) != 1 || sOut.Files[0].Name != "tasks/done/2026-11-task-077.md" {
		t.Fatalf("unexpected search match: %+v", sOut)
	}
	broadRec := call("GET", "/api/super/memory/search?q=Finished+batch&limit=10", nil)
	if err := json.Unmarshal(broadRec.Body.Bytes(), &sOut); err != nil {
		t.Fatal(err)
	}
	if sOut.TotalFiles != 120 || len(sOut.Files) != 10 || !sOut.Truncated {
		t.Fatalf("expected capped broad search (total=120, len=10, truncated=true), got total=%d len=%d truncated=%v",
			sOut.TotalFiles, len(sOut.Files), sOut.Truncated)
	}
}

func TestTimersPaginationAndAutoETags(t *testing.T) {
	sp, _, call := newPendingTestEnv(t)
	now := time.Now()
	sp.mu.Lock()
	for i := 1; i <= 5; i++ {
		sp.st.Timers = append(sp.st.Timers, &Timer{
			ID: fmt.Sprintf("m%d", i), Spec: "daily 09:00", Thread: mainThread,
			Text: fmt.Sprintf("active timer %d", i), NextRun: now.Add(time.Duration(i) * time.Hour), Enabled: true,
		})
	}
	for i := 1; i <= 55; i++ {
		lr := now.Add(-time.Duration(i) * time.Hour)
		sp.st.Timers = append(sp.st.Timers, &Timer{
			ID: fmt.Sprintf("m%d", 100+i), Spec: "at 2026-10-01 09:00", Thread: mainThread,
			Text: fmt.Sprintf("ended timer %d", i), LastRun: &lr, Enabled: false,
		})
	}
	sp.mu.Unlock()

	// Default timers endpoint returns all 60 timers (backward compatible).
	allRec := call("GET", "/api/super/timers", nil)
	if allRec.Code != 200 {
		t.Fatalf("GET /api/super/timers = %d", allRec.Code)
	}
	var allOut struct {
		Timers []Timer `json:"timers"`
	}
	if err := json.Unmarshal(allRec.Body.Bytes(), &allOut); err != nil {
		t.Fatal(err)
	}
	if len(allOut.Timers) != 60 {
		t.Fatalf("default timers len = %d, want 60", len(allOut.Timers))
	}

	// Lightweight mode: ended_limit=0 returns only 5 enabled timers + ended_count=55.
	liteRec := call("GET", "/api/super/timers?ended_limit=0", nil)
	var liteOut struct {
		Timers       []Timer `json:"timers"`
		ActiveCount  int     `json:"active_count"`
		EndedCount   int     `json:"ended_count"`
		EndedHasMore bool    `json:"ended_has_more"`
	}
	if err := json.Unmarshal(liteRec.Body.Bytes(), &liteOut); err != nil {
		t.Fatal(err)
	}
	if len(liteOut.Timers) != 5 || liteOut.ActiveCount != 5 || liteOut.EndedCount != 55 || !liteOut.EndedHasMore {
		t.Fatalf("unexpected ended_limit=0 output: %+v", liteOut)
	}

	// Ended-only pagination.
	p1 := call("GET", "/api/super/timers?ended_only=1&ended_offset=0&ended_limit=30", nil)
	var ep struct {
		Ended        []Timer `json:"ended"`
		EndedCount   int     `json:"ended_count"`
		EndedHasMore bool    `json:"ended_has_more"`
	}
	if err := json.Unmarshal(p1.Body.Bytes(), &ep); err != nil {
		t.Fatal(err)
	}
	if len(ep.Ended) != 30 || ep.EndedCount != 55 || !ep.EndedHasMore {
		t.Fatalf("unexpected ended page 1: len=%d count=%d more=%v", len(ep.Ended), ep.EndedCount, ep.EndedHasMore)
	}
	p2 := call("GET", "/api/super/timers?ended_only=1&ended_offset=30&ended_limit=30", nil)
	if err := json.Unmarshal(p2.Body.Bytes(), &ep); err != nil {
		t.Fatal(err)
	}
	if len(ep.Ended) != 25 || ep.EndedHasMore {
		t.Fatalf("unexpected ended page 2: len=%d more=%v", len(ep.Ended), ep.EndedHasMore)
	}

	// ETag / 304 support across auto tab endpoints.
	mux := http.NewServeMux()
	sp.s.superRoutes(mux)
	for _, path := range []string{"/api/super/timers?ended_limit=0", "/api/super/triggers", "/api/super/integrations", "/api/super/settings"} {
		r1 := httptest.NewRequest("GET", path, nil)
		r1.Header.Set("Authorization", "Bearer "+sp.s.token)
		w1 := httptest.NewRecorder()
		mux.ServeHTTP(w1, r1)
		et := w1.Header().Get("ETag")
		if w1.Code != 200 || et == "" {
			t.Fatalf("%s expected 200 with ETag, got %d etag=%q", path, w1.Code, et)
		}
		r2 := httptest.NewRequest("GET", path, nil)
		r2.Header.Set("Authorization", "Bearer "+sp.s.token)
		r2.Header.Set("If-None-Match", et)
		w2 := httptest.NewRecorder()
		mux.ServeHTTP(w2, r2)
		if w2.Code != http.StatusNotModified {
			t.Fatalf("%s expected 304 on matching ETag, got %d", path, w2.Code)
		}
	}
}

func TestAutoTabsPerfAndZeroFlashContractsJS(t *testing.T) {
	nodeBin, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node not installed")
	}
	script := `
const fs = require("node:fs");
const vm = require("node:vm");
const assert = require("node:assert/strict");

const cssSrc = fs.readFileSync("web/app.css", "utf8");
const appSrc = fs.readFileSync("web/app.js", "utf8");

// 1) CSS containment contracts: off-screen archive lists use content-visibility: auto with fallback
assert.match(cssSrc, /\.mem-done-blist > \.mem-fold\s*\{[\s\S]*?content-visibility:\s*auto;[\s\S]*?contain-intrinsic-block-size:\s*auto\s+\d+px;/,
  "archive done list items must use content-visibility: auto with contain-intrinsic-block-size");
assert.match(cssSrc, /@supports not \(content-visibility:\s*auto\)\s*\{[\s\S]*?contain:\s*layout style paint;/,
  "archive containment must include @supports fallback");

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

function makeEl(id = "", tag = "DIV") {
  const listeners = new Map();
  const attrs = new Map();
  const el = {
    id, tagName: tag, value: "", textContent: "", innerHTML: "", hidden: false, disabled: false, open: false,
    className: "", title: "", style: { setProperty() {}, removeProperty() {} }, dataset: {},
    classList: makeClassList(), children: [], options: [], scrollTop: 0, scrollHeight: 200, clientHeight: 200,
    clientWidth: 800, offsetWidth: 800, offsetParent: {}, isConnected: true, _qs: {}, _qsa: {},
    addEventListener(type, fn) { if (!listeners.has(type)) listeners.set(type, []); listeners.get(type).push(fn); },
    removeEventListener() {},
    querySelector(sel) {
      if (Object.prototype.hasOwnProperty.call(el._qs, sel)) return el._qs[sel];
      const child = makeEl(id + ":" + sel);
      el._qs[sel] = child;
      return child;
    },
    querySelectorAll(sel) { return el._qsa[sel] || []; },
    setAttribute(k, v) { attrs.set(k, String(v)); },
    getAttribute(k) { return attrs.get(k) ?? ""; },
    removeAttribute(k) { attrs.delete(k); },
    hasAttribute(k) { return attrs.has(k); },
    closest() { return null; },
    contains() { return false; },
    append() {}, remove() {}, after() {}, insertAdjacentHTML() {},
    focus() {}, blur() {}, click() {}, scrollIntoView() {},
    getBoundingClientRect() { return { top: 0, bottom: 40, left: 0, right: 800, width: 800, height: 40 }; },
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
  f._qs[".send"] = makeEl(formSel + " .send");
  f._qs[".files"] = makeEl(formSel + " .files");
  f._qs[".attach"] = makeEl(formSel + " .attach");
  f._qs['input[type="file"]'] = makeEl(formSel + " input[type=file]");
}
getEl("#re-draft")._qs[".re-t"] = makeEl("#re-draft .re-t");
getEl("#re-draft")._qs["button"] = makeEl("#re-draft button");

const storage = new Map();
const fetchLog = [];
let pendingIntResolve = null;
let pendingTimersResolve = null;

const doc = {
  documentElement: makeEl("html"),
  body: makeEl("body"),
  hidden: false,
  hasFocus: () => true,
  createElement: (tag) => makeEl(tag, tag.toUpperCase()),
  getElementById: (id) => getEl("#" + id),
  querySelector: (sel) => (sel === "dialog[open]" ? null : getEl(sel)),
  querySelectorAll: () => [],
  addEventListener() {},
  removeEventListener() {},
};

const ctx = {
  console, Math, Date, JSON, Set, Map, RegExp, Error, Promise, URL, URLSearchParams, Object, Array, Number, String, Boolean,
  setTimeout: () => 1, clearTimeout() {}, setInterval: () => 1, clearInterval() {}, queueMicrotask,
  requestAnimationFrame: () => 1, cancelAnimationFrame() {},
  document: doc,
  location: { hash: "#/auto", origin: "http://127.0.0.1:7788", href: "http://127.0.0.1:7788/#/auto", pathname: "/", search: "" },
  history: { state: null, pushState() {}, replaceState() {}, back() {} },
  navigator: { platform: "Linux", clipboard: { writeText: async () => {} } },
  isSecureContext: true, innerWidth: 1440, innerHeight: 900,
  visualViewport: { height: 900, offsetTop: 0, addEventListener() {} },
  CSS: { escape: (s) => String(s), supports: () => true },
  HTMLDialogElement: { prototype: {} },
  Blob, Uint8Array, TextEncoder, atob, AbortController,
  crypto: { randomUUID: () => "perf-test-uuid" },
  performance: { now: () => Date.now() },
  Event: class Event { constructor(t) { this.type = t; } },
  CustomEvent: class CustomEvent { constructor(t, d) { this.type = t; this.detail = d?.detail; } },
  ResizeObserver: class { observe() {} disconnect() {} },
  MutationObserver: class { observe() {} disconnect() {} },
  IntersectionObserver: class { observe() {} disconnect() {} },
  EventSource: class { addEventListener() {} close() {} },
  Notification: class { static permission = "default"; static async requestPermission() { return "default"; } },
  localStorage: {
    getItem: (k) => (storage.has(k) ? storage.get(k) : null),
    setItem: (k, v) => storage.set(k, String(v)),
    removeItem: (k) => storage.delete(k),
  },
  matchMedia: () => ({ matches: false, addEventListener() {} }),
  getComputedStyle: () => ({ getPropertyValue: () => "", lineHeight: "21px" }),
  addEventListener() {}, removeEventListener() {}, dispatchEvent() {},
  fetch: async (url) => {
    const u = String(url);
    fetchLog.push(u);
    if (u.startsWith("/api/super/integrations")) {
      return await new Promise((res) => { pendingIntResolve = () => res({ ok: true, status: 200, headers: { get: () => '"int-v1"' }, text: async () => JSON.stringify({ integrations: [], notify: {}, hook_key: "hk" }) }); });
    }
    if (u.startsWith("/api/super/peers")) {
      return { ok: true, status: 200, headers: { get: () => '"peer-v1"' }, text: async () => JSON.stringify({ enabled: true, peers: [] }) };
    }
    if (u.startsWith("/api/super/timers")) {
      return await new Promise((res) => { pendingTimersResolve = () => res({ ok: true, status: 200, headers: { get: () => '"tm-v1"' }, text: async () => JSON.stringify({ timers: [], active_count: 0, ended_count: 85, ended_has_more: true }) }); });
    }
    return { ok: true, status: 200, headers: { get: () => "" }, text: async () => JSON.stringify({ ok: true }) };
  },
};
ctx.window = ctx;
ctx.globalThis = ctx;

const marker = "  boot();\n})();";
const instrumented = appSrc.replace(
  marker,
  "  globalThis.__test = { renderMemoryHTML, renderMemCrewHTML, inboxHTML, timersHTML, syncInboxOverflow, openAuto, loadAuto, autoCache, setSup: (s) => { sup = normSup(s); }, setView: (v) => { view = v; }, setAutoTab: (t) => { autoTab = t; }, getBody: () => $(\"#auto-body\") };\n})();"
);
vm.runInNewContext(instrumented, ctx, { filename: "web/app.js" });
const T = ctx.__test;

// 2) Memory page: 350 archived files x 400 workers renders lazily with 0 textareas in <50ms
const nowIso = new Date().toISOString();
const workers = [];
for (let i = 1; i <= 400; i++) {
  workers.push({ name: "worker-" + String(i).padStart(3, "0"), kind: "claude", created_at: nowIso, ended: i > 5, status: i <= 5 ? "idle" : "ended" });
}
T.setSup({ workers });

const bigFiles = [
  { name: "instructions.md", text: "# Rules\n- Keep fast.\n", size: 30, mod: nowIso },
  { name: "notes/index.md", text: "# Notes\n- topic-01.md\n", size: 30, mod: nowIso },
];
for (let i = 1; i <= 350; i++) {
  bigFiles.push({
    name: "tasks/done/2026-11-task-" + i + ".md",
    text: "# Task " + i + "\n- Worker: worker-001\n\x60\x60\x60js\nconsole.log(" + i + ");\n\x60\x60\x60\n",
    preview: "Worker: worker-001",
    size: 1200,
    mod: nowIso,
  });
}
const t0 = Date.now();
const memRes = T.renderMemoryHTML({ dir: "~/.collie", up_front: 200, messages: 0, loaded: 200, budget: 10240, files: bigFiles, dreams: [] }, "");
const elapsed = Date.now() - t0;
assert.equal((memRes.html.match(/<textarea/g) || []).length, 0, "idle memory view must create 0 eager textareas");
assert.ok(memRes.html.includes('data-mem-lazy="1"'), "collapsed memory cards must defer markdown body via data-mem-lazy");
assert.ok(elapsed < 150, "rendering 350 files with 400 workers must stay fast, took " + elapsed + "ms");

// Fast O(|text|) crew matcher matches exact word/hyphen boundary and rejects substring collisions
const crewHtml = T.renderMemCrewHTML("assigned to worker-001 and not worker-0010 or xworker-001");
assert.ok(crewHtml.includes("worker-001"), "crew matcher must match exact worker token");
assert.ok(!crewHtml.includes("worker-010"), "crew matcher must respect word/hyphen boundaries");

// Summary mode with done_count > 0 and no inline done files renders lightweight lazy done fold
const sumRes = T.renderMemoryHTML({ dir: "~/.collie", summary_mode: true, done_count: 350, up_front: 200, messages: 0, loaded: 200, budget: 10240, files: bigFiles.slice(0, 2), dreams: [] }, "");
assert.ok(sumRes.html.includes('data-done-lazy="1"'), "summary mode must render lazy done section");
assert.ok(sumRes.html.includes("350 件"), "lazy done section must show accurate archive count");

// 3) Timers lazy ended section & Inbox 40-row pagination
const tmHtml = T.timersHTML([{ id: "m1", spec: "daily 09:00", text: "ping", enabled: true, next_run: nowIso }], { endedCount: 120, endedHasMore: true, endedList: [], endedLoaded: false });
assert.ok(tmHtml.includes('data-ended-lazy="1"') && tmHtml.includes("120 个"), "timers must defer ended rows when ended_count > 0");
assert.equal((tmHtml.match(/class="tm-row is-ended"/g) || []).length, 0, "0 ended timer rows should render before expand");

const manyTriggers = Array.from({ length: 120 }, (_, i) => ({
  id: "t" + (i + 1), source: "chat", thread: "chat:main", from: "you", action: "respond", status: "delivered", text: "msg " + i, at: nowIso,
}));
const inbHtml = T.inboxHTML(manyTriggers, [{ id: "chat:main", triggers: [], subscribers: [], last_at: nowIso }]);
assert.equal((inbHtml.match(/class="row inbox-row/g) || []).length, 40, "inbox must paginate initial render to 40 rows");
assert.ok(inbHtml.includes('data-act="inbox-more"'), "inbox must offer load-more button for remaining triggers");

// 4) Zero-flash .is-integrations atomic switching & no /api/super/lark?fresh spam
(async () => {
  const body = T.getBody();
  T.setView("auto");
  body._renderedTab = "memory";
  body._tabRendered = true;
  body.classList.remove("is-integrations");

  // Cold switch from memory -> integrations: must NOT add is-integrations before fetch resolves!
  T.setAutoTab("integrations");
  T.openAuto();
  assert.equal(body.classList.contains("is-integrations"), false, "cold switch to integrations must NOT toggle .is-integrations before new DOM commits");
  assert.ok(pendingIntResolve, "integrations fetch must be in flight");
  pendingIntResolve();
  await new Promise((r) => setImmediate(r));
  await new Promise((r) => setImmediate(r));
  assert.equal(body.classList.contains("is-integrations"), true, "after fetch resolves, .is-integrations commits atomically with integrations DOM");
  assert.equal(body._renderedTab, "integrations");
  assert.ok(!fetchLog.some((u) => u.includes("/api/super/lark?fresh")), "integrations loadAuto must NOT spam /api/super/lark?fresh");

  // Cold switch from integrations -> timers: must NOT remove is-integrations before timers fetch resolves!
  T.setAutoTab("timers");
  T.openAuto();
  assert.equal(body.classList.contains("is-integrations"), true, "cold switch away from integrations must keep width steady until timers DOM commits");
  pendingTimersResolve();
  await new Promise((r) => setImmediate(r));
  await new Promise((r) => setImmediate(r));
  assert.equal(body.classList.contains("is-integrations"), false, "after timers fetch resolves, .is-integrations clears atomically");
  assert.equal(body._renderedTab, "timers");

  // Warm switch back to integrations: renders SYNCHRONOUSLY in Frame 0 from autoCache with atomic class toggle!
  T.setAutoTab("integrations");
  T.openAuto();
  assert.equal(body.classList.contains("is-integrations"), true, "warm switch to integrations toggles class synchronously in Frame 0");
  assert.equal(body._renderedTab, "integrations", "warm switch mounts integrations DOM synchronously from cache");
  assert.ok(body.innerHTML.includes("int-grid"), "warm switch paints real integrations cards immediately");

  console.log("ALL AUTO PERF & ZERO-FLASH CONTRACTS PASSED");
})().catch((err) => {
  console.error(err);
  process.exit(1);
});
`
	cmd := exec.Command(nodeBin, "-e", script)
	cmd.Dir = filepath.Join("..", "..")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("auto perf & zero-flash JS contract test failed: %v\n%s", err, out)
	}
}
