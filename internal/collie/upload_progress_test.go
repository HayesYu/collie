package collie

import (
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
)

func TestWebUploadProgressXHRAndUI(t *testing.T) {
	t.Parallel()
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node not installed")
	}
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("cannot resolve test path")
	}
	repoRoot := filepath.Clean(filepath.Join(filepath.Dir(thisFile), "..", ".."))
	appPath := filepath.Join(repoRoot, "web", "app.js")
	i18nPath := filepath.Join(repoRoot, "web", "i18n.js")
	cssPath := filepath.Join(repoRoot, "web", "app.css")
	demoJsPath := filepath.Join(repoRoot, "docs", "demo", "demo.js")

	script := `
const fs = require("fs");
const vm = require("vm");
const assert = require("assert");

const appSrc = fs.readFileSync(process.argv[1], "utf8");
const i18nSrc = fs.readFileSync(process.argv[2], "utf8");
const cssSrc = fs.readFileSync(process.argv[3], "utf8");
const demoJsSrc = fs.readFileSync(process.argv[4], "utf8");

// ─── 1. CSS GPU transform & accessibility contracts ───
assert.ok(cssSrc.includes(".file {\n  position: relative; overflow: clip;"), ".file must use position:relative + overflow:clip for bottom 2px bar");
assert.ok(cssSrc.includes(".file .fs { color: var(--fg-3); flex: none; font-size: 11.5px; font-variant-numeric: tabular-nums; }"), ".file .fs must enforce tabular-nums");
assert.ok(cssSrc.includes(".file .fi-prog {") && cssSrc.includes("height: 2px;"), "2px bottom progress track must exist");
assert.ok(cssSrc.includes(".file .fi-prog-fill {") && cssSrc.includes("transform: scaleX(0);") && cssSrc.includes("will-change: transform;"), "progress fill must animate via pure GPU scaleX()");
assert.ok(cssSrc.includes("@keyframes fi-prog-indet") && cssSrc.includes("translateX(-35%) scaleX(.32)"), "indeterminate animation must use GPU transform only");
assert.ok(cssSrc.includes("@media (prefers-reduced-motion: reduce)") && cssSrc.includes(".file .fi-prog.is-proc .fi-prog-fill"), "reduced-motion fallback must be defined");
assert.ok(demoJsSrc.includes("DemoXMLHttpRequest") && demoJsSrc.includes('"/api/uploads"'), "docs/demo/demo.js must simulate XHR upload progress");

function makeClassList(initial = []) {
  const s = new Set(initial);
  return {
    add: (...xs) => xs.forEach((x) => s.add(x)),
    remove: (...xs) => xs.forEach((x) => s.delete(x)),
    toggle: (x, f) => (f === undefined ? (s.has(x) ? (s.delete(x), false) : (s.add(x), true)) : (f ? s.add(x) : s.delete(x), !!f)),
    contains: (x) => s.has(x),
  };
}

function makeEl(id = "") {
  const listeners = new Map();
  const captureListeners = new Map();
  const attrs = new Map();
  const children = [];
  let html = "";
  let htmlWrites = 0;
  const chipNodes = new Map();

  function parseChips(markup) {
    chipNodes.clear();
    const chipRe = /<span class="([^"]*\bfile\b[^"]*)" data-fid="([^"]+)" title="([^"]*)"[^>]*>([\s\S]*?)<\/span>(?=<span class="[^"]*\bfile\b|$)/g;
    let m;
    while ((m = chipRe.exec(markup))) {
      const [, clsStr, fid, title, body] = m;
      const fsMatch = body.match(/<span class="fs">([^<]*)<\/span>/);
      const progMatch = body.match(/<span class="([^"]*\bfi-prog\b[^"]*)"([^>]*)><span class="fi-prog-fill"(?: style="transform:([^"]*)")?><\/span><\/span>/);
      const chip = makeEl("chip:" + fid);
      chip.dataset = { fid };
      chip.title = title;
      chip.classList = makeClassList(clsStr.split(/\s+/).filter(Boolean));
      const fsEl = makeEl("fs:" + fid);
      fsEl.textContent = fsMatch ? fsMatch[1] : "";
      chip._qs[".fs"] = fsEl;
      if (progMatch) {
        const progEl = makeEl("prog:" + fid);
        progEl.classList = makeClassList(progMatch[1].split(/\s+/).filter(Boolean));
        const valM = progMatch[2].match(/aria-valuenow="([^"]*)"/);
        if (valM) progEl.setAttribute("aria-valuenow", valM[1]);
        const lblM = progMatch[2].match(/aria-label="([^"]*)"/);
        if (lblM) progEl.setAttribute("aria-label", lblM[1]);
        const fillEl = makeEl("fill:" + fid);
        fillEl.style = { transform: progMatch[3] || "", setProperty() {}, removeProperty() {} };
        chip._qs[".fi-prog"] = progEl;
        chip._qs[".fi-prog-fill"] = fillEl;
      } else {
        chip._qs[".fi-prog"] = null;
        chip._qs[".fi-prog-fill"] = null;
      }
      chipNodes.set(fid, chip);
    }
  }

  const el = {
    id,
    tagName: id.toUpperCase(),
    value: "",
    textContent: "",
    get innerHTML() { return html; },
    set innerHTML(v) {
      html = String(v ?? "");
      htmlWrites++;
      if (id.endsWith(" .files")) parseChips(html);
    },
    get _htmlWrites() { return htmlWrites; },
    _chipNodes: chipNodes,
    hidden: false,
    disabled: false,
    style: { transform: "", setProperty() {}, removeProperty() {} },
    dataset: {},
    className: "",
    classList: makeClassList(),
    options: [],
    selectedOptions: [],
    parentElement: null,
    children,
    _qs: {},
    _popoverOpen: false,
    get firstElementChild() { return children[0] || null; },
    addEventListener(type, fn, opts) {
      const isCap = typeof opts === "boolean" ? opts : !!opts?.capture;
      const m = isCap ? captureListeners : listeners;
      if (!m.has(type)) m.set(type, []);
      m.get(type).push(fn);
    },
    removeEventListener(type, fn, opts) {
      const isCap = typeof opts === "boolean" ? opts : !!opts?.capture;
      const m = isCap ? captureListeners : listeners;
      m.set(type, (m.get(type) || []).filter((x) => x !== fn));
    },
    dispatch(type, ev = {}, capture = false) {
      const event = {
        type,
        defaultPrevented: false,
        preventDefault() { this.defaultPrevented = true; },
        stopPropagation() {},
        composedPath() { return [el]; },
        target: el,
        ...ev,
      };
      const m = capture ? captureListeners : listeners;
      for (const fn of m.get(type) || []) fn(event);
      return event;
    },
    requestSubmit() { return el.dispatch("submit"); },
    querySelector(sel) {
      const fidM = typeof sel === "string" && sel.match(/^\.file\[data-fid="([^"]+)"\]$/);
      if (fidM) return chipNodes.get(fidM[1]) || null;
      if (Object.prototype.hasOwnProperty.call(el._qs, sel)) return el._qs[sel];
      const child = makeEl(id + ":" + sel);
      el._qs[sel] = child;
      return child;
    },
    querySelectorAll(sel) { return el._qsa?.[sel] || []; },
    setAttribute(k, v) { attrs.set(k, String(v)); },
    getAttribute(k) { return attrs.get(k) ?? ""; },
    removeAttribute(k) { attrs.delete(k); },
    hasAttribute(k) { return attrs.has(k); },
    closest() { return null; },
    contains(n) { return children.includes(n); },
    matches(sel) { return sel === ":popover-open" ? el._popoverOpen : false; },
    showPopover() { el._popoverOpen = true; },
    hidePopover() { el._popoverOpen = false; },
    append(...nodes) {
      for (const n of nodes) {
        if (!n || typeof n !== "object") continue;
        n.parentElement = el;
        children.push(n);
      }
    },
    remove() {
      if (el.parentElement) {
        const idx = el.parentElement.children.indexOf(el);
        if (idx >= 0) el.parentElement.children.splice(idx, 1);
        el.parentElement = null;
      }
    },
    after() {},
    focus() {},
    click() {},
    scrollIntoView() {},
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

for (const [formSel, inputSel, sendSel] of [
  ["#super-composer", "#super-input", null],
  ["#composer", "#input", null],
  ["#x-composer", "#x-input", null],
  ["#td-composer", "#td-input", "#td-send"],
  ["#wd-composer", "#wd-input", "#wd-send"],
]) {
  const f = getEl(formSel);
  const inp = getEl(inputSel);
  f._qs["textarea"] = inp;
  f._qs[".send"] = sendSel ? getEl(sendSel) : makeEl(formSel + " .send");
  f._qs[".files"] = makeEl(formSel + " .files");
  f._qs[".attach"] = makeEl(formSel + " .attach");
  f._qs['input[type="file"]'] = makeEl(formSel + " input[type=file]");
}

const toastLog = [];
const toastHost = getEl("#toasts");
const origAppend = toastHost.append.bind(toastHost);
toastHost.append = (...nodes) => {
  origAppend(...nodes);
  for (const n of nodes) toastLog.push({ msg: n.dataset?.msg || n.textContent, cls: n.className });
};

const revoked = [];
let urlSeq = 0;
class FakeURL extends URL {
  static createObjectURL() { return "blob:xhr-test/" + (++urlSeq); }
  static revokeObjectURL(u) { revoked.push(u); }
}

// Tracked Mock XMLHttpRequest
const xhrInstances = [];
class MockXHR {
  constructor() {
    this.method = "";
    this.url = "";
    this.async = true;
    this.withCredentials = false;
    this.headers = {};
    this.body = null;
    this.status = 0;
    this.statusText = "";
    this.responseText = "";
    this.aborted = false;
    this.abortCalls = 0;
    this.upload = { onprogress: null, onload: null };
    this.onload = null;
    this.onerror = null;
    this.onabort = null;
    this.ontimeout = null;
    xhrInstances.push(this);
  }
  open(method, url, async = true) {
    this.method = method;
    this.url = String(url);
    this.async = async;
  }
  setRequestHeader(k, v) {
    this.headers[k] = v;
  }
  send(body) {
    this.body = body;
  }
  abort() {
    this.aborted = true;
    this.abortCalls++;
    this.onabort?.();
  }
  emitProgress(loaded, total, lengthComputable = true) {
    this.upload.onprogress?.({ loaded, total, lengthComputable });
  }
  emitUploadLoad() {
    this.upload.onload?.();
  }
  respond(status, bodyObj, statusText = "OK") {
    this.status = status;
    this.statusText = statusText;
    this.responseText = typeof bodyObj === "string" ? bodyObj : JSON.stringify(bodyObj);
    this.onload?.();
  }
  failNetwork() {
    this.onerror?.();
  }
  failTimeout() {
    this.ontimeout?.();
  }
}

const fetchCalls = [];
let loginOk = true;
const storage = new Map([["collie.authToken", "saved-tok"]]);

const doc = {
  documentElement: makeEl("html"),
  body: makeEl("body"),
  hidden: false,
  activeElement: null,
  createElement: (tag) => makeEl(tag),
  getElementById: (id) => getEl("#" + id),
  querySelector: (sel) => (sel === "dialog[open]" ? null : getEl(sel)),
  querySelectorAll: () => [],
  addEventListener() {},
  hasFocus: () => false,
  ariaNotify() {},
};

const ctx = {
  console: { log() {}, info() {}, warn() {}, error() {} },
  document: doc,
  window: null,
  location: { hash: "#/collie", search: "", pathname: "/", origin: "http://127.0.0.1", hostname: "127.0.0.1" },
  history: { state: {}, replaceState() {}, back() {} },
  navigator: { userAgent: "NodeTest", language: "zh-CN", languages: ["zh-CN"], onLine: true },
  localStorage: {
    getItem: (k) => (storage.has(k) ? storage.get(k) : null),
    setItem: (k, v) => storage.set(k, String(v)),
    removeItem: (k) => storage.delete(k),
  },
  matchMedia: () => ({ matches: false, addEventListener() {} }),
  MutationObserver: class { observe() {} },
  EventSource: class { addEventListener() {} close() {} },
  XMLHttpRequest: MockXHR,
  CSS: { escape: (s) => String(s) },
  URL: FakeURL,
  URLSearchParams,
  Blob,
  Uint8Array,
  TextEncoder,
  atob,
  AbortController,
  crypto: { randomUUID: () => "uuid-prog-1" },
  performance: { now: () => 0 },
  requestAnimationFrame: () => 0,
  cancelAnimationFrame() {},
  setTimeout: () => 1,
  clearTimeout() {},
  setInterval: () => 0,
  clearInterval() {},
  addEventListener() {},
  removeEventListener() {},
  dispatchEvent() {},
  Event,
  HTMLDialogElement: { prototype: {} },
  getComputedStyle: () => ({ getPropertyValue: () => "" }),
  innerWidth: 1440,
  innerHeight: 900,
};
ctx.fetch = async (url, init = {}) => {
  const u = String(url);
  fetchCalls.push({ url: u, method: init.method || "GET", body: init.body });
  if (u === "/api/login") {
    return { ok: loginOk, status: loginOk ? 200 : 401, headers: { get: () => "" }, json: async () => ({ ok: loginOk }) };
  }
  return { ok: true, status: 200, headers: { get: () => "" }, json: async () => ({ ok: true, entry: { id: "e9", role: "user", text: "ok", at: new Date().toISOString() } }) };
};
ctx.window = ctx;
ctx.globalThis = ctx;

// Load i18n first so bilingual tx() works identically to browser
vm.runInNewContext(i18nSrc, ctx, { filename: "web/i18n.js" });

const marker = "  boot();\n})();";
assert.ok(appSrc.includes(marker));
const instrumented = appSrc.replace(marker, "  globalThis.__test = { setHubs(h){ hubs = h; }, setWdPane(id){ wdPaneId = id; } };\n})();");
vm.runInNewContext(instrumented, ctx, { filename: "web/app.js" });

const flush = () => new Promise((r) => setImmediate(r));
const settle = async (n = 8) => { for (let i = 0; i < n; i++) await flush(); };

function makeFile(name, sizeBytes = 4096, type = "video/mp4") {
  const bytes = new Uint8Array(Math.min(sizeBytes, 256));
  return {
    name,
    type,
    size: sizeBytes,
    lastModified: Date.now(),
    async arrayBuffer() { return bytes.buffer.slice(0); },
  };
}

(async () => {
  const form = getEl("#super-composer");
  const input = getEl("#super-input");
  const sendBtn = form._qs[".send"];
  const filesBox = form._qs[".files"];
  const picker = form._qs['input[type="file"]'];

  // ─── 2. Real-time XHR progress (0% -> 18% -> 45% -> 99% -> 处理中… -> ready) with ZERO innerHTML rebuilds ───
  xhrInstances.length = 0;
  toastLog.length = 0;
  fetchCalls.length = 0;
  picker.files = [makeFile("demo-clip.mp4", 1024 * 1024, "video/mp4")];
  picker.dispatch("change");
  await settle(4);

  assert.equal(xhrInstances.length, 1, "upload uses XMLHttpRequest");
  const x1 = xhrInstances[0];
  assert.equal(x1.method, "POST");
  assert.ok(x1.url.startsWith("/api/uploads?name=demo-clip.mp4"), "XHR URL preserves /api/uploads contract: " + x1.url);
  assert.equal(x1.withCredentials, true, "local hub sends same-origin credentials");
  assert.equal(form._files.length, 1);
  const f1 = form._files[0];
  assert.equal(f1.uploading, true);
  assert.equal(sendBtn.classList.contains("is-wait-upload"), true, "empty text + uploading marks send button is-wait-upload");

  const chip1 = filesBox._chipNodes.get(f1.id);
  assert.ok(chip1, "uploading chip rendered with data-fid");
  assert.ok(chip1._qs[".fi-prog"], "uploading chip renders .fi-prog track");
  assert.ok(chip1._qs[".fi-prog"].classList.contains("is-indet"), "starts indeterminate before first progress event");
  const writesBeforeProgress = filesBox._htmlWrites;

  // Empty submit during upload must be blocked with clear toast & keep chip visible
  input.value = "";
  form.requestSubmit();
  await settle(2);
  assert.equal(form._files.length, 1, "empty submit while uploading must keep progress chip visible");
  assert.equal(fetchCalls.filter((c) => c.url === "/api/super/say").length, 0, "must not send empty message while uploading");
  assert.ok(toastLog.some((t) => t.msg.includes("附件还在上传中")), "must toast clear wait hint: " + JSON.stringify(toastLog));

  // Progress 18% -> 45% -> 99% updates in-place with GPU scaleX() and ZERO innerHTML rebuilds
  x1.emitProgress(180, 1000);
  assert.equal(filesBox._htmlWrites, writesBeforeProgress, "18% tick must NOT rewrite box.innerHTML");
  assert.equal(chip1._qs[".fs"].textContent, "18%");
  assert.equal(chip1._qs[".fi-prog-fill"].style.transform, "scaleX(0.18)");
  assert.equal(chip1._qs[".fi-prog"].getAttribute("aria-valuenow"), "18");
  assert.equal(chip1._qs[".fi-prog"].classList.contains("is-indet"), false);

  x1.emitProgress(450, 1000);
  assert.equal(filesBox._htmlWrites, writesBeforeProgress, "45% tick must NOT rewrite box.innerHTML");
  assert.equal(chip1._qs[".fs"].textContent, "45%");
  assert.equal(chip1._qs[".fi-prog-fill"].style.transform, "scaleX(0.45)");
  assert.equal(chip1._qs[".fi-prog"].getAttribute("aria-valuenow"), "45");

  x1.emitProgress(996, 1000);
  assert.equal(chip1._qs[".fs"].textContent, "99%", "must clamp <100% upload bytes to 99%");

  // 100% uploaded, waiting for server disk save -> "处理中…" + scaleX(1) + .is-proc
  x1.emitProgress(1000, 1000);
  assert.equal(filesBox._htmlWrites, writesBeforeProgress, "100% processing tick must NOT rewrite box.innerHTML");
  assert.equal(chip1._qs[".fs"].textContent, "处理中…");
  assert.equal(chip1._qs[".fi-prog-fill"].style.transform, "scaleX(1)");
  assert.equal(chip1._qs[".fi-prog"].classList.contains("is-proc"), true);
  assert.equal(chip1.classList.contains("is-proc"), true);

  // Server responds HTTP 200 -> seamless switch to ready chip (no .fi-prog, shows size, clears is-wait-upload)
  x1.respond(200, { path: "/tmp/uploads/001-demo-clip.mp4", name: "demo-clip.mp4", size: 1024 * 1024 });
  await settle(4);
  assert.equal(f1.uploading, false);
  assert.equal(f1.path, "/tmp/uploads/001-demo-clip.mp4");
  assert.equal(sendBtn.classList.contains("is-wait-upload"), false, "is-wait-upload cleared when ready");
  assert.ok(!filesBox.innerHTML.includes("fi-prog"), "ready chip removes progress bar");
  assert.ok(filesBox.innerHTML.includes("1.0 MB"), "ready chip displays formatted file size: " + filesBox.innerHTML);
  assert.ok(filesBox.innerHTML.includes('title="移除"'), "ready chip remove button reverts title to 移除");

  // Now empty-text submit with ready attachment succeeds!
  form.requestSubmit();
  await settle(4);
  const sayAfterReady = fetchCalls.filter((c) => c.url === "/api/super/say");
  assert.equal(sayAfterReady.length, 1, "submitting after upload finishes sends cleanly");
  assert.ok(JSON.parse(sayAfterReady[0].body).text.includes("/tmp/uploads/001-demo-clip.mp4"));

  // ─── 3. Cancel upload mid-flight via × calls xhr.abort() with 0 error toasts ───
  xhrInstances.length = 0;
  toastLog.length = 0;
  revoked.length = 0;
  picker.files = [makeFile("abort-me.mov", 5 * 1024 * 1024, "video/quicktime")];
  picker.dispatch("change");
  await settle(4);
  assert.equal(xhrInstances.length, 1);
  const xAbort = xhrInstances[0];
  xAbort.emitProgress(320, 1000);
  assert.ok(filesBox.innerHTML.includes('title="取消上传"'), "uploading chip × button exposes 取消上传 title");

  const rmEl = { dataset: { rm: "0" }, closest: (s) => (s === "[data-rm]" ? rmEl : null) };
  filesBox.dispatch("click", { target: rmEl });
  await settle(4);
  assert.equal(xAbort.aborted, true, "clicking × while uploading must call xhr.abort()");
  assert.equal(xAbort.abortCalls, 1);
  assert.equal(form._files.length, 0, "cancelled chip removed immediately");
  assert.equal(toastLog.length, 0, "aborting upload must not emit error toast: " + JSON.stringify(toastLog));
  assert.equal(sendBtn.classList.contains("is-wait-upload"), false);

  // ─── 4. Failure states (network error, timeout, 413) + retry resets progress bar ───
  xhrInstances.length = 0;
  toastLog.length = 0;
  picker.files = [makeFile("flaky.mp4", 2048, "video/mp4")];
  picker.dispatch("change");
  await settle(4);
  const xErr = xhrInstances[0];
  xErr.emitProgress(550, 1000);
  xErr.failNetwork();
  await settle(4);
  assert.equal(form._files[0].uploading, false);
  assert.ok(form._files[0].err.includes("网络中断"), "network error translated cleanly: " + form._files[0].err);
  assert.ok(!filesBox.innerHTML.includes("fi-prog"), "errored chip removes progress bar");
  assert.ok(filesBox.innerHTML.includes('data-retry="0"'), "network error shows retry button");

  // Retry reuses preserved Blob, restarts XHR, shows live progress, then completes
  const retryEl = { dataset: { retry: "0" }, closest: (s) => (s === "[data-retry]" ? retryEl : null) };
  filesBox.dispatch("click", { target: retryEl });
  await settle(4);
  assert.equal(xhrInstances.length, 2, "retry triggers new XHR upload");
  const xRetry = xhrInstances[1];
  const retryChip = filesBox._chipNodes.get(form._files[0].id);
  xRetry.emitProgress(700, 1000);
  assert.equal(retryChip._qs[".fs"].textContent, "70%");
  assert.equal(retryChip._qs[".fi-prog-fill"].style.transform, "scaleX(0.7)");
  xRetry.emitUploadLoad();
  assert.equal(retryChip._qs[".fs"].textContent, "处理中…");
  xRetry.respond(200, { path: "/tmp/uploads/002-flaky.mp4", name: "flaky.mp4", size: 2048 });
  await settle(4);
  assert.equal(form._files[0].err, "");
  assert.equal(form._files[0].path, "/tmp/uploads/002-flaky.mp4");
  form._clearFiles();

  // Timeout error -> translated & retryable
  xhrInstances.length = 0;
  picker.files = [makeFile("slow.bin", 2048, "application/octet-stream")];
  picker.dispatch("change");
  await settle(4);
  xhrInstances[0].failTimeout();
  await settle(4);
  assert.ok(form._files[0].err.includes("超时"), "timeout translated: " + form._files[0].err);
  assert.equal(form._files[0].retryable, true);
  form._clearFiles();

  // 413 error -> translated & NOT retryable
  xhrInstances.length = 0;
  picker.files = [makeFile("server-413.bin", 2048, "application/octet-stream")];
  picker.dispatch("change");
  await settle(4);
  xhrInstances[0].respond(413, { error: "too large" }, "Payload Too Large");
  await settle(4);
  assert.ok(form._files[0].err.includes("25 MB"));
  assert.equal(form._files[0].retryable, false);
  form._clearFiles();

  // ─── 5. 401 triggers restoreSession() and retries XHR upload automatically ───
  xhrInstances.length = 0;
  fetchCalls.length = 0;
  loginOk = true;
  picker.files = [makeFile("reauth.png", 1024, "image/png")];
  picker.dispatch("change");
  await settle(4);
  assert.equal(xhrInstances.length, 1);
  xhrInstances[0].emitProgress(500, 1000);
  xhrInstances[0].respond(401, { error: "unauthorized" }, "Unauthorized");
  await settle(6);
  assert.equal(fetchCalls.filter((c) => c.url === "/api/login").length, 1, "401 restores session via /api/login");
  assert.equal(xhrInstances.length, 2, "401 retries upload via XHR after session restore");
  xhrInstances[1].emitProgress(600, 1000);
  xhrInstances[1].respond(200, { path: "/tmp/uploads/003-reauth.png", name: "reauth.png", size: 1024 });
  await settle(4);
  assert.equal(form._files[0].path, "/tmp/uploads/003-reauth.png");
  form._clearFiles();

  // ─── 6. Concurrent uploads: file 1 finishes without resetting file 2 progress ───
  xhrInstances.length = 0;
  picker.files = [makeFile("fast.txt", 512, "text/plain"), makeFile("big.mp4", 4096, "video/mp4")];
  picker.dispatch("change");
  await settle(4);
  assert.equal(xhrInstances.length, 2);
  xhrInstances[1].emitProgress(620, 1000);
  xhrInstances[0].respond(200, { path: "/tmp/uploads/fast.txt", name: "fast.txt", size: 512 });
  await settle(4);
  const chip2 = filesBox._chipNodes.get(form._files[1].id);
  assert.equal(chip2._qs[".fs"].textContent, "62%", "concurrent file 2 preserves 62% after file 1 completes");
  assert.equal(chip2._qs[".fi-prog-fill"].style.transform, "scaleX(0.62)", "concurrent file 2 preserves scaleX(0.62)");
  form._clearFiles();
  assert.equal(xhrInstances[1].aborted, true, "_clearFiles aborts remaining in-flight XHR");

  // ─── 7. Thread drawer (#td-composer) & English bilingual i18n keys ───
  ctx.CollieI18n.setLang("en");
  const tdForm = getEl("#td-composer");
  const tdBox = tdForm._qs[".files"];
  const tdPicker = tdForm._qs['input[type="file"]'];
  xhrInstances.length = 0;
  toastLog.length = 0;
  tdPicker.files = [makeFile("clip-en.mp4", 4096, "video/mp4")];
  tdPicker.dispatch("change");
  await settle(4);
  assert.equal(xhrInstances.length, 1, "thread drawer uploads via XHR");
  assert.ok(tdBox.innerHTML.includes('title="Cancel upload"'), "EN cancel title rendered");
  const tdChip = tdBox._chipNodes.get(tdForm._files[0].id);
  xhrInstances[0].emitProgress(400, 1000);
  assert.equal(tdChip._qs[".fs"].textContent, "40%");
  xhrInstances[0].emitUploadLoad();
  assert.equal(tdChip._qs[".fs"].textContent, "Processing…", "EN processing label rendered");
  tdForm.requestSubmit();
  await settle(2);
  assert.ok(toastLog.some((t) => t.msg.includes("still uploading")), "EN wait toast rendered: " + JSON.stringify(toastLog));
  tdForm._clearFiles();

  // ─── 8. Worker drawer (#wd-composer) preserves machine query & remote hub Bearer auth + withCredentials=false ───
  ctx.__test.setHubs([{ key: "lab", name: "Lab Hub", url: "https://lab.example", token: "tok-lab-secret" }]);
  ctx.__test.setWdPane("w1:p1@mac!lab");
  const wdForm = getEl("#wd-composer");
  const wdBox = wdForm._qs[".files"];
  const wdPicker = wdForm._qs['input[type="file"]'];
  xhrInstances.length = 0;
  wdPicker.files = [makeFile("trace.log", 1024, "text/plain")];
  wdPicker.dispatch("change");
  await settle(4);
  assert.equal(xhrInstances.length, 1, "worker drawer uploads via XHR");
  assert.equal(xhrInstances[0].url, "https://lab.example/api/uploads?name=trace.log&machine=mac", "remote hub URL + machine param preserved");
  assert.equal(xhrInstances[0].withCredentials, false, "remote hub XHR omits cookies");
  assert.equal(xhrInstances[0].headers.Authorization, "Bearer tok-lab-secret", "remote hub XHR sends Bearer auth header");
  xhrInstances[0].emitProgress(500, 1000);
  const wdChip = wdBox._chipNodes.get(wdForm._files[0].id);
  assert.equal(wdChip._qs[".fs"].textContent, "50%");
  xhrInstances[0].respond(200, { path: "/tmp/uploads/trace.log", name: "trace.log", size: 1024 });
  await settle(4);
  assert.equal(wdForm._files[0].path, "/tmp/uploads/trace.log");
  wdForm._clearFiles();
  ctx.__test.setHubs([]);

  // Verify bilingual dictionary key parity for all new upload keys
  for (const k of ["up.uploadingPct", "up.processing", "up.processingTip", "up.cancelTitle", "up.cancelAria", "up.waitUploading"]) {
    assert.ok(ctx.CollieI18n.D.zh[k], "missing zh key: " + k);
    assert.ok(ctx.CollieI18n.D.en[k], "missing en key: " + k);
  }

  console.log("ALL_UPLOAD_PROGRESS_TESTS_OK");
})().catch((e) => { console.error(e); process.exit(1); });
`

	cmd := exec.Command(node, "-e", script, appPath, i18nPath, cssPath, demoJsPath)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("node upload progress test failed: %v\n%s", err, out)
	}
}
