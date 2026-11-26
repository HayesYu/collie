package collie

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"
)

func TestSuperSayQuoteUserAndCollieEntries(t *testing.T) {
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
	srv := &Server{token: "test-tok", home: dir, web: fstest.MapFS{}}
	sp := &Super{s: srv, db: db, dir: dir, st: st, subs: map[chan struct{}]bool{}}
	srv.sup = sp
	mux := http.NewServeMux()
	srv.superRoutes(mux)

	postSay := func(body map[string]any) (int, map[string]any) {
		b, _ := json.Marshal(body)
		req := httptest.NewRequest("POST", "/api/super/say", bytes.NewReader(b))
		req.Header.Set("Authorization", "Bearer test-tok")
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		var out map[string]any
		_ = json.Unmarshal(rec.Body.Bytes(), &out)
		return rec.Code, out
	}

	// 1) Initial user message + Collie reply.
	code, r1 := postSay(map[string]any{"text": "先看下分页测试失败原因", "client_msg_id": "m-root"})
	if code != 200 {
		t.Fatalf("say 1 status=%d body=%+v", code, r1)
	}
	eRoot := r1["entry"].(string)
	eCollie, _ := sp.Reply(mainThread, "定位到了，是同秒排序不稳定。", eRoot, nil)

	// 2) User replies to Collie's message.
	code, r2 := postSay(map[string]any{"text": "补上 id DESC 吧", "re": eCollie.ID, "client_msg_id": "m-u2"})
	if code != 200 {
		t.Fatalf("say 2 status=%d body=%+v", code, r2)
	}
	eUser2 := r2["entry"].(string)

	// 3) User immediately sends another follow-up in the thread drawer, chaining onto their own latest message eUser2.
	code, r3 := postSay(map[string]any{"text": "顺便加 200 轮回归测试", "re": eUser2, "client_msg_id": "m-u3"})
	if code != 200 {
		t.Fatalf("say 3 status=%d body=%+v", code, r3)
	}
	eUser3 := r3["entry"].(string)

	sp.mu.Lock()
	ent2 := sp.findEntryLocked(eUser2)
	ent3 := sp.findEntryLocked(eUser3)
	trig2 := sp.findTriggerLocked(r2["trigger"].(string))
	trig3 := sp.findTriggerLocked(r3["trigger"].(string))
	ev := sp.addEntryLocked("event", mainThread, "worker 回报：done", "wk", "done")
	sp.mu.Unlock()

	if ent2 == nil || ent2.Re == nil || ent2.Re.Entry != eCollie.ID || ent2.Re.Who != "Collie" {
		t.Fatalf("ent2.Re = %+v, want Collie quote of %s", ent2, eCollie.ID)
	}
	if trig2 == nil || !strings.Contains(trig2.Text, "[In reply to what you said earlier:") {
		t.Fatalf("trig2 text = %q", trig2.Text)
	}
	if ent3 == nil || ent3.Re == nil || ent3.Re.Entry != eUser2 || ent3.Re.Who != "你" {
		t.Fatalf("ent3.Re = %+v, want user quote of %s", ent3, eUser2)
	}
	if trig3 == nil || !strings.Contains(trig3.Text, "[In reply to what the human said earlier:") {
		t.Fatalf("trig3 text = %q", trig3.Text)
	}

	// 4) Quoting an event or unknown ID from /api/super/say leaves Re nil without erroring.
	code, r4 := postSay(map[string]any{"text": "普通消息", "re": ev.ID})
	if code != 200 {
		t.Fatalf("say 4 status=%d", code)
	}
	sp.mu.Lock()
	ent4 := sp.findEntryLocked(r4["entry"].(string))
	sp.mu.Unlock()
	if ent4 == nil || ent4.Re != nil {
		t.Fatalf("quoting event from say should yield nil Re, got %+v", ent4)
	}

	// 5) SQLite fallback for sayQuoteLocked when target user message was trimmed from memory.
	sp.mu.Lock()
	for i := 0; i < 620; i++ {
		sp.addEntryLocked("collie", mainThread, fmt.Sprintf("fill %d", i), "", "")
	}
	sp.mu.Unlock()
	sp.flush()

	sp.mu.Lock()
	qOld := sp.sayQuoteLocked(eRoot)
	sp.mu.Unlock()
	if qOld == nil || qOld.Entry != eRoot || qOld.Who != "你" || !strings.Contains(qOld.Text, "分页测试失败原因") {
		t.Fatalf("SQLite fallback sayQuoteLocked(%s) = %+v", eRoot, qOld)
	}
}

func TestReplyThreadSettingsAndNotificationsJS(t *testing.T) {
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
      if (el._qs[sel]) return el._qs[sel];
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
    append() {},
    remove() {},
    after() {},
    focus() {},
    click() {},
    scrollIntoView() {},
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
getEl("#thread-drawer").hidden = true;
getEl("#thread-scrim").hidden = true;
getEl("#file-view").hidden = true;

const storage = new Map();
const docListeners = new Map();
const toasts = [];
getEl("#toasts").append = (t) => toasts.push(t.textContent);

const notified = [];
class FakeNotification {
  constructor(title, opts) {
    this.title = title;
    this.opts = opts || {};
    this.onclick = null;
    notified.push(this);
  }
  static permission = "granted";
  static async requestPermission() { return FakeNotification.permission; }
}

const fetchCalls = [];
let sayGate = null;
let saySeq = 0;
let earlierBatch = [];
function deferred() {
  let resolve, reject;
  const promise = new Promise((res, rej) => { resolve = res; reject = rej; });
  return { promise, resolve, reject };
}

let docHidden = false;
let docFocused = true;
let lastES = null;
class FakeES {
  constructor(url) {
    this.url = url;
    this.readyState = 1;
    this.ls = {};
    lastES = this;
  }
  addEventListener(t, fn) { (this.ls[t] ||= []).push(fn); }
  emit(t, data) { for (const fn of (this.ls[t] || [])) fn({ data: JSON.stringify(data) }); }
  close() { this.readyState = 2; }
}

const doc = {
  documentElement: makeEl("html"),
  body: makeEl("body"),
  get hidden() { return docHidden; },
  hasFocus: () => docFocused,
  createElement: (tag) => makeEl(tag),
  getElementById: (id) => getEl("#" + id),
  querySelector: (sel) => (sel === "dialog[open]" ? null : getEl(sel)),
  querySelectorAll: () => [],
  addEventListener(type, fn) {
    if (!docListeners.has(type)) docListeners.set(type, []);
    docListeners.get(type).push(fn);
  },
  dispatchDoc(type, ev = {}) {
    const e = {
      type,
      target: makeEl("doc"),
      defaultPrevented: false,
      preventDefault() { this.defaultPrevented = true; },
      stopPropagation() {},
      composedPath: () => [],
      ...ev,
    };
    for (const fn of [...(docListeners.get(type) || [])]) fn(e);
    return e;
  },
};

let tdUrlSeq = 0;
class FakeURL extends URL {
  static createObjectURL() { return "blob:td-test/" + (++tdUrlSeq); }
  static revokeObjectURL() {}
}

let focusedCount = 0;
const ctx = {
  console: { log() {}, info() {}, warn() {}, error() {} },
  document: doc,
  window: null,
  location: { hash: "#/collie", search: "", pathname: "/", origin: "http://127.0.0.1:7788", hostname: "127.0.0.1" },
  history: { state: {}, replaceState() {}, back() {} },
  navigator: { userAgent: "NodeTest", platform: "Linux" },
  localStorage: {
    getItem: (k) => (storage.has(k) ? storage.get(k) : null),
    setItem: (k, v) => storage.set(k, String(v)),
    removeItem: (k) => storage.delete(k),
  },
  matchMedia: () => ({ matches: false, addEventListener() {} }),
  MutationObserver: class { observe() {} },
  EventSource: FakeES,
  Notification: FakeNotification,
  isSecureContext: true,
  focus: () => { focusedCount++; },
  CSS: { escape: (s) => String(s), supports: () => true },
  URL: FakeURL,
  URLSearchParams,
  Blob,
  Uint8Array,
  TextEncoder,
  atob,
  AbortController,
  crypto: { randomUUID: () => "td-uuid-" + (++saySeq) },
  performance: { now: () => 0 },
  requestAnimationFrame: (fn) => { fn(); return 0; },
  cancelAnimationFrame() {},
  setTimeout: (fn) => { return 0; },
  clearTimeout() {},
  setInterval: () => 0,
  clearInterval() {},
  addEventListener() {},
  removeEventListener() {},
  dispatchEvent() {},
  Event,
  HTMLDialogElement: { prototype: {} },
  getComputedStyle: () => ({ getPropertyValue: () => "" }),
  innerWidth: 1440,
  innerHeight: 900,
};
const okJSON = (data) => ({ ok: true, status: 200, headers: { get: () => "" }, json: async () => data });
let uploadSeq = 0;
ctx.fetch = async (url, init = {}) => {
  const u = String(url);
  fetchCalls.push({ url: u, method: init.method || "GET", body: init.body });
  if (u.startsWith("/api/uploads")) {
    const parsedU = new URL(u, "http://127.0.0.1:7788");
    const name = parsedU.searchParams.get("name") || "file.png";
    const id = String(++uploadSeq).padStart(6, "0");
    return okJSON({ path: "/tmp/uploads/" + id + "-" + name, name, size: init.body?.size || 64 });
  }
  if (u.startsWith("/api/models")) return okJSON({ models: [], efforts: [] });
  if (u === "/api/super/say") {
    if (sayGate) await sayGate.promise;
    const parsed = JSON.parse(init.body || "{}");
    const newId = "e" + (500 + fetchCalls.length);
    return okJSON({ entry: newId, trigger: "t" + (500 + fetchCalls.length), client_msg_id: parsed.client_msg_id });
  }
  if (u.startsWith("/api/super/earlier")) {
    const out = earlierBatch.slice();
    earlierBatch = [];
    return okJSON({ entries: out, more: false });
  }
  return okJSON({ ok: true });
};
ctx.window = ctx;
ctx.globalThis = ctx;

const marker = "  boot();\n})();";
assert.ok(appSrc.includes(marker), "expected boot() tail in web/app.js");
const instrumented = appSrc.replace(marker,
  "  globalThis.__test = { getSup: () => sup, setSup: (s) => { sup = normSup(s); }, resetChat: () => { older = []; pendingSays.length = 0; dismissedLocal.clear(); }, setState: (st) => { localState = st; state = mergedState(); }, setHist: (h) => { hist = h || []; }, setView: (v) => { view = v; }, setBooted: (b) => { booted = !!b; }, route, connect, renderSuper, computeReplyThreads, threadTitleFromText, threadPreviewFromEntry, isThreadPending, renderCollieThreadsMenu, dismissPending, extractThreadWorkers, openThreadDrawer, closeThreadDrawer, getActiveThreadRoot: () => activeThreadRoot, settingsHTML, renderPrefs, notifyEnvState, syncCollieNotifyWatermark, sendTestNotification };\n})();"
);
vm.runInNewContext(instrumented, ctx, { filename: "web/app.js" });

const flush = () => new Promise((r) => setImmediate(r));
const settle = async (n = 8) => { for (let i = 0; i < n; i++) await flush(); };

(async () => {
  const T = ctx.__test;
  T.setView("collie");

  // ─── 1. Threshold >= 3 messages & zero redundant main-stream noise ───
  const now = Date.now();
  const ts = (m) => new Date(now - m * 60000).toISOString();
  const baseEntries = [
    // Standalone message
    { id: "e1", role: "user", text: "普通单条消息", at: ts(20) },
    // 2-message Q&A (MUST NOT become a thread under >= 3 rule)
    { id: "e2", role: "user", text: "两轮一问一答的提问", at: ts(18) },
    { id: "e3", role: "collie", text: "两轮一问一答的回复", at: ts(17), re: { entry: "e2", who: "你", text: "两轮一问一答的提问" } },
    // Single reply to worker event (MUST NOT become a thread)
    { id: "e4", role: "event", ref: "wk1", tone: "done", text: "wk1 回报：完成", at: ts(15) },
    { id: "e5", role: "collie", text: "wk1 做完了。", at: ts(14), re: { entry: "e4", worker: "wk1", who: "wk1", text: "完成" } },
    // 5-message branched chain (>= 3 messages -> MUST form a thread with 4 replies & 1 branch)
    { id: "e10", role: "user", text: "话题抽屉精简版怎么设计？希望主聊天流零冗余噪音。", at: ts(12) },
    { id: "e11", role: "collie", text: "主聊只在形成多轮往返时轻露一行入口。", at: ts(11), re: { entry: "e10", who: "你", text: "话题抽屉精简版怎么设计？" } },
    { id: "e12", role: "user", text: "桌面端右侧 432px，手机端全屏右滑。", at: ts(8), re: { entry: "e11", who: "Collie", text: "主聊只在形成多轮往返时轻露一行入口。" } },
    { id: "e13", role: "collie", text: "收到，抽屉底部带独立输入框自动接续最新。", at: ts(7), re: { entry: "e12", who: "你", text: "桌面端右侧 432px，手机端全屏右滑。" } },
    { id: "e14", role: "user", text: "分支补充：引用条点击也要能顺手打开抽屉。", at: ts(5), re: { entry: "e11", who: "Collie", text: "主聊只在形成多轮往返时轻露一行入口。" } },
  ];

  const info = T.computeReplyThreads(baseEntries);
  assert.equal(info.threads.size, 1, "only the >=3-message chain forms a thread");
  assert.ok(!info.msgToRoot.has("e1"), "standalone message not in thread");
  assert.ok(!info.msgToRoot.has("e2") && !info.msgToRoot.has("e3"), "2-message Q&A must not form a thread");
  assert.ok(!info.msgToRoot.has("e5"), "single reply to worker event must not form a thread");
  const th = info.threads.get("e10");
  assert.ok(th, "e10 is the thread root");
  assert.equal(th.totalCount, 5);
  assert.equal(th.replyCount, 4);
  assert.equal(th.branchCount, 1);
  assert.equal(th.title, "话题抽屉精简版怎么设计");
  // Contiguous run e10..e14 -> entry button shown ONLY ONCE on e14 (not stacked on e10..e13).
  assert.equal(info.showEntryOn.size, 1);
  assert.ok(info.showEntryOn.has("e14"));

  T.setSup({ router: { status: "idle", alive: true }, entries: baseEntries, workers: [] });
  T.renderSuper({ force: true });
  const mainHtml = getEl("#super-thread").innerHTML;
  // Exact minimal button copy: 4 条回复, no extra timestamp, no "这条线", no duplicate thread icon button in .msg-acts
  assert.ok(mainHtml.includes('data-open-thread="e10"'), "thread entry rendered for e10");
  assert.ok(mainHtml.includes('<span class="te-count">4 条回复</span>'), "minimal reply count label");
  assert.ok(!mainHtml.includes("这条线"), "must not contain redundant 这条线 prefix");
  assert.ok(!mainHtml.includes("te-time"), "must not contain duplicate timestamp in thread entry");
  // Ensure .msg-acts has zero data-open-thread button (no redundant ↳ icon in hover toolbar)
  const actsMatches = mainHtml.match(/<div class="msg-acts">[\s\S]*?<\/div>/g) || [];
  assert.ok(actsMatches.length >= 5, "msg-acts rendered");
  for (const a of actsMatches) {
    assert.ok(!a.includes("data-open-thread"), "hover toolbar must NOT add redundant thread icon button: " + a);
  }
  // 2-message Q&A quote bar has NO data-thread-root (keeps standard jump)
  assert.ok(!mainHtml.includes('data-goto="e2" data-thread-root='), "2-msg Q&A quote stays jump-only");
  // Branched >=3-message quote bar carries data-thread-root="e10"
  assert.ok(mainHtml.includes('data-goto="e11" data-thread-root="e10"'), "thread quote opens drawer");

  // ─── 2. Open drawer, verify contents, zero main-chat DOM rebuild, IME-safe + reentrancy-safe send chaining onto user message e14, and live Collie SSE sync ───
  const mainThreadEl = getEl("#super-thread");
  let innerWrites = 0;
  let storedMainHTML = mainThreadEl.innerHTML;
  Object.defineProperty(mainThreadEl, "innerHTML", {
    get() { return storedMainHTML; },
    set(v) { innerWrites++; storedMainHTML = v; },
    configurable: true,
  });
  let drawerScrollIntoViewCalls = 0;
  const origMakeNode = getEl("#td-list").querySelector.bind(getEl("#td-list"));
  getEl("#td-list").querySelector = (sel) => {
    const n = origMakeNode(sel);
    n.scrollIntoView = () => { drawerScrollIntoViewCalls++; };
    return n;
  };
  T.openThreadDrawer("e10", "e14", "e14");
  assert.equal(innerWrites, 0, "opening thread drawer must NOT rebuild #super-thread.innerHTML");
  assert.equal(drawerScrollIntoViewCalls, 0, "focusNodeInDrawer must scroll #td-body directly without calling scrollIntoView");
  assert.equal(T.getActiveThreadRoot(), "e10");
  assert.equal(getEl("#thread-drawer").hidden, false);
  assert.ok(getEl("#app").classList.contains("has-thread"));
  assert.equal(getEl("#td-title").textContent, "话题抽屉精简版怎么设计");
  assert.ok(getEl("#td-meta").textContent.includes("共 5 条消息") && getEl("#td-meta").textContent.includes("含 1 处分支"));
  const tdListHtml = getEl("#td-list").innerHTML;
  assert.ok(tdListHtml.includes("td-branch-ref"), "branch cue rendered for e14");
  assert.ok(tdListHtml.includes("起点") && tdListHtml.includes("分支"), "root and branch tags rendered");
  assert.ok(tdListHtml.includes('class="td-av user"') && tdListHtml.includes('viewBox="0 0 64 64"'), "drawer user avatar uses matching line-art SVG");

  // IME composing Enter must NOT submit
  const tdInput = getEl("#td-input");
  const tdComposer = getEl("#td-composer");
  tdInput.value = "继续补充一条";
  tdInput.dispatch("input");
  fetchCalls.length = 0;
  tdInput.dispatch("keydown", { key: "Enter", shiftKey: false, metaKey: false, ctrlKey: false, isComposing: true, keyCode: 229 });
  await settle();
  assert.equal(fetchCalls.filter((c) => c.url === "/api/super/say").length, 0, "IME composing Enter must not send");

  // Rapid double Enter submits ONLY ONCE, automatically chaining onto latest message e14 (role=user!)
  sayGate = deferred();
  tdInput.dispatch("keydown", { key: "Enter", shiftKey: false, metaKey: false, ctrlKey: false, isComposing: false, keyCode: 13 });
  tdInput.dispatch("keydown", { key: "Enter", shiftKey: false, metaKey: false, ctrlKey: false, isComposing: false, keyCode: 13 });
  tdComposer.requestSubmit();
  // Optimistic message appears immediately in drawer before POST settles!
  assert.ok(getEl("#td-list").innerHTML.includes("继续补充一条"), "optimistic drawer reply appears immediately");
  sayGate.resolve();
  await settle();

  const drawerSayReqs = fetchCalls.filter((c) => c.url === "/api/super/say");
  assert.equal(drawerSayReqs.length, 1, "reentrancy lock prevents double-send in drawer");
  const drawerPayload = JSON.parse(drawerSayReqs[0].body);
  assert.equal(drawerPayload.text, "继续补充一条");
  assert.equal(drawerPayload.re, "e14", "drawer auto-chains reply_to onto latest message e14 (even when role=user)");
  assert.ok(drawerPayload.client_msg_id, "drawer send includes client_msg_id");

  // Real-time SSE Collie reply automatically refreshes open drawer
  T.connect();
  const confirmedUserId = "e501";
  const nextEntries = [
    ...baseEntries,
    { id: confirmedUserId, role: "user", text: "继续补充一条", at: ts(1), re: { entry: "e14", who: "你", text: "分支补充" } },
    { id: "e502", role: "collie", text: "**收到**，已同步写入抽屉和主聊。", at: ts(0), re: { entry: confirmedUserId, who: "你", text: "继续补充一条" } },
  ];
  T.syncCollieNotifyWatermark(baseEntries, false);
  assert.equal(notified.length, 0, "cold-start watermark priming must not fire notifications");

  // Put page in background and deliver SSE super event -> BOTH drawer updates AND Collie desktop notification fires!
  docHidden = true;
  docFocused = false;
  lastES.emit("super", { router: { status: "idle", alive: true }, entries: nextEntries, workers: [] });
  assert.ok(getEl("#td-list").innerHTML.includes("已同步写入抽屉和主聊"), "open drawer refreshes live on Collie SSE reply");
  assert.equal(notified.length, 1, "new Collie reply in background fires browser notification");
  assert.equal(notified[0].title, "Collie");
  assert.equal(notified[0].opts.tag, "collie-reply");
  assert.equal(notified[0].opts.body, "收到，已同步写入抽屉和主聊。", "Markdown stripped cleanly from notification body");
  ctx.location.hash = "#/prefs";
  notified[0].onclick();
  assert.equal(ctx.location.hash, "#/collie", "clicking Collie notification navigates to #/collie");
  assert.ok(focusedCount >= 1, "clicking notification focuses window");

  // Foreground Collie reply does NOT fire desktop notification
  docHidden = false;
  docFocused = true;
  notified.length = 0;
  lastES.emit("super", {
    router: { status: "idle", alive: true },
    entries: [...nextEntries, { id: "e503", role: "collie", text: "前台消息不弹窗", at: ts(0) }],
    workers: [],
  });
  assert.equal(notified.length, 0, "foreground page must not pop desktop notification");

  // Esc closes thread drawer without rebuilding main chat DOM
  const writesBeforeClose = innerWrites;
  doc.dispatchDoc("keydown", { key: "Escape", isComposing: false, keyCode: 27 });
  assert.equal(innerWrites, writesBeforeClose, "closing thread drawer must NOT rebuild #super-thread.innerHTML");
  assert.equal(T.getActiveThreadRoot(), null, "Esc closes thread drawer");
  assert.ok(!getEl("#app").classList.contains("has-thread"));

  // ─── 2b. CSS & HTML contracts: restrained header threads button + unified sans typography on UI chrome ───
  const indexHtml = fs.readFileSync(path.join(process.argv[1], "web", "index.html"), "utf8");
  const appCss = fs.readFileSync(path.join(process.argv[1], "web", "app.css"), "utf8");
  assert.ok(indexHtml.includes('<div class="d-head-acts">'), "header actions grouped cleanly");
  assert.ok(!indexHtml.includes('id="collie-threads-btn" aria-haspopup='), "non-menu region trigger omits aria-haspopup");
  assert.ok(!appCss.includes(".item .sub { font-family: var(--mono); }"), "sidebar subtitles use unified sans font");
  assert.ok(!/\.kind\s*\{[^}]*--mono/.test(appCss), "header .kind badge uses unified sans font");
  assert.ok(!/\.chip \.k\s*\{[^}]*--mono/.test(appCss), "crew strip .chip .k uses unified sans font");
  const idxMax1000HasThread = appCss.lastIndexOf(".app.has-thread { grid-template-columns: var(--side-w) 1fr; }");
  const idxMax760HasThread = appCss.lastIndexOf(".app.has-thread { grid-template-columns: 1fr; }");
  assert.ok(idxMax1000HasThread > 0 && idxMax760HasThread > idxMax1000HasThread, "mobile 760px has-thread 1fr rule must override 1000px side-w rule");

  // ─── 3. Missing-root auto-fetch via /api/super/earlier ───
  const partialEntries = [
    { id: "e90", role: "collie", text: "第一次追问回复", at: ts(4), re: { entry: "e20", who: "你", text: "更早的起点问题" } },
    { id: "e91", role: "user", text: "第二次跟进确认", at: ts(2), re: { entry: "e90", who: "Collie", text: "第一次追问回复" } },
  ];
  earlierBatch = [
    { id: "e20", role: "user", text: "更早的起点问题。细节如下。", at: ts(60) },
  ];
  T.setSup({ router: { status: "idle", alive: true }, entries: partialEntries, workers: [], earlier: true });
  T.renderSuper({ force: true });
  assert.ok(getEl("#super-thread").innerHTML.includes('data-open-thread="e20"'), "missing root with >=2 loaded replies forms >=3 thread");
  fetchCalls.length = 0;
  T.openThreadDrawer("e20");
  await settle();
  assert.ok(fetchCalls.some((c) => c.url.includes("/api/super/earlier") && c.url.includes("until=e20")), "opening missing-root thread fetches earlier history");
  assert.ok(getEl("#td-list").innerHTML.includes("更早的起点问题。细节如下。"), "loaded root renders inside drawer");
  assert.equal(getEl("#td-title").textContent, "更早的起点问题");
  T.closeThreadDrawer({ immediate: true });

  // ─── 4. Settings page: 「重启 Collie」 moved into 「Collie 自己」 card header, removed from bottom ───
  const sHtml = T.settingsHTML({
    router_dir: "/home/demo/.collie/router",
    settings: { router_kind: "claude", router_model: "", router_effort: "", worker_kind: "codex", worker_cwd: "~", notify_level: "normal", stall_minutes: 20, reply_chars: 200, rotate_pct: 60, dream_at: "04:00" },
  });
  const firstCardMatch = sHtml.match(/<section class="card set-sec">[\s\S]*?<\/section>/);
  assert.ok(firstCardMatch, "first settings card rendered");
  assert.ok(firstCardMatch[0].includes("Collie 自己") && firstCardMatch[0].includes('data-act="router-restart"'), "重启 Collie moved to Collie 自己 card header");
  assert.ok(!sHtml.includes("set-foot"), "bottom set-foot restart bar removed");

  // ─── 5. Browser notifications preferences states + test button ───
  // 5a) Granted & enabled -> shows 测试 button, clicking sends test notification
  ctx.isSecureContext = true;
  FakeNotification.permission = "granted";
  storage.set("collie.notify", "true");
  T.renderPrefs();
  const prefsGranted = getEl("#prefs-body").innerHTML;
  assert.ok(prefsGranted.includes('data-act="test-notify"'), "test button shown when notification enabled & granted");
  notified.length = 0;
  assert.equal(T.sendTestNotification(), true);
  assert.equal(notified.length, 1);
  assert.equal(notified[0].opts.tag, "collie-test");

  // 5b) Denied -> clear site-settings hint, no test button
  FakeNotification.permission = "denied";
  T.renderPrefs();
  const prefsDenied = getEl("#prefs-body").innerHTML;
  assert.ok(prefsDenied.includes("已被浏览器拦截，请在地址栏左侧站点设置中重新开启"), "denied hint shown");
  assert.ok(!prefsDenied.includes('data-act="test-notify"'), "no test button when denied");

  // 5c) Insecure HTTP LAN -> clear non-HTTPS explanation, switch disabled, no test button
  ctx.isSecureContext = false;
  T.renderPrefs();
  const prefsInsecure = getEl("#prefs-body").innerHTML;
  assert.ok(prefsInsecure.includes("当前为非 HTTPS 内网地址，浏览器禁止桌面通知"), "insecure HTTP hint shown");
  assert.ok(prefsInsecure.includes('data-pref="notify" disabled'), "notify switch disabled in insecure HTTP context");
  assert.ok(!prefsInsecure.includes('data-act="test-notify"'), "no test button in insecure context");

  // ─── 6. Thread drawer associated Agent pills: precision, sorting, zero-noise hide, live SSE, and history back ───
  const wTs = (m) => new Date(now - m * 60000).toISOString();
  const sampleWorkers = [
    { name: "test", kind: "codex", status: "working", pane_id: "w90:p1", created_at: wTs(20) },
    { name: "wk-working", kind: "codex", status: "working", pane_id: "w91:p1", created_at: wTs(15) },
    { name: "wk-blocked", kind: "claude", status: "blocked", pane_id: "w92:p1", created_at: wTs(18) },
    { name: "wk-waiting", kind: "agy", status: "waiting", pane_id: "w93:p1", created_at: wTs(12) },
    { name: "wk-done-old", kind: "claude", status: "done", pane_id: "w94:p1", created_at: wTs(25), report_at: wTs(10) },
    { name: "wk-done-new", kind: "claude", status: "done", pane_id: "w95:p1", created_at: wTs(22), report_at: wTs(2) },
    { name: "wk-ended", kind: "codex", status: "gone", ended: true, created_at: wTs(30), report_at: wTs(14) },
  ];
  const sampleAgents = [
    { pane_id: "w90:p1", name: "test", kind: "codex", status: "working", is_agent: true },
    { pane_id: "w91:p1", name: "wk-working", kind: "codex", status: "working", is_agent: true },
    { pane_id: "w92:p1", name: "wk-blocked", kind: "claude", status: "blocked", is_agent: true },
    { pane_id: "w93:p1", name: "wk-waiting", kind: "agy", status: "waiting", is_agent: true },
    { pane_id: "w94:p1", name: "wk-done-old", kind: "claude", status: "done", is_agent: true },
    { pane_id: "w95:p1", name: "wk-done-new", kind: "claude", status: "done", is_agent: true },
  ];
  const sampleHist = [
    { id: "h-ended-1", name: "wk-ended", kind: "codex", ended: wTs(13), cwd: "/tmp/demo" },
  ];
  const workerEntries = [
    // Thread A: NO associated workers (contains substring traps for worker "test"!)
    { id: "ea1", role: "user", text: "请看 ~/code/test/main.go 和 https://example.com/test 以及 fix-flaky-test 的 testing 逻辑。", at: wTs(25) },
    { id: "ea2", role: "collie", text: "看了 test.go 和 my_test：\n\x60\x60\x60go\nfunc test() {}\n\x60\x60\x60\n不需要派 worker。", at: wTs(24), from: "feishu-user", re: { entry: "ea1", who: "你", text: "请看 ~/code/test/main.go" } },
    { id: "ea3", role: "user", text: "好的，先这样。", at: wTs(23), re: { entry: "ea2", who: "Collie", text: "看了 test.go" } },
    // Worker report event quoted by Thread B root
    { id: "eb0", role: "event", ref: "wk-done-old", tone: "done", text: "wk-done-old 回报：旧报告", at: wTs(10) },
    // Thread B: multiple workers via structured fields (re.worker, re.entry->event, e.worker, e.from, e.action) + exact prose boundary
    { id: "eb1", role: "collie", text: "已派出 **wk-working** 和 \x60wk-done-new\x60 并行推进。", at: wTs(9), re: { entry: "eb0", worker: "wk-done-old", who: "wk-done-old", text: "旧报告" } },
    { id: "eb2", role: "user", text: "顺便盯着点 wk-waiting，另外 wk-ended 的记录也能看吗？", at: wTs(7), action: { type: "spawn", worker: "wk-blocked" }, re: { entry: "eb1", who: "Collie", text: "已派出 wk-working" } },
    { id: "eb3", role: "collie", text: "可以，wk-working 还在跑（重复提及去重）。", at: wTs(5), worker: "wk-working", re: { entry: "eb2", who: "你", text: "顺便盯着点 wk-waiting" } },
  ];

  T.setState({ ok: true, agents: sampleAgents, shells: [], machines: [{ id: "", name: "local", local: true, online: true }] });
  T.setHist(sampleHist);
  T.setSup({ router: { status: "idle", alive: true }, entries: workerEntries, workers: sampleWorkers });
  T.setView("collie");
  T.renderSuper({ force: true });

  // 6a) Thread A: zero false positives against worker "test" & unknown e.from -> #td-workers completely hidden
  T.openThreadDrawer("ea1");
  const tdWorkersEl = getEl("#td-workers");
  assert.equal(tdWorkersEl.hidden, true, "worker-less thread must hide #td-workers completely");
  assert.equal(tdWorkersEl.innerHTML, "", "worker-less thread must render empty #td-workers");

  // 6b) Thread B: exact deduplication, priority order (blocked > working > waiting > done-new > done-old > ended), links & dot classes
  T.openThreadDrawer("eb1");
  assert.equal(tdWorkersEl.hidden, false, "thread with associated workers must display #td-workers");
  const threadInfoB = T.computeReplyThreads(workerEntries);
  const extractedB = T.extractThreadWorkers(threadInfoB.threads.get("eb1"), threadInfoB, workerEntries);
  assert.deepEqual(
    Array.from(extractedB, (w) => w.name),
    ["wk-blocked", "wk-working", "wk-waiting", "wk-done-new", "wk-done-old", "wk-ended"],
    "workers deduplicated and sorted by blocked > working > waiting > recent done > ended",
  );
  assert.equal(extractedB.find((w) => w.name === "wk-working").href, "#/p/w91%3Ap1");
  assert.equal(extractedB.find((w) => w.name === "wk-ended").href, "#/h/h-ended-1");
  const wHtml = tdWorkersEl.innerHTML;
  assert.ok(wHtml.includes('class="dot blocked"'), "blocked dot rendered");
  assert.ok(wHtml.includes('class="dot working"'), "working dot rendered");
  assert.ok(wHtml.includes('class="dot waiting"'), "waiting dot rendered");
  assert.ok(wHtml.includes('class="dot done"'), "done dot rendered");
  assert.ok(wHtml.includes('class="dot gone"'), "ended gone dot rendered");
  assert.ok(wHtml.includes('href="#/p/w91%3Ap1"') && wHtml.includes('href="#/h/h-ended-1"'), "live and history links rendered");
  assert.ok(!wHtml.includes('data-td-worker="test"'), "substring trap worker must not appear in Thread B");

  // 6c) Real-time SSE worker status update refreshes #td-workers live WITHOUT rebuilding #td-list
  let tdListWrites = 0;
  let tdListHtmlVal = getEl("#td-list").innerHTML;
  Object.defineProperty(getEl("#td-list"), "innerHTML", {
    get() { return tdListHtmlVal; },
    set(v) { tdListWrites++; tdListHtmlVal = v; },
    configurable: true,
  });
  const updatedAgents = sampleAgents.map((a) => a.name === "wk-working" ? { ...a, status: "waiting" } : a);
  const updatedWorkers = sampleWorkers.map((w) => w.name === "wk-working" ? { ...w, status: "waiting" } : w);
  lastES.emit("state", { ok: true, agents: updatedAgents, shells: [], machines: [{ id: "", name: "local", local: true, online: true }] });
  lastES.emit("super", { router: { status: "idle", alive: true }, entries: workerEntries, workers: updatedWorkers });
  assert.equal(tdListWrites, 0, "live worker status refresh must not needlessly rebuild #td-list");
  assert.ok(tdWorkersEl.innerHTML.includes('data-td-worker="wk-working" data-td-status="waiting"'), "SSE update refreshes pill status in place");

  // 6d) Navigating from drawer to Agent terminal and returning via history.back() restores drawer context
  let histState = { collieIdx: 0 };
  ctx.history = {
    get state() { return histState; },
    replaceState(s) { histState = s; },
    back() {},
  };
  T.setBooted(true);
  ctx.location.hash = "#/collie";
  T.route();
  T.openThreadDrawer("eb1");
  assert.equal(T.getActiveThreadRoot(), "eb1");
  // Navigate to agent terminal (new history entry without collieIdx -> gets idx 1)
  histState = {};
  ctx.location.hash = "#/p/w91%3Ap1";
  T.route();
  assert.equal(T.getActiveThreadRoot(), null, "navigating to agent terminal closes thread drawer");
  // Simulate history.back() back to collieIdx 0
  histState = { collieIdx: 0 };
  ctx.location.hash = "#/collie";
  T.route();
  assert.equal(T.getActiveThreadRoot(), "eb1", "returning via history.back() restores the open thread drawer");
  assert.equal(getEl("#td-workers").hidden, false, "restored thread drawer re-renders associated worker pills");
  // Navigating fresh to #/collie (not back) does NOT spuriously reopen drawer after closing
  T.closeThreadDrawer({ immediate: true });
  histState = {};
  ctx.location.hash = "#/p/w91%3Ap1";
  T.route();
  histState = { collieIdx: 0 };
  ctx.location.hash = "#/collie";
  T.route();
  assert.equal(T.getActiveThreadRoot(), null, "closing drawer before navigating away keeps drawer closed on back");

  // 6e) HTML & CSS contracts for .td-workers / .td-worker
  assert.ok(indexHtml.includes('<nav class="td-workers" id="td-workers"'), "index.html defines semantic nav.td-workers");
  assert.ok(appCss.includes(".td-head:has(+ .td-workers:not([hidden]))"), "CSS merges header border when .td-workers visible");
  assert.ok(appCss.includes("scrollbar-width: none;") && appCss.includes("@supports not (scrollbar-width: none)"), "CSS hides horizontal scrollbar with modern @supports fallback");
  assert.ok(!/\.td-worker\b[^}]*--mono/.test(appCss), ".td-worker pills strictly use --sans typography, never --mono");
  assert.ok(appCss.includes(".viewer > .gone {"), "pane-closed .gone banner must be scoped to .viewer > .gone");
  assert.ok(!/(?:^|\n)\s*\.gone\s*\{/.test(appCss), "unscoped .gone rule must not exist (would hijack .dot.gone to bottom-left of .threadpane)");

  // ─── 7. Thread drawer image-only send, text+file send, instant blob thumb, cross-thread cleanup & HTML/CSS contracts ───
  const PNG_BYTES = Uint8Array.from(Buffer.from("iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mNk+A8AAQUBAScY42YAAAAASUVORK5CYII=", "base64"));
  const makeFakeFile = (name, type, bytes) => ({
    name,
    type,
    size: bytes.byteLength,
    lastModified: Date.now(),
    async arrayBuffer() { return bytes.buffer.slice(bytes.byteOffset, bytes.byteOffset + bytes.byteLength); },
  });
  T.setView("collie");
  T.setSup({ router: { status: "idle", alive: true }, entries: workerEntries, workers: sampleWorkers });
  T.renderSuper({ force: true });
  T.openThreadDrawer("eb1");

  const tdSendEl = getEl("#td-send");
  tdInput.value = "";
  tdInput.dispatch("input");
  assert.equal(tdSendEl.disabled, true, "drawer send disabled with empty input and no files");

  // 7a) Image-only paste + send (zero text) -> uploads, enables send, sends attachments + re, renders instant blob thumb in drawer & main chat
  fetchCalls.length = 0;
  const pasteShot = makeFakeFile("drawer-shot.png", "image/png", PNG_BYTES);
  tdInput.dispatch("paste", {
    clipboardData: { items: [{ kind: "file", type: "image/png", getAsFile: () => pasteShot }], files: [pasteShot], getData: () => "" },
  });
  await settle();
  assert.equal(tdComposer._files.length, 1);
  assert.equal(tdSendEl.disabled, false, "drawer send enabled for image-only reply");
  tdComposer.requestSubmit();
  // Synchronous optimistic render with pre-seeded blob thumbnail!
  assert.ok(getEl("#td-list").innerHTML.includes('class="thumbs"') && getEl("#td-list").innerHTML.includes('src="blob:td-test/'), "drawer instantly displays pre-seeded image thumbnail");
  assert.ok(getEl("#super-thread").innerHTML.includes('class="thumbs"') && getEl("#super-thread").innerHTML.includes('src="blob:td-test/'), "main stream also displays image thumbnail");
  await settle();
  const imgOnlySay = fetchCalls.find((c) => c.url === "/api/super/say");
  assert.ok(imgOnlySay, "image-only drawer send called /api/super/say");
  const imgOnlyBody = JSON.parse(imgOnlySay.body);
  assert.ok(imgOnlyBody.text.startsWith("附件：\n/tmp/uploads/") && imgOnlyBody.text.endsWith("-drawer-shot.png"), "image-only text carries uploaded path: " + imgOnlyBody.text);
  assert.equal(imgOnlyBody.re, "eb3", "image-only drawer send carries reply_to latest entry eb3");
  assert.equal(tdComposer._files.length, 0, "drawer files cleared after send");
  assert.equal(tdSendEl.disabled, true, "drawer send disabled again after send");

  // 7b) Text + non-image file drop -> carries text + attachment + re and renders .att link inside drawer
  fetchCalls.length = 0;
  const dropLog = makeFakeFile("trace.log", "text/plain", new TextEncoder().encode("hello log"));
  tdComposer.dispatch("drop", {
    dataTransfer: { items: [{ kind: "file", type: "text/plain", getAsFile: () => dropLog }], files: [dropLog], getData: () => "" },
  });
  await settle();
  tdInput.value = "请看附件日志";
  tdInput.dispatch("input");
  tdComposer.requestSubmit();
  await settle();
  const textFileSay = fetchCalls.find((c) => c.url === "/api/super/say");
  assert.ok(textFileSay);
  const textFileBody = JSON.parse(textFileSay.body);
  assert.ok(textFileBody.text.startsWith("请看附件日志\n\n附件：\n/tmp/uploads/") && textFileBody.text.endsWith("-trace.log"));
  assert.ok(textFileBody.re && textFileBody.re !== "eb3", "follow-up drawer reply chains onto just-sent image entry: " + textFileBody.re);
  assert.ok(getEl("#td-list").innerHTML.includes('class="att"') && getEl("#td-list").innerHTML.includes("trace.log"), "drawer renders non-image attachment chip");

  // 7c) Switching to a different thread clears stale pending attachments (prevents cross-thread leak)
  const pendingImg = makeFakeFile("other.png", "image/png", PNG_BYTES);
  tdInput.dispatch("paste", {
    clipboardData: { items: [{ kind: "file", type: "image/png", getAsFile: () => pendingImg }], files: [pendingImg], getData: () => "" },
  });
  await settle();
  assert.equal(tdComposer._files.length, 1);
  T.openThreadDrawer("ea1");
  assert.equal(tdComposer._files.length, 0, "switching to another thread clears stale pending files");
  T.closeThreadDrawer({ immediate: true });

  // 7d) HTML & CSS contracts for thread drawer attachments
  assert.ok(indexHtml.includes('<div class="files td-files" hidden></div>'), "index.html adds .td-files preview strip inside #td-composer");
  assert.ok(indexHtml.includes('class="icon-btn attach td-attach"') && indexHtml.includes('aria-label="添加图片或文件"'), "index.html adds accessible + attach button to #td-composer");
  assert.ok(appCss.includes(".td-foot.drop .td-cbox"), "CSS styles dragover drop highlight on .td-cbox");
  assert.ok(appCss.includes(".td-files") && appCss.includes(".td-text .thumbs") && appCss.includes(".td-text .att"), "CSS styles drawer preview strip and rendered thumbs/attachments");

  // ─── 8. Thread list popover readability: smart worker-prefix strip, latest-reply preview & pending light dot ───
  // 8a) Smart worker-prefix stripping & Markdown cleanup boundaries
  assert.equal(
    T.threadTitleFromText("feat-collie-unread-anchor（未读自动定位与分割线）已发版，已升完三端。"),
    "未读自动定位与分割线 · 已发版，已升完三端",
  );
  assert.equal(
    T.threadTitleFromText("\x60feat-collie-unread-anchor\x60（未读自动定位与分割线）已发版"),
    "未读自动定位与分割线 · 已发版",
  );
  assert.equal(
    T.threadTitleFromText("**\x60polish-thread-list\x60**（话题线列表可读性优化）：已完成四象限验收。细节如下"),
    "话题线列表可读性优化 · 已完成四象限验收",
  );
  assert.equal(
    T.threadTitleFromText("fix-login（登录回调修复）"),
    "登录回调修复",
  );
  assert.equal(
    T.threadTitleFromText("fix-login（登录回调修复）。后续再补回归测试"),
    "登录回调修复",
  );
  assert.equal(
    T.threadTitleFromText("fix-login (登录回调修复) 已合入"),
    "登录回调修复 · 已合入",
  );
  assert.equal(
    T.threadTitleFromText("feat-thread-list-ui (Thread list readability) shipped to main"),
    "Thread list readability · shipped to main",
  );
  assert.equal(
    T.threadTitleFromText("feat-nested（阶段二 (v2) 优化）已完成"),
    "阶段二 (v2) 优化 · 已完成",
  );
  assert.equal(
    T.threadTitleFromText("fix-flaky-test 跑完了，结果全绿"),
    "fix-flaky-test 跑完了，结果全绿",
    "worker name without parenthetical topic must be preserved",
  );
  assert.equal(
    T.threadTitleFromText("已开好并回查生效（权限直授）：确认无误"),
    "已开好并回查生效（权限直授）：确认无误",
    "Chinese prefix with parentheses must not be stripped",
  );
  assert.equal(
    T.threadTitleFromText("Collie（或者其他工具）支持多端"),
    "Collie（或者其他工具）支持多端",
    "normal capitalized English noun with parentheses must not be stripped",
  );
  assert.equal(
    T.threadTitleFromText("real-time (beta) mode is slow"),
    "real-time (beta) mode is slow",
    "ordinary hyphenated English word + short tag must not be stripped",
  );
  assert.equal(
    T.threadTitleFromText("**普通中文标题** 带 \x60code\x60、~~旧文案~~ 与 [发布说明](https://example.com/rel)"),
    "普通中文标题 带 code、旧文案 与 发布说明",
    "non-worker title strips redundant Markdown symbols cleanly",
  );
  assert.equal(T.threadTitleFromText("   "), "话题线");

  // 8b) Latest-reply preview & pending light dot in #collie-threads-list
  const pTs = (m) => new Date(now - m * 60000).toISOString();
  const popEntries = [
    // Thread 1: worker-prefixed root, ends with Collie options (pending -> light dot)
    { id: "p1", role: "collie", text: "\x60feat-unread-anchor\x60（未读自动定位与分割线）已准备就绪，等待确认发版。", at: pTs(30) },
    { id: "p2", role: "user", text: "手机端和桌面端都验过了吗？", at: pTs(28), re: { entry: "p1", who: "Collie", text: "未读自动定位与分割线" } },
    { id: "p3", role: "collie", text: "都验完了：**四象限全绿**，要现在切 \x60v0.2.71\x60 发版吗？", at: pTs(2), options: ["直接发版", "再看看"], re: { entry: "p2", who: "你", text: "手机端和桌面端都验过了吗？" } },

    // Thread 2: ordinary Chinese root, ends with Collie check=true (pending -> light dot)
    { id: "p10", role: "user", text: "帮我检查一下夜间定时任务日志。", at: pTs(25) },
    { id: "p11", role: "collie", text: "已查完，全部正常。", at: pTs(24), re: { entry: "p10", who: "你", text: "帮我检查一下夜间定时任务日志。" } },
    { id: "p12", role: "collie", text: "顺带发现一处重试阈值偏高，你空了确认一眼。", at: pTs(5), check: true, re: { entry: "p11", who: "Collie", text: "已查完" } },

    // Thread 3: worker-prefixed root, ends with user reply (settled -> NO dot, preview starts with 你：)
    { id: "p20", role: "collie", text: "eval-bench-suite（夜间基准评测）跑出结果了，P99 下降 22%。", at: pTs(20) },
    { id: "p21", role: "user", text: "太棒了，贴一下对比图表。", at: pTs(18), re: { entry: "p20", who: "Collie", text: "夜间基准评测" } },
    { id: "p22", role: "collie", text: "图表已附上，还要继续跑吗？", at: pTs(15), options: ["继续", "先停"], re: { entry: "p21", who: "你", text: "贴一下对比图表" } },
    { id: "p23", role: "user", text: "先停吧，**结论够清楚了**\n\n附件：\n/tmp/uploads/001-summary.png", at: pTs(10), re: { entry: "p22", who: "Collie", text: "图表已附上" } },

    // Thread 4: ends with Collie options that were ALREADY resolved/dismissed (not in sup.pending -> NO dot)
    { id: "p30", role: "user", text: "清理一下旧缓存脚本。", at: pTs(19) },
    { id: "p31", role: "collie", text: "找到 3 个废弃脚本。", at: pTs(17), re: { entry: "p30", who: "你", text: "清理一下旧缓存脚本。" } },
    { id: "p32", role: "collie", text: "要直接删除吗？", at: pTs(14), options: ["删除", "归档"], re: { entry: "p31", who: "Collie", text: "找到 3 个废弃脚本。" } },
  ];

  T.resetChat();
  T.setView("collie");
  T.setSup({
    router: { status: "idle", alive: true },
    entries: popEntries,
    workers: [],
    pending: [
      { id: "p3", at: pTs(2), summary: "都验完了，要现在切 v0.2.71 发版吗？", options: ["直接发版", "再看看"] },
      { id: "p12", at: pTs(5), summary: "顺带发现一处重试阈值偏高", check: true },
    ],
  });
  T.renderSuper({ force: true });

  const popListHtml = getEl("#collie-threads-list").innerHTML;
  const popItems = popListHtml.match(/<button[\s\S]*?<\/button>/g) || [];
  assert.equal(popItems.length, 4, "all 4 threads rendered in #collie-threads-list");

  const findPopItem = (rootId) => popItems.find((h) => h.includes('data-th-pick="' + rootId + '"')) || "";
  const item1 = findPopItem("p1");
  const item2 = findPopItem("p10");
  const item3 = findPopItem("p20");
  const item4 = findPopItem("p30");

  // Thread 1: stripped worker prefix + Collie preview + pending dot
  assert.ok(item1.includes('<div class="th-pop-title"'), "item1 has title");
  assert.ok(item1.includes(">未读自动定位与分割线 · 已准备就绪，等待确认发版</div>"), "item1 stripped worker prefix cleanly: " + item1);
  assert.ok(item1.includes('<div class="th-pop-preview" title="Collie：都验完了：四象限全绿，要现在切 v0.2.71 发版吗？">Collie：都验完了：四象限全绿，要现在切 v0.2.71 发版吗？</div>'), "item1 renders cleaned Collie latest-reply preview: " + item1);
  assert.ok(item1.includes("is-pending") && item1.includes('data-th-pending="1"') && item1.includes('class="th-pop-dot"'), "item1 shows pending dot for unresolved options");

  // Thread 2: check=true pending dot
  assert.ok(item2.includes("is-pending") && item2.includes('class="th-pop-dot"'), "item2 shows pending dot for unresolved check");
  assert.ok(item2.includes("Collie：顺带发现一处重试阈值偏高，你空了确认一眼。"), "item2 renders Collie check preview");

  // Thread 3: stripped worker prefix + user preview (strips attachment trailer & bold) + NO dot
  assert.ok(item3.includes(">夜间基准评测 · 跑出结果了，P99 下降 22%</div>"), "item3 stripped worker prefix cleanly: " + item3);
  assert.ok(item3.includes("你：先停吧，结论够清楚了"), "item3 renders cleaned user latest-reply preview");
  assert.ok(!item3.includes("is-pending") && !item3.includes("th-pop-dot"), "settled user-ended thread has NO pending dot");

  // Thread 4: dismissed/resolved options (absent from sup.pending) -> NO dot
  assert.ok(!item4.includes("is-pending") && !item4.includes("th-pop-dot"), "resolved options thread absent from sup.pending has NO pending dot");

  // 8c) Dismissing p12 immediately clears its pending dot in #collie-threads-list
  await T.dismissPending("p12");
  const afterDismissItems = (getEl("#collie-threads-list").innerHTML.match(/<button[\s\S]*?<\/button>/g) || []);
  const item2After = afterDismissItems.find((h) => h.includes('data-th-pick="p10"')) || "";
  assert.ok(!item2After.includes("is-pending") && !item2After.includes("th-pop-dot"), "dismissing p12 immediately removes pending dot");

  // 8d) Alias markers (opts / buttons / pending) work cleanly when sup.pending is absent
  const aliasEntries = [
    { id: "a1", role: "user", text: "对比一下两个方案。", at: pTs(11) },
    { id: "a2", role: "collie", text: "方案 A 更轻，方案 B 更稳。", at: pTs(10), re: { entry: "a1", who: "你", text: "对比一下两个方案。" } },
    { id: "b1", role: "user", text: "看下另一个分支。", at: pTs(9) },
    { id: "b2", role: "collie", text: "已经整理好了。", at: pTs(8), re: { entry: "b1", who: "你", text: "看下另一个分支。" } },
    { id: "c1", role: "user", text: "第三条线也确认一下。", at: pTs(7) },
    { id: "c2", role: "collie", text: "已就绪。", at: pTs(6), re: { entry: "c1", who: "你", text: "第三条线也确认一下。" } },
    { id: "c3", role: "collie", text: "等你点头就开始。", at: pTs(5), pending: true, re: { entry: "c2", who: "Collie", text: "已就绪。" } },
    { id: "b3", role: "collie", text: "等你确认后合并。", at: pTs(3), opts: ["确认合并"], re: { entry: "b2", who: "Collie", text: "已经整理好了。" } },
    { id: "a3", role: "collie", text: "请拍板用哪个？", at: pTs(1), buttons: [{ label: "用方案 A" }, { label: "用方案 B" }], re: { entry: "a2", who: "Collie", text: "方案 A 更轻" } },
  ];
  T.setSup({ router: { status: "idle", alive: true }, entries: aliasEntries, workers: [] });
  const aliasInfo = T.computeReplyThreads(aliasEntries);
  assert.equal(T.isThreadPending(aliasInfo.threads.get("a1"), aliasEntries), true, "buttons alias marks thread pending");
  assert.equal(T.isThreadPending(aliasInfo.threads.get("b1"), aliasEntries), true, "opts alias marks thread pending");
  assert.equal(T.isThreadPending(aliasInfo.threads.get("c1"), aliasEntries), true, "pending flag alias marks thread pending");

  // 8e) CSS contracts for popover preview & pending dot alignment
  assert.ok(appCss.includes(".th-pop-preview"), "CSS defines .th-pop-preview");
  assert.ok(appCss.includes(".th-pop-dot"), "CSS defines .th-pop-dot");
  assert.ok(appCss.includes(".th-pop-list:has(.th-pop-item.is-pending)"), "CSS aligns gutter via :has() when pending items exist");
  assert.ok(appCss.includes("@media (forced-colors: active)"), "CSS supports Forced Colors Mode for pending dot");

  console.log("OK: all reply-thread, settings, and notification JS tests passed");
})().catch((err) => {
  console.error(err);
  process.exit(1);
});
`
	cmd := exec.Command(nodeBin, "-e", jsTest, root)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("node JS test failed: %v\n%s", err, string(out))
	}
}
