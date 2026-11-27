package collie

import (
	"os/exec"
	"path/filepath"
	"testing"
)

func TestWebV033JoinPageAndPeerApprovalContracts(t *testing.T) {
	if _, err := exec.LookPath("node"); err != nil {
		t.Skip("node not installed")
	}
	script := `
const fs = require("fs");
const vm = require("vm");
const assert = require("assert");

const joinHtml = fs.readFileSync("docs/join/index.html", "utf8");
const appSrc = fs.readFileSync("web/app.js", "utf8");
const cssSrc = fs.readFileSync("web/app.css", "utf8");
const idxHtml = fs.readFileSync("web/index.html", "utf8");

// 1) Static /join/ landing page static invariants: zero third-party network calls, no-referrer, hash-only parsing
assert.ok(joinHtml.includes('name="referrer" content="no-referrer"'), "join page must set no-referrer");
assert.ok(joinHtml.includes("collie-join-hub-url"), "join page must remember Hub URL in localStorage");
assert.ok(joinHtml.includes("http://127.0.0.1:17788"), "join page includes standard local placeholder");
assert.ok(joinHtml.includes("http://your-hub:7788"), "join page includes neutral remote placeholder");
assert.ok(!/fetch\(|XMLHttpRequest|new WebSocket/.test(joinHtml), "join page must NEVER perform outbound network requests");
assert.ok(idxHtml.includes('id="peer-req-bar"'), "main chat must include #peer-req-bar notification bar");
assert.ok(cssSrc.includes(".th-pop-preview") && cssSrc.includes("display: none;"), "threads side card aligns to clean 2-line white-paper list");

// 2) Runtime Web UI tests for /join/# extraction, QR URL, ?peer_invite= auto-open & URL stripping
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
  const listeners = new Map(), attrs = new Map(), styles = new Map();
  const el = {
    id, tagName: "DIV", value: "", textContent: "", innerHTML: "",
    hidden: false, disabled: false, checked: false, open: false, inert: false, tabIndex: 0,
    className: "", title: "", dataset: {}, classList: makeClassList(), children: [],
    selectedOptions: [], options: [], scrollTop: 0, scrollHeight: 1000, clientHeight: 600,
    scrollWidth: 400, clientWidth: 400, offsetWidth: 400, offsetHeight: 80, offsetParent: {}, isConnected: true,
    _qs: {}, _qsa: {},
    style: {
      setProperty(k, v) { styles.set(k, String(v)); },
      getPropertyValue(k) { return styles.get(k) || ""; },
      removeProperty(k) { styles.delete(k); },
    },
    addEventListener(type, fn) { if (!listeners.has(type)) listeners.set(type, []); listeners.get(type).push(fn); },
    removeEventListener(type, fn) { const l = listeners.get(type); if (!l) return; const i = l.indexOf(fn); if (i >= 0) l.splice(i, 1); },
    querySelector(sel) { if (el._qs[sel]) return el._qs[sel]; const c = makeEl(id + ":" + sel); el._qs[sel] = c; return c; },
    querySelectorAll(sel) { return el._qsa[sel] || []; },
    setAttribute(k, v) { attrs.set(k, String(v)); },
    getAttribute(k) { return attrs.get(k) ?? ""; },
    removeAttribute(k) { attrs.delete(k); },
    hasAttribute(k) { return attrs.has(k); },
    toggleAttribute(k, f) { const on = f === undefined ? !attrs.has(k) : !!f; if (on) attrs.set(k, ""); else attrs.delete(k); return on; },
    closest() { return null; }, contains() { return false; },
    append() {}, remove() {}, after() {}, focus() {}, click() {}, scrollIntoView() {},
    showModal() { el.open = true; }, close() { el.open = false; },
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
  f._qs[".send"] = makeEl(formSel + " .send");
  f._qs[".files"] = makeEl(formSel + " .files");
  f._qs[".attach"] = makeEl(formSel + " .attach");
  f._qs['input[type="file"]'] = makeEl(formSel + " input[type=file]");
}
getEl("#re-draft")._qs[".re-t"] = makeEl("#re-draft .re-t");
getEl("#re-draft")._qs["button"] = makeEl("#re-draft button");

const replacedUrls = [];
const storage = new Map();
const payloadObj = { v: 1, code: "PERMCARD9999", hub_id: "hub_alice001", hub_name: "Alice的边牧", addrs: ["http://127.0.0.1:18801"] };
const b64Payload = Buffer.from(JSON.stringify(payloadObj)).toString("base64url");
const canonicalInvite = "collie-peer://v1/" + b64Payload;
const joinUrl = "https://lihuanyu0w0.github.io/collie/join/#" + b64Payload;

const doc = {
  documentElement: makeEl("html"),
  body: makeEl("body"),
  hidden: false,
  hasFocus: () => true,
  createElement: (tag) => makeEl(tag),
  getElementById: (id) => getEl("#" + id),
  querySelector: (sel) => (sel === "dialog[open]" ? null : getEl(sel)),
  querySelectorAll: () => [],
  addEventListener() {}, removeEventListener() {},
};
const ctx = {
  console, Math, Date, JSON, Set, Map, RegExp, Error, Promise, URL, URLSearchParams, Object, Array, Number, String, Boolean,
  setTimeout: () => 1, clearTimeout() {}, setInterval: () => 1, clearInterval() {}, queueMicrotask,
  requestAnimationFrame: (fn) => { fn(Date.now()); return 1; }, cancelAnimationFrame() {},
  document: doc,
  location: {
    hash: "",
    origin: "http://127.0.0.1:18802",
    href: "http://127.0.0.1:18802/?peer_invite=" + encodeURIComponent(canonicalInvite),
    pathname: "/",
    search: "?peer_invite=" + encodeURIComponent(canonicalInvite),
  },
  history: {
    state: null,
    pushState() {},
    replaceState(_s, _t, u) {
      replacedUrls.push(u);
      const parsed = new URL(u, "http://127.0.0.1:18802");
      ctx.location.pathname = parsed.pathname;
      ctx.location.search = parsed.search;
      ctx.location.hash = parsed.hash;
    },
    back() {},
  },
  navigator: { platform: "MacIntel", clipboard: { writeText: async () => {} } },
  isSecureContext: true, innerWidth: 1440, innerHeight: 900,
  visualViewport: { height: 900, offsetTop: 0, addEventListener() {} },
  CSS: { escape: (s) => String(s), supports: () => true },
  HTMLDialogElement: { prototype: {} },
  Blob, Uint8Array, TextEncoder, TextDecoder, atob, AbortController,
  crypto: { randomUUID: () => "v033-uuid" },
  performance: { now: () => 1000 },
  Event: class Event { constructor(t) { this.type = t; } },
  CustomEvent: class CustomEvent { constructor(t, d) { this.type = t; this.detail = d?.detail; } },
  ResizeObserver: class { observe() {} disconnect() {} },
  MutationObserver: class { observe() {} disconnect() {} },
  IntersectionObserver: class { observe() {} disconnect() {} },
  EventSource: class { addEventListener() {} close() {} },
  Notification: class { static permission = "default"; static async requestPermission() { return "default"; } },
  localStorage: { getItem: (k) => (storage.has(k) ? storage.get(k) : null), setItem: (k, v) => storage.set(k, String(v)), removeItem: (k) => storage.delete(k) },
  matchMedia: () => ({ matches: false, addEventListener() {} }),
  getComputedStyle: () => ({ getPropertyValue: () => "" }),
  getSelection: () => ({ isCollapsed: true, toString: () => "" }),
  addEventListener() {}, removeEventListener() {}, dispatchEvent() {},
  fetch: async () => ({ ok: true, status: 200, text: async () => JSON.stringify({ ok: true }), json: async () => ({ ok: true }) }),
};
ctx.window = ctx;
ctx.globalThis = ctx;

const marker = "  boot();\n})();";
const instrumented = appSrc.replace(
  marker,
  "  globalThis.__test = { extractPeerInviteCode, parsePeerInviteClient, peerInviteJoinURL, peerShareText, capturePeerInviteFromURL, peerCardSummary, renderRouterPendingBars, setSup: (s) => { sup = normSup(s); }, getPendingInvite: () => pendingPeerInvite };\n})();"
);
vm.runInNewContext(instrumented, ctx, { filename: "web/app.js" });
const T = ctx.__test;

// Extract & parse all supported formats: pure code, /join/#<payload>, /join/#collie-peer://v1/<payload>, share copy, ?peer_invite=
for (const sample of [
  canonicalInvite,
  joinUrl,
  "https://lihuanyu0w0.github.io/collie/join/#" + canonicalInvite,
  "来加我的 Collie 好友边牧「Alice的边牧」，点击链接即可发起申请：\n" + joinUrl,
  "http://127.0.0.1:17788/?peer_invite=" + encodeURIComponent(canonicalInvite),
]) {
  assert.equal(T.extractPeerInviteCode(sample), canonicalInvite, "extract failed for: " + sample);
  const parsed = T.parsePeerInviteClient(sample, "hub_bob002");
  assert.equal(parsed?.ok, true, "parse failed for: " + sample);
  assert.equal(parsed.payload.hub_name, "Alice的边牧");
  assert.equal(parsed.inviteCode, canonicalInvite);
}

// Permanent card has no expiry; legacy expired card still rejected
const expiredObj = { ...payloadObj, exp: Math.floor(Date.now() / 1000) - 60 };
const expiredCode = "collie-peer://v1/" + Buffer.from(JSON.stringify(expiredObj)).toString("base64url");
assert.ok(T.parsePeerInviteClient(expiredCode, "hub_bob002")?.error, "expired legacy invite must fail");

// Share text contains clickable /join/#<payload> URL
const share = T.peerShareText({ invite_code: canonicalInvite, join_url: joinUrl, hub_name: "Alice的边牧" });
assert.ok(share.includes(joinUrl) && !share.includes("collie-peer://v1/"), "share text must use clean clickable /join/# URL");
assert.equal(T.peerInviteJoinURL({ invite_code: canonicalInvite }), joinUrl, "fallback join URL computed cleanly");

// capturePeerInviteFromURL extracts ?peer_invite= and immediately strips it from address bar
const captured = T.capturePeerInviteFromURL();
assert.equal(captured, canonicalInvite);
assert.equal(T.getPendingInvite(), canonicalInvite);
assert.equal(ctx.location.search, "", "peer_invite must be stripped from URL search");
assert.equal(replacedUrls[replacedUrls.length - 1], "/", "replaceState cleans URL to /");

// Main chat #peer-req-bar shows when pending_incoming > 0 and hides when 0
T.setSup({ router: { alive: true, pending: false }, peers_summary: { total: 1, active: 0, pending_incoming: 2, enabled: true } });
T.renderRouterPendingBars();
assert.equal(getEl("#peer-req-bar").hidden, false);
assert.ok(getEl("#peer-req-bar").innerHTML.includes("收到 2 条好友边牧申请"));
assert.equal(T.peerCardSummary(null).stateLbl, "2 条待审批");

T.setSup({ router: { alive: true, pending: false }, peers_summary: { total: 1, active: 1, pending_incoming: 0, enabled: true } });
T.renderRouterPendingBars();
assert.equal(getEl("#peer-req-bar").hidden, true);

console.log("OK: all v0.3.3 join page, permanent card & approval contracts passed");
`
	cmd := exec.Command("node", "-e", script)
	cmd.Dir = filepath.Join("..", "..")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("v0.3.3 contracts failed: %v\n%s", err, string(out))
	}
}
