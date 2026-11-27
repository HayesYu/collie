package collie

import (
	"os/exec"
	"path/filepath"
	"testing"
)

func TestWebAccentPolishAndPresets(t *testing.T) {
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

// ─── 1. Static HTML & CSS visual hierarchy contracts ───
assert.ok(htmlSrc.includes('<meta name="color-scheme" content="light dark">'), "index.html must declare color-scheme meta");
assert.ok(
  htmlSrc.includes('g("accent")') && htmlSrc.includes('document.documentElement.dataset.accent = a'),
  "index.html pre-paint script must restore collie.accent before first paint"
);

function cssBlock(selector) {
  const esc = selector.replace(/[.*+?^${}()|[\]\\]/g, "\\$&");
  const m = cssSrc.match(new RegExp(esc + "\\s*\\{([^}]*)\\}"));
  assert.ok(m, "expected CSS block for " + selector);
  return m[1];
}

// Neutral monochrome thread navigation & active states (no --attn tint)
for (const sel of [
  ".th-head-btn.has-active",
  ".th-head-btn.has-active .th-head-cnt",
  ".thread-entry.active",
  ".thread-entry.active .te-ico",
  ".thread-entry.active .te-count",
  ".thread-entry.active .te-chev",
  ".msg-acts button.focus-btn.active",
  ".item-th-btn.active",
  ".th-pop-item.active",
  ".th-pop-item.active .th-pop-title",
]) {
  assert.ok(!cssBlock(sel).includes("--attn"), sel + " must stay neutral monochrome without --attn");
}

// Neutral pending tray surface, border, label, dismiss; only .pending-dot lights up with --attn
for (const sel of [".pending-bar", ".pending-label", ".pending-x"]) {
  assert.ok(!cssBlock(sel).includes("--attn"), sel + " must stay neutral without --attn fill/border/text");
}
assert.ok(cssBlock(".pending-dot").includes("var(--attn)"), ".pending-dot must use var(--attn)");
assert.ok(
  cssBlock(".pending-dot.neutral").includes("var(--fg-3)") && !cssBlock(".pending-dot.neutral").includes("--attn"),
  ".pending-dot.neutral must be neutral gray without --attn"
);

// True attention anchors use --attn cleanly
for (const sel of [".unread-sep", ".count-pill.attn", ".count-pill.unread", ".super-jump .sj-badge", "#router-ctx.warn"]) {
  assert.ok(cssBlock(sel).includes("--attn"), sel + " must use --attn");
}
assert.ok(
  cssBlock(".toast.attn").includes("var(--attn-solid)") && !cssBlock(".toast.attn").includes("#b45309"),
  ".toast.attn must use --attn-solid token instead of hardcoded amber"
);
assert.ok(!cssSrc.includes("#9a4208; color: #fff"), "dark .toast.attn must not hardcode amber background");

// All 4 accent presets exist in both light and dark modes with WCAG AA >= 4.5:1 contrast
for (const preset of ["cobalt", "pine", "ink"]) {
  assert.ok(cssSrc.includes(':root[data-accent="' + preset + '"]'), "missing light CSS preset for " + preset);
  assert.ok(cssSrc.includes(':root[data-theme="dark"][data-accent="' + preset + '"]'), "missing explicit dark CSS preset for " + preset);
  assert.ok(cssSrc.includes(':root:not([data-theme="light"])[data-accent="' + preset + '"]'), "missing system dark CSS preset for " + preset);
}

function hexToRgb(h) {
  const s = h.replace("#", "");
  return [0, 2, 4].map((i) => parseInt(s.slice(i, i + 2), 16) / 255);
}
function relLum(h) {
  const [r, g, b] = hexToRgb(h).map((c) => (c <= 0.03928 ? c / 12.92 : ((c + 0.055) / 1.055) ** 2.4));
  return 0.2126 * r + 0.7152 * g + 0.0722 * b;
}
function contrast(a, b) {
  const la = relLum(a), lb = relLum(b);
  return (Math.max(la, lb) + 0.05) / (Math.min(la, lb) + 0.05);
}
function extractTokens(block) {
  const out = {};
  for (const m of block.matchAll(/--(attn|attn-soft|attn-solid|on-attn|on-attn-solid):\s*(#[0-9a-fA-F]{6})\s*;/g)) {
    out[m[1]] = m[2];
  }
  return out;
}
function checkPaletteContrast(label, block, bg, bg2) {
  const tok = extractTokens(block);
  for (const k of ["attn", "attn-soft", "attn-solid", "on-attn", "on-attn-solid"]) {
    assert.ok(tok[k], label + " missing --" + k);
  }
  assert.ok(contrast(tok["attn"], bg) >= 4.5, label + " --attn vs bg < 4.5");
  assert.ok(contrast(tok["attn"], bg2) >= 4.5, label + " --attn vs bg2 < 4.5");
  assert.ok(contrast(tok["attn"], tok["attn-soft"]) >= 4.5, label + " --attn vs --attn-soft < 4.5");
  assert.ok(contrast(tok["on-attn"], tok["attn"]) >= 4.5, label + " --on-attn vs --attn < 4.5");
  assert.ok(contrast(tok["on-attn-solid"], tok["attn-solid"]) >= 4.5, label + " --on-attn-solid vs --attn-solid < 4.5");
}
checkPaletteContrast("amber light", cssBlock(":root"), "#ffffff", "#f7f7f6");
checkPaletteContrast("amber dark", cssBlock(':root[data-theme="dark"]'), "#0b0b0c", "#131315");
for (const preset of ["cobalt", "pine", "ink"]) {
  checkPaletteContrast(preset + " light", cssBlock(':root[data-accent="' + preset + '"]'), "#ffffff", "#f7f7f6");
  checkPaletteContrast(preset + " dark", cssBlock(':root[data-theme="dark"][data-accent="' + preset + '"]'), "#0b0b0c", "#131315");
}

// ─── 2. Live DOM / JS behavior tests ───
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
    title: "",
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
    focus() { el._focused = true; },
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
const metaColorScheme = makeEl('meta[name="color-scheme"]');
metaColorScheme.content = "light dark";
bySel.set('meta[name="color-scheme"]', metaColorScheme);
const metaThemeColor = makeEl('meta[name="theme-color"]');
metaThemeColor.content = "#ffffff";

const storage = new Map();
const winListeners = new Map();
let darkMatches = false;
const doc = {
  documentElement: makeEl("html"),
  body: makeEl("body"),
  hidden: false,
  hasFocus: () => true,
  createElement: (tag) => makeEl(tag),
  getElementById: (id) => getEl("#" + id),
  querySelector: (sel) => (sel === "dialog[open]" ? null : getEl(sel)),
  querySelectorAll: (sel) => (sel === 'meta[name="theme-color"]' ? [metaThemeColor] : []),
  addEventListener() {},
  removeEventListener() {},
};

const ctx = {
  console, Math, Date, JSON, Set, Map, RegExp, Error, Promise, URL, URLSearchParams, Object, Array, Number, String, Boolean,
  setTimeout: () => 0, clearTimeout() {}, setInterval: () => 0, clearInterval() {}, queueMicrotask,
  requestAnimationFrame: (fn) => { fn(Date.now()); return 1; },
  cancelAnimationFrame() {},
  document: doc,
  location: { hash: "#/collie", origin: "http://127.0.0.1:7788", href: "http://127.0.0.1:7788/#/collie", pathname: "/", search: "" },
  history: { state: null, pushState() {}, replaceState() {}, back() {} },
  navigator: { platform: "MacIntel", clipboard: { writeText: async () => {} } },
  isSecureContext: true,
  innerWidth: 1280,
  innerHeight: 800,
  visualViewport: { height: 800, offsetTop: 0, addEventListener() {} },
  CSS: { escape: (s) => String(s), supports: () => true },
  HTMLDialogElement: { prototype: {} },
  Blob, Uint8Array, TextEncoder, atob, AbortController,
  crypto: { randomUUID: () => "accent-uuid" },
  performance: { now: () => 0 },
  Event: class Event { constructor(t) { this.type = t; } },
  CustomEvent: class CustomEvent { constructor(t, d) { this.type = t; this.detail = d?.detail; } },
  ResizeObserver: class { observe() {} disconnect() {} },
  MutationObserver: class { observe() {} disconnect() {} },
  IntersectionObserver: class { observe() {} disconnect() {} },
  EventSource: class { addEventListener() {} close() {} },
  Notification: class { static permission = "default"; static async requestPermission() { return "default"; } },
  localStorage: {
    getItem: (k) => (storage.has(k) ? storage.get(k) : null),
    setItem: (k, v) => storage.set(k, String(v)),
    removeItem: (k) => storage.delete(k),
  },
  matchMedia: (q) => ({ matches: q.includes("dark") ? darkMatches : false, addEventListener() {} }),
  getComputedStyle: () => ({ getPropertyValue: () => "" }),
  addEventListener(type, fn) {
    if (!winListeners.has(type)) winListeners.set(type, []);
    winListeners.get(type).push(fn);
  },
  removeEventListener() {},
  dispatchEvent(ev) {
    for (const fn of (winListeners.get(ev.type) || [])) fn(ev);
  },
  fetch: async () => ({ ok: true, status: 200, json: async () => ({ ok: true }), text: async () => "{}" }),
};
ctx.window = ctx;
ctx.globalThis = ctx;

const marker = "  boot();\n})();";
assert.ok(appSrc.includes(marker), "expected boot() tail in web/app.js");
const instrumented = appSrc.replace(
  marker,
  "  globalThis.__test = { setSup: (s) => { sup = normSup(s); }, setState: (st) => { localState = st; state = mergedState(); }, setView: (v) => { view = v; }, renderSuper, renderPendingBar, renderPrefs, applyLook, prefs };\n})();"
);
vm.runInNewContext(instrumented, ctx, { filename: "web/app.js" });

const T = ctx.__test;
T.setView("collie");

// 2a) Context gauge threshold: < 75% stays calm (no .warn), >= 75% or handover triggers .warn
const cx = getEl("#router-ctx");
function checkCtx(router, wantWarn, wantText) {
  T.setSup({ router, entries: [], pending: [], pinned: [], workers: [] });
  T.renderSuper();
  assert.equal(cx.textContent, wantText, "context label mismatch for " + JSON.stringify(router));
  assert.equal(cx.classList.contains("warn"), wantWarn, "context warn class mismatch for " + JSON.stringify(router));
}
checkCtx({ alive: true, status: "idle", context: 55, rotate_at: 60 }, false, "上下文 55%");
checkCtx({ alive: true, status: "idle", context: 74, rotate_at: 70 }, false, "上下文 74%");
checkCtx({ alive: true, status: "idle", context: 75, rotate_at: 80 }, true, "上下文 75%");
checkCtx({ alive: true, status: "idle", context: 88, rotate_at: 0 }, true, "上下文 88%");
checkCtx({ alive: true, status: "working", context: 40, handover: true, rotate_at: 70 }, true, "正在交接记忆…");

// 2b) Pending tray leading dot: lit --attn dot when pending > 0; calm neutral dot when pin-only
const nowIso = new Date().toISOString();
const pendingWrap = getEl("#pending-wrap");
const pendingBar = getEl("#pending-bar");
T.setSup({
  router: { alive: true, status: "idle", context: 42 },
  entries: [{ id: "e1", role: "collie", text: "要立刻刷入吗？", at: nowIso, options: ["刷入", "稍后"] }],
  pending: [{ id: "e1", at: nowIso, summary: "要立刻刷入吗？", options: ["刷入", "稍后"] }],
  pinned: [{ id: "e2", at: nowIso, role: "collie", summary: "已钉住的长期约定" }],
  workers: [],
});
T.renderPendingBar();
assert.equal(pendingWrap.hidden, false);
assert.equal(pendingWrap.classList.contains("pin-only"), false);
assert.ok(pendingBar.innerHTML.includes('class="pending-dot"'), "pending tray must show active .pending-dot");
assert.ok(!pendingBar.innerHTML.includes("pending-dot neutral"), "pending tray with pending items must not use neutral dot");

T.setSup({
  router: { alive: true, status: "idle", context: 42 },
  entries: [{ id: "e2", role: "collie", text: "已钉住的长期约定", at: nowIso }],
  pending: [],
  pinned: [{ id: "e2", at: nowIso, role: "collie", summary: "已钉住的长期约定" }],
  workers: [],
});
T.renderPendingBar();
assert.equal(pendingWrap.hidden, false);
assert.equal(pendingWrap.classList.contains("pin-only"), true);
assert.ok(pendingBar.innerHTML.includes('class="pending-dot neutral"'), "pin-only tray must use neutral dot");

// 2c) 4-accent preferences picker: default amber, click switching, persistence, invalid fallback, keyboard arrows, cross-tab sync
const htmlEl = doc.documentElement;
assert.equal(T.prefs.accent(), "amber");
assert.equal(htmlEl.dataset.accent, undefined, "default amber must omit data-accent");

T.setView("prefs");
T.renderPrefs();
const prefsBody = getEl("#prefs-body");
const initialHtml = prefsBody.innerHTML;
assert.ok(initialHtml.includes('role="radiogroup" aria-label="点睛色" data-pref="accent"'), "accent radiogroup missing");
for (const [val, lbl] of [["amber", "暖琥珀"], ["cobalt", "钴蓝"], ["pine", "松绿"], ["ink", "纯黑白"]]) {
  assert.ok(initialHtml.includes('data-v="' + val + '"') && initialHtml.includes(lbl), "missing accent option " + val);
}
assert.ok(
  initialHtml.indexOf('data-pref="theme"') < initialHtml.indexOf('data-pref="accent"') &&
  initialHtml.indexOf('data-pref="accent"') < initialHtml.indexOf('data-pref="text"'),
  "accent picker must sit directly beneath light/dark theme toggle in 外观"
);

// Click each accent option and verify real-time dataset + localStorage persistence
function clickAccent(v) {
  const group = makeEl("accent-group");
  group.dataset.pref = "accent";
  const btn = makeEl("accent-" + v);
  btn.dataset.v = v;
  btn.closest = (sel) => {
    if (sel.includes(".accent-pick")) return btn;
    if (sel === "[data-pref]") return group;
    return null;
  };
  prefsBody.dispatch("click", { target: btn });
}

clickAccent("cobalt");
assert.equal(storage.get("collie.accent"), JSON.stringify("cobalt"));
assert.equal(htmlEl.dataset.accent, "cobalt");
assert.ok(prefsBody.innerHTML.includes('class="accent-opt on" role="radio" aria-checked="true" tabindex="0" data-v="cobalt"'));

clickAccent("pine");
assert.equal(storage.get("collie.accent"), JSON.stringify("pine"));
assert.equal(htmlEl.dataset.accent, "pine");

clickAccent("ink");
assert.equal(storage.get("collie.accent"), JSON.stringify("ink"));
assert.equal(htmlEl.dataset.accent, "ink");

clickAccent("amber");
assert.equal(storage.get("collie.accent"), JSON.stringify("amber"));
assert.equal(htmlEl.dataset.accent, undefined, "switching back to amber clears data-accent");

// Keyboard ArrowRight navigation across radiogroup
const fakeGroup = makeEl("rg");
const radioBtns = ["amber", "cobalt", "pine", "ink"].map((v) => {
  const b = makeEl("r-" + v);
  b.dataset.v = v;
  b.closest = (sel) => (sel.includes("radiogroup") ? fakeGroup : sel.includes('role="radio"') ? b : null);
  return b;
});
fakeGroup._qsa['button[role="radio"]'] = radioBtns;
prefsBody.dispatch("keydown", { key: "ArrowRight", target: radioBtns[0] });
assert.equal(htmlEl.dataset.accent, "cobalt", "ArrowRight from amber must select cobalt");
prefsBody.dispatch("keydown", { key: "ArrowLeft", target: radioBtns[0] });
assert.equal(htmlEl.dataset.accent, "ink", "ArrowLeft from amber must wrap to ink");

// Invalid stored accent falls back cleanly to amber
storage.set("collie.accent", JSON.stringify("neon-pink"));
T.applyLook();
assert.equal(T.prefs.accent(), "amber");
assert.equal(htmlEl.dataset.accent, undefined);

// Cross-tab storage event updates accent and re-renders prefs
storage.set("collie.accent", JSON.stringify("pine"));
ctx.dispatchEvent({ type: "storage", key: "collie.accent" });
assert.equal(htmlEl.dataset.accent, "pine");
assert.ok(prefsBody.innerHTML.includes('aria-checked="true" tabindex="0" data-v="pine"'));

// Theme switching keeps <meta name="color-scheme"> synchronized
storage.set("collie.theme", JSON.stringify("dark"));
T.applyLook();
assert.equal(metaColorScheme.content, "dark");
storage.set("collie.theme", JSON.stringify("system"));
T.applyLook();
assert.equal(metaColorScheme.content, "light dark");

console.log("ALL ACCENT POLISH TESTS PASSED");
`
	cmd := exec.Command("node", "-e", script)
	cmd.Dir = filepath.Join("..", "..")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("node accent polish test failed: %v\n%s", err, out)
	}
}
