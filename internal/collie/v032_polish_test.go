package collie

import (
	"os/exec"
	"path/filepath"
	"testing"
)

func TestPeerModeLabelHumanCopy(t *testing.T) {
	t.Parallel()
	cases := map[string]string{
		"advise":  "先帮我查清，草稿给我审",
		"notify":  "只留个便签，不自动回复",
		"propose": "查完给方案，批准再干活",
		"":        "先帮我查清，草稿给我审",
	}
	for mode, want := range cases {
		if got := peerModeLabel(mode); got != want {
			t.Fatalf("peerModeLabel(%q) = %q, want %q", mode, got, want)
		}
	}
}

func TestWebV032PolishRuntimeContracts(t *testing.T) {
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
  const styles = new Map();
  const el = {
    id, tagName: "DIV", value: "", textContent: "", innerHTML: "",
    hidden: false, disabled: false, checked: false, open: false, inert: false, tabIndex: 0,
    className: "", title: "",
    style: {
      setProperty(k, v) { styles.set(k, String(v)); },
      getPropertyValue(k) { return styles.get(k) || ""; },
      removeProperty(k) { styles.delete(k); },
    },
    dataset: {}, classList: makeClassList(), children: [], selectedOptions: [], options: [],
    scrollTop: 0, scrollHeight: 1200, clientHeight: 600, scrollWidth: 400, clientWidth: 400,
    offsetWidth: 400, offsetHeight: 98, offsetParent: {}, isConnected: true,
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
    setAttribute(k, v) { attrs.set(k, String(v)); },
    getAttribute(k) { return attrs.get(k) ?? ""; },
    removeAttribute(k) { attrs.delete(k); },
    hasAttribute(k) { return attrs.has(k); },
    toggleAttribute(k, force) { const on = force === undefined ? !attrs.has(k) : !!force; if (on) attrs.set(k, ""); else attrs.delete(k); return on; },
    closest() { return null; }, contains() { return false; },
    append() {}, remove() {}, after() {}, focus() {}, click() {}, scrollIntoView() {},
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
getEl("#super-search").hidden = true;
getEl("#collie-menu").hidden = true;
getEl("#file-view").hidden = true;

const storage = new Map();
const doc = {
  documentElement: makeEl("html"),
  body: makeEl("body"),
  hidden: false,
  hasFocus: () => true,
  createElement: (tag) => makeEl(tag),
  getElementById: (id) => getEl("#" + id),
  querySelector: (sel) => (sel === "dialog[open]" ? null : getEl(sel)),
  querySelectorAll: () => [],
  addEventListener() {},
  removeEventListener() {},
};
const ctx = {
  console, Math, Date, JSON, Set, Map, RegExp, Error, Promise, URL, URLSearchParams, Object, Array, Number, String, Boolean,
  setTimeout: () => 1, clearTimeout() {}, setInterval: () => 1, clearInterval() {}, queueMicrotask,
  requestAnimationFrame: (fn) => { fn(Date.now()); return 1; },
  cancelAnimationFrame() {},
  document: doc,
  location: { hash: "#/collie", origin: "http://127.0.0.1:7788", href: "http://127.0.0.1:7788/#/collie", pathname: "/", search: "" },
  history: { state: null, pushState() {}, replaceState() {}, back() {} },
  navigator: { platform: "MacIntel", clipboard: { writeText: async () => {} } },
  isSecureContext: true, innerWidth: 1440, innerHeight: 900,
  visualViewport: { height: 900, offsetTop: 0, addEventListener() {} },
  CSS: { escape: (s) => String(s), supports: () => true },
  HTMLDialogElement: { prototype: {} },
  Blob, Uint8Array, TextEncoder, atob, AbortController,
  crypto: { randomUUID: () => "v032-uuid" },
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
  fetch: async () => ({ ok: true, status: 200, json: async () => ({ ok: true }) }),
};
ctx.window = ctx;
ctx.globalThis = ctx;

const marker = "  boot();\n})();";
const instrumented = appSrc.replace(
  marker,
  "  globalThis.__test = { setView: (v) => { view = v; }, setSup: (s) => { sup = normSup(s); }, renderSuper, openThreadDrawer, closeThreadDrawer, parseUserParts, renderUserMedia, syncNotebookVisibility };\n})();"
);
vm.runInNewContext(instrumented, ctx, { filename: "web/app.js" });
const T = ctx.__test;

// 1) Detached user image cards: single + multi-image + pure image-only (no dark bubble)
const pSingle = T.parseUserParts("看这张图\n\n附件：\n/tmp/a1b2c3-shot.png");
assert.equal(pSingle.imgs.length, 1);
assert.equal(pSingle.hasBubbleContent, true);
const pOnly = T.parseUserParts("附件：\n/tmp/a1b2c3-shot.png");
assert.equal(pOnly.imgs.length, 1);
assert.equal(pOnly.hasBubbleContent, false);
const pGrid2 = T.renderUserMedia(["/tmp/1.png", "/tmp/2.png"]);
assert.ok(pGrid2.includes('class="mine-media is-grid-2" data-count="2"'));
const pGrid3 = T.renderUserMedia(["/tmp/1.png", "/tmp/2.png", "/tmp/3.png"]);
assert.ok(pGrid3.includes('class="mine-media is-grid-3" data-count="3"'));

const nowIso = new Date().toISOString();
T.setView("collie");
T.setSup({
  router: { status: "idle", alive: true },
  entries: [
    { id: "e1", role: "user", text: "截图给你看\n\n附件：\n/tmp/a1b2c3-shot.png", at: nowIso },
    { id: "e2", role: "user", text: "附件：\n/tmp/d4e5f6-only.png", at: nowIso },
    { id: "e3", role: "collie", text: "收到，我来改。", at: nowIso, re: { entry: "e1", who: "你", text: "截图给你看" } },
  ],
  workers: [],
});
T.renderSuper({ force: true });
const threadHtml = getEl("#super-thread").innerHTML;
assert.ok(threadHtml.includes('class="mine has-media"') && threadHtml.includes('class="mine-media is-single"'), "text+image user row separates .mine-media above .msg.user");
const textBubbleMatch = threadHtml.match(/<div class="msg user" data-id="e1"[^>]*>([\s\S]*?)<\/div>/);
assert.ok(textBubbleMatch && textBubbleMatch[1].includes("截图给你看") && !textBubbleMatch[1].includes("mine-media") && !textBubbleMatch[1].includes("thumb"), "text bubble must NOT wrap image thumbnails");
assert.ok(threadHtml.includes('class="mine is-media-only"') && threadHtml.includes('<div class="msg user is-media-only" data-id="e2"'), "pure image-only message renders without dark text bubble");

// 2) Desktop side-by-side thread drawer freezes left main chat scroll & suppresses #super-working
const sc = getEl("#super-thread").parentElement;
sc.scrollHeight = 1400;
sc.clientHeight = 600;
sc.scrollTop = 800; // sitting at bottom before opening drawer
T.openThreadDrawer("e1", "e3", "e3");
assert.equal(getEl("#app").classList.contains("has-thread"), true);
// Simulate a new drawer reply + Collie thinking arriving while desktop drawer is open
sc.scrollHeight = 1680;
T.setSup({
  router: { status: "working", alive: true, turn_kind: "chat" },
  entries: [
    { id: "e1", role: "user", text: "截图给你看\n\n附件：\n/tmp/a1b2c3-shot.png", at: nowIso },
    { id: "e2", role: "user", text: "附件：\n/tmp/d4e5f6-only.png", at: nowIso },
    { id: "e3", role: "collie", text: "收到，我来改。", at: nowIso, re: { entry: "e1", who: "你", text: "截图给你看" } },
    { id: "e4", role: "user", text: "在抽屉里追加一句", at: nowIso, re: { entry: "e3", who: "Collie", text: "收到，我来改。" } },
  ],
  workers: [],
});
T.renderSuper({ immediate: true });
assert.equal(sc.scrollTop, 800, "left main chat scrollTop must stay completely frozen while desktop thread drawer is open");
assert.equal(getEl("#super-working").hidden, true, "left main chat must suppress #super-working thinking row while desktop thread drawer is open");

// Clicking main chat restores focus highlight; clicking drawer clears it
getEl("#v-collie").dispatch("pointerdown");
assert.equal(getEl("#v-collie").classList.contains("is-main-focused"), true);
getEl("#thread-drawer").dispatch("pointerdown");
assert.equal(getEl("#v-collie").classList.contains("is-main-focused"), false);

// Closing drawer clears is-main-focused and restores #super-working if still working
T.closeThreadDrawer({ immediate: true });
assert.equal(getEl("#app").classList.contains("has-thread"), false);
assert.equal(getEl("#super-working").hidden, false, "closing drawer restores #super-working when Collie is still actively thinking");

console.log("OK: all v0.3.2 polish runtime contracts passed");
`
	cmd := exec.Command("node", "-e", script)
	cmd.Dir = filepath.Join("..", "..")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("v0.3.2 polish checks failed: %v\n%s", err, string(out))
	}
}
