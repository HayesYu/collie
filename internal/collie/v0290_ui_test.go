package collie

import (
	"os/exec"
	"path/filepath"
	"testing"
)

func TestWebV0290DispatchCapsuleSwapAndPendingGuard(t *testing.T) {
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

// 1) Static contract checks
assert.ok(htmlSrc.includes('id="t-bar"') && htmlSrc.includes('id="t-toggle"') && htmlSrc.includes('id="t-controls" hidden') && htmlSrc.includes('id="t-reset"'), "index.html must contain collapsed dispatch capsule markup");
assert.ok(appSrc.includes("默认兜底配置；想按任务分模型（如写文档用某模型），对话里告诉 Collie 一次即会长期记住。"), "settings subtitle must explain memory-based routing");
assert.ok(appSrc.includes('id="fs-swap-toggle-btn"') && appSrc.includes('id="fs-swap-panel"'), "Feishu configured modal must provide inline swap bot entry");
assert.ok(cssSrc.includes(".dispatch-pill") && cssSrc.includes(".dispatch-reset") && cssSrc.includes(".int-sec-head"), "app.css must style dispatch capsule and swap button header");

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
    id, tagName: "DIV", value: "", textContent: "", innerHTML: "",
    hidden: false, disabled: false, checked: false, open: false,
    className: "", title: "", style: { setProperty() {}, removeProperty() {} },
    dataset: {}, classList: makeClassList(), children: [], selectedOptions: [], options: [],
    scrollTop: 0, scrollHeight: 200, clientHeight: 200, offsetWidth: 400, offsetParent: {}, isConnected: true,
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

const storage = new Map();
const fetchLog = [];
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
  setTimeout: () => 0, clearTimeout() {}, setInterval: () => 0, clearInterval() {}, queueMicrotask,
  requestAnimationFrame: (fn) => { fn(Date.now()); return 1; },
  cancelAnimationFrame() {},
  document: doc,
  location: { hash: "#/collie", origin: "http://127.0.0.1:7788", href: "http://127.0.0.1:7788/#/collie", pathname: "/", search: "" },
  history: { state: null, pushState() {}, replaceState() {}, back() {} },
  navigator: { platform: "MacIntel", clipboard: { writeText: async () => {} } },
  isSecureContext: true, innerWidth: 1280, innerHeight: 800,
  visualViewport: { height: 800, offsetTop: 0, addEventListener() {} },
  CSS: { escape: (s) => String(s), supports: () => true },
  HTMLDialogElement: { prototype: {} },
  Blob, Uint8Array, TextEncoder, atob, AbortController,
  crypto: { randomUUID: () => "v0290-uuid" },
  performance: { now: () => 0 },
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
  addEventListener() {}, removeEventListener() {}, dispatchEvent() {},
  fetch: async (url, init) => {
    const body = init?.body ? JSON.parse(init.body) : null;
    fetchLog.push({ url, method: init?.method || "GET", body });
    if (String(url).includes("/api/super/settings")) {
      return { ok: true, status: 200, json: async () => ({ settings: { worker_machine: "", worker_kind: "codex", worker_model: "gpt-default", worker_effort: "medium" } }) };
    }
    if (String(url).includes("/api/models")) {
      return { ok: true, status: 200, json: async () => ({ models: [{ id: "gpt-default", label: "GPT Default" }, { id: "claude-sonnet", label: "Sonnet" }], efforts: ["low", "medium", "high"] }) };
    }
    if (String(url).includes("/api/super/say")) {
      return { ok: true, status: 200, json: async () => ({ ok: true, entry: "e99", trigger: "t99" }) };
    }
    if (String(url).includes("/api/super/integrations/feishu/onboard")) {
      return { ok: true, status: 200, json: async () => ({ state: "idle" }) };
    }
    return { ok: true, status: 200, json: async () => ({ ok: true }) };
  },
};
ctx.window = ctx;
ctx.globalThis = ctx;

const marker = "  boot();\n})();";
const instrumented = appSrc.replace(
  marker,
  "  globalThis.__test = { setSup: (s) => { sup = normSup(s); }, setState: (st) => { localState = st; state = mergedState(); }, setView: (v) => { view = v; }, renderTarget, sendSuper, activePending, renderSuper, settingsHTML, entryPendingNow };\n})();"
);
vm.runInNewContext(instrumented, ctx, { filename: "web/app.js" });

(async () => {
  const T = ctx.__test;
  T.setView("collie");
  T.setState({ ok: true, machines: [{ id: "", name: "本机", local: true, online: true, agents: ["codex", "claude"] }], agents: [], shells: [] });

  // 2) Dispatch bar starts collapsed with "派活 · 默认", expands on click, shows override & reset
  await T.renderTarget(true);
  await new Promise((r) => setImmediate(r));
  const tBar = getEl("#t-bar"), tToggle = getEl("#t-toggle"), tLbl = getEl("#t-toggle-lbl"), tControls = getEl("#t-controls"), tReset = getEl("#t-reset");
  assert.equal(tControls.hidden, true, "dispatch controls must start hidden");
  assert.equal(tBar.classList.contains("is-collapsed"), true);
  assert.equal(tLbl.textContent, "派活 · 默认");
  assert.equal(tReset.hidden, true, "reset button hidden when no override");
  assert.ok(tToggle.title.includes("按记忆偏好优先") && tToggle.title.toLowerCase().includes("codex"), "tooltip must describe memory-first + fallback: " + tToggle.title);
  assert.ok(tToggle.getAttribute("aria-description").includes("按记忆偏好优先") && tToggle.getAttribute("aria-description").toLowerCase().includes("codex"), "aria-description must mirror fallback tooltip");

  // Click toggle to expand
  tToggle.dispatch("click");
  assert.equal(tControls.hidden, false, "clicking toggle expands dispatch controls");
  assert.equal(tToggle.getAttribute("aria-expanded"), "true");

  // Change agent to claude -> override active, reset visible
  const kSel = getEl("#t-kind");
  kSel.value = "claude";
  kSel.dispatch("change", { target: kSel });
  await new Promise((r) => setImmediate(r));
  assert.equal(tReset.hidden, false, "reset button must appear when overridden");
  assert.equal(tToggle.classList.contains("is-override"), true);

  // Collapse while overridden -> pill clearly displays active override + reset stays visible
  tToggle.dispatch("click");
  assert.equal(tControls.hidden, true);
  assert.ok(tLbl.textContent.includes("本次") && tLbl.textContent.toLowerCase().includes("claude"), "collapsed override label = " + tLbl.textContent);
  assert.equal(tReset.hidden, false, "reset stays visible while collapsed with override");

  // Click reset -> snaps back to default & collapsed
  tReset.dispatch("click");
  await new Promise((r) => setImmediate(r));
  assert.equal(tLbl.textContent, "派活 · 默认");
  assert.equal(tReset.hidden, true);
  assert.equal(tToggle.classList.contains("is-override"), false);

  // 3) Unquoted sendSuper must NOT optimistically clear pending --opt card
  const nowIso = new Date().toISOString();
  const entries = [
    { id: "e1", role: "collie", text: "要直接合并吗？", at: nowIso, options: ["合并", "再看看"] },
  ];
  T.setSup({
    router: { alive: true, status: "idle" },
    entries,
    pending: [{ id: "e1", at: nowIso, summary: "要直接合并吗？", options: ["合并", "再看看"] }],
    pinned: [],
    workers: [],
  });
  await T.sendSuper("顺便帮我查下另一个报错", null, false);
  assert.equal(T.activePending().length, 1, "unquoted sendSuper must keep --opt pending");
  assert.equal(T.activePending()[0].id, "e1");
  const lastSay = fetchLog.filter((x) => x.url === "/api/super/say").pop();
  assert.equal(lastSay.body.kind, "", "reset dispatch bar must send empty kind");
  assert.equal(lastSay.body.machine, "", "reset dispatch bar must send empty machine");

  // Legacy hub fallback without sup.pending also keeps --opt across unquoted user message
  const legacy = [
    { id: "e1", role: "collie", text: "要合并吗？", at: nowIso, options: ["合并", "不合并"] },
    { id: "e2", role: "user", text: "另起一个新话题", at: nowIso },
  ];
  T.setSup({ router: { alive: true, status: "idle" }, entries: legacy, pinned: [], workers: [] });
  assert.equal(T.entryPendingNow(legacy, 0, "e1"), true, "legacy fallback must keep --opt pending on unquoted user msg");
  legacy.push({ id: "e3", role: "user", text: "合并", at: nowIso, re: { entry: "e1" } });
  assert.equal(T.entryPendingNow(legacy, 0, "e1"), false, "explicit quote resolves --opt in legacy fallback");

  // 4) Settings HTML contains updated subtitle
  const sHtml = T.settingsHTML({ settings: { router_kind: "claude", worker_kind: "codex", worker_cwd: "~", reply_chars: 200, stall_minutes: 10 }, router_dir: "/tmp/r" });
  assert.ok(sHtml.includes("默认兜底配置；想按任务分模型（如写文档用某模型），对话里告诉 Collie 一次即会长期记住。"));

  console.log("ALL V0290 WEB UI TESTS PASSED");
})().catch((err) => { console.error(err); process.exit(1); });
`
	cmd := exec.Command("node", "-e", script)
	cmd.Dir = filepath.Join("..", "..")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("node v0290 web UI test failed: %v\n%s", err, out)
	}
}
