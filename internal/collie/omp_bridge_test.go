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

func TestOmpBridgeArgsAndACPArgs(t *testing.T) {
	t.Parallel()
	t.Run("bypass passes --allow-home and --approval-mode yolo before acp", func(t *testing.T) {
		cfg, err := parseOmpBridgeArgs([]string{"--auto-approve", "--model", "anthropic/claude-sonnet-4-5", "--effort", "high"})
		if err != nil {
			t.Fatal(err)
		}
		got := ompACPArgs(cfg)
		want := []string{"--allow-home", "--approval-mode", "yolo", "--model", "anthropic/claude-sonnet-4-5", "--thinking", "high", "acp"}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("got %#v, want %#v", got, want)
		}
	})

	t.Run("non-bypass explicitly passes --approval-mode always-ask before acp", func(t *testing.T) {
		cfg, err := parseOmpBridgeArgs([]string{"--model", "openai/gpt-5.4", "--thinking", "xhigh", "--omp-bin", "/tmp/omp"})
		if err != nil {
			t.Fatal(err)
		}
		if cfg.Bin != "/tmp/omp" || cfg.Bypass {
			t.Fatalf("parsed cfg = %+v", cfg)
		}
		got := ompACPArgs(cfg)
		want := []string{"--allow-home", "--approval-mode", "always-ask", "--model", "openai/gpt-5.4", "--thinking", "xhigh", "acp"}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("got %#v, want %#v", got, want)
		}
	})

	t.Run("explicit --approval-mode write is preserved and yolo enables bypass", func(t *testing.T) {
		cfgW, err := parseOmpBridgeArgs([]string{"--approval-mode", "write"})
		if err != nil {
			t.Fatal(err)
		}
		if cfgW.Bypass {
			t.Fatalf("write mode should not set Bypass")
		}
		wantW := []string{"--allow-home", "--approval-mode", "write", "acp"}
		if got := ompACPArgs(cfgW); !reflect.DeepEqual(got, wantW) {
			t.Fatalf("got %#v, want %#v", got, wantW)
		}

		cfgY, err := parseOmpBridgeArgs([]string{"--approval-mode=yolo"})
		if err != nil {
			t.Fatal(err)
		}
		if !cfgY.Bypass {
			t.Fatalf("yolo mode should set Bypass")
		}
		wantY := []string{"--allow-home", "--approval-mode", "yolo", "acp"}
		if got := ompACPArgs(cfgY); !reflect.DeepEqual(got, wantY) {
			t.Fatalf("got %#v, want %#v", got, wantY)
		}
	})

	t.Run("model with :thinking suffix omits duplicate --thinking while :free keeps it", func(t *testing.T) {
		cfg1, err := parseOmpBridgeArgs([]string{"--auto-approve", "--model", "openai/gpt-5.4:high", "--effort", "medium"})
		if err != nil {
			t.Fatal(err)
		}
		got1 := ompACPArgs(cfg1)
		if slices.Contains(got1, "--thinking") {
			t.Fatalf("duplicate --thinking emitted for suffixed model: %#v", got1)
		}

		cfg2, err := parseOmpBridgeArgs([]string{"--auto-approve", "--model", "openrouter/model:free", "--effort", "medium"})
		if err != nil {
			t.Fatal(err)
		}
		got2 := ompACPArgs(cfg2)
		want2 := []string{"--allow-home", "--approval-mode", "yolo", "--model", "openrouter/model:free", "--thinking", "medium", "acp"}
		if !reflect.DeepEqual(got2, want2) {
			t.Fatalf("got %#v, want %#v", got2, want2)
		}
	})
}

func TestOmpBridgeFormatSessionOpenError(t *testing.T) {
	t.Parallel()
	err := (ompProvider{}).FormatSessionOpenError("session/new", &rpcError{Code: -32000, Message: "No API key configured"})
	if !strings.Contains(err.Error(), "~/.omp") {
		t.Fatalf("expected ~/.omp hint in error: %v", err)
	}
	if !workerQuotaRe.MatchString(err.Error()) {
		t.Fatalf("error %q should match workerQuotaRe", err.Error())
	}
}

func TestOmpBridgeEndToEndWithFakeACP(t *testing.T) {
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

	fakeOmp := filepath.Join(tmp, "fake-omp")
	script := `#!/usr/bin/env python3
import json, sys

argv = sys.argv[1:]
if "acp" not in argv or argv.index("acp") != len(argv) - 1:
    sys.stderr.write(f"global flags must appear before acp: {argv}\n")
    sys.exit(2)
if "--allow-home" not in argv:
    sys.stderr.write(f"--allow-home is required: {argv}\n")
    sys.exit(2)
if "--approval-mode" not in argv:
    sys.stderr.write(f"--approval-mode is required: {argv}\n")
    sys.exit(2)
mode = argv[argv.index("--approval-mode") + 1]
thinking = argv[argv.index("--thinking") + 1] if "--thinking" in argv else "auto"

session_id = "omp-1111-2222-3333-444444444444"

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
                "agentInfo": {"name": "omp", "version": "17.0.6"},
            },
        })
    elif method == "session/new":
        # Emit >90KB available_commands_update to verify large frame handling.
        big_cmds = [{"name": f"cmd-{i}", "description": "x" * 900} for i in range(110)]
        send({"jsonrpc": "2.0", "method": "session/update", "params": {"sessionId": session_id, "update": {"sessionUpdate": "available_commands_update", "availableCommands": big_cmds}}})
        send({"jsonrpc": "2.0", "method": "session/update", "params": {"sessionId": session_id, "update": {"sessionUpdate": "session_info_update", "title": "Omp Refactor Task"}}})
        send({"jsonrpc": "2.0", "id": mid, "result": {
            "sessionId": session_id,
            "configOptions": [
                {"id": "model", "category": "model", "currentValue": "anthropic/claude-sonnet-4-5", "options": [{"value": "anthropic/claude-sonnet-4-5", "name": "Claude Sonnet 4.5"}]},
                {"id": "thinking", "category": "thought_level", "currentValue": thinking},
            ],
        }})
    elif method == "session/load":
        sid = params["sessionId"]
        send({"jsonrpc": "2.0", "method": "session/update", "params": {"sessionId": sid, "update": {"sessionUpdate": "user_message_chunk", "messageId": "u-rep", "content": {"type": "text", "text": "replayed omp user"}}}})
        send({"jsonrpc": "2.0", "method": "session/update", "params": {"sessionId": sid, "update": {"sessionUpdate": "agent_message_chunk", "messageId": "a-rep", "content": {"type": "text", "text": "replayed omp reply"}}}})
        send({"jsonrpc": "2.0", "id": mid, "result": {
            "configOptions": [
                {"id": "model", "category": "model", "currentValue": "anthropic/claude-sonnet-4-5", "options": [{"value": "anthropic/claude-sonnet-4-5", "name": "Claude Sonnet 4.5"}]},
                {"id": "thinking", "category": "thought_level", "currentValue": thinking},
            ],
        }})
    elif method == "session/prompt":
        sid = params["sessionId"]
        send({
            "jsonrpc": "2.0", "id": 10, "method": "session/request_permission",
            "params": {
                "sessionId": sid,
                "toolCall": {"toolCallId": "tc-perm", "title": "bash", "kind": "execute", "rawInput": {"command": "go test ./..."}},
                "options": [
                    {"optionId": "allow_once", "name": "Allow once", "kind": "allow_once"},
                    {"optionId": "allow_always", "name": "Allow always", "kind": "allow_always"},
                    {"optionId": "reject_once", "name": "Reject", "kind": "reject_once"},
                ],
            },
        })
        ans = json.loads(sys.stdin.readline())
        chosen = ans["result"]["outcome"]["optionId"]
        send({"jsonrpc": "2.0", "method": "session/update", "params": {"sessionId": sid, "update": {"sessionUpdate": "tool_call", "toolCallId": "tc-1", "title": "bash", "kind": "execute", "status": "in_progress", "rawInput": {"command": "go test ./..."}}}})
        send({"jsonrpc": "2.0", "method": "session/update", "params": {"sessionId": sid, "update": {"sessionUpdate": "tool_call_update", "toolCallId": "tc-1", "title": "bash", "kind": "execute", "status": "completed", "rawOutput": "PASS\n"}}})
        send({"jsonrpc": "2.0", "method": "session/update", "params": {"sessionId": sid, "update": {"sessionUpdate": "agent_message_chunk", "messageId": "m1", "content": {"type": "text", "text": f"mode={mode} thinking={thinking} perm={chosen}"}}}})
        send({"jsonrpc": "2.0", "id": mid, "result": {"stopReason": "end_turn"}})
`
	if err := os.WriteFile(fakeOmp, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}

	collieHome := filepath.Join(tmp, "collie-home")
	transcriptDir := filepath.Join(collieHome, "transcripts", "omp")

	// Part 1: AskPermissions=true (Bypass=false -> --approval-mode always-ask)
	pr, pw := io.Pipe()
	var outBuf bytes.Buffer
	b := &acpBridge{
		provider: ompProvider{},
		cfg: acpBridgeConfig{
			Cwd:        tmp,
			CollieHome: collieHome,
			Bin:        fakeOmp,
			Model:      "anthropic/claude-sonnet-4-5",
			Effort:     "high",
			Bypass:     false,
		},
		herdrSock:     fakeSock,
		paneID:        "w4:p1",
		transcriptDir: transcriptDir,
		stdin:         pr,
		stdout:        &outBuf,
		stderr:        &outBuf,
	}

	var seenMenu string
	var seenMeta acpPaneMeta
	go func() {
		deadline := time.Now().Add(5 * time.Second)
		for time.Now().Before(deadline) {
			b.outMu.Lock()
			ready := strings.Contains(outBuf.String(), "❯ ")
			b.outMu.Unlock()
			if ready {
				seenMeta, _ = readACPPaneMeta(transcriptDir, "w4:p1")
				break
			}
			time.Sleep(20 * time.Millisecond)
		}
		_, _ = pw.Write([]byte("run tests\r"))
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
			if strings.Contains(cur, "mode=always-ask thinking=high perm=allow_once") {
				break
			}
			time.Sleep(20 * time.Millisecond)
		}
		_ = pw.Close()
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := b.run(ctx); err != nil {
		t.Fatalf("omp bridge (ask) failed: %v\noutput:\n%s", err, outBuf.String())
	}

	if seenMeta.Title != "Omp Refactor Task" || seenMeta.Model != "Claude Sonnet 4.5" || seenMeta.Effort != "high" {
		t.Fatalf("pane meta = %+v, want title/model/effort populated from configOptions", seenMeta)
	}
	if !screenAsks(seenMenu) {
		t.Fatalf("screenAsks failed on omp permission menu:\n%s", seenMenu)
	}
	ap := parseApproval(seenMenu)
	if len(ap.Choices) != 3 || ap.Choices[0].Label != "Allow once" {
		t.Fatalf("parseApproval = %+v", ap.Choices)
	}

	var states, methods []string
	rpcMu.Lock()
	for _, c := range herdrCalls {
		m, _ := c["method"].(string)
		methods = append(methods, m)
		if p, ok := c["params"].(map[string]any); ok {
			if p["source"] != ompBridgeSource || p["agent"] != "omp" {
				t.Errorf("%s unexpected source/agent: %v / %v", m, p["source"], p["agent"])
			}
			if argv, ok := p["resume_argv"].([]any); ok && len(argv) >= 3 {
				if argv[0] != "collie" || argv[1] != "_bridge" || argv[2] != "omp" {
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

	logPath := filepath.Join(transcriptDir, "omp-1111-2222-3333-444444444444.jsonl")
	rawBefore, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("transcript missing: %v", err)
	}
	tr := newTranscript(logPath, "omp")
	tr.update()
	msgs := tr.view()
	if len(msgs) != 3 || msgs[0].Text != "run tests" || msgs[1].Name != "bash" || msgs[1].Output != "PASS" || msgs[2].Text != "mode=always-ask thinking=high perm=allow_once" {
		t.Fatalf("parsed omp transcript = %+v", msgs)
	}

	// Part 2: Bypass=true passes --approval-mode yolo and auto-allows any override permission request.
	prB, pwB := io.Pipe()
	var outB bytes.Buffer
	bBypass := &acpBridge{
		provider: ompProvider{},
		cfg: acpBridgeConfig{
			Cwd:        tmp,
			CollieHome: collieHome,
			Bin:        fakeOmp,
			Bypass:     true,
		},
		transcriptDir: transcriptDir,
		stdin:         prB,
		stdout:        &outB,
		stderr:        &outB,
	}
	go func() {
		_, _ = pwB.Write([]byte("quick check\r"))
		_ = pwB.Close()
	}()
	if err := bBypass.run(ctx); err != nil {
		t.Fatalf("omp bridge (bypass) failed: %v\noutput:\n%s", err, outB.String())
	}
	if !strings.Contains(outB.String(), "mode=yolo thinking=auto perm=allow_once") {
		t.Fatalf("bypass output mismatch:\n%s", outB.String())
	}
	if argv := bBypass.resumeArgv(); !slices.Contains(argv, "--auto-approve") {
		t.Fatalf("omp bypass resumeArgv = %#v, want --auto-approve", argv)
	}

	// Part 3A: Resume with existing JSONL does not duplicate replayed history.
	if err := os.WriteFile(logPath, rawBefore, 0o600); err != nil {
		t.Fatal(err)
	}
	prR1, pwR1 := io.Pipe()
	_ = pwR1.Close()
	var outR1 bytes.Buffer
	bR1 := &acpBridge{
		provider: ompProvider{},
		cfg: acpBridgeConfig{
			Cwd:        tmp,
			CollieHome: collieHome,
			Bin:        fakeOmp,
			Bypass:     true,
			Resume:     "omp-1111-2222-3333-444444444444",
		},
		transcriptDir: transcriptDir,
		stdin:         prR1,
		stdout:        &outR1,
		stderr:        &outR1,
	}
	if err := bR1.run(ctx); err != nil {
		t.Fatalf("omp resume with existing log failed: %v", err)
	}
	rawAfter, _ := os.ReadFile(logPath)
	if string(rawAfter) != string(rawBefore) {
		t.Fatalf("session/load replay duplicated existing JSONL")
	}

	// Part 3B: Resume without existing JSONL rebuilds history from replay.
	_ = os.Remove(logPath)
	prR2, pwR2 := io.Pipe()
	_ = pwR2.Close()
	var outR2 bytes.Buffer
	bR2 := &acpBridge{
		provider: ompProvider{},
		cfg: acpBridgeConfig{
			Cwd:        tmp,
			CollieHome: collieHome,
			Bin:        fakeOmp,
			Bypass:     true,
			Resume:     "omp-1111-2222-3333-444444444444",
		},
		transcriptDir: transcriptDir,
		stdin:         prR2,
		stdout:        &outR2,
		stderr:        &outR2,
	}
	if err := bR2.run(ctx); err != nil {
		t.Fatalf("omp resume without existing log failed: %v", err)
	}
	tr2 := newTranscript(logPath, "omp")
	tr2.update()
	rebuilt := tr2.view()
	if len(rebuilt) != 2 || rebuilt[0].Text != "replayed omp user" || rebuilt[1].Text != "replayed omp reply" {
		t.Fatalf("rebuilt omp transcript = %+v", rebuilt)
	}
}

func TestSuperSaveSettingsPiAndOmpWorkerOnly(t *testing.T) {
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

	for _, tc := range []struct {
		routerKind   string
		workerKind   string
		workerModel  string
		workerEffort string
		wantWorker   string
		wantEffort   string
	}{
		{"pi", "pi", "anthropic/claude-sonnet-4-5", "high", "pi", "high"},
		{"omp", "omp", "openai/gpt-5.4:high", "medium", "omp", ""},
		{"oh-my-pi", "oh-my-pi", "deepseek/deepseek-chat", "auto", "oh-my-pi", "auto"},
	} {
		payload := map[string]any{
			"settings": map[string]any{
				"router_kind":   tc.routerKind,
				"worker_kind":   tc.workerKind,
				"worker_model":  tc.workerModel,
				"worker_effort": tc.workerEffort,
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
			t.Fatalf("%s: status = %d, body = %s", tc.workerKind, rec.Code, rec.Body.String())
		}
		got := srv.sup.st.Settings
		if got.RouterKind != "claude" {
			t.Fatalf("%s: RouterKind = %q, want claude (forbidden as router)", tc.routerKind, got.RouterKind)
		}
		if got.WorkerKind != tc.wantWorker {
			t.Fatalf("WorkerKind = %q, want %q", got.WorkerKind, tc.wantWorker)
		}
		if got.WorkerEffort != tc.wantEffort {
			t.Fatalf("%s: WorkerEffort = %q, want %q", tc.workerKind, got.WorkerEffort, tc.wantEffort)
		}
	}
}
