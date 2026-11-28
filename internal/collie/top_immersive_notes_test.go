package collie

import (
	"os/exec"
	"path/filepath"
	"testing"
)

func TestWebTopImmersiveAndNotesLabelContracts(t *testing.T) {
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

// ─── 1. Static HTML / CSS Contracts for Seamless Top Immersion ───
assert.ok(
  htmlSrc.includes('<meta name="theme-color" content="#ffffff" media="(prefers-color-scheme: light)" data-theme-color="light">')
    && htmlSrc.includes('<meta name="theme-color" content="#111113" media="(prefers-color-scheme: dark)" data-theme-color="dark">'),
  "index.html must declare light and dark theme-color metas matching --bg tokens",
);
assert.ok(
  cssSrc.includes("html { background: var(--bg); }")
    && /\.app\s*\{[^}]*background:\s*var\(--bg\);/.test(cssSrc)
    && /\.main\s*\{[^}]*background:\s*var\(--bg\);/.test(cssSrc)
    && /\.d-head\s*\{[^}]*background:\s*var\(--bg\);/.test(cssSrc),
  "html, .app, .main, and .d-head must share var(--bg) so no transparent or mismatched layer bleeds into the top safe area",
);
assert.match(
  cssSrc,
  /#v-collie\s*\{[\s\S]*?padding-top:\s*0;[\s\S]*?contain:\s*strict;/,
  "#v-collie delegates safe-area-inset-top padding directly to .collie-top",
);
assert.match(
  cssSrc,
  /\.collie-top\s*\{[\s\S]*?background:\s*var\(--bg\);[\s\S]*?padding-top:\s*env\(safe-area-inset-top\);/,
  ".collie-top must own safe-area-inset-top with solid var(--bg) so status bar and header form one unbroken plane",
);
const mobileRespBlock = cssSrc.match(/\/\* ─── responsive ─── \*\/\n@media \(max-width: 760px\)\s*\{([\s\S]*?)\n\}/)?.[1] || "";
assert.ok(mobileRespBlock, "expected responsive <=760px block in app.css");
assert.ok(
  mobileRespBlock.includes(".login { background: var(--bg); }")
    && mobileRespBlock.includes(".side { border-right: 0; background: var(--bg); z-index: 2; content-visibility: visible; }")
    && mobileRespBlock.includes("body.show-main .side { z-index: 0; content-visibility: hidden; pointer-events: none; }")
    && mobileRespBlock.includes(".rv-head { background: var(--bg); }"),
  "mobile <=760px eliminates --bg-2 top-edge pollution from .side/.login and locks all top surfaces to var(--bg)",
);

// ─── 2. Static Contracts for Desktop & Mobile 「小本本」 Button ───
const notesBtnHtml = htmlSrc.match(/<button[^>]*id="open-notes-btn"[\s\S]*?<\/button>/)?.[0] || "";
assert.ok(
  notesBtnHtml.includes('id="open-notes-btn"')
    && notesBtnHtml.includes('aria-label="小本本"')
    && notesBtnHtml.includes("<span>小本本</span>")
    && notesBtnHtml.includes('id="open-notes-dot"')
    && !notesBtnHtml.includes("<svg"),
  "#open-notes-btn keeps clean translatable <span>小本本</span> markup without inline SVG",
);
assert.match(
  cssSrc,
  /\.head-text-btn\.notes-head-btn\s*\{[\s\S]*?min-block-size:\s*32px;[\s\S]*?color:\s*var\(--fg-2\);/,
  "desktop and mobile #open-notes-btn align 32px height with sibling header icon buttons",
);
assert.ok(
  !cssSrc.includes(".head-text-btn.notes-head-btn::before")
    && !cssSrc.includes(".notes-head-btn::before"),
  "#open-notes-btn must omit any CSS mask icon and stay pure minimalist text on both desktop and mobile",
);
assert.ok(
  !/\.head-text-btn\.notes-head-btn\s*>\s*span:first-child\s*\{[^}]*clip-path:\s*inset\(50%\)/.test(cssSrc)
    && !/\.head-text-btn\.notes-head-btn\s+\.th-head-dot\s*\{[^}]*position:\s*absolute;/.test(cssSrc),
  "mobile <=760px must keep <span>小本本</span> visible and keep unread dot inline without overlapping text",
);
assert.ok(
  cssSrc.includes("@container collie-top (inline-size < 340px)"),
  "extreme-narrow <340px container fallback tightens padding so 小本本 never clips",
);

// Demo bundle parity
assert.ok(
  demoHtmlSrc.includes('data-theme-color="light"')
    && demoCssSrc.includes("html { background: var(--bg); }")
    && demoCssSrc.includes(".side { border-right: 0; background: var(--bg);")
    && demoAppSrc.includes("function syncThemeColor("),
  "docs/demo must be synced with web/ changes",
);

// ─── 3. Pre-Paint Inline Script Contract (Zero First-Paint Status Bar Flash) ───
const headScript = htmlSrc.match(/<head>[\s\S]*?<script>([\s\S]*?)<\/script>[\s\S]*?<\/head>/)?.[1] || "";
assert.ok(headScript, "expected pre-paint inline script in <head>");
function makeMeta(name, content, media = "", themeColorKind = "") {
  const attrs = new Map();
  if (media) attrs.set("media", media);
  const el = {
    name,
    content,
    dataset: themeColorKind ? { themeColor: themeColorKind } : {},
    setAttribute(k, v) { attrs.set(k, String(v)); },
    getAttribute(k) { return attrs.get(k) ?? null; },
    removeAttribute(k) { attrs.delete(k); },
    hasAttribute(k) { return attrs.has(k); },
  };
  return el;
}
function runPrePaint(storedTheme) {
  const cs = makeMeta("color-scheme", "light dark");
  const lightM = makeMeta("theme-color", "#ffffff", "(prefers-color-scheme: light)", "light");
  const darkM = makeMeta("theme-color", "#111113", "(prefers-color-scheme: dark)", "dark");
  const root = { dataset: {} };
  const s = new Map();
  if (storedTheme !== null) s.set("collie.theme", JSON.stringify(storedTheme));
  vm.runInNewContext(headScript, {
    JSON,
    localStorage: { getItem: (k) => (s.has(k) ? s.get(k) : null) },
    document: {
      documentElement: root,
      querySelector: (sel) => (sel === 'meta[name="color-scheme"]' ? cs : null),
      querySelectorAll: (sel) => (sel === 'meta[name="theme-color"]' ? [lightM, darkM] : []),
    },
  });
  return { root, cs, lightM, darkM };
}
{
  const r = runPrePaint("dark");
  assert.equal(r.root.dataset.theme, "dark");
  assert.equal(r.cs.content, "dark");
  assert.equal(r.lightM.hasAttribute("media"), false);
  assert.equal(r.lightM.content, "#111113");
  assert.equal(r.darkM.getAttribute("media"), "not all");
}
{
  const r = runPrePaint("light");
  assert.equal(r.root.dataset.theme, "light");
  assert.equal(r.cs.content, "light");
  assert.equal(r.lightM.hasAttribute("media"), false);
  assert.equal(r.lightM.content, "#ffffff");
  assert.equal(r.darkM.getAttribute("media"), "not all");
}
{
  const r = runPrePaint(null);
  assert.equal(r.root.dataset.theme, undefined);
  assert.equal(r.cs.content, "light dark");
  assert.equal(r.lightM.getAttribute("media"), "(prefers-color-scheme: light)");
  assert.equal(r.darkM.getAttribute("media"), "(prefers-color-scheme: dark)");
}

// ─── 4. Runtime VM Tests for Dynamic Theme-Color Sync ───
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
  const el = {
    id, tagName, value: "", textContent: "", innerHTML: "",
    hidden: false, disabled: false, checked: false, open: false, inert: false, tabIndex: 0,
    className: "", title: "", href: "",
    style: {
      setProperty(k, v) { styles.set(k, String(v)); },
      getPropertyValue(k) { return styles.get(k) || ""; },
      removeProperty(k) { styles.delete(k); },
    },
    dataset: {}, classList: makeClassList(), children: [], selectedOptions: [], options: [],
    scrollTop: 0, scrollHeight: 400, clientHeight: 400, scrollWidth: 390, clientWidth: 390,
    offsetWidth: 390, offsetHeight: 40, offsetLeft: 0, offsetParent: {}, isConnected: true,
    _qs: {}, _qsa: {},
    addEventListener(type, fn) { if (!listeners.has(type)) listeners.set(type, []); listeners.get(type).push(fn); },
    removeEventListener(type, fn) { const l = listeners.get(type); if (!l) return; const i = l.indexOf(fn); if (i >= 0) l.splice(i, 1); },
    querySelector(sel) { if (el._qs[sel]) return el._qs[sel]; const c = makeEl(id + ":" + sel); el._qs[sel] = c; return c; },
    querySelectorAll(sel) { return el._qsa[sel] || []; },
    setAttribute(k, v) { attrs.set(k, String(v)); },
    getAttribute(k) { return attrs.get(k) ?? ""; },
    removeAttribute(k) { attrs.delete(k); },
    hasAttribute(k) { return attrs.has(k); },
    closest() { return null; }, contains(n) { return n === el; },
    append() {}, appendChild(c) { el.children.push(c); return c; }, remove() {}, after() {},
    focus() {}, blur() {}, click() {}, scrollIntoView() {}, showModal() {}, close() {},
    getBoundingClientRect() { return { top: 0, bottom: 40, left: 0, right: 390, width: 390, height: 40 }; },
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

const metaCS = makeMeta("color-scheme", "light dark");
const metaLight = makeMeta("theme-color", "#ffffff", "(prefers-color-scheme: light)", "light");
const metaDark = makeMeta("theme-color", "#111113", "(prefers-color-scheme: dark)", "dark");
let osDark = false;
const mqListeners = [];
const winListeners = new Map();
const storage = new Map();

const doc = {
  documentElement: makeEl("html"),
  body: makeEl("body"),
  head: makeEl("head"),
  activeElement: null,
  hidden: false,
  hasFocus: () => true,
  createElement: (tag) => makeEl(tag, tag.toUpperCase()),
  createDocumentFragment: () => makeEl("frag", "FRAGMENT"),
  getElementById: (id) => getEl("#" + id),
  querySelector: (sel) => (sel === 'meta[name="color-scheme"]' ? metaCS : sel === "dialog[open]" ? null : getEl(sel)),
  querySelectorAll: (sel) => (sel === 'meta[name="theme-color"]' ? [metaLight, metaDark] : []),
  addEventListener() {}, removeEventListener() {},
};
doc.activeElement = doc.body;

const ctx = {
  console, Math, Date, JSON, Set, Map, RegExp, Error, Promise, URL, URLSearchParams, Object, Array, Number, String, Boolean,
  setTimeout: () => 1, clearTimeout() {}, setInterval: () => 1, clearInterval() {}, queueMicrotask,
  requestAnimationFrame: (fn) => { fn(Date.now()); return 1; }, cancelAnimationFrame() {},
  document: doc,
  location: { hash: "#/collie", origin: "http://127.0.0.1:7788", href: "http://127.0.0.1:7788/#/collie", pathname: "/", search: "" },
  history: { state: null, pushState() {}, replaceState() {}, back() {} },
  navigator: { platform: "iPhone", clipboard: { writeText: async () => {} } },
  isSecureContext: true, innerWidth: 390, innerHeight: 844,
  CSS: { escape: (s) => String(s), supports: () => true },
  HTMLDialogElement: { prototype: {} },
  Blob, Uint8Array, TextEncoder, TextDecoder, atob, AbortController,
  crypto: { randomUUID: () => "ti-uuid" },
  performance: { now: () => 1000 },
  Event: class Event { constructor(t) { this.type = t; } },
  CustomEvent: class CustomEvent { constructor(t, d) { this.type = t; this.detail = d?.detail; } },
  ResizeObserver: class { observe() {} disconnect() {} },
  MutationObserver: class { observe() {} disconnect() {} },
  IntersectionObserver: class { observe() {} disconnect() {} },
  EventSource: class { addEventListener() {} close() {} },
  Notification: class { static permission = "default"; static async requestPermission() { return "default"; } },
  localStorage: { getItem: (k) => (storage.has(k) ? storage.get(k) : null), setItem: (k, v) => storage.set(k, String(v)), removeItem: (k) => storage.delete(k) },
  matchMedia: (q) => ({
    get matches() { return q.includes("prefers-color-scheme: dark") ? osDark : q.includes("max-width: 760px"); },
    addEventListener(type, fn) { if (q.includes("prefers-color-scheme: dark") && type === "change") mqListeners.push(fn); },
  }),
  getComputedStyle: () => ({ getPropertyValue: () => "" }),
  getSelection: () => ({ isCollapsed: true, toString: () => "" }),
  addEventListener(type, fn) { if (!winListeners.has(type)) winListeners.set(type, []); winListeners.get(type).push(fn); },
  removeEventListener() {},
  dispatchEvent(ev) { for (const fn of (winListeners.get(ev.type) || [])) fn(ev); },
  fetch: async () => ({ ok: true, status: 200, json: async () => ({ ok: true }) }),
};
ctx.window = ctx;
ctx.globalThis = ctx;

const marker = "  boot();\n})();";
const instrumented = appSrc.replace(
  marker,
  "  globalThis.__test = { applyLook, syncThemeColor };\n})();",
);
vm.runInNewContext(instrumented, ctx, { filename: "web/app.js" });
const T = ctx.__test;

// 4a) System mode restores clean media-scoped pair with zero duplicate un-media'd tags
assert.equal(metaCS.content, "light dark");
assert.equal(metaLight.getAttribute("media"), "(prefers-color-scheme: light)");
assert.equal(metaLight.content, "#ffffff");
assert.equal(metaDark.getAttribute("media"), "(prefers-color-scheme: dark)");
assert.equal(metaDark.content, "#111113");

// 4b) Manual dark pins exactly one active #111113 theme-color and silences the duplicate
storage.set("collie.theme", JSON.stringify("dark"));
T.applyLook();
assert.equal(doc.documentElement.dataset.theme, "dark");
assert.equal(metaCS.content, "dark");
assert.equal(metaLight.hasAttribute("media"), false);
assert.equal(metaLight.content, "#111113");
assert.equal(metaDark.getAttribute("media"), "not all");

// 4c) Manual light pins exactly one active #ffffff theme-color even when OS is dark
osDark = true;
storage.set("collie.theme", JSON.stringify("light"));
T.applyLook();
assert.equal(doc.documentElement.dataset.theme, "light");
assert.equal(metaCS.content, "light");
assert.equal(metaLight.hasAttribute("media"), false);
assert.equal(metaLight.content, "#ffffff");
assert.equal(metaDark.getAttribute("media"), "not all");

// 4d) Returning to system restores both media-scoped tags and follows OS changes
storage.set("collie.theme", JSON.stringify("system"));
T.applyLook();
assert.equal(doc.documentElement.dataset.theme, undefined);
assert.equal(metaCS.content, "light dark");
assert.equal(metaLight.getAttribute("media"), "(prefers-color-scheme: light)");
assert.equal(metaLight.content, "#ffffff");
assert.equal(metaDark.getAttribute("media"), "(prefers-color-scheme: dark)");
assert.equal(metaDark.content, "#111113");

// 4e) Cross-tab storage event updates theme-color dynamically
storage.set("collie.theme", JSON.stringify("dark"));
ctx.dispatchEvent({ type: "storage", key: "collie.theme" });
assert.equal(metaLight.hasAttribute("media"), false);
assert.equal(metaLight.content, "#111113");

console.log("top immersive & notes label contracts ok");
`
	cmd := exec.Command("node", "-e", script)
	cmd.Dir = filepath.Join("..", "..")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("top immersive & notes label test failed: %v\n%s", err, string(out))
	}
}
