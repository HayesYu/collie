package collie

import (
	"os/exec"
	"path/filepath"
	"testing"
)

func TestWebV0294UIPolishAndReplyToGuard(t *testing.T) {
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

// 1) Static HTML & CSS contracts for v0.2.94
// 1a) #super-working aligns identically with #super-thread in wide notebook mode and mobile
assert.ok(cssSrc.includes("#v-collie.has-notes .thread,\n  #v-collie.has-notes .working-row"), "working-row must share exact padding with .thread under #v-collie.has-notes");
assert.ok(cssSrc.includes(".working-row { padding: 0 16px 18px; }"), "mobile working-row must match mobile .thread 16px side padding");

// 1b) Top bar has neither 22px fog veil nor 1px scrolled bottom hairline (seamless header into chat stream)
assert.ok(!cssSrc.includes(".collie-top::after"), ".collie-top::after must be removed so header stays seamless when static or scrolled");
assert.ok(!cssSrc.includes(".collie-top.is-scrolled"), ".collie-top.is-scrolled hairline must be removed");

// 1c) #crew readable horizontal strip: hides secondary agent kind on overflow, never crushes chip names with .is-tight
assert.ok(cssSrc.includes(".crew.is-no-kind .chip .k { display: none; }"), "crew tier 1 must hide secondary agent kind");
assert.ok(!cssSrc.includes(".crew.is-tight") && cssSrc.includes("max-inline-size: 184px"), "crew chips must preserve readable width and scroll horizontally instead of crushing");

// 1d) Whole-card clickable .snap-card without redundant .snap-open-btn box
assert.ok(!cssSrc.includes(".snap-open-btn"), "redundant .snap-open-btn button box must be removed");
assert.ok(cssSrc.includes(".snap-chev"), "snap-card must render subtle chevron cue");
assert.ok(cssSrc.includes(".snap-titles { display: flex; flex-direction: column;"), "snap-titles span wrapper must stack title and meta vertically");

// 1e) Smooth compositor-only .reply-receipt disclosure (zero ::details-content block-size reflow, zero inline-block width snap)
assert.ok(cssSrc.includes(".reply-receipt {\n  display: block;"), "reply-receipt outer <details> must be display: block to prevent width jump");
assert.ok(cssSrc.includes(".reply-receipt > summary {\n  list-style: none;") && cssSrc.includes("display: inline-flex;"), "reply-receipt <summary> must keep compact inline-flex pill styling");
assert.ok(!cssSrc.includes(".reply-receipt::details-content"), "reply-receipt must not animate ::details-content block-size");
assert.ok(cssSrc.includes(".reply-receipt[open] > .rr-body { animation: rr-open .14s ease-out; }"), "reply-receipt must use pure compositor opacity+translateY fade-in");

// 1f) Notebook removes #np-expand pill, centers in right empty channel, isolates repaint, avoids body:has() & dynamic --notes-reserve
assert.ok(!htmlSrc.includes('id="np-expand"'), "notebook must not render #np-expand button");
assert.ok(cssSrc.includes("(100% - var(--np-col-right) - var(--notes-w)) * 0.55"), "notebook must center optically in the right channel");
assert.ok(!cssSrc.includes("body:has(#v-collie.has-notes)"), "top-level body:has(#v-collie.has-notes) selector must be removed");
assert.ok(cssSrc.includes("body.collie-has-notes .toasts"), "toasts offset must use lightweight body.collie-has-notes class");
assert.ok(cssSrc.includes("contain: layout style paint;"), ".notepane must isolate internal repaints via contain: layout style paint");
assert.ok(!cssSrc.includes("transition: padding .26s"), "thread/composer must not run 260ms frame-by-frame padding reflow animation");
assert.ok(appSrc.includes('"np-tasks-list", "np-files-list", "np-timers-list"'), "notebook lists must be excluded from global MutationObservers");

// 2) Runtime VM checks
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
  const styles = new Map();
  const el = {
    id, tagName: "DIV", value: "", textContent: "", innerHTML: "",
    hidden: false, disabled: false, checked: false, open: false,
    className: "", title: "",
    style: {
      setProperty(k, v) { styles.set(k, String(v)); },
      getPropertyValue(k) { return styles.get(k) || ""; },
      removeProperty(k) { styles.delete(k); },
    },
    dataset: {}, classList: makeClassList(), children: [], selectedOptions: [], options: [],
    scrollTop: 0, scrollHeight: 200, clientHeight: 200, scrollWidth: 400, clientWidth: 400,
    offsetWidth: 400, offsetHeight: 98, offsetParent: {}, isConnected: true,
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
getEl("#super-search").hidden = true;
getEl("#collie-menu").hidden = true;

const docListeners = new Map();
const storage = new Map();
const doc = {
  documentElement: makeEl("html"),
  body: makeEl("body"),
  hidden: false,
  hasFocus: () => true,
  createElement: (tag) => makeEl(tag),
  getElementById: (id) => getEl("#" + id),
  querySelector: (sel) => (sel === "dialog[open]" ? null : getEl(sel)),
  querySelectorAll: (sel) => (sel === ".menu" ? [getEl("#collie-menu")] : []),
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
  isSecureContext: true, innerWidth: 1728, innerHeight: 1000,
  visualViewport: { height: 1000, offsetTop: 0, addEventListener() {} },
  CSS: { escape: (s) => String(s), supports: () => true },
  HTMLDialogElement: { prototype: {} },
  Blob, Uint8Array, TextEncoder, atob, AbortController,
  crypto: { randomUUID: () => "v0294-uuid" },
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
  getSelection: () => ({ isCollapsed: true, toString: () => "" }),
  addEventListener() {}, removeEventListener() {}, dispatchEvent() {},
  fetch: async () => ({ ok: true, status: 200, json: async () => ({ ok: true }) }),
};
ctx.window = ctx;
ctx.globalThis = ctx;

const marker = "  boot();\n})();";
const instrumented = appSrc.replace(
  marker,
  "  globalThis.__test = { sup, syncNotebookVisibility, renderNotebook, setNotesExpanded, getNotesExpanded: () => notesExpanded, setNotebookData: (d) => { notebookData = d; }, setView: (v) => { view = v; }, syncCrewCompact, renderEntryArtifacts, replyOptsMarkup, setReply, getReplyTo: () => replyTo, reconcileStaleReplyTo, openThreadDrawer, route };\n})();"
);
vm.runInNewContext(instrumented, ctx, { filename: "web/app.js" });
const T = ctx.__test;

// 2a) Mutual exclusion: opening threads popover, search, or more menu yields floating notebook while keeping slot stable; closing restores it
T.setView("collie");
T.setNotebookData({ date: "2026-11-27", tasks: [{ id: "t1", title: "任务 A", note: "进展", tone: "active" }], artifacts: [], timers: [] });
T.syncNotebookVisibility();
assert.equal(getEl("#notes-pane").classList.contains("is-hidden"), false, "notebook visible initially on wide desktop");
assert.equal(getEl("#v-collie").classList.contains("has-notes"), true);
assert.equal(doc.body.classList.contains("collie-has-notes"), true, "body.collie-has-notes must sync with notebook slot");

// Switch section 1 in-place via inline #np-tab-threads -> stays open, swaps ONLY section 1 panel while #np-body (files & timers) stays visible
getEl("#np-tab-threads").dispatch("click");
assert.equal(getEl("#notes-pane").classList.contains("is-hidden"), false, "switching to threads keeps unified side card open");
assert.equal(getEl("#notes-pane").getAttribute("data-side-mode"), "threads");
assert.equal(getEl("#np-panel-threads").hidden, false);
assert.equal(getEl("#np-panel-tasks").hidden, true);
assert.equal(getEl("#np-body").hidden, false, "switching to threads must keep #np-body (今日产物 & 下一条定时) completely untouched");
assert.equal(getEl("#v-collie").classList.contains("has-notes"), true, "chat slot stays stable when switching side card modes");
getEl("#open-notes-btn").dispatch("click");
assert.equal(getEl("#notes-pane").classList.contains("is-hidden"), true, "clicking active 小本本 button closes unified side card");
assert.equal(getEl("#v-collie").classList.contains("has-notes"), false, "closing side card resets chat slot");
getEl("#open-notes-btn").dispatch("click");
assert.equal(getEl("#notes-pane").classList.contains("is-hidden"), false, "clicking 小本本 reopens unified side card");
getEl("#np-tab-notes").dispatch("click");
assert.equal(getEl("#notes-pane").getAttribute("data-side-mode"), "notes");
assert.equal(getEl("#np-panel-tasks").hidden, false);
assert.equal(getEl("#np-panel-threads").hidden, true);

// Open search -> notebook hides; close search -> notebook restores
getEl("#super-search-btn").dispatch("click");
assert.equal(getEl("#super-search").hidden, false);
assert.equal(getEl("#notes-pane").classList.contains("is-hidden"), true, "opening search must hide floating notebook");
assert.equal(getEl("#v-collie").classList.contains("has-notes"), true, "chat slot must stay stable when transient popover opens");
getEl("#super-search-close").dispatch("click");
assert.equal(getEl("#super-search").hidden, true);
assert.equal(getEl("#notes-pane").classList.contains("is-hidden"), false, "closing search must restore floating notebook");

// Open collie-menu -> notebook hides; Escape closes menu -> notebook restores
const menuOpener = makeEl("menu-opener");
menuOpener.dataset.menu = "collie-menu";
menuOpener.closest = (sel) => (sel === "[data-menu]" ? menuOpener : null);
doc.dispatch("click", { target: menuOpener });
assert.equal(getEl("#collie-menu").hidden, false);
assert.equal(getEl("#notes-pane").classList.contains("is-hidden"), true, "opening more menu must hide floating notebook");
doc.dispatch("keydown", { key: "Escape" });
assert.equal(getEl("#collie-menu").hidden, true);
assert.equal(getEl("#notes-pane").classList.contains("is-hidden"), false, "Escape closing more menu must restore floating notebook");

// 2b) #crew single-pass compacting: hides agent kind on overflow while keeping chips readable for horizontal scrolling (no .is-tight crush)
const crew = makeEl("crew-test");
crew.children = [{}, {}, {}, {}, {}, {}];
crew.clientWidth = 500;
Object.defineProperty(crew, "scrollWidth", {
  get() {
    if (crew.classList.contains("is-no-kind")) return 620;
    return 900;
  },
});
T.syncCrewCompact(crew);
assert.equal(crew.classList.contains("is-no-kind"), true, "overflowing crew must hide agent kind first");
assert.equal(crew.classList.contains("is-tight"), false, "overflowing crew must NOT crush chips with .is-tight");
assert.equal(crew.style.getPropertyValue("--crew-chip-max"), "", "crew must NOT dynamically crush chip width via --crew-chip-max");

// 2d) Whole-card clickable .snap-card without 分屏预览 button box
const snapHtml = T.renderEntryArtifacts({ id: "e90", role: "collie", text: "见 ~/work/prototype.html", files: ["~/work/prototype.html"] });
assert.ok(snapHtml.includes('<button type="button" class="snap-head" data-open-file="~/work/prototype.html"'), "snap-card head must be a clickable button opening the file");
assert.ok(!snapHtml.includes("snap-open-btn") && !snapHtml.includes("分屏预览"), "snap-card must not render redundant 分屏预览 button box");
assert.ok(snapHtml.includes("snap-chev"), "snap-card must include subtle chevron cue");

// 2e) Smooth compositor-only .reply-receipt markup puts .rr-body directly inside <details>
const pickedEntry = { id: "e91", role: "collie", text: "选哪个？", options: ["方案 A", "方案 B"], pick: { state: "picked", text: "方案 A", at: "2026-11-27T16:00:00Z" } };
T.sup.hasPending = true;
const recHtml = T.replyOptsMarkup(pickedEntry, 0, [pickedEntry], new Set());
assert.ok(recHtml.includes('</summary><div class="rr-body">'), "reply-receipt must place .rr-body directly inside <details> for .reply-receipt[open] > .rr-body compositor animation");

// 2f) Stale replyTo guard: cross-device reply, SSE pick, Escape key, and empty-composer view switch clear replyTo; non-empty draft preserves replyTo
T.sup.entries.push({ id: "e100", role: "collie", text: "patch-book-prompt-v2 已打包完毕", options: ["跑全量", "先不跑"] });
T.setReply(T.sup.entries[0]);
assert.equal(T.getReplyTo()?.entry, "e100");
// Escape on #super-input clears replyTo
getEl("#super-input").dispatch("keydown", { key: "Escape", isComposing: false, keyCode: 27 });
assert.equal(T.getReplyTo(), null, "Escape in #super-input must clear replyTo");

// Cross-device / Feishu user reply referencing e100 arriving via SSE clears stale replyTo
T.setReply(T.sup.entries[0]);
T.sup.entries.push({ id: "e101", role: "user", text: "好，跑全量", re: { entry: "e100", who: "Collie", text: "patch-book-prompt-v2" } });
T.reconcileStaleReplyTo(T.sup.entries);
assert.equal(T.getReplyTo(), null, "newer user reply to quoted entry via SSE must clear stale replyTo");

// SSE pick on quoted entry clears stale replyTo
T.sup.entries.push({ id: "e102", role: "collie", text: "要发布吗？", options: ["发版", "暂缓"] });
T.setReply(T.sup.entries[2]);
T.sup.entries[2].pick = { state: "picked", text: "发版", at: "2026-11-27T16:05:00Z" };
T.reconcileStaleReplyTo(T.sup.entries);
assert.equal(T.getReplyTo(), null, "SSE pick resolving quoted decision entry must clear stale replyTo");

// Opening thread drawer with empty composer clears stale replyTo; with typed draft preserves replyTo
T.sup.entries.push({ id: "e103", role: "collie", text: "另一条消息" });
T.sup.entries.push({ id: "e104", role: "user", text: "跟进", re: { entry: "e103", who: "Collie", text: "另一条消息" } });
T.setReply(T.sup.entries[3]);
getEl("#super-input").value = "";
T.openThreadDrawer("e103", "e104", "e104");
assert.equal(T.getReplyTo(), null, "opening thread drawer with empty main composer must clear stale replyTo");

T.setReply(T.sup.entries[3]);
getEl("#super-input").value = "我正在打字";
T.openThreadDrawer("e103", "e104", "e104");
assert.equal(T.getReplyTo()?.entry, "e103", "opening thread drawer must preserve replyTo when main composer has typed draft");

console.log("OK: all v0.2.94 UI & replyTo guard checks passed");
`
	cmd := exec.Command("node", "-e", script)
	cmd.Dir = filepath.Join("..", "..")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("v0.2.94 UI checks failed: %v\n%s", err, string(out))
	}
}
