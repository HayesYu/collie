package collie

import (
	"os/exec"
	"path/filepath"
	"testing"
)

func TestWebMobileHeaderPolishContracts(t *testing.T) {
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

// ─── 1. Static HTML / CSS Contracts ───
// 1a) Update capsule moved out of mobile top bar into #collie-menu top, with 5px green dot on ⋯
const collieHeadBlock = htmlSrc.match(/<section class="view" id="v-collie"[\s\S]*?<\/header>/)?.[0] || "";
assert.ok(collieHeadBlock, "expected #v-collie header in index.html");
const headActsBeforeMenu = collieHeadBlock.split('<div class="menu-wrap">')[0] || "";
assert.ok(!headActsBeforeMenu.includes('id="collie-update-btn"'), "#collie-update-btn must not sit directly in the top bar before menu-wrap");
const collieMenuBlock = collieHeadBlock.match(/<div class="menu" id="collie-menu" hidden>([\s\S]*?)<\/div>/)?.[1] || "";
assert.ok(collieMenuBlock.includes('id="collie-update-btn"') && collieMenuBlock.includes('id="collie-update-sep"'), "#collie-menu must host #collie-update-btn and #collie-update-sep");
assert.ok(
  collieMenuBlock.indexOf('id="collie-update-btn"') < collieMenuBlock.indexOf('data-act="router-open"'),
  "#collie-update-btn must appear at the very top of #collie-menu",
);
const moreBtnBlock = collieHeadBlock.match(/<button[^>]*id="collie-menu-btn"[\s\S]*?<\/button>/)?.[0] || "";
assert.ok(
  moreBtnBlock.includes('data-menu="collie-menu"')
    && moreBtnBlock.includes('aria-label="更多"')
    && moreBtnBlock.includes('id="collie-update-dot"'),
  "#collie-menu-btn must carry aria-label and mobile update dot",
);
assert.match(cssSrc, /\.menu-update-dot\s*\{[\s\S]*?width:\s*5px;\s*height:\s*5px;[\s\S]*?background:\s*var\(--ok\);/, "mobile ⋯ update indicator must be a 5px green dot");
assert.ok(cssSrc.includes("@media (min-width: 761px) {\n  .mobile-only { display: none !important; }"), "desktop (>760px) hides mobile-only update dot and menu entry");

// 1b) Mobile 小本本 switches to minimalist notepad icon via CSS mask while preserving desktop plain-text trigger & unread dot
const notesBtnHtml = collieHeadBlock.match(/<button[^>]*id="open-notes-btn"[\s\S]*?<\/button>/)?.[0] || "";
assert.ok(
  notesBtnHtml.includes('aria-label="小本本"')
    && notesBtnHtml.includes("<span>小本本</span>")
    && notesBtnHtml.includes('id="open-notes-dot"')
    && !notesBtnHtml.includes("<svg"),
  "#open-notes-btn keeps plain-text markup for desktop and aria-label for mobile icon mode",
);
assert.ok(
  cssSrc.includes(".head-text-btn.notes-head-btn::before")
    && cssSrc.includes("forced-color-adjust: none;")
    && cssSrc.includes(".head-text-btn.notes-head-btn > span:first-child"),
  "mobile <=760px renders minimalist CSS-masked notepad icon while keeping accessible text",
);

// 1c) Mobile hides [kind] pill & #router-model, eliminates empty-span flex gaps and half-letter clipping
assert.ok(cssSrc.includes("#v-collie .d-meta > :empty { display: none; }"), "empty subtitle spans must not consume flex gap");
assert.ok(cssSrc.includes("#v-collie #router-status, #v-collie #router-ctx { flex: none; }"), "status and context percentage must never shrink");
assert.ok(
  cssSrc.includes("@container collie-top (inline-size < 480px)")
    && cssSrc.includes("#v-collie .d-meta > #router-model { display: none; }"),
  "narrow header container hides #router-model before half-letter clipping can occur",
);
assert.ok(
  cssSrc.includes("#v-collie .d-name > .kind { display: none; }")
    && cssSrc.includes("#v-collie #router-model { display: none; }"),
  "mobile <=760px hides kind pill and router model so status and context stay clean",
);

// 1d) Crew chip spinner breathing room on mobile (including .crew.is-no-kind specificity)
assert.ok(
  cssSrc.includes(".crew .chip, .crew.is-no-kind .chip { height: 27px; padding: 0 11px 0 10.5px; gap: 9px; }")
    && cssSrc.includes(".chip > .dot.working, .chip > .dot.starting { margin-inline: 1.5px 2px; }"),
  "mobile crew chips must leave airy clearance around spinning indicator ring",
);

// 1e) Reply option buttons never prematurely truncate or break words mid-character on 390px
assert.ok(
  /\.reply-opt\s*\{[\s\S]*?overflow:\s*visible;\s*text-overflow:\s*clip;\s*overflow-wrap:\s*break-word;\s*word-break:\s*normal;/.test(cssSrc),
  ".reply-opt must wrap naturally with overflow:visible, text-overflow:clip, and overflow-wrap:break-word",
);
assert.ok(!/\.reply-opt\s*\{[^}]*overflow-wrap:\s*anywhere/.test(cssSrc), ".reply-opt must not use overflow-wrap: anywhere which allows mid-word flex crushing");
assert.ok(cssSrc.includes(".decision-wrap { margin: 2px 0 6px; display: grid; gap: 7px; min-width: 0; max-width: 100%; }"), ".decision-wrap must constrain grid width to prevent overflow");

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
    set innerHTML(v) { _innerHTML = String(v); },
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
    dispatch(type, ev = {}) {
      const e = { type, target: el, currentTarget: el, defaultPrevented: false, preventDefault() { this.defaultPrevented = true; }, stopPropagation() {}, composedPath: () => [el], ...ev };
      for (const fn of [...(listeners.get(type) || [])]) fn(e);
      return e;
    },
    querySelector(sel) { if (el._qs[sel]) return el._qs[sel]; const c = makeEl(id + ":" + sel); el._qs[sel] = c; return c; },
    querySelectorAll(sel) { return el._qsa[sel] || []; },
    setAttribute(k, v) { attrs.set(k, String(v)); if (k === "title") el.title = String(v); },
    getAttribute(k) { return attrs.get(k) ?? ""; },
    removeAttribute(k) { attrs.delete(k); },
    hasAttribute(k) { return attrs.has(k); },
    closest() { return null; }, contains(node) { return node === el; },
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
getEl("#collie-menu").hidden = true;

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
  crypto: { randomUUID: () => "mh-uuid" },
  performance: { now: () => 1000 },
  Event: class Event { constructor(t) { this.type = t; } },
  CustomEvent: class CustomEvent { constructor(t, d) { this.type = t; this.detail = d?.detail; } },
  ResizeObserver: class { observe() {} disconnect() {} },
  MutationObserver: class { observe() {} disconnect() {} },
  IntersectionObserver: class { observe() {} disconnect() {} },
  EventSource: class { addEventListener() {} close() {} },
  Notification: class { static permission = "default"; static async requestPermission() { return "default"; } },
  localStorage: { getItem: (k) => (storage.has(k) ? storage.get(k) : null), setItem: (k, v) => storage.set(k, String(v)), removeItem: (k) => storage.delete(k) },
  matchMedia: (q) => ({ matches: q.includes("max-width: 760px"), addEventListener() {} }),
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
  "  globalThis.__test = { setVersionInfo: (v) => { versionInfo = v; }, setUpdateFlow: (u) => Object.assign(updateFlow, u), renderUpdateEntry, replyOptsMarkup };\n})();",
);
vm.runInNewContext(instrumented, ctx, { filename: "web/app.js" });
const T = ctx.__test;

// 2a) Up-to-date hides menu update item, divider, and 5px green dot
T.setVersionInfo({
  version: "v0.3.12",
  latest: "v0.3.12",
  outdated: [],
  machines: [{ local: true, name: "hub", version: "v0.3.12", online: true }],
});
T.renderUpdateEntry();
assert.equal(getEl("#collie-update-btn").hidden, true);
assert.equal(getEl("#collie-update-sep").hidden, true);
assert.equal(getEl("#collie-update-dot").hidden, true);
assert.equal(getEl("#collie-menu-btn").getAttribute("aria-label"), "更多");

// 2b) Outdated version shows menu update item with full label + divider + 5px green dot on ⋯
T.setVersionInfo({
  version: "v0.3.11",
  latest: "v0.3.12",
  outdated: ["hub"],
  machines: [{ local: true, name: "hub", version: "v0.3.11", online: true }],
});
T.renderUpdateEntry();
assert.equal(getEl("#collie-update-btn").hidden, false);
assert.ok(getEl("#collie-update-btn").innerHTML.includes("更新到 v0.3.12"), "menu update button shows full version label");
assert.equal(getEl("#collie-update-sep").hidden, false);
assert.equal(getEl("#collie-update-dot").hidden, false);
assert.equal(getEl("#collie-update-dot").classList.contains("is-failed"), false);
assert.equal(getEl("#collie-menu-btn").getAttribute("aria-label"), "更多（有新版本 v0.3.12）");

// 2c) Failed update switches indicator dot to amber failed state and shows retry label
T.setUpdateFlow({ busy: false, phase: "failed", failedMachine: "hub", error: "network timeout" });
T.renderUpdateEntry();
assert.equal(getEl("#collie-update-btn").classList.contains("failed"), true);
assert.ok(getEl("#collie-update-btn").innerHTML.includes("重试更新"));
assert.equal(getEl("#collie-update-dot").hidden, false);
assert.equal(getEl("#collie-update-dot").classList.contains("is-failed"), true);

// 2d) Reply options preserve full <=16-char labels verbatim without truncation
const entry = {
  id: "e99",
  role: "collie",
  text: "要不要直接修？",
  options: ["直接派人瘦身手机顶栏", "先把更新胶囊移进更多菜单再精简"],
};
const optsHtml = T.replyOptsMarkup(entry, 0, [entry], new Set(["e99"]));
assert.ok(optsHtml.includes('data-opt="直接派人瘦身手机顶栏">直接派人瘦身手机顶栏</button>'));
assert.ok(optsHtml.includes('data-opt="先把更新胶囊移进更多菜单再精简">先把更新胶囊移进更多菜单再精简</button>'));

console.log("mobile header polish contracts ok");
`
	cmd := exec.Command("node", "-e", script)
	cmd.Dir = filepath.Join("..", "..")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("mobile header polish test failed: %v\n%s", err, string(out))
	}
}
