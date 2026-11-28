package collie

import (
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestMobileSendJankAndKeyboardFocusJS(t *testing.T) {
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

// ─── 1. Static CSS & JS Guardrail Contracts ───
assert.ok(
  /@property\s+--vtop\s*\{[\s\S]*?inherits:\s*false;[\s\S]*?initial-value:\s*0px;/.test(cssSrc),
  "--vtop must register as non-inheriting @property with computationally independent initial-value",
);
assert.ok(
  cssSrc.includes("transform: translate3d(0, var(--vtop, 0px), 0);"),
  "coarse pointer .app must translate via GPU translate3d(0, var(--vtop, 0px), 0)",
);
assert.ok(
  /\.composer\s*\{[^}]*contain:\s*layout style;/.test(cssSrc) && !/\.composer\s*\{[^}]*contain:\s*[^;]*paint/.test(cssSrc),
  ".composer must isolate layout/style containment without paint clipping (#super-jump / .pending-list stay visible)",
);
assert.ok(
  /\.send\s*\{[^}]*touch-action:\s*manipulation;/.test(cssSrc),
  ".send button must declare touch-action: manipulation to prevent mobile double-tap delay",
);
const cssNoComments = cssSrc.replace(/\/\*[\s\S]*?\*\//g, "");
assert.ok(
  !/#super-thread\s*>\s*\.(?:say|mine|event)[^{]*\{[^}]*content-visibility:\s*auto/.test(cssNoComments),
  "#super-thread direct rows must NEVER use content-visibility: auto",
);
assert.ok(
  appSrc.includes('window.visualViewport?.addEventListener("resize", scheduleSetVH);')
    && appSrc.includes('window.visualViewport?.addEventListener("scroll", scheduleSetVH);')
    && appSrc.includes('addEventListener("resize", scheduleSetVH);'),
  "visualViewport resize/scroll and window resize must coalesce through scheduleSetVH (rAF)",
);

// ─── 2. Minimal DOM + Forced-Reflow-Tracking VM Harness ───
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

let layoutDirty = false;
let trackReflow = false;
let forcedReflows = [];

class TextNode {
  constructor(text) { this.nodeType = 3; this.textContent = String(text ?? ""); this.parentNode = null; }
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
    this.open = false;
    this.value = "";
    this._scrollTop = 0;
    this._scrollHeight = 12000;
    this._scrollHeightReads = 0;
    this._scrollTopWrites = 0;
    this.scrollLeft = 0;
    this.clientWidth = 390;
    this.clientHeight = 700;
    this.offsetWidth = 390;
    this.offsetHeight = 700;
    this.offsetParent = {};
    this.isConnected = true;
    this._rect = { top: 780, bottom: 812, left: 340, right: 372, width: 32, height: 32 };
    this._styleProps = new Map();
    this._styleWrites = 0;
    this._heightWrites = 0;
    this._blurCount = 0;
    this._focusCount = 0;
    const self = this;
    let _height = "";
    this.style = {
      get height() { return _height; },
      set height(v) {
        _height = String(v);
        self._heightWrites++;
        if (trackReflow) layoutDirty = true;
      },
      opacity: "",
      transform: "",
      top: "",
      overflow: "",
      setProperty(k, v) {
        self._styleWrites++;
        self._styleProps.set(k, String(v));
        if (trackReflow) layoutDirty = true;
      },
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
  set scrollTop(v) {
    this._scrollTopWrites++;
    if (trackReflow && layoutDirty) {
      forcedReflows.push("set:scrollTop@" + (this.id || this.className || this.tagName));
      layoutDirty = false;
    }
    this._scrollTop = v;
  }
  get scrollHeight() {
    this._scrollHeightReads++;
    if (trackReflow && layoutDirty) {
      forcedReflows.push("get:scrollHeight@" + (this.id || this.className || this.tagName));
      layoutDirty = false;
    }
    return this._scrollHeight;
  }
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
  get nextElementSibling() {
    if (!this.parentNode) return null;
    const cs = this.parentNode.childNodes;
    const i = cs.indexOf(this);
    for (let j = i + 1; j < cs.length; j++) if (cs[j].nodeType === 1) return cs[j];
    return null;
  }
  contains(node) {
    let cur = node;
    while (cur) { if (cur === this) return true; cur = cur.parentNode; }
    return false;
  }
  setAttribute(k, v) {
    k = String(k); v = String(v);
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
    if (node.nodeType === 11) { for (const ch of [...node.childNodes]) this.appendChild(ch); return node; }
    if (node.parentNode) node.parentNode.removeChild(node);
    node.parentNode = this;
    this.childNodes.push(node);
    return node;
  }
  append(...nodes) { for (const n of nodes) if (n != null) this.appendChild(typeof n === "string" ? new TextNode(n) : n); }
  insertBefore(node, ref) {
    if (!ref) return this.appendChild(node);
    if (node.nodeType === 11) { for (const ch of [...node.childNodes]) this.insertBefore(ch, ref); return node; }
    if (node.parentNode) node.parentNode.removeChild(node);
    const idx = this.childNodes.indexOf(ref);
    if (idx < 0) return this.appendChild(node);
    node.parentNode = this;
    this.childNodes.splice(idx, 0, node);
    return node;
  }
  removeChild(node) {
    const idx = this.childNodes.indexOf(node);
    if (idx >= 0) { this.childNodes.splice(idx, 1); node.parentNode = null; }
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
    const e = {
      type,
      target: this,
      currentTarget: this,
      defaultPrevented: false,
      preventDefault() { this.defaultPrevented = true; },
      stopPropagation() {},
      ...ev,
    };
    for (const fn of [...(this._listeners.get(type) || [])]) fn(e);
    return e;
  }
  dispatchEvent(e) { return this.dispatch(e.type, e); }
  requestSubmit() { this.dispatch("submit"); }
  focus() { this._focusCount++; doc.activeElement = this; }
  blur() { this._blurCount++; if (doc.activeElement === this) doc.activeElement = doc.body; }
  getBoundingClientRect() { return this._rect; }
  matches(sel) { return String(sel).split(",").some((p) => matchSimple(this, p.trim())); }
  closest(sel) {
    let cur = this;
    while (cur && cur.nodeType === 1) { if (cur.matches(sel)) return cur; cur = cur.parentElement; }
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
    if (m[5] !== undefined) cur.appendChild(new TextNode(m[5]));
    else if (m[1]) {
      const closeTag = m[1].toUpperCase();
      for (let i = stack.length - 1; i > 0; i--) { if (stack[i].tagName === closeTag) { stack.length = i; break; } }
    } else if (m[2]) {
      const el = new DomNode(m[2]);
      const attrStr = m[3] || "";
      const are = /([a-zA-Z0-9_:-]+)(?:\s*=\s*(?:"([^"]*)"|'([^']*)'|([^\s"'=<>]+)))?/g;
      let am;
      while ((am = are.exec(attrStr))) el.setAttribute(am[1], unescAttr(am[2] ?? am[3] ?? am[4] ?? ""));
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
  if (tagM) { if (el.tagName !== tagM[1].toUpperCase()) return false; rest = rest.slice(tagM[0].length); }
  const idRe = /#([a-zA-Z0-9_-]+)/g;
  let im;
  while ((im = idRe.exec(rest))) if (el.id !== im[1]) return false;
  rest = rest.replace(idRe, "");
  const attrRe = /\[([a-zA-Z0-9_-]+)(?:=(?:"([^"]*)"|'([^']*)'|([^\]]*)))?\]/g;
  let am;
  while ((am = attrRe.exec(rest))) {
    const k = am[1], want = am[2] ?? am[3] ?? am[4];
    if (!el._attrs.has(k)) return false;
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
  if (s.startsWith(":scope >")) return el.parentNode === root && matchSimple(el, s.slice(8).trim());
  const toks = s.split(/\s+/).filter(Boolean);
  if (toks.length === 1) return matchSimple(el, toks[0]);
  if (!matchSimple(el, toks[toks.length - 1])) return false;
  let cur = el.parentElement;
  for (let i = toks.length - 2; i >= 0; i--) {
    if (toks[i] === ">") continue;
    let ok = false;
    while (cur) { if (matchSimple(cur, toks[i])) { ok = true; cur = cur.parentElement; break; } cur = cur.parentElement; }
    if (!ok) return false;
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
      const tag = id.endsWith("-input") || id === "input" ? "TEXTAREA" : id.endsWith("-composer") || id === "composer" ? "FORM" : "DIV";
      const n = new DomNode(tag, id);
      byId.set(id, n);
      bodyEl.appendChild(n);
    }
    return byId.get(id);
  }
  if (!bySel.has(sel)) bySel.set(sel, new DomNode("DIV"));
  return bySel.get(sel);
};

// Wire #app > #v-collie > .viewer.chatbg > #super-thread and composers.
const appEl = getEl("#app");
const vCollie = getEl("#v-collie");
const scroller = new DomNode("DIV");
scroller.className = "viewer chatbg";
const superThread = new DomNode("DIV", "super-thread");
superThread.className = "thread";
byId.set("super-thread", superThread);
scroller.appendChild(superThread);
vCollie.appendChild(scroller);
appEl.appendChild(vCollie);

for (const id of [
  "login", "login-error", "login-tip", "conn", "herdr-status",
  "nav-collie", "nav-auto", "nav-devices", "nav-prefs", "nav-queue", "nav-unread",
  "super-working", "crew", "router-kind", "router-status", "router-queue", "router-ctx", "router-model",
  "super-jump", "super-jump-badge", "pending-wrap", "pending-bar", "pending-list",
  "super-composer", "super-input", "re-draft", "t-machine", "t-kind", "t-model", "t-effort",
  "collie-threads-list", "notes-pane", "file-view", "thread-drawer", "worker-drawer",
  "td-composer", "td-input", "td-send", "td-body", "td-list",
  "wd-composer", "wd-input", "wd-send", "wd-thread", "wd-log",
  "composer", "input", "x-composer", "x-input",
  "v-auto", "v-devices", "v-prefs", "v-ext", "v-file", "detail", "viewer", "screen", "log", "thread",
  "jump", "gone", "live", "super-search", "lightbox", "toasts", "sheet",
]) getEl("#" + id);

for (const [formSel, inputSel, sendSel] of [
  ["#super-composer", "#super-input", ""],
  ["#td-composer", "#td-input", "#td-send"],
  ["#wd-composer", "#wd-input", "#wd-send"],
  ["#composer", "#input", ""],
  ["#x-composer", "#x-input", ""],
]) {
  const f = getEl(formSel);
  const inp = getEl(inputSel);
  const cbox = new DomNode("DIV");
  cbox.className = "cbox";
  cbox.appendChild(inp);
  const sendBtn = sendSel ? getEl(sendSel) : new DomNode("BUTTON");
  sendBtn.className = "send";
  const files = new DomNode("DIV"); files.className = "files";
  const att = new DomNode("BUTTON"); att.className = "attach";
  const fileIn = new DomNode("INPUT"); fileIn.setAttribute("type", "file");
  cbox.appendChild(files);
  cbox.appendChild(att);
  cbox.appendChild(sendBtn);
  f.appendChild(cbox);
  f.appendChild(fileIn);
}
getEl("#re-draft").appendChild(new DomNode("BUTTON"));
getEl("#super-jump").appendChild(getEl("#super-jump-badge"));
getEl("#super-jump").hidden = true;
getEl("#super-jump-badge").hidden = true;
getEl("#super-working").hidden = true;
getEl("#file-view").hidden = true;
getEl("#thread-drawer").hidden = true;
getEl("#worker-drawer").hidden = true;
getEl("#super-search").hidden = true;
getEl("#lightbox").open = false;

const docListeners = new Map();
const winListeners = new Map();
const doc = {
  body: bodyEl,
  documentElement: rootEl,
  hidden: false,
  activeElement: bodyEl,
  hasFocus: () => true,
  createElement: (tag) => new DomNode(tag),
  createDocumentFragment: () => new DomNode("#fragment"),
  getElementById: (id) => getEl("#" + id),
  querySelector: (sel) => {
    if (sel === "dialog[open]") return null;
    if (sel.startsWith("#") && !sel.includes(" ") && !sel.includes(".")) return getEl(sel);
    const hit = bodyEl.querySelector(sel);
    if (hit) return hit;
    if (sel.includes(".msg[data-id") || sel.includes(".event[data-id")) return null;
    return getEl(sel);
  },
  querySelectorAll: (sel) => bodyEl.querySelectorAll(sel),
  addEventListener: (t, fn) => { (docListeners.get(t) || docListeners.set(t, []).get(t)).push(fn); },
  removeEventListener: () => {},
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

let rafQueue = [];
let rafSeq = 1;
const flushRAF = () => {
  const q = rafQueue.splice(0);
  layoutDirty = false;
  for (const item of q) if (!item.canceled) item.fn(Date.now());
};

const fetchCalls = [];
let modelsCalls = 0;
let saySeq = 1;
const ctx = vm.createContext({
  console, setTimeout, clearTimeout,
  setInterval: () => 0, clearInterval: () => {},
  performance: { now: () => Date.now() },
  Date, Math, JSON, Map, Set, WeakMap, Promise, RegExp, Array, Object, Number, String, Boolean, Error,
  URLSearchParams, Uint8Array, TextEncoder, atob, AbortController,
  Event: class Event { constructor(t, opts = {}) { this.type = t; this.cancelable = !!opts.cancelable; } },
  Blob: class { constructor(parts, opts) { this.parts = parts; this.type = opts?.type; } },
  URL: { createObjectURL: () => "blob:x", revokeObjectURL: () => {} },
  // Simulate iOS Safari < 26.2 (no field-sizing: content) to verify zero-reflow grow() empty reset too!
  CSS: { escape: (s) => String(s), supports: () => false },
  Element: DomNode,
  HTMLDialogElement: { prototype: {} },
  EventSource: FakeEventSource,
  MutationObserver: class { observe() {} disconnect() {} },
  ResizeObserver: class { observe() {} disconnect() {} },
  getComputedStyle: () => ({ getPropertyValue: () => "" }),
  crypto: { randomUUID: () => "uuid-mobile-jank-" + (saySeq++) },
  navigator: { platform: "iPhone", userAgent: "iPhone Safari", clipboard: null },
  localStorage: { _m: new Map(), getItem(k) { return this._m.get(k) ?? null; }, setItem(k, v) { this._m.set(k, String(v)); }, removeItem(k) { this._m.delete(k); } },
  location: { href: "http://127.0.0.1:7788/#/collie", origin: "http://127.0.0.1:7788", hash: "#/collie", search: "", pathname: "/", hostname: "127.0.0.1" },
  history: { state: { collieIdx: 0 }, replaceState() {}, pushState() {}, back() {} },
  innerWidth: 390,
  innerHeight: 844,
  visualViewport: fakeVV,
  document: doc,
  matchMedia: (q) => ({ matches: /max-width:\s*760px|pointer:\s*coarse|hover:\s*none/.test(q), addEventListener() {} }),
  addEventListener: (t, fn) => { (winListeners.get(t) || winListeners.set(t, []).get(t)).push(fn); },
  removeEventListener: () => {},
  dispatchEvent: (ev) => { for (const fn of (winListeners.get(ev.type) || [])) fn(ev); },
  requestAnimationFrame: (fn) => { const id = rafSeq++; rafQueue.push({ id, fn, canceled: false }); return id; },
  cancelAnimationFrame: (id) => { for (const item of rafQueue) if (item.id === id) item.canceled = true; },
  fetch: async (url, init) => {
    const u = String(url);
    fetchCalls.push(u);
    if (u.startsWith("/api/models")) { modelsCalls++; return { ok: true, status: 200, headers: { get: () => null }, json: async () => ({ models: [] }) }; }
    if (u.startsWith("/api/super/say")) return { ok: true, status: 200, headers: { get: () => null }, json: async () => ({ entry: "e" + (500 + saySeq) }) };
    if (u.startsWith("/api/session")) return { ok: true, status: 200, headers: { get: () => null }, json: async () => ({ ok: true }) };
    if (u.startsWith("/api/super")) return { ok: true, status: 200, headers: { get: () => null }, json: async () => ({ router: { status: "idle", alive: true }, entries: [], workers: [], pending: [], pinned: [] }) };
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
  "    setView: (v) => { view = v; },",
  "    grow: (el) => grow(el),",
  "    isVVTransitioning: () => vvTransitioning,",
  "  };",
].join("\n");
const marker = "  boot();\n})();";
assert.ok(appSrc.includes(marker), "boot marker present");
vm.runInContext(appSrc.replace(marker, hook + "\n" + marker), ctx);

const wait = (ms) => new Promise((r) => setTimeout(r, ms));
const mkEntry = (n, role, text) => ({
  id: "e" + n,
  role,
  text,
  at: new Date(Date.UTC(2026, 10, 28, 10, Math.floor(n / 60), n % 60)).toISOString(),
});

(async () => {
  await wait(30);
  flushRAF();
  const T = ctx.__T;
  T.setView("collie");
  bodyEl.classList.add("show-main");

  const entries = [];
  for (let i = 1; i <= 200; i++) entries.push(mkEntry(i, i % 2 ? "user" : "collie", "消息 #" + i));
  T.setSup({ router: { status: "idle", alive: true }, entries, workers: [], pending: [], pinned: [] });
  T.renderSuper({ force: true });
  flushRAF();

  const superInput = getEl("#super-input");
  const superForm = getEl("#super-composer");
  const sendBtn = superForm.querySelector(".send");
  assert.ok(sendBtn, ".send button exists");

  // ─── Test A: Tapping ↑ (.send) while focused keeps focus & prevents iOS keyboard dismiss ───
  superInput.focus();
  superInput.value = "第一句：点上箭头不应关闭输入法";
  superInput.style.height = "96px";
  const blursBefore = superInput._blurCount;
  const shReadsBeforeGrow = superInput._scrollHeightReads;
  const modelsBefore = modelsCalls;
  fetchCalls.length = 0;

  sendBtn.dispatch("touchstart", { touches: [{ identifier: 1, clientX: 356, clientY: 796 }] });
  const te = sendBtn.dispatch("touchend", { changedTouches: [{ identifier: 1, clientX: 358, clientY: 798 }] });
  assert.equal(te.defaultPrevented, true, "touchend on .send while input focused MUST preventDefault to keep iOS keyboard open");
  assert.equal(doc.activeElement, superInput, "focus must remain on #super-input after tapping .send");
  assert.equal(superInput._blurCount, blursBefore, "tapping .send must trigger 0 blur events on #super-input");
  assert.equal(superInput.value, "", "textarea cleared synchronously on tap send");
  assert.equal(superInput.style.height, "", "empty textarea height reset directly via style.height=''");
  assert.equal(superInput._scrollHeightReads, shReadsBeforeGrow, "grow() empty reset must perform 0 scrollHeight reads");
  assert.equal(superThread.querySelectorAll(".mine").length, 101, "optimistic user bubble rendered immediately");

  await wait(30);
  assert.equal(modelsCalls, modelsBefore, "default dispatch sendSuper must NOT block on /api/models");
  assert.ok(fetchCalls.includes("/api/super/say"), "/api/super/say posted immediately");

  // Desktop mousedown also preserves focus.
  const md = sendBtn.dispatch("mousedown", { clientX: 356, clientY: 796 });
  assert.equal(md.defaultPrevented, true, "mousedown on .send while input focused preserves focus");

  // Dragging finger away (>14px) cancels tap send cleanly.
  superInput.value = "滑动取消不应发送";
  const sayCountBeforeDrag = fetchCalls.filter((u) => u === "/api/super/say").length;
  sendBtn.dispatch("touchstart", { touches: [{ identifier: 2, clientX: 356, clientY: 796 }] });
  sendBtn.dispatch("touchmove", { touches: [{ identifier: 2, clientX: 356, clientY: 760 }] });
  const teDrag = sendBtn.dispatch("touchend", { changedTouches: [{ identifier: 2, clientX: 356, clientY: 760 }] });
  assert.equal(teDrag.defaultPrevented, false, "dragged touch must NOT preventDefault or accidentally send");
  assert.equal(superInput.value, "滑动取消不应发送", "dragged touch keeps draft intact");
  assert.equal(fetchCalls.filter((u) => u === "/api/super/say").length, sayCountBeforeDrag, "dragged touch sends 0 requests");

  // Tapping .send when input is NOT focused does NOT intercept touchend.
  superInput.blur();
  sendBtn.dispatch("touchstart", { touches: [{ identifier: 3, clientX: 356, clientY: 796 }] });
  const teBlurred = sendBtn.dispatch("touchend", { changedTouches: [{ identifier: 3, clientX: 356, clientY: 796 }] });
  assert.equal(teBlurred.defaultPrevented, false, "touchend when input is unfocused lets native click handle submit");

  // ─── Test B: Keyboard open (shrink) -> 20-frame keyboard dismiss storm: 0 forced reflows & 0 :root thrash ───
  scroller.scrollHeight = 12000;
  scroller.clientHeight = 700;
  scroller.scrollTop = 11300; // at bottom (dist=0)

  // Open keyboard: 844 -> 508 (shrink)
  trackReflow = true;
  forcedReflows = [];
  layoutDirty = false;
  fakeVV.height = 508;
  fakeVV.offsetTop = 0;
  fakeVV.dispatch("resize");
  flushRAF(); // Frame 1: setVH read -> write #app height (0 post-write reflows!)
  assert.equal(forcedReflows.length, 0, "keyboard shrink setVH must perform 0 forced reflows; got " + JSON.stringify(forcedReflows));
  assert.equal(appEl.style.height, "508px", "#app height tracks visualViewport shrink");
  scroller.clientHeight = 364;
  flushRAF(); // Frame 2: clean post-layout pin
  assert.equal(scroller.scrollTop, 12000, "bottom-pinned scroller stays pinned after keyboard shrink");

  // Wait for single shrink settle timer, then run 20-frame keyboard dismiss storm (508 -> 844, offsetTop 28 -> 0)
  await wait(160);
  flushRAF();

  scroller.scrollTop = 11636; // clamped at bottom for clientHeight=364
  const rootWritesBefore = rootEl._styleWrites;
  const scReadsBefore = scroller._scrollHeightReads;
  const scWritesBefore = scroller._scrollTopWrites;
  forcedReflows = [];
  layoutDirty = false;

  const rendersBeforeDismiss = superThread._colliePatchStats.renders;
  for (let f = 1; f <= 20; f++) {
    const p = f / 20;
    const ease = 1 - Math.pow(1 - p, 3);
    fakeVV.height = Math.round(508 + (844 - 508) * ease);
    fakeVV.offsetTop = f < 20 ? Math.round(28 * (1 - ease)) : 0;
    fakeVV.dispatch("resize");
    fakeVV.dispatch("scroll");
    ctx.dispatchEvent({ type: "resize" });
    flushRAF();
    if (f === 10) {
      assert.equal(T.isVVTransitioning(), true, "multi-frame keyboard dismiss marks vvTransitioning");
      // Mid-dismiss background SSE arrives -> must defer until dismiss settles!
      liveES.emit("super", {
        router: { status: "working", alive: true },
        entries: [...entries, mkEntry(201, "collie", "键盘收起中途到达的 SSE")],
        workers: [],
        pending: [],
        pinned: [],
      });
      flushRAF();
      assert.equal(superThread._colliePatchStats.renders, rendersBeforeDismiss, "background SSE defers during multi-frame keyboard dismiss");
    }
  }

  assert.equal(forcedReflows.length, 0, "20-frame keyboard dismiss must trigger 0 forced reflows; got " + JSON.stringify(forcedReflows));
  assert.equal(rootEl._styleWrites, rootWritesBefore, "20-frame keyboard dismiss must perform 0 :root --vh/--vtop writes during animation");
  assert.equal(scroller._scrollHeightReads, scReadsBefore, "20-frame dismiss setVH must perform 0 scrollHeight reads");
  assert.equal(scroller._scrollTopWrites, scWritesBefore, "20-frame dismiss setVH must perform 0 scrollTop writes (relies on native clamp)");
  assert.equal(appEl.style.height, "844px", "#app height lands at exact 844px");
  assert.equal(appEl.style.getPropertyValue("--vtop"), "0px", "#app --vtop lands at 0px");

  // After settle (~140ms), :root --vh syncs ONCE and deferred SSE flushes cleanly!
  await wait(170);
  flushRAF();
  assert.equal(T.isVVTransitioning(), false, "vvTransitioning clears after settle");
  assert.equal(rootEl.style.getPropertyValue("--vh"), "844px", ":root --vh syncs cleanly to 844px after settle");
  assert.equal(superThread._colliePatchStats.renders, rendersBeforeDismiss + 1, "deferred SSE flushes once after keyboard settles");
  assert.ok(superThread.querySelector('.msg[data-id="e201"]'), "deferred SSE message e201 rendered after settle");

  console.log("OK: all mobile send jank & keyboard focus tests passed");
  process.exit(0);
})().catch((err) => {
  console.error(err);
  process.exit(1);
});
`
	cmd := exec.Command(nodeBin, "-e", jsTest, root)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("mobile send jank JS test failed: %v\n%s", err, string(out))
	}
	if !strings.Contains(string(out), "OK: all mobile send jank & keyboard focus tests passed") {
		t.Fatalf("unexpected node output:\n%s", string(out))
	}
}
