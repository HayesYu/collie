package collie

import (
	"os/exec"
	"path/filepath"
	"testing"
)

func TestWebMobileDrawerReturnStack(t *testing.T) {
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
const demoHtmlSrc = fs.readFileSync("docs/demo/index.html", "utf8");
const demoCssSrc = fs.readFileSync("docs/demo/app.css", "utf8");
const demoAppSrc = fs.readFileSync("docs/demo/app.js", "utf8");

// ─── 1. Static HTML / CSS / Demo Contracts ───
for (const [name, src] of [["web/index.html", htmlSrc], ["docs/demo/index.html", demoHtmlSrc]]) {
  for (const id of ["fv-return", "td-return", "wd-return"]) {
    assert.ok(src.includes('id="' + id + '"') && src.includes('id="' + id + '-label"'), name + " must declare #" + id + " and #" + id + "-label");
  }
  assert.ok(!src.includes("np-back"), name + " must keep #notes-pane free of np-back");
}
for (const [name, src] of [["web/app.css", cssSrc], ["docs/demo/app.css", demoCssSrc]]) {
  assert.ok(src.includes("@media (min-width: 1001px) {\n  .sd-back { display: none !important; }\n}"), name + " must hide .sd-back on desktop (>1000px)");
  assert.ok(src.includes("@media (max-width: 1000px) {\n  .sd-back:not([hidden]) { display: inline-flex; }\n}"), name + " must show .sd-back on <=1000px when not hidden");
  assert.ok(src.includes("@keyframes sd-drill-in") && src.includes("@keyframes sd-return-in"), name + " must define pure-GPU sd-drill-in / sd-return-in keyframes");
  const sdBackBlock = src.slice(src.indexOf(".sd-back {"), src.indexOf(".sd-title-wrap, .td-title-wrap"));
  assert.ok(!sdBackBlock.includes("backdrop-filter"), name + " mobile drawer return UI/animations must not use backdrop-filter");
  assert.ok(sdBackBlock.includes("@media (prefers-reduced-motion: no-preference)"), name + " swap animations must respect prefers-reduced-motion");
}
assert.ok(demoAppSrc.includes("DRAWER_RETURN_LABELS") && demoAppSrc.includes("popDrawerReturnStack"), "docs/demo/app.js must be synced");

// ─── 2. Runtime VM Tests (Mobile 390x844 + History + Desktop 1440x900) ───
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
      transform: "", opacity: "", transition: "", width: "",
      setProperty(k, v) { styles.set(k, String(v)); },
      getPropertyValue(k) { return styles.get(k) || ""; },
      removeProperty(k) { styles.delete(k); },
    },
    dataset: {}, classList: makeClassList(), children: [], selectedOptions: [], options: [],
    scrollTop: 0, scrollLeft: 0, scrollHeight: 1200, clientHeight: 600, scrollWidth: 390, clientWidth: 390,
    offsetWidth: 390, offsetHeight: 776, offsetLeft: 0, offsetParent: {}, isConnected: true,
    _qs: {}, _qsa: {}, _blurCount: 0, _focusCount: 0, _captured: [],
    addEventListener(type, fn) { if (!listeners.has(type)) listeners.set(type, []); listeners.get(type).push(fn); },
    removeEventListener(type, fn) { const l = listeners.get(type); if (!l) return; const i = l.indexOf(fn); if (i >= 0) l.splice(i, 1); },
    dispatch(type, ev = {}) {
      const e = {
        type, target: el, currentTarget: el, defaultPrevented: false,
        preventDefault() { this.defaultPrevented = true; },
        stopPropagation() {},
        composedPath: () => [el],
        ...ev,
      };
      for (const fn of [...(listeners.get(type) || [])]) fn(e);
      return e;
    },
    setPointerCapture(pid) { el._captured.push(pid); },
    querySelector(sel) { if (el._qs[sel]) return el._qs[sel]; const c = makeEl(id + ":" + sel); el._qs[sel] = c; return c; },
    querySelectorAll(sel) { return el._qsa[sel] || []; },
    setAttribute(k, v) { attrs.set(k, String(v)); if (k === "href") el.href = String(v); if (k === "title") el.title = String(v); if (k === "class") el.className = String(v); },
    getAttribute(k) { if (k === "href" && !attrs.has(k)) return el.href; return attrs.get(k) ?? ""; },
    removeAttribute(k) { attrs.delete(k); },
    hasAttribute(k) { return attrs.has(k); },
    closest() { return null; },
    contains(node) { return node === el || el.children.includes(node); },
    append() {}, appendChild(c) { c.parentNode = el; el.children.push(c); return c; },
    remove() {}, after() {},
    focus() { el._focusCount++; doc.activeElement = el; },
    blur() { el._blurCount++; if (doc.activeElement === el) doc.activeElement = doc.body; },
    click() { el.dispatch("click"); },
    scrollIntoView() {},
    showModal() { el.open = true; }, close() { el.open = false; },
    getBoundingClientRect() { return { top: 0, bottom: 776, left: 0, right: 390, width: 390, height: 776 }; },
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
getEl("#file-scrim").hidden = true;
getEl("#thread-drawer").hidden = true;
getEl("#thread-scrim").hidden = true;
getEl("#worker-drawer").hidden = true;
getEl("#worker-scrim").hidden = true;
getEl("#notes-pane").classList.add("is-hidden");
getEl("#fv-return").hidden = true;
getEl("#td-return").hidden = true;
getEl("#wd-return").hidden = true;

// Wire head elements & child containment for drag / focus blur checks
const fvDrawer = getEl("#file-view");
const fvHandle = getEl("#fv-handle");
const fvHead = fvDrawer.querySelector(".fv-head");
const fvBack = getEl("#fv-return");
const fvClose = makeEl("#fv-close", "BUTTON");
fvClose.closest = (sel) => (sel.includes("[data-fv-close]") || sel.includes("button:not(.sd-handle)") ? fvClose : null);
fvDrawer.contains = (n) => [fvDrawer, fvHandle, fvHead, fvBack, fvClose].includes(n);
fvHandle.closest = (sel) => (sel.includes("#fv-handle") ? fvHandle : null);

const tdDrawer = getEl("#thread-drawer");
const tdHandle = getEl("#td-handle");
const tdHead = tdDrawer.querySelector(".td-head");
const tdBack = getEl("#td-return");
const tdClose = getEl("#td-close");
tdDrawer.contains = (n) => [tdDrawer, tdHandle, tdHead, tdBack, tdClose, getEl("#td-input")].includes(n);
tdHandle.closest = (sel) => (sel.includes("#td-handle") ? tdHandle : null);

const wdDrawer = getEl("#worker-drawer");
const wdHandle = getEl("#wd-handle");
const wdHead = wdDrawer.querySelector(".wd-head");
const wdBack = getEl("#wd-return");
const wdClose = getEl("#wd-close");
wdDrawer.contains = (n) => [wdDrawer, wdHandle, wdHead, wdBack, wdClose, getEl("#wd-input")].includes(n);
wdHandle.closest = (sel) => (sel.includes("#wd-handle") ? wdHandle : null);

const notesPane = getEl("#notes-pane");
const npBody = getEl("#np-body");
const tdBody = getEl("#td-body");

const docListeners = new Map();
const winListeners = new Map();
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
  addEventListener(type, fn) { if (!docListeners.has(type)) docListeners.set(type, []); docListeners.get(type).push(fn); },
  removeEventListener() {},
  dispatch(type, ev = {}) {
    const e = { type, target: makeEl("doc-target"), defaultPrevented: false, preventDefault() { this.defaultPrevented = true; }, stopPropagation() {}, composedPath: () => [], ...ev };
    for (const fn of [...(docListeners.get(type) || [])]) fn(e);
    return e;
  },
};
doc.activeElement = doc.body;

// Real simulated history stack so pushState / back / go / popstate work end-to-end
const histEntries = [{ state: null, url: "http://127.0.0.1:7788/#/collie" }];
let histIdx = 0;
function applyHistUrl(url) {
  if (typeof url !== "string" || !url) return;
  const hashIdx = url.indexOf("#");
  if (hashIdx >= 0) ctx.location.hash = url.slice(hashIdx);
  ctx.location.href = url.startsWith("http") ? url : "http://127.0.0.1:7788/" + ctx.location.hash;
}
function firePopstate() {
  for (const fn of [...(winListeners.get("popstate") || [])]) fn({ type: "popstate", state: ctx.history.state });
}
let timers = [];
const fetchedFileUrls = [];
const ctx = {
  console, Math, Date, JSON, Set, Map, RegExp, Error, Promise, URL, URLSearchParams, Object, Array, Number, String, Boolean,
  setTimeout: (fn) => { const id = timers.length + 1; timers.push({ id, fn, cleared: false }); return id; },
  clearTimeout: (id) => { const t = timers.find((x) => x.id === id); if (t) t.cleared = true; },
  setInterval: () => 1, clearInterval() {}, queueMicrotask,
  requestAnimationFrame: (fn) => { fn(nowMs); return 1; },
  cancelAnimationFrame() {},
  document: doc,
  location: { hash: "#/collie", origin: "http://127.0.0.1:7788", href: "http://127.0.0.1:7788/#/collie", pathname: "/", search: "" },
  history: {
    get state() { return histEntries[histIdx].state; },
    get length() { return histEntries.length; },
    pushState(s, _, url) {
      histEntries.splice(histIdx + 1);
      histEntries.push({ state: s ? JSON.parse(JSON.stringify(s)) : null, url: url || ctx.location.href });
      histIdx = histEntries.length - 1;
      applyHistUrl(url);
    },
    replaceState(s, _, url) {
      histEntries[histIdx] = { state: s ? JSON.parse(JSON.stringify(s)) : null, url: url || histEntries[histIdx].url };
      applyHistUrl(url);
    },
    go(delta) {
      const next = histIdx + delta;
      if (next < 0 || next >= histEntries.length || next === histIdx) return;
      histIdx = next;
      applyHistUrl(histEntries[histIdx].url);
      firePopstate();
    },
    back() { ctx.history.go(-1); },
  },
  navigator: { platform: "iPhone", clipboard: { writeText: async () => {} } },
  isSecureContext: true, innerWidth: 390, innerHeight: 844,
  CSS: { escape: (s) => String(s), supports: () => true },
  HTMLDialogElement: { prototype: {} },
  Blob, Uint8Array, TextEncoder, TextDecoder, atob, AbortController,
  crypto: { randomUUID: () => "drawer-stack-uuid" },
  performance: { now: () => nowMs },
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
      || (q.includes("max-width: 1000px") && ctx.innerWidth <= 1000)
      || (q.includes("max-width: 1180px") && ctx.innerWidth <= 1180)
      || (q.includes("min-width: 1px"))
      || (q.includes("min-width: 1181px") && ctx.innerWidth >= 1181),
    addEventListener() {},
  }),
  getComputedStyle: (el) => ({ getPropertyValue: (k) => el?.style?.getPropertyValue?.(k) || "" }),
  getSelection: () => ({ isCollapsed: true, toString: () => "" }),
  addEventListener(type, fn) { if (!winListeners.has(type)) winListeners.set(type, []); winListeners.get(type).push(fn); },
  removeEventListener() {},
  dispatchEvent(ev) { for (const fn of [...(winListeners.get(ev.type) || [])]) fn(ev); },
  fetch: async (url) => {
    if (String(url).includes("/api/file")) {
      fetchedFileUrls.push(String(url));
      const u = new URL(String(url), "http://127.0.0.1:7788");
      const reqPath = u.searchParams.get("path") || "";
      const text = reqPath.includes("no-worker.md")
        ? "# 无 Worker 任务详情\n- **状态**：待人工确认\n- [ ] 检查最终交付物\n"
        : "# Notes\nhello world";
      return {
        ok: true,
        status: 200,
        headers: {
          get: (k) => {
            const lk = k.toLowerCase();
            if (lk === "content-type") return "application/octet-stream";
            if (lk === "x-file-kind") return "text";
            if (lk === "x-file-size") return String(new Blob([text]).size);
            if (lk === "x-file-path") return encodeURIComponent(reqPath);
            return "";
          },
        },
        blob: async () => new Blob([text]),
      };
    }
    return { ok: true, status: 200, json: async () => ({ ok: true, date: "2026-11-28", tasks: [], artifacts: [], timers: [], messages: [] }) };
  },
};
ctx.window = ctx;
ctx.globalThis = ctx;

const marker = "  boot();\n})();";
const instrumented = appSrc.replace(
  marker,
  "  globalThis.__test = { getView: () => view, setSup: (s) => { sup = normSup(s); }, setState: (s) => { state = s; }, setNotebookData: (d) => { notebookData = d; }, getNotesOpen: () => notesOpen, getSidePaneActiveMode: () => sidePaneActiveMode, setSidePanePref: (v) => { sidePanePref = v; }, setSidePaneOverlay: (v) => { sidePaneOverlay = v; }, getActiveThreadRoot: () => activeThreadRoot, getActiveWorkerKey: () => activeWorkerKey, getReturnStack: () => drawerReturnStack.map((x) => ({ ...x })), route, syncNotebookVisibility, switchNotebookTab, openThreadDrawer, closeThreadDrawer, openWorkerDrawer, closeWorkerDrawer, openFile, closeFilePane };\n})();"
);
vm.runInNewContext(instrumented, ctx, { filename: "web/app.js" });
const T = ctx.__test;

T.setState({
  agents: [
    { pane_id: "p-live-1", name: "fix-alpha", label: "修分页", kind: "codex", status: "working", is_agent: true, cwd: "/tmp/demo" },
  ],
  machines: [],
  shells: [],
});
T.setSup({
  router: { status: "idle", alive: true },
  workers: [
    { name: "fix-alpha", label: "修分页", pane_id: "p-live-1", status: "working", task: "修分页测试", thread: "e1", ended: false, created_at: "2026-11-28T02:00:00Z" },
  ],
  entries: [
    { id: "e1", role: "user", text: "帮忙修分页", at: "2026-11-28T02:00:00Z" },
    { id: "e2", role: "collie", text: "已派 fix-alpha 跟进，详见 /tmp/demo/plan.md", at: "2026-11-28T02:01:00Z", re: { entry: "e1", who: "你", text: "帮忙修分页" } },
    { id: "e3", role: "user", text: "好的继续", at: "2026-11-28T02:02:00Z", re: { entry: "e2", who: "Collie", text: "已派 fix-alpha 跟进" } },
  ],
});
T.setNotebookData({
  date: "2026-11-28",
  tasks: [{ id: "t1", title: "分页任务", worker: "fix-alpha", tone: "live" }],
  artifacts: [{ path: "/tmp/demo/plan.md", name: "plan.md", title: "分页方案", ext: "md" }],
  timers: [],
});

function visibleDrawerCount() {
  let n = 0;
  if (T.getNotesOpen() && !notesPane.classList.contains("is-hidden")) n++;
  if (getEl("#app").classList.contains("has-file") && !fvDrawer.hidden) n++;
  if (getEl("#app").classList.contains("has-thread") && !tdDrawer.hidden) n++;
  if (getEl("#app").classList.contains("has-worker") && !wdDrawer.hidden) n++;
  return n;
}
function openMobileNotes(tab = "notes", scrollTop = 0) {
  ctx.innerWidth = 390; ctx.innerHeight = 844;
  ctx.location.hash = "#/collie";
  T.route();
  T.setSidePaneOverlay(tab);
  T.syncNotebookVisibility({ immediate: true });
  if (tab !== T.getSidePaneActiveMode()) T.switchNotebookTab(tab);
  npBody.scrollTop = scrollTop;
  assert.equal(T.getNotesOpen(), true);
  assert.equal(visibleDrawerCount(), 1, "only notes drawer visible");
}
function dragDownDrawer(handleEl, drawerEl) {
  nowMs += 400;
  handleEl.dispatch("pointerdown", { target: handleEl, pointerId: 11, pointerType: "touch", clientX: 195, clientY: 100 });
  nowMs += 120;
  handleEl.dispatch("pointermove", { target: handleEl, pointerId: 11, pointerType: "touch", clientX: 195, clientY: 280 });
  nowMs += 40;
  handleEl.dispatch("pointerup", { target: handleEl, pointerId: 11, pointerType: "touch", clientX: 195, clientY: 280 });
}

// ── Case 1: 小本本 (进行中 Tab + scrollTop=148) -> Worker -> < 小本本 / close / swipe-down / Browser Back ──
openMobileNotes("notes", 148);
const npTask = makeEl("np-t1", "BUTTON");
npTask.dataset.npTask = "t1";
npTask.dataset.npWorker = "fix-alpha";
npTask.dataset.npTitle = "分页任务";
npTask.closest = (sel) => (sel === "[data-np-task]" ? npTask : null);

const histBefore1 = histIdx;
notesPane.dispatch("click", { target: npTask, button: 0 });
assert.equal(T.getActiveWorkerKey(), "fix-alpha");
assert.equal(visibleDrawerCount(), 1, "zero stacked drawers: notes hidden immediately when worker opens");
assert.equal(wdBack.hidden, false, "#wd-back visible when opened from notes");
assert.equal(getEl("#wd-return-label").textContent, "小本本");
assert.equal(wdBack.getAttribute("aria-label"), "返回小本本");
assert.deepEqual(T.getReturnStack(), [{ kind: "notes", tab: "notes", scrollTop: 148 }]);
assert.equal(histIdx, histBefore1 + 1, "drill-down pushes one history state");

// Clicking < 小本本 restores notes with original tab & scrollTop, unwinds history
wdBack.focus();
wdBack.dispatch("click");
assert.equal(T.getActiveWorkerKey(), null);
assert.equal(T.getNotesOpen(), true);
assert.equal(T.getSidePaneActiveMode(), "notes");
assert.equal(npBody.scrollTop, 148, "restores notes scrollTop");
assert.equal(visibleDrawerCount(), 1, "only notes visible after return");
assert.equal(T.getReturnStack().length, 0);
assert.equal(histIdx, histBefore1, "in-app back unwinds history entry");

// Re-enter Worker from notes, then swipe down handle -> returns to notes
npBody.scrollTop = 166;
notesPane.dispatch("click", { target: npTask, button: 0 });
assert.equal(T.getActiveWorkerKey(), "fix-alpha");
dragDownDrawer(wdHandle, wdDrawer);
assert.equal(T.getActiveWorkerKey(), null);
assert.equal(T.getNotesOpen(), true, "swiping down child worker drawer returns to notes");
assert.equal(npBody.scrollTop, 166);

// Re-enter Worker from notes, then Browser Back (history.back -> popstate) -> returns to notes
notesPane.dispatch("click", { target: npTask, button: 0 });
assert.equal(T.getActiveWorkerKey(), "fix-alpha");
ctx.history.back();
assert.equal(T.getActiveWorkerKey(), null);
assert.equal(T.getNotesOpen(), true, "browser back restores notes drawer");
assert.equal(T.getReturnStack().length, 0);

// ── Case 2: 小本本 (话题 Tab + scrollTop=210) -> 话题抽屉 -> close × returns to 小本本 话题 Tab + scrollTop=210 ──
openMobileNotes("threads", 210);
const thItem = makeEl("th-pick-e1", "BUTTON");
thItem.dataset.thPick = "e1";
thItem.dataset.thLatest = "e3";
thItem.closest = (sel) => (sel === "[data-th-pick]" ? thItem : null);

getEl("#collie-threads-list").dispatch("click", { target: thItem, button: 0 });
assert.equal(T.getActiveThreadRoot(), "e1");
assert.equal(visibleDrawerCount(), 1, "zero stacked drawers: notes hidden immediately when thread opens");
assert.equal(tdBack.hidden, false, "#td-back visible");
assert.equal(getEl("#td-return-label").textContent, "小本本");
assert.deepEqual(T.getReturnStack(), [{ kind: "notes", tab: "threads", scrollTop: 210 }]);

// Regular close button (#td-close) returns to notes with 话题 tab & scrollTop preserved
tdClose.dispatch("click");
assert.equal(T.getActiveThreadRoot(), null);
assert.equal(T.getNotesOpen(), true);
assert.equal(T.getSidePaneActiveMode(), "threads", "preserves 话题 tab on return");
assert.equal(npBody.scrollTop, 210, "preserves notes scrollTop on return");
assert.equal(visibleDrawerCount(), 1);

// ── Case 3: 小本本 (进行中 Tab + scrollTop=92) -> 文件预览抽屉 -> < 小本本 returns cleanly ──
openMobileNotes("notes", 92);
const npFile = makeEl("np-f1", "BUTTON");
npFile.dataset.npFile = "/tmp/demo/plan.md";
npFile.closest = (sel) => (sel === "[data-np-file]" ? npFile : null);
notesPane.dispatch("click", { target: npFile, button: 0 });
assert.equal(getEl("#app").classList.contains("has-file"), true);
assert.equal(visibleDrawerCount(), 1, "only file drawer visible");
assert.equal(fvBack.hidden, false);
assert.equal(getEl("#fv-return-label").textContent, "小本本");
fvDrawer.dispatch("click", { target: fvClose });
assert.equal(getEl("#app").classList.contains("has-file"), false);
assert.equal(T.getNotesOpen(), true, "closing file drawer returns to notes");
assert.equal(T.getSidePaneActiveMode(), "notes");
assert.equal(npBody.scrollTop, 92);

// ── Case 3b: 小本本「进行中」无 Worker (空心点) 任务 -> 直接在 #file-view 打开任务 .md + < 小本本 返回 ──
openMobileNotes("notes", 124);
fetchedFileUrls.length = 0;
const npIdleTask = makeEl("np-idle-task", "BUTTON");
npIdleTask.dataset.npTask = "no-worker";
npIdleTask.dataset.npWorker = "";
npIdleTask.dataset.npEntry = "";
npIdleTask.dataset.npTitle = "无 Worker 任务详情";
npIdleTask.dataset.npPath = "tasks/no-worker.md";
npIdleTask.dataset.npFilePath = "~/.collie/router/memory/tasks/no-worker.md";
npIdleTask.closest = (sel) => (sel === "[data-np-task]" ? npIdleTask : null);

const histBeforeIdleTask = histIdx;
notesPane.dispatch("click", { target: npIdleTask, button: 0 });
assert.equal(ctx.location.hash, "#/collie", "clicking hollow-dot task must stay on #/collie and NOT jump to #/auto");
assert.equal(T.getView(), "collie");
assert.equal(getEl("#app").classList.contains("has-file"), true, "hollow-dot task opens #file-view directly");
assert.equal(visibleDrawerCount(), 1, "zero stacked drawers when task opens #file-view");
assert.equal(fvBack.hidden, false, "#fv-return visible for hollow-dot task opened from 小本本");
assert.equal(getEl("#fv-return-label").textContent, "小本本");
assert.equal(fvBack.getAttribute("aria-label"), "返回小本本");
assert.deepEqual(T.getReturnStack(), [{ kind: "notes", tab: "notes", scrollTop: 124 }]);
assert.equal(histIdx, histBeforeIdleTask + 1);
assert.equal(fetchedFileUrls.length, 1, "must fetch task markdown via /api/file");
assert.ok(decodeURIComponent(fetchedFileUrls[0]).includes("path=~/.collie/router/memory/tasks/no-worker.md"), "must pass full ~/ or absolute file_path to /api/file: " + fetchedFileUrls[0]);

// Click < 小本本 returns cleanly to 小本本 with scrollTop=124 and unwinds history
fvBack.dispatch("click");
assert.equal(getEl("#app").classList.contains("has-file"), false);
assert.equal(T.getNotesOpen(), true, "clicking < 小本本 restores 小本本");
assert.equal(T.getSidePaneActiveMode(), "notes");
assert.equal(npBody.scrollTop, 124, "restores 小本本 scrollTop after viewing task markdown");
assert.equal(T.getReturnStack().length, 0);
assert.equal(histIdx, histBeforeIdleTask);

// Task with NO worker, NO file_path (only legacy relative tasks/... path) and NO entry_id falls back to #/auto memory tab
fetchedFileUrls.length = 0;
const npLegacyTask = makeEl("np-legacy-task", "BUTTON");
npLegacyTask.dataset.npTask = "legacy-only";
npLegacyTask.dataset.npWorker = "";
npLegacyTask.dataset.npEntry = "";
npLegacyTask.dataset.npPath = "tasks/legacy-only.md";
npLegacyTask.dataset.npFilePath = "";
npLegacyTask.closest = (sel) => (sel === "[data-np-task]" ? npLegacyTask : null);
notesPane.dispatch("click", { target: npLegacyTask, button: 0 });
assert.equal(fetchedFileUrls.length, 0, "must never pass bare relative tasks/... path to /api/file");
assert.equal(ctx.location.hash, "#/auto", "falls back to #/auto only when both worker and openable task file path are absent");
ctx.location.hash = "#/collie";
T.route();

// ── Case 4: 话题抽屉 (scrollTop=180) -> 文件预览抽屉 (both [data-open-file] and a.fpath) -> returns to 话题 ──
T.closeFilePane({ immediate: true });
T.setSidePaneOverlay("");
T.syncNotebookVisibility({ immediate: true });
T.openThreadDrawer("e1", "e3", "e3");
assert.equal(tdBack.hidden, true, "top-level thread opened from main chat has no back button");
tdBody.scrollTop = 180;

const tdFileBtn = makeEl("td-file-btn", "BUTTON");
tdFileBtn.dataset.openFile = "/tmp/demo/plan.md";
tdFileBtn.closest = (sel) => {
  if (sel.includes("[data-open-file]")) return tdFileBtn;
  if (sel === "#thread-drawer") return tdDrawer;
  return null;
};
getEl("#td-list").dispatch("click", { target: tdFileBtn, button: 0 });
assert.equal(getEl("#app").classList.contains("has-file"), true);
assert.equal(T.getActiveThreadRoot(), null);
assert.equal(visibleDrawerCount(), 1);
assert.equal(fvBack.hidden, false);
assert.equal(getEl("#fv-return-label").textContent, "话题");
assert.equal(fvBack.getAttribute("aria-label"), "返回话题");
assert.equal(T.getReturnStack()[0].kind, "thread");
assert.equal(T.getReturnStack()[0].entryId, "e1");
assert.equal(T.getReturnStack()[0].scrollTop, 180);

// Click < 话题 -> restores thread drawer & scrollTop=180
fvBack.dispatch("click");
assert.equal(getEl("#app").classList.contains("has-file"), false);
assert.equal(T.getActiveThreadRoot(), "e1");
assert.equal(tdBody.scrollTop, 180, "restores thread scrollTop");
assert.equal(tdBack.hidden, true, "top-level thread still has no back button");

// Also verify a.fpath link inside #thread-drawer records thread origin
tdBody.scrollTop = 205;
const fpathLink = makeEl("td-fpath", "A");
fpathLink.dataset.path = "/tmp/demo/plan.md";
fpathLink.closest = (sel) => {
  if (sel === "a.fpath") return fpathLink;
  if (sel === "#thread-drawer") return tdDrawer;
  return null;
};
doc.dispatch("click", { target: fpathLink, button: 0 });
assert.equal(getEl("#app").classList.contains("has-file"), true);
assert.equal(getEl("#fv-return-label").textContent, "话题");
dragDownDrawer(fvHandle, fvDrawer);
assert.equal(T.getActiveThreadRoot(), "e1", "swiping down file drawer returns to thread");
assert.equal(tdBody.scrollTop, 205);

// ── Case 5: 话题 -> Worker -> 点 #wd-thread-btn 退栈回原话题无死循环 ──
const tdWorkerLink = makeEl("td-wk-a", "A");
tdWorkerLink.dataset.tdWorker = "fix-alpha";
tdWorkerLink.href = "#/p/p-live-1";
tdWorkerLink.closest = (sel) => (sel === "a.td-worker[data-td-worker]" ? tdWorkerLink : null);
getEl("#td-workers").dispatch("click", { target: tdWorkerLink, button: 0 });
assert.equal(T.getActiveWorkerKey(), "fix-alpha");
assert.equal(wdBack.hidden, false);
assert.equal(getEl("#wd-return-label").textContent, "话题");
assert.equal(T.getReturnStack().length, 1);

getEl("#wd-thread-btn").dataset.wdThread = "e1";
getEl("#wd-thread-btn").dispatch("click");
assert.equal(T.getActiveWorkerKey(), null);
assert.equal(T.getActiveThreadRoot(), "e1");
assert.equal(T.getReturnStack().length, 0, "clicking linked same thread pops instead of creating thread<->worker cycle");
assert.equal(tdBack.hidden, true);
T.closeThreadDrawer({ immediate: true });

// ── Case 6: 2-level stack 小本本(话题 Tab, 130) -> 话题(240) -> 文件 -> 逐层返回 & 点遮罩全清 ──
openMobileNotes("threads", 130);
getEl("#collie-threads-list").dispatch("click", { target: thItem, button: 0 });
assert.equal(T.getActiveThreadRoot(), "e1");
assert.equal(getEl("#td-return-label").textContent, "小本本");
tdBody.scrollTop = 240;
getEl("#td-list").dispatch("click", { target: tdFileBtn, button: 0 });
assert.equal(getEl("#app").classList.contains("has-file"), true);
assert.equal(getEl("#fv-return-label").textContent, "话题");
assert.equal(T.getReturnStack().length, 2, "2-level stack: [notes, thread]");

// Step back 1: file -> thread (retains < 小本本!)
fvBack.dispatch("click");
assert.equal(T.getActiveThreadRoot(), "e1");
assert.equal(tdBody.scrollTop, 240);
assert.equal(tdBack.hidden, false, "thread still knows it came from 小本本");
assert.equal(getEl("#td-return-label").textContent, "小本本");
assert.equal(T.getReturnStack().length, 1);

// Re-enter file (depth 2), then tap outer scrim -> clears ALL stack & returns straight to main chat!
const histBeforeScrim = histIdx;
getEl("#td-list").dispatch("click", { target: tdFileBtn, button: 0 });
assert.equal(T.getReturnStack().length, 2);
getEl("#file-scrim").dispatch("click");
assert.equal(T.getReturnStack().length, 0, "scrim tap clears entire return stack");
assert.equal(getEl("#app").classList.contains("has-file"), false);
assert.equal(T.getActiveThreadRoot(), null, "scrim tap does NOT reopen thread");
assert.equal(T.getNotesOpen(), false, "scrim tap does NOT reopen notes");
assert.equal(fvBack.hidden, true);
assert.equal(tdBack.hidden, true);
assert.equal(wdBack.hidden, true);
assert.ok(histIdx <= histBeforeScrim, "scrim dismiss unwinds pushed drawer history entries");

// Opening a brand-new top-level drawer from main chat has zero ghost stack
T.openWorkerDrawer({ worker: "fix-alpha" });
assert.equal(T.getReturnStack().length, 0);
assert.equal(wdBack.hidden, true, "new top-level drawer has no ghost back button");
T.closeWorkerDrawer({ immediate: true });

// Opening a new top-level entry while a return stack exists wipes the old stack cleanly
openMobileNotes("notes", 77);
notesPane.dispatch("click", { target: npTask, button: 0 });
assert.equal(T.getReturnStack().length, 1);
// Simulate top-level main chat opening a thread directly (no returnTo)
T.openThreadDrawer("e1", "e3", "e3");
assert.equal(T.getReturnStack().length, 0, "new top-level entry clears stale returnTo stack");
assert.equal(tdBack.hidden, true);
T.closeThreadDrawer({ immediate: true });

// ── Case 7: Desktop (>1000px) zero side effects ──
ctx.innerWidth = 1440; ctx.innerHeight = 900;
T.setSidePanePref("notes");
T.route();
T.syncNotebookVisibility({ immediate: true });
assert.equal(T.getNotesOpen(), true);
const histDesktopBefore = histIdx;
notesPane.dispatch("click", { target: npTask, button: 0 });
assert.equal(T.getActiveWorkerKey(), "fix-alpha");
assert.equal(T.getReturnStack().length, 0, "desktop (>1000px) never pushes returnTo stack");
assert.equal(wdBack.hidden, true, "desktop (>1000px) never shows #wd-back");
assert.equal(histIdx, histDesktopBefore, "desktop (>1000px) never pushes drawer history");
T.closeWorkerDrawer({ immediate: true });

T.openThreadDrawer("e1", "e3", "e3");
getEl("#td-list").dispatch("click", { target: tdFileBtn, button: 0 });
assert.equal(getEl("#app").classList.contains("has-file"), true);
assert.equal(T.getReturnStack().length, 0, "desktop thread->file does not push returnTo stack");
assert.equal(fvBack.hidden, true, "desktop never shows #fv-back");
T.closeFilePane({ immediate: true });

// Desktop (>1000px) hollow-dot task click opens #file-view directly without jumping to #/auto or pushing mobile stack
T.setSidePanePref("notes");
T.syncNotebookVisibility({ immediate: true });
fetchedFileUrls.length = 0;
notesPane.dispatch("click", { target: npIdleTask, button: 0 });
assert.equal(ctx.location.hash, "#/collie", "desktop hollow-dot task must not jump to #/auto");
assert.equal(getEl("#app").classList.contains("has-file"), true, "desktop hollow-dot task opens #file-view");
assert.equal(T.getReturnStack().length, 0, "desktop hollow-dot task never pushes mobile returnTo stack");
assert.equal(fvBack.hidden, true, "desktop never shows #fv-return");
assert.equal(fetchedFileUrls.length, 1);
T.closeFilePane({ immediate: true });

// Resizing from mobile with active stack to desktop (>1000px) auto-clears stack & hides back buttons
openMobileNotes("notes", 50);
notesPane.dispatch("click", { target: npTask, button: 0 });
assert.equal(T.getReturnStack().length, 1);
assert.equal(wdBack.hidden, false);
ctx.innerWidth = 1280; ctx.innerHeight = 900;
ctx.dispatchEvent({ type: "resize" });
assert.equal(T.getReturnStack().length, 0, "resizing to >1000px clears mobile return stack");
assert.equal(wdBack.hidden, true, "resizing to >1000px hides back button");
T.closeWorkerDrawer({ immediate: true });

console.log("ALL_MOBILE_DRAWER_RETURN_STACK_TESTS_OK");
`
	cmd := exec.Command("node", "-e", script)
	cmd.Dir = filepath.Join("..", "..")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("mobile drawer return stack test failed: %v\n%s", err, out)
	}
}
