package collie

import (
	"os/exec"
	"path/filepath"
	"testing"
)

func TestWebUnifiedDrawerSearchGlideAndFixes(t *testing.T) {
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
assert.ok(cssSrc.includes("grid-template-columns: var(--side-w) minmax(0, 1fr) var(--drawer-w, 432px);"), "desktop >=1181px docks unified right drawer in grid column 3");

// 1b) Silky 240ms GPU glide staging (#t12615) — zero grid-template-columns animation
assert.ok(cssSrc.includes(".app.has-drawer.is-drawer-opening"), "desktop drawer open/widen uses GPU staging state");
assert.ok(cssSrc.includes("#v-collie.has-drawer-col-cap"), "narrow desktop open caps column width once on frame 0 for 0px handoff jump");
assert.ok(appSrc.includes("function easeOutSilk(") && appSrc.includes("function runDesktopDrawerGlide("), "JS lockstep cubic-bezier(0.22, 1, 0.36, 1) GPU glide engine must exist");

// 1c) Plan A in-card two-tab switcher inside #notes-pane (#t12590)
assert.ok(!htmlSrc.includes('id="collie-threads-btn"'), "Plan A drops duplicate top-bar 话题线 button");
assert.ok(htmlSrc.includes('id="np-tabs"') && htmlSrc.includes('id="np-tab-notes"') && htmlSrc.includes('id="np-tab-threads"') && htmlSrc.includes('id="np-tab-ink"'), "Plan A in-card 事项/话题 switcher markup must exist");
assert.ok(cssSrc.includes("@keyframes np-mode-enter"), "in-card tab switch uses restrained 140ms compositor enter");

// 1d) Worker drawer default read-only peek bar without permanent composer (#t12601)
assert.ok(htmlSrc.includes('id="wd-peek-foot"') && htmlSrc.includes('id="wd-interject-btn"') && htmlSrc.includes('id="wd-composer"') && htmlSrc.includes('id="wd-composer-collapse"'), "worker drawer must expose calm peek bar with on-demand 直接插话 composer");

// 1e) Raycast/Linear centered command search outside #v-collie (escaping contain: strict) (#t12579)
const vCollieCloseIdx = htmlSrc.indexOf("<!-- ── detail:");
const superSearchIdx = htmlSrc.indexOf('id="super-search"');
assert.ok(superSearchIdx > vCollieCloseIdx, "#super-search must sit outside #v-collie so contain: strict never traps position: fixed");
assert.ok(htmlSrc.includes('class="ss-card"') && htmlSrc.includes('class="ss-foot"'), "search overlay uses unified rounded card + footer kbd hints");
assert.ok(!/\.ss-hit\.active\s*\{[^}]*border-left:\s*3px/.test(cssSrc), "search active row must NOT use harsh thick dark left vertical bar");

// 1f) Message hover action bar hit-gap & stacking fix (#t12640)
assert.ok(cssSrc.includes("#v-collie .say:hover, #v-collie .mine:hover,\n#v-collie .say:focus-within, #v-collie .mine:focus-within { z-index: 4; }"), "hovered/focused message row must elevate z-index above next sibling stacking context");
assert.ok(cssSrc.includes("#v-collie .mine .msg-acts { justify-content: flex-end; height: 26px; padding-top: 2px; margin: 0 -6px -26px 0; }"), ".mine .msg-acts must replace margin-top gap with padding-top for seamless hover bridge");

// 1g) Desktop left sidebar collapse/expand (>1000px) + hide duplicate #crew when sidebar expanded + ResizeObserver scroll anchor lock (#t12615, #t12693, #t12699, #t12702)
assert.ok(htmlSrc.includes('id="side-collapse-btn"') && htmlSrc.includes('id="side-expand-btn"'), "desktop left sidebar collapse/expand buttons must exist");
assert.ok(cssSrc.includes(".app:not(.side-collapsed) #v-collie #crew { display: none; }"), "desktop (>1000px) hides duplicate top #crew strip when left sidebar is expanded");
assert.ok(cssSrc.includes(".app > .main { grid-column: 2; grid-row: 1; }"), "desktop .main must stay pinned to grid column 2 so hiding .side never collapses .main into 0px column 1");
assert.ok(cssSrc.includes(".app.side-collapsed { --side-w: 0px; }") && cssSrc.includes(".app.is-side-opening"), "desktop left sidebar collapse/expand uses 0px grid staging for silky GPU glide");
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
  "  globalThis.__test = { setView: (v) => { view = v; }, setSup: (s) => { sup = normSup(s); }, setState: (s) => { state = s; }, easeOutSilk, setSideCollapsed, openWorkerDrawer, closeWorkerDrawer, openThreadDrawer, closeThreadDrawer, showFilePane, closeFilePane, switchNotebookTab, syncNotebookVisibility };\n})();"
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

// Seamless 3-way mutual exclusion: Worker -> Thread -> File -> Close
getEl("#wd-thread-btn").dispatch("click");
assert.equal(getEl("#app").classList.contains("has-worker"), false, "switching to thread closes worker drawer");
assert.equal(getEl("#app").classList.contains("has-thread"), true, "thread drawer opens seamlessly");
assert.equal(getEl("#app").classList.contains("has-drawer"), true);

T.showFilePane();
assert.equal(getEl("#app").classList.contains("has-thread"), false, "opening file preview closes thread drawer");
assert.equal(getEl("#app").classList.contains("has-file"), true, "file preview opens seamlessly");
assert.equal(getEl("#app").classList.contains("has-drawer"), true);

T.closeFilePane({ immediate: true });
assert.equal(getEl("#app").classList.contains("has-file"), false);
assert.equal(getEl("#app").classList.contains("has-drawer"), false);

// 2c) Plan A in-card 事项 <-> 话题 tab switch updates ink & mode without hiding card
T.syncNotebookVisibility({ immediate: true });
T.switchNotebookTab("threads");
assert.equal(getEl("#notes-pane").getAttribute("data-side-mode"), "threads");
assert.ok(getEl("#np-tab-ink").style.transform.includes("translate3d(40px"), "sliding tab ink moves smoothly to 话题 tab");
assert.equal(getEl("#np-threads-body").classList.contains("is-mode-entering"), true, "explicit tab switch plays 140ms restrained enter");

// 2d) Centered command search backdrop mousedown & Escape close
getEl("#super-search-btn").dispatch("click");
assert.equal(getEl("#super-search").hidden, false);
getEl("#super-search").dispatch("mousedown", { target: getEl("#super-search") });
assert.equal(getEl("#super-search").hidden, true, "clicking backdrop closes centered search overlay");

// 2e) Desktop left sidebar collapse/expand persists state & toggles .side-collapsed cleanly (#t12615, #t12693)
T.setSideCollapsed(true);
assert.equal(getEl("#app").classList.contains("side-collapsed"), true, "collapsing sidebar sets .side-collapsed on #app");
assert.equal(doc.body.classList.contains("side-collapsed"), true, "collapsing sidebar syncs .side-collapsed on body");
assert.equal(storage.get("collie.sideCollapsed"), "true");
T.setSideCollapsed(false);
assert.equal(getEl("#app").classList.contains("side-collapsed"), false, "expanding sidebar removes .side-collapsed");
assert.equal(storage.get("collie.sideCollapsed"), "false");

// Clicking a #crew chip opens worker drawer on #/collie (#t12693)
const chipTarget = makeEl("chip");
chipTarget.dataset.worker = "fix-drawer";
chipTarget.closest = (sel) => (sel === "a.chip[data-worker]" ? chipTarget : null);
getEl("#crew").dispatch("click", { target: chipTarget, button: 0 });
assert.equal(getEl("#app").classList.contains("has-worker"), true, "clicking #crew chip opens right 3-in-1 worker drawer");
T.closeWorkerDrawer({ immediate: true });

console.log("OK: all unified drawer, Plan A tabs, centered search, GPU glide & hover contracts passed");
`
	cmd := exec.Command("node", "-e", script)
	cmd.Dir = filepath.Join("..", "..")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("unified drawer contracts failed: %v\n%s", err, string(out))
	}
}
