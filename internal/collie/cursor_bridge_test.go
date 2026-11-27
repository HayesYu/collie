package collie

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestCursorEnv(t *testing.T) {
	t.Parallel()
	base := []string{
		"PATH=/usr/bin",
		"NO_OPEN_BROWSER=0",
		"CURSOR_API_KEY=test-key-value",
		"AGENT_CLI_CREDENTIAL_STORE=file",
	}
	got := cursorEnv(base)
	m := map[string]string{}
	for _, kv := range got {
		if k, v, ok := strings.Cut(kv, "="); ok {
			m[k] = v
		}
	}
	if m["NO_OPEN_BROWSER"] != "1" {
		t.Fatalf("NO_OPEN_BROWSER = %q, want 1", m["NO_OPEN_BROWSER"])
	}
	if m["CURSOR_API_KEY"] != "test-key-value" || m["AGENT_CLI_CREDENTIAL_STORE"] != "file" {
		t.Fatalf("preserved env lost: %#v", m)
	}
}

func TestCursorBridgeArgsAndACPArgs(t *testing.T) {
	t.Parallel()
	t.Run("bypass passes --force and --approve-mcps before acp without --yolo", func(t *testing.T) {
		cfg, err := parseCursorBridgeArgs([]string{"--force", "--model", "auto", "--effort", "high"})
		if err != nil {
			t.Fatal(err)
		}
		got := cursorACPArgs(cfg)
		want := []string{"--force", "--approve-mcps", "--model", "auto", "acp"}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("got %#v, want %#v", got, want)
		}
		if slices.Contains(got, "--yolo") || slices.Contains(got, "--effort") {
			t.Fatalf("forbidden flag in cursor ACP args: %#v", got)
		}
	})

	t.Run("parameterized model preserved before acp", func(t *testing.T) {
		cfg, err := parseCursorBridgeArgs([]string{"-f", "--model=claude-opus-4-8[context=1m,effort=high,fast=false]", "--cursor-bin", "/tmp/cursor-agent"})
		if err != nil {
			t.Fatal(err)
		}
		if cfg.Bin != "/tmp/cursor-agent" || !cfg.Bypass {
			t.Fatalf("parsed cfg = %+v", cfg)
		}
		got := cursorACPArgs(cfg)
		want := []string{"--force", "--approve-mcps", "--model", "claude-opus-4-8[context=1m,effort=high,fast=false]", "acp"}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("got %#v, want %#v", got, want)
		}
	})

	t.Run("non-bypass default emits bare acp", func(t *testing.T) {
		cfg, err := parseCursorBridgeArgs([]string{"--resume", "sess-123"})
		if err != nil {
			t.Fatal(err)
		}
		got := cursorACPArgs(cfg)
		want := []string{"acp"}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("got %#v, want %#v", got, want)
		}
	})
}

func TestPickPermissionOptionsAndAskQuestion(t *testing.T) {
	t.Parallel()
	cursorOpts := []acpPermOption{
		{OptionID: "allow-once", Name: "Allow once", Kind: "allow_once"},
		{OptionID: "allow-always", Name: "Allow always", Kind: "allow_always"},
		{OptionID: "reject-once", Name: "Reject", Kind: "reject_once"},
	}
	allow, ok := pickAllowOption(cursorOpts)
	if !ok || allow.OptionID != "allow-once" {
		t.Fatalf("pickAllowOption(cursor) = %+v, %v", allow, ok)
	}
	reject, ok := pickRejectOption(cursorOpts)
	if !ok || reject.OptionID != "reject-once" {
		t.Fatalf("pickRejectOption(cursor) = %+v, %v", reject, ok)
	}
	if isAskQuestionPerm(cursorOpts) {
		t.Fatal("normal tool permission should not be detected as ask_question")
	}

	hyphenOnly := []acpPermOption{
		{OptionID: "allow-once", Name: "Allow", Kind: "allow-once"},
		{OptionID: "reject-once", Name: "Reject", Kind: "reject-once"},
	}
	if a, ok := pickAllowOption(hyphenOnly); !ok || a.OptionID != "allow-once" {
		t.Fatalf("pickAllowOption(hyphen) = %+v, %v", a, ok)
	}
	if r, ok := pickRejectOption(hyphenOnly); !ok || r.OptionID != "reject-once" {
		t.Fatalf("pickRejectOption(hyphen) = %+v, %v", r, ok)
	}

	askOpts := []acpPermOption{
		{OptionID: "opt_a", Name: "Option A", Kind: "allow_once"},
		{OptionID: "opt_b", Name: "Option B", Kind: "allow_once"},
		{OptionID: cursorAskQuestionSkipID, Name: "Skip", Kind: "reject_once"},
	}
	if !isAskQuestionPerm(askOpts) {
		t.Fatal("options with __ask_question_skip__ must be detected as ask_question")
	}
}

func TestCursorBridgeAuthErrorSkipsAuthenticate(t *testing.T) {
	cases := []struct {
		name     string
		code     int
		message  string
		dataMsg  string
		wantText string
	}{
		{
			name:     "-32000 unauthenticated does not call authenticate",
			code:     -32000,
			message:  "Authentication required",
			dataMsg:  "Authentication required. Please run 'agent login' first, then call authenticate() with methodId 'cursor_login'.",
			wantText: "cursor authentication required",
		},
		{
			name:     "-32603 service init failure surfaces clear credential error",
			code:     -32603,
			message:  "Internal error",
			dataMsg:  "Failed to initialize session services",
			wantText: "failed to initialize session services",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tmp := t.TempDir()
			fakeCursor := filepath.Join(tmp, "fake-cursor-agent")
			script := `#!/usr/bin/env python3
import json, os, sys

def send(obj):
    sys.stdout.write(json.dumps(obj) + "\n")
    sys.stdout.flush()

for raw in sys.stdin:
    raw = raw.strip()
    if not raw:
        continue
    msg = json.loads(raw)
    mid = msg.get("id")
    method = msg.get("method", "")
    if method == "initialize":
        send({
            "jsonrpc": "2.0", "id": mid,
            "result": {
                "protocolVersion": 1,
                "agentCapabilities": {"loadSession": True},
                "authMethods": [{"id": "cursor_login", "name": "Cursor Login"}],
            },
        })
    elif method == "authenticate":
        send({"jsonrpc": "2.0", "id": mid, "error": {"code": -32099, "message": "FORBIDDEN_AUTHENTICATE_CALLED"}})
    elif method in ("session/new", "session/load"):
        send({
            "jsonrpc": "2.0", "id": mid,
            "error": {"code": int(os.environ["MOCK_ERR_CODE"]), "message": os.environ["MOCK_ERR_MSG"], "data": {"message": os.environ["MOCK_ERR_DATA"]}},
        })
`
			if err := os.WriteFile(fakeCursor, []byte(script), 0o755); err != nil {
				t.Fatal(err)
			}
			t.Setenv("MOCK_ERR_CODE", itoa(tc.code))
			t.Setenv("MOCK_ERR_MSG", tc.message)
			t.Setenv("MOCK_ERR_DATA", tc.dataMsg)

			collieHome := filepath.Join(tmp, "collie-home")
			pr, pw := io.Pipe()
			defer pw.Close()
			var out bytes.Buffer
			b := &acpBridge{
				provider: cursorProvider{},
				cfg: acpBridgeConfig{
					Cwd:        tmp,
					CollieHome: collieHome,
					Bin:        fakeCursor,
					Bypass:     true,
				},
				transcriptDir: filepath.Join(collieHome, "transcripts", "cursor"),
				stdin:         pr,
				stdout:        &out,
				stderr:        &out,
			}
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			err := b.run(ctx)
			if err == nil {
				t.Fatal("expected auth/init error, got nil")
			}
			if strings.Contains(err.Error(), "FORBIDDEN_AUTHENTICATE_CALLED") {
				t.Fatalf("bridge illegally called authenticate: %v", err)
			}
			if !strings.Contains(strings.ToLower(err.Error()), tc.wantText) {
				t.Fatalf("err = %q, want substring %q", err.Error(), tc.wantText)
			}
			if !workerQuotaRe.MatchString(err.Error()) {
				t.Fatalf("err %q should match workerQuotaRe", err.Error())
			}
		})
	}
}

func TestCursorBridgeEndToEndWithFakeACP(t *testing.T) {
	t.Parallel()
	tmp := t.TempDir()
	fakeSock := filepath.Join(tmp, "herdr.sock")

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

	// Fake `cursor-agent` verifying PoC contract:
	// - global flags placed before `acp` (--force, --approve-mcps, --model), never --yolo/--effort
	// - NO_OPEN_BROWSER=1 set
	// - authenticate never invoked
	// - session_info_update updates pane metadata title
	// - request_permission uses toolCall.content (no rawInput) + allow-once/reject-once
	// - cursor/ask_question rejected with -32601 -> downgraded to request_permission with __ask_question_skip__
	// - session/load replays history
	fakeCursor := filepath.Join(tmp, "fake-cursor-agent")
	script := `#!/usr/bin/env python3
import json, os, sys

argv = sys.argv[1:]
if "acp" not in argv:
    sys.stderr.write("missing acp subcommand\n")
    sys.exit(2)
acp_idx = argv.index("acp")
if acp_idx != len(argv) - 1:
    sys.stderr.write(f"global flags must appear before acp: {argv}\n")
    sys.exit(2)
if "--yolo" in argv or "--effort" in argv:
    sys.stderr.write(f"forbidden flag in cursor acp argv: {argv}\n")
    sys.exit(2)
if os.environ.get("NO_OPEN_BROWSER") != "1":
    sys.stderr.write("NO_OPEN_BROWSER must be 1\n")
    sys.exit(2)

bypass = "--force" in argv
session_id = "cur-1111-2222-3333-444444444444"

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
        send({
            "jsonrpc": "2.0", "id": mid,
            "result": {
                "protocolVersion": 1,
                "agentCapabilities": {"loadSession": True},
                "authMethods": [{"id": "cursor_login", "name": "Cursor Login"}],
            },
        })
    elif method == "authenticate":
        send({"jsonrpc": "2.0", "id": mid, "error": {"code": -32099, "message": "authenticate must be skipped"}})
    elif method == "session/new":
        send({"jsonrpc": "2.0", "method": "session/update", "params": {
            "sessionId": session_id,
            "update": {"sessionUpdate": "session_info_update", "title": "Fix Cursor Bridge"},
        }})
        send({"jsonrpc": "2.0", "id": mid, "result": {
            "sessionId": session_id,
            "models": {"currentModelId": "auto", "availableModels": [{"modelId": "auto", "name": "Auto"}]},
        }})
    elif method == "session/load":
        sid = params["sessionId"]
        send({"jsonrpc": "2.0", "method": "session/update", "params": {"sessionId": sid, "update": {"sessionUpdate": "user_message_chunk", "messageId": "u-rep", "content": {"type": "text", "text": "replayed user"}}}})
        send({"jsonrpc": "2.0", "method": "session/update", "params": {"sessionId": sid, "update": {"sessionUpdate": "agent_message_chunk", "messageId": "a-rep", "content": {"type": "text", "text": "replayed answer"}}}})
        send({"jsonrpc": "2.0", "method": "session/update", "params": {"sessionId": sid, "update": {"sessionUpdate": "tool_call", "toolCallId": "tc-rep", "title": "Read README", "kind": "read", "status": "completed", "locations": [{"path": "/tmp/README.md"}], "content": [{"type": "content", "content": {"type": "text", "text": "ok"}}]}}})
        send({"jsonrpc": "2.0", "id": mid, "result": {"models": {"currentModelId": "auto"}}})
    elif method == "session/prompt":
        sid = params["sessionId"]
        prompt_text = (params.get("prompt") or [{}])[0].get("text", "")
        # Emit thought chunk (should be ignored in transcript)
        send({"jsonrpc": "2.0", "method": "session/update", "params": {"sessionId": sid, "update": {"sessionUpdate": "agent_thought_chunk", "content": {"type": "text", "text": "internal thought"}}}})
        # Send standard Cursor permission request (details in toolCall.content, NO rawInput)
        send({
            "jsonrpc": "2.0", "id": 100, "method": "session/request_permission",
            "params": {
                "sessionId": sid,
                "toolCall": {
                    "toolCallId": "perm-cmd-1",
                    "title": "Run shell command",
                    "kind": "execute",
                    "status": "pending",
                    "content": [{"type": "content", "content": {"type": "text", "text": "git status --short"}}],
                },
                "options": [
                    {"optionId": "allow-once", "name": "Allow once", "kind": "allow_once"},
                    {"optionId": "allow-always", "name": "Allow always", "kind": "allow_always"},
                    {"optionId": "reject-once", "name": "Reject", "kind": "reject_once"},
                ],
            },
        })
        perm_ans = json.loads(sys.stdin.readline())
        chosen_perm = perm_ans["result"]["outcome"]["optionId"]

        chosen_q = ""
        if "ask-question" in prompt_text:
            # First try cursor/ask_question (bridge must return -32601), then downgrade
            send({"jsonrpc": "2.0", "id": 101, "method": "cursor/ask_question", "params": {"question": "Pick target"}})
            q_err = json.loads(sys.stdin.readline())
            if (q_err.get("error") or {}).get("code") != -32601:
                sys.stderr.write(f"expected -32601 for cursor/ask_question, got {q_err}\n")
                sys.exit(2)
            send({
                "jsonrpc": "2.0", "id": 102, "method": "session/request_permission",
                "params": {
                    "sessionId": sid,
                    "toolCall": {
                        "toolCallId": "ask-q-1",
                        "title": "Choose deployment target",
                        "kind": "other",
                        "status": "pending",
                        "content": [{"type": "content", "content": {"type": "text", "text": "Which environment?"}}],
                    },
                    "options": [
                        {"optionId": "opt_staging", "name": "Staging", "kind": "allow_once"},
                        {"optionId": "opt_prod", "name": "Production", "kind": "allow_once"},
                        {"optionId": "__ask_question_skip__", "name": "Skip", "kind": "reject_once"},
                    ],
                },
            })
            q_ans = json.loads(sys.stdin.readline())
            chosen_q = q_ans["result"]["outcome"]["optionId"]

        send({"jsonrpc": "2.0", "method": "session/update", "params": {"sessionId": sid, "update": {"sessionUpdate": "tool_call", "toolCallId": "tc1", "title": "Run shell command", "kind": "execute", "status": "in_progress"}}})
        send({"jsonrpc": "2.0", "method": "session/update", "params": {"sessionId": sid, "update": {"sessionUpdate": "tool_call_update", "toolCallId": "tc1", "title": "Run shell command", "kind": "execute", "status": "completed", "locations": [{"path": "/tmp/work"}], "content": [{"type": "content", "content": {"type": "text", "text": "M main.go\n"}}]}}})
        reply = f"perm={chosen_perm}" + (f" q={chosen_q}" if chosen_q else "") + f" bypass={int(bypass)}"
        send({"jsonrpc": "2.0", "method": "session/update", "params": {"sessionId": sid, "update": {"sessionUpdate": "agent_message_chunk", "messageId": "m1", "content": {"type": "text", "text": reply}}}})
        send({"jsonrpc": "2.0", "id": mid, "result": {"stopReason": "end_turn"}})
`
	if err := os.WriteFile(fakeCursor, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}

	collieHome := filepath.Join(tmp, "collie-home")
	transcriptDir := filepath.Join(collieHome, "transcripts", "cursor")

	// Part 1: AskPermissions (Bypass=false) verifies toolCall.content extraction,
	// numbered approval menu parsing, allow-once round-trip, and transcript JSONL.
	pr, pw := io.Pipe()
	var outBuf bytes.Buffer
	b := &acpBridge{
		provider: cursorProvider{},
		cfg: acpBridgeConfig{
			Cwd:        tmp,
			CollieHome: collieHome,
			Bin:        fakeCursor,
			Model:      "auto",
			Bypass:     false,
		},
		herdrSock:     fakeSock,
		paneID:        "w3:p1",
		transcriptDir: transcriptDir,
		stdin:         pr,
		stdout:        &outBuf,
		stderr:        &outBuf,
		tty:           false,
	}

	var seenMenu string
	go func() {
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
		_, _ = pw.Write([]byte("check status\r"))
		for time.Now().Before(deadline) {
			b.stateMu.Lock()
			blocked := b.activePerm != nil
			b.stateMu.Unlock()
			if blocked {
				b.outMu.Lock()
				seenMenu = outBuf.String()
				b.outMu.Unlock()
				break
			}
			time.Sleep(20 * time.Millisecond)
		}
		_, _ = pw.Write([]byte("1"))
		for time.Now().Before(deadline) {
			b.outMu.Lock()
			cur := outBuf.String()
			b.outMu.Unlock()
			if strings.Contains(cur, "perm=allow-once bypass=0") {
				break
			}
			time.Sleep(20 * time.Millisecond)
		}
		_ = pw.Close()
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := b.run(ctx); err != nil {
		t.Fatalf("cursor bridge (ask) failed: %v\noutput:\n%s", err, outBuf.String())
	}

	if !strings.Contains(seenMenu, "git status --short") {
		t.Fatalf("approval menu missing toolCall.content detail:\n%s", seenMenu)
	}
	if !screenAsks(seenMenu) {
		t.Fatalf("screenAsks failed on Cursor permission menu:\n%s", seenMenu)
	}
	ap := parseApproval(seenMenu)
	if len(ap.Choices) != 3 || ap.Choices[0].Label != "Allow once" || ap.Choices[2].Label != "Reject" {
		t.Fatalf("parseApproval choices = %+v", ap.Choices)
	}

	// Verify Herdr calls use source=collie:cursor, agent=cursor, and plain resume_argv[0].
	var states, methods []string
	rpcMu.Lock()
	for _, c := range herdrCalls {
		m, _ := c["method"].(string)
		methods = append(methods, m)
		if p, ok := c["params"].(map[string]any); ok {
			if p["source"] != cursorBridgeSource || p["agent"] != "cursor" {
				t.Errorf("%s unexpected source/agent: %v / %v", m, p["source"], p["agent"])
			}
			if argv, ok := p["resume_argv"].([]any); ok && len(argv) >= 3 {
				if argv[0] != "collie" || argv[1] != "_bridge" || argv[2] != "cursor" {
					t.Errorf("%s resume_argv = %v", m, argv)
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
	for _, wantM := range []string{"pane.report_agent", "pane.report_agent_session", "pane.release_agent"} {
		if !slices.Contains(methods, wantM) {
			t.Fatalf("missing herdr call %q in %v", wantM, methods)
		}
	}
	for _, wantSt := range []string{"idle", "working", "blocked"} {
		if !slices.Contains(states, wantSt) {
			t.Fatalf("states %v missing %q", states, wantSt)
		}
	}

	// Verify transcript written under transcripts/cursor/<sid>.jsonl and parsed by acpLine.
	logPath := filepath.Join(transcriptDir, "cur-1111-2222-3333-444444444444.jsonl")
	rawBefore, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("transcript missing: %v", err)
	}
	if strings.Contains(string(rawBefore), "internal thought") {
		t.Fatal("agent_thought_chunk should not leak into transcript")
	}
	tr := newTranscript(logPath, "cursor")
	tr.update()
	msgs := tr.view()
	if len(msgs) != 3 {
		t.Fatalf("got %d msgs: %+v", len(msgs), msgs)
	}
	if msgs[0].Kind != "user" || msgs[0].Text != "check status" {
		t.Errorf("msg[0] = %+v", msgs[0])
	}
	if msgs[1].Kind != "tool" || msgs[1].Name != "Run shell command" || msgs[1].Input != "/tmp/work" || msgs[1].Output != "M main.go" {
		t.Errorf("msg[1] = %+v", msgs[1])
	}
	if msgs[2].Kind != "assistant" || msgs[2].Text != "perm=allow-once bypass=0" {
		t.Errorf("msg[2] = %+v", msgs[2])
	}

	// Part 2: Bypass=true auto-approves tool permission with allow-once,
	// yet MUST still block on downgraded ask_question (__ask_question_skip__)!
	prB, pwB := io.Pipe()
	var outB bytes.Buffer
	bBypass := &acpBridge{
		provider: cursorProvider{},
		cfg: acpBridgeConfig{
			Cwd:        tmp,
			CollieHome: collieHome,
			Bin:        fakeCursor,
			Bypass:     true,
		},
		herdrSock:     fakeSock,
		paneID:        "w3:p2",
		transcriptDir: transcriptDir,
		stdin:         prB,
		stdout:        &outB,
		stderr:        &outB,
		tty:           false,
	}

	var questionPrompt *acpPermRequest
	var paneMetaTitle string
	go func() {
		deadline := time.Now().Add(5 * time.Second)
		for time.Now().Before(deadline) {
			bBypass.outMu.Lock()
			ready := strings.Contains(outB.String(), "❯ ")
			bBypass.outMu.Unlock()
			if ready {
				if meta, ok := readACPPaneMeta(transcriptDir, "w3:p2"); ok {
					paneMetaTitle = meta.Title
				}
				break
			}
			time.Sleep(20 * time.Millisecond)
		}
		_, _ = pwB.Write([]byte("trigger ask-question\r"))
		for time.Now().Before(deadline) {
			bBypass.stateMu.Lock()
			ap := bBypass.activePerm
			bBypass.stateMu.Unlock()
			if ap != nil {
				cp := *ap
				questionPrompt = &cp
				break
			}
			time.Sleep(20 * time.Millisecond)
		}
		// Pick option 2 ("Production" -> opt_prod)
		_, _ = pwB.Write([]byte("2"))
		for time.Now().Before(deadline) {
			bBypass.outMu.Lock()
			cur := outB.String()
			bBypass.outMu.Unlock()
			if strings.Contains(cur, "perm=allow-once q=opt_prod bypass=1") {
				break
			}
			time.Sleep(20 * time.Millisecond)
		}
		_ = pwB.Close()
	}()

	if err := bBypass.run(ctx); err != nil {
		t.Fatalf("cursor bridge (bypass + ask_question) failed: %v\noutput:\n%s", err, outB.String())
	}
	if paneMetaTitle != "Fix Cursor Bridge" {
		t.Errorf("paneMetaTitle = %q, want 'Fix Cursor Bridge' from session_info_update", paneMetaTitle)
	}
	if questionPrompt == nil || !questionPrompt.IsQuestion || questionPrompt.Command != "Which environment?" {
		t.Fatalf("ask_question in Bypass mode did not prompt user properly: %+v", questionPrompt)
	}
	if !strings.Contains(outB.String(), "perm=allow-once q=opt_prod bypass=1") {
		t.Fatalf("unexpected bypass+question output:\n%s", outB.String())
	}
	if argv := bBypass.resumeArgv(); !slices.Contains(argv, "--force") || slices.Contains(argv, "-y") || slices.Contains(argv, "--yolo") {
		t.Fatalf("cursor bypass resumeArgv = %#v, want --force only", argv)
	}

	// Part 3A: Resume WITH existing JSONL must NOT duplicate replayed history.
	if err := os.WriteFile(logPath, rawBefore, 0o600); err != nil {
		t.Fatal(err)
	}
	prR1, pwR1 := io.Pipe()
	_ = pwR1.Close()
	var outR1 bytes.Buffer
	bR1 := &acpBridge{
		provider: cursorProvider{},
		cfg: acpBridgeConfig{
			Cwd:        tmp,
			CollieHome: collieHome,
			Bin:        fakeCursor,
			Bypass:     true,
			Resume:     "cur-1111-2222-3333-444444444444",
		},
		transcriptDir: transcriptDir,
		stdin:         prR1,
		stdout:        &outR1,
		stderr:        &outR1,
	}
	if err := bR1.run(ctx); err != nil {
		t.Fatalf("resume with existing log failed: %v", err)
	}
	rawAfter, _ := os.ReadFile(logPath)
	if string(rawAfter) != string(rawBefore) {
		t.Fatalf("session/load replay duplicated existing JSONL:\nbefore=%s\nafter=%s", rawBefore, rawAfter)
	}

	// Part 3B: Resume WITHOUT existing JSONL rebuilds history from replay.
	_ = os.Remove(logPath)
	prR2, pwR2 := io.Pipe()
	_ = pwR2.Close()
	var outR2 bytes.Buffer
	bR2 := &acpBridge{
		provider: cursorProvider{},
		cfg: acpBridgeConfig{
			Cwd:        tmp,
			CollieHome: collieHome,
			Bin:        fakeCursor,
			Bypass:     true,
			Resume:     "cur-1111-2222-3333-444444444444",
		},
		transcriptDir: transcriptDir,
		stdin:         prR2,
		stdout:        &outR2,
		stderr:        &outR2,
	}
	if err := bR2.run(ctx); err != nil {
		t.Fatalf("resume without existing log failed: %v", err)
	}
	tr2 := newTranscript(logPath, "cursor")
	tr2.update()
	rebuilt := tr2.view()
	if len(rebuilt) != 3 || rebuilt[0].Text != "replayed user" || rebuilt[1].Text != "replayed answer" || rebuilt[2].Name != "Read README" {
		t.Fatalf("rebuilt cursor transcript mismatch: %+v", rebuilt)
	}
}

func TestSuperSaveSettingsCursorWorkerOnlyAndParameterizedModel(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	db, err := openDB(filepath.Join(dir, "herd.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	st := defaultState(dir)
	st.Settings.RouterKind = "claude"
	st.Settings.WorkerKind = "claude"
	srv := &Server{
		home:  dir,
		herdr: &Herdr{local: &Machine{Local: true, Name: "hub", Home: dir}, remote: map[string]*Machine{}},
	}
	srv.sup = &Super{s: srv, dir: dir, db: db, st: st, subs: map[chan struct{}]bool{}}

	payload := map[string]any{
		"settings": map[string]any{
			"router_kind":   "cursor",
			"worker_kind":   "cursor",
			"worker_model":  "claude-opus-4-8[context=1m,effort=high,fast=false]",
			"worker_effort": "high",
			"worker_cwd":    dir,
			"notify_level":  "quiet",
			"dream_at":      "04:00",
		},
	}
	raw, _ := json.Marshal(payload)
	req := httptest.NewRequest(http.MethodPost, "/api/super/settings", bytes.NewReader(raw))
	rec := httptest.NewRecorder()
	srv.superSaveSettings(rec, req)
	if rec.Code != 200 {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	got := srv.sup.st.Settings
	if got.RouterKind != "claude" {
		t.Fatalf("RouterKind = %q, want claude (cursor forbidden as router)", got.RouterKind)
	}
	if got.WorkerKind != "cursor" {
		t.Fatalf("WorkerKind = %q, want cursor", got.WorkerKind)
	}
	if got.WorkerModel != "claude-opus-4-8[context=1m,effort=high,fast=false]" {
		t.Fatalf("WorkerModel = %q, want parameterized Cursor model preserved", got.WorkerModel)
	}
	if got.WorkerEffort != "" {
		t.Fatalf("WorkerEffort = %q, want empty for cursor", got.WorkerEffort)
	}
}
