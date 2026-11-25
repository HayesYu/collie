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

for (const [formSel, inputSel] of [["#super-composer", "#super-input"], ["#composer", "#input"], ["#x-composer", "#x-input"]]) {
  const f = getEl(formSel);
  f._qs["textarea"] = getEl(inputSel);
  f._qs[".send"] = makeEl(formSel + " .send");
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
  URL,
  URLSearchParams,
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
ctx.fetch = async (url, init = {}) => {
  const u = String(url);
  fetchCalls.push({ url: u, method: init.method || "GET", body: init.body });
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
  "  globalThis.__test = { getSup: () => sup, setSup: (s) => { sup = normSup(s); }, setView: (v) => { view = v; }, connect, renderSuper, computeReplyThreads, openThreadDrawer, closeThreadDrawer, getActiveThreadRoot: () => activeThreadRoot, settingsHTML, renderPrefs, notifyEnvState, syncCollieNotifyWatermark, sendTestNotification };\n})();"
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

  // ─── 2. Open drawer, verify contents, IME-safe + reentrancy-safe send chaining onto user message e14, and live Collie SSE sync ───
  T.openThreadDrawer("e10");
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

  // Esc closes thread drawer
  doc.dispatchDoc("keydown", { key: "Escape", isComposing: false, keyCode: 27 });
  assert.equal(T.getActiveThreadRoot(), null, "Esc closes thread drawer");
  assert.ok(!getEl("#app").classList.contains("has-thread"));

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
