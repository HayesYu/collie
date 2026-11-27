package collie

import (
	"encoding/json"
	"net/http"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

func TestClipWorkerSummaryRunes(t *testing.T) {
	if got := clipWorkerSummaryRunes("short", 10); got != "short" {
		t.Fatalf("short string changed: %q", got)
	}
	exact := strings.Repeat("中", 10)
	if got := clipWorkerSummaryRunes(exact, 10); got != exact {
		t.Fatalf("exact boundary string changed: %q", got)
	}
	over := strings.Repeat("测", 12) + "尾"
	got := clipWorkerSummaryRunes(over, 10)
	if !utf8.ValidString(got) {
		t.Fatalf("clipped string invalid UTF-8: %q", got)
	}
	if !strings.HasSuffix(got, "…") {
		t.Fatalf("expected ellipsis suffix, got %q", got)
	}
	if utf8.RuneCountInString(got) != 11 {
		t.Fatalf("expected 11 runes (10 + ellipsis), got %d: %q", utf8.RuneCountInString(got), got)
	}
	if !strings.HasPrefix(got, strings.Repeat("测", 10)) {
		t.Fatalf("prefix mismatch: %q", got)
	}
	withTrailingSpace := "hello   \t\nworld"
	if g := clipWorkerSummaryRunes(withTrailingSpace, 8); g != "hello…" {
		t.Fatalf("expected trailing whitespace trimmed before ellipsis, got %q", g)
	}
}

func TestSuperStateSlimsEndedWorkersPreservesActiveAndShow(t *testing.T) {
	sup, _, call := newPendingTestEnv(t)
	now := time.Now().UTC()

	longActiveTask := "任务目标（#t100）：" + strings.Repeat("活跃长任务描述", 200)
	longActiveReport := "阶段进展：" + strings.Repeat("活跃汇报详情", 120)
	longEndedTask := "任务目标（#t101）：" + strings.Repeat("已结束历史长提示词内容", 200)
	longEndedReport := "最终总结：" + strings.Repeat("已结束长最终报告细节", 120)
	longGoneTask := "GoneTask:" + strings.Repeat("abcdefghij", 80)
	longGoneReport := "GoneReport:" + strings.Repeat("0123456789", 60)
	shortStoppedTask := "短任务说明"
	shortStoppedReport := "已完成"

	sup.mu.Lock()
	sup.st.Workers["wk-active"] = &Worker{
		Name:       "wk-active",
		Kind:       "codex",
		PaneID:     "w1:p1",
		Task:       longActiveTask,
		LastReport: longActiveReport,
		Status:     "working",
		CreatedAt:  now.Add(-5 * time.Minute),
		ReportAt:   now.Add(-1 * time.Minute),
	}
	sup.st.Workers["wk-ended"] = &Worker{
		Name:       "wk-ended",
		Kind:       "claude",
		Task:       longEndedTask,
		LastReport: longEndedReport,
		Status:     "done",
		Ended:      true,
		CreatedAt:  now.Add(-2 * time.Hour),
		ReportAt:   now.Add(-1 * time.Hour),
	}
	sup.st.Workers["wk-gone"] = &Worker{
		Name:       "wk-gone",
		Kind:       "agy",
		Task:       longGoneTask,
		LastReport: longGoneReport,
		Status:     "gone",
		CreatedAt:  now.Add(-3 * time.Hour),
		ReportAt:   now.Add(-2 * time.Hour),
	}
	sup.st.Workers["wk-stopped"] = &Worker{
		Name:       "wk-stopped",
		Kind:       "codex",
		Task:       shortStoppedTask,
		LastReport: shortStoppedReport,
		Status:     "stopped",
		Ended:      true,
		CreatedAt:  now.Add(-30 * time.Minute),
		ReportAt:   now.Add(-20 * time.Minute),
	}
	sup.st.Timers = []*Timer{
		{ID: "m1", Spec: "daily 09:00", Text: "晨报", Enabled: true, NextRun: now.Add(time.Hour)},
		{ID: "m2", Spec: "in 30m", Text: "复核", Enabled: true, NextRun: now.Add(30 * time.Minute)},
		{ID: "m3", Spec: "daily 10:00", Text: "已停用", Enabled: false, NextRun: now.Add(2 * time.Hour)},
	}
	sup.mu.Unlock()

	// 1) GET /api/super slims ended/gone/stopped copies while keeping active untouched and reports timers_count
	rec := call(http.MethodGet, "/api/super", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /api/super status=%d body=%s", rec.Code, rec.Body.String())
	}
	var view superView
	if err := json.Unmarshal(rec.Body.Bytes(), &view); err != nil {
		t.Fatalf("unmarshal /api/super: %v", err)
	}
	if view.TimersCount != 2 {
		t.Fatalf("timers_count = %d, want 2 (only enabled timers)", view.TimersCount)
	}
	byName := map[string]*Worker{}
	for _, w := range view.Workers {
		byName[w.Name] = w
	}

	if got := byName["wk-active"]; got == nil || got.Task != longActiveTask || got.LastReport != longActiveReport {
		t.Fatalf("active worker must not be slimmed: got=%+v", got)
	}
	if got := byName["wk-ended"]; got == nil || !strings.HasSuffix(got.Task, "…") || utf8.RuneCountInString(got.Task) > endedWorkerTaskMaxRunes+1 {
		t.Fatalf("ended worker task not properly slimmed: got=%+v", got)
	}
	if got := byName["wk-ended"]; !strings.HasSuffix(got.LastReport, "…") || utf8.RuneCountInString(got.LastReport) > endedWorkerLastReportMaxRunes+1 {
		t.Fatalf("ended worker last_report not properly slimmed: runes=%d", utf8.RuneCountInString(got.LastReport))
	}
	if got := byName["wk-ended"]; !strings.HasPrefix(got.Task, "任务目标（#t101）：") || !strings.HasPrefix(got.LastReport, "最终总结：") {
		t.Fatalf("ended worker prefix lost: task=%q report=%q", got.Task, got.LastReport)
	}
	if got := byName["wk-gone"]; got == nil || !strings.HasSuffix(got.Task, "…") || utf8.RuneCountInString(got.Task) > endedWorkerTaskMaxRunes+1 {
		t.Fatalf("gone worker task not slimmed: got=%+v", got)
	}
	if got := byName["wk-gone"]; !strings.HasSuffix(got.LastReport, "…") || utf8.RuneCountInString(got.LastReport) > endedWorkerLastReportMaxRunes+1 {
		t.Fatalf("gone worker last_report not slimmed: runes=%d", utf8.RuneCountInString(got.LastReport))
	}
	if got := byName["wk-stopped"]; got == nil || got.Task != shortStoppedTask || got.LastReport != shortStoppedReport {
		t.Fatalf("short stopped worker must stay intact without ellipsis: got=%+v", got)
	}

	// 2) Underlying store remains 100% lossless (no in-place mutation)
	sup.mu.Lock()
	if sup.st.Workers["wk-ended"].Task != longEndedTask || sup.st.Workers["wk-ended"].LastReport != longEndedReport {
		sup.mu.Unlock()
		t.Fatalf("superState mutated underlying store worker fields")
	}
	sup.mu.Unlock()

	// 3) GET /api/super/show/{worker} stays 100% lossless
	showRec := call(http.MethodGet, "/api/super/show/wk-ended", nil)
	if showRec.Code != http.StatusOK {
		t.Fatalf("GET /api/super/show/wk-ended status=%d body=%s", showRec.Code, showRec.Body.String())
	}
	if !strings.Contains(showRec.Body.String(), longEndedTask) || !strings.Contains(showRec.Body.String(), longEndedReport) {
		t.Fatalf("GET /api/super/show must remain 100%% lossless for task and last_report")
	}
}

func TestWebV0295PerfDockMemoryAndAnchors(t *testing.T) {
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
const homeHtml = fs.readFileSync("docs/index.html", "utf8");
const readmeEn = fs.readFileSync("README.md", "utf8");
const readmeZh = fs.readFileSync("README.zh-CN.md", "utf8");

// ─── 1. Static Contracts ───
// 1a) P2-2 idle power-saving & residual scroll/recalc perf fixes
assert.ok(!/\.thread\s*>\s*\*\s*\+\s*\*\s*\{/.test(cssSrc), "universal adjacent-sibling .thread > * + * rule must be replaced by flex gap to prevent full-subtree recalc");
assert.ok(cssSrc.includes("--thread-gap: 28px") && cssSrc.includes("gap: var(--thread-gap)"), ".thread must use flex gap via --thread-gap");
assert.ok(cssSrc.includes(".viewer.chatbg") && cssSrc.includes("will-change: scroll-position"), "chat scroller must be promoted with will-change: scroll-position");
assert.ok(!/\.msg-acts\s*\{[^}]*transition:\s*opacity/.test(cssSrc), ".msg-acts must not animate opacity transition during scroll hover");
assert.ok(cssSrc.includes(".crew.is-overflow") && cssSrc.includes(".td-workers.is-overflow"), "ScrollTimeline strip-fade mask must be gated behind .is-overflow");
assert.ok(cssSrc.includes("html.is-bg .dot::after") && cssSrc.includes("html.is-idle .dot::after"), "idle/background animation power-saving gates must exist");
assert.ok(cssSrc.includes(".side.is-scroll-paused .dot::after"), "scroll-pause gate must freeze sidebar/crew/working animations during chat scroll");
assert.ok(!/\.dot\.idle\s*\{[^}]*animation:/.test(cssSrc), ".dot.idle must be calm static with zero infinite animation");
assert.ok(cssSrc.includes("wait-halo 2.8s ease-in-out infinite"), "preserve active wait-halo animation contract");

// 1b) Plan A (#t12590) single plain-text 小本本 trigger + frameless in-card 事项/话题 tabs, peer modal anti-clip + human copy, desktop drawer main-stream calm & detached user media cards
assert.ok(!htmlSrc.includes('id="collie-threads-btn"') && !htmlSrc.includes('id="collie-threads-pop"') && htmlSrc.includes('id="np-threads-body"') && htmlSrc.includes('id="np-tabs"'), "threads list must live inside unified #notes-pane with in-card #np-tabs and no duplicate top-bar button or dropdown");
assert.ok(!htmlSrc.includes('id="dk-tablist"') && !htmlSrc.includes('id="dk-tab-notes"') && !htmlSrc.includes('id="dk-tab-threads"'), "legacy #dk-tablist markup stays removed");
assert.ok(!htmlSrc.includes("展开详情"), "notebook card must never include a bottom 展开详情 text bar");
assert.ok(htmlSrc.includes('id="np-tasks-more"') && htmlSrc.includes('id="np-files-more"') && htmlSrc.includes('id="np-timers-more"'), "notebook must restore +N overflow pills");
const notesBtnHtml = htmlSrc.match(/<button[^>]*id="open-notes-btn"[\s\S]*?<\/button>/)?.[0] || "";
const thTabHtml = htmlSrc.match(/<button[^>]*id="np-tab-threads"[\s\S]*?<\/button>/)?.[0] || "";
assert.ok(notesBtnHtml.includes("小本本") && !notesBtnHtml.includes("<svg") && notesBtnHtml.includes('id="open-notes-dot"'), "notebook trigger must be plain-text 小本本 button with subtle pending dot and no SVG icon");
assert.ok(thTabHtml.includes("话题") && !thTabHtml.includes("<svg") && thTabHtml.includes('id="collie-threads-dot"'), "in-card 话题 tab must expose subtle pending dot without trailing number badge");
assert.ok(!htmlSrc.includes('id="td-back"'), "thread focus drawer header must omit redundant ← 话题线 back button");
assert.ok(!cssSrc.includes(".collie-top::after") && !cssSrc.includes(".collie-top.is-scrolled"), ".collie-top must stay seamless without bottom hairline or fog veil");
assert.ok(!appSrc.includes("+ 添加好友边牧"), "integrations top status row must not render redundant + 添加好友边牧 CTA button");
assert.ok(cssSrc.includes("--notes-reserve: calc(var(--notes-w) + var(--notes-gap))"), "wide desktop must preserve v0.2.94 left-biased --notes-reserve layout");
assert.ok(cssSrc.includes(".notepane.is-hidden") && cssSrc.includes("transform-origin: right top") && cssSrc.includes("translate3d(12px, 0, 0) scale(.985)"), "desktop notebook open/close must use silky compositor transform+opacity");
assert.ok(cssSrc.includes("transform: translate3d(var(--notes-shift), 0, 0)"), "main chat yield for notebook must use pure GPU transform translate3d without column-width rewrap");
assert.ok(cssSrc.includes(".np-fold") && cssSrc.includes("grid-template-rows: 0fr") && cssSrc.includes("grid-template-rows: 1fr"), "notebook compact<->expand must use isolated 0fr->1fr grid-row disclosure");
assert.ok(cssSrc.includes("#v-collie.is-parked") && cssSrc.includes("contain: strict"), "#v-collie must use parked layer with strict containment instead of display:none churn");
assert.ok(cssSrc.includes(".peer-qr-card") && cssSrc.includes("min-height: 214px"), "peer QR card must keep stable min-height to prevent tab/mode flicker");
assert.ok(cssSrc.includes('dialog.int-sheet[data-modal-kind="peers"]') && cssSrc.includes("max-height: min(620px, calc(100dvh - 32px))") && !cssSrc.includes("min-height: min(620px, calc(var(--vh) - 180px))"), "peer modal must enforce max-height: min(620px, calc(100dvh - 32px)) and flex scroll without clipping footer");
assert.ok(appSrc.includes("内网 / Tailscale 直连 · 令牌仅存本机") && !appSrc.includes("窄权令牌存 secrets.json"), "peer modal subtitle must use plain human language without secrets.json jargon");
assert.ok(appSrc.includes("先帮我查清，草稿给我审") && appSrc.includes("只留个便签，不自动回复") && appSrc.includes("查完给方案，批准再干活"), "peer reception modes must use plain human language");
assert.ok(cssSrc.includes(".app.has-thread #v-collie .viewer") && cssSrc.includes(".is-main-focused"), "desktop side-by-side thread drawer must gently dim left main stream and restore on hover/focus/click");
assert.ok(cssSrc.includes(".mine-media") && cssSrc.includes(".msg.user.is-media-only"), "user image messages must render detached rounded media cards above compact text bubble or standalone without dark box");
assert.ok(appSrc.includes("function syncPeerModalTabs") && appSrc.includes("function patchPeerQRCard") && appSrc.includes("invitesByMode"), "peer modal must mount once, cache invites, and patch in-place");
assert.ok(appSrc.includes("async function smartScrollCollie") && appSrc.includes("function syncSideActive") && appSrc.includes("function setCollieParked"), "smartScrollCollie, O(1) syncSideActive, and parked #v-collie must exist");

// 1c) Homepage & README Gemini / Antigravity copy
assert.ok(homeHtml.includes("Gemini") && homeHtml.includes("专武") && homeHtml.includes("Cursor"), "docs/index.html must include restrained bilingual Gemini/Antigravity positioning");
assert.ok(readmeEn.includes("Gemini command deck") && readmeEn.includes("Cursor"), "README.md must include English Gemini/Antigravity callout");
assert.ok(readmeZh.includes("Gemini 专武") && readmeZh.includes("Cursor"), "README.zh-CN.md must include Chinese Gemini/Antigravity callout");

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
getEl("#collie-threads-pop").hidden = true;
getEl("#file-view").hidden = true;

const docListeners = new Map();
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
  addEventListener(type, fn) { if (!docListeners.has(type)) docListeners.set(type, []); docListeners.get(type).push(fn); },
  removeEventListener() {},
  dispatch(type, ev = {}) {
    const e = { type, target: makeEl("doc-target"), defaultPrevented: false, preventDefault() { this.defaultPrevented = true; }, stopPropagation() {}, composedPath: () => [], ...ev };
    for (const fn of [...(docListeners.get(type) || [])]) fn(e);
    return e;
  },
};
const rafQueue = [];
let perfNow = 0;
const ctx = {
  console, Math, Date, JSON, Set, Map, RegExp, Error, Promise, URL, URLSearchParams, Object, Array, Number, String, Boolean,
  setTimeout: () => 1, clearTimeout() {}, setInterval: () => 1, clearInterval() {}, queueMicrotask,
  requestAnimationFrame: (fn) => { rafQueue.push(fn); return rafQueue.length; },
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
  crypto: { randomUUID: () => "v0295-uuid" },
  performance: { now: () => perfNow },
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
  "  globalThis.__test = { setView: (v) => { view = v; }, setBooted: (b) => { booted = !!b; }, route, setSup: (s) => { sup = normSup(s); }, setState: (s) => { state = s; }, syncAnimPowerState, superThreadWorkerFingerprint, setNotesExpanded, getNotesExpanded: () => notesExpanded, syncNotebookVisibility, setNotebookData: (d) => { notebookData = d; }, renderCollieThreadsMenu, formatAutoNavSub, integrationsHTML, openThreadDrawer, getActiveThreadRoot: () => activeThreadRoot, replyOptsMarkup, bucketDoneMemoryItems, renderMemoryHTML, showFilePane, closeFilePane, getCollieLayoutLock: () => collieLayoutLock, smartScrollCollie, renderPrefs, renderSuper, setCollieStickBottom: (v) => { collieStickBottom = !!v; }, getCollieStickBottom: () => collieStickBottom };\n})();"
);
vm.runInNewContext(instrumented, ctx, { filename: "web/app.js" });
const T = ctx.__test;

// 2a) P2-2 power-saving states & superThreadWorkerFingerprint decoupling
T.setState({ ok: true, agents: [], shells: [] });
T.setSup({ router: { status: "idle", alive: true }, entries: [], workers: [{ name: "w1", status: "done", ended: true }] });
doc.hidden = false;
T.syncAnimPowerState();
assert.equal(doc.documentElement.classList.contains("is-idle"), true, "all-idle state must set html.is-idle");
assert.equal(doc.documentElement.classList.contains("is-bg"), false);

T.setSup({ router: { status: "idle", alive: true }, entries: [], workers: [{ name: "w1", status: "working", report_at: "t1", topic: "A" }] });
T.syncAnimPowerState();
assert.equal(doc.documentElement.classList.contains("is-idle"), false, "active working worker must clear html.is-idle");

const fp1 = T.superThreadWorkerFingerprint();
T.setSup({ router: { status: "idle", alive: true }, entries: [], workers: [{ name: "w1", status: "working", report_at: "t2", topic: "B" }] });
const fp2 = T.superThreadWorkerFingerprint();
assert.equal(fp1, fp2, "superThreadWorkerFingerprint must ignore report_at/topic churn");
T.setSup({ router: { status: "idle", alive: true }, entries: [], workers: [{ name: "w1", status: "done", ended: true }] });
const fp3 = T.superThreadWorkerFingerprint();
assert.notEqual(fp2, fp3, "superThreadWorkerFingerprint must invalidate on worker status/ended transition");

doc.hidden = true;
T.syncAnimPowerState();
assert.equal(doc.documentElement.classList.contains("is-bg"), true, "document.hidden must set html.is-bg");
doc.hidden = false;
T.syncAnimPowerState();

// 2b) Independent top-bar 话题线 popover & 小本本 compact<->expand + Escape collapse + mutual exclusion
T.setView("collie");
const nowIso = new Date().toISOString();
T.setSup({
  router: { status: "idle", alive: true },
  entries: [{ id: "e1", role: "collie", text: "要发布吗？", at: nowIso, options: ["发版", "暂缓"] }],
  pending: [{ id: "e1", at: nowIso, summary: "要发布吗？", options: ["发版", "暂缓"] }],
  workers: [],
});
T.setNotebookData({
  date: "2026-11-27",
  tasks: [
    { id: "t1", title: "任务 1", note: "详情 1", tone: "live" },
    { id: "t2", title: "任务 2", note: "详情 2", tone: "attn" },
    { id: "t3", title: "任务 3", note: "详情 3", tone: "live" },
    { id: "t4", title: "任务 4", note: "详情 4", tone: "idle" },
  ],
  artifacts: [],
  timers: [],
});
ctx.innerWidth = 1440;
T.syncNotebookVisibility();
T.renderCollieThreadsMenu();
assert.equal(getEl("#open-notes-dot").hidden, false, "top-bar 小本本 button shows subtle pending dot when pending > 0");
assert.equal(getEl("#collie-threads-dot").hidden, false, "in-card 话题 tab shows pending dot when pending > 0");
assert.equal(getEl("#notes-pane").classList.contains("is-hidden"), false);
assert.equal(getEl("#notes-pane").getAttribute("data-side-mode"), "notes");
assert.equal(T.getNotesExpanded(), false);

// Expand via blank click, collapse via Escape
getEl("#notes-pane").dispatch("click", { target: makeEl("blank") });
assert.equal(T.getNotesExpanded(), true);
assert.equal(getEl("#notes-pane").classList.contains("is-expanded"), true);
doc.dispatch("keydown", { key: "Escape" });
assert.equal(T.getNotesExpanded(), false, "Escape collapses expanded floating notebook back to compact");
assert.equal(getEl("#notes-pane").classList.contains("is-hidden"), false, "Escape from expanded stays open in compact state");

// Switch side card in-place to 话题 -> persists "threads"; click 小本本 -> closes & persists "none"; click 小本本 again -> reopens; switch back to 事项 -> persists "notes"
getEl("#np-tab-threads").dispatch("click");
assert.equal(getEl("#notes-pane").classList.contains("is-hidden"), false);
assert.equal(getEl("#notes-pane").getAttribute("data-side-mode"), "threads");
assert.equal(getEl("#np-threads-body").hidden, false);
assert.equal(getEl("#np-body").hidden, true);
assert.equal(JSON.parse(storage.get("collie.sidePane")), "threads", "switching to threads persists collie.sidePane = threads");
getEl("#open-notes-btn").dispatch("click");
assert.equal(getEl("#notes-pane").classList.contains("is-hidden"), true);
assert.equal(getEl("#notes-pane").inert, true);
assert.equal(JSON.parse(storage.get("collie.sidePane")), "none", "closing side card persists collie.sidePane = none");
getEl("#open-notes-btn").dispatch("click");
assert.equal(getEl("#notes-pane").classList.contains("is-hidden"), false);
assert.equal(getEl("#notes-pane").getAttribute("data-side-mode"), "threads", "reopening restores last active tab");
getEl("#np-tab-notes").dispatch("click");
assert.equal(getEl("#notes-pane").getAttribute("data-side-mode"), "notes");
assert.equal(JSON.parse(storage.get("collie.sidePane")), "notes", "switching back to 事项 persists collie.sidePane = notes");

// Close & reopen notebook via card close × button
getEl("#np-close").dispatch("click");
assert.equal(getEl("#notes-pane").classList.contains("is-hidden"), true);
assert.equal(getEl("#notes-pane").inert, true);
assert.equal(JSON.parse(storage.get("collie.sidePane")), "none");
getEl("#open-notes-btn").dispatch("click");
assert.equal(getEl("#notes-pane").classList.contains("is-hidden"), false);
assert.equal(getEl("#notes-pane").inert, false);

// 2c) Decision card free-text reply dedup (#t11377)
const decEntry = { id: "e50", role: "collie", text: "怎么处理？", at: "2026-11-27T10:00:00Z", options: ["方案 A", "方案 B"] };
const pickedHtml = T.replyOptsMarkup({ ...decEntry, pick: { state: "picked", text: "方案 A", at: "2026-11-27T10:01:00Z" } }, 0, [decEntry], new Set());
assert.ok(pickedHtml.includes("已选：") && pickedHtml.includes("方案 A") && !pickedHtml.includes("is-replied"), "pure option click keeps 已选：<opt>");

const customHtml = T.replyOptsMarkup({ ...decEntry, pick: { state: "custom", text: "不要展开详情模式，保持单态卡片", at: "2026-11-27T10:02:00Z" } }, 0, [decEntry], new Set());
assert.ok(customHtml.includes("is-replied") && customHtml.includes("已回复"), "free-text reply renders light 已回复 pill");
assert.ok(!customHtml.includes("不要展开详情模式"), "free-text reply must NOT duplicate user's typed text in decision card pill or body");
assert.ok(!customHtml.includes("rr-custom-note"), "free-text reply must omit duplicate .rr-custom-note block");

const dismissHtml = T.replyOptsMarkup({ ...decEntry, pick: { state: "dismissed", at: "2026-11-27T10:03:00Z" } }, 0, [decEntry], new Set());
assert.ok(dismissHtml.includes("is-dismissed") && dismissHtml.includes("已划掉"), "dismissed card keeps 已划掉 state");

// 2d) Memory tasks/done/ progressive time bucketing (#t11370)
const refNow = new Date(2026, 10, 27, 15, 0, 0); // 2026-11-27 (Fri)
const doneItems = [
  { name: "tasks/done/today-a.md", text: "# 今日任务 A", size: 50, mod: new Date(2026, 10, 27, 14, 0, 0).toISOString() },
  { name: "tasks/done/today-b.md", text: "# 今日任务 B", size: 50, mod: new Date(2026, 10, 27, 9, 0, 0).toISOString() },
  { name: "tasks/done/yest.md", text: "# 昨日任务", size: 50, mod: new Date(2026, 10, 26, 18, 0, 0).toISOString() },
  { name: "tasks/done/near4d.md", text: "# 四天前任务", size: 50, mod: new Date(2026, 10, 23, 10, 0, 0).toISOString() },
  { name: "tasks/done/early-month.md", text: "# 本月初任务", size: 50, mod: new Date(2026, 10, 5, 10, 0, 0).toISOString() },
  { name: "tasks/done/prev-month.md", text: "# 上月归档任务\n含独特关键词火星计划", size: 60, mod: new Date(2026, 9, 18, 10, 0, 0).toISOString() },
];
const buckets = T.bucketDoneMemoryItems(doneItems, refNow);
assert.equal(buckets.length, 5, "expected 5 progressive time buckets (today, yesterday, 11-23, month-week, prev-month)");
assert.equal(buckets[0].label, "今天");
assert.equal(buckets[0].kind, "day");
assert.equal(buckets[0].items.length, 2);
assert.equal(buckets[1].label, "昨天");
assert.equal(buckets[1].kind, "day");
assert.ok(buckets[2].label.startsWith("11-23 周"), "near-7d bucket must format as MM-DD 周X: " + buckets[2].label);
assert.equal(buckets[2].kind, "day");
assert.equal(buckets[3].label, "11 月第 1 周");
assert.equal(buckets[3].kind, "week");
assert.equal(buckets[4].label, "2026 年 10 月");
assert.equal(buckets[4].kind, "month");

// Default idle render: ONLY the most recent day bucket ("今天") is open; older buckets start collapsed
const memFixture = { dir: "~/.collie", up_front: 100, messages: 0, loaded: 100, budget: 10000, dreams: [], files: doneItems };
const memIdle = T.renderMemoryHTML(memFixture, "");
assert.ok(memIdle.html.includes('class="mem-done-buckets"'), "done list renders progressive buckets");
const openBucketCount = (memIdle.html.match(/<details class="mem-done-bucket" open/g) || []).length;
assert.equal(openBucketCount, 1, "only the single most recent day bucket must default open");
assert.ok(memIdle.html.includes('<details class="mem-done-bucket" open data-mem-key="done:day:'), "opened bucket must be a day bucket");
const memSearch = T.renderMemoryHTML(memFixture, "火星计划");
assert.equal(memSearch.matchedFiles, 1);
assert.ok(memSearch.html.includes('<details class="mem-done" open'), "search hit auto-expands outer mem-done");
assert.ok(memSearch.html.includes('<details class="mem-done-bucket" open'), "search hit auto-expands matched month bucket");

// 2e) .filepane open/close scroll anchoring (#t11394)
const sc = getEl("#super-thread").parentElement;
sc.scrollTop = 420;
sc.scrollHeight = 1600;
sc.clientHeight = 600;
let msgTop = 120;
const msgRow = makeEl("anchor-msg");
msgRow.dataset.id = "e42";
msgRow.getBoundingClientRect = () => ({ top: msgTop, bottom: msgTop + 80, left: 0, right: 500, width: 500, height: 80 });
getEl("#super-thread")._qsa[".msg[data-id], .event[data-id]"] = [msgRow];
getEl("#super-thread")._qs['.msg[data-id="e42"], .event[data-id="e42"]'] = msgRow;
sc.getBoundingClientRect = () => ({ top: 100, bottom: 700, left: 0, right: 600, width: 600, height: 600 });

rafQueue.length = 0;
T.showFilePane();
assert.ok(getEl("#app").classList.contains("has-file"), "showFilePane must add .has-file");
const openLock = T.getCollieLayoutLock();
assert.ok(openLock && openLock.snap && openLock.snap.anchorId === "e42", "showFilePane must arm collie layout scroll lock anchored to visible message e42");
// Simulate column narrow causing msgRow to drift +140px downward -> rAF lock compensates scrollTop by +140px
msgTop = 260;
perfNow = 50;
const frameBatch = rafQueue.splice(0);
for (const fn of frameBatch) fn(perfNow);
assert.equal(sc.scrollTop, 560, "layout lock must restore scrollTop to keep anchored message fixed in viewport");

T.closeFilePane();
assert.ok(!getEl("#app").classList.contains("has-file"), "closeFilePane must remove .has-file");
const closeLock = T.getCollieLayoutLock();
assert.ok(closeLock && closeLock.snap && closeLock.snap.anchorId === "e42", "closeFilePane must preserve layout scroll lock so closing CSV/file preview does not drift main chat");

// 2f) Sidebar 自动化 overview subtitle & clean integrations top row
assert.deepEqual(
  T.formatAutoNavSub({ integrations_summary: { connected: 2, error: 0 }, peers_summary: { enabled: true, active: 1 }, timers_count: 3 }),
  { text: "3 接入 · 3 定时", err: false, title: "" },
);
assert.equal(
  T.formatAutoNavSub({ integrations_summary: { connected: 2, error: 1 }, timers_count: 2 }).text,
  "1 接入异常 · 2 定时",
);
assert.equal(
  T.formatAutoNavSub({ integrations_summary: { connected: 0, error: 0 }, timers_count: 0 }).text,
  "接入与定时",
);
const intHtml = T.integrationsHTML({ integrations: [], notify: {}, hook_key: "k" }, { enabled: true, peers: [] });
assert.ok(!intHtml.includes("+ 添加好友边牧"), "integrations top status row must omit redundant + 添加好友边牧 button");
assert.ok(intHtml.includes("peer-int-card") && intHtml.includes('data-peer-open="auto"'), "peer card entry must remain intact");

(async () => {
  // 2g) #t11669 Distance-aware smartScrollCollie: near -> direct smooth; far -> auto teleport ~180px + short smooth glide
  const calls = [];
  const fakeSc = {
    scrollTop: 0,
    scrollHeight: 12000,
    clientHeight: 800,
    scrollTo({ top, behavior }) {
      calls.push({ top: Math.round(top), behavior });
      this.scrollTop = top;
    },
    addEventListener() {},
    removeEventListener() {},
  };
  // Near jump (600px <= 1200px): single smooth scroll
  calls.length = 0;
  const nearMode = await T.smartScrollCollie(fakeSc, 600);
  assert.equal(nearMode, "smooth", "near jump must use direct smooth scroll");
  assert.equal(calls.length, 1);
  assert.equal(calls[0].behavior, "smooth");
  assert.equal(calls[0].top, 600);

  // Far jump downward (600 -> 9000, delta=8400 > 1200): instant auto teleport to 8820, rAF, then 180px smooth glide to 9000
  calls.length = 0;
  rafQueue.length = 0;
  const farPromise = T.smartScrollCollie(fakeSc, 9000);
  assert.equal(calls.length, 1, "far jump must synchronously teleport first before rAF");
  assert.equal(calls[0].behavior, "auto", "far jump teleport must use behavior:auto");
  assert.equal(calls[0].top, 8820, "far jump must teleport to 180px before target (9000-180)");
  while (rafQueue.length) rafQueue.shift()(16);
  const farMode = await farPromise;
  assert.equal(farMode, "teleport-glide", "far jump must complete with teleport-glide");
  assert.equal(calls.length, 2);
  assert.equal(calls[1].behavior, "smooth", "second phase must be short smooth glide");
  assert.equal(calls[1].top, 9000);

  // Far jump upward (9000 -> 400): instant auto teleport to 580, rAF, then smooth glide to 400
  calls.length = 0;
  rafQueue.length = 0;
  const upPromise = T.smartScrollCollie(fakeSc, 400);
  assert.equal(calls[0].behavior, "auto");
  assert.equal(calls[0].top, 580, "upward far jump must teleport to 180px below target (400+180)");
  while (rafQueue.length) rafQueue.shift()(32);
  await upPromise;
  assert.equal(calls[1].behavior, "smooth");
  assert.equal(calls[1].top, 400);

  // 2h) #t11654 / #t11679: Parked #v-collie preserves layout tree across route switches & renderPrefs caches unchanged HTML
  T.setBooted(true);
  T.setView("");
  ctx.location.hash = "#/collie";
  T.route();
  assert.equal(getEl("#v-collie").hidden, false);
  assert.equal(getEl("#v-collie").classList.contains("is-parked"), false);
  const prefsEl = getEl("#prefs-body");
  let prefsHtmlWrites = 0;
  let prefsHtmlVal = prefsEl.innerHTML;
  Object.defineProperty(prefsEl, "innerHTML", {
    get: () => prefsHtmlVal,
    set: (v) => { prefsHtmlWrites++; prefsHtmlVal = v; },
    configurable: true,
  });
  ctx.location.hash = "#/prefs";
  T.route();
  assert.equal(getEl("#v-collie").hidden, false, "leaving #/collie must keep #v-collie mounted (not display:none)");
  assert.equal(getEl("#v-collie").classList.contains("is-parked"), true, "leaving #/collie must apply .is-parked");
  assert.equal(getEl("#v-collie").getAttribute("aria-hidden"), "true", "parked #v-collie must set aria-hidden");
  assert.equal(prefsHtmlWrites, 1, "first visit to #/prefs writes HTML once");
  ctx.location.hash = "#/collie";
  T.route();
  assert.equal(getEl("#v-collie").classList.contains("is-parked"), false, "returning to #/collie unparks instantly");
  assert.equal(getEl("#v-collie").hasAttribute("aria-hidden"), false, "unparked #v-collie restores a11y visibility");
  ctx.location.hash = "#/prefs";
  T.route();
  assert.equal(prefsHtmlWrites, 1, "second visit to #/prefs with unchanged state must skip innerHTML rebuild");

  // 2i) #t11640: Sticky-bottom snaps when attachment <img> finishes loading, and yields when user wheels upward
  ctx.location.hash = "#/collie";
  T.route();
  const threadBox = getEl("#super-thread");
  const chatSc = threadBox.parentElement;
  chatSc.scrollHeight = 1400;
  chatSc.clientHeight = 600;
  chatSc.scrollTop = 800;
  T.setCollieStickBottom(true);
  chatSc.scrollHeight = 1950; // simulate async image expanding bubble height
  threadBox.dispatch("load", { target: { tagName: "IMG" } });
  assert.equal(chatSc.scrollTop, 1950, "image load while sticky-bottom must re-snap scroller to bottom");
  chatSc.dispatch("wheel", { deltaY: -40 });
  assert.equal(T.getCollieStickBottom(), false, "upward wheel must immediately cancel sticky-bottom");
  chatSc.scrollTop = 1200;
  chatSc.scrollHeight = 2300;
  threadBox.dispatch("load", { target: { tagName: "IMG" } });
  assert.equal(chatSc.scrollTop, 1200, "image load after upward wheel must not yank user back to bottom");

  console.log("OK: all v0.2.95+v0.3.1 perf, notebook/threads split, memory-bucket, reply-dedup, filepane-anchor, auto-sub, smart-scroll, parked-view & image-stick checks passed");
})().catch((e) => { console.error(e); process.exit(1); });
`
	cmd := exec.Command("node", "-e", script)
	cmd.Dir = filepath.Join("..", "..")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("v0.2.95 UI checks failed: %v\n%s", err, string(out))
	}
}
