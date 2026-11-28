package collie

import (
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestInlineReplyKeyboardFocusAndRerenderJS(t *testing.T) {
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

// Static CSS sanity: scrollers provide scroll-padding-block so focused inline inputs clear edges.
assert.ok(
  /\.viewer\.chatbg\s*\{[^}]*scroll-padding-block:\s*16px 24px/.test(cssSrc),
  ".viewer.chatbg must define scroll-padding-block for inline keyboard visibility",
);
assert.ok(
  /\.td-body\s*\{[^}]*scroll-padding-block:\s*16px 24px/.test(cssSrc),
  ".td-body must define scroll-padding-block for drawer inline keyboard visibility",
);

// ─── Minimal DOM + Forced-Reflow + Selection VM Harness ───
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
let docReady = false;

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
    this._value = "";
    this.selectionStart = 0;
    this.selectionEnd = 0;
    this.selectionDirection = "none";
    this._scrollTop = 0;
    this._scrollHeight = 1400;
    this.scrollLeft = 0;
    this.clientWidth = 1000;
    this.clientHeight = 680;
    this.offsetWidth = 1000;
    this.offsetHeight = 680;
    this.offsetParent = {};
    this._rect = null;
    this._styleProps = new Map();
    this._focusCount = 0;
    this._blurCount = 0;
    const self = this;
    let _height = "";
    this.style = {
      get height() { return _height; },
      set height(v) {
        _height = String(v);
        if (trackReflow) layoutDirty = true;
      },
      opacity: "",
      transform: "",
      transition: "",
      top: "",
      overflow: "",
      setProperty(k, v) {
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
  get isConnected() {
    let cur = this;
    while (cur) {
      if (cur === rootEl || cur === bodyEl) return true;
      cur = cur.parentNode;
    }
    return false;
  }
  get value() {
    if (this.tagName === "TEXTAREA" && this._value === "" && this.childNodes.length) {
      return this.textContent;
    }
    return this._value;
  }
  set value(v) { this._value = String(v ?? ""); }
  get scrollTop() { return this._scrollTop; }
  set scrollTop(v) {
    if (trackReflow && layoutDirty) {
      forcedReflows.push("set:scrollTop@" + (this.id || this.className || this.tagName));
      layoutDirty = false;
    }
    this._scrollTop = v;
  }
  get scrollHeight() {
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
    if (k === "hidden") { this.hidden = true; this._attrs.set(k, v); return; }
    if (k === "disabled") { this.disabled = true; this._attrs.set(k, v); return; }
    this._attrs.set(k, v);
  }
  getAttribute(k) { return this._attrs.has(k) ? this._attrs.get(k) : null; }
  hasAttribute(k) { return this._attrs.has(k); }
  removeAttribute(k) {
    if (k === "class") this._classes.clear();
    if (k === "hidden") this.hidden = false;
    if (k === "disabled") this.disabled = false;
    this._attrs.delete(k);
  }
  _detachIfFocused(node) {
    if (docReady && doc.activeElement && typeof node?.contains === "function" && node.contains(doc.activeElement)) {
      const ae = doc.activeElement;
      ae._blurCount++;
      doc.activeElement = bodyEl;
    }
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
    if (idx >= 0) {
      this._detachIfFocused(node);
      this.childNodes.splice(idx, 1);
      node.parentNode = null;
    }
    return node;
  }
  replaceChild(fresh, old) {
    const idx = this.childNodes.indexOf(old);
    if (idx < 0) return old;
    this._detachIfFocused(old);
    if (fresh.parentNode) fresh.parentNode.removeChild(fresh);
    fresh.parentNode = this;
    old.parentNode = null;
    this.childNodes[idx] = fresh;
    return old;
  }
  remove() { if (this.parentNode) this.parentNode.removeChild(this); }
  get textContent() { return this.childNodes.map((c) => c.textContent).join(""); }
  set textContent(v) {
    for (const c of this.childNodes) { this._detachIfFocused(c); c.parentNode = null; }
    this.childNodes = [];
    if (v !== "" && v != null) this.appendChild(new TextNode(String(v)));
  }
  get innerHTML() { return this.childNodes.map((c) => c.outerHTML).join(""); }
  set innerHTML(html) {
    const target = this.tagName === "TEMPLATE" ? this.content : this;
    for (const c of target.childNodes) { this._detachIfFocused(c); c.parentNode = null; }
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
      target: ev.target || this,
      currentTarget: this,
      defaultPrevented: false,
      preventDefault() { this.defaultPrevented = true; },
      stopPropagation() {},
      composedPath: () => [ev.target || this],
      ...ev,
    };
    for (const fn of [...(this._listeners.get(type) || [])]) fn(e);
    return e;
  }
  dispatchEvent(e) { return this.dispatch(e.type, e); }
  requestSubmit() { this.dispatch("submit"); }
  focus() {
    this._focusCount++;
    const prev = doc.activeElement;
    doc.activeElement = this;
    if (prev !== this) doc.dispatch("focusin", { target: this });
  }
  blur() {
    this._blurCount++;
    if (doc.activeElement === this) doc.activeElement = bodyEl;
    this.dispatch("blur");
  }
  setSelectionRange(start, end, dir = "none") {
    this.selectionStart = start;
    this.selectionEnd = end;
    this.selectionDirection = dir;
  }
  scrollTo(opts) { if (opts && typeof opts.top === "number") this.scrollTop = opts.top; }
  getBoundingClientRect() {
    if (typeof this._rect === "function") return this._rect();
    if (this._rect) return this._rect;
    return { top: 0, bottom: this.clientHeight || 40, left: 0, right: this.clientWidth || 800, width: this.clientWidth || 800, height: this.clientHeight || 40 };
  }
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

// Build realistic #app > #v-collie > .viewer.chatbg > #super-thread and #thread-drawer > #td-body > #td-list.
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

const tdDrawer = getEl("#thread-drawer");
const tdBody = getEl("#td-body");
const tdList = getEl("#td-list");
tdBody.appendChild(tdList);
tdDrawer.appendChild(tdBody);
appEl.appendChild(tdDrawer);

for (const id of [
  "login", "login-error", "login-tip", "conn", "herdr-status",
  "nav-collie", "nav-auto", "nav-devices", "nav-prefs", "nav-queue", "nav-unread",
  "super-working", "crew", "router-kind", "router-status", "router-queue", "router-ctx", "router-model",
  "super-jump", "super-jump-badge", "pending-wrap", "pending-bar", "pending-list",
  "super-composer", "super-input", "re-draft", "t-machine", "t-kind", "t-model", "t-effort",
  "collie-threads-list", "notes-pane", "file-view", "worker-drawer",
  "td-composer", "td-input", "td-send", "td-scrim", "td-title", "td-meta", "td-workers", "td-target", "td-locate-root", "td-close", "td-widen",
  "wd-composer", "wd-input", "wd-send", "wd-thread", "wd-log",
  "composer", "input", "x-composer", "x-input",
  "v-auto", "v-devices", "v-prefs", "v-ext", "v-file", "detail", "viewer", "screen", "log", "thread",
  "jump", "gone", "live", "super-search", "super-search-btn", "super-search-card", "super-search-input",
  "super-search-clear", "super-search-close", "super-search-meta", "super-search-panel",
  "super-search-head", "super-search-list", "super-search-hint", "lightbox", "toasts", "sheet",
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
const reDraft = getEl("#re-draft");
reDraft.hidden = true;
const reWho = new DomNode("SPAN"); reWho.className = "re-who";
const reT = new DomNode("SPAN"); reT.className = "re-t";
reDraft.append(reWho, reT, new DomNode("BUTTON"));

getEl("#super-jump").appendChild(getEl("#super-jump-badge"));
getEl("#super-jump").hidden = true;
getEl("#super-jump-badge").hidden = true;
getEl("#super-working").hidden = true;
getEl("#file-view").hidden = true;
tdDrawer.hidden = true;
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
    if (sel.includes(".msg[data-id") || sel.includes(".event[data-id") || sel.includes("data-inline")) return null;
    return getEl(sel);
  },
  querySelectorAll: (sel) => bodyEl.querySelectorAll(sel),
  addEventListener: (t, fn) => { (docListeners.get(t) || docListeners.set(t, []).get(t)).push(fn); },
  removeEventListener: () => {},
  dispatch: (t, ev = {}) => {
    const e = { type: t, defaultPrevented: false, preventDefault() { this.defaultPrevented = true; }, stopPropagation() {}, composedPath: () => [ev.target || bodyEl], ...ev };
    for (const fn of [...(docListeners.get(t) || [])]) fn(e);
    return e;
  },
};
docReady = true;

let liveES = null;
class FakeEventSource {
  constructor() { this.readyState = 1; this.ls = {}; liveES = this; }
  addEventListener(t, fn) { (this.ls[t] ||= []).push(fn); }
  emit(t, data) { for (const fn of (this.ls[t] || [])) fn({ data: JSON.stringify(data) }); }
  close() { this.readyState = 2; }
}

const roCallbacks = new Map();
class FakeResizeObserver {
  constructor(cb) { this.cb = cb; }
  observe(el) { (roCallbacks.get(el) || roCallbacks.set(el, []).get(el)).push(this.cb); }
  disconnect() {}
}
const triggerRO = (el) => {
  for (const cb of (roCallbacks.get(el) || [])) {
    cb([{ target: el, contentRect: { width: el.clientWidth, height: el.clientHeight } }]);
  }
};

const vvListeners = new Map();
const fakeVV = {
  width: 1194,
  height: 834,
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

let mockWidth = 1194;
let mockReducedMotion = true;
const narrowListeners = [];
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
  CSS: { escape: (s) => String(s), supports: () => false },
  Element: DomNode,
  HTMLDialogElement: { prototype: {} },
  EventSource: FakeEventSource,
  MutationObserver: class { observe() {} disconnect() {} },
  ResizeObserver: FakeResizeObserver,
  getComputedStyle: () => ({ getPropertyValue: () => "" }),
  crypto: { randomUUID: () => "uuid-inline-kb-" + (saySeq++) },
  navigator: { platform: "MacIntel", userAgent: "iPad Safari", clipboard: null },
  localStorage: { _m: new Map(), getItem(k) { return this._m.get(k) ?? null; }, setItem(k, v) { this._m.set(k, String(v)); }, removeItem(k) { this._m.delete(k); } },
  location: { href: "http://127.0.0.1:7788/#/collie", origin: "http://127.0.0.1:7788", hash: "#/collie", search: "", pathname: "/", hostname: "127.0.0.1" },
  history: { state: { collieIdx: 0 }, replaceState() {}, pushState() {}, back() {} },
  get innerWidth() { return mockWidth; },
  get innerHeight() { return fakeVV.height; },
  visualViewport: fakeVV,
  document: doc,
  matchMedia: (q) => ({
    get matches() {
      if (/prefers-reduced-motion:\s*reduce/.test(q)) return mockReducedMotion;
      if (/max-width:\s*760px/.test(q)) return mockWidth <= 760;
      if (/min-width:\s*1181px/.test(q)) return mockWidth >= 1181;
      if (/pointer:\s*coarse|hover:\s*none/.test(q)) return true;
      return false;
    },
    addEventListener(t, fn) { if (/max-width:\s*760px/.test(q) && t === "change") narrowListeners.push(fn); },
  }),
  addEventListener: (t, fn) => { (winListeners.get(t) || winListeners.set(t, []).get(t)).push(fn); },
  removeEventListener: () => {},
  dispatchEvent: (ev) => { for (const fn of (winListeners.get(ev.type) || [])) fn(ev); },
  requestAnimationFrame: (fn) => { const id = rafSeq++; rafQueue.push({ id, fn, canceled: false }); return id; },
  cancelAnimationFrame: (id) => { for (const item of rafQueue) if (item.id === id) item.canceled = true; },
  fetch: async (url) => {
    const u = String(url);
    if (u.startsWith("/api/super/say")) return { ok: true, status: 200, headers: { get: () => null }, json: async () => ({ entry: "e" + (500 + saySeq) }) };
    if (u.startsWith("/api/super")) return { ok: true, status: 200, headers: { get: () => null }, json: async () => ({ router: { status: "idle", alive: true }, entries: [], workers: [], pending: [], pinned: [] }) };
    if (u.startsWith("/api/state")) return { ok: true, status: 200, headers: { get: () => null }, json: async () => ({ ok: true, agents: [], shells: [], machines: [] }) };
    return { ok: true, status: 200, headers: { get: () => null }, json: async () => ({ ok: true, hubs: [], sessions: [], files: [] }) };
  },
});
ctx.window = ctx;
ctx.globalThis = ctx;

const hook = [
  "  window.__T = {",
  "    setSup: (s) => { sup = normSup(s); },",
  "    renderSuper: (o) => renderSuper(o),",
  "    setView: (v) => { view = v; },",
  "    getOpenInline: () => openInlineDecisionId,",
  "    getReplyTo: () => replyTo,",
  "    openThreadDrawer: (r, f, a, o) => openThreadDrawer(r, f, a, o),",
  "    closeThreadDrawer: (o) => closeThreadDrawer(o),",
  "    getActiveThreadRoot: () => activeThreadRoot,",
  "  };",
].join("\n");
const marker = "  boot();\n})();";
assert.ok(appSrc.includes(marker), "boot marker present");
vm.runInContext(appSrc.replace(marker, hook + "\n" + marker), ctx);

const wait = (ms) => new Promise((r) => setTimeout(r, ms));
const mkEntry = (n, role, text, extra = {}) => ({
  id: "e" + n,
  role,
  thread: "chat:main",
  text,
  at: new Date(Date.UTC(2026, 10, 28, 10, n, 0)).toISOString(),
  ...extra,
});

(async () => {
  await wait(30);
  flushRAF();
  const T = ctx.__T;
  T.setView("collie");
  bodyEl.classList.add("show-main");

  const baseEntries = [
    mkEntry(1, "user", "帮我看下发版清单"),
    mkEntry(2, "collie", "已梳理完毕，准备发版。"),
    mkEntry(3, "collie", "第一项检查通过，是否直接合入？", { options: ["直接合入", "先补测试"] }),
    mkEntry(4, "user", "继续检查第二项"),
    mkEntry(5, "collie", "PR #142 全量通过，直接合入还是补用例？", { options: ["直接合入", "先补用例"] }),
    mkEntry(6, "collie", "顺便汇总了其余 3 个模块的基准数据。"),
  ];
  T.setSup({
    router: { status: "idle", alive: true },
    entries: baseEntries,
    workers: [],
    pending: [
      { id: "e3", summary: "第一项检查", options: ["直接合入", "先补测试"] },
      { id: "e5", summary: "PR #142", options: ["直接合入", "先补用例"] },
    ],
    pinned: [],
  });
  T.renderSuper({ force: true });
  flushRAF();

  // ─── 1. iPad (>760px) Plan A: Synchronous focus inside click gesture (BEFORE rAF) ───
  const preTa = superThread.querySelector('[data-inline-input="e5"]');
  const preBox = superThread.querySelector('[data-inline-box="e5"]');
  const togBtn = superThread.querySelector('[data-toggle-inline="e5"]');
  assert.ok(preTa && preBox && togBtn, "decision card e5 markup rendered");
  assert.equal(preBox.hidden, true, "inline box starts hidden");

  // Click 补充一句 and DO NOT flush rAF yet!
  superThread.dispatch("click", { target: togBtn });

  const openTa = superThread.querySelector('[data-inline-input="e5"]');
  const openBox = superThread.querySelector('[data-inline-box="e5"]');
  assert.equal(T.getOpenInline(), "e5", "openInlineDecisionId set to e5");
  assert.equal(openBox.hidden, false, "inline box unhidden synchronously");
  assert.equal(openTa, preTa, "opening reveals existing <textarea> in place without DOM teardown");
  assert.equal(doc.activeElement, openTa, "MUST focus [data-inline-input] synchronously in click handler (before rAF) for iPadOS Safari");
  assert.ok(openTa._focusCount >= 1, "textarea.focus() called synchronously");

  // Simulate card clipped slightly below scroller bottom -> post-focus rAF scrolls it into view without blurring.
  scroller.clientHeight = 680;
  scroller.scrollHeight = 1400;
  scroller.scrollTop = 720; // near bottom
  scroller._rect = () => ({ top: 60, bottom: 60 + scroller.clientHeight, left: 0, right: 1000, width: 1000, height: scroller.clientHeight });
  const cardDocY = 1320; // sits near bottom of document (720 + 600 = 1320)
  openBox._rect = () => ({ top: 60 + (cardDocY - scroller.scrollTop), bottom: 60 + (cardDocY + 44 - scroller.scrollTop), left: 40, right: 320, width: 280, height: 44 });
  flushRAF();
  assert.equal(doc.activeElement, openTa, "focus stays on [data-inline-input] after post-focus rAF");

  // ─── 2. Keyboard expand (visualViewport shrink 834 -> 474): 0 forced reflows, stays focused, scrolls card into shrunk viewport without yanking to bottom ───
  scroller.scrollHeight = 1600;
  scroller.clientHeight = 680;
  scroller.scrollTop = 900; // within AT_BOTTOM_PX (dist = 20)!
  const midCardDocY = 1280; // visible at top=440 before keyboard, will be BELOW shrunk bottom (60+320=380) after keyboard!
  openBox._rect = () => ({
    top: 60 + (midCardDocY - scroller.scrollTop),
    bottom: 60 + (midCardDocY + 44 - scroller.scrollTop),
    left: 40, right: 320, width: 280, height: 44,
  });

  trackReflow = true;
  forcedReflows = [];
  layoutDirty = false;
  fakeVV.height = 474; // iPad keyboard expanded (-360px)
  fakeVV.dispatch("resize");
  flushRAF(); // Frame 1: setVH read -> write
  assert.equal(forcedReflows.length, 0, "keyboard shrink setVH must perform 0 forced reflows; got " + JSON.stringify(forcedReflows));
  assert.equal(doc.activeElement, openTa, "focus stays on [data-inline-input] during setVH Frame 1");

  // Layout updates scroller clientHeight (680 -> 320), ResizeObserver + Frame 2 applyPin run
  scroller.clientHeight = 320;
  triggerRO(scroller);
  flushRAF();
  assert.equal(doc.activeElement, openTa, "focus stays on [data-inline-input] after keyboard expand");
  assert.ok(scroller.scrollTop < scroller.scrollHeight, "must NOT blindly slam scrollTop to scrollHeight when inline input is focused");
  const rectAfterKb = openBox.getBoundingClientRect();
  const scRectAfterKb = scroller.getBoundingClientRect();
  assert.ok(rectAfterKb.bottom <= scRectAfterKb.bottom - 16, "inline box bottom must be above shrunk viewport bottom; got box=" + rectAfterKb.bottom + " sc=" + scRectAfterKb.bottom);
  assert.ok(rectAfterKb.top >= scRectAfterKb.top + 12, "inline box top must remain below viewport top; got box=" + rectAfterKb.top + " sc=" + scRectAfterKb.top);
  trackReflow = false;

  // 2b. Simulated WebKit focus drop to document.body during viewport shrink recovers focus back to [data-inline-input]
  doc.activeElement = bodyEl; // no outside pointerdown!
  fakeVV.height = 420;
  fakeVV.dispatch("resize");
  flushRAF();
  flushRAF();
  assert.equal(doc.activeElement, openTa, "accidental focus drop to body during keyboard shrink recovers focus to [data-inline-input]");

  // 2c. Explicit focus switch to #super-input during shrink is respected (never stolen back!)
  const superIn = getEl("#super-input");
  doc.dispatch("pointerdown", { target: superIn });
  superIn.focus();
  fakeVV.height = 380;
  fakeVV.dispatch("resize");
  flushRAF();
  flushRAF();
  assert.equal(doc.activeElement, superIn, "must never steal focus when user explicitly focused #super-input");

  // Re-focus inline textarea for Test 3, restore viewport height
  openTa.focus();
  fakeVV.height = 834;
  fakeVV.dispatch("resize");
  flushRAF();
  await wait(160);
  flushRAF();
  assert.equal(doc.activeElement, openTa, "focus remains on [data-inline-input] after viewport settles");

  // ─── 3. Typing + Mid-typing SSE / IME / Forced Rebuild preserves DOM, focus, value & selection ───
  openTa.value = "先合入主分支，顺手补文档";
  openTa.setSelectionRange(3, 6, "forward");
  doc.dispatch("input", { target: openTa });
  const blursBeforeSSE = openTa._blurCount;
  const scrollBeforeSSE = scroller.scrollTop;

  // Background SSE arrives with new message e7 and router status change
  const entriesWithE7 = [...baseEntries, mkEntry(7, "collie", "刚跑完一条后台巡检结果。")];
  liveES.emit("super", {
    router: { status: "working", alive: true },
    entries: entriesWithE7,
    workers: [],
    pending: [
      { id: "e3", summary: "第一项检查", options: ["直接合入", "先补测试"] },
      { id: "e5", summary: "PR #142", options: ["直接合入", "先补用例"] },
    ],
    pinned: [],
  });
  flushRAF();
  assert.ok(superThread.querySelector('.msg[data-id="e7"]'), "new SSE message e7 rendered");
  assert.equal(superThread.querySelector('[data-inline-input="e5"]'), openTa, "SSE must keep the EXACT SAME <textarea> DOM node in place");
  assert.equal(doc.activeElement, openTa, "SSE must NOT drop focus from [data-inline-input]");
  assert.equal(openTa._blurCount, blursBeforeSSE, "SSE must cause 0 blur events on focused [data-inline-input]");
  assert.equal(openTa.value, "先合入主分支，顺手补文档", "SSE must preserve typed value");
  assert.equal(openTa.selectionStart, 3, "SSE must preserve selectionStart");
  assert.equal(openTa.selectionEnd, 6, "SSE must preserve selectionEnd");
  assert.equal(scroller.scrollTop, scrollBeforeSSE, "SSE must NOT yank scrollTop to bottom while typing in [data-inline-input]");

  // IME composition guard on [data-inline-input] defers live SSE until compositionend
  openTa.dispatch("compositionstart");
  const entriesWithE8 = [...entriesWithE7, mkEntry(8, "collie", "IME 输入期间到达的消息。")];
  liveES.emit("super", {
    router: { status: "idle", alive: true },
    entries: entriesWithE8,
    workers: [],
    pending: [{ id: "e5", summary: "PR #142", options: ["直接合入", "先补用例"] }],
    pinned: [],
  });
  flushRAF();
  assert.equal(superThread.querySelector('.msg[data-id="e8"]'), null, "SSE renderSuper defers while IME composing in [data-inline-input]");
  openTa.dispatch("compositionend");
  flushRAF();
  assert.ok(superThread.querySelector('.msg[data-id="e8"]'), "deferred SSE renders immediately on compositionend");
  assert.equal(doc.activeElement, openTa, "focus stays on [data-inline-input] after compositionend flush");

  // Hard force rebuild where e5's message text changes (forcing DOM node replacement): Layer 2 restores focus & selection!
  const mutatedEntries = entriesWithE8.map((e) => e.id === "e5" ? { ...e, text: e.text + "（已更新）" } : e);
  T.setSup({
    router: { status: "idle", alive: true },
    entries: mutatedEntries,
    workers: [],
    pending: [{ id: "e5", summary: "PR #142", options: ["直接合入", "先补用例"] }],
    pinned: [],
  });
  T.renderSuper({ force: true });
  const rebuiltTa = superThread.querySelector('[data-inline-input="e5"]');
  assert.ok(rebuiltTa && rebuiltTa !== openTa, "mutated msgSig rebuilt e5 row");
  assert.equal(doc.activeElement, rebuiltTa, "Layer 2 safety net restores focus to rebuilt [data-inline-input]");
  assert.equal(rebuiltTa.value, "先合入主分支，顺手补文档", "Layer 2 preserves live unsent text");
  assert.equal(rebuiltTa.selectionStart, 3, "Layer 2 restores selectionStart");
  assert.equal(rebuiltTa.selectionEnd, 6, "Layer 2 restores selectionEnd");

  // Padding click inside form.reply-inline prevents ancestor .viewer[tabindex=-1] from stealing focus
  const rebuiltBox = superThread.querySelector('[data-inline-box="e5"]');
  const mdEv = doc.dispatch("mousedown", { target: rebuiltBox });
  assert.equal(mdEv.defaultPrevented, true, "mousedown on .reply-inline padding calls preventDefault to protect focus");
  assert.equal(doc.activeElement, rebuiltTa);

  // ─── 4. Thread Drawer (#thread-drawer) on iPad portrait (820x1180): sync focus + tap textarea doesn't close drawer ───
  mockWidth = 820;
  fakeVV.width = 820;
  fakeVV.height = 1180;
  // Add a reply on e5 so e5 has a thread
  const threadEntries = [
    ...mutatedEntries,
    mkEntry(9, "collie", "补充说明：单测耗时 4.2s。", { re: { entry: "e5", who: "Collie", text: mutatedEntries[4].text } }),
  ];
  T.setSup({
    router: { status: "idle", alive: true },
    entries: threadEntries,
    workers: [],
    pending: [{ id: "e5", summary: "PR #142", options: ["直接合入", "先补用例"] }],
    pinned: [],
  });
  // Close main inline first, then open thread drawer
  doc.dispatch("keydown", { target: rebuiltTa, key: "Escape", isComposing: false });
  assert.equal(T.getOpenInline(), null);
  T.openThreadDrawer("e5", "e5", "e5", { immediate: true });
  flushRAF();
  assert.equal(T.getActiveThreadRoot(), "e5");
  assert.equal(tdDrawer.hidden, false);

  const tdTog = tdList.querySelector('[data-toggle-inline="e5"]');
  assert.ok(tdTog, "drawer renders 补充一句 button on pending decision");
  tdList.dispatch("click", { target: tdTog }); // DO NOT flush rAF yet!
  const tdTa = tdList.querySelector('[data-inline-input="e5"]');
  assert.ok(tdTa, "drawer renders inline textarea");
  assert.equal(doc.activeElement, tdTa, "drawer 补充一句 focuses [data-inline-input] synchronously before rAF");

  // Tapping inside the inline textarea in drawer MUST NOT invoke locateFromDrawer / closeThreadDrawer!
  tdList.dispatch("click", { target: tdTa });
  assert.equal(T.getActiveThreadRoot(), "e5", "tapping drawer inline textarea must keep drawer open");
  assert.equal(tdDrawer.hidden, false, "drawer must remain visible when tapping inline textarea");
  assert.equal(doc.activeElement, tdTa, "drawer inline textarea keeps focus on tap");

  // SSE update while editing inside drawer keeps exact textarea instance & focus
  const tdEntriesNext = [...threadEntries, mkEntry(10, "collie", "抽屉内编辑中途到达的新回复", { re: { entry: "e5", who: "Collie", text: mutatedEntries[4].text } })];
  tdTa.value = "抽屉补充一句草稿";
  tdTa.setSelectionRange(2, 5);
  doc.dispatch("input", { target: tdTa });
  liveES.emit("super", {
    router: { status: "idle", alive: true },
    entries: tdEntriesNext,
    workers: [],
    pending: [{ id: "e5", summary: "PR #142", options: ["直接合入", "先补用例"] }],
    pinned: [],
  });
  flushRAF();
  assert.equal(tdList.querySelector('[data-inline-input="e5"]'), tdTa, "drawer SSE keeps same <textarea> DOM node in place");
  assert.equal(doc.activeElement, tdTa, "drawer SSE preserves focus on [data-inline-input]");
  assert.equal(tdTa.selectionStart, 2);
  assert.equal(tdTa.selectionEnd, 5);

  T.closeThreadDrawer({ immediate: true });
  doc.dispatch("keydown", { target: tdTa, key: "Escape", isComposing: false });
  flushRAF();

  // ─── 5. Mobile (<=760px, 390x844) Plan B: synchronous focus on #super-input & stays focused across keyboard shrink ───
  mockWidth = 390;
  fakeVV.width = 390;
  fakeVV.height = 844;
  narrowListeners.forEach((fn) => fn());
  T.renderSuper({ force: true });
  flushRAF();

  const mobTog = superThread.querySelector('[data-toggle-inline="e5"]');
  assert.ok(mobTog, "mobile renders 补充一句 button");
  superThread.dispatch("click", { target: mobTog });
  assert.equal(T.getOpenInline(), null, "mobile Plan B does not open mid-stream inline box");
  assert.equal(T.getReplyTo()?.entry, "e5", "mobile Plan B attaches e5 supplement target");
  assert.equal(reDraft.hidden, false, "mobile Plan B shows #re-draft strip");
  assert.equal(doc.activeElement, superIn, "mobile Plan B focuses #super-input synchronously");

  trackReflow = true;
  forcedReflows = [];
  layoutDirty = false;
  fakeVV.height = 508; // phone keyboard expand
  fakeVV.dispatch("resize");
  flushRAF();
  assert.equal(forcedReflows.length, 0, "mobile Plan B keyboard shrink performs 0 forced reflows");
  assert.equal(doc.activeElement, superIn, "mobile Plan B keeps focus on #super-input after keyboard shrink");
  trackReflow = false;

  console.log("OK: all inline reply keyboard focus & rerender regression tests passed");
  process.exit(0);
})().catch((err) => {
  console.error(err);
  process.exit(1);
});
`
	cmd := exec.Command(nodeBin, "-e", jsTest, root)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("inline reply kb focus JS test failed: %v\n%s", err, string(out))
	}
	if !strings.Contains(string(out), "OK: all inline reply keyboard focus & rerender regression tests passed") {
		t.Fatalf("unexpected node output:\n%s", string(out))
	}
}
