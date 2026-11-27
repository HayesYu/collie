package collie

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"testing/fstest"
	"time"
)

func TestEntryClapsLosslessMigrationAndAPI(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "herd.db")

	// 1) Simulate a legacy SQLite DB created BEFORE the `claps` column existed.
	legacy, err := sql.Open("sqlite", "file:"+dbPath+"?_pragma=busy_timeout(5000)")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := legacy.Exec(`
		CREATE TABLE entries (id TEXT PRIMARY KEY, n INTEGER NOT NULL, at INTEGER NOT NULL, thread TEXT NOT NULL, data TEXT NOT NULL);
		CREATE INDEX entries_thread ON entries(thread, n);
	`); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Truncate(time.Second)
	seedEntries := []*Entry{
		{ID: "e1", Role: "user", Thread: mainThread, Text: "帮我检查一下分页边界", At: now.Add(-10 * time.Minute)},
		{ID: "e2", Role: "collie", Thread: mainThread, Text: "已经定位并修复了同一秒订单排序不稳定问题。", At: now.Add(-9 * time.Minute)},
		{ID: "e3", Role: "event", Thread: mainThread, Tone: "done", Text: "worker 完成", At: now.Add(-8 * time.Minute)},
		{ID: "e4", Role: "collie", Thread: mainThread, Text: "200 轮回归测试全部通过，P99 降到 28ms。", At: now.Add(-7 * time.Minute)},
	}
	for _, e := range seedEntries {
		b, _ := json.Marshal(e)
		if _, err := legacy.Exec(`INSERT INTO entries(id,n,at,thread,data) VALUES(?,?,?,?,?)`,
			e.ID, idNum(e.ID), e.At.Unix(), e.Thread, string(b)); err != nil {
			t.Fatal(err)
		}
	}
	if err := legacy.Close(); err != nil {
		t.Fatal(err)
	}

	// 2) Reopen through openDB: lossless migration must add `claps` column with default 0.
	db, err := openDB(dbPath)
	if err != nil {
		t.Fatalf("openDB after legacy schema: %v", err)
	}
	defer db.Close()

	st, err := loadState(db, dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(st.Entries) != 4 {
		t.Fatalf("loaded %d entries, want 4", len(st.Entries))
	}
	for _, e := range st.Entries {
		if e.Claps != 0 {
			t.Fatalf("legacy entry %s claps = %d, want 0", e.ID, e.Claps)
		}
	}

	herdr := newHerdr(filepath.Join(dir, "missing-herdr.sock"), "devbox", dir)
	herdr.local.conn.dial = func(context.Context) (net.Conn, error) { return nil, errors.New("offline") }
	srv := &Server{
		herdr: herdr,
		token: "test-tok",
		home:  dir,
		web:   fstest.MapFS{},
		auths: newAuth(db, "test-tok"),
	}
	sp := &Super{s: srv, db: db, dir: dir, st: st, subs: map[chan struct{}]bool{}}
	srv.sup = sp
	handler := srv.Routes()

	call := func(method, path, tok string, body any) *httptest.ResponseRecorder {
		var rd *bytes.Reader
		if body != nil {
			b, _ := json.Marshal(body)
			rd = bytes.NewReader(b)
		} else {
			rd = bytes.NewReader(nil)
		}
		req := httptest.NewRequest(method, path, rd)
		if tok != "" {
			req.Header.Set("Authorization", "Bearer "+tok)
		}
		if body != nil {
			req.Header.Set("Content-Type", "application/json")
		}
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		return rec
	}

	// Unauthenticated request rejected.
	if rec := call("POST", "/api/chat/clap", "", map[string]any{"entry_id": "e2"}); rec.Code != 401 {
		t.Fatalf("unauth POST /api/chat/clap = %d, want 401", rec.Code)
	}

	// Invalid IDs, negative claps, user/event entries, missing entry.
	for _, bad := range []map[string]any{
		{},
		{"entry_id": ""},
		{"entry_id": "t12"},
		{"entry_id": "e0"},
		{"entry_id": "e2", "claps": -1},
		{"entry_id": "e1", "delta": 1}, // user message
		{"entry_id": "e3", "delta": 1}, // event row
	} {
		if rec := call("POST", "/api/chat/clap", "test-tok", bad); rec.Code != 400 {
			t.Fatalf("POST /api/chat/clap %v = %d (%s), want 400", bad, rec.Code, rec.Body.String())
		}
	}
	if rec := call("POST", "/api/chat/clap", "test-tok", map[string]any{"entry_id": "e999", "delta": 1}); rec.Code != 404 {
		t.Fatalf("missing entry POST /api/chat/clap = %d, want 404", rec.Code)
	}

	// Subscribe to SSE wakeup notifications.
	ch, unsub := sp.subscribe()
	defer unsub()

	type clapResp struct {
		OK      bool   `json:"ok"`
		EntryID string `json:"entry_id"`
		Claps   int    `json:"claps"`
		Max     int    `json:"max"`
		Changed bool   `json:"changed"`
	}

	// Default single clap (omitting delta/claps/reset => +1).
	rec := call("POST", "/api/chat/clap", "test-tok", map[string]any{"entry_id": "e2"})
	if rec.Code != 200 {
		t.Fatalf("single clap = %d: %s", rec.Code, rec.Body.String())
	}
	var out clapResp
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if !out.OK || out.EntryID != "e2" || out.Claps != 1 || out.Max != maxEntryClaps || !out.Changed {
		t.Fatalf("single clap resp = %+v, want claps=1 changed=true", out)
	}
	select {
	case <-ch:
	default:
		t.Fatal("ClapEntry did not wake SSE subscribers on change")
	}

	// Debounced rapid-click batch delta (+4 => 5) using a paired browser token.
	code, _ := srv.auths.newCode("browser")
	_, browserTok, err := srv.auths.redeem(code, "browser", "Phone · Safari", "iOS")
	if err != nil {
		t.Fatal(err)
	}
	rec = call("POST", "/api/chat/clap", browserTok, map[string]any{"entry_id": "e2", "delta": 4})
	if rec.Code != 200 {
		t.Fatalf("batch delta clap = %d: %s", rec.Code, rec.Body.String())
	}
	out = clapResp{}
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	if !out.OK || out.Claps != 5 || !out.Changed {
		t.Fatalf("batch delta resp = %+v, want claps=5 changed=true", out)
	}
	select {
	case <-ch:
	default:
		t.Fatal("batch delta did not wake SSE subscribers")
	}

	// Cap clamping at maxEntryClaps (50) and no-op suppression when already at cap.
	rec = call("POST", "/api/chat/clap", "test-tok", map[string]any{"entry_id": "e2", "delta": 999})
	if rec.Code != 200 {
		t.Fatalf("cap clap = %d: %s", rec.Code, rec.Body.String())
	}
	out = clapResp{}
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	if out.Claps != maxEntryClaps || !out.Changed {
		t.Fatalf("cap clap resp = %+v, want claps=%d changed=true", out, maxEntryClaps)
	}
	select {
	case <-ch:
	default:
		t.Fatal("cap clap did not wake SSE")
	}

	rec = call("POST", "/api/chat/clap", "test-tok", map[string]any{"entry_id": "e2", "delta": 5})
	out = clapResp{}
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	if out.Claps != maxEntryClaps || out.Changed {
		t.Fatalf("already-at-cap resp = %+v, want claps=%d changed=false", out, maxEntryClaps)
	}
	select {
	case <-ch:
		t.Fatal("no-op clap at cap spuriously woke SSE")
	default:
	}

	// Absolute set (claps: 7) and reset (reset: true => 0).
	rec = call("POST", "/api/chat/clap", "test-tok", map[string]any{"entry_id": "e4", "claps": 7})
	if rec.Code != 200 {
		t.Fatalf("set claps e4 = %d: %s", rec.Code, rec.Body.String())
	}
	out = clapResp{}
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	if out.Claps != 7 || !out.Changed {
		t.Fatalf("set claps e4 = %+v, want claps=7 changed=true", out)
	}
	select {
	case <-ch:
	default:
	}

	rec = call("POST", "/api/chat/clap", "test-tok", map[string]any{"entry_id": "e2", "reset": true})
	if rec.Code != 200 {
		t.Fatalf("reset clap e2 = %d: %s", rec.Code, rec.Body.String())
	}
	out = clapResp{}
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	if out.Claps != 0 || !out.Changed {
		t.Fatalf("reset clap e2 = %+v, want claps=0 changed=true", out)
	}

	// Zero-token invariant: clapping/resetting MUST NEVER create triggers, wake router, or queue notifications.
	sp.mu.Lock()
	if len(sp.st.Triggers) != 0 {
		t.Fatalf("claps created %d triggers, want 0", len(sp.st.Triggers))
	}
	if !sp.routerAskedAt.IsZero() {
		t.Fatalf("claps touched routerAskedAt = %v, want zero", sp.routerAskedAt)
	}
	if len(sp.notifyBuf) != 0 {
		t.Fatalf("claps queued %d notifications, want 0", len(sp.notifyBuf))
	}
	sp.mu.Unlock()

	// GET /api/super reflects updated claps (e4=7, e2 omitted/0).
	snapRec := call("GET", "/api/super", "test-tok", nil)
	if snapRec.Code != 200 {
		t.Fatalf("GET /api/super = %d", snapRec.Code)
	}
	var snap superView
	if err := json.Unmarshal(snapRec.Body.Bytes(), &snap); err != nil {
		t.Fatal(err)
	}
	clapsByID := map[string]int{}
	for _, e := range snap.Entries {
		clapsByID[e.ID] = e.Claps
	}
	if clapsByID["e4"] != 7 || clapsByID["e2"] != 0 {
		t.Fatalf("super snapshot claps = %+v, want e4=7 e2=0", clapsByID)
	}

	// Persistence across fresh loadState + direct SQLite column verification.
	var colClaps int
	if err := db.QueryRow(`SELECT claps FROM entries WHERE id='e4'`).Scan(&colClaps); err != nil || colClaps != 7 {
		t.Fatalf("SQLite entries.claps for e4 = %d (err=%v), want 7", colClaps, err)
	}
	reloaded, err := loadState(db, dir)
	if err != nil {
		t.Fatal(err)
	}
	foundE4 := false
	for _, e := range reloaded.Entries {
		if e.ID == "e4" {
			foundE4 = true
			if e.Claps != 7 {
				t.Fatalf("reloaded e4.Claps = %d, want 7", e.Claps)
			}
		}
	}
	if !foundE4 {
		t.Fatal("e4 missing after reload")
	}

	// Older historical entry (not in memory sp.st.Entries) can also be clapped and read back via /api/super/earlier.
	oldEntry := &Entry{ID: "e100", Role: "collie", Thread: mainThread, Text: "更早的历史回答", At: now.Add(-20 * time.Minute)}
	if err := upsertEntryClaps(db, oldEntry); err != nil {
		t.Fatal(err)
	}
	rec = call("POST", "/api/chat/clap", "test-tok", map[string]any{"entry_id": "e100", "delta": 3})
	if rec.Code != 200 {
		t.Fatalf("historical clap e100 = %d: %s", rec.Code, rec.Body.String())
	}
	hist := entryHistory(db, mainThread, 101, 5)
	if len(hist) == 0 || hist[0].ID != "e100" || hist[0].Claps != 3 {
		t.Fatalf("entryHistory for e100 = %+v, want e100 claps=3", hist)
	}
}

func TestClapEntryRaceFreeWithSuperStateMarshal(t *testing.T) {
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
	st.Entries = append(st.Entries, &Entry{
		ID: "e1", Role: "collie", Thread: mainThread, Text: "concurrent clap target", At: time.Now(),
	})
	herdr := newHerdr(filepath.Join(dir, "missing-herdr.sock"), "devbox", dir)
	herdr.local.conn.dial = func(context.Context) (net.Conn, error) { return nil, errors.New("offline") }
	srv := &Server{herdr: herdr, token: "t", home: dir, web: fstest.MapFS{}, auths: newAuth(db, "t")}
	sp := &Super{s: srv, db: db, dir: dir, st: st, subs: map[chan struct{}]bool{}}
	srv.sup = sp

	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		for i := 0; i < 40; i++ {
			_, _, _ = sp.ClapEntry("e1", 1, nil)
		}
	}()
	go func() {
		defer wg.Done()
		ctx := context.Background()
		for i := 0; i < 40; i++ {
			v := srv.superState(ctx)
			if _, err := json.Marshal(v); err != nil {
				t.Errorf("marshal superState: %v", err)
			}
		}
	}()
	wg.Wait()
}

func TestDreamInputIncludesClapsSignal(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	db, err := openDB(filepath.Join(dir, "herd.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	st := defaultState(dir)
	now := time.Now()
	st.Entries = []*Entry{
		{ID: "e1", Role: "user", Thread: mainThread, Text: "帮我整理发布清单", At: now.Add(-10 * time.Minute)},
		{ID: "e2", Role: "collie", Thread: mainThread, Text: "清单一页纸版本已备好", At: now.Add(-9 * time.Minute), Claps: 8},
		{ID: "e3", Role: "collie", Thread: mainThread, Text: "普通确认收到", At: now.Add(-8 * time.Minute), Claps: 0},
		{ID: "e4", Role: "collie", Thread: mainThread, Text: "压测指标对比表也补齐了", At: now.Add(-7 * time.Minute), Claps: 3},
	}
	srv := &Server{home: dir, web: fstest.MapFS{}}
	sp := &Super{s: srv, db: db, dir: dir, st: st, subs: map[chan struct{}]bool{}}
	if err := sp.writeDreamInput(); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(filepath.Join(sp.routerDir(), "dream", "input.md"))
	if err != nil {
		t.Fatal(err)
	}
	txt := string(b)
	if !strings.Contains(txt, "Collie [claps:8]: 清单一页纸版本已备好") {
		t.Fatalf("dream input missing inline [claps:8]:\n%s", txt)
	}
	if strings.Contains(txt, "Collie [claps:0]") {
		t.Fatalf("dream input should not emit [claps:0]:\n%s", txt)
	}
	if !strings.Contains(txt, "### Replies the human applauded (positive feedback signal)") {
		t.Fatalf("dream input missing applauded summary section:\n%s", txt)
	}
	idx8 := strings.Index(txt, "- [claps:8]")
	idx3 := strings.Index(txt, "- [claps:3]")
	if idx8 < 0 || idx3 < 0 || idx8 > idx3 {
		t.Fatalf("applauded summary not sorted descending (idx8=%d, idx3=%d):\n%s", idx8, idx3, txt)
	}
}

func TestWebClapsContractsAndDebounce(t *testing.T) {
	t.Parallel()
	if _, err := exec.LookPath("node"); err != nil {
		t.Skip("node not installed")
	}
	script := `
const fs = require("fs");
const vm = require("vm");
const assert = require("assert");

const cssSrc = fs.readFileSync("web/app.css", "utf8");
const appSrc = fs.readFileSync("web/app.js", "utf8");
const demoSrc = fs.readFileSync("scripts/demo/demo.js", "utf8");

// 1) Strict monochrome SVG aesthetic audit: fine-line thumbs-up SVG (zero heart, zero colored system emoji).
const clapSection = appSrc.slice(appSrc.indexOf("const ICON_CLAP"), appSrc.indexOf("const ICON_COPY"));
assert.ok(clapSection.includes("<svg") && clapSection.includes('class="clap-ico"'), "ICON_CLAP must be an original monochrome inline SVG");
assert.ok(clapSection.includes('class="clap-body"') && clapSection.includes('class="clap-seam"') && clapSection.includes("M7 10v12"), "ICON_CLAP must use crisp fine-line thumbs-up SVG with cuff seam");
assert.ok(!clapSection.includes("M12 19.6C8.5 16.9"), "ICON_CLAP must not use legacy heart SVG path");
for (const forbidden of ["👍", "👏", "❤️", "❤", "🎉", "🔥"]) {
  assert.ok(!clapSection.includes(forbidden), "clap icon must not contain emoji " + forbidden);
}

// 2) Pure GPU micro-motion & reduced-motion contracts in CSS.
assert.ok(cssSrc.includes("@keyframes clap-ray"), "CSS must define 6-ray radial burst animation");
assert.ok(cssSrc.includes("@media (prefers-reduced-motion: no-preference)"), "CSS must opt in to spatial clap motion under prefers-reduced-motion: no-preference");
assert.ok(cssSrc.includes("@media (prefers-reduced-motion: reduce)"), "CSS must handle reduced motion cleanly");
assert.ok(cssSrc.includes("animation-timing-function: linear("), "CSS icon spring pop must use linear() physics easing with cubic-bezier fallback");
assert.ok(cssSrc.includes("font-variant-numeric: tabular-nums"), "clap count must use tabular-nums");
assert.ok(cssSrc.includes("#v-collie .say.has-claps .msg-acts"), "clapped state must stay persistently visible");
assert.ok(demoSrc.includes('p === "/api/chat/clap"'), "demo.js must simulate POST /api/chat/clap");

// 3) Verify playClapFx never forces synchronous layout reflow via offsetWidth/getBoundingClientRect.
const fxStart = appSrc.indexOf("function playClapFx(");
const fxEnd = appSrc.indexOf("async function flushClap(");
const fxBody = appSrc.slice(fxStart, fxEnd);
assert.ok(fxBody.includes("for (let i = 0; i < 6; i++)"), "playClapFx must spawn 6 radial hairline rays");
assert.ok(!fxBody.includes("offsetWidth") && !fxBody.includes("offsetHeight") && !fxBody.includes("getBoundingClientRect"), "playClapFx must not force synchronous layout reflow");

// 4) Runtime debounce, optimistic update, right-click reset, and zero /api/super/say verification.
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
  const listeners = new Map();
  const attrs = new Map();
  const styles = new Map();
  const el = {
    id, tagName: "DIV", value: "", textContent: "", innerHTML: "",
    hidden: false, disabled: false, checked: false, open: false, inert: false, tabIndex: 0,
    className: "", title: "",
    style: {
      setProperty(k, v) { styles.set(k, String(v)); },
      getPropertyValue(k) { return styles.get(k) || ""; },
      removeProperty(k) { styles.delete(k); },
    },
    dataset: {}, classList: makeClassList(), children: [], selectedOptions: [], options: [],
    scrollTop: 0, scrollHeight: 1200, clientHeight: 600, scrollWidth: 400, clientWidth: 400,
    offsetWidth: 400, offsetHeight: 98, offsetParent: {}, isConnected: true,
    _qs: {}, _qsa: {},
    addEventListener(type, fn) { if (!listeners.has(type)) listeners.set(type, []); listeners.get(type).push(fn); },
    removeEventListener(type, fn) { const l = listeners.get(type); if (!l) return; const i = l.indexOf(fn); if (i >= 0) l.splice(i, 1); },
    dispatch(type, ev = {}) {
      const e = { type, target: el, currentTarget: el, defaultPrevented: false, preventDefault() { this.defaultPrevented = true; }, stopPropagation() {}, composedPath: () => [el], ...ev };
      for (const fn of [...(listeners.get(type) || [])]) fn(e);
      return e;
    },
    querySelector(sel) { if (el._qs[sel]) return el._qs[sel]; const c = makeEl(id + ":" + sel); el._qs[sel] = c; return c; },
    querySelectorAll(sel) { return el._qsa[sel] || []; },
    setAttribute(k, v) { attrs.set(k, String(v)); },
    getAttribute(k) { return attrs.get(k) ?? ""; },
    removeAttribute(k) { attrs.delete(k); },
    hasAttribute(k) { return attrs.has(k); },
    toggleAttribute(k, force) { const on = force === undefined ? !attrs.has(k) : !!force; if (on) attrs.set(k, ""); else attrs.delete(k); return on; },
    closest() { return null; }, contains() { return false; },
    append() {}, appendChild(c) { el.children.push(c); return c; }, remove() {}, after() {}, focus() {}, click() {}, scrollIntoView() {},
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
  f._qs[".send"] = formSel === "#td-composer" ? getEl("#td-send") : makeEl(formSel + " .send");
  f._qs[".files"] = makeEl(formSel + " .files");
  f._qs[".attach"] = makeEl(formSel + " .attach");
  f._qs['input[type="file"]'] = makeEl(formSel + " input[type=file]");
}

const docListeners = new Map();
const clapButtons = [];
const document = {
  hidden: false, visibilityState: "visible",
  documentElement: makeEl("html"),
  body: makeEl("body"),
  activeElement: null,
  hasFocus: () => true,
  querySelector: (sel) => getEl(sel),
  querySelectorAll: (sel) => sel.includes(".clap-btn") ? clapButtons : [],
  getElementById: (id) => getEl("#" + id),
  createElement: (tag) => Object.assign(makeEl(tag), { tagName: tag.toUpperCase() }),
  addEventListener: (type, fn) => { if (!docListeners.has(type)) docListeners.set(type, []); docListeners.get(type).push(fn); },
  removeEventListener: () => {},
};
function dispatchDoc(type, ev) {
  const e = { type, defaultPrevented: false, preventDefault() { this.defaultPrevented = true; }, stopPropagation() {}, ...ev };
  for (const fn of [...(docListeners.get(type) || [])]) fn(e);
  return e;
}

const fetchCalls = [];
let serverClaps = 2;
const okJSON = (data) => ({ ok: true, status: 200, headers: { get: () => "" }, json: async () => data, text: async () => JSON.stringify(data) });
let lastES = null;
class FakeES {
  constructor(url) { this.url = url; this.ls = {}; lastES = this; }
  addEventListener(t, fn) { (this.ls[t] ||= []).push(fn); }
  emit(t, data) { for (const fn of (this.ls[t] || [])) fn({ type: t, data: JSON.stringify(data) }); }
  close() {}
}

let nowMs = Date.now();
const RealDate = Date;
class FakeDate extends RealDate {
  constructor(...args) {
    if (args.length === 0) super(nowMs);
    else super(...args);
  }
  static now() { return nowMs; }
}
let nextTimerId = 1;
const fakeTimers = new Map();
function fakeSetTimeout(fn, ms = 0, ...args) {
  const id = nextTimerId++;
  fakeTimers.set(id, { id, at: nowMs + Math.max(0, Number(ms) || 0), fn, args });
  return id;
}
function fakeClearTimeout(id) { fakeTimers.delete(id); }
async function flushMicrotasks() {
  for (let i = 0; i < 4; i++) await new Promise((r) => setImmediate(r));
}
async function wait(ms) {
  const target = nowMs + Math.max(0, Number(ms) || 0);
  await flushMicrotasks();
  while (true) {
    let earliest = null;
    for (const t of fakeTimers.values()) {
      if (t.at <= target && (!earliest || t.at < earliest.at || (t.at === earliest.at && t.id < earliest.id))) {
        earliest = t;
      }
    }
    if (!earliest) break;
    fakeTimers.delete(earliest.id);
    nowMs = earliest.at;
    if (typeof earliest.fn === "function") earliest.fn(...earliest.args);
    await flushMicrotasks();
  }
  nowMs = target;
  await flushMicrotasks();
}

const ctx = {
  window: null, document, location: { hash: "#/collie", search: "", pathname: "/", origin: "http://127.0.0.1:7788", href: "http://127.0.0.1:7788/#/collie" },
  history: { state: {}, replaceState() {}, pushState() {}, back() {} },
  navigator: { platform: "MacIntel", clipboard: { writeText: async () => {} } },
  isSecureContext: true, innerWidth: 1440, innerHeight: 900,
  visualViewport: { height: 900, offsetTop: 0, addEventListener() {} },
  localStorage: { _m: new Map(), getItem(k) { return this._m.has(k) ? this._m.get(k) : null; }, setItem(k, v) { this._m.set(k, String(v)); }, removeItem(k) { this._m.delete(k); } },
  CSS: { escape: (s) => String(s), supports: () => true },
  HTMLDialogElement: { prototype: {} },
  Blob, Uint8Array, TextEncoder, atob, AbortController,
  crypto: { randomUUID: () => "clap-test-uuid" },
  Event: class Event { constructor(t) { this.type = t; } },
  CustomEvent: class CustomEvent { constructor(t, d) { this.type = t; this.detail = d?.detail; } },
  ResizeObserver: class { observe() {} disconnect() {} },
  MutationObserver: class { observe() {} disconnect() {} },
  IntersectionObserver: class { observe() {} disconnect() {} },
  Notification: class { static permission = "default"; static async requestPermission() { return "default"; } },
  EventSource: FakeES,
  URL, URLSearchParams, Map, Set, WeakMap, Date: FakeDate, Math, JSON, Number, String, Boolean, Array, Object, Promise, Error, RegExp,
  setTimeout: fakeSetTimeout, clearTimeout: fakeClearTimeout, setInterval: () => 1, clearInterval: () => {}, queueMicrotask,
  requestAnimationFrame: (fn) => fakeSetTimeout(fn, 0), cancelAnimationFrame: fakeClearTimeout,
  performance: { now: () => nowMs },
  matchMedia: () => ({ matches: false, addEventListener() {} }),
  getComputedStyle: () => ({ getPropertyValue: () => "" }),
  getSelection: () => ({ isCollapsed: true, toString: () => "" }),
  addEventListener: () => {}, removeEventListener: () => {}, dispatchEvent: () => {},
  console,
  fetch: async (url, init = {}) => {
    const u = String(url);
    const body = init.body ? JSON.parse(init.body) : null;
    fetchCalls.push({ url: u, method: init.method || "GET", body });
    if (u === "/api/session") return okJSON({ ok: true });
    if (u === "/api/chat/clap") {
      if (body.reset) serverClaps = 0;
      else if (typeof body.claps === "number") serverClaps = Math.max(0, Math.min(50, body.claps));
      else serverClaps = Math.max(0, Math.min(50, serverClaps + (body.delta || 1)));
      return okJSON({ ok: true, entry_id: body.entry_id, claps: serverClaps, max: 50, changed: true });
    }
    return okJSON({ ok: true, agents: [], shells: [], machines: [], entries: [], pending: [], pinned: [], workers: [], router: { status: "idle" } });
  },
};
ctx.window = ctx;
ctx.globalThis = ctx;
const marker = "  boot();\n})();";
const instrumented = appSrc.replace(
  marker,
  "  globalThis.__test = { setView: (v) => { view = v; }, setSup: (s) => { sup = normSup(s); }, renderSuper, effectiveClaps };\n})();"
);
vm.runInNewContext(instrumented, ctx, { filename: "web/app.js" });

(async () => {
  const T = ctx.__test;
  const nowIso = new Date().toISOString();
  T.setView("collie");
  T.setSup({
    router: { status: "idle", kind: "claude" },
    entries: [{ id: "e10", role: "collie", thread: "chat:main", text: "修复完成了", at: nowIso, claps: 2 }],
    pending: [], pinned: [], workers: [],
  });
  T.renderSuper({ force: true });

  const threadHTML = getEl("#super-thread").innerHTML;
  assert.ok(threadHTML.includes('data-clap="e10"'), "Collie reply renders clap button");
  assert.ok(threadHTML.includes("has-claps"), "clapped Collie reply has persistent has-claps class");
  assert.ok(threadHTML.includes('<span class="clap-n">2</span>'), "renders initial count 2");

  // Wire mock button into document.querySelectorAll(".clap-btn...") and click 4 times rapidly!
  const sayEl = makeEl("say-e10");
  sayEl.classList.add("say", "has-claps");
  const btn = makeEl("clap-e10");
  btn.dataset = { clap: "e10", claps: "2" };
  btn.classList.add("clap-btn", "on");
  const wrap = makeEl("clap-wrap");
  wrap._qsa[".clap-burst"] = [];
  const num = makeEl("clap-num");
  num.textContent = "2";
  btn._qs[".clap-ico-wrap"] = wrap;
  btn._qs[".clap-n"] = num;
  btn.closest = (sel) => sel === ".clap-btn[data-clap]" ? btn : sel === ".say" ? sayEl : null;
  clapButtons.push(btn);

  fetchCalls.length = 0;
  for (let i = 0; i < 4; i++) {
    dispatchDoc("click", { target: btn });
    await wait(40);
  }
  // Optimistic DOM count immediately reflects 2 + 4 = 6 before debounce fires!
  assert.equal(num.textContent, "6", "optimistic count updates immediately to 6");
  assert.equal(fetchCalls.filter((c) => c.url === "/api/chat/clap").length, 0, "rapid clicks debounced before 350ms");

  // Wait past 350ms debounce window: MUST aggregate into ONE API call with delta=4!
  await wait(420);
  const clapCalls = fetchCalls.filter((c) => c.url === "/api/chat/clap");
  assert.equal(clapCalls.length, 1, "4 rapid clicks aggregate into exactly 1 POST /api/chat/clap");
  assert.deepStrictEqual(clapCalls[0].body, { entry_id: "e10", delta: 4 }, "debounced payload sends aggregated delta=4");
  assert.equal(serverClaps, 6, "server persisted claps = 6");

  // Right-click resets to 0 immediately with reset: true and toast confirmation!
  const ctxEv = dispatchDoc("contextmenu", { target: btn });
  assert.equal(ctxEv.defaultPrevented, true, "contextmenu prevented on clapped button");
  assert.equal(num.textContent, "", "count cleared to empty string when reset to 0");
  assert.equal(sayEl.classList.contains("has-claps"), false, "has-claps removed on reset");
  await wait(40);
  const allClapCalls = fetchCalls.filter((c) => c.url === "/api/chat/clap");
  assert.equal(allClapCalls.length, 2, "reset sends immediate POST /api/chat/clap");
  assert.deepStrictEqual(allClapCalls[1].body, { entry_id: "e10", reset: true }, "reset payload uses reset: true");

  // Long-press (520ms) also resets claps cleanly, and immediate re-clapping after pointerup is never swallowed!
  dispatchDoc("click", { target: btn });
  dispatchDoc("click", { target: btn });
  await wait(420);
  assert.equal(serverClaps, 2, "clapped twice to 2 before long-press");
  dispatchDoc("pointerdown", { target: btn, button: 0, clientX: 100, clientY: 100 });
  await wait(580);
  dispatchDoc("pointerup", { target: btn });
  assert.equal(num.textContent, "", "long-press resets count to empty");
  assert.equal(serverClaps, 0, "long-press persisted reset to 0");
  // Wait 180ms after pointerup (past the 120ms trailing click suppression window) and re-clap twice.
  await wait(180);
  dispatchDoc("click", { target: btn });
  dispatchDoc("click", { target: btn });
  await wait(420);
  assert.equal(num.textContent, "2", "re-clapping twice after long-press reset counts both clicks");
  assert.equal(serverClaps, 2, "re-clapping after long-press persisted 2");

  // Zero-token iron rule: ZERO /api/super/say calls emitted.
  assert.equal(fetchCalls.filter((c) => c.url === "/api/super/say").length, 0, "clapping/resetting must never call /api/super/say");
  console.log("OK");
})().catch((e) => { console.error(e); process.exit(1); });
`
	cmd := exec.Command("node", "-e", script)
	cmd.Dir = filepath.Join("..", "..")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("node clap runtime test failed: %v\n%s", err, out)
	}
	if !strings.Contains(string(out), "OK") {
		t.Fatalf("unexpected node output: %s", out)
	}
}
