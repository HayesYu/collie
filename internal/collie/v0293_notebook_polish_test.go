package collie

import (
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestParseTaskMarkdownSkipsMetadataAndExtractsReadableNote(t *testing.T) {
	cases := []struct {
		name      string
		stem      string
		raw       string
		wantTitle string
		wantNote  string
	}{
		{
			name: "skips thread/worker metadata and prefers status without emoji or markdown noise",
			stem: "orders-v2",
			raw: `# ` + "`shop-api`" + ` 分页稳定性与压测优化

- **Thread**: chat:main (#t10883) · **Workers**: ` + "`fix-pagination`" + ` · **Timer**: 45m 巡检
- **Goal**:
  1. 解决同秒订单排序抖动并跑通 200 轮并发压测。
- **Status**: 🔄 11-27 14:57 **双线 worker** 已并行派发，正在跑 ` + "`go test -race`" + `。
`,
			wantTitle: "shop-api 分页稳定性与压测优化",
			wantNote:  "11-27 14:57 双线 worker 已并行派发，正在跑 go test -race。",
		},
		{
			name: "falls back to next when no status line exists",
			stem: "wave-3",
			raw: `# 索引升级与灰度观察
- **Thread**: chat:main (#t9833) · **Worker**: 无
- **Done**: 11-27 13:37 已完成基准校验。
- **Next**: ① 等增量任务跑完后再启动结构边回灌。
`,
			wantTitle: "索引升级与灰度观察",
			wantNote:  "① 等增量任务跑完后再启动结构边回灌。",
		},
		{
			name: "extracts multiline goal child bullet when status and next are absent",
			stem: "mobile-lcp",
			raw: `# 手机端首屏 LCP 优化
- **Thread**: chat:main (#t11001)
- **Goal**:
  1. 折叠区图片统一加 ` + "`loading=\"lazy\"`" + ` 并复测首屏耗时。
`,
			wantTitle: "手机端首屏 LCP 优化",
			wantNote:  "折叠区图片统一加 loading=\"lazy\" 并复测首屏耗时。",
		},
		{
			name: "plain chinese status label strips prefix cleanly",
			stem: "dark-mode",
			raw: `# 暗色模式对比度复核
状态：✅ 已通过移动端 390px 检查
`,
			wantTitle: "暗色模式对比度复核",
			wantNote:  "已通过移动端 390px 检查",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			gotTitle, gotNote := parseTaskMarkdown(tc.stem, tc.raw)
			if gotTitle != tc.wantTitle {
				t.Fatalf("title = %q, want %q", gotTitle, tc.wantTitle)
			}
			if gotNote != tc.wantNote {
				t.Fatalf("note = %q, want %q", gotNote, tc.wantNote)
			}
			if strings.Contains(gotNote, "Thread:") || strings.Contains(gotNote, "chat:main") {
				t.Fatalf("note leaked thread metadata: %q", gotNote)
			}
		})
	}
}

func TestWebV0293NotebookPolishContracts(t *testing.T) {
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

// 1) Header notebook icon is a folded-corner notepad, not split-sidebar rect+divider
const notesBtnMatch = htmlSrc.match(/id="open-notes-btn"[\s\S]*?<\/button>/);
assert.ok(notesBtnMatch, "expected #open-notes-btn in index.html");
assert.ok(!notesBtnMatch[0].includes('M14.5 4v16'), "header notebook icon must not use split-sidebar divider");
assert.ok(notesBtnMatch[0].includes('M14.5 20v-4.5') && notesBtnMatch[0].includes('M8 9h8'), "header notebook icon must use folded-corner lined note SVG");

// 2) Clear expand pill button markup (展开 / 收起)
assert.ok(htmlSrc.includes('class="np-expand-btn" id="np-expand"') && htmlSrc.includes('class="np-exp-lbl">展开</span>'), "expand button must render visible 展开/收起 pill");

// 3) Fixed width (no horizontal width jump), attached right of chat column, 0px chat shift on expand, restrained max-height min(48vh, 420px)
assert.ok(cssSrc.includes("--notes-w: 248px"), "notebook must use fixed 248px width");
assert.ok(!cssSrc.includes("--notes-w-exp"), "expanded notebook must not change width via --notes-w-exp");
assert.ok(!cssSrc.includes("#v-collie.has-notes.notes-expanded"), "expanding notebook must never alter main chat column padding/reserve");
assert.ok(cssSrc.includes("max-height: min(48vh, 420px)"), "expanded notebook must cap height to min(48vh, 420px)");
assert.ok(cssSrc.includes("calc(50% + (var(--col) - var(--notes-w) + var(--notes-gap)) / 2)"), "notebook must track right side of main conversation column");
assert.ok(cssSrc.includes(".notepane:not(.is-expanded) { cursor: pointer; }"), "compact notebook card must show pointer cursor across body");
assert.ok(cssSrc.includes(".np-expand-btn") && cssSrc.includes("min-height: 26px"), "expand button must be >=26px tall");

// 4) Runtime VM check: compact body click expands, artifact click stays preview, expand button toggles 展开/收起
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
    scrollTop: 0, scrollHeight: 200, clientHeight: 200, offsetWidth: 400, offsetHeight: 98, offsetParent: {}, isConnected: true,
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
const expLbl = makeEl(".np-exp-lbl");
expLbl.textContent = "展开";
getEl("#np-expand")._qs[".np-exp-lbl"] = expLbl;

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
  addEventListener() {},
  removeEventListener() {},
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
  isSecureContext: true, innerWidth: 1728, innerHeight: 1000,
  visualViewport: { height: 1000, offsetTop: 0, addEventListener() {} },
  CSS: { escape: (s) => String(s), supports: () => true },
  HTMLDialogElement: { prototype: {} },
  Blob, Uint8Array, TextEncoder, atob, AbortController,
  crypto: { randomUUID: () => "v0293-uuid" },
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
  fetch: async () => ({ ok: true, status: 200, json: async () => ({ ok: true }) }),
};
ctx.window = ctx;
ctx.globalThis = ctx;

const marker = "  boot();\n})();";
const instrumented = appSrc.replace(
  marker,
  "  globalThis.__test = { syncNotebookVisibility, renderNotebook, setNotebookData: (d) => { notebookData = d; }, setView: (v) => { view = v; }, getNotesExpanded: () => notesExpanded };\n})();"
);
vm.runInNewContext(instrumented, ctx, { filename: "web/app.js" });
const T = ctx.__test;

T.setView("collie");
T.setNotebookData({ date: "2026-11-27", tasks: [{ id: "t1", title: "任务 A", note: "进展 A", tone: "active" }], artifacts: [], timers: [] });
T.syncNotebookVisibility();
assert.equal(T.getNotesExpanded(), false);
assert.equal(expLbl.textContent, "展开");

// Clicking blank space in compact mode expands the card and flips button label to 收起
const blankTarget = makeEl("blank");
getEl("#notes-pane").dispatch("click", { target: blankTarget });
assert.equal(T.getNotesExpanded(), true, "clicking blank area on compact notebook must expand it");
assert.equal(expLbl.textContent, "收起", "expand button label must switch to 收起 when expanded");

// Clicking #np-expand toggles back to compact
getEl("#np-expand").dispatch("click");
assert.equal(T.getNotesExpanded(), false, "clicking 收起 button must collapse back to compact");
assert.equal(expLbl.textContent, "展开");
`
	cmd := exec.Command("node", "-e", script)
	cmd.Dir = filepath.Join("..", "..")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("v0.2.93 UI checks failed: %v\n%s", err, string(out))
	}
}
