package collie

import (
	"bufio"
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
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
	"unicode/utf8"
)

func TestCleanWorkerLabel(t *testing.T) {
	t.Parallel()
	cases := []struct {
		in, want string
	}{
		{"", ""},
		{"   \t\n  ", ""},
		{"修分页单测", "修分页单测"},
		{"  Fix   Auth\nFlow\t", "Fix Auth Flow"},
		{strings.Repeat("测", 80), strings.Repeat("测", workerLabelMaxRunes)},
	}
	for _, tc := range cases {
		got := cleanWorkerLabel(tc.in)
		if got != tc.want {
			t.Errorf("cleanWorkerLabel(%q) = %q, want %q", tc.in, got, tc.want)
		}
		if !utf8.ValidString(got) {
			t.Errorf("cleanWorkerLabel(%q) produced invalid UTF-8", tc.in)
		}
	}
}

func TestParseSpawnLabelFlag(t *testing.T) {
	t.Parallel()
	body, err := parseSpawn(
		[]string{"--name", "fix-pagination", "--label", "  修分页单测  ", "修复分页边界条件"},
		nil,
		true,
	)
	if err != nil {
		t.Fatalf("parseSpawn labeled: %v", err)
	}
	if body["name"] != "fix-pagination" || body["label"] != "修分页单测" || body["task"] != "修复分页边界条件" {
		t.Fatalf("unexpected labeled spawn body: %+v", body)
	}

	bodyEn, err := parseSpawn(
		[]string{"--name", "fix-auth", "--label", "Fix Auth", "fix token refresh"},
		nil,
		true,
	)
	if err != nil {
		t.Fatalf("parseSpawn english label: %v", err)
	}
	if bodyEn["label"] != "Fix Auth" {
		t.Fatalf("english label = %v, want Fix Auth", bodyEn["label"])
	}

	bodyPlain, err := parseSpawn([]string{"--name", "plain-wk", "do check"}, nil, true)
	if err != nil {
		t.Fatalf("parseSpawn unlabeled: %v", err)
	}
	if bodyPlain["label"] != "" {
		t.Fatalf("unlabeled spawn label = %v, want empty", bodyPlain["label"])
	}

	bodyResume, err := parseSpawn(
		[]string{"--resume", "fix-pagination", "--label", "补回归测试", "继续补单测"},
		nil,
		true,
	)
	if err != nil {
		t.Fatalf("parseSpawn resume with label: %v", err)
	}
	if bodyResume["resume"] != "fix-pagination" || bodyResume["label"] != "补回归测试" {
		t.Fatalf("unexpected resume body: %+v", bodyResume)
	}
}

func TestSQLiteWorkerLabelMigrationAndRoundTrip(t *testing.T) {
	t.Parallel()
	dbPath := filepath.Join(t.TempDir(), "legacy-herd.db")

	// Seed a legacy v0.3.4 database whose workers table lacks the `label` column.
	legacy, err := sql.Open("sqlite", "file:"+dbPath+"?_pragma=busy_timeout(5000)")
	if err != nil {
		t.Fatal(err)
	}
	legacySchema := `
CREATE TABLE kv       (k TEXT PRIMARY KEY, v TEXT NOT NULL);
CREATE TABLE triggers (id TEXT PRIMARY KEY, n INTEGER NOT NULL, at INTEGER NOT NULL,
                       status TEXT NOT NULL, source TEXT NOT NULL, thread TEXT NOT NULL, data TEXT NOT NULL);
CREATE TABLE threads  (id TEXT PRIMARY KEY, last_at INTEGER NOT NULL, data TEXT NOT NULL);
CREATE TABLE workers  (name TEXT PRIMARY KEY, created_at INTEGER NOT NULL, data TEXT NOT NULL);
CREATE TABLE entries  (id TEXT PRIMARY KEY, n INTEGER NOT NULL, at INTEGER NOT NULL, thread TEXT NOT NULL, data TEXT NOT NULL);
CREATE TABLE timers   (id TEXT PRIMARY KEY, data TEXT NOT NULL);
`
	if _, err := legacy.Exec(legacySchema); err != nil {
		legacy.Close()
		t.Fatal(err)
	}
	now := time.Now().Truncate(time.Second)
	oldWorkerJSON, _ := json.Marshal(map[string]any{
		"name":       "legacy-wk",
		"kind":       "codex",
		"cwd":        "/tmp",
		"task":       "legacy task",
		"thread":     mainThread,
		"created_by": "collie",
		"created_at": now,
		"status":     "working",
	})
	if _, err := legacy.Exec(`INSERT INTO workers(name, created_at, data) VALUES(?, ?, ?)`, "legacy-wk", now.Unix(), string(oldWorkerJSON)); err != nil {
		legacy.Close()
		t.Fatal(err)
	}
	legacy.Close()

	// Re-open through openDB: must migrate losslessly without dropping existing rows.
	db, err := openDB(dbPath)
	if err != nil {
		t.Fatalf("openDB on legacy db failed: %v", err)
	}
	defer db.Close()

	hasLabel, err := tableHasColumn(db, "workers", "label")
	if err != nil || !hasLabel {
		t.Fatalf("workers.label column missing after migration: has=%v err=%v", hasLabel, err)
	}

	st, err := loadState(db, "/home/x")
	if err != nil {
		t.Fatalf("loadState after migration: %v", err)
	}
	legacyLoaded := st.Workers["legacy-wk"]
	if legacyLoaded == nil || legacyLoaded.Label != "" || legacyLoaded.Task != "legacy task" {
		t.Fatalf("legacy worker damaged after migration: %+v", legacyLoaded)
	}

	// Save a labeled worker and verify both SQLite column and JSON payload round-trip.
	st.Workers["fix-pagination"] = &Worker{
		Name:      "fix-pagination",
		Label:     "修分页单测",
		Kind:      "codex",
		Cwd:       "/tmp",
		Task:      "修复分页边界",
		Thread:    mainThread,
		CreatedBy: "collie",
		CreatedAt: now,
		Status:    "working",
	}
	if err := saveState(db, st, nil); err != nil {
		t.Fatalf("saveState: %v", err)
	}

	var colLabel string
	if err := db.QueryRow(`SELECT label FROM workers WHERE name=?`, "fix-pagination").Scan(&colLabel); err != nil {
		t.Fatalf("query label column: %v", err)
	}
	if colLabel != "修分页单测" {
		t.Fatalf("workers.label column = %q, want 修分页单测", colLabel)
	}

	// Also verify a row whose label column is populated while JSON lacks `label` loads it.
	if _, err := db.Exec(`UPDATE workers SET label=? WHERE name=?`, "旧任务别名", "legacy-wk"); err != nil {
		t.Fatal(err)
	}

	reloaded, err := loadState(db, "/home/x")
	if err != nil {
		t.Fatalf("reload state: %v", err)
	}
	if reloaded.Workers["fix-pagination"] == nil || reloaded.Workers["fix-pagination"].Label != "修分页单测" {
		t.Fatalf("reloaded labeled worker = %+v", reloaded.Workers["fix-pagination"])
	}
	if reloaded.Workers["legacy-wk"] == nil || reloaded.Workers["legacy-wk"].Label != "旧任务别名" {
		t.Fatalf("reloaded column-labeled worker = %+v", reloaded.Workers["legacy-wk"])
	}
}

func TestWorkerLabelSpawnResumeStateListShowAndRouterDoc(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	workDir := filepath.Join(dir, "repo")
	if err := os.MkdirAll(workDir, 0o755); err != nil {
		t.Fatal(err)
	}
	// Provide a stub `claude` binary inside temp HOME/.local/bin so localAgents finds it deterministically.
	localBin := filepath.Join(dir, ".local", "bin")
	if err := os.MkdirAll(localBin, 0o755); err != nil {
		t.Fatal(err)
	}
	stubClaude := filepath.Join(localBin, "claude")
	if err := os.WriteFile(stubClaude, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	db, err := openDB(filepath.Join(dir, "herd.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	st, err := loadState(db, dir)
	if err != nil {
		t.Fatal(err)
	}
	st.Settings.WorkerKind = "claude"
	st.Settings.WorkerCwd = workDir

	type mockPane struct {
		wsID   string
		paneID string
		wsLbl  string
		name   string
		kind   string
		cwd    string
		status string
	}
	var mu sync.Mutex
	var wsSeq int
	var panes []mockPane
	var startedWorkspaces []string
	var startedAgentNames []string

	mockDial := func(ctx context.Context) (net.Conn, error) {
		c1, c2 := net.Pipe()
		go func() {
			defer c2.Close()
			rd := bufio.NewReader(c2)
			line, err := rd.ReadBytes('\n')
			if err != nil {
				return
			}
			var req struct {
				ID     string         `json:"id"`
				Method string         `json:"method"`
				Params map[string]any `json:"params"`
			}
			if err := json.Unmarshal(line, &req); err != nil {
				return
			}
			var res any
			mu.Lock()
			switch req.Method {
			case "workspace.create":
				wsSeq++
				wsID := fmt.Sprintf("w%d", wsSeq)
				paneID := fmt.Sprintf("w%d:p1", wsSeq)
				lbl, _ := req.Params["label"].(string)
				cwd, _ := req.Params["cwd"].(string)
				startedWorkspaces = append(startedWorkspaces, lbl)
				panes = append(panes, mockPane{wsID: wsID, paneID: paneID, wsLbl: lbl, cwd: cwd, status: "idle"})
				res = map[string]any{
					"workspace": map[string]any{"workspace_id": wsID},
					"root_pane": map[string]any{"pane_id": paneID},
				}
			case "agent.start":
				nm, _ := req.Params["name"].(string)
				kd, _ := req.Params["kind"].(string)
				pid, _ := req.Params["pane_id"].(string)
				startedAgentNames = append(startedAgentNames, nm)
				for i := range panes {
					if panes[i].paneID == pid {
						panes[i].name = nm
						panes[i].kind = kd
						panes[i].status = "working"
					}
				}
				res = map[string]any{"ok": true}
			case "workspace.list":
				ws := make([]map[string]any, 0, len(panes))
				for i, p := range panes {
					ws = append(ws, map[string]any{
						"workspace_id": p.wsID,
						"label":        p.wsLbl,
						"number":       i + 1,
						"agent_status": p.status,
						"pane_count":   1,
					})
				}
				res = map[string]any{"workspaces": ws}
			case "pane.list":
				ps := make([]map[string]any, 0, len(panes))
				for _, p := range panes {
					ps = append(ps, map[string]any{
						"pane_id":      p.paneID,
						"workspace_id": p.wsID,
						"agent":        p.kind,
						"agent_status": p.status,
						"cwd":          p.cwd,
					})
				}
				res = map[string]any{"panes": ps}
			case "agent.list":
				as := make([]map[string]any, 0, len(panes))
				for _, p := range panes {
					as = append(as, map[string]any{
						"name":         p.name,
						"pane_id":      p.paneID,
						"workspace_id": p.wsID,
						"agent":        p.kind,
						"agent_status": p.status,
						"cwd":          p.cwd,
					})
				}
				res = map[string]any{"agents": as}
			case "workspace.close":
				wsID, _ := req.Params["workspace_id"].(string)
				next := panes[:0]
				for _, p := range panes {
					if p.wsID != wsID {
						next = append(next, p)
					}
				}
				panes = next
				res = map[string]any{"ok": true}
			default:
				res = map[string]any{"ok": true}
			}
			mu.Unlock()
			resp, _ := json.Marshal(map[string]any{"id": req.ID, "result": res})
			_, _ = c2.Write(append(resp, '\n'))
		}()
		return c1, nil
	}

	herdr := newHerdr(filepath.Join(dir, "mock-herdr.sock"), "devbox", dir)
	herdr.local.conn.dial = mockDial
	srv := &Server{
		herdr: herdr,
		token: "test-tok",
		home:  dir,
		web:   fstest.MapFS{},
		auths: newAuth(db, "test-tok"),
	}
	sp := &Super{s: srv, db: db, dir: dir, bin: "collie", st: st, subs: map[chan struct{}]bool{}}
	srv.sup = sp
	handler := srv.Routes()

	call := func(method, path string, body any) *httptest.ResponseRecorder {
		var rd *bytes.Reader
		if body != nil {
			b, _ := json.Marshal(body)
			rd = bytes.NewReader(b)
		} else {
			rd = bytes.NewReader(nil)
		}
		req := httptest.NewRequest(method, path, rd)
		req.Header.Set("Authorization", "Bearer test-tok")
		if body != nil {
			req.Header.Set("Content-Type", "application/json")
		}
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		return rec
	}

	// 1) Spawn a labeled worker and an unlabeled worker.
	rec := call("POST", "/api/super/spawn", map[string]any{
		"kind":  "claude",
		"cwd":   workDir,
		"name":  "fix-pagination",
		"label": "修分页单测",
		"task":  "修复分页测试偶发失败",
	})
	if rec.Code != 202 {
		t.Fatalf("spawn labeled = %d: %s", rec.Code, rec.Body.String())
	}
	var spawnResp map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &spawnResp); err != nil {
		t.Fatal(err)
	}
	if spawnResp["worker"] != "fix-pagination" || spawnResp["label"] != "修分页单测" {
		t.Fatalf("spawn response = %+v, want worker=fix-pagination label=修分页单测", spawnResp)
	}

	rec2 := call("POST", "/api/super/spawn", map[string]any{
		"kind": "claude",
		"cwd":  workDir,
		"name": "plain-worker",
		"task": "无别名任务",
	})
	if rec2.Code != 202 {
		t.Fatalf("spawn plain = %d: %s", rec2.Code, rec2.Body.String())
	}
	var spawnResp2 map[string]any
	if err := json.Unmarshal(rec2.Body.Bytes(), &spawnResp2); err != nil {
		t.Fatal(err)
	}
	if _, has := spawnResp2["label"]; has {
		t.Fatalf("unlabeled spawn response should omit label, got %+v", spawnResp2)
	}

	// Underlying herdr must strictly receive ASCII worker names.
	mu.Lock()
	gotWS := append([]string(nil), startedWorkspaces...)
	gotNames := append([]string(nil), startedAgentNames...)
	mu.Unlock()
	if len(gotWS) != 2 || gotWS[0] != "fix-pagination" || gotWS[1] != "plain-worker" {
		t.Fatalf("herdr workspace labels = %v, want ASCII names", gotWS)
	}
	if len(gotNames) != 2 || gotNames[0] != "fix-pagination" || gotNames[1] != "plain-worker" {
		t.Fatalf("herdr agent names = %v, want ASCII names", gotNames)
	}

	// 2) GET /api/state exposes `label` for labeled workers and omits it when empty.
	stateRec := call("GET", "/api/state", nil)
	if stateRec.Code != 200 {
		t.Fatalf("GET /api/state = %d: %s", stateRec.Code, stateRec.Body.String())
	}
	var stateBody struct {
		OK     bool        `json:"ok"`
		Agents []AgentView `json:"agents"`
	}
	if err := json.Unmarshal(stateRec.Body.Bytes(), &stateBody); err != nil {
		t.Fatal(err)
	}
	byName := map[string]AgentView{}
	for _, a := range stateBody.Agents {
		byName[a.Name] = a
	}
	if byName["fix-pagination"].Label != "修分页单测" {
		t.Fatalf("GET /api/state fix-pagination label = %q, want 修分页单测", byName["fix-pagination"].Label)
	}
	if byName["plain-worker"].Label != "" {
		t.Fatalf("GET /api/state plain-worker label = %q, want empty", byName["plain-worker"].Label)
	}

	// 3) GET /api/super exposes worker.label; collie list & collie show include label cleanly.
	superRec := call("GET", "/api/super", nil)
	if superRec.Code != 200 {
		t.Fatalf("GET /api/super = %d", superRec.Code)
	}
	if !strings.Contains(superRec.Body.String(), `"label":"修分页单测"`) {
		t.Fatalf("GET /api/super missing label: %s", superRec.Body.String())
	}

	listOut := sp.List()
	if !strings.Contains(listOut, `fix-pagination`) || !strings.Contains(listOut, `label="修分页单测"`) {
		t.Fatalf("collie list missing labeled worker:\n%s", listOut)
	}
	for _, line := range strings.Split(listOut, "\n") {
		if strings.Contains(line, "plain-worker") && strings.Contains(line, "label=") {
			t.Fatalf("collie list should omit label= for unlabeled worker: %q", line)
		}
	}

	showOut, ok := sp.Show("fix-pagination")
	if !ok {
		t.Fatal("collie show fix-pagination returned !ok")
	}
	if !strings.Contains(showOut, "worker fix-pagination (claude)\nlabel: 修分页单测\n") {
		t.Fatalf("collie show labeled unexpected:\n%s", showOut)
	}
	if !strings.Contains(showOut, "herdr agent read fix-pagination ") {
		t.Fatalf("collie show terminal command must stay ASCII name:\n%s", showOut)
	}
	showPlain, ok := sp.Show("plain-worker")
	if !ok || strings.Contains(showPlain, "label:") {
		t.Fatalf("collie show unlabeled should omit label line:\n%s", showPlain)
	}

	// 4) --resume without --label preserves original label.
	sp.mu.Lock()
	sp.st.Workers["fix-pagination"].Ended = true
	sp.st.Workers["fix-pagination"].Status = "gone"
	sp.st.Workers["fix-pagination"].Session = "sess-1"
	sp.mu.Unlock()

	recKeep := call("POST", "/api/super/spawn", map[string]any{
		"resume": "fix-pagination",
		"task":   "继续补齐边界测试",
	})
	if recKeep.Code != 202 {
		t.Fatalf("resume keep label = %d: %s", recKeep.Code, recKeep.Body.String())
	}
	sp.mu.Lock()
	keptLabel := sp.st.Workers["fix-pagination"].Label
	sp.st.Workers["fix-pagination"].Ended = true
	sp.st.Workers["fix-pagination"].Status = "gone"
	sp.st.Workers["fix-pagination"].Session = "sess-2"
	sp.mu.Unlock()
	if keptLabel != "修分页单测" {
		t.Fatalf("--resume without --label lost label: got %q, want 修分页单测", keptLabel)
	}

	// 5) --resume with a new --label updates the label.
	recUpd := call("POST", "/api/super/spawn", map[string]any{
		"resume": "fix-pagination",
		"label":  "补回归测试",
		"task":   "跑完 200 轮回归",
	})
	if recUpd.Code != 202 {
		t.Fatalf("resume update label = %d: %s", recUpd.Code, recUpd.Body.String())
	}
	sp.mu.Lock()
	updatedLabel := sp.st.Workers["fix-pagination"].Label
	sp.mu.Unlock()
	if updatedLabel != "补回归测试" {
		t.Fatalf("--resume with new --label = %q, want 补回归测试", updatedLabel)
	}

	// 6) ROUTER.md template guides Collie to pass short --label with zero extra model calls.
	if err := sp.writeRouterFiles(); err != nil {
		t.Fatalf("writeRouterFiles: %v", err)
	}
	routerBytes, err := os.ReadFile(filepath.Join(sp.routerDir(), "ROUTER.md"))
	if err != nil {
		t.Fatal(err)
	}
	routerStr := string(routerBytes)
	for _, want := range []string{
		"[--label L]",
		"pass a concise `--label` in the human's language",
		"4–6 Chinese",
		"~2 English words",
		"zero extra",
		"spawn --resume WORKER [--label L]",
	} {
		if !strings.Contains(routerStr, want) {
			t.Fatalf("ROUTER.md missing worker label guidance %q:\n%s", want, routerStr)
		}
	}
}

func TestWebWorkerLabelUIContracts(t *testing.T) {
	t.Parallel()
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
const demoSrc = fs.readFileSync("docs/demo/demo.js", "utf8");

// Static markup & CSS checks
assert.ok(htmlSrc.includes('id="d-worker-id"'), "web/index.html must include #d-worker-id subtitle element");
assert.ok(cssSrc.includes(".d-meta > .d-worker-id"), "web/app.css must constrain .d-worker-id overflow");
assert.ok(demoSrc.includes('label: "查分页单测"') && demoSrc.includes('label: "写v1.4日志"'), "docs/demo/demo.js must include labeled demo workers");

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
    removeEventListener() {},
    querySelector(sel) { if (el._qs[sel]) return el._qs[sel]; const c = makeEl(id + ":" + sel); el._qs[sel] = c; return c; },
    querySelectorAll(sel) { return el._qsa[sel] || []; },
    setAttribute(k, v) { attrs.set(k, String(v)); },
    getAttribute(k) { return attrs.get(k) ?? ""; },
    removeAttribute(k) { attrs.delete(k); },
    hasAttribute(k) { return attrs.has(k); },
    toggleAttribute(k, force) { const on = force === undefined ? !attrs.has(k) : !!force; if (on) attrs.set(k, ""); else attrs.delete(k); return on; },
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
  f._qs[".send"] = formSel === "#td-composer" ? getEl("#td-send") : makeEl(formSel + " .send");
  f._qs[".files"] = makeEl(formSel + " .files");
  f._qs[".attach"] = makeEl(formSel + " .attach");
  f._qs['input[type="file"]'] = makeEl(formSel + " input[type=file]");
}
getEl("#re-draft")._qs[".re-t"] = makeEl("#re-draft .re-t");
getEl("#re-draft")._qs["button"] = makeEl("#re-draft button");
getEl("#super-search").hidden = true;
getEl("#collie-menu").hidden = true;
getEl("#file-view").hidden = true;

const storage = new Map();
const doc = {
  documentElement: makeEl("html"),
  body: makeEl("body"),
  hidden: false,
  hasFocus: () => true,
  createElement: (tag) => makeEl(tag),
  getElementById: (id) => getEl("#" + id),
  querySelector: (sel) => (sel === "dialog[open]" ? null : getEl(sel)),
  querySelectorAll: () => [],
  addEventListener() {},
  removeEventListener() {},
};
const ctx = {
  console, Math, Date, JSON, Set, Map, RegExp, Error, Promise, URL, URLSearchParams, Object, Array, Number, String, Boolean,
  setTimeout: () => 1, clearTimeout() {}, setInterval: () => 1, clearInterval() {}, queueMicrotask,
  requestAnimationFrame: (fn) => { fn(Date.now()); return 1; },
  cancelAnimationFrame() {},
  document: doc,
  location: { hash: "#/collie", origin: "http://127.0.0.1:7788", href: "http://127.0.0.1:7788/#/collie", pathname: "/", search: "" },
  history: { state: null, pushState() {}, replaceState() {}, back() {} },
  navigator: { platform: "MacIntel", clipboard: { writeText: async () => {} } },
  isSecureContext: true, innerWidth: 1440, innerHeight: 900,
  visualViewport: { height: 900, offsetTop: 0, addEventListener() {} },
  CSS: { escape: (s) => String(s), supports: () => true },
  HTMLDialogElement: { prototype: {} },
  Blob, Uint8Array, TextEncoder, atob, AbortController,
  crypto: { randomUUID: () => "v036-uuid" },
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
  fetch: async () => ({ ok: true, status: 200, json: async () => ({ ok: true }) }),
};
ctx.window = ctx;
ctx.globalThis = ctx;

const marker = "  boot();\n})();";
const instrumented = appSrc.replace(
  marker,
  "  globalThis.__test = { setView: (v) => { view = v; }, setCurrent: (p) => { current = p; }, setState: (s) => { state = s; }, setSup: (s) => { sup = normSup(s); }, renderSuper, renderSide, renderHead, renderThreadWorkers, computeReplyThreads, itemHTML, title };\n})();"
);
vm.runInNewContext(instrumented, ctx, { filename: "web/app.js" });
const T = ctx.__test;

const nowIso = new Date().toISOString();
T.setState({
  ok: true,
  machines: [{ id: "", name: "devbox", local: true, online: true, home: "/home/alex" }],
  shells: [],
  agents: [
    { pane_id: "w1:p1", workspace_id: "w1", name: "collie", kind: "claude", status: "idle", cwd: "/home/alex/.collie/router", is_agent: true, machine: "" },
    { pane_id: "w2:p1", workspace_id: "w2", name: "fix-pagination", label: "修分页单测", kind: "codex", status: "working", cwd: "/home/alex/code/shop-api", is_agent: true, machine: "" },
    { pane_id: "w3:p1", workspace_id: "w3", name: "plain-worker", kind: "claude", status: "working", cwd: "/home/alex/code/shop-web", is_agent: true, machine: "" },
  ],
});
T.setSup({
  router: { status: "idle", alive: true },
  entries: [
    { id: "e1", role: "user", text: "修一下分页测试", at: nowIso },
    { id: "e2", role: "collie", text: "已派 fix-pagination 去修", at: nowIso, re: { entry: "e1", who: "你", text: "修一下分页测试" } },
    { id: "e3", role: "event", tone: "spawn", ref: "fix-pagination", text: "派出 fix-pagination（codex）：修复分页测试偶发失败", at: nowIso },
  ],
  workers: [
    { name: "fix-pagination", label: "修分页单测", kind: "codex", status: "working", pane_id: "w2:p1", cwd: "/home/alex/code/shop-api", task: "修复 TestOrdersPagination 偶发失败", thread: "chat:main", created_at: nowIso },
    { name: "plain-worker", kind: "claude", status: "working", pane_id: "w3:p1", cwd: "/home/alex/code/shop-web", task: "更新构建脚本", thread: "chat:main", created_at: nowIso },
  ],
  pending: [],
  pinned: [],
});

// 1) Crew strip prioritizes label, keeps ASCII name + task summary in title, falls back to name when unlabeled
T.setView("collie");
T.renderSuper({ force: true });
const crewHtml = getEl("#crew").innerHTML;
assert.ok(crewHtml.includes('data-worker="fix-pagination"'), "crew chip must preserve ASCII data-worker");
assert.ok(crewHtml.includes('<span class="chip-name">修分页单测</span>'), "crew chip must show worker label");
assert.ok(crewHtml.includes('title="fix-pagination · codex · 工作中 — 修复 TestOrdersPagination 偶发失败"'), "crew chip title must show ASCII name and task summary, got: " + crewHtml);
assert.ok(crewHtml.includes('<span class="chip-name">plain-worker</span>'), "unlabeled worker in crew strip must fall back to ASCII name");

// 2) Left sidebar worker list prioritizes label, exposes ASCII name & task summary in title, falls back cleanly
T.renderSide();
const liveHtml = getEl("#live-list").innerHTML;
assert.ok(liveHtml.includes('>修分页单测</span><span class="by-collie">Collie 派出</span>'), "sidebar row must prioritize worker label");
assert.ok(liveHtml.includes('title="修分页单测 (fix-pagination) — 修复 TestOrdersPagination 偶发失败"'), "sidebar row title must contain ASCII name and task summary, got: " + liveHtml);
assert.ok(liveHtml.includes('>plain-worker</span><span class="by-collie">Collie 派出</span>'), "unlabeled sidebar row must fall back to ASCII name");

// 3) Worker detail page header shows label + subtle ASCII name subtitle, and hides subtitle when unlabeled
T.setView("pane");
T.setCurrent("w2:p1");
T.renderHead();
assert.equal(getEl("#d-name").textContent, "修分页单测", "detail #d-name must show label");
assert.equal(getEl("#d-worker-id").hidden, false, "detail #d-worker-id must be visible when worker has alias label");
assert.equal(getEl("#d-worker-id").textContent, "fix-pagination", "detail #d-worker-id must show ASCII worker name");

T.setCurrent("w3:p1");
T.renderHead();
assert.equal(getEl("#d-name").textContent, "plain-worker", "unlabeled detail #d-name must fall back to ASCII name");
assert.equal(getEl("#d-worker-id").hidden, true, "unlabeled detail #d-worker-id must stay hidden");

// 4) Thread drawer worker pill shows label while preserving ASCII data-td-worker and hover title
const threadEntries = [
  { id: "e1", role: "user", text: "修一下分页测试", at: nowIso },
  { id: "e2", role: "collie", text: "已派 fix-pagination 去修", at: nowIso, re: { entry: "e1", who: "你", text: "修一下分页测试" } },
  { id: "e3", role: "user", text: "好的，修完跑 200 轮压测", at: nowIso, re: { entry: "e2", who: "Collie", text: "已派 fix-pagination 去修" } },
];
const info = T.computeReplyThreads(threadEntries);
const th = info.threads.get("e1");
assert.ok(th, "3-message thread must exist");
T.renderThreadWorkers(th, info, threadEntries, true);
const tdHtml = getEl("#td-workers").innerHTML;
assert.ok(tdHtml.includes('data-td-worker="fix-pagination"'), "thread drawer pill must preserve ASCII data-td-worker");
assert.ok(tdHtml.includes('<span class="td-worker-name">修分页单测</span>'), "thread drawer pill must display label");
assert.ok(tdHtml.includes('title="fix-pagination · codex · 工作中 — 修复 TestOrdersPagination 偶发失败"'), "thread drawer pill title must expose ASCII name + task summary, got: " + tdHtml);
`
	cmd := exec.Command("node", "-e", script)
	cmd.Dir = filepath.Join("..", "..")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("node UI contract test failed: %v\n%s", err, out)
	}
}
