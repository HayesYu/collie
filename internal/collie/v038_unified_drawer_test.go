package collie

import (
	"os/exec"
	"path/filepath"
	"testing"
)

func TestWebUnifiedDrawerSearchGlideAndFixes(t *testing.T) {
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
// 1a) Unified 3-in-1 right drawer shell (.side-drawer) + zero left blank void (.sd-inner { width: 100% })
for (const id of ["file-view", "thread-drawer", "worker-drawer"]) {
  assert.ok(new RegExp('id="' + id + '"').test(htmlSrc), "missing drawer #" + id);
}
assert.ok(cssSrc.includes("@property --drawer-w") && cssSrc.includes("inherits: false"), "--drawer-w must register as non-inheriting @property");
assert.ok(/\.sd-inner[^{]*\{[^}]*width:\s*100%/.test(cssSrc), ".sd-inner must use width: 100% (prevents non-inheriting --file-w left blank void)");
assert.ok(!/\.fv-inner\s*\{[^}]*width:\s*var\(--file-w/.test(cssSrc), ".fv-inner must NOT bind width to non-inheriting --file-w");
assert.ok(cssSrc.includes("--drawer-default-w: clamp(440px, 34vw, 540px);"), "all three right drawers share single --drawer-default-w golden default width");
assert.ok(cssSrc.includes("grid-template-columns: var(--side-w) minmax(0, 1fr) var(--drawer-w, var(--drawer-default-w, 460px));"), "desktop >=1181px docks unified right drawer in grid column 3 with shared default width");

// 1b) Silky 240ms GPU glide staging (#t12615) — zero grid-template-columns animation
assert.ok(cssSrc.includes(".app.has-drawer.is-drawer-opening"), "desktop drawer open/widen uses GPU staging state");
assert.ok(cssSrc.includes("#v-collie.has-drawer-col-cap"), "narrow desktop open caps column width once on frame 0 for 0px handoff jump");
assert.ok(appSrc.includes("function easeOutSilk(") && appSrc.includes("function runDesktopDrawerGlide("), "JS lockstep cubic-bezier(0.22, 1, 0.36, 1) GPU glide engine must exist");

// 1c) Minimalist sticky-note header (小本本 + date + ×) + section-1 inline slash toggle 进行中 / 话题 (#t12746)
assert.ok(!htmlSrc.includes('id="collie-threads-btn"'), "drops duplicate top-bar 话题线 button");
assert.ok(htmlSrc.includes('id="np-title">小本本</h2>') && htmlSrc.includes('id="np-date"') && !htmlSrc.includes('id="np-tab-ink"') && !htmlSrc.includes('id="np-threads-body"'), "notebook header restores minimalist 小本本 + date without top tab bar or ink slider");
assert.ok(htmlSrc.includes('class="np-slash-tabs" id="np-tabs"') && htmlSrc.includes('id="np-tab-notes"') && htmlSrc.includes('id="np-tab-threads"') && htmlSrc.includes('id="np-panel-tasks"') && htmlSrc.includes('id="np-panel-threads"') && htmlSrc.includes('id="np-threads-more"'), "section-1 inline slash switcher 进行中 / 话题 and in-place panels must exist");
assert.ok(cssSrc.includes("@keyframes np-mode-enter") && cssSrc.includes(".np-sec-panel.is-mode-entering"), "first-group panel switch uses restrained 130ms compositor enter");

// 1d) Worker drawer ultra-slim header by default + Developer Mode toggle for frameless tabs (#t12601, #t12760, #t12833)
assert.ok(htmlSrc.includes('id="wd-peek-foot"') && htmlSrc.includes('id="wd-interject-btn"') && htmlSrc.includes('id="wd-composer"') && htmlSrc.includes('id="wd-composer-collapse"'), "worker drawer must expose calm peek bar with on-demand 直接插话 composer");
assert.ok(htmlSrc.includes('class="wd-mode-tabs" id="wd-mode-seg"') && htmlSrc.includes('id="wd-bar" hidden') && htmlSrc.includes('class="wd-mode-tabs d-mode-tabs" id="mode-seg"') && htmlSrc.includes('id="wd-thread-btn"') && htmlSrc.includes('id="wd-interrupt"'), "worker drawer and standalone detail share frameless mode tabs hidden by default, with restrained header icon actions");
assert.ok(!/\.wd-summary-text\s*\{[^}]*background:\s*var\(--bg-2\)/.test(cssSrc), "worker drawer summary must NOT use heavy gray bordered card box");

// 1e) Raycast/Linear centered command search outside #v-collie (escaping contain: strict) (#t12579)
const vCollieCloseIdx = htmlSrc.indexOf("<!-- ── detail:");
const superSearchIdx = htmlSrc.indexOf('id="super-search"');
assert.ok(superSearchIdx > vCollieCloseIdx, "#super-search must sit outside #v-collie so contain: strict never traps position: fixed");
assert.ok(htmlSrc.includes('class="ss-card"') && htmlSrc.includes('class="ss-foot"'), "search overlay uses unified rounded card + footer kbd hints");
assert.ok(!/\.ss-hit\.active\s*\{[^}]*border-left:\s*3px/.test(cssSrc), "search active row must NOT use harsh thick dark left vertical bar");

// 1f) Message hover action bar hit-gap & stacking fix (#t12640)
assert.ok(cssSrc.includes("#v-collie .say:hover, #v-collie .mine:hover,\n#v-collie .say:focus-within, #v-collie .mine:focus-within { z-index: 4; }"), "hovered/focused message row must elevate z-index above next sibling stacking context");
assert.ok(cssSrc.includes("#v-collie .mine .msg-acts { justify-content: flex-end; height: 26px; padding-top: 2px; margin: 0 -6px -26px 0; }"), ".mine .msg-acts must replace margin-top gap with padding-top for seamless hover bridge");

// 1g) Desktop 54px Icon Rail, merged Collie brand toggle, flat same-plane zero-shadow transitions, unified sidebar topic-icon alignment (#t12760, #t12764, #t12772, #t12988)
assert.ok(htmlSrc.includes('id="side-toggle-btn"') && !htmlSrc.includes('id="side-collapse-btn"') && htmlSrc.includes('id="side-expand-btn"') && htmlSrc.includes('id="side-rail"') && htmlSrc.includes('id="rail-live"') && htmlSrc.includes('id="rail-peers"'), "desktop 54px icon rail and merged top-left Collie toggle controls must exist");
assert.ok(cssSrc.includes(".app:not(.side-collapsed) #v-collie #crew { display: none; }"), "desktop (>1000px) hides duplicate top #crew strip when left sidebar is expanded");
assert.ok(cssSrc.includes(".app > .main { grid-column: 2; grid-row: 1; }"), "desktop .main must stay pinned to grid column 2");
assert.ok(cssSrc.includes(".app.side-collapsed { --side-w: 54px; }") && cssSrc.includes(".app.is-side-opening { --side-w: 54px; }"), "desktop left sidebar collapses to 54px icon rail instead of 0px");
assert.ok(cssSrc.includes(':root[data-theme="dark"] .brand img') && cssSrc.includes('.sr-brand img'), "dark mode inverts 54px icon rail Collie brand icon consistently");
assert.ok(
  cssSrc.includes("--bg: #111113;") &&
  cssSrc.includes("--bg-2: #151518;") &&
  cssSrc.includes("--bg-3: #1b1b1f;") &&
  cssSrc.includes("--human-bg: #25252a;") &&
  cssSrc.includes("--human-line: #ededeb;") &&
  cssSrc.includes("--user-bubble-shadow: inset 0 0 0 1px rgba(255,255,255,.07);") &&
  cssSrc.includes("--fg-3: #858580;"),
  "dark mode uses Plan A Linear Deep Graphite palette with eye-friendly charcoal user bubbles (#t12900)"
);
assert.ok(!htmlSrc.includes('<input type="file" multiple hidden>') && htmlSrc.includes('class="file-picker-input"'), "composer file pickers stay mounted in render tree (not display:none) for desktop/PWA reliability (#t12920)");
assert.ok(!cssSrc.includes("@media (min-width: 1680px) { :root { --col: 880px; } }") && cssSrc.includes("--col: clamp(780px, calc(480px + 20.8333vw), 880px);"), "conversation column width scales continuously via clamp() with zero 1680px step jump (#t12924)");
assert.ok(!cssSrc.includes("box-shadow: 8px 0 28px") && !cssSrc.includes("box-shadow: -8px 0 28px") && !cssSrc.includes("box-shadow: 12px 0 36px") && !cssSrc.includes("box-shadow: -12px 0 36px"), "desktop sidebar & right drawer glides must stay flat on the same plane with zero drop-shadow");
assert.ok(!cssSrc.includes(".item-wrap:has(> .item.has-th:not(.has-sum)) .item-th-btn") && !cssSrc.includes(".item.has-th:not(.has-sum) .dot"), "sidebar 关联话题 icon must align identically at bottom-right across 2-line and 3-line cards");
assert.ok(appSrc.includes("handleCollieScrollResize") && appSrc.includes("new ResizeObserver(handleCollieScrollResize)"), "main chat scroller must lock bottom/anchor across viewport width resizing (#t12699)");

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
    scrollTop: 0, scrollHeight: 1000, clientHeight: 600, scrollWidth: 400, clientWidth: 400,
    offsetWidth: 300, offsetHeight: 40, offsetLeft: 0, offsetParent: {}, isConnected: true,
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
    setAttribute(k, v) { attrs.set(k, String(v)); if (k === "href") el.href = String(v); if (k === "title") el.title = String(v); },
    getAttribute(k) { return attrs.get(k) ?? ""; },
    removeAttribute(k) { attrs.delete(k); },
    hasAttribute(k) { return attrs.has(k); },
    closest() { return null; }, contains() { return false; },
    append() {}, appendChild(c) { c.parentNode = el; el.children.push(c); return c; },
    remove() { if (el.parentNode) { const i = el.parentNode.children.indexOf(el); if (i >= 0) el.parentNode.children.splice(i, 1); } },
    after() {}, focus() {}, click() {}, scrollIntoView() {},
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
getEl(".side").offsetWidth = 300;
getEl("#np-tab-notes").offsetLeft = 0; getEl("#np-tab-notes").offsetWidth = 26;
getEl("#np-tab-threads").offsetLeft = 40; getEl("#np-tab-threads").offsetWidth = 26;

const docListeners = new Map();
const storage = new Map();
const doc = {
  documentElement: makeEl("html"),
  body: makeEl("body"),
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
  Blob, Uint8Array, TextEncoder, TextDecoder, atob, AbortController,
  crypto: { randomUUID: () => "v038-uuid" },
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
  getComputedStyle: (el) => ({ getPropertyValue: (k) => el?.style?.getPropertyValue?.(k) || "" }),
  getSelection: () => ({ isCollapsed: true, toString: () => "" }),
  addEventListener() {}, removeEventListener() {}, dispatchEvent() {},
  fetch: async () => ({ ok: true, status: 200, json: async () => ({ ok: true }) }),
};
ctx.window = ctx;
ctx.globalThis = ctx;

const marker = "  boot();\n})();";
const instrumented = appSrc.replace(
  marker,
  "  globalThis.__test = { setView: (v) => { view = v; }, setCurrent: (c) => { current = c; }, setSup: (s) => { sup = normSup(s); }, setState: (s) => { state = s; }, setNotebookData: (d) => { notebookData = d; }, currentSideW, easeOutSilk, setSideCollapsed, openWorkerDrawer, closeWorkerDrawer, openThreadDrawer, closeThreadDrawer, showFilePane, closeFilePane, switchNotebookTab, syncNotebookVisibility, renderPrefs, applyMode, curMode, curWdMode, syncDevModeUI, store, prefs };\n})();"
);
vm.runInNewContext(instrumented, ctx, { filename: "web/app.js" });
const T = ctx.__test;

// 2a) easeOutSilk curve monotonicity & exact endpoints
assert.equal(T.easeOutSilk(0), 0);
assert.equal(T.easeOutSilk(1), 1);
assert.ok(T.easeOutSilk(0.25) > 0.70 && T.easeOutSilk(0.25) < 0.90, "easeOutSilk(0.25) matches cubic-bezier(0.22, 1, 0.36, 1)");
assert.ok(T.easeOutSilk(0.5) > 0.92 && T.easeOutSilk(0.5) < 0.99, "easeOutSilk(0.5) matches cubic-bezier(0.22, 1, 0.36, 1)");
assert.ok(T.easeOutSilk(0.25) < T.easeOutSilk(0.5) && T.easeOutSilk(0.5) < T.easeOutSilk(0.75) && T.easeOutSilk(0.75) < 1, "easeOutSilk is strictly increasing");

// 2b) Worker drawer opens in default read-only peek mode (no permanent composer), shows --label alias, expands composer on demand, and switches seamlessly with Thread & File drawers
T.setView("collie");
T.setState({
  agents: [{ pane_id: "p-w1", name: "fix-drawer", label: "三合一抽屉", kind: "claude", status: "working", is_agent: true, cwd: "/tmp/proj" }],
  machines: [],
});
T.setSup({
  router: { status: "idle", alive: true },
  workers: [{ name: "fix-drawer", label: "三合一抽屉", pane_id: "p-w1", status: "working", task: "落地统一抽屉", ended: false }],
  entries: [
    { id: "e1", role: "user", text: "帮忙推进三合一抽屉", at: "2026-11-27T10:00:00Z" },
    { id: "e2", role: "collie", text: "已派 fix-drawer 推进中", at: "2026-11-27T10:01:00Z", re: { entry: "e1", who: "你", text: "帮忙推进三合一抽屉" } },
  ],
});

T.openWorkerDrawer({ worker: "fix-drawer", fromThreadRoot: "e1" });
assert.equal(getEl("#app").classList.contains("has-worker"), true);
assert.equal(getEl("#app").classList.contains("has-drawer"), true);
assert.equal(getEl("#worker-drawer").hidden, false);
assert.equal(getEl("#wd-title").textContent, "三合一抽屉", "worker drawer displays v0.3.6 --label alias");
assert.equal(getEl("#wd-title").title, "三合一抽屉 (fix-drawer)", "hover title preserves full ASCII worker name");
assert.equal(getEl("#wd-peek-foot").hidden, false, "default read-only peek footer is visible");
assert.equal(getEl("#wd-composer").hidden, true, "bottom composer stays hidden by default (#t12601)");
assert.equal(getEl("#wd-interject-btn").hidden, false, "live worker exposes on-demand 直接插话 trigger");

// Click 直接插话 -> reveals composer; click 收起 -> hides composer back to peek bar
getEl("#wd-interject-btn").dispatch("click");
assert.equal(getEl("#wd-composer").hidden, false, "clicking 直接插话 reveals on-demand composer");
assert.equal(getEl("#wd-peek-foot").hidden, true);
getEl("#wd-composer-collapse").dispatch("click");
assert.equal(getEl("#wd-composer").hidden, true, "clicking 收起 collapses composer back to peek bar");
assert.equal(getEl("#wd-peek-foot").hidden, false);

// Seamless 3-way mutual exclusion & 100% identical unified desktop width: Worker -> Thread -> File -> Close (#t12811)
const workerDrawerW = getEl("#app").style.getPropertyValue("--drawer-w");
assert.ok(workerDrawerW, "worker drawer sets unified --drawer-w");
getEl("#wd-thread-btn").dispatch("click");
assert.equal(getEl("#app").classList.contains("has-worker"), false, "switching to thread closes worker drawer");
assert.equal(getEl("#app").classList.contains("has-thread"), true, "thread drawer opens seamlessly");
assert.equal(getEl("#app").classList.contains("has-drawer"), true);
assert.equal(getEl("#app").style.getPropertyValue("--drawer-w"), workerDrawerW, "thread drawer shares 100% identical default width with worker drawer");

T.showFilePane();
assert.equal(getEl("#app").classList.contains("has-thread"), false, "opening file preview closes thread drawer");
assert.equal(getEl("#app").classList.contains("has-file"), true, "file preview opens seamlessly");
assert.equal(getEl("#app").classList.contains("has-drawer"), true);
assert.equal(getEl("#app").style.getPropertyValue("--drawer-w"), workerDrawerW, "file preview shares 100% identical default width with worker & thread drawers");

T.closeFilePane({ immediate: true });
assert.equal(getEl("#app").classList.contains("has-file"), false);
assert.equal(getEl("#app").classList.contains("has-drawer"), false);

// 2c) Section-1 inline slash switch 进行中 <-> 话题 replaces ONLY section-1 panel while 今日产物 & 下一条定时 stay untouched (#t12744, #t12746)
T.setNotebookData({
  date: "2026-11-28",
  tasks: [
    { id: "t1", title: "任务 1", note: "进展 1", tone: "live" },
    { id: "t2", title: "任务 2", note: "进展 2", tone: "live" },
    { id: "t3", title: "任务 3", note: "进展 3", tone: "live" },
    { id: "t4", title: "任务 4", note: "进展 4", tone: "idle" },
  ],
  artifacts: [{ path: "~/report.md", name: "report.md", title: "实验复盘", ext: "md" }],
  timers: [{ id: "tm1", spec: "daily 09:00", text: "晨检", next_run: "2026-11-28T09:00:00Z" }],
});
T.syncNotebookVisibility({ immediate: true });
const filesBefore = getEl("#np-files-list").innerHTML;
const timersBefore = getEl("#np-timers-list").innerHTML;
assert.equal(getEl("#np-tasks-more").hidden, false, "进行中 shows +1 pill when > 3 tasks");
assert.equal(getEl("#np-threads-more").hidden, true, "话题 +N pill stays hidden in 进行中 mode");
T.switchNotebookTab("threads");
assert.equal(getEl("#notes-pane").getAttribute("data-side-mode"), "threads");
assert.equal(getEl("#np-panel-tasks").hidden, true);
assert.equal(getEl("#np-panel-threads").hidden, false);
assert.equal(getEl("#np-body").hidden, false, "#np-body stays visible so 今日产物 & 下一条定时 never disappear");
assert.equal(getEl("#np-date").hidden, false, "minimalist header date stays visible in 话题 mode");
assert.equal(getEl("#np-tasks-more").hidden, true, "进行中 +N pill hides when switched to 话题");
assert.equal(getEl("#np-panel-threads").classList.contains("is-mode-entering"), true, "switching section-1 plays restrained compositor enter");
assert.equal(getEl("#np-files-list").innerHTML, filesBefore, "今日产物 DOM stays completely untouched");
assert.equal(getEl("#np-timers-list").innerHTML, timersBefore, "下一条定时 DOM stays completely untouched");
// ArrowLeft keyboard nav on #np-tabs switches back to 进行中
getEl("#np-tabs").dispatch("keydown", { key: "ArrowLeft" });
assert.equal(getEl("#notes-pane").getAttribute("data-side-mode"), "notes");
assert.equal(getEl("#np-panel-tasks").hidden, false);
assert.equal(getEl("#np-panel-threads").hidden, true);

// 2d) Centered command search backdrop mousedown & Escape close
getEl("#super-search-btn").dispatch("click");
assert.equal(getEl("#super-search").hidden, false);
getEl("#super-search").dispatch("mousedown", { target: getEl("#super-search") });
assert.equal(getEl("#super-search").hidden, true, "clicking backdrop closes centered search overlay");

// 2e) Desktop left sidebar collapse keeps 54px icon rail (currentSideW === 54) & persists state (#t12760, #t12772)
T.setSideCollapsed(true);
assert.equal(getEl("#app").classList.contains("side-collapsed"), true, "collapsing sidebar sets .side-collapsed on #app");
assert.equal(doc.body.classList.contains("side-collapsed"), true, "collapsing sidebar syncs .side-collapsed on body");
assert.equal(T.currentSideW(), 54, "collapsed desktop sidebar keeps 54px icon rail width");
assert.equal(storage.get("collie.sideCollapsed"), "true");
T.setSideCollapsed(false);
assert.equal(getEl("#app").classList.contains("side-collapsed"), false, "expanding sidebar removes .side-collapsed");
assert.equal(storage.get("collie.sideCollapsed"), "false");

// 2f) Clicking a #crew chip, left sidebar 工作中 Collie worker, or icon-rail agent opens right 3-in-1 worker drawer (#t12693, #t12760)
const chipTarget = makeEl("chip");
chipTarget.dataset.worker = "fix-drawer";
chipTarget.closest = (sel) => (sel === "a.chip[data-worker]" ? chipTarget : null);
getEl("#crew").dispatch("click", { target: chipTarget, button: 0 });
assert.equal(getEl("#app").classList.contains("has-worker"), true, "clicking #crew chip opens right 3-in-1 worker drawer");
T.closeWorkerDrawer({ immediate: true });

const liveWorkerTarget = makeEl("side-worker");
liveWorkerTarget.dataset.sideWorker = "fix-drawer";
liveWorkerTarget.dataset.paneId = "p-w1";
liveWorkerTarget.closest = (sel) => (sel === "a.item[data-side-worker]" ? liveWorkerTarget : null);
getEl("#live-list").dispatch("click", { target: liveWorkerTarget, button: 0 });
assert.equal(getEl("#app").classList.contains("has-worker"), true, "clicking Collie worker in left sidebar 工作中 opens right 3-in-1 worker drawer");
T.closeWorkerDrawer({ immediate: true });

const railWorkerTarget = makeEl("rail-worker");
railWorkerTarget.dataset.railWorker = "fix-drawer";
railWorkerTarget.dataset.railPane = "p-w1";
railWorkerTarget.closest = (sel) => (sel === "a.sr-agent[data-rail-pane]" ? railWorkerTarget : null);
getEl("#rail-live").dispatch("click", { target: railWorkerTarget, button: 0 });
assert.equal(getEl("#app").classList.contains("has-worker"), true, "clicking agent icon in collapsed 54px icon rail opens right 3-in-1 worker drawer");
assert.equal(getEl("#notes-pane").classList.contains("is-hidden"), true, "opening worker drawer hides floating notebook");

// 2g) Smooth drawer <-> notebook handoff via top-bar 小本本 button (no immediate:true hard cut, #t12807)
assert.ok(!appSrc.includes('closeWorkerDrawer({ immediate: true });\n      if ($("#app")?.classList?.contains("has-file")) closeFilePane({ immediate: true });'), "notesBtn must not hard-cut open right drawer with immediate:true");
getEl("#open-notes-btn").dispatch("click");
assert.equal(getEl("#app").classList.contains("has-worker"), false, "clicking 小本本 closes open right drawer cleanly");
assert.equal(getEl("#notes-pane").classList.contains("is-hidden"), false, "clicking 小本本 smoothly reveals floating notebook");

// 2h) Developer Mode preference (localStorage default OFF): hides 对话/屏幕/日志 tabs across worker drawer & standalone detail, plain shell auto-screen, ON reveals frameless tabs (#t12833)
assert.equal(T.prefs.devMode(), false, "developer mode defaults to false");
T.renderPrefs();
assert.ok(getEl("#prefs-body").innerHTML.includes('data-pref="devMode"') && getEl("#prefs-body").innerHTML.includes('aria-checked="false"'), "prefs renders 开发者模式 switch off by default");
assert.ok(!getEl("#prefs-body").innerHTML.includes('data-pref="mode2"'), "default prefs hides 屏幕/日志 default-view selector while developer mode is off");

T.setState({
  agents: [{ pane_id: "p-w1", name: "fix-drawer", label: "三合一抽屉", kind: "claude", status: "working", is_agent: true, cwd: "/tmp/proj" }],
  shells: [{ pane_id: "p-sh1", name: "logs", kind: "", status: "shell", is_agent: false, cwd: "/tmp/proj" }],
  machines: [],
});
T.openWorkerDrawer({ worker: "fix-drawer", fromThreadRoot: "e1" });
assert.equal(getEl("#wd-bar").hidden, true, "worker drawer hides mode tab bar when developer mode is off");
assert.equal(getEl("#wd-mode-seg").hidden, true, "worker drawer hides 对话/屏幕/日志 tabs when developer mode is off");
assert.equal(getEl("#wd-viewer").dataset.mode, "chat", "worker drawer forces clean 对话 view when developer mode is off");
assert.equal(getEl("#wd-thread-btn").hidden, false, "关联话题 icon stays accessible in drawer header");
assert.equal(getEl("#wd-interrupt").hidden, false, "中断 icon stays accessible in drawer header");

T.setCurrent("p-w1");
T.applyMode();
assert.equal(getEl("#mode-seg").hidden, true, "standalone agent detail hides 对话/屏幕/日志 when developer mode is off");
assert.equal(T.curMode(), "chat", "standalone agent defaults to 对话 when developer mode is off");

T.setCurrent("p-sh1");
T.applyMode();
assert.equal(getEl("#mode-seg").hidden, true, "plain shell hides mode switcher when developer mode is off");
assert.equal(T.curMode(), "screen", "plain non-agent shell still auto-displays terminal screen when developer mode is off");

// Enable Developer Mode -> tabs appear immediately in both worker drawer and standalone detail + prefs shows mode2
T.store.set("devMode", true);
T.syncDevModeUI();
T.renderPrefs();
assert.equal(T.prefs.devMode(), true);
assert.ok(getEl("#prefs-body").innerHTML.includes('data-pref="devMode"') && getEl("#prefs-body").innerHTML.includes('aria-checked="true"'), "prefs reflects developer mode ON");
assert.ok(getEl("#prefs-body").innerHTML.includes('data-pref="mode2"'), "prefs reveals default-view selector when developer mode is ON");
assert.equal(getEl("#wd-bar").hidden, false, "enabling developer mode reveals worker drawer tab bar");
assert.equal(getEl("#wd-mode-seg").hidden, false, "enabling developer mode reveals worker drawer 对话/屏幕/日志 tabs");
T.setCurrent("p-w1");
T.applyMode();
assert.equal(getEl("#mode-seg").hidden, false, "enabling developer mode reveals standalone detail 对话/屏幕/日志 tabs");

// Disable Developer Mode again -> tabs hide and active views return to 对话
T.store.set("devMode", false);
T.syncDevModeUI();
assert.equal(getEl("#wd-bar").hidden, true, "disabling developer mode hides worker drawer tab bar again");
assert.equal(getEl("#mode-seg").hidden, true, "disabling developer mode hides standalone detail tabs again");
assert.equal(T.curWdMode(), "chat", "disabling developer mode returns worker drawer to 对话");
T.closeWorkerDrawer({ immediate: true });

// 2i) Clicking #super-composer .attach reliably triggers file picker (showPicker + click fallback) (#t12920)
const superAttach = getEl("#super-composer")._qs[".attach"];
const superPicker = getEl("#super-composer")._qs['input[type="file"]'];
let superClickHits = 0;
let superShowPickerHits = 0;
superPicker.click = () => { superClickHits++; };
superAttach.dispatch("click");
assert.equal(superClickHits, 1, "clicking #super-composer .attach triggers picker.click() fallback");
superPicker.showPicker = () => { superShowPickerHits++; };
superAttach.dispatch("click");
assert.equal(superShowPickerHits, 1, "clicking #super-composer .attach prefers native showPicker() when available");
superPicker.showPicker = () => { throw new Error("blocked"); };
superAttach.dispatch("click");
assert.equal(superClickHits, 2, "showPicker() error cleanly falls back to picker.click()");

console.log("OK: all v0.3.8 + v0.3.9 unified drawer, inline slash tabs, 54px rail, flat glide, alignment & developer mode contracts passed");
`
	cmd := exec.Command("node", "-e", script)
	cmd.Dir = filepath.Join("..", "..")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("unified drawer contracts failed: %v\n%s", err, string(out))
	}
}
