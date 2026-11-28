package collie

import (
	"os/exec"
	"path/filepath"
	"testing"
)

func TestInlineFileImageGalleryAndLightbox(t *testing.T) {
	t.Parallel()
	nodeBin, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node not installed; skipping JS inline image gallery tests")
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

// 1. Static CSS & i18n contract checks
assert.ok(cssSrc.includes("--fg-single-max-h: 380px;"), "desktop single image must cap max-height at 380px");
assert.ok(cssSrc.includes("--fg-single-max-h: 320px;"), "mobile single image must cap max-height at 320px");
assert.ok(cssSrc.includes(".file-gallery:is(.is-2, .is-3, .is-4, .is-many) {\n  grid-template-columns: repeat(2, minmax(0, 1fr));"), "2-4+ file gallery must use compact 2-column equal-height grid");
assert.ok(cssSrc.includes("gap: 6px;") && cssSrc.includes("border-radius: 10px;") && cssSrc.includes("overflow: clip;"), "file gallery tiles must use 6px gap, 10px radius, overflow: clip");
assert.ok(cssSrc.includes("object-fit: cover;\n  object-position: top center;"), "multi-image gallery must crop screenshots with object-fit: cover + object-position: top center");
assert.ok(cssSrc.includes(".file-gallery-wrap, .file-gallery, .md-gallery,"), "message entry blur animation must skip .file-gallery to stay crisp & 60 FPS");
assert.ok(cssSrc.includes("touch-action: none;"), "lightbox stage must disable browser pan hijacking for reliable horizontal touch swipe");
assert.ok(cssSrc.includes(".lb-nav.prev { left: max(8px, env(safe-area-inset-left)); }") && cssSrc.includes(".lb-nav.next { right: max(8px, env(safe-area-inset-right)); }"), "mobile lightbox nav must respect left/right safe areas");
assert.ok(i18nSrc.includes('"file.galMoreAria": "查看大图') && i18nSrc.includes('"file.galMoreAria": "View image'), "zh/en i18n must both define file.galMoreAria");

// 2. Minimal DOM harness supporting querySelector, closest, classList, dataset, events, dialog & fetch
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
    set(t, k, v) {
      t[k] = String(v);
      attrs.set(datasetKeyToAttr(k), String(v));
      return true;
    },
    deleteProperty(t, k) {
      delete t[k];
      attrs.delete(datasetKeyToAttr(k));
      return true;
    },
  });
  const el = {
    tagName: String(tag).toUpperCase(),
    localName: String(tag).toLowerCase(),
    nodeType: 1,
    hidden: false,
    open: false,
    disabled: false,
    tabIndex: 0,
    href: "",
    _src: "",
    _html: "",
    _text: "",
    width: 0,
    height: 0,
    naturalWidth: 0,
    naturalHeight: 0,
    offsetWidth: 400,
    offsetHeight: 300,
    complete: true,
    children: [],
    parentNode: null,
    parentElement: null,
    dataset,
    style: {
      _props: new Map(),
      setProperty(k, v) { this._props.set(k, String(v)); },
      getPropertyValue(k) { return this._props.get(k) || ""; },
      removeProperty(k) { this._props.delete(k); },
      transform: "",
    },
    get src() { return this._src; },
    set src(v) {
      this._src = String(v || "");
      if (this._src) attrs.set("src", this._src);
      else attrs.delete("src");
      if (this._src && typeof this.onload === "function") {
        const fn = this.onload;
        Promise.resolve().then(() => fn());
      }
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
    showModal() { el.open = true; },
    close() {
      if (!el.open) return;
      el.open = false;
      el.dispatchEvent({ type: "close" });
    },
    focus() {},
    blur() {},
    setPointerCapture() {},
    getAnimations: () => [],
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

// Parse simple HTML returned by renderEntryArtifacts into real harness DOM tree
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
docEl.appendChild(headEl);
docEl.appendChild(bodyEl);

// Pre-populate lightbox DOM nodes so openLightbox / swipe / keyboard nav work end-to-end
const lbEl = makeEl("dialog"); lbEl.setAttribute("id", "lightbox"); lbEl.className = "lightbox";
const lbStageEl = makeEl("div"); lbStageEl.setAttribute("id", "lb-stage");
const lbImgEl = makeEl("img"); lbImgEl.setAttribute("id", "lb-img"); lbImgEl.hidden = true;
const lbSvgEl = makeEl("div"); lbSvgEl.setAttribute("id", "lb-svg"); lbSvgEl.hidden = true;
const lbStatusEl = makeEl("div"); lbStatusEl.setAttribute("id", "lb-status"); lbStatusEl.hidden = true;
const lbNameEl = makeEl("h2"); lbNameEl.setAttribute("id", "lb-name");
const lbCountEl = makeEl("span"); lbCountEl.setAttribute("id", "lb-count"); lbCountEl.hidden = true;
const lbMetaEl = makeEl("div"); lbMetaEl.setAttribute("id", "lb-meta");
const lbZoomEl = makeEl("button"); lbZoomEl.setAttribute("id", "lb-zoom");
const lbZoomPctEl = makeEl("span"); lbZoomPctEl.setAttribute("id", "lb-zoom-pct");
const lbOpenEl = makeEl("a"); lbOpenEl.setAttribute("id", "lb-open");
const lbDlEl = makeEl("a"); lbDlEl.setAttribute("id", "lb-dl");
const lbPrevEl = makeEl("button"); lbPrevEl.setAttribute("id", "lb-prev"); lbPrevEl.hidden = true;
const lbNextEl = makeEl("button"); lbNextEl.setAttribute("id", "lb-next"); lbNextEl.hidden = true;
const lbCloseEl = makeEl("button"); lbCloseEl.setAttribute("id", "lb-close");
lbStageEl.append(lbStatusEl, lbImgEl, lbSvgEl);
lbEl.append(lbNameEl, lbCountEl, lbMetaEl, lbZoomEl, lbZoomPctEl, lbOpenEl, lbDlEl, lbStageEl, lbPrevEl, lbNextEl, lbCloseEl);
bodyEl.appendChild(lbEl);

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
  querySelector: (sel) => {
    if (sel.startsWith("#")) return getById(sel.slice(1));
    return bodyEl.querySelector(sel) || (!bootFinished ? makeEl("div") : null);
  },
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

let blobSeq = 0;
const revokedUrls = [];
const MockURL = {
  createObjectURL: () => "blob:test/" + (++blobSeq),
  revokeObjectURL: (u) => revokedUrls.push(u),
};

const ctx = {
  console: { log() {}, info() {}, warn() {}, error() {} },
  document: doc,
  window: null,
  location: { hash: "", search: "", pathname: "/", origin: "http://127.0.0.1", hostname: "127.0.0.1", href: "http://127.0.0.1/" },
  history: { state: {}, replaceState() {}, back() {} },
  navigator: { userAgent: "NodeTest" },
  localStorage: { getItem: () => null, setItem() {} },
  matchMedia: () => ({ matches: false, addEventListener() {} }),
  MutationObserver: class { observe() {} },
  EventSource: class { addEventListener() {} close() {} },
  CSS: { escape: (s) => String(s), supports: () => false },
  URL: MockURL,
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
assert.ok(appSrc.includes(marker), "expected boot() tail in web/app.js");
const instrumented = appSrc.replace(
  marker,
  "  globalThis.__ig = { renderEntryArtifacts, renderCollieBody, extractEntryArtifacts, loadThumb, degradeFileGalleryTile, collectGallery, openLightbox, closeLightbox, showLbSlide, thumbs, failedFileGalleryImgs, clearFileGalleryImgFailed, getLbState: () => ({ open: lightbox.open, idx: lbIdx, count: lbItems.length, items: lbItems, countText: lbCount.textContent }) };\n})();"
);
vm.runInNewContext(instrumented, ctx, { filename: "web/app.js" });
bootFinished = true;
const G = ctx.__ig;

async function main() {
  // ─── Case 1: Single explicit image across all supported extensions ───
  for (const ext of ["png", "jpg", "jpeg", "webp", "gif", "bmp", "svg", "PNG", "JPG"]) {
    const html = G.renderEntryArtifacts({ id: "e-ext-" + ext, role: "collie", text: "截图好了", files: ["~/shots/demo." + ext] }, "");
    assert.ok(html.includes('class="file-gallery is-1" data-count="1"'), ext + " must render 1-image inline gallery");
    assert.ok(html.includes('loading="lazy" decoding="async"'), ext + " must include lazy + async decoding");
    assert.ok(html.includes('class="gal-tile thumb file-gal-tile"') && html.includes('data-img="~/shots/demo.' + ext + '"'), ext + " tile must bind thumb/gallery data attributes");
    assert.ok(!html.includes("snap-card") && !html.includes("gal-more"), ext + " single image must not render snap-card or +N badge");
  }

  // Explicit upload path works; router/memory remains filtered
  const upHtml = G.renderEntryArtifacts({ id: "e-up", role: "collie", text: "发图", files: ["~/.collie/uploads/20261128/pic.png", "~/.collie/router/memory/secret.png"] }, "");
  assert.ok(upHtml.includes('data-img="~/.collie/uploads/20261128/pic.png"') && !upHtml.includes("secret.png"), "explicit uploads image renders while router/memory stays filtered");

  // ─── Case 2: 2 images ───
  const g2 = G.renderEntryArtifacts({ id: "e-2", role: "collie", text: "双图对比", files: ["~/shots/m1.png", "~/shots/m2.png"] }, "");
  assert.ok(g2.includes('class="file-gallery is-2" data-count="2"'), "2 images -> is-2");
  assert.equal((g2.match(/class="gal-tile thumb file-gal-tile"/g) || []).length, 2, "2 images render 2 tiles");
  assert.ok(!g2.includes("gal-more") && !g2.includes("gal-hidden-item"), "2 images must have no +N or hidden items");

  // ─── Case 3: 3 images ───
  const g3 = G.renderEntryArtifacts({ id: "e-3", role: "collie", text: "三图", files: ["~/shots/a.png", "~/shots/b.jpg", "~/shots/c.webp"] }, "");
  assert.ok(g3.includes('class="file-gallery is-3" data-count="3"'), "3 images -> is-3");
  assert.equal((g3.match(/class="gal-tile thumb file-gal-tile"/g) || []).length, 3, "3 images render 3 tiles");

  // ─── Case 4: 4 images ───
  const g4 = G.renderEntryArtifacts({ id: "e-4", role: "collie", text: "四图验收", files: ["~/shots/1.png", "~/shots/2.png", "~/shots/3.png", "~/shots/4.png"] }, "");
  assert.ok(g4.includes('class="file-gallery is-4" data-count="4"'), "4 images -> is-4");
  assert.equal((g4.match(/class="gal-tile thumb file-gal-tile"/g) || []).length, 4, "4 images render 4 tiles");
  assert.ok(!g4.includes("gal-more") && !g4.includes("gal-hidden-item"), "4 images must have no +N overlay");

  // ─── Case 5: 5 images & 6 images (+N on 4th tile + hidden items for Lightbox) ───
  const files5 = [1, 2, 3, 4, 5].map((n) => "~/shots/p" + n + ".png");
  const g5 = G.renderEntryArtifacts({ id: "e-5", role: "collie", text: "五图合集", files: files5 }, "");
  assert.ok(g5.includes('class="file-gallery is-many" data-count="5"'), "5 images -> is-many with data-count=5");
  assert.equal((g5.match(/class="gal-tile thumb file-gal-tile"/g) || []).length, 4, "5 images must render exactly 4 visible tiles");
  assert.ok(g5.includes('<span class="gal-more" aria-hidden="true">+1</span>'), "4th tile must show +1 badge");
  assert.equal((g5.match(/class="gal-hidden-item"/g) || []).length, 1, "5th image preserved as hidden item for Lightbox");

  const files6 = [1, 2, 3, 4, 5, 6].map((n) => "~/shots/q" + n + ".png");
  const g6 = G.renderEntryArtifacts({ id: "e-6", role: "collie", text: "六图合集", files: files6 }, "");
  assert.ok(g6.includes('class="file-gallery is-many" data-count="6"') && g6.includes('<span class="gal-more" aria-hidden="true">+2</span>'), "6 images must show +2 on 4th tile");
  assert.equal((g6.match(/class="gal-hidden-item"/g) || []).length, 2, "6 images preserve 2 hidden items");

  // ─── Case 6: Mixed images + docs + explicit HTML + prose path deduplication ───
  const mixedDoc = G.renderEntryArtifacts({
    id: "e-mix-doc",
    role: "collie",
    text: "附截图与日志",
    files: ["~/shots/a.png", "~/shots/b.png", "~/docs/summary.md", "~/docs/metrics.csv"],
  }, "");
  assert.ok(mixedDoc.includes('class="file-gallery is-2" data-count="2"'), "mixed images+docs renders inline image gallery");
  assert.ok(mixedDoc.includes('class="msg-files"') && mixedDoc.includes('data-open-file="~/docs/summary.md"') && mixedDoc.includes('data-open-file="~/docs/metrics.csv"'), "companion non-image docs render as clean file chips");
  assert.ok(!mixedDoc.includes("snap-card"), "mixed images+docs must not wrap docs in heavy snap-card");

  const mixedHtml = G.renderEntryArtifacts({
    id: "e-mix-html",
    role: "collie",
    text: "截图+交互报告+表格",
    files: ["~/shots/a.png", "~/reports/bench.html", "~/docs/data.csv"],
  }, "");
  assert.ok(mixedHtml.includes('class="file-gallery is-1" data-count="1"'), "mixed images+html renders image gallery");
  assert.ok(mixedHtml.includes('data-rv-kind="html-file"') && mixedHtml.includes('data-rv-path="~/reports/bench.html"'), "explicit .html still renders sandbox card");
  assert.ok(mixedHtml.includes('data-open-file="~/docs/data.csv"'), "companion csv still renders as file chip");

  // Deduplication between prose bare image path and explicit --file image
  const bodyDedup = G.renderCollieBody("验收图见 ~/shots/a.png 与 ~/shots/other.png", ["~/shots/a.png"]);
  assert.ok(bodyDedup.includes('data-path="~/shots/a.png"'), "prose link to a.png stays clickable");
  assert.ok(!bodyDedup.includes('data-img="~/shots/a.png"'), "a.png already in e.files must NOT render duplicate trailing md-gallery");
  assert.ok(bodyDedup.includes('data-img="~/shots/other.png"'), "non-attached prose image other.png still renders");

  // ─── Case 7: Lightbox collects ALL 5 images, supports ArrowRight/ArrowLeft, prev/next, & horizontal touch swipe ───
  const host5 = makeEl("div");
  bodyEl.appendChild(host5);
  parseHtmlInto(host5, g5);
  const tiles5 = host5.querySelectorAll(".gal-tile");
  assert.equal(tiles5.length, 4);
  const collected = G.collectGallery(tiles5[3], { path: tiles5[3].dataset.img, alt: tiles5[3].dataset.imgAlt });
  assert.equal(collected.items.length, 5, "collectGallery must gather all 5 images including hidden 5th");
  assert.equal(collected.index, 3, "clicking 4th tile (+1) opens Lightbox at index 3");
  assert.deepEqual([...collected.items.map((x) => x.path)], files5, "Lightbox items preserve exact attachment order");

  // Seed mock image responses so Lightbox slides resolve cleanly
  fetchHandler = async (url) => {
    const u = String(url);
    assert.ok(u.includes("/api/file?") && u.includes("as=image"), "image fetch must use /api/file?path=...&as=image");
    if (u.includes("missing") || u.includes("denied")) {
      return { ok: false, status: u.includes("denied") ? 403 : 404, headers: { get: () => "" }, json: async () => ({ error: "denied" }), blob: async () => new Blob([]) };
    }
    if (u.includes("oversized")) {
      return { ok: true, status: 200, headers: { get: (k) => k === "X-File-Kind" ? "binary" : k === "X-File-Size" ? "25000000" : "" }, blob: async () => new Blob([]) };
    }
    return {
      ok: true,
      status: 200,
      headers: { get: (k) => k === "X-File-Kind" ? "image" : k === "X-File-Size" ? "1024" : k === "X-File-Path" ? "~/shots/ok.png" : "" },
      blob: async () => new Blob(["PNGDATA"]),
    };
  };

  G.openLightbox(collected.items, 0);
  await new Promise((r) => setImmediate(r));
  assert.equal(G.getLbState().open, true, "Lightbox opens as modal dialog");
  assert.equal(G.getLbState().countText, "1 / 5", "Lightbox header shows 1 / 5 counter");
  assert.equal(lbPrevEl.hidden, false, "prev button visible for multi-image gallery");
  assert.equal(lbNextEl.hidden, false, "next button visible for multi-image gallery");

  // Keyboard ArrowRight -> 2 / 5
  doc.dispatchEvent({ type: "keydown", key: "ArrowRight", preventDefault() {} });
  await new Promise((r) => setImmediate(r));
  assert.equal(G.getLbState().idx, 1);
  assert.equal(G.getLbState().countText, "2 / 5", "ArrowRight advances to 2 / 5");

  // Touch horizontal swipe left on lbStage -> 3 / 5
  lbStageEl.dispatchEvent({ type: "pointerdown", pointerId: 7, pointerType: "touch", button: 0, clientX: 280, clientY: 400, target: lbImgEl });
  lbStageEl.dispatchEvent({ type: "pointermove", pointerId: 7, pointerType: "touch", clientX: 160, clientY: 405, target: lbImgEl });
  lbStageEl.dispatchEvent({ type: "pointerup", pointerId: 7, pointerType: "touch", clientX: 160, clientY: 405, target: lbImgEl });
  await new Promise((r) => setImmediate(r));
  assert.equal(G.getLbState().idx, 2, "touch left swipe advances to slide 3");
  assert.equal(G.getLbState().countText, "3 / 5", "swipe updates counter to 3 / 5");

  // Touch horizontal swipe right -> back to 2 / 5
  lbStageEl.dispatchEvent({ type: "pointerdown", pointerId: 8, pointerType: "touch", button: 0, clientX: 120, clientY: 400, target: lbImgEl });
  lbStageEl.dispatchEvent({ type: "pointermove", pointerId: 8, pointerType: "touch", clientX: 240, clientY: 402, target: lbImgEl });
  lbStageEl.dispatchEvent({ type: "pointerup", pointerId: 8, pointerType: "touch", clientX: 240, clientY: 402, target: lbImgEl });
  await new Promise((r) => setImmediate(r));
  assert.equal(G.getLbState().idx, 1, "touch right swipe moves back to slide 2");

  // Blank backdrop tap closes Lightbox cleanly
  lbStageEl.dispatchEvent({ type: "pointerdown", pointerId: 9, pointerType: "touch", button: 0, clientX: 20, clientY: 20, target: lbStageEl });
  lbStageEl.dispatchEvent({ type: "pointerup", pointerId: 9, pointerType: "touch", clientX: 20, clientY: 20, target: lbStageEl });
  await new Promise((r) => setImmediate(r));
  assert.equal(G.getLbState().open, false, "tapping blank stage backdrop closes Lightbox");
  host5.remove();

  // ─── Case 8: Graceful degradation on 404 / 403 / non-image / decode error ───
  // 8a) Single image 404 -> hides gallery, renders clean file chip, zero broken tile
  const host1 = makeEl("div");
  bodyEl.appendChild(host1);
  parseHtmlInto(host1, G.renderEntryArtifacts({ id: "e-miss-1", role: "collie", text: "单图丢失", files: ["~/shots/missing-one.png"] }, ""));
  const missImg1 = host1.querySelector(".file-gal-tile img");
  await G.loadThumb(missImg1);
  const gal1 = host1.querySelector(".file-gallery");
  assert.equal(gal1.hidden, true, "single failed image hides empty .file-gallery grid");
  assert.equal(host1.querySelectorAll(".gal-tile").length, 0, "failed tile removed so no broken image icon shows");
  assert.equal(host1.querySelectorAll(".broken").length, 0, "must never leave ugly .broken box for file gallery");
  const chip1 = host1.querySelector(".file-gallery-fallbacks .snap-chip[data-open-file]");
  assert.ok(chip1 && chip1.dataset.openFile === "~/shots/missing-one.png", "failed single image gracefully degrades to snap-chip file strip");
  host1.remove();

  // Re-render remembers failed image path (directly renders fallback chip without skeleton flash)
  const reRenderMiss = G.renderEntryArtifacts({ id: "e-miss-1b", role: "collie", text: "重绘", files: ["~/shots/missing-one.png"] }, "");
  assert.ok(!reRenderMiss.includes('class="file-gallery ') && reRenderMiss.includes('class="snap-chip"') && reRenderMiss.includes('data-open-file="~/shots/missing-one.png"'), "subsequent render directly shows fallback chip for known failed path");

  // 8b) Partial failure in 2-image gallery (1 ok + 1 403 denied) -> healthy image stays as is-1, failed degrades to chip
  const host2 = makeEl("div");
  bodyEl.appendChild(host2);
  parseHtmlInto(host2, G.renderEntryArtifacts({ id: "e-part-2", role: "collie", text: "部分失败", files: ["~/shots/good-a.png", "~/shots/denied-b.jpg"] }, ""));
  const imgs2 = host2.querySelectorAll(".file-gal-tile img");
  await G.loadThumb(imgs2[0]);
  await G.loadThumb(imgs2[1]);
  const gal2 = host2.querySelector(".file-gallery");
  assert.equal(gal2.hidden, false, "gallery stays visible for healthy sibling");
  assert.ok(gal2.classList.contains("is-1") && gal2.dataset.count === "1", "remaining 1 healthy tile reconciles to is-1");
  assert.equal(host2.querySelectorAll(".gal-tile").length, 1);
  assert.ok(host2.querySelector(".gal-tile img").getAttribute("src")?.startsWith("blob:test/"), "healthy tile retains authenticated blob URL");
  const chip2 = host2.querySelector(".file-gallery-fallbacks .snap-chip");
  assert.ok(chip2 && chip2.dataset.openFile === "~/shots/denied-b.jpg", "403 failed tile degrades to snap-chip");
  host2.remove();

  // 8c) Failure on 4th tile (+1) of 5-image gallery -> promotes 5th hidden item into 4th slot, drops +1, reconciles to is-4
  const host5f = makeEl("div");
  bodyEl.appendChild(host5f);
  const f5fail = ["~/shots/ok1.png", "~/shots/ok2.png", "~/shots/ok3.png", "~/shots/oversized4.png", "~/shots/ok5.png"];
  parseHtmlInto(host5f, G.renderEntryArtifacts({ id: "e-5f", role: "collie", text: "第4张超限", files: f5fail }, ""));
  const tile4Img = host5f.querySelectorAll(".file-gal-tile img")[3];
  await G.loadThumb(tile4Img);
  const gal5f = host5f.querySelector(".file-gallery");
  assert.ok(gal5f.classList.contains("is-4") && gal5f.dataset.count === "4", "after 4th fails and 5th promotes, gallery reconciles to is-4");
  assert.equal(gal5f.querySelectorAll(".gal-tile").length, 4, "4 visible tiles remain after promoting hidden 5th");
  assert.equal(gal5f.querySelectorAll(".gal-more").length, 0, "+1 badge cleared once 5th promoted");
  assert.equal(gal5f.querySelectorAll(".gal-tile")[3].dataset.img, "~/shots/ok5.png", "5th image promoted into 4th tile");
  assert.equal(host5f.querySelector(".file-gallery-fallbacks .snap-chip")?.dataset?.openFile, "~/shots/oversized4.png", "oversized binary image degraded cleanly to chip");
  host5f.remove();

  // 8d) Browser <img> decode error event (corrupt image bytes after 200 OK) -> revokes blob and degrades to chip
  const hostErr = makeEl("div");
  bodyEl.appendChild(hostErr);
  parseHtmlInto(hostErr, G.renderEntryArtifacts({ id: "e-corrupt", role: "collie", text: "坏图", files: ["~/shots/corrupt.webp"] }, ""));
  const cImg = hostErr.querySelector(".file-gal-tile img");
  await G.loadThumb(cImg);
  const badUrl = cImg.getAttribute("src");
  assert.ok(badUrl && badUrl.startsWith("blob:test/"), "corrupt file initially received blob URL");
  doc.dispatchEvent({ type: "error", target: cImg });
  assert.ok(revokedUrls.includes(badUrl), "decode error revokes broken blob URL");
  assert.equal(hostErr.querySelector(".file-gallery").hidden, true, "decode error hides empty gallery");
  assert.equal(hostErr.querySelector(".file-gallery-fallbacks .snap-chip")?.dataset?.openFile, "~/shots/corrupt.webp", "decode error degrades cleanly to file chip");
  hostErr.remove();

  // 8e) Self-healing: clearing failed state restores gallery rendering
  G.clearFileGalleryImgFailed("~/shots/missing-one.png");
  const healedHtml = G.renderEntryArtifacts({ id: "e-heal", role: "collie", text: "恢复", files: ["~/shots/missing-one.png"] }, "");
  assert.ok(healedHtml.includes('class="file-gallery is-1" data-count="1"'), "self-healing restores inline gallery once file becomes readable");
}

main().catch((err) => {
  console.error(err);
  process.exit(1);
});
`
	cmd := exec.Command(nodeBin, "-e", jsTest, root)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("inline image gallery JS test failed: %v\n%s", err, string(out))
	}
}
