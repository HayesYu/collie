package collie

import (
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestTermDrawerAnchorPerfRegressionJS(t *testing.T) {
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

// CSS contract checks for terminal viewport scrollback & drawer content-visibility.
assert.ok(cssSrc.includes(".screen .xterm-viewport") && cssSrc.includes("overflow-y: auto !important"), "terminal viewport enables native vertical scrollback");
assert.ok(cssSrc.includes("#td-list > .td-node:nth-last-child(n+31)") && cssSrc.includes("contain-intrinsic-size: auto none auto 5.5rem"), "off-screen drawer nodes use content-visibility: auto with contain-intrinsic-size");

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
    this._qs = {};
    this.hidden = false;
    this.disabled = false;
    this.checked = false;
    this.value = "";
    this._scrollTop = 0;
    this._scrollHeight = 400;
    this._scrollHeightReads = 0;
    this.scrollLeft = 0;
    this.clientWidth = 1100;
    this.clientHeight = 760;
    this.offsetWidth = 260;
    this.offsetHeight = 600;
    this.offsetParent = {};
    this.isConnected = true;
    this._rect = null;
    this._rectReads = 0;
    this._qsaCalls = 0;
    this._styleProps = new Map();
    const self = this;
    this.style = {
      height: "",
      opacity: "",
      transform: "",
      top: "",
      overflow: "",
      setProperty(k, v) { self._styleProps.set(k, String(v)); },
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
  prepend(...nodes) {
    const first = this.firstChild;
    for (const n of nodes) {
      if (n == null) continue;
      this.insertBefore(typeof n === "string" ? new TextNode(n) : n, first);
    }
  }
  after(...nodes) {
    if (!this.parentNode) return;
    const ref = this.nextSibling;
    for (const n of nodes) {
      if (n == null) continue;
      this.parentNode.insertBefore(typeof n === "string" ? new TextNode(n) : n, ref);
    }
  }
  scrollIntoView() {}
  setSelectionRange() {}
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
    if (this._onInnerWrite) this._onInnerWrite(html);
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
    return { top: 0, bottom: this.clientHeight || 40, left: 0, right: this.clientWidth || 720, width: this.clientWidth || 720, height: this.clientHeight || 40 };
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
    this._qsaCalls++;
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
  querySelector(sel) {
    if (this._qs[sel]) return this._qs[sel];
    return this.querySelectorAll(sel)[0] || null;
  }
}

function matchSimple(el, sel) {
  if (!sel || el.nodeType !== 1) return false;
  let s = sel.replace(/^:scope\s*>\s*/, "");
  const attrs = [];
  s = s.replace(/\[([^\]]+)\]/g, (_, inner) => {
    const eq = inner.indexOf("=");
    if (eq < 0) attrs.push([inner.trim(), null]);
    else attrs.push([inner.slice(0, eq).trim(), inner.slice(eq + 1).trim().replace(/^["']|["']$/g, "")]);
    return "";
  });
  let tag = "";
  const idMatch = s.match(/#([A-Za-z0-9_-]+)/);
  const reqId = idMatch ? idMatch[1] : "";
  s = s.replace(/#[A-Za-z0-9_-]+/g, "");
  const classes = [];
  s = s.replace(/\.([A-Za-z0-9_-]+)/g, (_, c) => { classes.push(c); return ""; });
  tag = s.trim().toUpperCase();
  if (tag && tag !== "*" && el.tagName !== tag) return false;
  if (reqId && el.id !== reqId) return false;
  for (const c of classes) if (!el._classes.has(c)) return false;
  for (const [k, v] of attrs) {
    if (!el._attrs.has(k)) return false;
    if (v !== null && el._attrs.get(k) !== v) return false;
  }
  return true;
}

function matchComplex(el, sel, root) {
  if (sel.startsWith(":scope > ")) {
    return el.parentNode === root && matchSimple(el, sel.slice(9).trim());
  }
  const tokens = sel.split(/\s+/).filter(Boolean);
  if (!matchSimple(el, tokens[tokens.length - 1])) return false;
  let cur = el.parentElement;
  for (let i = tokens.length - 2; i >= 0; i--) {
    let found = false;
    while (cur) {
      if (matchSimple(cur, tokens[i])) { found = true; cur = cur.parentElement; break; }
      cur = cur.parentElement;
    }
    if (!found) return false;
  }
  return true;
}

function parseHtmlInto(root, html) {
  const stack = [root];
  const tagRe = /<!--[\s\S]*?-->|<\/([A-Za-z][A-Za-z0-9-]*)\s*>|<([A-Za-z][A-Za-z0-9-]*)((?:\s+[^\s=/>]+(?:\s*=\s*(?:"[^"]*"|'[^']*'|[^\s"'=<>\x60]+))?)*)\s*(\/?)>/g;
  let pos = 0, m;
  while ((m = tagRe.exec(html)) !== null) {
    if (m.index > pos) {
      const txt = html.slice(pos, m.index);
      if (txt) stack[stack.length - 1].appendChild(new TextNode(txt));
    }
    pos = tagRe.lastIndex;
    if (m[0].startsWith("<!--")) continue;
    if (m[1]) {
      const closeTag = m[1].toUpperCase();
      for (let i = stack.length - 1; i >= 1; i--) {
        if (stack[i].tagName === closeTag) { stack.length = i; break; }
      }
      continue;
    }
    const tag = m[2].toUpperCase();
    const el = new DomNode(tag);
    const attrStr = m[3] || "";
    const attrRe = /([^\s=/>]+)(?:\s*=\s*(?:"([^"]*)"|'([^']*)'|([^\s"'=<>\x60]+)))?/g;
    let am;
    while ((am = attrRe.exec(attrStr)) !== null) {
      el.setAttribute(am[1], unescAttr(am[2] ?? am[3] ?? am[4] ?? ""));
    }
    stack[stack.length - 1].appendChild(el);
    const selfClosing = m[4] === "/" || VOID_TAGS.has(tag);
    if (!selfClosing) stack.push(el);
  }
  if (pos < html.length) stack[stack.length - 1].appendChild(new TextNode(html.slice(pos)));
}

const bySel = new Map();
function getEl(sel) {
  if (!bySel.has(sel)) {
    const tag = sel.includes("input") ? "TEXTAREA" : sel.includes("btn") || sel === "#td-widen" || sel === "#td-close" ? "BUTTON" : "DIV";
    const id = sel.startsWith("#") ? sel.slice(1) : "";
    const el = new DomNode(tag, id);
    if (sel === "#super-thread") {
      const sc = new DomNode("DIV", "super-scroller");
      sc.clientWidth = 900;
      sc.clientHeight = 600;
      sc.scrollHeight = 40000;
      sc.scrollTop = 20000;
      sc._rect = { top: 60, bottom: 660, left: 260, right: 1160, width: 900, height: 600 };
      el._rect = { top: -19940, bottom: 20060, left: 320, right: 1100, width: 780, height: 40000 };
      sc.appendChild(el);
    } else if (sel === "#td-list") {
      const body = getEl("#td-body");
      body.clientHeight = 600;
      body.scrollHeight = 5000;
      body.scrollTop = 4400;
      body.appendChild(el);
    } else {
      el.parentNode = new DomNode("DIV");
    }
    bySel.set(sel, el);
  }
  return bySel.get(sel);
}

for (const [formSel, inputSel] of [["#super-composer", "#super-input"], ["#composer", "#input"], ["#x-composer", "#x-input"], ["#td-composer", "#td-input"]]) {
  const f = getEl(formSel);
  f._qs["textarea"] = getEl(inputSel);
  f._qs[".send"] = formSel === "#td-composer" ? getEl("#td-send") : new DomNode("BUTTON");
  f._qs[".files"] = new DomNode("DIV");
  f._qs[".attach"] = new DomNode("BUTTON");
  f._qs['input[type="file"]'] = new DomNode("INPUT");
}
getEl("#re-draft")._qs[".re-who"] = new DomNode("SPAN");
getEl("#re-draft")._qs[".re-t"] = new DomNode("SPAN");
getEl("#re-draft")._qs["button"] = new DomNode("BUTTON");
getEl("#super-jump")._qs["#super-jump-badge"] = getEl("#super-jump-badge");
getEl("#super-jump").hidden = true;
getEl("#super-jump-badge").hidden = true;
getEl("#super-working").hidden = true;
getEl("#thread-drawer").hidden = true;
getEl("#thread-scrim").hidden = true;
getEl("#file-view").hidden = true;
getEl("#jump").hidden = true;
getEl(".side").offsetWidth = 260;

// Track MutationObservers registered on document.body!
const registeredObservers = [];
class FakeMutationObserver {
  constructor(cb) { this.cb = cb; }
  observe(target, opts) { registeredObservers.push({ cb: this.cb, target, opts }); }
  disconnect() {}
}

// Track fake xterm.js Terminal instance to verify scrollback, viewport rows, delta writes, selection & zero-reflow!
let createdTerm = null;
class FakeTerminal {
  constructor(opts) {
    this.opts = opts;
    this.options = { ...opts };
    this.cols = opts.cols;
    this.rows = opts.rows;
    this.scrollback = opts.scrollback || 0;
    this._hasSelection = false;
    this.resets = 0;
    this.resizes = [];
    this.writes = [];
    this.scrollToBottomCalls = 0;
    this.scrollToLineCalls = [];
    this._onScrollCbs = [];
    this._onSelCbs = [];
    this._deferredWrite = false;
    this._pendingCbs = [];
    this.buffer = { active: { baseY: 0, viewportY: 0 } };
    this._core = { _renderService: { dimensions: { css: { cell: { height: 18 } } } } };
    this.textarea = new DomNode("TEXTAREA");
    createdTerm = this;
  }
  attachCustomWheelEventHandler(fn) { this._wheelHandler = fn; }
  open(el) {
    const xt = new DomNode("DIV");
    xt.className = "xterm";
    const vp = new DomNode("DIV");
    vp.className = "xterm-viewport";
    const rowsEl = new DomNode("DIV");
    rowsEl.className = "xterm-rows";
    xt.appendChild(vp);
    xt.appendChild(rowsEl);
    el.appendChild(xt);
    this._rowsEl = rowsEl;
    this._vpEl = vp;
  }
  onData() {}
  onScroll(cb) { this._onScrollCbs.push(cb); }
  onSelectionChange(cb) { this._onSelCbs.push(cb); }
  hasSelection() { return this._hasSelection; }
  setSelection(v) {
    this._hasSelection = v;
    for (const cb of this._onSelCbs) cb();
  }
  reset() {
    this.resets++;
    this.buffer.active.baseY = 0;
    this.buffer.active.viewportY = 0;
  }
  resize(cols, rows) {
    this.cols = cols;
    this.rows = rows;
    this.resizes.push([cols, rows]);
  }
  write(data, cb) {
    this.writes.push(data);
    // Simulate scrollback growth on newlines so baseY/viewportY stay realistic.
    const nlCount = (data.match(/\r\n/g) || []).length;
    if (nlCount > 0) {
      const wasBottom = this.buffer.active.viewportY === this.buffer.active.baseY;
      this.buffer.active.baseY = Math.min(this.scrollback, Math.max(0, this.buffer.active.baseY + nlCount));
      if (wasBottom) this.buffer.active.viewportY = this.buffer.active.baseY;
    }
    if (cb) {
      if (this._deferredWrite) this._pendingCbs.push(cb);
      else cb();
    }
  }
  flushWrites() {
    while (this._pendingCbs.length) {
      const cb = this._pendingCbs.shift();
      cb();
    }
  }
  scrollToBottom() {
    this.scrollToBottomCalls++;
    this.buffer.active.viewportY = this.buffer.active.baseY;
  }
  scrollToLine(line) {
    this.scrollToLineCalls.push(line);
    this.buffer.active.viewportY = Math.max(0, Math.min(this.buffer.active.baseY, line));
  }
  focus() {}
  blur() {}
}

let efpImpl = null;
const docListeners = new Map();
const winListeners = new Map();
const storage = new Map();
let liveES = null;
let saySeq = 500;
let uuidSeq = 0;

class FakeES {
  constructor(url) {
    this.url = url;
    this.readyState = 1;
    this.ls = {};
    if (url === "/api/events") liveES = this;
  }
  addEventListener(t, fn) { (this.ls[t] ||= []).push(fn); }
  emit(t, obj) {
    const data = JSON.stringify(obj);
    for (const fn of (this.ls[t] || [])) fn({ data });
  }
  close() { this.readyState = 2; }
}

const doc = {
  documentElement: new DomNode("HTML"),
  body: new DomNode("BODY"),
  activeElement: null,
  hidden: false,
  hasFocus: () => true,
  createElement: (tag) => new DomNode(tag),
  createDocumentFragment: () => new DomNode("#fragment"),
  getElementById: (id) => getEl("#" + id),
  querySelector: (sel) => (sel === "dialog[open]" ? null : getEl(sel)),
  querySelectorAll: () => [],
  elementsFromPoint: (x, y) => (efpImpl ? efpImpl(x, y) : []),
  addEventListener(type, fn) {
    if (!docListeners.has(type)) docListeners.set(type, []);
    docListeners.get(type).push(fn);
  },
  dispatch(type, ev) {
    for (const fn of (docListeners.get(type) || [])) fn(ev);
  },
};

const okJSON = (data) => ({
  ok: true, status: 200, statusText: "OK",
  headers: { get: () => null },
  json: async () => data,
  text: async () => JSON.stringify(data),
});

let nextRafId = 1;
const ctx = vm.createContext({
  console, setTimeout, clearTimeout, setInterval: () => 0, clearInterval() {}, URL, URLSearchParams,
  Blob, Uint8Array, TextEncoder, atob, AbortController, Event,
  Map, Set, Date, Math, JSON, RegExp, Array, Object, Promise, Error, Number, String, Boolean,
  MutationObserver: FakeMutationObserver,
  ResizeObserver: class { observe() {} disconnect() {} },
  Element: DomNode,
  HTMLDialogElement: { prototype: {} },
  Terminal: FakeTerminal,
  onscrollend: null,
  getComputedStyle: (el) => ({
    paddingTop: "14px",
    paddingBottom: "14px",
    paddingLeft: "16px",
    getPropertyValue: (k) => (k === "--mono" ? "monospace" : k === "--term-fg" ? "#eee" : el.style.getPropertyValue(k)),
  }),
  crypto: { randomUUID: () => "uuid-p-" + (++uuidSeq) },
  navigator: { userAgent: "Linux Chrome", platform: "Linux" },
  location: { href: "http://127.0.0.1:7788/#/collie", origin: "http://127.0.0.1:7788", pathname: "/", search: "", hash: "#/collie", hostname: "127.0.0.1" },
  history: { state: { collieIdx: 0 }, replaceState() {}, pushState() {}, back() {} },
  localStorage: {
    getItem: (k) => (storage.has(k) ? storage.get(k) : null),
    setItem: (k, v) => storage.set(k, String(v)),
    removeItem: (k) => storage.delete(k),
  },
  document: doc,
  EventSource: FakeES,
  CSS: { escape: (s) => String(s), supports: () => true },
  matchMedia: () => ({ matches: false, addEventListener() {} }),
  requestAnimationFrame: (fn) => { const id = nextRafId++; fn(); return id; },
  cancelAnimationFrame: () => {},
  performance: { now: () => Date.now() },
  innerWidth: 1440,
  innerHeight: 900,
  addEventListener(type, fn) {
    if (!winListeners.has(type)) winListeners.set(type, []);
    winListeners.get(type).push(fn);
  },
  removeEventListener() {},
  dispatchEvent() {},
  fetch: async (url, init = {}) => {
    const u = String(url);
    if (u === "/api/session") return okJSON({ ok: true });
    if (u === "/api/super") return okJSON({ router: { status: "idle", alive: true }, entries: [], workers: [], pending: [], pinned: [] });
    if (u === "/api/state") return okJSON({ ok: true, agents: [], shells: [], machines: [{ id: "", name: "hub", local: true, online: true }] });
    if (u === "/api/hubs") return okJSON({ hubs: [] });
    if (u === "/api/updates") return okJSON({});
    if (u === "/api/external") return okJSON({ external: [] });
    if (u === "/api/history") return okJSON({ history: [] });
    if (u === "/api/super/settings") return okJSON({ settings: { worker_kind: "claude" } });
    if (u.startsWith("/api/models")) return okJSON({ models: [], efforts: [] });
    if (u === "/api/chat/read") return okJSON({ ok: true, changed: true });
    if (u === "/api/super/say") {
      const id = "e" + (++saySeq);
      return okJSON({ entry: id, trigger: "t" + saySeq });
    }
    return okJSON({});
  },
});
ctx.window = ctx;
ctx.globalThis = ctx;

const hook = [
  "  window.__T = {",
  "    setSup: (s) => { sup = normSup(s); },",
  "    renderSuper: (o) => renderSuper(o),",
  "    sendSuper: (...a) => sendSuper(...a),",
  "    openThreadDrawer: (...a) => openThreadDrawer(...a),",
  "    closeThreadDrawer: (...a) => closeThreadDrawer(...a),",
  "    setView: (v) => { view = v; },",
  "    setMode: (m) => { mode = m; },",
  "    ensureTerm: () => ensureTerm(),",
  "    drawScreen: (f, r) => drawScreen(f, r),",
  "    resetScreen: () => resetScreen(),",
  "    computeScreenDelta: (p, l, v) => computeScreenDelta(p, l, v),",
  "    captureCollieAnchor: (sc) => captureCollieAnchor(sc),",
  "    getScrollByRoute: () => scrollByRoute,",
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
  at: new Date(Date.UTC(2026, 10, 26, 10, 0, n % 60, n)).toISOString(),
  ...extra,
});

(async () => {
  await wait(30);
  const T = ctx.__T;
  assert.ok(liveES, "EventSource connected");

  // ═══════════════════════════════════════════════════════════════════════
  // 1) P0: High-frequency streaming terminal (#screen) & MutationObservers
  // ═══════════════════════════════════════════════════════════════════════
  ctx.localStorage.setItem("collie.devMode", "true");
  T.setView("pane");
  T.setMode("screen");
  const screenEl = getEl("#screen");
  screenEl.clientWidth = 1100;
  screenEl.clientHeight = 760;
  T.ensureTerm();
  assert.ok(createdTerm, "xterm Terminal created");
  assert.ok(createdTerm.scrollback >= 500, "xterm uses native scrollback >= 500 (got " + createdTerm.scrollback + ")");
  assert.ok(createdTerm.rows >= 12 && createdTerm.rows <= 80, "xterm viewport rows bounded to visible window (got " + createdTerm.rows + ")");

  const mkLines = (start, count) => {
    const arr = [];
    for (let i = start; i < start + count; i++) arr.push("\x1b[32mPASS\x1b[0m line #" + i);
    return arr;
  };

  // Frame 1: 500 lines initial load -> NEVER expands term.rows to 500!
  screenEl._scrollHeightReads = 0;
  const lines1 = mkLines(1, 500);
  T.drawScreen({ text: lines1.join("\n"), cols: 132, rows: 42 });
  assert.ok(createdTerm.rows <= 80, "term.rows must NEVER expand to 500 (got " + createdTerm.rows + ")");
  assert.equal(screenEl._scrollHeightReads, 0, "drawScreen must NEVER synchronously read #screen.scrollHeight");
  assert.equal(createdTerm.writes.length, 1);

  // Frame 2: 500-line rolling window advances by 18 lines (5Hz streaming log!)
  const resetsBefore = createdTerm.resets;
  const lines2 = mkLines(19, 500);
  T.drawScreen({ text: lines2.join("\n"), cols: 132, rows: 42 });
  assert.equal(createdTerm.resets, resetsBefore, "sliding 500-line frame must NOT call term.reset()");
  assert.equal(screenEl._scrollHeightReads, 0, "sliding frame must NOT read #screen.scrollHeight");
  assert.equal(createdTerm.writes.length, 2);
  const deltaWrite = createdTerm.writes[1];
  assert.ok(deltaWrite.includes("line #501") && deltaWrite.includes("line #518"), "delta write contains the 18 new lines");
  assert.ok(!deltaWrite.includes("line #19") && !deltaWrite.includes("line #499"), "delta write omits the 482 unchanged lines");

  // Frame 3: In-place TUI spinner update on line 499 of 500 -> single-row viewport patch!
  const lines3 = [...lines2];
  lines3[498] = "\x1b[36m⠋ working...\x1b[0m";
  T.drawScreen({ text: lines3.join("\n"), cols: 132, rows: 42 });
  assert.equal(createdTerm.resets, resetsBefore, "in-place viewport patch must NOT call term.reset()");
  assert.equal(createdTerm.writes.length, 3);
  assert.ok(createdTerm.writes[2].includes("⠋ working...") && createdTerm.writes[2].length < 120, "in-place update writes only the 1 changed row");

  // Frame 4: While user has text selected -> defers into pendingScreen, flushes on clear.
  const writesBeforeSel = createdTerm.writes.length;
  createdTerm.setSelection(true);
  const lines4 = mkLines(37, 500);
  T.drawScreen({ text: lines4.join("\n"), cols: 132, rows: 42 });
  assert.equal(createdTerm.writes.length, writesBeforeSel, "selection pauses live terminal redraw");
  createdTerm.setSelection(false);
  assert.equal(createdTerm.writes.length, writesBeforeSel + 1, "clearing selection flushes deferred frame");

  // Frame 5: Scrolled up above bottom with shifted head -> pauses & shows #jump; clicking #jump resumes.
  createdTerm.buffer.active.baseY = 460;
  createdTerm.buffer.active.viewportY = 120;
  const writesBeforeScrollUp = createdTerm.writes.length;
  const lines5 = mkLines(60, 500);
  T.drawScreen({ text: lines5.join("\n"), cols: 132, rows: 42 });
  assert.equal(createdTerm.writes.length, writesBeforeScrollUp, "scrolled-up reading freezes shifting output");
  assert.equal(getEl("#jump").hidden, false, "#jump button appears when paused above bottom");
  getEl("#jump").dispatch("click");
  assert.equal(createdTerm.buffer.active.viewportY, createdTerm.buffer.active.baseY, "clicking #jump snaps to bottom");
  assert.equal(getEl("#jump").hidden, true, "clicking #jump hides jump button");
  assert.equal(createdTerm.writes.length, writesBeforeScrollUp + 1, "clicking #jump flushes pending screen frame");

  // Frame 6: Coalescing bursty frames while term.write() is in flight!
  createdTerm._deferredWrite = true;
  const writesBeforeBurst = createdTerm.writes.length;
  T.drawScreen({ text: mkLines(80, 500).join("\n"), cols: 132, rows: 42 });
  T.drawScreen({ text: mkLines(90, 500).join("\n"), cols: 132, rows: 42 });
  T.drawScreen({ text: mkLines(100, 500).join("\n"), cols: 132, rows: 42 });
  assert.equal(createdTerm.writes.length, writesBeforeBurst + 1, "in-flight write coalesces intermediate burst frames");
  createdTerm._deferredWrite = false;
  createdTerm.flushWrites();
  assert.equal(createdTerm.writes.length, writesBeforeBurst + 2, "only the latest coalesced frame flushes after completion");
  assert.ok(createdTerm.writes[createdTerm.writes.length - 1].includes("line #599"), "coalesced flush writes the latest frame");

  // Both global MutationObservers must skip nested #screen/.xterm-rows mutations with 0 querySelectorAll calls!
  assert.ok(registeredObservers.length >= 2, "both select and thumb MutationObservers registered");
  const fakeRow = new DomNode("DIV");
  createdTerm._rowsEl.appendChild(fakeRow);
  const addedSpan = new DomNode("SPAN");
  addedSpan.appendChild(new DomNode("SPAN"));
  fakeRow.appendChild(addedSpan);
  for (const obs of registeredObservers) {
    obs.cb([{ target: fakeRow, addedNodes: [addedSpan] }]);
  }
  assert.equal(addedSpan._qsaCalls, 0, "global MutationObservers must skip #screen/.xterm-rows without scanning children");

  // ═══════════════════════════════════════════════════════════════════════
  // 2) P1: Big Topic Focus Drawer (#td-list) Keyed Incremental DOM Patch
  // ═══════════════════════════════════════════════════════════════════════
  T.setView("collie");
  const tdList = getEl("#td-list");
  let tdInnerWrites = 0;
  tdList._onInnerWrite = () => { tdInnerWrites++; };

  // Seed a 100-message reply chain rooted at e1.
  const entries100 = [mkEntry(1, "user", "话题起点：性能回归压测讨论")];
  for (let i = 2; i <= 100; i++) {
    entries100.push(mkEntry(i, i % 2 === 0 ? "collie" : "user", "抽屉第 " + i + " 条回复内容", {
      re: { entry: "e" + (i - 1), who: i % 2 === 0 ? "你" : "Collie", text: "prev" },
    }));
  }
  T.setSup({ router: { status: "idle", alive: true }, entries: entries100, workers: [], pending: [], pinned: [] });
  T.renderSuper({ force: true });
  T.openThreadDrawer("e1", "e100", "e100");
  assert.equal(tdInnerWrites, 0, "real DOM path must NEVER write #td-list.innerHTML");
  assert.equal(tdList._tdPatchStats.fullRebuilds, 1, "initial 100-msg drawer mount is 1 bulk mount");

  const snapTd100 = [...tdList.querySelectorAll(".td-node")];
  assert.equal(snapTd100.length, 100, "100 .td-node items mounted");

  // Send optimistic user reply in drawer -> 100/100 existing .td-node refs preserved!
  const pSend = T.sendSuper("抽屉内增量发送测试", { entry: "e100", who: "Collie", text: "prev" }, false, "cid-td-1", { scrollBottom: false });
  await wait(10);
  const nodesAfterOpt = [...tdList.querySelectorAll(".td-node")];
  assert.equal(nodesAfterOpt.length, 101, "optimistic drawer node appended");
  assert.equal(tdList._tdPatchStats.fullRebuilds, 1, "optimistic send must NOT full-rebuild #td-list");
  assert.equal(tdList._tdPatchStats.lastAdded, 1, "only 1 node added");
  assert.equal(tdList._tdPatchStats.lastReused, 100, "all 100 prior drawer nodes reused");
  for (let i = 0; i < 100; i++) {
    assert.strictEqual(nodesAfterOpt[i], snapTd100[i], "drawer DOM node identity preserved at " + i);
  }
  const optTdNode = nodesAfterOpt[100];
  await pSend;

  // SSE promotes optimistic reply to persisted e501 AND delivers Collie reply e502!
  const entries102 = [
    ...entries100,
    mkEntry(501, "user", "抽屉内增量发送测试", { client_msg_id: "cid-td-1", re: { entry: "e100", who: "Collie", text: "prev" } }),
    mkEntry(502, "collie", "抽屉 SSE 实时回推，验证旧节点 100% 复用", { re: { entry: "e501", who: "你", text: "抽屉内增量发送测试" } }),
  ];
  liveES.emit("super", { router: { status: "idle", alive: true }, entries: entries102, workers: [], pending: [], pinned: [] });
  const nodesAfterSse = [...tdList.querySelectorAll(".td-node")];
  assert.equal(nodesAfterSse.length, 102, "Collie reply appended in drawer");
  assert.equal(tdInnerWrites, 0, "SSE drawer updates must NEVER write #td-list.innerHTML");
  assert.equal(tdList._tdPatchStats.fullRebuilds, 1, "SSE drawer updates must NEVER full-rebuild");
  for (let i = 0; i < 100; i++) {
    assert.strictEqual(nodesAfterSse[i], snapTd100[i], "all 100 initial drawer nodes remain strictly identical after SSE");
  }
  assert.strictEqual(nodesAfterSse[100], optTdNode, "optimistic user drawer node promoted in-place via promotedPendingKeys");
  assert.equal(nodesAfterSse[100].dataset.entryId, "e501", "promoted drawer node received server entry id");
  assert.ok(nodesAfterSse[100].querySelector(".td-node-acts"), "promoted drawer node gained reply/locate actions");
  T.closeThreadDrawer({ immediate: true });

  // ═══════════════════════════════════════════════════════════════════════
  // 3) P1: captureCollieAnchor O(1) elementsFromPoint + scroll-settle
  // ═══════════════════════════════════════════════════════════════════════
  const threadBox = getEl("#super-thread");
  const scroller = threadBox.parentElement;
  // Seed 200 messages with synthetic geometry for anchor verification.
  const entries200 = [];
  for (let i = 1; i <= 200; i++) {
    entries200.push(mkEntry(i, i % 2 === 1 ? "user" : "collie", "锚点测试消息 #" + i));
  }
  T.setSup({ router: { status: "idle", alive: true }, entries: entries200, workers: [], pending: [], pinned: [] });
  T.renderSuper({ force: true });

  const allMsgEls = threadBox.querySelectorAll(".msg[data-id]");
  assert.equal(allMsgEls.length, 200);
  // Place message #120 right at top of scroller (sr.top = 60): e120 sits at top=74..124, e119 at top=10..56 (above edge=64).
  for (let i = 0; i < allMsgEls.length; i++) {
    const top = 74 + (i - 119) * 60;
    const rect = { top, bottom: top + 44, left: 340, right: 1080, width: 740, height: 44 };
    allMsgEls[i]._rect = rect;
    allMsgEls[i]._rectReads = 0;
    const row = allMsgEls[i].closest(".say, .mine");
    if (row) { row._rect = rect; row._rectReads = 0; }
  }
  threadBox._qsaCalls = 0;

  // Simulate elementsFromPoint hitting the .mine wrapper of message #120 (tests right-aligned user bubble extraction!).
  const row120 = allMsgEls[119].closest(".say, .mine");
  efpImpl = (x, y) => (y >= 60 && y <= 140 ? [row120, threadBox, scroller] : []);
  const ancHit = T.captureCollieAnchor(scroller);
  assert.ok(ancHit, "O(1) elementsFromPoint returns an anchor");
  assert.equal(ancHit.anchorId, "e120", "O(1) elementsFromPoint returns exact anchorId");
  assert.equal(ancHit.anchorOffset, 14, "O(1) elementsFromPoint returns exact 0px anchorOffset");
  assert.equal(threadBox._qsaCalls, 0, "O(1) path must make ZERO querySelectorAll scans across messages");
  const totalRectReads = allMsgEls.reduce((s, el) => s + el._rectReads, 0);
  assert.ok(totalRectReads <= 4, "O(1) path touches <= 4 visible rects (got " + totalRectReads + ")");

  // Boundary fallback when elementsFromPoint misses (returns []): binary search still yields exact anchor!
  efpImpl = () => [];
  const ancFallback = T.captureCollieAnchor(scroller);
  assert.ok(ancFallback, "fallback binary search returns an anchor");
  assert.equal(ancFallback.anchorId, "e120", "fallback binary search preserves exact anchorId");
  assert.equal(ancFallback.anchorOffset, 14, "fallback binary search preserves exact 0px anchorOffset");

  // Scroll-settle coalescing: rapid scroll + scrollend bursts must NOT fire mid-scroll; fires ONCE after settling.
  efpImpl = (x, y) => (y >= 60 && y <= 140 ? [row120, threadBox, scroller] : []);
  scroller.scrollHeight = 40000;
  scroller.scrollTop = 10000;
  scroller.clientHeight = 600;
  let efpCalls = 0;
  efpImpl = (x, y) => { efpCalls++; return y >= 60 && y <= 140 ? [row120, threadBox, scroller] : []; };
  for (let i = 0; i < 15; i++) {
    doc.dispatch("scroll", { target: scroller });
    doc.dispatch("scrollend", { target: scroller });
    await wait(12);
  }
  assert.equal(efpCalls, 0, "rapid scroll+scrollend frames must NOT trigger captureCollieAnchor mid-scroll");
  await wait(180);
  assert.ok(efpCalls >= 1 && efpCalls <= 3, "captureCollieAnchor fires once after scroll truly settles (efpCalls=" + efpCalls + ")");

  console.log("OK: all terminal scrollback/delta/MO, drawer incremental patch, and O(1) anchor checks passed");
  process.exit(0);
})().catch((err) => {
  console.error(err);
  process.exit(1);
});
`
	cmd := exec.Command(nodeBin, "-e", jsTest, root)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("node term/drawer/anchor perf test failed: %v\n%s", err, string(out))
	}
	if !strings.Contains(string(out), "OK: all terminal scrollback/delta/MO, drawer incremental patch, and O(1) anchor checks passed") {
		t.Fatalf("unexpected node output:\n%s", string(out))
	}
}
