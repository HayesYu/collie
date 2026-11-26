package collie

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
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
	"unicode/utf8"
)

// Hidden subcommand `collie _bridge doubao [flags]` bridges Herdr's PTY and
// lifecycle socket API with `doubao acp serve` (ACP v1 JSON-RPC over stdio).
// It reports idle/working/blocked states to Herdr, persists incremental JSONL
// transcripts under <collieDir>/transcripts/doubao/<session_id>.jsonl for
// Collie's Web UI, handles interactive permission menus, and supports session
// restoration via ACP session/load.

const (
	doubaoBridgeSource    = "collie:doubao"
	doubaoPlaceholderTool = "继续处理任务中"
)

var doubaoPublicNoProxy = []string{
	"doubao.com",
	".doubao.com",
	"byteimg.com",
	".byteimg.com",
}

// doubaoEnv prepares the environment for invoking `doubao`: disables
// interactive update prompts and appends Doubao's public domains to
// no_proxy / NO_PROXY without clobbering existing entries.
func doubaoEnv(base []string) []string {
	out := make([]string, 0, len(base)+3)
	var curLower, curUpper string
	hasLower, hasUpper, hasNoUpdate := false, false, false
	for _, kv := range base {
		k, v, ok := strings.Cut(kv, "=")
		if !ok {
			out = append(out, kv)
			continue
		}
		switch k {
		case "DOUBAO_CLI_NO_UPDATE_CHECK":
			hasNoUpdate = true
			out = append(out, "DOUBAO_CLI_NO_UPDATE_CHECK=1")
		case "no_proxy":
			hasLower = true
			curLower = v
			out = append(out, "no_proxy="+mergeNoProxy(v, doubaoPublicNoProxy))
		case "NO_PROXY":
			hasUpper = true
			curUpper = v
			out = append(out, "NO_PROXY="+mergeNoProxy(v, doubaoPublicNoProxy))
		default:
			out = append(out, kv)
		}
	}
	if !hasNoUpdate {
		out = append(out, "DOUBAO_CLI_NO_UPDATE_CHECK=1")
	}
	if !hasLower {
		out = append(out, "no_proxy="+mergeNoProxy(curUpper, doubaoPublicNoProxy))
	}
	if !hasUpper {
		out = append(out, "NO_PROXY="+mergeNoProxy(curLower, doubaoPublicNoProxy))
	}
	return out
}

func mergeNoProxy(cur string, add []string) string {
	seen := map[string]bool{}
	var parts []string
	for _, p := range strings.Split(cur, ",") {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		low := strings.ToLower(p)
		if !seen[low] {
			seen[low] = true
			parts = append(parts, p)
		}
	}
	for _, p := range add {
		low := strings.ToLower(p)
		if !seen[low] {
			seen[low] = true
			parts = append(parts, p)
		}
	}
	return strings.Join(parts, ",")
}

// doubaoBridgeConfig holds parsed options for `collie _bridge doubao`.
type doubaoBridgeConfig struct {
	Model          string
	Effort         string
	Bypass         bool
	PermissionMode string
	Resume         string
	Cwd            string
	CollieHome     string
	Name           string
	DoubaoBin      string
}

func parseDoubaoBridgeArgs(args []string) (doubaoBridgeConfig, error) {
	var cfg doubaoBridgeConfig
	for i := 0; i < len(args); i++ {
		a := args[i]
		next := func(flagName string) (string, error) {
			if i+1 >= len(args) {
				return "", fmt.Errorf("missing value for %s", flagName)
			}
			i++
			return args[i], nil
		}
		switch {
		case a == "-y" || a == "--yolo" || a == "--dangerously-bypass-approvals-and-sandbox" || a == "--dangerously-skip-permissions":
			cfg.Bypass = true
		case a == "--model" || a == "-m":
			v, err := next(a)
			if err != nil {
				return cfg, err
			}
			cfg.Model = strings.TrimSpace(v)
		case strings.HasPrefix(a, "--model="):
			cfg.Model = strings.TrimSpace(strings.TrimPrefix(a, "--model="))
		case a == "--effort":
			v, err := next(a)
			if err != nil {
				return cfg, err
			}
			cfg.Effort = strings.TrimSpace(v)
		case strings.HasPrefix(a, "--effort="):
			cfg.Effort = strings.TrimSpace(strings.TrimPrefix(a, "--effort="))
		case a == "--permission-mode":
			v, err := next(a)
			if err != nil {
				return cfg, err
			}
			cfg.PermissionMode = strings.TrimSpace(v)
		case strings.HasPrefix(a, "--permission-mode="):
			cfg.PermissionMode = strings.TrimSpace(strings.TrimPrefix(a, "--permission-mode="))
		case a == "--resume":
			v, err := next(a)
			if err != nil {
				return cfg, err
			}
			cfg.Resume = strings.TrimSpace(v)
		case strings.HasPrefix(a, "--resume="):
			cfg.Resume = strings.TrimSpace(strings.TrimPrefix(a, "--resume="))
		case a == "-C" || a == "--cwd":
			v, err := next(a)
			if err != nil {
				return cfg, err
			}
			cfg.Cwd = strings.TrimSpace(v)
		case strings.HasPrefix(a, "--cwd="):
			cfg.Cwd = strings.TrimSpace(strings.TrimPrefix(a, "--cwd="))
		case a == "--collie-home":
			v, err := next(a)
			if err != nil {
				return cfg, err
			}
			cfg.CollieHome = strings.TrimSpace(v)
		case strings.HasPrefix(a, "--collie-home="):
			cfg.CollieHome = strings.TrimSpace(strings.TrimPrefix(a, "--collie-home="))
		case a == "--name":
			v, err := next(a)
			if err != nil {
				return cfg, err
			}
			cfg.Name = strings.TrimSpace(v)
		case strings.HasPrefix(a, "--name="):
			cfg.Name = strings.TrimSpace(strings.TrimPrefix(a, "--name="))
		case a == "--doubao-bin":
			v, err := next(a)
			if err != nil {
				return cfg, err
			}
			cfg.DoubaoBin = strings.TrimSpace(v)
		case strings.HasPrefix(a, "--doubao-bin="):
			cfg.DoubaoBin = strings.TrimSpace(strings.TrimPrefix(a, "--doubao-bin="))
		}
	}
	return cfg, nil
}

// doubaoACPArgs builds the argv for `doubao acp serve`.
// When neither model nor effort is explicitly set on a fresh session, it aligns
// with the default "Auto + high" profile.
func doubaoACPArgs(cfg doubaoBridgeConfig) []string {
	args := []string{"acp", "serve"}
	if cfg.Bypass {
		args = append(args, "-y")
	} else {
		mode := cfg.PermissionMode
		if mode == "" {
			mode = "default"
		}
		args = append(args, "--permission-mode", mode)
	}
	model := cfg.Model
	effort := cfg.Effort
	if cfg.Resume == "" {
		if model == "" {
			model = defaultDoubaoModel
		}
		if effort == "" && (strings.EqualFold(model, defaultDoubaoModel) || model == defaultDoubaoModelID) {
			effort = defaultDoubaoEffort
		}
	}
	if model != "" {
		args = append(args, "--model", model)
	}
	if effort != "" {
		args = append(args, "--effort", effort)
	}
	if cfg.Cwd != "" {
		args = append(args, "-C", cfg.Cwd)
	}
	return args
}

func runBridge(args []string) {
	if len(args) > 0 && args[0] == "claude-sg" {
		runClaudeSGBridge(args[1:])
		return
	}
	if len(args) == 0 || args[0] != "doubao" {
		fmt.Fprintln(os.Stderr, "usage: collie _bridge <doubao|claude-sg> [flags]")
		os.Exit(2)
	}
	cfg, err := parseDoubaoBridgeArgs(args[1:])
	if err != nil {
		fmt.Fprintf(os.Stderr, "[doubao-bridge] fatal: %v\n", err)
		os.Exit(2)
	}
	if cfg.Cwd == "" {
		cfg.Cwd, _ = os.Getwd()
	}
	if cfg.CollieHome == "" {
		cfg.CollieHome = collieDir()
	}
	home, _ := os.UserHomeDir()
	if cfg.DoubaoBin == "" {
		cfg.DoubaoBin = findBin("doubao", home)
	}
	if cfg.DoubaoBin == "" {
		fmt.Fprintln(os.Stderr, "[doubao-bridge] fatal: doubao command not found in PATH")
		os.Exit(1)
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGHUP)
	defer stop()

	restore, rawOK := func() (func(), bool) {
		if isTerminal(os.Stdin) {
			return makeRawInput(os.Stdin)
		}
		return func() {}, false
	}()
	if rawOK {
		defer restore()
		// Enable bracketed paste mode so Herdr.Prompt multi-line paste stays framed.
		_, _ = os.Stdout.WriteString("\x1b[?2004h")
		defer func() { _, _ = os.Stdout.WriteString("\x1b[?2004l") }()
	}

	b := &doubaoBridge{
		cfg:           cfg,
		herdrSock:     os.Getenv("HERDR_SOCKET_PATH"),
		paneID:        os.Getenv("HERDR_PANE_ID"),
		transcriptDir: filepath.Join(cfg.CollieHome, "transcripts", "doubao"),
		stdin:         os.Stdin,
		stdout:        os.Stdout,
		stderr:        os.Stderr,
		tty:           rawOK,
		pending:       map[int64]chan rpcFrame{},
		promptQueue:   make(chan string, 64),
		permReply:     make(chan string, 4),
	}
	if err := b.run(ctx); err != nil {
		fmt.Fprintf(os.Stderr, "\n[doubao-bridge] fatal: %v\n", err)
		os.Exit(1)
	}
}

// ---------------------------------------------------------------- local pane metadata

type doubaoPaneMeta struct {
	PaneID    string `json:"pane_id"`
	SessionID string `json:"session_id"`
	Path      string `json:"path"`
	Cwd       string `json:"cwd,omitempty"`
	Title     string `json:"title,omitempty"`
	Model     string `json:"model,omitempty"`
	Effort    string `json:"effort,omitempty"`
	PID       int    `json:"pid,omitempty"`
	UpdatedAt string `json:"updated_at,omitempty"`
}

func doubaoPaneMetaPath(transcriptDir, paneID string) string {
	safe := strings.NewReplacer("/", "_", "\\", "_", ":", "_", "@", "_").Replace(paneID)
	return filepath.Join(transcriptDir, ".panes", safe+".json")
}

func writeDoubaoPaneMeta(transcriptDir string, m doubaoPaneMeta) error {
	if transcriptDir == "" || m.PaneID == "" {
		return nil
	}
	p := doubaoPaneMetaPath(transcriptDir, m.PaneID)
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		return err
	}
	m.UpdatedAt = time.Now().UTC().Format(time.RFC3339)
	b, err := json.Marshal(m)
	if err != nil {
		return err
	}
	tmp := p + ".tmp." + strconv.Itoa(os.Getpid())
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, p)
}

func readDoubaoPaneMeta(transcriptDir, paneID string) (doubaoPaneMeta, bool) {
	var m doubaoPaneMeta
	if transcriptDir == "" || paneID == "" {
		return m, false
	}
	b, err := os.ReadFile(doubaoPaneMetaPath(transcriptDir, paneID))
	if err != nil || json.Unmarshal(b, &m) != nil || m.SessionID == "" {
		return doubaoPaneMeta{}, false
	}
	return m, true
}

func removeDoubaoPaneMeta(transcriptDir, paneID string, pid int) {
	if transcriptDir == "" || paneID == "" {
		return
	}
	p := doubaoPaneMetaPath(transcriptDir, paneID)
	if pid > 0 {
		if cur, ok := readDoubaoPaneMeta(transcriptDir, paneID); ok && cur.PID != 0 && cur.PID != pid {
			return
		}
	}
	_ = os.Remove(p)
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
	ID      json.RawMessage
	Title   string
	Kind    string
	Command string
	Options []acpPermOption
	Lines   int // number of terminal lines printed for the prompt menu
}

type doubaoLogEntry struct {
	Type      string   `json:"type"` // user | assistant_chunk | tool | error
	ID        string   `json:"id,omitempty"`
	Text      string   `json:"text,omitempty"`
	Title     string   `json:"title,omitempty"`
	Kind      string   `json:"kind,omitempty"`
	Status    string   `json:"status,omitempty"`
	Locations []string `json:"locations,omitempty"`
	Input     string   `json:"input,omitempty"`
	Output    string   `json:"output,omitempty"`
	Error     bool     `json:"error,omitempty"`
	TS        int64    `json:"ts,omitempty"`
}

// ---------------------------------------------------------------- bridge runtime

type doubaoBridge struct {
	cfg           doubaoBridgeConfig
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
}

func (b *doubaoBridge) run(ctx context.Context) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

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

	// 1) ACP initialize (must include non-empty clientInfo.name)
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

	// 2) session/new or session/load (no session/resume in doubao ACP)
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
			return fmt.Errorf("doubao acp exited: %w", err)
		}
	}
	cancel()
	select {
	case <-promptDone:
	case <-time.After(2 * time.Second):
	}
	return nil
}

func (b *doubaoBridge) startACP(ctx context.Context) error {
	acpArgs := doubaoACPArgs(b.cfg)
	cmd := exec.CommandContext(ctx, b.cfg.DoubaoBin, acpArgs...)
	cmd.Dir = b.cfg.Cwd
	cmd.Env = doubaoEnv(os.Environ())
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
		return fmt.Errorf("start doubao acp serve: %w", err)
	}
	b.cmd, b.acpIn, b.acpOut, b.acpErr = cmd, in, out, errPipe
	go b.copyStderr()
	return nil
}

func (b *doubaoBridge) copyStderr() {
	r := bufio.NewReaderSize(b.acpErr, 64*1024)
	for {
		line, err := r.ReadString('\n')
		if s := strings.TrimSpace(line); s != "" {
			// Ignore noisy telemetry lines while surfacing real errors.
			if !strings.HasPrefix(s, "[cli-chat] ") {
				b.printf("\x1b[33m%s\x1b[0m\n", s)
			}
		}
		if err != nil {
			return
		}
	}
}

func (b *doubaoBridge) openSession(ctx context.Context) error {
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
			return fmt.Errorf("acp session/load %s: %w", sid, err)
		}
	} else {
		raw, err = b.callACP(sessCtx, "session/new", map[string]any{
			"cwd":        b.cfg.Cwd,
			"mcpServers": []any{},
		})
		if err != nil {
			return fmt.Errorf("acp session/new: %w", err)
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
		if opt.ID == "effort" && opt.CurrentValue != "" {
			b.effortLabel = opt.CurrentValue
		}
		if opt.ID == "model" && b.modelLabel == "" && opt.CurrentValue != "" {
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

func (b *doubaoBridge) printBanner() {
	b.stateMu.Lock()
	sid, model, effort := b.sessionID, b.modelLabel, b.effortLabel
	b.stateMu.Unlock()
	if model == "" {
		model = orStr(b.cfg.Model, defaultDoubaoModel)
	}
	if effort == "" {
		effort = orStr(b.cfg.Effort, defaultDoubaoEffort)
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
	b.printf("\x1b[1mDoubao\x1b[0m \x1b[2m(%s · %s · %s · %s · %s)\x1b[0m\n❯ ", model, effort, mode, action, shortID)
}

func (b *doubaoBridge) cleanup() {
	b.reportRelease()
	removeDoubaoPaneMeta(b.transcriptDir, b.paneID, os.Getpid())
	b.stateMu.Lock()
	if b.logFile != nil {
		_ = b.logFile.Close()
		b.logFile = nil
	}
	b.stateMu.Unlock()
	if b.acpIn != nil {
		_ = b.acpIn.Close()
	}
	if b.cmd != nil && b.cmd.Process != nil {
		_ = b.cmd.Process.Signal(syscall.SIGTERM)
		done := make(chan struct{})
		go func() {
			_, _ = b.cmd.Process.Wait()
			close(done)
		}()
		select {
		case <-done:
		case <-time.After(1500 * time.Millisecond):
			_ = b.cmd.Process.Kill()
		}
	}
}

// ---------------------------------------------------------------- ACP JSON-RPC transport

func (b *doubaoBridge) sendJSON(v any) error {
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

func (b *doubaoBridge) callACP(ctx context.Context, method string, params any) (json.RawMessage, error) {
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

func (b *doubaoBridge) notifyACP(method string, params any) {
	_ = b.sendJSON(map[string]any{
		"jsonrpc": "2.0",
		"method":  method,
		"params":  params,
	})
}

// readACPLoop reads newline-delimited JSON-RPC frames using bufio.Reader.ReadBytes('\n')
// so large frames (such as ~60KB+ available_commands_update) never hit bufio.Scanner's
// 64KB token limit.
func (b *doubaoBridge) readACPLoop() {
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

func (b *doubaoBridge) dispatchACP(f rpcFrame) {
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

func (b *doubaoBridge) handleNotification(f rpcFrame) {
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
				b.appendLog(doubaoLogEntry{Type: "user", ID: u.MessageID, Text: u.Content.Text})
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
				b.appendLog(doubaoLogEntry{Type: "assistant_chunk", ID: u.MessageID, Text: u.Content.Text})
			}
			if !replaying {
				b.stateMu.Lock()
				b.streamedText = true
				b.stateMu.Unlock()
				b.printRaw(u.Content.Text)
			}
		}

	case "agent_thought_chunk":
		// Not emitted by doubao 2.38.2; safely ignored if introduced later.

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
			b.appendLog(doubaoLogEntry{
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
			if title != "" && title != prevTitle && (title != doubaoPlaceholderTool || prevTitle == "") {
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
				if title != doubaoPlaceholderTool {
					b.reportAgent("working", title)
				}
			} else if strings.EqualFold(u.Status, "completed") {
				display := orStr(title, prevTitle)
				if display != "" && display != doubaoPlaceholderTool {
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

func formatACPToolInput(raw json.RawMessage) string {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) || bytes.Equal(trimmed, []byte("{}")) {
		return ""
	}
	return toolSummary("", trimmed)
}

func extractACPToolOutput(rawOutput, content json.RawMessage) string {
	if s := decodeACPValue(rawOutput); s != "" {
		return s
	}
	var blocks []struct {
		Type    string          `json:"type"`
		Text    string          `json:"text"`
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
				} else if s := decodeACPValue(bl.Content); s != "" {
					parts = append(parts, s)
				}
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

func (b *doubaoBridge) handleServerRequest(f rpcFrame) {
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

	// If bypass was enabled, approve immediately without blocking.
	if b.cfg.Bypass {
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

	// Drain any stale reply before waiting.
	for len(b.permReply) > 0 {
		<-b.permReply
	}

	// Render a clean numbered approval menu matching Collie's parseApproval().
	var menu bytes.Buffer
	menuLines := 0
	writeLine := func(s string) {
		menu.WriteString(s)
		menu.WriteByte('\n')
		menuLines++
	}
	b.stateMu.Lock()
	hadStream := b.streamedText
	b.streamedText = false
	b.stateMu.Unlock()
	if hadStream {
		b.printRaw("\n")
	}
	writeLine("────────────────────────────────────────")
	writeLine("Doubao 请求执行权限：" + title)
	if cmdSummary != "" {
		for _, cl := range strings.Split(clip(cmdSummary, 240), "\n") {
			writeLine("  " + cl)
		}
	}
	writeLine("")
	for i, opt := range p.Options {
		label := orStr(strings.TrimSpace(opt.Name), opt.OptionID)
		writeLine(fmt.Sprintf("%d. %s", i+1, label))
	}
	writeLine(fmt.Sprintf("Enter number (1-%d) to confirm · Esc to cancel", len(p.Options)))

	req := &acpPermRequest{
		ID:      f.ID,
		Title:   title,
		Kind:    p.ToolCall.Kind,
		Command: cmdSummary,
		Options: p.Options,
		Lines:   menuLines,
	}
	b.stateMu.Lock()
	b.activePerm = req
	b.stateMu.Unlock()

	b.printRaw(menu.String())
	b.reportAgent("blocked", "Permission required: "+title)

	choice := <-b.permReply

	b.stateMu.Lock()
	b.activePerm = nil
	b.stateMu.Unlock()

	// Erase the temporary approval menu so screenAsks() never false-triggers later.
	if b.tty && menuLines > 0 {
		b.printf("\x1b[%dA\x1b[J", menuLines)
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
	} else if strings.EqualFold(choice, "y") {
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
		if o.Kind == "allow_once" || o.OptionID == "allow_once" {
			return o, true
		}
	}
	for _, o := range opts {
		if strings.HasPrefix(o.Kind, "allow") {
			return o, true
		}
	}
	return acpPermOption{}, false
}

func pickRejectOption(opts []acpPermOption) (acpPermOption, bool) {
	for _, o := range opts {
		if o.Kind == "reject_once" || o.OptionID == "reject_once" {
			return o, true
		}
	}
	for _, o := range opts {
		if strings.HasPrefix(o.Kind, "reject") {
			return o, true
		}
	}
	return acpPermOption{}, false
}

func (b *doubaoBridge) sendPermOutcome(id json.RawMessage, outcome, optionID string) {
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

// ---------------------------------------------------------------- prompt loop

func (b *doubaoBridge) promptLoop(ctx context.Context) {
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

func (b *doubaoBridge) executePrompt(ctx context.Context, text string) {
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
	b.appendLog(doubaoLogEntry{Type: "user", Text: text})
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
		b.appendLog(doubaoLogEntry{Type: "error", Text: msg})
		b.printf("\x1b[31m⚠ Error: %s\x1b[0m\n", msg)
	}
	b.printRaw("\n❯ ")
	b.reportAgent("idle", "")
}

// ---------------------------------------------------------------- stdin / bracketed-paste parser

type bridgeInputEvent struct {
	kind string // "prompt" | "perm" | "esc" | "ctrl_c" | "eof"
	text string
}

// readBridgeInput parses PTY/pipe stdin into structured prompt, permission choice,
// Esc, Ctrl-C and EOF events, keeping Bracketed Paste (\x1b[200~...\x1b[201~)
// multi-line blocks intact as a single prompt.
func readBridgeInput(r io.Reader, isPermActive func() bool, out chan<- bridgeInputEvent) {
	defer close(out)
	byteCh := make(chan byte, 4096)
	go func() {
		defer close(byteCh)
		buf := make([]byte, 4096)
		for {
			n, err := r.Read(buf)
			for i := 0; i < n; i++ {
				byteCh <- buf[i]
			}
			if err != nil {
				return
			}
		}
	}()

	const (
		pasteStart = "\x1b[200~"
		pasteEnd   = "\x1b[201~"
	)

	var (
		inPaste      bool
		lineBuf      []byte
		pasteTail    []byte
		swallowNLUnt time.Time
	)

	flushPrompt := func() {
		s := strings.TrimSpace(string(lineBuf))
		lineBuf = lineBuf[:0]
		if s != "" {
			out <- bridgeInputEvent{kind: "prompt", text: s}
		}
	}

	nextByteTimeout := func(d time.Duration) (byte, bool, bool) {
		select {
		case c, ok := <-byteCh:
			if !ok {
				return 0, false, true
			}
			return c, true, false
		case <-time.After(d):
			return 0, false, false
		}
	}

	for {
		c, ok := <-byteCh
		if !ok {
			if len(lineBuf) > 0 {
				flushPrompt()
			}
			out <- bridgeInputEvent{kind: "eof"}
			return
		}

		if inPaste {
			pasteTail = append(pasteTail, c)
			if len(pasteTail) >= len(pasteEnd) && string(pasteTail[len(pasteTail)-len(pasteEnd):]) == pasteEnd {
				chunk := pasteTail[:len(pasteTail)-len(pasteEnd)]
				chunk = bytes.ReplaceAll(chunk, []byte("\r\n"), []byte("\n"))
				chunk = bytes.ReplaceAll(chunk, []byte("\r"), []byte("\n"))
				lineBuf = append(lineBuf, chunk...)
				pasteTail = pasteTail[:0]
				inPaste = false
			}
			continue
		}

		// Handle ESC and ANSI CSI escape sequences (including bracketed paste start).
		if c == 0x1b {
			b1, got1, eof := nextByteTimeout(40 * time.Millisecond)
			if eof {
				out <- bridgeInputEvent{kind: "esc"}
				out <- bridgeInputEvent{kind: "eof"}
				return
			}
			if !got1 {
				out <- bridgeInputEvent{kind: "esc"}
				continue
			}
			if b1 != '[' && b1 != 'O' {
				out <- bridgeInputEvent{kind: "esc"}
				// Process b1 in the normal flow if printable.
				if b1 >= 0x20 && b1 != 0x7f {
					lineBuf = append(lineBuf, b1)
				}
				continue
			}
			// Read CSI / SS3 sequence until final byte (0x40..0x7E).
			seq := []byte{0x1b, b1}
			for len(seq) < 32 {
				bn, gotn, eofn := nextByteTimeout(40 * time.Millisecond)
				if eofn || !gotn {
					break
				}
				seq = append(seq, bn)
				if bn >= 0x40 && bn <= 0x7e {
					break
				}
			}
			if string(seq) == pasteStart {
				inPaste = true
				pasteTail = pasteTail[:0]
			}
			continue
		}

		switch c {
		case 0x03: // Ctrl-C
			lineBuf = lineBuf[:0]
			out <- bridgeInputEvent{kind: "ctrl_c"}
			continue
		case 0x04: // Ctrl-D
			if len(lineBuf) == 0 {
				out <- bridgeInputEvent{kind: "eof"}
				return
			}
			continue
		case '\r', '\n':
			if !swallowNLUnt.IsZero() && time.Now().Before(swallowNLUnt) && len(lineBuf) == 0 {
				swallowNLUnt = time.Time{}
				continue
			}
			flushPrompt()
			continue
		case 0x7f, 0x08: // Backspace
			if len(lineBuf) > 0 {
				_, size := utf8.DecodeLastRune(lineBuf)
				if size > 0 && size <= len(lineBuf) {
					lineBuf = lineBuf[:len(lineBuf)-size]
				}
			}
			continue
		}

		// When an approval prompt is active, a bare digit or y/n answers immediately.
		if isPermActive != nil && isPermActive() && len(lineBuf) == 0 {
			if (c >= '1' && c <= '9') || c == 'y' || c == 'Y' || c == 'n' || c == 'N' {
				swallowNLUnt = time.Now().Add(250 * time.Millisecond)
				out <- bridgeInputEvent{kind: "perm", text: string(c)}
				continue
			}
		}

		if c >= 0x20 || c == '\t' {
			lineBuf = append(lineBuf, c)
		}
	}
}

func (b *doubaoBridge) inputLoop(ctx context.Context, cancel context.CancelFunc) {
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

// ---------------------------------------------------------------- transcript & terminal helpers

func (b *doubaoBridge) appendLog(e doubaoLogEntry) {
	e.TS = time.Now().UnixMilli()
	raw, err := json.Marshal(e)
	if err != nil {
		return
	}
	b.stateMu.Lock()
	defer b.stateMu.Unlock()
	if b.logFile == nil {
		return
	}
	_, _ = b.logFile.Write(append(raw, '\n'))
}

func (b *doubaoBridge) printRaw(s string) {
	b.outMu.Lock()
	defer b.outMu.Unlock()
	_, _ = io.WriteString(b.stdout, s)
}

func (b *doubaoBridge) printf(format string, args ...any) {
	b.printRaw(fmt.Sprintf(format, args...))
}

func fileNonEmpty(path string) bool {
	fi, err := os.Stat(path)
	return err == nil && fi.Mode().IsRegular() && fi.Size() > 0
}

// ---------------------------------------------------------------- Herdr socket reporting

func (b *doubaoBridge) resumeArgv() []string {
	// Herdr requires resume_argv[0] to be a plain command name (no path separators).
	argv := []string{"collie", "_bridge", "doubao"}
	if b.cfg.Bypass {
		argv = append(argv, "-y")
	}
	if b.cfg.Model != "" {
		argv = append(argv, "--model", b.cfg.Model)
	}
	if b.cfg.Effort != "" {
		argv = append(argv, "--effort", b.cfg.Effort)
	}
	b.stateMu.Lock()
	sid := b.sessionID
	b.stateMu.Unlock()
	if sid != "" {
		argv = append(argv, "--resume", sid)
	}
	return argv
}

func (b *doubaoBridge) reportAgent(state, msg string) {
	if b.herdrSock == "" || b.paneID == "" {
		return
	}
	b.stateMu.Lock()
	sid, lp := b.sessionID, b.logPath
	b.stateMu.Unlock()
	seq := b.herdrSeq.Add(1)
	params := map[string]any{
		"pane_id": b.paneID,
		"source":  doubaoBridgeSource,
		"agent":   "doubao",
		"state":   state,
		"seq":     seq,
	}
	if msg != "" {
		params["message"] = clip(msg, 120)
	}
	if lp != "" {
		params["agent_session_path"] = lp
	} else if sid != "" {
		params["agent_session_id"] = sid
	}
	if sid != "" {
		params["resume_argv"] = b.resumeArgv()
	}
	if err := b.callHerdr("pane.report_agent", params); err != nil {
		// Fallback with minimal required fields in case an older/stricter herdr
		// rejects optional session/resume metadata.
		_ = b.callHerdr("pane.report_agent", map[string]any{
			"pane_id": b.paneID,
			"source":  doubaoBridgeSource,
			"agent":   "doubao",
			"state":   state,
			"seq":     b.herdrSeq.Add(1),
		})
	}
}

func (b *doubaoBridge) reportSession() {
	b.stateMu.Lock()
	sid, lp, model, effort := b.sessionID, b.logPath, b.modelLabel, b.effortLabel
	b.stateMu.Unlock()
	if sid == "" {
		return
	}
	// Persist local pane->session metadata so Collie resolves transcripts &
	// session IDs reliably even when herdr does not echo custom agent_session.
	if b.paneID != "" {
		_ = writeDoubaoPaneMeta(b.transcriptDir, doubaoPaneMeta{
			PaneID:    b.paneID,
			SessionID: sid,
			Path:      lp,
			Cwd:       b.cfg.Cwd,
			Model:     model,
			Effort:    effort,
			PID:       os.Getpid(),
		})
	}
	if b.herdrSock == "" || b.paneID == "" {
		return
	}
	params := map[string]any{
		"pane_id":     b.paneID,
		"source":      doubaoBridgeSource,
		"agent":       "doubao",
		"seq":         b.herdrSeq.Add(1),
		"resume_argv": b.resumeArgv(),
	}
	if lp != "" {
		params["agent_session_path"] = lp
	} else {
		params["agent_session_id"] = sid
	}
	_ = b.callHerdr("pane.report_agent_session", params)
}

func (b *doubaoBridge) reportRelease() {
	if b.herdrSock == "" || b.paneID == "" {
		return
	}
	_ = b.callHerdr("pane.release_agent", map[string]any{
		"pane_id": b.paneID,
		"source":  doubaoBridgeSource,
		"agent":   "doubao",
		"seq":     b.herdrSeq.Add(1),
	})
}

func (b *doubaoBridge) callHerdr(method string, params map[string]any) error {
	n := b.herdrSeq.Load()
	return callHerdrSocket(b.herdrSock, "db-"+strconv.FormatInt(n, 10), method, params)
}

func callHerdrSocket(sock, id, method string, params map[string]any) error {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	var d net.Dialer
	conn, err := d.DialContext(ctx, "unix", sock)
	if err != nil {
		return err
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(3 * time.Second))
	req, _ := json.Marshal(map[string]any{"id": id, "method": method, "params": params})
	if _, err := conn.Write(append(req, '\n')); err != nil {
		return err
	}
	r := bufio.NewReaderSize(conn, 32*1024)
	line, err := r.ReadBytes('\n')
	if err != nil {
		return err
	}
	var env struct {
		Error *HerdrError `json:"error"`
	}
	if json.Unmarshal(line, &env) == nil && env.Error != nil {
		return env.Error
	}
	return nil
}

// shellQuote quotes s safely for POSIX sh/zsh/bash command lines.
func shellQuote(s string) string {
	if s == "" {
		return "''"
	}
	safe := true
	for i := 0; i < len(s); i++ {
		c := s[i]
		if !((c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') ||
			c == '/' || c == '.' || c == '_' || c == '-' || c == ':' || c == '=' || c == '@' || c == '+') {
			safe = false
			break
		}
	}
	if safe {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
