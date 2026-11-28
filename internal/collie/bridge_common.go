package collie

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"syscall"
	"time"
	"unicode/utf8"
)

// Shared bridge runtime helpers for `collie _bridge <doubao|cursor|pi|omp> [flags]`.
// Protocol drivers (ACP v1 in acp_bridge.go and Pi JSONL RPC in pi_rpc_bridge.go)
// reuse this foundation for CLI argument parsing, PTY raw/bracketed-paste input,
// local .panes metadata, incremental JSONL transcripts, Herdr socket lifecycle
// reporting, and numbered approval menus matching Collie's parseApproval().

// isBridgeKind reports whether a canonical herdr kind runs via `collie _bridge`.
func isBridgeKind(kind string) bool {
	switch kind {
	case "doubao", "cursor", "pi", "omp":
		return true
	}
	return false
}

// acpBridgeConfig holds parsed options for `collie _bridge <kind>`.
type acpBridgeConfig struct {
	Model          string
	Effort         string
	Bypass         bool
	PermissionMode string
	Resume         string
	Cwd            string
	CollieHome     string
	Name           string
	Bin            string
}

func parseACPBridgeArgs(args []string, binFlag string) (acpBridgeConfig, error) {
	var cfg acpBridgeConfig
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
		case a == "-y" || a == "--yolo" || a == "-f" || a == "--force" ||
			a == "-a" || a == "--approve" || a == "--auto-approve" ||
			a == "--dangerously-bypass-approvals-and-sandbox" || a == "--dangerously-skip-permissions":
			cfg.Bypass = true
		case a == "--approve-mcps":
			// Accepted and ignored; providers add it when Bypass is active.
		case a == "--approval-mode":
			v, err := next(a)
			if err != nil {
				return cfg, err
			}
			applyApprovalMode(&cfg, v)
		case strings.HasPrefix(a, "--approval-mode="):
			applyApprovalMode(&cfg, strings.TrimPrefix(a, "--approval-mode="))
		case a == "--model" || a == "-m":
			v, err := next(a)
			if err != nil {
				return cfg, err
			}
			cfg.Model = strings.TrimSpace(v)
		case strings.HasPrefix(a, "--model="):
			cfg.Model = strings.TrimSpace(strings.TrimPrefix(a, "--model="))
		case a == "--effort" || a == "--thinking":
			v, err := next(a)
			if err != nil {
				return cfg, err
			}
			cfg.Effort = strings.TrimSpace(v)
		case strings.HasPrefix(a, "--effort="):
			cfg.Effort = strings.TrimSpace(strings.TrimPrefix(a, "--effort="))
		case strings.HasPrefix(a, "--thinking="):
			cfg.Effort = strings.TrimSpace(strings.TrimPrefix(a, "--thinking="))
		case a == "--permission-mode":
			v, err := next(a)
			if err != nil {
				return cfg, err
			}
			cfg.PermissionMode = strings.TrimSpace(v)
		case strings.HasPrefix(a, "--permission-mode="):
			cfg.PermissionMode = strings.TrimSpace(strings.TrimPrefix(a, "--permission-mode="))
		case a == "--resume" || a == "--session":
			v, err := next(a)
			if err != nil {
				return cfg, err
			}
			cfg.Resume = strings.TrimSpace(v)
		case strings.HasPrefix(a, "--resume="):
			cfg.Resume = strings.TrimSpace(strings.TrimPrefix(a, "--resume="))
		case strings.HasPrefix(a, "--session="):
			cfg.Resume = strings.TrimSpace(strings.TrimPrefix(a, "--session="))
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
		case a == "--bin" || (binFlag != "" && a == binFlag):
			v, err := next(a)
			if err != nil {
				return cfg, err
			}
			cfg.Bin = strings.TrimSpace(v)
		case strings.HasPrefix(a, "--bin="):
			cfg.Bin = strings.TrimSpace(strings.TrimPrefix(a, "--bin="))
		case binFlag != "" && strings.HasPrefix(a, binFlag+"="):
			cfg.Bin = strings.TrimSpace(strings.TrimPrefix(a, binFlag+"="))
		}
	}
	return cfg, nil
}

func applyApprovalMode(cfg *acpBridgeConfig, raw string) {
	v := strings.TrimSpace(raw)
	if strings.EqualFold(v, "yolo") {
		cfg.Bypass = true
		cfg.PermissionMode = ""
		return
	}
	cfg.Bypass = false
	cfg.PermissionMode = v
}

func enterBridgeRawMode() (func(), bool) {
	if !isTerminal(os.Stdin) {
		return func() {}, false
	}
	restore, rawOK := makeRawInput(os.Stdin)
	if !rawOK {
		return func() {}, false
	}
	// Enable bracketed paste mode so Herdr.Prompt multi-line paste stays framed.
	_, _ = os.Stdout.WriteString("\x1b[?2004h")
	return func() {
		_, _ = os.Stdout.WriteString("\x1b[?2004l")
		restore()
	}, true
}

func runBridge(args []string) {
	if len(args) > 0 && args[0] == "claude-sg" {
		runClaudeSGBridge(args[1:])
		return
	}
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "usage: collie _bridge <doubao|cursor|pi|omp|claude-sg> [flags]")
		os.Exit(2)
	}
	kind := args[0]
	if kind == "oh-my-pi" {
		kind = "omp"
	}
	if kind == "pi" {
		runPiBridgeCLI(args[1:])
		return
	}
	provider := acpProviderFor(kind)
	if provider == nil {
		fmt.Fprintln(os.Stderr, "usage: collie _bridge <doubao|cursor|pi|omp|claude-sg> [flags]")
		os.Exit(2)
	}
	cfg, err := parseACPBridgeArgs(args[1:], provider.BinFlag())
	if err != nil {
		fmt.Fprintf(os.Stderr, "%s fatal: %v\n", provider.LogPrefix(), err)
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
		cfg.Bin = findBin(provider.BinName(), home)
	}
	if cfg.Bin == "" {
		fmt.Fprintf(os.Stderr, "%s fatal: %s command not found in PATH\n", provider.LogPrefix(), provider.BinName())
		os.Exit(1)
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGHUP)
	defer stop()

	restore, rawOK := enterBridgeRawMode()
	if rawOK {
		defer restore()
	}

	b := &acpBridge{
		provider:      provider,
		cfg:           cfg,
		herdrSock:     os.Getenv("HERDR_SOCKET_PATH"),
		paneID:        os.Getenv("HERDR_PANE_ID"),
		transcriptDir: filepath.Join(cfg.CollieHome, "transcripts", provider.Kind()),
		stdin:         os.Stdin,
		stdout:        os.Stdout,
		stderr:        os.Stderr,
		tty:           rawOK,
		pending:       map[int64]chan rpcFrame{},
		promptQueue:   make(chan string, 64),
		permReply:     make(chan string, 4),
	}
	if err := b.run(ctx); err != nil {
		fmt.Fprintf(os.Stderr, "\n%s fatal: %v\n", provider.LogPrefix(), err)
		os.Exit(1)
	}
}

// ---------------------------------------------------------------- local pane metadata

type acpPaneMeta struct {
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

func acpPaneMetaPath(transcriptDir, paneID string) string {
	safe := strings.NewReplacer("/", "_", "\\", "_", ":", "_", "@", "_").Replace(paneID)
	return filepath.Join(transcriptDir, ".panes", safe+".json")
}

func writeACPPaneMeta(transcriptDir string, m acpPaneMeta) error {
	if transcriptDir == "" || m.PaneID == "" {
		return nil
	}
	p := acpPaneMetaPath(transcriptDir, m.PaneID)
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

func readACPPaneMeta(transcriptDir, paneID string) (acpPaneMeta, bool) {
	var m acpPaneMeta
	if transcriptDir == "" || paneID == "" {
		return m, false
	}
	b, err := os.ReadFile(acpPaneMetaPath(transcriptDir, paneID))
	if err != nil || json.Unmarshal(b, &m) != nil || m.SessionID == "" {
		return acpPaneMeta{}, false
	}
	return m, true
}

func removeACPPaneMeta(transcriptDir, paneID string, pid int) {
	if transcriptDir == "" || paneID == "" {
		return
	}
	p := acpPaneMetaPath(transcriptDir, paneID)
	if pid > 0 {
		if cur, ok := readACPPaneMeta(transcriptDir, paneID); ok && cur.PID != 0 && cur.PID != pid {
			return
		}
	}
	_ = os.Remove(p)
}

// ---------------------------------------------------------------- JSONL transcript & formatting

type acpLogEntry struct {
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

func marshalBridgeLogEntry(e acpLogEntry) ([]byte, error) {
	e.TS = time.Now().UnixMilli()
	raw, err := json.Marshal(e)
	if err != nil {
		return nil, err
	}
	return append(raw, '\n'), nil
}

func formatACPToolInput(raw json.RawMessage) string {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) || bytes.Equal(trimmed, []byte("{}")) {
		return ""
	}
	return toolSummary("", trimmed)
}

func fileNonEmpty(path string) bool {
	fi, err := os.Stat(path)
	return err == nil && fi.Mode().IsRegular() && fi.Size() > 0
}

// ---------------------------------------------------------------- approval / dialog menu

// renderBridgeMenu formats a clean numbered approval or choice menu matching
// Collie's parseApproval() so Web UI and CLI approval flows work uniformly.
func renderBridgeMenu(header, detail string, options []string) (string, int) {
	var menu bytes.Buffer
	lines := 0
	writeLine := func(s string) {
		menu.WriteString(s)
		menu.WriteByte('\n')
		lines++
	}
	writeLine("────────────────────────────────────────")
	writeLine(header)
	if detail != "" {
		for _, cl := range strings.Split(clip(detail, 240), "\n") {
			writeLine("  " + cl)
		}
	}
	writeLine("")
	for i, opt := range options {
		writeLine(fmt.Sprintf("%d. %s", i+1, opt))
	}
	writeLine(fmt.Sprintf("Enter number (1-%d) to confirm · Esc to cancel", len(options)))
	return menu.String(), lines
}

func eraseBridgeMenuSeq(tty bool, lines int) string {
	if !tty || lines <= 0 {
		return ""
	}
	return fmt.Sprintf("\x1b[%dA\x1b[J", lines)
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

// ---------------------------------------------------------------- Herdr socket reporting

func buildBridgeResumeArgv(kind, bypassFlag string, supportsEffort bool, cfg acpBridgeConfig, sid string) []string {
	// Herdr requires resume_argv[0] to be a plain command name (no path separators).
	argv := []string{"collie", "_bridge", kind}
	if cfg.Bypass && bypassFlag != "" {
		argv = append(argv, bypassFlag)
	}
	if cfg.Model != "" {
		argv = append(argv, "--model", cfg.Model)
	}
	if supportsEffort && cfg.Effort != "" {
		argv = append(argv, "--effort", cfg.Effort)
	}
	if sid != "" {
		argv = append(argv, "--resume", sid)
	}
	return argv
}

func reportBridgeAgent(sock, paneID, source, kind, state, msg, sid, logPath string, resumeArgv []string, seq *atomic.Int64) {
	if sock == "" || paneID == "" {
		return
	}
	n := seq.Add(1)
	params := map[string]any{
		"pane_id": paneID,
		"source":  source,
		"agent":   kind,
		"state":   state,
		"seq":     n,
	}
	if msg != "" {
		params["message"] = clip(msg, 120)
	}
	if logPath != "" {
		params["agent_session_path"] = logPath
	} else if sid != "" {
		params["agent_session_id"] = sid
	}
	if sid != "" && len(resumeArgv) > 0 {
		params["resume_argv"] = resumeArgv
	}
	if err := callHerdrSocket(sock, kind, n, "pane.report_agent", params); err != nil {
		// Fallback with minimal required fields in case an older/stricter herdr
		// rejects optional session/resume metadata.
		n2 := seq.Add(1)
		_ = callHerdrSocket(sock, kind, n2, "pane.report_agent", map[string]any{
			"pane_id": paneID,
			"source":  source,
			"agent":   kind,
			"state":   state,
			"seq":     n2,
		})
	}
}

func reportBridgeSession(sock, paneID, source, kind, sid, logPath string, resumeArgv []string, seq *atomic.Int64) {
	if sock == "" || paneID == "" || sid == "" {
		return
	}
	n := seq.Add(1)
	params := map[string]any{
		"pane_id":     paneID,
		"source":      source,
		"agent":       kind,
		"seq":         n,
		"resume_argv": resumeArgv,
	}
	if logPath != "" {
		params["agent_session_path"] = logPath
	} else {
		params["agent_session_id"] = sid
	}
	_ = callHerdrSocket(sock, kind, n, "pane.report_agent_session", params)
}

func reportBridgeRelease(sock, paneID, source, kind string, seq *atomic.Int64) {
	if sock == "" || paneID == "" {
		return
	}
	n := seq.Add(1)
	_ = callHerdrSocket(sock, kind, n, "pane.release_agent", map[string]any{
		"pane_id": paneID,
		"source":  source,
		"agent":   kind,
		"seq":     n,
	})
}

func callHerdrSocket(sock, kind string, seq int64, method string, params map[string]any) error {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	var d net.Dialer
	conn, err := d.DialContext(ctx, "unix", sock)
	if err != nil {
		return err
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(3 * time.Second))
	id := kind + "-" + strconv.FormatInt(seq, 10)
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

// ---------------------------------------------------------------- process & shell helpers

func terminateChildProcess(cmd *exec.Cmd) {
	if cmd == nil || cmd.Process == nil {
		return
	}
	_ = cmd.Process.Signal(syscall.SIGTERM)
	done := make(chan struct{})
	go func() {
		_, _ = cmd.Process.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(1500 * time.Millisecond):
		_ = cmd.Process.Kill()
	}
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
