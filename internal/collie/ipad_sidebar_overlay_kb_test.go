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
      && /@property\s+--kb-top\s*\{[\s\S]*?inherits:\s*false;[\s\S]*?initial-value:\s*0px;/.test(src),
    name + " must register non-inheriting @property --kb-inset and --kb-top",
  );
  assert.ok(
    src.includes("@media (max-width: 760px) and (pointer: coarse)")
      && src.includes("transform: translate3d(0, var(--vtop, 0px), 0);"),
    name + " must scope .app translate3d(--vtop) strictly to phones <=760px",
  );
  assert.ok(
    src.includes("@media (min-width: 761px) {\n  .app { height: 100%; }")
      && src.includes("padding-bottom: var(--kb-inset, 0px);")
      && src.includes("bottom: var(--kb-inset, 0px);"),
    name + " must keep #app full height on >760px and apply --kb-inset only to .main/#v-collie/.side-drawer",
  );
  assert.ok(
    src.includes("@media (min-width: 761px) and (max-width: 1000px),\n       (min-width: 761px) and (pointer: coarse),\n       (min-width: 761px) and (hover: none) and (any-pointer: coarse)"),
    name + " must define tablet portrait (761-1000px) & touch wide-screen overlay sidebar media query",
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
  // <=760px only hides .side-rail (not <=1000px, so iPad portrait 761-1000px keeps 54px rail)
  assert.ok(!/@media\s*\(max-width:\s*1000px\)\s*\{\s*\.side-rail\s*\{\s*display:\s*none/.test(src), name + " must NOT hide .side-rail on 761-1000px tablet portrait");
}

for (const [name, src] of [["web/app.js", appSrc], ["docs/demo/app.js", demoAppSrc]]) {
  assert.ok(src.includes("const isOverlaySidebarViewport ="), name + " must define isOverlaySidebarViewport");
  assert.ok(src.includes("function setSideOverlayOpen("), name + " must define setSideOverlayOpen");
  assert.ok(src.includes("function applyWideKeyboardInsets("), name + " must define applyWideKeyboardInsets");
  assert.ok(src.includes("function hasWideRootVHOverlayOpen("), name + " must define hasWideRootVHOverlayOpen");
  assert.ok(!src.includes('if (innerWidth <= 1000) {\n      e.preventDefault();'), name + " [data-toggle-side] must NOT block 761-1000px tablet portrait toggle");
}

// ─── 2. Runtime VM Verification: iPad Landscape, iPad Portrait, Keyboard Open/Dismiss, Desktop Zero-Regression ───
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
      setProperty(k, v) { el._styleWrites++; styles.set(k, String(v)); },
      getPropertyValue(k) { return styles.get(k) || ""; },
      removeProperty(k) { el._styleWrites++; styles.delete(k); },
    },
    _qs: {},
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
    querySelectorAll() { return []; },
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
  ".main", "#v-collie", "#super-thread", "#super-composer", "#super-input",
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

let mockTouch = true;
let mockReduced = false;
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
  querySelectorAll: (sel) => (sel === ".viewer, #log, #td-body, #wd-thread, #wd-log" ? [getEl("#super-thread").parentElement] : []),
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
  navigator: { platform: "iPad", clipboard: { writeText: async () => {} } },
  isSecureContext: true, innerWidth: 1194, innerHeight: 834, scrollY: 0,
  scrollTo: (x, y) => { scrollToCalls.push([x, y]); ctx.scrollY = y; },
  visualViewport: fakeVV,
  CSS: { escape: (s) => String(s), supports: () => true },
  HTMLDialogElement: { prototype: {} },
  Blob, Uint8Array, TextEncoder, TextDecoder, atob, AbortController,
  crypto: { randomUUID: () => "ipad-uuid" },
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

const marker = "  boot();\n})();";
const hook = "  globalThis.__ipadTest = { isOverlaySidebarViewport, currentSideW, setSideOverlayOpen, syncSideOverlayMode, setSideCollapsed, setVH, getSideOverlayOpen: () => sideOverlayOpen, setView: (v) => { view = v; }, setNotesOpen: (v) => { notesOpen = v; } };\n})();";
vm.runInNewContext(appSrc.replace(marker, hook), ctx, { filename: "web/app.js" });
const T = ctx.__ipadTest;
const appEl = getEl("#app");
const sideFull = getEl("#side-full");
const sideScrim = getEl("#side-scrim");
const sideExpandBtn = getEl("#side-expand-btn");
const sideToggleBtn = getEl("#side-toggle-btn");
const mainEl = getEl(".main");
const colEl = getEl("#v-collie");
const wdEl = getEl("#worker-drawer");

// ─── 2a) iPad Landscape (1194x834, touch): pure-GPU overlay open/close with 0 reflow & constant 54px currentSideW ───
assert.equal(T.isOverlaySidebarViewport(), true, "iPad landscape 1194x834 touch enters overlay sidebar mode");
assert.equal(T.currentSideW(), 54, "iPad landscape reserves permanent 54px rail");
assert.equal(appEl.classList.contains("side-overlay-open"), false, "overlay starts closed on boot");
assert.equal(sideFull.inert, true, "closed overlay .side-full starts inert");

reflowReads = [];
trackReflow = true;
sideExpandBtn.closest = (s) => (s === "[data-toggle-side]" ? sideExpandBtn : null);
doc.dispatch("click", { target: sideExpandBtn, button: 0 });
assert.equal(reflowReads.length, 0, "opening iPad overlay sidebar triggers 0 offsetWidth/offsetHeight reflows: " + reflowReads.join(","));
assert.equal(appEl.classList.contains("side-overlay-open"), true, "tapping expand opens overlay");
assert.equal(appEl.classList.contains("is-side-opening"), false, "overlay mode must NEVER trigger desktop push-glide .is-side-opening");
assert.equal(T.currentSideW(), 54, "currentSideW remains exactly 54px while overlay is open");
assert.equal(sideFull.inert, false);
assert.equal(sideFull.getAttribute("role"), "dialog");
assert.equal(sideFull.getAttribute("aria-modal"), "true");
assert.equal(sideExpandBtn.getAttribute("aria-expanded"), "true");

// Clicking scrim dismisses cleanly with 0 reflow
sideScrim.dispatch("click");
assert.equal(reflowReads.length, 0, "closing iPad overlay via scrim triggers 0 reflows");
assert.equal(appEl.classList.contains("side-overlay-open"), false);
assert.equal(sideFull.inert, true);
assert.equal(sideFull.getAttribute("role"), null);

// Opening again and pressing Escape dismisses cleanly and restores focus to #side-expand-btn
doc.dispatch("click", { target: sideExpandBtn, button: 0 });
assert.equal(T.getSideOverlayOpen(), true);
sideFull.contains = (n) => n === sideToggleBtn;
doc.activeElement = sideToggleBtn;
const escEv = doc.dispatch("keydown", { key: "Escape" });
assert.equal(escEv.defaultPrevented, true, "Escape dismisses open iPad overlay sidebar");
assert.equal(T.getSideOverlayOpen(), false);
assert.equal(doc.activeElement, sideExpandBtn, "Escape restores focus to #side-expand-btn");
trackReflow = false;

// ─── 2b) iPad Portrait (820x1180, both touch & non-touch): [data-toggle-side] toggles overlay cleanly (#t15172) ───
ctx.innerWidth = 820;
ctx.innerHeight = 1180;
fakeVV.width = 820;
fakeVV.height = 1180;
mockTouch = false; // even with Magic Keyboard / Trackpad fine pointer, 761-1000px stays overlay!
T.syncSideOverlayMode();
assert.equal(T.isOverlaySidebarViewport(), true, "761-1000px portrait viewport uses overlay sidebar mode");
assert.equal(T.currentSideW(), 54, "761-1000px portrait keeps 54px rail");
doc.dispatch("click", { target: sideExpandBtn, button: 0 });
assert.equal(appEl.classList.contains("side-overlay-open"), true, "761-1000px portrait expand button opens overlay instead of jumping route");
sideToggleBtn.closest = (s) => (s === "[data-toggle-side]" ? sideToggleBtn : null);
doc.dispatch("click", { target: sideToggleBtn, button: 0 });
assert.equal(appEl.classList.contains("side-overlay-open"), false, "761-1000px portrait collapse button closes overlay back to 54px rail");

// ─── 2c) iPad Keyboard Open & Dismiss: #app height untouched (.side-foot pinned), 0 :root --vh flash, atomic reset (#t15142, #t15150) ───
mockTouch = true;
ctx.innerWidth = 1194;
ctx.innerHeight = 834;
fakeVV.width = 1194;
fakeVV.height = 834;
fakeVV.offsetTop = 0;
T.setVH();
T.setNotesOpen(true); // even with floating notes card open, wide keyboard open/close must NOT thrash :root --vh!

const appHeightWritesBefore = appEl._heightWrites;
const rootWritesBefore = rootEl._styleWrites;
assert.equal(appEl.style.height, "", "wide #app must NOT receive inline height");
assert.equal(mainEl.style.getPropertyValue("--kb-inset"), "0px");
assert.equal(colEl.style.getPropertyValue("--kb-inset"), "0px");

// Simulate soft keyboard open (shrink by 320px + Safari visualViewport.offsetTop pan of 24px)
fakeVV.height = 514;
fakeVV.offsetTop = 24;
fakeVV.dispatch("resize");
flushRAF();
assert.equal(appEl.style.height, "", "keyboard open on iPad must NOT set #app inline height (keeps .side/.side-foot pinned)");
assert.equal(appEl._heightWrites, appHeightWritesBefore, "keyboard open performs 0 #app height writes");
assert.equal(appEl.style.getPropertyValue("--vtop"), "", "keyboard open on iPad must NOT set --vtop on #app");
assert.equal(rootEl._styleWrites, rootWritesBefore, "keyboard open on iPad performs 0 :root --vh writes (no full-doc recalc)");
assert.equal(mainEl.style.getPropertyValue("--kb-top"), "24px", ".main absorbs offsetTop");
assert.equal(mainEl.style.getPropertyValue("--kb-inset"), "296px", ".main absorbs exact keyboard bottom inset (834-514-24)");
assert.equal(colEl.style.getPropertyValue("--kb-inset"), "296px", "#v-collie shrinks above keyboard");
assert.equal(wdEl.style.getPropertyValue("--kb-inset"), "296px", "right drawers shrink above keyboard");

// Simulate keyboard dismiss (height restores 514 -> 834, offsetTop 24 -> 0, residual scrollY = 18)
ctx.scrollY = 18;
fakeVV.height = 834;
fakeVV.offsetTop = 0;
fakeVV.dispatch("resize");
fakeVV.dispatch("scroll");
flushRAF();
assert.equal(appEl.style.height, "", "keyboard dismiss keeps #app inline height empty");
assert.equal(appEl._heightWrites, appHeightWritesBefore, "keyboard dismiss performs 0 #app height writes");
assert.equal(rootEl._styleWrites, rootWritesBefore, "keyboard dismiss performs 0 :root --vh writes (0 full-screen flash)");
assert.equal(mainEl.style.getPropertyValue("--kb-top"), "0px", ".main --kb-top resets atomically to 0px");
assert.equal(mainEl.style.getPropertyValue("--kb-inset"), "0px", ".main --kb-inset resets atomically to 0px");
assert.equal(colEl.style.getPropertyValue("--kb-inset"), "0px", "#v-collie --kb-inset resets atomically to 0px");
assert.equal(wdEl.style.getPropertyValue("--kb-inset"), "0px", "right drawers reset atomically to 0px");
assert.deepEqual(scrollToCalls, [[0, 0]], "residual Safari window.scrollY resets to (0, 0) cleanly on dismiss");

// Settle timer after 150ms ALSO must perform 0 :root --vh writes (eliminating 130ms/140ms delayed flash)
advanceTimers(200);
assert.equal(rootEl._styleWrites, rootWritesBefore, "post-dismiss settle timer performs 0 delayed :root --vh writes");

// ─── 2d) Pure Desktop (>1000px, fine pointer / hover): keeps original docked push/pull sidebar ───
mockTouch = false;
ctx.innerWidth = 1440;
ctx.innerHeight = 900;
fakeVV.width = 1440;
fakeVV.height = 900;
T.syncSideOverlayMode();
assert.equal(T.isOverlaySidebarViewport(), false, "desktop 1440x900 mouse/keyboard stays in docked mode");
assert.equal(sideFull.inert, false, "desktop .side-full is never inert");
assert.equal(T.currentSideW(), 300, "desktop expanded sidebar width stays 300px");

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
