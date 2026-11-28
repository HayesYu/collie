package collie

import (
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestChatMotionContractsAndRuntimeJS(t *testing.T) {
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

const root = process.argv[1];
const cssSrc = fs.readFileSync(path.join(root, "web", "app.css"), "utf8");
const appSrc = fs.readFileSync(path.join(root, "web", "app.js"), "utf8");

// ─── 1. Static CSS & JS Motion Contracts ───
// 1a) Scheme B staggered mist parameters, 5-block cap, heavy-block blur exemption, and literal-time @supports guard
assert.ok(cssSrc.includes("--cm-step: 34ms;"), "Scheme B stagger step must be 34ms");
assert.ok(cssSrc.includes("animation: cm-mist-in 240ms var(--ease-out-expo) both;"), "Scheme B text block duration must be 240ms");
assert.ok(cssSrc.includes("filter: blur(2.5px);"), "Scheme B soft mist blur must be capped at 2.5px");
assert.ok(cssSrc.includes("transform: translate3d(0, 5px, 0);"), "Scheme B lift must be 5px");
assert.ok(cssSrc.includes(":nth-child(n + 6)"), "Scheme B must hard-cap stagger at block 5");
assert.ok(cssSrc.includes("@supports (animation-delay: calc(sibling-index() * 0.1s))"), "sibling-index() must be gated with a literal time unit in @supports");
assert.ok(!/@supports\s*\([^)]*sibling-index\(\)[^)]*var\(/.test(cssSrc), "@supports must never use var() with sibling-index()");
assert.ok(cssSrc.includes(".md-table") && cssSrc.includes(".decision-wrap") && cssSrc.includes("animation-name: cm-crisp-in;"), "heavy/interactive blocks must skip blur");
assert.ok(cssSrc.includes('#v-collie .say[data-enter="collie-short"]'), "single-sentence short replies must degrade to single-beat mist");

// 1b) Strict GPU compositor guardrails (no box-shadow flash animation, no height/max-height motion, no content-visibility: auto on #super-thread rows)
assert.ok(!cssSrc.includes("@keyframes flash-collie"), "legacy box-shadow flash-collie keyframe must be replaced");
assert.ok(cssSrc.includes("@keyframes cm-flash-breath") && cssSrc.includes("@keyframes cm-flash-reduced"), "quote jump highlight must use pure ::before opacity keyframes");
assert.ok(!/@keyframes\s+cm-[a-z-]+\s*\{[^}]*(?:height|max-height|margin|padding|box-shadow)\s*:/.test(cssSrc), "new cm-* keyframes must not animate layout or box-shadow properties");
assert.ok(!/#super-thread\s*>\s*\.(?:say|mine|event)[^{]*\{[^}]*content-visibility:\s*auto/.test(cssSrc), "must never apply content-visibility: auto to #super-thread direct rows");

// 1c) Thinking shimmer + background/idle pause + reduced-motion fallback
assert.ok(cssSrc.includes("animation: cm-wk-shimmer 2100ms linear infinite;"), "thinking shimmer must run 2.1s linear monochrome sweep");
assert.ok(cssSrc.includes("--shimmer-base: #8e8e88;") && cssSrc.includes("--shimmer-peak: #111111;"), "light theme monochrome shimmer tokens");
assert.ok(cssSrc.includes("--shimmer-base: #7a7a75;") && cssSrc.includes("--shimmer-peak: #f2f2ef;"), "dark theme monochrome shimmer tokens");
assert.ok(cssSrc.includes("html.is-bg .working-row .wk-t") && cssSrc.includes("html.is-idle .working-row .wk-t"), "shimmer must remain paused in background/idle tabs");

// 1d) #super-jump bidirectional entry/exit with separate transition-behavior: allow-discrete + @starting-style + [data-closing]
const jumpCssBlock = cssSrc.match(/\.super-jump\s*\{[\s\S]*?\}/)?.[0] || "";
assert.ok(jumpCssBlock.includes("transition-behavior: allow-discrete;"), "#super-jump must declare separate transition-behavior: allow-discrete");
assert.ok(!/transition:\s*[^;]*allow-discrete/.test(jumpCssBlock), "#super-jump must not put allow-discrete inside transition shorthand");
assert.ok(cssSrc.includes("@starting-style") && cssSrc.includes(".super-jump:where([hidden], [data-closing])"), "#super-jump must provide @starting-style and [data-closing] fallback");
assert.ok(cssSrc.includes("cm-badge-pop-a") && cssSrc.includes("cm-arrow-nod-a"), "#super-jump must include unread nudge bounce and arrow nod");

// 1e) Decision convergence, #crew enter/exit/ping, clap odometer roll, and reduced-motion hygiene
assert.ok(cssSrc.includes(".reply-opts.is-settling .reply-opt.is-chosen") && cssSrc.includes(".reply-receipt[data-just-settled=\"1\"] .rr-ico path") && cssSrc.includes("cm-chk-draw"), "decision convergence and checkmark draw styles must exist");
assert.ok(cssSrc.includes(".crew[data-live=\"1\"] .chip") && cssSrc.includes(".crew .chip[data-closing]") && cssSrc.includes("cm-chip-ping"), "#crew keyed enter/exit and single-shot blocked ping styles must exist");
assert.ok(cssSrc.includes("cm-num-in-a") && cssSrc.includes("cm-num-out-a") && cssSrc.includes("content: attr(data-prev);"), "clap and unread odometer roll styles must exist");
assert.ok(!/\*\s*\{[^}]*animation-duration:\s*0\.01ms\s*!important/.test(cssSrc), "must never use blunt global * { animation-duration: 0.01ms !important }");

// ─── 2. Runtime Node VM Tests ───
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

let mockAnimFactory = () => [];
let mockReducedMotion = false;
let mockDocHidden = false;
let mockSupportsDisplay = false;
let mockSupportsSibling = false;

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
    this.scrollTop = 0;
    this.scrollHeight = 4000;
    this.scrollLeft = 0;
    this.clientWidth = 900;
    this.clientHeight = 700;
    this.offsetWidth = 900;
    this.offsetHeight = 700;
    this.offsetParent = {};
    this.isConnected = true;
    this._styleProps = new Map();
    const self = this;
    this.style = {
      cssText: "",
      display: "",
      transform: "",
      opacity: "",
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
  hasAttribute(k) { return this._attrs.has(k); }
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
    for (const n of nodes) if (n != null) this.appendChild(typeof n === "string" ? new TextNode(n) : n);
  }
  replaceChildren(...nodes) {
    for (const c of this.childNodes) c.parentNode = null;
    this.childNodes = [];
    this.append(...nodes);
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
    ev.type = type;
    ev.target = ev.target || this;
    ev.currentTarget = this;
    ev.preventDefault = ev.preventDefault || (() => { ev.defaultPrevented = true; });
    ev.stopPropagation = ev.stopPropagation || (() => {});
    for (const fn of [...(this._listeners.get(type) || [])]) fn(ev);
    return ev;
  }
  getAnimations(opts) { return mockAnimFactory(this, opts); }
  focus() { doc.activeElement = this; }
  blur() { if (doc.activeElement === this) doc.activeElement = null; }
  scrollTo(opts) { if (opts && typeof opts.top === "number") this.scrollTop = opts.top; }
  getBoundingClientRect() { return { top: 0, bottom: 40, left: 0, right: 800, width: 800, height: 40 }; }
  matches(sel) { return String(sel).split(",").some((p) => matchSimple(this, p.trim())); }
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
    if (m[5] !== undefined) cur.appendChild(new TextNode(m[5]));
    else if (m[1]) {
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
getEl("#re-draft").appendChild(new DomNode("BUTTON"));
const jumpBtn = getEl("#super-jump");
const jumpBadge = getEl("#super-jump-badge");
jumpBtn.appendChild(new DomNode("SVG"));
jumpBtn.appendChild(jumpBadge);
jumpBtn.hidden = true;
jumpBadge.hidden = true;
getEl("#super-working").hidden = true;

const docListeners = new Map();
const doc = {
  body: bodyEl,
  documentElement: rootEl,
  get hidden() { return mockDocHidden; },
  activeElement: null,
  hasFocus: () => !mockDocHidden,
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
  addEventListener: (t, fn) => {
    if (!docListeners.has(t)) docListeners.set(t, []);
    docListeners.get(t).push(fn);
  },
  removeEventListener: () => {},
  dispatch: (t, ev = {}) => { for (const fn of (docListeners.get(t) || [])) fn(ev); },
};

let saySeq = 200;
let sayCalls = [];
const ctx = vm.createContext({
  console, setTimeout, clearTimeout, setInterval: () => 0, clearInterval: () => {},
  performance: { now: () => Date.now() },
  Date, Math, JSON, Map, Set, WeakMap, Promise, RegExp, Array, Object, Number, String, Boolean, Error,
  URLSearchParams, Uint8Array, TextEncoder, atob, AbortController, Event,
  Blob: class { constructor(parts, opts) { this.parts = parts; this.type = opts?.type; } },
  URL: { createObjectURL: () => "blob:mock", revokeObjectURL: () => {} },
  CSS: {
    escape: (s) => String(s).replace(/([^a-zA-Z0-9_-])/g, "\\$1"),
    supports: (a, b) => {
      if (String(a).includes("sibling-index")) return mockSupportsSibling;
      if (a === "transition-behavior" && b === "allow-discrete") return true;
      return false;
    },
  },
  Element: DomNode,
  HTMLDialogElement: { prototype: {} },
  EventSource: class { addEventListener() {} close() {} },
  MutationObserver: class { observe() {} disconnect() {} },
  ResizeObserver: class { observe() {} disconnect() {} },
  getComputedStyle: (el) => ({
    display: mockSupportsDisplay && el?.style?.cssText?.includes("allow-discrete") ? "block" : (el?.style?.display || "block"),
    getPropertyValue: () => "",
  }),
  crypto: { randomUUID: () => "motion-uuid-" + (++saySeq) },
  navigator: { platform: "MacIntel", userAgent: "Chrome", clipboard: null },
  localStorage: { _m: new Map(), getItem(k) { return this._m.get(k) ?? null; }, setItem(k, v) { this._m.set(k, String(v)); }, removeItem(k) { this._m.delete(k); } },
  location: { href: "http://127.0.0.1:7788/#/collie", origin: "http://127.0.0.1:7788", hash: "#/collie", search: "", pathname: "/", hostname: "127.0.0.1" },
  history: { state: { collieIdx: 0 }, replaceState: () => {}, pushState: () => {}, back: () => {} },
  innerWidth: 1440, innerHeight: 900,
  visualViewport: { height: 900, offsetTop: 0, addEventListener: () => {} },
  document: doc,
  matchMedia: (q) => ({
    matches: q.includes("prefers-reduced-motion: reduce") ? mockReducedMotion : false,
    addEventListener: () => {},
  }),
  addEventListener: () => {}, removeEventListener: () => {}, dispatchEvent: () => {},
  requestAnimationFrame: (fn) => { fn(Date.now()); return 1; },
  cancelAnimationFrame: () => {},
  fetch: async (url, init = {}) => {
    const u = String(url);
    const body = init.body ? JSON.parse(init.body) : null;
    if (u === "/api/super/say") {
      sayCalls.push(body);
      return { ok: true, status: 200, headers: { get: () => null }, json: async () => ({ ok: true, entry: "e" + (++saySeq) }) };
    }
    if (u === "/api/chat/read") return { ok: true, status: 200, headers: { get: () => null }, json: async () => ({ ok: true }) };
    if (u === "/api/chat/clap") return { ok: true, status: 200, headers: { get: () => null }, json: async () => ({ ok: true, entry_id: body?.entry_id, claps: 3 }) };
    if (u.startsWith("/api/super/earlier")) {
      return { ok: true, status: 200, headers: { get: () => null }, json: async () => ({ entries: [{ id: "e0", role: "collie", text: "更早的历史消息", at: "2026-11-28T07:00:00Z" }], more: false }) };
    }
    return { ok: true, status: 200, headers: { get: () => null }, json: async () => ({ ok: true, router: { status: "idle", alive: true }, entries: [], workers: [], pending: [], pinned: [], agents: [], shells: [], machines: [], hubs: [] }) };
  },
});
ctx.window = ctx;
ctx.globalThis = ctx;

const hook = [
  "  window.__M = {",
  "    setView: (v) => { view = v; },",
  "    setSup: (s) => { sup = normSup(s); },",
  "    renderSuper: (o) => renderSuper(o),",
  "    sendSuper: (...a) => sendSuper(...a),",
  "    loadOlder: (...a) => loadOlder(...a),",
  "    setCollieParked: (p) => setCollieParked(p),",
  "    updateSuperJump: () => updateSuperJump(),",
  "    handleClapClick: (id, btn) => handleClapClick(id, btn),",
  "    handleQuickOptionClick: (ev, o) => handleQuickOptionClick(ev, o),",
  "    setDrawerOpen: (rootId) => { activeThreadRoot = rootId || ''; $('#app').classList.toggle('has-thread', !!rootId); },",
  "    setReadCursor: (id) => { readCursorId = id; serverReadCursorId = id; },",
  "    getAnimatedKeysSize: () => superAnimatedKeys.size,",
  "  };",
].join("\n");
const marker = "  boot();\n})();";
assert.ok(appSrc.includes(marker));
vm.runInContext(appSrc.replace(marker, hook + "\n})();"), ctx);

const wait = (ms) => new Promise((r) => setTimeout(r, ms));
const mkEntry = (n, role, text, extra = {}) => ({
  id: "e" + n,
  role,
  text,
  at: new Date(Date.UTC(2026, 10, 28, 9, Math.floor(n / 60), n % 60)).toISOString(),
  ...extra,
});

(async () => {
  const M = ctx.__M;
  M.setView("collie");

  // 1) Initial hydration: ZERO enter animations on historical messages
  const baseEntries = [
    mkEntry(1, "user", "帮我看看今天构建情况"),
    mkEntry(2, "collie", "全部绿灯，耗时 12s。", { claps: 1 }),
  ];
  M.setReadCursor("e2");
  M.setSup({ router: { status: "idle", alive: true }, entries: baseEntries, workers: [{ name: "w-lint", kind: "claude", status: "working" }], pending: [], pinned: [], earlier: true });
  M.renderSuper({ force: true, unreadAnchor: true });
  assert.equal(superThread._colliePatchStats.lastAnimated, 0, "initial hydration must perform 0 enter animations");
  assert.equal(superThread.querySelectorAll("[data-enter]").length, 0, "initial messages must not have data-enter");
  // Set scroller cleanly at bottom
  scroller.scrollTop = scroller.scrollHeight - scroller.clientHeight;

  // 2) Live bottom single short Collie reply -> data-enter="collie-short", cleaned immediately on animationend
  const e3 = mkEntry(3, "collie", "收到，已派人去查。");
  M.setSup({ router: { status: "idle", alive: true }, entries: [...baseEntries, e3], workers: [{ name: "w-lint", kind: "claude", status: "working" }], pending: [], pinned: [], earlier: true });
  M.renderSuper();
  assert.equal(superThread._colliePatchStats.lastAnimated, 1, "live single short Collie reply must animate once");
  const e3Row = superThread.querySelector('.msg[data-id="e3"]').closest(".say");
  assert.equal(e3Row.dataset.enter, "collie-short", "single-block reply must degrade to collie-short");
  const e3Block = e3Row.querySelector(".msg.assistant").firstElementChild;
  e3Row.dispatch("animationend", { target: e3Block, animationName: "cm-mist-in" });
  assert.equal(e3Row.hasAttribute("data-enter"), false, "animationend must immediately strip data-enter to free compositor layer");

  // 3) Live bottom multi-block Collie reply (6+ blocks) -> data-enter="collie", --sibling-index capped at 4, cleaned on animationend
  const fence = String.fromCharCode(96).repeat(3);
  const longMd = "第一段结论。\n\n第二段说明。\n\n- 要点一\n- 要点二\n\n" + fence + "js\nconst ok = true;\n" + fence + "\n\n第五段补充。\n\n第六段验证封顶。";
  const e4 = mkEntry(4, "collie", longMd, { options: ["按方案 B 开干", "先观望"] });
  M.setSup({ router: { status: "idle", alive: true }, entries: [...baseEntries, e3, e4], workers: [{ name: "w-lint", kind: "claude", status: "working" }], pending: [{ id: "e4", options: e4.options }], pinned: [], earlier: true });
  M.renderSuper();
  const e4Row = superThread.querySelector('.msg[data-id="e4"]').closest(".say");
  assert.equal(e4Row.dataset.enter, "collie", "multi-block reply must use Scheme B data-enter=collie");
  const e4Kids = e4Row.querySelector(".msg.assistant").children.filter((c) => !c.classList.contains("msg-acts"));
  assert.ok(e4Kids.length >= 6, "long reply should have >=6 content blocks");
  assert.equal(e4Kids[0].style.getPropertyValue("--sibling-index"), "0", "block 0 delay index = 0");
  assert.equal(e4Kids[4].style.getPropertyValue("--sibling-index"), "4", "block 4 delay index = 4");
  assert.equal(e4Kids[5].style.getPropertyValue("--sibling-index"), "4", "block 5+ delay index hard-capped at 4");
  e4Row.dispatch("animationend", { target: e4Kids[4], animationName: "cm-mist-in" });
  assert.equal(e4Row.hasAttribute("data-enter"), false, "multi-block animationend must immediately strip data-enter");
  assert.equal(e4Kids[0].style.getPropertyValue("--sibling-index"), "", "cleanup must remove inline --sibling-index");

  // 4) In-place promote (pin/clap update on e4) -> ZERO replay
  M.setSup({ router: { status: "idle", alive: true }, entries: [...baseEntries, e3, { ...e4, claps: 2 }], workers: [{ name: "w-lint", kind: "claude", status: "working" }], pending: [{ id: "e4", options: e4.options }], pinned: [{ id: "e4", role: "collie", summary: "s" }], earlier: true });
  M.renderSuper();
  assert.equal(superThread._colliePatchStats.lastAnimated, 0, "promoteCollieNodeInPlace must never replay entry animation");
  assert.equal(e4Row.hasAttribute("data-enter"), false, "e4 must stay clean after in-place promote");

  // 5) Outgoing user send -> data-enter="user", promoted to real ID without replaying
  const sendP = M.sendSuper("继续推进下一步", null, false);
  const optUserRow = superThread.lastElementChild;
  assert.equal(optUserRow.dataset.enter, "user", "optimistic user send must get data-enter=user");
  optUserRow.dispatch("animationend", { target: optUserRow.querySelector(".msg.user"), animationName: "cm-user-in" });
  assert.equal(optUserRow.hasAttribute("data-enter"), false, "user bubble data-enter stripped on animationend");
  await sendP;
  // Simulate intermediate renderSuper BEFORE SSE delivers the persisted entry (mine.id updated while still in pendingSays):
  // must promote the existing bubble in-place with ZERO animation replay!
  M.renderSuper();
  assert.equal(superThread._colliePatchStats.lastAnimated, 0, "pre-SSE render after POST /api/super/say must NOT replay user bubble");
  assert.strictEqual(superThread.lastElementChild, optUserRow, "pre-SSE promotion must reuse exact optimistic DOM node");
  const realUserId = "e" + saySeq;
  const afterSendEntries = [...baseEntries, e3, e4, { id: realUserId, role: "user", text: "继续推进下一步", at: new Date().toISOString(), client_msg_id: sayCalls[0].client_msg_id }];
  M.setSup({ router: { status: "idle", alive: true }, entries: afterSendEntries, workers: [{ name: "w-lint", kind: "claude", status: "working" }], pending: [{ id: "e4", options: e4.options }], pinned: [], earlier: true });
  M.renderSuper();
  assert.equal(superThread._colliePatchStats.lastAnimated, 0, "promoting optimistic user bubble must NOT replay");

  // 6) Batch >= 3 replies arriving at bottom -> ZERO animation
  const batchEntries = [
    ...afterSendEntries,
    mkEntry(301, "collie", "批量汇报 1"),
    mkEntry(302, "collie", "批量汇报 2"),
    mkEntry(303, "collie", "批量汇报 3"),
  ];
  M.setSup({ router: { status: "idle", alive: true }, entries: batchEntries, workers: [{ name: "w-lint", kind: "claude", status: "working" }], pending: [{ id: "e4", options: e4.options }], pinned: [], earlier: true });
  M.renderSuper();
  assert.equal(superThread._colliePatchStats.lastAnimated, 0, "batch >= 3 tail replies must skip all enter animations");
  assert.equal(superThread.querySelectorAll("[data-enter]").length, 0, "zero data-enter nodes after batch >= 3");

  // 7) Background tab -> single reply gets 0 animation and never replays on visible return
  mockDocHidden = true;
  const e304 = mkEntry(304, "collie", "后台到达的消息");
  M.setSup({ router: { status: "idle", alive: true }, entries: [...batchEntries, e304], workers: [{ name: "w-lint", kind: "claude", status: "working" }], pending: [{ id: "e4", options: e4.options }], pinned: [], earlier: true });
  M.renderSuper();
  assert.equal(superThread._colliePatchStats.lastAnimated, 0, "hidden tab must skip enter animation");
  mockDocHidden = false;
  M.renderSuper();
  assert.equal(superThread.querySelectorAll("[data-enter]").length, 0, "returning from background must never replay");

  // 8) Desktop side-by-side thread drawer freezes main stream -> 0 animation
  M.setDrawerOpen("e4");
  const e305 = mkEntry(305, "collie", "抽屉打开时到达的消息");
  M.setSup({ router: { status: "idle", alive: true }, entries: [...batchEntries, e304, e305], workers: [{ name: "w-lint", kind: "claude", status: "working" }], pending: [{ id: "e4", options: e4.options }], pinned: [], earlier: true });
  M.renderSuper();
  assert.equal(superThread._colliePatchStats.lastAnimated, 0, "drawerFreezeMain must skip main-thread enter animation");
  M.setDrawerOpen("");

  // 9) Scrolled-up / Unread anchor state: 0 animation, 0px scroll drift, #super-jump nudges & rolls badge!
  M.setReadCursor("e305");
  scroller.scrollTop = 400; // clearly above bottom (4000 - 400 - 700 = 2900 > 42)
  M.updateSuperJump();
  assert.equal(jumpBtn.hidden, false, "#super-jump visible when scrolled up");
  const e306 = mkEntry(306, "collie", "上翻态到达的新未读 1");
  M.setSup({ router: { status: "idle", alive: true }, entries: [...batchEntries, e304, e305, e306], workers: [{ name: "w-lint", kind: "claude", status: "working" }], pending: [{ id: "e4", options: e4.options }], pinned: [], earlier: true });
  M.renderSuper();
  assert.equal(superThread._colliePatchStats.lastAnimated, 0, "scrolled-up unread arrival MUST have 0 enter animation");
  assert.equal(scroller.scrollTop, 400, "scrolled-up unread arrival MUST keep 0px scroll drift");
  assert.equal(jumpBadge.textContent, "1", "jump badge updates to 1");
  assert.ok(jumpBtn.classList.contains("nudge-a") || jumpBtn.classList.contains("nudge-b"), "jump button must nudge on unread increment");
  const e307 = mkEntry(307, "collie", "上翻态到达的新未读 2");
  M.setSup({ router: { status: "idle", alive: true }, entries: [...batchEntries, e304, e305, e306, e307], workers: [{ name: "w-lint", kind: "claude", status: "working" }], pending: [{ id: "e4", options: e4.options }], pinned: [], earlier: true });
  M.renderSuper();
  assert.equal(jumpBadge.textContent, "2", "odometer roll keeps exact textContent=2 even mid-animation");
  assert.equal(jumpBadge.getAttribute("data-prev"), "1", "odometer roll stores previous count in data-prev");
  assert.ok(jumpBadge.querySelector(".num-cur"), "odometer roll wraps current number in .num-cur");

  // 10) #super-jump Firefox fallback ([data-closing] + getAnimations)
  let resolveJumpAnim;
  mockAnimFactory = (el) => el === jumpBtn ? [{ finished: new Promise((r) => { resolveJumpAnim = r; }) }] : [];
  scroller.scrollTop = scroller.scrollHeight - scroller.clientHeight;
  M.setReadCursor("e307");
  M.updateSuperJump();
  assert.equal(jumpBtn.hasAttribute("data-closing"), true, "Firefox fallback marks #super-jump data-closing before hiding");
  assert.equal(jumpBtn.hidden, false, "#super-jump stays visible during exit transition");
  resolveJumpAnim();
  await wait(10);
  assert.equal(jumpBtn.hidden, true, "#super-jump hides after exit animation finishes");
  assert.equal(jumpBtn.hasAttribute("data-closing"), false, "data-closing removed after hide");
  mockAnimFactory = () => [];

  // 11) History loadOlder prepend -> 0 animation
  scroller.scrollTop = 100;
  await M.loadOlder(true);
  assert.equal(superThread._colliePatchStats.lastAnimated, 0, "loadOlder prepend must perform 0 enter animations");

  // 12) Route park clears in-flight data-enter -> 0 replay on unpark
  scroller.scrollTop = scroller.scrollHeight - scroller.clientHeight;
  const e308 = mkEntry(308, "collie", "测试切路由清理");
  M.setSup({ router: { status: "idle", alive: true }, entries: [...batchEntries, e304, e305, e306, e307, e308], workers: [{ name: "w-lint", kind: "claude", status: "working" }], pending: [{ id: "e4", options: e4.options }], pinned: [], earlier: true });
  M.renderSuper();
  assert.equal(superThread.querySelectorAll("[data-enter]").length, 1, "in-flight data-enter present before park");
  M.setCollieParked(true);
  assert.equal(superThread.querySelectorAll("[data-enter]").length, 0, "parking #v-collie strips all in-flight data-enter");
  M.setCollieParked(false);
  assert.equal(superThread.querySelectorAll("[data-enter]").length, 0, "unparking #v-collie replays 0 animations");

  // 13) Decision --opt press convergence -> receipt emerge + data-just-settled
  const optBtn = superThread.querySelector(".reply-opt");
  assert.ok(optBtn && optBtn.dataset.opt === "按方案 B 开干", "first option button present");
  const optGrp = optBtn.closest(".reply-opts");
  const optP = M.handleQuickOptionClick({ target: optBtn, preventDefault() {} });
  assert.equal(optBtn.classList.contains("is-chosen"), true, "clicked option gets is-chosen");
  assert.equal(optGrp.classList.contains("is-settling"), true, "option group enters is-settling convergence");
  await optP;
  const receiptEl = superThread.querySelector('.reply-receipt[data-entry="e4"]');
  assert.ok(receiptEl, "decision settled to .reply-receipt");
  assert.equal(receiptEl.getAttribute("data-just-settled"), "1", "newly settled receipt carries data-just-settled=1 for emerge + checkmark draw");

  // 14) #crew keyed incremental add, blocked ping, and animated exit
  const crewEl = getEl("#crew");
  const lintChip = crewEl.querySelector('.chip[data-worker="w-lint"]');
  assert.ok(lintChip, "initial w-lint chip exists");
  assert.equal(crewEl.dataset.live, "1", "#crew armed live after initial render");
  // Add w-ui and transition w-lint -> blocked (lintChip identity must be preserved!)
  M.setSup({ router: { status: "idle", alive: true }, entries: [...batchEntries, e304, e305, e306, e307, e308], workers: [{ name: "w-lint", kind: "claude", status: "blocked" }, { name: "w-ui", kind: "codex", status: "working" }], pending: [], pinned: [], earlier: true });
  M.renderSuper();
  assert.strictEqual(crewEl.querySelector('.chip[data-worker="w-lint"]'), lintChip, "existing worker chip must be reused in place");
  assert.equal(lintChip.classList.contains("is-blocked"), true, "blocked worker chip gets is-blocked");
  assert.equal(lintChip.classList.contains("ping-blocked"), true, "transition to blocked triggers single-shot ping-blocked");
  assert.ok(crewEl.querySelector('.chip[data-worker="w-ui"]'), "new worker chip added incrementally");
  // Remove w-ui with animated exit
  let resolveChipExit;
  const uiChip = crewEl.querySelector('.chip[data-worker="w-ui"]');
  mockAnimFactory = (el) => el === uiChip ? [{ finished: new Promise((r) => { resolveChipExit = r; }) }] : [];
  M.setSup({ router: { status: "idle", alive: true }, entries: [...batchEntries, e304, e305, e306, e307, e308], workers: [{ name: "w-lint", kind: "claude", status: "blocked" }], pending: [], pinned: [], earlier: true });
  M.renderSuper();
  assert.equal(uiChip.hasAttribute("data-closing"), true, "removed worker chip enters data-closing");
  resolveChipExit();
  await wait(10);
  assert.equal(crewEl.querySelector('.chip[data-worker="w-ui"]'), null, "worker chip removed cleanly after exit animation");
  mockAnimFactory = () => [];

  // 15) Clap odometer roll keeps exact textContent and supports reduced-motion skip
  const clapBtn = superThread.querySelector('.clap-btn[data-clap="e2"]');
  const clapNum = clapBtn.querySelector(".clap-n");
  assert.equal(clapNum.textContent, "1");
  M.handleClapClick("e2", clapBtn);
  assert.equal(clapNum.textContent, "2", "clap odometer roll preserves exact textContent=2");
  assert.equal(clapNum.getAttribute("data-prev"), "1", "clap odometer stores previous value in data-prev");
  assert.ok(clapNum.classList.contains("roll-a") || clapNum.classList.contains("roll-b"), "clap odometer applies roll class");
  mockReducedMotion = true;
  M.handleClapClick("e2", clapBtn);
  assert.equal(clapNum.textContent, "3", "reduced-motion clap updates count cleanly");
  assert.equal(clapNum.hasAttribute("data-prev"), false, "reduced-motion clap skips odometer roll");

  console.log("OK: all chat motion contracts & runtime tests passed");
  process.exit(0);
})().catch((err) => {
  console.error(err);
  process.exit(1);
});
`
	cmd := exec.Command(nodeBin, "-e", jsTest, root)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("chat motion test failed: %v\n%s", err, string(out))
	}
	if !strings.Contains(string(out), "OK: all chat motion contracts & runtime tests passed") {
		t.Fatalf("unexpected node output:\n%s", string(out))
	}
}
