package collie

import (
	"os/exec"
	"path/filepath"
	"testing"
)

func TestWebV0310FlickerSmoothnessAndSidebarContracts(t *testing.T) {
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

// ─── 1. Static HTML / CSS Contracts ───

// 1a) Dark-mode image anti-white-flash tokens & zero hardcoded #f6f6f4 (#t12961)
assert.ok(!cssSrc.includes("#f6f6f4"), "web/app.css must not hardcode bright #f6f6f4 on image containers");
assert.ok(cssSrc.includes("--img-bg: #f4f4f2;") && cssSrc.includes("--img-bg: #1b1b1f;"), "light/dark --img-bg tokens must exist");
assert.ok(cssSrc.includes(".thumb img.is-img-loading") && cssSrc.includes(".md-img-btn img.is-img-loading"), "image loading reveal classes must exist");

// 1b) Left sidebar merged Collie icon toggle (#t12988) + zero rail pop-in flicker (#t12984)
assert.ok(htmlSrc.includes('id="side-toggle-btn"') && htmlSrc.includes('data-toggle-side'), "expanded sidebar top-left Collie icon toggles collapse");
assert.ok(!htmlSrc.includes('id="side-collapse-btn"'), "redundant separate #side-collapse-btn must be removed");
assert.ok(htmlSrc.includes('id="side-expand-btn"') && htmlSrc.includes('class="sr-btn sr-brand brand-toggle"'), "collapsed rail top-left Collie icon toggles expand at identical coordinates");
assert.ok(cssSrc.includes(".brand-toggle:hover .bt-ico") && cssSrc.includes(".brand-toggle:hover .bt-logo"), "hover cross-fades Collie icon to linear sidebar icon");
assert.ok(!cssSrc.includes(".app.side-collapsed .side-rail {\n    display: flex;\n    animation: np-mode-enter"), "collapsed .side-rail must NOT replay delayed np-mode-enter after collapse");
assert.ok(cssSrc.includes(".side.is-side-closing .side-rail") && cssSrc.includes(".app.is-side-opening > .side .side-rail"), "left rail stays dual-mounted during collapse/expand glide");

// 1c) v0.3.11 Sidebar & 54px Rail polish:
//     - Expanded .side-nav places 自动化 card directly under 和 Collie 对话
//     - Expanded .side-foot places wide left device status (flex: 1) + right peers/prefs icon buttons
//     - 54px Rail .sr-top order: expand toggle -> 主会话 (#rail-collie + unread dot) -> + -> 自动化
//     - 54px Rail .sr-foot order: 设备 (#rail-devices + 6px bottom-right cutout badge) -> 好友边牧 -> 偏好设置
//     - #notes-pane anchors to viewport right via --notes-right (independent of --side-w) & excludes #crew height on >=1360px
const sideNavBlock = htmlSrc.match(/<nav class="side-nav"[^>]*>([\s\S]*?)<\/nav>/);
assert.ok(sideNavBlock, ".side-nav must exist");
const sideNavIds = [...sideNavBlock[1].matchAll(/id="(nav-collie|nav-auto)"/g)].map((m) => m[1]);
assert.deepEqual(sideNavIds, ["nav-collie", "nav-auto"], "expanded .side-nav must keep 和 Collie 对话 followed by 自动化");
const footBlock = htmlSrc.match(/<footer class="side-foot">([\s\S]*?)<\/footer>/);
const railTopBlock = htmlSrc.match(/<div class="sr-top">([\s\S]*?)<\/div>/);
const railFootBlock = htmlSrc.match(/<div class="sr-foot">([\s\S]*?)<\/div>\s*<\/nav>/);
assert.ok(footBlock && railTopBlock && railFootBlock, "expanded side-foot and collapsed sr-top / sr-foot must exist");
assert.ok(!footBlock[1].includes('id="nav-auto"'), "expanded side-foot must not contain 自动化");
const footIds = [...footBlock[1].matchAll(/id="nav-(devices|peers|prefs)"/g)].map((m) => m[1]);
assert.deepEqual(footIds, ["devices", "peers", "prefs"], "expanded footer order must be left devices status + right peers, prefs");
const railTopIds = [...railTopBlock[1].matchAll(/id="(side-expand-btn|rail-collie|rail-new-btn|rail-auto)"/g)].map((m) => m[1]);
assert.deepEqual(railTopIds, ["side-expand-btn", "rail-collie", "rail-new-btn", "rail-auto"], "54px rail top nav order must be expand toggle, main chat, new, automation");
assert.ok(railTopBlock[1].includes('id="rail-collie" href="#/collie"') && railTopBlock[1].includes('id="rail-collie-dot"'), "54px rail #rail-collie links to #/collie and carries unread dot");
const railFootIds = [...railFootBlock[1].matchAll(/id="rail-(devices|peers|prefs)"/g)].map((m) => m[1]);
assert.deepEqual(railFootIds, ["devices", "peers", "prefs"], "54px rail footer order must be devices, peers, prefs");
assert.match(cssSrc, /\.sr-conn-dot\s*\{[\s\S]*?position:\s*absolute;\s*right:\s*6px;\s*bottom:\s*6px;[\s\S]*?width:\s*6px;\s*height:\s*6px;[\s\S]*?box-shadow:\s*0 0 0 1\.5px var\(--bg-2\);/, "rail device online dot must be a 6px bottom-right cutout micro-badge");
assert.ok(!appSrc.includes('$("#conn").className ='), "connect() must never overwrite #conn className and strip positioning classes");
assert.match(cssSrc, /\.notepane\s*\{[\s\S]*?left:\s*auto;\s*right:\s*var\(--notes-right/, ".notepane must anchor from right via viewport-only --notes-right");
assert.match(cssSrc, /#v-collie\.has-notes #crew\s*\{[\s\S]*?margin-right:\s*calc\(var\(--notes-right\) \+ var\(--notes-w\) \+ 16px\);/, "#crew must stop clear of #notes-pane on >=1360px");

// 1d) Smooth GPU drawer narrow glide + containment + graceful notebook handoff (#t12980, #t13038)
assert.ok(cssSrc.includes(".app.has-drawer.is-drawer-narrowing"), "narrowing drawer from wide to default width must use GPU staging class");
assert.ok(cssSrc.includes(".sd-inner") && cssSrc.includes("contain: layout style paint;"), ".sd-inner must isolate layout/style/paint");
assert.ok(cssSrc.includes(".notepane.is-handoff-in"), "notebook baton-return after drawer close must have dedicated handoff transition");

// 1e) Top-bar 小本本 pill hides smoothly while #notes-pane is visible (#t13048)
assert.ok(cssSrc.includes(".notes-head-btn.is-card-open") && cssSrc.includes("pointer-events: none;") && cssSrc.includes("#v-collie.no-notes-anim .notes-head-btn"), "top-bar 小本本 pill fades out cleanly with zero jump when card is open");
assert.ok(htmlSrc.includes('id="open-notes-btn"') && htmlSrc.includes("<span>小本本</span>"), "top-bar 小本本 pill keeps text 小本本 unchanged");

// 1f) Main-chat inline worker link styling (#t13010)
assert.ok(cssSrc.includes("a:is(.fpath, .url, .worker-link)"), "prose worker backtick links must have dedicated subtle style");

// 1g) File preview copy & new-tab buttons target persistent #fv-body / #rv-body (#t13132)
assert.ok(htmlSrc.includes('id="fv-copy" command="--copy" commandfor="fv-body"') && htmlSrc.includes('id="rv-copy" command="--copy" commandfor="rv-body"'), "copy buttons must target persistent fv-body/rv-body so HTML sandbox & CSV table previews never drop command events");
assert.ok(appSrc.includes('$("#fv-wide")?.addEventListener("click"') && appSrc.includes("function openSandboxedHtmlWindow("), "#fv-wide must handle standalone PWA, remote hubs, and regular tabs cleanly");

// 1h) Thread drawer drops redundant clap badge while keeping main chat claps intact (#t13166)
assert.ok(!appSrc.includes('clapActBtn(m.id, claps, "td-act")'), "thread drawer must not render clap buttons on messages");
assert.ok(!cssSrc.includes(".td-act.clap-btn") && !cssSrc.includes(".td-node.has-claps"), "thread drawer clap CSS rules must be cleaned up");
assert.ok(appSrc.includes("clapActBtn(e.id, claps)") && cssSrc.includes("#v-collie .say.has-claps .msg-acts"), "main chat claps must remain intact");

// 1i) Scrolling main chat never pauses working spinner rings; background-tab power-saving stays (#t13213)
assert.ok(!cssSrc.includes("is-scroll-paused") && !appSrc.includes("is-scroll-paused"), "scroll-pause class must not freeze working spinner rings");
assert.ok(cssSrc.includes("html.is-bg .dot::after") && appSrc.includes('root.classList.toggle("is-bg", bg)'), "background-tab animation pause stays intact");

// 1j) Ctrl-C interrupt buttons require 3s double-click confirmation (#t13368)
assert.ok(htmlSrc.includes('id="wd-interrupt"') && htmlSrc.includes('class="sd-arm-lbl"'), "#wd-interrupt must include armed confirmation label");
assert.ok(cssSrc.includes(".sd-act-btn.is-stop.is-armed") && cssSrc.includes(".menu button.is-armed"), "armed red danger styles must exist for both drawer and terminal menu interrupt buttons");
assert.ok(appSrc.includes("const INTERRUPT_ARM_MS = 3000;") && appSrc.includes("function triggerArmedInterrupt("), "3s double-click interrupt confirmation controller must exist");

// 1k) Tablet HTML drawer coordinate stability (#t13467): minmax(0, 1fr) prevents column blowout on <=1180px & <=760px, iframe clamps width
assert.ok(cssSrc.includes(".app.has-worker { grid-template-columns: var(--side-w) minmax(0, 1fr); }"), "tablet <=1180px overlay must use minmax(0, 1fr) instead of bare 1fr");
assert.ok(cssSrc.includes(".fv-html-sandbox { width: 100%; height: 100%; flex: 1; min-width: 0; max-width: 100%;"), "HTML sandbox iframe must clamp min-width/max-width inside flex column");
assert.ok(appSrc.includes("function composerMetrics(") && appSrc.includes("const isDesktopDrawerViewport ="), "composerMetrics and matchMedia-synced isDesktopDrawerViewport must exist");

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
function makeEl(id = "", tagName = "DIV") {
  const listeners = new Map(), attrs = new Map(), styles = new Map();
  let _innerHTML = "";
  const el = {
    id, tagName, value: "", textContent: "",
    get innerHTML() { return _innerHTML; },
    set innerHTML(v) { _innerHTML = String(v); el._innerHTMLWrites++; },
    get childElementCount() { return el.children.length || (_innerHTML ? 1 : 0); },
    get firstElementChild() { return el.children[0] || null; },
    _innerHTMLWrites: 0,
    hidden: false, disabled: false, checked: false, open: false, inert: false, tabIndex: 0,
    className: "", title: "", href: "", src: "", complete: false, naturalWidth: 0,
    style: {
      transform: "", opacity: "", transition: "", paddingLeft: "", paddingRight: "", maxWidth: "",
      setProperty(k, v) { styles.set(k, String(v)); },
      getPropertyValue(k) { return styles.get(k) || ""; },
      removeProperty(k) { styles.delete(k); },
    },
    dataset: {}, classList: makeClassList(), children: [], selectedOptions: [], options: [],
    scrollTop: 0, scrollHeight: 1200, clientHeight: 600, scrollWidth: 400, clientWidth: 1120,
    offsetWidth: 1140, offsetHeight: 40, offsetLeft: 0, offsetParent: {}, isConnected: true,
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
    setAttribute(k, v) { attrs.set(k, String(v)); if (k === "href") el.href = String(v); if (k === "title") el.title = String(v); if (k === "class") el.className = String(v); },
    getAttribute(k) { if (k === "href" && !attrs.has(k)) return el.href; return attrs.get(k) ?? ""; },
    removeAttribute(k) { attrs.delete(k); },
    hasAttribute(k) { return attrs.has(k); },
    closest() { return null; }, contains() { return false; },
    append(...ns) { for (const n of ns) if (n && typeof n === "object") { n.parentNode = el; el.children.push(n); } },
    appendChild(c) { c.parentNode = el; el.children.push(c); return c; },
    insertBefore(n, ref) { n.parentNode = el; const i = ref ? el.children.indexOf(ref) : -1; if (i >= 0) el.children.splice(i, 0, n); else el.children.push(n); return n; },
    removeChild(c) { const i = el.children.indexOf(c); if (i >= 0) el.children.splice(i, 1); c.parentNode = null; return c; },
    remove() { if (el.parentNode) el.parentNode.removeChild(el); },
    after() {}, focus() {}, blur() {}, click() {}, scrollIntoView() {},
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
for (const [formSel, inputSel] of [
  ["#super-composer", "#super-input"],
  ["#composer", "#input"],
  ["#x-composer", "#x-input"],
  ["#td-composer", "#td-input"],
  ["#wd-composer", "#wd-input"],
]) {
  const f = getEl(formSel);
  f._qs["textarea"] = getEl(inputSel);
  f._qs[".send"] = makeEl(formSel + " .send");
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
getEl("#worker-drawer").hidden = true;
getEl(".side").offsetWidth = 300;
getEl(".side")._qs[".side-rail"] = getEl("#side-rail");
getEl(".side")._qs[".side-full"] = getEl(".side-full");
const sideToggleBtn = getEl("#side-toggle-btn");
const sideExpandBtn = getEl("#side-expand-btn");

let rafQueue = [];
let nowMs = 1000;
let timerSeq = 1;
const activeTimers = new Map();
const fetchCalls = [];
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
  querySelectorAll: (sel) => (sel === "[data-toggle-side]" ? [sideToggleBtn, sideExpandBtn] : sel === ".menu" ? [getEl("#collie-menu"), getEl("#more-menu")] : []),
  addEventListener(type, fn) { if (!docListeners.has(type)) docListeners.set(type, []); docListeners.get(type).push(fn); },
  removeEventListener() {},
  dispatch(type, ev = {}) {
    const e = { type, target: makeEl("doc-target"), defaultPrevented: false, preventDefault() { this.defaultPrevented = true; }, stopPropagation() {}, composedPath: () => [], ...ev };
    for (const fn of [...(docListeners.get(type) || [])]) fn(e);
    return e;
  },
};
const esInstances = [];
const winListeners = new Map();
const ctx = {
  console, Math, Date, JSON, Set, Map, RegExp, Error, Promise, URL, URLSearchParams, Object, Array, Number, String, Boolean,
  setTimeout: (fn, ms = 0) => { const id = timerSeq++; activeTimers.set(id, { id, fn, at: nowMs + (Number(ms) || 0) }); return id; },
  clearTimeout: (id) => { activeTimers.delete(id); },
  setInterval: () => 1, clearInterval() {}, queueMicrotask,
  requestAnimationFrame: (fn) => { const id = rafQueue.length + 1; rafQueue.push({ id, fn, cancelled: false }); return id; },
  cancelAnimationFrame: (id) => { const r = rafQueue.find((x) => x.id === id); if (r) r.cancelled = true; },
  document: doc,
  location: { hash: "#/collie", origin: "http://127.0.0.1:7788", href: "http://127.0.0.1:7788/#/collie", pathname: "/", search: "" },
  history: { state: null, pushState() {}, replaceState() {}, back() {} },
  navigator: { platform: "MacIntel", clipboard: { writeText: async () => {} } },
  isSecureContext: true, innerWidth: 1440, innerHeight: 900,
  visualViewport: { height: 900, offsetTop: 0, addEventListener() {} },
  CSS: { escape: (s) => String(s), supports: () => true },
  HTMLDialogElement: { prototype: {} },
  Blob, Uint8Array, TextEncoder, TextDecoder, atob, AbortController, URL,
  crypto: { randomUUID: () => "v0310-uuid" },
  performance: { now: () => nowMs },
  Event: class Event { constructor(t) { this.type = t; } },
  CustomEvent: class CustomEvent { constructor(t, d) { this.type = t; this.detail = d?.detail; } },
  ResizeObserver: class { observe() {} disconnect() {} },
  MutationObserver: class { observe() {} disconnect() {} },
  IntersectionObserver: class { observe() {} disconnect() {} },
  EventSource: class {
    constructor(url) { this.url = String(url || ""); this._l = new Map(); this.closed = false; esInstances.push(this); }
    addEventListener(t, fn) { if (!this._l.has(t)) this._l.set(t, []); this._l.get(t).push(fn); }
    dispatch(t, ev = {}) { for (const fn of [...(this._l.get(t) || [])]) fn(ev); }
    close() { this.closed = true; }
  },
  Notification: class { static permission = "default"; static async requestPermission() { return "default"; } },
  localStorage: { getItem: (k) => (storage.has(k) ? storage.get(k) : null), setItem: (k, v) => storage.set(k, String(v)), removeItem: (k) => storage.delete(k) },
  matchMedia: () => ({ matches: false, addEventListener() {} }),
  getComputedStyle: (el) => ({ getPropertyValue: (k) => el?.style?.getPropertyValue?.(k) || "" }),
  getSelection: () => ({ isCollapsed: true, toString: () => "" }),
  addEventListener(t, fn) { if (!winListeners.has(t)) winListeners.set(t, []); winListeners.get(t).push(fn); },
  removeEventListener(t, fn) { const l = winListeners.get(t); if (!l) return; const i = l.indexOf(fn); if (i >= 0) l.splice(i, 1); },
  dispatchEvent(ev) { for (const fn of [...(winListeners.get(ev?.type) || [])]) fn(ev); },
  fetch: async (url, init = {}) => { fetchCalls.push({ url: String(url), method: init.method || "GET", body: init.body || "" }); return { ok: true, status: 200, json: async () => ({ ok: true, turns: [] }) }; },
};
ctx.window = ctx;
ctx.globalThis = ctx;
function stepRaf(dtMs) {
  nowMs += dtMs;
  const batch = rafQueue.splice(0, rafQueue.length);
  for (const r of batch) {
    if (!r.cancelled) r.fn(nowMs);
  }
}
function advanceTimers(dtMs) {
  nowMs += dtMs;
  for (const [id, t] of [...activeTimers.entries()]) {
    if (t.at <= nowMs) {
      activeTimers.delete(id);
      t.fn();
    }
  }
}

const marker = "  boot();\n})();";
const instrumented = appSrc.replace(
  marker,
  "  globalThis.__test = { setView: (v) => { view = v; }, setCurrent: (c) => { current = c; }, setSup: (s) => { sup = normSup(s); }, setState: (s) => { state = s; }, setNotebookData: (d) => { notebookData = d; }, setFvCur: (c) => { fvCur = c; }, route, setConnDotState, syncSideActive, syncCollieTopHeight, renderSide, disarmInterrupt, isInterruptArmed, threadMetrics, composerMetrics, isDesktopDrawerViewport, isLayoutGliding, runDesktopDrawerGlide, showFilePane, closeFilePane, setSideCollapsed, openWorkerDrawer, closeWorkerDrawer, drawWdLive, syncWorkerDrawerMeta, wdLiveCache, drawLive, renderHead, clearDetailLive, openThreadDrawer, closeThreadDrawer, syncNotebookVisibility, applyThumbSrc, watchImgDecode, decodedThumbs, md, resolveInlineWorker, openWorkerFromLink, openExternalLinkInStandalone, invokeCopy, getActiveWorkerKey: () => activeWorkerKey };\n})();"
);
vm.runInNewContext(instrumented, ctx, { filename: "web/app.js" });
const T = ctx.__test;

// 2a) Gutter-aware threadMetrics matches exact Chrome layout box (1440px, 300px side, 20px stable scrollbar gutter)
const fullM = T.threadMetrics(1140, 780, 20);
assert.equal(fullM.effColW, 780);
assert.equal(fullM.padLeft, 180);
const openM = T.threadMetrics(1140 - 490, 780, 20);
assert.equal(openM.effColW, 590);
assert.equal(openM.padLeft, 30);

// 2b) Worker switch in right drawer: zero sd-swap opacity blink, zero synchronous thread wipe, same-worker click is no-op (#t12961)
T.setView("collie");
T.setState({
  agents: [
    { pane_id: "p-w1", name: "fix-alpha", label: "任务甲", kind: "claude", status: "working", is_agent: true, cwd: "/tmp/a" },
    { pane_id: "p-w2", name: "fix-beta", label: "任务乙", kind: "codex", status: "working", is_agent: true, cwd: "/tmp/b" },
  ],
  shells: [],
  machines: [],
});
T.setSup({
  router: { status: "idle", alive: true },
  workers: [
    { name: "fix-alpha", label: "任务甲", pane_id: "p-w1", status: "working", task: "推进甲", ended: false },
    { name: "fix-beta", label: "任务乙", pane_id: "p-w2", status: "working", task: "推进乙", ended: false },
  ],
  entries: [],
});
T.openWorkerDrawer({ worker: "fix-alpha", immediate: true });
assert.equal(getEl("#wd-title").textContent, "任务甲");
const wdThread = getEl("#wd-thread");
wdThread.innerHTML = '<div class="turn">prev-content</div>';
const writesBeforeSwitch = wdThread._innerHTMLWrites;
T.openWorkerDrawer({ worker: "fix-beta" });
assert.equal(getEl("#worker-drawer").classList.contains("is-switching"), false, "switching worker in-place must NOT trigger sd-swap opacity fade");
assert.equal(wdThread._innerHTMLWrites, writesBeforeSwitch, "switching worker must keep previous content visible instead of wiping #wd-thread synchronously");
assert.equal(getEl("#wd-title").textContent, "任务乙");
// Clicking already-open worker is a silent no-op
const writesBeforeSame = wdThread._innerHTMLWrites;
T.openWorkerDrawer({ worker: "fix-beta" });
assert.equal(wdThread._innerHTMLWrites, writesBeforeSame, "re-clicking active worker must be a no-op");
T.closeWorkerDrawer({ immediate: true });

// 2c) Dark-mode image anti-flash: loading class before decode, zero delay when already cached in decodedThumbs (#t12961)
const img1 = makeEl("img1", "IMG");
T.applyThumbSrc(img1, "/api/file?path=shot.png", "shot.png");
assert.equal(img1.classList.contains("is-img-loading"), true, "fresh image starts with .is-img-loading until decoded");
img1.dispatch("load");
assert.equal(img1.classList.contains("is-img-loading"), false, "load event reveals image");
assert.equal(T.decodedThumbs.has("shot.png"), true, "decoded image key is cached");
const img2 = makeEl("img2", "IMG");
T.applyThumbSrc(img2, "/api/file?path=shot.png", "shot.png");
assert.equal(img2.classList.contains("is-img-loading"), false, "cached image redraw skips loading fade completely");

// 2d) Left sidebar collapse/expand glide keeps .side-rail visible from frame 0 (#t12984) & Collie icon click toggles (#t12988)
T.setSideCollapsed(true);
assert.equal(getEl(".side").classList.contains("is-side-closing"), true, "collapse enters GPU glide class on .side");
assert.equal(T.isLayoutGliding(), true, "isLayoutGliding() is true during sidebar collapse glide");
assert.ok(parseFloat(getEl("#side-rail").style.opacity) >= 0.35, "rail icons must already be visible on frame 0 of collapse (zero blank gap)");
stepRaf(16);
stepRaf(260);
assert.equal(getEl("#app").classList.contains("side-collapsed"), true);
assert.equal(getEl(".side").classList.contains("is-side-closing"), false);
assert.equal(T.isLayoutGliding(), false);

// Clicking top-left Collie icon in rail expands sidebar back
sideExpandBtn.closest = (s) => (s === "[data-toggle-side]" ? sideExpandBtn : null);
doc.dispatch("click", { target: sideExpandBtn, button: 0 });
assert.equal(getEl("#app").classList.contains("is-side-opening"), true, "clicking rail Collie icon starts expand glide");
stepRaf(16);
stepRaf(260);
assert.equal(getEl("#app").classList.contains("side-collapsed"), false, "sidebar expanded via Collie icon");

// 2e) Drawer narrow glide uses .is-drawer-narrowing GPU staging (#t12980)
getEl("#app").classList.add("has-drawer", "has-worker");
getEl("#worker-drawer").hidden = false;
T.runDesktopDrawerGlide({ kind: "narrow", drawerEl: getEl("#worker-drawer"), fromW: 570, toW: 490 });
assert.equal(getEl("#app").classList.contains("is-drawer-narrowing"), true, "narrowing drawer stages .is-drawer-narrowing");
assert.equal(T.isLayoutGliding(), true, "isLayoutGliding() pauses ResizeObservers during drawer narrow glide");
stepRaf(16);
stepRaf(260);
assert.equal(getEl("#app").classList.contains("is-drawer-narrowing"), false);
assert.equal(T.isLayoutGliding(), false);
getEl("#app").classList.remove("has-drawer", "has-worker");
getEl("#worker-drawer").hidden = true;

// 2f) Main chat worker links (prose backtick, event, need, ask-head) open right #worker-drawer without full-screen route jump (#t13010)
const renderedMd = T.md("已派 ` + "`fix-alpha`" + ` 处理中，顺便看 ` + "`not-a-worker`" + `。");
assert.ok(renderedMd.includes('class="worker-link"') && renderedMd.includes('data-open-worker="fix-alpha"'), "md() linkifies known backtick worker names");
assert.ok(!renderedMd.includes('data-open-worker="not-a-worker"'), "md() leaves regular inline code untouched");

ctx.location.hash = "#/collie";
const proseLink = makeEl("prose-link", "A");
proseLink.dataset.openWorker = "fix-alpha";
proseLink.href = "#/w/fix-alpha";
proseLink.closest = (sel) => (sel.includes("data-open-worker") ? proseLink : null);
const proseEv = getEl("#super-thread").dispatch("click", { target: proseLink, button: 0 });
assert.equal(proseEv.defaultPrevented, true, "clicking prose worker link intercepts default hash jump");
assert.equal(getEl("#app").classList.contains("has-worker"), true, "clicking prose worker link opens right #worker-drawer");
assert.equal(T.getActiveWorkerKey(), "fix-alpha");
assert.equal(ctx.location.hash, "#/collie", "URL stays on #/collie without jumping full-screen");

// Also verify event row link in #super-thread
T.closeWorkerDrawer({ immediate: true });
const eventLink = makeEl("event-link", "A");
eventLink.classList.add("event");
eventLink.dataset.ref = "fix-beta";
eventLink.href = "#/p/p-w2";
eventLink.closest = (sel) => (sel.includes("a.event[href]") ? eventLink : null);
const eventEv = getEl("#super-thread").dispatch("click", { target: eventLink, button: 0 });
assert.equal(eventEv.defaultPrevented, true, "clicking event worker link intercepts default navigation");
assert.equal(getEl("#app").classList.contains("has-worker"), true, "clicking event worker link opens right #worker-drawer");
assert.equal(T.getActiveWorkerKey(), "fix-beta");

// 2g) Graceful notebook handoff (#t13038) & top-bar 小本本 pill hides while card is open (#t13048)
T.setNotebookData({ date: "2026-11-28", tasks: [{ id: "t1", title: "示例任务", note: "进行中", tone: "live" }], artifacts: [], timers: [] });
T.syncNotebookVisibility({ immediate: true });
// While worker drawer is open, #notes-pane is hidden -> top-bar 小本本 pill MUST be visible
assert.equal(getEl("#notes-pane").classList.contains("is-hidden"), true);
assert.equal(getEl("#open-notes-btn").classList.contains("is-card-open"), false, "top-bar 小本本 pill stays visible while right drawer replaces notebook");
assert.equal(getEl("#open-notes-btn").getAttribute("aria-hidden"), "false");
assert.equal(getEl("#open-notes-btn").tabIndex, 0);

// Close worker drawer -> notebook returns with baton-return .is-handoff-in AND top-bar 小本本 pill smoothly hides
T.closeWorkerDrawer();
assert.equal(getEl("#notes-pane").classList.contains("is-hidden"), false, "closing worker drawer reveals floating notebook");
assert.equal(getEl("#notes-pane").classList.contains("is-handoff-in"), true, "closing right drawer adds .is-handoff-in baton-return class");
assert.equal(getEl("#open-notes-btn").classList.contains("is-card-open"), true, "top-bar 小本本 pill hides smoothly while notebook card is open");
assert.equal(getEl("#open-notes-btn").getAttribute("aria-hidden"), "true");
assert.equal(getEl("#open-notes-btn").tabIndex, -1);

// Click #np-close -> hides notebook card -> top-bar 小本本 pill fades back in
getEl("#np-close").dispatch("click");
assert.equal(getEl("#notes-pane").classList.contains("is-hidden"), true, "clicking #np-close hides notebook card");
assert.equal(getEl("#open-notes-btn").classList.contains("is-card-open"), false, "closing notebook card brings top-bar 小本本 pill back");
assert.equal(getEl("#open-notes-btn").getAttribute("aria-hidden"), "false");
assert.equal(getEl("#open-notes-btn").tabIndex, 0);

// 2h) #file-view HTML sandbox copy (including non-HTTPS execCommand fallback) & new-tab open (#t13132)
(async () => {
  let execCopyCalls = 0;
  let lastCopiedVal = "";
  doc.execCommand = (cmd) => {
    if (cmd === "copy") {
      execCopyCalls++;
      const ta = doc.body.children.find((c) => c.tagName === "TEXTAREA");
      lastCopiedVal = ta ? ta.value : "";
      return true;
    }
    return false;
  };
  ctx.isSecureContext = false;
  T.setFvCur({ reqPath: "~/demo/index.html", path: "~/demo/index.html", machine: "", kind: "html", size: 42, rawText: "<!doctype html><h1>Hello</h1>", truncated: false });
  const fvCopyBtn = getEl("#fv-copy");
  fvCopyBtn.id = "fv-copy";
  await T.invokeCopy(fvCopyBtn, getEl("#fv-body"));
  assert.equal(execCopyCalls, 1, "non-HTTPS HTML copy uses execCommand fallback");
  assert.equal(lastCopiedVal, "<!doctype html><h1>Hello</h1>", "copies full HTML source even without #fv-code-src");
  assert.equal(fvCopyBtn.hasAttribute("data-copied"), true, "marks #fv-copy as copied");

  // New-tab button in regular browser opens /api/file?...&as=html via window.open
  ctx.isSecureContext = true;
  const openedWins = [];
  ctx.open = (url, target) => {
    const iframe = makeEl("iframe", "IFRAME");
    const win = {
      closed: false, opener: ctx, url, target,
      document: { html: "", open() {}, write(s) { this.html = s; }, close() {}, querySelector: () => iframe },
      _iframe: iframe,
    };
    openedWins.push(win);
    return win;
  };
  const fvWide = getEl("#fv-wide");
  fvWide.setAttribute("href", "/api/file?path=~%2Fdemo%2Findex.html&as=html");
  const wEv1 = fvWide.dispatch("click", { button: 0 });
  assert.equal(wEv1.defaultPrevented, true);
  assert.equal(openedWins.length, 1);
  assert.equal(openedWins[0].url, "/api/file?path=~%2Fdemo%2Findex.html&as=html");
  assert.equal(openedWins[0].opener, null, "severs opener on new tab");

  // In standalone PWA mode, opens about:blank + sandboxed iframe srcdoc (never swallowed by launch_handler: focus-existing)
  ctx.navigator.standalone = true;
  const wEv2 = fvWide.dispatch("click", { button: 0 });
  assert.equal(wEv2.defaultPrevented, true);
  assert.equal(openedWins.length, 2);
  assert.equal(openedWins[1].url, "");
  assert.equal(openedWins[1]._iframe.srcdoc, "<!doctype html><h1>Hello</h1>");
  assert.equal(openedWins[1].opener, null);
  ctx.navigator.standalone = false;

  // 2i) Thread drawer omits clap button even when Collie message has claps=6 (#t13166)
  T.setSup({
    router: { status: "idle", alive: true },
    workers: [],
    entries: [
      { id: "c1", role: "user", text: "测试话题起点", at: "2026-11-28T02:11:00Z" },
      { id: "c2", role: "collie", text: "简洁回复内容", at: "2026-11-28T02:12:00Z", claps: 6, re: { entry: "c1", who: "你", text: "测试话题起点" } },
    ],
  });
  T.openThreadDrawer("c1", "c2", "", { immediate: true });
  const tdHtml = getEl("#td-list").innerHTML;
  assert.ok(tdHtml.includes("简洁回复内容"), "thread drawer renders reply content");
  assert.ok(!tdHtml.includes("clap-btn") && !tdHtml.includes("data-clap") && !tdHtml.includes("has-claps"), "thread drawer omits clap button and has-claps class");
  T.closeThreadDrawer({ immediate: true });

  // 2j) External links after Chinese punctuation & macOS Chrome PWA standalone click handling (#t13280)
  const extUrl = "https://git.example.com/org/repo/merge_requests/8";
  const mdBare = T.md("合并请求：" + extUrl);
  const mdBold = T.md("**合并请求：" + extUrl + "**");
  const mdParen = T.md("- 合并请求：" + extUrl + "（已合入）");
  for (const [lbl, h] of [["bare", mdBare], ["bold", mdBold], ["paren", mdParen]]) {
    assert.ok(h.includes('class="url" href="' + extUrl + '" target="_blank" rel="noopener noreferrer"'), lbl + " renders valid external anchor");
  }
  const extAnchor = makeEl("ext-url", "A");
  extAnchor.setAttribute("href", extUrl);
  extAnchor.setAttribute("target", "_blank");
  extAnchor.setAttribute("rel", "noopener noreferrer");
  extAnchor.closest = (sel) => (sel === "a[href]" ? extAnchor : null);
  let focusCount = 0;
  ctx.open = (url, target) => {
    const win = { closed: false, opener: ctx, url, target, focus() { focusCount++; } };
    openedWins.push(win);
    return win;
  };
  const beforeExtCount = openedWins.length;
  // Normal browser tab: leave native anchor click alone
  ctx.navigator.standalone = false;
  const evNorm = { button: 0, target: extAnchor, defaultPrevented: false, preventDefault() { this.defaultPrevented = true; } };
  assert.equal(T.openExternalLinkInStandalone(extAnchor, evNorm), false);
  assert.equal(evNorm.defaultPrevented, false);
  assert.equal(openedWins.length, beforeExtCount);

  // Standalone PWA mode: open explicitly via window.open, sever opener, focus window
  ctx.navigator.standalone = true;
  const evPwa = { button: 0, target: extAnchor, defaultPrevented: false, preventDefault() { this.defaultPrevented = true; } };
  assert.equal(T.openExternalLinkInStandalone(extAnchor, evPwa), true);
  assert.equal(evPwa.defaultPrevented, true);
  assert.equal(openedWins.length, beforeExtCount + 1);
  assert.equal(openedWins[beforeExtCount].url, extUrl);
  assert.equal(openedWins[beforeExtCount].target, "_blank");
  assert.equal(openedWins[beforeExtCount].opener, null);
  assert.equal(focusCount, 1);

  // Modifier click (Cmd+Click) is never intercepted
  const evMod = { button: 0, metaKey: true, target: extAnchor, defaultPrevented: false, preventDefault() { this.defaultPrevented = true; } };
  assert.equal(T.openExternalLinkInStandalone(extAnchor, evMod), false);
  assert.equal(evMod.defaultPrevented, false);

  // Popup-blocked fallback copies URL cleanly instead of failing silently
  let copiedExt = "";
  ctx.navigator.clipboard = { writeText: async (s) => { copiedExt = s; } };
  ctx.open = () => null;
  const evBlocked = { button: 0, target: extAnchor, defaultPrevented: false, preventDefault() { this.defaultPrevented = true; } };
  assert.equal(T.openExternalLinkInStandalone(extAnchor, evBlocked), true);
  assert.equal(evBlocked.defaultPrevented, true);
  await new Promise((r) => queueMicrotask(r));
  assert.equal(copiedExt, extUrl, "blocked standalone window.open falls back to copying URL");
  ctx.navigator.standalone = false;

  // 2k) v0.3.11 sidebar/rail/notes contracts:
  //     - setConnDotState preserves base classes on #conn & #rail-conn
  //     - #rail-collie tracks active + aria-current="page" across routes
  //     - setSideCollapsed never writes inline transform/transition onto #notes-pane
  //     - syncCollieTopHeight excludes #crew height on wide desktop (>= 1360px) so dy = 0
  getEl("#conn").classList.add("conn");
  getEl("#rail-conn").classList.add("sr-conn-dot");
  T.setConnDotState(true);
  assert.equal(getEl("#conn").classList.contains("conn"), true);
  assert.equal(getEl("#conn").classList.contains("on"), true);
  assert.equal(getEl("#conn").classList.contains("off"), false);
  assert.equal(getEl("#rail-conn").classList.contains("sr-conn-dot"), true);
  assert.equal(getEl("#rail-conn").classList.contains("on"), true);
  T.setConnDotState(false);
  assert.equal(getEl("#rail-conn").classList.contains("sr-conn-dot"), true, "offline toggle must preserve .sr-conn-dot positioning class");
  assert.equal(getEl("#rail-conn").classList.contains("off"), true);

  T.setView("prefs");
  T.syncSideActive({ skipBadge: true });
  assert.equal(getEl("#rail-collie").classList.contains("active"), false);
  assert.equal(getEl("#rail-prefs").classList.contains("active"), true);
  assert.equal(getEl("#rail-prefs").getAttribute("aria-current"), "page");
  T.setView("collie");
  T.syncSideActive({ skipBadge: true });
  assert.equal(getEl("#rail-collie").classList.contains("active"), true);
  assert.equal(getEl("#rail-collie").getAttribute("aria-current"), "page");
  assert.equal(getEl("#rail-prefs").getAttribute("aria-current"), "");

  getEl("#v-collie").classList.add("has-notes");
  getEl("#notes-pane").style.transform = "";
  getEl("#notes-pane").style.transition = "";
  getEl("#v-collie .collie-top").offsetHeight = 60;
  getEl("#crew").offsetHeight = 0;
  T.syncCollieTopHeight();
  assert.equal(getEl("#notes-pane").style.getPropertyValue("--collie-top-h"), "60px");
  // Collapsing reveals #crew (38px) -> .collie-top becomes 98px, but --collie-top-h MUST stay 60px on >= 1360px!
  T.setSideCollapsed(true);
  assert.equal(getEl("#notes-pane").style.transform, "", "collapse frame 0 must not write inline transform on #notes-pane");
  getEl("#v-collie .collie-top").offsetHeight = 98;
  getEl("#crew").offsetHeight = 38;
  stepRaf(16);
  assert.equal(getEl("#notes-pane").style.transform, "", "collapse mid-glide must not move #notes-pane");
  stepRaf(260);
  assert.equal(getEl("#notes-pane").style.getPropertyValue("--collie-top-h"), "60px", "collapsing sidebar and revealing #crew must keep --collie-top-h fixed at 60px (dy=0)");
  // Expanding hides #crew again -> --collie-top-h stays 60px and #notes-pane transform stays untouched
  T.setSideCollapsed(false);
  assert.equal(getEl("#notes-pane").style.transform, "", "expand frame 0 must not write inline transform on #notes-pane");
  getEl("#v-collie .collie-top").offsetHeight = 60;
  getEl("#crew").offsetHeight = 0;
  stepRaf(16);
  assert.equal(getEl("#notes-pane").style.transform, "", "expand mid-glide must not move #notes-pane");
  stepRaf(260);
  assert.equal(getEl("#notes-pane").style.getPropertyValue("--collie-top-h"), "60px", "expanding sidebar must keep --collie-top-h fixed at 60px (dy=0)");

  // 2l) Ctrl-C interrupt 3s double-click confirmation for #wd-interrupt and #more-menu [data-act="interrupt"] (#t13368)
  const keyCalls = () => fetchCalls.filter((c) => c.url.includes("/keys"));
  const wdInt = getEl("#wd-interrupt");
  wdInt.title = "发送 Ctrl-C 中断当前执行";
  wdInt.setAttribute("aria-label", "中断当前执行");
  T.openWorkerDrawer({ worker: "fix-alpha", immediate: true });
  fetchCalls.length = 0;

  // First click arms only — zero keys sent
  wdInt.dispatch("click");
  assert.equal(keyCalls().length, 0, "first click on #wd-interrupt must NOT send ctrl+c");
  assert.equal(wdInt.classList.contains("is-armed"), true, "first click enters armed highlight state");
  assert.equal(wdInt.title, "再点一次确认中断");
  assert.equal(wdInt.getAttribute("aria-label"), "再点一次确认中断");

  // Wait > 3s -> auto-disarms with zero keys sent
  advanceTimers(3100);
  assert.equal(wdInt.classList.contains("is-armed"), false, "3s timeout auto-disarms #wd-interrupt");
  assert.equal(wdInt.title, "发送 Ctrl-C 中断当前执行");
  assert.equal(wdInt.getAttribute("aria-label"), "中断当前执行");
  assert.equal(keyCalls().length, 0, "timeout must not send ctrl+c");

  // Arm then switch worker -> auto-disarms with zero keys sent
  wdInt.dispatch("click");
  assert.equal(wdInt.classList.contains("is-armed"), true);
  T.openWorkerDrawer({ worker: "fix-beta", immediate: true });
  assert.equal(wdInt.classList.contains("is-armed"), false, "switching worker must auto-disarm #wd-interrupt");
  assert.equal(keyCalls().length, 0);

  // Arm then close drawer -> auto-disarms with zero keys sent
  wdInt.dispatch("click");
  assert.equal(wdInt.classList.contains("is-armed"), true);
  T.closeWorkerDrawer({ immediate: true });
  assert.equal(wdInt.classList.contains("is-armed"), false, "closing worker drawer must auto-disarm #wd-interrupt");
  assert.equal(keyCalls().length, 0);

  // Re-open worker, click twice within 3s -> sends ["ctrl+c"] and disarms cleanly
  T.openWorkerDrawer({ worker: "fix-alpha", immediate: true });
  wdInt.dispatch("click");
  assert.equal(wdInt.classList.contains("is-armed"), true);
  assert.equal(keyCalls().length, 0);
  advanceTimers(1200);
  wdInt.dispatch("click");
  for (let i = 0; i < 8; i++) await Promise.resolve();
  assert.equal(wdInt.classList.contains("is-armed"), false, "second click disarms #wd-interrupt");
  assert.equal(wdInt.title, "发送 Ctrl-C 中断当前执行");
  assert.equal(keyCalls().length, 1, "second click within 3s sends ctrl+c");
  assert.ok(keyCalls()[0].url.includes("/api/panes/p-w1/keys") && keyCalls()[0].body.includes('"ctrl+c"'));
  T.closeWorkerDrawer({ immediate: true });

  // Full-screen terminal #more-menu [data-act="interrupt"] double-click confirmation
  fetchCalls.length = 0;
  T.setView("pane");
  T.setCurrent("p-w1");
  const moreMenu = getEl("#more-menu");
  moreMenu.id = "more-menu";
  moreMenu.hidden = false;
  const menuIntBtn = makeEl("menu-int", "BUTTON");
  menuIntBtn.dataset.act = "interrupt";
  menuIntBtn.textContent = "中断 (Ctrl-C)";
  menuIntBtn.title = "发送 Ctrl-C 中断当前执行";
  menuIntBtn.closest = (sel) => {
    if (sel.includes("data-act")) return menuIntBtn;
    if (sel === ".menu") return moreMenu;
    return null;
  };
  doc.dispatch("click", { target: menuIntBtn });
  assert.equal(keyCalls().length, 0, "first click on menu interrupt must NOT send ctrl+c");
  assert.equal(menuIntBtn.classList.contains("is-armed"), true);
  assert.equal(menuIntBtn.textContent, "再点一次确认中断");
  assert.equal(moreMenu.hidden, false, "first click keeps #more-menu open for immediate second click");

  // Timeout disarms menu button without sending
  advanceTimers(3100);
  assert.equal(menuIntBtn.classList.contains("is-armed"), false);
  assert.equal(menuIntBtn.textContent, "中断 (Ctrl-C)");
  assert.equal(keyCalls().length, 0);

  // Switching pane via route() disarms menu button without sending
  doc.dispatch("click", { target: menuIntBtn });
  assert.equal(menuIntBtn.classList.contains("is-armed"), true);
  ctx.location.hash = "#/p/p-w2";
  T.route();
  assert.equal(menuIntBtn.classList.contains("is-armed"), false, "switching pane must auto-disarm menu interrupt");
  assert.equal(menuIntBtn.textContent, "中断 (Ctrl-C)");
  assert.equal(keyCalls().length, 0);

  // Two clicks within 3s on new pane sends ["ctrl+c"] and closes menu
  moreMenu.hidden = false;
  doc.dispatch("click", { target: menuIntBtn });
  assert.equal(menuIntBtn.classList.contains("is-armed"), true);
  assert.equal(moreMenu.hidden, false);
  advanceTimers(900);
  doc.dispatch("click", { target: menuIntBtn });
  for (let i = 0; i < 8; i++) await Promise.resolve();
  assert.equal(menuIntBtn.classList.contains("is-armed"), false);
  assert.equal(menuIntBtn.textContent, "中断 (Ctrl-C)");
  assert.equal(moreMenu.hidden, true, "second click closes #more-menu after sending");
  assert.equal(keyCalls().length, 1, "second click within 3s sends ctrl+c on active pane");
  assert.ok(keyCalls()[0].url.includes("/api/panes/p-w2/keys") && keyCalls()[0].body.includes('"ctrl+c"'));

  // 2m) Tablet HTML drawer coordinate stability (#t13467):
  //     - Separate composerMetrics (no scrollbar gutter) vs threadMetrics (with gutter) eliminates 4-6px composer end-jump
  //     - Wide tablet (>=1181px, e.g. iPad Pro 11" 1194x834) applies frame-0 has-drawer-col-cap for 0px overlap & 0px end-jump
  //     - Tablet overlay (<=1180px, e.g. 820x1180 / 1024x768 / 1180x820) leaves zero inline width/transform on main chat & drawer
  //     - Rotating across 1180px while open finishes glide and clears inline drawer width & chat transforms
  T.setView("collie");
  const compClosed = T.composerMetrics(895, 780);
  const compOpen = T.composerMetrics(455, 780);
  assert.equal(compClosed.effCompW, 804);
  assert.equal(compClosed.padLeft, 45.5);
  assert.equal(compOpen.effCompW, 431);
  assert.equal(compOpen.padLeft, 12);

  // iPad Pro 11" landscape (1194x834, 299px sidebar, 440px HTML drawer -> 455px main)
  ctx.innerWidth = 1194;
  ctx.innerHeight = 834;
  getEl(".side").offsetWidth = 299;
  const colEl = getEl("#v-collie");
  const thEl = getEl("#super-thread");
  const compEl = getEl("#super-composer");
  thEl.parentElement.offsetWidth = 895;
  thEl.parentElement.clientWidth = 875;
  T.showFilePane();
  assert.equal(getEl("#app").classList.contains("is-drawer-opening"), true, "wide tablet >=1181px starts staged glide");
  assert.equal(colEl.classList.contains("has-drawer-col-cap"), true, "tight wide tablet caps column width on frame 0 so content never slides under incoming HTML drawer");
  assert.equal(colEl.style.getPropertyValue("--drawer-col-cap"), "395px");
  assert.equal(colEl.style.getPropertyValue("--drawer-comp-w"), "431px");
  assert.notEqual(thEl.style.transform, compEl.style.transform, "thread (guttered) and composer (gutter-free) must apply distinct exact translate3d offsets");
  stepRaf(16);
  stepRaf(260);
  assert.equal(colEl.classList.contains("has-drawer-col-cap"), false, "frame N removes has-drawer-col-cap atomically");
  assert.equal(thEl.style.transform, "", "frame N clears thread inline transform");
  assert.equal(compEl.style.transform, "", "frame N clears composer inline transform");

  // Rotate while open from 1194x834 -> 820x1180 (crosses 1180px into tablet overlay)
  ctx.innerWidth = 820;
  ctx.innerHeight = 1180;
  getEl(".side").offsetWidth = 268;
  ctx.dispatchEvent({ type: "resize" });
  assert.equal(getEl("#file-view").style.width, "", "rotating across 1180px clears desktop inline width on #file-view");
  assert.equal(thEl.style.transform, "");
  assert.equal(compEl.style.transform, "");
  T.closeFilePane({ immediate: true });

  // Open HTML drawer directly on tablet overlay (1180x820): zero inline transforms, zero inline width, zero desktop glide
  ctx.innerWidth = 1180;
  ctx.innerHeight = 820;
  getEl(".side").offsetWidth = 297;
  T.showFilePane();
  assert.equal(getEl("#app").classList.contains("has-file"), true);
  assert.equal(getEl("#app").classList.contains("is-drawer-opening"), false, "tablet <=1180px must stay pure overlay without desktop glide");
  assert.equal(colEl.classList.contains("has-drawer-col-cap"), false);
  assert.equal(getEl("#file-view").style.width, "", "tablet <=1180px must not stamp desktop width onto #file-view");
  assert.equal(thEl.style.transform, "");
  assert.equal(compEl.style.transform, "");
  T.closeFilePane({ immediate: true });
  ctx.innerWidth = 1440;
  ctx.innerHeight = 900;
  getEl(".side").offsetWidth = 300;

  // 2n) Worker ended auto-close + remove redundant #wd-gone banner + clear #wd-live & full-screen #live (#t13522, #t13532)
  const toastTexts = () => getEl("#toasts").children.map((c) => c.dataset?.msg || c.children?.[0]?.textContent || c.textContent || "");
  const clearToasts = () => { getEl("#toasts").children.length = 0; };
  T.setView("collie");
  T.setState({
    agents: [
      { pane_id: "p-w1", name: "fix-alpha", label: "任务甲", kind: "claude", status: "working", is_agent: true, cwd: "/tmp/a" },
      { pane_id: "p-w2", name: "fix-beta", label: "任务乙", kind: "codex", status: "working", is_agent: true, cwd: "/tmp/b" },
    ],
    shells: [],
    machines: [],
  });
  T.setSup({
    router: { status: "idle", alive: true },
    workers: [
      { name: "fix-alpha", label: "任务甲", pane_id: "p-w1", status: "working", task: "推进甲", ended: false },
      { name: "fix-beta", label: "任务乙", pane_id: "p-w2", status: "working", task: "推进乙", ended: false },
      { name: "done-gamma", label: "已完成丙", pane_id: "", status: "done", task: "历史任务", last_report: "已收尾", ended: true },
    ],
    entries: [],
  });

  // Case 1: Watching live worker -> agent finishes via state update -> clears #wd-live & cache, hides #wd-gone, auto-closes drawer, shows toast
  clearToasts();
  T.openWorkerDrawer({ worker: "fix-alpha", immediate: true });
  T.drawWdLive({ status: "working", tail: "running tests...", meta: "2s" });
  assert.equal(getEl("#wd-live").hidden, false, "live worker shows #wd-live spinner while active");
  assert.equal(T.wdLiveCache.has("p-w1"), true, "wdLiveCache caches active spinner while live");
  assert.equal(getEl("#wd-gone").hidden, true, "#wd-gone stays hidden while live");

  T.setState({
    agents: [
      { pane_id: "p-w2", name: "fix-beta", label: "任务乙", kind: "codex", status: "working", is_agent: true, cwd: "/tmp/b" },
    ],
    shells: [],
    machines: [],
  });
  T.setSup({
    router: { status: "idle", alive: true },
    workers: [
      { name: "fix-alpha", label: "任务甲", pane_id: "p-w1", status: "done", task: "推进甲", ended: true },
      { name: "fix-beta", label: "任务乙", pane_id: "p-w2", status: "working", task: "推进乙", ended: false },
      { name: "done-gamma", label: "已完成丙", pane_id: "", status: "done", task: "历史任务", last_report: "已收尾", ended: true },
    ],
    entries: [],
  });
  T.syncWorkerDrawerMeta();
  assert.equal(getEl("#wd-live").hidden, true, "ended worker must hide #wd-live immediately");
  assert.equal(getEl("#wd-live").innerHTML, "", "ended worker must clear #wd-live innerHTML");
  assert.equal(T.wdLiveCache.has("p-w1"), false, "ended worker must clear wdLiveCache");
  assert.equal(getEl("#wd-gone").hidden, true, "#wd-gone orange banner must never appear in drawer");
  assert.equal(T.getActiveWorkerKey(), null, "live->ended worker auto-closes drawer");
  assert.equal(getEl("#app").classList.contains("has-worker"), false, "auto-close removes .has-worker");
  assert.ok(toastTexts().some((t) => t.includes("任务甲 已完成")), "auto-close shows subtle completion toast");
  // Repeated sync must not duplicate toast
  const toastCountAfterClose = getEl("#toasts").children.length;
  T.syncWorkerDrawerMeta();
  assert.equal(getEl("#toasts").children.length, toastCountAfterClose, "subsequent meta sync must not duplicate completion toast");

  // Case 2: Watching live worker -> SSE "gone" fires before state removes agent -> clears spinner, closes drawer, shows toast
  clearToasts();
  esInstances.length = 0;
  T.openWorkerDrawer({ worker: "fix-beta", immediate: true });
  T.drawWdLive({ status: "working", tail: "compiling...", meta: "4s" });
  assert.equal(getEl("#wd-live").hidden, false);
  const betaEs = esInstances[esInstances.length - 1];
  assert.ok(betaEs, "live worker opens EventSource stream");
  betaEs.dispatch("gone");
  assert.equal(getEl("#wd-live").hidden, true, "SSE gone clears #wd-live immediately");
  assert.equal(getEl("#wd-live").innerHTML, "");
  assert.equal(T.wdLiveCache.has("p-w2"), false);
  assert.equal(getEl("#wd-gone").hidden, true, "SSE gone must not show redundant #wd-gone banner");
  assert.equal(T.getActiveWorkerKey(), null, "SSE gone auto-closes watched live drawer");
  assert.ok(toastTexts().some((t) => t.includes("任务乙 已完成")), "SSE gone shows completion toast");

  // Case 3: Opening an already-ended historical worker stays open quietly without #wd-gone banner, spinner, or toast
  clearToasts();
  T.openWorkerDrawer({ worker: "done-gamma", immediate: true });
  assert.equal(T.getActiveWorkerKey(), "done-gamma", "opening ended historical worker keeps drawer open");
  assert.equal(getEl("#app").classList.contains("has-worker"), true);
  assert.equal(getEl("#wd-gone").hidden, true, "historical ended worker must hide redundant #wd-gone banner");
  assert.equal(getEl("#wd-live").hidden, true, "historical ended worker must not show #wd-live spinner");
  assert.equal(getEl("#wd-live").innerHTML, "");
  assert.equal(getEl("#wd-peek-note").textContent, "任务已结束 · 只读记录", "calm read-only footer stays visible");
  assert.equal(getEl("#toasts").children.length, 0, "opening historical worker must not fire completion toast");
  T.closeWorkerDrawer({ immediate: true });

  // Case 4: Full-screen pane (#live) clears spinner when session ends (renderHead missing agent & SSE gone)
  T.setView("pane");
  T.setCurrent("p-w2");
  T.drawLive({ status: "working", tail: "building...", meta: "3s" });
  assert.equal(getEl("#live").hidden, false);
  T.setState({ agents: [], shells: [], machines: [] });
  T.renderHead();
  assert.equal(getEl("#live").hidden, true, "full-screen #live must hide when pane leaves state.agents");
  assert.equal(getEl("#live").innerHTML, "", "full-screen #live must clear innerHTML when ended");
  assert.equal(getEl("#live")._html, "", "full-screen #live must reset _html cache");

  console.log("ALL_V0310_FLICKER_SMOOTHNESS_TESTS_OK");
})().catch((e) => { console.error(e); process.exit(1); });
`
	cmd := exec.Command("node", "-e", script)
	cmd.Dir = filepath.Join("..", "..")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("v0.3.10 flicker/smoothness test failed: %v\n%s", err, out)
	}
}
