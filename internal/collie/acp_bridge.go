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
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// ACP v1 JSON-RPC 2.0 bridge runtime used by `collie _bridge <doubao|cursor|omp> [flags]`.
// Shared PTY/paste input, pane metadata, JSONL transcript helpers, Herdr socket
// reporting, and numbered approval menus live in bridge_common.go.

type acpProvider interface {
	Kind() string
	DisplayName() string
	Source() string
	LogPrefix() string
	BinName() string
	BinFlag() string
	BypassFlag() string
	SupportsEffort() bool
	BuildACPArgs(cfg acpBridgeConfig) []string
	BuildEnv(base []string) []string
	FilterStderrLine(line string) (string, bool)
	PlaceholderToolTitle() string
	DefaultBannerModel(cfg acpBridgeConfig) string
	DefaultBannerEffort(cfg acpBridgeConfig) string
	FormatSessionOpenError(op string, err error) error
}

func acpProviderFor(kind string) acpProvider {
	switch kind {
	case "doubao":
		return doubaoProvider{}
	case "cursor":
		return cursorProvider{}
	case "omp", "oh-my-pi":
		return ompProvider{}
	}
	return nil
}

// ---------------------------------------------------------------- JSON-RPC wire types

type rpcFrame struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method,omitempty"`
	Params  json.RawMessage `json:"params,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

type rpcError struct {
	Code    int             `json:"code"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data,omitempty"`
}

func (e *rpcError) Error() string {
	if e == nil {
		return ""
	}
	if len(e.Data) > 0 && string(e.Data) != "null" {
		return fmt.Sprintf("rpc error %d: %s (%s)", e.Code, e.Message, string(e.Data))
	}
	return fmt.Sprintf("rpc error %d: %s", e.Code, e.Message)
}

type acpPermOption struct {
	OptionID string `json:"optionId"`
	Name     string `json:"name"`
	Kind     string `json:"kind"`
}

type acpPermRequest struct {
	ID         json.RawMessage
	Title      string
	Kind       string
	Command    string
	IsQuestion bool
	Options    []acpPermOption
	Lines      int // number of terminal lines printed for the prompt menu
}

// ---------------------------------------------------------------- bridge runtime

type acpBridge struct {
	provider      acpProvider
	cfg           acpBridgeConfig
	herdrSock     string
	paneID        string
	transcriptDir string
	stdin         io.Reader
	stdout        io.Writer
	stderr        io.Writer
	tty           bool

	cmd      *exec.Cmd
	acpIn    io.WriteCloser
	acpOut   io.ReadCloser
	acpErr   io.ReadCloser
	writeMu  sync.Mutex
	outMu    sync.Mutex
	nextID   atomic.Int64
	herdrSeq atomic.Int64

	pendMu  sync.Mutex
	pending map[int64]chan rpcFrame

	stateMu       sync.Mutex
	sessionID     string
	sessionTitle  string
	logPath       string
	logFile       *os.File
	modelLabel    string
	effortLabel   string
	replaying     bool
	skipReplayLog bool
	working       bool
	streamedText  bool
	activePerm    *acpPermRequest
	toolTitles    map[string]string
	lastCtrlC     time.Time

	promptQueue chan string
	permReply   chan string
	done        chan struct{}
}

func (b *acpBridge) run(ctx context.Context) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	if b.provider == nil {
		return errors.New("acp bridge missing provider")
	}
	if b.pending == nil {
		b.pending = map[int64]chan rpcFrame{}
	}
	if b.promptQueue == nil {
		b.promptQueue = make(chan string, 64)
	}
	if b.permReply == nil {
		b.permReply = make(chan string, 4)
	}
	b.done = make(chan struct{})
	defer close(b.done)

	if err := os.MkdirAll(b.transcriptDir, 0o700); err != nil {
		return fmt.Errorf("create transcript dir: %w", err)
	}
	b.toolTitles = map[string]string{}

	if err := b.startACP(ctx); err != nil {
		return err
	}
	defer b.cleanup()

	childDone := make(chan error, 1)
	go func() {
		b.readACPLoop()
		childDone <- b.cmd.Wait()
	}()

	// 1) ACP initialize (must include non-empty clientInfo.name).
	// Intentionally skip `authenticate` and go straight to session/new|load:
	// pre-authenticated CLIs are already ready, while calling `authenticate`
	// when logged out can spawn a blocking browser OAuth flow.
	initCtx, initCancel := context.WithTimeout(ctx, 30*time.Second)
	_, err := b.callACP(initCtx, "initialize", map[string]any{
		"protocolVersion":    1,
		"clientCapabilities": map[string]any{},
		"clientInfo": map[string]any{
			"name":    "collie",
			"version": version,
		},
	})
	initCancel()
	if err != nil {
		return fmt.Errorf("acp initialize: %w", err)
	}

	// 2) session/new or session/load
	if err := b.openSession(ctx); err != nil {
		return err
	}

	// 3) Report active agent first, then session metadata, to Herdr + local pane index
	b.reportAgent("idle", "")
	b.reportSession()
	b.printBanner()

	// 4) Run prompt worker + stdin loop until exit
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
			return fmt.Errorf("%s acp exited: %w", b.provider.Kind(), err)
		}
	}
	cancel()
	select {
	case <-promptDone:
	case <-time.After(2 * time.Second):
	}
	return nil
}

func (b *acpBridge) startACP(ctx context.Context) error {
	acpArgs := b.provider.BuildACPArgs(b.cfg)
	cmd := exec.CommandContext(ctx, b.cfg.Bin, acpArgs...)
	cmd.Dir = b.cfg.Cwd
	cmd.Env = b.provider.BuildEnv(os.Environ())
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
		return fmt.Errorf("start %s acp: %w", b.provider.Kind(), err)
	}
	b.cmd, b.acpIn, b.acpOut, b.acpErr = cmd, in, out, errPipe
	go b.copyStderr()
	return nil
}

func (b *acpBridge) copyStderr() {
	r := bufio.NewReaderSize(b.acpErr, 64*1024)
	for {
		line, err := r.ReadString('\n')
		if s := strings.TrimSpace(line); s != "" {
			if cleaned, ok := b.provider.FilterStderrLine(s); ok && cleaned != "" {
				b.printf("\x1b[33m%s\x1b[0m\n", cleaned)
			}
		}
		if err != nil {
			return
		}
	}
}

func (b *acpBridge) openSession(ctx context.Context) error {
	sessCtx, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()

	var raw json.RawMessage
	var err error
	if b.cfg.Resume != "" {
		sid := b.cfg.Resume
		lp := filepath.Join(b.transcriptDir, sid+".jsonl")
		hadExisting := fileNonEmpty(lp)
		f, ferr := os.OpenFile(lp, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
		if ferr != nil {
			return fmt.Errorf("open transcript %s: %w", lp, ferr)
		}
		b.stateMu.Lock()
		b.sessionID = sid
		b.logPath = lp
		b.logFile = f
		b.replaying = true
		b.skipReplayLog = hadExisting
		b.stateMu.Unlock()

		raw, err = b.callACP(sessCtx, "session/load", map[string]any{
			"sessionId":  sid,
			"cwd":        b.cfg.Cwd,
			"mcpServers": []any{},
		})
		b.stateMu.Lock()
		b.replaying = false
		b.skipReplayLog = false
		b.stateMu.Unlock()
		if err != nil {
			return b.provider.FormatSessionOpenError("session/load "+sid, err)
		}
	} else {
		raw, err = b.callACP(sessCtx, "session/new", map[string]any{
			"cwd":        b.cfg.Cwd,
			"mcpServers": []any{},
		})
		if err != nil {
			return b.provider.FormatSessionOpenError("session/new", err)
		}
	}

	var res struct {
		SessionID string `json:"sessionId"`
		Models    *struct {
			CurrentModelID  string `json:"currentModelId"`
			AvailableModels []struct {
				ModelID string `json:"modelId"`
				Name    string `json:"name"`
			} `json:"availableModels"`
		} `json:"models"`
		ConfigOptions []struct {
			ID           string `json:"id"`
			Category     string `json:"category"`
			CurrentValue string `json:"currentValue"`
			Options      []struct {
				Value string `json:"value"`
				Name  string `json:"name"`
			} `json:"options"`
		} `json:"configOptions"`
	}
	_ = json.Unmarshal(raw, &res)

	b.stateMu.Lock()
	if b.sessionID == "" {
		b.sessionID = res.SessionID
	}
	if b.sessionID == "" {
		b.stateMu.Unlock()
		return errors.New("acp session/new returned empty sessionId")
	}
	if b.logFile == nil {
		lp := filepath.Join(b.transcriptDir, b.sessionID+".jsonl")
		f, ferr := os.OpenFile(lp, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
		if ferr != nil {
			b.stateMu.Unlock()
			return fmt.Errorf("open transcript %s: %w", lp, ferr)
		}
		b.logPath = lp
		b.logFile = f
	}
	// Resolve human-friendly model & effort labels for the banner.
	if res.Models != nil {
		b.modelLabel = res.Models.CurrentModelID
		for _, m := range res.Models.AvailableModels {
			if m.ModelID == res.Models.CurrentModelID && m.Name != "" {
				b.modelLabel = m.Name
				break
			}
		}
	}
	for _, opt := range res.ConfigOptions {
		if b.provider.SupportsEffort() && (opt.ID == "effort" || opt.ID == "thinking" || opt.Category == "thought_level") && opt.CurrentValue != "" {
			b.effortLabel = opt.CurrentValue
		}
		if (opt.ID == "model" || opt.Category == "model") && b.modelLabel == "" && opt.CurrentValue != "" {
			b.modelLabel = opt.CurrentValue
			for _, o := range opt.Options {
				if o.Value == opt.CurrentValue && o.Name != "" {
					b.modelLabel = o.Name
					break
				}
			}
		}
	}
	b.stateMu.Unlock()
	return nil
}

func (b *acpBridge) printBanner() {
	b.stateMu.Lock()
	sid, model, effort := b.sessionID, b.modelLabel, b.effortLabel
	b.stateMu.Unlock()
	if model == "" {
		model = b.provider.DefaultBannerModel(b.cfg)
	}
	if b.provider.SupportsEffort() {
		if effort == "" {
			effort = b.provider.DefaultBannerEffort(b.cfg)
		}
	} else {
		effort = ""
	}
	mode := "default"
	if b.cfg.Bypass {
		mode = "bypass"
	} else if b.cfg.PermissionMode != "" {
		mode = b.cfg.PermissionMode
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
		b.printf("\x1b[1m%s\x1b[0m \x1b[2m(%s · %s · %s · %s · %s)\x1b[0m\n❯ ", b.provider.DisplayName(), model, effort, mode, action, shortID)
	} else {
		b.printf("\x1b[1m%s\x1b[0m \x1b[2m(%s · %s · %s · %s)\x1b[0m\n❯ ", b.provider.DisplayName(), model, mode, action, shortID)
	}
}

func (b *acpBridge) cleanup() {
	b.reportRelease()
	removeACPPaneMeta(b.transcriptDir, b.paneID, os.Getpid())
	b.stateMu.Lock()
	if b.logFile != nil {
		_ = b.logFile.Close()
		b.logFile = nil
	}
	b.stateMu.Unlock()
	if b.acpIn != nil {
		_ = b.acpIn.Close()
	}
	terminateChildProcess(b.cmd)
}

// ---------------------------------------------------------------- ACP JSON-RPC transport

func (b *acpBridge) sendJSON(v any) error {
	raw, err := json.Marshal(v)
	if err != nil {
		return err
	}
	b.writeMu.Lock()
	defer b.writeMu.Unlock()
	if b.acpIn == nil {
		return io.ErrClosedPipe
	}
	_, err = b.acpIn.Write(append(raw, '\n'))
	return err
}

func (b *acpBridge) callACP(ctx context.Context, method string, params any) (json.RawMessage, error) {
	id := b.nextID.Add(1)
	ch := make(chan rpcFrame, 1)
	b.pendMu.Lock()
	b.pending[id] = ch
	b.pendMu.Unlock()
	defer func() {
		b.pendMu.Lock()
		delete(b.pending, id)
		b.pendMu.Unlock()
	}()
	if err := b.sendJSON(map[string]any{
		"jsonrpc": "2.0",
		"id":      id,
		"method":  method,
		"params":  params,
	}); err != nil {
		return nil, err
	}
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case resp, ok := <-ch:
		if !ok {
			return nil, io.EOF
		}
		if resp.Error != nil {
			return nil, resp.Error
		}
		return resp.Result, nil
	}
}

func (b *acpBridge) notifyACP(method string, params any) {
	_ = b.sendJSON(map[string]any{
		"jsonrpc": "2.0",
		"method":  method,
		"params":  params,
	})
}

// readACPLoop reads newline-delimited JSON-RPC frames using bufio.Reader.ReadBytes('\n')
// so large frames (such as ~60KB+ available_commands_update) never hit bufio.Scanner's
// 64KB token limit.
func (b *acpBridge) readACPLoop() {
	r := bufio.NewReaderSize(b.acpOut, 256*1024)
	defer func() {
		b.pendMu.Lock()
		for id, ch := range b.pending {
			close(ch)
			delete(b.pending, id)
		}
		b.pendMu.Unlock()
	}()
	for {
		line, err := r.ReadBytes('\n')
		if len(line) > 0 {
			trimmed := bytes.TrimSpace(line)
			if len(trimmed) > 0 {
				var frame rpcFrame
				if json.Unmarshal(trimmed, &frame) == nil {
					b.dispatchACP(frame)
				}
			}
		}
		if err != nil {
			return
		}
	}
}

func (b *acpBridge) dispatchACP(f rpcFrame) {
	hasID := len(f.ID) > 0 && string(f.ID) != "null"
	if f.Method != "" && hasID {
		go b.handleServerRequest(f)
		return
	}
	if f.Method != "" {
		b.handleNotification(f)
		return
	}
	if hasID {
		var id int64
		if json.Unmarshal(f.ID, &id) == nil {
			b.pendMu.Lock()
			ch := b.pending[id]
			b.pendMu.Unlock()
			if ch != nil {
				select {
				case ch <- f:
				default:
				}
			}
		}
	}
}

// ---------------------------------------------------------------- ACP notifications & permissions

func (b *acpBridge) handleNotification(f rpcFrame) {
	if f.Method != "session/update" {
		return
	}
	var params struct {
		SessionID string          `json:"sessionId"`
		Update    json.RawMessage `json:"update"`
	}
	if json.Unmarshal(f.Params, &params) != nil || len(params.Update) == 0 {
		return
	}
	var head struct {
		SessionUpdate string `json:"sessionUpdate"`
	}
	if json.Unmarshal(params.Update, &head) != nil {
		return
	}
	// Never persist available_commands_update (~60KB) into transcripts.
	if head.SessionUpdate == "available_commands_update" {
		return
	}

	b.stateMu.Lock()
	replaying := b.replaying
	skipLog := b.skipReplayLog
	b.stateMu.Unlock()

	placeholder := b.provider.PlaceholderToolTitle()

	switch head.SessionUpdate {
	case "user_message_chunk":
		var u struct {
			MessageID string `json:"messageId"`
			Content   struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"content"`
		}
		if json.Unmarshal(params.Update, &u) == nil && u.Content.Text != "" {
			if !skipLog {
				b.appendLog(acpLogEntry{Type: "user", ID: u.MessageID, Text: u.Content.Text})
			}
		}

	case "agent_message_chunk":
		var u struct {
			MessageID string `json:"messageId"`
			Content   struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"content"`
		}
		if json.Unmarshal(params.Update, &u) == nil && u.Content.Text != "" {
			if !skipLog {
				b.appendLog(acpLogEntry{Type: "assistant_chunk", ID: u.MessageID, Text: u.Content.Text})
			}
			if !replaying {
				b.stateMu.Lock()
				b.streamedText = true
				b.stateMu.Unlock()
				b.printRaw(u.Content.Text)
			}
		}

	case "agent_thought_chunk":
		// Thought deltas are intentionally not persisted into assistant replies.

	case "session_info_update":
		var u struct {
			Title string `json:"title"`
		}
		if json.Unmarshal(params.Update, &u) == nil {
			if title := strings.TrimSpace(u.Title); title != "" {
				b.stateMu.Lock()
				changed := title != b.sessionTitle
				b.sessionTitle = title
				b.stateMu.Unlock()
				if changed {
					b.writePaneMeta()
				}
			}
		}

	case "tool_call", "tool_call_update":
		var u struct {
			ToolCallID string `json:"toolCallId"`
			Title      string `json:"title"`
			Kind       string `json:"kind"`
			Status     string `json:"status"`
			Locations  []struct {
				Path string `json:"path"`
			} `json:"locations"`
			RawInput  json.RawMessage `json:"rawInput"`
			RawOutput json.RawMessage `json:"rawOutput"`
			Content   json.RawMessage `json:"content"`
		}
		if json.Unmarshal(params.Update, &u) != nil {
			return
		}
		var locs []string
		for _, l := range u.Locations {
			if p := strings.TrimSpace(l.Path); p != "" {
				locs = append(locs, p)
			}
		}
		input := formatACPToolInput(u.RawInput)
		output := extractACPToolOutput(u.RawOutput, u.Content)
		isErr := strings.EqualFold(u.Status, "failed") || strings.EqualFold(u.Status, "error")

		if !skipLog {
			b.appendLog(acpLogEntry{
				Type:      "tool",
				ID:        u.ToolCallID,
				Title:     strings.TrimSpace(u.Title),
				Kind:      strings.TrimSpace(u.Kind),
				Status:    strings.TrimSpace(u.Status),
				Locations: locs,
				Input:     input,
				Output:    output,
				Error:     isErr,
			})
		}

		if !replaying {
			title := strings.TrimSpace(u.Title)
			isPlaceholder := placeholder != "" && title == placeholder
			b.stateMu.Lock()
			prevTitle := b.toolTitles[u.ToolCallID]
			hadStream := b.streamedText
			if title != "" {
				b.toolTitles[u.ToolCallID] = title
			}
			if hadStream {
				b.streamedText = false
			}
			b.stateMu.Unlock()

			if hadStream {
				b.printRaw("\n")
			}
			if title != "" && title != prevTitle && (!isPlaceholder || prevTitle == "") {
				if strings.EqualFold(u.Status, "completed") {
					suffix := ""
					if len(locs) > 0 {
						suffix = " (" + locs[0] + ")"
					}
					b.printf("\x1b[2m  ✓ %s%s\x1b[0m\n", title, suffix)
				} else if isErr {
					b.printf("\x1b[31m  ✗ %s\x1b[0m\n", title)
				} else {
					b.printf("\x1b[2m● %s\x1b[0m\n", title)
				}
				if !isPlaceholder {
					b.reportAgent("working", title)
				}
			} else if strings.EqualFold(u.Status, "completed") {
				display := orStr(title, prevTitle)
				if display != "" && (placeholder == "" || display != placeholder) {
					suffix := ""
					if len(locs) > 0 {
						suffix = " (" + locs[0] + ")"
					}
					b.printf("\x1b[2m  ✓ %s%s\x1b[0m\n", display, suffix)
				}
			} else if isErr {
				display := orStr(title, prevTitle)
				b.printf("\x1b[31m  ✗ %s\x1b[0m\n", orStr(display, "tool failed"))
			}
		}
	}
}

func extractACPToolOutput(rawOutput, content json.RawMessage) string {
	if s := decodeACPValue(rawOutput); s != "" {
		return s
	}
	var blocks []struct {
		Type    string          `json:"type"`
		Text    string          `json:"text"`
		Path    string          `json:"path"`
		Content json.RawMessage `json:"content"`
	}
	if json.Unmarshal(content, &blocks) == nil {
		var parts []string
		for _, bl := range blocks {
			if bl.Text != "" {
				parts = append(parts, bl.Text)
				continue
			}
			if len(bl.Content) > 0 {
				var inner struct {
					Type string `json:"type"`
					Text string `json:"text"`
				}
				if json.Unmarshal(bl.Content, &inner) == nil && inner.Text != "" {
					parts = append(parts, inner.Text)
					continue
				}
				if s := decodeACPValue(bl.Content); s != "" {
					parts = append(parts, s)
					continue
				}
			}
			if bl.Type == "diff" && strings.TrimSpace(bl.Path) != "" {
				parts = append(parts, "diff "+strings.TrimSpace(bl.Path))
			}
		}
		if len(parts) > 0 {
			return strings.Join(parts, "\n")
		}
	}
	return ""
}

func decodeACPValue(raw json.RawMessage) string {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		return ""
	}
	var s string
	if json.Unmarshal(trimmed, &s) == nil {
		return s
	}
	return string(trimmed)
}

func isAskQuestionPerm(opts []acpPermOption) bool {
	for _, o := range opts {
		if o.OptionID == cursorAskQuestionSkipID {
			return true
		}
	}
	return false
}

func (b *acpBridge) handleServerRequest(f rpcFrame) {
	if f.Method != "session/request_permission" {
		_ = b.sendJSON(map[string]any{
			"jsonrpc": "2.0",
			"id":      f.ID,
			"error":   map[string]any{"code": -32601, "message": "method not supported: " + f.Method},
		})
		return
	}
	var p struct {
		SessionID string `json:"sessionId"`
		ToolCall  struct {
			ToolCallID string          `json:"toolCallId"`
			Title      string          `json:"title"`
			Kind       string          `json:"kind"`
			RawInput   json.RawMessage `json:"rawInput"`
			Content    json.RawMessage `json:"content"`
			Locations  []struct {
				Path string `json:"path"`
			} `json:"locations"`
		} `json:"toolCall"`
		Options []acpPermOption `json:"options"`
	}
	if json.Unmarshal(f.Params, &p) != nil {
		_ = b.sendJSON(map[string]any{
			"jsonrpc": "2.0",
			"id":      f.ID,
			"result":  map[string]any{"outcome": map[string]any{"outcome": "cancelled"}},
		})
		return
	}

	isQuestion := isAskQuestionPerm(p.Options)

	// If bypass was enabled, approve tool permissions immediately without
	// blocking, but never auto-answer interactive model questions downgraded
	// from cursor/ask_question (marked with __ask_question_skip__).
	if b.cfg.Bypass && !isQuestion {
		if opt, ok := pickAllowOption(p.Options); ok {
			b.sendPermOutcome(f.ID, "selected", opt.OptionID)
			return
		}
	}

	if len(p.Options) == 0 {
		b.sendPermOutcome(f.ID, "cancelled", "")
		return
	}

	title := orStr(strings.TrimSpace(p.ToolCall.Title), orStr(strings.TrimSpace(p.ToolCall.Kind), "Tool"))
	cmdSummary := formatACPToolInput(p.ToolCall.RawInput)
	if cmdSummary == "" {
		cmdSummary = strings.TrimSpace(extractACPToolOutput(nil, p.ToolCall.Content))
	}
	if cmdSummary == "" && len(p.ToolCall.Locations) > 0 {
		var locs []string
		for _, l := range p.ToolCall.Locations {
			if lp := strings.TrimSpace(l.Path); lp != "" {
				locs = append(locs, lp)
			}
		}
		cmdSummary = strings.Join(locs, ", ")
	}

	// Drain any stale reply before waiting.
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

	header := b.provider.DisplayName() + " 请求执行权限：" + title
	if isQuestion {
		header = b.provider.DisplayName() + " 提问：" + title
	}
	optLabels := make([]string, len(p.Options))
	for i, opt := range p.Options {
		optLabels[i] = orStr(strings.TrimSpace(opt.Name), opt.OptionID)
	}
	menuText, menuLines := renderBridgeMenu(header, cmdSummary, optLabels)

	req := &acpPermRequest{
		ID:         f.ID,
		Title:      title,
		Kind:       p.ToolCall.Kind,
		Command:    cmdSummary,
		IsQuestion: isQuestion,
		Options:    p.Options,
		Lines:      menuLines,
	}
	b.stateMu.Lock()
	b.activePerm = req
	b.stateMu.Unlock()

	b.printRaw(menuText)
	if isQuestion {
		b.reportAgent("blocked", "Question: "+title)
	} else {
		b.reportAgent("blocked", "Permission required: "+title)
	}

	var choice string
	select {
	case choice = <-b.permReply:
	case <-b.done:
		b.stateMu.Lock()
		b.activePerm = nil
		b.stateMu.Unlock()
		return
	}

	b.stateMu.Lock()
	b.activePerm = nil
	b.stateMu.Unlock()

	// Erase the temporary approval menu so screenAsks() never false-triggers later.
	if seq := eraseBridgeMenuSeq(b.tty, menuLines); seq != "" {
		b.printRaw(seq)
	}

	if choice == "esc" {
		if opt, ok := pickRejectOption(p.Options); ok {
			b.printf("\x1b[33m[✗ 已拒绝: %s]\x1b[0m\n", title)
			b.reportAgent("working", "Permission rejected")
			b.sendPermOutcome(f.ID, "selected", opt.OptionID)
			return
		}
		b.printf("\x1b[33m[✗ 已取消: %s]\x1b[0m\n", title)
		b.reportAgent("working", "Permission cancelled")
		b.sendPermOutcome(f.ID, "cancelled", "")
		return
	}

	var picked acpPermOption
	found := false
	if idx, err := strconv.Atoi(choice); err == nil && idx >= 1 && idx <= len(p.Options) {
		picked, found = p.Options[idx-1], true
	} else if strings.EqualFold(choice, "y") && !isQuestion {
		picked, found = pickAllowOption(p.Options)
	} else if strings.EqualFold(choice, "n") {
		picked, found = pickRejectOption(p.Options)
	}
	if !found {
		picked = p.Options[0]
	}
	b.printf("\x1b[32m[✓ 已选择: %s · %s]\x1b[0m\n", orStr(picked.Name, picked.OptionID), title)
	b.reportAgent("working", "Continuing…")
	b.sendPermOutcome(f.ID, "selected", picked.OptionID)
}

func pickAllowOption(opts []acpPermOption) (acpPermOption, bool) {
	for _, o := range opts {
		if o.Kind == "allow_once" || o.Kind == "allow-once" || o.OptionID == "allow_once" || o.OptionID == "allow-once" {
			return o, true
		}
	}
	for _, o := range opts {
		if strings.HasPrefix(o.Kind, "allow") || strings.HasPrefix(o.OptionID, "allow") {
			return o, true
		}
	}
	return acpPermOption{}, false
}

func pickRejectOption(opts []acpPermOption) (acpPermOption, bool) {
	for _, o := range opts {
		if o.Kind == "reject_once" || o.Kind == "reject-once" || o.OptionID == "reject_once" || o.OptionID == "reject-once" {
			return o, true
		}
	}
	for _, o := range opts {
		if strings.HasPrefix(o.Kind, "reject") || strings.HasPrefix(o.OptionID, "reject") {
			return o, true
		}
	}
	return acpPermOption{}, false
}

func (b *acpBridge) sendPermOutcome(id json.RawMessage, outcome, optionID string) {
	out := map[string]any{"outcome": outcome}
	if optionID != "" {
		out["optionId"] = optionID
	}
	_ = b.sendJSON(map[string]any{
		"jsonrpc": "2.0",
		"id":      id,
		"result":  map[string]any{"outcome": out},
	})
}

// ---------------------------------------------------------------- prompt & input loops

func (b *acpBridge) promptLoop(ctx context.Context) {
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

func (b *acpBridge) executePrompt(ctx context.Context, text string) {
	text = strings.TrimSpace(text)
	if text == "" {
		return
	}
	b.stateMu.Lock()
	sid := b.sessionID
	b.working = true
	b.streamedText = false
	b.stateMu.Unlock()

	// Real-time session/prompt does not echo user_message_chunk, so bridge logs it.
	b.appendLog(acpLogEntry{Type: "user", Text: text})
	b.printf("\n\x1b[1m> %s\x1b[0m\n", clip(oneLine(text), 120))
	b.reportAgent("working", "Thinking…")

	_, err := b.callACP(ctx, "session/prompt", map[string]any{
		"sessionId": sid,
		"prompt": []map[string]any{
			{"type": "text", "text": text},
		},
	})

	b.stateMu.Lock()
	hadStream := b.streamedText
	b.streamedText = false
	b.working = false
	b.stateMu.Unlock()

	if hadStream {
		b.printRaw("\n")
	}
	if err != nil && ctx.Err() == nil {
		msg := err.Error()
		b.appendLog(acpLogEntry{Type: "error", Text: msg})
		b.printf("\x1b[31m⚠ Error: %s\x1b[0m\n", msg)
	}
	b.printRaw("\n❯ ")
	b.reportAgent("idle", "")
}

func (b *acpBridge) inputLoop(ctx context.Context, cancel context.CancelFunc) {
	evCh := make(chan bridgeInputEvent, 32)
	go readBridgeInput(b.stdin, func() bool {
		b.stateMu.Lock()
		defer b.stateMu.Unlock()
		return b.activePerm != nil
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
				hasPerm := b.activePerm != nil
				working := b.working
				sid := b.sessionID
				b.stateMu.Unlock()
				if hasPerm {
					select {
					case b.permReply <- "esc":
					default:
					}
				} else if working && sid != "" {
					b.notifyACP("session/cancel", map[string]any{"sessionId": sid})
				}
			case "ctrl_c":
				b.stateMu.Lock()
				hasPerm := b.activePerm != nil
				working := b.working
				sid := b.sessionID
				prev := b.lastCtrlC
				b.lastCtrlC = time.Now()
				b.stateMu.Unlock()
				if hasPerm {
					select {
					case b.permReply <- "esc":
					default:
					}
				} else if working && time.Since(prev) > 2*time.Second && sid != "" {
					b.printf("\n\x1b[33m[Interrupting… press Ctrl-C again to exit]\x1b[0m\n")
					b.notifyACP("session/cancel", map[string]any{"sessionId": sid})
				} else {
					cancel()
					return
				}
			case "eof":
				// In non-TTY test mode, wait briefly for queued prompts to finish before exiting.
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

func (b *acpBridge) appendLog(e acpLogEntry) {
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

func (b *acpBridge) printRaw(s string) {
	b.outMu.Lock()
	defer b.outMu.Unlock()
	_, _ = io.WriteString(b.stdout, s)
}

func (b *acpBridge) printf(format string, args ...any) {
	b.printRaw(fmt.Sprintf(format, args...))
}

func (b *acpBridge) resumeArgv() []string {
	b.stateMu.Lock()
	sid := b.sessionID
	b.stateMu.Unlock()
	return buildBridgeResumeArgv(b.provider.Kind(), b.provider.BypassFlag(), b.provider.SupportsEffort(), b.cfg, sid)
}

func (b *acpBridge) writePaneMeta() {
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

func (b *acpBridge) reportAgent(state, msg string) {
	b.stateMu.Lock()
	sid, lp := b.sessionID, b.logPath
	b.stateMu.Unlock()
	var argv []string
	if sid != "" {
		argv = b.resumeArgv()
	}
	reportBridgeAgent(b.herdrSock, b.paneID, b.provider.Source(), b.provider.Kind(), state, msg, sid, lp, argv, &b.herdrSeq)
}

func (b *acpBridge) reportSession() {
	b.stateMu.Lock()
	sid, lp := b.sessionID, b.logPath
	b.stateMu.Unlock()
	if sid == "" {
		return
	}
	// Persist local pane->session metadata so Collie resolves transcripts &
	// session IDs reliably even when herdr does not echo custom agent_session.
	b.writePaneMeta()
	reportBridgeSession(b.herdrSock, b.paneID, b.provider.Source(), b.provider.Kind(), sid, lp, b.resumeArgv(), &b.herdrSeq)
}

func (b *acpBridge) reportRelease() {
	reportBridgeRelease(b.herdrSock, b.paneID, b.provider.Source(), b.provider.Kind(), &b.herdrSeq)
}
