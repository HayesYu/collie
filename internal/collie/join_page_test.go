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

function runJoinPage({ hash = "", savedHub = null, lang = "zh-CN" } = {}) {
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
      append(...nodes) {
        for (const n of nodes) {
          el.children.push(n);
          el.textContent += typeof n === "string" ? n : (n.textContent || "");
        }
      },
      focus() {}, select() {}, remove() {},
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
      documentElement: makeEl("html"),
      body: makeEl("body"),
      createElement: (tag) => makeEl(tag),
      createTextNode: (txt) => ({ textContent: String(txt) }),
      getElementById: (id) => getEl(id),
      querySelector: (sel) => (sel.includes("color-scheme") ? metaScheme : null),
      querySelectorAll: (sel) => (sel.includes('hub_mode') ? radios : []),
      addEventListener(type, fn) { if (!docListeners.has(type)) docListeners.set(type, []); docListeners.get(type).push(fn); },
      removeEventListener() {},
      execCommand() {},
    },
    location: {
      hash,
      origin: "https://lihuanyu0w0.github.io",
      pathname: "/collie/join/",
      href: "https://lihuanyu0w0.github.io/collie/join/" + hash,
    },
    navigator: {
      language: lang,
      clipboard: { writeText: async (txt) => { clipboardText = txt; } },
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
    "syncFromHash();\nglobalThis.__join = { getState: () => cardState, extractInviteFromInput, parseCardFromHash, buildPwaLaunchUrl, buildHttpLaunchUrl, normalizeUserHubUrl };\n})();"
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
assert.equal(runV2.getEl("card-title").textContent, "Alex的边牧 邀请你成为好友边牧");
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

console.log("OK: all join page v1/v2/PWA contracts passed");
`
	cmd := exec.Command("node", "-e", script)
	cmd.Dir = filepath.Join("..", "..")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("join page v1/v2/PWA contracts failed: %v\n%s", err, string(out))
	}
}
