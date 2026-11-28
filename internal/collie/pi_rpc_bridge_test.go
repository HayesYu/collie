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

func TestPiBridgeArgsAndRPCArgs(t *testing.T) {
	t.Parallel()
	t.Run("bypass passes --approve, --model, --thinking, and --session", func(t *testing.T) {
		cfg, err := parsePiBridgeArgs([]string{"--approve", "--model", "anthropic/claude-sonnet-4-5", "--effort", "high", "--resume", "pi-sess-1"})
		if err != nil {
			t.Fatal(err)
		}
		got := piRPCArgs(cfg)
		want := []string{"--mode", "rpc", "--approve", "--model", "anthropic/claude-sonnet-4-5", "--thinking", "high", "--session", "pi-sess-1"}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("got %#v, want %#v", got, want)
		}
	})

	t.Run("non-bypass omits --approve and accepts --thinking / --session aliases", func(t *testing.T) {
		cfg, err := parsePiBridgeArgs([]string{"--model=openai/gpt-5.4", "--thinking=low", "--session=s-abc", "--pi-bin", "/tmp/pi"})
		if err != nil {
			t.Fatal(err)
		}
		if cfg.Bin != "/tmp/pi" || cfg.Bypass {
			t.Fatalf("parsed cfg = %+v", cfg)
		}
		got := piRPCArgs(cfg)
		want := []string{"--mode", "rpc", "--model", "openai/gpt-5.4", "--thinking", "low", "--session", "s-abc"}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("got %#v, want %#v", got, want)
		}
	})

	t.Run("model with :thinking suffix suppresses duplicate --thinking", func(t *testing.T) {
		cfg, err := parsePiBridgeArgs([]string{"--approve", "--model", "anthropic/claude-sonnet-4-5:medium", "--effort", "high"})
		if err != nil {
			t.Fatal(err)
		}
		got := piRPCArgs(cfg)
		want := []string{"--mode", "rpc", "--approve", "--model", "anthropic/claude-sonnet-4-5:medium"}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("got %#v, want %#v", got, want)
		}
	})
}

func TestPiBridgeEndToEndWithFakeRPC(t *testing.T) {
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

	fakePi := filepath.Join(tmp, "fake-pi")
	script := `#!/usr/bin/env python3
import json, sys

argv = sys.argv[1:]
if "--mode" not in argv or argv[argv.index("--mode") + 1] != "rpc":
    sys.stderr.write(f"expected --mode rpc: {argv}\n")
    sys.exit(2)

session_id = "pi-1111-2222-3333-444444444444"
if "--session" in argv:
    session_id = argv[argv.index("--session") + 1]

def send(obj):
    sys.stdout.write(json.dumps(obj, ensure_ascii=False) + "\n")
    sys.stdout.flush()

for raw in sys.stdin:
    raw = raw.strip()
    if not raw:
        continue
    msg = json.loads(raw)
    t = msg.get("type")
    mid = msg.get("id")
    if t == "get_state":
        # Include a >90KB field to confirm ReadBytes('\n') handles large JSONL lines.
        send({
            "id": mid, "type": "response", "command": "get_state", "success": True,
            "data": {
                "sessionId": session_id,
                "sessionFile": f"/tmp/{session_id}.jsonl",
                "sessionName": "Initial Pi Session",
                "thinkingLevel": "high",
                "model": {"id": "claude-sonnet-4-5", "name": "Claude Sonnet 4.5", "provider": "anthropic"},
                "bigPadding": "x" * 95000,
            },
        })
    elif t == "get_messages":
        send({
            "id": mid, "type": "response", "command": "get_messages", "success": True,
            "data": {
                "messages": [
                    {"role": "user", "content": "old pi question"},
                    {"role": "assistant", "content": [
                        {"type": "thinking", "thinking": "hidden"},
                        {"type": "toolCall", "id": "old-tc1", "name": "read", "arguments": {"path": "README.md"}},
                        {"type": "text", "text": "old pi answer"},
                    ]},
                    {"role": "toolResult", "toolCallId": "old-tc1", "toolName": "read", "content": [{"type": "text", "text": "readme body"}], "isError": False},
                ],
            },
        })
    elif t == "prompt":
        text = msg.get("message", "")
        if "reject-prompt" in text:
            send({"id": mid, "type": "response", "command": "prompt", "success": False, "error": "no api key configured"})
            continue
        send({"id": mid, "type": "response", "command": "prompt", "success": True})
        send({"type": "agent_start"})
        # Fire-and-forget UI updates
        send({"type": "extension_ui_request", "id": "u-title", "method": "setTitle", "title": "Pi Updated Title"})
        send({"type": "extension_ui_request", "id": "u-status", "method": "setStatus", "statusKey": "k", "statusText": "running"})
        send({"type": "extension_ui_request", "id": "u-notify", "method": "notify", "message": "hook ready", "notifyType": "info"})

        if "ext-dialogs" in text:
            # 1) input dialog must auto-cancel so headless bridge never hangs
            send({"type": "extension_ui_request", "id": "dlg-in", "method": "input", "title": "Need input"})
            r_in = json.loads(sys.stdin.readline())
            if r_in.get("id") != "dlg-in" or not r_in.get("cancelled"):
                sys.stderr.write(f"input dialog not cancelled: {r_in}\n")
                sys.exit(2)
            # 2) editor dialog must auto-cancel too
            send({"type": "extension_ui_request", "id": "dlg-ed", "method": "editor", "title": "Edit note"})
            r_ed = json.loads(sys.stdin.readline())
            if r_ed.get("id") != "dlg-ed" or not r_ed.get("cancelled"):
                sys.stderr.write(f"editor dialog not cancelled: {r_ed}\n")
                sys.exit(2)
            # 3) confirm dialog answered with "1" -> confirmed: True
            send({"type": "extension_ui_request", "id": "dlg-cf", "method": "confirm", "title": "Apply patch?", "message": "Modify main.go"})
            r_cf = json.loads(sys.stdin.readline())
            # 4) select dialog answered with "2" -> value: "Staging"
            send({"type": "extension_ui_request", "id": "dlg-sel", "method": "select", "title": "Target env", "options": ["Dev", "Staging", "Prod"]})
            r_sel = json.loads(sys.stdin.readline())

            send({"type": "message_start", "message": {"role": "assistant"}})
            reply = f"cf={r_cf.get('confirmed')} sel={r_sel.get('value')}"
            send({"type": "message_update", "assistantMessageEvent": {"type": "text_delta", "contentIndex": 0, "delta": reply}})
            send({"type": "message_end", "message": {"role": "assistant", "stopReason": "stop"}})
            send({"type": "agent_end", "messages": [], "willRetry": False})
            send({"type": "agent_settled"})
            continue

        if "error-turn" in text:
            send({"type": "message_start", "message": {"role": "assistant"}})
            send({"type": "message_end", "message": {"role": "assistant", "stopReason": "error", "errorMessage": "Model not found: demo/missing"}})
            send({"type": "agent_end", "messages": [], "willRetry": False})
            send({"type": "agent_settled"})
            continue

        # Standard turn: thinking delta (ignored) + text delta + tool start/end + second assistant message
        send({"type": "message_start", "message": {"role": "assistant"}})
        send({"type": "message_update", "assistantMessageEvent": {"type": "thinking_delta", "contentIndex": 0, "delta": "secret thought"}})
        send({"type": "message_update", "assistantMessageEvent": {"type": "text_delta", "contentIndex": 1, "delta": "Checking "}})
        send({"type": "message_update", "assistantMessageEvent": {"type": "text_delta", "contentIndex": 1, "delta": "repo."}})
        send({"type": "message_end", "message": {"role": "assistant", "stopReason": "toolUse"}})

        send({"type": "tool_execution_start", "toolCallId": "tc-bash-1", "toolName": "bash", "args": {"command": "git status -s"}})
        send({"type": "tool_execution_end", "toolCallId": "tc-bash-1", "toolName": "bash", "result": {"content": [{"type": "text", "text": "M main.go\n"}]}, "isError": False})

        send({"type": "message_start", "message": {"role": "assistant"}})
        send({"type": "message_update", "assistantMessageEvent": {"type": "text_delta", "contentIndex": 0, "delta": "All clean."}})
        send({"type": "message_end", "message": {"role": "assistant", "stopReason": "stop"}})
        send({"type": "agent_end", "messages": [], "willRetry": False})
        send({"type": "agent_settled"})
`
	if err := os.WriteFile(fakePi, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}

	collieHome := filepath.Join(tmp, "collie-home")
	transcriptDir := filepath.Join(collieHome, "transcripts", "pi")

	// Part 1: Standard streaming + tools + extension UI dialogs + errors
	pr, pw := io.Pipe()
	var outBuf bytes.Buffer
	b := &piRPCBridge{
		cfg: acpBridgeConfig{
			Cwd:        tmp,
			CollieHome: collieHome,
			Bin:        fakePi,
			Model:      "anthropic/claude-sonnet-4-5",
			Effort:     "high",
			Bypass:     true,
		},
		herdrSock:     fakeSock,
		paneID:        "w5:p1",
		transcriptDir: transcriptDir,
		stdin:         pr,
		stdout:        &outBuf,
		stderr:        &outBuf,
	}

	var confirmMenu, selectMenu string
	var updatedTitle string
	go func() {
		deadline := time.Now().Add(8 * time.Second)
		waitForOut := func(sub string) {
			for time.Now().Before(deadline) {
				b.outMu.Lock()
				has := strings.Contains(outBuf.String(), sub)
				b.outMu.Unlock()
				if has {
					return
				}
				time.Sleep(15 * time.Millisecond)
			}
		}
		waitForOut("❯ ")

		// Turn 1: standard streaming + tool
		_, _ = pw.Write([]byte("\x1b[200~inspect\nrepo\x1b[201~\r"))
		waitForOut("All clean.")
		for time.Now().Before(deadline) {
			if meta, ok := readACPPaneMeta(transcriptDir, "w5:p1"); ok && meta.Title == "Pi Updated Title" {
				updatedTitle = meta.Title
				break
			}
			time.Sleep(15 * time.Millisecond)
		}

		// Turn 2: extension UI dialogs (input/editor auto-cancel, confirm=1, select=2)
		_, _ = pw.Write([]byte("ext-dialogs\r"))
		for time.Now().Before(deadline) {
			b.stateMu.Lock()
			dlg := b.activeDialog
			b.stateMu.Unlock()
			if dlg != nil && dlg.ID == "dlg-cf" {
				b.outMu.Lock()
				confirmMenu = outBuf.String()
				b.outMu.Unlock()
				break
			}
			time.Sleep(15 * time.Millisecond)
		}
		_, _ = pw.Write([]byte("1"))
		for time.Now().Before(deadline) {
			b.stateMu.Lock()
			dlg := b.activeDialog
			b.stateMu.Unlock()
			if dlg != nil && dlg.ID == "dlg-sel" {
				b.outMu.Lock()
				selectMenu = outBuf.String()
				b.outMu.Unlock()
				break
			}
			time.Sleep(15 * time.Millisecond)
		}
		_, _ = pw.Write([]byte("2"))
		waitForOut("cf=True sel=Staging")

		// Turn 3: prompt rejection error + Turn 4: message_end stopReason=error
		_, _ = pw.Write([]byte("reject-prompt\r"))
		waitForOut("no api key configured")
		_, _ = pw.Write([]byte("error-turn\r"))
		waitForOut("Model not found: demo/missing")
		_ = pw.Close()
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 12*time.Second)
	defer cancel()
	if err := b.run(ctx); err != nil {
		t.Fatalf("pi bridge failed: %v\noutput:\n%s", err, outBuf.String())
	}

	if updatedTitle != "Pi Updated Title" {
		t.Errorf("updatedTitle = %q, want 'Pi Updated Title'", updatedTitle)
	}
	if !screenAsks(confirmMenu) {
		t.Fatalf("screenAsks failed on confirm menu:\n%s", confirmMenu)
	}
	if ap := parseApproval(confirmMenu); len(ap.Choices) != 2 || ap.Choices[0].Label != "Yes" || ap.Choices[1].Label != "No" {
		t.Fatalf("confirm parseApproval = %+v", ap.Choices)
	}
	if ap := parseApproval(selectMenu); len(ap.Choices) != 3 || ap.Choices[1].Label != "Staging" {
		t.Fatalf("select parseApproval = %+v", ap.Choices)
	}

	// Verify Herdr reports
	var states, methods []string
	rpcMu.Lock()
	for _, c := range herdrCalls {
		m, _ := c["method"].(string)
		methods = append(methods, m)
		if p, ok := c["params"].(map[string]any); ok {
			if p["source"] != piBridgeSource || p["agent"] != "pi" {
				t.Errorf("%s unexpected source/agent: %v / %v", m, p["source"], p["agent"])
			}
			if argv, ok := p["resume_argv"].([]any); ok && len(argv) >= 4 {
				if argv[0] != "collie" || argv[1] != "_bridge" || argv[2] != "pi" || argv[3] != "--approve" {
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

	// Verify transcript parsing & no thinking leakage
	logPath := filepath.Join(transcriptDir, "pi-1111-2222-3333-444444444444.jsonl")
	rawLog, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("transcript missing: %v", err)
	}
	if strings.Contains(string(rawLog), "secret thought") {
		t.Fatal("thinking_delta leaked into transcript")
	}
	tr := newTranscript(logPath, "pi")
	tr.update()
	msgs := tr.view()
	if len(msgs) < 8 {
		t.Fatalf("expected >=8 messages, got %d: %+v", len(msgs), msgs)
	}
	if msgs[0].Kind != "user" || msgs[0].Text != "inspect\nrepo" {
		t.Errorf("msgs[0] = %+v", msgs[0])
	}
	if msgs[1].Kind != "assistant" || msgs[1].Text != "Checking repo." {
		t.Errorf("msgs[1] = %+v", msgs[1])
	}
	if msgs[2].Kind != "tool" || msgs[2].Name != "bash" || msgs[2].Input != "git status -s" || msgs[2].Output != "M main.go" {
		t.Errorf("msgs[2] = %+v", msgs[2])
	}
	if msgs[3].Kind != "assistant" || msgs[3].Text != "All clean." {
		t.Errorf("msgs[3] = %+v", msgs[3])
	}
	if msgs[5].Kind != "assistant" || msgs[5].Text != "cf=True sel=Staging" {
		t.Errorf("msgs[5] = %+v", msgs[5])
	}
	if !strings.Contains(msgs[7].Text, "no api key configured") {
		t.Errorf("msgs[7] = %+v, want prompt error", msgs[7])
	}

	// Part 2A: Resume WITH existing JSONL must not duplicate history.
	prR1, pwR1 := io.Pipe()
	_ = pwR1.Close()
	var outR1 bytes.Buffer
	bR1 := &piRPCBridge{
		cfg: acpBridgeConfig{
			Cwd:        tmp,
			CollieHome: collieHome,
			Bin:        fakePi,
			Bypass:     true,
			Resume:     "pi-1111-2222-3333-444444444444",
		},
		transcriptDir: transcriptDir,
		stdin:         prR1,
		stdout:        &outR1,
		stderr:        &outR1,
	}
	if err := bR1.run(ctx); err != nil {
		t.Fatalf("pi resume with existing log failed: %v", err)
	}
	rawAfter, _ := os.ReadFile(logPath)
	if string(rawAfter) != string(rawLog) {
		t.Fatalf("pi resume modified existing transcript")
	}

	// Part 2B: Resume WITHOUT existing JSONL backfills via get_messages.
	_ = os.Remove(logPath)
	prR2, pwR2 := io.Pipe()
	_ = pwR2.Close()
	var outR2 bytes.Buffer
	bR2 := &piRPCBridge{
		cfg: acpBridgeConfig{
			Cwd:        tmp,
			CollieHome: collieHome,
			Bin:        fakePi,
			Bypass:     true,
			Resume:     "pi-1111-2222-3333-444444444444",
		},
		transcriptDir: transcriptDir,
		stdin:         prR2,
		stdout:        &outR2,
		stderr:        &outR2,
	}
	if err := bR2.run(ctx); err != nil {
		t.Fatalf("pi resume without existing log failed: %v", err)
	}
	tr2 := newTranscript(logPath, "pi")
	tr2.update()
	rebuilt := tr2.view()
	if len(rebuilt) != 3 || rebuilt[0].Text != "old pi question" || rebuilt[1].Name != "read" || rebuilt[1].Output != "readme body" || rebuilt[2].Text != "old pi answer" {
		t.Fatalf("rebuilt pi transcript = %+v", rebuilt)
	}
}

func TestPiBridgeLegacyAgentEndAndAbort(t *testing.T) {
	t.Parallel()
	tmp := t.TempDir()
	fakePi := filepath.Join(tmp, "fake-pi-legacy")
	script := `#!/usr/bin/env python3
import json, sys, threading, time

def send(obj):
    sys.stdout.write(json.dumps(obj) + "\n")
    sys.stdout.flush()

abort_ev = threading.Event()
seen_cmds = []

def run_retry_then_end():
    send({"type": "agent_start"})
    # First agent_end has willRetry=True -> bridge must stay working.
    send({"type": "agent_end", "messages": [], "willRetry": True})
    time.sleep(0.06)
    send({"type": "auto_retry_start", "attempt": 1, "maxAttempts": 2, "delayMs": 10, "errorMessage": "529 overloaded"})
    send({"type": "auto_retry_end", "success": True, "attempt": 1})
    send({"type": "message_start", "message": {"role": "assistant"}})
    send({"type": "message_update", "assistantMessageEvent": {"type": "text_delta", "contentIndex": 0, "delta": "recovered"}})
    send({"type": "message_end", "message": {"role": "assistant", "stopReason": "stop"}})
    # Legacy pi omits agent_settled and only emits agent_end(willRetry=False).
    send({"type": "agent_end", "messages": [], "willRetry": False})

def run_slow():
    send({"type": "agent_start"})
    abort_ev.wait(timeout=5)
    send({"type": "message_start", "message": {"role": "assistant"}})
    send({"type": "message_update", "assistantMessageEvent": {"type": "text_delta", "contentIndex": 0, "delta": "aborted-ok:" + ",".join(seen_cmds)}})
    send({"type": "message_end", "message": {"role": "assistant", "stopReason": "aborted"}})
    send({"type": "agent_end", "messages": [], "willRetry": False})
    send({"type": "agent_settled"})

for raw in sys.stdin:
    raw = raw.strip()
    if not raw:
        continue
    msg = json.loads(raw)
    t = msg.get("type")
    mid = msg.get("id")
    if t == "get_state":
        send({"id": mid, "type": "response", "command": "get_state", "success": True, "data": {"sessionId": "pi-legacy-1", "thinkingLevel": "low"}})
    elif t == "prompt":
        send({"id": mid, "type": "response", "command": "prompt", "success": True})
        if msg.get("message") == "slow":
            threading.Thread(target=run_slow, daemon=True).start()
        else:
            threading.Thread(target=run_retry_then_end, daemon=True).start()
    elif t == "clear_queue":
        seen_cmds.append("clear_queue")
        send({"id": mid, "type": "response", "command": "clear_queue", "success": True, "data": {"steering": [], "followUp": []}})
    elif t == "abort":
        seen_cmds.append("abort")
        abort_ev.set()
        send({"id": mid, "type": "response", "command": "abort", "success": True})
`
	if err := os.WriteFile(fakePi, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}

	collieHome := filepath.Join(tmp, "collie-home")
	pr, pw := io.Pipe()
	var outBuf bytes.Buffer
	b := &piRPCBridge{
		cfg: acpBridgeConfig{
			Cwd:        tmp,
			CollieHome: collieHome,
			Bin:        fakePi,
			Bypass:     true,
		},
		transcriptDir: filepath.Join(collieHome, "transcripts", "pi"),
		stdin:         pr,
		stdout:        &outBuf,
		stderr:        &outBuf,
		endGraceDelay: 40 * time.Millisecond,
	}

	go func() {
		deadline := time.Now().Add(8 * time.Second)
		waitForOut := func(sub string) {
			for time.Now().Before(deadline) {
				b.outMu.Lock()
				has := strings.Contains(outBuf.String(), sub)
				b.outMu.Unlock()
				if has {
					return
				}
				time.Sleep(15 * time.Millisecond)
			}
		}
		waitForOut("❯ ")

		// 1) Retry + legacy agent_end without agent_settled
		_, _ = pw.Write([]byte("retry-test\r"))
		waitForOut("recovered")
		for time.Now().Before(deadline) {
			b.stateMu.Lock()
			w := b.working
			b.stateMu.Unlock()
			if !w {
				break
			}
			time.Sleep(15 * time.Millisecond)
		}

		// 2) Slow turn interrupted by Esc -> sends clear_queue + abort
		_, _ = pw.Write([]byte("slow\r"))
		for time.Now().Before(deadline) {
			b.stateMu.Lock()
			w := b.working
			b.stateMu.Unlock()
			if w {
				break
			}
			time.Sleep(15 * time.Millisecond)
		}
		time.Sleep(25 * time.Millisecond)
		_, _ = pw.Write([]byte{0x1b})
		waitForOut("aborted-ok:clear_queue,abort")
		_ = pw.Close()
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := b.run(ctx); err != nil {
		t.Fatalf("legacy/abort test failed: %v\noutput:\n%s", err, outBuf.String())
	}
	out := outBuf.String()
	if !strings.Contains(out, "recovered") {
		t.Fatalf("missing recovered output:\n%s", out)
	}
	if !strings.Contains(out, "aborted-ok:clear_queue,abort") {
		t.Fatalf("Esc did not send clear_queue + abort in order:\n%s", out)
	}
}
