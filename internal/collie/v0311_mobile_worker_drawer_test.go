package collie

import (
	"os/exec"
	"path/filepath"
	"testing"
)

func TestWebV0311UnifiedMobileTabletWorkerDrawer(t *testing.T) {
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

// ─── 1. Static HTML / CSS / JS Contracts ───
// 1a) No <=1000px or >1000px gate blocking worker drawer on mobile/tablet entry points
assert.ok(!/function openWorkerFromLink[\s\S]{0,220}innerWidth\s*<=\s*1000/.test(appSrc), "openWorkerFromLink must not block <=1000px");
assert.ok(!/const handleSideWorkerClick[\s\S]{0,260}innerWidth\s*<=\s*1000/.test(appSrc), "handleSideWorkerClick must not block <=1000px");
assert.ok(!/\$\("#rail-live"\)\?\.addEventListener\("click"[\s\S]{0,260}innerWidth\s*<=\s*1000/.test(appSrc), "#rail-live click handler must not block <=1000px");
assert.ok(!/if \(h\.startsWith\("#\/w\/"\)\)[\s\S]{0,360}innerWidth\s*>\s*1000/.test(appSrc), "#/w/ route must not restrict drawer to >1000px");

// 1b) Scroll containment on scrims & drawers (prevent background scroll bleed-through)
assert.ok(/\.worker-scrim\s*\{[\s\S]*?overflow:\s*hidden;\s*overscroll-behavior:\s*contain;\s*touch-action:\s*none;/.test(cssSrc), "drawer scrims must set overflow:hidden + overscroll-behavior:contain + touch-action:none");
assert.ok(/\.side-drawer\s*\{[\s\S]*?overscroll-behavior:\s*contain;/.test(cssSrc), ".side-drawer must contain overscroll");
assert.ok(/\.wd-viewer\s*\{[^}]*overscroll-behavior:\s*contain;/.test(cssSrc), ".wd-viewer must contain overscroll");
assert.ok(/\.wd-thread\s*\{[^}]*overscroll-behavior:\s*contain;/.test(cssSrc), ".wd-thread must contain overscroll");
assert.ok(/\.wd-peek-foot\s*\{[\s\S]*?overscroll-behavior:\s*contain;\s*touch-action:\s*none;/.test(cssSrc), ".wd-peek-foot must trap scroll chaining");
assert.ok(cssSrc.includes("#v-collie.no-notes-anim .notes-scrim,"), "notebook overlay handoff must cancel .notes-scrim transition immediately to prevent double scrim");

// 1c) Mobile safe-area, right-aligned composer controls, and coarse touch targets
assert.ok(cssSrc.includes(".wd-cbar .wd-collapse-btn { margin-left: auto; }"), "mobile worker composer must keep 收起 + 发送 right-aligned when hint hides");
assert.ok(cssSrc.includes("#td-locate-root, #td-close, #wd-close, #wd-popout, #wd-thread-btn, #wd-interrupt"), "all worker drawer header actions must share coarse touch target sizing");
assert.ok(htmlSrc.includes('id="wd-popout"') && htmlSrc.includes('id="wd-handle"'), "worker drawer retains #wd-popout full-screen button and mobile #wd-handle");

// 1d) Mobile keyboard resize pins #wd-thread / #wd-log and refits wdTerm
assert.ok(appSrc.includes('".viewer, #log, #td-body, #wd-thread, #wd-log"'), "setVH must keep #wd-thread and #wd-log anchored when mobile keyboard opens");
assert.ok(appSrc.includes("wdScreenPinned") && appSrc.includes("fitWdTerm()"), "setVH must refit worker drawer terminal when mobile keyboard resizes viewport");

// ─── 2. Runtime VM Tests across Mobile (390x844) & Tablet (768x1024) ───
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
    get childElementCount() { return el.children.length || (_innerHTML ? 1 : 0); },
    hidden: false, disabled: false, checked: false, open: false, inert: false, tabIndex: 0,
    className: "", title: "", href: "",
    style: {
      transform: "", opacity: "", transition: "",
      setProperty(k, v) { styles.set(k, String(v)); },
      getPropertyValue(k) { return styles.get(k) || ""; },
      removeProperty(k) { styles.delete(k); },
    },
    dataset: {}, classList: makeClassList(), children: [], selectedOptions: [], options: [],
    scrollTop: 400, scrollHeight: 1000, clientHeight: 600, scrollWidth: 390, clientWidth: 390,
    offsetWidth: 390, offsetHeight: 40, offsetLeft: 0, offsetParent: {}, isConnected: true,
    _qs: {}, _qsa: {}, _blurCount: 0, _focusCount: 0,
    addEventListener(type, fn) { if (!listeners.has(type)) listeners.set(type, []); listeners.get(type).push(fn); },
    removeEventListener(type, fn) { const l = listeners.get(type); if (!l) return; const i = l.indexOf(fn); if (i >= 0) l.splice(i, 1); },
    dispatch(type, ev = {}) {
      const e = { type, target: el, currentTarget: el, defaultPrevented: false, preventDefault() { this.defaultPrevented = true; }, stopPropagation() {}, composedPath: () => [el], ...ev };
      for (const fn of [...(listeners.get(type) || [])]) fn(e);
      return e;
    },
    querySelector(sel) { if (el._qs[sel]) return el._qs[sel]; const c = makeEl(id + ":" + sel); el._qs[sel] = c; return c; },
    querySelectorAll(sel) { return el._qsa[sel] || []; },
    setAttribute(k, v) { attrs.set(k, String(v)); if (k === "href") el.href = String(v); if (k === "title") el.title = String(v); if (k === "class") el.className = String(v); },
    getAttribute(k) { if (k === "href" && !attrs.has(k)) return el.href; return attrs.get(k) ?? ""; },
    removeAttribute(k) { attrs.delete(k); },
    hasAttribute(k) { return attrs.has(k); },
    closest() { return null; }, contains(node) { return node === el || el.children.includes(node); },
    append() {}, appendChild(c) { c.parentNode = el; el.children.push(c); return c; },
    remove() { if (el.parentNode) { const i = el.parentNode.children.indexOf(el); if (i >= 0) el.parentNode.children.splice(i, 1); } },
    after() {},
    focus() { el._focusCount++; doc.activeElement = el; },
    blur() { el._blurCount++; if (doc.activeElement === el) doc.activeElement = doc.body; },
    click() {}, scrollIntoView() {},
    showModal() { el.open = true; }, close() { el.open = false; },
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
getEl("#super-search").hidden = true;
getEl("#collie-menu").hidden = true;
getEl("#file-view").hidden = true;
getEl("#thread-drawer").hidden = true;
getEl("#worker-drawer").hidden = true;
getEl("#worker-scrim").hidden = true;
getEl("#worker-drawer").contains = (n) => n === getEl("#worker-drawer") || n === getEl("#wd-input");

const docListeners = new Map();
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
  querySelectorAll: (sel) => {
    if (sel.includes("#wd-thread")) return [getEl("#wd-thread")];
    return [];
  },
  addEventListener(type, fn) { if (!docListeners.has(type)) docListeners.set(type, []); docListeners.get(type).push(fn); },
  removeEventListener() {},
  dispatch(type, ev = {}) {
    const e = { type, target: makeEl("doc-target"), defaultPrevented: false, preventDefault() { this.defaultPrevented = true; }, stopPropagation() {}, composedPath: () => [], ...ev };
    for (const fn of [...(docListeners.get(type) || [])]) fn(e);
    return e;
  },
};
doc.activeElement = doc.body;
const vvListeners = new Map();
const visualViewport = {
  height: 844,
  offsetTop: 0,
  addEventListener(t, fn) { if (!vvListeners.has(t)) vvListeners.set(t, []); vvListeners.get(t).push(fn); },
  emit(t) { for (const fn of vvListeners.get(t) || []) fn(); },
};
const ctx = {
  console, Math, Date, JSON, Set, Map, RegExp, Error, Promise, URL, URLSearchParams, Object, Array, Number, String, Boolean,
  setTimeout: () => 1, clearTimeout() {}, setInterval: () => 1, clearInterval() {}, queueMicrotask,
  requestAnimationFrame: (fn) => { fn(Date.now()); return 1; },
  cancelAnimationFrame() {},
  document: doc,
  location: { hash: "#/", origin: "http://127.0.0.1:7788", href: "http://127.0.0.1:7788/#/", pathname: "/", search: "" },
  history: { state: null, pushState() {}, replaceState(s, _, url) { if (typeof url === "string" && url.startsWith("#")) ctx.location.hash = url; }, back() {} },
  navigator: { platform: "iPhone", clipboard: { writeText: async () => {} } },
  isSecureContext: true, innerWidth: 390, innerHeight: 844,
  visualViewport,
  CSS: { escape: (s) => String(s), supports: () => true },
  HTMLDialogElement: { prototype: {} },
  Blob, Uint8Array, TextEncoder, TextDecoder, atob, AbortController,
  crypto: { randomUUID: () => "v0311-uuid" },
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
    matches: (q.includes("max-width: 760px") && ctx.innerWidth <= 760)
      || (q.includes("max-width: 1180px") && ctx.innerWidth <= 1180),
    addEventListener() {},
  }),
  getComputedStyle: (el) => ({ getPropertyValue: (k) => el?.style?.getPropertyValue?.(k) || "" }),
  getSelection: () => ({ isCollapsed: true, toString: () => "" }),
  addEventListener() {}, removeEventListener() {}, dispatchEvent() {},
  fetch: async () => ({ ok: true, status: 200, json: async () => ({ ok: true, messages: [] }) }),
};
ctx.window = ctx;
ctx.globalThis = ctx;

const marker = "  boot();\n})();";
const instrumented = appSrc.replace(
  marker,
  "  globalThis.__test = { getView: () => view, setView: (v) => { view = v; }, setSup: (s) => { sup = normSup(s); }, setState: (s) => { state = s; }, setHist: (h) => { hist = h; }, setNotebookData: (d) => { notebookData = d; }, setSidePaneOverlay: (v) => { sidePaneOverlay = v; }, getActiveWorkerKey: () => activeWorkerKey, getActiveThreadRoot: () => activeThreadRoot, getWdHistId: () => wdHistId, getWdPaneId: () => wdPaneId, route, openWorkerDrawer, openWorkerFromAnywhere, closeWorkerDrawer, openThreadDrawer, closeThreadDrawer, renderMemCrewHTML, syncNotebookVisibility, setVH };\n})();"
);
vm.runInNewContext(instrumented, ctx, { filename: "web/app.js" });
const T = ctx.__test;

T.setState({
  agents: [
    { pane_id: "p-live-1", name: "fix-alpha", label: "修分页", kind: "codex", status: "working", is_agent: true, cwd: "/tmp/shop" },
    { pane_id: "p-live-2", name: "fix-beta", label: "写日志", kind: "claude", status: "blocked", is_agent: true, cwd: "/tmp/web" },
  ],
  machines: [],
  shells: [],
});
T.setHist([
  { id: "h-ended-1", name: "done-worker", kind: "claude", session: "s-1", cwd: "/tmp/shop", ended: "2026-11-28T03:00:00Z" },
]);
T.setSup({
  router: { status: "idle", alive: true },
  workers: [
    { name: "fix-alpha", label: "修分页", pane_id: "p-live-1", status: "working", task: "修分页测试", ended: false, created_at: "2026-11-28T02:00:00Z" },
    { name: "fix-beta", label: "写日志", pane_id: "p-live-2", status: "blocked", task: "写发布日志", ended: false, created_at: "2026-11-28T02:10:00Z" },
    { name: "done-worker", label: "归档任务", pane_id: "", status: "done", task: "清理历史数据", last_report: "已清理完成", ended: true, created_at: "2026-11-28T01:00:00Z" },
  ],
  entries: [
    { id: "e1", role: "user", text: "帮忙修分页", at: "2026-11-28T02:00:00Z" },
    { id: "e2", role: "collie", text: "已派 fix-alpha 跟进", at: "2026-11-28T02:01:00Z", re: { entry: "e1", who: "你", text: "帮忙修分页" } },
  ],
});

// 2a) Mobile (390x844) on #/ (sidebar home, view=null): clicking #live-list worker hides sidebar (show-main -> #/collie) and opens bottom #worker-drawer
ctx.innerWidth = 390; ctx.innerHeight = 844;
ctx.location.hash = "#/";
T.route();
assert.equal(T.getView(), null);
assert.equal(doc.body.classList.contains("show-main"), false, "mobile #/ starts on sidebar");

const sideItem = makeEl("side-w1", "A");
sideItem.dataset.sideWorker = "fix-alpha";
sideItem.dataset.paneId = "p-live-1";
sideItem.closest = (sel) => (sel === "a.item[data-side-worker]" ? sideItem : null);
const sideEv = getEl("#live-list").dispatch("click", { target: sideItem, button: 0 });
assert.equal(sideEv.defaultPrevented, true, "mobile sidebar worker click prevents full-screen #/p/ jump");
assert.equal(T.getView(), "collie", "mobile sidebar worker click transitions cleanly to #/collie");
assert.equal(doc.body.classList.contains("show-main"), true, "mobile sidebar hides before drawer shows");
assert.equal(getEl("#app").classList.contains("has-worker"), true);
assert.equal(getEl("#worker-drawer").hidden, false);
assert.equal(getEl("#worker-scrim").hidden, false);
assert.equal(T.getActiveWorkerKey(), "fix-alpha");
assert.equal(getEl("#wd-popout").hidden, false, "#wd-popout remains visible for explicit full-screen escape");
assert.equal(getEl("#wd-popout").href, "#/p/p-live-1");

// Switching worker via #attention on mobile switches in-place cleanly
const attnItem = makeEl("attn-w2", "A");
attnItem.dataset.sideWorker = "fix-beta";
attnItem.dataset.paneId = "p-live-2";
attnItem.closest = (sel) => (sel === "a.item[data-side-worker]" ? attnItem : null);
getEl("#attention").dispatch("click", { target: attnItem, button: 0 });
assert.equal(T.getActiveWorkerKey(), "fix-beta");
assert.equal(getEl("#wd-title").textContent, "写日志");

// Expanding 直接插话 + keyboard resize via visualViewport keeps #wd-thread pinned & closing blurs #wd-input
getEl("#wd-interject-btn").dispatch("click");
assert.equal(getEl("#wd-composer").hidden, false);
assert.equal(doc.activeElement, getEl("#wd-input"));
const wdThread = getEl("#wd-thread");
wdThread.scrollHeight = 1400; wdThread.clientHeight = 500; wdThread.scrollTop = 890;
visualViewport.height = 500;
visualViewport.emit("resize");
assert.equal(wdThread.scrollTop, 1400, "mobile keyboard shrink keeps #wd-thread pinned to bottom");
const blursBefore = getEl("#wd-input")._blurCount;
T.closeWorkerDrawer({ immediate: true });
assert.equal(getEl("#wd-input")._blurCount, blursBefore + 1, "closing worker drawer blurs focused #wd-input before aria-hidden");
assert.equal(getEl("#app").classList.contains("has-worker"), false);

// 2b) Mobile (390x844): chat links (prose backtick, event, need, ask-head) open #worker-drawer
const chatLink = makeEl("chat-w", "A");
chatLink.dataset.openWorker = "fix-alpha";
chatLink.href = "#/p/p-live-1";
chatLink.closest = (sel) => (sel.includes("data-open-worker") ? chatLink : null);
const chatEv = getEl("#super-thread").dispatch("click", { target: chatLink, button: 0 });
assert.equal(chatEv.defaultPrevented, true, "mobile chat worker link opens drawer");
assert.equal(getEl("#app").classList.contains("has-worker"), true);
assert.equal(T.getActiveWorkerKey(), "fix-alpha");
T.closeWorkerDrawer({ immediate: true });

// 2c) Mobile (390x844): opening worker from 小本本 overlay clears #v-collie.has-notes immediately (zero double scrim)
T.setNotebookData({ date: "2026-11-28", tasks: [{ id: "t1", title: "分页任务", worker: "fix-alpha", tone: "live" }], artifacts: [], timers: [] });
T.setSidePaneOverlay("notes");
T.syncNotebookVisibility({ immediate: true });
assert.equal(getEl("#v-collie").classList.contains("has-notes"), true);
const npTask = makeEl("np-t", "BUTTON");
npTask.dataset.npTask = "t1";
npTask.dataset.npWorker = "fix-alpha";
npTask.dataset.npTitle = "分页任务";
npTask.closest = (sel) => (sel === "[data-np-task]" ? npTask : null);
let sawNoNotesAnim = false;
const origColAdd = getEl("#v-collie").classList.add;
getEl("#v-collie").classList.add = (...cs) => {
  if (cs.includes("no-notes-anim")) sawNoNotesAnim = true;
  return origColAdd(...cs);
};
getEl("#notes-pane").dispatch("click", { target: npTask, button: 0 });
getEl("#v-collie").classList.add = origColAdd;
assert.equal(getEl("#v-collie").classList.contains("has-notes"), false, "notebook scrim clears immediately when handing off to worker drawer");
assert.equal(sawNoNotesAnim, true, "notebook handoff applies no-notes-anim to suppress scrim fade overlap");
assert.equal(getEl("#app").classList.contains("has-worker"), true);
T.closeWorkerDrawer({ immediate: true });

// 2d) Mobile (390x844) & Tablet (768x1024): Memory .mem-crew renders data-open-worker/data-pane-id/data-hist-id and opens drawer over #/auto
const crewHtml = T.renderMemCrewHTML("跟进 fix-alpha 和 done-worker");
assert.ok(crewHtml.includes('data-open-worker="fix-alpha"') && crewHtml.includes('data-pane-id="p-live-1"'), ".mem-crew marks live worker with data-open-worker and data-pane-id");
assert.ok(crewHtml.includes('data-open-worker="done-worker"') && crewHtml.includes('data-hist-id="h-ended-1"'), ".mem-crew marks ended worker with data-open-worker and data-hist-id");

for (const w of [390, 768]) {
  ctx.innerWidth = w;
  ctx.location.hash = "#/auto";
  T.route();
  assert.equal(T.getView(), "auto");

  // Live worker from Memory .mem-crew opens drawer while staying on #/auto
  const memLive = makeEl("mem-live", "A");
  memLive.dataset.openWorker = "fix-alpha";
  memLive.dataset.paneId = "p-live-1";
  memLive.href = "#/p/p-live-1";
  memLive.closest = (sel) => (sel.includes(".mem-crew a") ? memLive : null);
  const mlEv = getEl("#auto-body").dispatch("click", { target: memLive, button: 0 });
  assert.equal(mlEv.defaultPrevented, true, "memory live .mem-crew opens drawer at width=" + w);
  assert.equal(T.getView(), "auto", "opening memory worker drawer preserves #/auto context at width=" + w);
  assert.equal(getEl("#app").classList.contains("has-worker"), true);
  assert.equal(T.getWdPaneId(), "p-live-1");

  // Ended worker from Memory .mem-crew switches in-place with histId and read-only footer
  const memEnded = makeEl("mem-ended", "A");
  memEnded.dataset.openWorker = "done-worker";
  memEnded.dataset.histId = "h-ended-1";
  memEnded.href = "#/h/h-ended-1";
  memEnded.closest = (sel) => (sel.includes(".mem-crew a") ? memEnded : null);
  const meEv = getEl("#auto-body").dispatch("click", { target: memEnded, button: 0 });
  assert.equal(meEv.defaultPrevented, true, "memory ended .mem-crew opens drawer at width=" + w);
  assert.equal(T.getActiveWorkerKey(), "done-worker");
  assert.equal(T.getWdHistId(), "h-ended-1");
  assert.equal(getEl("#wd-popout").href, "#/h/h-ended-1");
  assert.equal(getEl("#wd-interject-btn").hidden, true, "ended worker hides interject button");

  // Modifier click is NOT intercepted
  const modEv = getEl("#auto-body").dispatch("click", { target: memEnded, button: 0, metaKey: true });
  assert.equal(modEv.defaultPrevented, false, "Cmd+Click leaves native link behavior alone");

  // Clicking #wd-thread-btn while drawer is open over #/auto cleanly routes to #/collie + opens #thread-drawer
  getEl("#wd-thread-btn").dataset.wdThread = "e1";
  getEl("#wd-thread-btn").dispatch("click");
  assert.equal(T.getView(), "collie", "#wd-thread-btn from #/auto transitions to #/collie");
  assert.equal(T.getActiveThreadRoot(), "e1", "#wd-thread-btn opens linked thread drawer");
  T.closeThreadDrawer({ immediate: true });
}

// 2e) Tablet (768x1024) #/w/ deep route & #rail-live both open #worker-drawer
ctx.innerWidth = 768; ctx.innerHeight = 1024;
ctx.location.hash = "#/w/fix-alpha";
T.route();
assert.equal(T.getView(), "collie");
assert.equal(ctx.location.hash, "#/collie");
assert.equal(getEl("#app").classList.contains("has-worker"), true, "#/w/ route opens worker drawer on tablet");
assert.equal(T.getActiveWorkerKey(), "fix-alpha");
T.closeWorkerDrawer({ immediate: true });

const railTarget = makeEl("rail-w", "A");
railTarget.dataset.railWorker = "fix-beta";
railTarget.dataset.railPane = "p-live-2";
railTarget.closest = (sel) => (sel === "a.sr-agent[data-rail-pane]" ? railTarget : null);
const railEv = getEl("#rail-live").dispatch("click", { target: railTarget, button: 0 });
assert.equal(railEv.defaultPrevented, true, "#rail-live opens worker drawer even at <=1000px");
assert.equal(T.getActiveWorkerKey(), "fix-beta");

// Clicking #wd-popout closes drawer immediately and allows full-screen navigation
const popEv = getEl("#wd-popout").dispatch("click", { button: 0 });
assert.equal(popEv.defaultPrevented, false, "#wd-popout lets browser navigate to full-screen pane/history");
assert.equal(getEl("#app").classList.contains("has-worker"), false, "#wd-popout closes drawer immediately");

console.log("ALL_V0311_MOBILE_TABLET_WORKER_DRAWER_TESTS_OK");
`
	cmd := exec.Command("node", "-e", script)
	cmd.Dir = filepath.Join("..", "..")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("v0.3.11 mobile/tablet worker drawer test failed: %v\n%s", err, out)
	}
}
