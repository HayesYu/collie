package collie

import (
	"bufio"
	"context"
	"encoding/json"
	"net"
	"net/http/httptest"
	"os/exec"
	"path/filepath"
	"reflect"
	"sync"
	"testing"
	"testing/fstest"
	"time"
)

func TestSidebarStableSortBackend(t *testing.T) {
	dir := t.TempDir()
	db, err := openDB(filepath.Join(dir, "herd.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	base := time.Now().UTC().Truncate(time.Second)
	st, err := loadState(db, dir)
	if err != nil {
		t.Fatal(err)
	}
	st.Workers = map[string]*Worker{
		"wk-old": {
			Name: "wk-old", Kind: "codex", PaneID: "w2:p1", WorkspaceID: "w2",
			Cwd: "/tmp/shop-api", Task: "older worker", Thread: mainThread,
			CreatedAt: base.Add(-30 * time.Minute), Status: "working",
		},
		"wk-tie-b": {
			Name: "wk-tie-b", Kind: "claude", PaneID: "w4:p1", WorkspaceID: "w4",
			Cwd: "/tmp/shop-web", Task: "tie worker b", Thread: mainThread,
			CreatedAt: base.Add(-15 * time.Minute), Status: "done",
		},
		"wk-tie-a": {
			Name: "wk-tie-a", Kind: "claude", PaneID: "w3:p1", WorkspaceID: "w3",
			Cwd: "/tmp/shop-web", Task: "tie worker a", Thread: mainThread,
			CreatedAt: base.Add(-15 * time.Minute), Status: "working",
		},
		"wk-new": {
			Name: "wk-new", Kind: "codex", PaneID: "w5:p1", WorkspaceID: "w5",
			Cwd: "/tmp/shop-api", Task: "newest worker", Thread: mainThread,
			CreatedAt: base.Add(-5 * time.Minute), Status: "done",
		},
	}

	type mockPane struct {
		wsID   string
		paneID string
		wsNum  int
		name   string
		kind   string
		status string
		title  string
	}
	var (
		mu    sync.Mutex
		panes = []mockPane{
			{wsID: "w1", paneID: "w1:p1", wsNum: 1, name: routerName, kind: "claude", status: "idle"},
			{wsID: "w2", paneID: "w2:p1", wsNum: 2, name: "wk-old", kind: "codex", status: "working", title: "step 1"},
			{wsID: "w3", paneID: "w3:p1", wsNum: 3, name: "wk-tie-a", kind: "claude", status: "working", title: "step 1"},
			{wsID: "w4", paneID: "w4:p1", wsNum: 4, name: "wk-tie-b", kind: "claude", status: "done", title: "step 1"},
			{wsID: "w5", paneID: "w5:p1", wsNum: 5, name: "wk-new", kind: "codex", status: "done", title: "step 1"},
			{wsID: "w6", paneID: "w6:p1", wsNum: 6, name: "manual-launched", kind: "claude", status: "idle", title: "manual"},
			{wsID: "w7", paneID: "w7:p1", wsNum: 7, name: "preexisting-tui", kind: "codex", status: "working", title: "tui"},
		}
	)

	mockDial := func(ctx context.Context) (net.Conn, error) {
		c1, c2 := net.Pipe()
		go func() {
			defer c2.Close()
			r := bufio.NewReader(c2)
			line, err := r.ReadBytes('\n')
			if err != nil {
				return
			}
			var req struct {
				ID     string `json:"id"`
				Method string `json:"method"`
			}
			if err := json.Unmarshal(line, &req); err != nil {
				return
			}
			mu.Lock()
			var res any
			switch req.Method {
			case "workspace.list":
				ws := make([]map[string]any, 0, len(panes))
				for _, p := range panes {
					ws = append(ws, map[string]any{
						"workspace_id": p.wsID,
						"label":        p.name,
						"number":       p.wsNum,
						"agent_status": p.status,
						"pane_count":   1,
					})
				}
				res = map[string]any{"workspaces": ws}
			case "pane.list":
				ps := make([]map[string]any, 0, len(panes))
				for _, p := range panes {
					ps = append(ps, map[string]any{
						"pane_id":                 p.paneID,
						"workspace_id":            p.wsID,
						"agent":                   p.kind,
						"agent_status":            p.status,
						"cwd":                     "/tmp/shop-api",
						"terminal_title_stripped": p.title,
					})
				}
				res = map[string]any{"panes": ps}
			case "agent.list":
				as := make([]map[string]any, 0, len(panes))
				for _, p := range panes {
					as = append(as, map[string]any{
						"name":                    p.name,
						"pane_id":                 p.paneID,
						"workspace_id":            p.wsID,
						"agent":                   p.kind,
						"agent_status":            p.status,
						"cwd":                     "/tmp/shop-api",
						"terminal_title_stripped": p.title,
					})
				}
				res = map[string]any{"agents": as}
			default:
				res = map[string]any{"ok": true}
			}
			mu.Unlock()
			resp, _ := json.Marshal(map[string]any{"id": req.ID, "result": res})
			_, _ = c2.Write(append(resp, '\n'))
		}()
		return c1, nil
	}

	herdr := newHerdr(filepath.Join(dir, "mock-herdr.sock"), "devbox", dir)
	herdr.local.conn.dial = mockDial
	srv := &Server{
		herdr:   herdr,
		token:   "test-tok",
		home:    dir,
		web:     fstest.MapFS{},
		auths:   newAuth(db, "test-tok"),
		started: map[string]time.Time{"w6:p1": base.Add(-10 * time.Minute)},
	}
	sp := &Super{s: srv, db: db, dir: dir, bin: "collie", st: st, subs: map[chan struct{}]bool{}}
	srv.sup = sp
	handler := srv.Routes()

	fetchState := func() PaneState {
		req := httptest.NewRequest("GET", "/api/state", nil)
		req.Header.Set("Authorization", "Bearer test-tok")
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		if rec.Code != 200 {
			t.Fatalf("GET /api/state = %d: %s", rec.Code, rec.Body.String())
		}
		var out PaneState
		if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
			t.Fatalf("unmarshal state: %v", err)
		}
		return out
	}

	nonRouterNames := func(ps PaneState) []string {
		var out []string
		for _, a := range ps.Agents {
			if a.Name != routerName {
				out = append(out, a.Name)
			}
		}
		return out
	}

	// 1) Initial GET /api/state: created_at populated and Agents ordered strictly by created_at DESC + stable tie-breaker.
	// preexisting-tui gets firstSeen = now (> base), then wk-new (-5m), manual-launched (-10m), wk-tie-a (-15m, w3), wk-tie-b (-15m, w4), wk-old (-30m).
	s1 := fetchState()
	wantOrder := []string{"preexisting-tui", "wk-new", "manual-launched", "wk-tie-a", "wk-tie-b", "wk-old"}
	if got := nonRouterNames(s1); !reflect.DeepEqual(got, wantOrder) {
		t.Fatalf("initial Agents order = %v, want %v", got, wantOrder)
	}
	createdByName := map[string]time.Time{}
	for _, a := range s1.Agents {
		if a.Name == routerName {
			continue
		}
		if a.CreatedAt == nil || a.CreatedAt.IsZero() {
			t.Fatalf("agent %s missing created_at", a.Name)
		}
		createdByName[a.Name] = *a.CreatedAt
	}
	if !createdByName["wk-new"].Equal(base.Add(-5 * time.Minute)) {
		t.Fatalf("wk-new created_at = %v, want %v", createdByName["wk-new"], base.Add(-5*time.Minute))
	}
	if !createdByName["manual-launched"].Equal(base.Add(-10 * time.Minute)) {
		t.Fatalf("manual-launched created_at = %v, want %v", createdByName["manual-launched"], base.Add(-10*time.Minute))
	}

	// 2) Mutate every agent's runtime status and title: order & created_at must remain 100% unchanged.
	statusRounds := []map[string]string{
		{"wk-old": "working", "wk-tie-a": "done", "wk-tie-b": "working", "wk-new": "idle", "manual-launched": "working", "preexisting-tui": "done"},
		{"wk-old": "blocked", "wk-tie-a": "idle", "wk-tie-b": "blocked", "wk-new": "working", "manual-launched": "done", "preexisting-tui": "idle"},
		{"wk-old": "done", "wk-tie-a": "working", "wk-tie-b": "starting", "wk-new": "blocked", "manual-launched": "working", "preexisting-tui": "working"},
	}
	for round, changes := range statusRounds {
		mu.Lock()
		for i := range panes {
			if next, ok := changes[panes[i].name]; ok {
				panes[i].status = next
				panes[i].title = "output tick " + string(rune('A'+round))
			}
		}
		mu.Unlock()
		sn := fetchState()
		if got := nonRouterNames(sn); !reflect.DeepEqual(got, wantOrder) {
			t.Fatalf("round %d: Agents order drifted to %v, want %v", round, got, wantOrder)
		}
		for _, a := range sn.Agents {
			if a.Name == routerName {
				continue
			}
			if a.CreatedAt == nil || !a.CreatedAt.Equal(createdByName[a.Name]) {
				t.Fatalf("round %d: agent %s created_at drifted: got %v, want %v", round, a.Name, a.CreatedAt, createdByName[a.Name])
			}
		}
	}

	// 3) Pruning dead pane resets firstSeen so a reused pane_id gets a fresh creation timestamp.
	oldTuiCreated := createdByName["preexisting-tui"]
	mu.Lock()
	panes = panes[:len(panes)-1] // close w7:p1
	mu.Unlock()
	_ = fetchState()
	time.Sleep(5 * time.Millisecond)
	mu.Lock()
	panes = append(panes, mockPane{wsID: "w7", paneID: "w7:p1", wsNum: 7, name: "preexisting-tui", kind: "codex", status: "working"})
	mu.Unlock()
	sReused := fetchState()
	for _, a := range sReused.Agents {
		if a.PaneID == "w7:p1" && (a.CreatedAt == nil || !a.CreatedAt.After(oldTuiCreated)) {
			t.Fatalf("reused pane w7:p1 created_at = %v, want strictly after %v", a.CreatedAt, oldTuiCreated)
		}
	}

	// 4) superState Workers sort is deterministic even under equal CreatedAt (immune to Go map randomization).
	wantWorkers := []string{"wk-new", "wk-tie-a", "wk-tie-b", "wk-old"}
	for i := 0; i < 30; i++ {
		sv := srv.superState(context.Background())
		got := make([]string, 0, len(sv.Workers))
		for _, w := range sv.Workers {
			got = append(got, w.Name)
		}
		if !reflect.DeepEqual(got, wantWorkers) {
			t.Fatalf("superState iteration %d: Workers order = %v, want %v", i, got, wantWorkers)
		}
	}

	// 5) sortSessions keeps live external sessions ordered by Started DESC (+ ID ASC) regardless of Updated,
	// while resting (non-live) sessions still order by lastActive DESC.
	extList := []*External{
		{ID: "claude-100", Live: true, Started: base.Add(-20 * time.Minute), Updated: base.Add(-1 * time.Minute)},
		{ID: "claude-200", Live: true, Started: base.Add(-10 * time.Minute), Updated: base.Add(-9 * time.Minute)},
		{ID: "codex-150", Live: true, Started: base.Add(-10 * time.Minute), Updated: base.Add(-2 * time.Minute)},
		{ID: "rest-old-upd", Live: false, Started: base.Add(-5 * time.Minute), Updated: base.Add(-4 * time.Minute)},
		{ID: "rest-new-upd", Live: false, Started: base.Add(-60 * time.Minute), Updated: base.Add(-1 * time.Minute)},
	}
	sortSessions(extList)
	gotExtIDs := make([]string, len(extList))
	for i, e := range extList {
		gotExtIDs[i] = e.ID
	}
	wantExtIDs := []string{"claude-200", "codex-150", "claude-100", "rest-new-upd", "rest-old-upd"}
	if !reflect.DeepEqual(gotExtIDs, wantExtIDs) {
		t.Fatalf("sortSessions initial = %v, want %v", gotExtIDs, wantExtIDs)
	}
	// Advance Updated on older live session: live order must NOT change.
	extList[2].Updated = base.Add(5 * time.Minute)
	sortSessions(extList)
	for i, e := range extList {
		gotExtIDs[i] = e.ID
	}
	if !reflect.DeepEqual(gotExtIDs, wantExtIDs) {
		t.Fatalf("sortSessions after Updated bump = %v, want %v", gotExtIDs, wantExtIDs)
	}
}

func TestSidebarStableSortUI(t *testing.T) {
	if _, err := exec.LookPath("node"); err != nil {
		t.Skip("node not installed")
	}

	script := `
const fs = require("fs");
const vm = require("vm");
const assert = require("assert");

const appSrc = fs.readFileSync("web/app.js", "utf8");

function makeClassList(onChange) {
  const set = new Set();
  return {
    add(...cs) { cs.forEach((c) => { if (c) set.add(c); }); onChange?.(); },
    remove(...cs) { cs.forEach((c) => set.delete(c)); onChange?.(); },
    toggle(c, force) {
      const on = force === undefined ? !set.has(c) : !!force;
      if (on) set.add(c); else set.delete(c);
      onChange?.();
      return on;
    },
    contains(c) { return set.has(c); },
    toString() { return [...set].join(" "); },
  };
}
function makeEl(id, tag = "DIV") {
  const attrs = new Map();
  const styles = new Map();
  let _html = "";
  let _text = "";
  const cl = makeClassList();
  const el = {
    id, tagName: tag, hidden: false, open: true, value: "", title: "", href: "",
    get innerHTML() { return _html; },
    set innerHTML(v) { _html = String(v); },
    get textContent() { return _text; },
    set textContent(v) { _text = String(v); },
    get className() { return cl.toString(); },
    set className(v) {
      for (const c of cl.toString().split(/\s+/).filter(Boolean)) cl.remove(c);
      for (const c of String(v || "").split(/\s+/).filter(Boolean)) cl.add(c);
    },
    style: {
      setProperty(k, v) { styles.set(k, String(v)); },
      getPropertyValue(k) { return styles.get(k) || ""; },
      removeProperty(k) { styles.delete(k); },
    },
    dataset: {}, classList: cl, children: [], selectedOptions: [], options: [],
    scrollTop: 0, scrollHeight: 1200, clientHeight: 600, scrollWidth: 400, clientWidth: 400,
    offsetWidth: 400, offsetHeight: 98, offsetParent: {}, isConnected: true,
    _qs: {}, _qsa: {},
    addEventListener() {}, removeEventListener() {},
    querySelector(sel) { if (el._qs[sel]) return el._qs[sel]; const c = makeEl(id + ":" + sel); el._qs[sel] = c; return c; },
    querySelectorAll(sel) { return el._qsa[sel] || []; },
    setAttribute(k, v) { attrs.set(k, String(v)); },
    getAttribute(k) { return attrs.get(k) ?? ""; },
    removeAttribute(k) { attrs.delete(k); },
    hasAttribute(k) { return attrs.has(k); },
    toggleAttribute(k, force) { const on = force === undefined ? !attrs.has(k) : !!force; if (on) attrs.set(k, ""); else attrs.delete(k); return on; },
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
getEl("#super-search").hidden = true;
getEl("#collie-menu").hidden = true;
getEl("#file-view").hidden = true;

const storage = new Map();
const doc = {
  documentElement: makeEl("html"),
  body: makeEl("body"),
  hidden: false,
  hasFocus: () => true,
  createElement: (tag) => makeEl(tag),
  getElementById: (id) => getEl("#" + id),
  querySelector: (sel) => (sel === "dialog[open]" ? null : getEl(sel)),
  querySelectorAll: () => [],
  addEventListener() {}, removeEventListener() {},
};
const ctx = {
  console, Math, Date, JSON, Set, Map, RegExp, Error, Promise, URL, URLSearchParams, Object, Array, Number, String, Boolean, Intl,
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
  crypto: { randomUUID: () => "sort-test-uuid" },
  performance: { now: () => 1000 },
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
  getSelection: () => ({ isCollapsed: true, toString: () => "" }),
  addEventListener() {}, removeEventListener() {}, dispatchEvent() {},
  fetch: async () => ({ ok: true, status: 200, json: async () => ({ ok: true }) }),
};
ctx.window = ctx;
ctx.globalThis = ctx;

const marker = "  boot();\n})();";
const instrumented = appSrc.replace(
  marker,
  "  globalThis.__test = { setState: (s) => { state = s; }, setSup: (s) => { sup = normSup(s); }, setExt: (e) => { ext = e; }, renderSide, compareLiveSession };\n})();"
);
vm.runInNewContext(instrumented, ctx, { filename: "web/app.js" });
const T = ctx.__test;

const hrefIds = (html) => [...String(html || "").matchAll(/href="#\/[px]\/([^"]+)"/g)].map((m) => decodeURIComponent(m[1]));
const dotStatusByHref = (html) => {
  const out = {};
  for (const m of String(html || "").matchAll(/href="#\/[px]\/([^"]+)"[\s\S]*?<span class="(?:dot|sr-dot) ([^"]+)"/g)) {
    out[decodeURIComponent(m[1])] = m[2];
  }
  return out;
};

const base = Date.parse("2026-11-28T10:00:00Z");
const ts = (min) => new Date(base + min * 60000).toISOString();

// 1) Mixed panes (workers with direct created_at, worker with created_at only on sup.workers, standalone pane, live external session)
// must sort strictly by creation time DESC regardless of initial status or updated activity time.
const makeAgents = (stMap, topicMap = {}) => [
  { pane_id: "w1:p1", name: "collie", kind: "claude", status: "idle", is_agent: true, cwd: "/tmp/router" },
  { pane_id: "w2:p1", name: "wk-oldest", kind: "codex", status: stMap["w2:p1"], is_agent: true, cwd: "/tmp/api", created_at: ts(10), topic: topicMap["w2:p1"] || "oldest" },
  { pane_id: "w3:p1", name: "wk-from-sup", kind: "claude", status: stMap["w3:p1"], is_agent: true, cwd: "/tmp/web", topic: topicMap["w3:p1"] || "from sup" },
  { pane_id: "w4:p1", name: "standalone-mid", kind: "agy", status: stMap["w4:p1"], is_agent: true, cwd: "/tmp/api", created_at: ts(25), topic: topicMap["w4:p1"] || "mid" },
  { pane_id: "w5:p1", name: "wk-newest", kind: "codex", status: stMap["w5:p1"], is_agent: true, cwd: "/tmp/api", created_at: ts(40), topic: topicMap["w5:p1"] || "newest" },
];
const makeWorkers = (stMap) => [
  { name: "wk-oldest", kind: "codex", pane_id: "w2:p1", status: stMap["w2:p1"], created_at: ts(10), ended: false, task: "oldest task" },
  { name: "wk-from-sup", kind: "claude", pane_id: "w3:p1", status: stMap["w3:p1"], created_at: ts(30), ended: false, task: "sup timestamp task" },
  { name: "wk-newest", kind: "codex", pane_id: "w5:p1", status: stMap["w5:p1"], created_at: ts(40), ended: false, task: "newest task" },
];
const makeExt = (extStatus, extUpdatedMin) => [
  { id: "claude-901", kind: "claude", app: "tmux", cwd: "/tmp/api", title: "ext session", live: true, status: extStatus, started: ts(20), updated: ts(extUpdatedMin) },
];

const wantLiveOrder = ["w5:p1", "w3:p1", "w4:p1", "claude-901", "w2:p1"];

// Round 1: newest is done/idle while oldest is working, external is blocked with latest updated
const r1 = { "w2:p1": "working", "w3:p1": "starting", "w4:p1": "waiting", "w5:p1": "done" };
T.setState({ ok: true, agents: makeAgents(r1), shells: [], machines: [{ id: "", name: "devbox", local: true, online: true }] });
T.setSup({ router: { status: "idle", alive: true }, entries: [], workers: makeWorkers(r1) });
T.setExt(makeExt("blocked", 55));
T.renderSide();

assert.deepEqual(hrefIds(getEl("#live-list").innerHTML), wantLiveOrder, "initial #live-list must follow created_at DESC regardless of status");
assert.deepEqual(hrefIds(getEl("#rail-live").innerHTML), wantLiveOrder, "initial #rail-live must match created_at DESC");
assert.equal(getEl("#live-count").textContent, "3/5 在跑", "#live-count still counts working/waiting/starting as busy");
assert.deepEqual(dotStatusByHref(getEl("#live-list").innerHTML), {
  "w5:p1": "done", "w3:p1": "starting", "w4:p1": "waiting", "claude-901": "blocked", "w2:p1": "working",
});

// Round 2: flip statuses completely + bump external updated + refresh topics + reverse input arrays
const r2 = { "w2:p1": "done", "w3:p1": "idle", "w4:p1": "working", "w5:p1": "working" };
T.setState({ ok: true, agents: makeAgents(r2, { "w2:p1": "finished!", "w5:p1": "streaming..." }).reverse(), shells: [], machines: [{ id: "", name: "devbox", local: true, online: true }] });
T.setSup({ router: { status: "idle", alive: true }, entries: [], workers: makeWorkers(r2).reverse() });
T.setExt(makeExt("working", 99));
T.renderSide();

assert.deepEqual(hrefIds(getEl("#live-list").innerHTML), wantLiveOrder, "status & output transitions must NEVER reorder #live-list");
assert.deepEqual(hrefIds(getEl("#rail-live").innerHTML), wantLiveOrder, "status & output transitions must NEVER reorder #rail-live");
assert.equal(getEl("#live-count").textContent, "3/5 在跑");
assert.deepEqual(dotStatusByHref(getEl("#live-list").innerHTML), {
  "w5:p1": "working", "w3:p1": "idle", "w4:p1": "working", "claude-901": "working", "w2:p1": "done",
}, "status dots must reflect latest statuses in place");

// Round 3: herdr pane transitions working -> blocked -> working:
// #attention orders blocked panes by created_at DESC, #rail-live keeps exact creation-time slot with zero icon jump,
// and unblocking restores the pane to its exact original slot in #live-list.
const r3 = { "w2:p1": "blocked", "w3:p1": "idle", "w4:p1": "blocked", "w5:p1": "done" };
T.setState({ ok: true, agents: makeAgents(r3), shells: [], machines: [{ id: "", name: "devbox", local: true, online: true }] });
T.setSup({ router: { status: "idle", alive: true }, entries: [], workers: makeWorkers(r3) });
T.setExt(makeExt("working", 105));
T.renderSide();

assert.deepEqual(hrefIds(getEl("#attention").innerHTML), ["w4:p1", "w2:p1"], "#attention sorts blocked agents by created_at DESC");
assert.deepEqual(hrefIds(getEl("#live-list").innerHTML), ["w5:p1", "w3:p1", "claude-901"], "remaining #live-list items keep exact relative order");
assert.deepEqual(hrefIds(getEl("#rail-live").innerHTML), wantLiveOrder, "#rail-live must NOT jump icons when agents toggle blocked");
assert.equal(dotStatusByHref(getEl("#rail-live").innerHTML)["w4:p1"], "blocked", "#rail-live shows blocked dot in place");

// Unblock w4:p1 & w2:p1 -> return to exact original slots in #live-list.
const r4 = { "w2:p1": "idle", "w3:p1": "working", "w4:p1": "done", "w5:p1": "working" };
T.setState({ ok: true, agents: makeAgents(r4), shells: [], machines: [{ id: "", name: "devbox", local: true, online: true }] });
T.setSup({ router: { status: "idle", alive: true }, entries: [], workers: makeWorkers(r4) });
T.renderSide();
assert.deepEqual(hrefIds(getEl("#live-list").innerHTML), wantLiveOrder, "unblocked agents return to exact creation-time slots");

// 2) Newly dispatched worker lands at index 0 without disturbing existing items.
const withNew = [
  ...makeAgents(r4),
  { pane_id: "w9:p1", name: "wk-fresh", kind: "claude", status: "starting", is_agent: true, cwd: "/tmp/api", created_at: ts(50) },
];
T.setState({ ok: true, agents: withNew, shells: [], machines: [{ id: "", name: "devbox", local: true, online: true }] });
T.renderSide();
assert.deepEqual(hrefIds(getEl("#live-list").innerHTML), ["w9:p1", ...wantLiveOrder], "newly spawned worker inserts cleanly at top");

// 3) Identical creation timestamps break ties deterministically with natural numeric key order regardless of input permutation.
const tied = [
  { pane_id: "page-10", name: "agent-10", kind: "codex", status: "working", is_agent: true, cwd: "/tmp/api", created_at: ts(20) },
  { pane_id: "page-2", name: "agent-2", kind: "codex", status: "done", is_agent: true, cwd: "/tmp/api", created_at: ts(20) },
  { pane_id: "page-1", name: "agent-1", kind: "codex", status: "idle", is_agent: true, cwd: "/tmp/api", created_at: ts(20) },
];
T.setExt([]);
T.setSup({ router: { status: "idle", alive: true }, entries: [], workers: [] });
T.setState({ ok: true, agents: tied, shells: [], machines: [] });
T.renderSide();
const orderA = hrefIds(getEl("#live-list").innerHTML);
assert.deepEqual(orderA, ["page-1", "page-2", "page-10"], "tied timestamps break ties deterministically in natural key order");
T.setState({ ok: true, agents: [tied[2], tied[0], tied[1]], shells: [], machines: [] });
T.renderSide();
assert.deepEqual(hrefIds(getEl("#live-list").innerHTML), orderA, "permuted input with tied timestamps yields identical order");

// 4) Ended worker from an older session must NOT hijack created_at of a reused pane_id.
T.setSup({
  router: { status: "idle", alive: true },
  entries: [],
  workers: [
    { name: "old-ended", kind: "codex", pane_id: "w5:p1", status: "gone", created_at: ts(1), ended: true },
    { name: "active-ref", kind: "codex", pane_id: "w6:p1", status: "working", created_at: ts(10), ended: false },
  ],
});
T.setState({
  ok: true,
  agents: [
    { pane_id: "w5:p1", name: "reused-pane", kind: "codex", status: "working", is_agent: true, cwd: "/tmp/api", created_at: ts(30) },
    { pane_id: "w6:p1", name: "active-ref", kind: "codex", status: "working", is_agent: true, cwd: "/tmp/api" },
  ],
  shells: [],
  machines: [],
});
T.renderSide();
assert.deepEqual(hrefIds(getEl("#live-list").innerHTML), ["w5:p1", "w6:p1"], "reused pane keeps its own newer created_at instead of old ended worker");

console.log("OK: all sidebar stable created_at DESC sort UI contracts passed");
`
	cmd := exec.Command("node", "-e", script)
	cmd.Dir = filepath.Join("..", "..")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("sidebar stable sort UI test failed: %v\n%s", err, string(out))
	}
}
