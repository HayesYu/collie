package collie

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"testing"
	"time"
)

func mockAgyConnectServer(t *testing.T, tlsMode bool, csrf string, owned map[string]bool, acceptUnlisted bool) (*httptest.Server, *[]string, *sync.Mutex) {
	t.Helper()
	var mu sync.Mutex
	var sent []string
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Header.Get("x-codeium-csrf-token") != csrf {
			w.WriteHeader(403)
			json.NewEncoder(w).Encode(map[string]string{"code": "permission_denied", "message": "bad csrf"})
			return
		}
		var req map[string]any
		json.NewDecoder(r.Body).Decode(&req)
		switch r.URL.Path {
		case agyService + "GetAllCascadeTrajectories":
			sums := map[string]any{}
			for id := range owned {
				sums[id] = map[string]any{"status": "CASCADE_RUN_STATUS_IDLE"}
			}
			json.NewEncoder(w).Encode(map[string]any{"trajectorySummaries": sums})
		case agyService + "GetCascadeTrajectorySteps":
			id, _ := req["cascadeId"].(string)
			if owned[id] {
				json.NewEncoder(w).Encode(map[string]any{"steps": []any{}})
				return
			}
			w.WriteHeader(404)
			json.NewEncoder(w).Encode(map[string]string{"code": "not_found", "message": "cascade not found"})
		case agyService + "SendUserCascadeMessage":
			id, _ := req["cascadeId"].(string)
			if !owned[id] && !acceptUnlisted {
				w.WriteHeader(404)
				json.NewEncoder(w).Encode(map[string]string{"code": "not_found", "message": "cascade not found"})
				return
			}
			mu.Lock()
			sent = append(sent, id)
			mu.Unlock()
			json.NewEncoder(w).Encode(map[string]any{"ok": true})
		default:
			w.WriteHeader(404)
			json.NewEncoder(w).Encode(map[string]string{"code": "unimplemented", "message": "unknown method"})
		}
	})
	var ts *httptest.Server
	if tlsMode {
		ts = httptest.NewTLSServer(handler)
	} else {
		ts = httptest.NewServer(handler)
	}
	t.Cleanup(ts.Close)
	return ts, &sent, &mu
}

func portOfURL(t *testing.T, raw string) int {
	t.Helper()
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	p, err := strconv.Atoi(u.Port())
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func TestFindAgyDaemonServersAndMultiInstanceRouting(t *testing.T) {
	home := t.TempDir()
	daemonDir := filepath.Join(home, ".gemini", "antigravity", "daemon")
	if err := os.MkdirAll(daemonDir, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(forgetAgyServer)
	forgetAgyServer()

	// 1) Remote ls_ws1 (HTTP), owns conv-1.
	ls1, sent1, mu1 := mockAgyConnectServer(t, false, "csrf-ls1", map[string]bool{"conv-1": true}, false)
	// 2) Remote ls_ws2 (HTTPS-only self-signed loopback), owns conv-2.
	ls2, sent2, mu2 := mockAgyConnectServer(t, true, "csrf-ls2", map[string]bool{"conv-2": true}, false)
	// 3) Local hub_1 (HTTP), accepts unlisted conv-3.
	hub, sentHub, muHub := mockAgyConnectServer(t, false, "csrf-hub", map[string]bool{}, true)

	pid := os.Getpid()
	writeJSONFile := func(name string, v any) {
		b, _ := json.Marshal(v)
		if err := os.WriteFile(filepath.Join(daemonDir, name), b, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	writeJSONFile("ls_ws1.json", map[string]any{"pid": pid, "httpPort": portOfURL(t, ls1.URL), "csrfToken": "csrf-ls1"})
	writeJSONFile("ls_ws2.json", map[string]any{"pid": pid, "httpPort": 0, "httpsPort": portOfURL(t, ls2.URL), "csrfToken": "csrf-ls2"})
	writeJSONFile("hub_1.json", map[string]any{"pid": pid, "httpPort": portOfURL(t, hub.URL), "csrfToken": "csrf-hub"})
	// Stale discovery file for a non-existent PID must be ignored.
	writeJSONFile("ls_dead.json", map[string]any{"pid": 99999999, "httpPort": portOfURL(t, ls1.URL), "csrfToken": "csrf-dead"})

	srv := &Server{home: home}
	servers := srv.agyDesktopServers()
	if len(servers) != 3 {
		t.Fatalf("discovered %d servers, want 3", len(servers))
	}
	if !servers[0].hub {
		t.Fatalf("expected hub_*.json first, got %+v", servers[0])
	}

	ctx := context.Background()
	if err := srv.agyAppSend(ctx, "conv-1", "hello ls1"); err != nil {
		t.Fatalf("send conv-1: %v", err)
	}
	if err := srv.agyAppSend(ctx, "conv-2", "hello ls2 https"); err != nil {
		t.Fatalf("send conv-2: %v", err)
	}
	if err := srv.agyAppSend(ctx, "conv-3", "hello hub fallback"); err != nil {
		t.Fatalf("send conv-3: %v", err)
	}

	mu1.Lock()
	got1 := append([]string(nil), (*sent1)...)
	mu1.Unlock()
	mu2.Lock()
	got2 := append([]string(nil), (*sent2)...)
	mu2.Unlock()
	muHub.Lock()
	gotHub := append([]string(nil), (*sentHub)...)
	muHub.Unlock()

	if len(got1) != 1 || got1[0] != "conv-1" {
		t.Fatalf("ls1 sent = %v, want [conv-1]", got1)
	}
	if len(got2) != 1 || got2[0] != "conv-2" {
		t.Fatalf("ls2 sent = %v, want [conv-2]", got2)
	}
	if len(gotHub) != 1 || gotHub[0] != "conv-3" {
		t.Fatalf("hub sent = %v, want [conv-3]", gotHub)
	}
}

func initAgySummaryDB(t *testing.T, root string, rows []struct {
	id, title, uri, status string
	notIdle                int
}) {
	t.Helper()
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", filepath.Join(root, "conversation_summaries.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	_, err = db.Exec(`CREATE TABLE conversation_summaries (
		conversation_id TEXT,
		title TEXT,
		preview TEXT,
		workspace_uris TEXT,
		status TEXT,
		not_fully_idle INTEGER,
		nesting_depth INTEGER,
		killed INTEGER,
		last_modified_time TEXT,
		last_user_input_time TEXT
	)`)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Format("2006-01-02 15:04:05")
	for _, r := range rows {
		uris, _ := json.Marshal([]string{r.uri})
		_, err := db.Exec(`INSERT INTO conversation_summaries VALUES (?, ?, '', ?, ?, ?, 0, 0, ?, ?)`,
			r.id, r.title, string(uris), r.status, r.notIdle, now, now)
		if err != nil {
			t.Fatal(err)
		}
	}
}

func TestAgyDesktopSessionsCanSendAndCanTakeOver(t *testing.T) {
	home := t.TempDir()
	agRoot := filepath.Join(home, ".gemini", "antigravity")
	wsDir := filepath.Join(home, "proj")
	os.MkdirAll(wsDir, 0o755)
	initAgySummaryDB(t, agRoot, []struct {
		id, title, uri, status string
		notIdle                int
	}{
		{"aaaa1111-1111-1111-1111-111111111111", "running task", "file://" + wsDir, "CASCADE_RUN_STATUS_RUNNING", 0},
		{"bbbb2222-2222-2222-2222-222222222222", "waiting task", "file://" + wsDir, "CASCADE_RUN_STATUS_IDLE", 1},
		{"cccc3333-3333-3333-3333-333333333333", "idle task", "file://" + wsDir, "CASCADE_RUN_STATUS_IDLE", 0},
	})
	t.Cleanup(func() {
		forgetAgyServer()
		forgetDesktopRows()
	})
	forgetAgyServer()
	forgetDesktopRows()

	srv := &Server{home: home}

	// Without any daemon running: CanSend=false, idle session has CanTakeOver=true, live sessions do not.
	list := srv.desktopSessions(nil, map[string]bool{}, time.Now())
	bySess := map[string]*External{}
	for _, e := range list {
		bySess[e.Session] = e
	}
	if len(bySess) != 3 {
		t.Fatalf("got %d agy desktop sessions, want 3", len(bySess))
	}
	for _, id := range []string{"aaaa1111-1111-1111-1111-111111111111", "bbbb2222-2222-2222-2222-222222222222"} {
		e := bySess[id]
		if !e.Live || e.CanSend || e.CanTakeOver || e.ReadOnly == "" {
			t.Fatalf("live session %s without daemon: live=%v send=%v take=%v ro=%q", id, e.Live, e.CanSend, e.CanTakeOver, e.ReadOnly)
		}
	}
	idle := bySess["cccc3333-3333-3333-3333-333333333333"]
	if idle.Live || idle.CanSend || !idle.CanTakeOver || idle.ReadOnly == "" {
		t.Fatalf("idle session without daemon: live=%v send=%v take=%v ro=%q", idle.Live, idle.CanSend, idle.CanTakeOver, idle.ReadOnly)
	}

	// With remote ls_*.json daemon running: CanSend=true on all; CanTakeOver=true only on idle.
	ls, _, _ := mockAgyConnectServer(t, false, "csrf-remote", map[string]bool{"cccc3333-3333-3333-3333-333333333333": true}, true)
	daemonDir := filepath.Join(agRoot, "daemon")
	os.MkdirAll(daemonDir, 0o755)
	b, _ := json.Marshal(map[string]any{"pid": os.Getpid(), "httpPort": portOfURL(t, ls.URL), "csrfToken": "csrf-remote"})
	os.WriteFile(filepath.Join(daemonDir, "ls_remote.json"), b, 0o600)
	forgetAgyServer()
	forgetDesktopRows()

	list2 := srv.desktopSessions(nil, map[string]bool{}, time.Now())
	bySess2 := map[string]*External{}
	for _, e := range list2 {
		bySess2[e.Session] = e
	}
	if !bySess2["aaaa1111-1111-1111-1111-111111111111"].CanSend || bySess2["aaaa1111-1111-1111-1111-111111111111"].CanTakeOver {
		t.Fatalf("running session with ls daemon: %+v", bySess2["aaaa1111-1111-1111-1111-111111111111"])
	}
	idle2 := bySess2["cccc3333-3333-3333-3333-333333333333"]
	if !idle2.CanSend || idle2.via != "app" || !idle2.CanTakeOver {
		t.Fatalf("idle session with ls daemon: send=%v via=%q take=%v", idle2.CanSend, idle2.via, idle2.CanTakeOver)
	}
}

func TestAgyExternalArgsSymlinkAndRememberedSession(t *testing.T) {
	cid := "503fff43-ae8a-49c2-a8c1-8f0f4f7262cd"
	for _, tc := range []struct {
		args []string
		want string
	}{
		{[]string{"agy", "--conversation", cid}, cid},
		{[]string{"agy", "--conversation=" + cid}, cid},
		{[]string{"agy", "--resume", cid}, cid},
		{[]string{"agy", "--resume=" + cid}, cid},
		{[]string{"agy", "-r", cid}, cid},
		{[]string{"agy", "-r=" + cid}, cid},
		{[]string{"agy", "-c", cid}, cid},
		{[]string{"agy", "-c"}, ""},
		{[]string{"agy", "--resume", "not-an-id"}, ""},
	} {
		if got := agyConversationArg(tc.args); got != tc.want {
			t.Errorf("agyConversationArg(%v) = %q, want %q", tc.args, got, tc.want)
		}
	}

	base := t.TempDir()
	realHome := filepath.Join(base, "data00", "home", "u")
	linkParent := filepath.Join(base, "home")
	linkHome := filepath.Join(linkParent, "u")
	realProj := filepath.Join(realHome, "repo")
	linkProj := filepath.Join(linkHome, "repo")
	os.MkdirAll(realProj, 0o755)
	os.MkdirAll(linkParent, 0o755)
	if err := os.Symlink(realHome, linkHome); err != nil {
		t.Fatal(err)
	}

	cliRoot := filepath.Join(realHome, ".gemini", "antigravity-cli")
	agRoot := filepath.Join(realHome, ".gemini", "antigravity")
	cliLog := filepath.Join(cliRoot, "brain", cid, ".system_generated", "logs", "transcript.jsonl")
	os.MkdirAll(filepath.Dir(cliLog), 0o755)
	os.WriteFile(cliLog, []byte("{}\n"), 0o644)

	// DB stores symlinked path (/home/u/repo) while /proc/<pid>/cwd yields real path (/data00/home/u/repo).
	initAgySummaryDB(t, cliRoot, []struct {
		id, title, uri, status string
		notIdle                int
	}{
		{cid, "cli title", "file://" + linkProj, "CASCADE_RUN_STATUS_IDLE", 0},
	})

	srv := &Server{home: realHome}
	now := time.Now()

	// 1) --resume flag locates conversation directly.
	e1 := &External{Kind: "agy", Cwd: realProj, Started: now.Add(-time.Minute)}
	srv.locateExternal(e1, &proc{args: []string{"agy", "--resume", cid}})
	if e1.Session != cid || e1.path != cliLog {
		t.Fatalf("--resume locateExternal = (%q, %q), want (%q, %q)", e1.Session, e1.path, cid, cliLog)
	}

	// 2) -r flag locates conversation directly.
	e2 := &External{Kind: "agy", Cwd: realProj, Started: now.Add(-time.Minute)}
	srv.locateExternal(e2, &proc{args: []string{"agy", "-r", cid}})
	if e2.Session != cid || e2.path != cliLog {
		t.Fatalf("-r locateExternal = (%q, %q), want (%q, %q)", e2.Session, e2.path, cid, cliLog)
	}

	// 3) No flag: symlink-normalized cwd matches DB's symlink URI.
	e3 := &External{Kind: "agy", Cwd: realProj, Started: now.Add(-time.Minute)}
	srv.locateExternal(e3, &proc{args: []string{"agy"}})
	if e3.Session != cid || e3.path != cliLog {
		t.Fatalf("symlink cwd locateExternal = (%q, %q), want (%q, %q)", e3.Session, e3.path, cid, cliLog)
	}

	// 4) guessAgy also matches symlinked cwd & remembered resumed desktop conversation.
	deskID := "9999aaaa-bbbb-cccc-dddd-eeeeeeeeeeee"
	deskLog := filepath.Join(agRoot, "brain", deskID, ".system_generated", "logs", "transcript.jsonl")
	os.MkdirAll(filepath.Dir(deskLog), 0o755)
	os.WriteFile(deskLog, []byte("{}\n"), 0o644)
	initAgySummaryDB(t, agRoot, []struct {
		id, title, uri, status string
		notIdle                int
	}{
		{deskID, "desktop title", "file://" + linkProj, "CASCADE_RUN_STATUS_IDLE", 0},
	})
	t.Cleanup(func() { forgetAgySession("w99:p1") })

	srv.rememberAgySession("w99:p1", deskID)
	if got := srv.guessAgy(filepath.Join(cliRoot, "brain"), "w99:p1", realProj); got != deskLog {
		t.Fatalf("guessAgy remembered desktop fallback = %q, want %q", got, deskLog)
	}
	if got := srv.agyTitle(deskID); got != "desktop title" {
		t.Fatalf("agyTitle desktop fallback = %q, want %q", got, "desktop title")
	}
	// Once CLI imports and creates its own brain transcript, guessAgy seamlessly prefers it.
	importedLog := filepath.Join(cliRoot, "brain", deskID, ".system_generated", "logs", "transcript.jsonl")
	os.MkdirAll(filepath.Dir(importedLog), 0o755)
	os.WriteFile(importedLog, []byte("{}\n"), 0o644)
	if got := srv.guessAgy(filepath.Join(cliRoot, "brain"), "w99:p1", realProj); got != importedLog {
		t.Fatalf("guessAgy imported CLI transcript = %q, want %q", got, importedLog)
	}
}
