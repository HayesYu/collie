package collie

import (
	"os/exec"
	"path/filepath"
	"testing"
)

func TestUnifiedAttachmentCardsVideoAndFiles(t *testing.T) {
	t.Parallel()
	nodeBin, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node not installed; skipping unified attachment cards JS tests")
	}

	root := filepath.Join("..", "..")
	jsTest := `
const assert = require("node:assert/strict");
const fs = require("node:fs");
const path = require("node:path");
const vm = require("node:vm");

const root = process.argv[1];
const appSrc = fs.readFileSync(path.join(root, "web/app.js"), "utf8");
const cssSrc = fs.readFileSync(path.join(root, "web/app.css"), "utf8");
const i18nSrc = fs.readFileSync(path.join(root, "web/i18n.js"), "utf8");

// 1. Static CSS & i18n contracts
assert.ok(cssSrc.includes("--vid-max-h: 360px;"), "desktop video card must cap max-height at 360px");
assert.ok(cssSrc.includes("--vid-max-h: 320px;"), "mobile video card must cap max-height at 320px");
assert.ok(cssSrc.includes(".att-video-card {") && cssSrc.includes("border-radius: 10px;") && cssSrc.includes("border: 1px solid var(--line);"), "video card must use 10px radius and 1px hairline border");
assert.ok(cssSrc.includes("color-scheme: dark;"), "video card must enforce dark color-scheme on explicit dark surface");
assert.ok(cssSrc.includes(".att-video-wrap, .att-video-card, .msg-files,"), "assistant entry motion must skip blur on video & file cards");
assert.ok(cssSrc.includes(".mine[data-enter=\"user\"] > :is(.re, .mine-media, .mine-videos, .att, .msg.user)"), "user entry motion must animate detached video & file cards cleanly");
for (const k of ["file.videoAria", "file.videoTooBig", "file.videoCantPlay", "file.videoOpenTip", "file.videoDownloadTip"]) {
  const hits = i18nSrc.match(new RegExp('"' + k.replace(".", "\\.") + '"', "g")) || [];
  assert.equal(hits.length, 2, "zh & en i18n must both define " + k);
}

// 2. Minimal DOM harness for renderSuper, renderEntryArtifacts, parseUserParts, userText, hydrateVideoEl, openFile
let bootFinished = false;
const docListeners = new Map();
const winListeners = new Map();
const byId = new Map();
let fetchHandler = async () => ({ ok: false, status: 404, headers: { get: () => "" }, blob: async () => new Blob([]), json: async () => ({ error: "not found" }) });

function attrToDatasetKey(name) {
  return name.slice(5).replace(/-([a-z])/g, (_, c) => c.toUpperCase());
}
function datasetKeyToAttr(key) {
  return "data-" + String(key).replace(/[A-Z]/g, (c) => "-" + c.toLowerCase());
}
function makeEl(tag = "div") {
  const attrs = new Map();
  const listeners = new Map();
  const classSet = new Set();
  const rawDataset = {};
  const dataset = new Proxy(rawDataset, {
    get(t, k) { return t[k]; },
    set(t, k, v) { t[k] = String(v); attrs.set(datasetKeyToAttr(k), String(v)); return true; },
    deleteProperty(t, k) { delete t[k]; attrs.delete(datasetKeyToAttr(k)); return true; },
  });
  const el = {
    tagName: String(tag).toUpperCase(),
    localName: String(tag).toLowerCase(),
    nodeType: 1,
    hidden: false,
    disabled: false,
    href: "",
    _src: "",
    _html: "",
    _text: "",
    width: 0,
    height: 0,
    videoWidth: 0,
    videoHeight: 0,
    children: [],
    parentNode: null,
    parentElement: null,
    dataset,
    style: {
      _props: new Map(),
      setProperty(k, v) { this._props.set(k, String(v)); },
      getPropertyValue(k) { return this._props.get(k) || ""; },
      removeProperty(k) { this._props.delete(k); },
    },
    get src() { return this._src; },
    set src(v) {
      this._src = String(v || "");
      if (this._src) attrs.set("src", this._src);
      else attrs.delete("src");
    },
    get className() { return [...classSet].join(" "); },
    set className(v) {
      classSet.clear();
      for (const c of String(v || "").trim().split(/\s+/).filter(Boolean)) classSet.add(c);
    },
    classList: {
      add(...cs) { for (const c of cs) if (c) classSet.add(c); },
      remove(...cs) { for (const c of cs) classSet.delete(c); },
      toggle(c, force) {
        if (force === undefined) { if (classSet.has(c)) { classSet.delete(c); return false; } classSet.add(c); return true; }
        if (force) { classSet.add(c); return true; }
        classSet.delete(c); return false;
      },
      contains(c) { return classSet.has(c); },
    },
    get isConnected() { return true; },
    get childNodes() { return this.children; },
    get childElementCount() { return this.children.length; },
    get firstChild() { return this.children[0] || null; },
    get nextSibling() {
      const p = this.parentNode;
      if (!p) return null;
      const idx = p.children.indexOf(this);
      return idx >= 0 ? (p.children[idx + 1] || null) : null;
    },
    get innerHTML() { return this._html; },
    set innerHTML(v) { this._html = String(v || ""); this.children = []; },
    get textContent() { return this._text; },
    set textContent(v) { this._text = String(v || ""); },
    setAttribute(k, v) {
      const lk = String(k).toLowerCase();
      attrs.set(lk, String(v));
      if (lk === "id") byId.set(String(v), el);
      else if (lk === "class") el.className = String(v);
      else if (lk === "src") el._src = String(v);
      else if (lk === "hidden") el.hidden = true;
      else if (lk.startsWith("data-")) rawDataset[attrToDatasetKey(lk)] = String(v);
    },
    getAttribute(k) {
      const lk = String(k).toLowerCase();
      if (lk === "src") return el._src || null;
      return attrs.has(lk) ? attrs.get(lk) : null;
    },
    hasAttribute(k) { return attrs.has(String(k).toLowerCase()); },
    removeAttribute(k) {
      const lk = String(k).toLowerCase();
      attrs.delete(lk);
      if (lk === "src") el._src = "";
      if (lk === "hidden") el.hidden = false;
      if (lk.startsWith("data-")) delete rawDataset[attrToDatasetKey(lk)];
    },
    appendChild(child) {
      if (!child) return child;
      if (child.parentNode) child.remove();
      child.parentNode = el;
      child.parentElement = el;
      el.children.push(child);
      return child;
    },
    append(...nodes) { for (const n of nodes) if (n && typeof n === "object") el.appendChild(n); },
    insertBefore(child, ref) {
      if (!child) return child;
      if (child.parentNode) child.remove();
      child.parentNode = el;
      child.parentElement = el;
      const idx = ref ? el.children.indexOf(ref) : -1;
      if (idx >= 0) el.children.splice(idx, 0, child);
      else el.children.push(child);
      return child;
    },
    remove() {
      const p = el.parentNode;
      if (!p) return;
      const idx = p.children.indexOf(el);
      if (idx >= 0) p.children.splice(idx, 1);
      el.parentNode = null;
      el.parentElement = null;
    },
    matches(sel) {
      for (const part of String(sel).split(",").map((s) => s.trim()).filter(Boolean)) {
        if (matchesSimple(el, part)) return true;
      }
      return false;
    },
    closest(sel) {
      let cur = el;
      while (cur) {
        if (cur.matches(sel)) return cur;
        cur = cur.parentElement;
      }
      return null;
    },
    querySelectorAll(sel) {
      const out = [];
      const parts = String(sel).split(",").map((s) => s.trim()).filter(Boolean);
      const walk = (node) => {
        for (const ch of node.children) {
          for (const p of parts) {
            if (matchesChain(ch, p, el)) { out.push(ch); break; }
          }
          walk(ch);
        }
      };
      walk(el);
      return out;
    },
    querySelector(sel) { return el.querySelectorAll(sel)[0] || (!bootFinished ? makeEl("div") : null); },
    addEventListener(ev, fn) {
      if (!listeners.has(ev)) listeners.set(ev, []);
      listeners.get(ev).push(fn);
    },
    removeEventListener(ev, fn) {
      const arr = listeners.get(ev);
      if (!arr) return;
      const i = arr.indexOf(fn);
      if (i >= 0) arr.splice(i, 1);
    },
    dispatchEvent(ev) {
      const e = { target: el, currentTarget: el, preventDefault() { this.defaultPrevented = true; }, ...ev };
      for (const fn of [...(listeners.get(e.type) || [])]) fn(e);
      return !e.defaultPrevented;
    },
    focus() {},
    blur() {},
    getBoundingClientRect: () => ({ left: 0, top: 0, width: 400, height: 300, right: 400, bottom: 300 }),
  };
  return el;
}
function matchesSimple(el, sel) {
  let s = sel.trim();
  if (!s) return false;
  const tagMatch = s.match(/^[a-zA-Z0-9_-]+/);
  if (tagMatch) {
    if (el.localName !== tagMatch[0].toLowerCase()) return false;
    s = s.slice(tagMatch[0].length);
  }
  while (s.length) {
    if (s[0] === ".") {
      const m = s.match(/^\.([A-Za-z0-9_-]+)/);
      if (!m || !el.classList.contains(m[1])) return false;
      s = s.slice(m[0].length);
    } else if (s[0] === "#") {
      const m = s.match(/^#([A-Za-z0-9_-]+)/);
      if (!m || el.getAttribute("id") !== m[1]) return false;
      s = s.slice(m[0].length);
    } else if (s[0] === "[") {
      const m = s.match(/^\[([A-Za-z0-9_-]+)(?:="([^"]*)")?\]/);
      if (!m) return false;
      const k = m[1].toLowerCase();
      if (m[2] !== undefined) {
        if (el.getAttribute(k) !== m[2]) return false;
      } else {
        if (!el.hasAttribute(k)) return false;
      }
      s = s.slice(m[0].length);
    } else {
      return false;
    }
  }
  return true;
}
function matchesChain(el, chain, stopRoot) {
  const segs = chain.trim().split(/\s+/).filter(Boolean);
  if (!segs.length) return false;
  if (!matchesSimple(el, segs[segs.length - 1])) return false;
  let cur = el.parentElement;
  for (let i = segs.length - 2; i >= 0; i--) {
    let found = false;
    while (cur && cur !== stopRoot) {
      if (matchesSimple(cur, segs[i])) { found = true; cur = cur.parentElement; break; }
      cur = cur.parentElement;
    }
    if (!found) return false;
  }
  return true;
}
function parseHtmlInto(parent, html) {
  parent.children = [];
  const stack = [parent];
  const re = /<\/([a-zA-Z0-9-]+)>|<([a-zA-Z0-9-]+)([^>]*)>|([^<]+)/g;
  const voids = new Set(["img", "br", "hr", "input", "meta", "link", "source"]);
  let m;
  while ((m = re.exec(html)) !== null) {
    if (m[1]) {
      if (stack.length > 1) stack.pop();
    } else if (m[2]) {
      const tag = m[2].toLowerCase();
      const attrStr = m[3] || "";
      const selfClose = attrStr.trim().endsWith("/") || voids.has(tag);
      const node = makeEl(tag);
      const attrRe = /([A-Za-z0-9:_-]+)(?:="([^"]*)")?/g;
      let am;
      while ((am = attrRe.exec(attrStr)) !== null) {
        if (am[1] === "/") continue;
        const val = am[2] !== undefined ? am[2].replace(/&quot;/g, '"').replace(/&amp;/g, "&").replace(/&lt;/g, "<").replace(/&gt;/g, ">") : "";
        node.setAttribute(am[1], val);
      }
      stack[stack.length - 1].appendChild(node);
      if (!selfClose) stack.push(node);
    } else if (m[4]) {
      stack[stack.length - 1]._text += m[4];
    }
  }
}

const bodyEl = makeEl("body");
const headEl = makeEl("head");
const docEl = makeEl("html");
docEl.append(headEl, bodyEl);
const superThreadEl = makeEl("div"); superThreadEl.setAttribute("id", "super-thread");
const fvEl = makeEl("section"); fvEl.setAttribute("id", "file-view"); fvEl.hidden = true;
const fvNameEl = makeEl("div"); fvNameEl.setAttribute("id", "fv-name");
const fvPathEl = makeEl("div"); fvPathEl.setAttribute("id", "fv-path");
const fvBodyEl = makeEl("div"); fvBodyEl.setAttribute("id", "fv-body");
fvEl.append(fvNameEl, fvPathEl, fvBodyEl);
bodyEl.append(superThreadEl, fvEl);

function getById(id) {
  if (!byId.has(id)) {
    const el = makeEl("div");
    el.setAttribute("id", id);
    bodyEl.appendChild(el);
  }
  return byId.get(id);
}

const doc = {
  documentElement: docEl,
  head: headEl,
  body: bodyEl,
  hidden: false,
  activeElement: null,
  createElement: (tag) => makeEl(tag),
  createDocumentFragment: () => makeEl("#fragment"),
  getElementById: (id) => getById(id),
  querySelector: (sel) => sel.startsWith("#") ? getById(sel.slice(1)) : (bodyEl.querySelector(sel) || (!bootFinished ? makeEl("div") : null)),
  querySelectorAll: (sel) => bodyEl.querySelectorAll(sel),
  addEventListener(ev, fn) {
    if (!docListeners.has(ev)) docListeners.set(ev, []);
    docListeners.get(ev).push(fn);
  },
  removeEventListener() {},
  dispatchEvent(ev) {
    for (const fn of [...(docListeners.get(ev.type) || [])]) fn(ev);
  },
  hasFocus: () => true,
};

const ctx = {
  console: { log() {}, info() {}, warn() {}, error() {} },
  document: doc,
  window: null,
  location: { hash: "", search: "", pathname: "/", origin: "http://127.0.0.1", hostname: "127.0.0.1", href: "http://127.0.0.1/" },
  history: { state: {}, replaceState() {}, back() {} },
  navigator: { userAgent: "SafariTest" },
  localStorage: { getItem: () => null, setItem() {} },
  matchMedia: () => ({ matches: false, addEventListener() {} }),
  MutationObserver: class { observe() {} },
  EventSource: class { addEventListener() {} close() {} },
  CSS: { escape: (s) => String(s), supports: () => false },
  URL: { createObjectURL: () => "blob:test/1", revokeObjectURL() {} },
  URLSearchParams,
  Blob,
  performance: { now: () => 0 },
  requestAnimationFrame: (fn) => { fn(); return 0; },
  cancelAnimationFrame() {},
  setTimeout: (fn) => { if (typeof fn === "function") fn(); return 0; },
  clearTimeout() {},
  setInterval: () => 0,
  clearInterval() {},
  addEventListener(ev, fn) {
    if (!winListeners.has(ev)) winListeners.set(ev, []);
    winListeners.get(ev).push(fn);
  },
  removeEventListener() {},
  dispatchEvent(ev) {
    for (const fn of [...(winListeners.get(ev?.type) || [])]) fn(ev);
  },
  Event,
  HTMLDialogElement: { prototype: {} },
  getComputedStyle: () => ({ getPropertyValue: () => "" }),
  fetch: (...args) => fetchHandler(...args),
  innerWidth: 1440,
  innerHeight: 900,
};
ctx.window = ctx;
ctx.globalThis = ctx;

const marker = "  boot();\n})();";
const instrumented = appSrc.replace(
  marker,
  "  globalThis.__att = { setView: (v) => { view = v; }, setSup: (s) => { sup = normSup(s); }, renderSuper, parseUserParts, renderUserAttachments, userText, renderEntryArtifacts, extractEntryArtifacts, hydrateVideoEl, observeVideos, degradeVideoCard, failedMediaVideos, clearMediaVideoFailed, videoDims, openFile, threadEntryPlainText };\n})();"
);
vm.runInNewContext(instrumented, ctx, { filename: "web/app.js" });
bootFinished = true;
const A = ctx.__att;

async function main() {
  // ─── Case 1: User video attachments (multi-line, single-line, all 4 video extensions, Safari attributes, zero raw path leak) ───
  for (const ext of ["mp4", "mov", "webm", "m4v", "MP4", "MOV"]) {
    const p = "/home/alex/.collie/uploads/20261128/ec2cd5-ScreenRecording_11-28." + ext;
    const ml = A.parseUserParts("附件：\n" + p);
    assert.deepEqual([...ml.videos], [p], "multi-line " + ext + " extracted as video");
    assert.equal(ml.hasBubbleContent, false, "video-only user message has no text bubble");
    assert.ok(ml.videoHtml.includes('class="att-video-wrap mine-videos"'), ext + " wraps video card in mine-videos");
    assert.ok(ml.videoHtml.includes('controls playsinline webkit-playsinline preload="metadata" loading="lazy" width="640" height="360"'), ext + " sets Safari playsinline + metadata + lazy + explicit dims");
    assert.ok(ml.videoHtml.includes('aria-label="视频 ScreenRecording_11-28.' + ext + '"'), ext + " strips 6-hex upload prefix in aria-label");
    assert.ok(!ml.textHtml.includes(p), ext + " never leaks raw path into textHtml");

    const sl = A.parseUserParts("附件：" + p);
    assert.deepEqual([...sl.videos], [p], "single-line 附件：/path " + ext + " extracted as video");
    assert.equal(sl.hasBubbleContent, false, "single-line video-only has no text bubble");
  }

  // Non-path prose mentioning "附件：" must NOT be stripped
  const proseKeep = A.parseUserParts("请看说明\n附件：请先备份数据库");
  assert.equal(proseKeep.videos.length, 0);
  assert.equal(proseKeep.files.length, 0);
  assert.ok(proseKeep.textHtml.includes("请先备份数据库"), "prose non-path line after 附件： stays intact");

  // User text + video + [The human picked ...] suffix renders cleanly in #super-thread
  const nowIso = new Date().toISOString();
  A.setView("collie");
  A.setSup({
    router: { status: "idle", alive: true },
    entries: [
      { id: "u-vid-only", role: "user", text: "附件：/home/alex/.collie/uploads/20261128/ec2cd5-ScreenRecording_demo.mp4", at: nowIso },
      { id: "u-vid-txt", role: "user", text: "录屏给你看\n\n附件：\n/home/alex/.collie/uploads/20261128/ec2cd5-ScreenRecording_demo.mp4\n\n[The human picked where this runs: spawn]", at: nowIso },
      { id: "u-file-only", role: "user", text: "附件：\n/home/alex/.collie/uploads/20261128/a1b2c3-spec.pdf\n/home/alex/.collie/uploads/20261128/b2c3d4-archive.zip", at: nowIso },
    ],
    workers: [],
  });
  A.renderSuper({ force: true });
  const superHtml = superThreadEl.innerHTML;
  assert.ok(superHtml.includes('<div class="msg user is-media-only" data-id="u-vid-only"'), "video-only user message renders in is-media-only without black bubble");
  const vidTxtBubble = superHtml.match(/<div class="msg user" data-id="u-vid-txt"[^>]*>([\s\S]*?)<\/div>/);
  assert.ok(vidTxtBubble && vidTxtBubble[1].includes("录屏给你看"), "user caption stays in bubble");
  assert.ok(!vidTxtBubble[1].includes("ec2cd5-ScreenRecording") && !vidTxtBubble[1].includes("附件：") && !vidTxtBubble[1].includes("The human picked"), "user black bubble must NEVER print raw video path or system suffix");
  assert.ok(superHtml.includes('<div class="msg user is-media-only" data-id="u-file-only"'), "file-only user message renders detached cards without black bubble");
  assert.ok(!superHtml.includes("📎"), "user attachment cards must NOT use raw paperclip emoji");

  // Thread preview strips single-line attachment block too
  assert.equal(A.threadEntryPlainText({ role: "user", text: "附件：/tmp/ec2cd5-demo.mp4" }), "附件消息");
  assert.equal(A.threadEntryPlainText({ role: "user", text: "请看录屏\n\n附件：/tmp/ec2cd5-demo.mp4" }), "请看录屏");

  // ─── Case 2: Unified file card parity between User & Assistant (category icon + clean name + right badge) ───
  const userFiles = A.parseUserParts("看文档\n\n附件：\n/tmp/a1b2c3-design.pdf\n/tmp/b2c3d4-bundle.zip\n/tmp/c3d4e5-voice.mp3\n/tmp/d4e5f6-config.json\n/tmp/e5f607-main.go");
  assert.equal(userFiles.files.length, 5);
  assert.ok(userFiles.attHtml.includes('class="att"'), "preserves .att wrapper class");
  assert.ok(userFiles.attHtml.includes('class="snap-chip" data-kind="doc" data-open-file="/tmp/a1b2c3-design.pdf"'), "user PDF renders unified snap-chip with doc icon");
  assert.ok(userFiles.attHtml.includes('<span class="snap-chip-name">design.pdf</span><span class="snap-chip-ext">PDF</span>'), "user PDF shows stripped name then right PDF badge");
  assert.ok(userFiles.attHtml.includes('data-kind="archive"') && userFiles.attHtml.includes('>ZIP<'), "user ZIP has archive icon + ZIP badge");
  assert.ok(userFiles.attHtml.includes('data-kind="audio"') && userFiles.attHtml.includes('>MP3<'), "user MP3 has audio icon + MP3 badge");
  assert.ok(userFiles.attHtml.includes('data-kind="sheet"') && userFiles.attHtml.includes('>JSON<'), "user JSON has sheet/data icon + JSON badge");
  assert.ok(userFiles.attHtml.includes('data-kind="code"') && userFiles.attHtml.includes('>GO<'), "user GO has code icon + GO badge");
  assert.ok(!userFiles.attHtml.includes("📎"), "no raw paperclip emoji in unified file chips");
  assert.ok(!userFiles.attHtml.includes(">a1b2c3-") && !userFiles.attHtml.includes('aria-label="预览 a1b2c3-'), "visible filename & aria-label strip 6-hex upload prefix");

  const asstFiles = A.renderEntryArtifacts({
    id: "c-files",
    role: "collie",
    text: "整理好了交付物。",
    files: ["~/docs/a1b2c3-design.pdf", "~/docs/summary.md", "~/out/bundle.zip", "~/audio/rec.mp3"],
  }, "");
  assert.ok(asstFiles.includes('class="msg-files"') && !asstFiles.includes("snap-card"), "assistant non-HTML generic files render as unified msg-files chips, not heavy snap-card");
  assert.ok(asstFiles.includes('class="snap-chip" data-kind="doc" data-open-file="~/docs/a1b2c3-design.pdf"'), "assistant PDF uses exact same unified snap-chip as user");
  assert.ok(asstFiles.includes('<span class="snap-chip-name">design.pdf</span><span class="snap-chip-ext">PDF</span>'), "assistant strips 6-hex prefix and places badge on right");
  assert.ok(asstFiles.includes('<span class="snap-chip-name">summary.md</span><span class="snap-chip-ext">MD</span>'), "assistant MD chip unified");
  assert.ok(asstFiles.includes('data-kind="archive"') && asstFiles.includes('>ZIP<'), "assistant ZIP chip unified");
  assert.ok(asstFiles.includes('data-kind="audio"') && asstFiles.includes('>MP3<'), "assistant MP3 chip unified");

  // Topic drawer userText renders unified video & file cards cleanly
  const tdUser = A.userText("同步录屏和附件\n\n附件：\n/tmp/ec2cd5-clip.mp4\n/tmp/a1b2c3-spec.pdf");
  assert.ok(tdUser.includes("同步录屏和附件") && tdUser.includes("att-video-card") && tdUser.includes('class="snap-chip"') && !tdUser.includes("📎"), "thread drawer userText uses unified video & file cards");

  // ─── Case 3: Assistant video & mixed image + video + html + doc deduplication ───
  const asstVid = A.renderEntryArtifacts({
    id: "c-vid",
    role: "collie",
    text: "这是实测录屏：",
    files: ["~/clips/ec2cd5-ui-flow.mp4"],
  }, "");
  assert.ok(asstVid.includes('class="att-video-wrap file-videos"') && asstVid.includes('class="att-video-card"'), "assistant --file .mp4 renders unified inline video card");
  assert.ok(asstVid.includes('playsinline webkit-playsinline preload="metadata" loading="lazy"'), "assistant video sets Safari playsinline + metadata + lazy");
  assert.ok(!asstVid.includes("snap-card"), "assistant video does not fall back to heavy snap-card");

  // Mixed user attachments deduplicate repeated paths across types
  const userMix = A.parseUserParts("全量素材\n\n附件：\n/tmp/1.png\n/tmp/1.png\n/tmp/2.mp4\n/tmp/2.mp4\n/tmp/3.pdf\n/tmp/3.pdf");
  assert.equal(userMix.imgs.length, 1);
  assert.equal(userMix.videos.length, 1);
  assert.equal(userMix.files.length, 1);

  // Mixed assistant attachments: image gallery + video card + HTML sandbox + doc chips (no duplicates)
  const asstMix = A.renderEntryArtifacts({
    id: "c-mix",
    role: "collie",
    text: "附上图表 ~/reports/bench.html、录屏、截图与文档",
    files: ["~/shots/s1.png", "~/shots/s2.png", "~/clips/demo.mov", "~/reports/bench.html", "~/docs/report.pdf", "~/docs/notes.md", "~/clips/demo.mov"],
  }, "");
  assert.ok(asstMix.includes('class="file-gallery is-2" data-count="2"'), "mixed assistant keeps 2-column image gallery");
  assert.equal((asstMix.match(/class="att-video-card"/g) || []).length, 1, "mixed assistant deduplicates video cards");
  assert.ok(asstMix.includes('data-rv-kind="html-file" data-rv-path="~/reports/bench.html"'), "mixed assistant preserves HTML sandbox card");
  assert.ok(asstMix.includes('data-open-file="~/docs/report.pdf"') && asstMix.includes('data-open-file="~/docs/notes.md"'), "mixed assistant renders companion PDF/MD chips");
  assert.ok(!asstMix.includes('data-kind="video" data-open-file="~/clips/demo.mov"'), "healthy inline video is not duplicated as file chip");

  // ─── Case 4: Video lazy hydration, metadata dims (0 CLS), & graceful degradation on error ───
  const host = makeEl("div");
  bodyEl.appendChild(host);
  parseHtmlInto(host, asstVid);
  const vidEl = host.querySelector("video.att-video-el");
  assert.ok(vidEl, "parsed video element");
  assert.equal(vidEl.getAttribute("src"), null, "video starts without eager src until observed/hydrated");
  A.hydrateVideoEl(vidEl);
  assert.equal(vidEl.src, "/api/file?path=~%2Fclips%2Fec2cd5-ui-flow.mp4&as=video#t=0.001", "hydrateVideoEl binds /api/file?as=video with #t=0.001 first-frame hint");

  // loadedmetadata locks intrinsic dimensions and adds is-ready
  vidEl.videoWidth = 1080;
  vidEl.videoHeight = 1920;
  doc.dispatchEvent({ type: "loadedmetadata", target: vidEl });
  assert.equal(vidEl.width, 1080);
  assert.equal(vidEl.height, 1920);
  assert.ok(host.querySelector(".att-video-card").classList.contains("is-ready"), "loadedmetadata marks video card is-ready");
  const cachedDim = A.videoDims.get("~/clips/ec2cd5-ui-flow.mp4");
  assert.ok(cachedDim && cachedDim.w === 1080 && cachedDim.h === 1920, "caches intrinsic dimensions for 0-CLS re-renders");

  // Video error -> degrades gracefully to unified file chip and hides empty video list
  doc.dispatchEvent({ type: "error", target: vidEl });
  assert.equal(host.querySelectorAll(".att-video-card").length, 0, "failed video card removed cleanly");
  assert.equal(host.querySelector(".att-video-list").hidden, true, "empty video list hidden");
  const fbChip = host.querySelector(".att-video-fallbacks .snap-chip[data-open-file]");
  assert.ok(fbChip && fbChip.dataset.openFile === "~/clips/ec2cd5-ui-flow.mp4" && fbChip.dataset.kind === "video", "failed video degrades to unified video snap-chip");

  // Subsequent re-render remembers failed state directly
  const reVid = A.renderEntryArtifacts({ id: "c-vid-re", role: "collie", text: "重绘", files: ["~/clips/ec2cd5-ui-flow.mp4"] }, "");
  assert.ok(!reVid.includes("att-video-card") && reVid.includes('class="snap-chip" data-kind="video"'), "known failed video renders directly as fallback chip");

  // Self-heal restores video card
  A.clearMediaVideoFailed("~/clips/ec2cd5-ui-flow.mp4");
  const healedVid = A.renderEntryArtifacts({ id: "c-vid-heal", role: "collie", text: "恢复", files: ["~/clips/ec2cd5-ui-flow.mp4"] }, "");
  assert.ok(healedVid.includes('class="att-video-card is-ready"') && healedVid.includes('width="1080" height="1920"'), "healed video card reuses cached dims for 0 CLS");
  host.remove();

  // ─── Case 5: openFile for video uses HEAD probe & mounts inline drawer player ───
  const calls = [];
  fetchHandler = async (url, init) => {
    const u = String(url);
    if (u.includes("/api/file")) calls.push({ url: u, method: init?.method || "GET" });
    return {
      ok: true,
      status: 200,
      headers: { get: (k) => k === "X-File-Kind" ? "video" : k === "X-File-Size" ? "204800" : k === "X-File-Path" ? "~/clips/ec2cd5-ui-flow.mp4" : "" },
    };
  };
  await A.openFile("~/clips/ec2cd5-ui-flow.mp4");
  assert.equal(calls.length, 1, "openFile issues a single /api/file HEAD probe: " + JSON.stringify(calls));
  assert.equal(calls[0].method, "HEAD", "openFile probes video with lightweight HEAD request");
  assert.ok(calls[0].url.includes("as=video"), "HEAD probe targets as=video");
  assert.equal(fvNameEl.textContent, "ui-flow.mp4", "file drawer title strips 6-hex upload prefix");
  assert.ok(fvBodyEl.innerHTML.includes('<div class="fv-video"><video class="att-video-el" controls playsinline'), "file drawer renders inline video player");

  console.log("ALL_UNIFIED_ATTACHMENT_CARD_TESTS_PASSED");
}

main().catch((err) => {
  console.error(err);
  process.exit(1);
});
`
	cmd := exec.Command(nodeBin, "-e", jsTest, root)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("unified attachment cards JS test failed: %v\n%s", err, string(out))
	}
}
