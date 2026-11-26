package collie

import (
	"bufio"
	"context"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestParseClaudeSGBridgeArgs(t *testing.T) {
	cfg, err := parseClaudeSGBridgeArgs([]string{
		"--name", "worker-1",
		"--project=/tmp/proj",
		"--launcher", "/usr/local/bin/claude-sg",
		"--",
		"--model", "opus",
		"--allowedTools", "Bash(collie report:*)",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.Name != "worker-1" || cfg.Project != "/tmp/proj" || cfg.Launcher != "/usr/local/bin/claude-sg" {
		t.Fatalf("unexpected cfg: %+v", cfg)
	}
	want := []string{"--model", "opus", "--allowedTools", "Bash(collie report:*)"}
	if !slices.Equal(cfg.ClaudeArgs, want) {
		t.Fatalf("ClaudeArgs = %v, want %v", cfg.ClaudeArgs, want)
	}

	if _, err := parseClaudeSGBridgeArgs([]string{"--name"}); err == nil {
		t.Fatal("expected error for missing --name value")
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
	srv.claudeModels(info)
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
			handleHerdrStream(context.Background(), server, sock, srv)
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
