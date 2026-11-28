package collie

import (
	"os/exec"
	"path/filepath"
	"testing"
)

func TestWebMobileDrawerDragFPSAndZeroReflow(t *testing.T) {
	t.Parallel()
	if _, err := exec.LookPath("node"); err != nil {
		t.Skip("node not installed")
	}
	script := `
const fs = require("fs");
const vm = require("vm");
const assert = require("assert/strict");

const cssSrc = fs.readFileSync("web/app.css", "utf8");
const appSrc = fs.readFileSync("web/app.js", "utf8");
const quotaCssSrc = fs.readFileSync("web/quota.css", "utf8");
const quotaJsSrc = fs.readFileSync("web/quota.js", "utf8");
const demoCssSrc = fs.readFileSync("docs/demo/app.css", "utf8");
const demoAppSrc = fs.readFileSync("docs/demo/app.js", "utf8");
const demoQuotaCssSrc = fs.readFileSync("docs/demo/quota.css", "utf8");
const demoQuotaJsSrc = fs.readFileSync("docs/demo/quota.js", "utf8");

// ─── 1. Static CSS / JS / Demo Sync Contracts ───
// 1a) Zero backdrop-filter blur on mobile scrims & .sheet::backdrop, and pure GPU opacity backdrop fade
assert.ok(
  !/\.sheet::backdrop\s*\{[^}]*backdrop-filter:/i.test(cssSrc) &&
    !/\.notes-scrim\s*\{[^}]*backdrop-filter:/i.test(cssSrc) &&
    !/\.drawer-scrim[^{]*\{[^}]*backdrop-filter:/i.test(cssSrc),
  ".sheet::backdrop and drawer scrims must never apply backdrop-filter blur over heavy mobile chat DOM",
);
assert.ok(
  !/dialog\.(?:sheet|int-sheet|tm-sheet|mem-io-sheet)[^{]*::backdrop\s*\{[^}]*transition-property:\s*background-color/i.test(cssSrc),
  "sheet dialog backdrops must animate compositor opacity instead of main-thread background-color",
);
assert.ok(
  !cssSrc.includes("#v-collie:has(.notepane.is-dragging)"),
  "must not use #v-collie:has(.notepane.is-dragging) which invalidates thousands of chat nodes on drag",
);
assert.ok(
  cssSrc.includes(".drawer-scrim.is-dragging,") &&
    cssSrc.includes(".notes-scrim.is-dragging") &&
    cssSrc.includes("transition: none !important;") &&
    cssSrc.includes("will-change: opacity;"),
  "all drawer scrims must disable transitions and promote opacity via direct .is-dragging class",
);

// 1b) Mobile drawers use lightweight shadows, contain: layout style, will-change: auto at rest, 260ms iOS damping curve, GPU pill scaleX
assert.ok(
  !cssSrc.includes("0 -8px 32px rgba(0, 0, 0, .14)") && !cssSrc.includes("0 -12px 40px rgba(0, 0, 0, .58)"),
  "heavy 32px/40px mobile bottom-sheet shadows must be replaced with lightweight static shadows",
);
assert.ok(
  cssSrc.includes("transition: transform 260ms cubic-bezier(.32, .72, 0, 1);"),
  "mobile bottom sheets must use 260ms iOS spring-damping curve on pure transform",
);
assert.ok(
  !/\.sd-handle-pill[^{]*\{[^}]*width\s+\.15s/.test(cssSrc) &&
    !/\.np-handle-pill[^{]*\{[^}]*width\s+\.15s/.test(cssSrc),
  "grabber pills must animate GPU transform instead of layout-thrashing width",
);
assert.ok(
  cssSrc.includes(".side-drawer.is-dragging .sd-handle-pill { background: var(--fg-3); transform: scaleX(1.16); }") &&
    cssSrc.includes(".notepane.is-dragging .np-handle-pill { background: var(--fg-3); transform: scaleX(1.16); }"),
  "grabber pills must widen via compositor transform: scaleX(1.16)",
);
assert.ok(
  quotaCssSrc.includes("#quota-popover.is-sheet {") &&
    quotaCssSrc.includes("contain: layout style;") &&
    quotaCssSrc.includes("transform: translate3d(0, 28px, 0);") &&
    quotaCssSrc.includes("#quota-popover.is-sheet.is-dragging"),
  "mobile quota bottom sheet must declare contain: layout style, translate3d, and dragging state",
);

// 1c) Demo synced cleanly
assert.ok(demoCssSrc.includes(".drawer-scrim.is-dragging,") && !demoCssSrc.includes("#v-collie:has(.notepane.is-dragging)"), "docs/demo/app.css must be synced");
assert.ok(demoAppSrc.includes("promoteSheetWillChange") && demoAppSrc.includes("cachedFadeDist"), "docs/demo/app.js must be synced");
assert.ok(demoQuotaCssSrc.includes("#quota-popover.is-sheet.is-dragging"), "docs/demo/quota.css must be synced");
assert.ok(demoQuotaJsSrc.includes("translate3d(0, ${pendingDy}px, 0)"), "docs/demo/quota.js must be synced");

// ─── 2. Runtime VM Regression Tests: rAF Coalescing, 0 Forced Reflows, Pure translate3d & will-change Cleanup ───
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

let nowMs = 1000;
let trackLayoutReads = false;
let layoutReadLog = [];
let reducedMotion = false;
let rafQueue = [];
let rafSeq = 1;
let timerSeq = 1;
const timers = new Map();

function flushRAF() {
  const batch = rafQueue.splice(0, rafQueue.length);
  for (const item of batch) {
    if (!item.canceled) item.fn(nowMs);
  }
  return batch.filter((x) => !x.canceled).length;
}
function advanceTimers(ms) {
  nowMs += ms;
  for (const [id, t] of [...timers.entries()]) {
    if (t.at <= nowMs) {
      timers.delete(id);
      t.fn();
    }
  }
}

function makeEl(id = "", tagName = "DIV") {
  const listeners = new Map(), attrs = new Map(), styles = new Map();
  let _transform = "", _opacity = "", _willChange = "", _transition = "";
  let _transformWrites = 0;
  let _offsetHeight = 776, _offsetWidth = 390, _scrollHeight = 1400, _clientHeight = 700;
  const el = {
    id, tagName, value: "", textContent: "", innerHTML: "",
    hidden: false, disabled: false, checked: false, open: false, inert: false, tabIndex: 0,
    className: "", title: "", href: "",
    scrollTop: 0, scrollWidth: 390, clientWidth: 390, offsetLeft: 0, offsetParent: {}, isConnected: true,
    _qs: {}, _qsa: {}, _captured: [],
    get _transformWrites() { return _transformWrites; },
    set _transformWrites(v) { _transformWrites = v; },
    get offsetHeight() {
      if (trackLayoutReads) layoutReadLog.push("offsetHeight@" + id);
      return _offsetHeight;
    },
    set offsetHeight(v) { _offsetHeight = v; },
    get offsetWidth() {
      if (trackLayoutReads) layoutReadLog.push("offsetWidth@" + id);
      return _offsetWidth;
    },
    set offsetWidth(v) { _offsetWidth = v; },
    get scrollHeight() {
      if (trackLayoutReads) layoutReadLog.push("scrollHeight@" + id);
      return _scrollHeight;
    },
    set scrollHeight(v) { _scrollHeight = v; },
    get clientHeight() {
      if (trackLayoutReads) layoutReadLog.push("clientHeight@" + id);
      return _clientHeight;
    },
    set clientHeight(v) { _clientHeight = v; },
    style: {
      get transform() { return _transform; },
      set transform(v) { _transform = String(v); _transformWrites++; },
      get opacity() { return _opacity; },
      set opacity(v) { _opacity = String(v); },
      get willChange() { return _willChange; },
      set willChange(v) { _willChange = String(v); },
      get transition() { return _transition; },
      set transition(v) { _transition = String(v); },
      setProperty(k, v) { styles.set(k, String(v)); },
      getPropertyValue(k) { return styles.get(k) || ""; },
      removeProperty(k) { styles.delete(k); },
    },
    dataset: {}, classList: makeClassList(), children: [], selectedOptions: [], options: [],
    addEventListener(type, fn) { if (!listeners.has(type)) listeners.set(type, []); listeners.get(type).push(fn); },
    removeEventListener(type, fn) { const l = listeners.get(type); if (!l) return; const i = l.indexOf(fn); if (i >= 0) l.splice(i, 1); },
    dispatch(type, ev = {}) {
      const e = {
        type, target: el, currentTarget: el, defaultPrevented: false,
        preventDefault() { this.defaultPrevented = true; },
        stopPropagation() {},
        composedPath: () => [el],
        ...ev,
      };
      for (const fn of [...(listeners.get(type) || [])]) fn(e);
      return e;
    },
    setPointerCapture(pid) { el._captured.push(pid); },
    querySelector(sel) { if (el._qs[sel]) return el._qs[sel]; const c = makeEl(id + ":" + sel); el._qs[sel] = c; return c; },
    querySelectorAll(sel) { return el._qsa[sel] || []; },
    setAttribute(k, v) { attrs.set(k, String(v)); if (k === "class") el.className = String(v); },
    getAttribute(k) { return attrs.get(k) ?? ""; },
    removeAttribute(k) { attrs.delete(k); },
    hasAttribute(k) { return attrs.has(k); },
    closest() { return null; },
    contains(node) { return node === el || el.children.includes(node); },
    append() {}, appendChild(c) { c.parentNode = el; el.children.push(c); return c; },
    remove() {}, after() {}, focus() {}, blur() {}, click() {}, scrollIntoView() {},
    showModal() { el.open = true; }, close() { el.open = false; },
    getBoundingClientRect() {
      if (trackLayoutReads) layoutReadLog.push("getBoundingClientRect@" + id);
      return { top: 0, bottom: 776, left: 0, right: 390, width: 390, height: 776 };
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
getEl("#file-scrim").hidden = true;
getEl("#thread-drawer").hidden = true;
getEl("#thread-scrim").hidden = true;
getEl("#worker-drawer").hidden = true;
getEl("#worker-scrim").hidden = true;
getEl("#notes-pane").classList.add("is-hidden");

const doc = {
  documentElement: makeEl("html"),
  body: makeEl("body"),
  activeElement: null,
  hidden: false,
  hasFocus: () => true,
  createElement: (tag) => makeEl(tag, tag.toUpperCase()),
  createDocumentFragment: () => makeEl("frag", "FRAGMENT"),
  getElementById: (id) => getEl("#" + id),
  querySelector: (sel) => (sel === "dialog[open]" ? null : getEl(sel)),
  querySelectorAll: () => [],
  addEventListener() {},
  removeEventListener() {},
};
doc.activeElement = doc.body;
const storage = new Map();
const ctx = {
  console, Math, Date, JSON, Set, Map, RegExp, Error, Promise, URL, URLSearchParams, Object, Array, Number, String, Boolean,
  setTimeout: (fn, ms = 0) => { const id = timerSeq++; timers.set(id, { id, fn, at: nowMs + (Number(ms) || 0) }); return id; },
  clearTimeout: (id) => { timers.delete(id); },
  setInterval: () => 1, clearInterval() {}, queueMicrotask,
  requestAnimationFrame: (fn) => { const id = rafSeq++; rafQueue.push({ id, fn, canceled: false }); return id; },
  cancelAnimationFrame: (id) => { for (const r of rafQueue) if (r.id === id) r.canceled = true; },
  document: doc,
  location: { hash: "#/collie", origin: "http://127.0.0.1:7788", href: "http://127.0.0.1:7788/#/collie", pathname: "/", search: "" },
  history: { state: null, pushState() {}, replaceState(s, _, url) { if (typeof url === "string" && url.startsWith("#")) ctx.location.hash = url; }, back() {} },
  navigator: { platform: "iPhone", clipboard: { writeText: async () => {} } },
  isSecureContext: true, innerWidth: 390, innerHeight: 844,
  CSS: { escape: (s) => String(s), supports: () => true },
  HTMLDialogElement: { prototype: {} },
  Blob, Uint8Array, TextEncoder, TextDecoder, atob, AbortController,
  crypto: { randomUUID: () => "fps-test-uuid" },
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
    matches: (q.includes("prefers-reduced-motion: reduce") && reducedMotion)
      || (q.includes("max-width: 760px") && ctx.innerWidth <= 760)
      || (q.includes("max-width: 1180px") && ctx.innerWidth <= 1180),
    addEventListener() {},
  }),
  getComputedStyle: (el) => ({ getPropertyValue: (k) => el?.style?.getPropertyValue?.(k) || "" }),
  getSelection: () => ({ isCollapsed: true, toString: () => "" }),
  addEventListener() {}, removeEventListener() {}, dispatchEvent() {},
  fetch: async () => ({ ok: true, status: 200, json: async () => ({ ok: true, entries: [], workers: [], turns: [] }) }),
};
ctx.window = ctx;
ctx.globalThis = ctx;

const marker = "  boot();\n})();";
const instrumented = appSrc.replace(
  marker,
  "  globalThis.__test = { route, setView: (v) => { view = v; }, setSup: (s) => { sup = normSup(s); }, setState: (s) => { state = s; }, getNotesOpen: () => notesOpen, showFilePane, closeFilePane, openThreadDrawer, closeThreadDrawer, openWorkerDrawer, closeWorkerDrawer, getSuperRenderCalls: () => __superRenders };\n})();",
).replace(
  "  function renderSuper(",
  "  let __superRenders = 0;\n  function renderSuper(",
).replace(
  "  function renderSuper(opts = {}) {\n",
  "  function renderSuper(opts = {}) {\n    __superRenders++;\n",
);
vm.runInNewContext(instrumented, ctx, { filename: "web/app.js" });
const T = ctx.__test;

T.setState({
  agents: [{ pane_id: "p1", name: "fps-worker", label: "帧率助手", kind: "claude", status: "working", is_agent: true, cwd: "/tmp/a" }],
  machines: [], shells: [],
});
T.setSup({
  router: { status: "idle", alive: true },
  workers: [{ name: "fps-worker", label: "帧率助手", pane_id: "p1", status: "working", task: "压测", ended: false }],
  entries: [
    { id: "e1", role: "user", text: "测试话题", at: "2026-11-28T10:00:00Z" },
    { id: "e2", role: "collie", text: "第一条回复", at: "2026-11-28T10:01:00Z", re: { entry: "e1", who: "你", text: "测试话题" } },
  ],
});
T.route();
flushRAF();

const drawers = [
  {
    name: "notes",
    open: () => getEl("#open-notes-btn").dispatch("click"),
    isClosed: () => !T.getNotesOpen() && getEl("#notes-pane").classList.contains("is-hidden"),
    drawer: getEl("#notes-pane"),
    scrim: getEl("#notes-scrim"),
    handle: getEl("#np-handle"),
  },
  {
    name: "thread",
    open: () => T.openThreadDrawer("e1", "e2", ""),
    isClosed: () => getEl("#thread-drawer").hidden && !getEl("#app").classList.contains("has-thread"),
    drawer: getEl("#thread-drawer"),
    scrim: getEl("#thread-scrim"),
    handle: getEl("#td-handle"),
  },
  {
    name: "worker",
    open: () => T.openWorkerDrawer({ worker: "fps-worker" }),
    isClosed: () => getEl("#worker-drawer").hidden && !getEl("#app").classList.contains("has-worker"),
    drawer: getEl("#worker-drawer"),
    scrim: getEl("#worker-scrim"),
    handle: getEl("#wd-handle"),
  },
  {
    name: "file",
    open: () => T.showFilePane(),
    isClosed: () => getEl("#file-view").hidden && !getEl("#app").classList.contains("has-file"),
    drawer: getEl("#file-view"),
    scrim: getEl("#file-scrim"),
    handle: getEl("#fv-handle"),
  },
];

for (const d of drawers) {
  // 2a) Open -> settle -> idle will-change cleared
  d.open();
  flushRAF();
  advanceTimers(400);
  flushRAF();
  assert.equal(d.drawer.style.willChange, "", d.name + ": idle drawer must clear will-change after open settles");
  assert.equal(d.scrim.style.willChange, "", d.name + ": idle scrim must clear will-change after open settles");

  // 2b) Partial drag (snap-back): 6 pointermoves before 1 rAF coalesce to 1 translate3d write & 0 layout reads
  nowMs += 500;
  d.handle.dispatch("pointerdown", { target: d.handle, pointerId: 10, pointerType: "touch", clientX: 195, clientY: 100 });
  layoutReadLog = [];
  trackLayoutReads = true;
  d.drawer._transformWrites = 0;

  for (let i = 1; i <= 6; i++) {
    nowMs += 4;
    d.handle.dispatch("pointermove", { target: d.handle, pointerId: 10, pointerType: "touch", clientX: 195, clientY: 100 + i * 10 });
  }
  assert.equal(d.drawer._transformWrites, 0, d.name + ": pointermove must defer DOM writes to requestAnimationFrame");
  assert.equal(layoutReadLog.length, 0, d.name + ": 0 layout reads before rAF flush; got " + JSON.stringify(layoutReadLog));

  flushRAF();
  assert.equal(d.drawer._transformWrites, 1, d.name + ": 6 pointermoves in one frame coalesce into exactly 1 transform write");
  assert.equal(d.drawer.style.transform, "translate3d(0, 60px, 0)", d.name + ": uses pure GPU translate3d");
  assert.equal(d.drawer.classList.contains("is-dragging"), true, d.name + ": drawer enters .is-dragging");
  assert.equal(d.scrim.classList.contains("is-dragging"), true, d.name + ": scrim enters .is-dragging");
  assert.equal(d.drawer.style.willChange, "transform", d.name + ": promotes will-change: transform while dragging");
  assert.equal(d.scrim.style.willChange, "opacity", d.name + ": promotes scrim will-change: opacity while dragging");
  assert.equal(layoutReadLog.length, 0, d.name + ": 0 layout reads during rAF drag flush; got " + JSON.stringify(layoutReadLog));

  // Release (slow short drag -> snap back)
  nowMs += 400;
  d.handle.dispatch("pointerup", { target: d.handle, pointerId: 10, pointerType: "touch", clientX: 195, clientY: 160 });
  trackLayoutReads = false;
  assert.equal(layoutReadLog.length, 0, d.name + ": 0 layout reads on snap-back release; got " + JSON.stringify(layoutReadLog));
  assert.equal(d.drawer.classList.contains("is-dragging"), false);
  assert.equal(d.scrim.classList.contains("is-dragging"), false);
  assert.equal(d.drawer.style.transform, "");
  assert.equal(d.scrim.style.opacity, "");
  // Simulating transitionend clears will-change immediately without waiting for timeout
  d.drawer.dispatch("transitionend", { propertyName: "transform" });
  d.scrim.dispatch("transitionend", { propertyName: "opacity" });
  assert.equal(d.drawer.style.willChange, "", d.name + ": snap-back transitionend clears drawer will-change");
  assert.equal(d.scrim.style.willChange, "", d.name + ": snap-back transitionend clears scrim will-change");

  // 2c) 30-frame continuous downward drag + dismiss: 0 layout reads during drag & release, 0 renderSuper on mobile close
  const superRendersBefore = T.getSuperRenderCalls();
  nowMs += 300;
  d.handle.dispatch("pointerdown", { target: d.handle, pointerId: 11, pointerType: "touch", clientX: 195, clientY: 90 });
  layoutReadLog = [];
  trackLayoutReads = true;
  let lastY = 90;
  for (let f = 1; f <= 30; f++) {
    nowMs += 8;
    d.handle.dispatch("pointermove", { target: d.handle, pointerId: 11, pointerType: "touch", clientX: 195, clientY: 90 + (f - 0.5) * 8 });
    nowMs += 8;
    lastY = 90 + f * 8;
    d.handle.dispatch("pointermove", { target: d.handle, pointerId: 11, pointerType: "touch", clientX: 195, clientY: lastY });
    flushRAF();
  }
  assert.equal(layoutReadLog.length, 0, d.name + ": 30 frames (60 pointermoves) must perform 0 layout reads; got " + JSON.stringify(layoutReadLog));
  assert.equal(d.drawer.style.transform, "translate3d(0, 240px, 0)", d.name + ": final drag transform matches translate3d");

  d.handle.dispatch("pointerup", { target: d.handle, pointerId: 11, pointerType: "touch", clientX: 195, clientY: lastY });
  trackLayoutReads = false;
  assert.equal(layoutReadLog.length, 0, d.name + ": mobile dismiss release must perform 0 layout reads; got " + JSON.stringify(layoutReadLog));

  advanceTimers(400);
  assert.equal(d.isClosed(), true, d.name + ": drawer closes cleanly after drag past threshold");
  assert.equal(d.drawer.style.willChange, "", d.name + ": drawer will-change cleared after dismiss");
  assert.equal(d.scrim.style.willChange, "", d.name + ": scrim will-change cleared after dismiss");
  assert.equal(T.getSuperRenderCalls(), superRendersBefore, d.name + ": closing mobile overlay must trigger 0 renderSuper rebuilds");
}

// 2d) prefers-reduced-motion: reduce never leaves will-change lingering
reducedMotion = true;
nowMs += 500;
getEl("#open-notes-btn").dispatch("click");
const npHandle = getEl("#np-handle");
npHandle.dispatch("pointerdown", { target: npHandle, pointerId: 21, pointerType: "touch", clientX: 195, clientY: 100 });
npHandle.dispatch("pointermove", { target: npHandle, pointerId: 21, pointerType: "touch", clientX: 195, clientY: 160 });
flushRAF();
assert.equal(getEl("#notes-pane").style.willChange, "", "reduced-motion must not promote will-change");
npHandle.dispatch("pointerup", { target: npHandle, pointerId: 21, pointerType: "touch", clientX: 195, clientY: 160 });
assert.equal(getEl("#notes-pane").style.willChange, "", "reduced-motion keeps will-change empty after release");
reducedMotion = false;

// ─── 3. Quota Mobile Sheet rAF Zero-Reflow Drag Test ───
const qEls = new Map();
let qTrackReads = false;
let qReadCount = 0;
let qRafQueue = [];
function makeQEl(id) {
  const ls = new Map(), attrs = new Map(), styles = new Map();
  let popOpen = false;
  let tfWrites = 0;
  let _tf = "", _wc = "";
  const el = {
    id, textContent: "", innerHTML: "", title: "", hidden: false, disabled: false,
    classList: makeClassList(),
    get _tfWrites() { return tfWrites; },
    set _tfWrites(v) { tfWrites = v; },
    get offsetHeight() { if (qTrackReads) qReadCount++; return 420; },
    style: {
      get transform() { return _tf; },
      set transform(v) { _tf = String(v); tfWrites++; },
      get willChange() { return _wc; },
      set willChange(v) { _wc = String(v); },
      left: "", right: "", top: "", bottom: "",
      setProperty(k, v) { styles.set(k, String(v)); },
      getPropertyValue(k) { return styles.get(k) || ""; },
    },
    _qs: {},
    querySelector(s) { if (!el._qs[s]) el._qs[s] = makeQEl(id + ":" + s); return el._qs[s]; },
    addEventListener(t, fn) { if (!ls.has(t)) ls.set(t, []); ls.get(t).push(fn); },
    dispatch(t, ev = {}) {
      const e = { type: t, target: el, preventDefault() {}, stopPropagation() {}, ...ev };
      for (const fn of [...(ls.get(t) || [])]) fn(e);
    },
    setAttribute(k, v) { attrs.set(k, String(v)); },
    getAttribute(k) { return attrs.get(k) ?? ""; },
    matches(s) { return s === ":popover-open" ? popOpen : false; },
    showPopover() { popOpen = true; el.dispatch("toggle", { newState: "open" }); },
    hidePopover() { popOpen = false; el.dispatch("toggle", { newState: "closed" }); },
    setPointerCapture() {},
    closest() { return null; },
    focus() {}, contains() { return false; },
    getBoundingClientRect() { if (qTrackReads) qReadCount++; return { left: 0, right: 390, top: 424, bottom: 844, width: 390, height: 420 }; },
  };
  return el;
}
const getQEl = (s) => { if (!qEls.has(s)) qEls.set(s, makeQEl(s)); return qEls.get(s); };
const qTimers = [];
const qCtx = {
  console, Math, Date, JSON, Set, Map, Array, Object, Number, String, Boolean, URL, Promise,
  performance: { now: () => nowMs },
  setInterval: () => 1, clearInterval() {},
  setTimeout: (fn) => { qTimers.push(fn); return qTimers.length; },
  clearTimeout() {},
  requestAnimationFrame: (fn) => { qRafQueue.push(fn); return qRafQueue.length; },
  cancelAnimationFrame() { qRafQueue = []; },
  CSS: { supports: () => true },
  matchMedia: (q) => ({ matches: q.includes("max-width: 760px") }),
  document: {
    querySelector: (s) => getQEl(s),
    querySelectorAll: () => [],
    createElement: (t) => makeQEl(t),
    body: { append() {} },
    addEventListener() {},
  },
};
qCtx.window = { innerWidth: 390, innerHeight: 844, addEventListener() {} };
vm.createContext(qCtx);
vm.runInContext(quotaJsSrc, qCtx);
qCtx.window.__collieQuota.init({ api: async () => ({ updated_at: "2026-11-28T10:00:00Z", agents: [] }), toast() {} });
qCtx.window.__collieQuota.open(getQEl("#nav-quota"));
const qPop = getQEl("#quota-popover");
const qHandle = qPop.querySelector(".qp-handle");
assert.equal(qPop.classList.contains("is-sheet"), true);

qHandle.dispatch("pointerdown", { target: qHandle, pointerId: 1, pointerType: "touch", clientX: 195, clientY: 500 });
qTrackReads = true;
qReadCount = 0;
qPop._tfWrites = 0;
qHandle.dispatch("pointermove", { target: qHandle, pointerId: 1, pointerType: "touch", clientX: 195, clientY: 580 });
qHandle.dispatch("pointermove", { target: qHandle, pointerId: 1, pointerType: "touch", clientX: 195, clientY: 660 });
assert.equal(qPop._tfWrites, 0, "quota sheet pointermove defers writes to rAF");
qRafQueue.splice(0).forEach((fn) => fn(nowMs));
assert.equal(qPop._tfWrites, 1, "quota sheet coalesces moves to 1 translate3d write");
assert.equal(qPop.style.transform, "translate3d(0, 160px, 0)");
assert.equal(qPop.style.willChange, "transform");
assert.equal(qReadCount, 0, "quota sheet performs 0 layout reads during drag");
nowMs += 100;
qHandle.dispatch("pointerup", { target: qHandle, pointerId: 1, pointerType: "touch", clientX: 195, clientY: 660 });
qTrackReads = false;
assert.equal(qReadCount, 0, "quota sheet performs 0 layout reads on release");
assert.equal(qPop.matches(":popover-open"), false, "quota sheet dismisses cleanly past threshold");
assert.equal(qPop.style.willChange, "", "quota sheet clears will-change on close");

console.log("ALL_MOBILE_DRAWER_DRAG_FPS_TESTS_OK");
`
	cmd := exec.Command("node", "-e", script)
	cmd.Dir = filepath.Join("..", "..")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("mobile drawer drag FPS regression test failed: %v\n%s", err, out)
	}
}
