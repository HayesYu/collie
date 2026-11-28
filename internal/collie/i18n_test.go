package collie

import (
	"bytes"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/lihuanyu0w0/collie/web"
)

func TestWebI18nEmbeddedAndDemoSynced(t *testing.T) {
	t.Parallel()
	embedded, err := fs.ReadFile(web.FS, "i18n.js")
	if err != nil {
		t.Fatalf("web/i18n.js must be embedded in web.FS: %v", err)
	}
	if len(embedded) < 1024 {
		t.Fatalf("embedded web/i18n.js unexpectedly small (%d bytes)", len(embedded))
	}
	demoCopy, err := os.ReadFile(filepath.Join("..", "..", "docs", "demo", "i18n.js"))
	if err != nil {
		t.Fatalf("docs/demo/i18n.js must exist (run scripts/demo.sh): %v", err)
	}
	if !bytes.Equal(embedded, demoCopy) {
		t.Fatalf("docs/demo/i18n.js is out of sync with web/i18n.js; run `sh scripts/demo.sh`")
	}
}

func TestWebBilingualI18nAndNamingContracts(t *testing.T) {
	t.Parallel()
	if _, err := exec.LookPath("node"); err != nil {
		t.Skip("node not installed")
	}
	script := `
const fs = require("fs");
const vm = require("vm");
const assert = require("assert");

const htmlSrc = fs.readFileSync("web/index.html", "utf8");
const i18nSrc = fs.readFileSync("web/i18n.js", "utf8");
const quotaSrc = fs.readFileSync("web/quota.js", "utf8");
const appSrc = fs.readFileSync("web/app.js", "utf8");
const joinSrc = fs.readFileSync("docs/join/index.html", "utf8");
const demoJsSrc = fs.readFileSync("scripts/demo/demo.js", "utf8");
const demoHtmlSrc = fs.readFileSync("docs/demo/index.html", "utf8");

// ─── 1. Pre-paint anti-flicker & script order contracts ───
assert.ok(htmlSrc.includes('localStorage.getItem("collie.lang")'), "web/index.html <head> must initialize language before first paint");
assert.ok(htmlSrc.indexOf('<script src="/i18n.js"></script>') > 0 &&
  htmlSrc.indexOf('<script src="/i18n.js"></script>') < htmlSrc.indexOf('<script src="/quota.js"></script>') &&
  htmlSrc.indexOf('<script src="/quota.js"></script>') < htmlSrc.indexOf('<script src="/app.js"></script>'),
  "web/index.html must load i18n.js before quota.js and app.js");
assert.ok(demoHtmlSrc.includes('<script src="i18n.js"></script>') &&
  demoHtmlSrc.indexOf('<script src="i18n.js"></script>') < demoHtmlSrc.indexOf('<script src="demo.js"></script>'),
  "docs/demo/index.html must load i18n.js before demo.js and app.js");
assert.ok(joinSrc.includes('localStorage.getItem("collie.lang")'), "docs/join/index.html must honor collie.lang preference before paint");

// ─── 2. Load CollieI18n module & verify 100% dictionary completeness & parity ───
function loadI18n({ navLang = "zh-CN", savedPref = null } = {}) {
  const storage = new Map();
  if (savedPref !== null) storage.set("collie.lang", savedPref);
  const attrs = new Map();
  const docEl = {
    lang: "",
    dataset: {},
    setAttribute(k, v) { attrs.set(k, String(v)); },
    getAttribute(k) { return attrs.get(k) ?? null; },
  };
  const listeners = new Map();
  const nodes = {
    text: [],
    ph: [],
    title: [],
    aria: [],
    lead: [],
    notesBtn: null,
  };
  const doc = {
    documentElement: docEl,
    title: "Collie",
    querySelectorAll(sel) {
      if (sel === "[data-i18n]") return nodes.text;
      if (sel === "[data-i18n-placeholder]") return nodes.ph;
      if (sel === "[data-i18n-title]") return nodes.title;
      if (sel === "[data-i18n-aria]") return nodes.aria;
      if (sel === "[data-i18n-lead]") return nodes.lead;
      return [];
    },
    querySelector(sel) {
      if (sel === "#open-notes-btn > span:first-child") return nodes.notesBtn;
      return null;
    },
    getElementById() { return null; },
  };
  const win = {
    navigator: { language: navLang, languages: [navLang] },
    localStorage: {
      getItem: (k) => (storage.has(k) ? storage.get(k) : null),
      setItem: (k, v) => storage.set(k, String(v)),
      removeItem: (k) => storage.delete(k),
    },
    document: doc,
    addEventListener(type, fn) {
      if (!listeners.has(type)) listeners.set(type, []);
      listeners.get(type).push(fn);
    },
    dispatchEvent() {},
    console, Math, Date, JSON, Set, Map, RegExp, Object, Array, String, Number, Boolean, Error,
  };
  win.window = win;
  win.globalThis = win;
  vm.runInNewContext(i18nSrc, win, { filename: "web/i18n.js" });
  return { I: win.CollieI18n, docEl, storage, nodes, win, listeners };
}

const { I } = loadI18n({ navLang: "zh-CN" });
const zhKeys = Object.keys(I.D.zh).sort();
const enKeys = Object.keys(I.D.en).sort();
assert.deepEqual(zhKeys, enKeys, "D.zh and D.en must have identical key sets");
assert.ok(zhKeys.length >= 1000, "dictionary should cover the full UI surface (" + zhKeys.length + " keys)");

// Every key non-empty + 100% placeholder parity
for (const k of zhKeys) {
  assert.equal(typeof I.D.zh[k], "string", "zh key must be string: " + k);
  assert.equal(typeof I.D.en[k], "string", "en key must be string: " + k);
  assert.ok(I.D.zh[k].length > 0, "zh key must be non-empty: " + k);
  assert.ok(I.D.en[k].length > 0, "en key must be non-empty: " + k);
  const zp = [...I.D.zh[k].matchAll(/\{(\w+)\}/g)].map((m) => m[1]).sort();
  const ep = [...I.D.en[k].matchAll(/\{(\w+)\}/g)].map((m) => m[1]).sort();
  assert.deepEqual(zp, ep, "placeholder mismatch for key: " + k);
}

// Every referenced key in HTML / app.js / quota.js / demo.js exists in both dictionaries
const usedKeys = new Set();
for (const m of htmlSrc.matchAll(/data-i18n(?:-[a-z-]+)?="([^"]+)"/g)) usedKeys.add(m[1]);
for (const m of appSrc.matchAll(/\btx\(\s*["']([^"']+)["']/g)) {
  if (!m[1].endsWith(".")) usedKeys.add(m[1]);
}
for (const m of quotaSrc.matchAll(/\btr\(\s*["']([^"']+)["']/g)) usedKeys.add(m[1]);
for (const m of demoJsSrc.matchAll(/\bCollieI18n\.t\(\s*["']([^"']+)["']/g)) usedKeys.add(m[1]);
for (const k of usedKeys) {
  assert.ok(Object.prototype.hasOwnProperty.call(I.D.zh, k), "missing zh key: " + k);
  assert.ok(Object.prototype.hasOwnProperty.call(I.D.en, k), "missing en key: " + k);
}

// ─── 3. Auto system language detection & switching ───
for (const [nav, wantLang, wantHtmlLang] of [
  ["zh-CN", "zh", "zh-CN"],
  ["zh-TW", "zh", "zh-CN"],
  ["zh-HK", "zh", "zh-CN"],
  ["zh", "zh", "zh-CN"],
  ["en-US", "en", "en"],
  ["en-GB", "en", "en"],
  ["ja-JP", "en", "en"],
  ["fr-FR", "en", "en"],
  ["", "en", "en"],
]) {
  const r = loadI18n({ navLang: nav });
  assert.equal(r.I.getPref(), "auto");
  assert.equal(r.I.getLang(), wantLang, "auto detection failed for navigator.language=" + nav);
  assert.equal(r.docEl.lang, wantHtmlLang, "html lang failed for navigator.language=" + nav);
}

// Explicit preference overrides navigator.language and updates DOM without reload
{
  const r = loadI18n({ navLang: "zh-CN" });
  const mkNode = (attrs) => {
    const a = { ...attrs };
    return {
      textContent: "",
      placeholder: "",
      title: "",
      getAttribute(k) { return Object.prototype.hasOwnProperty.call(a, k) ? a[k] : null; },
      setAttribute(k, v) { a[k] = String(v); this[k] = String(v); },
    };
  };
  const textEl = mkNode({ "data-i18n": "peer.title" });
  const phEl = mkNode({ "data-i18n-placeholder": "collie.composerPh" });
  const titleEl = mkNode({ "data-i18n-title": "side.pals" });
  const ariaEl = mkNode({ "data-i18n-aria": "side.pals" });
  const notesSpan = { textContent: "小本本" };
  r.nodes.text.push(textEl);
  r.nodes.ph.push(phEl);
  r.nodes.title.push(titleEl);
  r.nodes.aria.push(ariaEl);
  r.nodes.notesBtn = notesSpan;

  let changes = 0;
  r.I.onChange(() => changes++);
  r.I.setLang("en");
  assert.equal(r.I.getPref(), "en");
  assert.equal(r.I.getLang(), "en");
  assert.equal(JSON.parse(r.storage.get("collie.lang")), "en");
  assert.equal(r.docEl.lang, "en");
  assert.equal(textEl.textContent, "Collie's Pals");
  assert.equal(titleEl.title, "Pals");
  assert.equal(ariaEl["aria-label"], "Pals");
  assert.equal(notesSpan.textContent, "Notes");
  assert.equal(changes, 1);

  r.I.setLang("zh");
  assert.equal(r.I.getLang(), "zh");
  assert.equal(r.docEl.lang, "zh-CN");
  assert.equal(textEl.textContent, "Collie 的好狗友");
  assert.equal(titleEl.title, "狗友");
  assert.equal(notesSpan.textContent, "小本本");
  assert.equal(changes, 2);

  r.I.setLang("auto");
  assert.equal(r.I.getPref(), "auto");
  assert.equal(r.I.getLang(), "zh");
}

// ─── 4. Naming convergence regression ('Collie 的好狗友' / '狗友' ↔ 'Collie's Pals' / 'Pals') ───
assert.equal(I.D.zh["peer.title"], "Collie 的好狗友");
assert.equal(I.D.en["peer.title"], "Collie's Pals");
assert.equal(I.D.zh["side.pals"], "狗友");
assert.equal(I.D.en["side.pals"], "Pals");
assert.equal(I.D.zh["peer.tabAdd"], "添加狗友");
assert.equal(I.D.en["peer.tabAdd"], "Add a pal");

for (const [name, text] of [
  ["web/index.html", htmlSrc],
  ["web/app.js", appSrc],
  ["web/quota.js", quotaSrc],
  ["docs/join/index.html", joinSrc],
  ["scripts/demo/demo.js", demoJsSrc],
  ["docs/demo/index.html", demoHtmlSrc],
]) {
  const nonComment = text.split("\n").filter((l) => !/^\s*\/\//.test(l)).join("\n");
  assert.ok(!nonComment.includes("好友边牧"), name + " must not contain legacy wording '好友边牧'");
}
for (const k of zhKeys) {
  assert.ok(!I.D.zh[k].includes("好友边牧"), "D.zh[" + k + "] must not contain '好友边牧'");
  assert.ok(!I.D.zh[k].includes("好友"), "D.zh[" + k + "] should converge '好友' to '狗友'");
  assert.ok(!/Peer Collie/i.test(I.D.en[k]), "D.en[" + k + "] must not contain 'Peer Collie'");
}
assert.ok(!/Peer Collie/i.test(joinSrc), "docs/join/index.html must not contain 'Peer Collie'");

// Historical backend event localization compatibility
assert.equal(I.localizeEventText("[好友边牧来信 · Alice] 帮忙查下构建", "zh"), "[狗友来信 · Alice] 帮忙查下构建");
assert.equal(I.localizeEventText("[好友边牧来信 · Alice] check build", "en"), "[Pal mail · Alice] check build");
assert.equal(I.localizeEventText("Alice（hub_1）发来好友边牧申请，点此审批", "zh"), "Alice（hub_1）发来狗友申请，点此审批");
assert.equal(I.localizeEventText("Alice（hub_1）发来好友边牧申请，点此审批", "en"), "Alice (hub_1) sent a pal request — click to review");

// ─── 5. Live zero-reload switching in web/app.js + web/quota.js together ───
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
  const listeners = new Map(), attrs = new Map(), styles = new Map();
  const el = {
    id, tagName: "DIV", value: "", textContent: "", innerHTML: "",
    hidden: false, disabled: false, checked: false, open: false, inert: false, tabIndex: 0,
    className: "", title: "", dataset: {}, classList: makeClassList(), children: [],
    selectedOptions: [], options: [], scrollTop: 0, scrollHeight: 600, clientHeight: 600,
    scrollWidth: 400, clientWidth: 400, offsetWidth: 160, offsetHeight: 32, offsetParent: {}, isConnected: true,
    _qs: {}, _qsa: {},
    style: {
      setProperty(k, v) { styles.set(k, String(v)); },
      getPropertyValue(k) { return styles.get(k) || ""; },
      removeProperty(k) { styles.delete(k); },
    },
    addEventListener(type, fn) { if (!listeners.has(type)) listeners.set(type, []); listeners.get(type).push(fn); },
    removeEventListener() {},
    dispatch(type, ev = {}) {
      const e = { type, target: el, currentTarget: el, defaultPrevented: false, preventDefault() { this.defaultPrevented = true; }, stopPropagation() {}, composedPath: () => [el], ...ev };
      for (const fn of [...(listeners.get(type) || [])]) fn(e);
      return e;
    },
    querySelector(sel) { if (el._qs[sel]) return el._qs[sel]; if (sel.startsWith("#")) return getEl(sel); const c = makeEl(id + ":" + sel); el._qs[sel] = c; return c; },
    querySelectorAll(sel) { return el._qsa[sel] || []; },
    setAttribute(k, v) { attrs.set(k, String(v)); if (k === "title") el.title = String(v); },
    getAttribute(k) { return attrs.get(k) ?? ""; },
    removeAttribute(k) { attrs.delete(k); },
    hasAttribute(k) { return attrs.has(k); },
    closest() { return null; }, contains() { return false; },
    append() {}, appendChild(c) { el.children.push(c); return c; }, remove() {}, after() {},
    focus() {}, click() {}, scrollIntoView() {}, showModal() { el.open = true; }, close() { el.open = false; },
    getBoundingClientRect() { return { top: 0, bottom: 32, left: 0, right: 160, width: 160, height: 32 }; },
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
  f._qs[".send"] = makeEl(formSel + " .send");
  f._qs[".files"] = makeEl(formSel + " .files");
  f._qs[".attach"] = makeEl(formSel + " .attach");
  f._qs['input[type="file"]'] = makeEl(formSel + " input[type=file]");
}
getEl("#re-draft")._qs[".re-t"] = makeEl("#re-draft .re-t");
getEl("#re-draft")._qs["button"] = makeEl("#re-draft button");

const storage = new Map();
const docEl = makeEl("html");
const doc = {
  documentElement: docEl,
  body: makeEl("body"),
  title: "Collie",
  activeElement: null,
  hidden: false,
  hasFocus: () => true,
  createElement: (tag) => makeEl(tag),
  getElementById: (id) => getEl("#" + id),
  querySelector: (sel) => (sel === "dialog[open]" ? null : getEl(sel)),
  querySelectorAll: () => [],
  addEventListener() {}, removeEventListener() {},
};
const ctx = {
  console, Math, Date, JSON, Set, Map, RegExp, Error, Promise, URL, URLSearchParams, Object, Array, Number, String, Boolean, Proxy,
  setTimeout: () => 1, clearTimeout() {}, setInterval: () => 1, clearInterval() {}, queueMicrotask,
  requestAnimationFrame: (fn) => { fn(Date.now()); return 1; }, cancelAnimationFrame() {},
  document: doc,
  location: { hash: "#/prefs", origin: "http://127.0.0.1:7788", href: "http://127.0.0.1:7788/#/prefs", pathname: "/", search: "" },
  history: { state: null, pushState() {}, replaceState() {}, back() {} },
  navigator: { language: "zh-CN", languages: ["zh-CN"], platform: "MacIntel", userAgent: "Mozilla/5.0 (Macintosh)", maxTouchPoints: 0, clipboard: { writeText: async () => {}, readText: async () => "" } },
  isSecureContext: true, innerWidth: 1440, innerHeight: 900,
  visualViewport: { height: 900, offsetTop: 0, addEventListener() {} },
  CSS: { escape: (s) => String(s), supports: () => true },
  HTMLDialogElement: { prototype: { closedBy: "any" } },
  HTMLElement: { prototype: {} },
  Blob, Uint8Array, TextEncoder, TextDecoder, atob, AbortController,
  crypto: { randomUUID: () => "i18n-test-uuid" },
  performance: { now: () => 1000 },
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
  fetch: async (url) => {
    const u = String(url);
    const okJson = (obj) => ({ ok: true, status: 200, text: async () => JSON.stringify(obj), json: async () => obj });
    if (u === "/api/super/peers") return okJson({ hub_id: "hub_1", hub_name: "Demo Collie", enabled: true, addrs: ["http://127.0.0.1:7788"], peers: [], blocklist: [] });
    if (u === "/api/super/peers/invite") return okJson({ invite_code: "collie-peer://v2/e30", join_url: "https://lihuanyu0w0.github.io/collie/join/#2.e30", hub_id: "hub_1", hub_name: "Demo Collie", permanent: true });
    return okJson({ ok: true });
  },
};
ctx.window = ctx;
ctx.globalThis = ctx;

vm.runInNewContext(i18nSrc, ctx, { filename: "web/i18n.js" });
vm.runInNewContext(quotaSrc, ctx, { filename: "web/quota.js" });
const marker = "  boot();\n})();";
const instrumented = appSrc.replace(
  marker,
  "  globalThis.__i18nTest = { renderPrefs, openPeerModal, renderRouterPendingBars, setSup: (s) => { sup = normSup(s); }, setView: (v) => { view = v; } };\n})();"
);
vm.runInNewContext(instrumented, ctx, { filename: "web/app.js" });
const T = ctx.__i18nTest;

(async () => {
  T.setView("prefs");
  T.renderPrefs();
  assert.ok(getEl("#prefs-body").innerHTML.includes("语言") && getEl("#prefs-body").innerHTML.includes("跟随系统"), "prefs renders Chinese language control");
  assert.ok(getEl("#prefs-body").innerHTML.includes('lang="zh-CN"') && getEl("#prefs-body").innerHTML.includes('lang="en"'), "WCAG 3.1.2 lang spans present on language options");

  T.setSup({ router: { alive: true, pending: false }, peers_summary: { total: 1, active: 0, pending_incoming: 2, enabled: true } });
  T.renderRouterPendingBars();
  assert.ok(getEl("#peer-req-bar").innerHTML.includes("收到 2 条狗友申请"), "zh peer request bar uses converged wording");

  await T.openPeerModal("auto", { refresh: true });
  await new Promise((r) => setTimeout(r, 10));
  assert.equal(getEl("#int-modal-title").textContent, "Collie 的好狗友");

  // Switch live to English — zero reload, all open views + modal repaint immediately
  ctx.CollieI18n.setLang("en");
  assert.equal(docEl.lang, "en");
  assert.equal(JSON.parse(storage.get("collie.lang")), "en");
  assert.ok(getEl("#prefs-body").innerHTML.includes("Language") && getEl("#prefs-body").innerHTML.includes("Auto"), "prefs repaints immediately in English");
  assert.ok(getEl("#peer-req-bar").innerHTML.includes("2 pal request(s) waiting"), "peer request bar repaints immediately in English");
  assert.equal(getEl("#int-modal-title").textContent, "Collie's Pals", "open peer modal repaints immediately in English");

  // Switch back to Chinese live
  ctx.CollieI18n.setLang("zh");
  assert.equal(docEl.lang, "zh-CN");
  assert.ok(getEl("#prefs-body").innerHTML.includes("语言"), "prefs repaints immediately back to Chinese");
  assert.equal(getEl("#int-modal-title").textContent, "Collie 的好狗友", "open peer modal repaints immediately back to Chinese");

  console.log("OK: bilingual i18n, completeness (" + zhKeys.length + " keys), and naming convergence contracts passed");
})().catch((err) => {
  console.error(err);
  process.exit(1);
});
`
	cmd := exec.Command("node", "-e", script)
	cmd.Dir = filepath.Join("..", "..")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("bilingual i18n contracts failed: %v\n%s", err, string(out))
	}
}
