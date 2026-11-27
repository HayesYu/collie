package collie

import (
	"os/exec"
	"path/filepath"
	"testing"
)

func TestInboxExpandAndMobileFooterContractsJS(t *testing.T) {
	t.Parallel()
	nodeBin, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node not installed")
	}
	root := filepath.Join("..", "..")
	jsTest := `
const fs = require("node:fs");
const path = require("node:path");
const vm = require("node:vm");
const assert = require("node:assert/strict");

const root = process.argv[1];
const html = fs.readFileSync(path.join(root, "web", "index.html"), "utf8");
const css = fs.readFileSync(path.join(root, "web", "app.css"), "utf8");
const appSrc = fs.readFileSync(path.join(root, "web", "app.js"), "utf8");
const demoSrc = fs.readFileSync(path.join(root, "scripts", "demo", "demo.js"), "utf8");

// ─── 1. Static CSS & HTML contracts ───
// Base .row .text remains 3-line clamped for all existing pages (devices, etc.).
assert.match(css, /\.row \.text\s*\{[\s\S]*?-webkit-line-clamp:\s*3;/, ".row .text must stay 3-line clamped by default");
assert.match(css, /\.inbox-row\.is-expanded \.inbox-text\s*\{[\s\S]*?display:\s*block;[\s\S]*?-webkit-line-clamp:\s*unset;/, "expanded inbox text must remove line clamp");
assert.match(css, /\.inbox-toggle\[hidden\]\s*\{\s*display:\s*none;\s*\}/, "hidden inbox toggle must not render");
assert.match(css, /\.inbox-toggle:focus-visible\s*\{[\s\S]*?outline:/, "inbox toggle must expose visible keyboard focus ring");
assert.match(css, /@media \(prefers-reduced-motion:\s*reduce\)\s*\{[\s\S]*?\.inbox-toggle/, "inbox toggle must honor prefers-reduced-motion");

// Mobile sidebar footer layout (#t7173): only + in header, status left + prefs gear right (44px) in footer.
const headBlock = html.match(/<header class="side-head">([\s\S]*?)<\/header>/);
assert.ok(headBlock, "side-head must exist");
assert.doesNotMatch(headBlock[1], /nav-prefs-mobile|href="#\/prefs"/, "mobile header must not duplicate prefs gear");
assert.match(headBlock[1], /id="new-btn"/, "side-head keeps + button");
const footBlock = html.match(/<footer class="side-foot">([\s\S]*?)<\/footer>/);
assert.ok(footBlock, "side-foot must exist");
assert.match(footBlock[1], /id="nav-devices"/, "side-foot keeps device status link");
assert.match(footBlock[1], /id="nav-prefs"/, "side-foot keeps prefs gear link");
const mobileBlock = css.match(/@media \(max-width:\s*760px\)\s*\{\s*\.side-head #new-btn[\s\S]*?\n\}/);
assert.ok(mobileBlock, "mobile media query must style .side-foot and .side-prefs-btn");
assert.match(mobileBlock[0], /\.side-prefs-btn\s*\{[\s\S]*?width:\s*44px;\s*height:\s*44px;/, "mobile prefs gear must keep 44px touch target");
assert.match(mobileBlock[0], /a\.foot-status\s*\{[\s\S]*?width:\s*fit-content;[\s\S]*?min-block-size:\s*44px;/, "mobile foot-status must hug text width with 44px touch height");
assert.match(mobileBlock[0], /env\(safe-area-inset-bottom\)/, "mobile side-foot must preserve iOS safe-area bottom spacing");
assert.doesNotMatch(mobileBlock[0], /\.side-foot \.side-prefs-btn\s*\{\s*display:\s*none;/, "mobile side-foot must not hide prefs gear");

// Demo inbox must contain at least one >3-line trigger so visitors can try expand/collapse.
const demoLong = demoSrc.match(/id:\s*"t32"[\s\S]*?text:\s*"([^"]+)"/);
assert.ok(demoLong && (demoLong[1].match(/\\n/g) || []).length >= 4, "demo inbox trigger t32 must span >3 lines");

// ─── 2. Runtime JS behavior via vm sandbox ───
function makeClassList() {
  const set = new Set();
  return {
    add: (...cs) => cs.forEach((c) => c && set.add(c)),
    remove: (...cs) => cs.forEach((c) => c && set.delete(c)),
    toggle: (c, f) => { const on = f === undefined ? !set.has(c) : !!f; if (on) set.add(c); else set.delete(c); return on; },
    contains: (c) => set.has(c),
    toString: () => [...set].join(" "),
  };
}

function makeEl(id = "", tag = "DIV") {
  const listeners = new Map();
  const attrs = new Map();
  const el = {
    id,
    tagName: tag,
    value: "",
    textContent: "",
    innerHTML: "",
    hidden: false,
    disabled: false,
    checked: false,
    className: "",
    style: { setProperty() {}, removeProperty() {} },
    dataset: {},
    classList: makeClassList(),
    children: [],
    scrollTop: 0,
    scrollHeight: 200,
    clientHeight: 200,
    clientWidth: 600,
    offsetWidth: 600,
    offsetParent: {},
    isConnected: true,
    _qs: {},
    _qsa: {},
    _rect: { top: 0, bottom: 80, left: 0, right: 600, width: 600, height: 80 },
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
      if (Object.prototype.hasOwnProperty.call(el._qs, sel)) return el._qs[sel];
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
    contains(n) { return n === el || (el._contains && el._contains.has(n)); },
    append() {},
    remove() {},
    after() {},
    focus(opts) { el._focused = true; el._focusOpts = opts || null; },
    click() {},
    scrollIntoView() {},
    getBoundingClientRect() { return el._rect; },
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

for (const [formSel, inputSel] of [["#super-composer", "#super-input"], ["#composer", "#input"], ["#x-composer", "#x-input"]]) {
  const f = getEl(formSel);
  f._qs["textarea"] = getEl(inputSel);
  f._qs[".send"] = makeEl(formSel + " .send");
  f._qs[".files"] = makeEl(formSel + " .files");
  f._qs[".attach"] = makeEl(formSel + " .attach");
  f._qs['input[type="file"]'] = makeEl(formSel + " input[type=file]");
}
getEl("#re-draft")._qs[".re-t"] = makeEl("#re-draft .re-t");
getEl("#re-draft")._qs["button"] = makeEl("#re-draft button");
getEl("#thread-drawer").hidden = true;
getEl("#thread-scrim").hidden = true;
getEl("#file-view").hidden = true;

let selectionText = "";
let selectionAnchor = null;
let activeElement = null;
const storage = new Map();

function buildRow(id, { truncated = false, expanded = false, top = 0, bottom = 90 } = {}) {
  const row = makeEl("row-" + id, "LI");
  row.dataset = { inboxId: id };
  row._rect = { top, bottom, left: 0, right: 600, width: 600, height: bottom - top };
  if (expanded) { row.classList.add("is-expanded", "is-expandable"); }

  const textEl = makeEl("inbox-text-" + id, "DIV");
  textEl.clientHeight = 60;
  textEl.scrollHeight = truncated ? 140 : 40;
  textEl._rect = { top: top + 20, bottom: bottom - 10, left: 60, right: 540, width: 480, height: 60 };

  const linkEl = makeEl("link-" + id, "A");
  const lblEl = makeEl("lbl-" + id, "SPAN");
  lblEl.textContent = expanded ? "收起" : "展开";

  const btnEl = makeEl("btn-" + id, "BUTTON");
  btnEl.dataset = { inboxToggle: id };
  btnEl.hidden = !expanded;
  btnEl.setAttribute("aria-expanded", expanded ? "true" : "false");
  btnEl._qs[".inbox-toggle-lbl"] = lblEl;

  btnEl.closest = (sel) => {
    if (sel === "button[data-inbox-toggle]" || sel === "[data-inbox-toggle]" || sel === "button") return btnEl;
    if (sel === ".inbox-row") return row;
    return null;
  };
  lblEl.closest = (sel) => btnEl.closest(sel);
  textEl.closest = (sel) => {
    if (sel === ".inbox-row.is-expandable .inbox-text") return row.classList.contains("is-expandable") ? textEl : null;
    if (sel === ".inbox-row") return row;
    return null;
  };
  linkEl.closest = (sel) => {
    if (sel === ".inbox-row.is-expandable .inbox-text") return row.classList.contains("is-expandable") ? textEl : null;
    if (sel === "a, button, input, select, textarea, summary, label") return linkEl;
    if (sel === ".inbox-row") return row;
    return null;
  };

  row._qs[".inbox-text"] = textEl;
  row._qs[".inbox-toggle"] = btnEl;
  return { row, textEl, linkEl, btnEl, lblEl };
}

let mockTriggers = [];
let mockThreads = [];
const autoBody = getEl("#auto-body");
autoBody.clientHeight = 500;
autoBody._rect = { top: 0, bottom: 500, left: 0, right: 600, width: 600, height: 500 };
autoBody._qs[".inbox-subs-fold[open]"] = null;
autoBody._qs[".inbox-subs-fold"] = null;

class FakeES {
  constructor() { this.readyState = 1; this.ls = {}; }
  addEventListener(t, fn) { (this.ls[t] ||= []).push(fn); }
  close() { this.readyState = 2; }
}

const doc = {
  documentElement: makeEl("html"),
  body: makeEl("body"),
  hidden: false,
  get activeElement() { return activeElement; },
  hasFocus: () => true,
  createElement: (tag) => makeEl(tag, tag.toUpperCase()),
  getElementById: (id) => getEl("#" + id),
  querySelector: (sel) => (sel === "dialog[open]" ? null : getEl(sel)),
  querySelectorAll: () => [],
  addEventListener() {},
};

const ctx = {
  console, URL, URLSearchParams, JSON, Math, Date, Map, Set, RegExp, Promise, Array, Object, Number, String, Boolean, Error, Event,
  setTimeout: () => 0,
  clearTimeout() {},
  setInterval: () => 0,
  clearInterval() {},
  performance: { now: () => 1000 },
  document: doc,
  EventSource: FakeES,
  MutationObserver: class { observe() {} disconnect() {} },
  ResizeObserver: class { observe() {} disconnect() {} },
  HTMLElement: function() {},
  HTMLDialogElement: { prototype: {} },
  CSS: { escape: (s) => String(s), supports: () => true },
  crypto: { randomUUID: () => "inbox-test-uuid" },
  isSecureContext: true,
  innerWidth: 1440,
  innerHeight: 900,
  focus() {},
  dispatchEvent() {},
  getComputedStyle: () => ({ getPropertyValue: () => "" }),
  navigator: { userAgent: "NodeTest", platform: "Linux", clipboard: { writeText: async () => {} } },
  location: { origin: "http://127.0.0.1:7788", hostname: "127.0.0.1", hash: "#/auto", search: "", pathname: "/" },
  history: { state: {}, pushState() {}, replaceState() {}, back() {} },
  localStorage: {
    getItem: (k) => (storage.has(k) ? storage.get(k) : null),
    setItem: (k, v) => storage.set(k, String(v)),
    removeItem: (k) => storage.delete(k),
  },
  matchMedia: () => ({ matches: false, addEventListener() {} }),
  getSelection: () => ({ toString: () => selectionText, anchorNode: selectionAnchor, focusNode: selectionAnchor }),
  addEventListener() {},
  removeEventListener() {},
  requestAnimationFrame: (fn) => { fn(); return 1; },
  cancelAnimationFrame() {},
  fetch: async (url) => {
    const u = String(url);
    if (u === "/api/super/triggers") {
      return {
        ok: true,
        status: 200,
        headers: { get: () => "application/json" },
        text: async () => JSON.stringify({ triggers: mockTriggers, threads: mockThreads }),
        json: async () => ({ triggers: mockTriggers, threads: mockThreads }),
      };
    }
    return {
      ok: true,
      status: 200,
      headers: { get: () => "application/json" },
      text: async () => JSON.stringify({ ok: true }),
      json: async () => ({ ok: true }),
    };
  },
};
ctx.window = ctx;
ctx.globalThis = ctx;

const marker = "  boot();\n})();";
assert.ok(appSrc.includes(marker), "expected boot() tail in web/app.js");
const instrumented = appSrc.replace(marker,
  "  globalThis.__test = { inboxHTML, inboxExpanded, syncInboxRow, syncInboxOverflow, toggleInboxRow, loadAuto, setView: (v) => { view = v; }, setAutoTab: (t) => { autoTab = t; }, getBody: () => $(\"#auto-body\") };\n})();"
);
vm.runInNewContext(instrumented, ctx, { filename: "web/app.js" });

(async () => {
  const T = ctx.__test;
  const now = new Date().toISOString();
  mockTriggers = [
    { id: "t1", source: "chat", thread: "chat:main", from: "you", action: "respond", status: "delivered", text: "短消息一行", at: now, targets: ["collie"] },
    { id: "t2", source: "worker", thread: "chat:main", from: "wk", action: "respond", status: "delivered", text: "第一行\n第二行\n第三行\n第四行超长内容\n第五行末尾", at: now, targets: ["collie"] },
  ];
  mockThreads = [{ id: "chat:main", triggers: mockTriggers, subscribers: [], last_at: now }];

  // 2a) Default inboxHTML renders collapsed, hidden buttons with aria-expanded="false".
  const initialHTML = T.inboxHTML(mockTriggers, mockThreads);
  assert.match(initialHTML, /data-inbox-id="t2"/, "row carries data-inbox-id");
  assert.match(initialHTML, /data-inbox-toggle="t2"[^>]*aria-expanded="false"[^>]*aria-controls="inbox-text-t2"[^>]*hidden/, "toggle button starts hidden & collapsed");
  assert.doesNotMatch(initialHTML, /data-inbox-id="t2"[^>]*is-expanded/, "collapsed by default");

  // 2b) syncInboxOverflow shows toggle ONLY on truncated rows (scrollHeight > clientHeight + 1).
  const rShort = buildRow("t1", { truncated: false, top: 0, bottom: 70 });
  const rLong = buildRow("t2", { truncated: true, top: 70, bottom: 180 });
  autoBody._qsa[".inbox-row"] = [rShort.row, rLong.row];
  autoBody._qsa["[data-inbox-toggle]"] = [rShort.btnEl, rLong.btnEl];
  autoBody._contains = new Set([rShort.row, rShort.textEl, rShort.btnEl, rLong.row, rLong.textEl, rLong.btnEl]);
  T.syncInboxOverflow(autoBody);

  assert.equal(rShort.row.classList.contains("is-expandable"), false, "short row must not be expandable");
  assert.equal(rShort.btnEl.hidden, true, "short row button stays hidden");
  assert.equal(rLong.row.classList.contains("is-expandable"), true, "truncated row becomes expandable");
  assert.equal(rLong.btnEl.hidden, false, "truncated row reveals toggle button");
  assert.equal(rLong.btnEl.getAttribute("aria-expanded"), "false");
  assert.equal(rLong.lblEl.textContent, "展开");

  // 2c) Clicking text area expands in place, updates aria-expanded & Set.
  autoBody.dispatch("click", { target: rLong.textEl });
  assert.equal(T.inboxExpanded.has("t2"), true, "expanded id stored in memory Set");
  assert.equal(rLong.row.classList.contains("is-expanded"), true);
  assert.equal(rLong.btnEl.getAttribute("aria-expanded"), "true");
  assert.equal(rLong.lblEl.textContent, "收起");
  assert.equal(rLong.btnEl.hidden, false, "expanded row keeps 收起 button even when scrollHeight == clientHeight");

  // 2d) Guard: dragging/selecting text on .inbox-text MUST NOT collapse it.
  selectionText = "第四行超长内容";
  selectionAnchor = rLong.textEl;
  autoBody.dispatch("click", { target: rLong.textEl });
  assert.equal(T.inboxExpanded.has("t2"), true, "non-empty text selection must prevent collapsing on text click");

  // 2e) Guard: clicking an inline <a> inside .inbox-text MUST NOT collapse it.
  selectionText = "";
  selectionAnchor = null;
  autoBody.dispatch("click", { target: rLong.linkEl });
  assert.equal(T.inboxExpanded.has("t2"), true, "clicking inline link must not toggle collapse");

  // 2f) Background refresh (new trigger arrives) preserves expanded Set, scroll anchor, and button focus.
  T.setView("auto");
  T.setAutoTab("inbox");
  autoBody.scrollTop = 120;
  rShort.row._rect = { top: -90, bottom: -20, left: 0, right: 600, width: 600, height: 70 };
  rLong.row._rect = { top: 20, bottom: 180, left: 0, right: 600, width: 600, height: 160 };
  activeElement = rLong.btnEl;
  autoBody._inboxRendered = true;
  autoBody._inboxSig = "old-sig";

  // Simulate new item inserted above t2 pushing t2 down by 90px on re-render.
  const rLongAfter = buildRow("t2", { truncated: true, expanded: true, top: 110, bottom: 270 });
  const rNew = buildRow("t3", { truncated: false, top: 20, bottom: 110 });
  let htmlSetCount = 0;
  Object.defineProperty(autoBody, "innerHTML", {
    get() { return this._html || ""; },
    set(v) {
      this._html = v;
      htmlSetCount++;
      this._qsa[".inbox-row"] = [rNew.row, rLongAfter.row];
      this._qsa["[data-inbox-toggle]"] = [rNew.btnEl, rLongAfter.btnEl];
      this._contains = new Set([rNew.row, rNew.btnEl, rLongAfter.row, rLongAfter.btnEl]);
    },
  });

  mockTriggers = [
    { id: "t3", source: "chat", thread: "chat:main", from: "you", action: "respond", status: "queued", text: "最新到达的一条消息", at: now, targets: ["collie"] },
    ...mockTriggers,
  ];
  await T.loadAuto();
  assert.equal(htmlSetCount, 1, "inbox re-renders when triggers change");
  assert.match(autoBody.innerHTML, /class="row inbox-row is-expanded is-expandable" data-inbox-id="t2"/, "re-render preserves expanded state for t2");
  assert.match(autoBody.innerHTML, /data-inbox-toggle="t2"[^>]*aria-expanded="true"/, "re-render preserves aria-expanded=true");
  assert.equal(autoBody.scrollTop, 210, "scroll anchor compensates for 90px item inserted above without jumping");
  assert.equal(rLongAfter.btnEl._focused, true, "keyboard focus restored to toggle button");
  assert.equal(rLongAfter.btnEl._focusOpts?.preventScroll, true, "focus restoration must pass preventScroll: true");

  // 2g) Identical background refresh skips DOM teardown completely.
  await T.loadAuto();
  assert.equal(htmlSetCount, 1, "unchanged inbox payload must not tear down DOM");

  // 2h) Clicking toggle button collapses cleanly (even if text is selected elsewhere).
  selectionText = "some selection";
  autoBody.dispatch("click", { target: rLongAfter.btnEl });
  assert.equal(T.inboxExpanded.has("t2"), false, "explicit toggle button collapses t2");
  assert.equal(rLongAfter.row.classList.contains("is-expanded"), false);
  assert.equal(rLongAfter.btnEl.getAttribute("aria-expanded"), "false");
  assert.equal(rLongAfter.lblEl.textContent, "展开");

  console.log("OK: all inbox expand/collapse and mobile footer contract tests passed");
})().catch((err) => {
  console.error(err);
  process.exit(1);
});
`
	cmd := exec.Command(nodeBin, "-e", jsTest, root)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("inbox expand JS contract test failed: %v\n%s", err, out)
	}
}
