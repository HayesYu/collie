package collie

import (
	"os/exec"
	"path/filepath"
	"testing"
)

func TestWebMobileWorkerDrawerJankAndOpenAnim(t *testing.T) {
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
const demoCssSrc = fs.readFileSync("docs/demo/app.css", "utf8");
const demoAppSrc = fs.readFileSync("docs/demo/app.js", "utf8");

// ─── 1. Static CSS & Demo Sync Anti-Regression Contracts ───
for (const [name, src] of [["web/app.css", cssSrc], ["docs/demo/app.css", demoCssSrc]]) {
  // 1a) Never apply content-visibility: auto to #wd-thread rows (causes 300-460px scroll jumps
  //     on bottom-pinned open & upward scroll, especially on Safari without overflow-anchor).
  assert.ok(!/#wd-thread[^{]*\{[^}]*content-visibility:\s*auto/.test(src), name + " must never apply content-visibility: auto to #wd-thread");
  assert.ok(src.includes("#wd-thread > .msg,\n#wd-thread > .tools,\n#wd-thread > .mine-group,\n#wd-thread > .ask-card {\n  contain: layout style;\n}"), name + " must isolate #wd-thread rows with contain: layout style");

  // 1b) Mobile & tablet sheet opening keyframes disable competing CSS transitions cleanly
  const openingDrawerMatches = src.match(/\.side-drawer\.is-sheet-opening\s*\{[^}]*transition:\s*none\s*!important;/g) || [];
  assert.ok(openingDrawerMatches.length >= 2, name + " must disable CSS transition on .side-drawer.is-sheet-opening for both <=1180px and <=760px");
  const openingScrimMatches = src.match(/\.drawer-scrim\.is-scrim-opening\s*\{[^}]*transition:\s*none\s*!important;/g) || [];
  assert.ok(openingScrimMatches.length >= 2, name + " must disable CSS transition on .drawer-scrim.is-scrim-opening for both <=1180px and <=760px");
  assert.ok(/\.side-drawer\.is-sheet-opening\s*\{[^}]*backface-visibility:\s*hidden;/.test(src), name + " mobile .side-drawer.is-sheet-opening must set backface-visibility: hidden");

  // 1c) Mobile sheet open pauses background #v-collie and in-sheet skeleton/dot animations
  assert.ok(src.includes(".side-drawer.is-sheet-opening .thread-skel .sk") && src.includes(":is(.app.has-worker, .app.has-thread, .app.has-file) #v-collie .working-row"), name + " mobile sheet open must pause background and skeleton ambient animations");

  // 1d) Fixed single-line height on .wd-live.is-working prevents #wd-thread clientHeight pumping
  assert.ok(/\.wd-live\s*\{[^}]*contain:\s*layout style;/.test(src), name + " .wd-live must isolate layout & style");
  const liveWorkingMatch = src.match(/\.wd-live\.is-working\s*\{([^}]*)\}/);
  assert.ok(liveWorkingMatch && liveWorkingMatch[1].includes("height: 35px"), name + " .wd-live.is-working must pin fixed 35px height");
  assert.ok(/\.wd-live\.is-working \.live-working pre\s*\{[^}]*white-space:\s*nowrap;[^}]*text-overflow:\s*ellipsis;/.test(src), name + " .wd-live.is-working pre must stay single-line with ellipsis");
}

// ─── 2. Static JS & Demo Sync Contracts ───
for (const [name, src] of [["web/app.js", appSrc], ["docs/demo/app.js", demoAppSrc]]) {
  assert.ok(src.includes("function isAnyMobileSheetAnimating()") && src.includes("function flushDeferredSheetUpdates()"), name + " must defer background scheduleLiveUpdates during mobile sheet slide");
  assert.ok(src.includes("if (isLayoutGliding() || isAnyMobileSheetAnimating()) return;"), name + " tableRO/codeRO syncs must defer while mobile sheets animate");
  assert.ok(!src.includes("if (w <= 0) { el._roLastW = 0; continue; }"), name + " tableRO/codeRO must preserve _roLastW across content-visibility: hidden reopen");
  assert.ok(src.includes("function prewarmWdThreadMd(") && src.includes("function cancelWdPrewarm()"), name + " must prewarm uncached assistant markdown purely in JS micro-slices during slide");
  assert.ok(src.includes("const isWdSlidePending =") && src.includes("function computeWdTermFontSize(") && src.includes("function applyWdTermFontSize("), name + " worker terminal must align slide deferral, initial font size, and measured cellH");
  assert.ok(src.includes("function flushWdPendingLive()"), name + " drawWdLive must defer during mobile slide and flush on settle");
  assert.ok(src.includes("wdLogEl._lastText"), name + " #wd-log stream updates must short-circuit identical frames");
  assert.ok((src.match(/if \(!desktopMode && rootVHSyncTimer\) syncRootVHNow\(\);/g) || []).length >= 3, name + " all three mobile drawers must flush pending --vh sync before unhiding");
  assert.ok(src.includes('wdDrawer?.classList?.contains("is-sheet-opening")') && src.includes('tdDrawer?.classList?.contains("is-sheet-opening")'), name + " readKbFlipPlan must skip keyboard FLIP while a mobile drawer sheet is opening");
}

// ─── 3. Runtime VM Verification (390x844 Mobile Viewport) ───
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
let reflowReads = [];
let trackReflow = false;
let liveInnerHTMLWrites = 0;

function makeEl(id = "", tagName = "DIV") {
  const listeners = new Map(), attrs = new Map(), styles = new Map();
  let _html = "", _text = "";
  const el = {
    id, tagName, localName: tagName.toLowerCase(), nodeType: 1,
    hidden: false, inert: false, disabled: false, value: "", tabIndex: 0,
    title: "", href: "", className: "",
    scrollTop: 400, scrollLeft: 0, clientWidth: 390, clientHeight: 620, scrollWidth: 390, scrollHeight: 1000,
    dataset: {}, children: [], parentElement: null, isConnected: true,
    classList: makeClassList(),
    style: {
      setProperty: (k, v) => styles.set(k, String(v)),
      removeProperty: (k) => styles.delete(k),
      getPropertyValue: (k) => styles.get(k) || "",
      transform: "", opacity: "", width: "", height: "", willChange: "", overflowAnchor: "",
    },
    get offsetWidth() { if (trackReflow) reflowReads.push("offW#" + id); return 390; },
    get offsetHeight() { if (trackReflow) reflowReads.push("offH#" + id); return 60; },
    get scrollHeight() { if (trackReflow) reflowReads.push("scrH#" + id); return el._sh ?? 1000; },
    set scrollHeight(v) { el._sh = v; },
    get clientHeight() { if (trackReflow) reflowReads.push("cliH#" + id); return el._ch ?? 620; },
    set clientHeight(v) { el._ch = v; },
    get innerHTML() { return _html; },
    set innerHTML(v) {
      _html = String(v);
      if (id === "wd-live") liveInnerHTMLWrites++;
    },
    get textContent() { return _text; },
    set textContent(v) { _text = String(v); },
    get childElementCount() { return el.children.length || (_html ? 1 : 0); },
    setAttribute: (k, v) => attrs.set(k, String(v)),
    getAttribute: (k) => (attrs.has(k) ? attrs.get(k) : null),
    removeAttribute: (k) => attrs.delete(k),
    hasAttribute: (k) => attrs.has(k),
    addEventListener: (t, fn) => { if (!listeners.has(t)) listeners.set(t, []); listeners.get(t).push(fn); },
    removeEventListener: (t, fn) => { const arr = listeners.get(t); if (arr) listeners.set(t, arr.filter((f) => f !== fn)); },
    dispatch: (type, ev = {}) => {
      const e = { type, target: el, currentTarget: el, preventDefault() {}, stopPropagation() {}, ...ev };
      for (const fn of [...(listeners.get(type) || [])]) fn(e);
      return e;
    },
    _qs: {},
    querySelector: (sel) => {
      if (sel in el._qs) return el._qs[sel];
      if (sel === ".in-active-thread") return null;
      return getEl(id + ":" + sel);
    },
    querySelectorAll: () => [],
    closest: () => null,
    contains: (n) => n === el,
    focus: () => {},
    blur: () => {},
    insertAdjacentHTML: (_pos, chunk) => { _html = String(chunk) + _html; },
    append: (...ns) => { for (const n of ns) if (n && typeof n === "object") { n.parentElement = el; el.children.push(n); } },
    appendChild: (n) => { if (n && typeof n === "object") { n.parentElement = el; el.children.push(n); } return n; },
    removeChild: (n) => { el.children = el.children.filter((x) => x !== n); return n; },
    getBoundingClientRect: () => {
      if (trackReflow) reflowReads.push("gBCR#" + id);
      return { top: 0, left: 0, right: 390, bottom: 620, width: 390, height: 620 };
    },
  };
  return el;
}

const byId = new Map();
const getEl = (sel) => {
  const key = String(sel || "");
  const id = key.startsWith("#") ? key.slice(1) : key;
  if (!byId.has(key)) byId.set(key, makeEl(id));
  return byId.get(key);
};
for (const id of [
  "app", "v-collie", "super-thread", "super-composer", "super-input", "super-send",
  "file-view", "file-scrim", "fv-body", "fv-title", "fv-path", "fv-return",
  "thread-drawer", "thread-scrim", "td-list", "td-body", "td-title", "td-meta", "td-workers", "td-return", "td-input", "td-send",
  "worker-drawer", "worker-scrim", "wd-viewer", "wd-thread", "wd-live", "wd-screen", "wd-log", "wd-jump",
  "wd-title", "wd-status", "wd-bar", "wd-summary", "wd-summary-text", "wd-return", "wd-gone", "wd-more-btn", "wd-more-menu",
  "wd-peek-foot", "wd-peek-note", "wd-interject-btn", "wd-composer", "wd-input", "wd-send", "wd-hint", "wd-mode-seg",
  "notes-pane", "notes-scrim", "open-notes-btn", "np-body", "np-date", "np-tasks-list", "np-files-list", "np-timers-list",
]) {
  const el = getEl("#" + id);
  if (id === "file-view" || id === "thread-drawer" || id === "worker-drawer" || id === "wd-live" || id === "wd-jump") {
    el.hidden = true;
  }
}
getEl("#notes-pane").classList.add("is-hidden");

// Wire #wd-live querySelector(".live-working") dynamically based on innerHTML
const wdLiveEl = getEl("#wd-live");
const liveRowEl = makeEl("wd-live-row");
const livePreEl = makeEl("wd-live-pre", "PRE");
const liveMetaEl = makeEl("wd-live-meta", "SPAN");
liveRowEl._qs = { pre: livePreEl, ".meta": liveMetaEl };
wdLiveEl.querySelector = (sel) => {
  if (sel === ".live-working") return wdLiveEl.innerHTML.includes("live-working") ? liveRowEl : null;
  return null;
};

// Mock Terminal (xterm.js) for screen-mode regression tests
const termOps = [];
class MockTerminal {
  constructor(opts) {
    this.cols = opts.cols;
    this.rows = opts.rows;
    this.options = { fontSize: opts.fontSize, lineHeight: opts.lineHeight || 1.15 };
    this._cellH = Math.ceil(this.options.fontSize * 1.5);
    this._core = {
      _renderService: { dimensions: { css: { cell: { height: this._cellH } } } },
      _charSizeService: {
        measure: () => {
          this._cellH = Math.ceil(this.options.fontSize * 1.5);
          this._core._renderService.dimensions.css.cell.height = this._cellH;
          termOps.push({ op: "measure", fs: this.options.fontSize, cellH: this._cellH });
        },
      },
    };
    this.buffer = { active: { baseY: 0, viewportY: 0 } };
    termOps.push({ op: "ctor", cols: this.cols, rows: this.rows, fs: this.options.fontSize });
  }
  attachCustomWheelEventHandler() {}
  open() { termOps.push({ op: "open", fs: this.options.fontSize, rows: this.rows }); }
  onData() {}
  onScroll() {}
  hasSelection() { return false; }
  blur() {}
  reset() { termOps.push({ op: "reset" }); }
  resize(c, r) { this.cols = c; this.rows = r; termOps.push({ op: "resize", cols: c, rows: r }); }
  write(out, cb) {
    const lineCount = String(out).split("\n").length;
    this.buffer.active.baseY = Math.max(0, lineCount - this.rows);
    termOps.push({ op: "write", len: out.length });
    if (cb) cb();
  }
  scrollToBottom() {
    this.buffer.active.viewportY = this.buffer.active.baseY;
    termOps.push({ op: "scrollToBottom", y: this.buffer.active.viewportY });
  }
  scrollToLine(line) {
    this.buffer.active.viewportY = line;
    termOps.push({ op: "scrollToLine", y: line });
  }
}

const timers = new Map();
let timerSeq = 1;
let roCallbacks = [];
const noop = () => {};
const ctx = {
  console,
  setTimeout: (fn, ms = 0) => { const id = timerSeq++; timers.set(id, { fn, ms, kind: "timer" }); return id; },
  clearTimeout: (id) => { timers.delete(id); },
  setInterval: () => 0, clearInterval: noop,
  requestAnimationFrame: (fn) => { const id = timerSeq++; timers.set(id, { fn, ms: 16, kind: "raf" }); return id; },
  cancelAnimationFrame: (id) => { timers.delete(id); },
  performance: { now: () => nowMs },
  innerWidth: 390, innerHeight: 844, devicePixelRatio: 3,
  visualViewport: { height: 844, width: 390, offsetTop: 0, scale: 1, addEventListener: noop, removeEventListener: noop },
  navigator: { userAgent: "Mobile", platform: "iPhone", clipboard: { writeText: async () => {} } },
  location: { hash: "#/collie", origin: "http://127.0.0.1:8080", pathname: "/", search: "" },
  history: { state: {}, replaceState: noop, pushState: noop },
  localStorage: { getItem: () => null, setItem: noop, removeItem: noop },
  sessionStorage: { getItem: () => null, setItem: noop, removeItem: noop },
  matchMedia: (q) => ({ matches: q.includes("max-width: 760px") || q.includes("no-preference") || q.includes("coarse"), addEventListener: noop }),
  CSS: { escape: (s) => String(s) },
  URL, URLSearchParams, Event: class { constructor(t) { this.type = t; } },
  HTMLElement: { prototype: { onbeforematch: null } },
  HTMLDialogElement: { prototype: {} },
  ResizeObserver: class {
    constructor(cb) { this.cb = cb; roCallbacks.push(cb); }
    observe() {} unobserve() {} disconnect() {}
  },
  MutationObserver: class { observe() {} disconnect() {} },
  IntersectionObserver: class { observe() {} unobserve() {} disconnect() {} },
  EventSource: class { constructor() { this.ls = new Map(); } addEventListener(t, fn) { this.ls.set(t, fn); } close() {} },
  Terminal: MockTerminal,
  getComputedStyle: (el) => ({
    paddingTop: "8px", paddingBottom: "24px", paddingLeft: "10px", paddingRight: "10px",
    getPropertyValue: (k) => (k === "--mono" ? "monospace" : ""),
  }),
  fetch: async () => ({ ok: true, json: async () => ({}), text: async () => "" }),
  addEventListener: noop, removeEventListener: noop, dispatchEvent: noop,
  document: {
    documentElement: makeEl("html", "HTML"),
    head: makeEl("head", "HEAD"),
    body: makeEl("body", "BODY"),
    activeElement: null,
    hidden: false,
    visibilityState: "visible",
    querySelector: (sel) => {
      if (sel === ".side-drawer.is-sheet-opening") {
        for (const id of ["#worker-drawer", "#thread-drawer", "#file-view"]) {
          const el = getEl(id);
          if (el.classList.contains("is-sheet-opening")) return el;
        }
        return null;
      }
      if (sel.includes(".in-active-thread")) return null;
      return getEl(sel);
    },
    querySelectorAll: () => [],
    createElement: (tag) => makeEl("", tag),
    addEventListener: noop, removeEventListener: noop,
  },
};
ctx.window = ctx;
ctx.globalThis = ctx;

const marker = "  boot();\n})();";
const expose = [
  "  globalThis.__wdJankTest = {",
  "    setState: (s) => { state = s; },",
  "    setSup: (s) => { sup = s; },",
  "    setView: (v) => { view = v; },",
  "    openWorkerDrawer, closeWorkerDrawer, drawWdLive, drawWdScreen, fitWdTerm, drawThread,",
  "    isWdSlidePending, isWdSheetAnimating, isAnyMobileSheetAnimating, scheduleLiveUpdates,",
  "    getDeferredSheetMask: () => deferredSheetMask,",
  "    getLiveUpdateMask: () => liveUpdateMask,",
  "    getWdPendingScreen: () => wdPendingScreen,",
  "    getWdPendingLive: () => wdPendingLive,",
  "    getWdTerm: () => wdTerm,",
  "    setWdMode: (m) => { wdMode = m; },",
  "    setDevMode: (on) => { store.set('devMode', !!on); },",
  "    readKbFlipPlan, setVH,",
  "    setPendingVH: (vh) => { lastVH = vh; rootVHSyncTimer = setTimeout(syncRootVHNow, 140); },",
  "    hasRootVHTimer: () => !!rootVHSyncTimer,",
  "    LIVE_SUPER, LIVE_SIDE,",
  "  };",
  "})();",
].join("\n");
vm.runInNewContext(appSrc.replace(marker, expose), ctx, { filename: "web/app.js" });
const T = ctx.__wdJankTest;

T.setView("collie");
T.setState({
  ok: true,
  machines: [{ id: "", name: "devbox", home: "/tmp", local: true, online: true }],
  agents: [{ pane_id: "w5:p1", workspace_id: "w5", name: "wk-live", label: "Live Worker", kind: "codex", status: "working", is_agent: true, cwd: "/tmp/work" }],
  shells: [],
});
T.setSup({
  router: { alive: true, status: "idle", pane_id: "w1:p1", kind: "claude" },
  entries: [],
  workers: [{ name: "wk-live", label: "Live Worker", kind: "codex", cwd: "/tmp/work", pane_id: "w5:p1", status: "working" }],
});

// ─── 3a) openWorkerDrawer flushes pending --vh timer, skips kbFlip while opening, defers live updates ───
T.setPendingVH("780px");
assert.equal(T.hasRootVHTimer(), true, "setup pending rootVHSyncTimer");
reflowReads = [];
trackReflow = true;
T.openWorkerDrawer({ worker: "wk-live" });
assert.equal(reflowReads.length, 0, "openWorkerDrawer on mobile must trigger zero forced reflows: " + reflowReads.join(","));
assert.equal(T.hasRootVHTimer(), false, "openWorkerDrawer must flush stale rootVHSyncTimer before unhiding");
assert.equal(ctx.document.documentElement.style.getPropertyValue("--vh"), "780px", "root --vh updated synchronously before sheet opens");
assert.equal(getEl("#worker-drawer").classList.contains("is-sheet-opening"), true, "worker drawer starts GPU sheet opening animation");
assert.equal(T.isAnyMobileSheetAnimating(), true, "isAnyMobileSheetAnimating reports active slide");
assert.equal(T.readKbFlipPlan(-144, null), null, "readKbFlipPlan must skip keyboard FLIP while #worker-drawer.is-sheet-opening is active");

// Background SSE live updates must defer during opening slide
T.scheduleLiveUpdates(T.LIVE_SUPER | T.LIVE_SIDE);
assert.equal(T.getDeferredSheetMask() & (T.LIVE_SUPER | T.LIVE_SIDE), T.LIVE_SUPER | T.LIVE_SIDE, "background super/side updates defer during mobile sheet slide");
assert.equal(T.getLiveUpdateMask(), 0, "no immediate liveUpdateMask flushed mid-slide");

// ─── 3b) drawWdLive defers during slide (0 reflows), then patches in place across ticks without innerHTML churn ───
reflowReads = [];
liveInnerHTMLWrites = 0;
T.drawWdLive({ status: "working", tail: "line1\nline2", meta: "1s" });
assert.equal(reflowReads.length, 0, "drawWdLive during opening slide must trigger 0 forced reflows: " + reflowReads.join(","));
assert.equal(liveInnerHTMLWrites, 0, "drawWdLive during opening slide must not touch DOM");
assert.ok(T.getWdPendingLive(), "drawWdLive queues pending live payload for post-slide flush");
trackReflow = false;

// Settle opening animation via animationend
nowMs += 280;
getEl("#worker-drawer").dispatch("animationend");
assert.equal(getEl("#worker-drawer").classList.contains("is-sheet-opening"), false, "animationend clears .is-sheet-opening");
assert.equal(T.getDeferredSheetMask(), 0, "animationend flushes deferred background live updates");
assert.equal(wdLiveEl.hidden, false, "post-slide flush reveals #wd-live");
assert.equal(wdLiveEl.classList.contains("is-working"), true, "#wd-live marks .is-working");
assert.equal(liveInnerHTMLWrites, 1, "initial .live-working mounts once via innerHTML");
assert.equal(livePreEl.textContent, "line1 · line2", "multiline tail flattened to single line");

// Subsequent working ticks (including transient empty tail) must NOT rebuild innerHTML or collapse bar
T.drawWdLive({ status: "working", tail: "", meta: "2s" });
assert.equal(liveInnerHTMLWrites, 1, "transient empty tail keeps .live-working mounted with 0 innerHTML rebuilds");
assert.equal(wdLiveEl.hidden, false, "transient empty tail while working must not hide/collapse #wd-live");
assert.equal(livePreEl.textContent, "line1 · line2", "transient empty tail preserves last tail text");
T.drawWdLive({ status: "working", tail: "step 2\nrunning", meta: "3s" });
assert.equal(liveInnerHTMLWrites, 1, "new tail patches pre.textContent in place with 0 innerHTML rebuilds");
assert.equal(livePreEl.textContent, "step 2 · running", "patched tail flattens newlines");

// ─── 3c) drawWdScreen: zero infinite recursion when .is-sheet-opening lingers past _slideSettleAt - 16 ───
T.closeWorkerDrawer({ immediate: true });
T.setDevMode(true);
T.setWdMode("screen");
nowMs = 3000;
termOps.length = 0;
T.openWorkerDrawer({ worker: "wk-live", mode: "screen" });
assert.equal(T.getWdTerm(), null, "terminal cold open defers xterm mount until after mobile slide settles");
assert.equal(T.isWdSlidePending(), true, "isWdSlidePending true right after open");

// Frame arrives mid-slide -> buffered in wdPendingScreen, NOT mounted mid-slide
const frame1 = { text: Array.from({ length: 80 }, (_, i) => "line " + i).join("\n"), cols: 120, rows: 32 };
T.drawWdScreen(frame1);
assert.equal(T.getWdTerm(), null, "frame arriving mid-slide does not mount xterm during slide");
assert.equal(T.getWdPendingScreen(), frame1, "frame buffered in wdPendingScreen");

// CRITICAL REGRESSION: advance clock past _slideSettleAt - 16 while .is-sheet-opening is STILL on drawer!
// Must NEVER throw RangeError: Maximum call stack size exceeded.
nowMs = 3300;
assert.equal(getEl("#worker-drawer").classList.contains("is-sheet-opening"), true, ".is-sheet-opening still present before animationend");
assert.equal(T.isWdSlidePending(), false, "isWdSlidePending aligned with scheduleAfterWdSlide threshold");
T.drawWdScreen(frame1);
const term = T.getWdTerm();
assert.ok(term, "drawWdScreen mounts xterm cleanly without recursion once slide settle window passes");
const ctorOp = termOps.find((o) => o.op === "ctor");
assert.ok(ctorOp && ctorOp.fs === 7.5, "ensureWdTerm precomputes mobile fontSize (7.5px) instead of flashing 12px -> 7.5px, got " + JSON.stringify(ctorOp));
assert.ok(termOps.some((o) => o.op === "measure"), "ensureWdTerm measures real cell height immediately after open");

// Scroll up in terminal -> incoming frame with same head must preserve viewportY (savedLine)
getEl("#worker-drawer").dispatch("animationend");
term.buffer.active.baseY = 40;
term.buffer.active.viewportY = 10;
termOps.length = 0;
const frame2 = { text: Array.from({ length: 85 }, (_, i) => "line " + i).join("\n"), cols: 120, rows: 32 };
T.drawWdScreen(frame2);
assert.ok(!termOps.some((o) => o.op === "scrollToBottom"), "scrolled-up terminal must not snap to bottom on appended frame");
assert.equal(term.buffer.active.viewportY, 10, "scrolled-up terminal preserves saved viewportY line");
`
	cmd := exec.Command("node", "-e", script)
	cmd.Dir = filepath.Join("..", "..")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("mobile worker drawer jank test failed: %v\n%s", err, out)
	}
}
