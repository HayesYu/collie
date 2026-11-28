package collie

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
)

// Pi RPC bridge for `collie _bridge pi [flags]` driving native `pi --mode rpc`
// over newline-delimited JSONL without requiring any third-party pi-acp wrapper.
// It shares PTY/paste input, .panes metadata, JSONL transcript format, Herdr
// lifecycle reporting, and numbered menus from bridge_common.go.

const (
	piBridgeSource         = "collie:pi"
	piBridgeLogPrefix      = "[pi-bridge]"
	defaultPiEndGraceDelay = 150 * time.Millisecond
)

func piEnv(base []string) []string {
	return append([]string(nil), base...)
}

func parsePiBridgeArgs(args []string) (acpBridgeConfig, error) {
	return parseACPBridgeArgs(args, "--pi-bin")
}

// piRPCArgs builds the argv for `pi --mode rpc ...`.
// `--approve` trusts project-local `.pi/` resources when Bypass=true.
// `--effort` is translated to `--thinking` unless the model already specifies
// a `:<thinking>` suffix. `--resume` maps to `--session`.
func piRPCArgs(cfg acpBridgeConfig) []string {
	args := []string{"--mode", "rpc"}
	if cfg.Bypass {
		args = append(args, "--approve")
	}
	if cfg.Model != "" {
		args = append(args, "--model", cfg.Model)
	}
	if eff := effectiveEffort("pi", cfg.Model, cfg.Effort); eff != "" {
		args = append(args, "--thinking", eff)
	}
	if cfg.Resume != "" {
		args = append(args, "--session", cfg.Resume)
	}
	return args
}

func runPiBridgeCLI(args []string) {
	cfg, err := parsePiBridgeArgs(args)
	if err != nil {
		fmt.Fprintf(os.Stderr, "%s fatal: %v\n", piBridgeLogPrefix, err)
		os.Exit(2)
	}
	if cfg.Cwd == "" {
		cfg.Cwd, _ = os.Getwd()
	}
	if cfg.CollieHome == "" {
		cfg.CollieHome = collieDir()
	}
	home, _ := os.UserHomeDir()
	if cfg.Bin == "" {
		cfg.Bin = findBin("pi", home)
	}
	if cfg.Bin == "" {
		fmt.Fprintf(os.Stderr, "%s fatal: pi command not found in PATH\n", piBridgeLogPrefix)
		os.Exit(1)
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGHUP)
	defer stop()

	restore, rawOK := enterBridgeRawMode()
	if rawOK {
		defer restore()
	}

	b := &piRPCBridge{
		cfg:           cfg,
		herdrSock:     os.Getenv("HERDR_SOCKET_PATH"),
		paneID:        os.Getenv("HERDR_PANE_ID"),
		transcriptDir: filepath.Join(cfg.CollieHome, "transcripts", "pi"),
		stdin:         os.Stdin,
		stdout:        os.Stdout,
		stderr:        os.Stderr,
		tty:           rawOK,
		pending:       map[string]chan piRPCResponse{},
		promptQueue:   make(chan string, 64),
		permReply:     make(chan string, 4),
	}
	if err := b.run(ctx); err != nil {
		fmt.Fprintf(os.Stderr, "\n%s fatal: %v\n", piBridgeLogPrefix, err)
		os.Exit(1)
	}
}

// ---------------------------------------------------------------- wire types

type piRPCResponse struct {
	ID      string          `json:"id,omitempty"`
	Type    string          `json:"type"`
	Command string          `json:"command"`
	Success bool            `json:"success"`
	Data    json.RawMessage `json:"data,omitempty"`
	Error   string          `json:"error,omitempty"`
}

type piExtDialog struct {
	ID      string
	Method  string // "select" | "confirm"
	Title   string
	Message string
	Options []string
	Lines   int
}

// ---------------------------------------------------------------- bridge runtime

type piRPCBridge struct {
	cfg           acpBridgeConfig
	herdrSock     string
	paneID        string
	transcriptDir string
	stdin         io.Reader
	stdout        io.Writer
	stderr        io.Writer
	tty           bool
	endGraceDelay time.Duration

	cmd      *exec.Cmd
	rpcIn    io.WriteCloser
	rpcOut   io.ReadCloser
	rpcErr   io.ReadCloser
	writeMu  sync.Mutex
	outMu    sync.Mutex
	nextID   atomic.Int64
	herdrSeq atomic.Int64

	pendMu  sync.Mutex
	pending map[string]chan piRPCResponse

	stateMu      sync.Mutex
	sessionID    string
	sessionTitle string
	logPath      string
	logFile      *os.File
	modelLabel   string
	effortLabel  string
	working      bool
	streamedText bool
	msgSeq       int64
	curMsgID     string
	activeDialog *piExtDialog
	toolNames    map[string]string
	lastCtrlC    time.Time
	lastStderr   string

	turnSeq  int64
	turnDone chan struct{}
	endTimer *time.Timer

	promptQueue chan string
	permReply   chan string
	done        chan struct{}
}

func (b *piRPCBridge) run(ctx context.Context) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	if b.pending == nil {
		b.pending = map[string]chan piRPCResponse{}
	}
	if b.promptQueue == nil {
		b.promptQueue = make(chan string, 64)
	}
	if b.permReply == nil {
		b.permReply = make(chan string, 4)
	}
	if b.endGraceDelay <= 0 {
		b.endGraceDelay = defaultPiEndGraceDelay
	}
	b.done = make(chan struct{})
	defer close(b.done)

	if err := os.MkdirAll(b.transcriptDir, 0o700); err != nil {
		return fmt.Errorf("create transcript dir: %w", err)
	}
	b.toolNames = map[string]string{}

	if err := b.startPi(ctx); err != nil {
		return err
	}
	defer b.cleanup()

	childDone := make(chan error, 1)
	go func() {
		b.readRPCLoop()
		childDone <- b.cmd.Wait()
	}()

	// 1) Handshake via get_state to obtain sessionId, model, thinkingLevel, sessionName.
	if err := b.initSession(ctx); err != nil {
		return err
	}

	// 2) Report active agent & session metadata to Herdr + local pane index.
	b.reportAgent("idle", "")
	b.reportSession()
	b.printBanner()

	// 3) Run prompt loop + stdin loop until exit.
	promptDone := make(chan struct{})
	go func() {
		defer close(promptDone)
		b.promptLoop(ctx)
	}()

	inputDone := make(chan struct{})
	go func() {
		defer close(inputDone)
		b.inputLoop(ctx, cancel)
	}()

	select {
	case <-ctx.Done():
	case <-promptDone:
		cancel()
	case err := <-childDone:
		cancel()
		if err != nil && !errors.Is(ctx.Err(), context.Canceled) {
			return fmt.Errorf("pi rpc exited: %w", err)
		}
	}
	cancel()
	select {
	case <-promptDone:
	case <-time.After(2 * time.Second):
	}
	return nil
}

func (b *piRPCBridge) startPi(ctx context.Context) error {
	rpcArgs := piRPCArgs(b.cfg)
	cmd := exec.CommandContext(ctx, b.cfg.Bin, rpcArgs...)
	cmd.Dir = b.cfg.Cwd
	cmd.Env = piEnv(os.Environ())
	in, err := cmd.StdinPipe()
	if err != nil {
		return err
	}
	out, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	errPipe, err := cmd.StderrPipe()
	if err != nil {
		return err
	}
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start pi rpc: %w", err)
	}
	b.cmd, b.rpcIn, b.rpcOut, b.rpcErr = cmd, in, out, errPipe
	go b.copyStderr()
	return nil
}

func (b *piRPCBridge) copyStderr() {
	r := bufio.NewReaderSize(b.rpcErr, 64*1024)
	for {
		line, err := r.ReadString('\n')
		if s := strings.TrimSpace(stripANSI(line)); s != "" {
			b.stateMu.Lock()
			b.lastStderr = s
			b.stateMu.Unlock()
			b.printf("\x1b[33m%s\x1b[0m\n", s)
		}
		if err != nil {
			return
		}
	}
}

func (b *piRPCBridge) initSession(ctx context.Context) error {
	initCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	resp, err := b.callRPC(initCtx, "get_state", nil)
	if err != nil {
		b.stateMu.Lock()
		hint := b.lastStderr
		b.stateMu.Unlock()
		if b.cfg.Resume != "" && hint != "" {
			return fmt.Errorf("pi resume session %s failed (%s); start a fresh worker if cwd changed: %w", b.cfg.Resume, hint, err)
		}
		if hint != "" {
			return fmt.Errorf("pi get_state failed (%s): %w", hint, err)
		}
		return fmt.Errorf("pi get_state: %w", err)
	}

	var st struct {
		SessionID     string `json:"sessionId"`
		SessionFile   string `json:"sessionFile"`
		SessionName   string `json:"sessionName"`
		ThinkingLevel string `json:"thinkingLevel"`
		Model         *struct {
			ID       string `json:"id"`
			Name     string `json:"name"`
			Provider string `json:"provider"`
		} `json:"model"`
	}
	_ = json.Unmarshal(resp.Data, &st)

	sid := strings.TrimSpace(st.SessionID)
	if sid == "" {
		sid = strings.TrimSpace(b.cfg.Resume)
	}
	if sid == "" {
		return errors.New("pi get_state returned empty sessionId")
	}

	lp := filepath.Join(b.transcriptDir, sid+".jsonl")
	hadExisting := fileNonEmpty(lp)
	f, err := os.OpenFile(lp, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("open transcript %s: %w", lp, err)
	}

	modelLabel := ""
	if st.Model != nil {
		modelLabel = orStr(strings.TrimSpace(st.Model.Name), strings.TrimSpace(st.Model.ID))
	}
	effortLabel := strings.TrimSpace(st.ThinkingLevel)

	b.stateMu.Lock()
	b.sessionID = sid
	b.sessionTitle = strings.TrimSpace(st.SessionName)
	b.logPath = lp
	b.logFile = f
	b.modelLabel = modelLabel
	b.effortLabel = effortLabel
	b.stateMu.Unlock()

	// If resuming without a local JSONL transcript, backfill history via get_messages.
	if b.cfg.Resume != "" && !hadExisting {
		b.backfillHistory(ctx)
	}
	return nil
}

func (b *piRPCBridge) backfillHistory(ctx context.Context) {
	msgCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	resp, err := b.callRPC(msgCtx, "get_messages", nil)
	if err != nil {
		return
	}
	var data struct {
		Messages []json.RawMessage `json:"messages"`
	}
	if json.Unmarshal(resp.Data, &data) != nil {
		return
	}
	for i, raw := range data.Messages {
		var m struct {
			Role         string          `json:"role"`
			Content      json.RawMessage `json:"content"`
			ToolCallID   string          `json:"toolCallId"`
			ToolName     string          `json:"toolName"`
			IsError      bool            `json:"isError"`
			StopReason   string          `json:"stopReason"`
			ErrorMessage string          `json:"errorMessage"`
		}
		if json.Unmarshal(raw, &m) != nil {
			continue
		}
		switch m.Role {
		case "user":
			if text := extractPiMessageText(m.Content); text != "" {
				b.appendLog(acpLogEntry{Type: "user", ID: fmt.Sprintf("replay-u-%d", i), Text: text})
			}
		case "assistant":
			var blocks []struct {
				Type      string          `json:"type"`
				Text      string          `json:"text"`
				ID        string          `json:"id"`
				Name      string          `json:"name"`
				Arguments json.RawMessage `json:"arguments"`
			}
			if json.Unmarshal(m.Content, &blocks) == nil {
				for j, bl := range blocks {
					switch bl.Type {
					case "text":
						if bl.Text != "" {
							b.appendLog(acpLogEntry{Type: "assistant_chunk", ID: fmt.Sprintf("replay-a-%d", i), Text: bl.Text})
						}
					case "toolCall":
						b.appendLog(acpLogEntry{
							Type:   "tool",
							ID:     orStr(bl.ID, fmt.Sprintf("replay-tc-%d-%d", i, j)),
							Title:  strings.TrimSpace(bl.Name),
							Status: "completed",
							Input:  formatACPToolInput(bl.Arguments),
						})
					}
				}
			} else if text := extractPiMessageText(m.Content); text != "" {
				b.appendLog(acpLogEntry{Type: "assistant_chunk", ID: fmt.Sprintf("replay-a-%d", i), Text: text})
			}
			if m.StopReason == "error" && strings.TrimSpace(m.ErrorMessage) != "" {
				b.appendLog(acpLogEntry{Type: "error", Text: strings.TrimSpace(m.ErrorMessage)})
			}
		case "toolResult":
			out := extractPiMessageText(m.Content)
			status := "completed"
			if m.IsError {
				status = "failed"
			}
			b.appendLog(acpLogEntry{
				Type:   "tool",
				ID:     m.ToolCallID,
				Title:  strings.TrimSpace(m.ToolName),
				Status: status,
				Output: out,
				Error:  m.IsError,
			})
		}
	}
}

func (b *piRPCBridge) printBanner() {
	b.stateMu.Lock()
	sid, model, effort := b.sessionID, b.modelLabel, b.effortLabel
	b.stateMu.Unlock()
	if model == "" {
		model = orStr(b.cfg.Model, "default")
	}
	if effort == "" {
		effort = effectiveEffort("pi", b.cfg.Model, b.cfg.Effort)
	}
	mode := "default"
	if b.cfg.Bypass {
		mode = "approve"
	}
	shortID := sid
	if len(shortID) > 8 {
		shortID = shortID[:8]
	}
	action := "ready"
	if b.cfg.Resume != "" {
		action = "resumed"
	}
	if effort != "" {
		b.printf("\x1b[1mPi\x1b[0m \x1b[2m(%s · %s · %s · %s · %s)\x1b[0m\n❯ ", model, effort, mode, action, shortID)
	} else {
		b.printf("\x1b[1mPi\x1b[0m \x1b[2m(%s · %s · %s · %s)\x1b[0m\n❯ ", model, mode, action, shortID)
	}
}

func (b *piRPCBridge) cleanup() {
	b.stateMu.Lock()
	if b.endTimer != nil {
		b.endTimer.Stop()
		b.endTimer = nil
	}
	b.stateMu.Unlock()
	b.reportRelease()
	removeACPPaneMeta(b.transcriptDir, b.paneID, os.Getpid())
	b.stateMu.Lock()
	if b.logFile != nil {
		_ = b.logFile.Close()
		b.logFile = nil
	}
	b.stateMu.Unlock()
	if b.rpcIn != nil {
		_ = b.rpcIn.Close()
	}
	terminateChildProcess(b.cmd)
}

// ---------------------------------------------------------------- JSONL transport

func (b *piRPCBridge) sendJSON(v any) error {
	raw, err := json.Marshal(v)
	if err != nil {
		return err
	}
	b.writeMu.Lock()
	defer b.writeMu.Unlock()
	if b.rpcIn == nil {
		return io.ErrClosedPipe
	}
	_, err = b.rpcIn.Write(append(raw, '\n'))
	return err
}

func (b *piRPCBridge) callRPC(ctx context.Context, cmdType string, extra map[string]any) (piRPCResponse, error) {
	id := fmt.Sprintf("req-%d", b.nextID.Add(1))
	ch := make(chan piRPCResponse, 1)
	b.pendMu.Lock()
	b.pending[id] = ch
	b.pendMu.Unlock()
	defer func() {
		b.pendMu.Lock()
		delete(b.pending, id)
		b.pendMu.Unlock()
	}()

	payload := map[string]any{"id": id, "type": cmdType}
	for k, v := range extra {
		payload[k] = v
	}
	if err := b.sendJSON(payload); err != nil {
		return piRPCResponse{}, err
	}
	select {
	case <-ctx.Done():
		return piRPCResponse{}, ctx.Err()
	case resp, ok := <-ch:
		if !ok {
			return piRPCResponse{}, io.EOF
		}
		if !resp.Success {
			return resp, errors.New(orStr(strings.TrimSpace(resp.Error), "pi rpc command failed: "+cmdType))
		}
		return resp, nil
	}
}

// readRPCLoop reads strict LF-delimited JSONL records using bufio.Reader.ReadBytes('\n')
// so arbitrarily large payloads never hit bufio.Scanner's 64KB token limit.
func (b *piRPCBridge) readRPCLoop() {
	r := bufio.NewReaderSize(b.rpcOut, 256*1024)
	defer func() {
		b.pendMu.Lock()
		for id, ch := range b.pending {
			close(ch)
			delete(b.pending, id)
		}
		b.pendMu.Unlock()
		b.finishActiveTurn()
	}()
	for {
		line, err := r.ReadBytes('\n')
		if len(line) > 0 {
			line = bytes.TrimSuffix(line, []byte("\n"))
			line = bytes.TrimSuffix(line, []byte("\r"))
			if len(bytes.TrimSpace(line)) > 0 {
				b.dispatchLine(line)
			}
		}
		if err != nil {
			return
		}
	}
}

func (b *piRPCBridge) dispatchLine(raw []byte) {
	var head struct {
		Type string `json:"type"`
	}
	if json.Unmarshal(raw, &head) != nil || head.Type == "" {
		return
	}
	if head.Type == "response" {
		var resp piRPCResponse
		if json.Unmarshal(raw, &resp) == nil && resp.ID != "" {
			b.pendMu.Lock()
			ch := b.pending[resp.ID]
			b.pendMu.Unlock()
			if ch != nil {
				select {
				case ch <- resp:
				default:
				}
			}
		}
		return
	}
	if head.Type == "extension_ui_request" {
		cp := append([]byte(nil), raw...)
		go b.handleExtUIRequest(cp)
		return
	}
	b.handleEvent(head.Type, raw)
}

// ---------------------------------------------------------------- event handling

func (b *piRPCBridge) cancelEndTimerLocked() {
	if b.endTimer != nil {
		b.endTimer.Stop()
		b.endTimer = nil
	}
}

func (b *piRPCBridge) finishTurnLocked(expectedSeq int64) {
	if expectedSeq != 0 && b.turnSeq != expectedSeq {
		return
	}
	b.cancelEndTimerLocked()
	if b.turnDone != nil {
		close(b.turnDone)
		b.turnDone = nil
	}
}

func (b *piRPCBridge) finishActiveTurn() {
	b.stateMu.Lock()
	b.finishTurnLocked(0)
	b.stateMu.Unlock()
}

func (b *piRPCBridge) handleEvent(evType string, raw []byte) {
	switch evType {
	case "agent_start", "turn_start", "compaction_start", "summarization_retry_scheduled":
		b.stateMu.Lock()
		b.cancelEndTimerLocked()
		b.stateMu.Unlock()

	case "message_start":
		var ev struct {
			Message struct {
				Role string `json:"role"`
			} `json:"message"`
		}
		if json.Unmarshal(raw, &ev) == nil && ev.Message.Role == "assistant" {
			b.stateMu.Lock()
			b.cancelEndTimerLocked()
			b.msgSeq++
			b.curMsgID = fmt.Sprintf("m-%d", b.msgSeq)
			b.stateMu.Unlock()
		}

	case "message_update":
		var ev struct {
			AssistantMessageEvent struct {
				Type  string `json:"type"`
				Delta string `json:"delta"`
			} `json:"assistantMessageEvent"`
		}
		if json.Unmarshal(raw, &ev) != nil {
			return
		}
		if ev.AssistantMessageEvent.Type == "text_delta" && ev.AssistantMessageEvent.Delta != "" {
			b.stateMu.Lock()
			b.cancelEndTimerLocked()
			if b.curMsgID == "" {
				b.msgSeq++
				b.curMsgID = fmt.Sprintf("m-%d", b.msgSeq)
			}
			msgID := b.curMsgID
			b.streamedText = true
			b.stateMu.Unlock()
			b.appendLog(acpLogEntry{Type: "assistant_chunk", ID: msgID, Text: ev.AssistantMessageEvent.Delta})
			b.printRaw(ev.AssistantMessageEvent.Delta)
		}

	case "message_end":
		var ev struct {
			Message struct {
				Role         string `json:"role"`
				StopReason   string `json:"stopReason"`
				ErrorMessage string `json:"errorMessage"`
			} `json:"message"`
		}
		if json.Unmarshal(raw, &ev) != nil {
			return
		}
		if ev.Message.Role == "assistant" {
			b.stateMu.Lock()
			hadStream := b.streamedText
			b.streamedText = false
			b.msgSeq++
			b.curMsgID = fmt.Sprintf("m-%d", b.msgSeq)
			b.stateMu.Unlock()
			if errMsg := strings.TrimSpace(ev.Message.ErrorMessage); ev.Message.StopReason == "error" && errMsg != "" {
				if hadStream {
					b.printRaw("\n")
				}
				b.appendLog(acpLogEntry{Type: "error", Text: errMsg})
				b.printf("\x1b[31m⚠ Error: %s\x1b[0m\n", errMsg)
			}
		}

	case "tool_execution_start":
		var ev struct {
			ToolCallID string          `json:"toolCallId"`
			ToolName   string          `json:"toolName"`
			Args       json.RawMessage `json:"args"`
		}
		if json.Unmarshal(raw, &ev) != nil {
			return
		}
		title := orStr(strings.TrimSpace(ev.ToolName), "Tool")
		input := formatACPToolInput(ev.Args)
		b.stateMu.Lock()
		b.cancelEndTimerLocked()
		hadStream := b.streamedText
		b.streamedText = false
		if ev.ToolCallID != "" {
			b.toolNames[ev.ToolCallID] = title
		}
		b.stateMu.Unlock()
		if hadStream {
			b.printRaw("\n")
		}
		b.appendLog(acpLogEntry{
			Type:   "tool",
			ID:     ev.ToolCallID,
			Title:  title,
			Status: "in_progress",
			Input:  input,
		})
		b.printf("\x1b[2m● %s\x1b[0m\n", title)
		b.reportAgent("working", title)

	case "tool_execution_end":
		var ev struct {
			ToolCallID string `json:"toolCallId"`
			ToolName   string `json:"toolName"`
			Result     struct {
				Content json.RawMessage `json:"content"`
			} `json:"result"`
			IsError bool `json:"isError"`
		}
		if json.Unmarshal(raw, &ev) != nil {
			return
		}
		b.stateMu.Lock()
		prevName := b.toolNames[ev.ToolCallID]
		b.stateMu.Unlock()
		title := orStr(strings.TrimSpace(ev.ToolName), orStr(prevName, "Tool"))
		output := extractPiMessageText(ev.Result.Content)
		status := "completed"
		if ev.IsError {
			status = "failed"
		}
		b.appendLog(acpLogEntry{
			Type:   "tool",
			ID:     ev.ToolCallID,
			Title:  title,
			Status: status,
			Output: output,
			Error:  ev.IsError,
		})
		if ev.IsError {
			b.printf("\x1b[31m  ✗ %s\x1b[0m\n", title)
		} else {
			b.printf("\x1b[2m  ✓ %s\x1b[0m\n", title)
		}

	case "auto_retry_start":
		var ev struct {
			Attempt      int    `json:"attempt"`
			MaxAttempts  int    `json:"maxAttempts"`
			ErrorMessage string `json:"errorMessage"`
		}
		if json.Unmarshal(raw, &ev) == nil {
			b.stateMu.Lock()
			b.cancelEndTimerLocked()
			b.stateMu.Unlock()
			b.printf("\x1b[2m[retrying %d/%d: %s]\x1b[0m\n", ev.Attempt, ev.MaxAttempts, clip(oneLine(ev.ErrorMessage), 100))
		}

	case "auto_retry_end":
		var ev struct {
			Success    bool   `json:"success"`
			FinalError string `json:"finalError"`
		}
		if json.Unmarshal(raw, &ev) == nil && !ev.Success {
			if msg := strings.TrimSpace(ev.FinalError); msg != "" {
				b.appendLog(acpLogEntry{Type: "error", Text: msg})
				b.printf("\x1b[31m⚠ Error: %s\x1b[0m\n", msg)
			}
		}

	case "extension_error":
		var ev struct {
			Event string `json:"event"`
			Error string `json:"error"`
		}
		if json.Unmarshal(raw, &ev) == nil && strings.TrimSpace(ev.Error) != "" {
			b.printf("\x1b[33m[extension %s error: %s]\x1b[0m\n", orStr(ev.Event, "hook"), strings.TrimSpace(ev.Error))
		}

	case "agent_end":
		var ev struct {
			WillRetry bool `json:"willRetry"`
		}
		_ = json.Unmarshal(raw, &ev)
		b.stateMu.Lock()
		b.cancelEndTimerLocked()
		if !ev.WillRetry && b.turnDone != nil {
			seq := b.turnSeq
			delay := b.endGraceDelay
			b.endTimer = time.AfterFunc(delay, func() {
				b.stateMu.Lock()
				b.finishTurnLocked(seq)
				b.stateMu.Unlock()
			})
		}
		b.stateMu.Unlock()

	case "agent_settled":
		b.stateMu.Lock()
		b.finishTurnLocked(b.turnSeq)
		b.stateMu.Unlock()
	}
}

func extractPiMessageText(raw json.RawMessage) string {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		return ""
	}
	var s string
	if json.Unmarshal(trimmed, &s) == nil {
		return s
	}
	var blocks []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if json.Unmarshal(trimmed, &blocks) == nil {
		var parts []string
		for _, bl := range blocks {
			if bl.Type == "text" && bl.Text != "" {
				parts = append(parts, bl.Text)
			}
		}
		return strings.Join(parts, "\n")
	}
	return ""
}

// ---------------------------------------------------------------- extension UI requests

func (b *piRPCBridge) handleExtUIRequest(raw []byte) {
	var req struct {
		ID         string   `json:"id"`
		Method     string   `json:"method"`
		Title      string   `json:"title"`
		Message    string   `json:"message"`
		Options    []string `json:"options"`
		NotifyType string   `json:"notifyType"`
	}
	if json.Unmarshal(raw, &req) != nil || req.Method == "" {
		return
	}

	switch req.Method {
	case "setTitle":
		if title := strings.TrimSpace(req.Title); title != "" {
			b.stateMu.Lock()
			changed := title != b.sessionTitle
			b.sessionTitle = title
			b.stateMu.Unlock()
			if changed {
				b.writePaneMeta()
			}
		}
	case "notify":
		if msg := strings.TrimSpace(req.Message); msg != "" {
			switch req.NotifyType {
			case "error":
				b.printf("\x1b[31m[pi] %s\x1b[0m\n", msg)
			case "warning":
				b.printf("\x1b[33m[pi] %s\x1b[0m\n", msg)
			default:
				b.printf("\x1b[2m[pi] %s\x1b[0m\n", msg)
			}
		}
	case "setStatus", "setWidget", "set_editor_text":
		// Fire-and-forget TUI-only methods: consume silently.
	case "input", "editor":
		// Headless bridge cannot open a free-form editor; cancel immediately so
		// extensions never deadlock waiting for stdin.
		_ = b.sendJSON(map[string]any{
			"type":      "extension_ui_response",
			"id":        req.ID,
			"cancelled": true,
		})
	case "confirm", "select":
		opts := req.Options
		if req.Method == "confirm" {
			opts = []string{"Yes", "No"}
		}
		if len(opts) == 0 {
			_ = b.sendJSON(map[string]any{
				"type":      "extension_ui_response",
				"id":        req.ID,
				"cancelled": true,
			})
			return
		}
		b.handleExtDialog(req.ID, req.Method, orStr(strings.TrimSpace(req.Title), "Confirm"), strings.TrimSpace(req.Message), opts)
	}
}

func (b *piRPCBridge) handleExtDialog(id, method, title, message string, options []string) {
	for len(b.permReply) > 0 {
		<-b.permReply
	}

	b.stateMu.Lock()
	hadStream := b.streamedText
	b.streamedText = false
	b.stateMu.Unlock()
	if hadStream {
		b.printRaw("\n")
	}

	header := "Pi 提问：" + title
	if method == "confirm" {
		header = "Pi 请求确认：" + title
	}
	menuText, menuLines := renderBridgeMenu(header, message, options)

	dlg := &piExtDialog{
		ID:      id,
		Method:  method,
		Title:   title,
		Message: message,
		Options: options,
		Lines:   menuLines,
	}
	b.stateMu.Lock()
	b.activeDialog = dlg
	b.stateMu.Unlock()

	b.printRaw(menuText)
	b.reportAgent("blocked", "Prompt: "+title)

	var choice string
	select {
	case choice = <-b.permReply:
	case <-b.done:
		b.stateMu.Lock()
		b.activeDialog = nil
		b.stateMu.Unlock()
		return
	}

	b.stateMu.Lock()
	b.activeDialog = nil
	b.stateMu.Unlock()

	if seq := eraseBridgeMenuSeq(b.tty, menuLines); seq != "" {
		b.printRaw(seq)
	}

	if choice == "esc" {
		b.printf("\x1b[33m[✗ 已取消: %s]\x1b[0m\n", title)
		b.reportAgent("working", "Cancelled")
		_ = b.sendJSON(map[string]any{
			"type":      "extension_ui_response",
			"id":        id,
			"cancelled": true,
		})
		return
	}

	if method == "confirm" {
		confirmed := false
		switch strings.ToLower(choice) {
		case "1", "y":
			confirmed = true
		case "2", "n":
			confirmed = false
		default:
			confirmed = false
		}
		label := "No"
		if confirmed {
			label = "Yes"
		}
		b.printf("\x1b[32m[✓ 已选择: %s · %s]\x1b[0m\n", label, title)
		b.reportAgent("working", "Continuing…")
		_ = b.sendJSON(map[string]any{
			"type":      "extension_ui_response",
			"id":        id,
			"confirmed": confirmed,
		})
		return
	}

	picked := options[0]
	if idx, err := strconv.Atoi(choice); err == nil && idx >= 1 && idx <= len(options) {
		picked = options[idx-1]
	}
	b.printf("\x1b[32m[✓ 已选择: %s · %s]\x1b[0m\n", picked, title)
	b.reportAgent("working", "Continuing…")
	_ = b.sendJSON(map[string]any{
		"type":  "extension_ui_response",
		"id":    id,
		"value": picked,
	})
}

// ---------------------------------------------------------------- prompt & input loops

func (b *piRPCBridge) promptLoop(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case text, ok := <-b.promptQueue:
			if !ok {
				return
			}
			b.executePrompt(ctx, text)
		}
	}
}

func (b *piRPCBridge) executePrompt(ctx context.Context, text string) {
	text = strings.TrimSpace(text)
	if text == "" {
		return
	}

	turnDone := make(chan struct{})
	b.stateMu.Lock()
	b.cancelEndTimerLocked()
	b.turnSeq++
	seq := b.turnSeq
	b.turnDone = turnDone
	b.working = true
	b.streamedText = false
	b.msgSeq++
	b.curMsgID = fmt.Sprintf("m-%d", b.msgSeq)
	b.stateMu.Unlock()

	b.appendLog(acpLogEntry{Type: "user", Text: text})
	b.printf("\n\x1b[1m> %s\x1b[0m\n", clip(oneLine(text), 120))
	b.reportAgent("working", "Thinking…")

	_, err := b.callRPC(ctx, "prompt", map[string]any{"message": text})
	if err != nil {
		b.stateMu.Lock()
		b.finishTurnLocked(seq)
		b.streamedText = false
		b.working = false
		b.stateMu.Unlock()
		if ctx.Err() == nil {
			msg := err.Error()
			b.appendLog(acpLogEntry{Type: "error", Text: msg})
			b.printf("\x1b[31m⚠ Error: %s\x1b[0m\n", msg)
		}
		b.printRaw("\n❯ ")
		b.reportAgent("idle", "")
		return
	}

	select {
	case <-turnDone:
	case <-ctx.Done():
	}

	b.stateMu.Lock()
	b.cancelEndTimerLocked()
	if b.turnSeq == seq {
		b.turnDone = nil
	}
	hadStream := b.streamedText
	b.streamedText = false
	b.working = false
	b.stateMu.Unlock()

	if hadStream {
		b.printRaw("\n")
	}
	b.printRaw("\n❯ ")
	b.reportAgent("idle", "")
}

func (b *piRPCBridge) interruptTurn(ctx context.Context) {
	ic, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	_, _ = b.callRPC(ic, "clear_queue", nil)
	_, _ = b.callRPC(ic, "abort", nil)
}

func (b *piRPCBridge) inputLoop(ctx context.Context, cancel context.CancelFunc) {
	evCh := make(chan bridgeInputEvent, 32)
	go readBridgeInput(b.stdin, func() bool {
		b.stateMu.Lock()
		defer b.stateMu.Unlock()
		return b.activeDialog != nil
	}, evCh)

	for {
		select {
		case <-ctx.Done():
			return
		case ev, ok := <-evCh:
			if !ok {
				return
			}
			switch ev.kind {
			case "prompt":
				select {
				case b.promptQueue <- ev.text:
				case <-ctx.Done():
					return
				}
			case "perm":
				select {
				case b.permReply <- ev.text:
				default:
				}
			case "esc":
				b.stateMu.Lock()
				hasDlg := b.activeDialog != nil
				working := b.working
				b.stateMu.Unlock()
				if hasDlg {
					select {
					case b.permReply <- "esc":
					default:
					}
				} else if working {
					go b.interruptTurn(ctx)
				}
			case "ctrl_c":
				b.stateMu.Lock()
				hasDlg := b.activeDialog != nil
				working := b.working
				prev := b.lastCtrlC
				b.lastCtrlC = time.Now()
				b.stateMu.Unlock()
				if hasDlg {
					select {
					case b.permReply <- "esc":
					default:
					}
				} else if working && time.Since(prev) > 2*time.Second {
					b.printf("\n\x1b[33m[Interrupting… press Ctrl-C again to exit]\x1b[0m\n")
					go b.interruptTurn(ctx)
				} else {
					cancel()
					return
				}
			case "eof":
				if !b.tty {
					close(b.promptQueue)
					return
				}
				cancel()
				return
			}
		}
	}
}

// ---------------------------------------------------------------- transcript, terminal & Herdr helpers

func (b *piRPCBridge) appendLog(e acpLogEntry) {
	raw, err := marshalBridgeLogEntry(e)
	if err != nil {
		return
	}
	b.stateMu.Lock()
	defer b.stateMu.Unlock()
	if b.logFile == nil {
		return
	}
	_, _ = b.logFile.Write(raw)
}

func (b *piRPCBridge) printRaw(s string) {
	b.outMu.Lock()
	defer b.outMu.Unlock()
	_, _ = io.WriteString(b.stdout, s)
}

func (b *piRPCBridge) printf(format string, args ...any) {
	b.printRaw(fmt.Sprintf(format, args...))
}

func (b *piRPCBridge) resumeArgv() []string {
	b.stateMu.Lock()
	sid := b.sessionID
	b.stateMu.Unlock()
	return buildBridgeResumeArgv("pi", "--approve", true, b.cfg, sid)
}

func (b *piRPCBridge) writePaneMeta() {
	if b.paneID == "" {
		return
	}
	b.stateMu.Lock()
	sid, title, lp, model, effort := b.sessionID, b.sessionTitle, b.logPath, b.modelLabel, b.effortLabel
	b.stateMu.Unlock()
	if sid == "" {
		return
	}
	_ = writeACPPaneMeta(b.transcriptDir, acpPaneMeta{
		PaneID:    b.paneID,
		SessionID: sid,
		Path:      lp,
		Cwd:       b.cfg.Cwd,
		Title:     title,
		Model:     model,
		Effort:    effort,
		PID:       os.Getpid(),
	})
}

func (b *piRPCBridge) reportAgent(state, msg string) {
	b.stateMu.Lock()
	sid, lp := b.sessionID, b.logPath
	b.stateMu.Unlock()
	var argv []string
	if sid != "" {
		argv = b.resumeArgv()
	}
	reportBridgeAgent(b.herdrSock, b.paneID, piBridgeSource, "pi", state, msg, sid, lp, argv, &b.herdrSeq)
}

func (b *piRPCBridge) reportSession() {
	b.stateMu.Lock()
	sid, lp := b.sessionID, b.logPath
	b.stateMu.Unlock()
	if sid == "" {
		return
	}
	b.writePaneMeta()
	reportBridgeSession(b.herdrSock, b.paneID, piBridgeSource, "pi", sid, lp, b.resumeArgv(), &b.herdrSeq)
}

func (b *piRPCBridge) reportRelease() {
	reportBridgeRelease(b.herdrSock, b.paneID, piBridgeSource, "pi", &b.herdrSeq)
}
