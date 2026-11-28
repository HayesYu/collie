package collie

import (
	"os/exec"
	"path/filepath"
	"testing"
)

func TestWebMobileWorkerSheetUIAndPromptBubble(t *testing.T) {
	t.Parallel()
	if _, err := exec.LookPath("node"); err != nil {
		t.Skip("node not installed")
	}
	script := `
const fs = require("fs");
const vm = require("vm");
const assert = require("assert/strict");

const htmlSrc = fs.readFileSync("web/index.html", "utf8");
const cssSrc = fs.readFileSync("web/app.css", "utf8");
const appSrc = fs.readFileSync("web/app.js", "utf8");
const demoHtmlSrc = fs.readFileSync("docs/demo/index.html", "utf8");
const demoCssSrc = fs.readFileSync("docs/demo/app.css", "utf8");
const demoAppSrc = fs.readFileSync("docs/demo/app.js", "utf8");

// ─── 1. Static HTML / CSS / Demo Sync Contracts ───
for (const [name, src] of [["web/index.html", htmlSrc], ["docs/demo/index.html", demoHtmlSrc]]) {
  assert.ok(src.includes('id="wd-meta-kind"'), name + " must declare inline mobile #wd-meta-kind");
  assert.ok(
    src.includes('id="wd-more-btn"') &&
      src.includes('popovertarget="wd-more-menu"') &&
      src.includes('id="wd-more-menu"') &&
      src.includes('popover="auto"'),
    name + " must wire mobile overflow trigger to native popover=auto #wd-more-menu",
  );
  // Immediately adjacent sibling avoids implicit aria-details
  const trigIdx = src.indexOf('id="wd-more-btn"');
  const trigEnd = src.indexOf("</button>", trigIdx);
  const popIdx = src.indexOf('<div id="wd-more-menu"', trigEnd);
  assert.ok(popIdx > trigEnd && popIdx - trigEnd < 40, name + " must place #wd-more-menu immediately after #wd-more-btn");
  assert.ok(!src.includes('role="menu"') && !src.includes('aria-haspopup="menu"'), name + " must not falsely claim ARIA menu semantics on popover button group");
}

for (const [name, src] of [["web/app.css", cssSrc], ["docs/demo/app.css", demoCssSrc]]) {
  assert.ok(src.includes(".wd-more-pop[popover]"), name + " must style #wd-more-menu popover");
  assert.ok(src.includes("position-area: block-end span-inline-start;"), name + " must anchor #wd-more-menu via CSS Anchor Positioning");
  assert.ok(src.includes("@supports not (position-area: block-end)"), name + " must provide non-anchor fixed fallback");
  assert.ok(src.includes(".msg.user.is-collapsible:not(.is-expanded) .wd-prompt-body"), name + " must clamp long prompt bubbles with gradient fade");
  const popStart = src.indexOf("/* Mobile worker overflow popover");
  const popEnd = src.indexOf(".wd-mp-info {");
  const popBlock = src.slice(popStart, popEnd);
  assert.ok(!popBlock.includes("backdrop-filter"), name + " mobile worker overflow popover must never use backdrop-filter");
  assert.ok(popBlock.includes("transition-behavior: allow-discrete") && popBlock.includes("@starting-style"), name + " popover transition must use compositor opacity/translate with allow-discrete and @starting-style");
  assert.ok(src.includes(".wd-summary { display: none !important; }"), name + " mobile <=760px hides duplicate 3rd-layer #wd-summary strip");
  assert.ok(src.includes("#wd-thread-btn, #wd-interrupt, #wd-popout { display: none !important; }"), name + " mobile <=760px collapses secondary inline header icons into overflow popover");
}
assert.ok(demoAppSrc.includes("parseWorkerPromptEnvelope") && demoAppSrc.includes("syncWdMoreMenuInfo"), "docs/demo/app.js must be synced");

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
let nowMs = 1000;
let timerSeq = 1;
const timers = new Map();
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
  let _innerHTML = "", _popOpen = false;
  const el = {
    id, tagName, value: "", textContent: "",
    get innerHTML() { return _innerHTML; },
    set innerHTML(v) { _innerHTML = String(v); },
    get childElementCount() { return el.children.length || (_innerHTML ? 1 : 0); },
    hidden: false, disabled: false, checked: false, open: false, inert: false, tabIndex: 0,
    className: "", title: "", href: "",
    style: {
      transform: "", opacity: "", transition: "", top: "", right: "", left: "",
      setProperty(k, v) { styles.set(k, String(v)); },
      getPropertyValue(k) { return styles.get(k) || ""; },
      removeProperty(k) { styles.delete(k); },
    },
    dataset: {}, classList: makeClassList(), children: [], selectedOptions: [], options: [],
    scrollTop: 0, scrollHeight: 800, clientHeight: 600, scrollWidth: 390, clientWidth: 390,
    offsetWidth: 260, offsetHeight: 200, offsetLeft: 0, offsetParent: {}, isConnected: true,
    _qs: {}, _qsa: {},
    addEventListener(type, fn) { if (!listeners.has(type)) listeners.set(type, []); listeners.get(type).push(fn); },
    removeEventListener(type, fn) { const l = listeners.get(type); if (!l) return; const i = l.indexOf(fn); if (i >= 0) l.splice(i, 1); },
    dispatch(type, ev = {}) {
      const e = { type, target: el, currentTarget: el, defaultPrevented: false, preventDefault() { this.defaultPrevented = true; }, stopPropagation() {}, composedPath: () => [el], ...ev };
      for (const fn of [...(listeners.get(type) || [])]) fn(e);
      return e;
    },
    matches(sel) { return sel === ":popover-open" ? _popOpen : false; },
    showPopover() { if (_popOpen) return; _popOpen = true; el.dispatch("toggle", { newState: "open", oldState: "closed" }); },
    hidePopover() { if (!_popOpen) return; _popOpen = false; el.dispatch("toggle", { newState: "closed", oldState: "open" }); },
    querySelector(sel) { if (el._qs[sel]) return el._qs[sel]; const c = makeEl(id + ":" + sel); el._qs[sel] = c; return c; },
    querySelectorAll(sel) { return el._qsa[sel] || []; },
    setAttribute(k, v) { attrs.set(k, String(v)); if (k === "href") el.href = String(v); if (k === "title") el.title = String(v); if (k === "class") el.className = String(v); },
    getAttribute(k) { if (k === "href" && !attrs.has(k)) return el.href; return attrs.get(k) ?? ""; },
    removeAttribute(k) { attrs.delete(k); },
    hasAttribute(k) { return attrs.has(k); },
    closest() { return null; }, contains(node) { return node === el || el.children.includes(node); },
    append() {}, appendChild(c) { c.parentNode = el; el.children.push(c); return c; },
    remove() {}, after() {}, focus() {}, blur() {}, click() { el.dispatch("click"); }, scrollIntoView() {},
    showModal() { el.open = true; }, close() { el.open = false; },
    getBoundingClientRect() { return { top: 20, bottom: 56, left: 300, right: 336, width: 36, height: 36 }; },
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
getEl("#worker-drawer").hidden = true;
getEl("#worker-scrim").hidden = true;
getEl("#thread-drawer").hidden = true;
getEl("#file-view").hidden = true;
getEl("#super-search").hidden = true;
getEl("#collie-menu").hidden = true;
getEl("#wd-mp-interrupt-label").textContent = "中断当前执行 (Ctrl-C)";

const docListeners = new Map();
const storage = new Map();
const fetchCalls = [];
let copiedText = "";
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
  dispatch(type, ev = {}) {
    const e = { type, target: makeEl("doc-target"), defaultPrevented: false, preventDefault() { this.defaultPrevented = true; }, stopPropagation() {}, composedPath: () => [], ...ev };
    for (const fn of [...(docListeners.get(type) || [])]) fn(e);
    return e;
  },
};
doc.activeElement = doc.body;

const ctx = {
  console, Math, Date, JSON, Set, Map, RegExp, Error, Promise, URL, URLSearchParams, Object, Array, Number, String, Boolean,
  setTimeout: (fn, ms = 0) => { const id = timerSeq++; timers.set(id, { id, fn, at: nowMs + (Number(ms) || 0) }); return id; },
  clearTimeout: (id) => { timers.delete(id); },
  setInterval: () => 1, clearInterval() {}, queueMicrotask,
  requestAnimationFrame: (fn) => { fn(nowMs); return 1; }, cancelAnimationFrame() {},
  document: doc,
  location: { hash: "#/collie", origin: "http://127.0.0.1:7788", href: "http://127.0.0.1:7788/#/collie", pathname: "/", search: "" },
  history: { state: null, pushState() {}, replaceState() {}, back() {} },
  navigator: { platform: "iPhone", clipboard: { writeText: async (t) => { copiedText = t; } } },
  isSecureContext: true, innerWidth: 390, innerHeight: 844,
  CSS: { escape: (s) => String(s), supports: () => false },
  HTMLDialogElement: { prototype: {} },
  Blob, Uint8Array, TextEncoder, TextDecoder, atob, AbortController,
  crypto: { randomUUID: () => "wd-sheet-uuid" },
  performance: { now: () => nowMs },
  Event: class Event { constructor(t) { this.type = t; } },
  CustomEvent: class CustomEvent { constructor(t, d) { this.type = t; this.detail = d?.detail; } },
  ResizeObserver: class { observe() {} disconnect() {} },
  MutationObserver: class { observe() {} disconnect() {} },
  IntersectionObserver: class { observe() {} disconnect() {} },
  EventSource: class { addEventListener() {} close() {} },
  Notification: class { static permission = "default"; static async requestPermission() { return "default"; } },
  localStorage: { getItem: (k) => (storage.has(k) ? storage.get(k) : null), setItem: (k, v) => storage.set(k, String(v)), removeItem: (k) => storage.delete(k) },
  matchMedia: (q) => ({ matches: q.includes("max-width: 760px") && ctx.innerWidth <= 760, addEventListener() {} }),
  getComputedStyle: () => ({ getPropertyValue: () => "" }),
  getSelection: () => ({ isCollapsed: true, toString: () => "" }),
  addEventListener() {}, removeEventListener() {}, dispatchEvent() {},
  fetch: async (url, opts) => {
    fetchCalls.push({ url: String(url), body: opts?.body || "" });
    return { ok: true, status: 200, json: async () => ({ ok: true, models: [{ id: "neon-xhigh", label: "Gemini 4 Neon" }], default: "neon-xhigh" }) };
  },
};
ctx.window = ctx;
ctx.globalThis = ctx;

const marker = "  boot();\n})();";
const instrumented = appSrc.replace(
  marker,
  "  globalThis.__test = { setView: (v) => { view = v; }, setSup: (s) => { sup = normSup(s); }, setState: (s) => { state = s; }, parseWorkerPromptEnvelope, renderWorkerUserBubble, parseUserParts, drawThread, openWorkerDrawer, closeWorkerDrawer, curWdMode, isWdMoreOpen, closeWdMoreMenu };\n})();",
);
vm.runInNewContext(instrumented, ctx, { filename: "web/app.js" });
const T = ctx.__test;

// 2a) parseWorkerPromptEnvelope cleans Collie trigger wrapper, un-indents Markdown, strips [spilled; run ...], and restores fullTask when available
const fullTaskMd = [
  "Refactor the parser pipeline to keep clean module boundaries.",
  "",
  "- Reference \x60pkg/extract/pipeline.go\x60 and existing module layout.",
  "- Check \x60/home/demo/workspace/app/SUMMARY.md\x60 for design notes.",
  "",
  "Deliverables:",
  "1. **Zero regressions**: keep all unit tests green.",
  "2. **Clean Markdown**: format headings, inline code, and lists properly.",
].join("\n");

const spilledEnvelope = [
  "[collie] 1 new event",
  "",
  "#t14761 · chat:main · system · from collie · 13:36",
  '  You are worker "impl-parser-cleanup", managed by Collie (thread chat:main). Your task:',
  "  ",
  "  Refactor the parser pipeline to keep clean module boundaries.",
  "  ",
  "  - Reference \x60pkg/extract/pipeline.go\x60 and existing module layout.",
  "  1. **Zero regressions**: keep all unit ... [spilled; run \x60collie show t14761\x60]",
].join("\n");

const restored = T.parseWorkerPromptEnvelope(spilledEnvelope, { fullTask: fullTaskMd });
assert.equal(restored.isTaskBrief, true);
assert.equal(restored.isDispatched, true);
assert.equal(restored.time, "13:36");
assert.equal(restored.restoredFull, true);
assert.equal(restored.spilled, false);
assert.equal(restored.useMarkdown, true);
assert.equal(restored.collapsible, true);
assert.ok(!restored.body.includes("[spilled;"), "restored task must not contain [spilled; ...] machine tail");
assert.ok(!restored.body.includes("You are worker"), "must strip boilerplate worker preamble");
assert.ok(restored.body.includes("Clean Markdown"), "must restore complete fullTask body");

// 2b) Without fullTask, spilled tail is stripped and broken mid-token Markdown is healed cleanly
const midCutEnvelope = [
  "[collie] 1 new event",
  "",
  "#t14762 · chat:main · system · from collie · 13:40",
  "  Please check \x60src/web/drawer and **strict zero ... [spilled; run \x60collie show t14762\x60]",
].join("\n");
const cleaned = T.parseWorkerPromptEnvelope(midCutEnvelope, {});
assert.equal(cleaned.spilled, true);
assert.equal(cleaned.restoredFull, false);
assert.ok(!cleaned.body.includes("collie show t14762"), "must strip raw collie show command");
assert.ok(cleaned.body.endsWith("…"), "truncated excerpt ends cleanly with ellipsis");
assert.ok(!cleaned.body.includes("**"), "unclosed trailing ** marker must be stripped cleanly");

// 2c) Direct spawn brief strips How this works boilerplate; short human message stays plain & uncollapsed
const directBrief = 'You are worker "fix-ui", managed by Collie (thread chat:main). Your task:\n\nFix header alignment.\n\nHow this works:\n- Collie relays between you and the human, through collie report "…".\n- Follow-ups arrive with [collie].';
const directRes = T.parseWorkerPromptEnvelope(directBrief);
assert.equal(directRes.isTaskBrief, true);
assert.equal(directRes.body, "Fix header alignment.");
assert.equal(directRes.collapsible, false, "short brief stays uncollapsed");

const shortHuman = T.parseWorkerPromptEnvelope("跑一遍单测看看");
assert.equal(shortHuman.isDispatched, false);
assert.equal(shortHuman.useMarkdown, false);
assert.equal(shortHuman.collapsible, false);

// 2d) Rendered bubble uses clean HTML (<strong>, <code>, <ul>/<ol>) with zero raw ** or backticks and zero [spilled
const bubbleHtml = T.renderWorkerUserBubble(T.parseUserParts(spilledEnvelope), getEl("#wd-thread"), 0, true, fullTaskMd);
assert.ok(bubbleHtml.includes("is-md") && bubbleHtml.includes("is-collapsible") && bubbleHtml.includes("wd-prompt-toggle"));
assert.ok(bubbleHtml.includes("<strong>Zero regressions</strong>"), "bold Markdown must render as <strong>");
assert.ok(bubbleHtml.includes("<code>pkg/extract/pipeline.go</code>"), "inline code must render as <code>");
assert.ok(!bubbleHtml.includes("[spilled;") && !bubbleHtml.includes("**Zero regressions**"), "raw Markdown markers and spilled tail must not leak");

// 2e) Mobile header + #wd-more-menu sync, fallback positioning, terminal toggle, copy cwd, and double-tap interrupt
T.setView("collie");
T.setState({
  agents: [{ pane_id: "p-w1", name: "impl-parser-cleanup", label: "沉淀解析模块", kind: "antigravity", status: "working", is_agent: true, cwd: "/home/demo/workspace/app", run: ["neon-xhigh", "high"] }],
  machines: [],
  shells: [],
});
T.setSup({
  router: { status: "idle", alive: true },
  workers: [{ name: "impl-parser-cleanup", label: "沉淀解析模块", pane_id: "p-w1", kind: "antigravity", status: "working", task: fullTaskMd, cwd: "/home/demo/workspace/app", run: ["neon-xhigh", "high"], ended: false }],
  entries: [
    { id: "e1", role: "user", text: "帮忙沉淀解析模块", at: "2026-11-28T05:30:00Z" },
    { id: "e2", role: "collie", text: "已派 impl-parser-cleanup 跟进", at: "2026-11-28T05:31:00Z", re: { entry: "e1", who: "你", text: "帮忙沉淀解析模块" } },
  ],
});

T.openWorkerDrawer({ worker: "impl-parser-cleanup", fromThreadRoot: "e1", title: "沉淀解析模块", note: "13:36 已派发" });
assert.equal(getEl("#wd-meta-kind").textContent, "antigravity");
assert.equal(getEl("#wd-meta-kind").hidden, false);
assert.equal(getEl("#wd-mp-cwd-text").textContent, "~/workspace/app");
assert.equal(getEl("#wd-mp-thread").hidden, false);
assert.equal(getEl("#wd-mp-screen").hidden, false);
assert.equal(getEl("#wd-mp-screen-label").textContent, "查看终端屏幕");
assert.equal(getEl("#wd-mp-popout").href, "#/p/p-w1");

// Opening popover computes fallback coordinates when CSS anchor positioning is unsupported
getEl("#wd-more-menu").showPopover();
assert.equal(T.isWdMoreOpen(), true);
assert.equal(getEl("#wd-more-btn").getAttribute("aria-expanded"), "true");
assert.equal(getEl("#wd-more-menu").style.top, "62px");
assert.equal(getEl("#wd-more-menu").style.right, "54px");

// Pressing Escape while #wd-more-menu is open closes ONLY the popover, keeping #worker-drawer open
doc.dispatch("keydown", { key: "Escape" });
assert.equal(T.isWdMoreOpen(), false, "Escape closes open #wd-more-menu");
assert.equal(getEl("#worker-drawer").hidden, false, "Escape on open popover must not close #worker-drawer");

// Re-open and copy cwd button copies full path and closes popover
getEl("#wd-more-menu").showPopover();
getEl("#wd-mp-cwd").dispatch("click");
assert.equal(copiedText, "/home/demo/workspace/app");
assert.equal(T.isWdMoreOpen(), false);

// Toggle terminal screen inside mobile overflow menu works even when developer mode is off
getEl("#wd-more-menu").showPopover();
getEl("#wd-mp-screen").dispatch("click");
assert.equal(T.curWdMode(), "screen");
assert.equal(getEl("#wd-mp-screen-label").textContent, "返回对话视图");
getEl("#wd-mp-screen").dispatch("click");
assert.equal(T.curWdMode(), "chat");
assert.equal(getEl("#wd-mp-screen-label").textContent, "查看终端屏幕");

// Double-tap interrupt inside #wd-more-menu keeps popover open on 1st click, sends ctrl+c & closes on 2nd click
fetchCalls.length = 0;
getEl("#wd-more-menu").showPopover();
getEl("#wd-mp-interrupt").dispatch("click");
assert.equal(fetchCalls.filter((c) => c.url.includes("/keys")).length, 0, "1st click must arm without sending ctrl+c");
assert.equal(getEl("#wd-mp-interrupt").classList.contains("is-armed"), true);
assert.equal(getEl("#wd-mp-interrupt-label").textContent, "再点一次确认中断");
assert.equal(T.isWdMoreOpen(), true, "popover stays open while armed for 2nd tap");

// Closing popover while armed auto-disarms cleanly
getEl("#wd-more-menu").hidePopover();
assert.equal(getEl("#wd-mp-interrupt").classList.contains("is-armed"), false);
assert.equal(getEl("#wd-mp-interrupt-label").textContent, "中断当前执行 (Ctrl-C)");

// Arm and confirm within 3s sends ctrl+c and closes popover
(async () => {
  getEl("#wd-more-menu").showPopover();
  getEl("#wd-mp-interrupt").dispatch("click");
  advanceTimers(900);
  getEl("#wd-mp-interrupt").dispatch("click");
  for (let i = 0; i < 8; i++) await Promise.resolve();
  const keyCalls = fetchCalls.filter((c) => c.url.includes("/keys"));
  assert.equal(keyCalls.length, 1, "2nd tap within 3s sends ctrl+c");
  assert.ok(keyCalls[0].body.includes('"ctrl+c"'), "sends ctrl+c payload");
  assert.equal(T.isWdMoreOpen(), false, "popover closes after confirmed interrupt");

  T.closeWorkerDrawer({ immediate: true });
  console.log("ALL_MOBILE_WORKER_SHEET_UI_TESTS_OK");
})().catch((err) => {
  console.error(err);
  process.exit(1);
});
`
	cmd := exec.Command("node", "-e", script)
	cmd.Dir = filepath.Join("..", "..")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("mobile worker sheet UI test failed: %v\n%s", err, out)
	}
}
