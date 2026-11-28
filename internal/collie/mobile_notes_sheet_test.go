package collie

import (
	"os/exec"
	"path/filepath"
	"testing"
)

func TestWebMobileNotesBottomSheetAlignment(t *testing.T) {
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
const demoHtmlSrc = fs.readFileSync("docs/demo/index.html", "utf8");
const demoCssSrc = fs.readFileSync("docs/demo/app.css", "utf8");
const demoAppSrc = fs.readFileSync("docs/demo/app.js", "utf8");

// ─── 1. Static HTML / CSS / JS Contracts ───
// 1a) Redundant mobile left "<" back arrow (.np-back / #np-back) is completely removed
for (const [name, src] of [
  ["web/index.html", htmlSrc],
  ["web/app.css", cssSrc],
  ["web/app.js", appSrc],
  ["docs/demo/index.html", demoHtmlSrc],
  ["docs/demo/app.css", demoCssSrc],
  ["docs/demo/app.js", demoAppSrc],
]) {
  assert.ok(!src.includes("np-back"), name + " must not contain redundant np-back arrow");
}

// 1b) Mobile (<= 760px) notebook sheet geometry, shadow, scrim, handle, and header match the 3 unified drawers
const mobileNotesIdx = cssSrc.indexOf("/* Mobile (<= 760px): unified rounded bottom sheet aligned with .filepane / .threadpane / .workerpane */");
assert.ok(mobileNotesIdx > 0, "mobile unified bottom sheet section for .notepane must exist");
const mobileNotesCss = cssSrc.slice(mobileNotesIdx, cssSrc.indexOf("@media (prefers-reduced-motion: reduce)", mobileNotesIdx));

assert.ok(
  mobileNotesCss.includes("height: min(calc(var(--vh) * 0.92), calc(var(--vh) - max(36px, env(safe-area-inset-top) + 10px)));"),
  "mobile .notepane height must match .filepane / .threadpane / .workerpane"
);
assert.ok(!mobileNotesCss.includes("var(--vh) * 0.80"), "legacy 0.80vh cap must be removed");
assert.ok(mobileNotesCss.includes("border-radius: 20px 20px 0 0;"), "mobile .notepane border-radius must be 20px 20px 0 0");
assert.ok(!mobileNotesCss.includes("18px 18px 0 0"), "legacy 18px radius must be removed");
assert.ok(
  mobileNotesCss.includes("box-shadow: 0 -8px 32px rgba(0, 0, 0, .14), 0 -1px 4px rgba(0, 0, 0, .06);"),
  "mobile light .notepane shadow must match unified bottom sheets"
);
assert.ok(
  mobileNotesCss.includes(":root[data-theme=\"dark\"] .notepane,\n  :root[data-theme=\"dark\"] .notepane.is-expanded") &&
    mobileNotesCss.includes("background: var(--bg);") &&
    mobileNotesCss.includes("box-shadow: 0 -12px 40px rgba(0, 0, 0, .58);"),
  "mobile dark .notepane (including .is-expanded) must match unified sheet background and shadow"
);
assert.ok(
  mobileNotesCss.includes(".notes-scrim { background: rgba(10, 10, 12, .38); }") &&
    mobileNotesCss.includes(':root[data-theme="dark"] .notes-scrim { background: rgba(0, 0, 0, .62); }') &&
    mobileNotesCss.includes(':root:not([data-theme="light"]) .notes-scrim { background: rgba(0, 0, 0, .62); }'),
  "mobile .notes-scrim light/dark opacity must match .drawer-scrim"
);

// Grabber (24px height, 38x4 pill widening to 44px while dragging, touch-action: none)
assert.ok(
  /\.np-handle\s*\{[\s\S]*?height:\s*24px;\s*padding:\s*8px 0 4px;[\s\S]*?touch-action:\s*none;\s*cursor:\s*grab;/.test(mobileNotesCss),
  "mobile .np-handle must be 24px tall with matching padding, touch-action:none and grab cursor"
);
assert.ok(
  mobileNotesCss.includes(".notepane.is-dragging .np-handle-pill { background: var(--fg-3); width: 44px; }"),
  "mobile .np-handle-pill must widen to 44px with dragging feedback"
);
assert.ok(
  mobileNotesCss.includes(".notepane.is-dragging") && mobileNotesCss.includes("transition: none !important;"),
  "mobile .notepane.is-dragging must disable transitions for 1:1 drag tracking"
);

// Header padding, min-height, typography, close button aligned with .sd-head / .fv-head / .td-head / .wd-head
assert.ok(
  /\.notepane \.np-head\s*\{[\s\S]*?gap:\s*10px;[\s\S]*?padding:\s*2px 10px 10px 16px;\s*height:\s*auto;\s*min-height:\s*46px;\s*padding-top:\s*2px;[\s\S]*?touch-action:\s*pan-x;/.test(mobileNotesCss),
  "mobile .np-head must match .sd-head padding, min-height and touch-action"
);
assert.ok(mobileNotesCss.includes(".notepane .np-title { font-size: 14.5px; }"), "mobile .np-title must match .sd-title 14.5px");
assert.ok(
  mobileNotesCss.includes(".notepane .np-act-btn { min-inline-size: 30px; min-block-size: 30px; width: 30px; height: 30px;") &&
    mobileNotesCss.includes(".notepane .np-act-btn svg { width: 16px; height: 16px; stroke-width: 1.7; }"),
  "mobile .np-act-btn close button must match .sd-act-btn dimensions and icon stroke"
);

// Scroll containment on scrim, sheet, and body (zero background scroll bleed-through)
assert.ok(
  /\.notes-scrim\s*\{[\s\S]*?overflow:\s*hidden;\s*overscroll-behavior:\s*contain;\s*touch-action:\s*none;/.test(cssSrc),
  ".notes-scrim must trap background scroll chaining"
);
assert.ok(
  /\.notepane\s*\{[\s\S]*?overflow:\s*hidden;\s*overscroll-behavior:\s*contain;/.test(cssSrc) &&
    mobileNotesCss.includes("overscroll-behavior: contain;"),
  ".notepane must set overscroll-behavior: contain"
);
assert.ok(/\.np-body\s*\{[\s\S]*?overscroll-behavior:\s*contain;/.test(cssSrc), ".np-body must contain scroll chaining");

// Desktop & tablet rules remain intact (zero regression)
assert.ok(
  /\.notepane\s*\{[\s\S]*?border-radius:\s*14px;/.test(cssSrc) &&
    cssSrc.includes(".notepane .np-head { height: var(--head-h); padding: 0 10px 0 16px; border-bottom: 1px solid var(--line); }"),
  "desktop floating card and tablet slide-over header rules must remain intact"
);

// Shared mobile drag helper wired to #notes-pane and allows .np-handle
assert.ok(
  appSrc.includes('button:not(.sd-handle):not(.np-handle), a') &&
    appSrc.includes('bindDrawerSheetDrag(\n    notesPane,\n    notesScrim,\n    [$("#np-handle"), npHeadEl],'),
  "app.js must wire shared bindDrawerSheetDrag to #notes-pane with #np-handle and #np-head"
);

// Demo synced
assert.ok(demoCssSrc.includes("height: min(calc(var(--vh) * 0.92), calc(var(--vh) - max(36px, env(safe-area-inset-top) + 10px)));"), "docs/demo/app.css must be synced");
assert.ok(demoAppSrc.includes("bindDrawerSheetDrag(\n    notesPane,"), "docs/demo/app.js must be synced");

// ─── 2. Runtime VM Tests: Mobile Drag/Tap/Close + Tablet & Desktop Zero Regression ───
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
function makeEl(id = "", tagName = "DIV") {
  const listeners = new Map(), attrs = new Map(), styles = new Map();
  const el = {
    id, tagName, value: "", textContent: "", innerHTML: "",
    hidden: false, disabled: false, checked: false, open: false, inert: false, tabIndex: 0,
    className: "", title: "", href: "",
    style: {
      transform: "", opacity: "", transition: "",
      setProperty(k, v) { styles.set(k, String(v)); },
      getPropertyValue(k) { return styles.get(k) || ""; },
      removeProperty(k) { styles.delete(k); },
    },
    dataset: {}, classList: makeClassList(), children: [], selectedOptions: [], options: [],
    scrollTop: 0, scrollHeight: 800, clientHeight: 600, scrollWidth: 390, clientWidth: 390,
    offsetWidth: 390, offsetHeight: 776, offsetLeft: 0, offsetParent: {}, isConnected: true,
    _qs: {}, _qsa: {}, _blurCount: 0, _focusCount: 0, _captured: [],
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
    remove() {}, after() {},
    focus() { el._focusCount++; doc.activeElement = el; },
    blur() { el._blurCount++; if (doc.activeElement === el) doc.activeElement = doc.body; },
    click() {}, scrollIntoView() {},
    showModal() { el.open = true; }, close() { el.open = false; },
    getBoundingClientRect() { return { top: 0, bottom: 776, left: 0, right: 390, width: 390, height: 776 }; },
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
getEl("#worker-scrim").hidden = true;

// Wire #notes-pane child relationships and closest() behavior
const notesPane = getEl("#notes-pane");
const notesScrim = getEl("#notes-scrim");
const npHandle = getEl("#np-handle");
const npHead = getEl("#np-head");
const npTitle = getEl("#np-title");
const npClose = getEl("#np-close");
notesPane.classList.add("is-hidden");
notesPane.contains = (n) => [notesPane, npHandle, npHead, npTitle, npClose].includes(n);
npHandle.closest = (sel) => (sel.includes("#np-handle") ? npHandle : null);
npTitle.closest = () => null;
npClose.closest = (sel) => (sel.includes("button:not(.sd-handle):not(.np-handle)") || sel.includes("#np-close") ? npClose : null);

const docListeners = new Map();
const storage = new Map();
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
  addEventListener(type, fn) { if (!docListeners.has(type)) docListeners.set(type, []); docListeners.get(type).push(fn); },
  removeEventListener() {},
};
doc.activeElement = doc.body;

const ctx = {
  console, Math, Date, JSON, Set, Map, RegExp, Error, Promise, URL, URLSearchParams, Object, Array, Number, String, Boolean,
  setTimeout: () => 1, clearTimeout() {}, setInterval: () => 1, clearInterval() {}, queueMicrotask,
  requestAnimationFrame: (fn) => { fn(nowMs); return 1; },
  cancelAnimationFrame() {},
  document: doc,
  location: { hash: "#/collie", origin: "http://127.0.0.1:7788", href: "http://127.0.0.1:7788/#/collie", pathname: "/", search: "" },
  history: { state: null, pushState() {}, replaceState(s, _, url) { if (typeof url === "string" && url.startsWith("#")) ctx.location.hash = url; }, back() {} },
  navigator: { platform: "iPhone", clipboard: { writeText: async () => {} } },
  isSecureContext: true, innerWidth: 390, innerHeight: 844,
  CSS: { escape: (s) => String(s), supports: () => true },
  HTMLDialogElement: { prototype: {} },
  Blob, Uint8Array, TextEncoder, TextDecoder, atob, AbortController,
  crypto: { randomUUID: () => "mobile-notes-uuid" },
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
    matches: (q.includes("max-width: 760px") && ctx.innerWidth <= 760)
      || (q.includes("max-width: 1180px") && ctx.innerWidth <= 1180),
    addEventListener() {},
  }),
  getComputedStyle: (el) => ({ getPropertyValue: (k) => el?.style?.getPropertyValue?.(k) || "" }),
  getSelection: () => ({ isCollapsed: true, toString: () => "" }),
  addEventListener() {}, removeEventListener() {}, dispatchEvent() {},
  fetch: async () => ({ ok: true, status: 200, json: async () => ({ ok: true, date: "2026-11-28", tasks: [], artifacts: [], timers: [] }) }),
};
ctx.window = ctx;
ctx.globalThis = ctx;

const marker = "  boot();\n})();";
const instrumented = appSrc.replace(
  marker,
  "  globalThis.__test = { getNotesOpen: () => notesOpen, setSidePaneOverlay: (v) => { sidePaneOverlay = v; }, setSidePanePref: (v) => { sidePanePref = v; }, route, syncNotebookVisibility };\n})();"
);
vm.runInNewContext(instrumented, ctx, { filename: "web/app.js" });
const T = ctx.__test;

function openMobileNotes() {
  ctx.innerWidth = 390; ctx.innerHeight = 844;
  ctx.location.hash = "#/collie";
  T.route();
  getEl("#open-notes-btn").dispatch("click");
  assert.equal(T.getNotesOpen(), true, "mobile notes sheet opens");
  assert.equal(getEl("#v-collie").classList.contains("has-notes"), true);
  assert.equal(notesPane.classList.contains("is-hidden"), false);
  assert.equal(notesPane.getAttribute("aria-hidden"), "false");
  assert.equal(notesPane.inert, false);
}

// 2a) Mobile partial pull-down (< threshold, slow) tracks finger and snaps back cleanly without closing
openMobileNotes();
nowMs = 2000;
npHandle.dispatch("pointerdown", { target: npHandle, pointerId: 1, pointerType: "touch", clientX: 195, clientY: 100 });
nowMs = 2400;
npHandle.dispatch("pointermove", { target: npHandle, pointerId: 1, pointerType: "touch", clientX: 195, clientY: 160 });
assert.equal(notesPane.classList.contains("is-dragging"), true, "dragging down on #np-handle adds .is-dragging");
assert.equal(notesPane.style.transform, "translate3d(0, 60px, 0)", "sheet follows finger during pull-down");
assert.ok(Number(notesScrim.style.opacity) < 1 && Number(notesScrim.style.opacity) > 0, "scrim opacity fades smoothly with drag distance");
nowMs = 2700;
npHandle.dispatch("pointerup", { target: npHandle, pointerId: 1, pointerType: "touch", clientX: 195, clientY: 160 });
assert.equal(notesPane.classList.contains("is-dragging"), false, "releasing drag clears .is-dragging");
assert.equal(notesPane.style.transform, "", "releasing drag clears inline transform");
assert.equal(notesScrim.style.opacity, "", "releasing drag restores scrim opacity");
assert.equal(T.getNotesOpen(), true, "short slow drag snaps back and stays open");
// Immediate post-drag synthetic click is ignored by skipClickUntil
nowMs = 2720;
npHandle.dispatch("click");
assert.equal(T.getNotesOpen(), true, "post-drag click within guard window does not dismiss sheet");

// 2b) Mobile pull-down past threshold on #np-handle dismisses sheet cleanly
nowMs = 3000;
npHandle.dispatch("pointerdown", { target: npHandle, pointerId: 2, pointerType: "touch", clientX: 195, clientY: 100 });
nowMs = 3180;
npHandle.dispatch("pointermove", { target: npHandle, pointerId: 2, pointerType: "touch", clientX: 195, clientY: 260 });
assert.equal(notesPane.style.transform, "translate3d(0, 160px, 0)");
nowMs = 3220;
npHandle.dispatch("pointerup", { target: npHandle, pointerId: 2, pointerType: "touch", clientX: 195, clientY: 260 });
assert.equal(T.getNotesOpen(), false, "pull-down past threshold closes mobile notes sheet");
assert.equal(getEl("#v-collie").classList.contains("has-notes"), false);
assert.equal(notesPane.classList.contains("is-hidden"), true);
assert.equal(notesPane.getAttribute("aria-hidden"), "true");
assert.equal(notesPane.inert, true);
assert.equal(notesPane.style.transform, "");
assert.equal(notesScrim.style.opacity, "");

// 2c) Mobile pull-down from #np-head title area dismisses sheet; horizontal swipe cancels drag
openMobileNotes();
nowMs = 4000;
npHead.dispatch("pointerdown", { target: npTitle, pointerId: 3, pointerType: "touch", clientX: 120, clientY: 110 });
npHead.dispatch("pointermove", { target: npTitle, pointerId: 3, pointerType: "touch", clientX: 165, clientY: 114 });
assert.equal(notesPane.classList.contains("is-dragging"), false, "horizontal swipe does not start vertical sheet drag");
npHead.dispatch("pointerup", { target: npTitle, pointerId: 3, pointerType: "touch", clientX: 165, clientY: 114 });
assert.equal(T.getNotesOpen(), true);

nowMs = 4200;
npHead.dispatch("pointerdown", { target: npTitle, pointerId: 4, pointerType: "touch", clientX: 120, clientY: 110 });
nowMs = 4260;
npHead.dispatch("pointermove", { target: npTitle, pointerId: 4, pointerType: "touch", clientX: 122, clientY: 190 });
nowMs = 4290;
npHead.dispatch("pointerup", { target: npTitle, pointerId: 4, pointerType: "touch", clientX: 122, clientY: 190 });
assert.equal(T.getNotesOpen(), false, "fast downward flick on #np-head dismisses mobile notes sheet");

// 2d) Pressing #np-close does NOT initiate drag, blurs focused close button, and closes cleanly
openMobileNotes();
npClose.focus();
const blursBefore = npClose._blurCount;
nowMs = 5000;
npHead.dispatch("pointerdown", { target: npClose, pointerId: 5, pointerType: "touch", clientX: 360, clientY: 110 });
npHead.dispatch("pointermove", { target: npClose, pointerId: 5, pointerType: "touch", clientX: 360, clientY: 260 });
assert.equal(notesPane.classList.contains("is-dragging"), false, "pointerdown on #np-close must not hijack click into drag");
npClose.dispatch("click");
assert.equal(T.getNotesOpen(), false, "clicking #np-close dismisses mobile notes sheet");
assert.equal(npClose._blurCount, blursBefore + 1, "closing notes sheet blurs focused descendant before aria-hidden");

// 2e) Tapping #np-handle without dragging dismisses sheet
openMobileNotes();
nowMs = 6000;
npHandle.dispatch("click");
assert.equal(T.getNotesOpen(), false, "tapping #np-handle closes mobile notes sheet");

// 2f) Tablet (768x1024) & Desktop (1440x900): drag helper is completely inactive (zero transform jitter)
for (const [w, h, openViaBtn] of [[768, 1024, true], [1440, 900, false]]) {
  ctx.innerWidth = w; ctx.innerHeight = h;
  T.setSidePanePref("notes");
  T.route();
  if (openViaBtn) {
    T.setSidePaneOverlay("notes");
    T.syncNotebookVisibility({ immediate: true });
  } else {
    T.syncNotebookVisibility({ immediate: true });
  }
  assert.equal(T.getNotesOpen(), true, "notes open at " + w + "x" + h);
  notesPane.style.transform = "";
  nowMs += 500;
  npHead.dispatch("pointerdown", { target: npTitle, pointerId: 10 + w, pointerType: "mouse", button: 0, clientX: 200, clientY: 100 });
  npHead.dispatch("pointermove", { target: npTitle, pointerId: 10 + w, pointerType: "mouse", button: 0, clientX: 200, clientY: 300 });
  assert.equal(notesPane.classList.contains("is-dragging"), false, "no bottom-sheet drag at width=" + w);
  assert.equal(notesPane.style.transform, "", "zero inline transform on #notes-pane at width=" + w);
  npHead.dispatch("pointerup", { target: npTitle, pointerId: 10 + w, pointerType: "mouse", button: 0, clientX: 200, clientY: 300 });
  assert.equal(T.getNotesOpen(), true, "notes remain open after pointer gesture at width=" + w);
}

console.log("ALL_MOBILE_NOTES_SHEET_TESTS_OK");
`
	cmd := exec.Command("node", "-e", script)
	cmd.Dir = filepath.Join("..", "..")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("mobile notes bottom sheet test failed: %v\n%s", err, out)
	}
}
