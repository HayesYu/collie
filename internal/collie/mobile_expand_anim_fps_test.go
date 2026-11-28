package collie

import (
	"os/exec"
	"path/filepath"
	"testing"
)

func TestWebMobileExpandAnimFPSContracts(t *testing.T) {
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

// ─── 1. Static CSS / JS / Demo Sync Anti-Regression Contracts ───
for (const [name, src] of [["web/app.css", cssSrc], ["docs/demo/app.css", demoCssSrc]]) {
  // 1a) High-cardinality & drawer-header :has() selectors replaced with direct static/JS classes
  for (const forbidden of [
    ".wd-top:has(",
    ".td-head:has(",
    ".sd-head:has(",
    ".fv-head:has(",
    ".wd-head:has(",
    "a:is(.fpath, .url, .worker-link):has(",
    ".md-table td:has(",
    ".md-table-card td:has(",
    ".md-table-card[data-cols=",
    ".md-code:has(",
    ".md-code.is-diff .diff-line:has(",
    ".md-table-card:has(",
  ]) {
    if (forbidden === ".md-table-card[data-cols=") {
      assert.ok(!src.includes('.md-table-card[data-cols="2"]:has(') && !src.includes('.md-table-card[data-cols="3"]:has('), name + " must not use :has() on .md-table-card[data-cols]");
    } else {
      assert.ok(!src.includes(forbidden), name + " must not use high-cardinality selector " + forbidden);
    }
  }
  for (const required of [
    ".wd-top:not(.has-summary):not(.has-bar) .wd-head",
    ".wd-top.has-bar .wd-summary",
    ".td-head.has-workers",
    ".wd-head.has-back",
    "a:is(.fpath, .url, .worker-link).has-code-link",
    ".md-table td.has-code",
    '.md-table-card[data-cols="2"].is-last-num',
    ".md-code.has-tag pre",
    ".md-code.is-diff .diff-line.has-sig",
    ".md-code.is-copy-done .md-float-acts",
    ".md-table-card.is-copy-done .md-float-acts",
  ]) {
    assert.ok(src.includes(required), name + " must define static class replacement " + required);
  }

  // 1b) No per-frame ::details-content block-size layout transition in chat/worker lists
  assert.ok(!src.includes("details.md-fold::details-content"), name + " must not animate details.md-fold::details-content block-size");
  assert.ok(!src.includes(".tool.subagent::details-content"), name + " must not animate .tool.subagent::details-content block-size");
  assert.ok(src.includes("@keyframes fold-reveal") && src.includes("details.md-fold[open] > .md-fold-body") && src.includes(".tool.subagent[open] > .sub-body"), name + " must use GPU fold-reveal on details expand");
  assert.ok(!/details\.md-fold\s*>\s*summary::before\s*\{[^}]*background-color\s+\./.test(src), name + " details.md-fold chevron must not animate paint-triggering background-color");
  assert.ok(!/\.tool\.subagent\s*>\s*summary\.sub-sum::before\s*\{[^}]*background-color\s+\./.test(src), name + " .tool.subagent chevron must not animate paint-triggering background-color");

  // 1c) Worker first-prompt expand: contain: layout style + GPU reveal, no max-height transition
  assert.ok(src.includes("@keyframes wd-prompt-reveal") && src.includes(".msg.user.is-collapsible.is-just-expanded .wd-prompt-body"), name + " worker prompt expand must use GPU wd-prompt-reveal");
  const promptBlockIdx = src.indexOf(".msg.user.is-collapsible {");
  assert.ok(promptBlockIdx > 0 && src.slice(promptBlockIdx, promptBlockIdx + 240).includes("contain: layout style"), name + " collapsible prompt bubble must isolate layout & style");

  // 1d) Mobile sheet open animations & warm reopen content-visibility: hidden cache
  assert.ok(src.includes("[hidden]:not(.super-search, .ss-panel, .side-drawer) { display: none !important; }"), name + " global [hidden] rule must exempt .side-drawer for mobile content-visibility cache");
  assert.ok(src.includes("@keyframes sd-sheet-in-y") && src.includes(".side-drawer.is-sheet-opening") && src.includes(".drawer-scrim.is-scrim-opening"), name + " mobile drawers must animate open via GPU keyframes without offsetWidth reflow");
  assert.ok(src.includes("@supports (content-visibility: hidden)") && src.includes("content-visibility: hidden;"), name + " hidden mobile drawers must preserve layout/style cache via content-visibility: hidden");

  // 1e) Topic drawer focus pulse uses GPU ::after opacity fade, not background repaint
  assert.ok(src.includes(".td-node.is-focused::after") && src.includes("@keyframes td-focus-fade") && !src.includes("@keyframes td-focus {"), name + " .td-node.is-focused must fade ::after opacity instead of repainting background");

  // 1f) Worker overflow popover isolates layout & disables ::backdrop & gates anchor-name while sliding
  assert.ok(src.includes(".wd-more-pop[popover]::backdrop { display: none; }"), name + " #wd-more-menu must hide ::backdrop to avoid per-frame backdrop recalc");
  assert.ok(src.includes(".workerpane:not(.is-sheet-opening):not(.is-dragging):not(.is-closing) .wd-more-btn { anchor-name: --wd-more-anchor; }"), name + " anchor-name must be gated while worker sheet is moving");

  // 1g) Mobile notebook flattens .np-fold grids & kills remaining mobile backdrop-filter
  assert.ok(src.includes(".notepane .np-fold,\n  .notepane.is-expanded .np-fold { display: block;"), name + " mobile/tablet notebook must flatten .np-fold to display: block");
  assert.ok(src.includes(".rv-head, .set-save-state { backdrop-filter: none; -webkit-backdrop-filter: none; }"), name + " mobile must disable backdrop-filter on .rv-head and .set-save-state");

  // 1h) Never add content-visibility: auto to #super-thread (scroll anchor stability red line)
  assert.ok(!/#super-thread[^{]*\{[^}]*content-visibility:\s*auto/.test(src), name + " must never apply content-visibility: auto to #super-thread");

  // 1i) Desktop / iPad landscape docked drawer zero-twitch contract (#t15047):
  // .md-table-vp.is-scrollable MUST NOT alter column geometry (max-width / white-space / width / padding),
  // otherwise ResizeObserver(tableRO) enters a 60Hz +-19px vertical oscillation loop when main chat narrows.
  const scrollRules = [...src.matchAll(/\.md-table-vp\.is-scrollable[^{]*\{([^}]*)\}/g)];
  assert.ok(scrollRules.length >= 1, name + " must define .md-table-vp.is-scrollable sticky rule");
  for (const m of scrollRules) {
    assert.ok(!/\b(?:max-width|min-width|width|white-space|padding|font-size)\s*:/.test(m[1]), name + " .md-table-vp.is-scrollable must stay geometry-invariant to prevent 60Hz tableRO oscillation: " + m[0]);
  }
  assert.ok(src.includes('.md-table-card:not([data-cols="1"]):not([data-cols="2"]):not([data-cols="3"]) thead th:first-child'), name + " 4+ col tables must apply unconditional first-col max-width");
}

for (const [name, src] of [["web/app.js", appSrc], ["docs/demo/app.js", demoAppSrc]]) {
  assert.ok(src.includes("function playMobileSheetOpen(") && src.includes("function clearMobileSheetOpen("), name + " must use playMobileSheetOpen helper");
  assert.ok(!src.includes("if (!mobileSwap) void pane.offsetWidth;") && !src.includes("if (!mobileSwap) void tdDrawer.offsetWidth;") && !src.includes("if (!mobileSwap) void wdDrawer.offsetWidth;"), name + " drawer open paths must never force offsetWidth reflow");
  assert.ok(src.includes("syncTablesBatch") && src.includes("syncCodeBlocksBatch"), name + " rich table/code hydration must batch DOM reads before writes");
  assert.ok(src.includes("_renderedCacheKey"), name + " worker drawer warm reopen must reuse rendered transcript DOM via _renderedCacheKey");
  assert.ok(src.includes("scheduleAfterWdSlide") && src.includes("workerMsgsRawSig") && src.includes("bindLazyToolRuns") && src.includes("groupThreadMsgs"), name + " worker drawer open must defer mid-slide DOM dumps, short-circuit identical transcripts, and lazy-hydrate collapsed toolruns");
  assert.ok(src.includes("is-just-expanded"), name + " prompt/code expand must toggle transient .is-just-expanded class");
  // Desktop / iPad landscape docked drawer zero-twitch JS contracts (#t15047)
  assert.ok(src.includes("_roLastW") && src.includes("flushPostGlideRichSync") && src.includes("syncCollieResizeBaseline"), name + " rich ROs must be width-only, glide-deferred, and sync resize baseline on glide end");
  assert.ok(src.includes("lockSnap: passedSnap = null") && src.includes("let preStageCls = \"\";"), name + " runDesktopDrawerGlide must accept pre-mutation lockSnap and stage grid class before layout reads");
  assert.ok(src.includes("_pendingHtmlCardHeights") && src.includes("function applyHtmlCardHeight("), name + " HTML card height updates must defer during layout glide and preserve bottom stick");
}

// ─── 2. Runtime VM Verification: Render Classes, 0 Forced Reflow, Mobile Progressive Slice & Warm Reuse ───
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
let reflowReads = [];
let trackReflow = false;
function makeEl(id = "", tagName = "DIV") {
  const listeners = new Map(), attrs = new Map(), styles = new Map();
  let _html = "", _text = "";
  const el = {
    id, tagName, localName: tagName.toLowerCase(), nodeType: 1,
    hidden: false, inert: false, disabled: false, value: "", tabIndex: 0,
    scrollTop: 0, scrollLeft: 0, clientWidth: 390, clientHeight: 720, scrollWidth: 390, scrollHeight: 720,
    dataset: {}, children: [], parentElement: null,
    classList: makeClassList(),
    style: {
      setProperty: (k, v) => styles.set(k, String(v)),
      removeProperty: (k) => styles.delete(k),
      getPropertyValue: (k) => styles.get(k) || "",
      transform: "", opacity: "", width: "", willChange: "",
    },
    get offsetWidth() { if (trackReflow) reflowReads.push("offsetWidth#" + id); return 390; },
    get offsetHeight() { if (trackReflow) reflowReads.push("offsetHeight#" + id); return 60; },
    get innerHTML() { return _html; },
    set innerHTML(v) { _html = String(v); },
    get textContent() { return _text; },
    set textContent(v) { _text = String(v); },
    get childElementCount() { return el.children.length; },
    setAttribute: (k, v) => attrs.set(k, String(v)),
    getAttribute: (k) => (attrs.has(k) ? attrs.get(k) : null),
    removeAttribute: (k) => attrs.delete(k),
    hasAttribute: (k) => attrs.has(k),
    addEventListener: (t, fn) => { if (!listeners.has(t)) listeners.set(t, []); listeners.get(t).push(fn); },
    removeEventListener: (t, fn) => { const arr = listeners.get(t); if (arr) listeners.set(t, arr.filter((f) => f !== fn)); },
    querySelector: (sel) => (sel === ".in-active-thread" ? null : getEl(sel)),
    querySelectorAll: () => [],
    closest: () => null,
    contains: (n) => n === el,
    focus: () => {},
    blur: () => {},
    append: (...ns) => { for (const n of ns) if (n && typeof n === "object") { n.parentElement = el; el.children.push(n); } },
    appendChild: (n) => { if (n && typeof n === "object") { n.parentElement = el; el.children.push(n); } return n; },
    removeChild: (n) => { el.children = el.children.filter((x) => x !== n); return n; },
    getBoundingClientRect: () => ({ top: 0, left: 0, right: 390, bottom: 720, width: 390, height: 720 }),
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
  "worker-drawer", "worker-scrim", "wd-thread", "wd-title", "wd-status", "wd-bar", "wd-summary", "wd-return", "wd-gone", "wd-more-btn", "wd-more-menu",
  "notes-pane", "notes-scrim", "open-notes-btn", "np-body", "np-date", "np-tasks-list", "np-files-list", "np-timers-list",
]) {
  const el = getEl("#" + id);
  if (id === "file-view" || id === "thread-drawer" || id === "worker-drawer") el.hidden = true;
}
getEl("#notes-pane").classList.add("is-hidden");

const docListeners = new Map();
const timers = new Map();
let timerSeq = 1;
const noop = () => {};
const ctx = {
  console, setTimeout: (fn, ms = 0) => { const id = timerSeq++; timers.set(id, { fn, ms }); return id; },
  clearTimeout: (id) => { timers.delete(id); },
  setInterval: () => 0, clearInterval: noop,
  requestAnimationFrame: (fn) => { const id = timerSeq++; timers.set(id, { fn, ms: 16 }); return id; },
  cancelAnimationFrame: (id) => { timers.delete(id); },
  performance: { now: () => 1000 },
  innerWidth: 390, innerHeight: 844, devicePixelRatio: 3,
  navigator: { userAgent: "Mobile", clipboard: { writeText: async () => {} } },
  location: { hash: "#/collie", origin: "http://127.0.0.1:8080", pathname: "/", search: "" },
  history: { state: {}, replaceState: noop, pushState: noop },
  localStorage: { getItem: () => null, setItem: noop, removeItem: noop },
  sessionStorage: { getItem: () => null, setItem: noop, removeItem: noop },
  matchMedia: (q) => ({ matches: q.includes("max-width: 760px") || q.includes("no-preference"), addEventListener: noop }),
  CSS: { escape: (s) => String(s) },
  URL, URLSearchParams, Event: class { constructor(t) { this.type = t; } },
  HTMLElement: { prototype: { onbeforematch: null } },
  HTMLDialogElement: { prototype: {} },
  ResizeObserver: class { observe() {} unobserve() {} disconnect() {} },
  MutationObserver: class { observe() {} disconnect() {} },
  IntersectionObserver: class { observe() {} unobserve() {} disconnect() {} },
  EventSource: class { addEventListener() {} close() {} },
  getComputedStyle: () => ({ getPropertyValue: () => "" }),
  fetch: async () => ({ ok: true, json: async () => ({}), text: async () => "" }),
  addEventListener: noop, removeEventListener: noop, dispatchEvent: noop,
  document: {
    documentElement: makeEl("html", "HTML"),
    head: makeEl("head", "HEAD"),
    body: makeEl("body", "BODY"),
    activeElement: null,
    visibilityState: "visible",
    querySelector: (sel) => (sel.includes(".in-active-thread") ? null : getEl(sel)),
    querySelectorAll: () => [],
    createElement: (tag) => makeEl("", tag),
    addEventListener: (t, fn) => { if (!docListeners.has(t)) docListeners.set(t, []); docListeners.get(t).push(fn); },
    removeEventListener: noop,
  },
};
ctx.window = ctx;
ctx.globalThis = ctx;

const marker = "  boot();\n})();";
const expose = [
  "  globalThis.__perfTest = {",
  "    mdRender, setSup: (s) => { sup = s; }, setView: (v) => { view = v; },",
  "    openThreadDrawer, closeThreadDrawer, showFilePane, closeFilePane, syncNotebookVisibility,",
  "    setNotebookData: (d) => { notebookData = d; },",
  "    getTdLastCount: () => tdLastCount,",
  "  };",
  "})();",
].join("\n");
vm.runInNewContext(appSrc.replace(marker, expose), ctx, { filename: "web/app.js" });
const T = ctx.__perfTest;

// 2a) Static render classes emitted by mdRender()
T.setSup({ workers: [{ name: "wk-perf", status: "working", pane_id: "w1:p1" }], entries: [] });
const sampleMd = [
  "See \x60wk-perf\x60, \x60~/src/app.js\x60, and \x60https://example.com/a\x60.",
  "",
  "| Item | Code | Latency |",
  "| --- | --- | ---: |",
  "| Drawer | \x60translate3d\x60 | 16.7ms |",
  "",
  "\x60\x60\x60diff",
  "@@ -1,2 +1,2 @@",
  "-old line",
  "+new line",
  " ctx line",
  "\x60\x60\x60",
].join("\n");
const html = T.mdRender(sampleMd);
assert.ok(html.includes('class="worker-link has-code-link"'), "worker code-link emits .has-code-link");
assert.ok(html.includes('class="fpath has-code-link"'), "file path code-link emits .has-code-link");
assert.ok(html.includes('class="url has-code-link"'), "URL code-link emits .has-code-link");
assert.ok(html.includes('class="md-table-card is-last-num"'), "numeric last-column table emits .is-last-num");
assert.ok(html.includes('<td class="has-code"><code>translate3d</code></td>'), "table cell with <code> emits .has-code");
assert.ok(html.includes('class="md-code is-diff has-tag"'), "diff block emits .has-tag");
assert.ok(html.includes('class="diff-line is-del has-sig"') && html.includes('class="diff-line is-add has-sig"') && html.includes('class="diff-line has-sig"'), "diff sig lines emit .has-sig");

// 2b) Mobile drawer opens trigger ZERO offsetWidth/offsetHeight forced reflows
T.setView("collie");
const entries = [];
for (let i = 1; i <= 15; i++) {
  entries.push({
    id: "e" + i,
    role: "collie",
    at: "2026-11-28T16:00:00Z",
    text: "Message #" + i,
    re: i > 1 ? { entry: "e" + (i - 1), who: "collie", text: "Message #" + (i - 1) } : undefined,
  });
}
T.setSup({ entries, workers: [] });
reflowReads = [];
trackReflow = true;
T.showFilePane();
assert.equal(reflowReads.length, 0, "showFilePane() on mobile must trigger zero offsetWidth/offsetHeight reflows: " + reflowReads.join(","));
assert.equal(getEl("#file-view").classList.contains("is-sheet-opening"), true, "showFilePane() uses CSS keyframe .is-sheet-opening");
T.closeFilePane({ immediate: true });

reflowReads = [];
T.openThreadDrawer("e1", "e15");
assert.equal(reflowReads.length, 0, "openThreadDrawer() on mobile cold open must trigger zero offsetWidth/offsetHeight reflows: " + reflowReads.join(","));
assert.equal(getEl("#thread-drawer").classList.contains("is-sheet-opening"), true, "openThreadDrawer() uses CSS keyframe .is-sheet-opening");
assert.equal(T.getTdLastCount(), 6, "mobile cold open of 15-msg thread renders initial 6-msg slice before progressive fill");

// Flush progressive chunk timers -> mounts all 15 messages
for (let step = 0; step < 6; step++) {
  for (const [id, t] of [...timers.entries()]) {
    timers.delete(id);
    t.fn();
  }
}
assert.equal(T.getTdLastCount(), 15, "progressive post-slide chunks mount full 15-msg thread");
T.closeThreadDrawer({ immediate: true });

// 2c) Mobile notebook open triggers zero forced reflow and avoids document.body class churn
T.setNotebookData({ date: "2026-11-28", tasks: [{ id: "t1", title: "Task 1" }], artifacts: [], timers: [] });
reflowReads = [];
T.syncNotebookVisibility();
assert.equal(reflowReads.length, 0, "mobile syncNotebookVisibility() must trigger zero forced reflows: " + reflowReads.join(","));
assert.equal(ctx.document.body.classList.contains("collie-has-notes"), false, "mobile notebook must not toggle desktop-only body.collie-has-notes");
trackReflow = false;
`
	cmd := exec.Command("node", "-e", script)
	cmd.Dir = filepath.Join("..", "..")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("mobile expand anim fps test failed: %v\n%s", err, out)
	}
}
