package collie

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lihuanyu0w0/collie/web"
)

func TestInstantPaintCacheContractsAndRuntime(t *testing.T) {
	t.Parallel()

	// 1. Static asset embedding & demo parity checks.
	root := filepath.Join("..", "..")
	cacheBytes, err := os.ReadFile(filepath.Join(root, "web", "cache.js"))
	if err != nil {
		t.Fatalf("read web/cache.js: %v", err)
	}
	demoCacheBytes, err := os.ReadFile(filepath.Join(root, "docs", "demo", "cache.js"))
	if err != nil {
		t.Fatalf("read docs/demo/cache.js: %v", err)
	}
	if string(cacheBytes) != string(demoCacheBytes) {
		t.Fatalf("docs/demo/cache.js is out of sync with web/cache.js; run sh scripts/demo.sh")
	}

	embeddedCache, err := web.FS.ReadFile("cache.js")
	if err != nil || len(embeddedCache) == 0 {
		t.Fatalf("web.FS must embed cache.js: err=%v len=%d", err, len(embeddedCache))
	}

	htmlBytes, err := os.ReadFile(filepath.Join(root, "web", "index.html"))
	if err != nil {
		t.Fatalf("read web/index.html: %v", err)
	}
	html := string(htmlBytes)
	cacheTagIdx := strings.Index(html, `<script src="/cache.js"></script>`)
	appTagIdx := strings.Index(html, `<script src="/app.js"></script>`)
	if cacheTagIdx < 0 || appTagIdx < 0 || cacheTagIdx >= appTagIdx {
		t.Fatalf("web/index.html must load /cache.js before /app.js")
	}

	demoHtmlBytes, err := os.ReadFile(filepath.Join(root, "docs", "demo", "index.html"))
	if err != nil {
		t.Fatalf("read docs/demo/index.html: %v", err)
	}
	demoHtml := string(demoHtmlBytes)
	if !strings.Contains(demoHtml, `<script src="cache.js"></script>`) || !strings.Contains(demoHtml, `<script src="demo.js"></script>`) {
		t.Fatalf("docs/demo/index.html must include cache.js and demo.js")
	}

	if _, err := exec.LookPath("node"); err != nil {
		t.Skip("node not installed; skipping JS instant cache runtime tests")
	}

	// 2. Unit tests for web/cache.js (SHA-256, 200KB cap & trimming, QuotaExceededError,
	//    origin/token isolation, schema mismatch invalidation, debounce/flush/cancel).
	cacheUnitScript := `
const assert = require("node:assert/strict");
const crypto = require("node:crypto");
const fs = require("node:fs");
const vm = require("node:vm");

const cacheSrc = fs.readFileSync(process.argv[1], "utf8");

function makeStorage({ quotaLimit = Infinity, failOnce = false, failAlways = false } = {}) {
  const map = new Map();
  let failedOnce = false;
  return {
    _map: map,
    get length() { return map.size; },
    key(i) { return [...map.keys()][i] ?? null; },
    getItem(k) { return map.has(k) ? map.get(k) : null; },
    setItem(k, v) {
      const s = String(v);
      if (failAlways) {
        const err = new Error("Quota exceeded");
        err.name = "QuotaExceededError";
        throw err;
      }
      if (failOnce && !failedOnce) {
        failedOnce = true;
        const err = new Error("Quota exceeded");
        err.name = "QuotaExceededError";
        throw err;
      }
      if (Buffer.byteLength(s, "utf8") > quotaLimit) {
        const err = new Error("Quota exceeded");
        err.name = "QuotaExceededError";
        throw err;
      }
      map.set(k, s);
    },
    removeItem(k) { map.delete(k); },
  };
}

let timerSeq = 1;
const timers = new Map();
const sandbox = {
  console,
  TextEncoder,
  Uint8Array,
  Uint32Array,
  DataView,
  Map,
  Set,
  Date,
  Math,
  JSON,
  Object,
  Array,
  String,
  Number,
  Boolean,
  Error,
  location: { origin: "http://127.0.0.1:7788", protocol: "http:", host: "127.0.0.1:7788" },
  setTimeout(fn, ms) { const id = timerSeq++; timers.set(id, { fn, ms }); return id; },
  clearTimeout(id) { timers.delete(id); },
};
sandbox.window = sandbox;
sandbox.globalThis = sandbox;
vm.runInNewContext(cacheSrc, sandbox);
const C = sandbox.__collieCache;
assert.ok(C, "__collieCache must be exposed");

// 2a) Pure-JS synchronous SHA-256 matches Node crypto across ASCII & UTF-8 Chinese inputs.
for (const sample of ["", "abc", "http://127.0.0.1:7788", "tok-secret-99887766", "你好Collie边牧🚀"]) {
  const want = crypto.createHash("sha256").update(sample, "utf8").digest("hex");
  assert.equal(C.sha256Hex(sample), want, "sha256 mismatch for: " + sample);
}

// 2b) Basic round-trip: keeps latest 45 entries, marks earlier=true, preserves pending/pinned/sidebar.
{
  const st = makeStorage();
  const entries = Array.from({ length: 70 }, (_, i) => ({
    id: "e" + (i + 1),
    role: i % 2 === 0 ? "user" : "collie",
    text: "Message #" + (i + 1),
    at: new Date(1700000000000 + i * 60000).toISOString(),
  }));
  // Include an unsent local optimistic bubble (no id / pending: true) that MUST NOT be cached.
  entries.push({ _localId: "tmp1", pending: true, role: "user", text: "optimistic unsent" });

  const sup = {
    router: { alive: true, status: "idle", kind: "claude", model: "sonnet" },
    entries,
    earlier: false,
    pending: [{ id: "e70", at: entries[69].at, summary: "确认方案", options: ["好", "先等等"] }],
    pinned: [{ id: "e68", at: entries[67].at, role: "collie", summary: "关键结论" }],
    last_read_entry_id: "e69",
    workers: [{ name: "w-demo", label: "跑测试", kind: "codex", status: "working", pane_id: "w2:p1" }],
    queued: 1,
  };
  const state = {
    ok: true,
    agents: [{ pane_id: "w2:p1", name: "w-demo", kind: "codex", status: "working", is_agent: true, machine: "" }],
    shells: [{ pane_id: "w3:p1", name: "logs", kind: "", status: "shell", is_agent: false, machine: "" }],
    machines: [{ id: "", name: "hub", local: true, online: true }],
    last_read_entry_id: "e69",
  };
  const wRes = C.writeNow({ sup, state }, { origin: "http://127.0.0.1:7788", token: "secret-token-A", storage: st });
  assert.equal(wRes.ok, true);
  assert.ok(wRes.bytes <= C.MAX_BYTES);
  assert.equal(wRes.entries, C.MAX_ENTRIES, "capped to latest MAX_ENTRIES (45)");

  // Plaintext token must NEVER appear in key or stored JSON value.
  const rawStored = st.getItem(wRes.key);
  assert.ok(!wRes.key.includes("secret-token-A"), "key must not leak plaintext token");
  assert.ok(!rawStored.includes("secret-token-A"), "value must not leak plaintext token");

  const snap = C.read({ origin: "http://127.0.0.1:7788", token: "secret-token-A", storage: st });
  assert.ok(snap, "snapshot must read back");
  assert.equal(snap.sup.entries.length, C.MAX_ENTRIES);
  assert.equal(snap.sup.entries[0].id, "e26", "oldest kept entry should be e26");
  assert.equal(snap.sup.entries[snap.sup.entries.length - 1].id, "e70", "newest kept entry should be e70");
  assert.equal(snap.sup.earlier, true, "trimmed snapshot sets earlier=true");
  assert.equal(snap.sup.pending.length, 1);
  assert.equal(snap.sup.pinned.length, 1);
  assert.equal(snap.sup.last_read_entry_id, "e69");
  assert.equal(snap.state.agents.length, 1);
}

// 2c) Strict 200 KB UTF-8 cap: huge Chinese messages auto-trim oldest entries & clip oversized single message.
{
  const st = makeStorage();
  const bigChunk = "边牧预渲染性能测试数据块".repeat(600); // ~19.8 KB UTF-8 each
  const entries = Array.from({ length: 45 }, (_, i) => ({
    id: "e" + (i + 1),
    role: i % 2 === 0 ? "user" : "collie",
    text: (i === 44 ? "【最新超大消息】" + "汉字".repeat(12000) : "第" + (i + 1) + "条：" + bigChunk),
    at: new Date(1700000000000 + i * 60000).toISOString(),
  }));
  const wRes = C.writeNow(
    { sup: { router: { status: "idle" }, entries, pending: [], pinned: [], workers: [] }, state: null },
    { origin: "http://127.0.0.1:7788", token: "tok-big", storage: st }
  );
  assert.equal(wRes.ok, true);
  assert.ok(wRes.bytes <= 200 * 1024, "serialized UTF-8 bytes must be <= 200 KB, got " + wRes.bytes);
  assert.equal(Buffer.byteLength(st.getItem(wRes.key), "utf8"), wRes.bytes);
  assert.ok(wRes.trimmedEntries > 0, "oldest entries must be trimmed to fit 200 KB");
  const snap = C.read({ origin: "http://127.0.0.1:7788", token: "tok-big", storage: st });
  assert.ok(snap && snap.sup.entries.length >= 1 && snap.sup.entries.length < 45);
  assert.equal(snap.sup.entries[snap.sup.entries.length - 1].id, "e45", "newest entry must be retained");
  assert.equal(snap.sup.earlier, true, "trimmed snapshot sets earlier=true");
}

// 2d) QuotaExceededError fault tolerance: retries compactly without throwing, and survives permanent quota failure.
{
  const stRetry = makeStorage({ failOnce: true });
  const entries = Array.from({ length: 40 }, (_, i) => ({
    id: "e" + (i + 1), role: "collie", text: "Entry " + (i + 1) + " " + "x".repeat(400), at: new Date().toISOString(),
  }));
  const res1 = C.writeNow(
    { sup: { router: { status: "idle" }, entries, pending: [], pinned: [], workers: [] }, state: null },
    { origin: "http://127.0.0.1:7788", token: "tok-q", storage: stRetry }
  );
  assert.equal(res1.ok, true, "quota fallback retry should succeed");
  assert.equal(res1.quotaFallback, true);
  assert.ok(res1.bytes <= 64 * 1024 && res1.entries <= 16);

  const stHard = makeStorage({ failAlways: true });
  const res2 = C.writeNow(
    { sup: { router: { status: "idle" }, entries, pending: [], pinned: [], workers: [] }, state: null },
    { origin: "http://127.0.0.1:7788", token: "tok-q", storage: stHard }
  );
  assert.equal(res2.ok, false);
  assert.equal(res2.quotaExceeded, true);
}

// 2e) Strict origin + token isolation (never cross Hub / sandbox / account / anonymous).
{
  const st = makeStorage();
  const mkSup = (txt) => ({ router: { status: "idle" }, entries: [{ id: "e1", role: "collie", text: txt, at: new Date().toISOString() }], pending: [], pinned: [], workers: [] });
  const kHubA = C.writeNow({ sup: mkSup("HUB-A-MAIN"), state: null }, { origin: "http://127.0.0.1:8888", token: "tok-1", storage: st }).key;
  const kHubB = C.writeNow({ sup: mkSup("HUB-B-SANDBOX"), state: null }, { origin: "http://127.0.0.1:8899", token: "tok-1", storage: st }).key;
  const kTok2 = C.writeNow({ sup: mkSup("HUB-A-TOK2"), state: null }, { origin: "http://127.0.0.1:8888", token: "tok-2", storage: st }).key;
  const kAnon = C.writeNow({ sup: mkSup("HUB-A-ANON"), state: null }, { origin: "http://127.0.0.1:8888", token: "", storage: st }).key;

  assert.equal(new Set([kHubA, kHubB, kTok2, kAnon]).size, 4, "all four isolation scopes must have unique keys");
  assert.equal(C.read({ origin: "http://127.0.0.1:8888", token: "tok-1", storage: st }).sup.entries[0].text, "HUB-A-MAIN");
  assert.equal(C.read({ origin: "http://127.0.0.1:8899", token: "tok-1", storage: st }).sup.entries[0].text, "HUB-B-SANDBOX");
  assert.equal(C.read({ origin: "http://127.0.0.1:8888", token: "tok-2", storage: st }).sup.entries[0].text, "HUB-A-TOK2");
  assert.equal(C.read({ origin: "http://127.0.0.1:8888", token: "", storage: st }).sup.entries[0].text, "HUB-A-ANON");
  assert.equal(C.read({ origin: "http://127.0.0.1:8888", token: "tok-unknown", storage: st }), null);

  // Tampered origin inside stored envelope must be rejected and purged.
  const tampered = JSON.parse(st.getItem(kHubA));
  tampered.origin = "http://evil.example:8888";
  st.setItem(kHubA, JSON.stringify(tampered));
  assert.equal(C.read({ origin: "http://127.0.0.1:8888", token: "tok-1", storage: st }), null);
  assert.equal(st.getItem(kHubA), null, "tampered key must be deleted on read");
}

// 2f) Schema version mismatch, corrupt JSON, and expiry auto-invalidation.
{
  const st = makeStorage();
  const key = C.buildKey("http://127.0.0.1:7788", "tok-ver");
  st.setItem(key, JSON.stringify({
    v: 999,
    origin: "http://127.0.0.1:7788",
    authFp: C.tokenFingerprint("tok-ver"),
    savedAt: Date.now(),
    sup: { entries: [{ id: "e1", role: "collie", text: "old schema" }] },
  }));
  assert.equal(C.read({ origin: "http://127.0.0.1:7788", token: "tok-ver", storage: st }), null, "mismatched schema must return null");
  assert.equal(st.getItem(key), null, "mismatched schema must be removed automatically");

  st.setItem(key, "{not-valid-json");
  assert.equal(C.read({ origin: "http://127.0.0.1:7788", token: "tok-ver", storage: st }), null);
  assert.equal(st.getItem(key), null, "corrupt JSON must be removed automatically");

  // Legacy key v0 on same origin must be pruned when v1 writes, while other origins stay untouched.
  const legacyKey = C.buildKey("http://127.0.0.1:7788", "tok-ver", 0);
  const otherOriginKey = C.buildKey("http://127.0.0.1:9999", "tok-ver", 1);
  st.setItem(legacyKey, JSON.stringify({ v: 0, origin: "http://127.0.0.1:7788", authFp: C.tokenFingerprint("tok-ver"), savedAt: Date.now(), sup: { entries: [] } }));
  st.setItem(otherOriginKey, JSON.stringify({ v: 1, origin: "http://127.0.0.1:9999", authFp: C.tokenFingerprint("tok-ver"), savedAt: Date.now(), sup: { entries: [] } }));
  C.writeNow({ sup: { entries: [] }, state: null }, { origin: "http://127.0.0.1:7788", token: "tok-ver", storage: st });
  assert.equal(st.getItem(legacyKey), null, "legacy v0 key on same origin must be pruned");
  assert.ok(st.getItem(otherOriginKey) !== null, "other origin key must not be touched");
}

// 2g) Debounced scheduleWrite + flush + clear cancels pending writes.
{
  const st = makeStorage();
  let val = "first";
  C.scheduleWrite(() => ({ sup: { entries: [{ id: "e1", role: "collie", text: val, at: new Date().toISOString() }] }, state: null }), {
    origin: "http://127.0.0.1:7788", token: "tok-deb", storage: st, debounceMs: 350,
  });
  val = "second";
  C.scheduleWrite(() => ({ sup: { entries: [{ id: "e1", role: "collie", text: val, at: new Date().toISOString() }] }, state: null }), {
    origin: "http://127.0.0.1:7788", token: "tok-deb", storage: st, debounceMs: 350,
  });
  assert.equal(timers.size, 1, "burst writes coalesce into one debounce timer");
  assert.equal(C.read({ origin: "http://127.0.0.1:7788", token: "tok-deb", storage: st }), null, "not written before flush/timer");
  C.flush();
  assert.equal(timers.size, 0);
  assert.equal(C.read({ origin: "http://127.0.0.1:7788", token: "tok-deb", storage: st }).sup.entries[0].text, "second");

  // Schedule another write, then clear(): timer cancelled and storage emptied.
  C.scheduleWrite(() => ({ sup: { entries: [{ id: "e2", role: "collie", text: "ghost", at: new Date().toISOString() }] }, state: null }), {
    origin: "http://127.0.0.1:7788", token: "tok-deb", storage: st, debounceMs: 350,
  });
  assert.equal(timers.size, 1);
  C.clear({ origin: "http://127.0.0.1:7788", token: "tok-deb", storage: st });
  assert.equal(timers.size, 0, "clear() must cancel pending debounced write");
  assert.equal(C.read({ origin: "http://127.0.0.1:7788", token: "tok-deb", storage: st }), null);
}
`
	cmd1 := exec.Command("node", "-e", cacheUnitScript, filepath.Join(root, "web", "cache.js"))
	if out, err := cmd1.CombinedOutput(); err != nil {
		t.Fatalf("web/cache.js unit tests failed: %v\n%s", err, out)
	}

	// 3. Integrated VM tests: web/cache.js + web/app.js together
	//    Covers: 0ms cold-start paint, zero pre-auth leaks, in-place deduped reconciliation
	//    (including promoteCollieNodeInPlace + unread divider + scroll preservation),
	//    401/403/logout/account-switch cache purge, and iOS visibilitychange resume catch-up.
	appIntegScript := `
const assert = require("node:assert/strict");
const fs = require("node:fs");
const vm = require("node:vm");

const cacheSrc = fs.readFileSync(process.argv[1], "utf8");
const appSrc = fs.readFileSync(process.argv[2], "utf8");
const marker = "  boot();\n})();";
assert.ok(appSrc.includes(marker), "expected boot() tail in web/app.js");

function createDomHarness({ origin = "http://127.0.0.1:7788", hash = "#/collie", initialStorage = null, mobile = false } = {}) {
  const storageMap = new Map(initialStorage || []);
  const elements = new Map();
  const docListeners = new Map();
  const winListeners = new Map();
  let docHidden = false;
  let reloadCount = 0;
  let nowMs = 1795800000000;
  let esInstances = [];

  function makeClassList(initial = "") {
    const set = new Set(String(initial || "").split(/\s+/).filter(Boolean));
    return {
      _set: set,
      add: (...cs) => cs.forEach((c) => c && set.add(c)),
      remove: (...cs) => cs.forEach((c) => set.delete(c)),
      toggle: (c, f) => { const on = f === undefined ? !set.has(c) : !!f; if (on) set.add(c); else set.delete(c); return on; },
      contains: (c) => set.has(c),
      toString: () => [...set].join(" "),
    };
  }

  // Minimal DOM node capable of supporting patchSuperThread keyed node tracking + promoteCollieNodeInPlace.
  function makeNode(tag = "DIV", id = "") {
    const attrs = new Map();
    if (id) attrs.set("id", id);
    const listeners = new Map();
    let _cls = "";
    let _innerHTML = "";
    const node = {
      nodeType: 1,
      tagName: String(tag).toUpperCase(),
      id,
      hidden: id === "#login" || id === "#app" || id === "#super-working" || id === "#pending-wrap" || id === "#pending-list",
      open: false,
      value: "",
      disabled: false,
      checked: false,
      title: "",
      dataset: {},
      style: { setProperty() {}, removeProperty() {}, getPropertyValue() { return ""; } },
      children: [],
      parentNode: null,
      parentElement: null,
      scrollTop: 0,
      scrollLeft: 0,
      clientHeight: 600,
      clientWidth: mobile ? 390 : 1000,
      offsetWidth: 100,
      offsetParent: {},
      isConnected: true,
      options: [],
      selectedOptions: [],
      get className() { return _cls; },
      set className(v) {
        _cls = String(v || "");
        node.classList = makeClassList(_cls);
      },
      classList: makeClassList(""),
      get firstChild() { return node.children[0] || null; },
      get firstElementChild() { return node.children[0] || null; },
      get lastElementChild() { return node.children[node.children.length - 1] || null; },
      get childElementCount() { return node.children.length; },
      get nextElementSibling() {
        const p = node.parentNode;
        if (!p) return null;
        const idx = p.children.indexOf(node);
        return idx >= 0 ? (p.children[idx + 1] || null) : null;
      },
      get scrollHeight() {
        if (node === scroller) {
          const box = getEl("#super-thread");
          return Math.max(600, box.children.length * 80);
        }
        return Math.max(node.clientHeight, node.children.length * 80);
      },
      get textContent() { return node._text ?? ""; },
      set textContent(v) { node._text = String(v ?? ""); },
      get innerHTML() { return _innerHTML; },
      set innerHTML(html) {
        _innerHTML = String(html ?? "");
        if (node.tagName === "TEMPLATE") {
          node.content.children = parseTopLevelNodes(_innerHTML);
          for (const ch of node.content.children) { ch.parentNode = node.content; ch.parentElement = null; }
        }
      },
      appendChild(c) {
        if (!c) return c;
        if (c._isFrag) {
          const items = [...c.children];
          c.children.length = 0;
          for (const it of items) node.appendChild(it);
          return c;
        }
        if (c.parentNode && c.parentNode !== node) {
          const oi = c.parentNode.children.indexOf(c);
          if (oi >= 0) c.parentNode.children.splice(oi, 1);
        } else if (c.parentNode === node) {
          const oi = node.children.indexOf(c);
          if (oi >= 0) node.children.splice(oi, 1);
        }
        c.parentNode = node;
        c.parentElement = node;
        node.children.push(c);
        return c;
      },
      insertBefore(c, ref) {
        if (!ref) return node.appendChild(c);
        if (c._isFrag) {
          const items = [...c.children];
          c.children.length = 0;
          for (const it of items) node.insertBefore(it, ref);
          return c;
        }
        if (c.parentNode) {
          const oi = c.parentNode.children.indexOf(c);
          if (oi >= 0) c.parentNode.children.splice(oi, 1);
        }
        const idx = node.children.indexOf(ref);
        c.parentNode = node;
        c.parentElement = node;
        if (idx < 0) node.children.push(c);
        else node.children.splice(idx, 0, c);
        return c;
      },
      removeChild(c) {
        const idx = node.children.indexOf(c);
        if (idx >= 0) node.children.splice(idx, 1);
        c.parentNode = null;
        c.parentElement = null;
        return c;
      },
      replaceChild(n, o) {
        const idx = node.children.indexOf(o);
        if (idx >= 0) {
          node.children[idx] = n;
          o.parentNode = null; o.parentElement = null;
          n.parentNode = node; n.parentElement = node;
        }
        return o;
      },
      append(...args) { for (const a of args) if (a && typeof a === "object") node.appendChild(a); },
      after() {},
      remove() { if (node.parentNode) node.parentNode.removeChild(node); },
      setAttribute(k, v) {
        attrs.set(k, String(v));
        if (k === "id") node.id = String(v);
        if (k === "class") node.className = String(v);
        if (k.startsWith("data-")) {
          const camel = k.slice(5).replace(/-([a-z])/g, (_, ch) => ch.toUpperCase());
          node.dataset[camel] = String(v);
        }
      },
      getAttribute(k) { return attrs.has(k) ? attrs.get(k) : null; },
      removeAttribute(k) { attrs.delete(k); },
      hasAttribute(k) { return attrs.has(k); },
      addEventListener(t, fn) { (listeners.get(t) || listeners.set(t, []).get(t)).push(fn); },
      removeEventListener() {},
      dispatch(t, ev = {}) { for (const fn of listeners.get(t) || []) fn({ target: node, ...ev }); },
      querySelector(sel) {
        if (sel === "#unread-sep") {
          return node.children.find((c) => c.id === "unread-sep" || c._rawHtml?.includes('id="unread-sep"')) || null;
        }
        if (sel === ".msg.assistant") {
          return node._msgAssistant || null;
        }
        if (sel === ".msg.user") {
          return node._msgUser || null;
        }
        if (sel === "button[type='submit']") return getEl("#login-submit");
        if (sel.includes(".msg[data-id=")) {
          const m = /data-id="([^"]+)"/.exec(sel);
          if (m) return node.children.find((c) => c._entryId === m[1]) || null;
          return null;
        }
        if (sel.startsWith(":scope") || sel === ".pin-ind" || sel === ".thread-entry" || sel === ".clap-btn[data-clap]" || sel === ".pin-btn.on" || sel === ".focus-btn" || sel === ".dot" || sel === ".wstate" || sel === ".avatar") {
          return null;
        }
        return getEl((id || tag) + ":" + sel);
      },
      querySelectorAll(sel) {
        if (sel === ".msg[data-id], .event[data-id]") {
          return node.children.filter((c) => !!c._entryId);
        }
        if (sel === "[data-enter]") return [];
        return [];
      },
      closest() { return null; },
      contains(other) { return node === other || node.children.includes(other); },
      focus() {},
      blur() {},
      click() {},
      scrollIntoView() {},
      getBoundingClientRect() {
        const box = getEl("#super-thread");
        const idx = box.children.indexOf(node);
        const top = idx >= 0 ? (idx * 80 - scroller.scrollTop) : 0;
        return { top, bottom: top + 80, left: 0, right: 320, width: 320, height: 80 };
      },
    };
    if (node.tagName === "TEMPLATE") {
      node.content = { children: [], get firstElementChild() { return this.children[0] || null; } };
    }
    return node;
  }

  function parseTopLevelNodes(html) {
    // Parse each top-level rendered super item (<div class="say...>, <div class="mine...>, <div class="unread-sep"...>, <div class="time-sep"...>, <a/div class="event"...>)
    const out = [];
    const s = String(html || "").trim();
    if (!s) return out;
    // Split on top-level elements by scanning tag depth.
    let i = 0;
    const voidTags = new Set(["img", "br", "hr", "input", "path", "circle", "rect", "line", "polygon", "ellipse"]);
    while (i < s.length) {
      const lt = s.indexOf("<", i);
      if (lt < 0) break;
      const openM = /^<([a-zA-Z0-9-]+)([^>]*)>/.exec(s.slice(lt));
      if (!openM) break;
      const tag = openM[1].toLowerCase();
      let depth = 1;
      let pos = lt + openM[0].length;
      if (!openM[0].endsWith("/>") && !voidTags.has(tag)) {
        const tagRe = /<\/?([a-zA-Z0-9-]+)(?:\s[^>]*)?\/?>/g;
        tagRe.lastIndex = pos;
        let m;
        while ((m = tagRe.exec(s)) && depth > 0) {
          const full = m[0];
          const tn = m[1].toLowerCase();
          if (full.startsWith("</")) depth--;
          else if (!full.endsWith("/>") && !voidTags.has(tn)) depth++;
          pos = tagRe.lastIndex;
        }
      }
      const chunk = s.slice(lt, pos);
      const el = makeNode(tag);
      el._rawHtml = chunk;
      const clsM = /^<[a-z0-9-]+[^>]*\bclass="([^"]*)"/i.exec(chunk);
      if (clsM) el.className = clsM[1];
      const idM = /^<[a-z0-9-]+[^>]*\bid="([^"]*)"/i.exec(chunk);
      if (idM) el.setAttribute("id", idM[1]);
      const dataIdM = /\bdata-id="([^"]+)"/.exec(chunk);
      if (dataIdM) el._entryId = dataIdM[1];
      const firstUnM = /\bdata-first-unread="([^"]+)"/.exec(chunk);
      if (firstUnM) el.dataset.firstUnread = firstUnM[1];
      if (el.classList.contains("say")) {
        const msgA = makeNode("DIV");
        msgA.className = "msg assistant";
        if (dataIdM) msgA.dataset.id = dataIdM[1];
        el._msgAssistant = msgA;
      } else if (el.classList.contains("mine")) {
        const msgU = makeNode("DIV");
        msgU.className = "msg user";
        if (dataIdM) msgU.dataset.id = dataIdM[1];
        el._msgUser = msgU;
      }
      out.push(el);
      i = pos;
    }
    return out;
  }

  const scroller = makeNode("DIV", "#super-scroller");
  function getEl(sel) {
    if (!elements.has(sel)) {
      const el = makeNode("DIV", sel);
      if (sel === "#super-thread") {
        el.parentElement = scroller;
        el.parentNode = scroller;
        scroller.children.push(el);
      } else {
        el.parentElement = makeNode("DIV", sel + ":parent");
      }
      elements.set(sel, el);
    }
    return elements.get(sel);
  }
  getEl("#super-thread");
  getEl("#login").hidden = true;
  getEl("#app").hidden = true;

  const storage = {
    get length() { return storageMap.size; },
    key(i) { return [...storageMap.keys()][i] ?? null; },
    getItem(k) { return storageMap.has(k) ? storageMap.get(k) : null; },
    setItem(k, v) { storageMap.set(k, String(v)); },
    removeItem(k) { storageMap.delete(k); },
  };

  let fetchHandler = async () => ({ ok: true, status: 200, headers: { get: () => null }, text: async () => "{}", json: async () => ({}) });
  const fetchLog = [];

  class MockEventSource {
    constructor(url) {
      this.url = url;
      this.readyState = 1; // OPEN
      this._listeners = new Map();
      esInstances.push(this);
    }
    addEventListener(t, fn) { (this._listeners.get(t) || this._listeners.set(t, []).get(t)).push(fn); }
    emit(t, data) {
      for (const fn of this._listeners.get(t) || []) fn({ data: typeof data === "string" ? data : JSON.stringify(data) });
    }
    close() { this.readyState = 2; }
  }
  MockEventSource.CONNECTING = 0;
  MockEventSource.OPEN = 1;
  MockEventSource.CLOSED = 2;

  const doc = {
    get hidden() { return docHidden; },
    documentElement: makeNode("HTML", "html"),
    body: makeNode("BODY", "body"),
    title: "Collie",
    hasFocus: () => !docHidden,
    querySelector: (s) => (s === "dialog[open]" ? null : getEl(s)),
    querySelectorAll: (s) => {
      if (s === ".viewer, #log, #td-body, #wd-thread, #wd-log") return [];
      return [];
    },
    getElementById: (id) => getEl("#" + id),
    createElement: (tag) => makeNode(tag),
    createDocumentFragment: () => ({ _isFrag: true, children: [], get firstChild() { return this.children[0] || null; }, appendChild(c) { this.children.push(c); return c; } }),
    addEventListener(t, fn) { (docListeners.get(t) || docListeners.set(t, []).get(t)).push(fn); },
    dispatchDoc(t, ev = {}) { for (const fn of docListeners.get(t) || []) fn(ev); },
  };

  const sandbox = {
    console,
    TextEncoder,
    Uint8Array,
    Uint32Array,
    DataView,
    URL,
    URLSearchParams,
    AbortController,
    AbortSignal,
    DOMException,
    Event,
    Map,
    Set,
    Promise,
    JSON,
    Math,
    RegExp,
    Object,
    Array,
    String,
    Number,
    Boolean,
    Error,
    encodeURIComponent,
    decodeURIComponent,
    Date: class MockDate extends Date {
      constructor(...args) { super(...(args.length ? args : [nowMs])); }
      static now() { return nowMs; }
    },
    performance: { now: () => nowMs - 1795800000000 },
    setTimeout: (fn, ms) => globalThis.setTimeout(fn, Math.min(ms || 0, 15)),
    clearTimeout: (id) => globalThis.clearTimeout(id),
    setInterval: () => 0,
    clearInterval: () => {},
    requestAnimationFrame: (fn) => { fn(); return 0; },
    cancelAnimationFrame: () => {},
    MutationObserver: class { observe() {} disconnect() {} },
    ResizeObserver: class { observe() {} disconnect() {} },
    IntersectionObserver: class { observe() {} unobserve() {} disconnect() {} },
    CSS: { escape: (s) => String(s), supports: () => true },
    crypto: { randomUUID: () => "cache-test-uuid" },
    HTMLDialogElement: { prototype: {} },
    getComputedStyle: () => ({ getPropertyValue: () => "" }),
    innerWidth: mobile ? 390 : 1440,
    innerHeight: mobile ? 844 : 900,
    localStorage: storage,
    navigator: { userAgent: mobile ? "Mozilla/5.0 (iPhone; CPU iPhone OS 18_0 like Mac OS X) Safari/604.1" : "Mozilla/5.0 Chrome/130.0" },
    location: { origin, protocol: "http:", host: origin.replace(/^https?:\/\//, ""), hostname: "127.0.0.1", pathname: "/", search: "", hash, reload() { reloadCount++; } },
    history: { state: null, replaceState() {}, pushState() {} },
    document: doc,
    matchMedia: (q) => ({ matches: mobile && q.includes("760px"), addEventListener() {} }),
    addEventListener(t, fn) { (winListeners.get(t) || winListeners.set(t, []).get(t)).push(fn); },
    removeEventListener() {},
    dispatchEvent() {},
    confirm: () => true,
    fetch: async (url, init) => {
      fetchLog.push({ url: String(url), method: init?.method || "GET" });
      return fetchHandler(String(url), init);
    },
    EventSource: MockEventSource,
  };
  sandbox.window = sandbox;
  sandbox.globalThis = sandbox;

  vm.runInNewContext(cacheSrc, sandbox);
  const instrumented = appSrc.replace(
    marker,
    "  globalThis.__T = { boot, api, login, pair, doLogout, showLogin, renderSuper, noteCollieUserScroll, authBackup, getSup: () => sup, getState: () => state, getView: () => view, isHydrated: () => hydratedFromCache, isLiveSynced: () => liveSuperSynced, clearSnapCache, flushSnapCacheWrite };\n})();"
  );
  vm.runInNewContext(instrumented, sandbox);

  return {
    sandbox,
    T: sandbox.__T,
    C: sandbox.__collieCache,
    storage,
    storageMap,
    getEl,
    scroller,
    doc,
    fetchLog,
    esInstances,
    setHidden(v) { docHidden = !!v; },
    advanceMs(ms) { nowMs += ms; },
    setFetch(fn) { fetchHandler = fn; },
    getReloadCount: () => reloadCount,
  };
}

function mkRes(status, body) {
  return {
    ok: status >= 200 && status < 300,
    status,
    statusText: status === 200 ? "OK" : "ERR",
    headers: { get: () => null },
    text: async () => JSON.stringify(body),
    json: async () => body,
  };
}

function deferred() {
  let resolve, reject;
  const promise = new Promise((res, rej) => { resolve = res; reject = rej; });
  return { promise, resolve, reject };
}

function makeEntries(from, to) {
  const out = [];
  for (let i = from; i <= to; i++) {
    out.push({
      id: "e" + i,
      role: i % 2 === 1 ? "user" : "collie",
      text: (i % 2 === 1 ? "用户提问 " : "Collie 回复 ") + i,
      at: new Date(1795790000000 + i * 60000).toISOString(),
      ...(i === 144 ? { claps: 1 } : {}),
    });
  }
  return out;
}

(async () => {
  // ─── Scenario A: 0ms Cold-Start Snapshot Hit + Delayed /api/super Seamless In-Place Reconciliation ───
  {
    const h = createDomHarness({ origin: "http://127.0.0.1:7788", hash: "#/collie", mobile: true });
    h.T.authBackup.set("tok-alpha");
    const cachedEntries = makeEntries(101, 145);
    const seedSup = {
      router: { alive: true, status: "working", kind: "claude", model: "sonnet" },
      entries: cachedEntries,
      earlier: true,
      pending: [{ id: "e144", at: cachedEntries[43].at, summary: "请确认方案", options: ["同意", "暂缓"] }],
      pinned: [{ id: "e142", at: cachedEntries[41].at, role: "collie", summary: "已钉住摘要" }],
      last_read_entry_id: "e145",
      workers: [{ name: "w1", label: "缓存任务", kind: "codex", status: "working", pane_id: "w1:p1" }],
      queued: 0,
    };
    const seedState = {
      ok: true,
      agents: [{ pane_id: "w1:p1", name: "w1", kind: "codex", status: "working", is_agent: true, machine: "" }],
      shells: [],
      machines: [{ id: "", name: "hub", local: true, online: true }],
      last_read_entry_id: "e145",
    };
    h.C.writeNow({ sup: seedSup, state: seedState }, { origin: "http://127.0.0.1:7788", token: "tok-alpha", storage: h.storage });

    const sessDef = deferred();
    const superDef = deferred();
    const stateDef = deferred();
    h.setFetch((url) => {
      if (url === "/api/session") return sessDef.promise;
      if (url === "/api/super") return superDef.promise;
      if (url === "/api/state") return stateDef.promise;
      if (url === "/api/hubs") return Promise.resolve(mkRes(200, { hubs: [] }));
      if (url === "/api/super/settings") return Promise.resolve(mkRes(200, { settings: { router_kind: "claude", worker_kind: "claude" } }));
      if (url === "/api/super/notebook") return Promise.resolve(mkRes(200, { tasks: [] }));
      return Promise.resolve(mkRes(200, { ok: true }));
    });

    // Fire boot() WITHOUT awaiting network!
    const bootPromise = h.T.boot();

    // IMMEDIATELY (0ms, synchronously before ANY fetch resolves):
    assert.equal(h.getEl("#app").hidden, false, "0ms paint: #app must be visible synchronously before /api/session resolves");
    assert.equal(h.getEl("#login").hidden, true, "0ms paint: #login stays hidden");
    assert.equal(h.T.isHydrated(), true, "hydratedFromCache must be true during network wait");
    assert.equal(h.T.isLiveSynced(), false, "liveSuperSynced must still be false");
    assert.equal(h.getEl("#super-working").hidden, true, "stale cached router.status='working' must NOT flash spinner before live sync");
    assert.equal(h.getEl("#pending-wrap").hidden, false, "0ms paint: pending bar visible from snapshot");

    const box = h.getEl("#super-thread");
    const cachedRenderedIds = box.children.filter((c) => c._entryId).map((c) => c._entryId);
    assert.equal(cachedRenderedIds.length, 45, "0ms paint: all 45 cached messages rendered synchronously");
    assert.equal(cachedRenderedIds[0], "e101");
    assert.equal(cachedRenderedIds[44], "e145");
    assert.equal(box._colliePatchStats.fullRebuilds, 1, "initial cached paint counts as 1 initial build");

    // Pre-auth guard: ZERO unauthorized read/settings/notebook/models fetches before /api/session.
    assert.deepEqual(h.fetchLog.map((x) => x.url), ["/api/session"], "only /api/session fires pre-auth");

    // Capture exact DOM element references for e140..e145 to prove in-place reuse & promotion.
    const nodeRefs = new Map();
    for (const ch of box.children) {
      if (ch._entryId) nodeRefs.set(ch._entryId, ch);
    }

    // Now resolve /api/session, then delayed /api/super (with older e80..e100, updated e144 claps=5, and 2 new replies e146..e147).
    sessDef.resolve(mkRes(200, { ok: true }));
    await new Promise((r) => setTimeout(r, 20));
    const liveEntries = makeEntries(80, 147);
    liveEntries.find((e) => e.id === "e144").claps = 5;
    superDef.resolve(mkRes(200, {
      router: { alive: true, status: "idle", kind: "claude", model: "sonnet" },
      entries: liveEntries,
      earlier: false,
      pending: [{ id: "e144", at: liveEntries[64].at, summary: "请确认方案", options: ["同意", "暂缓"] }],
      pinned: [{ id: "e142", at: liveEntries[62].at, role: "collie", summary: "已钉住摘要" }],
      last_read_entry_id: "e145",
      workers: [{ name: "w1", label: "缓存任务", kind: "codex", status: "done", ended: true, pane_id: "w1:p1" }],
      queued: 0,
    }));
    stateDef.resolve(mkRes(200, {
      ok: true,
      agents: [],
      shells: [],
      machines: [{ id: "", name: "hub", local: true, online: true }],
      last_read_entry_id: "e145",
    }));
    await bootPromise;

    assert.equal(h.T.isHydrated(), false, "hydratedFromCache cleared after live sync");
    assert.equal(h.T.isLiveSynced(), true, "liveSuperSynced true after live sync");
    assert.equal(box._colliePatchStats.fullRebuilds, 1, "live reconciliation MUST NOT trigger a full DOM rebuild");

    // Zero duplicate bubbles and exact 68 entries (e80..e147).
    const finalIds = box.children.filter((c) => c._entryId).map((c) => c._entryId);
    assert.equal(finalIds.length, 68, "total reconciled entries e80..e147");
    assert.equal(new Set(finalIds).size, 68, "ZERO duplicate bubbles after cache-to-live reconciliation");

    // Verify DOM node reuse + in-place promotion (e144 updated claps 1->5 via promoteCollieNodeInPlace).
    for (const id of ["e140", "e141", "e142", "e143", "e144", "e145"]) {
      const curNode = box.children.find((c) => c._entryId === id);
      assert.strictEqual(curNode, nodeRefs.get(id), "DOM node " + id + " must be reused/promoted in-place without replacement");
    }

    // New unread reply e146 arrived while away -> #unread-sep placed cleanly right before e146.
    const sepNode = box.children.find((c) => c.id === "unread-sep");
    assert.ok(sepNode && sepNode.dataset.firstUnread === "e146", "#unread-sep must anchor at first new unread e146");

    // Verify iOS visibilitychange background -> flush snapshot synchronously, foreground -> keep DOM + reconnect SSE + catch-up e148.
    h.setHidden(true);
    h.doc.dispatchDoc("visibilitychange");
    const flushedSnap = h.C.read({ origin: "http://127.0.0.1:7788", token: "tok-alpha", storage: h.storage });
    assert.ok(flushedSnap && flushedSnap.sup.entries[flushedSnap.sup.entries.length - 1].id === "e147", "hiding page flushes latest live snapshot");

    const esCountBefore = h.esInstances.length;
    const liveWith148 = makeEntries(80, 148);
    h.setFetch((url) => {
      if (url === "/api/super") return Promise.resolve(mkRes(200, { router: { status: "idle" }, entries: liveWith148, pending: [], pinned: [], workers: [], last_read_entry_id: "e145" }));
      if (url === "/api/state") return Promise.resolve(mkRes(200, { ok: true, agents: [], shells: [], machines: [{ id: "", name: "hub", local: true, online: true }], last_read_entry_id: "e145" }));
      return Promise.resolve(mkRes(200, { ok: true }));
    });
    h.advanceMs(6000);
    h.setHidden(false);
    h.doc.dispatchDoc("visibilitychange");
    await new Promise((r) => setTimeout(r, 30));

    assert.equal(h.getEl("#app").hidden, false, "foreground resume keeps #app visible (0 white screen)");
    assert.ok(h.esInstances.length > esCountBefore, "foreground resume after >5s hidden recycles stale SSE connection");
    const afterResumeIds = box.children.filter((c) => c._entryId).map((c) => c._entryId);
    assert.equal(afterResumeIds[afterResumeIds.length - 1], "e148", "foreground resume incrementally pulls new entry e148");
    assert.equal(new Set(afterResumeIds).size, afterResumeIds.length, "0 duplicate bubbles after resume catch-up");
    assert.equal(box._colliePatchStats.fullRebuilds, 1, "resume catch-up never triggers full rebuild");
  }

  // ─── Scenario B: 401 on /api/session immediately clears cached snapshot & DOM and shows #login ───
  {
    const h = createDomHarness({ origin: "http://127.0.0.1:7788", hash: "#/collie" });
    h.T.authBackup.set("tok-revoked");
    h.C.writeNow(
      { sup: { router: { status: "idle" }, entries: makeEntries(1, 5), pending: [], pinned: [], workers: [] }, state: null },
      { origin: "http://127.0.0.1:7788", token: "tok-revoked", storage: h.storage }
    );
    h.setFetch(() => Promise.resolve(mkRes(401, { error: "unauthorized" })));
    await h.T.boot();
    assert.equal(h.C.read({ origin: "http://127.0.0.1:7788", token: "tok-revoked", storage: h.storage }), null, "401 must purge local snapshot cache");
    assert.equal(h.getEl("#super-thread").children.length, 0, "401 must wipe hydrated cached DOM");
    assert.equal(h.getEl("#login").hidden, false, "401 shows #login");
    assert.equal(h.getEl("#app").hidden, true, "401 hides #app");
  }

  // ─── Scenario C: 403 immediately clears cached snapshot & DOM ───
  {
    const h = createDomHarness({ origin: "http://127.0.0.1:7788", hash: "#/collie" });
    h.T.authBackup.set("tok-forbidden");
    h.C.writeNow(
      { sup: { router: { status: "idle" }, entries: makeEntries(1, 5), pending: [], pinned: [], workers: [] }, state: null },
      { origin: "http://127.0.0.1:7788", token: "tok-forbidden", storage: h.storage }
    );
    h.setFetch((url) => {
      if (url === "/api/session") return Promise.resolve(mkRes(403, { error: "forbidden" }));
      return Promise.resolve(mkRes(200, { ok: true }));
    });
    await h.T.boot();
    assert.equal(h.C.read({ origin: "http://127.0.0.1:7788", token: "tok-forbidden", storage: h.storage }), null, "403 must purge local snapshot cache");
    assert.equal(h.getEl("#super-thread").children.length, 0, "403 must wipe hydrated cached DOM");
    assert.equal(h.getEl("#login").hidden, false, "403 pre-auth shows #login");
  }

  // ─── Scenario D: doLogout() & account switch login() immediately clear snapshot cache ───
  {
    const h = createDomHarness({ origin: "http://127.0.0.1:7788", hash: "#/collie" });
    h.T.authBackup.set("tok-user-1");
    h.C.writeNow(
      { sup: { router: { status: "idle" }, entries: makeEntries(1, 3), pending: [], pinned: [], workers: [] }, state: null },
      { origin: "http://127.0.0.1:7788", token: "tok-user-1", storage: h.storage }
    );
    h.setFetch(() => Promise.resolve(mkRes(200, { ok: true })));
    await h.T.login("tok-user-2");
    assert.equal(h.C.read({ origin: "http://127.0.0.1:7788", token: "tok-user-1", storage: h.storage }), null, "switching token must clear previous account snapshot");

    h.C.writeNow(
      { sup: { router: { status: "idle" }, entries: makeEntries(1, 3), pending: [], pinned: [], workers: [] }, state: null },
      { origin: "http://127.0.0.1:7788", token: "tok-user-2", storage: h.storage }
    );
    await h.T.doLogout();
    assert.equal(h.C.read({ origin: "http://127.0.0.1:7788", token: "tok-user-2", storage: h.storage }), null, "doLogout must clear snapshot cache");
    assert.equal(h.getReloadCount(), 1);
  }

  // ─── Scenario E: User scrolls up during slow network -> live reconciliation preserves scroll position ───
  {
    const h = createDomHarness({ origin: "http://127.0.0.1:7788", hash: "#/collie", mobile: true });
    h.T.authBackup.set("tok-scroll");
    h.C.writeNow(
      { sup: { router: { status: "idle" }, entries: makeEntries(101, 145), pending: [], pinned: [], workers: [], last_read_entry_id: "e145" }, state: null },
      { origin: "http://127.0.0.1:7788", token: "tok-scroll", storage: h.storage }
    );
    const superDef = deferred();
    h.setFetch((url) => {
      if (url === "/api/session") return Promise.resolve(mkRes(200, { ok: true }));
      if (url === "/api/super") return superDef.promise;
      if (url === "/api/state") return Promise.resolve(mkRes(200, { ok: true, agents: [], shells: [], machines: [{ id: "", name: "hub", local: true, online: true }] }));
      return Promise.resolve(mkRes(200, { ok: true, hubs: [], settings: {}, tasks: [] }));
    });
    const bootP = h.T.boot();
    // Simulate user scrolling up into middle of cached history before /api/super lands.
    h.advanceMs(200);
    h.scroller.scrollTop = 900;
    h.T.noteCollieUserScroll(h.scroller);

    // Now live /api/super arrives with 20 older entries (e81..e100) + 2 new replies (e146..e147).
    superDef.resolve(mkRes(200, {
      router: { status: "idle" },
      entries: makeEntries(81, 147),
      pending: [], pinned: [], workers: [],
      last_read_entry_id: "e145",
    }));
    await bootP;
    assert.ok(h.scroller.scrollTop < h.scroller.scrollHeight - h.scroller.clientHeight - 100, "live reconciliation must NOT yank scrolled-up user to bottom");
  }
})().catch((err) => { console.error(err); process.exit(1); });
`
	cmd2 := exec.Command("node", "-e", appIntegScript, filepath.Join(root, "web", "cache.js"), filepath.Join(root, "web", "app.js"))
	if out, err := cmd2.CombinedOutput(); err != nil {
		t.Fatalf("integrated instant cache runtime tests failed: %v\n%s", err, out)
	}
}
