package collie

import (
	"os/exec"
	"path/filepath"
	"testing"
)

func TestJoinPageV1V2AndPWAContracts(t *testing.T) {
	t.Parallel()
	if _, err := exec.LookPath("node"); err != nil {
		t.Skip("node not installed")
	}
	script := `
const fs = require("fs");
const vm = require("vm");
const assert = require("assert");

const html = fs.readFileSync("docs/join/index.html", "utf8");

// Static privacy & zero-outbound invariants
assert.ok(html.includes('name="referrer" content="no-referrer"'), "must set no-referrer");
assert.ok(html.includes("collie-join-hub-url"), "must remember Hub URL in localStorage");
assert.ok(html.includes("http://127.0.0.1:17788"), "must include local Node proxy preset");
assert.ok(html.includes("http://127.0.0.1:7788"), "must include local Hub preset");
assert.ok(html.includes("http://your-hub:7788"), "must include neutral custom placeholder");
assert.ok(html.includes("web+collie://"), "must integrate web+collie:// PWA protocol");
assert.ok(!/fetch\(|XMLHttpRequest|new WebSocket/.test(html), "must NEVER perform outbound network requests");

// Extract main inline script (last <script>...</script>)
const scripts = [...html.matchAll(/<script>([\s\S]*?)<\/script>/g)];
assert.ok(scripts.length >= 2, "expected FOUC theme script + main script");
const mainSrc = scripts[scripts.length - 1][1];

function runJoinPage({ hash = "", savedHub = null, lang = "zh-CN", userAgent = "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 Chrome/128.0.0.0 Safari/537.36", maxTouchPoints = 0, clipboardFail = false, execCopyOk = true } = {}) {
  const storage = new Map();
  if (savedHub !== null) storage.set("collie-join-hub-url", savedHub);

  function makeClassList() {
    const set = new Set();
    return {
      add: (...cs) => cs.forEach((c) => c && set.add(c)),
      remove: (...cs) => cs.forEach((c) => set.delete(c)),
      toggle: (c, f) => { const on = f === undefined ? !set.has(c) : !!f; if (on) set.add(c); else set.delete(c); return on; },
      contains: (c) => set.has(c),
    };
  }
  function makeEl(id = "") {
    const listeners = new Map(), attrs = new Map();
    let popOpen = false;
    const el = {
      id, tagName: "DIV", value: "", textContent: "", innerHTML: "", placeholder: "", content: "light dark",
      hidden: false, disabled: false, checked: false, open: false, className: "", dataset: {},
      children: [], classList: makeClassList(), style: {},
      addEventListener(type, fn) { if (!listeners.has(type)) listeners.set(type, []); listeners.get(type).push(fn); },
      removeEventListener(type, fn) { const l = listeners.get(type); if (!l) return; const i = l.indexOf(fn); if (i >= 0) l.splice(i, 1); },
      dispatch(type, ev = {}) { (listeners.get(type) || []).forEach((fn) => fn(ev)); },
      setAttribute(k, v) { attrs.set(k, String(v)); },
      getAttribute(k) { return attrs.get(k) ?? ""; },
      removeAttribute(k) { attrs.delete(k); },
      setCustomValidity(m) { el._validity = m; },
      matches(sel) { return sel === ":popover-open" ? popOpen : false; },
      showPopover() { popOpen = true; },
      hidePopover() { popOpen = false; },
      contains() { return false; },
      append(...nodes) {
        for (const n of nodes) {
          el.children.push(n);
          el.textContent += typeof n === "string" ? n : (n.textContent || "");
        }
      },
      focus() {}, select() {}, setSelectionRange() {}, remove() {},
    };
    return el;
  }

  const byId = new Map();
  function getEl(id) {
    if (!byId.has(id)) byId.set(id, makeEl(id));
    return byId.get(id);
  }
  const radios = [
    Object.assign(getEl("mode-node"), { name: "hub_mode", value: "node", checked: true }),
    Object.assign(getEl("mode-hub"), { name: "hub_mode", value: "hub", checked: false }),
    Object.assign(getEl("mode-pwa"), { name: "hub_mode", value: "pwa", checked: false }),
    Object.assign(getEl("mode-custom"), { name: "hub_mode", value: "custom", checked: false }),
  ];
  const metaScheme = makeEl("meta-scheme");
  metaScheme.content = "light dark";
  const winListeners = new Map();
  const docListeners = new Map();
  let clipboardText = "";

  const ctx = {
    console, Math, Date, JSON, Set, Map, RegExp, Error, Promise, URL, Object, Array, Number, String, Boolean,
    Uint8Array, TextDecoder, atob, encodeURIComponent, decodeURIComponent,
    setTimeout: () => 1, clearTimeout() {}, setInterval: () => 1, clearInterval() {},
    requestAnimationFrame: (fn) => { fn(); return 1; },
    document: {
      title: "",
      hidden: false,
      activeElement: null,
      documentElement: makeEl("html"),
      body: makeEl("body"),
      createElement: (tag) => makeEl(tag),
      createTextNode: (txt) => ({ textContent: String(txt) }),
      getElementById: (id) => getEl(id),
      querySelector: (sel) => (sel.includes("color-scheme") ? metaScheme : null),
      querySelectorAll: (sel) => (sel.includes('hub_mode') ? radios : []),
      addEventListener(type, fn) { if (!docListeners.has(type)) docListeners.set(type, []); docListeners.get(type).push(fn); },
      removeEventListener() {},
      execCommand() { return execCopyOk; },
    },
    location: {
      hash,
      origin: "https://lihuanyu0w0.github.io",
      pathname: "/collie/join/",
      href: "https://lihuanyu0w0.github.io/collie/join/" + hash,
    },
    navigator: {
      language: lang,
      userAgent,
      maxTouchPoints,
      clipboard: {
        writeText: async (txt) => {
          if (clipboardFail) throw new Error("denied");
          clipboardText = txt;
        },
      },
    },
    localStorage: {
      getItem: (k) => (storage.has(k) ? storage.get(k) : null),
      setItem: (k, v) => storage.set(k, String(v)),
      removeItem: (k) => storage.delete(k),
    },
    matchMedia: () => ({ matches: false }),
    addEventListener(type, fn) { if (!winListeners.has(type)) winListeners.set(type, []); winListeners.get(type).push(fn); },
    removeEventListener() {},
  };
  ctx.window = ctx;
  ctx.globalThis = ctx;

  const instrumented = mainSrc.replace(
    "syncFromHash();\n})();",
    "syncFromHash();\nglobalThis.__join = { getState: () => cardState, getMode: () => selectedMode, isMobileClient, detectMobileClient, extractInviteFromInput, parseCardFromHash, buildPwaLaunchUrl, buildHttpLaunchUrl, normalizeUserHubUrl };\n})();"
  );
  vm.runInNewContext(instrumented, ctx, { filename: "docs/join/index.html" });

  return {
    ctx,
    getEl,
    storage,
    winListeners,
    getClipboard: () => clipboardText,
    J: ctx.__join,
  };
}

// 1) Compact v2 permanent card (#2.<b64>): auto http:// prefix + https:// preserved + web+collie://join/2.<b64> + collie-peer://v2/<b64>
const v2Obj = {
  h: "hub_demo002",
  n: "Alex的边牧",
  a: ["198.51.100.12:7788", "[2001:db8::12]:7788", "https://collie.example.com"],
  t: "TOKEN_V2_DEMO",
};
const v2B64 = Buffer.from(JSON.stringify(v2Obj)).toString("base64url");
const runV2 = runJoinPage({ hash: "#2." + v2B64 });
const stV2 = runV2.J.getState();
assert.equal(stV2.status, "permanent");
assert.equal(stV2.version, 2);
assert.equal(stV2.hashPayload, "2." + v2B64);
assert.equal(stV2.inviteCode, "collie-peer://v2/" + v2B64);
assert.deepEqual(stV2.payload.addrs, [
  "http://198.51.100.12:7788",
  "http://[2001:db8::12]:7788",
  "https://collie.example.com",
]);
assert.equal(runV2.J.buildPwaLaunchUrl(stV2), "web+collie://join/2." + v2B64);
assert.equal(runV2.getEl("card-title").textContent, "Alex的边牧 邀请你成为好狗友");
assert.equal(runV2.ctx.document.title, "Alex的边牧 · Collie 好狗友邀请");
assert.equal(runV2.getEl("meta-hub-id").textContent, "hub_demo002");
assert.equal(runV2.getEl("open-section").hidden, false);
assert.equal(runV2.getEl("mode-node").checked, true, "default preset should be local Node proxy 127.0.0.1:17788");
assert.equal(runV2.getEl("custom-wrap").hidden, true, "custom input must remain hidden by default");

// Submitting default form jumps to 127.0.0.1:17788 with collie-peer://v2/<b64>
runV2.getEl("hub-form").dispatch("submit", { preventDefault() {} });
assert.equal(
  runV2.ctx.location.href,
  "http://127.0.0.1:17788/?peer_invite=" + encodeURIComponent("collie-peer://v2/" + v2B64)
);

// Copy invite code copies collie-peer://v2/<b64>
runV2.getEl("copy-code-btn").dispatch("click");
assert.equal(runV2.getClipboard(), "collie-peer://v2/" + v2B64);

// Direct PWA launch sets location.href to web+collie://join/2.<b64> and persists web+collie:// upon window blur handoff
const runPwa = runJoinPage({ hash: "#2." + v2B64 });
runPwa.getEl("pwa-direct-btn").dispatch("click");
assert.equal(runPwa.ctx.location.href, "web+collie://join/2." + v2B64);
assert.equal(runPwa.storage.get("collie-join-hub-url"), undefined, "do not persist PWA before handoff confirmation");
(runPwa.winListeners.get("blur") || []).forEach((fn) => fn());
assert.equal(runPwa.storage.get("collie-join-hub-url"), "web+collie://", "persist web+collie:// after handoff");
assert.equal(runPwa.getEl("mode-pwa").checked, true);
assert.equal(runPwa.getEl("pwa-quick-row").hidden, true);

// 2) v2 one-time invite (k: "once", e: future) & expired v2 invite
const v2OnceObj = { h: "hub_once01", n: "Sam", a: ["203.0.113.8:7788"], t: "ONCE_TOK", k: "once", e: Math.floor(Date.now() / 1000) + 600 };
const v2OnceB64 = Buffer.from(JSON.stringify(v2OnceObj)).toString("base64url");
const runOnce = runJoinPage({ hash: "#collie-peer://v2/" + v2OnceB64 });
assert.equal(runOnce.J.getState().status, "legacy");
assert.equal(runOnce.J.getState().kind, "once");
assert.equal(runOnce.getEl("status-note").hidden, false);

const v2ExpObj = { h: "hub_exp01", n: "Sam", a: ["203.0.113.8:7788"], t: "EXP_TOK", k: "once", e: Math.floor(Date.now() / 1000) - 60 };
const v2ExpB64 = Buffer.from(JSON.stringify(v2ExpObj)).toString("base64url");
const runExp = runJoinPage({ hash: "#2." + v2ExpB64 });
assert.equal(runExp.J.getState().status, "expired");
assert.equal(runExp.getEl("open-section").hidden, true);
assert.equal(runExp.getEl("manual-box").hidden, false);

// 3) Legacy v1 permanent card (#<v1b64>) stays 100% compatible
const v1Obj = { v: 1, code: "PERMCARD_V1", hub_id: "hub_v1_001", hub_name: "Alice's Collie", addrs: ["http://198.51.100.20:7788"] };
const v1B64 = Buffer.from(JSON.stringify(v1Obj)).toString("base64url");
const runV1 = runJoinPage({ hash: "#" + v1B64, savedHub: "web+collie://", lang: "en-US" });
const stV1 = runV1.J.getState();
assert.equal(stV1.status, "permanent");
assert.equal(stV1.version, 1);
assert.equal(stV1.hashPayload, v1B64);
assert.equal(stV1.inviteCode, "collie-peer://v1/" + v1B64);
assert.equal(runV1.getEl("card-title").textContent, "Alice's Collie invites you to become pals");
assert.equal(runV1.ctx.document.title, "Alice's Collie · Collie Pal Invite");
assert.equal(runV1.getEl("mode-pwa").checked, true, "remembered web+collie:// restores pwa mode");
assert.equal(runV1.getEl("pwa-quick-row").hidden, true);
runV1.getEl("hub-form").dispatch("submit", { preventDefault() {} });
assert.equal(runV1.ctx.location.href, "web+collie://join/" + v1B64);

// 4) Extraction handles v2 share copy, web+collie://join/2.<b64>, ?peer_invite=, and v1 variants
assert.deepEqual(runV2.J.extractInviteFromInput("https://lihuanyu0w0.github.io/collie/join/#2." + v2B64), { ver: 2, b64: v2B64 });
assert.deepEqual(runV2.J.extractInviteFromInput("web+collie://join/2." + v2B64), { ver: 2, b64: v2B64 });
assert.deepEqual(runV2.J.extractInviteFromInput("collie-peer://v2/" + v2B64), { ver: 2, b64: v2B64 });
assert.deepEqual(runV2.J.extractInviteFromInput("http://127.0.0.1:17788/?peer_invite=" + encodeURIComponent("collie-peer://v2/" + v2B64)), { ver: 2, b64: v2B64 });
assert.deepEqual(runV2.J.extractInviteFromInput("https://lihuanyu0w0.github.io/collie/join/#" + v1B64), { ver: 1, b64: v1B64 });
assert.deepEqual(runV2.J.extractInviteFromInput("collie-peer://v1/" + v1B64), { ver: 1, b64: v1B64 });

// 5) Mobile UX (iPhone Safari / in-app WebView / Android / iPadOS) without saved custom URL:
//    - Hides desktop 127.0.0.1 presets & web+collie:// quick launch
//    - Never selects web+collie:// even if stale localStorage had it
//    - Primary CTA copies raw invite code & opens clear toast + inline guidance
//    - Saving custom LAN/Tailscale URL switches seamlessly to direct http(s) launch
(async () => {
  const iPhoneLarkUA = "Mozilla/5.0 (iPhone; CPU iPhone OS 17_5 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) Mobile/15E148 Lark/7.20";
  const androidWechatUA = "Mozilla/5.0 (Linux; Android 14; Pixel 8) AppleWebKit/537.36 (KHTML, like Gecko) Version/4.0 Chrome/126.0.0.0 Mobile Safari/537.36 MicroMessenger/8.0";
  const iPadDesktopUA = "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/17.5 Safari/605.1.15";

  // 5a) Fresh mobile visit (even with stale web+collie:// in localStorage) defaults to mobile_copy
  const runMob = runJoinPage({ hash: "#2." + v2B64, savedHub: "web+collie://", lang: "zh-CN", userAgent: iPhoneLarkUA });
  assert.equal(runMob.J.isMobileClient, true);
  assert.equal(runMob.J.getMode(), "mobile_copy", "mobile must ignore stale web+collie:// and default to copy-first");
  assert.equal(runMob.getEl("addr-presets-desktop").hidden, true, "desktop presets must be hidden on mobile");
  assert.equal(runMob.getEl("pwa-quick-row").hidden, true, "web+collie:// quick row must be hidden on mobile");
  assert.equal(runMob.getEl("mode-pwa").checked, false, "mobile must never check web+collie://");
  assert.equal(runMob.getEl("copy-code-btn").hidden, true, "duplicate secondary copy button hidden in copy-first mode");
  assert.equal(runMob.getEl("open-btn").classList.contains("is-copy-primary"), true);
  assert.equal(runMob.getEl("open-btn-text").textContent, "复制邀请码，去 Collie 粘贴");
  assert.equal(runMob.getEl("addr-switch-text").textContent, "填写我的局域网 / Tailscale 地址直达");
  assert.equal(runMob.getEl("custom-wrap").hidden, false, "custom URL input ready inside collapsible disclosure");

  // Submitting in mobile_copy mode copies invite code without navigating away
  const origHref = runMob.ctx.location.href;
  runMob.getEl("hub-form").dispatch("submit", { preventDefault() {} });
  await new Promise((r) => setTimeout(r, 10));
  assert.equal(runMob.ctx.location.href, origHref, "mobile copy primary CTA must NOT navigate to web+collie://");
  assert.equal(runMob.getClipboard(), "collie-peer://v2/" + v2B64);
  assert.equal(runMob.getEl("join-toast").hidden, false, "mobile copy shows toast guidance");
  assert.ok(runMob.getEl("join-toast-msg").textContent.includes("狗友"), "toast guides back to Collie peer modal");
  assert.equal(runMob.getEl("mobile-copy-guide").hidden, false, "inline copy guide remains visible");

  // Rejects web+collie:// and 127.0.0.1 in mobile custom URL input
  assert.equal(runMob.J.normalizeUserHubUrl("web+collie://").ok, false, "mobile rejects web+collie://");
  assert.equal(runMob.J.normalizeUserHubUrl("http://127.0.0.1:7788").ok, false, "mobile rejects 127.0.0.1");

  // Saving LAN/Tailscale URL remembers it & transitions live to direct http(s) launch + secondary copy button
  runMob.getEl("hub-url").value = "192.168.1.42:7788";
  runMob.getEl("save-custom-btn").dispatch("click");
  assert.equal(runMob.storage.get("collie-join-hub-url"), "http://192.168.1.42:7788");
  assert.equal(runMob.J.getMode(), "custom");
  assert.equal(runMob.getEl("open-btn").classList.contains("is-copy-primary"), false);
  assert.equal(runMob.getEl("open-btn-text").textContent, "在我的 Collie 中打开");
  assert.equal(runMob.getEl("copy-code-btn").hidden, false, "secondary copy button appears once custom URL is saved");
  assert.equal(runMob.getEl("clear-custom-btn").hidden, false, "clear saved URL button visible on mobile custom mode");
  runMob.getEl("hub-form").dispatch("submit", { preventDefault() {} });
  assert.equal(
    runMob.ctx.location.href,
    "http://192.168.1.42:7788/?peer_invite=" + encodeURIComponent("collie-peer://v2/" + v2B64)
  );

  // Clearing saved custom URL returns cleanly to copy-first mode
  runMob.getEl("clear-custom-btn").dispatch("click");
  assert.equal(runMob.storage.get("collie-join-hub-url"), undefined);
  assert.equal(runMob.J.getMode(), "mobile_copy");
  assert.equal(runMob.getEl("open-btn-text").textContent, "复制邀请码，去 Collie 粘贴");
  assert.equal(runMob.getEl("copy-code-btn").hidden, true);

  // 5b) Mobile with remembered custom HTTPS URL (English)
  const runMobSaved = runJoinPage({ hash: "#2." + v2B64, savedHub: "https://collie.example.com", lang: "en-US", userAgent: androidWechatUA });
  assert.equal(runMobSaved.J.isMobileClient, true);
  assert.equal(runMobSaved.J.getMode(), "custom");
  assert.equal(runMobSaved.getEl("open-btn-text").textContent, "Open in my Collie");
  assert.equal(runMobSaved.getEl("copy-code-btn").hidden, false);
  assert.equal(runMobSaved.getEl("pwa-quick-row").hidden, true);
  runMobSaved.getEl("hub-form").dispatch("submit", { preventDefault() {} });
  assert.equal(
    runMobSaved.ctx.location.href,
    "https://collie.example.com/?peer_invite=" + encodeURIComponent("collie-peer://v2/" + v2B64)
  );

  // 5c) iPadOS desktop-UA with multi-touch detected as mobile; macOS with 0 touch points stays desktop
  const runIpad = runJoinPage({ hash: "#2." + v2B64, userAgent: iPadDesktopUA, maxTouchPoints: 5, lang: "en-US" });
  assert.equal(runIpad.J.isMobileClient, true, "iPadOS desktop UA + touchPoints>1 must be treated as mobile");
  assert.equal(runIpad.getEl("open-btn-text").textContent, "Copy invite code & paste in Collie");
  assert.equal(runV2.J.isMobileClient, false, "macOS desktop with 0 touchPoints stays desktop");

  // 5d) Clipboard write failure shows selectable fallback code input
  const runFail = runJoinPage({ hash: "#2." + v2B64, userAgent: iPhoneLarkUA, clipboardFail: true, execCopyOk: false });
  runFail.getEl("hub-form").dispatch("submit", { preventDefault() {} });
  await new Promise((r) => setTimeout(r, 10));
  assert.equal(runFail.getEl("mobile-copy-guide").classList.contains("is-err"), true);
  assert.equal(runFail.getEl("mobile-copy-fallback-code").hidden, false);
  assert.equal(runFail.getEl("mobile-copy-fallback-code").value, "collie-peer://v2/" + v2B64);

  console.log("OK: all join page v1/v2/PWA & mobile UX contracts passed");
})().catch((e) => { console.error(e); process.exit(1); });
`
	cmd := exec.Command("node", "-e", script)
	cmd.Dir = filepath.Join("..", "..")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("join page v1/v2/PWA contracts failed: %v\n%s", err, string(out))
	}
}
