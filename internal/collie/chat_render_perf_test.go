package collie

import (
	"bytes"
	"compress/gzip"
	"io"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lihuanyu0w0/collie/web"
)

func TestStaticGzipNegotiationAndBudget(t *testing.T) {
	t.Parallel()
	srv := &Server{token: "test-tok", home: t.TempDir(), web: web.FS}
	h := srv.Routes()

	firstScreenPaths := []string{"/", "/app.css", "/app.js", "/icon.svg", "/manifest.webmanifest"}
	totalGzBytes := 0
	totalRawBytes := 0

	for _, reqPath := range firstScreenPaths {
		fsPath := strings.TrimPrefix(reqPath, "/")
		if fsPath == "" {
			fsPath = "index.html"
		}
		orig, err := fs.ReadFile(web.FS, fsPath)
		if err != nil {
			t.Fatalf("read web.FS %s: %v", fsPath, err)
		}
		totalRawBytes += len(orig)

		// 1) Gzip request
		recGz := httptest.NewRecorder()
		reqGz := httptest.NewRequest(http.MethodGet, reqPath, nil)
		reqGz.Header.Set("Accept-Encoding", "br, gzip;q=0.9, deflate")
		h.ServeHTTP(recGz, reqGz)
		if recGz.Code != http.StatusOK {
			t.Fatalf("GET %s (gzip) status = %d, want 200", reqPath, recGz.Code)
		}
		if got := recGz.Header().Get("Content-Encoding"); got != "gzip" {
			t.Fatalf("GET %s (gzip) Content-Encoding = %q, want gzip", reqPath, got)
		}
		if vary := recGz.Header().Get("Vary"); !strings.Contains(vary, "Accept-Encoding") {
			t.Fatalf("GET %s (gzip) Vary = %q, want Accept-Encoding", reqPath, vary)
		}
		gzTag := recGz.Header().Get("ETag")
		if gzTag == "" || !strings.HasSuffix(gzTag, `-gz"`) {
			t.Fatalf("GET %s (gzip) ETag = %q, want -gz suffix", reqPath, gzTag)
		}
		gzBody := recGz.Body.Bytes()
		totalGzBytes += len(gzBody)
		if len(gzBody) >= len(orig) {
			t.Fatalf("GET %s gzip size (%d) not smaller than raw (%d)", reqPath, len(gzBody), len(orig))
		}
		zr, err := gzip.NewReader(bytes.NewReader(gzBody))
		if err != nil {
			t.Fatalf("GET %s invalid gzip payload: %v", reqPath, err)
		}
		decoded, err := io.ReadAll(zr)
		_ = zr.Close()
		if err != nil {
			t.Fatalf("GET %s gunzip read failed: %v", reqPath, err)
		}
		if !bytes.Equal(decoded, orig) {
			t.Fatalf("GET %s gunzipped bytes mismatch original", reqPath)
		}

		// 2) Uncompressed request (explicit gzip;q=0)
		recRaw := httptest.NewRecorder()
		reqRaw := httptest.NewRequest(http.MethodGet, reqPath, nil)
		reqRaw.Header.Set("Accept-Encoding", "gzip;q=0, identity")
		h.ServeHTTP(recRaw, reqRaw)
		if recRaw.Code != http.StatusOK {
			t.Fatalf("GET %s (raw) status = %d, want 200", reqPath, recRaw.Code)
		}
		if got := recRaw.Header().Get("Content-Encoding"); got != "" {
			t.Fatalf("GET %s (raw) unexpected Content-Encoding = %q", reqPath, got)
		}
		if vary := recRaw.Header().Get("Vary"); !strings.Contains(vary, "Accept-Encoding") {
			t.Fatalf("GET %s (raw) missing Vary: Accept-Encoding", reqPath)
		}
		rawTag := recRaw.Header().Get("ETag")
		if rawTag == "" || rawTag == gzTag {
			t.Fatalf("GET %s raw ETag %q must differ from gzip ETag %q", reqPath, rawTag, gzTag)
		}
		if !bytes.Equal(recRaw.Body.Bytes(), orig) {
			t.Fatalf("GET %s raw body mismatch", reqPath)
		}

		// 3) Conditional 304 matches representation-specific ETag only
		rec304 := httptest.NewRecorder()
		req304 := httptest.NewRequest(http.MethodGet, reqPath, nil)
		req304.Header.Set("Accept-Encoding", "gzip")
		req304.Header.Set("If-None-Match", gzTag)
		h.ServeHTTP(rec304, req304)
		if rec304.Code != http.StatusNotModified {
			t.Fatalf("GET %s with matching gzip ETag status = %d, want 304", reqPath, rec304.Code)
		}

		recMismatch := httptest.NewRecorder()
		reqMismatch := httptest.NewRequest(http.MethodGet, reqPath, nil)
		reqMismatch.Header.Set("Accept-Encoding", "gzip")
		reqMismatch.Header.Set("If-None-Match", rawTag)
		h.ServeHTTP(recMismatch, reqMismatch)
		if recMismatch.Code != http.StatusOK {
			t.Fatalf("GET %s with raw ETag on gzip request status = %d, want 200", reqPath, recMismatch.Code)
		}
	}

	// Hard budget: cold-start first-screen gzipped transfer <= 480 KB
	// (v0.3.13 merged baseline reached 450.0 KB vs ~2.1 MB in v0.3.11; still guards against eager vendor bundles)
	const maxBudgetBytes = 480 * 1024
	if totalGzBytes > maxBudgetBytes {
		t.Fatalf("first-screen gzipped transfer = %d bytes (%.1f KB), exceeds 480 KB budget", totalGzBytes, float64(totalGzBytes)/1024)
	}
	t.Logf("first-screen transfer: raw=%.1f KB -> gzip=%.1f KB (%.1f%% reduction)",
		float64(totalRawBytes)/1024, float64(totalGzBytes)/1024, 100*(1-float64(totalGzBytes)/float64(totalRawBytes)))

	// 4) HEAD request returns Content-Encoding + Content-Length with empty body
	recHead := httptest.NewRecorder()
	reqHead := httptest.NewRequest(http.MethodHead, "/app.js", nil)
	reqHead.Header.Set("Accept-Encoding", "gzip")
	h.ServeHTTP(recHead, reqHead)
	if recHead.Code != http.StatusOK || recHead.Header().Get("Content-Encoding") != "gzip" || recHead.Body.Len() != 0 {
		t.Fatalf("HEAD /app.js gzip failed: code=%d ce=%q bodyLen=%d", recHead.Code, recHead.Header().Get("Content-Encoding"), recHead.Body.Len())
	}

	// 5) Range requests fall back to raw FileServer (206 Partial Content, uncompressed)
	recRange := httptest.NewRecorder()
	reqRange := httptest.NewRequest(http.MethodGet, "/app.js", nil)
	reqRange.Header.Set("Accept-Encoding", "gzip")
	reqRange.Header.Set("Range", "bytes=0-31")
	h.ServeHTTP(recRange, reqRange)
	if recRange.Code != http.StatusPartialContent || recRange.Header().Get("Content-Encoding") != "" || recRange.Body.Len() != 32 {
		t.Fatalf("Range GET /app.js failed: code=%d ce=%q len=%d", recRange.Code, recRange.Header().Get("Content-Encoding"), recRange.Body.Len())
	}

	// 6) Non-compressible binary assets are never gzipped; KaTeX fonts get immutable cache
	recFont := httptest.NewRecorder()
	reqFont := httptest.NewRequest(http.MethodGet, "/vendor/katex/fonts/KaTeX_Main-Regular.woff2", nil)
	reqFont.Header.Set("Accept-Encoding", "gzip")
	h.ServeHTTP(recFont, reqFont)
	if recFont.Code != http.StatusOK || recFont.Header().Get("Content-Encoding") != "" {
		t.Fatalf("woff2 should not be gzipped: code=%d ce=%q", recFont.Code, recFont.Header().Get("Content-Encoding"))
	}
	if cc := recFont.Header().Get("Cache-Control"); !strings.Contains(cc, "immutable") {
		t.Fatalf("woff2 Cache-Control = %q, want immutable", cc)
	}
}

func TestChatRenderPerfAndP0Contracts(t *testing.T) {
	t.Parallel()
	nodeBin, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node not installed; skipping JS chat render perf tests")
	}
	root := filepath.Join("..", "..")
	jsTest := `
const assert = require("node:assert/strict");
const fs = require("node:fs");
const path = require("node:path");
const vm = require("node:vm");

const root = process.argv[1];
const htmlSrc = fs.readFileSync(path.join(root, "web/index.html"), "utf8");
const demoHtmlSrc = fs.readFileSync(path.join(root, "docs/demo/index.html"), "utf8");
const demoAppSrc = fs.readFileSync(path.join(root, "docs/demo/app.js"), "utf8");
const cssSrc = fs.readFileSync(path.join(root, "web/app.css"), "utf8");
const appSrc = fs.readFileSync(path.join(root, "web/app.js"), "utf8");
const katexLib = require(path.join(root, "web/vendor/katex/katex.min.js"));

// ─── 1. Static HTML / CSS / Demo contracts ───
assert.ok(!htmlSrc.includes("/vendor/xterm"), "web/index.html must lazy-load xterm (0 first-screen tags)");
assert.ok(!htmlSrc.includes("/vendor/katex"), "web/index.html must lazy-load katex (0 first-screen tags)");
assert.ok(!demoHtmlSrc.includes("vendor/xterm") && !demoHtmlSrc.includes("vendor/katex"), "docs/demo/index.html must not eagerly load xterm/katex");
assert.ok(demoHtmlSrc.includes('<script src="demo.js"></script>\n<script src="app.js"></script>'), "docs/demo/index.html must load demo.js before app.js");
assert.ok(demoAppSrc.includes('"vendor/xterm.js"') && demoAppSrc.includes('"vendor/katex/katex.min.js"'), "docs/demo/app.js must rewrite lazy vendor paths to relative paths");
assert.ok(!/#super-thread\s*>\s*\*\s*\{[^}]*content-visibility:\s*auto/.test(cssSrc), "never apply content-visibility: auto to #super-thread direct rows");
assert.ok(!/\.code-tail\[hidden="until-found"\][^{]*\{[^}]*(?:display:\s*none|visibility:\s*hidden)/.test(cssSrc), "never apply display:none or visibility:hidden to [hidden=until-found]");
assert.ok(!cssSrc.includes(".md-code-head") && !cssSrc.includes(".md-table-bar"), "persistent gray top bars must be removed from code blocks and tables");
assert.ok(cssSrc.includes(".md-float-acts") && !/\.md-float-acts\s*\{[^}]*backdrop-filter:/.test(cssSrc), "floating micro-action pill must exist without backdrop-filter blur");

// ─── 2. Runtime VM Harness ───
function createHarness({ withKatex = false, mobile = false } = {}) {
  const noop = () => {};
  const headAdded = [];
  const docListeners = new Map();
  const ioInstances = [];
  const moInstances = [];
  const revokedUrls = [];
  let blobSeq = 0;

  function makeClassList(initial = "") {
    const set = new Set(initial.split(/\s+/).filter(Boolean));
    return {
      add: (...cs) => cs.forEach((c) => c && set.add(c)),
      remove: (...cs) => cs.forEach((c) => set.delete(c)),
      toggle: (c, f) => { const on = f === undefined ? !set.has(c) : !!f; if (on) set.add(c); else set.delete(c); return on; },
      contains: (c) => set.has(c),
      toString: () => [...set].join(" "),
    };
  }
  function makeEl(tag = "DIV") {
    const attrs = new Map();
    const listeners = new Map();
    const el = {
      nodeType: 1,
      tagName: tag.toUpperCase(),
      hidden: false,
      open: false,
      className: "",
      dataset: {},
      textContent: "",
      innerHTML: "",
      scrollLeft: 0,
      scrollWidth: 400,
      clientWidth: 400,
      offsetWidth: 100,
      isConnected: true,
      parentNode: null,
      parentElement: null,
      children: [],
      get childElementCount() { return this.children.length; },
      style: { setProperty: noop, removeProperty: noop, getPropertyValue: () => "" },
      classList: makeClassList(),
      setAttribute(k, v) { attrs.set(k, String(v)); if (k === "src") el.src = String(v); },
      getAttribute(k) { return attrs.has(k) ? attrs.get(k) : null; },
      removeAttribute(k) { attrs.delete(k); if (k === "src") delete el.src; },
      hasAttribute(k) { return attrs.has(k); },
      addEventListener(t, fn) { if (!listeners.has(t)) listeners.set(t, []); listeners.get(t).push(fn); },
      removeEventListener: noop,
      dispatch(t, ev = {}) { for (const fn of listeners.get(t) || []) fn({ target: el, ...ev }); },
      querySelector: () => makeEl("DIV"),
      querySelectorAll: () => [],
      appendChild(c) { c.parentNode = el; c.parentElement = el; el.children.push(c); return c; },
      replaceChild(n, o) { const i = el.children.indexOf(o); if (i >= 0) el.children[i] = n; n.parentNode = el; n.parentElement = el; return o; },
      replaceWith(n) { if (el.parentNode) el.parentNode.replaceChild(n, el); },
      append: noop,
      after: noop,
      closest: () => null,
      contains: () => false,
      getBoundingClientRect: () => ({ top: 2000, bottom: 2100, left: 0, right: 100, width: 100, height: 100 }),
      focus: noop,
      scrollIntoView: noop,
      options: [],
      selectedOptions: [],
    };
    return el;
  }

  const head = makeEl("HEAD");
  const origAppend = head.appendChild.bind(head);
  head.appendChild = (node) => {
    headAdded.push(node);
    return origAppend(node);
  };

  const doc = {
    head,
    documentElement: makeEl("HTML"),
    body: makeEl("BODY"),
    hidden: false,
    createElement: (tag) => makeEl(tag),
    getElementById: () => makeEl("DIV"),
    querySelector: (sel) => {
      if (sel.startsWith('link[data-lazy-css="')) {
        const key = sel.slice('link[data-lazy-css="'.length, -2);
        return headAdded.find((n) => n.tagName === "LINK" && n.dataset.lazyCss === key) || null;
      }
      if (sel.startsWith('script[data-lazy-js="')) {
        const key = sel.slice('script[data-lazy-js="'.length, -2);
        return headAdded.find((n) => n.tagName === "SCRIPT" && n.dataset.lazyJs === key) || null;
      }
      return makeEl("DIV");
    },
    querySelectorAll: () => [],
    addEventListener(t, fn) { if (!docListeners.has(t)) docListeners.set(t, []); docListeners.get(t).push(fn); },
    hasFocus: () => false,
  };

  const ctx = {
    console: { log: noop, info: noop, warn: noop, error: noop },
    document: doc,
    window: null,
    location: { hash: "", search: "", pathname: "/", origin: "http://127.0.0.1", hostname: "127.0.0.1" },
    history: { state: {}, replaceState: noop, back: noop },
    navigator: { userAgent: "NodeTest" },
    localStorage: { getItem: () => null, setItem: noop },
    matchMedia: (q) => ({ matches: mobile && q.includes("760px"), addEventListener: noop }),
    MutationObserver: class { constructor(cb) { this.cb = cb; moInstances.push(this); } observe() {} disconnect() {} },
    IntersectionObserver: class {
      constructor(cb, opts) { this.cb = cb; this.opts = opts; this.targets = new Set(); ioInstances.push(this); }
      observe(t) { this.targets.add(t); }
      unobserve(t) { this.targets.delete(t); }
      disconnect() { this.targets.clear(); }
    },
    EventSource: class { addEventListener() {} close() {} },
    CSS: { escape: (s) => String(s) },
    URL: Object.assign(URL, {
      createObjectURL: () => "blob:test/" + (++blobSeq),
      revokeObjectURL: (u) => revokedUrls.push(u),
    }),
    URLSearchParams,
    performance: { now: () => 0 },
    requestAnimationFrame: () => 0,
    setTimeout: () => 0,
    clearTimeout: noop,
    setInterval: () => 0,
    clearInterval: noop,
    addEventListener: noop,
    removeEventListener: noop,
    dispatchEvent: noop,
    Event,
    HTMLElement: { prototype: { onbeforematch: null } },
    HTMLDialogElement: { prototype: {} },
    getComputedStyle: () => ({ getPropertyValue: () => "" }),
    fetch: () => Promise.reject(new Error("no network")),
    innerWidth: mobile ? 390 : 1440,
    innerHeight: mobile ? 844 : 900,
  };
  ctx.window = ctx;
  ctx.globalThis = ctx;
  if (withKatex) ctx.katex = katexLib;

  const marker = "  boot();\n})();";
  assert.ok(appSrc.includes(marker), "expected boot() tail in web/app.js");
  const expose = [
    "  globalThis.__test = {",
    "    md, mdRender, renderCollieBody, imagesIn, plainLine,",
    "    mdCache, mdCacheWorkerKeys, mdCacheMathKeys, sup, state,",
    "    ensureXterm, ensureKaTeX, hydratePendingMath,",
    "    thumbs, evictThumbsLRU, observeThumbs, observeThumbImg,",
    "    richUiState, setRichUiState, getRichBlockKey, setCodeBlockExpanded,",
    "    extractCodeBlockText, collectGallery,",
    "  };",
    "})();",
  ].join("\n");
  vm.runInNewContext(appSrc.replace(marker, expose), ctx, { filename: "web/app.js" });
  return { T: ctx.__test, ctx, headAdded, ioInstances, moInstances, revokedUrls, makeEl };
}

// ─── 3. Lazy-loading: 0 requests for plain text; singleton on demand ───
(async () => {
  const { T, ctx, headAdded, ioInstances, moInstances, revokedUrls, makeEl } = createHarness({ withKatex: false });
  assert.equal(moInstances.length, 1, "only select MutationObserver should be registered on document.body");

  // Plain text triggers 0 xterm / katex injections
  const plainHtml = T.md("这是一段普通纯文本消息，包含 **粗体** 和 *斜体*。");
  assert.ok(plainHtml.includes("<em>斜体</em>"), "italic renders in plain text");
  assert.equal(headAdded.length, 0, "plain text must trigger 0 lazy script/css injections");

  // Pending math triggers ensureKaTeX singleton once and updates mdCache on load
  const mathSrc = "行内公式 $E = mc^2$";
  const pendingHtml = T.md(mathSrc);
  assert.ok(pendingHtml.includes("md-math-pending") && pendingHtml.includes("md-math-fallback"), "math without KaTeX emits pending fallback");
  assert.ok(T.mdCacheMathKeys.has(mathSrc), "pending math key tracked in mdCacheMathKeys");

  const mathNode = makeEl("SPAN");
  mathNode.dataset.tex = "E = mc^2";
  mathNode.dataset.display = "0";
  const parentWrap = makeEl("P");
  parentWrap.appendChild(mathNode);
  const rootBox = makeEl("DIV");
  rootBox.querySelectorAll = (sel) => (sel.includes("md-math-pending") ? [mathNode] : []);

  T.hydratePendingMath(rootBox);
  T.hydratePendingMath(rootBox); // second call reuses singleton
  const katexCss = headAdded.filter((n) => n.tagName === "LINK" && n.href === "/vendor/katex/katex.min.css");
  const katexJs = headAdded.filter((n) => n.tagName === "SCRIPT" && n.src === "/vendor/katex/katex.min.js");
  assert.equal(katexCss.length, 1, "katex.min.css injected exactly once");
  assert.equal(katexJs.length, 1, "katex.min.js injected exactly once");

  // Simulate katex script load
  ctx.katex = katexLib;
  katexJs[0].onload();
  await Promise.resolve();
  await Promise.resolve();
  assert.equal(T.mdCacheMathKeys.size, 0, "mdCacheMathKeys cleared after KaTeX hydration");
  assert.ok(T.mdCache.get(mathSrc).includes("katex"), "mdCache entry updated in place with rendered KaTeX HTML");

  // ensureXterm singleton check
  const p1 = T.ensureXterm();
  const p2 = T.ensureXterm();
  assert.equal(p1, p2, "ensureXterm returns singleton promise");
  const xtermJs = headAdded.filter((n) => n.tagName === "SCRIPT" && n.src === "/vendor/xterm.js");
  assert.equal(xtermJs.length, 1, "xterm.js injected exactly once");
  ctx.Terminal = function MockTerminal() {};
  xtermJs[0].onload();
  assert.equal(await p1, ctx.Terminal, "ensureXterm resolves to window.Terminal");

  // ─── 4. mdCache fine-grained invalidation (mdCacheWorkerKeys) ───
  T.mdCache.clear();
  T.mdCacheWorkerKeys.clear();
  T.sup.workers = [];
  T.state.agents = [];
  const kPlain = "无任务名的纯文本消息";
  const kAlpha = "已派发 " + "\x60worker-alpha\x60" + " 处理";
  const kBeta = "另外 " + "\x60worker-beta\x60" + " 待命";
  const hPlain1 = T.md(kPlain);
  const hAlpha1 = T.md(kAlpha);
  const hBeta1 = T.md(kBeta);
  assert.ok(!hAlpha1.includes("worker-link"), "before spawn worker-alpha is plain code");
  assert.ok(!T.mdCacheWorkerKeys.has(kPlain), "plain text without worker-like inline code is not tracked in mdCacheWorkerKeys");
  assert.ok(T.mdCacheWorkerKeys.has(kAlpha) && T.mdCacheWorkerKeys.has(kBeta), "entries with inline code tokens tracked");

  // Sentinel tag in mdCache to prove kPlain and kBeta are NEVER evicted when worker-alpha spawns
  T.mdCache.set(kPlain, "SENTINEL_PLAIN");
  T.mdCache.set(kBeta, "SENTINEL_BETA");
  T.sup.workers = [{ name: "worker-alpha", pane_id: "w9:p1", status: "working" }];
  const hAlpha2 = T.md(kAlpha);
  assert.ok(hAlpha2.includes('data-open-worker="worker-alpha"'), "worker-alpha entry invalidated and re-rendered as worker-link");
  assert.equal(T.mdCache.get(kPlain), "SENTINEL_PLAIN", "unrelated plain entry must NOT be evicted");
  assert.equal(T.mdCache.get(kBeta), "SENTINEL_BETA", "unrelated worker-beta entry must NOT be evicted");
  assert.equal(hPlain1.length > 0 && hBeta1.length > 0, true);

  // Removing worker-alpha invalidates only kAlpha
  T.sup.workers = [];
  const hAlpha3 = T.md(kAlpha);
  assert.ok(!hAlpha3.includes("worker-link"), "removed worker-alpha reverts to plain code");
  assert.equal(T.mdCache.get(kPlain), "SENTINEL_PLAIN", "unrelated entry still cached after worker removal");
  assert.equal(T.mdCache.get(kBeta), "SENTINEL_BETA", "other worker entry still cached after worker-alpha removal");

  // ─── 5. Thumb IntersectionObserver(400px) + LRU eviction (120 desktop / 60 mobile) ───
  const thumbBtnEl = makeEl("BUTTON");
  thumbBtnEl.classList.add("thumb");
  thumbBtnEl.dataset.img = "/tmp/lazy-shot.png";
  const thumbImgEl = makeEl("IMG");
  thumbBtnEl.appendChild(thumbImgEl);
  T.observeThumbImg(thumbImgEl);
  assert.equal(ioInstances.length >= 1, true, "IntersectionObserver created for lazy thumb");
  assert.equal(ioInstances[0].opts.rootMargin, "400px 0px", "IntersectionObserver uses 400px rootMargin");
  assert.equal(ioInstances[0].targets.has(thumbBtnEl), true, "unloaded thumb observes visible .thumb button container (not display:none img) with IO");

  // Verify IO callback resolves inner <img> cleanly when observed button intersects
  thumbBtnEl.querySelector = (sel) => (sel === "img" ? thumbImgEl : null);

  for (let i = 0; i < 130; i++) {
    T.thumbs.set("/tmp/pic-" + i + ".png", "blob:test/pic-" + i);
  }
  T.evictThumbsLRU();
  assert.equal(T.thumbs.size, 120, "desktop thumbs LRU caps at 120");
  assert.equal(revokedUrls.length, 10, "oldest 10 offscreen ObjectURLs revoked");
  assert.ok(!T.thumbs.has("/tmp/pic-0.png") && T.thumbs.has("/tmp/pic-129.png"), "LRU evicted oldest entries and kept newest");

  const mob = createHarness({ mobile: true });
  for (let i = 0; i < 75; i++) {
    mob.T.thumbs.set("/tmp/m-" + i + ".png", "blob:test/m-" + i);
  }
  mob.T.evictThumbsLRU();
  assert.equal(mob.T.thumbs.size, 60, "mobile thumbs LRU caps at 60");
  assert.equal(mob.revokedUrls.length, 15, "mobile LRU revoked 15 oldest offscreen URLs");

  // ─── 6. P0-1: Code Block 2.0 (headerless, quiet corner tag, floating copy, >18 fold to 14 + until-found, Diff) ───
  const shortCode = T.mdRender("\x60\x60\x60go main.go\npackage main\nfunc main() {}\n\x60\x60\x60");
  assert.ok(shortCode.includes('class="md-code"') && !shortCode.includes("md-code-head"), "short code renders headerless card without persistent gray bar");
  assert.ok(shortCode.includes('class="md-code-tag"') && shortCode.includes('<span class="md-code-lang">Go</span>'), "quiet corner language label rendered");
  assert.ok(shortCode.includes('class="md-float-acts md-code-acts"') && shortCode.includes('data-copy-code'), "floating copy button rendered");
  assert.ok(!shortCode.includes("data-code-fold"), "short block <=18 lines is not collapsed");
  const plainCode = T.mdRender("\x60\x60\x60\nplain output\n\x60\x60\x60");
  assert.ok(!plainCode.includes('class="md-code-tag"') && plainCode.includes('data-copy-code'), "unlabeled code omits resting corner tag while keeping hover copy");

  const longLines = ["/* multi-line comment"];
  for (let i = 2; i <= 21; i++) longLines.push("line " + i);
  longLines.push("end comment */");
  const longCode = T.mdRender("\x60\x60\x60js\n" + longLines.join("\n") + "\n\x60\x60\x60");
  assert.ok(longCode.includes('class="md-code is-collapsed"'), ">18-line code block collapsed by default");
  assert.ok(longCode.includes('data-total-lines="22"') && longCode.includes('data-head-lines="14"'), "tracks 22 total / 14 head lines");
  assert.ok(longCode.includes('class="code-tail" hidden="until-found"'), "collapsed tail uses hidden=until-found");
  assert.ok(longCode.includes("展开剩余 8 行"), "fold button shows remaining 8 lines");
  // Verify balanced <span> tags inside head and tail across multi-line comment split
  const headMatch = longCode.match(/<span class="code-head-part">([\s\S]*?)<\/span><span class="code-tail"/);
  assert.ok(headMatch, "head/tail split markup present");
  const headOpens = (headMatch[1].match(/<span\b/g) || []).length;
  const headCloses = (headMatch[1].match(/<\/span>/g) || []).length;
  assert.equal(headOpens, headCloses, "highlighted span tags stay balanced in head part");

  const diffMd = [
    "\x60\x60\x60diff internal/collie/server.go",
    "@@ -10,3 +10,5 @@ func static() {",
    " context line",
    "-old line 1",
    "+new line 1",
    "+new line 2",
    "+new line 3",
    "\x60\x60\x60",
  ].join("\n");
  const diffHtml = T.mdRender(diffMd);
  assert.ok(diffHtml.includes("md-code is-diff") && !diffHtml.includes("md-code-head"), "diff block gets is-diff class without top bar");
  assert.ok(diffHtml.includes('class="md-code-tag"') && diffHtml.includes('<span class="add">+3</span><span class="del">−1</span>'), "diff quiet corner tag displays +3 −1 stats");
  assert.ok(diffHtml.includes("diff-line is-hunk") && diffHtml.includes("diff-line is-add") && diffHtml.includes("diff-line is-del"), "diff lines get full-row semantics");

  // ─── 7. P0-2: Adaptive Multi-Image Gallery (1/2/3/4/5-6/6+ & merge with imagesIn) ───
  const g1 = T.mdRender("![one](/tmp/1.png)");
  assert.ok(g1.includes('class="md-gallery is-1" data-count="1"'), "1 image -> is-1");
  const g2 = T.mdRender("![a](/tmp/1.png)\n![b](/tmp/2.png)");
  assert.ok(g2.includes('class="md-gallery is-2" data-count="2"'), "2 consecutive images -> is-2");
  const g4 = T.mdRender("![a](/tmp/1.png)\n\n![b](/tmp/2.png)\n![c](/tmp/3.png)\n![d](/tmp/4.png)");
  assert.ok(g4.includes('class="md-gallery is-4" data-count="4"'), "4 images across blank line merge into is-4");
  const g7 = T.mdRender([1,2,3,4,5,6,7].map((i) => "![s" + i + "](https://example.com/" + i + ".png)").join("\n"));
  assert.ok(g7.includes('class="md-gallery is-many" data-count="7"'), "7 images -> is-many");
  assert.equal((g7.match(/class="gal-tile /g) || []).length, 6, "6+ gallery renders 6 visible tiles");
  assert.ok(g7.includes('<span class="gal-more" aria-hidden="true">+1</span>'), "6th tile shows +1 overflow count");
  assert.equal((g7.match(/class="gal-hidden-item"/g) || []).length, 1, "7th image preserved as hidden gallery item for lightbox");

  // Merge Markdown images + trailing prose path images into ONE deduplicated .md-gallery
  const merged = T.renderCollieBody("请看对比 /tmp/a.png 和 /tmp/b.png\n\n![图2](/tmp/b.png)\n![图3](/tmp/c.png)");
  assert.equal((merged.match(/class="md-gallery /g) || []).length, 1, "Markdown images and trailing path images merge into single gallery");
  assert.ok(merged.includes('class="md-gallery is-3" data-count="3"'), "merged gallery deduplicates /tmp/b.png -> 3 images");

  // ─── 8. P0-3: Enhanced Markdown Table (headerless + hover floating TSV/MD pill) ───
  const tblHtml = T.mdRender([
    "| 指标 | 现状 | 优化后 | 降幅 |",
    "| --- | --- | --- | --- |",
    "| 首屏传输 | 2,092.5 KB | 369.7 KB | -82.3% |",
    "| 长列表 | 142ms | 13.7s | +5.5% |",
  ].join("\n"));
  assert.ok(tblHtml.includes('class="md-table-card"') && !tblHtml.includes("md-table-bar") && !tblHtml.includes("md-table-meta"), "table card omits persistent top bar and dimension strip");
  assert.ok(tblHtml.includes('class="md-float-acts md-table-acts"') && tblHtml.includes('data-copy-table="tsv"') && tblHtml.includes('data-copy-table="md"'), "table card renders floating TSV and MD copy buttons");
  assert.ok(tblHtml.includes('class="md-table" tabindex="0" role="region"'), "table keeps accessible .md-table scroll region");
  assert.ok(tblHtml.includes('<th scope="col"') && tblHtml.includes('<th scope="row"'), "table uses semantic col and row headers for sticky first column");
  assert.ok(tblHtml.includes('class="is-num"'), "numeric/percentage/duration cells auto-tagged with is-num");

  // ─── 9. P0-4: GFM Semantics & Strict <details> XSS Protection ───
  const gfmInline = T.mdRender("保留 foo_bar_baz 与 __init__ 与 ~/a_b_c/d_e.go，渲染 *斜体*、_下划线斜体_、***粗斜*** 和 ~~删除线~~。");
  assert.ok(gfmInline.includes("foo_bar_baz") && !gfmInline.includes("foo<em>bar</em>baz"), "foo_bar_baz must NOT be turned into italic");
  assert.ok(gfmInline.includes("__init__") && gfmInline.includes("<em>斜体</em>") && gfmInline.includes("<em>下划线斜体</em>") && gfmInline.includes("<strong><em>粗斜</em></strong>") && gfmInline.includes("<del>删除线</del>"), "italic, bold-italic, and strikethrough render cleanly");

  // Merged adjacent blockquotes
  const mq = T.mdRender("> 第一行引用\n> 第二行引用\n>\n> 第二段引用");
  assert.equal((mq.match(/<blockquote>/g) || []).length, 1, "adjacent > lines merge into one blockquote");
  assert.ok(mq.includes("<p>第一行引用<br>第二行引用</p><p>第二段引用</p>"), "blockquote groups paragraphs cleanly");

  // 4-tier Callouts
  for (const [tag, cls, lbl] of [["NOTE","is-note","说明"],["TIP","is-tip","提示"],["WARNING","is-warn","注意"],["CAUTION","is-caution","风险"]]) {
    const co = T.mdRender("> [!" + tag + "] 标题\n> 正文内容");
    assert.ok(co.includes('class="md-callout ' + cls + '"') && co.includes(lbl + " · 标题") && co.includes("正文内容"), "callout " + tag + " renders properly");
  }

  // Read-only Task List
  const tasks = T.mdRender("- [x] 已完成项\n- [ ] 待处理项");
  assert.ok(tasks.includes('class="md-task-list"') && tasks.includes('class="md-task-item is-done"') && tasks.includes('class="md-task-item"'), "task list renders done and todo items");
  assert.ok(!tasks.includes("<input"), "task list is read-only without fake interactive checkboxes");

  // Safe whitelist <details>/<summary> with inner Markdown & zero headings in summary
  const safeDet = T.mdRender([
    "<details>",
    "<summary>### 查看详细日志</summary>",
    "",
    "- [x] 核对项 1",
    "",
    "\x60\x60\x60sh",
    "echo ok",
    "\x60\x60\x60",
    "</details>",
  ].join("\n"));
  assert.ok(safeDet.includes('<details class="md-fold"><summary>查看详细日志</summary>'), "whitelisted details renders with stripped heading prefix in summary");
  assert.ok(!/<summary>[^<]*<h[1-6]/i.test(safeDet), "summary never contains heading elements");
  assert.ok(safeDet.includes('class="md-task-list"') && safeDet.includes('class="md-code"'), "inner Markdown renders inside details body");

  // XSS payloads in <details>/<summary> and body MUST be completely neutralized
  const xssPayloads = [
    '<details onclick="alert(1)" ontoggle="alert(2)"><summary onmouseover="alert(3)">x</summary><script>alert(4)</script></details>',
    '<details>\n<summary onclick="alert(1)">Title</summary>\n<img src=x onerror="alert(2)">\n</details>',
    '<details>\n<summary>Safe</summary>\n<script>alert(1)</script><img src=x onerror=alert(2)><svg onload=alert(3)><a href="javascript:alert(4)">bad</a>\n</details>',
  ];
  const xssOuts = xssPayloads.map((payload) => T.mdRender(payload));
  assert.ok(!xssOuts[0].includes("<details"), "XSS: details tag with extra attributes must NOT open real <details>: " + xssOuts[0]);
  assert.ok(!xssOuts[1].includes("<summary onclick"), "XSS: summary tag with attributes must stay escaped: " + xssOuts[1]);
  assert.ok(xssOuts[2].includes('<details class="md-fold"><summary>Safe</summary>'), "whitelisted details renders safe shell while escaping inner HTML");
  for (const out of xssOuts) {
    assert.ok(!/<script\b/i.test(out), "XSS: no <script> tag allowed: " + out);
    assert.ok(!/<img\b/i.test(out), "XSS: no raw <img> tag allowed: " + out);
    assert.ok(!/<svg\b[^>]*onload/i.test(out), "XSS: no <svg onload> allowed: " + out);
    assert.ok(!/<[^>]*\bon(?:click|toggle|mouseover|error|load)\s*=/i.test(out), "XSS: no inline event handler attribute in any HTML tag: " + out);
    assert.ok(!/<a\b[^>]*href\s*=\s*["']javascript:/i.test(out), "XSS: no javascript: href allowed: " + out);
  }

  // Transient UI state store check
  T.setRichUiState("e42|code|0", true);
  T.setRichUiState("e42|details|0", true);
  T.setRichUiState("e42|table|0", 128);
  assert.equal(T.richUiState.get("e42|code|0"), true);
  assert.equal(T.richUiState.get("e42|details|0"), true);
  assert.equal(T.richUiState.get("e42|table|0"), 128);

  console.log("ALL_CHAT_RENDER_PERF_TESTS_PASSED");
})().catch((err) => {
  console.error(err);
  process.exit(1);
});
`
	cmd := exec.Command(nodeBin, "-e", jsTest, root)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("node chat render perf test failed: %v\n%s", err, string(out))
	}
	if !strings.Contains(string(out), "ALL_CHAT_RENDER_PERF_TESTS_PASSED") {
		t.Fatalf("unexpected node test output:\n%s", string(out))
	}
}
