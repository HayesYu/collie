package collie

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestDoubaoEnv(t *testing.T) {
	base := []string{
		"PATH=/usr/bin",
		"no_proxy=localhost,127.0.0.1,doubao.com",
		"DOUBAO_CLI_NO_UPDATE_CHECK=0",
	}
	got := doubaoEnv(base)
	m := map[string]string{}
	for _, kv := range got {
		k, v, ok := strings.Cut(kv, "=")
		if ok {
			m[k] = v
		}
	}
	if m["DOUBAO_CLI_NO_UPDATE_CHECK"] != "1" {
		t.Fatalf("DOUBAO_CLI_NO_UPDATE_CHECK = %q, want 1", m["DOUBAO_CLI_NO_UPDATE_CHECK"])
	}
	for _, k := range []string{"no_proxy", "NO_PROXY"} {
		for _, dom := range doubaoPublicNoProxy {
			if !strings.Contains(","+m[k]+",", ","+dom+",") {
				t.Errorf("%s=%q missing %q", k, m[k], dom)
			}
		}
	}
	if strings.Count(","+m["no_proxy"]+",", ",doubao.com,") != 1 {
		t.Errorf("no_proxy duplicated doubao.com: %q", m["no_proxy"])
	}
}

func TestDoubaoBridgeArgsAndACPArgs(t *testing.T) {
	t.Run("fresh defaults align to Auto + high", func(t *testing.T) {
		cfg, err := parseDoubaoBridgeArgs([]string{"-y", "-C", "/tmp/work"})
		if err != nil {
			t.Fatal(err)
		}
		got := doubaoACPArgs(cfg)
		want := []string{"acp", "serve", "-y", "--model", "Auto", "--effort", "high", "-C", "/tmp/work"}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("got %#v, want %#v", got, want)
		}
	})

	t.Run("non-bypass uses permission-mode default without -y", func(t *testing.T) {
		cfg, err := parseDoubaoBridgeArgs([]string{"--model", "5101", "--effort", "max"})
		if err != nil {
			t.Fatal(err)
		}
		got := doubaoACPArgs(cfg)
		want := []string{"acp", "serve", "--permission-mode", "default", "--model", "5101", "--effort", "max"}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("got %#v, want %#v", got, want)
		}
	})

	t.Run("bypass ignores permission-mode to avoid mutual exclusion", func(t *testing.T) {
		cfg, err := parseDoubaoBridgeArgs([]string{"--dangerously-bypass-approvals-and-sandbox", "--permission-mode", "default"})
		if err != nil {
			t.Fatal(err)
		}
		got := doubaoACPArgs(cfg)
		if slices.Contains(got, "--permission-mode") || !slices.Contains(got, "-y") {
			t.Fatalf("mutually exclusive flags leaked: %#v", got)
		}
	})

	t.Run("resume without explicit model omits default model override", func(t *testing.T) {
		cfg, err := parseDoubaoBridgeArgs([]string{"-y", "--resume", "sess-abc"})
		if err != nil {
			t.Fatal(err)
		}
		got := doubaoACPArgs(cfg)
		want := []string{"acp", "serve", "-y"}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("got %#v, want %#v", got, want)
		}
	})
}

func TestReadBridgeInput(t *testing.T) {
	pr, pw := io.Pipe()
	var permActive sync.Map
	evCh := make(chan bridgeInputEvent, 16)
	go readBridgeInput(pr, func() bool {
		v, _ := permActive.Load("on")
		b, _ := v.(bool)
		return b
	}, evCh)

	go func() {
		// 1) Multi-line bracketed paste followed by Enter
		_, _ = pw.Write([]byte("\x1b[200~line1\nline2\r\nline3\x1b[201~"))
		time.Sleep(20 * time.Millisecond)
		_, _ = pw.Write([]byte("\r"))
		time.Sleep(30 * time.Millisecond)

		// 2) Plain input with backspace
		_, _ = pw.Write([]byte("hellox\x7f!\r"))
		time.Sleep(30 * time.Millisecond)

		// 3) Permission digit without Enter, plus a trailing CR that must be swallowed
		permActive.Store("on", true)
		_, _ = pw.Write([]byte("1\r"))
		time.Sleep(30 * time.Millisecond)
		permActive.Store("on", false)

		// 4) Standalone Esc
		_, _ = pw.Write([]byte{0x1b})
		time.Sleep(80 * time.Millisecond)

		_ = pw.Close()
	}()

	var got []bridgeInputEvent
	for ev := range evCh {
		got = append(got, ev)
	}
	want := []bridgeInputEvent{
		{kind: "prompt", text: "line1\nline2\nline3"},
		{kind: "prompt", text: "hello!"},
		{kind: "perm", text: "1"},
		{kind: "esc"},
		{kind: "eof"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("events = %#v\nwant   = %#v", got, want)
	}
}

func TestDoubaoBridgeEndToEndWithFakeACP(t *testing.T) {
	tmp := t.TempDir()
	fakeSock := filepath.Join(tmp, "herdr.sock")

	// Start a minimal fake Herdr Unix socket server to capture all lifecycle reports.
	ln, err := net.Listen("unix", fakeSock)
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()

	var rpcMu sync.Mutex
	var herdrCalls []map[string]any
	go func() {
		for {
			conn, err := ln.Accept()
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
				var req map[string]any
				if json.Unmarshal(line, &req) == nil {
					rpcMu.Lock()
					herdrCalls = append(herdrCalls, req)
					rpcMu.Unlock()
					resp, _ := json.Marshal(map[string]any{"id": req["id"], "result": map[string]any{"type": "ok"}})
					_, _ = c.Write(append(resp, '\n'))
				}
			}(conn)
		}
	}()

	// Write a fake `doubao` script speaking ACP v1 JSON-RPC, including:
	// - validating non-empty clientInfo.name on initialize
	// - emitting a >90KB available_commands_update frame (exceeding 64KB Scanner cap)
	// - streaming agent_message_chunk, tool_call placeholder -> tool_call_update, and session/request_permission
	// - supporting session/load with history replay
	fakeDoubao := filepath.Join(tmp, "fake-doubao")
	script := `#!/usr/bin/env python3
import json, sys

session_id = "11111111-2222-3333-4444-555555555555"

def send(obj):
    sys.stdout.write(json.dumps(obj, ensure_ascii=False) + "\n")
    sys.stdout.flush()

for raw in sys.stdin:
    raw = raw.strip()
    if not raw:
        continue
    msg = json.loads(raw)
    mid = msg.get("id")
    method = msg.get("method", "")
    params = msg.get("params") or {}
    if method == "initialize":
        ci = params.get("clientInfo") or {}
        if not ci.get("name"):
            send({"jsonrpc": "2.0", "id": mid, "error": {"code": -32602, "message": "Product name is required"}})
            continue
        send({"jsonrpc": "2.0", "id": mid, "result": {"protocolVersion": 1, "agentInfo": {"name": "doubao", "version": "2.38.2"}}})
    elif method == "session/new":
        # >90KB available_commands_update frame to verify large-buffer reading
        big_cmds = [{"name": f"cmd-{i}", "description": "x" * 900} for i in range(110)]
        send({"jsonrpc": "2.0", "method": "session/update", "params": {"sessionId": session_id, "update": {"sessionUpdate": "available_commands_update", "availableCommands": big_cmds}}})
        send({"jsonrpc": "2.0", "id": mid, "result": {
            "sessionId": session_id,
            "models": {"currentModelId": "9", "availableModels": [{"modelId": "9", "name": "Auto"}]},
            "configOptions": [{"id": "effort", "currentValue": "high"}],
        }})
    elif method == "session/load":
        sid = params["sessionId"]
        send({"jsonrpc": "2.0", "method": "session/update", "params": {"sessionId": sid, "update": {"sessionUpdate": "user_message_chunk", "messageId": "u-replay", "content": {"type": "text", "text": "old user prompt"}}}})
        send({"jsonrpc": "2.0", "method": "session/update", "params": {"sessionId": sid, "update": {"sessionUpdate": "agent_message_chunk", "messageId": "a-replay", "content": {"type": "text", "text": "old assistant reply"}}}})
        send({"jsonrpc": "2.0", "method": "session/update", "params": {"sessionId": sid, "update": {"sessionUpdate": "tool_call", "toolCallId": "tc-replay", "title": "Read file", "kind": "read", "status": "completed", "locations": [{"path": "/tmp/a.txt"}], "rawOutput": "ok"}}})
        send({"jsonrpc": "2.0", "id": mid, "result": {
            "models": {"currentModelId": "9", "availableModels": [{"modelId": "9", "name": "Auto"}]},
            "configOptions": [{"id": "effort", "currentValue": "high"}],
        }})
    elif method == "session/prompt":
        sid = params["sessionId"]
        # Request interactive permission (non-bypass)
        if "-y" not in sys.argv:
            send({
                "jsonrpc": "2.0", "id": 0, "method": "session/request_permission",
                "params": {
                    "sessionId": sid,
                    "toolCall": {"toolCallId": "perm-1", "title": "Bash", "kind": "execute", "rawInput": {"command": "echo hi"}},
                    "options": [
                        {"optionId": "allow_once", "name": "Allow once", "kind": "allow_once"},
                        {"optionId": "reject_once", "name": "Reject", "kind": "reject_once"},
                    ],
                },
            })
            # Wait for client response
            ans = json.loads(sys.stdin.readline())
            chosen = ans["result"]["outcome"]["optionId"]
        else:
            chosen = "bypass"
        send({"jsonrpc": "2.0", "method": "session/update", "params": {"sessionId": sid, "update": {"sessionUpdate": "tool_call", "toolCallId": "t1", "title": "继续处理任务中", "kind": "read", "status": "in_progress"}}})
        send({"jsonrpc": "2.0", "method": "session/update", "params": {"sessionId": sid, "update": {"sessionUpdate": "tool_call_update", "toolCallId": "t1", "title": "Run echo", "kind": "read", "status": "completed", "locations": [{"path": "/tmp/sample.txt"}], "rawOutput": "HELLO_OK\n"}}})
        send({"jsonrpc": "2.0", "method": "session/update", "params": {"sessionId": sid, "update": {"sessionUpdate": "agent_message_chunk", "messageId": "m1", "content": {"type": "text", "text": "Done: "}}}})
        send({"jsonrpc": "2.0", "method": "session/update", "params": {"sessionId": sid, "update": {"sessionUpdate": "agent_message_chunk", "messageId": "m1", "content": {"type": "text", "text": chosen}}}})
        send({"jsonrpc": "2.0", "id": mid, "result": {"stopReason": "end_turn"}})
`
	if err := os.WriteFile(fakeDoubao, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}

	collieHome := filepath.Join(tmp, "collie-home")
	transcriptDir := filepath.Join(collieHome, "transcripts", "doubao")

	// Part 1: Fresh session with interactive permission approval
	pr, pw := io.Pipe()
	var outBuf bytes.Buffer
	b := &doubaoBridge{
		cfg: doubaoBridgeConfig{
			Cwd:        tmp,
			CollieHome: collieHome,
			DoubaoBin:  fakeDoubao,
			Bypass:     false,
		},
		herdrSock:     fakeSock,
		paneID:        "w9:p1",
		transcriptDir: transcriptDir,
		stdin:         pr,
		stdout:        &outBuf,
		stderr:        &outBuf,
		tty:           false,
		pending:       map[int64]chan rpcFrame{},
		promptQueue:   make(chan string, 16),
		permReply:     make(chan string, 4),
	}

	go func() {
		// Wait until bridge reports idle, then send bracketed-paste prompt, wait for blocked, answer "1"
		deadline := time.Now().Add(5 * time.Second)
		for time.Now().Before(deadline) {
			rpcMu.Lock()
			n := len(herdrCalls)
			rpcMu.Unlock()
			if n >= 2 {
				break
			}
			time.Sleep(20 * time.Millisecond)
		}
		_, _ = pw.Write([]byte("\x1b[200~hello\nmultiline\x1b[201~\r"))
		for time.Now().Before(deadline) {
			b.stateMu.Lock()
			blocked := b.activePerm != nil
			b.stateMu.Unlock()
			if blocked {
				break
			}
			time.Sleep(20 * time.Millisecond)
		}
		_, _ = pw.Write([]byte("1"))
		// Wait for turn to finish (back to idle)
		for time.Now().Before(deadline) {
			b.stateMu.Lock()
			working := b.working
			b.stateMu.Unlock()
			if !working && strings.Contains(outBuf.String(), "Done: allow_once") {
				break
			}
			time.Sleep(20 * time.Millisecond)
		}
		_ = pw.Close()
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := b.run(ctx); err != nil {
		t.Fatalf("bridge run failed: %v\noutput:\n%s", err, outBuf.String())
	}

	// Verify lifecycle states reported to Herdr (and resume_argv[0] is plain command name)
	var states []string
	var methods []string
	rpcMu.Lock()
	for _, c := range herdrCalls {
		m, _ := c["method"].(string)
		methods = append(methods, m)
		if p, ok := c["params"].(map[string]any); ok {
			if argv, ok := p["resume_argv"].([]any); ok && len(argv) > 0 {
				if first, _ := argv[0].(string); first != "collie" {
					t.Errorf("%s resume_argv[0] = %q, want plain command name 'collie'", m, first)
				}
			}
			if m == "pane.report_agent" {
				if st, ok := p["state"].(string); ok {
					states = append(states, st)
				}
			}
		}
	}
	rpcMu.Unlock()
	if !slices.Contains(methods, "pane.report_agent_session") || !slices.Contains(methods, "pane.release_agent") {
		t.Fatalf("missing lifecycle calls: %v", methods)
	}
	for _, wantState := range []string{"idle", "working", "blocked"} {
		if !slices.Contains(states, wantState) {
			t.Fatalf("states %v missing %q", states, wantState)
		}
	}

	// Verify JSONL transcript content & parsing
	logPath := filepath.Join(transcriptDir, "11111111-2222-3333-4444-555555555555.jsonl")
	rawBefore, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("transcript missing: %v", err)
	}
	if strings.Contains(string(rawBefore), "available_commands_update") {
		t.Fatal("available_commands_update leaked into transcript")
	}
	tr := newTranscript(logPath, "doubao")
	tr.update()
	msgs := tr.view()
	if len(msgs) != 3 {
		t.Fatalf("got %d msgs: %+v", len(msgs), msgs)
	}
	if msgs[0].Kind != "user" || msgs[0].Text != "hello\nmultiline" {
		t.Errorf("msg[0] = %+v", msgs[0])
	}
	if msgs[1].Kind != "tool" || msgs[1].Name != "Run echo" || msgs[1].Input != "/tmp/sample.txt" || msgs[1].Output != "HELLO_OK" {
		t.Errorf("msg[1] = %+v", msgs[1])
	}
	if msgs[2].Kind != "assistant" || msgs[2].Text != "Done: allow_once" {
		t.Errorf("msg[2] = %+v", msgs[2])
	}

	// Part 2A: Resume WITH existing JSONL must NOT duplicate replayed history
	pr2, pw2 := io.Pipe()
	var out2 bytes.Buffer
	b2 := &doubaoBridge{
		cfg: doubaoBridgeConfig{
			Cwd:        tmp,
			CollieHome: collieHome,
			DoubaoBin:  fakeDoubao,
			Bypass:     true,
			Resume:     "11111111-2222-3333-4444-555555555555",
		},
		transcriptDir: transcriptDir,
		stdin:         pr2,
		stdout:        &out2,
		stderr:        &out2,
		tty:           false,
		pending:       map[int64]chan rpcFrame{},
		promptQueue:   make(chan string, 16),
		permReply:     make(chan string, 4),
	}
	_ = pw2.Close()
	if err := b2.run(ctx); err != nil {
		t.Fatalf("resume with existing log failed: %v", err)
	}
	rawAfter, _ := os.ReadFile(logPath)
	if string(rawAfter) != string(rawBefore) {
		t.Fatalf("session/load replay duplicated existing JSONL:\nbefore=%s\nafter=%s", rawBefore, rawAfter)
	}

	// Part 2B: Resume WITHOUT existing JSONL rebuilds history from replay
	_ = os.Remove(logPath)
	pr3, pw3 := io.Pipe()
	var out3 bytes.Buffer
	b3 := &doubaoBridge{
		cfg: doubaoBridgeConfig{
			Cwd:        tmp,
			CollieHome: collieHome,
			DoubaoBin:  fakeDoubao,
			Bypass:     true,
			Resume:     "11111111-2222-3333-4444-555555555555",
		},
		transcriptDir: transcriptDir,
		stdin:         pr3,
		stdout:        &out3,
		stderr:        &out3,
		tty:           false,
		pending:       map[int64]chan rpcFrame{},
		promptQueue:   make(chan string, 16),
		permReply:     make(chan string, 4),
	}
	_ = pw3.Close()
	if err := b3.run(ctx); err != nil {
		t.Fatalf("resume without existing log failed: %v", err)
	}
	tr3 := newTranscript(logPath, "doubao")
	tr3.update()
	rebuilt := tr3.view()
	if len(rebuilt) != 3 || rebuilt[0].Text != "old user prompt" || rebuilt[1].Text != "old assistant reply" || rebuilt[2].Name != "Read file" {
		t.Fatalf("rebuilt transcript mismatch: %+v", rebuilt)
	}
}
