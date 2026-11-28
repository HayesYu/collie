package collie

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"syscall"
	"time"
)

const (
	claudeSGBinEnv       = "COLLIE_CLAUDE_LAUNCHER"
	claudeSGProjectEnv   = "COLLIE_CLAUDE_PROJECT"
	claudeSGDirEnv       = "COLLIE_CLAUDE_SG_DIR"
	claudeSGBridgeSource = "collie:claude-sg"
	claudeSGContainerDir = "/mac-home/.local/state/claude-sg/collie"
)

func claudeSGLauncher() string {
	return strings.TrimSpace(os.Getenv(claudeSGBinEnv))
}

func claudeSGProject() string {
	return strings.TrimSpace(os.Getenv(claudeSGProjectEnv))
}

func isExecutable(p string) bool {
	if p == "" {
		return false
	}
	fi, err := os.Stat(p)
	return err == nil && !fi.IsDir() && fi.Mode()&0o111 != 0
}

func claudeSGStateDir(home string) string {
	if home == "" {
		home, _ = os.UserHomeDir()
	}
	return filepath.Join(home, ".local", "state", "claude-sg", "collie")
}

func isSafeSessionID(id string) bool {
	if id == "" {
		return false
	}
	for i := 0; i < len(id); i++ {
		c := id[i]
		ok := (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') || c == '.' || c == '_' || c == '-'
		if !ok {
			return false
		}
	}
	return id != "." && id != ".."
}

func safePaneFileName(paneID string) string {
	var b strings.Builder
	b.Grow(len(paneID))
	for i := 0; i < len(paneID); i++ {
		c := paneID[i]
		if (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') || c == '.' || c == '_' || c == '-' {
			b.WriteByte(c)
		} else {
			b.WriteByte('_')
		}
	}
	return b.String()
}

func claudeSGEventFile(stateDir, paneID string) string {
	if stateDir == "" || paneID == "" {
		return ""
	}
	return filepath.Join(stateDir, "events", safePaneFileName(paneID)+".json")
}

type claudeSGEvent struct {
	PaneID              string `json:"pane_id"`
	State               string `json:"state"`
	SessionID           string `json:"session_id"`
	ContainerTranscript string `json:"container_transcript"`
	Event               string `json:"event"`
	Message             string `json:"message"`
	TS                  int64  `json:"ts"`
}

func readClaudeSGEvent(path string) (claudeSGEvent, bool) {
	var ev claudeSGEvent
	if path == "" {
		return ev, false
	}
	b, err := os.ReadFile(path)
	if err != nil || json.Unmarshal(b, &ev) != nil {
		return claudeSGEvent{}, false
	}
	return ev, true
}

type claudeSGBridgeConfig struct {
	Name       string
	Project    string
	Workdir    string
	Launcher   string
	ClaudeArgs []string
}

func parseClaudeSGBridgeArgs(args []string) (claudeSGBridgeConfig, error) {
	var cfg claudeSGBridgeConfig
	for i := 0; i < len(args); i++ {
		a := args[i]
		if a == "--" {
			cfg.ClaudeArgs = append(cfg.ClaudeArgs, args[i+1:]...)
			break
		}
		next := func(flagName string) (string, error) {
			if i+1 >= len(args) {
				return "", fmt.Errorf("missing value for %s", flagName)
			}
			i++
			return args[i], nil
		}
		switch {
		case a == "--name":
			v, err := next("--name")
			if err != nil {
				return cfg, err
			}
			cfg.Name = v
		case strings.HasPrefix(a, "--name="):
			cfg.Name = strings.TrimPrefix(a, "--name=")
		case a == "--project":
			v, err := next("--project")
			if err != nil {
				return cfg, err
			}
			cfg.Project = v
		case strings.HasPrefix(a, "--project="):
			cfg.Project = strings.TrimPrefix(a, "--project=")
		case a == "--workdir":
			v, err := next("--workdir")
			if err != nil {
				return cfg, err
			}
			cfg.Workdir = v
		case strings.HasPrefix(a, "--workdir="):
			cfg.Workdir = strings.TrimPrefix(a, "--workdir=")
		case a == "--launcher":
			v, err := next("--launcher")
			if err != nil {
				return cfg, err
			}
			cfg.Launcher = v
		case strings.HasPrefix(a, "--launcher="):
			cfg.Launcher = strings.TrimPrefix(a, "--launcher=")
		default:
			cfg.ClaudeArgs = append(cfg.ClaudeArgs, a)
		}
	}
	return cfg, nil
}

func setEnvVars(base []string, overrides map[string]string) []string {
	out := make([]string, 0, len(base)+len(overrides))
	seen := make(map[string]bool, len(overrides))
	for _, kv := range base {
		k, _, ok := strings.Cut(kv, "=")
		if ok {
			if v, replace := overrides[k]; replace {
				if !seen[k] {
					seen[k] = true
					out = append(out, k+"="+v)
				}
				continue
			}
		}
		out = append(out, kv)
	}
	for k, v := range overrides {
		if !seen[k] {
			out = append(out, k+"="+v)
		}
	}
	return out
}

func runClaudeSGBridge(args []string) {
	cfg, err := parseClaudeSGBridgeArgs(args)
	if err != nil {
		fmt.Fprintf(os.Stderr, "[claude-sg-bridge] fatal: %v\n", err)
		os.Exit(2)
	}
	home, _ := os.UserHomeDir()
	if cfg.Project == "" {
		cfg.Project, _ = os.Getwd()
	}
	if cfg.Launcher == "" {
		cfg.Launcher = claudeSGLauncher()
	}
	if !isExecutable(cfg.Launcher) {
		fmt.Fprintf(os.Stderr, "[claude-sg-bridge] fatal: claude-sg launcher not executable: %q\n", cfg.Launcher)
		os.Exit(1)
	}

	stateDir := claudeSGStateDir(home)
	if err := os.MkdirAll(filepath.Join(stateDir, "events"), 0o700); err != nil {
		fmt.Fprintf(os.Stderr, "[claude-sg-bridge] fatal: mkdir events: %v\n", err)
		os.Exit(1)
	}
	if err := os.MkdirAll(filepath.Join(stateDir, "transcripts"), 0o700); err != nil {
		fmt.Fprintf(os.Stderr, "[claude-sg-bridge] fatal: mkdir transcripts: %v\n", err)
		os.Exit(1)
	}

	paneID := strings.TrimSpace(os.Getenv("HERDR_PANE_ID"))
	herdrSock := strings.TrimSpace(os.Getenv("HERDR_SOCKET_PATH"))
	if herdrSock == "" && home != "" {
		herdrSock = filepath.Join(home, ".config", "herdr", "herdr.sock")
	}
	evFile := claudeSGEventFile(stateDir, paneID)
	if evFile != "" {
		_ = os.Remove(evFile)
	}

	collieBin, _ := os.Executable()
	if r, err := filepath.EvalSymlinks(collieBin); err == nil && r != "" {
		collieBin = r
	}
	if collieBin == "" {
		collieBin = "collie"
	}

	cmdArgs := []string{"run", "--project", cfg.Project}
	if cfg.Workdir != "" {
		cmdArgs = append(cmdArgs, "--workdir", cfg.Workdir)
	}
	cmdArgs = append(cmdArgs, "--")
	cmdArgs = append(cmdArgs, cfg.ClaudeArgs...)
	cmd := exec.Command(cfg.Launcher, cmdArgs...)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	overrides := map[string]string{claudeSGDirEnv: claudeSGContainerDir}
	if paneID != "" {
		overrides["HERDR_PANE_ID"] = paneID
	}
	cmd.Env = setEnvVars(os.Environ(), overrides)

	// Let Ctrl-C reach the foreground docker/claude TUI instead of terminating the bridge.
	signal.Ignore(syscall.SIGINT)
	sigCh := make(chan os.Signal, 2)
	signal.Notify(sigCh, syscall.SIGTERM, syscall.SIGHUP)
	defer signal.Stop(sigCh)

	if err := cmd.Start(); err != nil {
		fmt.Fprintf(os.Stderr, "[claude-sg-bridge] fatal: start launcher: %v\n", err)
		os.Exit(1)
	}

	var seq atomic.Int64
	doneCh := make(chan struct{})
	go func() {
		select {
		case <-sigCh:
			if cmd.Process != nil {
				_ = cmd.Process.Signal(syscall.SIGTERM)
			}
			select {
			case <-doneCh:
			case <-time.After(2 * time.Second):
				if cmd.Process != nil {
					_ = cmd.Process.Kill()
				}
			}
		case <-doneCh:
		}
	}()

	watchDone := make(chan struct{})
	go func() {
		defer close(watchDone)
		watchClaudeSGEvents(doneCh, evFile, stateDir, herdrSock, paneID, collieBin, cfg, &seq)
	}()
	cliDone := make(chan struct{})
	go func() {
		defer close(cliDone)
		watchClaudeSGCLI(doneCh, claudeSGCLIDir(stateDir, paneID), collieBin, os.Environ())
	}()

	waitErr := cmd.Wait()
	close(doneCh)
	<-watchDone
	<-cliDone
	if evFile != "" {
		_ = os.Remove(evFile)
	}
	releaseClaudeSGAgent(herdrSock, paneID, &seq)

	if waitErr != nil {
		code := 1
		var ee *exec.ExitError
		if errors.As(waitErr, &ee) {
			code = ee.ExitCode()
		}
		if code != 0 {
			fmt.Fprintf(os.Stderr, "\n[claude-sg-bridge] fatal: launcher exited with code %d: %v\n", code, waitErr)
		}
		os.Exit(code)
	}
}

func watchClaudeSGEvents(done <-chan struct{}, evFile, stateDir, herdrSock, paneID, collieBin string, cfg claudeSGBridgeConfig, seq *atomic.Int64) {
	if evFile == "" || herdrSock == "" || paneID == "" {
		<-done
		return
	}
	tick := time.NewTicker(150 * time.Millisecond)
	defer tick.Stop()
	var lastTS int64
	var lastState, lastSid, lastLog string
	for {
		if ev, ok := readClaudeSGEvent(evFile); ok {
			lp := ""
			if isSafeSessionID(ev.SessionID) {
				cand := filepath.Join(stateDir, "transcripts", ev.SessionID+".jsonl")
				if isFile(cand) {
					lp = cand
				}
			}
			if ev.TS != lastTS || ev.State != lastState || ev.SessionID != lastSid || lp != lastLog {
				lastTS, lastState, lastSid, lastLog = ev.TS, ev.State, ev.SessionID, lp
				switch ev.State {
				case "exited":
					releaseClaudeSGAgent(herdrSock, paneID, seq)
				case "idle", "working", "blocked":
					reportClaudeSGAgent(herdrSock, paneID, collieBin, cfg, ev.State, ev.Message, ev.SessionID, lp, seq)
				}
			}
		}
		select {
		case <-done:
			return
		case <-tick.C:
		}
	}
}

func claudeSGResumeArgv(collieBin string, cfg claudeSGBridgeConfig, sessionID string) []string {
	argv := []string{collieBin, "_bridge", "claude-sg"}
	if cfg.Name != "" {
		argv = append(argv, "--name", cfg.Name)
	}
	if cfg.Project != "" {
		argv = append(argv, "--project", cfg.Project)
	}
	if cfg.Workdir != "" {
		argv = append(argv, "--workdir", cfg.Workdir)
	}
	if cfg.Launcher != "" {
		argv = append(argv, "--launcher", cfg.Launcher)
	}
	if sessionID != "" {
		argv = append(argv, "--", "--resume", sessionID)
	}
	return argv
}

func reportClaudeSGAgent(herdrSock, paneID, collieBin string, cfg claudeSGBridgeConfig, state, msg, sid, logPath string, seq *atomic.Int64) {
	if herdrSock == "" || paneID == "" {
		return
	}
	n := seq.Add(1)
	params := map[string]any{
		"pane_id": paneID,
		"source":  claudeSGBridgeSource,
		"agent":   "claude",
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
	if sid != "" {
		params["resume_argv"] = claudeSGResumeArgv(collieBin, cfg, sid)
	}
	if err := callHerdrSocket(herdrSock, "csg", n, "pane.report_agent", params); err != nil {
		n2 := seq.Add(1)
		_ = callHerdrSocket(herdrSock, "csg", n2, "pane.report_agent", map[string]any{
			"pane_id": paneID,
			"source":  claudeSGBridgeSource,
			"agent":   "claude",
			"state":   state,
			"seq":     n2,
		})
	}
}

func releaseClaudeSGAgent(herdrSock, paneID string, seq *atomic.Int64) {
	if herdrSock == "" || paneID == "" {
		return
	}
	n := seq.Add(1)
	_ = callHerdrSocket(herdrSock, "csg", n, "pane.release_agent", map[string]any{
		"pane_id": paneID,
		"source":  claudeSGBridgeSource,
		"agent":   "claude",
		"seq":     n,
	})
}

func (s *Server) startClaudeSGAgent(ctx context.Context, name, cwd, label string, args []string) (paneID, wsID string, err error) {
	wsID, paneID, err = s.herdr.CreateWorkspace(ctx, "", cwd, label)
	if err != nil {
		return "", "", err
	}
	if err = s.launchClaudeSGInPane(ctx, paneID, name, cwd, args); err != nil {
		return paneID, wsID, err
	}
	return paneID, wsID, nil
}

func splitAbsParts(p string) ([]string, bool) {
	c := filepath.Clean(p)
	if !strings.HasPrefix(c, "/") {
		return nil, false
	}
	trimmed := strings.TrimPrefix(c, "/")
	if trimmed == "" {
		return nil, true
	}
	return strings.Split(trimmed, "/"), true
}

func relUnderBaseOnce(base, target string, caseInsensitive bool) (string, bool) {
	bParts, bOK := splitAbsParts(base)
	tParts, tOK := splitAbsParts(target)
	if !bOK || !tOK || len(tParts) < len(bParts) {
		return "", false
	}
	for i := range bParts {
		if caseInsensitive {
			if !strings.EqualFold(bParts[i], tParts[i]) {
				return "", false
			}
		} else if bParts[i] != tParts[i] {
			return "", false
		}
	}
	if len(tParts) == len(bParts) {
		return "", true
	}
	return strings.Join(tParts[len(bParts):], "/"), true
}

func relUnderBase(base, target string, caseInsensitive bool) (string, bool) {
	if rel, ok := relUnderBaseOnce(base, target, caseInsensitive); ok {
		return rel, true
	}
	rb, errB := filepath.EvalSymlinks(base)
	rt, errT := filepath.EvalSymlinks(target)
	if errB == nil && errT == nil {
		return relUnderBaseOnce(rb, rt, caseInsensitive)
	}
	return "", false
}

func resolveClaudeSGTargetCase(pinnedProject, requestedCwd, home string, caseInsensitive bool) (containerProject, containerWorkdir string) {
	pinned := strings.TrimSpace(pinnedProject)
	if pinned == "" {
		return requestedCwd, ""
	}
	containerProject = filepath.Clean(pinned)
	req := strings.TrimSpace(requestedCwd)
	if req == "" {
		return containerProject, "/workspace"
	}
	if rel, ok := relUnderBase(containerProject, req, caseInsensitive); ok {
		if rel == "" {
			return containerProject, "/workspace"
		}
		return containerProject, "/workspace/" + rel
	}
	if h := strings.TrimSpace(home); h != "" {
		if rel, ok := relUnderBase(filepath.Clean(h), req, caseInsensitive); ok {
			if rel == "" {
				return containerProject, "/mac-home"
			}
			return containerProject, "/mac-home/" + rel
		}
	}
	return containerProject, "/workspace"
}

func resolveClaudeSGTarget(pinnedProject, requestedCwd, home string) (containerProject, containerWorkdir string) {
	return resolveClaudeSGTargetCase(pinnedProject, requestedCwd, home, runtime.GOOS == "darwin")
}

func (s *Server) launchClaudeSGInPane(ctx context.Context, paneID, name, cwd string, args []string) error {
	launcher := claudeSGLauncher()
	if !isExecutable(launcher) {
		return fmt.Errorf("claude-sg launcher not executable: %q", launcher)
	}
	if cwd == "" {
		cwd = s.home
	}
	projectDir, workdir := resolveClaudeSGTarget(claudeSGProject(), cwd, s.home)
	if projectDir == "" {
		projectDir = cwd
	}
	cmdArgs := []string{s.collieBinFor(""), "_bridge", "claude-sg", "--name", name, "--project", projectDir}
	if workdir != "" {
		cmdArgs = append(cmdArgs, "--workdir", workdir)
	}
	cmdArgs = append(cmdArgs, "--launcher", launcher)
	if len(args) > 0 {
		cmdArgs = append(cmdArgs, "--")
		cmdArgs = append(cmdArgs, args...)
	}
	quoted := make([]string, len(cmdArgs))
	for i, a := range cmdArgs {
		quoted[i] = shellQuote(a)
	}
	cmdLine := strings.Join(quoted, " ")

	time.Sleep(150 * time.Millisecond)
	sendErr := s.herdr.Call(ctx, "pane.send_input", map[string]any{
		"pane_id": paneID,
		"text":    cmdLine,
		"keys":    []string{"Enter"},
	}, nil)
	if sendErr != nil {
		if e1 := s.herdr.Call(ctx, "pane.send_text", map[string]any{"pane_id": paneID, "text": cmdLine}, nil); e1 != nil {
			return e1
		}
		if e2 := s.herdr.Call(ctx, "pane.send_keys", map[string]any{"pane_id": paneID, "keys": []string{"enter"}}, nil); e2 != nil {
			return e2
		}
	}

	deadline := time.Now().Add(90 * time.Second)
	for poll := 0; ; poll++ {
		var info struct {
			Agent struct {
				Agent  *string `json:"agent"`
				Status string  `json:"agent_status"`
			} `json:"agent"`
		}
		if callErr := s.herdr.Call(ctx, "agent.get", map[string]any{"target": paneID}, &info); callErr == nil &&
			info.Agent.Agent != nil && *info.Agent.Agent == "claude" {
			_ = s.herdr.Call(ctx, "agent.rename", map[string]any{"target": paneID, "name": name}, nil)
			s.markStarted(paneID)
			s.herdr.Remember(paneID, name)
			if info.Agent.Status == "blocked" {
				return &HerdrError{Code: "agent_not_ready", Message: "claude-sg agent blocked on startup"}
			}
			return nil
		}
		if poll > 0 && poll%6 == 0 {
			if rr, rerr := s.herdr.Read(ctx, paneID, "recent_unwrapped", "text", 25); rerr == nil {
				for _, l := range strings.Split(rr.Text, "\n") {
					if i := strings.Index(l, "[claude-sg-bridge] fatal:"); i >= 0 {
						return errors.New(strings.TrimSpace(l[i:]))
					}
					if i := strings.Index(l, "claude-sg:"); i >= 0 {
						return errors.New(strings.TrimSpace(l[i:]))
					}
				}
			}
		}
		if time.Now().After(deadline) || ctx.Err() != nil {
			if rr, rerr := s.herdr.Read(ctx, paneID, "recent_unwrapped", "text", 25); rerr == nil {
				if why := deathReason(rr.Text); why != "" {
					return errors.New(why)
				}
			}
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return errors.New("claude-sg bridge did not become ready in time")
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(250 * time.Millisecond):
		}
	}
}

type herdrRPCRequest struct {
	ID     string          `json:"id"`
	Method string          `json:"method"`
	Params json.RawMessage `json:"params"`
}

type herdrStartAgentParams struct {
	Name   string   `json:"name"`
	Kind   string   `json:"kind"`
	PaneID string   `json:"pane_id"`
	Args   []string `json:"args"`
}

func parseClaudeStartRPC(line []byte) (id string, params herdrStartAgentParams, intercept bool) {
	var req herdrRPCRequest
	if json.Unmarshal(line, &req) != nil || req.Method != "agent.start" {
		return "", params, false
	}
	if json.Unmarshal(req.Params, &params) != nil || params.Kind != "claude" || params.PaneID == "" {
		return "", params, false
	}
	return req.ID, params, true
}

func handleHerdrStream(ctx context.Context, st net.Conn, sock string, srv *Server, connected *atomic.Bool) {
	if srv == nil || claudeSGLauncher() == "" {
		pipeHerdrStream(ctx, st, sock, nil, nil, connected)
		return
	}
	br := bufio.NewReaderSize(st, 256*1024)
	line, err := br.ReadBytes('\n')
	if err != nil && len(line) == 0 {
		st.Close()
		return
	}
	if id, params, ok := parseClaudeStartRPC(line); ok {
		defer st.Close()
		reqCtx, cancel := context.WithTimeout(ctx, 95*time.Second)
		defer cancel()
		cwd := srv.home
		var paneResp struct {
			Pane *Pane `json:"pane"`
		}
		if srv.herdr.Call(reqCtx, "pane.get", map[string]any{"pane_id": params.PaneID}, &paneResp) == nil && paneResp.Pane != nil {
			if p := deref(paneResp.Pane.ForegroundCwd); p != "" {
				cwd = p
			} else if p := deref(paneResp.Pane.Cwd); p != "" {
				cwd = p
			}
		}
		var reply map[string]any
		if err := srv.launchClaudeSGInPane(reqCtx, params.PaneID, params.Name, cwd, params.Args); err != nil {
			code := "claude_sg_start_failed"
			var he *HerdrError
			if errors.As(err, &he) && he.Code != "" {
				code = he.Code
			}
			reply = map[string]any{
				"id": id,
				"error": map[string]any{
					"code":    code,
					"message": err.Error(),
				},
			}
		} else {
			reply = map[string]any{
				"id":     id,
				"result": map[string]any{"pane_id": params.PaneID},
			}
		}
		b, _ := json.Marshal(reply)
		_, _ = st.Write(append(b, '\n'))
		return
	}
	pipeHerdrStream(ctx, st, sock, line, br, connected)
}

func pipeHerdrStream(ctx context.Context, st net.Conn, sock string, firstLine []byte, br *bufio.Reader, connected *atomic.Bool) {
	dctx, dcancel := context.WithTimeout(ctx, 12*time.Second)
	h, err := dialHerdrWithState(dctx, sock, connected)
	dcancel()
	if err != nil {
		log.Printf("herdr: %v", err)
		st.Close()
		return
	}
	if len(firstLine) > 0 {
		if _, err := h.Write(firstLine); err != nil {
			h.Close()
			st.Close()
			return
		}
	}
	go func() {
		var r io.Reader = st
		if br != nil {
			r = br
		}
		_, _ = io.Copy(h, r)
		if uc, ok := h.(*net.UnixConn); ok {
			_ = uc.CloseWrite()
		}
	}()
	_, _ = io.Copy(st, h)
	h.Close()
	st.Close()
}
