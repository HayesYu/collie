package collie

import (
	"os/exec"
	"path/filepath"
	"testing"
)

func TestWebV0310FlickerSmoothnessAndSidebarContracts(t *testing.T) {
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

// 1c) Unified 4-icon sidebar footer matching collapsed rail 1:1 (#t12994)
const footBlock = htmlSrc.match(/<footer class="side-foot">([\s\S]*?)<\/footer>/);
const railFootBlock = htmlSrc.match(/<div class="sr-foot">([\s\S]*?)<\/div>\s*<\/nav>/);
assert.ok(footBlock && railFootBlock, "both expanded side-foot and collapsed sr-foot must exist");
const footIds = [...footBlock[1].matchAll(/id="nav-(auto|peers|devices|prefs)"/g)].map((m) => m[1]);
const railIds = [...railFootBlock[1].matchAll(/id="rail-(auto|peers|devices|prefs)"/g)].map((m) => m[1]);
assert.deepEqual(footIds, ["auto", "peers", "devices", "prefs"], "expanded footer 4-icon order must be auto, peers, devices, prefs");
assert.deepEqual(railIds, footIds, "collapsed rail footer must match expanded footer 1:1");
const sideNavBlock = htmlSrc.match(/<nav class="side-nav"[\s\S]*?<\/nav>/);
assert.ok(sideNavBlock && !sideNavBlock[0].includes('id="nav-auto"'), "redundant 自动化 text row must be removed from .side-nav");

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
  querySelectorAll: (sel) => (sel === "[data-toggle-side]" ? [sideToggleBtn, sideExpandBtn] : []),
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
  Blob, Uint8Array, TextEncoder, TextDecoder, atob, AbortController,
  crypto: { randomUUID: () => "v0310-uuid" },
  performance: { now: () => nowMs },
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
  fetch: async () => ({ ok: true, status: 200, json: async () => ({ ok: true, turns: [] }) }),
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

const marker = "  boot();\n})();";
const instrumented = appSrc.replace(
  marker,
  "  globalThis.__test = { setView: (v) => { view = v; }, setSup: (s) => { sup = normSup(s); }, setState: (s) => { state = s; }, setNotebookData: (d) => { notebookData = d; }, setFvCur: (c) => { fvCur = c; }, threadMetrics, isLayoutGliding, runDesktopDrawerGlide, setSideCollapsed, openWorkerDrawer, closeWorkerDrawer, openThreadDrawer, closeThreadDrawer, syncNotebookVisibility, applyThumbSrc, watchImgDecode, decodedThumbs, md, resolveInlineWorker, openWorkerFromLink, invokeCopy, getActiveWorkerKey: () => activeWorkerKey };\n})();"
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
