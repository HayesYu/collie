package collie

import (
	"bytes"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lihuanyu0w0/collie/web"
)

func TestWebEmbeddedMermaidOfflineAndLazy(t *testing.T) {
	t.Parallel()

	for _, req := range []string{"vendor/mermaid/mermaid.min.js", "vendor/mermaid/LICENSE"} {
		fi, err := fs.Stat(web.FS, req)
		if err != nil {
			t.Fatalf("required Mermaid asset missing from embed.FS: %s (%v)", req, err)
		}
		if fi.Size() <= 0 {
			t.Fatalf("embedded Mermaid asset %s is empty", req)
		}
	}

	mmdBytes, err := fs.ReadFile(web.FS, "vendor/mermaid/mermaid.min.js")
	if err != nil {
		t.Fatalf("read vendor/mermaid/mermaid.min.js: %v", err)
	}
	for _, forbidden := range []string{"unpkg.com", "jsdelivr", "cdnjs", "fonts.googleapis", "chunks/mermaid"} {
		if bytes.Contains(mmdBytes, []byte(forbidden)) {
			t.Fatalf("vendor/mermaid/mermaid.min.js must be self-contained offline without %q", forbidden)
		}
	}

	// Zero initial-load cost: index.html must NOT eagerly load mermaid.min.js.
	indexBytes, err := fs.ReadFile(web.FS, "index.html")
	if err != nil {
		t.Fatalf("read web/index.html: %v", err)
	}
	if strings.Contains(string(indexBytes), "mermaid.min.js") {
		t.Fatal("web/index.html must NOT eagerly load mermaid.min.js; it must be lazy-loaded on demand")
	}
	if !strings.Contains(string(indexBytes), `id="lb-svg"`) {
		t.Fatal("web/index.html must include #lb-svg container in lightbox for Mermaid fullscreen view")
	}

	// Embedded HTTP serving check
	srv := &Server{token: "test-tok", home: t.TempDir(), web: web.FS}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/vendor/mermaid/mermaid.min.js", nil)
	srv.Routes().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /vendor/mermaid/mermaid.min.js status = %d, want 200", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); !strings.Contains(ct, "javascript") {
		t.Fatalf("GET mermaid.min.js Content-Type = %q, want javascript", ct)
	}

	// Demo directory stays in sync with web/vendor/mermaid
	demoMmd, err := os.ReadFile(filepath.Join("..", "..", "docs", "demo", "vendor", "mermaid", "mermaid.min.js"))
	if err != nil {
		t.Fatalf("read docs/demo/vendor/mermaid/mermaid.min.js: %v (run sh scripts/demo.sh)", err)
	}
	if !bytes.Equal(demoMmd, mmdBytes) {
		t.Fatal("docs/demo/vendor/mermaid/mermaid.min.js is out of sync with web/vendor/mermaid/mermaid.min.js")
	}
}

func TestRichVisualsAndCommonMarkFences(t *testing.T) {
	t.Parallel()
	nodeBin, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node not installed; skipping JS rich visuals tests")
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

// Static CSS contract checks
assert.ok(cssSrc.includes(".rv-mermaid") && cssSrc.includes(".rv-svg") && cssSrc.includes(".rv-html-card"), "CSS must style Mermaid, SVG, and inline HTML cards");
assert.ok(cssSrc.includes(".rv-float-acts") && cssSrc.includes(".rv-html-foot") && !cssSrc.includes(".rv-bar {") && !cssSrc.includes(".rv-html-head {"), "CSS must use frameless floating micro-actions instead of persistent top bars");
assert.ok(cssSrc.includes(".rv-mermaid.is-fallback") && cssSrc.includes(".rv-svg.is-fallback"), "CSS must style silent fallback for invalid Mermaid/SVG blocks");
assert.ok(cssSrc.includes(".lb-svg") && cssSrc.includes(".rv-svg-stage.is-light-canvas"), "CSS must style lightbox SVG stage and dark-mode light canvas fallback");
assert.ok(cssSrc.includes("--rv-html-max, 460px") && cssSrc.includes("--rv-html-max: 380px"), "CSS must clamp desktop/mobile HTML card heights");

// Minimal DOM harness supporting elements, classes, attributes, dataset, children, querySelector, DOMParser, message & theme events
let bootFinished = false;
function makeEl(tag = "div") {
  const attrs = new Map();
  const listeners = new Map();
  const classSet = new Set();
  const el = {
    tagName: String(tag).toUpperCase(),
    localName: String(tag).toLowerCase(),
    nodeType: 1,
    hidden: false,
    _html: "",
    _text: "",
    children: [],
    parentNode: null,
    parentElement: null,
    dataset: {},
    style: {
      _props: new Map(),
      setProperty(k, v) { this._props.set(k, String(v)); },
      getPropertyValue(k) { return this._props.get(k) || ""; },
      height: "",
      minHeight: "",
      maxWidth: "",
      transform: "",
      colorScheme: "",
      cssText: "",
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
    get firstElementChild() { return this.children[0] || null; },
    get lastElementChild() { return this.children[this.children.length - 1] || null; },
    get attributes() { return [...attrs.entries()].map(([name, value]) => ({ name, value })); },
    get nextElementSibling() {
      const p = this.parentNode;
      if (!p || !Array.isArray(p.children)) return null;
      const idx = p.children.indexOf(this);
      return idx >= 0 && idx + 1 < p.children.length ? p.children[idx + 1] : null;
    },
    setAttribute(k, v) {
      const val = String(v);
      attrs.set(k, val);
      if (k.startsWith("data-")) {
        const dk = k.slice(5).replace(/-([a-z])/g, (_, ch) => ch.toUpperCase());
        this.dataset[dk] = val;
      }
      if (k === "class") this.className = val;
    },
    getAttribute(k) { return attrs.has(k) ? attrs.get(k) : null; },
    removeAttribute(k) {
      attrs.delete(k);
      if (k.startsWith("data-")) {
        const dk = k.slice(5).replace(/-([a-z])/g, (_, ch) => ch.toUpperCase());
        delete this.dataset[dk];
      }
    },
    hasAttribute(k) { return attrs.has(k); },
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
    appendChild(child) {
      if (child.tagName === "#FRAGMENT") {
        for (const c of [...child.children]) this.appendChild(c);
        child.children.length = 0;
        return child;
      }
      if (child.parentNode && child.parentNode !== this) child.parentNode.removeChild(child);
      child.parentNode = this;
      child.parentElement = this;
      this.children.push(child);
      return child;
    },
    replaceChildren(...nodes) {
      for (const c of [...this.children]) this.removeChild(c);
      for (const n of nodes) if (n && typeof n === "object") this.appendChild(n);
    },
    insertBefore(child, ref) {
      if (!ref) return this.appendChild(child);
      if (child.tagName === "#FRAGMENT") {
        for (const c of [...child.children]) this.insertBefore(c, ref);
        child.children.length = 0;
        return child;
      }
      if (child.parentNode) child.parentNode.removeChild(child);
      const idx = this.children.indexOf(ref);
      child.parentNode = this;
      child.parentElement = this;
      if (idx < 0) this.children.push(child);
      else this.children.splice(idx, 0, child);
      return child;
    },
    replaceChild(fresh, old) {
      const idx = this.children.indexOf(old);
      if (idx < 0) return old;
      if (fresh.parentNode) fresh.parentNode.removeChild(fresh);
      old.parentNode = null;
      old.parentElement = null;
      fresh.parentNode = this;
      fresh.parentElement = this;
      this.children[idx] = fresh;
      return old;
    },
    removeChild(child) {
      const idx = this.children.indexOf(child);
      if (idx >= 0) this.children.splice(idx, 1);
      child.parentNode = null;
      child.parentElement = null;
      return child;
    },
    remove() { if (this.parentNode) this.parentNode.removeChild(this); },
    append(...ns) { for (const n of ns) if (n && typeof n === "object") this.appendChild(n); },
    after() {},
    cloneNode(deep = false) {
      const cp = makeEl(this.localName || this.tagName);
      for (const [k, v] of attrs.entries()) cp.setAttribute(k, v);
      cp._text = this._text;
      cp._html = this._html;
      if (deep) {
        for (const c of this.children) cp.appendChild(c.cloneNode(true));
      }
      return cp;
    },
    closest(sel) {
      let cur = this;
      while (cur) {
        if (matchesSimple(cur, sel)) return cur;
        cur = cur.parentElement;
      }
      return null;
    },
    contains(other) {
      let cur = other;
      while (cur) { if (cur === this) return true; cur = cur.parentNode; }
      return false;
    },
    querySelector(sel) { return this.querySelectorAll(sel)[0] || (!bootFinished ? makeEl("div") : null); },
    querySelectorAll(sel) {
      const out = [];
      const walk = (node) => {
        for (const c of node.children || []) {
          if (matchesSimple(c, sel)) out.push(c);
          walk(c);
        }
      };
      walk(this);
      return out;
    },
    getElementsByTagName(name) {
      const low = String(name || "").toLowerCase();
      const out = [];
      const walk = (node) => {
        for (const c of node.children || []) {
          if (low === "*" || c.localName === low) out.push(c);
          walk(c);
        }
      };
      walk(this);
      return out;
    },
    getElementsByTagNameNS(_ns, name) { return this.getElementsByTagName(name); },
    get outerHTML() {
      const tag = this.localName || this.tagName.toLowerCase();
      const attrStr = [...attrs.entries()].map(([k, v]) => " " + k + '="' + String(v).replace(/"/g, "&quot;") + '"').join("");
      const inner = this.children.length ? this.children.map((c) => c.outerHTML).join("") : this._text;
      return "<" + tag + attrStr + ">" + inner + "</" + tag + ">";
    },
    getBoundingClientRect: () => ({ top: 0, bottom: 100, left: 0, right: 600, width: 600, height: 100 }),
    focus() {},
    scrollIntoView() {},
    get innerHTML() { return this._html; },
    set innerHTML(v) { this._html = String(v || ""); this.children = []; },
    get textContent() { return this._text; },
    set textContent(v) { this._text = String(v || ""); },
    options: [],
    selectedOptions: [],
    contentWindow: null,
  };
  return el;
}

// Lightweight XML parser for DOMParser("image/svg+xml") unit testing
class MiniXmlDOMParser {
  parseFromString(raw) {
    const src = String(raw || "").trim();
    const doc = makeEl("#document");
    const stack = [doc];
    const tokRe = /<!--[\s\S]*?-->|<\/([A-Za-z_][\w:.-]*)\s*>|<([A-Za-z_][\w:.-]*)((?:\s+[^<>]*?)?)\s*(\/?)>|([^<]+)/g;
    let m, pos = 0;
    while ((m = tokRe.exec(src))) {
      if (m.index !== pos) {
        const err = makeEl("parsererror");
        doc.replaceChildren(err);
        return { documentElement: err, getElementsByTagName: (n) => doc.getElementsByTagName(n), getElementsByTagNameNS: (_ns, n) => doc.getElementsByTagName(n) };
      }
      pos = tokRe.lastIndex;
      if (m[0].startsWith("<!--")) {
        const c = makeEl("#comment"); c.nodeType = 8;
        stack[stack.length - 1].appendChild(c);
        continue;
      }
      if (m[1]) {
        const closing = m[1].toLowerCase();
        const top = stack[stack.length - 1];
        if (!top || stack.length <= 1 || top.localName !== closing) {
          const err = makeEl("parsererror");
          doc.replaceChildren(err);
          return { documentElement: err, getElementsByTagName: (n) => doc.getElementsByTagName(n), getElementsByTagNameNS: (_ns, n) => doc.getElementsByTagName(n) };
        }
        stack.pop();
        continue;
      }
      if (m[2]) {
        const tagName = m[2];
        const attrPart = m[3] || "";
        const selfClose = m[4] === "/";
        const el = makeEl(tagName);
        for (const am of attrPart.matchAll(/([A-Za-z_][\w:.-]*)\s*=\s*(?:"([^"]*)"|'([^']*)')/g)) {
          el.setAttribute(am[1], am[2] ?? am[3] ?? "");
        }
        stack[stack.length - 1].appendChild(el);
        if (!selfClose) stack.push(el);
        continue;
      }
      if (m[5]) {
        const top = stack[stack.length - 1];
        if (top && stack.length > 1) top._text += m[5];
      }
    }
    if (stack.length !== 1 || doc.children.length !== 1) {
      const err = makeEl("parsererror");
      doc.replaceChildren(err);
      return { documentElement: err, getElementsByTagName: (n) => doc.getElementsByTagName(n), getElementsByTagNameNS: (_ns, n) => doc.getElementsByTagName(n) };
    }
    const root = doc.children[0];
    return {
      documentElement: root,
      getElementsByTagName: (n) => doc.getElementsByTagName(n),
      getElementsByTagNameNS: (_ns, n) => doc.getElementsByTagName(n),
    };
  }
}

function matchesSimple(el, selectorList) {
  if (!el || el.nodeType !== 1 || !selectorList) return false;
  for (const rawSel of String(selectorList).split(",")) {
    const sel = rawSel.trim().replace(/^:scope\s*>\s*/, "");
    if (!sel) continue;
    const parts = sel.split(/\s+/);
    const last = parts[parts.length - 1];
    if (!matchesAtom(el, last)) continue;
    if (parts.length === 1) return true;
    let p = el.parentElement;
    let ok = true;
    for (let k = parts.length - 2; k >= 0; k--) {
      while (p && !matchesAtom(p, parts[k])) p = p.parentElement;
      if (!p) { ok = false; break; }
      p = p.parentElement;
    }
    if (ok) return true;
  }
  return false;
}
function matchesAtom(el, atom) {
  let s = atom;
  const tagM = s.match(/^([a-z][a-z0-9-]*)/i);
  if (tagM) {
    if (el.tagName !== tagM[1].toUpperCase()) return false;
    s = s.slice(tagM[1].length);
  }
  for (const m of s.matchAll(/#([a-zA-Z0-9_-]+)/g)) {
    if (el.id !== m[1]) return false;
  }
  for (const m of s.matchAll(/\.([a-zA-Z0-9_-]+)/g)) {
    if (!el.classList.contains(m[1])) return false;
  }
  for (const m of s.matchAll(/\[([^=\]]+)(?:="([^"]*)")?\]/g)) {
    const k = m[1], want = m[2];
    const dk = k.startsWith("data-") ? k.slice(5).replace(/-([a-z])/g, (_, c) => c.toUpperCase()) : "";
    const has = el.hasAttribute(k) || (dk && el.dataset[dk] !== undefined);
    if (!has) return false;
    if (want !== undefined) {
      const got = el.getAttribute(k) ?? (dk ? el.dataset[dk] : null);
      if (String(got) !== want) return false;
    }
  }
  return true;
}

const byId = new Map();
const getById = (id) => {
  if (!byId.has(id)) byId.set(id, makeEl("div"));
  return byId.get(id);
};
const winListeners = new Map();
const darkListeners = [];
let darkMatches = false;
const docEl = makeEl("html");
const headEl = makeEl("head");
const bodyEl = makeEl("body");
const doc = {
  documentElement: docEl,
  head: headEl,
  body: bodyEl,
  hidden: false,
  createElement: (t) => {
    const e = makeEl(t);
    if (String(t).toLowerCase() === "template") {
      e.content = makeEl("#FRAGMENT");
      Object.defineProperty(e, "innerHTML", {
        get() { return e._html; },
        set(v) {
          e._html = String(v || "");
          e.content.children = [];
          const root = makeEl("div");
          root._html = e._html;
          if (e._html.includes('class="say')) {
            root.className = "say";
            const msg = makeEl("div");
            msg.className = "msg assistant";
            msg._html = e._html;
            root.appendChild(msg);
          }
          e.content.children.push(root);
        },
      });
    }
    if (String(t).toLowerCase() === "iframe") {
      const posted = [];
      let srcdocVal = "";
      let srcdocWrites = 0;
      e.contentWindow = { posted, postMessage(msg, target) { posted.push({ msg, target }); } };
      Object.defineProperty(e, "srcdoc", {
        get() { return srcdocVal; },
        set(v) { srcdocVal = String(v || ""); srcdocWrites++; },
      });
      e._getSrcdocWrites = () => srcdocWrites;
    }
    return e;
  },
  importNode: (node, deep) => node.cloneNode(!!deep),
  createDocumentFragment: () => makeEl("#FRAGMENT"),
  getElementById: (id) => getById(id),
  querySelector: (sel) => {
    if (sel.startsWith("#")) return getById(sel.slice(1));
    return bodyEl.querySelector(sel) || headEl.querySelector(sel) || (!bootFinished ? makeEl("div") : null);
  },
  querySelectorAll: (sel) => bodyEl.querySelectorAll(sel),
  addEventListener() {},
  hasFocus: () => true,
};

const warnings = [];
const ctx = {
  console: { log() {}, info() {}, warn: (...a) => warnings.push(a.join(" ")), error: (...a) => warnings.push(a.join(" ")) },
  document: doc,
  DOMParser: MiniXmlDOMParser,
  window: null,
  location: { hash: "", search: "", pathname: "/", origin: "http://127.0.0.1", hostname: "127.0.0.1", href: "http://127.0.0.1/" },
  history: { state: {}, replaceState() {}, back() {} },
  navigator: { userAgent: "NodeTest" },
  localStorage: { getItem: () => null, setItem() {} },
  matchMedia: () => ({
    get matches() { return darkMatches; },
    addEventListener(ev, fn) { if (ev === "change") darkListeners.push(fn); },
  }),
  MutationObserver: class { observe() {} },
  EventSource: class { addEventListener() {} close() {} },
  CSS: { escape: (s) => String(s) },
  URL,
  URLSearchParams,
  Blob,
  performance: { now: () => 0 },
  requestAnimationFrame: (fn) => { fn(); return 0; },
  cancelAnimationFrame() {},
  setTimeout: () => 0,
  clearTimeout() {},
  setInterval: () => 0,
  clearInterval() {},
  addEventListener(ev, fn) {
    if (!winListeners.has(ev)) winListeners.set(ev, []);
    winListeners.get(ev).push(fn);
  },
  removeEventListener() {},
  dispatchEvent(ev) {
    for (const fn of winListeners.get(ev?.type) || []) fn(ev);
  },
  Event,
  HTMLDialogElement: { prototype: {} },
  getComputedStyle: () => ({ getPropertyValue: () => "" }),
  fetch: () => Promise.reject(new Error("no network")),
  innerWidth: 1440,
  innerHeight: 900,
};
ctx.window = ctx;
ctx.globalThis = ctx;

const marker = "  boot();\n})();";
assert.ok(appSrc.includes(marker), "expected boot() tail in web/app.js");
const instrumented = appSrc.replace(
  marker,
  "  globalThis.__rv = { mdRender, stripFencedBlocks, parseHtmlCardInfo, renderEntryArtifacts, HTML_SANDBOX_ATTR, mermaidConfig, renderMermaidCard, sanitizeSvgElement, renderSvgCard, svgSanitizedCache, mountHtmlCard, handleRichCardMessage, setRichCardMode, buildSandboxedCardSrcdoc, htmlCardFrames, mermaidSvgCache, onRichVisualThemeChange, renderSuper, setSup: (s) => { sup = normSup(s); }, setView: (v) => { view = v; }, syncActiveFileMarks };\n})();"
);
vm.runInNewContext(instrumented, ctx, { filename: "web/app.js" });
bootFinished = true;
const R = ctx.__rv;

// 1. CommonMark §4.5 & §6.1 regression (#t13962): 4-backtick inline code spans containing triple backticks MUST NOT open fenced blocks!
{
  const bt = "\x60";
  const bt3 = bt.repeat(3);
  const bt4 = bt.repeat(4);
  const msg = [
    "收到！已派 " + bt + "feat-collie-rich-visuals" + bt + " 开发：",
    "",
    "- **离线按需 Mermaid 直渲**：" + bt4 + " " + bt3 + "mermaid " + bt4 + " 仅出现时按需加载；",
    "- **HTML 沙盒内联卡**：" + bt + "--file xxx.html" + bt + " 附件与 " + bt4 + " " + bt3 + "html-card " + bt4 + " 直接在主会话展开沙盒卡片（普通 " + bt4 + " " + bt3 + "html " + bt4 + " 保持高亮不误渲）。",
    "",
    "验完带明暗实机图报你，一起攒进 " + bt + "v0.3.12" + bt + "！",
  ].join("\n");

  const html = R.mdRender(msg);
  assert.ok(!html.includes("<pre>"), "inline 4-backtick code spans must NEVER create <pre> fenced blocks:\n" + html);
  assert.ok(!html.includes("rv-mermaid") && !html.includes("rv-html-card"), "inline literal fences must NEVER trigger Mermaid or HTML cards:\n" + html);
  assert.ok(html.includes("<code>" + bt3 + "mermaid</code>"), "4-backtick inline span renders clean triple-backtick mermaid:\n" + html);
  assert.ok(html.includes("<code>" + bt3 + "html-card</code>"), "4-backtick inline span renders clean triple-backtick html-card:\n" + html);
  assert.ok(html.includes("<code>" + bt3 + "html</code>"), "4-backtick inline span renders clean triple-backtick html:\n" + html);
  assert.ok(html.includes("验完带明暗实机图报你"), "trailing paragraph must NOT be swallowed:\n" + html);

  // Column-0 4-backtick inline span (backtick fence info string cannot contain backticks per CommonMark §4.5)
  const col0 = R.mdRender(bt4 + " " + bt3 + "mermaid " + bt4 + " 可以原生渲染图表。");
  assert.equal(col0, "<p><code>" + bt3 + "mermaid</code> 可以原生渲染图表。</p>");

  // CommonMark §6.1 inline code span space trimming
  assert.equal(R.mdRender(bt + bt + " " + bt + "foo" + bt + " " + bt + bt + " 与 " + bt + " a " + bt + " 与 " + bt + "  " + bt), "<p><code>" + bt + "foo" + bt + "</code> 与 <code>a</code> 与 <code>  </code></p>");

  // Nested 4-backtick markdown fence containing 3-backtick inner mermaid fence must NOT prematurely close or trigger Mermaid
  const nested = [
    bt4 + "markdown",
    bt3 + "mermaid",
    "graph TD",
    "  A --> B",
    bt3,
    bt4,
    "外层正文保留。",
  ].join("\n");
  const nestedHtml = R.mdRender(nested);
  assert.ok(!nestedHtml.includes("rv-mermaid"), "inner mermaid fence inside 4-backtick markdown fence must stay literal code");
  assert.ok(nestedHtml.includes("<p>外层正文保留。</p>"), "text after 4-backtick fence preserved");

  // Tilde fence ~~~mermaid works, and 4-space indented fence stays prose/not a fence
  const tildeHtml = R.mdRender("~~~mermaid\ngraph LR\n  A --> B\n~~~");
  assert.ok(tildeHtml.includes('data-rv-kind="mermaid"'), "tilde ~~~mermaid fence renders Mermaid card");

  const indent4 = R.mdRender("    " + bt3 + "mermaid\n    graph LR\n    " + bt3);
  assert.ok(!indent4.includes('data-rv-kind="mermaid"'), "4-space indented fence must not open a fenced block");

  // Closing fence with trailing text must not close the block
  const fakeClose = R.mdRender(bt3 + "js\nconst a = 1;\n" + bt3 + "not-a-close\nconst b = 2;\n" + bt3);
  assert.ok(fakeClose.includes("const b") && fakeClose.includes("not-a-close"), "trailing text after closing fence must not close fence");
}

// 2. Mermaid card markup, theme config, lazy render, source toggle & silent syntax-error fallback
async function testMermaidLifecycle() {
  const bt3 = "\x60\x60\x60";
  const mmdHtml = R.mdRender("前文\n\n" + bt3 + "mermaid\ngraph TD\n  A[提交] --> B[验收]\n" + bt3 + "\n\n后文");
  assert.ok(mmdHtml.includes('data-rv-kind="mermaid"'), "mermaid fence emits mermaid card");
  assert.ok(mmdHtml.includes('class="rv-float-acts"') && !mmdHtml.includes('class="rv-bar"'), "mermaid card uses floating micro-actions without persistent top bar");
  assert.ok(mmdHtml.includes('data-rv-toggle="source"') && mmdHtml.includes('data-rv-expand="mermaid"'), "mermaid card includes source toggle & fullscreen expand");
  assert.ok(mmdHtml.includes("<p>前文</p>") && mmdHtml.includes("<p>后文</p>"), "surrounding prose preserved");

  // Theme config security & monochrome audit
  const lightCfg = R.mermaidConfig(false);
  const darkCfg = R.mermaidConfig(true);
  assert.equal(lightCfg.securityLevel, "strict");
  assert.equal(darkCfg.securityLevel, "strict");
  assert.equal(lightCfg.suppressErrorRendering, true);
  assert.equal(darkCfg.suppressErrorRendering, true);
  assert.equal(lightCfg.startOnLoad, false);
  assert.equal(lightCfg.themeVariables.background, "#ffffff");
  assert.equal(darkCfg.themeVariables.background, "#111113");
  assert.ok(lightCfg.themeCSS.includes("stroke-width: 1px") && lightCfg.themeCSS.includes("rx: 8px"), "Mermaid themeCSS enforces 1px hairline strokes and 8px rounded corners");

  // Mock global mermaid to test valid render, cache reuse, theme re-render, and silent syntax-error fallback
  let renderCalls = 0;
  let lastInitDark = null;
  ctx.mermaid = {
    initialize(cfg) { lastInitDark = !!cfg.themeVariables.darkMode; },
    async parse(code) { return !code.includes("BROKEN_SYNTAX"); },
    async render(id, code) {
      renderCalls++;
      return { svg: '<svg viewBox="0 0 200 80" data-theme="' + (lastInitDark ? "dark" : "light") + '"><text>' + code.slice(0, 12) + "</text></svg>" };
    },
  };

  function makeMermaidCardDom(code, hash) {
    const card = makeEl("div");
    card.className = "rv-card rv-mermaid is-pending";
    card.setAttribute("data-rv-kind", "mermaid");
    card.setAttribute("data-rv-hash", hash);
    card.setAttribute("data-rv-mode", "diagram");
    const acts = makeEl("div"); acts.className = "rv-float-acts";
    const toggleBtn = makeEl("button"); toggleBtn.className = "rv-ico"; toggleBtn.setAttribute("data-rv-toggle", "source"); toggleBtn.title = "查看源码";
    const expBtn = makeEl("button"); expBtn.className = "rv-ico"; expBtn.setAttribute("data-rv-expand", "mermaid"); expBtn.hidden = true;
    acts.appendChild(toggleBtn); acts.appendChild(expBtn);
    const stage = makeEl("div"); stage.className = "rv-stage rv-mermaid-stage";
    const pre = makeEl("pre"); pre.className = "rv-source"; pre.hidden = true;
    const codeEl = makeEl("code"); codeEl.textContent = code;
    pre.appendChild(codeEl);
    card.appendChild(acts); card.appendChild(stage); card.appendChild(pre);
    bodyEl.appendChild(card);
    return { card, acts, stage, pre, toggleBtn, expBtn };
  }

  // Valid diagram
  const okCard = makeMermaidCardDom("graph TD\nA-->B", "hash-ok-1");
  R.renderMermaidCard(okCard.card);
  await new Promise((r) => setImmediate(r));
  await new Promise((r) => setImmediate(r));
  assert.equal(renderCalls, 1, "first render calls mermaid.render once");
  assert.ok(okCard.card.classList.contains("is-ready"), "valid diagram marks card is-ready");
  assert.equal(okCard.expBtn.hidden, false, "expand button reveals after SVG renders");
  assert.ok(okCard.stage.innerHTML.includes('data-theme="light"'), "light theme SVG rendered");

  // Re-calling with same hash/theme must skip mermaid.render (zero flicker)
  R.renderMermaidCard(okCard.card);
  await new Promise((r) => setImmediate(r));
  assert.equal(renderCalls, 1, "same hash/theme must reuse existing SVG without re-rendering");

  // Source / diagram toggle
  R.setRichCardMode(okCard.card, "source");
  assert.equal(okCard.stage.hidden, true);
  assert.equal(okCard.pre.hidden, false);
  assert.equal(okCard.toggleBtn.getAttribute("aria-pressed"), "true");
  assert.equal(okCard.toggleBtn.title, "切回图表");
  R.setRichCardMode(okCard.card, "visual");
  assert.equal(okCard.stage.hidden, false);
  assert.equal(okCard.pre.hidden, true);
  assert.equal(okCard.toggleBtn.title, "查看源码");

  // Dark theme switch auto-rerenders ready cards
  docEl.dataset.theme = "dark";
  R.onRichVisualThemeChange();
  await new Promise((r) => setImmediate(r));
  await new Promise((r) => setImmediate(r));
  assert.equal(renderCalls, 2, "theme change re-renders ready Mermaid card");
  assert.ok(okCard.stage.innerHTML.includes('data-theme="dark"'), "dark theme SVG rendered");

  // Switching back to light hits mermaidSvgCache with ZERO extra mermaid.render call!
  docEl.dataset.theme = "light";
  R.onRichVisualThemeChange();
  assert.equal(renderCalls, 2, "switching back to cached theme must hit cache with 0 render calls");
  assert.ok(okCard.stage.innerHTML.includes('data-theme="light"'), "cached light SVG restored instantly");

  // Invalid Mermaid syntax silently degrades to plain code block with 0 console errors
  const badCard = makeMermaidCardDom("BROKEN_SYNTAX @@@", "hash-bad-1");
  R.renderMermaidCard(badCard.card);
  await new Promise((r) => setImmediate(r));
  await new Promise((r) => setImmediate(r));
  assert.ok(badCard.card.classList.contains("is-fallback"), "syntax error degrades to .is-fallback");
  assert.equal(badCard.acts.hidden, true, "fallback hides floating action pill");
  assert.equal(badCard.stage.hidden, true, "fallback hides diagram stage");
  assert.equal(badCard.pre.hidden, false, "fallback shows raw code block");
  assert.equal(warnings.length, 0, "syntax error must produce zero console warnings/errors");
}

// 3. Inline HTML cards: both triggers, zero false positives, strict sandbox, safe postMessage height
async function testHtmlCards() {
  // 3a) Explicit --file report.html attachment triggers frameless inline sandbox card
  const attHtml = R.renderEntryArtifacts({ id: "e-html-1", role: "collie", text: "报告已生成", files: ["~/reports/weekly.html", "~/reports/data.csv"] }, "");
  assert.ok(attHtml.includes('data-rv-kind="html-file"'), "explicit .html attachment renders inline HTML card");
  assert.ok(attHtml.includes('class="rv-float-acts"') && !attHtml.includes('class="rv-html-head"'), "HTML attachment uses floating micro-actions without top header bar");
  assert.ok(attHtml.includes('class="rv-html-foot"') && attHtml.includes('<button type="button" class="snap-head" data-open-file="~/reports/weekly.html"'), "HTML attachment includes subtle bottom drawer trigger preserving snap-head contract");
  assert.ok(!attHtml.includes("HTML · ~/reports/weekly.html"), "HTML attachment must not display raw file path noise inline");
  assert.ok(attHtml.includes("weekly.html") && attHtml.includes("在抽屉打开"), "footer shows clean filename and drawer cue");
  assert.ok(attHtml.includes('data-rv-open-win="~/reports/weekly.html"'), "floating new-window icon carries target path");
  assert.ok(attHtml.includes("data.csv"), "companion non-HTML files remain chips");

  // Prose mention ONLY (not in e.files) must NOT auto-render inline iframe card
  const proseOnly = R.renderEntryArtifacts({ id: "e-html-2", role: "collie", text: "参考 ~/tmp/mention-only.html" }, "");
  assert.ok(proseOnly.includes("snap-card") && !proseOnly.includes('data-rv-kind="html-file"'), "prose-only HTML path must NOT auto-embed inline iframe");

  // 3b) Explicit html-card and html preview vs normal html
  const bt3 = "\x60\x60\x60";
  const cardFence1 = R.mdRender(bt3 + "html-card 指标卡\n<div>KPI 99.9%</div>\n" + bt3);
  assert.ok(cardFence1.includes('data-rv-kind="html-code"') && cardFence1.includes('data-rv-title="指标卡"'), "html-card fence emits inline HTML card with accessible title");
  assert.ok(cardFence1.includes('class="rv-float-acts"') && !cardFence1.includes('class="rv-bar"'), "html-card uses floating micro-actions without persistent top bar");
  assert.ok(cardFence1.includes('data-rv-toggle="source"') && cardFence1.includes('data-rv-open-code-win="1"'), "html-card includes floating source toggle & new window icon");

  const cardFence2 = R.mdRender(bt3 + "html preview\n<p>Preview</p>\n" + bt3);
  assert.ok(cardFence2.includes('data-rv-kind="html-code"'), "html preview fence emits inline HTML card");

  const normalHtml = R.mdRender(bt3 + "html\n<div class=\"box\">plain code</div>\n" + bt3);
  assert.ok(!normalHtml.includes('data-rv-kind="html-code"') && normalHtml.includes('<pre><code data-lang="html">'), "normal html fence must stay highlighted code block");

  // 3c) Mount HTML code card and verify strict sandbox attributes & safe postMessage height validation
  assert.ok(!R.HTML_SANDBOX_ATTR.includes("allow-same-origin"), "HTML_SANDBOX_ATTR must NEVER allow-same-origin");

  const card = makeEl("section");
  card.className = "rv-card rv-html-card is-pending";
  card.setAttribute("data-rv-kind", "html-code");
  card.setAttribute("data-rv-hash", "html-hash-1");
  card.setAttribute("data-rv-title", "Demo Card");
  const stage = makeEl("div"); stage.className = "rv-html-stage"; stage.setAttribute("data-rv-stage", "");
  const pre = makeEl("pre"); pre.className = "rv-source"; pre.hidden = true;
  const code = makeEl("code"); code.textContent = "<div>Hello KPI</div>";
  pre.appendChild(code);
  card.appendChild(stage); card.appendChild(pre);
  bodyEl.appendChild(card);

  await R.mountHtmlCard(card);
  const iframe = stage.querySelector("iframe");
  assert.ok(iframe, "mountHtmlCard must create iframe inside stage");
  assert.equal(iframe.getAttribute("sandbox"), R.HTML_SANDBOX_ATTR, "iframe must use HTML_SANDBOX_ATTR");
  assert.ok(!iframe.getAttribute("sandbox").includes("allow-same-origin"), "iframe sandbox must never include allow-same-origin");
  assert.equal(iframe.getAttribute("referrerpolicy"), "no-referrer", "iframe must enforce no-referrer");
  assert.ok(iframe.getAttribute("allow").includes("focus-without-user-activation 'none'"), "iframe must block focus stealing");
  assert.ok(typeof iframe.srcdoc === "string" && iframe.srcdoc.includes("Hello KPI"), "iframe uses srcdoc with fragment wrapped cleanly");

  // Re-mounting same hash must be a no-op (no duplicate iframe)
  await R.mountHtmlCard(card);
  assert.equal(stage.children.length, 1, "re-mounting same hash must preserve existing iframe");

  const [[frameId, rec]] = [...R.htmlCardFrames.entries()];
  // Spoofed source must be rejected
  R.handleRichCardMessage({ source: {}, origin: "null", data: { type: "collie:card-height", id: frameId, token: rec.token, height: 340 } });
  assert.equal(stage.style.height, "", "spoofed event.source must be rejected");
  // Wrong token must be rejected
  R.handleRichCardMessage({ source: iframe.contentWindow, origin: "null", data: { type: "collie:card-height", id: frameId, token: "wrong-token", height: 340 } });
  assert.equal(stage.style.height, "", "wrong token must be rejected");
  // Valid bridge message updates height clamped cleanly
  R.handleRichCardMessage({ source: iframe.contentWindow, origin: "null", data: { type: "collie:card-height", id: frameId, token: rec.token, height: 340 } });
  assert.equal(stage.style.height, "342px", "valid height message updates stage height");
  // Excessive height clamped to desktop max 460px
  R.handleRichCardMessage({ source: iframe.contentWindow, origin: "null", data: { type: "collie:card-height", id: frameId, token: rec.token, height: 9999 } });
  assert.equal(stage.style.height, "460px", "desktop height must clamp at 460px");
  // Mobile viewport clamps to 380px
  ctx.innerWidth = 390;
  R.handleRichCardMessage({ source: iframe.contentWindow, origin: "null", data: { type: "collie:card-height", id: frameId, token: rec.token, height: 9999 } });
  assert.equal(stage.style.height, "380px", "mobile height must clamp at 380px");
  ctx.innerWidth = 1440;
  // 3d) HTML theme passthrough (data-theme, color-scheme, --collie-* semantic vars) & live postMessage sync without iframe rebuild
  const fullDoc = '<!doctype html><html lang="zh-CN"><head><style>body{background:#fafafa}</style></head><body><h1>Report</h1></body></html>';
  const injectedDark = R.buildSandboxedCardSrcdoc(fullDoc, { frameId: "f1", token: "t1", isFragment: false, isDark: true });
  assert.ok(injectedDark.includes('data-theme="dark"'), "full HTML doc receives data-theme=dark");
  assert.ok(injectedDark.includes('name="color-scheme" content="dark"'), "full HTML doc receives color-scheme meta");
  assert.ok(injectedDark.includes("--collie-bg:#111113") && injectedDark.includes("--collie-fg:#ededeb") && injectedDark.includes("--collie-surface:#151518") && injectedDark.includes("--collie-line:#26262b") && injectedDark.includes("--collie-muted:#858580"), "srcdoc injects Collie semantic theme variables");
  assert.ok(injectedDark.includes("body{background:#fafafa}"), "page original styles remain intact");
  assert.ok(injectedDark.indexOf('id="collie-theme-vars"') < injectedDark.indexOf("body{background:#fafafa}"), "injected theme vars precede author styles so author styles are not overridden");

  // Bare fragment html-card carries theme, seamless --collie-bg (no gray shell), & zero extra padding
  assert.ok(iframe.srcdoc.includes('data-theme="light"') && iframe.srcdoc.includes("--collie-bg:#ffffff"), "html-card fragment carries current light theme & Collie vars");
  assert.ok(iframe.srcdoc.includes("padding:0;background:var(--collie-bg)"), "html-card fragment blends seamlessly with --collie-bg without gray shell or padding");
  assert.equal(iframe._getSrcdocWrites(), 1, "iframe srcdoc written exactly once on mount");

  // Mobile tap inside sandbox iframe toggles .is-acts-open via validated collie:card-tap message
  ctx.innerWidth = 390;
  R.handleRichCardMessage({ source: iframe.contentWindow, origin: "null", data: { type: "collie:card-tap", id: frameId, token: rec.token } });
  assert.equal(card.classList.contains("is-acts-open"), true, "mobile card-tap reveals floating micro-actions");
  R.handleRichCardMessage({ source: iframe.contentWindow, origin: "null", data: { type: "collie:card-tap", id: frameId, token: rec.token } });
  assert.equal(card.classList.contains("is-acts-open"), false, "second mobile card-tap hides floating micro-actions");
  ctx.innerWidth = 1440;

  // Switching Collie theme pushes postMessage({ type: "collie-theme", theme }) without rebuilding iframe or rewriting srcdoc!
  docEl.dataset.theme = "dark";
  R.onRichVisualThemeChange();
  assert.equal(stage.querySelector("iframe"), iframe, "theme switch must NEVER recreate mounted iframe");
  assert.equal(iframe._getSrcdocWrites(), 1, "theme switch must NEVER rewrite iframe.srcdoc");
  assert.equal(iframe.style.colorScheme, "dark", "iframe element colorScheme updates on dark switch");
  const lastMsgDark = iframe.contentWindow.posted[iframe.contentWindow.posted.length - 1];
  assert.equal(lastMsgDark?.msg?.type, "collie-theme", "dark switch posts type=collie-theme");
  assert.equal(lastMsgDark?.msg?.theme, "dark", "dark switch posts theme=dark");

  docEl.dataset.theme = "light";
  R.onRichVisualThemeChange();
  const lastMsgLight = iframe.contentWindow.posted[iframe.contentWindow.posted.length - 1];
  assert.equal(lastMsgLight?.msg?.type, "collie-theme", "light switch posts type=collie-theme");
  assert.equal(lastMsgLight?.msg?.theme, "light", "light switch posts theme=light");
  assert.equal(iframe._getSrcdocWrites(), 1, "switching back to light keeps srcdoc writes at 1");
}

// 4. Safe SVG direct rendering (svg fence) + strict XSS sanitization + silent fallback + hash cache reuse
async function testSvgSafeDirectRender() {
  const bt3 = "\x60\x60\x60";
  const safeSvgMd = R.mdRender(bt3 + 'svg\n<svg width="120" height="40"><rect width="120" height="40" fill="currentColor"/></svg>\n' + bt3);
  assert.ok(safeSvgMd.includes('data-rv-kind="svg"') && safeSvgMd.includes('data-rv-toggle="source"') && safeSvgMd.includes('data-rv-expand="svg"'), "svg fence emits rv-svg card with source toggle & lightbox button");

  // XSS attack vector sanitization audit
  const xssSvg = [
    '<svg width="160" height="60" onload="alert(1)" onclick="steal()">',
    '  <defs><linearGradient id="g1"><stop offset="0%" stop-color="#111"/></linearGradient></defs>',
    '  <style>@import url(https://evil.example/x.css); .a { fill: url(#g1); background: url(https://evil.example/t.png); }</style>',
    '  <script>alert("xss")</script>',
    '  <foreignObject width="100" height="50"><body xmlns="http://www.w3.org/1999/xhtml"><img src="x" onerror="alert(2)"/></body></foreignObject>',
    '  <iframe src="https://evil.example"></iframe>',
    '  <object data="javascript:alert(3)"></object>',
    '  <embed src="data:text/html,evil"/>',
    '  <a href="java&#x09;script:alert(4)" xlink:href="javascript:alert(5)"><rect width="40" height="20" onmouseover="alert(6)" fill="url(#g1)"/></a>',
    '  <image href="data:image/svg+xml;base64,PHN2Zz4=" xlink:href="https://evil.example/track.png" width="10" height="10"/>',
    '  <use href="#g1" xlink:href="https://evil.example/sprite.svg#a"/>',
    '  <set attributeName="href" to="javascript:alert(7)"/>',
    '  <circle cx="80" cy="30" r="18" stroke="currentColor" fill="none" onerror="alert(8)"/>',
    '</svg>',
  ].join("\n");

  const sanitized = R.sanitizeSvgElement(xssSvg);
  assert.ok(sanitized && sanitized.node, "valid svg root with dangerous children sanitizes cleanly");
  const out = sanitized.node.outerHTML;
  assert.ok(!/<script\b/i.test(out), "must strip <script>: " + out);
  assert.ok(!/<foreignObject\b/i.test(out), "must strip <foreignObject>: " + out);
  assert.ok(!/<iframe\b/i.test(out), "must strip <iframe>: " + out);
  assert.ok(!/<object\b/i.test(out), "must strip <object>: " + out);
  assert.ok(!/<embed\b/i.test(out), "must strip <embed>: " + out);
  assert.ok(!/<set\b/i.test(out), "must strip malicious SMIL <set>: " + out);
  assert.ok(!/\bon[a-z]+\s*=/i.test(out), "must strip ALL on* event attributes: " + out);
  assert.ok(!/javascript:|data:|@import|evil\.example/i.test(out), "must strip javascript:/data:/@import/external URLs: " + out);
  assert.ok(out.includes('xmlns="http://www.w3.org/2000/svg"'), "must inject SVG XML namespace before DOMParser parsing: " + out);
  assert.ok(out.includes('href="#g1"') && out.includes("url(#g1)"), "must preserve safe internal #fragment refs: " + out);
  assert.ok(out.includes('viewBox="0 0 160 60"') && out.includes("<circle"), "must synthesize viewBox and keep safe SVG shapes: " + out);
  assert.equal(sanitized.lightCanvas, false, "SVG using currentColor adapts directly without forced light paper box");

  // Hardcoded dark-ink SVG without dark background triggers lightCanvas readability safeguard
  const darkInk = R.sanitizeSvgElement('<svg viewBox="0 0 100 40"><path d="M0 20 L100 20" stroke="#111111" fill="none"/></svg>');
  assert.ok(darkInk && darkInk.lightCanvas === true, "dark-ink-only SVG enables lightCanvas for dark-mode legibility");

  // Non-<svg> root & malformed XML must return null and silently fall back to plain code block
  assert.equal(R.sanitizeSvgElement("<div>not svg</div>"), null, "non-svg root rejected");
  assert.equal(R.sanitizeSvgElement("<svg><rect></svg>"), null, "malformed XML rejected");

  function makeSvgCardDom(code, hash) {
    const card = makeEl("div");
    card.className = "rv-card rv-svg is-pending";
    card.setAttribute("data-rv-kind", "svg");
    card.setAttribute("data-rv-hash", hash);
    card.setAttribute("data-rv-mode", "diagram");
    const acts = makeEl("div"); acts.className = "rv-float-acts";
    const toggleBtn = makeEl("button"); toggleBtn.className = "rv-ico"; toggleBtn.setAttribute("data-rv-toggle", "source"); toggleBtn.title = "查看源码";
    const expBtn = makeEl("button"); expBtn.className = "rv-ico"; expBtn.setAttribute("data-rv-expand", "svg"); expBtn.hidden = true;
    acts.appendChild(toggleBtn); acts.appendChild(expBtn);
    const stage = makeEl("div"); stage.className = "rv-stage rv-svg-stage";
    const pre = makeEl("pre"); pre.className = "rv-source"; pre.hidden = true;
    const codeEl = makeEl("code"); codeEl.textContent = code;
    pre.appendChild(codeEl);
    card.appendChild(acts); card.appendChild(stage); card.appendChild(pre);
    bodyEl.appendChild(card);
    return { card, acts, stage, pre, toggleBtn, expBtn };
  }

  const okSvgCard = makeSvgCardDom('<svg viewBox="0 0 80 30"><rect width="80" height="30" fill="currentColor"/></svg>', "svg-ok-1");
  R.renderSvgCard(okSvgCard.card);
  assert.ok(okSvgCard.card.classList.contains("is-ready"), "valid svg card renders ready");
  assert.equal(okSvgCard.expBtn.hidden, false, "svg expand button enabled after render");
  assert.ok(okSvgCard.stage.querySelector("svg"), "sanitized svg element mounted directly via DOM node");
  R.setRichCardMode(okSvgCard.card, "source");
  assert.equal(okSvgCard.stage.hidden, true);
  assert.equal(okSvgCard.pre.hidden, false);
  assert.equal(okSvgCard.toggleBtn.title, "切回图表");
  R.setRichCardMode(okSvgCard.card, "visual");
  assert.equal(okSvgCard.stage.hidden, false);
  assert.equal(okSvgCard.pre.hidden, true);

  const badSvgCard = makeSvgCardDom("<div>not an svg</div>", "svg-bad-1");
  R.renderSvgCard(badSvgCard.card);
  assert.ok(badSvgCard.card.classList.contains("is-fallback"), "invalid svg silently degrades to .is-fallback");
  assert.equal(badSvgCard.acts.hidden, true);
  assert.equal(badSvgCard.stage.hidden, true);
  assert.equal(badSvgCard.pre.hidden, false);
}

// 5. Anti-flicker: SSE append reuses historical message DOM nodes untouched
async function testSseNoFlicker() {
  R.setView("collie");
  const scroller = makeEl("div");
  scroller.scrollHeight = 800;
  scroller.clientHeight = 800;
  scroller.scrollTop = 0;
  const box = getById("super-thread");
  scroller.appendChild(box);
  const now = Date.now();
  const bt3 = "\x60\x60\x60";
  const e1 = { id: "e1", role: "collie", text: bt3 + "mermaid\ngraph LR\nA-->B\n" + bt3, at: new Date(now - 60000).toISOString() };
  R.setSup({ router: { status: "idle", alive: true }, entries: [e1], workers: [] });
  R.renderSuper({ force: true });
  const firstSay = box.children.find((c) => c.classList.contains("say"));
  assert.ok(firstSay, "initial Collie message rendered");

  // Append a new SSE entry -> historical node MUST remain the exact same object reference!
  const e2 = { id: "e2", role: "collie", text: "新到达的 SSE 消息", at: new Date(now).toISOString() };
  R.setSup({ router: { status: "idle", alive: true }, entries: [e1, e2], workers: [] });
  R.renderSuper();
  const afterSay = box.children.find((c) => c._collieKey === "e1");
  assert.strictEqual(afterSay, firstSay, "SSE append must preserve exact historical DOM node (zero Mermaid/SVG/iframe flicker)");
  assert.ok(box._colliePatchStats.lastReused >= 1, "patchSuperThread must report reused historical nodes");
}

(async () => {
  await testMermaidLifecycle();
  await testHtmlCards();
  await testSvgSafeDirectRender();
  await testSseNoFlicker();
  console.log("ALL_RICH_VISUALS_TESTS_PASSED");
})().catch((err) => {
  console.error(err);
  process.exit(1);
});
`
	cmd := exec.Command(nodeBin, "-e", jsTest, root)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("rich visuals JS test failed: %v\n%s", err, string(out))
	}
	if !strings.Contains(string(out), "ALL_RICH_VISUALS_TESTS_PASSED") {
		t.Fatalf("unexpected test output:\n%s", string(out))
	}
}

func TestRouterDocIncludesRichReplyGuidance(t *testing.T) {
	t.Parallel()
	sp, _, _ := newPendingTestEnv(t)
	sp.bin = "collie"
	if err := sp.writeRouterFiles(); err != nil {
		t.Fatalf("writeRouterFiles: %v", err)
	}
	routerBytes, err := os.ReadFile(filepath.Join(sp.routerDir(), "ROUTER.md"))
	if err != nil {
		t.Fatal(err)
	}
	routerStr := string(routerBytes)
	start := strings.Index(routerStr, "## Talking to the human")
	end := strings.Index(routerStr, "## Memory")
	if start < 0 || end <= start {
		t.Fatalf("ROUTER.md missing expected section boundaries:\n%s", routerStr)
	}
	talkingSection := routerStr[start:end]
	for _, want := range []string{
		"Markdown tables",
		"Mermaid diagrams (`mermaid`)",
		"inline SVG (`svg`)",
		"lightweight HTML blocks (`html-card`)",
		"KaTeX math",
		"fenced code blocks",
		"`--file`",
		"plain text",
		"heavy visualizations or long HTML",
	} {
		if !strings.Contains(talkingSection, want) {
			t.Fatalf("ROUTER.md Talking to the human section missing %q:\n%s", want, talkingSection)
		}
	}
	for _, mirror := range []string{"AGENTS.md", "GEMINI.md", "CLAUDE.md"} {
		b, err := os.ReadFile(filepath.Join(sp.routerDir(), mirror))
		if err != nil {
			t.Fatalf("read %s: %v", mirror, err)
		}
		if string(b) != routerStr {
			t.Fatalf("%s diverged from ROUTER.md", mirror)
		}
	}
}
