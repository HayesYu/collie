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
assert.ok(cssSrc.includes(".lb-nav:hover:not(:disabled)") && cssSrc.includes(".lb-nav:active:not(:disabled)"), "disabled lightbox nav buttons must not animate hover or press states");
assert.ok(cssSrc.includes(".lb-nav:disabled { opacity: .28; cursor: default; }"), "disabled lightbox nav buttons must soften visually without passing clicks through to stage backdrop");
assert.ok(!appSrc.includes("((idx % lbItems.length) + lbItems.length) % lbItems.length"), "showLbSlide must not wrap around via modulo");
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

// Pre-populate notebook & stream containers so Today's Files and cross-message streams work end-to-end
const notesPaneEl = makeEl("aside"); notesPaneEl.setAttribute("id", "notes-pane");
const npFilesListEl = makeEl("ul"); npFilesListEl.setAttribute("id", "np-files-list");
const npTasksListEl = makeEl("ul"); npTasksListEl.setAttribute("id", "np-tasks-list");
const npTimersListEl = makeEl("ul"); npTimersListEl.setAttribute("id", "np-timers-list");
notesPaneEl.append(npTasksListEl, npFilesListEl, npTimersListEl);
bodyEl.appendChild(notesPaneEl);

const superThreadEl = makeEl("div"); superThreadEl.setAttribute("id", "super-thread");
const tdListEl = makeEl("div"); tdListEl.setAttribute("id", "td-list");
const wdThreadEl = makeEl("div"); wdThreadEl.setAttribute("id", "wd-thread");
const fvEl = makeEl("section"); fvEl.setAttribute("id", "file-view"); fvEl.hidden = true;
const fvNameEl = makeEl("div"); fvNameEl.setAttribute("id", "fv-name");
const fvPathEl = makeEl("div"); fvPathEl.setAttribute("id", "fv-path");
const fvBodyEl = makeEl("div"); fvBodyEl.setAttribute("id", "fv-body");
fvEl.append(fvNameEl, fvPathEl, fvBodyEl);
bodyEl.append(superThreadEl, tdListEl, wdThreadEl, fvEl);

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
  "  globalThis.__ig = { renderEntryArtifacts, renderCollieBody, extractEntryArtifacts, loadThumb, degradeFileGalleryTile, collectGallery, collectNotebookImageGallery, renderNotebook, setNotebookData: (d) => { notebookData = d; _lastNotebookRenderKey = ''; }, openLightbox, closeLightbox, showLbSlide, thumbs, failedFileGalleryImgs, clearFileGalleryImgFailed, getLbState: () => ({ open: lightbox.open, idx: lbIdx, count: lbItems.length, items: lbItems, countText: lbCount.textContent, prevDisabled: lbPrevBtn.disabled, nextDisabled: lbNextBtn.disabled, seq: lbSeq, imgTransform: lbImg.style.transform, dragging: lightbox.classList.contains('is-dragging') }) };\n})();"
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
  assert.equal(G.getLbState().prevDisabled, true, "prev button disabled on first slide (1 / 5)");
  assert.equal(G.getLbState().nextDisabled, false, "next button enabled on first slide (1 / 5)");

  // Keyboard ArrowRight -> 2 / 5 (both arrows enabled on middle slide)
  doc.dispatchEvent({ type: "keydown", key: "ArrowRight", preventDefault() {} });
  await new Promise((r) => setImmediate(r));
  assert.equal(G.getLbState().idx, 1);
  assert.equal(G.getLbState().countText, "2 / 5", "ArrowRight advances to 2 / 5");
  assert.equal(G.getLbState().prevDisabled, false, "prev button enabled on middle slide");
  assert.equal(G.getLbState().nextDisabled, false, "next button enabled on middle slide");

  // Touch horizontal swipe left on lbStage -> 3 / 5
  lbStageEl.dispatchEvent({ type: "pointerdown", pointerId: 7, pointerType: "touch", button: 0, clientX: 280, clientY: 400, target: lbImgEl });
  lbStageEl.dispatchEvent({ type: "pointermove", pointerId: 7, pointerType: "touch", clientX: 160, clientY: 405, target: lbImgEl });
  assert.equal(G.getLbState().imgTransform, "translate3d(-54.0px, 0, 0)", "non-boundary swipe uses normal 0.45 follow factor");
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

  // ─── Case 7b: 2-image boundary no-wrap, rubber-band damping, disabled arrows & clamping ───
  const items2 = [{ path: "~/shots/m1.png", alt: "first" }, { path: "~/shots/m2.png", alt: "second" }];
  // Opening with out-of-range negative index clamps to 0
  G.openLightbox(items2, -4);
  await new Promise((r) => setImmediate(r));
  assert.equal(G.getLbState().open, true);
  assert.equal(G.getLbState().idx, 0, "openLightbox clamps negative index to 0");
  assert.equal(G.getLbState().countText, "1 / 2");
  assert.equal(G.getLbState().prevDisabled, true, "1/2: prev button must be disabled");
  assert.equal(G.getLbState().nextDisabled, false, "1/2: next button must be enabled");

  const seqAtFirst = G.getLbState().seq;
  // Direct out-of-bounds / same-index showLbSlide calls are silent no-ops
  await G.showLbSlide(-1);
  await G.showLbSlide(0);
  await G.showLbSlide(2);
  assert.equal(G.getLbState().idx, 0, "out-of-bounds and same-index showLbSlide must not change idx");
  assert.equal(G.getLbState().seq, seqAtFirst, "no-op showLbSlide must not trigger reload or flicker");

  // ArrowLeft and prev button click at 1/2 stay at 1/2 without wrapping
  doc.dispatchEvent({ type: "keydown", key: "ArrowLeft", preventDefault() {} });
  lbPrevEl.dispatchEvent({ type: "click" });
  await new Promise((r) => setImmediate(r));
  assert.equal(G.getLbState().idx, 0, "ArrowLeft and prev click at 1/2 must not wrap to 2/2");
  assert.equal(G.getLbState().countText, "1 / 2");
  assert.equal(G.getLbState().seq, seqAtFirst, "boundary ArrowLeft must not increment lbSeq");

  // Right swipe at 1/2 applies rubber-band damping (0.25x, capped at 48px) and bounces back without wrapping
  lbStageEl.dispatchEvent({ type: "pointerdown", pointerId: 11, pointerType: "touch", button: 0, clientX: 80, clientY: 400, target: lbImgEl });
  lbStageEl.dispatchEvent({ type: "pointermove", pointerId: 11, pointerType: "touch", clientX: 240, clientY: 402, target: lbImgEl });
  assert.equal(G.getLbState().dragging, true);
  assert.equal(G.getLbState().imgTransform, "translate3d(40.0px, 0, 0)", "1/2 right drag must apply 0.25 rubber-band damping");
  lbStageEl.dispatchEvent({ type: "pointermove", pointerId: 11, pointerType: "touch", clientX: 380, clientY: 402, target: lbImgEl });
  assert.equal(G.getLbState().imgTransform, "translate3d(48.0px, 0, 0)", "1/2 right drag must clamp rubber-band displacement at 48px");
  lbStageEl.dispatchEvent({ type: "pointerup", pointerId: 11, pointerType: "touch", clientX: 380, clientY: 402, target: lbImgEl });
  await new Promise((r) => setImmediate(r));
  assert.equal(G.getLbState().dragging, false, "is-dragging cleared on release so GPU bounce transition runs");
  assert.equal(G.getLbState().imgTransform, "", "1/2 right swipe release resets transform to center");
  assert.equal(G.getLbState().idx, 0, "1/2 right swipe must stay on 1/2");
  assert.equal(G.getLbState().seq, seqAtFirst, "1/2 right swipe must not reload slide");

  // Advance to 2/2 via next button -> prev enabled, next disabled
  lbNextEl.dispatchEvent({ type: "click" });
  await new Promise((r) => setImmediate(r));
  assert.equal(G.getLbState().idx, 1, "next click advances to 2/2");
  assert.equal(G.getLbState().countText, "2 / 2");
  assert.equal(G.getLbState().prevDisabled, false, "2/2: prev button must be enabled");
  assert.equal(G.getLbState().nextDisabled, true, "2/2: next button must be disabled");

  const seqAtLast = G.getLbState().seq;
  // ArrowRight and next button click at 2/2 stay at 2/2 without wrapping
  doc.dispatchEvent({ type: "keydown", key: "ArrowRight", preventDefault() {} });
  lbNextEl.dispatchEvent({ type: "click" });
  await new Promise((r) => setImmediate(r));
  assert.equal(G.getLbState().idx, 1, "ArrowRight and next click at 2/2 must not wrap to 1/2");
  assert.equal(G.getLbState().countText, "2 / 2");
  assert.equal(G.getLbState().seq, seqAtLast, "boundary ArrowRight must not increment lbSeq");

  // Left swipe at 2/2 applies rubber-band damping (-0.25x, capped at -48px) and bounces back without wrapping
  lbStageEl.dispatchEvent({ type: "pointerdown", pointerId: 12, pointerType: "touch", button: 0, clientX: 320, clientY: 400, target: lbImgEl });
  lbStageEl.dispatchEvent({ type: "pointermove", pointerId: 12, pointerType: "touch", clientX: 160, clientY: 404, target: lbImgEl });
  assert.equal(G.getLbState().imgTransform, "translate3d(-40.0px, 0, 0)", "2/2 left drag must apply 0.25 rubber-band damping");
  lbStageEl.dispatchEvent({ type: "pointermove", pointerId: 12, pointerType: "touch", clientX: 20, clientY: 404, target: lbImgEl });
  assert.equal(G.getLbState().imgTransform, "translate3d(-48.0px, 0, 0)", "2/2 left drag must clamp rubber-band displacement at -48px");
  lbStageEl.dispatchEvent({ type: "pointerup", pointerId: 12, pointerType: "touch", clientX: 20, clientY: 404, target: lbImgEl });
  await new Promise((r) => setImmediate(r));
  assert.equal(G.getLbState().dragging, false);
  assert.equal(G.getLbState().imgTransform, "", "2/2 left swipe release resets transform to center");
  assert.equal(G.getLbState().idx, 1, "2/2 left swipe must stay on 2/2");
  assert.equal(G.getLbState().seq, seqAtLast, "2/2 left swipe must not reload slide");

  // Opening with out-of-range positive index clamps to last slide
  G.openLightbox(items2, 99);
  await new Promise((r) => setImmediate(r));
  assert.equal(G.getLbState().idx, 1, "openLightbox clamps oversized index to last slide");
  assert.equal(G.getLbState().prevDisabled, false);
  assert.equal(G.getLbState().nextDisabled, true);

  await G.closeLightbox();
  assert.equal(G.getLbState().open, false);

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

  // ─── Case 9: Notebook Today's Files image stream (skips non-images, dedups path, preserves #file-view for docs & boundary no-wrap) ───
  const fetchedUrls = [];
  fetchHandler = async (url) => {
    const u = String(url);
    fetchedUrls.push(u);
    if (u.includes("as=image")) {
      return {
        ok: true,
        status: 200,
        headers: { get: (k) => k === "X-File-Kind" ? "image" : k === "X-File-Size" ? "2048" : k === "X-File-Path" ? "~/shots/ok.png" : "" },
        blob: async () => new Blob(["PNGDATA"]),
      };
    }
    return {
      ok: true,
      status: 200,
      headers: { get: (k) => k === "X-File-Kind" ? "text" : k === "X-File-Size" ? "32" : k === "X-File-Path" ? "~/docs/notes.md" : "" },
      blob: async () => new Blob(["# Summary\nAll good."]),
    };
  };

  G.setNotebookData({
    date: "2026-11-28",
    tasks: [],
    artifacts: [
      { path: "~/shots/nb-1.png", name: "nb-1.png", title: "nb-1.png", ext: "png" },
      { path: "~/docs/notes.md", name: "notes.md", title: "notes.md", ext: "md" },
      { path: "~/shots/nb-2.jpg", name: "nb-2.jpg", title: "nb-2.jpg", ext: "jpg" },
      { path: "~/reports/perf.html", name: "perf.html", title: "perf.html", ext: "html" },
      { path: "~/shots/nb-1.png", name: "nb-1.png", title: "nb-1.png", ext: "png" },
      { path: "~/shots/nb-3.WEBP", name: "nb-3.WEBP", title: "nb-3.WEBP", ext: "webp" },
    ],
    timers: [],
  });
  G.renderNotebook();
  parseHtmlInto(npFilesListEl, npFilesListEl.innerHTML);
  const npBtns = npFilesListEl.querySelectorAll("[data-np-file]");
  assert.equal(npBtns.length, 6, "Today's Files renders all 6 artifacts including folded overflow rows");

  // Click 2nd image (nb-2.jpg, 3rd artifact row overall) -> opens Lightbox at 2 / 3
  fetchedUrls.length = 0;
  notesPaneEl.dispatchEvent({ type: "click", target: npBtns[2] });
  await new Promise((r) => setImmediate(r));
  assert.equal(G.getLbState().open, true, "clicking Today's Files image opens Lightbox");
  assert.equal(G.getLbState().idx, 1, "clicking 2nd image in Today's Files lands at index 1");
  assert.equal(G.getLbState().countText, "2 / 3", "Today's Files image stream skips .md/.html and deduplicates duplicate path -> 2 / 3");
  assert.deepEqual(
    [...G.getLbState().items.map((x) => x.path)],
    ["~/shots/nb-1.png", "~/shots/nb-2.jpg", "~/shots/nb-3.WEBP"],
    "Today's Files image stream preserves display order and keeps first occurrence on dedup"
  );
  assert.equal(fetchedUrls.length, 1, "opening slide 2/3 lazily fetches only the active image");

  // Boundary no-wrap check in Today's Files stream
  doc.dispatchEvent({ type: "keydown", key: "ArrowLeft", preventDefault() {} });
  await new Promise((r) => setImmediate(r));
  assert.equal(G.getLbState().countText, "1 / 3");
  assert.equal(G.getLbState().prevDisabled, true, "1/3 disables prev arrow");
  doc.dispatchEvent({ type: "keydown", key: "ArrowLeft", preventDefault() {} });
  await new Promise((r) => setImmediate(r));
  assert.equal(G.getLbState().idx, 0, "1/3 ArrowLeft must not wrap to 3/3");

  doc.dispatchEvent({ type: "keydown", key: "ArrowRight", preventDefault() {} });
  await new Promise((r) => setImmediate(r));
  doc.dispatchEvent({ type: "keydown", key: "ArrowRight", preventDefault() {} });
  await new Promise((r) => setImmediate(r));
  assert.equal(G.getLbState().countText, "3 / 3");
  assert.equal(G.getLbState().nextDisabled, true, "3/3 disables next arrow");
  doc.dispatchEvent({ type: "keydown", key: "ArrowRight", preventDefault() {} });
  await new Promise((r) => setImmediate(r));
  assert.equal(G.getLbState().idx, 2, "3/3 ArrowRight must not wrap to 1/3");

  // Clicking duplicate path row maps to its deduplicated first slot (1 / 3)
  notesPaneEl.dispatchEvent({ type: "click", target: npBtns[4] });
  await new Promise((r) => setImmediate(r));
  assert.equal(G.getLbState().idx, 0, "clicking duplicate artifact path targets its first occurrence");
  await G.closeLightbox();

  // Clicking non-image .md file opens #file-view and does NOT open Lightbox
  fvNameEl.textContent = "";
  notesPaneEl.dispatchEvent({ type: "click", target: npBtns[1] });
  await new Promise((r) => setImmediate(r));
  assert.equal(G.getLbState().open, false, "clicking .md in Today's Files must not open Lightbox");
  assert.equal(fvNameEl.textContent, "notes.md", "clicking .md in Today's Files opens #file-view");

  // ─── Case 10: Main chat (#super-thread) cross-message image stream with +N hidden items, md-img-btn, per-message dedup & exact duplicate targeting ───
  const msg2Files = [1, 2, 3, 4, 5].map((n) => "~/shots/s2-" + n + ".png");
  const msg2Body = G.renderCollieBody("重点看第 3 张 ~/shots/s2-3.png 的效果", msg2Files);
  const msg2Art = G.renderEntryArtifacts({ id: "e-s2", role: "collie", text: "五图验收", files: msg2Files }, "");
  const msg3Body = G.renderCollieBody("远程走势图：\n![chart](https://example.com/chart.png)", ["~/uploads/u1.png"]);
  const msg3Art = G.renderEntryArtifacts({ id: "e-s3", role: "collie", text: "复核原图", files: ["~/uploads/u1.png"] }, "");
  const superHtml = [
    '<div class="mine is-media-only"><div class="msg user is-media-only"><div class="mine-media is-single" data-count="1"><span class="thumbs"><button type="button" class="thumb" data-img="~/uploads/u1.png"><img alt="" src=""></button></span></div></div></div>',
    '<div class="say"><span class="avatar"><img src="/icon.svg" alt=""></span><div class="msg assistant" data-id="e-s2">' + msg2Body + msg2Art + '</div></div>',
    '<div class="say"><span class="avatar"><img src="/icon.svg" alt=""></span><div class="msg assistant" data-id="e-s3">' + msg3Body + msg3Art + '</div></div>',
  ].join("");
  parseHtmlInto(superThreadEl, superHtml);

  const msg1Thumb = superThreadEl.children[0].querySelector(".thumb[data-img]");
  const msg2ProseLink = superThreadEl.children[1].querySelector('a.fpath[data-path="~/shots/s2-3.png"]');
  const msg2Tiles = superThreadEl.children[1].querySelectorAll(".file-gal-tile");
  const msg3MdBtn = superThreadEl.children[2].querySelector(".md-img-btn[data-img-url]");
  const msg3DupTile = superThreadEl.children[2].querySelector('.file-gal-tile[data-img="~/uploads/u1.png"]');
  assert.ok(msg1Thumb && msg2ProseLink && msg2Tiles.length === 4 && msg3MdBtn && msg3DupTile, "all 3 main chat message fixtures parsed cleanly");

  const expectedStream = [
    "p:~/uploads/u1.png",
    "p:~/shots/s2-1.png",
    "p:~/shots/s2-2.png",
    "p:~/shots/s2-3.png",
    "p:~/shots/s2-4.png",
    "p:~/shots/s2-5.png",
    "u:https://example.com/chart.png",
    "p:~/uploads/u1.png",
  ];
  const itemKeys = (list) => [...list.map((x) => (x.path ? "p:" + x.path : "u:" + x.url))];

  const fromMsg1 = G.collectGallery(msg1Thumb, { path: "~/uploads/u1.png", alt: "" });
  assert.deepEqual(itemKeys(fromMsg1.items), expectedStream, "#super-thread collects all 8 images across 3 messages in chronological order without avatars or within-message duplicates");
  assert.equal(fromMsg1.index, 0, "clicking msg1 u1.png opens at 1 / 8 (index 0)");

  const fromMsg3Dup = G.collectGallery(msg3DupTile, { path: "~/uploads/u1.png", alt: "u1.png" });
  assert.deepEqual(itemKeys(fromMsg3Dup.items), expectedStream);
  assert.equal(fromMsg3Dup.index, 7, "clicking msg3 duplicate u1.png must target index 7 (8 / 8), NOT msg1's index 0");

  const fromMsg2Link = G.collectGallery(msg2ProseLink, { path: "~/shots/s2-3.png", alt: "" });
  assert.equal(fromMsg2Link.index, 3, "clicking prose image link deduplicated with msg2 gallery targets exact gallery slot (4 / 8)");

  const fromMsg2PlusOne = G.collectGallery(msg2Tiles[3], { path: "~/shots/s2-4.png", alt: "s2-4.png" });
  assert.equal(fromMsg2PlusOne.index, 4, "clicking +1 overflow tile targets 5 / 8, followed by hidden 5th image at 6 / 8");

  const fromMsg3Md = G.collectGallery(msg3MdBtn, { url: "https://example.com/chart.png", alt: "chart" });
  assert.equal(fromMsg3Md.index, 6, "clicking markdown remote image targets 7 / 8");

  // Lazy loading verification + cross-message traversal 1/8 -> 8/8 without wrap
  G.thumbs.clear();
  fetchedUrls.length = 0;
  G.openLightbox(fromMsg1.items, fromMsg1.index);
  await new Promise((r) => setImmediate(r));
  assert.equal(G.getLbState().countText, "1 / 8");
  assert.equal(G.getLbState().prevDisabled, true);
  assert.equal(fetchedUrls.length, 1, "lazy loading fetches only slide 1/8 initially (does not preload all 8)");
  for (let step = 1; step < 8; step++) {
    doc.dispatchEvent({ type: "keydown", key: "ArrowRight", preventDefault() {} });
    await new Promise((r) => setImmediate(r));
    assert.equal(G.getLbState().countText, (step + 1) + " / 8");
  }
  assert.equal(G.getLbState().nextDisabled, true, "8/8 disables next arrow");
  doc.dispatchEvent({ type: "keydown", key: "ArrowRight", preventDefault() {} });
  await new Promise((r) => setImmediate(r));
  assert.equal(G.getLbState().idx, 7, "8/8 ArrowRight stays at last slide without wrapping");
  await G.closeLightbox();

  // ─── Case 11: Thread drawer (#td-list) & Worker drawer (#wd-thread) stay strictly isolated from #super-thread ───
  const tdArt = G.renderEntryArtifacts({ id: "td-2", role: "collie", text: "抽屉双图", files: ["~/td/b1.png", "~/td/b2.png"] }, "");
  parseHtmlInto(tdListEl, [
    '<article class="td-node" data-td-id="td-1"><div class="td-rail"><span class="td-av user"></span></div><div class="td-content"><div class="td-text td-user-text"><span class="thumbs"><button type="button" class="thumb" data-img="~/td/a.png"><img alt=""></button></span></div></div></article>',
    '<article class="td-node" data-td-id="td-2"><div class="td-rail"><span class="td-av collie"><img src="/icon.svg" alt=""></span></div><div class="td-content"><div class="td-text msg assistant">' + tdArt + '</div></div></article>',
  ].join(""));
  const tdFirst = tdListEl.querySelector('.thumb[data-img="~/td/a.png"]');
  const tdLast = tdListEl.querySelector('.file-gal-tile[data-img="~/td/b2.png"]');
  const tdStream = G.collectGallery(tdFirst, { path: "~/td/a.png", alt: "" });
  assert.deepEqual(itemKeys(tdStream.items), ["p:~/td/a.png", "p:~/td/b1.png", "p:~/td/b2.png"], "#td-list collects its own 3-image stream, skipping .td-av icon and isolating from #super-thread");
  assert.equal(tdStream.index, 0);
  const tdStreamLast = G.collectGallery(tdLast, { path: "~/td/b2.png", alt: "b2.png" });
  assert.equal(tdStreamLast.index, 2, "clicking last image in #td-list lands on 3 / 3");

  parseHtmlInto(wdThreadEl, [
    '<div class="mine-group"><div class="mine-media is-single" data-count="1"><span class="thumbs"><button type="button" class="thumb" data-img="~/wd/w1.png"><img alt=""></button></span></div><div class="msg user">看图</div></div>',
    '<div class="msg assistant"><button type="button" class="md-img-btn" data-img-url="https://example.com/w2.png" data-img-alt="w2"><img class="md-img" src="https://example.com/w2.png" alt="w2"></button><p>另有 <a class="fpath img-link" href="#" data-path="~/wd/w3.png">~/wd/w3.png</a></p></div>',
  ].join(""));
  const wdMid = wdThreadEl.querySelector(".md-img-btn[data-img-url]");
  const wdStream = G.collectGallery(wdMid, { url: "https://example.com/w2.png", alt: "w2" });
  assert.deepEqual(
    itemKeys(wdStream.items),
    ["p:~/wd/w1.png", "u:https://example.com/w2.png", "p:~/wd/w3.png"],
    "#wd-thread collects its own 3-image stream independently from #super-thread and #td-list"
  );
  assert.equal(wdStream.index, 1, "clicking 2nd image in #wd-thread lands on 2 / 3");
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
