package collie

import (
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestWebIPadSidebarOverlayAndKeyboardIsolation(t *testing.T) {
	t.Parallel()
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

const rootDir = process.argv[1];
const htmlSrc = fs.readFileSync(path.join(rootDir, "web", "index.html"), "utf8");
const cssSrc = fs.readFileSync(path.join(rootDir, "web", "app.css"), "utf8");
const appSrc = fs.readFileSync(path.join(rootDir, "web", "app.js"), "utf8");
const demoHtmlSrc = fs.readFileSync(path.join(rootDir, "docs", "demo", "index.html"), "utf8");
const demoCssSrc = fs.readFileSync(path.join(rootDir, "docs", "demo", "app.css"), "utf8");
const demoAppSrc = fs.readFileSync(path.join(rootDir, "docs", "demo", "app.js"), "utf8");

// ─── 1. Static HTML / CSS / Demo Sync Contracts ───
for (const [name, src] of [["web/index.html", htmlSrc], ["docs/demo/index.html", demoHtmlSrc]]) {
  assert.ok(src.includes('id="side-scrim"') && src.includes('class="side-scrim"'), name + " must declare #side-scrim");
  assert.ok(src.includes('class="side-full" id="side-full"'), name + " must declare #side-full");
  assert.ok(src.includes('id="side-expand-btn"') && src.includes('aria-controls="side-full"'), name + " #side-expand-btn must reference #side-full");
}

for (const [name, src] of [["web/app.css", cssSrc], ["docs/demo/app.css", demoCssSrc]]) {
  assert.ok(
    /@property\s+--kb-inset\s*\{[\s\S]*?inherits:\s*false;[\s\S]*?initial-value:\s*0px;/.test(src)
      && /@property\s+--kb-top\s*\{[\s\S]*?inherits:\s*false;[\s\S]*?initial-value:\s*0px;/.test(src)
      && /@property\s+--side-full-h\s*\{[\s\S]*?inherits:\s*false;[\s\S]*?initial-value:\s*100%;/.test(src),
    name + " must register non-inheriting @property --kb-inset, --kb-top, and --side-full-h",
  );
  assert.ok(
    src.includes("@media (max-width: 760px) and (pointer: coarse)")
      && src.includes("transform: translate3d(0, var(--vtop, 0px), 0);"),
    name + " must scope .app translate3d(--vtop) strictly to phones <=760px",
  );
  assert.ok(
    !src.includes("@media (min-width: 761px) and (pointer: coarse) { .app { position: fixed; inset: 0; } }"),
    name + " must NOT pin .app with position: fixed; inset: 0 on >760px (prevents WebKit tile discard flash)",
  );
  assert.ok(
    src.includes("@media (min-width: 761px) {\n  .app { position: relative; height: var(--side-full-h, 100%); overflow: hidden; }\n  .app > .side { height: var(--side-full-h, 100%); align-self: start; }")
      && src.includes("padding-bottom: var(--kb-inset, 0px);")
      && src.includes("bottom: var(--kb-inset, 0px);"),
    name + " must bind #app/.side to --side-full-h on >760px and apply --kb-inset only to .main/#v-collie/.side-drawer/.notepane",
  );
  // Active #v-collie uses contain: layout style (no strict paint tile discard on iPadOS keyboard resize), while parked keeps contain: strict
  const vColIdx = src.indexOf("#v-collie {");
  assert.ok(vColIdx > 0 && src.slice(vColIdx, vColIdx + 240).includes("contain: layout style;"), name + " active #v-collie must use contain: layout style to prevent WebKit keyboard white flash");
  const vParkedIdx = src.indexOf("#v-collie.is-parked {");
  assert.ok(vParkedIdx > 0 && src.slice(vParkedIdx, vParkedIdx + 120).includes("contain: strict;"), name + " parked #v-collie.is-parked must keep contain: strict");
  // Overlay sidebar media query strictly scoped to tablet portrait (761-1000px); iPad landscape (>1000px touch) stays docked!
  assert.ok(
    src.includes("@media (min-width: 761px) and (max-width: 1000px) {"),
    name + " must scope overlay sidebar media query to tablet portrait (761-1000px)",
  );
  assert.ok(
    !src.includes("(min-width: 761px) and (pointer: coarse),\n       (min-width: 761px) and (hover: none)"),
    name + " must NOT force overlay sidebar on >1000px touch wide screens (iPad landscape stays docked)",
  );
  // Touch 44px hit-target extensions remain intact
  assert.ok(
    src.includes("@media (pointer: coarse) {\n  .brand-toggle::after { content: \"\"; position: absolute; inset: -5px; }\n  .sr-btn::after { content: \"\"; position: absolute; inset: -2px -5px; }\n}"),
    name + " must preserve pointer: coarse 44px hit-target extensions",
  );
  assert.ok(
    src.includes(".app.side-overlay-open > .side .side-full")
      && src.includes("transform: translate3d(0, 0, 0);")
      && src.includes("transform: translate3d(-100%, 0, 0);")
      && src.includes(".app.side-overlay-open > .side-scrim"),
    name + " must animate overlay .side-full via pure GPU translate3d and .side-scrim opacity",
  );
  // No backdrop-filter on .side-scrim or .side-full (performance red line)
  const scrimIdx = src.indexOf(".side-scrim {");
  assert.ok(scrimIdx > 0 && !src.slice(scrimIdx, scrimIdx + 360).includes("backdrop-filter"), name + " .side-scrim must never use backdrop-filter");
  const overlayIdx = src.indexOf(".app > .side .side-full {");
  assert.ok(overlayIdx > 0 && !src.slice(overlayIdx, overlayIdx + 700).includes("backdrop-filter"), name + " overlay .side-full must never use backdrop-filter");
  assert.ok(src.slice(overlayIdx, overlayIdx + 320).includes("height: var(--side-full-h, 100%);"), name + " portrait overlay .side-full must pin height to --side-full-h");
  // <=760px only hides .side-rail (not <=1000px, so iPad portrait 761-1000px keeps 54px rail)
  assert.ok(!/@media\s*\(max-width:\s*1000px\)\s*\{\s*\.side-rail\s*\{\s*display:\s*none/.test(src), name + " must NOT hide .side-rail on 761-1000px tablet portrait");
}

for (const [name, src] of [["web/app.js", appSrc], ["docs/demo/app.js", demoAppSrc]]) {
  assert.ok(
    src.includes('const isOverlaySidebarViewport = () => typeof innerWidth === "number" && innerWidth > 760 && innerWidth <= 1000;'),
    name + " must strictly scope isOverlaySidebarViewport to 761-1000px",
  );
  assert.ok(src.includes("function setSideOverlayOpen("), name + " must define setSideOverlayOpen");
  assert.ok(src.includes("function applyWideKeyboardInsets("), name + " must define applyWideKeyboardInsets");
  assert.ok(src.includes("function applyWideBaseHeight("), name + " must define applyWideBaseHeight");
  assert.ok(src.includes("function hasWideRootVHOverlayOpen("), name + " must define hasWideRootVHOverlayOpen");
  assert.ok(src.includes('addEventListener("orientationchange"'), name + " must wire orientationchange handler");
  assert.ok(!src.includes('if (innerWidth <= 1000) {\n      e.preventDefault();'), name + " [data-toggle-side] must NOT block 761-1000px tablet portrait toggle");
}

// ─── 2. Runtime VM Verification: Landscape Docked, Portrait Overlay, Real iPadOS Keyboard, Rotation Anchor, Desktop ───
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
let reflowReads = [];
let trackReflow = false;
let postWriteScrollYReads = 0;
let inWritePhaseSpy = false;
function makeEl(id = "", tagName = "DIV") {
  const listeners = new Map(), attrs = new Map(), styles = new Map();
  let _height = "";
  const el = {
    id, tagName, localName: tagName.toLowerCase(), nodeType: 1,
    hidden: false, inert: false, disabled: false, value: "", open: false, tabIndex: 0,
    textContent: "", innerHTML: "", className: "", title: "", href: "",
    scrollTop: 0, scrollHeight: 4000, clientHeight: 700, scrollWidth: 800, clientWidth: 1140,
    offsetParent: {}, isConnected: true, children: [], dataset: {},
    _styleWrites: 0, _heightWrites: 0, _focusCount: 0,
    classList: makeClassList(),
    get offsetWidth() { if (trackReflow) reflowReads.push("offsetWidth#" + id); return id === ".side" ? 300 : 1140; },
    get offsetHeight() { if (trackReflow) reflowReads.push("offsetHeight#" + id); return 60; },
    style: {
      transform: "", opacity: "", transition: "", width: "",
      get height() { return _height; },
      set height(v) { el._heightWrites++; _height = String(v); },
      setProperty(k, v) { el._styleWrites++; if (k === "--kb-inset" || k === "--kb-top") inWritePhaseSpy = true; styles.set(k, String(v)); },
      getPropertyValue(k) { return styles.get(k) || ""; },
      removeProperty(k) { el._styleWrites++; styles.delete(k); },
    },
    _qs: {},
    _qsa: {},
    setAttribute(k, v) { attrs.set(k, String(v)); },
    getAttribute(k) { return attrs.has(k) ? attrs.get(k) : null; },
    removeAttribute(k) { attrs.delete(k); },
    hasAttribute(k) { return attrs.has(k); },
    addEventListener(t, fn) { if (!listeners.has(t)) listeners.set(t, []); listeners.get(t).push(fn); },
    removeEventListener(t, fn) { const l = listeners.get(t); if (l) listeners.set(t, l.filter((f) => f !== fn)); },
    dispatch(t, ev = {}) {
      const e = { type: t, target: el, currentTarget: el, defaultPrevented: false, preventDefault() { this.defaultPrevented = true; }, stopPropagation() {}, stopImmediatePropagation() {}, ...ev };
      for (const fn of [...(listeners.get(t) || [])]) fn(e);
      return e;
    },
    querySelector(sel) { return el._qs[sel] || getEl(sel); },
    querySelectorAll(sel) { return el._qsa[sel] || []; },
    closest() { return null; },
    contains(n) { return n === el; },
    matches() { return false; },
    focus() { el._focusCount++; doc.activeElement = el; },
    blur() { if (doc.activeElement === el) doc.activeElement = null; },
    append() {}, appendChild(c) { return c; }, removeChild(c) { return c; },
    getBoundingClientRect() { return { top: 0, left: 0, right: 300, bottom: 700, width: 300, height: 700 }; },
  };
  return el;
}

const bySel = new Map();
function getEl(sel) {
  if (!bySel.has(sel)) {
    const id = sel.startsWith("#") ? sel.slice(1) : sel;
    const el = makeEl(id);
    el.parentElement = makeEl(id + ":parent");
    bySel.set(sel, el);
  }
  return bySel.get(sel);
}

for (const sel of [
  "#app", ".side", "#side-full", "#side-rail", "#side-scrim", "#side-toggle-btn", "#side-expand-btn",
  ".main", "#v-collie", "#super-thread", "#super-composer", "#super-input", "#crew",
  "#file-view", "#thread-drawer", "#worker-drawer", "#notes-pane", "#super-search", "#quota-popover", "#wd-more-menu",
]) {
  getEl(sel);
}
getEl(".side")._qs[".side-full"] = getEl("#side-full");
getEl(".side")._qs[".side-rail"] = getEl("#side-rail");
getEl("#file-view").hidden = true;
getEl("#thread-drawer").hidden = true;
getEl("#worker-drawer").hidden = true;
getEl("#super-search").hidden = true;
getEl("#super-input").tagName = "TEXTAREA";
getEl("#super-input").matches = (s) => /textarea|input/.test(s);

// Wire scroller for #v-collie (used by ResizeObserver & rotation anchor verification)
const colScroller = getEl("#super-thread").parentElement;
colScroller.id = "collie-scroller";
colScroller.clientWidth = 894;
colScroller.clientHeight = 720;
colScroller.scrollHeight = 4000;
colScroller.scrollTop = 3280; // at bottom (4000 - 3280 - 720 = 0)
colScroller.getBoundingClientRect = () => ({ top: 60, left: 300, right: 300 + colScroller.clientWidth, bottom: 60 + colScroller.clientHeight, width: colScroller.clientWidth, height: colScroller.clientHeight });
getEl("#v-collie")._qs[".viewer"] = colScroller;

const msg12 = makeEl("msg-12");
msg12.dataset = { id: "e12" };
let msg12DocTop = 1800; // position inside scroll content
msg12.getBoundingClientRect = () => ({ top: 60 + (msg12DocTop - colScroller.scrollTop), bottom: 60 + (msg12DocTop - colScroller.scrollTop) + 80, left: 300, right: 800, width: 500, height: 80 });
getEl("#super-thread")._qsa[".msg[data-id], .event[data-id]"] = [msg12];
getEl("#super-thread")._qs['.msg[data-id="e12"], .event[data-id="e12"]'] = msg12;

let mockTouch = true;
let mockPlatform = "iPad";
let mockMaxTouchPoints = 5;
let mockReduced = false;
let _scrollY = 0;
const fakeVV = {
  width: 1194, height: 834, offsetTop: 0, scale: 1,
  _l: new Map(),
  addEventListener(t, fn) { if (!this._l.has(t)) this._l.set(t, []); this._l.get(t).push(fn); },
  dispatch(t) { for (const fn of [...(this._l.get(t) || [])]) fn({ type: t }); },
};

let rafQueue = [];
let nowMs = 1000;
const timers = new Map();
let timerSeq = 1;
const docListeners = new Map();
const winListeners = new Map();
const roCallbacks = [];
const scrollToCalls = [];
const storage = new Map();

const rootEl = makeEl("html", "HTML");
const bodyEl = makeEl("body", "BODY");
const doc = {
  documentElement: rootEl,
  body: bodyEl,
  hidden: false,
  activeElement: null,
  hasFocus: () => true,
  createElement: (tag) => makeEl(tag, tag.toUpperCase()),
  createDocumentFragment: () => makeEl("frag", "FRAGMENT"),
  getElementById: (id) => getEl("#" + id),
  querySelector: (sel) => (sel === "dialog[open]" ? null : getEl(sel)),
  querySelectorAll: (sel) => (sel === ".viewer, #log, #td-body, #wd-thread, #wd-log" ? [colScroller] : []),
  addEventListener(t, fn) { if (!docListeners.has(t)) docListeners.set(t, []); docListeners.get(t).push(fn); },
  removeEventListener() {},
  dispatch(t, ev = {}) {
    const e = { type: t, target: makeEl("doc-target"), defaultPrevented: false, preventDefault() { this.defaultPrevented = true; }, stopPropagation() {}, stopImmediatePropagation() {}, ...ev };
    for (const fn of [...(docListeners.get(t) || [])]) fn(e);
    return e;
  },
};

const ctx = {
  console, Math, Date, JSON, Set, Map, RegExp, Error, Promise, URL, URLSearchParams, Object, Array, Number, String, Boolean,
  setTimeout: (fn, ms = 0) => { const id = timerSeq++; timers.set(id, { id, fn, at: nowMs + (Number(ms) || 0) }); return id; },
  clearTimeout: (id) => { timers.delete(id); },
  setInterval: () => 1, clearInterval() {}, queueMicrotask,
  requestAnimationFrame: (fn) => { const id = rafQueue.length + 1; rafQueue.push({ id, fn, cancelled: false }); return id; },
  cancelAnimationFrame: (id) => { const r = rafQueue.find((x) => x.id === id); if (r) r.cancelled = true; },
  document: doc,
  location: { hash: "#/collie", origin: "http://127.0.0.1:7788", href: "http://127.0.0.1:7788/#/collie", pathname: "/", search: "" },
  history: { state: null, pushState() {}, replaceState() {}, back() {} },
  navigator: {
    get platform() { return mockPlatform; },
    get userAgent() { return mockPlatform; },
    get maxTouchPoints() { return mockMaxTouchPoints; },
    clipboard: { writeText: async () => {} },
  },
  isSecureContext: true, innerWidth: 1194, innerHeight: 834,
  get scrollY() { if (inWritePhaseSpy) postWriteScrollYReads++; return _scrollY; },
  set scrollY(v) { _scrollY = v; },
  scrollTo: (x, y) => { scrollToCalls.push([x, y]); _scrollY = y; },
  visualViewport: fakeVV,
  CSS: { escape: (s) => String(s), supports: () => true },
  HTMLDialogElement: { prototype: {} },
  Blob, Uint8Array, TextEncoder, TextDecoder, atob, AbortController,
  crypto: { randomUUID: () => "ipad-uuid" },
  performance: { now: () => nowMs },
  Event: class Event { constructor(t) { this.type = t; } },
  CustomEvent: class CustomEvent { constructor(t, d) { this.type = t; this.detail = d?.detail; } },
  ResizeObserver: class {
    constructor(cb) { this.cb = cb; this.targets = []; roCallbacks.push(this); }
    observe(t) { this.targets.push(t); }
    disconnect() {}
  },
  MutationObserver: class { observe() {} disconnect() {} },
  IntersectionObserver: class { observe() {} disconnect() {} },
  EventSource: class { addEventListener() {} close() {} },
  Notification: class { static permission = "default"; static async requestPermission() { return "default"; } },
  localStorage: { getItem: (k) => (storage.has(k) ? storage.get(k) : null), setItem: (k, v) => storage.set(k, String(v)), removeItem: (k) => storage.delete(k) },
  matchMedia: (q) => ({
    matches:
      (q === "(max-width: 760px)" && ctx.innerWidth <= 760)
      || (q === "(max-width: 1000px)" && ctx.innerWidth <= 1000)
      || (q === "(min-width: 1181px)" && ctx.innerWidth >= 1181)
      || (q === "(min-width: 1px)")
      || (q === "(hover: none)" && mockTouch)
      || (q === "(pointer: coarse)" && mockTouch)
      || (q === "(any-pointer: coarse)" && mockTouch)
      || (q === "(prefers-reduced-motion: reduce)" && mockReduced),
    addEventListener() {},
  }),
  getComputedStyle: (el) => ({ getPropertyValue: (k) => el?.style?.getPropertyValue?.(k) || "" }),
  getSelection: () => ({ isCollapsed: true, toString: () => "" }),
  addEventListener(t, fn) { if (!winListeners.has(t)) winListeners.set(t, []); winListeners.get(t).push(fn); },
  removeEventListener() {},
  dispatchEvent(ev) { for (const fn of [...(winListeners.get(ev?.type) || [])]) fn(ev); },
  fetch: async () => ({ ok: true, status: 200, json: async () => ({ ok: true }) }),
};
ctx.window = ctx;
ctx.globalThis = ctx;

function flushRAF(dt = 16) {
  nowMs += dt;
  const batch = rafQueue.splice(0, rafQueue.length);
  for (const r of batch) if (!r.cancelled) r.fn(nowMs);
}
function advanceTimers(dt) {
  nowMs += dt;
  for (const [id, t] of [...timers.entries()]) {
    if (t.at <= nowMs) { timers.delete(id); t.fn(); }
  }
}
function fireResizeObserver(target, width, height) {
  for (const ro of roCallbacks) {
    if (ro.targets.includes(target)) ro.cb([{ target, contentRect: { width, height } }]);
  }
}

const marker = "  boot();\n})();";
const hook = "  globalThis.__ipadTest = { isOverlaySidebarViewport, currentSideW, setSideOverlayOpen, syncSideOverlayMode, setSideCollapsed, setVH, syncCollieResizeBaseline, reanchorCollieOnViewportChange, getSideOverlayOpen: () => sideOverlayOpen, getSideCollapsed: () => sideCollapsed, setView: (v) => { view = v; }, setNotesOpen: (v) => { notesOpen = v; }, getCollieStickBottom: () => collieStickBottom, setCollieStickBottom: (v) => { collieStickBottom = !!v; } };\n})();";
vm.runInNewContext(appSrc.replace(marker, hook), ctx, { filename: "web/app.js" });
const T = ctx.__ipadTest;
const appEl = getEl("#app");
const sideEl = getEl(".side");
const sideFull = getEl("#side-full");
const sideScrim = getEl("#side-scrim");
const sideExpandBtn = getEl("#side-expand-btn");
const sideToggleBtn = getEl("#side-toggle-btn");
const mainEl = getEl(".main");
const colEl = getEl("#v-collie");
const wdEl = getEl("#worker-drawer");
const notesEl = getEl("#notes-pane");
const superInp = getEl("#super-input");

// ─── 2a) iPad Landscape (1194x834, touch): DEFAULT DOCKED EXPANDED + manual collapse/expand (#t15294) ───
assert.equal(T.isOverlaySidebarViewport(), false, "iPad landscape 1194x834 touch uses docked sidebar (NOT overlay)");
assert.equal(T.currentSideW(), 300, "iPad landscape starts expanded at docked width (300px)");
assert.equal(appEl.classList.contains("side-collapsed"), false, "iPad landscape starts expanded by default");
assert.equal(appEl.classList.contains("side-overlay-open"), false, "iPad landscape never sets side-overlay-open");
assert.equal(sideFull.inert, false, "iPad landscape docked .side-full is never inert");
assert.equal(appEl.style.getPropertyValue("--side-full-h"), "834px", "iPad landscape locks --side-full-h to full baseline 834px");
assert.equal(sideEl.style.getPropertyValue("--side-full-h"), "834px", ".side receives --side-full-h 834px");

// Manual collapse & expand on iPad landscape via [data-toggle-side]
sideToggleBtn.closest = (s) => (s === "[data-toggle-side]" ? sideToggleBtn : null);
sideExpandBtn.closest = (s) => (s === "[data-toggle-side]" ? sideExpandBtn : null);
doc.dispatch("click", { target: sideToggleBtn, button: 0 });
flushRAF(300);
assert.equal(appEl.classList.contains("side-collapsed"), true, "iPad landscape manual collapse sets .side-collapsed");
assert.equal(T.currentSideW(), 54, "collapsed iPad landscape sidebar shrinks to 54px rail");
doc.dispatch("click", { target: sideExpandBtn, button: 0 });
flushRAF(300);
assert.equal(appEl.classList.contains("side-collapsed"), false, "iPad landscape manual expand restores docked sidebar");
assert.equal(T.currentSideW(), 300, "re-expanded iPad landscape sidebar returns to 300px");

// ─── 2b) iPad Portrait (820x1180, both touch & fine pointer): 54px rail + pure-GPU overlay with 0 reflow ───
ctx.innerWidth = 820;
ctx.innerHeight = 1180;
fakeVV.width = 820;
fakeVV.height = 1180;
T.syncSideOverlayMode();
T.setVH();
assert.equal(T.isOverlaySidebarViewport(), true, "761-1000px iPad portrait enters pure-GPU overlay sidebar mode");
assert.equal(T.currentSideW(), 54, "761-1000px iPad portrait reserves permanent 54px rail");
assert.equal(appEl.classList.contains("side-overlay-open"), false, "portrait overlay starts closed");
assert.equal(sideFull.inert, true, "closed portrait overlay .side-full is inert");
assert.equal(sideFull.style.getPropertyValue("--side-full-h"), "1180px", "portrait overlay .side-full pins height to 1180px baseline");

reflowReads = [];
trackReflow = true;
doc.dispatch("click", { target: sideExpandBtn, button: 0 });
assert.equal(reflowReads.length, 0, "opening portrait overlay triggers 0 reflows: " + reflowReads.join(","));
assert.equal(appEl.classList.contains("side-overlay-open"), true, "tapping expand opens portrait overlay");
assert.equal(appEl.classList.contains("is-side-opening"), false, "overlay mode never triggers desktop glide");
assert.equal(T.currentSideW(), 54, "currentSideW stays 54px while overlay is open");
assert.equal(sideFull.inert, false);
assert.equal(sideFull.getAttribute("role"), "dialog");
assert.equal(sideFull.getAttribute("aria-modal"), "true");
assert.equal(sideExpandBtn.getAttribute("aria-expanded"), "true");

// Scrim tap dismisses cleanly with 0 reflow
sideScrim.dispatch("click");
assert.equal(reflowReads.length, 0, "closing portrait overlay via scrim triggers 0 reflows");
assert.equal(appEl.classList.contains("side-overlay-open"), false);
assert.equal(sideFull.inert, true);
assert.equal(sideFull.getAttribute("role"), null);
assert.equal(sideFull.getAttribute("aria-label"), null);

// Escape dismisses and restores focus to #side-expand-btn
doc.dispatch("click", { target: sideExpandBtn, button: 0 });
sideFull.contains = (n) => n === sideToggleBtn;
doc.activeElement = sideToggleBtn;
const escEv = doc.dispatch("keydown", { key: "Escape" });
assert.equal(escEv.defaultPrevented, true, "Escape dismisses portrait overlay");
assert.equal(T.getSideOverlayOpen(), false);
assert.equal(doc.activeElement, sideExpandBtn, "Escape restores focus to #side-expand-btn");
trackReflow = false;

// Even with Magic Keyboard fine pointer, 761-1000px stays overlay
mockTouch = false;
T.syncSideOverlayMode();
assert.equal(T.isOverlaySidebarViewport(), true, "761-1000px portrait stays overlay even with fine pointer");
doc.dispatch("click", { target: sideExpandBtn, button: 0 });
assert.equal(appEl.classList.contains("side-overlay-open"), true);
doc.dispatch("click", { target: sideToggleBtn, button: 0 });
assert.equal(appEl.classList.contains("side-overlay-open"), false);

// ─── 2c) Real iPadOS Safari/PWA Keyboard Open & Dismiss: BOTH innerHeight + vv.height shrink + staggered desync (#t15302, #t15306) ───
mockTouch = true;
mockPlatform = "iPad";
mockMaxTouchPoints = 5;
T.setNotesOpen(true);
// Portrait 820x1180 baseline
ctx.innerWidth = 820; ctx.innerHeight = 1180; fakeVV.width = 820; fakeVV.height = 1180; fakeVV.offsetTop = 0;
T.setVH();
let appHeightWritesBefore = appEl._heightWrites;
let rootWritesBefore = rootEl._styleWrites;
let sideBaseBefore = sideEl._styleWrites;
doc.activeElement = superInp;

// Staggered Frame 1: vv.height drops 1180 -> 860 BEFORE innerHeight shrinks
fakeVV.height = 860;
fakeVV.dispatch("resize");
flushRAF();
assert.equal(sideEl.style.getPropertyValue("--side-full-h"), "1180px", "portrait .side height stays locked at 1180px (.sr-foot/.side-foot pinned)");
assert.equal(mainEl.style.getPropertyValue("--kb-inset"), "320px", "Frame 1: --kb-inset = 320px");
assert.equal(colEl.style.getPropertyValue("--kb-inset"), "320px");
assert.equal(wdEl.style.getPropertyValue("--kb-inset"), "320px");
assert.equal(notesEl.style.getPropertyValue("--kb-inset"), "320px");
assert.equal(rootEl._styleWrites, rootWritesBefore, "Frame 1: 0 :root --vh writes");

// Staggered Frame 2: iPadOS Safari ALSO shrinks window.innerHeight 1180 -> 860!
const mainWritesAfterF1 = mainEl._styleWrites;
ctx.innerHeight = 860;
ctx.dispatchEvent({ type: "resize" });
flushRAF();
assert.equal(sideEl.style.getPropertyValue("--side-full-h"), "1180px", "Frame 2 (innerHeight=860): .side stays locked at 1180px, ZERO lift!");
assert.equal(sideFull.style.getPropertyValue("--side-full-h"), "1180px", "Frame 2: overlay .side-full stays locked at 1180px");
assert.equal(appEl.style.getPropertyValue("--side-full-h"), "1180px", "Frame 2: #app stays locked at 1180px (ZERO double-shrink)");
assert.equal(mainEl.style.getPropertyValue("--kb-inset"), "320px", "Frame 2: --kb-inset stays strictly monotonic at 320px (ZERO spike/reset flash)");
assert.equal(mainEl._styleWrites, mainWritesAfterF1, "Frame 2: 0 redundant style writes when innerHeight catches up to vv.height");
assert.equal(sideEl._styleWrites, sideBaseBefore, "0 redundant .side --side-full-h writes during keyboard open");
assert.equal(rootEl._styleWrites, rootWritesBefore, "Frame 2: 0 :root --vh writes even when innerHeight shrunk!");
assert.equal(appEl._heightWrites, appHeightWritesBefore, "0 #app inline height writes");

// Staggered Dismiss Frame 1: innerHeight restores 860 -> 1180 BEFORE vv.height restores
ctx.innerHeight = 1180;
ctx.dispatchEvent({ type: "resize" });
flushRAF();
assert.equal(mainEl.style.getPropertyValue("--kb-inset"), "320px", "Dismiss Frame 1: stays monotonically 320px until vv.height restores (no bounce)");
assert.equal(rootEl._styleWrites, rootWritesBefore, "Dismiss Frame 1: 0 :root --vh writes");

// Staggered Dismiss Frame 2: vv.height restores 860 -> 1180 with residual scrollY = 18
_scrollY = 18;
inWritePhaseSpy = false;
postWriteScrollYReads = 0;
fakeVV.height = 1180;
fakeVV.dispatch("resize");
flushRAF();
assert.equal(postWriteScrollYReads, 0, "dismiss performs 0 post-write window.scrollY layout reads (0 forced reflow)");
assert.equal(mainEl.style.getPropertyValue("--kb-inset"), "0px", "Dismiss Frame 2: --kb-inset resets atomically to 0px");
assert.equal(colEl.style.getPropertyValue("--kb-inset"), "0px");
assert.equal(wdEl.style.getPropertyValue("--kb-inset"), "0px");
assert.equal(notesEl.style.getPropertyValue("--kb-inset"), "0px");
assert.equal(rootEl._styleWrites, rootWritesBefore, "Dismiss Frame 2: 0 :root --vh writes (0 flash)");
assert.equal(appEl._heightWrites, appHeightWritesBefore, "0 #app inline height writes on dismiss");
assert.deepEqual(scrollToCalls, [[0, 0]], "residual Safari window.scrollY resets cleanly to (0, 0)");
advanceTimers(220);
assert.equal(rootEl._styleWrites, rootWritesBefore, "post-dismiss settle timer performs 0 delayed :root --vh writes");

// Landscape 1194x834 keyboard open (both simultaneous innerHeight+vv shrink AND offsetTop=24)
ctx.innerWidth = 1194; ctx.innerHeight = 834; fakeVV.width = 1194; fakeVV.height = 834; fakeVV.offsetTop = 0;
T.syncSideOverlayMode();
T.setVH();
assert.equal(sideEl.style.getPropertyValue("--side-full-h"), "834px", "rotating to landscape resets --side-full-h to 834px");
rootWritesBefore = rootEl._styleWrites;
ctx.innerHeight = 514; fakeVV.height = 514; fakeVV.offsetTop = 24;
fakeVV.dispatch("resize");
flushRAF();
assert.equal(sideEl.style.getPropertyValue("--side-full-h"), "834px", "landscape keyboard open keeps .side locked at 834px (.side-foot pinned)");
assert.equal(mainEl.style.getPropertyValue("--kb-top"), "24px");
assert.equal(mainEl.style.getPropertyValue("--kb-inset"), "296px");
assert.equal(rootEl._styleWrites, rootWritesBefore, "landscape keyboard open performs 0 :root --vh writes");
ctx.innerHeight = 834; fakeVV.height = 834; fakeVV.offsetTop = 0;
fakeVV.dispatch("resize");
flushRAF();
assert.equal(mainEl.style.getPropertyValue("--kb-top"), "0px");
assert.equal(mainEl.style.getPropertyValue("--kb-inset"), "0px");
assert.equal(rootEl._styleWrites, rootWritesBefore, "landscape keyboard dismiss performs 0 :root --vh writes");

// ─── 2d) iPad Landscape (1194x834) <-> Portrait (820x1180) Rotation Main Chat Zero-Drift Anchor (#t15309) ───
T.setView("collie");
T.setCollieStickBottom(true);
colScroller.clientWidth = 894;
colScroller.clientHeight = 720;
colScroller.scrollHeight = 4000;
colScroller.scrollTop = 3280; // dist = 0
T.syncCollieResizeBaseline();

// Rotate Landscape (1194x834) -> Portrait (820x1180): taller & narrower (scrollHeight grows due to wrapping)
ctx.innerWidth = 820; ctx.innerHeight = 1180; fakeVV.width = 820; fakeVV.height = 1180;
colScroller.clientWidth = 766;
colScroller.clientHeight = 1040;
colScroller.scrollHeight = 4600; // wrapped lines made content taller; old scrollTop=3280 now has dist=280px!
T.syncSideOverlayMode();
T.setVH();
fireResizeObserver(colScroller, 766, 1040);
flushRAF();
advanceTimers(200);
assert.ok(colScroller.scrollHeight - colScroller.scrollTop - colScroller.clientHeight <= 1, "Landscape->Portrait bottom-pinned chat stays glued to bottom (dist <= 1px)");
assert.equal(T.getCollieStickBottom(), true, "Landscape->Portrait preserves collieStickBottom=true");

// Rotate Portrait (820x1180) -> Landscape (1194x834): shorter & wider
ctx.innerWidth = 1194; ctx.innerHeight = 834; fakeVV.width = 1194; fakeVV.height = 834;
colScroller.clientWidth = 894;
colScroller.clientHeight = 720;
colScroller.scrollHeight = 4000;
// Before restore, scrollTop was 4600 (clamped/stale); simulate browser clamping to 3000 -> dist = 280px
colScroller.scrollTop = 3000;
T.syncSideOverlayMode();
T.setVH();
fireResizeObserver(colScroller, 894, 720);
flushRAF();
advanceTimers(200);
assert.ok(colScroller.scrollHeight - colScroller.scrollTop - colScroller.clientHeight <= 1, "Portrait->Landscape bottom-pinned chat stays glued to bottom (dist <= 1px)");

// Mid-history reading: user scrolls up to message e12 at offset +120px from top of scroller
T.setCollieStickBottom(false);
msg12DocTop = 1800;
colScroller.scrollTop = 1680; // msg12 rect.top - sr.top = 1800 - 1680 = +120px
T.syncCollieResizeBaseline();
// Rotate Landscape -> Portrait: wrapping shifts msg12 down to docTop = 2150px
ctx.innerWidth = 820; ctx.innerHeight = 1180; fakeVV.width = 820; fakeVV.height = 1180;
colScroller.clientWidth = 766;
colScroller.clientHeight = 1040;
colScroller.scrollHeight = 4600;
msg12DocTop = 2150;
T.syncSideOverlayMode();
T.setVH();
fireResizeObserver(colScroller, 766, 1040);
flushRAF();
advanceTimers(200);
assert.equal(T.getCollieStickBottom(), false, "Mid-history rotation must NOT jump to bottom");
const offsetAfterRot = msg12.getBoundingClientRect().top - colScroller.getBoundingClientRect().top;
assert.ok(Math.abs(offsetAfterRot - 120) <= 1, "Mid-history rotation keeps message e12 anchored at +120px, got " + offsetAfterRot);

// ─── 2e) Pure Desktop (>1000px, fine pointer / hover): docked sidebar & vertical window resize does NOT clip .side-foot ───
mockTouch = false;
mockPlatform = "MacIntel";
mockMaxTouchPoints = 0;
doc.activeElement = null;
ctx.innerWidth = 1440; ctx.innerHeight = 900; fakeVV.width = 1440; fakeVV.height = 900;
T.syncSideOverlayMode();
T.setVH();
assert.equal(T.isOverlaySidebarViewport(), false, "desktop 1440x900 mouse/keyboard stays in docked mode");
assert.equal(sideFull.inert, false, "desktop .side-full is never inert");
assert.equal(T.currentSideW(), 300, "desktop expanded sidebar width stays 300px");
assert.equal(sideEl.style.getPropertyValue("--side-full-h"), "", "pure desktop leaves --side-full-h unset (100% tracks window resize)");
// Vertical window drag shorter on desktop (900 -> 650): must keep --side-full-h unset & --kb-inset 0px
ctx.innerHeight = 650; fakeVV.height = 650;
T.setVH();
assert.equal(sideEl.style.getPropertyValue("--side-full-h"), "", "desktop vertical resize keeps .side at 100% so .side-foot is never clipped");
assert.equal(mainEl.style.getPropertyValue("--kb-inset"), "0px", "desktop vertical resize never injects fake keyboard inset");

console.log("OK: all iPad sidebar overlay & keyboard isolation tests passed");
`
	cmd := exec.Command(nodeBin, "-e", jsTest, root)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("iPad sidebar overlay & keyboard test failed: %v\n%s", err, string(out))
	}
	if !strings.Contains(string(out), "OK: all iPad sidebar overlay & keyboard isolation tests passed") {
		t.Fatalf("unexpected node output:\n%s", string(out))
	}
}
