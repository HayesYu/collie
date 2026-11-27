package collie

import (
	"os/exec"
	"path/filepath"
	"testing"
)

func TestWebMemoryUXContractsAndBehavior(t *testing.T) {
	if _, err := exec.LookPath("node"); err != nil {
		t.Skip("node not installed")
	}
	script := `
const fs = require("fs");
const vm = require("vm");
const assert = require("assert");

const cssSrc = fs.readFileSync("web/app.css", "utf8");
const appSrc = fs.readFileSync("web/app.js", "utf8");

// ─── 1. Static CSS contracts (monochrome hierarchy + highlight + 390px mobile) ───
assert.ok(cssSrc.includes("::highlight(mem-search)"), "CSS must style ::highlight(mem-search)");
assert.ok(cssSrc.includes("mark.mem-hl"), "CSS must provide mark.mem-hl fallback style");
assert.ok(cssSrc.includes(".mem-search-wrap"), "CSS must define .mem-search-wrap");
assert.ok(cssSrc.includes(".mem-dream-sum"), "CSS must define .mem-dream-sum");
assert.ok(cssSrc.includes(".mem-ins-group"), "CSS must define .mem-ins-group");
assert.ok(cssSrc.includes(".mem-ins-tag"), "CSS must define .mem-ins-tag");

function cssBlock(selector) {
  const esc = selector.replace(/[.*+?^${}()|[\]\\]/g, "\\$&");
  const m = cssSrc.match(new RegExp(esc + "\\s*\\{([^}]*)\\}"));
  assert.ok(m, "expected CSS block for " + selector);
  return m[1];
}
// Restrained monochrome chrome: search bar, dream summary, inferred pill & hit badges must not tint with --attn
for (const sel of [".mem-search-wrap", ".mem-dream-sum", ".mem-ins-tag", ".mem-hit-cnt", ".mem-ins-item.is-inferred"]) {
  assert.ok(!cssBlock(sel).includes("--attn"), sel + " must stay restrained monochrome without --attn");
}

// ─── 2. Load web/app.js in sandboxed VM and verify parsers, HTML & interactions ───
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
    id,
    tagName: "DIV",
    value: "",
    textContent: "",
    innerHTML: "",
    hidden: false,
    disabled: false,
    checked: false,
    open: false,
    className: "",
    title: "",
    style: { setProperty() {}, removeProperty() {} },
    dataset: {},
    classList: makeClassList(),
    children: [],
    scrollTop: 0,
    scrollHeight: 200,
    clientHeight: 200,
    offsetWidth: 400,
    offsetParent: {},
    isConnected: true,
    _qs: {},
    _qsa: {},
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
      if (el._qs[sel]) return el._qs[sel];
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
    contains() { return false; },
    append() {},
    remove() {},
    after() {},
    focus() { el._focused = true; },
    blur() { el._blurred = true; },
    setSelectionRange(a, b) { el._sel = [a, b]; },
    click() {},
    scrollIntoView() {},
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
const highlightMap = new Map();
const apiPosts = [];
let mockMem = null;

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
  location: { hash: "#/auto", origin: "http://127.0.0.1:7788", href: "http://127.0.0.1:7788/#/auto", pathname: "/", search: "" },
  history: { state: null, pushState() {}, replaceState() {}, back() {} },
  navigator: { platform: "MacIntel", clipboard: { writeText: async () => {} } },
  isSecureContext: true,
  innerWidth: 1280,
  innerHeight: 800,
  visualViewport: { height: 800, offsetTop: 0, addEventListener() {} },
  CSS: { escape: (s) => String(s), supports: () => true, highlights: highlightMap },
  HTMLDialogElement: { prototype: {} },
  Blob, Uint8Array, TextEncoder, atob, AbortController,
  crypto: { randomUUID: () => "mem-ux-uuid" },
  performance: { now: () => 0 },
  Event: class Event { constructor(t) { this.type = t; } },
  CustomEvent: class CustomEvent { constructor(t, d) { this.type = t; this.detail = d?.detail; } },
  ResizeObserver: class { observe() {} disconnect() {} },
  MutationObserver: class { observe() {} disconnect() {} },
  IntersectionObserver: class { observe() {} disconnect() {} },
  EventSource: class { addEventListener() {} close() {} },
  Notification: class { static permission = "default"; static async requestPermission() { return "default"; } },
  localStorage: {
    getItem: (k) => (storage.has(k) ? storage.get(k) : null),
    setItem: (k, v) => storage.set(k, String(v)),
    removeItem: (k) => storage.delete(k),
  },
  matchMedia: () => ({ matches: false, addEventListener() {} }),
  getComputedStyle: () => ({ getPropertyValue: () => "", lineHeight: "21px" }),
  addEventListener() {},
  removeEventListener() {},
  dispatchEvent() {},
  fetch: async (url, init = {}) => {
    const method = (init.method || "GET").toUpperCase();
    if (String(url).includes("/api/super/memory") && method === "POST") {
      const body = JSON.parse(init.body || "{}");
      apiPosts.push(body);
      if (mockMem && body.path) {
        const f = mockMem.files.find((x) => x.name === body.path);
        if (f) f.text = body.text;
      }
      return { ok: true, status: 200, json: async () => ({ ok: true }), text: async () => '{"ok":true}' };
    }
    if (String(url).includes("/api/super/memory")) {
      return { ok: true, status: 200, json: async () => mockMem, text: async () => JSON.stringify(mockMem) };
    }
    return { ok: true, status: 200, json: async () => ({ ok: true }), text: async () => '{"ok":true}' };
  },
};
ctx.window = ctx;
ctx.globalThis = ctx;

const marker = "  boot();\n})();";
assert.ok(appSrc.includes(marker), "expected boot() tail in web/app.js");
const instrumented = appSrc.replace(
  marker,
  "  globalThis.__test = { parseInstructions, removeInstructionLines, lineRangeOffsets, extractDreamSummary, formatDreamStamp, renderMemoryHTML, buildMemorySnippet, setMemDreamExpanded: (v) => { memDreamExpanded = !!v; }, setMemData: (d) => { memData = d; }, setView: (v) => { view = v; }, setAutoTab: (t) => { autoTab = t; }, getMemInsUndo: () => memInsUndo };\n})();"
);
vm.runInNewContext(instrumented, ctx, { filename: "web/app.js" });

const T = ctx.__test;

// 2a) parseInstructions: splits explicit vs (inferred) prefix/suffix/full-width + nested sub-bullets, preserves code blocks
const sampleIns = [
  "# 你的约定",
  "- 发布说明用中文，每条一句话",
  "- (inferred) 优先在 devbox 上跑测试",
  "  - 界面相关改动放 macbook",
  "- （inferred）：汇报时先给结论再展开细节",
  "- 动生产数据库前先问我",
  "- 合并前确认单测通过（inferred）。",
  "",
  "~~~text",
  "- not a bullet inside code fence",
  "~~~",
].join("\n").replace(/~~~/g, "\x60\x60\x60") + "\n";

const parsed = T.parseInstructions(sampleIns);
assert.equal(parsed.explicit.length, 2, "expected 2 explicit rules");
assert.equal(parsed.inferred.length, 3, "expected 3 inferred rules");
assert.equal(parsed.extraBlocks.length, 1, "code fence block must be preserved as extraBlock");
assert.equal(parsed.explicit[0].text, "发布说明用中文，每条一句话");
assert.equal(parsed.explicit[1].text, "动生产数据库前先问我");
assert.equal(parsed.inferred[0].text, "优先在 devbox 上跑测试");
assert.equal(parsed.inferred[0].startLine, 2);
assert.equal(parsed.inferred[0].endLine, 3, "nested sub-bullet must stay attached to parent inferred item");
assert.equal(parsed.inferred[1].text, "汇报时先给结论再展开细节");
assert.equal(parsed.inferred[2].text, "合并前确认单测通过。");

// 2b) removeInstructionLines: surgical lossless removal preserving every other line verbatim
const afterDel = T.removeInstructionLines(sampleIns, 2, 3);
assert.ok(!afterDel.includes("优先在 devbox 上跑测试"), "deleted parent line must be gone");
assert.ok(!afterDel.includes("界面相关改动放 macbook"), "deleted nested sub-bullet must be gone");
assert.ok(afterDel.includes("- 发布说明用中文，每条一句话"), "explicit rule must remain verbatim");
assert.ok(afterDel.includes("- （inferred）：汇报时先给结论再展开细节"), "sibling inferred rule must remain verbatim");
assert.ok(afterDel.includes("- not a bullet inside code fence"), "code fence must remain verbatim");

// lineRangeOffsets selects exact line span
const off = T.lineRangeOffsets(sampleIns, 1, 1);
assert.equal(sampleIns.slice(off.start, off.end), "- 发布说明用中文，每条一句话");

// 2c) extractDreamSummary: extracts stamp, lead headline, up to 3 points
const dreamEntry = {
  name: "2026-11-27-0400.md",
  text: [
    "# 2026-11-27 做梦日记",
    "整理了分页测试笔记并归档 1 个已完成任务。",
    "",
    "- notes/shop-api.md：补充同秒订单按 id 二次排序说明",
    "- instructions.md：新增 1 条推断习惯 (inferred)",
    "- tasks/done/dark-mode.md：归档已完成任务",
    "- notes/machines.md：更新工具清单（第 4 条不应挤进前 3 要点）",
  ].join("\n"),
};
const dSum = T.extractDreamSummary(dreamEntry);
assert.equal(dSum.stamp, "2026-11-27 04:00");
assert.equal(dSum.headline, "整理了分页测试笔记并归档 1 个已完成任务。");
assert.equal(dSum.points.length, 3, "summary points must be capped at 3");
assert.equal(dSum.hasMore, true, "extra 4th bullet must flag hasMore");

// 2d) renderMemoryHTML: default view (Dream summary card + grouped instructions) vs search view
const nowIso = new Date().toISOString();
mockMem = {
  dir: "~/.collie",
  up_front: 4096,
  messages: 1024,
  loaded: 5120,
  budget: 10240,
  dreaming: false,
  last_dream: nowIso,
  next_dream: nowIso,
  dreams: [dreamEntry],
  files: [
    { name: "instructions.md", text: sampleIns, size: 512, mod: nowIso },
    { name: "tasks/v1.4-release.md", text: "# v1.4 发布\n- [ ] 核对分页压测脚本", size: 256, mod: nowIso },
    { name: "notes/index.md", text: "# 索引\n- shop-api.md：订单分页与索引", size: 128, mod: nowIso },
    { name: "notes/shop-api.md", text: "# shop-api\n- 同一秒内订单排序必须加 id 兜底", size: 256, mod: nowIso },
    { name: "tasks/done/ci-cache.md", text: "# CI 缓存提速\n已完成：构建从 9 分钟降到 4 分钟。", size: 180, mod: nowIso },
    { name: "messages.md", text: "# recent\n- 11-27 09:00 [chat:main] 已完成回归", size: 120, mod: nowIso },
  ],
};

// Idle view
T.setMemDreamExpanded(false);
const idle = T.renderMemoryHTML(mockMem, "");
assert.equal(idle.searching, false);
assert.ok(idle.html.includes("最近做梦摘要"), "idle view must include dream summary block");
assert.ok(idle.html.includes("2026-11-27 04:00"), "idle view must format dream timestamp");
assert.ok(idle.html.includes("查看完整日志"), "idle view must include expand toggle button");
assert.ok(idle.html.includes('data-ins-group="explicit"') && idle.html.includes("你亲口说的"), "must show explicit instructions group");
assert.ok(idle.html.includes('data-ins-group="inferred"') && idle.html.includes("Collie 从对话中推断的"), "must show inferred instructions group");
assert.ok(idle.html.includes('data-act="mem-ins-del"') && idle.html.includes('data-act="mem-ins-edit"'), "inferred items must offer quick edit and delete actions");
const idleDreamRegion = idle.html.slice(idle.html.indexOf('class="mem-dream-sum"'), idle.html.indexOf('class="mem-load"'));
assert.ok(!idleDreamRegion.includes("第 4 条不应挤进前 3 要点"), "collapsed dream summary must not dump 4th point");

// Expanded dream summary view
T.setMemDreamExpanded(true);
const expandedDream = T.renderMemoryHTML(mockMem, "");
const expDreamRegion = expandedDream.html.slice(expandedDream.html.indexOf('class="mem-dream-sum"'), expandedDream.html.indexOf('class="mem-load"'));
assert.ok(expandedDream.html.includes("收起日志"), "expanded dream toggle must switch label");
assert.ok(expDreamRegion.includes("第 4 条不应挤进前 3 要点"), "expanded dream view must render full journal");
T.setMemDreamExpanded(false);

// Search across done task + notes + dreams + instructions
const hitDone = T.renderMemoryHTML(mockMem, "缓存提速");
assert.equal(hitDone.searching, true);
assert.equal(hitDone.matchedFiles, 1, "expected 1 matched done task");
assert.ok(hitDone.html.includes('<details class="mem-done" open'), "matching done section must auto-expand");
assert.ok(hitDone.html.includes('<mark class="mem-hl">缓存提速</mark>'), "snippet must highlight matched keyword");
assert.ok(!hitDone.html.includes("mem-status-card"), "status card hides while searching so results stay above the fold");

// Multi-term search matching path + body
const hitMulti = T.renderMemoryHTML(mockMem, "shop-api 兜底");
assert.equal(hitMulti.matchedFiles, 1);
assert.ok(hitMulti.html.includes('data-path="notes/shop-api.md"'), "multi-term search must match across path and body");

// Search matching dream journal
const hitDream = T.renderMemoryHTML(mockMem, "归档 1 个");
assert.equal(hitDream.matchedFiles, 1);
assert.ok(hitDream.html.includes("做梦日志") && hitDream.html.includes('data-mem-key="dream:2026-11-27-0400.md"'), "dream journal search must auto-expand matched dream");

// Empty search result state
const hitNone = T.renderMemoryHTML(mockMem, "不存在的关键词xyz");
assert.equal(hitNone.matchedFiles, 0);
assert.ok(hitNone.html.includes("未找到包含「不存在的关键词xyz」的记忆"), "empty search must show friendly empty state");
assert.ok(hitNone.html.includes('data-act="mem-search-clear"'), "empty search must offer clear button");

// XSS safety: script tags in query or file must be escaped
const xss = T.renderMemoryHTML(mockMem, "<img src=x onerror=alert(1)>");
assert.ok(!xss.html.includes("<img src=x"), "search query must be HTML-escaped in empty state");

// 2e) One-click delete + Undo via #auto-body click delegation
(async () => {
  T.setView("auto");
  T.setAutoTab("memory");
  T.setMemData(mockMem);
  const autoBody = getEl("#auto-body");
  const fakeBox = makeEl("ins-box");
  fakeBox.dataset.path = "instructions.md";
  const fakeRow = makeEl("ins-row");
  const fakeText = makeEl("ins-text");
  fakeText.textContent = "优先在 devbox 上跑测试";
  fakeRow._qs[".mem-ins-text"] = fakeText;
  const delBtn = makeEl("del-btn");
  delBtn.dataset.act = "mem-ins-del";
  delBtn.dataset.line = "2";
  delBtn.dataset.endLine = "3";
  delBtn.closest = (sel) => (sel === "button" ? delBtn : sel === "[data-path]" ? fakeBox : sel === ".mem-ins-item" ? fakeRow : null);

  autoBody.dispatch("click", { target: delBtn });
  await new Promise((r) => setImmediate(r));
  assert.equal(apiPosts.length, 1, "clicking delete must POST /api/super/memory");
  assert.equal(apiPosts[0].path, "instructions.md");
  assert.ok(!apiPosts[0].text.includes("优先在 devbox 上跑测试"), "saved text must remove the inferred line");
  assert.ok(T.getMemInsUndo(), "undo snapshot must be armed after one-click delete");

  // Click undo
  const undoBtn = makeEl("undo-btn");
  undoBtn.dataset.act = "mem-ins-undo";
  undoBtn.closest = (sel) => (sel === "button" ? undoBtn : null);
  autoBody.dispatch("click", { target: undoBtn });
  await new Promise((r) => setImmediate(r));
  assert.equal(apiPosts.length, 2, "clicking undo must POST /api/super/memory");
  assert.equal(apiPosts[1].text, sampleIns, "undo must restore exact original instructions.md");
  assert.equal(T.getMemInsUndo(), null, "undo state must clear after restoring");

  console.log("ALL MEMORY UX TESTS PASSED");
})().catch((err) => {
  console.error(err);
  process.exit(1);
});
`
	cmd := exec.Command("node", "-e", script)
	cmd.Dir = filepath.Join("..", "..")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("node memory ux test failed: %v\n%s", err, out)
	}
}
