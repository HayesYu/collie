package collie

import (
	"os/exec"
	"path/filepath"
	"testing"
)

func TestWebV0311PeerModalRedesignContracts(t *testing.T) {
	if _, err := exec.LookPath("node"); err != nil {
		t.Skip("node not installed")
	}
	script := `
const fs = require("fs");
const vm = require("vm");
const assert = require("assert");

const htmlSrc = fs.readFileSync("web/index.html", "utf8");
const cssSrc = fs.readFileSync("web/app.css", "utf8");
const appSrc = fs.readFileSync("web/app.js", "utf8");
const demoSrc = fs.readFileSync("scripts/demo/demo.js", "utf8");

// ─── 1. Static Contracts ───
// Top-layer popovers live inside #int-modal so they are never modal-inert under showModal()
const intDialogMatch = htmlSrc.match(/<dialog[^>]*id="int-modal"[\s\S]*?<\/dialog>/);
assert.ok(intDialogMatch, "#int-modal dialog must exist in web/index.html");
assert.ok(
  intDialogMatch[0].includes('id="peer-mode-popover"') &&
  intDialogMatch[0].includes('id="peer-action-popover"') &&
  intDialogMatch[0].includes('popover="auto"'),
  "peer custom popovers must live inside #int-modal with popover=auto"
);

// Redesign tokens: soft warm-gray dark QR plate + multiply blend + elevated dark popover surface + full-width alignment
assert.ok(cssSrc.includes("--pm-qr-plate: #e5e5e1"), "dark mode QR plate must use soft warm-gray #e5e5e1");
assert.ok(cssSrc.includes("mix-blend-mode: multiply"), "QR PNG must multiply-blend onto warm-gray plate without bright white glare");
assert.ok(cssSrc.includes("--pm-pop-bg: #222229"), "dark mode popover menu must elevate surface color above #161619 modal");
assert.ok(cssSrc.includes(".pm-pop[popover].is-match-width") && cssSrc.includes("width: anchor-size(width)"), "full-width dropdown triggers must match popover width to trigger");
assert.ok(cssSrc.includes(".pm-pop[popover].is-align-start") && cssSrc.includes("position-area: block-end span-inline-end"), "left-side compact triggers must align to inline-start");
assert.ok(cssSrc.includes(".pm-fold::details-content"), "advanced details disclosure must use native <details> animation");

// Zero engineering clutter in peer modal header/copy and zero nested segmented sub-tabs
assert.ok(!appSrc.includes("好友边牧 · 受控收件箱"), "peer modal title must be clean Collie 的好狗友 without engineering suffix");
assert.ok(!appSrc.includes("Collie 的好狗友 · 受控收件箱"), "peer modal title must be clean Collie 的好狗友 without engineering suffix");
assert.ok(!appSrc.includes("data-peer-add-sub"), "nested add-friend segmented sub-tabs must be removed in favor of single-level 3 tabs");
assert.ok(appSrc.includes('id="peer-tab-friends"') && appSrc.includes('id="peer-tab-card"') && appSrc.includes('id="peer-tab-add"'), "single-level 3 tabs must exist");
assert.ok(appSrc.includes('id="peer-adv-details"') && appSrc.includes("连接与高级详情"), "raw addrs/code/hub_id/reset must fold into 连接与高级详情");
assert.ok(appSrc.includes('id="peer-paste-btn"') && appSrc.includes("navigator.clipboard?.readText"), "Add Friend input must include one-tap clipboard Paste button");
assert.ok(appSrc.includes('id="peer-scan-capture"') && appSrc.includes('capture="environment"'), "Add Friend panel must include native camera capture input for mobile HTTP");
assert.ok(cssSrc.includes(".pm-paste-box") && cssSrc.includes(".pm-paste-btn"), "peer paste box and button styles must be defined");
assert.ok(demoSrc.includes("100.64.0.18:7788"), "demo.js peer mock should stay synced");

// Unified Pals nose-boop dogs + heart icon across expanded sidebar, collapsed rail, integrations/modal, and demo
const bt = String.fromCharCode(96);
const palsDogSvg = '<svg viewBox="0 0 24 24" aria-hidden="true"><path d="M3 17.5V11l2.6-3.5L7.8 10.2h2.4l.6 2.6-2.2 1.4v3.3"/><path d="M21 17.5V11l-2.6-3.5-2.2 2.7h-2.4l-.6 2.6 2.2 1.4v3.3"/><path d="M12 6.8c-.6-1.1-2.2-1-2.2.2 0 1 2.2 2.2 2.2 2.2s2.2-1.2 2.2-2.2c0-1.2-1.6-1.3-2.2-.2z"/></svg>';
const navPeersMatch = htmlSrc.match(/id="nav-peers"[\s\S]*?<\/button>/);
const railPeersMatch = htmlSrc.match(/id="rail-peers"[\s\S]*?<\/button>/);
assert.ok(navPeersMatch && navPeersMatch[0].includes(palsDogSvg), "#nav-peers must use the nose-boop dogs SVG icon");
assert.ok(railPeersMatch && railPeersMatch[0].includes(palsDogSvg), "#rail-peers must use the nose-boop dogs SVG icon");
assert.ok(appSrc.includes("peers: " + bt + palsDogSvg + bt), "INT_ICONS.peers must use the nose-boop dogs SVG icon");
assert.ok(!htmlSrc.includes("M3.5 18.5c.8-3 3-4.5 5.5-4.5") && !appSrc.includes("M16 11a3 3 0 1 0-3-3"), "legacy two-person peers SVG paths must be fully removed");
const demoHtmlSrc = fs.readFileSync("docs/demo/index.html", "utf8");
const demoAppSrc = fs.readFileSync("docs/demo/app.js", "utf8");
assert.ok(demoHtmlSrc.includes(palsDogSvg) && demoAppSrc.includes("peers: " + bt + palsDogSvg + bt), "docs/demo must stay synced with the nose-boop dogs SVG icon");

// ─── 2. Runtime VM Tests ───
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
  let popOpen = false;
  const el = {
    id, tagName: "DIV", value: "", textContent: "", innerHTML: "",
    hidden: false, disabled: false, checked: false, open: false, inert: false, tabIndex: 0,
    className: "", title: "", dataset: {}, classList: makeClassList(), children: [],
    selectedOptions: [], options: [], scrollTop: 0, scrollHeight: 800, clientHeight: 600,
    scrollWidth: 400, clientWidth: 400, offsetWidth: 160, offsetHeight: 32, offsetParent: {}, isConnected: true,
    _qs: {}, _qsa: {},
    style: {
      setProperty(k, v) { styles.set(k, String(v)); },
      getPropertyValue(k) { return styles.get(k) || ""; },
      removeProperty(k) { styles.delete(k); },
      set width(v) { styles.set("width", String(v)); },
      get width() { return styles.get("width") || ""; },
      set minWidth(v) { styles.set("min-width", String(v)); },
      set maxWidth(v) { styles.set("max-width", String(v)); },
      set left(v) { styles.set("left", String(v)); },
      get left() { return styles.get("left") || ""; },
      set top(v) { styles.set("top", String(v)); },
      get top() { return styles.get("top") || ""; },
    },
    addEventListener(type, fn) { if (!listeners.has(type)) listeners.set(type, []); listeners.get(type).push(fn); },
    removeEventListener(type, fn) { const l = listeners.get(type); if (!l) return; const i = l.indexOf(fn); if (i >= 0) l.splice(i, 1); },
    dispatch(type, ev = {}) {
      const e = { type, target: el, currentTarget: el, defaultPrevented: false, preventDefault() { this.defaultPrevented = true; }, stopPropagation() {}, stopImmediatePropagation() {}, composedPath: () => [el], ...ev };
      for (const fn of [...(listeners.get(type) || [])]) fn(e);
      return e;
    },
    querySelector(sel) {
      if (el._qs[sel]) return el._qs[sel];
      if (sel.startsWith("#")) return getEl(sel);
      const c = makeEl(id + ":" + sel);
      el._qs[sel] = c;
      return c;
    },
    querySelectorAll(sel) { return el._qsa[sel] || []; },
    setAttribute(k, v) { attrs.set(k, String(v)); },
    getAttribute(k) { return attrs.get(k) ?? ""; },
    removeAttribute(k) { attrs.delete(k); },
    hasAttribute(k) { return attrs.has(k); },
    toggleAttribute(k, f) { const on = f === undefined ? !attrs.has(k) : !!f; if (on) attrs.set(k, ""); else attrs.delete(k); return on; },
    matches(sel) { return sel === ":popover-open" ? popOpen : false; },
    showPopover() { popOpen = true; el.dispatch("toggle", { newState: "open", oldState: "closed" }); },
    hidePopover() { if (!popOpen) return; popOpen = false; el.dispatch("toggle", { newState: "closed", oldState: "open" }); },
    closest() { return null; }, contains() { return false; },
    _focusCount: 0, _clickCount: 0,
    append() {}, remove() {}, after() {},
    focus() { el._focusCount++; }, select() {}, click() { el._clickCount++; }, scrollIntoView() {},
    showModal() { el.open = true; }, close() { el.open = false; el.dispatch("close"); },
    getBoundingClientRect() { return { top: 200, bottom: 236, left: 20, right: 20 + el.offsetWidth, width: el.offsetWidth, height: el.offsetHeight }; },
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

const apiCalls = [];
let mockPeersState = {
  hub_id: "hub_self_01",
  hub_name: "小屿的边牧",
  enabled: true,
  addrs: ["http://192.168.1.42:7788", "http://100.64.0.18:7788"],
  peers: [],
  blocklist: [],
};
let inviteSeqMock = 0;

const storage = new Map();
const doc = {
  documentElement: makeEl("html"),
  body: makeEl("body"),
  activeElement: null,
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
  location: { hash: "#/collie", origin: "http://127.0.0.1:7788", href: "http://127.0.0.1:7788/#/collie", pathname: "/", search: "" },
  history: { state: null, pushState() {}, replaceState() {}, back() {} },
  navigator: { platform: "MacIntel", userAgent: "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7)", maxTouchPoints: 0, clipboard: { writeText: async () => {}, readText: async () => "" } },
  isSecureContext: true, innerWidth: 1280, innerHeight: 860,
  visualViewport: { height: 860, offsetTop: 0, addEventListener() {} },
  CSS: { escape: (s) => String(s), supports: () => true },
  HTMLDialogElement: { prototype: { closedBy: "any" } },
  HTMLElement: { prototype: {} },
  Blob, Uint8Array, TextEncoder, TextDecoder, atob, AbortController,
  crypto: { randomUUID: () => "v0311-uuid" },
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
  fetch: async (url, init = {}) => {
    const u = String(url);
    const method = (init.method || "GET").toUpperCase();
    const body = init.body ? JSON.parse(init.body) : null;
    apiCalls.push({ url: u, method, body });
    const okJson = (obj) => ({ ok: true, status: 200, text: async () => JSON.stringify(obj), json: async () => obj });
    if (u === "/api/super/peers" && method === "GET") return okJson(mockPeersState);
    if (u === "/api/super/peers/settings" && method === "POST") {
      if (body?.hub_name) mockPeersState = { ...mockPeersState, hub_name: body.hub_name };
      return okJson({ ok: true, hub_name: mockPeersState.hub_name, enabled: true });
    }
    if (u === "/api/super/peers/invite" && method === "POST") {
      inviteSeqMock++;
      const payload = { h: mockPeersState.hub_id, n: mockPeersState.hub_name, a: ["192.168.1.42:7788"], t: "card_" + inviteSeqMock };
      const b64 = Buffer.from(JSON.stringify(payload)).toString("base64url");
      return okJson({
        invite_code: "collie-peer://v2/" + b64,
        join_url: "https://lihuanyu0w0.github.io/collie/join/#2." + b64,
        hub_id: mockPeersState.hub_id,
        hub_name: mockPeersState.hub_name,
        mode: body?.mode || "advise",
        addrs: mockPeersState.addrs,
        permanent: true,
      });
    }
    return okJson({ ok: true });
  },
};
ctx.window = ctx;
ctx.globalThis = ctx;

const marker = "  boot();\n})();";
const instrumented = appSrc.replace(
  marker,
  "  globalThis.__test = { openPeerModal, switchPeerTab, getDraft: () => intModalDraft, isPeerTriggerFullWidth, openPeerPopover, positionPeerPopoverFallback, ensurePeerInvite, patchPeerHeaderAndList, patchPeerQRCard, patchPeerScanPanel };\n})();"
);
vm.runInNewContext(instrumented, ctx, { filename: "web/app.js" });
const T = ctx.__test;

(async () => {
  // 2a) 0 peers: auto opens "card", hides redundant 0 count & pending badge
  await T.openPeerModal("auto", { refresh: true });
  await new Promise((r) => setTimeout(r, 10));
  assert.equal(getEl("#int-modal").open, true);
  assert.equal(getEl("#int-modal-title").textContent, "Collie 的好狗友");
  assert.equal(T.getDraft().tab, "card", "0 peers auto-opens Collie 名片 tab");
  assert.ok(getEl("#int-modal-tabs").innerHTML.includes("Collie 名片"), "card tab renders unified Collie 名片 label");
  assert.ok(getEl("#int-modal-body").innerHTML.includes("邀请码") && getEl("#int-modal-body").innerHTML.includes("名片密钥"), "advanced section uses 邀请码 and preserves 名片密钥");
  assert.ok(!getEl("#int-modal-body").innerHTML.includes("名片码"), "peer modal must not expose legacy 名片码 label");
  assert.equal(getEl("#peer-tab-friends-cnt").hidden, true, "0 peers must hide redundant friend count");
  assert.equal(getEl("#peer-tab-pending-badge").hidden, true, "0 pending must hide amber badge");
  assert.ok(getEl("#peer-adv-addrs").textContent.includes("192.168.1.42:7788"), "advanced details populate normalized addrs");
  assert.ok(getEl("#peer-adv-code").textContent.startsWith("collie-peer://v2/"), "advanced details populate invite code");

  // 2a-2) Inline nickname edit saves via /api/super/peers/settings & refreshes invite QR
  getEl("#peer-name-display-btn").dispatch("click");
  assert.equal(getEl("#peer-name-editor-form").hidden, false, "clicking nickname reveals inline editor");
  getEl("#peer-hub-name").value = "小屿的新边牧";
  getEl("#peer-name-editor-form").dispatch("submit");
  await new Promise((r) => setTimeout(r, 15));
  assert.ok(apiCalls.some((c) => c.url === "/api/super/peers/settings" && c.body?.hub_name === "小屿的新边牧"), "inline nickname submit calls POST /api/super/peers/settings");
  assert.equal(getEl("#peer-hub-name-text").textContent, "小屿的新边牧", "nickname updates in-place");

  // 2b) Two-step reset confirmation calls invite with reset:true
  assert.equal(getEl("#peer-reset-confirm").hidden, true);
  getEl("#peer-reset-ask").dispatch("click");
  assert.equal(getEl("#peer-reset-confirm").hidden, false, "clicking 重置名片 reveals inline confirmation");
  getEl("#peer-reset-cancel").dispatch("click");
  assert.equal(getEl("#peer-reset-confirm").hidden, true, "clicking 取消 hides confirmation");
  getEl("#peer-reset-ask").dispatch("click");
  getEl("#peer-reset-do").dispatch("click");
  await new Promise((r) => setTimeout(r, 10));
  assert.ok(apiCalls.some((c) => c.url === "/api/super/peers/invite" && c.body?.reset === true), "confirming reset calls POST /api/super/peers/invite with reset:true");

  // 2c) Pending incoming + paired peers: auto opens "friends", shows amber badge & hides plain count
  mockPeersState = {
    ...mockPeersState,
    peers: [
      { id: "xiaoya", hub_id: "hub_xy", name: "小雅的边牧", primary_addr: "http://192.168.1.56:7788", status: "pending_incoming", mode: "advise", today_count: 0 },
      { id: "aming", hub_id: "hub_am", name: "阿明的边牧", primary_addr: "http://192.168.1.24:7788", status: "active", mode: "advise", today_count: 6 },
      { id: "linxi", hub_id: "hub_lx", name: "林夕的边牧", primary_addr: "http://100.64.0.31:7788", status: "paused", mode: "notify", today_count: 0 },
      { id: "chenmo", hub_id: "hub_cm", name: "陈默的边牧", primary_addr: "http://192.168.1.73:7788", status: "pending_outgoing", mode: "advise", today_count: 0 },
    ],
  };
  await T.openPeerModal("auto", { refresh: true });
  assert.equal(T.getDraft().tab, "friends");
  assert.equal(getEl("#peer-tab-pending-badge").hidden, false, "pending incoming must display amber badge");
  assert.equal(getEl("#peer-tab-pending-badge").textContent, "1");
  assert.equal(getEl("#peer-tab-friends-cnt").hidden, true, "plain count hides while pending badge is active");
  const listHtml = getEl("#peer-list-slot").innerHTML;
  assert.ok(listHtml.includes("小雅的边牧") && listHtml.includes("先查清，草稿给我审"), "pending request card renders full natural mode label");
  assert.ok(listHtml.includes("阿明的边牧") && listHtml.includes("林夕的边牧") && listHtml.includes("陈默的边牧"), "friends list renders paired, paused, and outgoing peers");
  assert.ok(listHtml.includes('data-peer-approve="xiaoya"') && listHtml.includes('data-peer-reject-block="xiaoya"'), "pending card retains approve/reject/block actions");

  // 2d) Paired without pending: hides amber badge and shows clean count
  mockPeersState = { ...mockPeersState, peers: mockPeersState.peers.filter((p) => p.status !== "pending_incoming") };
  await T.openPeerModal("auto", { refresh: true });
  assert.equal(getEl("#peer-tab-pending-badge").hidden, true);
  assert.equal(getEl("#peer-tab-friends-cnt").hidden, false);
  assert.equal(getEl("#peer-tab-friends-cnt").textContent, "3");

  // 2e) Deep-link ?peer_invite= opens Add Friend with compact confirmed card
  const sampleInv = "collie-peer://v2/" + Buffer.from(JSON.stringify({ h: "hub_zy", n: "周远的边牧", a: ["192.168.1.88:7788"], t: "tok_zy" })).toString("base64url");
  await T.openPeerModal("add", { sub: "scan", inviteRaw: sampleInv, refresh: true });
  assert.equal(T.getDraft().tab, "add");
  assert.equal(getEl("#peer-add-input-wrap").hidden, true, "valid parsed invite collapses raw paste area");
  assert.equal(getEl("#peer-scan-parsed-slot").hidden, false);
  assert.ok(getEl("#peer-scan-parsed-slot").innerHTML.includes("周远的边牧"));
  assert.equal(getEl("#peer-pair-submit").disabled, false);

  // 2f) Full-width dropdown trigger matches trigger width; left-side compact trigger aligns start
  const fullTrig = makeEl("full-trig");
  fullTrig.offsetWidth = 316;
  fullTrig.parentElement = makeEl("full-parent");
  fullTrig.parentElement.clientWidth = 316;
  assert.equal(T.isPeerTriggerFullWidth(fullTrig), true, "full-width trigger detected for 100% width popover");
  const modePop = getEl("#peer-mode-popover");
  ctx.CSS.supports = () => false; // also verify fallback coordinate geometry
  T.openPeerPopover(modePop, fullTrig, { fromPointer: true });
  assert.equal(modePop.classList.contains("is-match-width"), true);
  assert.equal(modePop.style.width, "316px", "fallback positioning locks full-width popover to exact trigger width");
  assert.equal(modePop.style.left, "20px", "fallback positioning flushes left edge to trigger");

  const leftCompactTrig = makeEl("left-compact");
  leftCompactTrig.offsetWidth = 140;
  leftCompactTrig.parentElement = makeEl("row-parent");
  leftCompactTrig.parentElement.clientWidth = 340;
  getEl("#int-modal").offsetWidth = 370;
  T.openPeerPopover(modePop, leftCompactTrig, { fromPointer: true });
  assert.equal(modePop.classList.contains("is-match-width"), false);
  assert.equal(modePop.classList.contains("is-align-start"), true, "left-side compact trigger aligns start inside modal");

  // 2g) Add Friend one-tap Paste button: success auto-parses, denied/unsupported focuses input with guidance
  getEl("#peer-clear-parsed-btn").dispatch("click");
  assert.equal(getEl("#peer-add-input-wrap").hidden, false);
  ctx.navigator.clipboard = { writeText: async () => {}, readText: async () => sampleInv };
  getEl("#peer-paste-btn").dispatch("click");
  await new Promise((r) => setTimeout(r, 15));
  assert.equal(getEl("#peer-add-input-wrap").hidden, true, "pasting valid invite collapses input wrap into confirmed card");
  assert.equal(getEl("#peer-scan-parsed-slot").hidden, false);
  assert.ok(getEl("#peer-scan-parsed-slot").innerHTML.includes("周远的边牧"));
  assert.equal(getEl("#peer-pair-submit").disabled, false);

  // Permission denied fallback -> focuses input & shows guidance note
  getEl("#peer-clear-parsed-btn").dispatch("click");
  const pasteInput = getEl("#peer-paste-input");
  const prevFocus = pasteInput._focusCount;
  ctx.navigator.clipboard = { writeText: async () => {}, readText: async () => { throw new Error("NotAllowedError"); } };
  getEl("#peer-paste-btn").dispatch("click");
  await new Promise((r) => setTimeout(r, 15));
  assert.equal(pasteInput._focusCount, prevFocus + 1, "clipboard permission denied must focus invite input");
  assert.equal(getEl("#peer-scan-note-slot").hidden, false, "clipboard permission denied shows guidance note");
  assert.ok(getEl("#peer-scan-note-slot").textContent.includes("长按"), "guidance note instructs user to long-press paste");

  // Unsupported clipboard API (HTTP LAN) -> focuses input & shows guidance note
  ctx.navigator.clipboard = undefined;
  const prevFocus2 = pasteInput._focusCount;
  getEl("#peer-paste-btn").dispatch("click");
  await new Promise((r) => setTimeout(r, 15));
  assert.equal(pasteInput._focusCount, prevFocus2 + 1, "unsupported clipboard must focus invite input");
  assert.equal(getEl("#peer-scan-note-slot").hidden, false);
  assert.ok(getEl("#peer-scan-note-slot").textContent.includes("长按"));

  // 2h) Non-secure HTTP scan handling (#t13874):
  //     - Desktop HTTP hides scan button cleanly (no dead error button)
  //     - Mobile HTTP keeps scan button & triggers native camera capture input without gray error box
  //     - Secure context (HTTPS / localhost) keeps live scan button visible on desktop & mobile
  ctx.isSecureContext = false;
  ctx.navigator.userAgent = "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7)";
  ctx.navigator.maxTouchPoints = 0;
  T.patchPeerScanPanel();
  assert.equal(getEl("#peer-scan-toggle").hidden, true, "desktop HTTP must hide live camera scan button");

  ctx.navigator.userAgent = "Mozilla/5.0 (iPhone; CPU iPhone OS 17_5 like Mac OS X) Mobile/15E148 Safari/604.1";
  T.patchPeerScanPanel();
  assert.equal(getEl("#peer-scan-toggle").hidden, false, "mobile HTTP keeps scan button for native camera capture");
  const capEl = getEl("#peer-scan-capture");
  const prevCapClicks = capEl._clickCount;
  getEl("#peer-scan-toggle").dispatch("click");
  assert.equal(capEl._clickCount, prevCapClicks + 1, "mobile HTTP scan click triggers hidden capture=environment file input");
  assert.equal(getEl("#peer-scan-note-slot").hidden, true, "mobile HTTP scan must NOT show gray HTTP error banner");

  ctx.isSecureContext = true;
  ctx.navigator.userAgent = "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7)";
  T.patchPeerScanPanel();
  assert.equal(getEl("#peer-scan-toggle").hidden, false, "secure context (HTTPS/localhost) preserves scan button on desktop");

  console.log("OK: all v0.3.11 peer modal redesign contracts passed");
})().catch((e) => { console.error(e); process.exit(1); });
`
	cmd := exec.Command("node", "-e", script)
	cmd.Dir = filepath.Join("..", "..")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("v0.3.11 peer modal tests failed: %v\n%s", err, string(out))
	}
}
