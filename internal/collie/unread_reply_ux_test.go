package collie

import (
	"encoding/json"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestSuperSayInPlacePersistenceAndHybridThread(t *testing.T) {
	t.Parallel()
	sp, db, call := newPendingTestEnv(t)

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

	// 1) Collie emits a decision card (e1) and a plain non-decision message (e2).
	recOpt := call("POST", "/api/super/reply", map[string]any{
		"text":    "PR #142 全量测试通过，直接合入还是补用例？",
		"options": []string{"直接合入", "先补一条用例"},
	})
	if recOpt.Code != 200 {
		t.Fatalf("reply opt status=%d body=%s", recOpt.Code, recOpt.Body.String())
	}
	var outOpt struct {
		Entry string `json:"entry"`
	}
	_ = json.Unmarshal(recOpt.Body.Bytes(), &outOpt)
	optID := outOpt.Entry

	recPlain := call("POST", "/api/super/reply", map[string]any{
		"text": "首页演示动图体积压到了 640 KB，首屏加载快了 1.5 秒。",
	})
	if recPlain.Code != 200 {
		t.Fatalf("reply plain status=%d body=%s", recPlain.Code, recPlain.Body.String())
	}
	var outPlain struct {
		Entry string `json:"entry"`
	}
	_ = json.Unmarshal(recPlain.Body.Bytes(), &outPlain)
	plainID := outPlain.Entry

	// 2) In-place custom supplement on decision card while reading above bottom.
	recInPlace := call("POST", "/api/super/say", map[string]any{
		"text":          "先合入主分支，顺便把快捷键说明写进变更记录",
		"re":            optID,
		"in_place":      true,
		"client_msg_id": "inplace-cmsg-1",
	})
	if recInPlace.Code != 200 {
		t.Fatalf("in_place say status=%d body=%s", recInPlace.Code, recInPlace.Body.String())
	}
	var res1 map[string]any
	_ = json.Unmarshal(recInPlace.Body.Bytes(), &res1)
	if res1["in_place"] != true {
		t.Fatalf("expected in_place=true in response, got %+v", res1)
	}
	userEntryID, _ := res1["entry"].(string)
	trigID, _ := res1["trigger"].(string)
	if userEntryID == "" || userEntryID == optID || trigID == "" {
		t.Fatalf("expected new user entry id & trigger id, got %+v", res1)
	}

	// Verify entry has InPlace=true, decision card Pick has State="custom"+InPlace=true, and Pending is resolved.
	snap := getSnap()
	if len(snap.Pending) != 0 {
		t.Fatalf("expected pending resolved after in-place supplement, got %+v", snap.Pending)
	}
	var cardEntry, userEntry *Entry
	for _, e := range snap.Entries {
		if e.ID == optID {
			cardEntry = e
		}
		if e.ID == userEntryID {
			userEntry = e
		}
	}
	if cardEntry == nil || cardEntry.Pick == nil || cardEntry.Pick.State != "custom" || !cardEntry.Pick.InPlace || cardEntry.Pick.Text != "先合入主分支，顺便把快捷键说明写进变更记录" {
		t.Fatalf("unexpected card Pick after in_place supplement: %+v", cardEntry)
	}
	if userEntry == nil || userEntry.Role != "user" || !userEntry.InPlace || userEntry.Re == nil || userEntry.Re.Entry != optID {
		t.Fatalf("unexpected user in_place entry: %+v", userEntry)
	}

	// 3) Duplicate retry with same client_msg_id must dedup and keep in_place=true.
	recDup := call("POST", "/api/super/say", map[string]any{
		"text":          "先合入主分支，顺便把快捷键说明写进变更记录",
		"re":            optID,
		"in_place":      true,
		"client_msg_id": "inplace-cmsg-1",
	})
	if recDup.Code != 200 {
		t.Fatalf("dup in_place status=%d", recDup.Code)
	}
	var resDup map[string]any
	_ = json.Unmarshal(recDup.Body.Bytes(), &resDup)
	if resDup["duplicate"] != true || resDup["in_place"] != true || resDup["entry"] != userEntryID {
		t.Fatalf("unexpected dup response: %+v", resDup)
	}

	// 4) Collie follow-up reply to that trigger enters main timeline (InPlace=false) AND chains via Re.Entry=userEntryID.
	recFollow := call("POST", "/api/super/reply", map[string]any{
		"text": "收到，已经按要求合入并补好了变更记录。",
		"re":   trigID,
	})
	if recFollow.Code != 200 {
		t.Fatalf("collie follow-up status=%d body=%s", recFollow.Code, recFollow.Body.String())
	}
	var outFollow struct {
		Entry string `json:"entry"`
	}
	_ = json.Unmarshal(recFollow.Body.Bytes(), &outFollow)

	snap = getSnap()
	var followEntry *Entry
	for _, e := range snap.Entries {
		if e.ID == outFollow.Entry {
			followEntry = e
		}
	}
	if followEntry == nil || followEntry.InPlace {
		t.Fatalf("collie follow-up must remain visible in main timeline (InPlace=false): %+v", followEntry)
	}
	if followEntry.Re == nil || followEntry.Re.Entry != userEntryID || followEntry.Re.Who != "你" {
		t.Fatalf("collie follow-up must chain back to user in_place entry %s, got %+v", userEntryID, followEntry.Re)
	}

	// 5) In-place direct quote on a non-decision message persists cleanly.
	recQuote := call("POST", "/api/super/say", map[string]any{
		"text":     "顺便检查一下暗色模式下的封面图对比度",
		"re":       plainID,
		"in_place": true,
	})
	if recQuote.Code != 200 {
		t.Fatalf("in_place quote status=%d body=%s", recQuote.Code, recQuote.Body.String())
	}
	var resQuote map[string]any
	_ = json.Unmarshal(recQuote.Body.Bytes(), &resQuote)
	if resQuote["in_place"] != true {
		t.Fatalf("expected in_place=true for quote on plain message, got %+v", resQuote)
	}

	// 6) Standalone message without `re` must safely ignore `in_place: true` so it is never orphaned.
	recStandalone := call("POST", "/api/super/say", map[string]any{
		"text":     "这是一条不带引用的普通消息",
		"in_place": true,
	})
	if recStandalone.Code != 200 {
		t.Fatalf("standalone say status=%d", recStandalone.Code)
	}
	var resStand map[string]any
	_ = json.Unmarshal(recStandalone.Body.Bytes(), &resStand)
	if resStand["in_place"] == true {
		t.Fatalf("standalone message without re must not set in_place=true: %+v", resStand)
	}

	// 7) Persist and reload from SQLite: InPlace on Entry and DecisionPick survive restart.
	sp.flush()
	reloaded, err := loadState(db, sp.dir)
	if err != nil {
		t.Fatalf("loadState: %v", err)
	}
	var foundOpt, foundUser, foundStandalone bool
	for _, e := range reloaded.Entries {
		if e.ID == optID && e.Pick != nil && e.Pick.InPlace {
			foundOpt = true
		}
		if e.ID == userEntryID && e.InPlace && e.Re != nil && e.Re.Entry == optID {
			foundUser = true
		}
		if e.ID == resStand["entry"] && !e.InPlace {
			foundStandalone = true
		}
	}
	if !foundOpt || !foundUser || !foundStandalone {
		t.Fatalf("SQLite reload mismatch: foundOpt=%v foundUser=%v foundStandalone=%v", foundOpt, foundUser, foundStandalone)
	}
}

func TestUnreadReplyUXStateMachineAndPlanABJS(t *testing.T) {
	t.Parallel()
	nodeBin, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node not installed")
	}
	root := filepath.Join("..", "..")

	jsTest := `
const assert = require("node:assert/strict");
const fs = require("node:fs");
const path = require("node:path");
const vm = require("node:vm");

const root = process.argv[1];
const appSrc = fs.readFileSync(path.join(root, "web", "app.js"), "utf8");
const cssSrc = fs.readFileSync(path.join(root, "web", "app.css"), "utf8");
const htmlSrc = fs.readFileSync(path.join(root, "web", "index.html"), "utf8");

// 1) CSS & restrained copy checks (zero prototype jargon; modern field-sizing + fallback).
assert.ok(cssSrc.includes(".super-jump.stage-unread"), "CSS must style stage 1 compact unread jump pill");
assert.ok(cssSrc.includes("field-sizing: content") && cssSrc.includes("@supports not (field-sizing: content)"), "CSS must provide field-sizing: content + Grid fallback");
assert.ok(cssSrc.includes(".reply-inline-send.send") && cssSrc.includes(".inplace-receipt"), "CSS must style circular inline send button & minimal 1-line in-place receipt");
for (const banned of ["原地收口 · 不刷主底", "留在原地记入话题线", "跳到最新 · 余", "落主底 · 0px"]) {
  assert.ok(!appSrc.includes(banned) && !htmlSrc.includes(banned), "UI copy must remain restrained without prototype jargon: " + banned);
}

// 2) Sandboxed DOM harness supporting viewport switching (desktop >760px vs mobile <=760px),
// layout geometry for #unread-sep and message rows, and form/IME events.
let isNarrow = false;
let supportsFieldSizing = true;
const narrowListeners = [];
let nowMs = 10000;
let curRowHeight = 140;
let layoutNodes = [];
let saySeq = 500;
let serverLastRead = "e4";
const fetchCalls = [];
const storage = new Map();

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
    id, tagName: "DIV", value: "", textContent: "", _innerHTML: "",
    hidden: false, disabled: false, checked: false, open: false,
    className: "", title: "", style: { setProperty() {}, removeProperty() {} },
    dataset: {}, classList: makeClassList(), children: [], selectedOptions: [], options: [],
    scrollTop: 0, scrollHeight: 200, clientHeight: 600, clientWidth: 1000,
    offsetWidth: 400, offsetHeight: 98, offsetParent: {}, isConnected: true,
    _qs: {}, _qsa: {}, focused: false,
    get innerHTML() { return el._innerHTML; },
    set innerHTML(v) { el._innerHTML = String(v || ""); },
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
    append() {}, remove() {}, after() {},
    focus() { el.focused = true; doc.activeElement = el; },
    blur() { el.focused = false; },
    setSelectionRange() {},
    click() { el.dispatch("click"); },
    scrollIntoView() {},
    scrollTo(opts) {
      if (typeof opts === "object" && typeof opts.top === "number") el.scrollTop = opts.top;
      return Promise.resolve();
    },
    requestSubmit() { el.dispatch("submit"); },
    getBoundingClientRect() { return { top: 0, bottom: el.clientHeight || 600, left: 0, right: 800, width: 800, height: el.clientHeight || 600 }; },
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

const threadBox = getEl("#super-thread");
const scroller = threadBox.parentElement;
scroller.clientHeight = 600;
scroller.clientWidth = 1000;
scroller.getBoundingClientRect = () => ({ top: 0, bottom: scroller.clientHeight, left: 0, right: scroller.clientWidth, width: scroller.clientWidth, height: scroller.clientHeight });

function rebuildLayout(html) {
  layoutNodes = [];
  let y = 20;
  const re = /id="unread-sep"|data-id="(e\d+|_p\d+)"/g;
  let m;
  while ((m = re.exec(html)) !== null) {
    if (m[0] === 'id="unread-sep"') {
      const node = makeEl("unread-sep");
      node.isSep = true;
      node.y = y;
      node.h = 28;
      node.getBoundingClientRect = () => ({ top: node.y - scroller.scrollTop, bottom: node.y + node.h - scroller.scrollTop, height: node.h, left: 0, right: 700, width: 700 });
      layoutNodes.push(node);
      y += node.h + 12;
    } else {
      const id = m[1];
      const node = makeEl("row:" + id);
      node.id = id;
      node.dataset.id = id;
      node.y = y;
      node.h = curRowHeight;
      node.getBoundingClientRect = () => ({ top: node.y - scroller.scrollTop, bottom: node.y + node.h - scroller.scrollTop, height: node.h, left: 0, right: 700, width: 700 });
      node.closest = () => node;
      layoutNodes.push(node);
      y += node.h + 16;
    }
  }
  scroller.scrollHeight = Math.max(scroller.clientHeight, y + 40);
}
Object.defineProperty(threadBox, "innerHTML", {
  get() { return threadBox._innerHTML; },
  set(v) { threadBox._innerHTML = String(v || ""); rebuildLayout(threadBox._innerHTML); },
});
threadBox.querySelector = (sel) => {
  if (sel === "#unread-sep") return layoutNodes.find((x) => x.isSep) || null;
  const dm = sel.match(/data-id="([^"]+)"/);
  if (dm) return layoutNodes.find((x) => !x.isSep && x.id === dm[1]) || null;
  return null;
};
threadBox.querySelectorAll = (sel) => {
  if (sel.includes(".msg[data-id]")) return layoutNodes.filter((x) => !x.isSep);
  return [];
};

for (const [formSel, inputSel] of [["#super-composer", "#super-input"], ["#composer", "#input"], ["#x-composer", "#x-input"], ["#td-composer", "#td-input"]]) {
  const f = getEl(formSel);
  f._qs["textarea"] = getEl(inputSel);
  f._qs[".send"] = formSel === "#td-composer" ? getEl("#td-send") : makeEl(formSel + " .send");
  f._qs[".files"] = makeEl(formSel + " .files");
  f._qs[".attach"] = makeEl(formSel + " .attach");
  f._qs['input[type="file"]'] = makeEl(formSel + " input[type=file]");
}
const reDraft = getEl("#re-draft");
reDraft.hidden = true;
reDraft._qs[".re-who"] = makeEl("#re-draft .re-who");
reDraft._qs[".re-t"] = makeEl("#re-draft .re-t");
reDraft._qs["button"] = makeEl("#re-draft button");
const superJump = getEl("#super-jump");
const superJumpBadge = getEl("#super-jump-badge");
superJump._qs["#super-jump-badge"] = superJumpBadge;
superJump.hidden = true;
superJumpBadge.hidden = true;

const docListeners = new Map();
const doc = {
  documentElement: makeEl("html"),
  body: makeEl("body"),
  activeElement: null,
  hidden: false,
  hasFocus: () => true,
  createElement: (tag) => makeEl(tag),
  getElementById: (id) => getEl("#" + id),
  querySelector: (sel) => {
    if (sel === "dialog[open]") return null;
    if (sel.includes('data-id="')) {
      const m = sel.match(/data-id="([^"]+)"/);
      if (m) return layoutNodes.find((x) => !x.isSep && x.id === m[1]) || null;
    }
    return getEl(sel);
  },
  querySelectorAll: () => [],
  addEventListener(type, fn) { if (!docListeners.has(type)) docListeners.set(type, []); docListeners.get(type).push(fn); },
  removeEventListener(type, fn) { const l = docListeners.get(type); if (!l) return; const i = l.indexOf(fn); if (i >= 0) l.splice(i, 1); },
  dispatchDoc(type, ev = {}) {
    const e = { type, defaultPrevented: false, preventDefault() { this.defaultPrevented = true; }, stopPropagation() {}, composedPath: () => [ev.target || doc.body], ...ev };
    for (const fn of [...(docListeners.get(type) || [])]) fn(e);
    return e;
  },
};

const ctx = {
  console, Math, Date, JSON, Set, Map, RegExp, Error, Promise, URL, URLSearchParams, Object, Array, Number, String, Boolean,
  setTimeout: (fn, ms) => setTimeout(fn, ms >= 500 ? 120 : Math.min(ms || 0, 20)),
  clearTimeout, setInterval: () => 1, clearInterval() {}, queueMicrotask,
  requestAnimationFrame: (fn) => { fn(nowMs); return 1; },
  cancelAnimationFrame() {},
  document: doc,
  location: { hash: "#/collie", origin: "http://127.0.0.1:7788", href: "http://127.0.0.1:7788/#/collie", pathname: "/", search: "" },
  history: { state: null, pushState() {}, replaceState() {}, back() {} },
  navigator: { platform: "MacIntel", clipboard: { writeText: async () => {} } },
  isSecureContext: true, innerWidth: 1440, innerHeight: 900,
  visualViewport: { height: 900, offsetTop: 0, addEventListener() {} },
  CSS: { escape: (s) => String(s), supports: (prop, val) => (prop === "field-sizing" ? supportsFieldSizing : true) },
  HTMLDialogElement: { prototype: {} },
  Blob, Uint8Array, TextEncoder, atob, AbortController,
  crypto: { randomUUID: () => "ux-" + (++saySeq) },
  performance: { now: () => nowMs },
  Event: class Event { constructor(t) { this.type = t; } },
  CustomEvent: class CustomEvent { constructor(t, d) { this.type = t; this.detail = d?.detail; } },
  ResizeObserver: class { observe() {} disconnect() {} },
  MutationObserver: class { observe() {} disconnect() {} },
  IntersectionObserver: class { observe() {} disconnect() {} },
  EventSource: class { addEventListener() {} close() {} },
  Notification: class { static permission = "default"; static async requestPermission() { return "default"; } },
  localStorage: { getItem: (k) => (storage.has(k) ? storage.get(k) : null), setItem: (k, v) => storage.set(k, String(v)), removeItem: (k) => storage.delete(k) },
  matchMedia: (q) => ({
    get matches() { return String(q).includes("760px") ? isNarrow : false; },
    addEventListener(t, fn) { if (String(q).includes("760px") && t === "change") narrowListeners.push(fn); },
  }),
  getComputedStyle: () => ({ getPropertyValue: () => "" }),
  addEventListener() {}, removeEventListener() {}, dispatchEvent() {},
  fetch: async (url, init = {}) => {
    const body = init.body ? JSON.parse(init.body) : null;
    fetchCalls.push({ url: String(url), method: init.method || "GET", body });
    if (String(url).includes("/api/chat/read")) {
      if (body?.entry_id) serverLastRead = body.entry_id;
      return { ok: true, status: 200, json: async () => ({ ok: true, last_read_entry_id: serverLastRead }) };
    }
    if (String(url).includes("/api/super/say")) {
      const id = body?.pick ? body.re : "e" + (++saySeq);
      return { ok: true, status: 200, json: async () => ({ ok: true, entry: id, trigger: "t" + saySeq, ...(body?.pick ? { picked: true } : {}), ...(body?.in_place ? { in_place: true } : {}) }) };
    }
    return { ok: true, status: 200, json: async () => ({ ok: true }) };
  },
};
ctx.window = ctx;
ctx.globalThis = ctx;

const marker = "  boot();\n})();";
const instrumented = appSrc.replace(
  marker,
  "  globalThis.__test = { getSup: () => sup, setSup: (s) => { sup = normSup(s); }, setView: (v) => { view = v; }, setBooted: (b) => { booted = !!b; }, renderSuper, computeUnreadInfo, computeUnreadJumpTarget, computeReplyThreads, updateSuperJump, maybeAdvanceReadByScroll, replyOptsMarkup, handleQuickOptionClick, submitInlineDecision, setReply, getReplyTo: () => replyTo, getOpenInline: () => openInlineDecisionId, getInlineDraft, openThreadDrawer, closeThreadDrawer, resolveVisibleEntryId, clearProgScroll: () => { programmaticScrollUntil = 0; }, resetState: (srv = '') => { older = []; sup = { router: { status: 'idle', alive: true }, entries: [], workers: [], pending: [], pinned: [] }; pendingSays.length = 0; dismissedLocal.clear(); pickedReplyOpts.clear(); inlineDecisionDrafts.clear(); openInlineDecisionId = null; setReply(null); readCursorId = ''; serverReadCursorId = srv; sessionUnreadMarkerId = ''; unreadAnchorApplied = false; activeCollieRestore = null; cancelInitialAnchor(); clearScrollReadTimer(); cancelPendingChatRead(); localStorage.removeItem('collie.chatReadEntryId'); } };\n})();"
);
vm.runInNewContext(instrumented, ctx, { filename: "web/app.js" });
const T = ctx.__test;
const wait = (ms) => new Promise((r) => setTimeout(r, ms));

const mkEntry = (num, role, text, extra = {}) => ({
  id: "e" + num,
  role,
  thread: "chat:main",
  text,
  at: new Date(Date.UTC(2026, 10, 28, 1, num, 0)).toISOString(),
  ...extra,
});

(async () => {
  T.setBooted(true);
  T.setView("collie");

  const baseEntries = [
    mkEntry(1, "user", "早，帮忙跑一遍回归测试"),
    mkEntry(2, "collie", "收到，已经分头处理。"),
    mkEntry(3, "user", "顺便查一下安装脚本"),
    mkEntry(4, "collie", "已加入队列。"),
    mkEntry(5, "collie", "1/6 PR #142 全量测试通过", { options: ["直接合入", "先补用例"] }),
    mkEntry(6, "collie", "2/6 首页演示动图压缩完成"),
    mkEntry(7, "collie", "3/6 v0.4.0 发版说明已起草", { options: ["按推荐稿走", "稍后细看"] }),
    mkEntry(8, "collie", "4/6 安装脚本边缘报错修复完毕"),
    mkEntry(9, "collie", "5/6 移动端滚动排查完成"),
    mkEntry(10, "collie", "6/6 晨间汇总全部绿灯"),
  ];

  // ─── A. Two-Stage #super-jump State Machine ───
  serverLastRead = "e4";
  T.resetState("e4");
  T.setSup({
    router: { status: "idle", alive: true },
    entries: baseEntries,
    workers: [],
    pending: [
      { id: "e5", summary: "1/6 PR #142", options: ["直接合入", "先补用例"] },
      { id: "e7", summary: "3/6 v0.4.0", options: ["按推荐稿走", "稍后细看"] },
    ],
    pinned: [],
    last_read_entry_id: "e4",
  });
  T.renderSuper({ force: true, unreadAnchor: true });

  // Scroll above #unread-sep -> Stage 1 ("↓ 6 未读")
  scroller.scrollTop = 0;
  T.updateSuperJump();
  assert.equal(superJump.hidden, false);
  assert.equal(superJump.dataset.stage, "unread-start", "above divider must enter stage 1");
  assert.equal(superJump.classList.contains("stage-unread"), true);
  assert.equal(superJumpBadge.hidden, false);
  assert.equal(superJumpBadge.textContent, "6 未读");

  // Click Stage 1 -> jumps to #unread-sep (~14px top pad), unread untouched, flips to Stage 2 pure circle
  fetchCalls.length = 0;
  superJump.dispatch("click");
  await wait(30);
  const sep = layoutNodes.find((n) => n.isSep);
  assert.ok(Math.abs(sep.y - scroller.scrollTop - 14) <= 2, "stage 1 click lands ~14px above #unread-sep");
  assert.equal(fetchCalls.filter((c) => c.url.includes("/api/chat/read")).length, 0, "stage 1 must NOT mark read");
  assert.equal(T.computeUnreadInfo().count, 6, "stage 1 preserves all 6 unread");
  assert.equal(sep.classList.contains("is-pulse"), true, "stage 1 arrival gently pulses divider");
  assert.equal(superJump.dataset.stage, "latest", "landing at divider flips to stage 2");
  assert.equal(superJump.classList.contains("stage-unread"), false);
  assert.equal(superJumpBadge.hidden, true, "stage 2 returns to minimal pure circular arrow");

  // Scroll partially through e5..e6 so cursor advances to e6 (4 left), then scroll back up above e7 ->
  // Stage 1 must target e7 (remaining first unread) and NEVER jump backwards to already-read #unread-sep!
  nowMs += 2000;
  T.clearProgScroll();
  const e7Node = layoutNodes.find((n) => n.id === "e7");
  scroller.scrollTop = e7Node.y;
  T.maybeAdvanceReadByScroll();
  await wait(40);
  assert.equal(T.computeUnreadInfo().firstUnreadId, "e7", "read cursor advanced past e5..e6");
  assert.equal(T.computeUnreadInfo().count, 4, "4 unread remain");
  scroller.scrollTop = 0;
  T.updateSuperJump();
  assert.equal(superJump.dataset.stage, "unread-start");
  assert.equal(superJumpBadge.textContent, "4 未读");
  superJump.dispatch("click");
  await wait(30);
  assert.ok(Math.abs(e7Node.y - scroller.scrollTop - 14) <= 2, "stage 1 jumps to current first unread e7, never backwards");

  // Click Stage 2 -> lands at bottom and immediately commits read
  superJump.dispatch("click");
  await wait(30);
  assert.equal(scroller.scrollTop, scroller.scrollHeight, "stage 2 jumps to bottom");
  assert.equal(serverLastRead, "e10", "stage 2 syncs read cursor to latest");
  assert.equal(superJump.hidden, true, "jump button hides at bottom");

  // Single-screen unread boundary: fitsAtBottom=true skips Stage 1 and goes straight to Stage 2
  curRowHeight = 90;
  serverLastRead = "e9";
  T.resetState("e9");
  T.setSup({ router: { status: "idle", alive: true }, entries: baseEntries, workers: [], pending: [], pinned: [], last_read_entry_id: "e9" });
  T.renderSuper({ force: true, unreadAnchor: true });
  scroller.scrollTop = 0;
  T.updateSuperJump();
  assert.equal(T.computeUnreadJumpTarget().fitsAtBottom, true, "1 unread fits on single screen at bottom");
  assert.equal(superJump.dataset.stage, "latest", "single-screen unread skips stage 1 to avoid empty 2-step click");
  assert.equal(superJumpBadge.hidden, true);
  curRowHeight = 140;

  // ─── B. Earlier-Message In-Place Settling · Asymmetric Hybrid & 0px Drift ───
  fetchCalls.length = 0;
  serverLastRead = "e4";
  T.resetState("e4");
  T.setSup({
    router: { status: "idle", alive: true },
    entries: baseEntries,
    workers: [],
    pending: [
      { id: "e5", summary: "1/6 PR #142", options: ["直接合入", "先补用例"] },
      { id: "e7", summary: "3/6 v0.4.0", options: ["按推荐稿走", "稍后细看"] },
    ],
    pinned: [],
    last_read_entry_id: "e4",
  });
  T.renderSuper({ force: true, unreadAnchor: true });
  const beforeScroll = scroller.scrollTop;
  const mineCountBefore = (threadBox.innerHTML.match(/class="mine/g) || []).length;

  // B1) Non-bottom custom supplement on decision card e5 -> in_place=true, 0px drift, 0 stray bottom bubble, ↳ 1 条回复
  await T.submitInlineDecision("e5", "先合入主分支，顺便更新说明");
  const sayInPlaceReq = fetchCalls.find((c) => c.url.includes("/api/super/say") && c.body?.re === "e5");
  assert.ok(sayInPlaceReq, "must POST /api/super/say for e5 supplement");
  assert.equal(sayInPlaceReq.body.in_place, true, "non-bottom supplement must send in_place: true");
  assert.equal(scroller.scrollTop, beforeScroll, "non-bottom supplement must keep strictly 0px viewport drift");
  assert.equal(fetchCalls.filter((c) => c.url.includes("/api/chat/read")).length, 0, "must preserve unread cursor");
  const mineCountAfter = (threadBox.innerHTML.match(/class="mine/g) || []).length;
  assert.equal(mineCountAfter, mineCountBefore, "in-place supplement must emit ZERO stray .mine bubbles at main bottom");
  assert.ok(threadBox.innerHTML.includes("先合入主分支，顺便更新说明"), "in-place receipt shows concise summary right under e5");
  assert.ok(threadBox.innerHTML.includes("1 条回复"), "in-place reply immediately surfaces ↳ 1 条回复 thread pill on e5");

  // B2) Non-bottom direct quote on non-decision message e6 via #super-composer -> in_place=true, 0px drift, .inplace-receipt
  fetchCalls.length = 0;
  T.setReply(baseEntries[5], { mode: "quote", focus: false });
  getEl("#super-input").value = "帮我顺便核对暗色模式首屏图";
  getEl("#super-composer").requestSubmit();
  await wait(30);
  const quoteReq = fetchCalls.find((c) => c.url.includes("/api/super/say") && c.body?.re === "e6");
  assert.ok(quoteReq, "must POST /api/super/say for e6 quote");
  assert.equal(quoteReq.body.in_place, true, "non-bottom quote reply must send in_place: true");
  assert.equal(scroller.scrollTop, beforeScroll, "non-bottom quote reply must keep strictly 0px viewport drift");
  assert.equal((threadBox.innerHTML.match(/class="mine/g) || []).length, mineCountBefore, "non-bottom quote must emit ZERO stray .mine bubbles");
  assert.ok(threadBox.innerHTML.includes('class="inplace-receipt') && threadBox.innerHTML.includes("帮我顺便核对暗色模式首屏图"), "non-decision target renders minimal 1-line ✓ receipt");

  // B3) Collie follow-up reply quoting the in-place entry enters main timeline visibly AND chains to ↳ 2 条回复!
  const withHybrid = [
    ...baseEntries,
    mkEntry(11, "user", "帮我顺便核对暗色模式首屏图", { re: { entry: "e6", who: "Collie", text: baseEntries[5].text }, in_place: true }),
    mkEntry(12, "collie", "已核对暗色首屏图，对比度正常。", { re: { entry: "e11", who: "你", text: "帮我顺便核对暗色模式首屏图" } }),
  ];
  const thInfo = T.computeReplyThreads(withHybrid);
  const e6Thread = thInfo.threads.get("e6");
  assert.ok(e6Thread && e6Thread.replyCount === 2, "Collie follow-up chains with in-place user reply into 2-reply thread on e6");
  assert.equal(T.resolveVisibleEntryId("e11", withHybrid), "e6", "locating hidden in-place e11 resolves to visible parent e6");

  // B4) At-bottom send exemption: when user IS near bottom, quote reply emits normal user bubble & scrolls to bottom!
  fetchCalls.length = 0;
  scroller.scrollTop = scroller.scrollHeight - scroller.clientHeight;
  T.setReply(baseEntries[9], { mode: "quote", focus: false });
  getEl("#super-input").value = "收到，在底部直接追问一句";
  getEl("#super-composer").requestSubmit();
  await wait(30);
  const bottomReq = fetchCalls.find((c) => c.url.includes("/api/super/say") && c.body?.re === "e10");
  assert.ok(bottomReq && !bottomReq.body.in_place, "at-bottom quote reply must NOT set in_place");
  assert.ok((threadBox.innerHTML.match(/class="mine/g) || []).length > mineCountBefore, "at-bottom quote reply renders normal bottom user bubble");

  // ─── C. Desktop Plan A (>760px) vs Mobile Plan B (<=760px) + IME Safety ───
  isNarrow = false;
  supportsFieldSizing = false; // also verify @supports not fallback data-replicated-value!
  fetchCalls.length = 0;
  serverLastRead = "e4";
  T.resetState("e4");
  T.setSup({
    router: { status: "idle", alive: true },
    entries: baseEntries,
    workers: [],
    pending: [{ id: "e7", summary: "3/6 v0.4.0", options: ["按推荐稿走", "稍后细看"] }],
    pinned: [],
    last_read_entry_id: "e4",
  });
  T.renderSuper({ force: true, unreadAnchor: true });

  // C1) Desktop Plan A: click 补充一句 opens inline <form class="reply-inline"> with minimal placeholder & circular send button
  const togBtn = makeEl("tog-e7");
  togBtn.dataset.toggleInline = "e7";
  togBtn.closest = (sel) => (sel === "[data-toggle-inline]" ? togBtn : null);
  await T.handleQuickOptionClick({ target: togBtn, preventDefault() {} });
  assert.equal(T.getOpenInline(), "e7", "desktop click opens Plan A inline card");
  assert.equal(T.getReplyTo(), null, "desktop Plan A does not hijack bottom composer");
  assert.ok(threadBox.innerHTML.includes('placeholder="补充一句…"'), "Plan A uses minimal placeholder");
  assert.ok(threadBox.innerHTML.includes('class="send reply-inline-send"'), "Plan A uses circular .send button");

  // IME safety + Shift+Enter + Esc draft preservation on Plan A textarea
  const riForm = makeEl("ri-form-e7");
  riForm.dataset.inlineBox = "e7";
  let formSubmitted = 0;
  riForm.requestSubmit = () => { formSubmitted++; };
  const riWrap = makeEl("ri-wrap-e7");
  const riSend = makeEl("ri-send-e7");
  riSend.disabled = true;
  riForm._qs["[data-inline-send]"] = riSend;
  const riTa = makeEl("ri-ta-e7");
  riTa.dataset.inlineInput = "e7";
  riTa.parentElement = riWrap;
  riTa.closest = (sel) => (sel === "[data-inline-input]" ? riTa : sel === "[data-inline-box]" || sel === "form.reply-inline" ? riForm : null);

  riTa.value = "第一行草稿";
  doc.dispatchDoc("input", { target: riTa });
  assert.equal(T.getInlineDraft("e7"), "第一行草稿", "typing persists draft");
  assert.equal(riWrap.dataset.replicatedValue, "第一行草稿", "fallback updates data-replicated-value when field-sizing unsupported");
  assert.equal(riSend.disabled, false, "non-empty input enables circular send button");

  // Shift+Enter -> must NOT submit
  const evShift = doc.dispatchDoc("keydown", { target: riTa, key: "Enter", shiftKey: true, isComposing: false, keyCode: 13, timeStamp: 2000 });
  assert.equal(evShift.defaultPrevented, false, "Shift+Enter allows newline");
  assert.equal(formSubmitted, 0);

  // IME composing Enter (isComposing=true / keyCode=229) -> must NOT submit
  const evIme = doc.dispatchDoc("keydown", { target: riTa, key: "Enter", shiftKey: false, isComposing: true, keyCode: 229, timeStamp: 2010 });
  assert.equal(evIme.defaultPrevented, false);
  assert.equal(formSubmitted, 0);

  // Safari WebKit bug 165004: Enter within 30ms of compositionend -> prevented, must NOT submit
  riTa.dispatch("compositionstart", { timeStamp: 2020 });
  riTa.dispatch("compositionend", { timeStamp: 2050 });
  const evSafari = doc.dispatchDoc("keydown", { target: riTa, key: "Enter", shiftKey: false, isComposing: false, keyCode: 13, timeStamp: 2056 });
  assert.equal(evSafari.defaultPrevented, true, "Safari post-IME Enter prevents stray newline");
  assert.equal(formSubmitted, 0, "Safari post-IME Enter must NOT submit");

  // Real Enter (>30ms after compositionend) -> submits!
  const evEnter = doc.dispatchDoc("keydown", { target: riTa, key: "Enter", shiftKey: false, isComposing: false, keyCode: 13, timeStamp: 2120 });
  assert.equal(evEnter.defaultPrevented, true);
  assert.equal(formSubmitted, 1, "plain Enter submits Plan A card");

  // Esc collapses Plan A card while keeping draft & showing .draft-dot!
  doc.dispatchDoc("keydown", { target: riTa, key: "Escape", isComposing: false });
  assert.equal(T.getOpenInline(), null, "Esc collapses Plan A inline card");
  assert.equal(T.getInlineDraft("e7"), "第一行草稿", "Esc preserves unsent draft");
  assert.ok(threadBox.innerHTML.includes('class="draft-dot"'), "collapsed button shows subtle draft dot");

  // C2) Mobile Plan B (<=760px): clicking 补充一句 NEVER expands mid-stream; attaches compact "回复 · <摘要>" bar at bottom!
  isNarrow = true;
  const superIn = getEl("#super-input");
  superIn.value = "";
  const scrollBeforeMobile = scroller.scrollTop;
  await T.handleQuickOptionClick({ target: togBtn, preventDefault() {} });
  assert.equal(T.getOpenInline(), null, "mobile Plan B must NOT open mid-stream inline card");
  assert.equal(T.getReplyTo()?.entry, "e7", "mobile Plan B attaches e7 to bottom composer");
  assert.equal(T.getReplyTo()?.mode, "supplement", "mobile Plan B marks supplement mode");
  assert.equal(reDraft.hidden, false, "mobile Plan B reveals compact #re-draft strip");
  assert.equal(reDraft._qs[".re-who"].textContent, "回复 ·", "mobile quote strip uses minimal 回复 · label");
  assert.equal(superIn.value, "第一行草稿", "mobile Plan B restores saved draft into bottom input");
  assert.equal(superIn.focused, true, "mobile Plan B focuses bottom main composer");
  assert.equal(scroller.scrollTop, scrollBeforeMobile, "mobile Plan B causes 0px viewport jump");

  // Breakpoint handoff Mobile -> Desktop preserves draft and hands off cleanly to Plan A!
  superIn.value = "手机写了一半切桌面继续";
  superIn.dispatch("input");
  isNarrow = false;
  narrowListeners.forEach((fn) => fn());
  assert.equal(T.getReplyTo(), null, "switching to desktop clears bottom supplement target");
  assert.equal(T.getOpenInline(), "e7", "switching to desktop hands off to Plan A card");
  assert.equal(T.getInlineDraft("e7"), "手机写了一半切桌面继续", "breakpoint handoff preserves draft");

  console.log("OK: all unread-reply-ux JS state machine & Plan A/B checks passed");
})().catch((err) => {
  console.error(err);
  process.exit(1);
});
`
	cmd := exec.Command(nodeBin, "-e", jsTest, root)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("unread-reply-ux JS tests failed: %v\n%s", err, string(out))
	}
}
