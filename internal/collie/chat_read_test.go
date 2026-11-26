package collie

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http/httptest"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"
)

func TestChatReadCursorPersistenceAndAPI(t *testing.T) {
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
	if st.LastReadEntryID != "" {
		t.Fatalf("fresh DB LastReadEntryID = %q, want empty", st.LastReadEntryID)
	}

	herdr := newHerdr(filepath.Join(dir, "missing-herdr.sock"), "devbox", dir)
	herdr.local.conn.dial = func(context.Context) (net.Conn, error) { return nil, errors.New("offline") }
	srv := &Server{
		herdr: herdr,
		token: "test-tok",
		home:  dir,
		web:   fstest.MapFS{},
		auths: newAuth(db, "test-tok"),
	}
	sp := &Super{s: srv, db: db, dir: dir, st: st, subs: map[chan struct{}]bool{}}
	srv.sup = sp
	handler := srv.Routes()

	call := func(method, path, tok string, body any) *httptest.ResponseRecorder {
		var rd *bytes.Reader
		if body != nil {
			b, _ := json.Marshal(body)
			rd = bytes.NewReader(b)
		} else {
			rd = bytes.NewReader(nil)
		}
		req := httptest.NewRequest(method, path, rd)
		if tok != "" {
			req.Header.Set("Authorization", "Bearer "+tok)
		}
		if body != nil {
			req.Header.Set("Content-Type", "application/json")
		}
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		return rec
	}

	// Unauthenticated POST /api/chat/read is rejected.
	if rec := call("POST", "/api/chat/read", "", map[string]any{"entry_id": "e10"}); rec.Code != 401 {
		t.Fatalf("unauth POST /api/chat/read = %d, want 401", rec.Code)
	}

	// Initially both /api/state and /api/super omit last_read_entry_id (empty).
	for _, ep := range []string{"/api/state", "/api/super"} {
		rec := call("GET", ep, "test-tok", nil)
		if rec.Code != 200 {
			t.Fatalf("GET %s = %d: %s", ep, rec.Code, rec.Body.String())
		}
		var m map[string]any
		if err := json.Unmarshal(rec.Body.Bytes(), &m); err != nil {
			t.Fatalf("GET %s invalid JSON: %v", ep, err)
		}
		if v, ok := m["last_read_entry_id"]; ok && v != "" {
			t.Fatalf("initial GET %s last_read_entry_id = %v, want omitted/empty", ep, v)
		}
	}

	// Invalid entry IDs return 400.
	for _, bad := range []map[string]any{
		{},
		{"entry_id": ""},
		{"entry_id": "e0"},
		{"entry_id": "t12"},
		{"entry_id": "abc"},
		{"entry_id": "e-5"},
		{"entry_id": "e12x"},
	} {
		if rec := call("POST", "/api/chat/read", "test-tok", bad); rec.Code != 400 {
			t.Fatalf("POST /api/chat/read %v = %d, want 400", bad, rec.Code)
		}
	}

	// Subscribe to SSE wakeup to confirm MarkChatRead notifies only on forward change.
	ch, unsub := sp.subscribe()
	defer unsub()

	// Advance cursor via entry_id.
	rec := call("POST", "/api/chat/read", "test-tok", map[string]any{"entry_id": "e12"})
	if rec.Code != 200 {
		t.Fatalf("POST /api/chat/read e12 = %d: %s", rec.Code, rec.Body.String())
	}
	var out struct {
		OK              bool   `json:"ok"`
		LastReadEntryID string `json:"last_read_entry_id"`
		Changed         bool   `json:"changed"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if !out.OK || !out.Changed || out.LastReadEntryID != "e12" {
		t.Fatalf("advance e12 = %+v, want ok=true changed=true id=e12", out)
	}
	select {
	case <-ch:
	default:
		t.Fatal("MarkChatRead did not wake SSE subscribers on advance")
	}

	// Monotonic: posting older or equal entry_id must not regress or spuriously notify.
	for _, stale := range []string{"e5", "e12"} {
		rec = call("POST", "/api/chat/read", "test-tok", map[string]any{"entry_id": stale})
		if rec.Code != 200 {
			t.Fatalf("POST /api/chat/read %s = %d", stale, rec.Code)
		}
		out = struct {
			OK              bool   `json:"ok"`
			LastReadEntryID string `json:"last_read_entry_id"`
			Changed         bool   `json:"changed"`
		}{}
		_ = json.Unmarshal(rec.Body.Bytes(), &out)
		if !out.OK || out.Changed || out.LastReadEntryID != "e12" {
			t.Fatalf("stale %s response = %+v, want changed=false id=e12", stale, out)
		}
		select {
		case <-ch:
			t.Fatalf("stale %s should not wake subscribers", stale)
		default:
		}
	}

	// Paired browser device can also advance using last_read_entry_id alias.
	code, _ := srv.auths.newCode("browser")
	_, browserTok, err := srv.auths.redeem(code, "browser", "Phone · Safari", "iOS")
	if err != nil {
		t.Fatal(err)
	}
	rec = call("POST", "/api/chat/read", browserTok, map[string]any{"last_read_entry_id": "e42"})
	if rec.Code != 200 {
		t.Fatalf("browser POST /api/chat/read = %d: %s", rec.Code, rec.Body.String())
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	if !out.OK || !out.Changed || out.LastReadEntryID != "e42" {
		t.Fatalf("browser advance e42 = %+v, want changed=true id=e42", out)
	}

	// Both /api/state and /api/super now expose last_read_entry_id == "e42".
	for _, ep := range []string{"/api/state", "/api/super"} {
		rec := call("GET", ep, "test-tok", nil)
		if rec.Code != 200 {
			t.Fatalf("GET %s = %d", ep, rec.Code)
		}
		var m map[string]any
		_ = json.Unmarshal(rec.Body.Bytes(), &m)
		if got, _ := m["last_read_entry_id"].(string); got != "e42" {
			t.Fatalf("GET %s last_read_entry_id = %q, want e42", ep, got)
		}
	}

	// Persisted straight to SQLite without calling sp.flush(): reload immediately sees e42.
	reloaded, err := loadState(db, dir)
	if err != nil {
		t.Fatal(err)
	}
	if reloaded.LastReadEntryID != "e42" {
		t.Fatalf("reloaded LastReadEntryID = %q, want e42", reloaded.LastReadEntryID)
	}

	// Full saveState round-trip also preserves LastReadEntryID.
	reloaded.LastReadEntryID = "e99"
	if err := saveState(db, reloaded, nil); err != nil {
		t.Fatal(err)
	}
	reloaded2, err := loadState(db, dir)
	if err != nil {
		t.Fatal(err)
	}
	if reloaded2.LastReadEntryID != "e99" {
		t.Fatalf("saveState round-trip LastReadEntryID = %q, want e99", reloaded2.LastReadEntryID)
	}
}

func TestUnreadAnchorAndJumpPillJS(t *testing.T) {
	nodeBin, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node not installed")
	}
	root := filepath.Join("..", "..")
	jsTest := `
const fs = require("fs");
const path = require("path");
const vm = require("vm");
const assert = require("assert/strict");

const appSrc = fs.readFileSync(path.join(process.argv[1], "web", "app.js"), "utf8");
const cssSrc = fs.readFileSync(path.join(process.argv[1], "web", "app.css"), "utf8");

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
    id,
    tagName: "DIV",
    value: "",
    textContent: "",
    innerHTML: "",
    hidden: false,
    disabled: false,
    checked: false,
    className: "",
    style: { setProperty() {}, removeProperty() {} },
    dataset: {},
    classList: makeClassList(),
    children: [],
    scrollTop: 0,
    scrollHeight: 200,
    clientHeight: 200,
    offsetWidth: 400,
    offsetParent: {},
    isConnected: true,
    _qs: {},
    _qsa: {},
    _rect: { top: 0, bottom: 40, left: 0, right: 300, width: 300, height: 40 },
    addEventListener(type, fn) {
      if (!listeners.has(type)) listeners.set(type, []);
      listeners.get(type).push(fn);
    },
    removeEventListener(type, fn) {
      const list = listeners.get(type);
      if (!list) return;
      const idx = list.indexOf(fn);
      if (idx >= 0) list.splice(idx, 1);
    },
    dispatch(type, ev = {}) {
      const e = {
        type,
        target: el,
        currentTarget: el,
        defaultPrevented: false,
        preventDefault() { this.defaultPrevented = true; },
        stopPropagation() {},
        composedPath: () => [el],
        ...ev,
      };
      for (const fn of [...(listeners.get(type) || [])]) fn(e);
      return e;
    },
    requestSubmit() { this.dispatch("submit", {}); },
    querySelector(sel) {
      if (typeof el._querySelector === "function") {
        const custom = el._querySelector(sel);
        if (custom !== undefined) return custom;
      }
      if (el._qs[sel]) return el._qs[sel];
      const child = makeEl(id + ":" + sel);
      el._qs[sel] = child;
      return child;
    },
    querySelectorAll(sel) {
      if (typeof el._querySelectorAll === "function") return el._querySelectorAll(sel);
      return el._qsa[sel] || [];
    },
    setAttribute(k, v) { attrs.set(k, String(v)); },
    getAttribute(k) { return attrs.get(k) ?? ""; },
    removeAttribute(k) { attrs.delete(k); },
    hasAttribute(k) { return attrs.has(k); },
    closest() { return null; },
    contains() { return false; },
    append() {},
    remove() {},
    after() {},
    focus() {},
    click() {},
    scrollIntoView() {},
    scrollTo(opts) {
      const top = typeof opts === "object" ? opts.top : opts;
      const maxTop = Math.max(0, el.scrollHeight - el.clientHeight);
      el.scrollTop = Math.max(0, Math.min(maxTop, top));
      return Promise.resolve();
    },
    getBoundingClientRect() {
      if (typeof el._getRect === "function") return el._getRect();
      return el._rect;
    },
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
getEl("#super-jump")._qs["#super-jump-badge"] = getEl("#super-jump-badge");
getEl("#super-jump-badge").hidden = true;
getEl("#super-jump").hidden = true;
getEl("#thread-drawer").hidden = true;
getEl("#thread-scrim").hidden = true;
getEl("#file-view").hidden = true;

// Wire dynamic layout model for #super-thread and its scroller.
const threadBox = getEl("#super-thread");
const scroller = threadBox.parentElement;
scroller.clientHeight = 600;
scroller.scrollHeight = 600;
scroller.scrollTop = 0;
scroller._getRect = () => ({ top: 0, bottom: scroller.clientHeight, left: 0, right: 800, width: 800, height: scroller.clientHeight });

let layoutNodes = []; // [{ id, isSep, y, h }]
let simOptHeights = false;
function rebuildLayout(rowHeight = 140) {
  const html = threadBox.innerHTML || "";
  layoutNodes = [];
  const tokenRe = /(<div class="unread-sep" id="unread-sep"[^>]*>)|data-id="(e\d+)"/g;
  let m, y = 32;
  const matches = [];
  while ((m = tokenRe.exec(html)) !== null) {
    matches.push({ idx: m.index, isSep: !!m[1], id: m[2] || "__sep__" });
  }
  for (let i = 0; i < matches.length; i++) {
    const cur = matches[i];
    if (cur.isSep) {
      layoutNodes.push({ id: "__sep__", isSep: true, y, h: 24 });
      y += 40;
    } else {
      let h = rowHeight;
      if (simOptHeights) {
        const end = i + 1 < matches.length ? matches[i + 1].idx : html.length;
        const slice = html.slice(cur.idx, end);
        if (slice.includes('class="reply-opt"')) h += 48;
        else if (slice.includes("reply-picked")) h += 28;
      }
      layoutNodes.push({ id: cur.id, isSep: false, y, h });
      y += h + 28;
    }
  }
  scroller.scrollHeight = Math.max(scroller.clientHeight, y + 24);
}
const wrapLayoutNode = (n) => {
  const el = makeEl(n.id);
  el.dataset = n.isSep ? {} : { id: n.id };
  el._getRect = () => ({ top: n.y - scroller.scrollTop, bottom: n.y + n.h - scroller.scrollTop, left: 0, right: 700, width: 700, height: n.h });
  el.closest = () => el;
  return el;
};
threadBox._querySelector = (sel) => {
  if (sel === "#unread-sep") {
    const n = layoutNodes.find((x) => x.isSep);
    return n ? wrapLayoutNode(n) : null;
  }
  const m = /data-id="([^"]+)"/.exec(sel);
  if (m) {
    const id = m[1].replace(/\\/g, "");
    const n = layoutNodes.find((x) => !x.isSep && x.id === id);
    return n ? wrapLayoutNode(n) : null;
  }
  return undefined;
};
threadBox._querySelectorAll = (sel) => {
  if (sel.includes(".msg[data-id]")) {
    return layoutNodes.filter((x) => !x.isSep).map(wrapLayoutNode);
  }
  return [];
};
let curRowHeight = 140;
let innerHTMLVal = "";
Object.defineProperty(threadBox, "innerHTML", {
  get: () => innerHTMLVal,
  set: (v) => { innerHTMLVal = String(v || ""); rebuildLayout(curRowHeight); },
});

const storage = new Map();
const docListeners = new Map();
const winListeners = new Map();
const fetchCalls = [];
let docHidden = false;
let serverLastRead = "";
let saySeq = 500;
let sayFail = false;
let uuidSeq = 0;

class FakeES {
  constructor(url) { this.url = url; this.readyState = 1; this.ls = {}; }
  addEventListener(t, fn) { (this.ls[t] ||= []).push(fn); }
  close() { this.readyState = 2; }
}

const doc = {
  documentElement: makeEl("html"),
  body: makeEl("body"),
  activeElement: null,
  get hidden() { return docHidden; },
  hasFocus: () => !docHidden,
  createElement: (tag) => makeEl(tag),
  getElementById: (id) => getEl("#" + id),
  querySelector: (sel) => (sel === "dialog[open]" ? null : getEl(sel)),
  querySelectorAll: () => [],
  addEventListener(type, fn) {
    if (!docListeners.has(type)) docListeners.set(type, []);
    docListeners.get(type).push(fn);
  },
  dispatchDoc(type, ev = {}) {
    for (const fn of [...(docListeners.get(type) || [])]) fn({ type, target: makeEl("doc"), ...ev });
  },
};

const okJSON = (data) => ({
  ok: true, status: 200, statusText: "OK",
  headers: { get: () => null },
  json: async () => data,
  text: async () => JSON.stringify(data),
});

const resizeObservers = [];
const ctx = vm.createContext({
  console, setTimeout, clearTimeout, setInterval: () => 0, clearInterval() {}, URL, URLSearchParams,
  Blob, Uint8Array, TextEncoder, atob, AbortController, Event,
  Map, Set, Date, Math, JSON, RegExp, Array, Object, Promise, Error, Number, String, Boolean,
  MutationObserver: class { observe() {} disconnect() {} },
  ResizeObserver: class {
    constructor(cb) { this.cb = cb; this.active = true; resizeObservers.push(this); }
    observe() {}
    disconnect() { this.active = false; }
    fire() { if (this.active) this.cb([]); }
  },
  HTMLDialogElement: { prototype: {} },
  getComputedStyle: () => ({ getPropertyValue: () => "" }),
  crypto: { randomUUID: () => "cr-uuid-" + (++uuidSeq) },
  navigator: { userAgent: "Mac Chrome", platform: "MacIntel" },
  location: { href: "http://127.0.0.1:7788/#/collie", origin: "http://127.0.0.1:7788", pathname: "/", search: "", hash: "#/collie", hostname: "127.0.0.1" },
  history: { state: { collieIdx: 0 }, replaceState() {}, pushState() {}, back() {} },
  localStorage: {
    getItem: (k) => (storage.has(k) ? storage.get(k) : null),
    setItem: (k, v) => storage.set(k, String(v)),
    removeItem: (k) => storage.delete(k),
  },
  document: doc,
  EventSource: FakeES,
  CSS: { escape: (s) => String(s), supports: () => true },
  matchMedia: () => ({ matches: false, addEventListener() {} }),
  requestAnimationFrame: (fn) => { fn(); return 1; },
  cancelAnimationFrame: () => {},
  performance: { now: () => Date.now() },
  innerWidth: 1440,
  innerHeight: 900,
  addEventListener(type, fn) {
    if (!winListeners.has(type)) winListeners.set(type, []);
    winListeners.get(type).push(fn);
  },
  removeEventListener() {},
  dispatchEvent() {},
  fetch: async (url, init = {}) => {
    const u = String(url);
    fetchCalls.push({ url: u, method: init.method || "GET", body: init.body, keepalive: !!init.keepalive });
    if (u === "/api/session") return okJSON({ ok: true });
    if (u === "/api/super") return okJSON({ router: { status: "idle" }, entries: [], workers: [], pending: [], pinned: [], last_read_entry_id: serverLastRead });
    if (u === "/api/state") return okJSON({ ok: true, agents: [], shells: [], machines: [{ id: "", name: "hub", local: true, online: true }], last_read_entry_id: serverLastRead });
    if (u === "/api/chat/read") {
      const p = JSON.parse(init.body || "{}");
      const id = p.entry_id || p.last_read_entry_id || "";
      const n = parseInt(id.replace(/^e/, ""), 10) || 0;
      const curN = parseInt(serverLastRead.replace(/^e/, ""), 10) || 0;
      if (n > curN) serverLastRead = id;
      return okJSON({ ok: true, last_read_entry_id: serverLastRead, changed: n > curN });
    }
    if (u === "/api/super/say") {
      if (sayFail) {
        return {
          ok: false, status: 500, statusText: "Server Error",
          headers: { get: () => null },
          json: async () => ({ error: "发送失败" }),
          text: async () => JSON.stringify({ error: "发送失败" }),
        };
      }
      const id = "e" + (++saySeq);
      return okJSON({ entry: id, trigger: "t" + saySeq });
    }
    if (u.startsWith("/api/super/settings")) return okJSON({ settings: { router_kind: "claude", worker_kind: "claude" } });
    if (u.startsWith("/api/models")) return okJSON({ models: [], efforts: [] });
    if (u === "/api/hubs") return okJSON({ hubs: [] });
    if (u === "/api/version") return okJSON({ version: "v0.2.69", latest: "v0.2.69" });
    if (u === "/api/external" || u === "/api/history") return okJSON({ sessions: [] });
    return okJSON({ ok: true });
  },
});
ctx.window = ctx;
ctx.globalThis = ctx;

const marker = "  boot();\n})();";
assert.ok(appSrc.includes(marker), "expected boot marker in web/app.js");
const instrumented = appSrc.replace(
  marker,
  "  globalThis.__test = { getSup: () => sup, setSup: (s) => { sup = normSup(s); }, setLocalState, setView: (v) => { view = v; }, setBooted: (b) => { booted = !!b; }, route, renderSide, renderSuper, renderPendingBar, activePending, sendSuper, computeUnreadInfo, updateSuperJump, maybeScheduleReadAtBottom, flushPendingChatRead, openThreadDrawer, closeThreadDrawer, getReadState: () => ({ readCursorId, serverReadCursorId, sessionUnreadMarkerId, unreadAnchorApplied }), resetReadState: (srv = '', loc = '') => { older = []; sup = { router: { status: 'idle' }, entries: [], workers: [], pending: [], pinned: [] }; pendingSays.length = 0; dismissedLocal.clear(); pickedReplyOpts.clear(); readCursorId = ''; serverReadCursorId = srv; sessionUnreadMarkerId = ''; unreadAnchorApplied = false; clearTimeout(readSyncTimer); readSyncTimer = 0; pendingReadSyncId = ''; if (loc) localStorage.setItem('collie.chatReadEntryId', JSON.stringify(loc)); else localStorage.removeItem('collie.chatReadEntryId'); } };\n})();"
);
vm.runInContext(instrumented, ctx);

const wait = (ms) => new Promise((r) => setTimeout(r, ms));
const mkEntry = (n, role, text, extra = {}) => ({
  id: "e" + n,
  role,
  thread: "chat:main",
  text,
  at: new Date(Date.now() - (50 - n) * 60000).toISOString(),
  ...extra,
});

(async () => {
  const T = ctx.__test;
  T.setBooted(true);
  T.setView("collie");

  // 1) First-time enablement (no cursor): sticks to bottom, NO unread divider, seeds cursor.
  fetchCalls.length = 0;
  serverLastRead = "";
  T.resetReadState("", "");
  const tenEntries = [];
  for (let i = 1; i <= 10; i++) tenEntries.push(mkEntry(i, i % 2 ? "user" : "collie", "msg " + i));
  T.setSup({ router: { status: "idle" }, entries: tenEntries, workers: [], pending: [], pinned: [] });
  T.renderSuper({ force: true, unreadAnchor: true });
  assert.ok(!threadBox.innerHTML.includes('id="unread-sep"'), "first-time open must not show unread divider");
  assert.equal(scroller.scrollTop, scroller.scrollHeight, "first-time open must stick to bottom");
  assert.equal(getEl("#super-jump").hidden, true, "jump pill hidden at bottom");
  await wait(520);
  assert.equal(serverLastRead, "e10", "first-time bottom visit must persist cursor e10");

  // 2) Substantive unread filtering + many unread (off-screen above bottom): anchors to divider with ~14px pad + shows pill.
  fetchCalls.length = 0;
  serverLastRead = "e4";
  T.resetReadState("", "");
  const manyEntries = [
    mkEntry(1, "user", "old u1"),
    mkEntry(2, "collie", "old c2"),
    mkEntry(3, "user", "old u3"),
    mkEntry(4, "collie", "last read c4"),
    mkEntry(5, "user", "own message after read"),
    mkEntry(6, "event", "派出 w1（codex）", { tone: "spawn", ref: "w1" }),
    mkEntry(7, "collie", "first unread collie reply"),
    mkEntry(8, "collie", "second unread collie reply"),
    mkEntry(9, "collie", "third unread collie reply"),
    mkEntry(10, "collie", "fourth unread collie reply"),
    mkEntry(11, "collie", "fifth unread collie reply"),
    mkEntry(12, "event", "w1 启动失败", { tone: "error", ref: "w1" }),
  ];
  T.setSup({ router: { status: "idle" }, entries: manyEntries, workers: [], pending: [], pinned: [], last_read_entry_id: "e4" });
  const info = T.computeUnreadInfo();
  assert.equal(info.firstUnreadId, "e7", "first substantive unread must skip user & spawn event");
  assert.equal(info.count, 6, "substantive unread count must include 5 collie + 1 error event");

  T.renderSuper({ force: true, unreadAnchor: true });
  assert.ok(threadBox.innerHTML.includes('id="unread-sep" data-first-unread="e7"'), "unread divider must sit above e7");
  const sepNode = layoutNodes.find((x) => x.isSep);
  assert.ok(sepNode, "layout must include unread-sep");
  const sepVisualTop = sepNode.y - scroller.scrollTop;
  assert.ok(sepVisualTop >= 12 && sepVisualTop <= 16, "divider top breathing room must be 12-16px, got " + sepVisualTop);
  assert.equal(getEl("#super-jump").hidden, false, "jump button must appear when anchored above bottom");
  assert.equal(getEl("#super-jump-badge").hidden, false, "jump badge must appear when unread > 0");
  assert.equal(getEl("#super-jump-badge").textContent, "6", "jump badge must show compact unread count");
  assert.equal(getEl("#super-jump").getAttribute("aria-label"), "回到底部，6 条未读", "jump aria-label must describe unread count");
  assert.equal(getEl("#super-jump").title, "回到底部，6 条未读", "jump title must describe unread count");
  assert.equal(getEl("#super-jump").classList.contains("has-unread"), true, "jump button must carry has-unread class");
  await wait(520);
  assert.equal(fetchCalls.filter((c) => c.url === "/api/chat/read").length, 0, "must not advance cursor while stopped above bottom");

  // 3) Thread drawer 0px scroll anchor preservation while stopped at unread anchor.
  const scrollBeforeDrawer = scroller.scrollTop;
  T.openThreadDrawer("e7", "e7", "e7");
  assert.equal(scroller.scrollTop, scrollBeforeDrawer, "opening thread drawer must keep 0px scroll drift");
  T.closeThreadDrawer({ immediate: true });
  assert.equal(scroller.scrollTop, scrollBeforeDrawer, "closing thread drawer must keep 0px scroll drift");
  assert.equal(fetchCalls.filter((c) => c.url === "/api/chat/read").length, 0, "drawer toggle must not prematurely advance read cursor");

  // 4) Clicking jump button scrolls to bottom and immediately advances server + local cursor.
  getEl("#super-jump").dispatch("click");
  await wait(30);
  assert.equal(serverLastRead, "e12", "clicking jump button must immediately POST /api/chat/read to e12");
  assert.equal(JSON.parse(storage.get("collie.chatReadEntryId")), "e12", "localStorage cursor updated to e12");
  assert.equal(scroller.scrollTop, scroller.scrollHeight, "clicking jump button must land at bottom");
  assert.equal(getEl("#super-jump").hidden, true, "jump button must hide at bottom");
  assert.equal(getEl("#super-jump-badge").hidden, true, "jump badge must hide after read");
  assert.equal(getEl("#super-jump-badge").textContent, "", "jump badge must clear after read");
  assert.equal(getEl("#super-jump").getAttribute("aria-label"), "回到底部", "jump aria-label resets after read");

  // 4b) Scrolled up with 0 unread: pure circular icon button without badge text.
  scroller.scrollTop = 0;
  T.updateSuperJump();
  assert.equal(getEl("#super-jump").hidden, false, "jump button appears when scrolled up with 0 unread");
  assert.equal(getEl("#super-jump-badge").hidden, true, "badge stays hidden when 0 unread");
  assert.equal(getEl("#super-jump-badge").textContent, "", "no text rendered when 0 unread");
  assert.equal(getEl("#super-jump").classList.contains("has-unread"), false, "has-unread cleared when 0 unread");
  assert.equal(getEl("#super-jump").getAttribute("aria-label"), "回到底部", "aria-label is 回到底部 when 0 unread");
  scroller.scrollTop = scroller.scrollHeight;
  T.updateSuperJump();

  // 5) Simulated reload after reading: lands at bottom, NO unread divider, NO jump button.
  serverLastRead = "e12";
  T.resetReadState("", "");
  T.setSup({ router: { status: "idle" }, entries: manyEntries, workers: [], pending: [], pinned: [], last_read_entry_id: "e12" });
  T.renderSuper({ force: true, unreadAnchor: true });
  assert.ok(!threadBox.innerHTML.includes('id="unread-sep"'), "reloading after read must not show unread divider");
  assert.equal(scroller.scrollTop, scroller.scrollHeight, "reloading after read must stick to bottom");
  assert.equal(getEl("#super-jump").hidden, true, "reloading after read keeps jump button hidden");

  // 6) Few unread (fits at bottom): anti-annoyance boundary keeps bottom + subtle divider + debounced read.
  fetchCalls.length = 0;
  serverLastRead = "e11";
  curRowHeight = 90;
  T.resetReadState("", "");
  T.setSup({ router: { status: "idle" }, entries: manyEntries, workers: [], pending: [], pinned: [], last_read_entry_id: "e11" });
  T.renderSuper({ force: true, unreadAnchor: true });
  assert.ok(threadBox.innerHTML.includes('id="unread-sep" data-first-unread="e12"'), "short unread still renders subtle divider");
  assert.equal(scroller.scrollTop, scroller.scrollHeight, "short unread visible at bottom must NOT pull viewport upward");
  await wait(520);
  assert.equal(serverLastRead, "e12", "short unread visible at bottom must advance read cursor after debounce");

  // 7) Background tab guard: hidden tab receiving new message at bottom must NOT advance read cursor.
  fetchCalls.length = 0;
  docHidden = true;
  const withE13 = [...manyEntries, mkEntry(13, "collie", "arrived while tab hidden")];
  T.setSup({ router: { status: "idle" }, entries: withE13, workers: [], pending: [], pinned: [], last_read_entry_id: "e12" });
  T.renderSuper();
  await wait(520);
  assert.equal(serverLastRead, "e12", "hidden tab must not auto-advance read cursor");
  docHidden = false;

  // 8) Sending a message clears unread divider and advances read cursor to the new entry.
  curRowHeight = 140;
  T.resetReadState("", "");
  T.setSup({ router: { status: "idle" }, entries: manyEntries, workers: [], pending: [], pinned: [], last_read_entry_id: "e4" });
  T.renderSuper({ force: true, unreadAnchor: true });
  assert.ok(threadBox.innerHTML.includes('id="unread-sep"'));
  await T.sendSuper("收到，继续", null, false);
  assert.ok(!threadBox.innerHTML.includes('id="unread-sep"'), "sending message clears unread divider");
  assert.equal(serverLastRead, "e501", "sending message advances server cursor to new entry");

  // 9) Stale localStorage cursor higher than hub sequence is ignored cleanly.
  serverLastRead = "";
  T.resetReadState("", "e9999");
  T.setSup({ router: { status: "idle" }, entries: tenEntries, workers: [], pending: [], pinned: [] });
  T.renderSuper({ force: true, unreadAnchor: true });
  assert.ok(!threadBox.innerHTML.includes('id="unread-sep"'), "stale localStorage cursor must be discarded");
  assert.equal(scroller.scrollTop, scroller.scrollHeight, "stale cursor reset sticks to bottom");

  // 10) Unread > 99 caps badge display at 99+ while preserving exact count in aria-label/title.
  const hugeEntries = [mkEntry(1, "collie", "read baseline")];
  for (let i = 2; i <= 106; i++) hugeEntries.push(mkEntry(i, "collie", "unread " + i));
  serverLastRead = "e1";
  T.resetReadState("", "");
  T.setSup({ router: { status: "idle" }, entries: hugeEntries, workers: [], pending: [], pinned: [], last_read_entry_id: "e1" });
  T.renderSuper({ force: true, unreadAnchor: true });
  assert.equal(getEl("#super-jump").hidden, false, "jump button shown for 105 unread");
  assert.equal(getEl("#super-jump-badge").hidden, false, "badge shown for 105 unread");
  assert.equal(getEl("#super-jump-badge").textContent, "99+", "badge caps at 99+");
  assert.equal(getEl("#super-jump").getAttribute("aria-label"), "回到底部，105 条未读", "aria-label preserves exact 105 unread count");
  assert.equal(getEl("#super-jump").title, "回到底部，105 条未读", "title preserves exact 105 unread count");

  // 11) P1 Sidebar subtle unread pill on non-main views (mutually exclusive with amber pending).
  assert.ok(cssSrc.includes(".count-pill.unread"), "CSS must define subtle .count-pill.unread style");
  const np = getEl("#nav-pending");
  // On main chat view with 0 pending & 6 unread: sidebar pill stays hidden (jump button handles it).
  serverLastRead = "e4";
  T.resetReadState("", "");
  T.setView("collie");
  T.setSup({ router: { status: "idle" }, entries: manyEntries, workers: [], pending: [], pinned: [], last_read_entry_id: "e4" });
  T.renderSide();
  assert.equal(np.hidden, true, "sidebar unread pill stays hidden on #/collie");

  // On non-main view (auto / pane / prefs / null mobile list) with 0 pending & 6 unread: neutral subtle unread pill.
  for (const otherView of ["auto", "pane", "prefs", null]) {
    T.setView(otherView);
    T.renderSide();
    assert.equal(np.hidden, false, "sidebar unread pill must show on view=" + String(otherView));
    assert.equal(np.classList.contains("unread"), true, "sidebar unread pill carries .unread class");
    assert.equal(np.classList.contains("attn"), false, "sidebar unread pill must not carry .attn class");
    assert.equal(np.textContent, "6", "sidebar unread pill shows exact unread count");
    assert.equal(np.title, "6 条未读", "sidebar unread pill title describes unread count");
    assert.equal(np.getAttribute("aria-label"), "6 条未读", "sidebar unread pill aria-label describes unread count");
  }

  // Unread > 99 on non-main view caps at 99+ while preserving exact count in title/aria-label.
  T.setView("auto");
  serverLastRead = "e1";
  T.resetReadState("", "");
  T.setSup({ router: { status: "idle" }, entries: hugeEntries, workers: [], pending: [], pinned: [], last_read_entry_id: "e1" });
  T.renderSide();
  assert.equal(np.hidden, false, "sidebar unread pill shown for 105 unread");
  assert.equal(np.classList.contains("unread"), true, "sidebar unread pill uses .unread");
  assert.equal(np.textContent, "99+", "sidebar unread pill caps at 99+");
  assert.equal(np.title, "105 条未读", "sidebar unread title preserves exact 105 count");

  // Pending > 0 on non-main view takes precedence: amber .attn pill with「待处理」copy, no dual badges.
  T.setView("auto");
  serverLastRead = "e4";
  T.resetReadState("", "");
  const checkEntries = [
    mkEntry(1, "user", "hi"),
    mkEntry(2, "collie", "请去控制台手动续签证书", { check: true }),
    mkEntry(3, "collie", "另一条普通汇报"),
  ];
  T.setSup({
    router: { status: "idle" },
    entries: checkEntries,
    workers: [],
    pending: [{ id: "e2", at: checkEntries[1].at, summary: "请去控制台手动续签证书", check: true }],
    pinned: [],
    last_read_entry_id: "e1",
  });
  T.renderSide();
  assert.equal(np.hidden, false, "sidebar pending pill shown");
  assert.equal(np.classList.contains("attn"), true, "pending pill uses amber .attn");
  assert.equal(np.classList.contains("unread"), false, "pending pill removes .unread");
  assert.equal(np.textContent, "1", "pending pill shows pending count");
  assert.equal(np.title, "1 条待处理", "pending pill uses unified 待处理 wording");

  // Cross-device read cursor update from server (e.g. Feishu reply) clears non-main sidebar unread pill.
  T.setSup({ router: { status: "idle" }, entries: manyEntries, workers: [], pending: [], pinned: [], last_read_entry_id: "e12" });
  T.renderSide();
  assert.equal(np.hidden, true, "advancing server last_read_entry_id clears sidebar unread pill");

  // 12) P0 Web copy unification (已处理 / 待处理 / 标为已处理) + pure --check survives unquoted user send.
  T.setView("collie");
  serverLastRead = "e1";
  T.resetReadState("", "");
  const p0Entries = [
    mkEntry(1, "user", "巡检一下"),
    mkEntry(2, "collie", "测试环境证书将于明天到期，请在控制台续签", { check: true }),
  ];
  T.setSup({
    router: { status: "idle" },
    entries: p0Entries,
    workers: [],
    pending: [{ id: "e2", at: p0Entries[1].at, summary: "测试环境证书将于明天到期，请在控制台续签", check: true }],
    pinned: [],
    last_read_entry_id: "e1",
  });
  T.renderSuper({ force: true });
  assert.ok(threadBox.innerHTML.includes("已处理</button>"), "check button must read 已处理");
  assert.ok(!threadBox.innerHTML.includes("已看</button>"), "check button must not read 已看");
  const pendingBarHtml = getEl("#pending-bar").innerHTML;
  assert.ok(pendingBarHtml.includes(">待处理<"), "pending bar must read 待处理");
  assert.ok(!pendingBarHtml.includes("待你确认"), "pending bar must not read 待你确认");
  assert.ok(pendingBarHtml.includes('title="标为已处理"') && pendingBarHtml.includes('aria-label="标为已处理"'), "pending dismiss X must use 标为已处理");

  // Unquoted user send must NOT optimistically clear pure --check item.
  await T.sendSuper("顺便问下构建状态", null, false);
  assert.equal(T.activePending().length, 1, "pure --check must stay active after unquoted sendSuper");
  assert.equal(T.activePending()[0].id, "e2", "e2 remains pending after unquoted sendSuper");

  // Legacy hub fallback (without sup.pending array) also keeps pure --check across unquoted user message.
  const legacyEntries = [
    mkEntry(1, "collie", "请去审批后台点一下通过", { check: true }),
    mkEntry(2, "user", "顺便跑下测试"),
  ];
  T.resetReadState("", "");
  T.setSup({ router: { status: "idle" }, entries: legacyEntries, workers: [], pinned: [] });
  T.renderSuper({ force: true });
  assert.ok(threadBox.innerHTML.includes('data-ack="e1"') && threadBox.innerHTML.includes("已处理</button>"), "legacy fallback keeps pure --check across unquoted user msg");

  // 13) #t8536 Reload scroll-anchor stability against async image expansion & short-unread fitsAtBottom rule.
  // 13a) Reload with NO unread: async image loads expand scrollHeight by +1800px -> stays glued to bottom!
  curRowHeight = 140;
  serverLastRead = "e12";
  T.resetReadState("", "");
  T.setSup({ router: { status: "idle" }, entries: manyEntries, workers: [], pending: [], pinned: [], last_read_entry_id: "e12" });
  T.renderSuper({ force: true, unreadAnchor: true });
  assert.equal(scroller.scrollTop, scroller.scrollHeight, "initial reload with no unread lands at bottom");
  const initialBottomH = scroller.scrollHeight;
  scroller.scrollHeight = initialBottomH + 900;
  threadBox.dispatch("load", { target: { tagName: "IMG" } });
  assert.equal(scroller.scrollTop, initialBottomH + 900, "img load keeps viewport glued to bottom during initial settle");
  scroller.scrollHeight = initialBottomH + 1800;
  for (const ro of resizeObservers) ro.fire();
  assert.equal(scroller.scrollTop, initialBottomH + 1800, "ResizeObserver keeps viewport glued to bottom during initial settle");

  // 13b) Reload WITH off-screen unread: images ABOVE #unread-sep expand by +1200px -> #unread-sep stays locked at UNREAD_TOP_PAD (14px)!
  curRowHeight = 140;
  serverLastRead = "e4";
  T.resetReadState("", "");
  T.setSup({ router: { status: "idle" }, entries: manyEntries, workers: [], pending: [], pinned: [], last_read_entry_id: "e4" });
  T.renderSuper({ force: true, unreadAnchor: true });
  const sepBeforeGrow = layoutNodes.find((x) => x.isSep);
  assert.ok(sepBeforeGrow);
  assert.equal(sepBeforeGrow.y - scroller.scrollTop, 14, "initial unread divider locked at 14px");
  for (const n of layoutNodes) {
    if (n.y >= sepBeforeGrow.y) n.y += 1200;
  }
  scroller.scrollHeight += 1200;
  threadBox.dispatch("load", { target: { tagName: "IMG" } });
  assert.equal(sepBeforeGrow.y - scroller.scrollTop, 14, "after images above expand +1200px, #unread-sep stays locked at 14px");

  // 13c) User manual scroll (wheel / touchstart / pointerdown / ArrowUp) IMMEDIATELY releases lock (never fights user).
  const userScrolledTo = 180;
  scroller.dispatch("wheel", { deltaY: -80 });
  scroller.scrollTop = userScrolledTo;
  for (const n of layoutNodes) {
    if (n.y >= sepBeforeGrow.y) n.y += 400;
  }
  scroller.scrollHeight += 400;
  threadBox.dispatch("load", { target: { tagName: "IMG" } });
  for (const ro of resizeObservers) ro.fire();
  assert.equal(scroller.scrollTop, userScrolledTo, "manual wheel immediately releases initial anchor lock so later img loads never hijack scroll");

  // 13d) Short-unread boundary (sepTop - UNREAD_TOP_PAD >= maxTop): 3 short unread messages totaling ~380px (< 600px viewport)
  // MUST evaluate fitsAtBottom = true and stick cleanly to bottom instead of stopping mid-air!
  curRowHeight = 90;
  serverLastRead = "e9";
  T.resetReadState("", "");
  T.setSup({ router: { status: "idle" }, entries: manyEntries, workers: [], pending: [], pinned: [], last_read_entry_id: "e9" });
  T.renderSuper({ force: true, unreadAnchor: true });
  const shortSep = layoutNodes.find((x) => x.isSep);
  assert.ok(shortSep, "3-msg short unread still renders divider");
  const shortMaxTop = Math.max(0, scroller.scrollHeight - scroller.clientHeight);
  assert.ok(shortSep.y - 14 >= shortMaxTop, "precondition: #unread-sep sits inside bottom-scrolled viewport");
  assert.equal(scroller.scrollTop, scroller.scrollHeight, "short unread fitting in viewport sticks cleanly to bottom");

  // 14) #t8739 Clicking --opt option while unread / scrolled above bottom MUST NOT jump to bottom or mark lower unreads as read.
  // Drain any pending async /api/chat/read microtasks from earlier synchronous subtests before resetting state.
  await wait(20);
  assert.ok(cssSrc.includes(".reply-opts.reply-picked") && cssSrc.includes(".reply-picked-tag"), "CSS must style inline settled .reply-picked feedback");
  const makeOptClickTarget = (entryId, optText) => {
    const grp = makeEl("opt-grp-" + entryId);
    grp.dataset.entry = entryId;
    const btn = makeEl("opt-btn-" + entryId);
    btn.dataset.opt = optText;
    grp._qsa = { button: [btn] };
    btn.closest = (sel) => (sel.includes(".reply-opt[data-opt]") ? btn : sel === ".reply-opts[data-entry]" ? grp : null);
    return { btn, grp };
  };

  // 14a) Unread below viewport: clicking --opt on e7 keeps 0px scroll drift, keeps unread cursor/divider/jump badge, shows inline 已选择 feedback,
  // and subsequent SSE delivery + new Collie reply ticks badge 6 -> 7 with 0px drift.
  simOptHeights = true;
  curRowHeight = 140;
  fetchCalls.length = 0;
  serverLastRead = "e4";
  T.resetReadState("", "");
  const optUnreadEntries = manyEntries.map((x) => (
    x.id === "e7" ? { ...x, options: ["按方案 A 推进", "先观望"] } : x
  ));
  T.setSup({
    router: { status: "idle", alive: true },
    entries: optUnreadEntries,
    workers: [],
    pending: [{ id: "e7", at: optUnreadEntries[6].at, summary: "first unread collie reply", options: ["按方案 A 推进", "先观望"] }],
    pinned: [],
    last_read_entry_id: "e4",
  });
  T.renderSuper({ force: true, unreadAnchor: true });
  const scrollBeforeOpt = scroller.scrollTop;
  const distBeforeOpt = scroller.scrollHeight - scroller.scrollTop - scroller.clientHeight;
  assert.ok(distBeforeOpt > 200, "precondition: substantial unread stream sits below viewport, dist=" + distBeforeOpt);
  assert.ok(threadBox.innerHTML.includes('data-opt="按方案 A 推进"'), "e7 renders clickable option buttons");

  const click14a = makeOptClickTarget("e7", "按方案 A 推进");
  threadBox.dispatch("click", { target: click14a.btn });
  await wait(40);

  const sayCalls14a = fetchCalls.filter((c) => c.url === "/api/super/say");
  assert.equal(sayCalls14a.length, 1, "option click posts /api/super/say once");
  assert.equal(JSON.parse(sayCalls14a[0].body).text, "按方案 A 推进");
  assert.equal(JSON.parse(sayCalls14a[0].body).re, "e7");
  assert.equal(scroller.scrollTop, scrollBeforeOpt, "clicking --opt with unreads below must keep 0px scroll drift");
  assert.equal(fetchCalls.filter((c) => c.url === "/api/chat/read").length, 0, "clicking --opt above bottom must NOT POST /api/chat/read");
  assert.equal(serverLastRead, "e4", "server read cursor stays at e4");
  assert.equal(T.getReadState().readCursorId, "e4", "client read cursor stays at e4");
  assert.ok(threadBox.innerHTML.includes('id="unread-sep" data-first-unread="e7"'), "unread divider stays intact above e7");
  assert.equal(getEl("#super-jump").hidden, false, "jump pill stays visible after off-bottom opt click");
  assert.equal(getEl("#super-jump-badge").textContent, "6", "unread count remains 6 until user reads lower messages");
  assert.ok(threadBox.innerHTML.includes('class="reply-opts reply-picked') && threadBox.innerHTML.includes("已选择：") && threadBox.innerHTML.includes("按方案 A 推进"), "e7 shows settled inline 已选择 feedback");
  assert.ok(!threadBox.innerHTML.includes('data-opt="按方案 A 推进"'), "e7 option buttons collapse after selection");

  // SSE delivers persisted user message + new Collie reply below viewport: still 0px scroll drift and badge ticks 6 -> 7.
  const sentId14a = "e" + saySeq;
  const collieFollowId = "e" + (saySeq + 1);
  saySeq += 1;
  const afterSseEntries = [
    ...optUnreadEntries,
    mkEntry(parseInt(sentId14a.slice(1), 10), "user", "按方案 A 推进", { re: { entry: "e7", who: "Collie", text: "first unread collie reply" } }),
    mkEntry(parseInt(collieFollowId.slice(1), 10), "collie", "收到，已按方案 A 启动处理。"),
  ];
  T.setSup({
    router: { status: "idle", alive: true },
    entries: afterSseEntries,
    workers: [],
    pending: [],
    pinned: [],
    last_read_entry_id: "e4",
  });
  T.renderSuper();
  await wait(30);
  assert.equal(scroller.scrollTop, scrollBeforeOpt, "SSE echo + Collie follow-up keep 0px scroll drift");
  assert.equal(serverLastRead, "e4", "SSE echo does not advance read cursor");
  assert.equal(getEl("#super-jump-badge").textContent, "7", "new Collie reply increments unread badge 6 -> 7");
  assert.ok(threadBox.innerHTML.includes("reply-picked") && threadBox.innerHTML.includes("按方案 A 推进"), "inline 已选择 feedback persists cleanly after SSE");

  // Clicking jump button afterwards scrolls to bottom and advances read cursor to the latest reply.
  getEl("#super-jump").dispatch("click");
  await wait(30);
  assert.equal(scroller.scrollTop, scroller.scrollHeight, "jump pill still takes user to bottom on demand");
  assert.equal(serverLastRead, collieFollowId, "jump pill advances read cursor to latest message");

  // 14b) Scrolled up in history with 0 unread (long stream below viewport): clicking --opt keeps 0px drift and does not jump to bottom.
  fetchCalls.length = 0;
  serverLastRead = "e12";
  T.resetReadState("", "");
  T.setSup({
    router: { status: "idle", alive: true },
    entries: optUnreadEntries,
    workers: [],
    pending: [{ id: "e7", at: optUnreadEntries[6].at, summary: "first unread collie reply", options: ["按方案 A 推进", "先观望"] }],
    pinned: [],
    last_read_entry_id: "e12",
  });
  T.renderSuper({ force: true, unreadAnchor: true });
  const e7Node14b = layoutNodes.find((x) => x.id === "e7");
  assert.ok(e7Node14b);
  scroller.scrollTop = Math.max(0, e7Node14b.y - 80);
  const scroll14b = scroller.scrollTop;
  assert.ok(scroller.scrollHeight - scroller.scrollTop - scroller.clientHeight > 200, "precondition: scrolled up far above bottom");
  const click14b = makeOptClickTarget("e7", "先观望");
  threadBox.dispatch("click", { target: click14b.btn });
  await wait(40);
  assert.equal(scroller.scrollTop, scroll14b, "0-unread scrolled-up history opt click keeps 0px drift");
  assert.equal(fetchCalls.filter((c) => c.url === "/api/chat/read").length, 0, "0-unread scrolled-up opt click does not advance read cursor");
  assert.ok(threadBox.innerHTML.includes("先观望") && threadBox.innerHTML.includes("reply-picked"), "0-unread scrolled-up opt click shows inline 已选择 feedback");

  // 14c) Partially above-top clicked message (e7.top < -4) anchors to e7 itself (0px drift even as e7 height shrinks by 20px!).
  fetchCalls.length = 0;
  serverLastRead = "e4";
  T.resetReadState("", "");
  T.setSup({
    router: { status: "idle", alive: true },
    entries: optUnreadEntries,
    workers: [],
    pending: [{ id: "e7", at: optUnreadEntries[6].at, summary: "first unread collie reply", options: ["按方案 A 推进", "先观望"] }],
    pinned: [],
    last_read_entry_id: "e4",
  });
  T.renderSuper({ force: true, unreadAnchor: true });
  const e7Node14c = layoutNodes.find((x) => x.id === "e7");
  assert.ok(e7Node14c);
  // Scroll so e7's top is 40px ABOVE viewport top while its bottom/options are visible inside viewport.
  scroller.scrollTop = e7Node14c.y + 40;
  const scroll14c = scroller.scrollTop;
  const click14c = makeOptClickTarget("e7", "按方案 A 推进");
  threadBox.dispatch("click", { target: click14c.btn });
  await wait(40);
  assert.equal(scroller.scrollTop, scroll14c, "partially above-top clicked option message keeps exact 0px scrollTop drift despite height collapse");

  // 14d) /api/super/say failure rolls back .reply-picked, restores clickable buttons, and keeps 0px scroll drift.
  fetchCalls.length = 0;
  sayFail = true;
  serverLastRead = "e4";
  T.resetReadState("", "");
  T.setSup({
    router: { status: "idle", alive: true },
    entries: optUnreadEntries,
    workers: [],
    pending: [{ id: "e7", at: optUnreadEntries[6].at, summary: "first unread collie reply", options: ["按方案 A 推进", "先观望"] }],
    pinned: [],
    last_read_entry_id: "e4",
  });
  T.renderSuper({ force: true, unreadAnchor: true });
  const scroll14d = scroller.scrollTop;
  const click14d = makeOptClickTarget("e7", "按方案 A 推进");
  threadBox.dispatch("click", { target: click14d.btn });
  await wait(40);
  sayFail = false;
  assert.equal(scroller.scrollTop, scroll14d, "failed off-bottom opt click keeps 0px scroll drift");
  assert.ok(threadBox.innerHTML.includes('data-opt="按方案 A 推进"'), "failed opt click restores clickable buttons");
  assert.ok(!threadBox.innerHTML.includes("reply-picked"), "failed opt click clears inline 已选择 feedback");

  // 14e) Already at bottom (atBottom): clicking --opt sticks naturally to bottom, advances read cursor, and omits redundant .reply-picked badge.
  fetchCalls.length = 0;
  serverLastRead = "e11";
  T.resetReadState("", "");
  const botOptEntries = manyEntries.map((x) => (
    x.id === "e12" ? mkEntry(12, "collie", "要立刻发布吗？", { options: ["立刻发布", "稍后再发"] }) : x
  ));
  T.setSup({
    router: { status: "idle", alive: true },
    entries: botOptEntries,
    workers: [],
    pending: [{ id: "e12", at: botOptEntries[11].at, summary: "要立刻发布吗？", options: ["立刻发布", "稍后再发"] }],
    pinned: [],
    last_read_entry_id: "e11",
  });
  T.renderSuper({ force: true, unreadAnchor: true });
  scroller.scrollTop = scroller.scrollHeight - scroller.clientHeight;
  const click14e = makeOptClickTarget("e12", "立刻发布");
  threadBox.dispatch("click", { target: click14e.btn });
  await wait(40);
  const expectedBotRead = "e" + saySeq;
  assert.equal(scroller.scrollTop, scroller.scrollHeight, "at-bottom opt click sticks smoothly to bottom");
  assert.equal(serverLastRead, expectedBotRead, "at-bottom opt click advances read cursor to sent message");
  assert.ok(!threadBox.innerHTML.includes("reply-picked"), "at-bottom opt click omits duplicate inline 已选择 tag");

  // 14f) Inside 1v1 topic focus drawer (#td-list) while main chat has unreads below: drawer sticks to bottom,
  // main chat stays anchored at 0px drift and unread cursor stays untouched.
  fetchCalls.length = 0;
  serverLastRead = "e4";
  T.resetReadState("", "");
  T.setSup({
    router: { status: "idle", alive: true },
    entries: optUnreadEntries,
    workers: [],
    pending: [{ id: "e7", at: optUnreadEntries[6].at, summary: "first unread collie reply", options: ["按方案 A 推进", "先观望"] }],
    pinned: [],
    last_read_entry_id: "e4",
  });
  T.renderSuper({ force: true, unreadAnchor: true });
  const mainScrollBeforeDrawerOpt = scroller.scrollTop;
  T.openThreadDrawer("e7", "e7", "e7");
  const tdBody = getEl("#td-body");
  tdBody.clientHeight = 400;
  tdBody.scrollHeight = 900;
  tdBody.scrollTop = 100;
  const click14f = makeOptClickTarget("e7", "按方案 A 推进");
  getEl("#td-list").dispatch("click", { target: click14f.btn });
  await wait(40);
  assert.equal(tdBody.scrollTop, tdBody.scrollHeight, "clicking --opt inside focus drawer sticks drawer to bottom");
  assert.equal(scroller.scrollTop, mainScrollBeforeDrawerOpt, "clicking --opt inside focus drawer keeps main stream anchored at 0px drift");
  assert.equal(serverLastRead, "e4", "clicking --opt inside focus drawer does not advance main unread cursor");
  assert.ok(!getEl("#td-list").innerHTML.includes("reply-picked"), "focus drawer omits duplicate reply-picked pill above live reply");
  T.closeThreadDrawer({ immediate: true });

  // 14g) Quoting an older message via bottom composer still scrolls to bottom as expected when user types & sends.
  fetchCalls.length = 0;
  serverLastRead = "e4";
  T.resetReadState("", "");
  T.setSup({
    router: { status: "idle", alive: true },
    entries: optUnreadEntries,
    workers: [],
    pending: [],
    pinned: [],
    last_read_entry_id: "e4",
  });
  T.renderSuper({ force: true, unreadAnchor: true });
  await T.sendSuper("我补充一句自定义意见", { entry: "e7", who: "Collie", text: "first unread collie reply" }, true);
  assert.equal(scroller.scrollTop, scroller.scrollHeight, "composer send with quote still scrolls to bottom");
  assert.equal(serverLastRead, "e" + saySeq, "composer send advances read cursor");
  simOptHeights = false;

  console.log("OK: all unread anchor & chat read JS regression checks passed");
})().catch((err) => {
  console.error(err);
  process.exit(1);
});
`
	cmd := exec.Command(nodeBin, "-e", jsTest, root)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("node unread anchor test failed: %v\n%s", err, string(out))
	}
	if !strings.Contains(string(out), "OK: all unread anchor & chat read JS regression checks passed") {
		t.Fatalf("unexpected node output:\n%s", string(out))
	}
}
