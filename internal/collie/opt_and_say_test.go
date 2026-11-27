package collie

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"testing/fstest"
	"time"
	"unicode/utf8"
)

func TestCleanOptionsLongChineseAndCap(t *testing.T) {
	t.Parallel()
	longCN := "先按样例表（1,020w）跑完增量核验，你顺手提 256G 升配申请后再灌全量并补齐回归单测与监控看板检查"
	if len(longCN) <= 80 {
		t.Fatalf("test fixture must exceed legacy 80-byte clip limit, got %d bytes", len(longCN))
	}
	secondCN := "先只做本地离线验证，等下周窗口再切全量回刷并同步更新文档与定时任务配置（不改线上基线）"
	multiline := "  第一段说明\n  第二段补充\t带制表符  "

	got := cleanOptions([]string{longCN, secondCN, multiline})
	want := []string{longCN, secondCN, "第一段说明 第二段补充 带制表符"}
	if !slices.Equal(got, want) {
		t.Fatalf("cleanOptions mismatch:\n got = %#v\nwant = %#v", got, want)
	}
	for _, opt := range got {
		if strings.Contains(opt, "…") || strings.HasSuffix(opt, "...") {
			t.Fatalf("unexpected ellipsis in cleaned option: %q", opt)
		}
	}

	// Abuse-length option is capped at maxReplyOptionRunes without appending "…".
	huge := strings.Repeat("中", maxReplyOptionRunes+60)
	capped := cleanOptions([]string{huge})
	if len(capped) != 1 {
		t.Fatalf("capped len = %d, want 1", len(capped))
	}
	if !utf8.ValidString(capped[0]) {
		t.Fatalf("capped option is not valid UTF-8")
	}
	if gotRunes := utf8.RuneCountInString(capped[0]); gotRunes != maxReplyOptionRunes {
		t.Fatalf("capped rune count = %d, want %d", gotRunes, maxReplyOptionRunes)
	}
	if strings.Contains(capped[0], "…") {
		t.Fatalf("capped option must not append ellipsis: %q", capped[0])
	}

	// Full API + Pending + SQLite persistence round-trip preserves long Chinese options intact.
	sp, db, call := newPendingTestEnv(t)
	rec := call("POST", "/api/super/reply", map[string]any{
		"text":    "下一步怎么推进？",
		"options": []string{longCN, secondCN},
	})
	if rec.Code != 200 {
		t.Fatalf("reply status = %d: %s", rec.Code, rec.Body.String())
	}
	var replyOut struct {
		Entry string `json:"entry"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &replyOut); err != nil {
		t.Fatal(err)
	}

	snapRec := call("GET", "/api/super", nil)
	var snap superView
	if err := json.Unmarshal(snapRec.Body.Bytes(), &snap); err != nil {
		t.Fatal(err)
	}
	if len(snap.Entries) != 1 || !slices.Equal(snap.Entries[0].Options, []string{longCN, secondCN}) {
		t.Fatalf("snapshot entries options = %#v", snap.Entries)
	}
	if len(snap.Pending) != 1 || !slices.Equal(snap.Pending[0].Options, []string{longCN, secondCN}) {
		t.Fatalf("snapshot pending options = %#v", snap.Pending)
	}

	// Clicking the long option sends the full text verbatim without ellipsis.
	sayRec := call("POST", "/api/super/say", map[string]any{
		"text":          longCN,
		"re":            replyOut.Entry,
		"client_msg_id": "opt-click-1",
	})
	if sayRec.Code != 200 {
		t.Fatalf("say status = %d: %s", sayRec.Code, sayRec.Body.String())
	}

	sp.flush()
	reloaded, err := loadState(db, sp.dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(reloaded.Entries) != 2 {
		t.Fatalf("reloaded entries = %d, want 2", len(reloaded.Entries))
	}
	if !slices.Equal(reloaded.Entries[0].Options, []string{longCN, secondCN}) {
		t.Fatalf("reloaded options = %#v", reloaded.Entries[0].Options)
	}
	if reloaded.Entries[1].Text != longCN {
		t.Fatalf("reloaded user text = %q, want %q", reloaded.Entries[1].Text, longCN)
	}
}

func TestSuperSayClientMsgIDDedup(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	db, err := openDB(filepath.Join(dir, "herd.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	st, err := loadState(db, dir)
	if err != nil {
		t.Fatal(err)
	}
	srv := &Server{token: "test-tok", home: dir, web: fstest.MapFS{}}
	sp := &Super{s: srv, db: db, dir: dir, st: st, subs: map[chan struct{}]bool{}}
	srv.sup = sp
	mux := http.NewServeMux()
	srv.superRoutes(mux)

	postSay := func(body map[string]any) (int, map[string]any) {
		b, _ := json.Marshal(body)
		req := httptest.NewRequest("POST", "/api/super/say", bytes.NewReader(b))
		req.Header.Set("Authorization", "Bearer test-tok")
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		var out map[string]any
		_ = json.Unmarshal(rec.Body.Bytes(), &out)
		return rec.Code, out
	}

	// 1) Concurrent duplicate requests with the same client_msg_id create only 1 entry & 1 trigger.
	const n = 8
	var wg sync.WaitGroup
	results := make([]map[string]any, n)
	codes := make([]int, n)
	for i := range n {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			codes[idx], results[idx] = postSay(map[string]any{
				"text":          "带图消息测试\n\n附件：\n/tmp/demo/50a667-image.png",
				"client_msg_id": "msg-same-001",
			})
		}(i)
	}
	wg.Wait()

	firstEntry := ""
	firstTrig := ""
	dupCount := 0
	for i := range n {
		if codes[i] != 200 {
			t.Fatalf("req %d status = %d, body = %+v", i, codes[i], results[i])
		}
		e, _ := results[i]["entry"].(string)
		tr, _ := results[i]["trigger"].(string)
		if e == "" || tr == "" {
			t.Fatalf("req %d missing entry/trigger: %+v", i, results[i])
		}
		if firstEntry == "" {
			firstEntry, firstTrig = e, tr
		} else if e != firstEntry || tr != firstTrig {
			t.Fatalf("req %d returned (%s,%s), want (%s,%s)", i, e, tr, firstEntry, firstTrig)
		}
		if dup, _ := results[i]["duplicate"].(bool); dup {
			dupCount++
		}
	}
	if dupCount != n-1 {
		t.Fatalf("duplicate responses = %d, want %d", dupCount, n-1)
	}
	sp.mu.Lock()
	gotEntries, gotTriggers := len(sp.st.Entries), len(sp.st.Triggers)
	sp.mu.Unlock()
	if gotEntries != 1 || gotTriggers != 1 {
		t.Fatalf("after concurrent dup say: entries=%d triggers=%d, want 1/1", gotEntries, gotTriggers)
	}

	// 2) Different client_msg_id (or omitted) allows intentional repeated messages.
	code2, out2 := postSay(map[string]any{"text": "继续", "client_msg_id": "msg-diff-a"})
	code3, out3 := postSay(map[string]any{"text": "继续", "client_msg_id": "msg-diff-b"})
	code4, out4 := postSay(map[string]any{"text": "继续"})
	if code2 != 200 || code3 != 200 || code4 != 200 {
		t.Fatalf("statuses = %d/%d/%d", code2, code3, code4)
	}
	if out2["entry"] == out3["entry"] || out3["entry"] == out4["entry"] {
		t.Fatalf("distinct messages collapsed unexpectedly: %+v %+v %+v", out2, out3, out4)
	}

	// 3) Expired dedup receipt does not block forever.
	sp.mu.Lock()
	sp.sayDedup["msg-expired"] = sayReceipt{entry: "eOld", trigger: "tOld", at: time.Now().Add(-sayDedupTTL - time.Second)}
	sp.mu.Unlock()
	code5, out5 := postSay(map[string]any{"text": "过期后重发", "client_msg_id": "msg-expired"})
	if code5 != 200 || out5["entry"] == "eOld" {
		t.Fatalf("expired client_msg_id should create fresh entry, got code=%d out=%+v", code5, out5)
	}
	if dup, _ := out5["duplicate"].(bool); dup {
		t.Fatalf("expired client_msg_id should not report duplicate: %+v", out5)
	}
}

func TestWebComposerNoDoubleSendAndLongOpts(t *testing.T) {
	t.Parallel()
	nodeBin, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node not installed; skipping JS composer regression tests")
	}
	root := filepath.Join("..", "..")
	jsTest := `
const assert = require("node:assert/strict");
const fs = require("node:fs");
const path = require("node:path");
const vm = require("node:vm");

const root = process.argv[1];
const appSrc = fs.readFileSync(path.join(root, "web/app.js"), "utf8");

function makeEl(id = "") {
  const listeners = new Map();
  const attrs = new Map();
  const classes = new Set();
  const el = {
    id,
    tagName: "DIV",
    value: "",
    innerHTML: "",
    textContent: "",
    hidden: false,
    disabled: false,
    scrollHeight: 40,
    scrollTop: 0,
    clientHeight: 400,
    dataset: {},
    style: { setProperty() {} },
    className: "",
    classList: {
      add: (...cs) => cs.forEach((c) => classes.add(c)),
      remove: (...cs) => cs.forEach((c) => classes.delete(c)),
      toggle: (c, force) => { if (force === undefined) { classes.has(c) ? classes.delete(c) : classes.add(c); } else if (force) classes.add(c); else classes.delete(c); },
      contains: (c) => classes.has(c),
    },
    children: [],
    options: [],
    selectedOptions: [],
    parentElement: null,
    _qs: {},
    addEventListener(type, fn) {
      if (!listeners.has(type)) listeners.set(type, []);
      listeners.get(type).push(fn);
    },
    removeEventListener(type, fn) {
      const arr = listeners.get(type) || [];
      listeners.set(type, arr.filter((x) => x !== fn));
    },
    dispatch(type, ev = {}) {
      const event = {
        type,
        defaultPrevented: false,
        preventDefault() { this.defaultPrevented = true; },
        stopPropagation() {},
        composedPath() { return [el]; },
        target: el,
        ...ev,
      };
      const results = [];
      for (const fn of listeners.get(type) || []) results.push(fn(event));
      return { event, results };
    },
    requestSubmit() {
      return el.dispatch("submit");
    },
    querySelector(sel) {
      if (el._qs[sel]) return el._qs[sel];
      const child = makeEl(id + ":" + sel);
      el._qs[sel] = child;
      return child;
    },
    querySelectorAll(sel) {
      return el._qsa?.[sel] || [];
    },
    setAttribute(k, v) { attrs.set(k, String(v)); },
    getAttribute(k) { return attrs.get(k) ?? ""; },
    removeAttribute(k) { attrs.delete(k); },
    hasAttribute(k) { return attrs.has(k); },
    closest() { return null; },
    contains() { return false; },
    append() {},
    remove() {},
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

// Pre-wire forms and child controls so selectors inside bindComposer/bindFiles hit stable elements.
for (const [formSel, inputSel] of [["#super-composer", "#super-input"], ["#composer", "#input"], ["#x-composer", "#x-input"]]) {
  const f = getEl(formSel);
  const inp = getEl(inputSel);
  f._qs["textarea"] = inp;
  f._qs[".send"] = makeEl(formSel + " .send");
  f._qs[".files"] = makeEl(formSel + " .files");
  f._qs[".attach"] = makeEl(formSel + " .attach");
  f._qs['input[type="file"]'] = makeEl(formSel + " input[type=file]");
}
const reDraft = getEl("#re-draft");
reDraft._qs[".re-t"] = makeEl("#re-draft .re-t");
reDraft._qs["button"] = makeEl("#re-draft button");

const toasts = [];
getEl("#toasts").append = (t) => toasts.push(t.textContent);
const revoked = [];
const fetchCalls = [];
let uploadGate = null;
let uploadFail = false;
let sayGate = null;
let sayFail = false;
let saySeq = 0;

function deferred() {
  let resolve, reject;
  const promise = new Promise((res, rej) => { resolve = res; reject = rej; });
  return { promise, resolve, reject };
}

class FakeURL extends URL {
  static createObjectURL(file) { return "blob:test/" + (file.name || "img"); }
  static revokeObjectURL(u) { revoked.push(u); }
}

const doc = {
  documentElement: makeEl("html"),
  body: makeEl("body"),
  hidden: false,
  createElement: (tag) => makeEl(tag),
  getElementById: (id) => getEl("#" + id),
  querySelector: (sel) => getEl(sel),
  querySelectorAll: () => [],
  addEventListener() {},
  hasFocus: () => false,
};

const ctx = {
  console: { log() {}, info() {}, warn() {}, error() {} },
  document: doc,
  window: null,
  location: { hash: "#/collie", search: "", pathname: "/", origin: "http://127.0.0.1", hostname: "127.0.0.1" },
  history: { state: {}, replaceState() {}, back() {} },
  navigator: { userAgent: "NodeTest" },
  localStorage: { getItem: () => null, setItem() {} },
  matchMedia: () => ({ matches: false, addEventListener() {} }),
  MutationObserver: class { observe() {} },
  EventSource: class { addEventListener() {} close() {} },
  CSS: { escape: (s) => String(s) },
  URL: FakeURL,
  URLSearchParams,
  crypto: { randomUUID: () => "uuid-" + (++saySeq) },
  performance: { now: () => 0 },
  requestAnimationFrame: () => 0,
  cancelAnimationFrame() {},
  setTimeout: () => 0,
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
    fetchCalls.push({ url: u, method: init.method || "GET", body: init.body });
    if (u.startsWith("/api/uploads")) {
      if (uploadGate) await uploadGate.promise;
      if (uploadFail) return errJSON(500, "Upload Failed", { error: "上传失败" });
      return okJSON({ path: "/tmp/uploads/50a667-image.png", name: "image.png", size: 1234 });
    }
    if (u.startsWith("/api/models")) {
      return okJSON({ models: [], efforts: [] });
    }
    if (u === "/api/super/say") {
      if (sayGate) await sayGate.promise;
      if (sayFail) return errJSON(500, "Server Error", { error: "发送失败" });
      const parsed = JSON.parse(init.body || "{}");
      return okJSON({ entry: "e" + (100 + fetchCalls.length), trigger: "t" + (100 + fetchCalls.length), client_msg_id: parsed.client_msg_id });
    }
    if (u.includes("/keys")) {
      return okJSON({ ok: true });
    }
    return okJSON({ ok: true });
  };
ctx.window = ctx;
ctx.globalThis = ctx;

const marker = "  boot();\n})();";
assert.ok(appSrc.includes(marker), "expected boot() tail in web/app.js");
const instrumented = appSrc.replace(marker, "  globalThis.__test = { sup, state, setReply, getReplyTo: () => replyTo, sendSuper, pendingSays, renderSuper, setView: (v) => { view = v; }, setCurrent: (c) => { current = c; } };\n})();");
vm.runInNewContext(instrumented, ctx, { filename: "web/app.js" });

const flush = () => new Promise((r) => setImmediate(r));
const settle = async (n = 8) => { for (let i = 0; i < n; i++) await flush(); };

(async () => {
  const form = getEl("#super-composer");
  const input = getEl("#super-input");
  const sendBtn = form._qs[".send"];

  // 1. Pasting an image and pressing Enter TWICE rapidly while upload is still in flight
  // must clear input & attachment chips synchronously and send ONLY ONE POST /api/super/say.
  uploadGate = deferred();
  sayGate = deferred();
  input.value = "带截图发一次测试";
  input.dispatch("paste", {
    clipboardData: { files: [{ name: "image.png", size: 1234, type: "image/png" }] },
  });
  assert.equal(form._files.length, 1, "pasted file queued");

  const k1 = input.dispatch("keydown", { key: "Enter", shiftKey: false, metaKey: false, ctrlKey: false, isComposing: false, keyCode: 13, repeat: false });
  assert.equal(k1.event.defaultPrevented, true);
  // Immediately after first synchronous tick:
  assert.equal(sendBtn.disabled, true, "send button disabled synchronously");
  assert.equal(input.value, "", "draft cleared synchronously on first tick");
  assert.equal(form._files.length, 0, "files detached synchronously on first tick");
  assert.equal(revoked.length, 0, "blob URL not revoked before send succeeds");

  // Hammer Enter again, repeat Enter, and programmatic requestSubmit while still uploading/sending:
  input.dispatch("keydown", { key: "Enter", shiftKey: false, metaKey: false, ctrlKey: false, isComposing: false, keyCode: 13, repeat: false });
  input.dispatch("keydown", { key: "Enter", shiftKey: false, metaKey: false, ctrlKey: false, isComposing: false, keyCode: 13, repeat: true });
  form.requestSubmit();

  uploadGate.resolve();
  await settle();
  // Even while /api/super/say is in flight, another submit must be ignored:
  form.requestSubmit();
  sayGate.resolve();
  await settle();

  const sayReqs = fetchCalls.filter((c) => c.url === "/api/super/say");
  assert.equal(sayReqs.length, 1, "expected exactly 1 /api/super/say POST, got " + sayReqs.length + "; toasts=" + JSON.stringify(toasts) + "; fetch=" + JSON.stringify(fetchCalls));
  const payload1 = JSON.parse(sayReqs[0].body);
  assert.equal(payload1.text, "带截图发一次测试\n\n附件：\n/tmp/uploads/50a667-image.png");
  assert.ok(payload1.client_msg_id && payload1.client_msg_id.length > 0, "client_msg_id sent");
  assert.equal(sendBtn.disabled, false, "send button re-enabled after finish");
  assert.equal(revoked.length, 1, "blob URL revoked after successful send");

  // 2. Empty Enter on #super-composer does nothing (0 POSTs).
  fetchCalls.length = 0;
  input.value = "   ";
  input.dispatch("keydown", { key: "Enter", shiftKey: false, metaKey: false, ctrlKey: false, isComposing: false, keyCode: 13, repeat: false });
  await settle();
  assert.equal(fetchCalls.filter((c) => c.url === "/api/super/say").length, 0, "empty super submit must not POST");

  // 3. Rollback on /api/super/say failure: draft, uploaded file (with intact blob URL), and replyTo all restore.
  uploadGate = null;
  sayGate = null;
  revoked.length = 0;
  fetchCalls.length = 0;
  ctx.__test.sup.entries.push({ id: "e1", role: "collie", text: "要继续吗？" });
  ctx.__test.setReply({ id: "e1", text: "要继续吗？" });
  input.value = "失败回滚验证";
  input.dispatch("paste", {
    clipboardData: { files: [{ name: "shot.png", size: 888, type: "image/png" }] },
  });
  await settle();
  sayFail = true;
  form.requestSubmit();
  await settle();
  assert.equal(input.value, "失败回滚验证", "draft restored after say failure");
  assert.equal(form._files.length, 1, "attachment restored after say failure");
  assert.equal(revoked.length, 0, "blob URL preserved on failure rollback");
  assert.equal(ctx.__test.getReplyTo()?.entry, "e1", "replyTo restored on failure");
  assert.equal(sendBtn.disabled, false, "send button unlocked after failure");

  // Retry succeeds without re-uploading the file:
  sayFail = false;
  fetchCalls.length = 0;
  form.requestSubmit();
  await settle();
  assert.equal(fetchCalls.filter((c) => c.url.startsWith("/api/uploads")).length, 0, "no duplicate re-upload on retry");
  assert.equal(fetchCalls.filter((c) => c.url === "/api/super/say").length, 1, "retry sends exactly once");
  assert.equal(input.value, "", "draft cleared after retry success");
  assert.equal(form._files.length, 0, "files cleared after retry success");
  assert.equal(revoked.length, 1, "blob URL revoked after retry success");

  // 4. Rollback when in-flight upload fails after user pressed Enter early.
  revoked.length = 0;
  fetchCalls.length = 0;
  uploadGate = deferred();
  uploadFail = true;
  input.value = "上传失败回滚";
  input.dispatch("paste", {
    clipboardData: { files: [{ name: "bad.png", size: 999, type: "image/png" }] },
  });
  form.requestSubmit();
  assert.equal(input.value, "", "draft detached during upload");
  assert.equal(form._files.length, 0, "files detached during upload");
  uploadGate.resolve();
  await settle();
  assert.equal(input.value, "上传失败回滚", "draft restored on upload failure");
  assert.equal(form._files.length, 1, "failed file chip restored so user can remove it");
  assert.ok(form._files[0].err, "restored file keeps error state");
  assert.equal(fetchCalls.filter((c) => c.url === "/api/super/say").length, 0, "must not POST /api/super/say when upload failed");
  form._clearFiles();
  uploadFail = false;
  uploadGate = null;

  // 5. Clicking a long --opt button rapidly twice sends full text (no ellipsis) ONLY ONCE,
  // and re-enables on error.
  fetchCalls.length = 0;
  const longOpt = "先按样例表（1,020w）跑完增量核验，你顺手提 256G 升配申请后再灌全量并补齐回归单测与监控看板检查";
  ctx.__test.sup.entries.push({ id: "e99", role: "collie", text: "怎么处理？", options: [longOpt, "先不用"] });
  const optBtn = makeEl("opt-btn");
  optBtn.dataset.opt = longOpt;
  const optGrp = makeEl("opt-grp");
  optGrp.dataset.entry = "e99";
  optGrp._qsa = { button: [optBtn] };
  optBtn.closest = (sel) => (sel === ".reply-opt[data-opt]" ? optBtn : sel === ".reply-opts[data-entry]" ? optGrp : null);

  sayGate = deferred();
  const threadEl = getEl("#super-thread");
  threadEl.dispatch("click", { target: optBtn });
  // Second rapid click even if DOM dataset.busy were cleared by an SSE re-render:
  delete optGrp.dataset.busy;
  threadEl.dispatch("click", { target: optBtn });
  sayGate.resolve();
  await settle();

  const optReqs = fetchCalls.filter((c) => c.url === "/api/super/say");
  assert.equal(optReqs.length, 1, "rapid double-click on --opt must send only 1 POST");
  const optPayload = JSON.parse(optReqs[0].body);
  assert.equal(optPayload.text, longOpt, "clicked --opt must send exact full text without ellipsis");
  assert.equal(optPayload.re, "e99");
  assert.ok(!optPayload.text.includes("…"), "no ellipsis in sent option text");
  assert.ok(optPayload.client_msg_id, "option click sends client_msg_id");

  // 6. Terminal #composer still allows empty Enter to send ["enter"] key.
  fetchCalls.length = 0;
  ctx.__test.state.agents.push({ pane_id: "w1:p1", is_agent: false });
  ctx.__test.setCurrent("w1:p1");
  getEl("#input").value = "";
  getEl("#composer").requestSubmit();
  await settle();
  assert.equal(fetchCalls.filter((c) => c.url.includes("/keys")).length, 1, "empty Enter on terminal composer sends enter key");

  console.log("OK: all composer & option JS regression checks passed");
})().catch((err) => {
  console.error(err);
  process.exit(1);
});
`
	cmd := exec.Command(nodeBin, "-e", jsTest, root)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("node JS regression test failed: %v\n%s", err, string(out))
	}
}
