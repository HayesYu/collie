package collie

import (
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
)

func TestWebPasteUploadRetryAndToast(t *testing.T) {
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

	script := `
const fs = require("fs");
const vm = require("vm");
const assert = require("assert");

const appSrc = fs.readFileSync(process.argv[1], "utf8");

function makeClassList() {
  const s = new Set();
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
  const el = {
    id,
    tagName: id.toUpperCase(),
    value: "",
    textContent: "",
    innerHTML: "",
    hidden: false,
    disabled: false,
    style: { setProperty() {}, removeProperty() {} },
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
      if (el._qs[sel]) return el._qs[sel];
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

for (const [formSel, inputSel] of [["#super-composer", "#super-input"], ["#composer", "#input"], ["#x-composer", "#x-input"], ["#td-composer", "#td-input"]]) {
  const f = getEl(formSel);
  const inp = getEl(inputSel);
  f._qs["textarea"] = inp;
  f._qs[".send"] = formSel === "#td-composer" ? getEl("#td-send") : makeEl(formSel + " .send");
  f._qs[".files"] = makeEl(formSel + " .files");
  f._qs[".attach"] = makeEl(formSel + " .attach");
  f._qs['input[type="file"]'] = makeEl(formSel + " input[type=file]");
}

const toastLog = [];
const toastHost = getEl("#toasts");
const origAppend = toastHost.append.bind(toastHost);
toastHost.append = (...nodes) => {
  origAppend(...nodes);
  for (const n of nodes) toastLog.push({ msg: n.dataset?.msg || n.textContent, cls: n.className, el: n });
};

const ariaNotified = [];
const revoked = [];
const createdUrls = [];
let urlSeq = 0;

class FakeURL extends URL {
  static createObjectURL(blob) {
    const u = "blob:mem/" + (++urlSeq);
    createdUrls.push({ u, blob });
    return u;
  }
  static revokeObjectURL(u) { revoked.push(u); }
}

const fetchCalls = [];
let nextUploadHandler = null;
let fakeOnline = true;
let openDialog = null;

const doc = {
  documentElement: makeEl("html"),
  body: makeEl("body"),
  hidden: false,
  activeElement: null,
  createElement: (tag) => makeEl(tag),
  getElementById: (id) => getEl("#" + id),
  querySelector: (sel) => (sel === "dialog[open]" ? openDialog : getEl(sel)),
  querySelectorAll: () => [],
  addEventListener() {},
  hasFocus: () => false,
  ariaNotify(text, opts) { ariaNotified.push({ text, opts }); },
};

const ctx = {
  console: { log() {}, info() {}, warn() {}, error() {} },
  document: doc,
  window: null,
  location: { hash: "#/collie", search: "", pathname: "/", origin: "http://127.0.0.1", hostname: "127.0.0.1" },
  history: { state: {}, replaceState() {}, back() {} },
  navigator: { userAgent: "NodeTest", get onLine() { return fakeOnline; } },
  localStorage: { getItem: () => null, setItem() {} },
  matchMedia: () => ({ matches: false, addEventListener() {} }),
  MutationObserver: class { observe() {} },
  EventSource: class { addEventListener() {} close() {} },
  CSS: { escape: (s) => String(s) },
  URL: FakeURL,
  URLSearchParams,
  Blob,
  Uint8Array,
  TextEncoder,
  atob,
  AbortController,
  crypto: { randomUUID: () => "uuid-1" },
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

const okJSON = (data) => ({ ok: true, status: 200, headers: { get: () => "" }, json: async () => data });
const errJSON = (status, statusText, data) => ({ ok: false, status, statusText, headers: { get: () => "" }, json: async () => data });

ctx.fetch = async (url, init = {}) => {
  const u = String(url);
  fetchCalls.push({ url: u, method: init.method || "GET", body: init.body, signal: init.signal });
  if (u.startsWith("/api/uploads")) {
    if (nextUploadHandler) return await nextUploadHandler(u, init);
    return okJSON({ path: "/tmp/uploads/abc123-file.png", name: "file.png", size: init.body?.size || 64 });
  }
  return okJSON({ ok: true });
};
ctx.window = ctx;
ctx.globalThis = ctx;

const marker = "  boot();\n})();";
assert.ok(appSrc.includes(marker));
const instrumented = appSrc.replace(marker, "  globalThis.__test = { toast };\n})();");
vm.runInNewContext(instrumented, ctx, { filename: "web/app.js" });

const flush = () => new Promise((r) => setImmediate(r));
const settle = async (n = 10) => { for (let i = 0; i < n; i++) await flush(); };

// Minimal valid 1x1 PNG bytes
const PNG_B64 = "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mNk+A8AAQUBAScY42YAAAAASUVORK5CYII=";
const PNG_BYTES = Uint8Array.from(Buffer.from(PNG_B64, "base64"));

function makeFakeFile({ name, type, bytes, failRead = false, readOnce = false, size, lastModified = Date.now() }) {
  let reads = 0;
  return {
    name,
    type,
    size: size !== undefined ? size : (bytes ? bytes.byteLength : 0),
    lastModified,
    get readCount() { return reads; },
    async arrayBuffer() {
      reads++;
      if (failRead) throw new Error("NotReadableError: temp file missing");
      if (readOnce && reads > 1) throw new Error("NotReadableError: handle expired");
      return bytes ? bytes.buffer.slice(bytes.byteOffset, bytes.byteOffset + bytes.byteLength) : new ArrayBuffer(0);
    },
  };
}

(async () => {
  const form = getEl("#super-composer");
  const input = getEl("#super-input");
  const filesBox = form._qs[".files"];

  // 1) Multi-item clipboard: stale 32-hex .jpg cache handle + readable image/png bitmap
  // Must read synchronously on paste, fall back seamlessly to image/png memory Blob, 0 error toasts.
  toastLog.length = 0;
  fetchCalls.length = 0;
  createdUrls.length = 0;
  const staleJpg = makeFakeFile({ name: "7faa5def3d49fc2e1e5601dab3be57b3.jpg", type: "image/jpeg", size: 42000, failRead: true, lastModified: Date.now() - 60000 });
  const clipPng = makeFakeFile({ name: "image.png", type: "image/png", bytes: PNG_BYTES, readOnce: true, lastModified: Date.now() });
  const ev1 = input.dispatch("paste", {
    clipboardData: {
      items: [
        { kind: "file", type: "image/jpeg", getAsFile: () => staleJpg },
        { kind: "file", type: "image/png", getAsFile: () => clipPng },
      ],
      files: [staleJpg],
      getData: () => "",
    },
  });
  assert.equal(ev1.defaultPrevented, true, "paste with files prevents default");
  assert.equal(staleJpg.readCount, 1, "stale jpg read started synchronously during paste");
  assert.equal(clipPng.readCount, 1, "clip png read started synchronously during paste");
  assert.equal(form._files.length, 1, "grouped into 1 attachment chip");
  await settle();
  assert.equal(toastLog.length, 0, "no error toast when fallback png succeeds: " + JSON.stringify(toastLog));
  assert.equal(fetchCalls.filter((c) => c.url.startsWith("/api/uploads")).length, 1, "uploaded exactly once");
  const up1 = fetchCalls.find((c) => c.url.startsWith("/api/uploads"));
  assert.ok(up1.body instanceof Blob, "upload body is stable in-memory Blob");
  assert.equal(up1.body.size, PNG_BYTES.byteLength);
  assert.ok(decodeURIComponent(up1.url).includes(".png"), "fallback renamed to .png: " + up1.url);
  assert.ok(createdUrls[0].blob instanceof Blob, "thumb URL created from memory Blob, not volatile File");
  assert.equal(form._files[0].path, "/tmp/uploads/abc123-file.png");
  form._clearFiles();

  // 2) Stale cache file + HTML <img src="data:image/png;base64,..."> fallback
  toastLog.length = 0;
  fetchCalls.length = 0;
  const zeroJpg = makeFakeFile({ name: "7faa5def3d49fc2e1e5601dab3be57b3.jpg", type: "image/jpeg", bytes: new Uint8Array(0), size: 0 });
  input.dispatch("paste", {
    clipboardData: {
      items: [{ kind: "file", type: "image/jpeg", getAsFile: () => zeroJpg }],
      files: [zeroJpg],
      getData: (t) => (t === "text/html" ? '<img src="data:image/png;base64,' + PNG_B64 + '">' : ""),
    },
  });
  await settle();
  assert.equal(toastLog.length, 0, "HTML data-image fallback succeeds without error");
  assert.equal(fetchCalls.filter((c) => c.url.startsWith("/api/uploads")).length, 1);
  assert.equal(form._files[0].size, PNG_BYTES.byteLength);
  form._clearFiles();

  // 3) Unreadable clipboard cache file with NO fallback: human Chinese error, no raw Failed to fetch, no thumb img, no retry btn
  toastLog.length = 0;
  fetchCalls.length = 0;
  const deadJpg = makeFakeFile({ name: "7faa5def3d49fc2e1e5601dab3be57b3.jpg", type: "image/jpeg", size: 12345, failRead: true });
  input.dispatch("paste", {
    clipboardData: {
      items: [{ kind: "file", type: "image/jpeg", getAsFile: () => deadJpg }],
      files: [deadJpg],
      getData: () => "",
    },
  });
  await settle();
  assert.equal(fetchCalls.filter((c) => c.url.startsWith("/api/uploads")).length, 0, "must not call /api/uploads when unreadable");
  assert.equal(form._files.length, 1);
  assert.ok(form._files[0].err.includes("剪贴板里的图片已失效"), "friendly Chinese read error: " + form._files[0].err);
  assert.ok(!/Failed to fetch/i.test(form._files[0].err), "never expose raw Failed to fetch");
  assert.equal(form._files[0].thumb, "", "no broken thumb URL created");
  assert.ok(!filesBox.innerHTML.includes("<img"), "chip uses SVG fallback, no broken <img>: " + filesBox.innerHTML);
  assert.ok(filesBox.innerHTML.includes("fi-ico"), "chip renders SVG icon");
  assert.ok(!filesBox.innerHTML.includes("data-retry"), "unreadable file has no retry button");
  assert.equal(toastLog.length, 1);
  assert.ok(toastLog[0].msg.includes("…") && toastLog[0].msg.includes("剪贴板里的图片已失效"), "toast shortens long hash name: " + toastLog[0].msg);
  form._clearFiles();

  // 4) Plain text paste with HTML should NOT be intercepted
  const evText = input.dispatch("paste", {
    clipboardData: {
      items: [],
      files: [],
      getData: (t) => (t === "text/plain" ? "hello world" : '<p>hello <img src="data:image/png;base64,' + PNG_B64 + '"></p>'),
    },
  });
  assert.equal(evText.defaultPrevented, false, "normal text paste must not be blocked");
  assert.equal(form._files.length, 0);

  // 5) Network error (TypeError: Failed to fetch) -> Chinese translation + Retry button using preserved memory Blob
  toastLog.length = 0;
  fetchCalls.length = 0;
  const oncePng = makeFakeFile({ name: "screenshot.png", type: "image/png", bytes: PNG_BYTES, readOnce: true });
  nextUploadHandler = async () => { throw new TypeError("Failed to fetch"); };
  input.dispatch("paste", {
    clipboardData: { items: [{ kind: "file", type: "image/png", getAsFile: () => oncePng }], files: [oncePng], getData: () => "" },
  });
  await settle();
  assert.equal(form._files.length, 1);
  assert.ok(form._files[0].err.includes("网络中断或服务正在重启"), "translated Failed to fetch: " + form._files[0].err);
  assert.ok(!/Failed to fetch/i.test(toastLog[0].msg), "toast never shows Failed to fetch: " + toastLog[0].msg);
  assert.ok(filesBox.innerHTML.includes('data-retry="0"'), "retry button rendered for network failure");
  assert.ok(form._files[0].blob instanceof Blob, "memory Blob preserved for retry");
  assert.equal(oncePng.readCount, 1, "raw file only read once");

  // Click Retry -> succeeds using preserved Blob without touching raw File again!
  nextUploadHandler = async () => okJSON({ path: "/tmp/uploads/retry-ok.png", name: "screenshot.png", size: PNG_BYTES.byteLength });
  const retryEl = { dataset: { retry: "0" }, closest: (s) => (s === "[data-retry]" ? retryEl : null) };
  filesBox.dispatch("click", { target: retryEl });
  await settle();
  assert.equal(oncePng.readCount, 1, "retry must NOT re-read original expired File handle");
  assert.equal(form._files[0].err, "", "error cleared after retry success");
  assert.equal(form._files[0].path, "/tmp/uploads/retry-ok.png", "path populated after retry");
  assert.ok(!filesBox.innerHTML.includes("data-retry"), "retry button removed after success");

  // 6) Broken <img> thumbnail fallback via capture error event
  const prevThumb = form._files[0].thumb;
  assert.ok(prevThumb, "thumb present before error simulation");
  revoked.length = 0;
  filesBox.dispatch("error", { target: { tagName: "IMG", dataset: { fi: "0" } } }, true);
  assert.equal(form._files[0].thumb, "", "invalid thumb cleared on img error");
  assert.equal(form._files[0].thumbFailed, true);
  assert.ok(revoked.includes(prevThumb), "invalid thumb URL revoked");
  assert.ok(!filesBox.innerHTML.includes("<img"), "img replaced with SVG icon");
  assert.ok(filesBox.innerHTML.includes("fi-ico"));
  form._clearFiles();

  // 7) 413 Too Large & >25MB pre-check: clear Chinese message, no retry button
  toastLog.length = 0;
  fetchCalls.length = 0;
  const huge = makeFakeFile({ name: "huge.mov", type: "video/quicktime", size: 30 * 1024 * 1024 });
  input.dispatch("paste", { clipboardData: { files: [huge], getData: () => "" } });
  await settle();
  assert.equal(fetchCalls.length, 0, "files >25MB fail fast before upload");
  assert.equal(form._files[0].err, "文件太大了（最多 25 MB）");
  assert.ok(!filesBox.innerHTML.includes("data-retry"), "too-large file has no retry button");
  form._clearFiles();

  // 8) Offline & 503 error translation
  toastLog.length = 0;
  fakeOnline = false;
  nextUploadHandler = async () => { throw new TypeError("Failed to fetch"); };
  const small1 = makeFakeFile({ name: "a.png", type: "image/png", bytes: PNG_BYTES });
  input.dispatch("paste", { clipboardData: { files: [small1], getData: () => "" } });
  await settle();
  assert.ok(form._files[0].err.includes("网络已断开"), "offline translated: " + form._files[0].err);
  form._clearFiles();
  fakeOnline = true;

  toastLog.length = 0;
  nextUploadHandler = async () => errJSON(503, "Service Unavailable", {});
  const small2 = makeFakeFile({ name: "b.png", type: "image/png", bytes: PNG_BYTES });
  input.dispatch("paste", { clipboardData: { files: [small2], getData: () => "" } });
  await settle();
  assert.ok(form._files[0].err.includes("服务正在重启"), "503 translated: " + form._files[0].err);
  assert.ok(filesBox.innerHTML.includes("data-retry"), "503 is retryable");
  form._clearFiles();

  // 9) Drop event deduplicates items + files and uploads memory Blob
  fetchCalls.length = 0;
  nextUploadHandler = null;
  const dropped = makeFakeFile({ name: "drop.png", type: "image/png", bytes: PNG_BYTES });
  const dropEv = form.dispatch("drop", {
    dataTransfer: {
      items: [{ kind: "file", type: "image/png", getAsFile: () => dropped }],
      files: [dropped],
      getData: () => "",
    },
  });
  assert.equal(dropEv.defaultPrevented, true);
  await settle();
  assert.equal(form._files.length, 1, "drop deduplicates items and files");
  assert.equal(fetchCalls.filter((c) => c.url.startsWith("/api/uploads")).length, 1);
  form._clearFiles();

  // 10) Toast uses popover="manual", ariaNotify, and defers behind open modal dialog
  toastLog.length = 0;
  ariaNotified.length = 0;
  while (toastHost.children.length) toastHost.firstElementChild.remove();
  toastHost._popoverOpen = false;
  ctx.__test.toast("上传恢复正常", "");
  assert.equal(toastHost._popoverOpen, true, "toast host opens popover");
  assert.equal(ariaNotified.length, 1);
  assert.equal(ariaNotified[0].text, "上传恢复正常", "ariaNotify announces only visible text");
  // Dismiss manually via close button -> hides popover when empty
  const closeBtn = toastLog[0].el.children[1];
  closeBtn.dispatch("click");
  assert.equal(toastHost.children.length, 0);
  assert.equal(toastHost._popoverOpen, false, "popover hides when last toast dismissed");

  // Modal dialog open -> toast shows IMMEDIATELY and re-promotes #toasts above <dialog> in Top Layer (#t12623)
  openDialog = makeEl("dialog");
  openDialog.open = true;
  let hideCalls = 0, showCalls = 0;
  const origHide = toastHost.hidePopover, origShow = toastHost.showPopover;
  toastHost.hidePopover = () => { hideCalls++; toastHost._popoverOpen = false; };
  toastHost.showPopover = () => { showCalls++; toastHost._popoverOpen = true; };
  toastHost._popoverOpen = true; // simulate already-open popover underneath the modal dialog
  toastLog.length = 0;
  ctx.__test.toast("已复制邀请链接", "");
  assert.equal(toastLog.length, 1, "toast inside open modal dialog must appear immediately without waiting for dialog close");
  assert.equal(hideCalls, 1, "already-open toast popover must hidePopover() before re-promoting above open <dialog>");
  assert.equal(showCalls, 1, "toast popover must showPopover() to sit on top of open <dialog> in Top Layer");
  assert.equal(toastHost._popoverOpen, true);
  // Fallback when Popover API is unavailable: reparent #toasts into open <dialog> and restore on close
  while (toastHost.children.length) toastHost.firstElementChild.remove();
  toastHost.hidePopover = undefined;
  toastHost.showPopover = undefined;
  doc.body.appendChild = (c) => { c.parentNode = doc.body; return c; };
  openDialog.appendChild = (c) => { c.parentNode = openDialog; return c; };
  toastLog.length = 0;
  ctx.__test.toast("回退弹窗提示", "");
  assert.equal(toastLog.length, 1, "non-popover fallback still shows toast immediately inside open dialog");
  assert.equal(toastHost.parentNode, openDialog, "non-popover fallback mounts #toasts inside open <dialog>");
  openDialog.open = false;
  openDialog.dispatch("close");
  assert.equal(toastHost.parentNode, doc.body, "closing dialog restores #toasts back to document.body");
  toastHost.hidePopover = origHide;
  toastHost.showPopover = origShow;
  openDialog = null;

  // 11) All pasted files unreadable (e.g. macOS sandboxed temp .jpg path) -> falls back to navigator.clipboard.read() bitmap Blob
  toastLog.length = 0;
  fetchCalls.length = 0;
  createdUrls.length = 0;
  nextUploadHandler = null;
  let clipReadCalls = 0;
  ctx.isSecureContext = true;
  ctx.navigator.clipboard = {
    async read() {
      clipReadCalls++;
      return [{
        types: ["text/html", "image/png"],
        async getType(t) {
          assert.equal(t, "image/png");
          return new Blob([PNG_BYTES], { type: "image/png" });
        },
      }];
    },
  };
  const blockedJpg = makeFakeFile({ name: "9b8c7d6e5f4a3b2c1d0e9f8a7b6c5d4e.jpg", type: "image/jpeg", size: 28000, failRead: true });
  input.dispatch("paste", {
    clipboardData: {
      items: [{ kind: "file", type: "image/jpeg", getAsFile: () => blockedJpg }],
      files: [blockedJpg],
      getData: () => "",
    },
  });
  await settle();
  assert.equal(clipReadCalls, 1, "clipboard.read() called once as fallback");
  assert.equal(toastLog.length, 0, "no error toast when clipboard.read() recovers bitmap: " + JSON.stringify(toastLog));
  assert.equal(fetchCalls.filter((c) => c.url.startsWith("/api/uploads")).length, 1, "recovered bitmap uploaded once");
  const upClip = fetchCalls.find((c) => c.url.startsWith("/api/uploads"));
  assert.ok(upClip.body instanceof Blob && upClip.body.size === PNG_BYTES.byteLength, "uploaded memory Blob matches PNG bitmap");
  assert.ok(decodeURIComponent(upClip.url).includes(".png"), "filename extension swapped to .png: " + upClip.url);
  assert.equal(form._files[0].path, "/tmp/uploads/abc123-file.png");
  form._clearFiles();

  // 12) clipboard.read() rejects or returns no image -> preserves friendly Chinese error
  toastLog.length = 0;
  fetchCalls.length = 0;
  ctx.navigator.clipboard = { async read() { throw new Error("NotAllowedError"); } };
  const blockedJpg2 = makeFakeFile({ name: "9b8c7d6e5f4a3b2c1d0e9f8a7b6c5d4e.jpg", type: "image/jpeg", size: 28000, failRead: true });
  input.dispatch("paste", {
    clipboardData: {
      items: [{ kind: "file", type: "image/jpeg", getAsFile: () => blockedJpg2 }],
      files: [blockedJpg2],
      getData: () => "",
    },
  });
  await settle();
  assert.equal(fetchCalls.filter((c) => c.url.startsWith("/api/uploads")).length, 0, "must not upload when fallback fails");
  assert.ok(form._files[0].err.includes("剪贴板里的图片已失效"), "keeps friendly Chinese error after clipboard.read() failure");
  form._clearFiles();
  delete ctx.navigator.clipboard;

  // 13) Thread drawer composer (#td-composer) shares paste + clipboard.read() + drag/drop + picker + retry + delete
  const tdForm = getEl("#td-composer");
  const tdInput = getEl("#td-input");
  const tdSend = getEl("#td-send");
  const tdFilesBox = tdForm._qs[".files"];
  const tdAttach = tdForm._qs[".attach"];
  const tdPicker = tdForm._qs['input[type="file"]'];
  let tdPickerClicked = 0;
  tdPicker.click = () => { tdPickerClicked++; };
  assert.equal(tdSend.disabled, true, "drawer send button disabled when empty");

  // 13a) Click '+' attach button opens file picker & picker change uploads + enables send
  toastLog.length = 0;
  fetchCalls.length = 0;
  nextUploadHandler = null;
  tdAttach.dispatch("click");
  assert.equal(tdPickerClicked, 1, "clicking drawer + button opens file picker");
  const pickedImg = makeFakeFile({ name: "drawer-pick.png", type: "image/png", bytes: PNG_BYTES });
  tdPicker.files = [pickedImg];
  tdPicker.dispatch("change");
  assert.equal(tdSend.disabled, false, "drawer send button enabled as soon as attachment added");
  await settle();
  assert.equal(tdForm._files.length, 1);
  assert.equal(tdForm._files[0].path, "/tmp/uploads/abc123-file.png");
  assert.ok(tdFilesBox.innerHTML.includes("drawer-pick.png"), "drawer preview strip renders picked image chip");

  // Remove item -> disables send button again
  const rmEl = { dataset: { rm: "0" }, closest: (s) => (s === "[data-rm]" ? rmEl : null) };
  tdFilesBox.dispatch("click", { target: rmEl });
  assert.equal(tdForm._files.length, 0, "clicking remove deletes drawer attachment");
  assert.equal(tdSend.disabled, true, "drawer send button disabled after removing last attachment");

  // 13b) Paste stale image into drawer textarea -> clipboard.read() fallback recovers bitmap
  toastLog.length = 0;
  fetchCalls.length = 0;
  let tdClipReads = 0;
  ctx.isSecureContext = true;
  ctx.navigator.clipboard = {
    async read() {
      tdClipReads++;
      return [{ types: ["image/png"], async getType() { return new Blob([PNG_BYTES], { type: "image/png" }); } }];
    },
  };
  const tdBlocked = makeFakeFile({ name: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa.jpg", type: "image/jpeg", size: 12000, failRead: true });
  const tdPasteEv = tdInput.dispatch("paste", {
    clipboardData: { items: [{ kind: "file", type: "image/jpeg", getAsFile: () => tdBlocked }], files: [tdBlocked], getData: () => "" },
  });
  assert.equal(tdPasteEv.defaultPrevented, true, "drawer paste prevents default");
  await settle();
  assert.equal(tdClipReads, 1, "drawer paste uses clipboard.read() fallback");
  assert.equal(tdForm._files.length, 1);
  assert.equal(tdForm._files[0].path, "/tmp/uploads/abc123-file.png");
  tdForm._clearFiles();
  delete ctx.navigator.clipboard;

  // 13c) Dragover + drop onto drawer composer + upload failure + retry
  fetchCalls.length = 0;
  toastLog.length = 0;
  nextUploadHandler = async () => { throw new TypeError("Failed to fetch"); };
  const tdDragOver = tdForm.dispatch("dragover", { dataTransfer: { types: ["Files"] } });
  assert.equal(tdDragOver.defaultPrevented, true, "drawer dragover prevents default for Files");
  assert.equal(tdForm.classList.contains("drop"), true, "drawer composer gets .drop highlight on dragover");
  const tdDropped = makeFakeFile({ name: "drop-drawer.png", type: "image/png", bytes: PNG_BYTES, readOnce: true });
  const tdDropEv = tdForm.dispatch("drop", {
    dataTransfer: { items: [{ kind: "file", type: "image/png", getAsFile: () => tdDropped }], files: [tdDropped], getData: () => "" },
  });
  assert.equal(tdDropEv.defaultPrevented, true);
  assert.equal(tdForm.classList.contains("drop"), false, ".drop cleared on drop");
  await settle();
  assert.ok(tdFilesBox.innerHTML.includes('data-retry="0"'), "drawer renders retry button on failed upload");
  nextUploadHandler = async () => okJSON({ path: "/tmp/uploads/drawer-retry.png", name: "drop-drawer.png", size: PNG_BYTES.byteLength });
  const tdRetryEl = { dataset: { retry: "0" }, closest: (s) => (s === "[data-retry]" ? tdRetryEl : null) };
  tdFilesBox.dispatch("click", { target: tdRetryEl });
  await settle();
  assert.equal(tdForm._files[0].err, "", "drawer retry clears error");
  assert.equal(tdForm._files[0].path, "/tmp/uploads/drawer-retry.png", "drawer retry uploads preserved memory Blob");
  tdForm._clearFiles();

  // 14) #t11660: WeChat macOS delayed paste (>10s old) where stale 32-hex .jpg + generic image.png coexist
  toastLog.length = 0;
  fetchCalls.length = 0;
  nextUploadHandler = null;
  const oldTime = Date.now() - 120_000;
  const wxDeadJpg = makeFakeFile({ name: "667e00fe934ec1122334455667788990.jpg", type: "image/jpeg", size: 88000, failRead: true, lastModified: oldTime });
  const wxBitmapPng = makeFakeFile({ name: "image.png", type: "image/png", bytes: PNG_BYTES, readOnce: true, lastModified: oldTime });
  const wxEv = input.dispatch("paste", {
    clipboardData: {
      items: [
        { kind: "file", type: "image/jpeg", getAsFile: () => wxDeadJpg },
        { kind: "file", type: "image/png", getAsFile: () => wxBitmapPng },
      ],
      files: [wxDeadJpg],
      getData: () => "",
    },
  });
  assert.equal(wxEv.defaultPrevented, true);
  assert.equal(form._files.length, 1, "delayed WeChat paste must merge stale hash .jpg and image.png into 1 candidate group");
  await settle();
  assert.equal(toastLog.length, 0, "delayed WeChat paste must recover image.png with zero error toasts: " + JSON.stringify(toastLog));
  assert.equal(fetchCalls.filter((c) => c.url.startsWith("/api/uploads")).length, 1);
  assert.equal(form._files[0].err, "");
  form._clearFiles();

  // 15) #t11660: navigator.clipboard.read() iterates multiple image/* types when first fails/empty
  toastLog.length = 0;
  fetchCalls.length = 0;
  ctx.isSecureContext = true;
  ctx.navigator.clipboard = {
    async read() {
      return [{
        types: ["image/jpeg", "image/png"],
        async getType(t) {
          if (t === "image/jpeg") throw new Error("NotReadableError");
          return new Blob([PNG_BYTES], { type: "image/png" });
        },
      }];
    },
  };
  const wxOnlyDead = makeFakeFile({ name: "667e00fe934ec1122334455667788991.jpg", type: "image/jpeg", size: 54000, failRead: true });
  input.dispatch("paste", {
    clipboardData: {
      items: [{ kind: "file", type: "image/jpeg", getAsFile: () => wxOnlyDead }],
      files: [wxOnlyDead],
      getData: () => "",
    },
  });
  await settle();
  assert.equal(toastLog.length, 0, "multi-type clipboard.read() must fall through failing jpeg to png: " + JSON.stringify(toastLog));
  assert.equal(fetchCalls.filter((c) => c.url.startsWith("/api/uploads")).length, 1);
  assert.equal(form._files[0].err, "");
  form._clearFiles();

  // 16) #t11660: HTML-only <img src="file:///..."> paste falls back to navigator.clipboard.read()
  toastLog.length = 0;
  fetchCalls.length = 0;
  let htmlFallbackReads = 0;
  ctx.navigator.clipboard = {
    async read() {
      htmlFallbackReads++;
      return [{ types: ["image/png"], async getType() { return new Blob([PNG_BYTES], { type: "image/png" }); } }];
    },
  };
  const htmlOnlyEv = input.dispatch("paste", {
    clipboardData: {
      items: [],
      files: [],
      getData: (t) => (t === "text/html" ? '<img src="file:///Users/me/Library/Containers/com.tencent.xinWeChat/temp.jpg">' : ""),
    },
  });
  assert.equal(htmlOnlyEv.defaultPrevented, true, "HTML-only img paste must intercept when clipboard.read() is available");
  await settle();
  assert.equal(htmlFallbackReads, 1, "HTML-only img paste must invoke clipboard.read()");
  assert.equal(toastLog.length, 0, "HTML-only img paste recovers cleanly with zero error toasts: " + JSON.stringify(toastLog));
  assert.equal(fetchCalls.filter((c) => c.url.startsWith("/api/uploads")).length, 1);
  form._clearFiles();
  delete ctx.navigator.clipboard;

  // 17) #t13006: macOS WeChat stale hash .jpg + companion item with empty file.type (uses _itemType)
  toastLog.length = 0;
  fetchCalls.length = 0;
  nextUploadHandler = null;
  const wxSandboxJpg = makeFakeFile({ name: "592f4e66e91b4aa2bb33cc44dd55434df0.jpg", type: "image/jpeg", size: 92000, failRead: true });
  const wxTypedViaItem = makeFakeFile({ name: "image.png", type: "", bytes: PNG_BYTES, readOnce: true });
  const wx17Ev = input.dispatch("paste", {
    clipboardData: {
      items: [
        { kind: "file", type: "image/jpeg", getAsFile: () => wxSandboxJpg },
        { kind: "file", type: "image/png", getAsFile: () => wxTypedViaItem },
      ],
      files: [wxSandboxJpg],
      getData: () => "",
    },
  });
  assert.equal(wx17Ev.defaultPrevented, true);
  assert.equal(form._files.length, 1, "stale WeChat jpg + companion _itemType png must group into 1 entry");
  await settle();
  assert.equal(toastLog.length, 0, "companion _itemType png must recover cleanly without toast: " + JSON.stringify(toastLog.map((t) => t.msg)));
  assert.equal(fetchCalls.filter((c) => c.url.startsWith("/api/uploads")).length, 1);
  assert.equal(form._files[0].err, "");
  form._clearFiles();

  // 18) #t13006: WeChat UUID .jpg unreadable -> navigator.clipboard.read() recovers from text/html data:image/png
  toastLog.length = 0;
  fetchCalls.length = 0;
  ctx.isSecureContext = true;
  ctx.navigator.clipboard = {
    async read() {
      return [{
        types: ["text/html"],
        async getType(t) {
          assert.equal(t, "text/html");
          return new Blob(['<img src="data:image/png;base64,' + PNG_B64 + '">'], { type: "text/html" });
        },
      }];
    },
  };
  const wxUuidDead = makeFakeFile({ name: "592f4e66-e91b-4aa2-bb33-cc44dd55434d.jpg", type: "image/jpeg", size: 42000, failRead: true });
  input.dispatch("paste", {
    clipboardData: {
      items: [{ kind: "file", type: "image/jpeg", getAsFile: () => wxUuidDead }],
      files: [wxUuidDead],
      getData: () => "",
    },
  });
  await settle();
  assert.equal(toastLog.length, 0, "text/html data:image fallback must recover without error toast: " + JSON.stringify(toastLog.map((t) => t.msg)));
  assert.equal(fetchCalls.filter((c) => c.url.startsWith("/api/uploads")).length, 1);
  assert.equal(form._files[0].err, "");
  form._clearFiles();

  // 19) #t13006: Raw TIFF bytes from macOS NSPasteboardTypeTIFF sniff as image/tiff
  toastLog.length = 0;
  fetchCalls.length = 0;
  const TIFF_BYTES = Uint8Array.from([0x49, 0x49, 0x2a, 0x00, 0x08, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00]);
  ctx.navigator.clipboard = {
    async read() {
      return [{ types: ["image/tiff"], async getType() { return new Blob([TIFF_BYTES], { type: "image/tiff" }); } }];
    },
  };
  const wxDeadForTiff = makeFakeFile({ name: "592f4e66e91b4aa2bb33cc44dd55434df9.jpg", type: "image/jpeg", size: 42000, failRead: true });
  input.dispatch("paste", {
    clipboardData: {
      items: [{ kind: "file", type: "image/jpeg", getAsFile: () => wxDeadForTiff }],
      files: [wxDeadForTiff],
      getData: () => "",
    },
  });
  await settle();
  assert.equal(toastLog.length, 0, "TIFF fallback must upload cleanly without error toast: " + JSON.stringify(toastLog.map((t) => t.msg)));
  assert.equal(fetchCalls.filter((c) => c.url.startsWith("/api/uploads")).length, 1);
  assert.equal(form._files[0].err, "");
  form._clearFiles();

  // 20) #t13006: Non-HTTPS origin without navigator.clipboard.read() shows actionable HTTPS/drag hint
  toastLog.length = 0;
  fetchCalls.length = 0;
  ctx.isSecureContext = false;
  delete ctx.navigator.clipboard;
  const wxNonSecure = makeFakeFile({ name: "592f4e66e91b4aa2bb33cc44dd55434df8.jpg", type: "image/jpeg", size: 42000, failRead: true });
  input.dispatch("paste", {
    clipboardData: {
      items: [{ kind: "file", type: "image/jpeg", getAsFile: () => wxNonSecure }],
      files: [wxNonSecure],
      getData: () => "",
    },
  });
  await settle();
  assert.ok(toastLog.some((t) => /HTTPS|拖入/.test(t.msg)), "non-HTTPS unreadable WeChat paste must explain HTTPS/drag fallback: " + JSON.stringify(toastLog.map((t) => t.msg)));
  ctx.isSecureContext = true;
  form._clearFiles();

  console.log("ALL_PASTE_UPLOAD_TESTS_OK");
})().catch((e) => { console.error(e); process.exit(1); });
`

	cmd := exec.Command(node, "-e", script, appPath)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("node paste/upload test failed: %v\n%s", err, out)
	}
}
