package collie

import (
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestMobileCollieUpwardScrollRegressionJS(t *testing.T) {
	t.Parallel()
	nodeBin, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node not installed")
	}
	root := filepath.Join("..", "..")
	jsTest := `
const fs = require("fs");
const path = require("path");
const vm = require("vm");
const assert = require("assert/strict");

const appSrc = fs.readFileSync(path.join(process.argv[1], "web", "app.js"), "utf8");
const cssSrc = fs.readFileSync(path.join(process.argv[1], "web", "app.css"), "utf8");

// 1) CSS contracts for upward-scroll stability on mobile/Safari & Chromium.
assert.ok(
  cssSrc.includes("#super-thread > .say, #super-thread > .mine, #super-thread > .event { contain: layout style; }"),
  "#super-thread rows must keep layout/style containment",
);
assert.ok(
  !/#super-thread\s*>\s*\.say:nth-last-child[\s\S]*?content-visibility:\s*auto/.test(cssSrc),
  "#super-thread rows must NOT use content-visibility: auto above the viewport",
);
assert.ok(
  /\.viewer\.chatbg\s*\{[^}]*overscroll-behavior-y:\s*contain/.test(cssSrc),
  ".viewer.chatbg must contain vertical overscroll",
);

const VOID_TAGS = new Set(["IMG", "INPUT", "BR", "HR", "META", "LINK", "PATH", "CIRCLE", "RECT", "ELLIPSE"]);
function unescAttr(s) {
  return String(s || "").replace(/&quot;/g, '"').replace(/&#39;/g, "'").replace(/&lt;/g, "<").replace(/&gt;/g, ">").replace(/&amp;/g, "&");
}
function escAttr(s) {
  return String(s || "").replace(/&/g, "&amp;").replace(/"/g, "&quot;").replace(/</g, "&lt;").replace(/>/g, "&gt;");
}
function camelToDataAttr(prop) {
  return "data-" + String(prop).replace(/[A-Z]/g, (m) => "-" + m.toLowerCase());
}

class TextNode {
  constructor(text) {
    this.nodeType = 3;
    this.textContent = String(text ?? "");
    this.parentNode = null;
  }
  get outerHTML() { return this.textContent; }
}

class DomNode {
  constructor(tag = "DIV", id = "") {
    this.nodeType = tag === "#fragment" ? 11 : 1;
    this.tagName = String(tag).toUpperCase();
    this.childNodes = [];
    this.parentNode = null;
    this._attrs = new Map();
    this._listeners = new Map();
    this._classes = new Set();
    this.hidden = false;
    this.disabled = false;
    this.checked = false;
    this.value = "";
    this._scrollTop = 0;
    this._scrollHeight = 12000;
    this._scrollHeightReads = 0;
    this.scrollLeft = 0;
    this.clientWidth = 390;
    this.clientHeight = 700;
    this.offsetWidth = 390;
    this.offsetHeight = 700;
    this.offsetParent = {};
    this.isConnected = true;
    this._rect = null;
    this._rectReads = 0;
    this._styleProps = new Map();
    this._styleWrites = 0;
    this.naturalWidth = 0;
    this.naturalHeight = 0;
    const self = this;
    this.style = {
      height: "",
      opacity: "",
      transform: "",
      top: "",
      overflow: "",
      setProperty(k, v) { self._styleWrites++; self._styleProps.set(k, String(v)); },
      getPropertyValue(k) { return self._styleProps.get(k) || ""; },
      removeProperty(k) { self._styleProps.delete(k); },
    };
    this.classList = {
      add: (...cs) => { for (const c of cs) if (c) self._classes.add(c); self._syncClassAttr(); },
      remove: (...cs) => { for (const c of cs) self._classes.delete(c); self._syncClassAttr(); },
      toggle: (c, f) => {
        const on = f === undefined ? !self._classes.has(c) : !!f;
        if (on) self._classes.add(c); else self._classes.delete(c);
        self._syncClassAttr();
        return on;
      },
      contains: (c) => self._classes.has(c),
      toString: () => [...self._classes].join(" "),
    };
    this.dataset = new Proxy({}, {
      get(_, prop) { return typeof prop === "string" ? self._attrs.get(camelToDataAttr(prop)) : undefined; },
      set(_, prop, val) { self._attrs.set(camelToDataAttr(prop), String(val)); return true; },
      deleteProperty(_, prop) { self._attrs.delete(camelToDataAttr(prop)); return true; },
      has(_, prop) { return typeof prop === "string" && self._attrs.has(camelToDataAttr(prop)); },
    });
    if (id) this.setAttribute("id", id);
    if (this.tagName === "TEMPLATE") this.content = new DomNode("#fragment");
  }
  get scrollTop() { return this._scrollTop; }
  set scrollTop(v) { this._scrollTop = v; }
  get scrollHeight() { this._scrollHeightReads++; return this._scrollHeight; }
  set scrollHeight(v) { this._scrollHeight = v; }
  get src() { return this._attrs.get("src") || ""; }
  set src(v) { this._attrs.set("src", String(v)); }
  get width() { return Number(this._attrs.get("width") || 0); }
  set width(v) { this._attrs.set("width", String(v)); }
  get height() { return Number(this._attrs.get("height") || 0); }
  set height(v) { this._attrs.set("height", String(v)); }
  _syncClassAttr() {
    const s = [...this._classes].join(" ");
    if (s) this._attrs.set("class", s);
    else this._attrs.delete("class");
  }
  get id() { return this._attrs.get("id") || ""; }
  set id(v) { if (v) this._attrs.set("id", String(v)); else this._attrs.delete("id"); }
  get className() { return this._attrs.get("class") || ""; }
  set className(v) {
    this._classes = new Set(String(v || "").trim().split(/\s+/).filter(Boolean));
    this._syncClassAttr();
  }
  get title() { return this._attrs.get("title") || ""; }
  set title(v) { this._attrs.set("title", String(v)); }
  get parentElement() { return this.parentNode && this.parentNode.nodeType === 1 ? this.parentNode : null; }
  get children() { return this.childNodes.filter((n) => n.nodeType === 1); }
  get childElementCount() { return this.children.length; }
  get firstChild() { return this.childNodes[0] || null; }
  get firstElementChild() { return this.children[0] || null; }
  get lastElementChild() { const cs = this.children; return cs[cs.length - 1] || null; }
  get nextSibling() {
    if (!this.parentNode) return null;
    const i = this.parentNode.childNodes.indexOf(this);
    return i >= 0 ? this.parentNode.childNodes[i + 1] || null : null;
  }
  get nextElementSibling() {
    if (!this.parentNode) return null;
    const cs = this.parentNode.childNodes;
    const i = cs.indexOf(this);
    for (let j = i + 1; j < cs.length; j++) if (cs[j].nodeType === 1) return cs[j];
    return null;
  }
  get previousElementSibling() {
    if (!this.parentNode) return null;
    const cs = this.parentNode.childNodes;
    const i = cs.indexOf(this);
    for (let j = i - 1; j >= 0; j--) if (cs[j].nodeType === 1) return cs[j];
    return null;
  }
  contains(node) {
    let cur = node;
    while (cur) {
      if (cur === this) return true;
      cur = cur.parentNode;
    }
    return false;
  }
  setAttribute(k, v) {
    k = String(k);
    v = String(v);
    if (k === "class") { this.className = v; return; }
    if (k === "hidden") { this.hidden = true; return; }
    this._attrs.set(k, v);
  }
  getAttribute(k) { return this._attrs.has(k) ? this._attrs.get(k) : null; }
  removeAttribute(k) {
    if (k === "class") this._classes.clear();
    if (k === "hidden") this.hidden = false;
    this._attrs.delete(k);
  }
  appendChild(node) {
    if (node.nodeType === 11) {
      for (const ch of [...node.childNodes]) this.appendChild(ch);
      return node;
    }
    if (node.parentNode) node.parentNode.removeChild(node);
    node.parentNode = this;
    this.childNodes.push(node);
    return node;
  }
  append(...nodes) {
    for (const n of nodes) {
      if (n == null) continue;
      this.appendChild(typeof n === "string" ? new TextNode(n) : n);
    }
  }
  insertBefore(node, ref) {
    if (!ref) return this.appendChild(node);
    if (node.nodeType === 11) {
      for (const ch of [...node.childNodes]) this.insertBefore(ch, ref);
      return node;
    }
    if (node.parentNode) node.parentNode.removeChild(node);
    const idx = this.childNodes.indexOf(ref);
    if (idx < 0) return this.appendChild(node);
    node.parentNode = this;
    this.childNodes.splice(idx, 0, node);
    return node;
  }
  removeChild(node) {
    const idx = this.childNodes.indexOf(node);
    if (idx >= 0) {
      this.childNodes.splice(idx, 1);
      node.parentNode = null;
    }
    return node;
  }
  replaceChild(fresh, old) {
    const idx = this.childNodes.indexOf(old);
    if (idx < 0) return old;
    if (fresh.parentNode) fresh.parentNode.removeChild(fresh);
    fresh.parentNode = this;
    old.parentNode = null;
    this.childNodes[idx] = fresh;
    return old;
  }
  remove() { if (this.parentNode) this.parentNode.removeChild(this); }
  get textContent() { return this.childNodes.map((c) => c.textContent).join(""); }
  set textContent(v) {
    for (const c of this.childNodes) c.parentNode = null;
    this.childNodes = [];
    if (v !== "" && v != null) this.appendChild(new TextNode(String(v)));
  }
  get innerHTML() { return this.childNodes.map((c) => c.outerHTML).join(""); }
  set innerHTML(html) {
    const target = this.tagName === "TEMPLATE" ? this.content : this;
    for (const c of target.childNodes) c.parentNode = null;
    target.childNodes = [];
    parseHtmlInto(target, String(html || ""));
  }
  get outerHTML() {
    const tag = this.tagName.toLowerCase();
    let attrs = "";
    for (const [k, v] of this._attrs) attrs += " " + k + '="' + escAttr(v) + '"';
    if (VOID_TAGS.has(this.tagName)) return "<" + tag + attrs + ">";
    return "<" + tag + attrs + ">" + this.innerHTML + "</" + tag + ">";
  }
  addEventListener(type, fn) {
    if (!this._listeners.has(type)) this._listeners.set(type, []);
    this._listeners.get(type).push(fn);
  }
  removeEventListener(type, fn) {
    const ls = this._listeners.get(type);
    if (!ls) return;
    const i = ls.indexOf(fn);
    if (i >= 0) ls.splice(i, 1);
  }
  dispatch(type, ev = {}) {
    ev.target = ev.target || this;
    ev.currentTarget = this;
    ev.preventDefault = ev.preventDefault || (() => { ev.defaultPrevented = true; });
    ev.stopPropagation = ev.stopPropagation || (() => {});
    for (const fn of (this._listeners.get(type) || [])) fn(ev);
  }
  focus() { doc.activeElement = this; }
  blur() { if (doc.activeElement === this) doc.activeElement = null; }
  scrollTo(opts) { if (opts && typeof opts.top === "number") this._scrollTop = opts.top; }
  getBoundingClientRect() {
    this._rectReads++;
    if (this._rect) return this._rect;
    return { top: 0, bottom: this.clientHeight || 40, left: 0, right: this.clientWidth || 390, width: this.clientWidth || 390, height: this.clientHeight || 40 };
  }
  matches(sel) {
    return String(sel).split(",").some((part) => matchSimple(this, part.trim()));
  }
  closest(sel) {
    let cur = this;
    while (cur && cur.nodeType === 1) {
      if (cur.matches(sel)) return cur;
      cur = cur.parentElement;
    }
    return null;
  }
  querySelectorAll(sel) {
    const parts = String(sel).split(",").map((s) => s.trim()).filter(Boolean);
    const out = [];
    const walk = (node) => {
      for (const ch of node.childNodes) {
        if (ch.nodeType !== 1) continue;
        if (parts.some((p) => matchComplex(ch, p, this))) out.push(ch);
        walk(ch);
      }
    };
    walk(this);
    return out;
  }
  querySelector(sel) { return this.querySelectorAll(sel)[0] || null; }
}

function parseHtmlInto(parent, html) {
  const stack = [parent];
  const re = /<!--[\s\S]*?-->|<\/([a-zA-Z0-9-]+)\s*>|<([a-zA-Z0-9-]+)((?:\s+[^<>]*?)?)\s*(\/?)>|([^<]+)/g;
  let m;
  while ((m = re.exec(html))) {
    const cur = stack[stack.length - 1];
    if (m[5] !== undefined) {
      cur.appendChild(new TextNode(m[5]));
    } else if (m[1]) {
      const closeTag = m[1].toUpperCase();
      for (let i = stack.length - 1; i > 0; i--) {
        if (stack[i].tagName === closeTag) { stack.length = i; break; }
      }
    } else if (m[2]) {
      const el = new DomNode(m[2]);
      const attrStr = m[3] || "";
      const are = /([a-zA-Z0-9_:-]+)(?:\s*=\s*(?:"([^"]*)"|'([^']*)'|([^\s"'=<>]+)))?/g;
      let am;
      while ((am = are.exec(attrStr))) {
        const val = am[2] ?? am[3] ?? am[4] ?? "";
        el.setAttribute(am[1], unescAttr(val));
      }
      cur.appendChild(el);
      if (!m[4] && !VOID_TAGS.has(el.tagName)) stack.push(el);
    }
  }
}

function matchSimple(el, sel) {
  if (!sel) return false;
  sel = sel.replace(/^:scope\s*/, "");
  if (!sel || sel === "*") return true;
  if (sel.includes(":not(")) {
    const m = sel.match(/^(.*?):not\(([^)]+)\)(.*)$/);
    if (m) return matchSimple(el, (m[1] + m[3]).trim() || "*") && !matchSimple(el, m[2].trim());
  }
  let rest = sel;
  const tagM = rest.match(/^([a-zA-Z][a-zA-Z0-9-]*)/);
  if (tagM) {
    if (el.tagName !== tagM[1].toUpperCase()) return false;
    rest = rest.slice(tagM[0].length);
  }
  const idRe = /#([a-zA-Z0-9_-]+)/g;
  let im;
  while ((im = idRe.exec(rest))) if (el.id !== im[1]) return false;
  rest = rest.replace(idRe, "");
  const attrRe = /\[([a-zA-Z0-9_-]+)(?:=(?:"([^"]*)"|'([^']*)'|([^\]]*)))?\]/g;
  let am;
  while ((am = attrRe.exec(rest))) {
    const k = am[1], want = am[2] ?? am[3] ?? am[4];
    const has = el._attrs.has(k);
    if (!has) return false;
    if (want !== undefined && el._attrs.get(k) !== want) return false;
  }
  rest = rest.replace(attrRe, "");
  const clsRe = /\.([a-zA-Z0-9_-]+)/g;
  let cm;
  while ((cm = clsRe.exec(rest))) if (!el._classes.has(cm[1])) return false;
  return true;
}

function matchComplex(el, sel, root) {
  const s = sel.trim();
  if (s.startsWith(":scope >")) {
    const sub = s.slice(8).trim();
    return el.parentNode === root && matchSimple(el, sub);
  }
  const toks = s.split(/\s+/).filter(Boolean);
  if (toks.length === 1) return matchSimple(el, toks[0]);
  if (!matchSimple(el, toks[toks.length - 1])) return false;
  let cur = el.parentElement;
  for (let i = toks.length - 2; i >= 0; i--) {
    if (toks[i] === ">") continue;
    let matched = false;
    while (cur) {
      if (matchSimple(cur, toks[i])) { matched = true; cur = cur.parentElement; break; }
      cur = cur.parentElement;
    }
    if (!matched) return false;
  }
  return true;
}

const byId = new Map();
const bySel = new Map();
const rootEl = new DomNode("HTML");
const bodyEl = new DomNode("BODY");
rootEl.appendChild(bodyEl);
const getEl = (sel) => {
  const m = /^#([a-zA-Z0-9_-]+)$/.exec(sel);
  if (m) {
    const id = m[1];
    if (!byId.has(id)) {
      const n = new DomNode("DIV", id);
      byId.set(id, n);
      bodyEl.appendChild(n);
    }
    return byId.get(id);
  }
  if (!bySel.has(sel)) bySel.set(sel, new DomNode("DIV"));
  return bySel.get(sel);
};

// Build #v-collie > .viewer.chatbg > #super-thread hierarchy
const vCollie = getEl("#v-collie");
const scroller = new DomNode("DIV");
scroller.className = "viewer chatbg";
const superThread = new DomNode("DIV", "super-thread");
superThread.className = "thread";
byId.set("super-thread", superThread);
scroller.appendChild(superThread);
vCollie.appendChild(scroller);

for (const id of [
  "app", "login", "login-error", "login-tip", "conn", "herdr-status",
  "nav-collie", "nav-auto", "nav-devices", "nav-prefs", "nav-queue", "nav-unread",
  "super-working", "crew", "router-kind", "router-status", "router-queue", "router-ctx", "router-model",
  "super-jump", "super-jump-badge", "pending-wrap", "pending-bar", "pending-list",
  "super-composer", "super-input", "re-draft", "t-machine", "t-kind", "t-model", "t-effort",
  "collie-threads-btn", "collie-threads-pop", "collie-threads-list", "collie-threads-count",
  "td-drawer", "td-body", "td-list", "td-input", "td-workers", "td-title", "td-sub",
  "v-auto", "v-devices", "v-prefs", "v-ext", "v-file", "detail", "viewer", "screen", "log", "thread",
  "jump", "gone", "live", "super-search", "super-search-btn", "super-search-input",
  "super-search-clear", "super-search-close", "super-search-meta", "super-search-panel",
  "super-search-head", "super-search-list", "new-btn", "launch-form", "launch-go", "launch-error",
  "l-prompt", "l-model", "l-effort", "l-cwd", "l-name", "l-machine", "sheet", "toasts",
  "lightbox", "lb-stage", "lb-img", "lb-name", "lb-count", "lb-meta", "lb-status",
  "lb-zoom", "lb-zoom-pct", "lb-open", "lb-dl", "lb-prev", "lb-next", "lb-close",
  "collie-pending-bar", "settings-pending-bar", "auto-body", "devices-body", "prefs-body",
  "rv-scroller", "rv-dl", "rv-home", "x-thread",
]) {
  getEl("#" + id);
}
for (const formSel of ["#super-composer", "#composer", "#x-composer", "#td-composer"]) {
  const f = getEl(formSel);
  for (const [tag, cls] of [["TEXTAREA", ""], ["BUTTON", "send"], ["DIV", "files"], ["BUTTON", "attach"], ["INPUT", ""]]) {
    const ch = new DomNode(tag);
    if (cls) ch.className = cls;
    if (tag === "INPUT") ch.setAttribute("type", "file");
    f.appendChild(ch);
  }
}
const reDraft = getEl("#re-draft");
reDraft.appendChild(new DomNode("BUTTON"));
getEl("#super-jump").appendChild(getEl("#super-jump-badge"));
getEl("#super-jump").hidden = true;
getEl("#super-jump-badge").hidden = true;
getEl("#super-working").hidden = true;

const docListeners = new Map();
let efpImpl = () => [];
let efpCalls = 0;

const doc = {
  body: bodyEl,
  documentElement: rootEl,
  hidden: false,
  activeElement: null,
  hasFocus: () => true,
  createElement: (tag) => new DomNode(tag),
  createDocumentFragment: () => new DomNode("#fragment"),
  getElementById: (id) => getEl("#" + id),
  querySelector: (sel) => {
    if (sel.startsWith("#") && !sel.includes(" ") && !sel.includes(".")) return getEl(sel);
    const hit = bodyEl.querySelector(sel);
    if (hit) return hit;
    if (sel.includes("dialog[open]") || sel.includes(".msg[data-id") || sel.includes(".event[data-id")) return null;
    return getEl(sel);
  },
  querySelectorAll: (sel) => bodyEl.querySelectorAll(sel),
  elementsFromPoint: (x, y) => { efpCalls++; return efpImpl(x, y); },
  addEventListener: (t, fn) => {
    if (!docListeners.has(t)) docListeners.set(t, []);
    docListeners.get(t).push(fn);
  },
  removeEventListener: () => {},
  dispatch: (t, ev = {}) => {
    for (const fn of (docListeners.get(t) || [])) fn(ev);
  },
};

let liveES = null;
class FakeEventSource {
  constructor() { this.readyState = 1; this.ls = {}; liveES = this; }
  addEventListener(t, fn) { (this.ls[t] ||= []).push(fn); }
  emit(t, data) { for (const fn of (this.ls[t] || [])) fn({ data: JSON.stringify(data) }); }
  close() { this.readyState = 2; }
}

const vvListeners = new Map();
const fakeVV = {
  height: 844,
  offsetTop: 0,
  addEventListener: (t, fn) => { (vvListeners.get(t) || vvListeners.set(t, []).get(t)).push(fn); },
  dispatch: (t) => { for (const fn of (vvListeners.get(t) || [])) fn({}); },
};

let earlierResolver = null;
let earlierCalls = 0;
let rafQueue = [];
let rafSeq = 1;
const flushRAF = () => {
  const q = rafQueue.splice(0);
  for (const item of q) if (!item.canceled) item.fn(Date.now());
};

const ctx = vm.createContext({
  console,
  setTimeout,
  clearTimeout,
  setInterval: () => 0,
  clearInterval: () => {},
  performance: { now: () => Date.now() },
  Date,
  Math,
  JSON,
  Map,
  Set,
  WeakMap,
  Promise,
  RegExp,
  Array,
  Object,
  Number,
  String,
  Boolean,
  Error,
  URLSearchParams,
  Uint8Array,
  TextEncoder,
  atob,
  AbortController,
  Event,
  Blob: class { constructor(parts, opts) { this.parts = parts; this.type = opts?.type; } },
  URL: { createObjectURL: () => "blob:fake-thumb", revokeObjectURL: () => {} },
  CSS: { escape: (s) => String(s).replace(/([^a-zA-Z0-9_-])/g, "\\$1"), supports: () => false },
  Element: DomNode,
  HTMLDialogElement: { prototype: {} },
  EventSource: FakeEventSource,
  MutationObserver: class { observe() {} disconnect() {} },
  ResizeObserver: class { observe() {} disconnect() {} },
  getComputedStyle: () => ({ getPropertyValue: () => "" }),
  crypto: { randomUUID: () => "uuid-scroll-test" },
  navigator: { platform: "iPhone", userAgent: "iPhone Safari", clipboard: null },
  localStorage: { _m: new Map(), getItem(k) { return this._m.get(k) ?? null; }, setItem(k, v) { this._m.set(k, String(v)); }, removeItem(k) { this._m.delete(k); } },
  location: { href: "http://127.0.0.1:7788/#/collie", origin: "http://127.0.0.1:7788", hash: "#/collie", search: "", pathname: "/", hostname: "127.0.0.1" },
  history: { state: { collieIdx: 0 }, replaceState: () => {}, pushState: () => {}, back: () => {} },
  innerWidth: 390,
  innerHeight: 844,
  visualViewport: fakeVV,
  onscrollend: null,
  document: doc,
  matchMedia: (q) => ({ matches: /max-width:\s*760px|pointer:\s*coarse|hover:\s*none/.test(q), addEventListener: () => {} }),
  addEventListener: () => {},
  removeEventListener: () => {},
  dispatchEvent: () => {},
  requestAnimationFrame: (fn) => {
    const id = rafSeq++;
    rafQueue.push({ id, fn, canceled: false });
    return id;
  },
  cancelAnimationFrame: (id) => {
    for (const item of rafQueue) if (item.id === id) item.canceled = true;
  },
  fetch: async (url) => {
    const u = String(url);
    if (u.startsWith("/api/super/earlier")) {
      earlierCalls++;
      return new Promise((resolve) => {
        earlierResolver = (entries, more) => resolve({
          ok: true, status: 200, headers: { get: () => null },
          json: async () => ({ entries, more }),
        });
      });
    }
    if (u.startsWith("/api/session")) return { ok: true, status: 200, headers: { get: () => null }, json: async () => ({ ok: true }) };
    if (u.startsWith("/api/super")) return { ok: true, status: 200, headers: { get: () => null }, json: async () => ({ router: { status: "idle" }, entries: [], workers: [], pending: [], pinned: [] }) };
    if (u.startsWith("/api/state")) return { ok: true, status: 200, headers: { get: () => null }, json: async () => ({ ok: true, agents: [], shells: [], machines: [] }) };
    if (u.startsWith("/api/hubs")) return { ok: true, status: 200, headers: { get: () => null }, json: async () => ({ hubs: [] }) };
    return { ok: true, status: 200, headers: { get: () => null }, json: async () => ({ sessions: [], files: [] }) };
  },
});
ctx.window = ctx;
ctx.globalThis = ctx;

const hook = [
  "  window.__T = {",
  "    setSup: (s) => { sup = normSup(s); },",
  "    renderSuper: (o) => renderSuper(o),",
  "    loadOlder: (...a) => loadOlder(...a),",
  "    setView: (v) => { view = v; },",
  "    getActiveRestore: () => activeCollieRestore,",
  "    isCollieScrolling: () => collieScrolling,",
  "    getOlderShowRow: () => olderShowRow,",
  "    getThumbDims: () => thumbDims,",
  "    setVH: () => setVH(),",
  "  };",
].join("\n");
const marker = "  boot();\n})();";
assert.ok(appSrc.includes(marker), "boot marker present");
vm.runInContext(appSrc.replace(marker, hook + "\n" + marker), ctx);

const wait = (ms) => new Promise((r) => setTimeout(r, ms));
const mkEntry = (n, role, text, extra = {}) => ({
  id: "e" + n,
  role,
  text,
  at: new Date(Date.UTC(2026, 10, 26, 10, Math.floor(n / 60), n % 60)).toISOString(),
  ...extra,
});

(async () => {
  await wait(30);
  flushRAF();
  const T = ctx.__T;
  T.setView("collie");

  // Seed 200 messages (e101..e300) with file paths "/" and an image path on e250.
  const initial200 = [];
  for (let i = 101; i <= 300; i++) {
    const role = i % 2 === 0 ? "collie" : "user";
    const text = i === 250
      ? "查看截图 /tmp/demo/shot.png 和路径 /tmp/demo/app.js"
      : (role === "collie" ? "排查 /tmp/demo/file_" + i + ".go 完成" : "好的 #" + i);
    initial200.push(mkEntry(i, role, text));
  }
  T.setSup({ router: { status: "idle", alive: true }, entries: initial200, workers: [], pending: [], pinned: [], earlier: true });
  T.renderSuper({ force: true });
  flushRAF();

  const rowsBefore = [...superThread.children];
  const msgEls = superThread.querySelectorAll(".msg[data-id]");
  assert.equal(msgEls.length, 200, "200 initial messages rendered");

  // 2) Thumbnail image load must NOT replace ANY Collie message nodes (0 DOM replacements!).
  const thumbImg = superThread.querySelector(".thumb[data-img] img");
  assert.ok(thumbImg, "thumbnail img rendered for /tmp/demo/shot.png");
  assert.equal(thumbImg.getAttribute("decoding"), "async", "thumbnail img uses decoding=async");
  thumbImg.naturalWidth = 640;
  thumbImg.naturalHeight = 480;
  superThread.dispatch("load", { target: thumbImg });
  const dim = T.getThumbDims().get("/tmp/demo/shot.png");
  assert.ok(dim && dim.w === 640 && dim.h === 480, "thumbDims recorded");
  assert.equal(thumbImg.width, 640, "live img width stamped in-place");
  assert.equal(thumbImg.height, 480, "live img height stamped in-place");

  // Trigger a pin update on e300 via renderSuper -> only e300 updates, all other 199 messages reused!
  T.setSup({ router: { status: "idle", alive: true }, entries: initial200, workers: [], pending: [], pinned: [{ id: "e300", at: initial200[199].at, role: "collie", summary: "p" }], earlier: true });
  T.renderSuper();
  assert.equal(superThread._colliePatchStats.lastUpdated, 1, "only pinned e300 updated, ZERO unrelated collie messages replaced after image load");
  for (let i = 0; i < rowsBefore.length - 1; i++) {
    if (rowsBefore[i].querySelector?.('.msg[data-id="e300"]')) continue;
    assert.strictEqual(superThread.children[i], rowsBefore[i], "DOM identity preserved at row " + i);
  }

  // 3) renderSuper() fast-exit when unchanged must do ZERO captureCollieAnchor layout/efp calls!
  scroller.scrollHeight = 24000;
  scroller.clientHeight = 700;
  scroller.scrollTop = 9000;
  efpCalls = 0;
  T.renderSuper();
  assert.equal(efpCalls, 0, "unchanged renderSuper fast-exit must make 0 elementsFromPoint calls");

  // 4) Active upward scroll defers background SSE LIVE_SUPER until scrollend settles!
  // Simulate user scrolling upward above bottom (wait > programmaticScrollUntil).
  await wait(140);
  const rendersBeforeScroll = superThread._colliePatchStats.renders;
  scroller.scrollTop = 8500;
  scroller.dispatch("scroll");
  assert.equal(T.isCollieScrolling(), true, "upward scroll above bottom marks collieScrolling");

  // Background SSE arrives mid-scroll -> deferred (no DOM patch yet)!
  const withE301 = [...initial200, mkEntry(301, "collie", "后台推送的新消息 /tmp/x")];
  liveES.emit("super", { router: { status: "idle", alive: true }, entries: withE301, workers: [], pending: [], pinned: [], earlier: true });
  flushRAF();
  assert.equal(superThread._colliePatchStats.renders, rendersBeforeScroll, "SSE super update deferred during active upward scroll");

  // User scroll settles (scrollend + 80ms trailing coalesce) -> single flush lands cleanly!
  scroller.dispatch("scrollend");
  await wait(110);
  flushRAF();
  assert.equal(T.isCollieScrolling(), false, "collieScrolling cleared after scrollend settle");
  assert.equal(superThread._colliePatchStats.renders, rendersBeforeScroll + 1, "deferred SSE flushed once after scroll settled");
  assert.equal(superThread.querySelectorAll(".msg[data-id]").length, 201, "new SSE message rendered after settle");

  // 5) Proactive prefetch + loadOlder() fresh anchor + single-shot restore (NO 340ms rebound lock!).
  // Move scrollTop into prefetch zone (< 1.1 * 700 = 770) while well above top (> 80).
  scroller.scrollTop = 600;
  scroller.dispatch("scroll");
  flushRAF();
  assert.equal(earlierCalls, 1, "proactive prefetch triggered loadOlder at scrollTop=600 (< 770)");
  assert.equal(T.getOlderShowRow(), false, "silent prefetch does NOT insert .earlier-row mid-scroll");

  // While /api/super/earlier is in flight, user keeps scrolling UP from 600 -> 200, bringing e104 to top!
  scroller.scrollTop = 200;
  const e104 = superThread.querySelector('.msg[data-id="e104"]');
  assert.ok(e104, "e104 present");
  scroller._rect = { top: 60, bottom: 760, left: 0, right: 390, width: 390, height: 700 };
  e104._rect = { top: 82, bottom: 142, left: 16, right: 374, width: 358, height: 60 }; // offset = +22px from scroller.top
  efpImpl = () => [e104];

  // Resolve /api/super/earlier with 100 older messages (e1..e100).
  const older100 = [];
  for (let i = 1; i <= 100; i++) older100.push(mkEntry(i, i % 2 === 0 ? "collie" : "user", "更早历史 #" + i));

  // Simulate layout shift when 100 messages prepend (+12000px below top): update e104._rect right when insertBefore happens!
  const origInsertBefore = superThread.insertBefore.bind(superThread);
  superThread.insertBefore = (node, ref) => {
    const res = origInsertBefore(node, ref);
    if (superThread.querySelector('.msg[data-id="e1"]')) {
      scroller._scrollHeight = 36000;
      e104._rect = { top: 82 + 12000, bottom: 142 + 12000, left: 16, right: 374, width: 358, height: 60 };
    }
    return res;
  };

  earlierResolver(older100, false);
  await wait(20);

  assert.equal(superThread.querySelectorAll(".msg[data-id]").length, 301, "100 older entries prepended");
  assert.equal(scroller.scrollTop, 12200, "fresh post-scroll anchor compensated exact +12000px prepend height (200 + 12000)");
  assert.equal(T.getActiveRestore(), null, "loadOlder must NOT arm 340ms activeCollieRestore lock");

  // User immediately scrolls UP another 500px on next frame AND background SSE arrives -> ZERO rebound!
  scroller.scrollTop = 11700;
  flushRAF();
  assert.equal(scroller.scrollTop, 11700, "next animation frame must NOT yank scrollTop back after loadOlder");
  const e100 = superThread.querySelector('.msg[data-id="e100"]');
  e100._rect = { top: 75, bottom: 135, left: 16, right: 374, width: 358, height: 60 };
  efpImpl = () => [e100];
  T.setSup({ router: { status: "idle", alive: true }, entries: [...withE301, mkEntry(302, "collie", "再来一条")], workers: [], pending: [], pinned: [], earlier: true });
  T.renderSuper();
  assert.equal(scroller.scrollTop, 11700, "SSE renderSuper after loadOlder must NOT teleport back to pre-load anchor");

  // 6) setVH() dirty-check skips unchanged visualViewport scroll events with 0 DOM/style work.
  T.setVH();
  const writesBefore = rootEl._styleWrites;
  const shReadsBefore = scroller._scrollHeightReads;
  for (let i = 0; i < 20; i++) {
    fakeVV.dispatch("scroll");
    flushRAF();
  }
  assert.equal(rootEl._styleWrites, writesBefore, "no-op visualViewport scroll must perform 0 :root CSS property writes");
  assert.equal(scroller._scrollHeightReads, shReadsBefore, "no-op visualViewport scroll must perform 0 scrollHeight reads");

  console.log("OK: all mobile Collie upward-scroll regression checks passed");
  process.exit(0);
})().catch((err) => {
  console.error(err);
  process.exit(1);
});
`
	cmd := exec.Command(nodeBin, "-e", jsTest, root)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("node mobile upward-scroll regression test failed: %v\n%s", err, string(out))
	}
	if !strings.Contains(string(out), "OK: all mobile Collie upward-scroll regression checks passed") {
		t.Fatalf("unexpected node output:\n%s", string(out))
	}
}
