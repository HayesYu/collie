package collie

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestParseClaudeSGBridgeArgs(t *testing.T) {
	cfg, err := parseClaudeSGBridgeArgs([]string{
		"--name", "worker-1",
		"--project=/tmp/proj",
		"--workdir", "/workspace/agent/collie",
		"--launcher", "/usr/local/bin/claude-sg",
		"--",
		"--model", "opus",
		"--allowedTools", "Bash(collie report:*)",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.Name != "worker-1" || cfg.Project != "/tmp/proj" || cfg.Workdir != "/workspace/agent/collie" || cfg.Launcher != "/usr/local/bin/claude-sg" {
		t.Fatalf("unexpected cfg: %+v", cfg)
	}
	want := []string{"--model", "opus", "--allowedTools", "Bash(collie report:*)"}
	if !slices.Equal(cfg.ClaudeArgs, want) {
		t.Fatalf("ClaudeArgs = %v, want %v", cfg.ClaudeArgs, want)
	}
	resumed := claudeSGResumeArgv("/usr/local/bin/collie", cfg, "sid-123")
	wantResume := []string{"/usr/local/bin/collie", "_bridge", "claude-sg", "--name", "worker-1", "--project", "/tmp/proj", "--workdir", "/workspace/agent/collie", "--launcher", "/usr/local/bin/claude-sg", "--", "--resume", "sid-123"}
	if !slices.Equal(resumed, wantResume) {
		t.Fatalf("claudeSGResumeArgv = %v, want %v", resumed, wantResume)
	}

	if _, err := parseClaudeSGBridgeArgs([]string{"--name"}); err == nil {
		t.Fatal("expected error for missing --name value")
	}
}

func TestResolveClaudeSGTarget(t *testing.T) {
	pinned := "/Users/bytedance/Code"
	home := "/Users/bytedance"

	// Unpinned keeps requested cwd with no extra workdir override.
	p, w := resolveClaudeSGTargetCase("", "/Users/bytedance/code/foo", home, true)
	if p != "/Users/bytedance/code/foo" || w != "" {
		t.Fatalf("unpinned = (%q, %q)", p, w)
	}

	cases := []struct {
		cwd      string
		ci       bool
		wantProj string
		wantWork string
	}{
		{"", true, pinned, "/workspace"},
		{"/Users/bytedance/Code", true, pinned, "/workspace"},
		{"/Users/bytedance/code", true, pinned, "/workspace"},
		{"/Users/bytedance/code/agent/collie", true, pinned, "/workspace/agent/collie"},
		{"/Users/bytedance/Code/agent/collie", false, pinned, "/workspace/agent/collie"},
		{"/Users/bytedance/personal/foo", true, pinned, "/mac-home/personal/foo"},
		{"/Users/bytedance", true, pinned, "/mac-home"},
		{"/Users/bytedance/Code2", true, pinned, "/mac-home/Code2"},
		{"/tmp/other", true, pinned, "/workspace"},
	}
	for _, tc := range cases {
		gotProj, gotWork := resolveClaudeSGTargetCase(pinned, tc.cwd, home, tc.ci)
		if gotProj != tc.wantProj || gotWork != tc.wantWork {
			t.Fatalf("resolveClaudeSGTargetCase(%q, %q, ci=%v) = (%q, %q), want (%q, %q)",
				pinned, tc.cwd, tc.ci, gotProj, gotWork, tc.wantProj, tc.wantWork)
		}
	}
}

func TestSafePaneFileNameAndSessionID(t *testing.T) {
	if got := safePaneFileName("w1:p2@mac/1"); got != "w1_p2_mac_1" {
		t.Fatalf("safePaneFileName = %q", got)
	}
	for _, bad := range []string{"", ".", "..", "../x", "a/b"} {
		if isSafeSessionID(bad) {
			t.Fatalf("expected unsafe session id for %q", bad)
		}
	}
	if !isSafeSessionID("b1fddf83-bd2a-42ae-807e-50937f9cfe3c") {
		t.Fatal("expected valid UUID session id")
	}
}

func TestFindTranscriptClaudeSG(t *testing.T) {
	home := t.TempDir()
	launcher := filepath.Join(home, "claude-sg")
	if err := os.WriteFile(launcher, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv(claudeSGBinEnv, launcher)

	stateDir := claudeSGStateDir(home)
	if err := os.MkdirAll(filepath.Join(stateDir, "transcripts"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(stateDir, "events"), 0o700); err != nil {
		t.Fatal(err)
	}
	sid := "11111111-2222-3333-4444-555555555555"
	tp := filepath.Join(stateDir, "transcripts", sid+".jsonl")
	if err := os.WriteFile(tp, []byte(`{"type":"user","message":{"content":"hi"}}`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	evPath := claudeSGEventFile(stateDir, "w1:p1")
	if err := os.WriteFile(evPath, []byte(`{"pane_id":"w1:p1","state":"idle","session_id":"`+sid+`","ts":1}`), 0o600); err != nil {
		t.Fatal(err)
	}

	srv := &Server{home: home}
	if got, fmt := srv.findTranscript("claude", "id", sid, "w1:p1", "/tmp"); got != tp || fmt != "claude" {
		t.Fatalf("by id: got (%q, %q), want (%q, claude)", got, fmt, tp)
	}
	if got, fmt := srv.findTranscript("claude", "", "", "w1:p1@mac", "/tmp"); got != tp || fmt != "claude" {
		t.Fatalf("by pane fallback: got (%q, %q), want (%q, claude)", got, fmt, tp)
	}
	if got, _ := srv.findTranscript("claude", "id", "../evil", "w1:p1", "/tmp"); got != "" {
		t.Fatalf("expected traversal rejection, got %q", got)
	}
}

func TestClaudeModelsWithClaudeSGLauncher(t *testing.T) {
	home := t.TempDir()
	if err := os.MkdirAll(filepath.Join(home, ".claude"), 0o755); err != nil {
		t.Fatal(err)
	}
	// Host settings with custom gateway models that must NOT leak into claude-sg mode.
	if err := os.WriteFile(filepath.Join(home, ".claude", "settings.json"), []byte(`{
		"model": "custom-host-gateway-model",
		"env": {"CLAUDE_CODE_MODEL_CAPABILITIES": "custom-host-gateway-model=effort"}
	}`), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv(claudeSGBinEnv, "/tmp/fake-claude-sg")

	srv := &Server{home: home}
	info := &ModelInfo{Kind: "claude", Efforts: allEfforts}
	if srv.claudeModels(context.Background(), info) {
		t.Fatal("probe of a missing launcher should fall back")
	}
	if info.Default != "" || len(info.Models) != 3 {
		t.Fatalf("unexpected info: %+v", info)
	}
	for i, want := range []string{"opus", "sonnet", "haiku"} {
		if info.Models[i].ID != want {
			t.Fatalf("model[%d] = %q, want %q", i, info.Models[i].ID, want)
		}
	}
}

func TestParseClaudeStartRPC(t *testing.T) {
	id, p, ok := parseClaudeStartRPC([]byte(`{"id":"co-7","method":"agent.start","params":{"kind":"claude","name":"w1","pane_id":"w2:p1","args":["--model","opus"]}}`))
	if !ok || id != "co-7" || p.PaneID != "w2:p1" || p.Name != "w1" || len(p.Args) != 2 {
		t.Fatalf("unexpected parse result: ok=%v id=%q params=%+v", ok, id, p)
	}
	for _, line := range []string{
		`{"id":"co-8","method":"agent.start","params":{"kind":"agy","pane_id":"w2:p1"}}`,
		`{"id":"co-9","method":"pane.list","params":{}}`,
		`{"id":"co-10","method":"agent.start","params":{"kind":"claude"}}`,
	} {
		if _, _, ok := parseClaudeStartRPC([]byte(line)); ok {
			t.Fatalf("should not intercept: %s", line)
		}
	}
}

func TestAgentMissingWithClaudeSGLauncher(t *testing.T) {
	home := t.TempDir()
	launcher := filepath.Join(home, "claude-sg")
	if err := os.WriteFile(launcher, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv(claudeSGBinEnv, launcher)

	srv := &Server{home: home}
	if err := srv.agentMissing("", "claude"); err != nil {
		t.Fatalf("expected executable launcher to pass, got %v", err)
	}

	t.Setenv(claudeSGBinEnv, filepath.Join(home, "missing-launcher"))
	if err := srv.agentMissing("", "claude"); err == nil {
		t.Fatal("expected error for missing claude-sg launcher")
	}
}

func TestHandleHerdrStream(t *testing.T) {
	home := t.TempDir()
	launcher := filepath.Join(home, "claude-sg")
	if err := os.WriteFile(launcher, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv(claudeSGBinEnv, launcher)

	sockDir, err := os.MkdirTemp("/tmp", "csg-sock-*")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(sockDir) })
	sock := filepath.Join(sockDir, "h.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatalf("listen unix %s: %v", sock, err)
	}
	t.Cleanup(func() { _ = ln.Close() })

	var mu sync.Mutex
	var methods []string
	var sentText string
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				defer c.Close()
				r := bufio.NewReader(c)
				line, err := r.ReadBytes('\n')
				if err != nil {
					return
				}
				var req struct {
					ID     string         `json:"id"`
					Method string         `json:"method"`
					Params map[string]any `json:"params"`
				}
				if json.Unmarshal(line, &req) != nil {
					return
				}
				mu.Lock()
				methods = append(methods, req.Method)
				if req.Method == "pane.send_input" {
					sentText, _ = req.Params["text"].(string)
				}
				mu.Unlock()
				var res any = map[string]any{"echo": req.Method}
				switch req.Method {
				case "pane.get":
					res = map[string]any{"pane": map[string]any{"pane_id": "w2:p1", "cwd": home}}
				case "agent.get":
					res = map[string]any{"agent": map[string]any{"agent": "claude", "agent_status": "idle"}}
				}
				b, _ := json.Marshal(map[string]any{"id": req.ID, "result": res})
				_, _ = c.Write(append(b, '\n'))
			}(c)
		}
	}()

	srv := &Server{herdr: newHerdr(sock, "mac", home), home: home, node: true}
	roundTrip := func(reqLine string) map[string]any {
		t.Helper()
		client, server := net.Pipe()
		defer client.Close()
		done := make(chan struct{})
		go func() {
			defer close(done)
			handleHerdrStream(context.Background(), server, sock, srv, nil)
		}()
		_ = client.SetDeadline(time.Now().Add(5 * time.Second))
		if _, err := client.Write([]byte(reqLine + "\n")); err != nil {
			t.Fatalf("write: %v", err)
		}
		respLine, err := bufio.NewReader(client).ReadBytes('\n')
		if err != nil {
			t.Fatalf("read: %v", err)
		}
		<-done
		var out map[string]any
		if err := json.Unmarshal(respLine, &out); err != nil {
			t.Fatalf("unmarshal %q: %v", respLine, err)
		}
		return out
	}

	// 1. Non-claude calls pass through unchanged.
	r1 := roundTrip(`{"id":"r1","method":"pane.list","params":{}}`)
	if res, _ := r1["result"].(map[string]any); res["echo"] != "pane.list" {
		t.Fatalf("pane.list passthrough failed: %+v", r1)
	}
	r2 := roundTrip(`{"id":"r2","method":"agent.start","params":{"kind":"agy","pane_id":"w2:p1"}}`)
	if res, _ := r2["result"].(map[string]any); res["echo"] != "agent.start" {
		t.Fatalf("agy agent.start passthrough failed: %+v", r2)
	}

	// 2. Claude agent.start is intercepted and launches _bridge claude-sg in pane.
	r3 := roundTrip(`{"id":"r3","method":"agent.start","params":{"kind":"claude","name":"w-sg","pane_id":"w2:p1","args":["--model","opus"]}}`)
	if res, _ := r3["result"].(map[string]any); res["pane_id"] != "w2:p1" {
		t.Fatalf("claude interception failed: %+v", r3)
	}
	mu.Lock()
	gotMethods := append([]string(nil), methods...)
	gotSent := sentText
	mu.Unlock()
	if !strings.Contains(gotSent, "_bridge claude-sg") || !strings.Contains(gotSent, "--model opus") {
		t.Fatalf("unexpected bridge command sent to pane: %q", gotSent)
	}
	// agent.start appeared only once (from the agy passthrough, never for claude).
	startCount := 0
	for _, m := range gotMethods {
		if m == "agent.start" {
			startCount++
		}
	}
	if startCount != 1 {
		t.Fatalf("expected 1 forwarded agent.start (for agy only), got %d in %v", startCount, gotMethods)
	}
}

func TestForkReleaseRepoURL(t *testing.T) {
	if releaseRepoURL != "https://github.com/HayesYu/collie" {
		t.Fatalf("default releaseRepoURL = %q, want https://github.com/HayesYu/collie", releaseRepoURL)
	}
	cases := map[string]string{
		"":                                     "https://github.com/HayesYu/collie",
		"HayesYu/collie":                       "https://github.com/HayesYu/collie",
		"HayesYu/collie.git":                   "https://github.com/HayesYu/collie",
		"https://github.com/HayesYu/collie/":   "https://github.com/HayesYu/collie",
		"https://github.com/lihuanyu0w0/collie": "https://github.com/lihuanyu0w0/collie",
	}
	for in, want := range cases {
		if got := forkReleaseRepoURL(in); got != want {
			t.Fatalf("forkReleaseRepoURL(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestNodeAssistedHubSelfUpdate(t *testing.T) {
	saveUpdateHooks(t)
	origWait := nodeRelayWaitTimeout
	origPoll := nodeRelayPollInterval
	t.Cleanup(func() {
		nodeRelayWaitTimeout = origWait
		nodeRelayPollInterval = origPoll
	})
	nodeRelayWaitTimeout = 2 * time.Second
	nodeRelayPollInterval = 15 * time.Millisecond

	const tag = "v0.2.82"
	newScript := "#!/bin/sh\necho \"collie " + tag + "\"\n"
	sum := sha256.Sum256([]byte(newScript))
	newSHA := hex.EncodeToString(sum[:])
	assetName := "collie-" + runtime.GOOS + "-" + runtime.GOARCH

	var failDirect atomic.Bool
	var mockGitHubHits atomic.Int32
	gh := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/releases/latest":
			http.Redirect(w, r, "/releases/tag/"+tag, http.StatusFound)
		case "/releases/tag/" + tag:
			w.WriteHeader(http.StatusOK)
		case "/releases/download/" + tag + "/checksums.txt":
			mockGitHubHits.Add(1)
			if failDirect.Load() {
				http.NotFound(w, r)
				return
			}
			fmt.Fprintf(w, "%s  %s\n", newSHA, assetName)
		case "/releases/download/" + tag + "/" + assetName:
			mockGitHubHits.Add(1)
			if failDirect.Load() {
				http.NotFound(w, r)
				return
			}
			_, _ = w.Write([]byte(newScript))
		default:
			http.NotFound(w, r)
		}
	}))
	defer gh.Close()
	releaseRepoURL = gh.URL

	// 1) Node /api/update/fetch validates auth mode, query params, and streams SHA-256 verified binary.
	nodeSrv := &Server{node: true}
	nodeRoutes := nodeSrv.NodeRoutes()

	nonNodeRec := httptest.NewRecorder()
	(&Server{node: false}).serveRelayAsset(nonNodeRec, httptest.NewRequest("GET", "/api/update/fetch?tag="+tag+"&os="+runtime.GOOS+"&arch="+runtime.GOARCH, nil))
	if nonNodeRec.Code != 403 {
		t.Fatalf("non-node serveRelayAsset status=%d, want 403", nonNodeRec.Code)
	}

	badRec := httptest.NewRecorder()
	nodeRoutes.ServeHTTP(badRec, httptest.NewRequest("GET", "/api/update/fetch?tag=bad&os="+runtime.GOOS+"&arch="+runtime.GOARCH, nil))
	if badRec.Code != 400 {
		t.Fatalf("bad tag serveRelayAsset status=%d, want 400", badRec.Code)
	}

	okRec := httptest.NewRecorder()
	nodeRoutes.ServeHTTP(okRec, httptest.NewRequest("GET", "/api/update/fetch?tag="+tag+"&os="+runtime.GOOS+"&arch="+runtime.GOARCH, nil))
	if okRec.Code != 200 {
		t.Fatalf("valid serveRelayAsset status=%d body=%s", okRec.Code, okRec.Body.String())
	}
	if okRec.Header().Get(relayUpdateTagHeader) != tag || okRec.Header().Get(relayUpdateSHAHeader) != newSHA {
		t.Fatalf("unexpected relay headers: tag=%q sha=%q", okRec.Header().Get(relayUpdateTagHeader), okRec.Header().Get(relayUpdateSHAHeader))
	}
	if okRec.Body.String() != newScript {
		t.Fatalf("serveRelayAsset body = %q, want %q", okRec.Body.String(), newScript)
	}

	// Helper to route Hub->Node HTTP calls through nodeRoutes in memory.
	nodeClient := &http.Client{Transport: updateRoundTripFunc(func(r *http.Request) (*http.Response, error) {
		rec := httptest.NewRecorder()
		nodeRoutes.ServeHTTP(rec, r)
		return rec.Result(), nil
	})}

	// 2) COLLIE_UPDATE_PREFER_NODE=1 waits briefly for a reconnecting node and installs via node.
	t.Setenv("COLLIE_UPDATE_PREFER_NODE", "1")
	exe1 := filepath.Join(t.TempDir(), "collie-hub-1")
	if err := os.WriteFile(exe1, []byte("#!/bin/sh\necho 'collie v0.2.81'\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	h1 := newHerdr("/nonexistent.sock", "hubbox", t.TempDir())
	h1.attach(&Machine{ID: "mac", Name: "mac", OS: runtime.GOOS + "/" + runtime.GOARCH, Online: false})
	hub1 := &Server{herdr: h1}

	go func() {
		time.Sleep(45 * time.Millisecond)
		h1.attach(&Machine{ID: "mac", Name: "mac", OS: runtime.GOOS + "/" + runtime.GOARCH, Online: true, client: nodeClient})
	}()

	hitsBeforePrefer := mockGitHubHits.Load()
	viaNode, err := hub1.downloadSelfUpdate(context.Background(), tag, exe1, false)
	if err != nil || !viaNode {
		t.Fatalf("prefer-node downloadSelfUpdate failed: viaNode=%v err=%v", viaNode, err)
	}
	got1, _ := os.ReadFile(exe1)
	if string(got1) != newScript {
		t.Fatalf("exe1 = %q, want %q", string(got1), newScript)
	}
	// Cached in node's getOrFetchRelayAsset from step 1, so mockGitHub saw zero extra hits.
	if mockGitHubHits.Load() != hitsBeforePrefer {
		t.Fatalf("expected node cache reuse, mockGitHub hits went %d -> %d", hitsBeforePrefer, mockGitHubHits.Load())
	}

	// 3) Default mode: when direct Hub download fails (or direct ctx expires), falls back to node cleanly.
	t.Setenv("COLLIE_UPDATE_PREFER_NODE", "")
	updateMaxAttempts = 1
	failDirect.Store(true)
	exe2 := filepath.Join(t.TempDir(), "collie-hub-2")
	if err := os.WriteFile(exe2, []byte("#!/bin/sh\necho 'collie v0.2.81'\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	h2 := newHerdr("/nonexistent.sock", "hubbox", t.TempDir())
	h2.attach(&Machine{
		ID: "mac", Name: "mac", OS: runtime.GOOS + "/" + runtime.GOARCH, Online: true,
		client: &http.Client{Transport: updateRoundTripFunc(func(r *http.Request) (*http.Response, error) {
			// Node still succeeds (cached or via working network path) even while direct Hub path failed.
			failDirect.Store(false)
			rec := httptest.NewRecorder()
			nodeRoutes.ServeHTTP(rec, r)
			return rec.Result(), nil
		})},
	})
	hub2 := &Server{herdr: h2}
	// Even with an already-canceled direct context, fallback uses fresh selfUpdateRelayTimeout context.
	expiredCtx, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer cancel()

	viaNode2, err := hub2.downloadSelfUpdate(expiredCtx, tag, exe2, false)
	if err != nil || !viaNode2 {
		t.Fatalf("fallback downloadSelfUpdate failed: viaNode=%v err=%v", viaNode2, err)
	}
	got2, _ := os.ReadFile(exe2)
	if string(got2) != newScript {
		t.Fatalf("exe2 = %q, want %q", string(got2), newScript)
	}
}
