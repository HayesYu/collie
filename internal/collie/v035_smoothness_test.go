package collie

import (
	"os/exec"
	"path/filepath"
	"testing"
)

func TestWebV035SmoothnessAndLayoutContracts(t *testing.T) {
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

// ─── 1. Static CSS / HTML Contracts for all 10 v0.3.5 items ───
// 1) P0-1: --thread-w non-inheriting @property, position:fixed .threadpane GPU glide, 1180px iPad overlay breakpoint
assert.ok(cssSrc.includes("@property --thread-w") && cssSrc.includes("inherits: false"), "P0-1: --thread-w must register as non-inheriting @property");
assert.ok(!/\.app\s*\{[^}]*transition:\s*grid-template-columns/.test(cssSrc), "P0-1: .app must not animate grid-template-columns");
assert.ok(!/\.td-inner\s*\{[^}]*transition:\s*width/.test(cssSrc), "P0-1: .td-inner must not animate width");
assert.ok(cssSrc.includes("@media (max-width: 1180px)") && cssSrc.includes("@media (min-width: 1181px)"), "P0-1: iPad overlay breakpoint raised to 1180px");
assert.ok(cssSrc.includes("padding-right: var(--thread-top-pr, 0px);"), "P0-1: .collie-top reserves right clearance locally without touching #super-thread");

// 2) P0-2: content-visibility: hidden parked #v-collie (zero visibility:hidden / zero .is-parked .dot rules) + mobile 1x1 grid
assert.ok(cssSrc.includes("#v-collie.is-parked") && cssSrc.includes("content-visibility: hidden;"), "P0-2: parked #v-collie must use content-visibility: hidden");
assert.ok(!cssSrc.includes("#v-collie.is-parked .dot"), "P0-2: must remove #v-collie.is-parked .dot descendant pause rules that trigger unpark recalc");
assert.ok(!appSrc.includes("col.inert = true"), "P0-2: must never set inert on #v-collie (causes full-tree IsInert style recalc)");
assert.ok(cssSrc.includes(".app { grid-template-columns: minmax(0, 1fr); grid-template-rows: 1fr; }"), "P0-2: mobile uses stacked 1x1 grid");
assert.ok(!/\.app:not\(\.in-detail\) \.main\s*\{\s*display:\s*none;/.test(cssSrc), "P0-2: mobile must not destroy .main render tree via display:none");

// 4) P0-4 + Unified left-bias desktop reading preference fix (all widths including >=1664px / 1920px)
assert.ok(
  cssSrc.includes("--notes-shift-abs: min(\n      calc(var(--notes-reserve) / 2),\n      max(0px, calc(var(--notes-free) - 20px))\n    );"),
  "P0-4: unified left-bias formula must center in remaining left space at all desktop widths",
);
assert.ok(!/#v-collie \.thread,\s*#v-collie \.working-row,\s*#v-collie \.composer\s*\{[^}]*transition:\s*transform/.test(cssSrc), "P0-4: must not put CSS transition: transform on 40k-node .thread");

// 5) P1-1 / P1-2: Zero backdrop-filter blur on drawer scrims & super-jump, no layout/paint transitions on .notepane
for (const sel of [".notes-scrim", ".file-scrim", ".thread-scrim", ".super-jump"]) {
  const re = new RegExp(sel.replace(".", "\\.") + "\\s*\\{[^}]*backdrop-filter:");
  assert.ok(!re.test(cssSrc), "P1-1: " + sel + " must not use backdrop-filter blur");
}
assert.ok(!/dialog\.(?:lightbox|mem-io-sheet|tm-sheet|int-sheet)::backdrop\s*\{[^}]*transition:[^;]*backdrop-filter/.test(cssSrc), "P1-1: dialog::backdrop must not animate backdrop-filter");

// 7) External user quote bar above bubble
assert.ok(cssSrc.includes("#v-collie .mine > .re") && cssSrc.includes("align-self: flex-end;"), "Item 7: external right-aligned user quote bar CSS must exist");
assert.ok(!cssSrc.includes(".msg.user .re {"), "Item 7: legacy in-bubble .msg.user .re styles must be replaced");

// 8) Crew strip readability & zero hover expand
assert.ok(cssSrc.includes(".chip {") && cssSrc.includes("flex: 0 0 auto;"), "Item 8: crew chips must use flex: 0 0 auto");
assert.ok(cssSrc.includes("max-inline-size: 184px;"), "Item 8: crew chip names keep comfortable readable width");
assert.ok(!cssSrc.includes(".crew.is-tight") && !cssSrc.includes("--crew-chip-max"), "Item 8: remove over-truncating .is-tight / --crew-chip-max");
assert.ok(!cssSrc.includes(".chip:hover .chip-name") && !/\.chip\s*\{[^}]*interpolate-size/.test(cssSrc), "Item 8: remove hover width-expansion animation on crew chips");

// 10) De-colliding labels: left sidebar 工作中 vs notebook section-1 进行中 / 话题 + folded 历史话题
assert.ok(htmlSrc.includes('<h2>工作中 <span class="count" id="live-count"></span></h2>'), "Item 10: left sidebar uses 工作中");
assert.ok(!htmlSrc.includes('<h2>进行中 <span class="count" id="live-count"></span></h2>'), "Item 10: left sidebar no longer uses 进行中");
assert.ok(htmlSrc.includes('>进行中</button>') && htmlSrc.includes('>话题</span>') && appSrc.includes('>历史话题</h3>'), "Item 10: section 1 uses inline 进行中/话题 switcher and folded 历史话题");
// 11) Zero view-switch jitter on #notes-pane (#t12442): no always-replaying np-swap-in animation
assert.ok(!cssSrc.includes("np-swap-in"), "Item 11: remove np-swap-in keyframe that replayed opacity/translateY jitter on route return");

// ─── 2. Runtime VM Tests ───
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
let rectReads = 0;
let replaceChildCalls = 0;
function makeEl(id = "", tagName = "DIV") {
  const listeners = new Map();
  const attrs = new Map();
  const styles = new Map();
  const el = {
    id, tagName, value: "", textContent: "", innerHTML: "",
    hidden: false, disabled: false, checked: false, open: false, inert: false, tabIndex: 0,
    className: "", title: "", href: "",
    style: {
      transform: "",
      setProperty(k, v) { styles.set(k, String(v)); },
      getPropertyValue(k) { return styles.get(k) || ""; },
      removeProperty(k) { styles.delete(k); },
    },
    dataset: {}, classList: makeClassList(), children: [], selectedOptions: [], options: [],
    scrollTop: 0, scrollHeight: 1200, clientHeight: 600, scrollWidth: 400, clientWidth: 400,
    offsetWidth: 300, offsetHeight: 98, offsetParent: {}, isConnected: true,
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
    setAttribute(k, v) { attrs.set(k, String(v)); if (k === "href") el.href = String(v); if (k === "title") el.title = String(v); },
    getAttribute(k) { return attrs.get(k) ?? ""; },
    removeAttribute(k) { attrs.delete(k); },
    hasAttribute(k) { return attrs.has(k); },
    closest() { return null; }, contains() { return false; },
    append() {}, appendChild(c) { c.parentNode = el; el.children.push(c); return c; },
    replaceChild(n, o) { replaceChildCalls++; const i = el.children.indexOf(o); if (i >= 0) el.children[i] = n; n.parentNode = el; return o; },
    insertBefore(n, ref) { n.parentNode = el; const i = ref ? el.children.indexOf(ref) : -1; if (i >= 0) el.children.splice(i, 0, n); else el.children.push(n); return n; },
    removeChild(c) { const i = el.children.indexOf(c); if (i >= 0) el.children.splice(i, 1); c.parentNode = null; return c; },
    remove() { if (el.parentNode) el.parentNode.removeChild(el); },
    after() {}, focus() {}, click() {}, scrollIntoView() {},
    showModal() { el.open = true; }, close() { el.open = false; },
    getBoundingClientRect() { rectReads++; return { top: 0, bottom: 40, left: 0, right: 300, width: 300, height: 40 }; },
  };
  Object.defineProperty(el, "firstElementChild", { get() { return el.children[0] || null; } });
  Object.defineProperty(el, "nextElementSibling", {
    get() {
      if (!el.parentNode) return null;
      const i = el.parentNode.children.indexOf(el);
      return i >= 0 ? (el.parentNode.children[i + 1] || null) : null;
    },
  });
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
getEl("#thread-drawer").hidden = true;
getEl("#thread-scrim").hidden = true;
getEl(".side").offsetWidth = 300;

const docListeners = new Map();
const storage = new Map();
const doc = {
  documentElement: makeEl("html"),
  body: makeEl("body"),
  hidden: false,
  hasFocus: () => true,
  createElement: (tag) => makeEl(tag, tag.toUpperCase()),
  createDocumentFragment: () => makeEl("frag", "FRAGMENT"),
  getElementById: (id) => getEl("#" + id),
  querySelector: (sel) => (sel === "dialog[open]" ? null : getEl(sel)),
  querySelectorAll: () => [],
  addEventListener(type, fn) { if (!docListeners.has(type)) docListeners.set(type, []); docListeners.get(type).push(fn); },
  removeEventListener() {},
  dispatch(type, ev = {}) {
    const e = { type, target: makeEl("doc-target"), defaultPrevented: false, preventDefault() { this.defaultPrevented = true; }, stopPropagation() {}, composedPath: () => [], ...ev };
    for (const fn of [...(docListeners.get(type) || [])]) fn(e);
    return e;
  },
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
  isSecureContext: true, innerWidth: 1920, innerHeight: 1080,
  visualViewport: { height: 1080, offsetTop: 0, addEventListener() {} },
  CSS: { escape: (s) => String(s), supports: () => true },
  HTMLDialogElement: { prototype: {} },
  Blob, Uint8Array, TextEncoder, atob, AbortController,
  crypto: { randomUUID: () => "v035-uuid" },
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
  getComputedStyle: (el) => ({ getPropertyValue: (k) => el?.style?.getPropertyValue?.(k) || "" }),
  getSelection: () => ({ isCollapsed: true, toString: () => "" }),
  addEventListener() {}, removeEventListener() {}, dispatchEvent() {},
  fetch: async () => ({ ok: true, status: 200, json: async () => ({ ok: true }) }),
};
ctx.window = ctx;
ctx.globalThis = ctx;

const marker = "  boot();\n})();";
const instrumented = appSrc.replace(
  marker,
  "  globalThis.__test = { setView: (v) => { view = v; }, setSup: (s) => { sup = normSup(s); }, renderSuper, computeCollieShiftPx, syncNotebookVisibility, setThreadsExpanded, getThreadsExpanded: () => threadsExpanded, openThreadDrawer, closeThreadDrawer, setCollieParked, promoteEventInPlace, promoteCollieNodeInPlace, promoteUserNodeInPlace, parseSuperNode };\n})();"
);
vm.runInNewContext(instrumented, ctx, { filename: "web/app.js" });
const T = ctx.__test;

// 2a) P0-4 unified left-bias computation across 1440px and 1920px (never 0px on 1920px!)
ctx.innerWidth = 1920;
assert.equal(T.computeCollieShiftPx(280), -140, "1920px with side card must shift -140px (optical center in remaining left space)");
assert.equal(T.computeCollieShiftPx(432), -216, "1920px with 432px thread drawer must shift -216px");
ctx.innerWidth = 1440;
assert.equal(T.computeCollieShiftPx(280), -140, "1440px with side card must shift -140px");
assert.equal(T.computeCollieShiftPx(432), -160, "1440px with 432px thread drawer shifts -160px clamped to 20px left gutter");

// 2b) P0-2 setCollieParked never sets inert on #v-collie, and switching away/back on wide desktop freezes #notes-pane with 0 is-hidden churn (#t12442)
ctx.innerWidth = 1440;
T.setView("collie");
T.setCollieParked(false);
T.syncNotebookVisibility({ immediate: true });
assert.equal(getEl("#notes-pane").classList.contains("is-hidden"), false, "notes pane open on wide desktop");
assert.equal(getEl("#v-collie").classList.contains("has-notes"), true, "collie has-notes slot active");
T.setView("prefs");
T.setCollieParked(true);
T.syncNotebookVisibility({ immediate: true });
assert.equal(getEl("#v-collie").classList.contains("is-parked"), true);
assert.equal(getEl("#v-collie").inert, false, "P0-2: parked #v-collie must NEVER set inert=true");
assert.equal(getEl("#notes-pane").classList.contains("is-hidden"), false, "Item 11: leaving #/collie keeps #notes-pane frozen in-place inside parked #v-collie (zero transition replay on return)");
assert.equal(getEl("#v-collie").classList.contains("has-notes"), true, "Item 11: leaving #/collie keeps #v-collie.has-notes frozen inside parked view");
assert.equal(doc.body.classList.contains("collie-has-notes"), false, "leaving #/collie still clears body.collie-has-notes for toasts");
T.setView("collie");
T.syncNotebookVisibility({ immediate: true });
T.setCollieParked(false);
assert.equal(getEl("#notes-pane").classList.contains("is-hidden"), false, "returning to #/collie reveals #notes-pane immediately in-place");
assert.equal(doc.body.classList.contains("collie-has-notes"), true, "returning to #/collie restores body.collie-has-notes");

// 2c) Item 7 (external detached quote bar above all user messages) + Item 9 (consecutive Collie cross-topic / post-thread-entry break)
const now = Date.now();
const ts = (m) => new Date(now - m * 60000).toISOString();
const entries = [
  // Topic A: 3-msg thread ending on Collie message a3 (which displays ↳ 2 条回复)
  { id: "a1", role: "user", text: "这是一条非常长的原始问题，用来测试用户短回复时引用条放在深色气泡正上方外部而不会把下方短气泡撑出大片黑底。", at: ts(20) },
  { id: "a2", role: "collie", text: "话题 A 第一条回复，先把根因列出来。", at: ts(19), re: { entry: "a1", who: "你", text: "这是一条非常长的原始问题" } },
  { id: "a2b", role: "collie", text: "同话题 A 紧接着补一句，应该合并隐藏头像。", at: ts(18.8), re: { entry: "a2", who: "Collie", text: "话题 A 第一条回复" } },
  { id: "a3", role: "user", text: "好的", at: ts(18), re: { entry: "a1", who: "你", text: "这是一条非常长的原始问题，用来测试用户短回复时引用条放在深色气泡正上方外部而不会把下方短气泡撑出大片黑底。" } },
  { id: "a4", role: "collie", text: "话题 A 最终确认并挂出话题入口。", at: ts(17), re: { entry: "a3", who: "你", text: "好的" } },
  // Immediately following Collie message (even without quote) after a4 (which has ↳ 3 条回复) -> MUST NOT merge (.cont = false)!
  { id: "s1", role: "collie", text: "紧跟在带回复入口消息后面的新消息，应独立带头像。", at: ts(16.5) },
  // Immediately following Collie message with cross-topic quote -> MUST NOT merge (.cont = false)!
  { id: "b0", role: "user", text: "话题 B：另外帮我查一下图表导出。\n\n附件：\n/tmp/chart.png", at: ts(25) },
  { id: "b1", role: "collie", text: "话题 B 汇报：图表导出已修复。", at: ts(16), re: { entry: "b0", who: "你", text: "话题 B：另外帮我查一下图表导出。" } },
  // User media+text with quote + user media-only with quote
  { id: "u_mix", role: "user", text: "看这张\n\n附件：\n/tmp/mix.png", at: ts(15), re: { entry: "a1", who: "你", text: "这是一条非常长的原始问题" } },
  { id: "u_img", role: "user", text: "附件：\n/tmp/only.png", at: ts(14), re: { entry: "a1", who: "你", text: "这是一条非常长的原始问题" } },
];
// Order chronologically so b0 is earlier
entries.sort((x, y) => +new Date(x.at) - +new Date(y.at));

T.setSup({ router: { status: "idle", alive: true }, entries, workers: [] });
T.renderSuper({ force: true });
const html = getEl("#super-thread").innerHTML;

// Item 7 assertions: quote <a class="re"> MUST appear OUTSIDE <div class="msg user"> in all 3 user message variants!
const mineA3 = html.match(/<div class="mine has-quote"[^>]*>([\s\S]*?)data-id="a3"[\s\S]*?<\/div><\/div>/);
assert.ok(mineA3, "text-only quoted user message has .mine.has-quote");
const a3ReIdx = html.indexOf('data-goto="a1"');
const a3MsgIdx = html.indexOf('<div class="msg user" data-id="a3"');
assert.ok(a3ReIdx > 0 && a3ReIdx < a3MsgIdx, "Item 7: text-only user quote sits ABOVE <div class=\"msg user\">");
const a3Inner = html.match(/<div class="msg user" data-id="a3"[^>]*>([\s\S]*?)<\/div>/)?.[1] || "";
assert.ok(!a3Inner.includes('class="re"'), "Item 7: text-only user dark bubble must NOT contain .re inside");

const mixStart = html.indexOf('<div class="mine has-media has-quote"');
assert.ok(mixStart >= 0, "Item 7: media+text quoted user message has .mine.has-media.has-quote");
const mixSlice = html.slice(mixStart, html.indexOf('data-id="u_mix"') + 60);
assert.ok(mixSlice.indexOf('<a class="re"') < mixSlice.indexOf('<div class="mine-media'), "Item 7: quote sits ABOVE .mine-media in media+text message");

const imgStart = html.indexOf('<div class="mine is-media-only has-quote"');
assert.ok(imgStart >= 0, "Item 7: media-only quoted user message has .mine.is-media-only.has-quote");
const imgSlice = html.slice(imgStart, html.indexOf('data-id="u_img"') + 60);
assert.ok(imgSlice.indexOf('<a class="re"') < imgSlice.indexOf('<div class="msg user is-media-only"'), "Item 7: quote sits ABOVE .msg.user.is-media-only");

// Item 9 assertions:
// a2b follows same-topic a2 (no thread entry on a2, no new quote) -> MUST merge (.say cont)
assert.ok(/<div class="say cont"[^>]*><span class="avatar"><\/span><div class="msg assistant" data-id="a2b"/.test(html), "Item 9: same-topic follow-up a2b merges with .say.cont and hides avatar");
// s1 follows a4 (which carries ↳ N 条回复 thread entry) -> MUST break merge (standalone .say with avatar)
assert.ok(/<div class="say"[^>]*><span class="avatar"><img src="\/icon\.svg" alt=""><\/span><div class="msg assistant" data-id="s1"/.test(html), "Item 9: message s1 after thread-entry a4 breaks merge and shows Collie avatar");
// b1 follows s1 with a cross-topic quote to b0 -> MUST break merge (standalone .say with avatar)
assert.ok(/<div class="say"[^>]*><span class="avatar"><img src="\/icon\.svg" alt=""><\/span><div class="msg assistant" data-id="b1"/.test(html), "Item 9: cross-topic quoted b1 breaks merge and shows Collie avatar");

// 2d) Item 6: threads card expands without rebuilding #collie-threads-list innerHTML
const thListEl = getEl("#collie-threads-list");
let thWrites = 0;
let thHtmlVal = thListEl.innerHTML;
Object.defineProperty(thListEl, "innerHTML", {
  get: () => thHtmlVal,
  set: (v) => { thWrites++; thHtmlVal = v; },
  configurable: true,
});
getEl("#notes-pane").setAttribute("data-side-mode", "threads");
T.setThreadsExpanded(true);
T.setThreadsExpanded(false);
assert.equal(thWrites, 0, "Item 6: expanding/collapsing 话题线 card must perform ZERO innerHTML rebuilds");

// 2e) P0-3: promoteEventInPlace updates dot & wstate in-place with zero replaceChild and layoutChanged=false when wstate stays present
const evEl = makeEl("ev1", "A");
const dotEl = makeEl("dot", "SPAN");
dotEl.className = "dot running";
const wsEl = makeEl("ws", "SPAN");
wsEl.className = "wstate";
wsEl.textContent = "还在跑";
evEl._qs[".dot"] = dotEl;
evEl._qs[".wstate"] = wsEl;
evEl.href = "#/p/w1";
evEl.setAttribute("href", "#/p/w1");
replaceChildCalls = 0;
const evRes = T.promoteEventInPlace(evEl, {
  canPromoteEventInPlace: true,
  key: "e_ev",
  sig: "next-sig",
  tagName: "a",
  cls: "event",
  href: "#/p/w1",
  title: "",
  tone: "done",
  wstate: "已结束",
});
assert.ok(evRes && evRes.layoutChanged === false, "P0-3: updating event dot/wstate in-place reports layoutChanged=false");
assert.equal(dotEl.className, "dot done");
assert.equal(wsEl.textContent, "已结束");
assert.equal(replaceChildCalls, 0, "P0-3: in-place event update performs 0 replaceChild calls");

console.log("OK: all v0.3.5 smoothness & layout contracts passed");
`
	cmd := exec.Command("node", "-e", script)
	cmd.Dir = filepath.Join("..", "..")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("v0.3.5 smoothness checks failed: %v\n%s", err, string(out))
	}
}
