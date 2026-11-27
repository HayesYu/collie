package collie

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"testing/fstest"
	"time"
)

func TestListDirsLocalFilteringHiddenLimitsAndRecent(t *testing.T) {
	realHome := t.TempDir()
	// Simulate a symlinked $HOME (e.g. /home/u -> /data00/home/u).
	linkRoot := t.TempDir()
	homeLink := filepath.Join(linkRoot, "user")
	if err := os.Symlink(realHome, homeLink); err != nil {
		t.Fatalf("symlink home: %v", err)
	}

	// Create sample directory structure.
	for _, rel := range []string{
		"agent-workspace/collie",
		"agent-workspace/Collie-Docs",
		"agent-workspace/beta",
		"agent-workspace/.git",
		"agent-workspace/.hidden-lab",
		".collie/router",
		".config/herdr",
		"zeta",
		"alpha",
	} {
		if err := os.MkdirAll(filepath.Join(realHome, rel), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	// Plain file should never appear in dirs list.
	if err := os.WriteFile(filepath.Join(realHome, "README.md"), []byte("hi"), 0o600); err != nil {
		t.Fatal(err)
	}
	// Symlink to a directory should be listed as a directory.
	if err := os.Symlink(filepath.Join(realHome, "alpha"), filepath.Join(realHome, "alpha-link")); err != nil {
		t.Fatal(err)
	}
	// Oversized directory (> 200 subdirs) to verify cap + truncated flag.
	bigDir := filepath.Join(realHome, "huge")
	if err := os.MkdirAll(bigDir, 0o755); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 230; i++ {
		if err := os.Mkdir(filepath.Join(bigDir, fmt.Sprintf("dir-%03d", i)), 0o755); err != nil {
			t.Fatal(err)
		}
	}

	// Wire a Super store with worker cwds on hub and on a remote machine ("mac").
	dbDir := t.TempDir()
	db, err := openDB(filepath.Join(dbDir, "herd.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	st := defaultState(homeLink)
	st.Settings.WorkerCwd = filepath.Join(realHome, "alpha") // via realHome alias, should dedup cleanly to ~/alpha
	st.Workers = map[string]*Worker{
		"w-hub-new": {Name: "w-hub-new", Machine: "", Cwd: filepath.Join(realHome, "agent-workspace/collie"), CreatedAt: time.Now().Add(-time.Minute)},
		"w-hub-old": {Name: "w-hub-old", Machine: "", Cwd: filepath.Join(homeLink, "zeta"), CreatedAt: time.Now().Add(-time.Hour)},
		"w-mac":     {Name: "w-mac", Machine: "mac", Cwd: "/Users/alice/projects/ios", CreatedAt: time.Now()},
	}
	sup := &Super{db: db, st: st, subs: map[chan struct{}]bool{}}

	srv := &Server{
		token: "tok",
		home:  homeLink,
		sup:   sup,
		herdr: newHerdr(filepath.Join(dbDir, "missing.sock"), "hub", homeLink),
		web:   fstest.MapFS{"index.html": {Data: []byte("ok")}, "app.js": {Data: []byte("")}, "app.css": {Data: []byte("")}},
	}
	handler := srv.Routes()

	getDirs := func(query string) DirListResponse {
		t.Helper()
		req := httptest.NewRequest(http.MethodGet, "/api/dirs"+query, nil)
		req.Header.Set("Authorization", "Bearer tok")
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		if rec.Code != 200 {
			t.Fatalf("GET /api/dirs%s -> %d: %s", query, rec.Code, rec.Body.String())
		}
		var out DirListResponse
		if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
			t.Fatalf("decode /api/dirs%s: %v", query, err)
		}
		return out
	}

	// 1. Home root: sorted case-insensitively, hides dotfiles, includes symlinked dir, populates recent without mac leaks.
	r := getDirs("?path=~")
	if !r.Exists || r.Display != "~" || r.BrowseDisplay != "~" {
		t.Fatalf("home listing unexpected: %+v", r)
	}
	wantHomeDirs := []string{"agent-workspace", "alpha", "alpha-link", "huge", "zeta"}
	if !slices.Equal(r.Dirs, wantHomeDirs) {
		t.Fatalf("home dirs = %v, want %v", r.Dirs, wantHomeDirs)
	}
	var recentDisplays []string
	for _, sc := range r.Recent {
		recentDisplays = append(recentDisplays, sc.Display)
	}
	for _, want := range []string{"~", "~/agent-workspace", "~/agent-workspace/collie", "~/zeta", "~/alpha"} {
		if !slices.Contains(recentDisplays, want) {
			t.Fatalf("recent %v missing expected %q", recentDisplays, want)
		}
	}
	if slices.Contains(recentDisplays, "/Users/alice/projects/ios") {
		t.Fatalf("hub recent leaked remote mac cwd: %v", recentDisplays)
	}

	// 2. Typing partial prefix filters parent dir cleanly and marks exists=false without 400 error.
	r = getDirs("?path=" + url.QueryEscape("~/agent-workspace/col"))
	if r.Exists {
		t.Fatalf("expected exists=false for partial prefix, got %+v", r)
	}
	if r.BrowseDisplay != "~/agent-workspace" || r.Prefix != "col" {
		t.Fatalf("unexpected browse/prefix: %+v", r)
	}
	if !slices.Equal(r.Dirs, []string{"collie", "Collie-Docs"}) {
		t.Fatalf("prefix dirs = %v, want [collie Collie-Docs]", r.Dirs)
	}

	// 3. Typing dot-prefix reveals hidden dirs automatically; explicit "/." reveals hidden dirs in that folder.
	r = getDirs("?path=" + url.QueryEscape("~/.c"))
	if !r.Hidden || !slices.Equal(r.Dirs, []string{".collie", ".config"}) {
		t.Fatalf("dot prefix dirs = %v (hidden=%v), want [.collie .config]", r.Dirs, r.Hidden)
	}
	r = getDirs("?path=" + url.QueryEscape("~/agent-workspace/."))
	if !r.Hidden || !slices.Contains(r.Dirs, ".hidden-lab") || !slices.Contains(r.Dirs, ".git") {
		t.Fatalf("explicit /. dirs = %v (hidden=%v), expected .git and .hidden-lab", r.Dirs, r.Hidden)
	}
	r = getDirs("?path=" + url.QueryEscape("~/agent-workspace") + "&hidden=1")
	if !r.Hidden || !slices.Contains(r.Dirs, ".hidden-lab") {
		t.Fatalf("hidden=1 dirs = %v, expected .hidden-lab", r.Dirs)
	}

	// 4. Deep non-existent path degrades gracefully to HTTP 200 with nearest existing ancestor.
	r = getDirs("?path=" + url.QueryEscape("~/does-not-exist/sub/new-proj"))
	if r.Exists || r.BrowseDisplay != "~" || r.Note == "" {
		t.Fatalf("deep missing path should degrade gracefully: %+v", r)
	}

	// 5. Huge directory caps at dirMaxResults (200) with truncated=true.
	r = getDirs("?path=" + url.QueryEscape("~/huge"))
	if !r.Truncated || len(r.Dirs) != dirMaxResults {
		t.Fatalf("huge dir len=%d truncated=%v, want %d/true", len(r.Dirs), r.Truncated, dirMaxResults)
	}
	if r.Dirs[0] != "dir-000" || r.Dirs[len(r.Dirs)-1] != "dir-199" {
		t.Fatalf("huge dir sort mismatch: first=%q last=%q", r.Dirs[0], r.Dirs[len(r.Dirs)-1])
	}
}

func TestListDirsRemoteNodeOnlineLegacyAndOffline(t *testing.T) {
	hubHome := t.TempDir()
	nodeHome := t.TempDir()
	for _, rel := range []string{"agent-workspace/mac-app", "Desktop", ".ssh"} {
		if err := os.MkdirAll(filepath.Join(nodeHome, rel), 0o755); err != nil {
			t.Fatal(err)
		}
	}

	nodeSrv := &Server{home: nodeHome, node: true, web: fstest.MapFS{}}
	nodeHTTP := httptest.NewServer(nodeSrv.NodeRoutes())
	defer nodeHTTP.Close()
	nodeURL, _ := url.Parse(nodeHTTP.URL)

	// Legacy remote node returning old v0.2.51 JSON shape (and 400 on missing path).
	legacyHTTP := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := r.URL.Query().Get("path")
		if strings.Contains(p, "missing") {
			writeJSON(w, 400, map[string]any{"error": "no such file or directory"})
			return
		}
		writeJSON(w, 200, map[string]any{
			"path":    "/Users/legacy",
			"display": "~",
			"parent":  "/Users",
			"dirs":    []string{"code", "notes"},
		})
	}))
	defer legacyHTTP.Close()
	legacyURL, _ := url.Parse(legacyHTTP.URL)

	herdr := newHerdr(filepath.Join(hubHome, "missing.sock"), "hub", hubHome)
	attachHTTP := func(id, home string, target *url.URL, online bool) {
		herdr.attach(&Machine{
			ID:     id,
			Name:   id,
			Home:   home,
			Online: online,
			client: &http.Client{
				Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
					cp := req.Clone(req.Context())
					cp.URL.Scheme = target.Scheme
					cp.URL.Host = target.Host
					cp.RequestURI = ""
					return http.DefaultTransport.RoundTrip(cp)
				}),
			},
		})
	}
	attachHTTP("mac", nodeHome, nodeURL, true)
	attachHTTP("legacy", "/Users/legacy", legacyURL, true)
	herdr.attach(&Machine{ID: "offline-box", Name: "offline-box", Home: "/Users/bob", Online: false})

	dbDir := t.TempDir()
	db, err := openDB(filepath.Join(dbDir, "herd.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	st := defaultState(hubHome)
	st.Workers = map[string]*Worker{
		"w-mac": {Name: "w-mac", Machine: "mac", Cwd: filepath.Join(nodeHome, "agent-workspace/mac-app"), CreatedAt: time.Now()},
		"w-off": {Name: "w-off", Machine: "offline-box", Cwd: "/Users/bob/work/repo", CreatedAt: time.Now()},
	}
	sup := &Super{db: db, st: st, subs: map[chan struct{}]bool{}}

	hubSrv := &Server{
		herdr: herdr,
		sup:   sup,
		token: "tok",
		home:  hubHome,
		web:   fstest.MapFS{"index.html": {Data: []byte("ok")}, "app.js": {Data: []byte("")}, "app.css": {Data: []byte("")}},
	}
	handler := hubSrv.Routes()

	call := func(query string) DirListResponse {
		t.Helper()
		req := httptest.NewRequest(http.MethodGet, "/api/dirs"+query, nil)
		req.Header.Set("Authorization", "Bearer tok")
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		if rec.Code != 200 {
			t.Fatalf("GET /api/dirs%s -> %d: %s", query, rec.Code, rec.Body.String())
		}
		var out DirListResponse
		if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
			t.Fatalf("decode: %v", err)
		}
		return out
	}

	// 1. Online new remote node merges node shortcuts + hub worker recent dirs.
	r := call("?machine=mac&path=~")
	if r.Offline || !r.Exists || !slices.Equal(r.Dirs, []string{"agent-workspace", "Desktop"}) {
		t.Fatalf("mac online response unexpected: %+v", r)
	}
	var macRecents []string
	for _, sc := range r.Recent {
		macRecents = append(macRecents, sc.Display)
	}
	for _, want := range []string{"~", "~/agent-workspace", "~/agent-workspace/mac-app"} {
		if !slices.Contains(macRecents, want) {
			t.Fatalf("mac recent %v missing %q", macRecents, want)
		}
	}

	// 2. Legacy node: 200 works seamlessly, and 400 on missing path degrades to 200 exists=false.
	r = call("?machine=legacy&path=~")
	if r.Offline || !r.Exists || !slices.Equal(r.Dirs, []string{"code", "notes"}) {
		t.Fatalf("legacy online response unexpected: %+v", r)
	}
	r = call("?machine=legacy&path=~/missing")
	if r.Offline || r.Exists || r.Note == "" {
		t.Fatalf("legacy missing path should degrade gracefully: %+v", r)
	}

	// 3. Offline node returns 200 offline=true with hub-known recent dirs and calm note.
	r = call("?machine=offline-box&path=~")
	if !r.Offline || r.Exists || len(r.Dirs) != 0 || r.Note == "" {
		t.Fatalf("offline response unexpected: %+v", r)
	}
	var offRecents []string
	for _, sc := range r.Recent {
		offRecents = append(offRecents, sc.Display)
	}
	if !slices.Contains(offRecents, "~/work/repo") {
		t.Fatalf("offline node should still include historical worker cwd in recent: %v", offRecents)
	}
}

func TestRouterPendingTracksAllLaunchSettings(t *testing.T) {
	dbDir := t.TempDir()
	db, err := openDB(filepath.Join(dbDir, "herd.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	st := defaultState(dbDir)
	st.Settings.RouterKind = "claude"
	st.Settings.RouterModel = "sonnet"
	st.Settings.RouterEffort = "medium"
	st.Settings.RouterArgs = nil
	st.Settings.AskPermissions = false

	sup := &Super{db: db, st: st, subs: map[chan struct{}]bool{}}
	// Simulate a pre-upgrade running router that only persisted router_run (no router_launch yet).
	sup.setRouterRun([2]string{"sonnet", "medium"})

	srv := &Server{
		token: "tok",
		home:  dbDir,
		sup:   sup,
		herdr: newHerdr(filepath.Join(dbDir, "missing.sock"), "hub", dbDir),
		web:   fstest.MapFS{"index.html": {Data: []byte("ok")}, "app.js": {Data: []byte("")}, "app.css": {Data: []byte("")}},
	}
	sup.s = srv
	sup.dir = dbDir
	handler := srv.Routes()

	save := func(s Settings) {
		t.Helper()
		b, _ := json.Marshal(map[string]any{"settings": s})
		req := httptest.NewRequest(http.MethodPost, "/api/super/settings", bytes.NewReader(b))
		req.Header.Set("Authorization", "Bearer tok")
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		if rec.Code != 200 {
			t.Fatalf("POST /api/super/settings -> %d: %s", rec.Code, rec.Body.String())
		}
	}

	orig := st.Settings

	// Initially matches running router -> not pending.
	sup.mu.Lock()
	if sup.routerPendingLocked("claude", sup.st.Settings) {
		sup.mu.Unlock()
		t.Fatal("expected pending=false initially")
	}
	sup.mu.Unlock()

	// 1. Changing ask_permissions must flip pending=true even from legacy router_run baseline.
	s1 := orig
	s1.AskPermissions = true
	save(s1)
	sup.mu.Lock()
	if !sup.routerPendingLocked("claude", sup.st.Settings) {
		sup.mu.Unlock()
		t.Fatal("expected pending=true after changing ask_permissions")
	}
	sup.mu.Unlock()

	// Reverting ask_permissions flips pending back to false.
	save(orig)
	sup.mu.Lock()
	if sup.routerPendingLocked("claude", sup.st.Settings) {
		sup.mu.Unlock()
		t.Fatal("expected pending=false after reverting ask_permissions")
	}
	sup.mu.Unlock()

	// 2. Changing router_args flips pending=true.
	s2 := orig
	s2.RouterArgs = []string{"--permission-mode", "plan"}
	save(s2)
	sup.mu.Lock()
	if !sup.routerPendingLocked("claude", sup.st.Settings) {
		sup.mu.Unlock()
		t.Fatal("expected pending=true after changing router_args")
	}
	sup.mu.Unlock()

	// 3. Changing router_kind flips pending=true; worker_cwd change alone does NOT flip pending.
	save(orig)
	s3 := orig
	s3.WorkerCwd = filepath.Join(dbDir, "custom-work")
	save(s3)
	sup.mu.Lock()
	if sup.routerPendingLocked("claude", sup.st.Settings) {
		sup.mu.Unlock()
		t.Fatal("worker_cwd change must not mark router pending")
	}
	sup.mu.Unlock()

	s4 := s3
	s4.RouterKind = "codex"
	save(s4)
	sup.mu.Lock()
	if !sup.routerPendingLocked("claude", sup.st.Settings) {
		sup.mu.Unlock()
		t.Fatal("router_kind change must mark router pending")
	}
	sup.mu.Unlock()

	// 4. When router is asleep (!Alive), superState keeps Pending=false.
	view := srv.superState(t.Context())
	if view.Router.Alive || view.Router.Pending {
		t.Fatalf("asleep router must not show pending: %+v", view.Router)
	}
}

func TestWebDirPickerAutoSaveAndFooterContracts(t *testing.T) {
	nodeBin, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node not installed; skipping JS unit tests")
	}
	root := filepath.Join("..", "..")
	jsTest := `
const assert = require("node:assert/strict");
const fs = require("node:fs");
const path = require("node:path");
const vm = require("node:vm");

const root = process.argv[1];
const html = fs.readFileSync(path.join(root, "web/index.html"), "utf8");
const css = fs.readFileSync(path.join(root, "web/app.css"), "utf8");
const appJs = fs.readFileSync(path.join(root, "web/app.js"), "utf8");

// 1. HTML structure contracts:
assert.match(html, /id="collie-pending-bar"/, "main chat must have #collie-pending-bar");
assert.match(html, /id="l-cwd"/, "launcher must keep #l-cwd input");
assert.doesNotMatch(html, /id="dir-up"/, "legacy launcher #dir-up should be replaced by unified picker");
const sideNavMatch = html.match(/<nav class="side-nav"[^>]*>([\s\S]*?)<\/nav>/);
assert.ok(sideNavMatch, "side-nav must exist");
const sideNavOrder = [...sideNavMatch[1].matchAll(/id="(nav-collie|nav-auto)"/g)].map((m) => m[1]);
assert.deepEqual(sideNavOrder, ["nav-collie", "nav-auto"], "side-nav must place 自动化 card directly under 和 Collie 对话");
const footMatch = html.match(/<footer class="side-foot">([\s\S]*?)<\/footer>/);
assert.ok(footMatch, "side-foot must exist");
const foot = footMatch[1];
assert.match(foot, /id="update-all-btn"/, "side-foot keeps update capsule");
assert.match(foot, /class="sf-row"/, "side-foot uses left-status + right-actions .sf-row");
const footOrder = [...foot.matchAll(/id="(nav-devices|nav-peers|nav-prefs)"/g)].map((m) => m[1]);
assert.deepEqual(footOrder, ["nav-devices", "nav-peers", "nav-prefs"], "side-foot keeps devices status on left and peers + prefs icons on right");
assert.doesNotMatch(foot, /id="nav-auto"/, "自动化 must live in .side-nav, not .side-foot");
assert.doesNotMatch(foot, /id="logout"/, "logout must be moved out of side-foot");

// 2. CSS contracts: green update capsule, left-flex device status + right icons & amber restart bar.
assert.match(css, /\.sf-row\s*\{[\s\S]*?display:\s*flex;/, ".sf-row flex layout must exist");
assert.match(css, /a\.foot-status\s*\{[\s\S]*?flex:\s*1 1 0%;[\s\S]*?min-width:\s*0;/, "expanded footer device status must expand horizontally on the left");
assert.match(css, /\.update-pill\s*\{[\s\S]*?color:\s*var\(--ok\);/, "update pill must use green var(--ok)");
assert.match(css, /\.router-pending-bar\s*\{/, "router-pending-bar styles must exist");

// 3. JS contracts: settings auto-save (no Save button), logout in prefs, dir picker helpers.
assert.match(appJs, /id="settings-pending-bar"/, "settings must render #settings-pending-bar");
assert.match(appJs, /id="settings-save-state"/, "settings must render #settings-save-state");
assert.doesNotMatch(appJs, /<div class="set-actions">[\s\S]*?>保存<\/button>/, "settings must not render bottom Save button");
assert.match(appJs, /data-act="logout"/, "prefs must expose logout button");
assert.match(appJs, /enhanceDirInput\(form\.worker_cwd/, "settings worker_cwd must use unified dir picker");
assert.match(appJs, /enhanceDirInput\(cwdInput/, "launcher #l-cwd must use unified dir picker");
assert.doesNotMatch(html, /<select[^>]*data-native/, "no HTML select may opt out of custom dropdown");
assert.doesNotMatch(appJs, /<select[^>]*data-native/, "no dynamic JS select may opt out of custom dropdown");

// 3b. Automation 5-tab contracts (memory, timers, inbox, integrations, settings):
const tabsMatch = html.match(/<div class="tabs" id="auto-tabs">([\s\S]*?)<\/div>/);
assert.ok(tabsMatch, "auto-tabs must exist");
const tabKeys = [...tabsMatch[1].matchAll(/data-tab="([^"]+)"/g)].map((m) => m[1]);
assert.deepEqual(tabKeys, ["memory", "timers", "inbox", "integrations", "settings"], "auto-tabs must contain memory, timers, inbox, integrations, settings");
assert.match(sideNavMatch[1], /id="nav-auto"[^>]*title="自动化 · 接入与定时"/, "side-nav #nav-auto keeps tooltip 自动化 · 接入与定时");
assert.doesNotMatch(appJs, /id="settings-hooks-fold"/, "hooks UI must not be rendered in settings");
assert.match(appJs, /id="inbox-thread-filter"/, "multi-thread filter must live in inbox");

// 3c. Mobile UI polish contracts (automation header, timer dialog & rows, settings alignment, sidebar mobile footer):
const autoHeadMatch = html.match(/<section class="view" id="v-auto"[\s\S]*?<header class="d-head">([\s\S]*?)<\/header>/);
assert.ok(autoHeadMatch, "v-auto header must exist");
assert.doesNotMatch(autoHeadMatch[1], /Always-on/, "v-auto header must drop verbose Always-on subtitle");
assert.match(html, /<dialog class="sheet tm-sheet" id="timer-modal" closedby="any" aria-labelledby="tm-modal-title">/, "timer modal must use native dialog with closedby=any");
assert.doesNotMatch(html, /id="nav-prefs-mobile"/, "sidebar header must keep only + on mobile; prefs gear stays in side-foot");
const sideHeadMatch = html.match(/<header class="side-head">([\s\S]*?)<\/header>/);
assert.ok(sideHeadMatch, "side-head must exist");
assert.match(sideHeadMatch[1], /id="new-btn"/, "side-head must keep #new-btn");
assert.doesNotMatch(sideHeadMatch[1], /href="#\/prefs"/, "side-head must not duplicate prefs gear");
assert.doesNotMatch(css, /\.side-foot\s+\.side-prefs-btn\s*\{\s*display:\s*none;/, "mobile side-foot must keep prefs gear visible");
assert.doesNotMatch(appJs, /class="set-top-meta"/, "settings must not reserve static top auto-save row");
assert.match(appJs, /id="settings-save-state"[^>]*hidden/, "settings save indicator must be hidden when idle");
assert.match(appJs, /data-act="timer-open"/, "timers tab must render compact + 新建定时 button");
assert.match(appJs, /class="tm-row/, "timers list must use full-width tm-row layout");
assert.match(css, /\.set-sec \.pick,\s*\.set-sec \.pick\.compact\s*\{[\s\S]*?width:\s*100%/, "settings dropdown triggers must stretch full width");
assert.match(html, /id="td-handle"/, "thread drawer must include mobile bottom-sheet grabber handle");
assert.match(css, /@media \(max-width:\s*760px\)\s*\{[\s\S]*?\.threadpane\s*\{[\s\S]*?border-radius:\s*20px 20px 0 0;[\s\S]*?transform:\s*translate(?:Y\(100%\)|3d\(0,\s*100%,\s*0\));/, "mobile thread drawer must render as rounded bottom sheet");

// 4. Pure helper logic tests extracted from app.js via vm.
const extractFn = (name) => {
  const idx = appJs.indexOf("function " + name + "(");
  assert.ok(idx >= 0, "missing function " + name);
  let depth = 0, i = appJs.indexOf("{", idx);
  for (; i < appJs.length; i++) {
    if (appJs[i] === "{") depth++;
    else if (appJs[i] === "}" && --depth === 0) return appJs.slice(idx, i + 1);
  }
  throw new Error("unclosed function " + name);
};

const sandbox = {
  state: { machines: [{ id: "", home: "/data00/home/alice" }, { id: "mac", home: "/Users/alice" }] },
  tilde: (p) => (p || "").replace(/^\/Users\/[^/]+|^\/home\/[^/]+/, "~"),
};
vm.runInNewContext([
  extractFn("tildeForMachine"),
  extractFn("joinDirPath"),
  extractFn("buildDirBreadcrumbs"),
  extractFn("normalizeAutoTab"),
  "this.api = { tildeForMachine, joinDirPath, buildDirBreadcrumbs, normalizeAutoTab };",
].join("\n"), sandbox);

const { tildeForMachine, joinDirPath, buildDirBreadcrumbs, normalizeAutoTab } = sandbox.api;
assert.equal(normalizeAutoTab("hooks"), "integrations");
assert.equal(normalizeAutoTab("integrations"), "integrations");
assert.equal(normalizeAutoTab("threads"), "inbox");
assert.equal(normalizeAutoTab("bogus"), "memory");
assert.equal(normalizeAutoTab("timers"), "timers");
assert.equal(normalizeAutoTab("settings"), "settings");
assert.equal(tildeForMachine("/data00/home/alice/agent-workspace/collie", ""), "~/agent-workspace/collie");
assert.equal(tildeForMachine("/Users/alice/code/app", "mac"), "~/code/app");
assert.equal(tildeForMachine("/opt/data", ""), "/opt/data");
assert.equal(joinDirPath("~", "agent-workspace"), "~/agent-workspace");
assert.equal(joinDirPath("~/agent-workspace/", "collie"), "~/agent-workspace/collie");
assert.equal(joinDirPath("/", "var"), "/var");
assert.deepEqual(JSON.parse(JSON.stringify(buildDirBreadcrumbs("~/agent-workspace/collie"))), [
  { label: "~", path: "~" },
  { label: "agent-workspace", path: "~/agent-workspace" },
  { label: "collie", path: "~/agent-workspace/collie" },
]);
assert.deepEqual(JSON.parse(JSON.stringify(buildDirBreadcrumbs("/var/log"))), [
  { label: "/", path: "/" },
  { label: "var", path: "/var" },
  { label: "log", path: "/var/log" },
]);
`
	cmd := exec.Command(nodeBin, "-e", jsTest, root)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("JS dir picker & UI contract test failed: %v\n%s", err, out)
	}
}
