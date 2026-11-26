package collie

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"sort"
	"strings"
	"time"
)

// Claude Code has no `models` subcommand, but its stream-json control
// protocol answers an `initialize` request with the same model list its own
// /model picker shows (aliases, pinned versions, per-model effort support) for
// the account and gateway it is actually signed in to. No prompt is sent, so
// asking costs no tokens.

// claudeModelProbe is swapped out by tests.
var claudeModelProbe = probeClaudeModels

var claudeProbeArgs = []string{
	"-p", "--input-format", "stream-json", "--output-format", "stream-json",
	"--verbose", "--no-session-persistence",
}

// claudeProbeCommand is how to start Claude Code here: inside the claude-sg
// container when a launcher is configured, else the local `claude`.
func (s *Server) claudeProbeCommand() []string {
	if l := claudeSGLauncher(); l != "" {
		if !isExecutable(l) {
			return nil
		}
		argv := []string{l, "run"}
		if p := claudeSGProject(); p != "" {
			argv = append(argv, "--project", p)
		}
		argv = append(append(argv, "--"), claudeProbeArgs...)
		return argv
	}
	bin := findBin("claude", s.home)
	if bin == "" {
		return nil
	}
	return append([]string{bin}, claudeProbeArgs...)
}

// claudeProbeModel is one entry of the initialize response's "models".
type claudeProbeModel struct {
	Value          string   `json:"value"`
	ResolvedModel  string   `json:"resolvedModel"`
	DisplayName    string   `json:"displayName"`
	Description    string   `json:"description"`
	SupportsEffort bool     `json:"supportsEffort"`
	EffortLevels   []string `json:"supportedEffortLevels"`
}

// claudeProbeEnv keeps the probe from posing as a herdr pane or as a child of
// whatever Claude session started collie (hooks would otherwise report it).
func claudeProbeEnv(base []string) []string {
	out := make([]string, 0, len(base))
	for _, kv := range base {
		k, _, _ := strings.Cut(kv, "=")
		switch {
		case k == "HERDR_PANE_ID", k == claudeSGDirEnv,
			k == "CLAUDE_CODE_CHILD_SESSION", k == "CLAUDE_CODE_SESSION_ID",
			k == "CLAUDE_CODE_MESSAGING_SOCKET", k == "CLAUDE_CODE_MESSAGING_TOKEN",
			k == "CLAUDECODE":
			continue
		}
		out = append(out, kv)
	}
	return out
}

// probeClaudeModels runs argv, sends one initialize control request and
// returns the models from its control_response.
func probeClaudeModels(ctx context.Context, argv []string) ([]claudeProbeModel, error) {
	if len(argv) == 0 {
		return nil, errors.New("claude not found")
	}
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	cmd.Env = claudeProbeEnv(os.Environ())
	cmd.WaitDelay = 3 * time.Second
	in, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	out, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	closed := false
	closeIn := func() {
		if !closed {
			closed = true
			in.Close()
		}
	}
	defer func() {
		// EOF on stdin ends `claude -p` (and the docker exec behind claude-sg)
		// cleanly; kill only if it lingers.
		closeIn()
		done := make(chan struct{})
		go func() { cmd.Wait(); close(done) }()
		select {
		case <-done:
		case <-time.After(3 * time.Second):
			cmd.Process.Kill()
			<-done
		}
	}()

	const reqID = "collie-models"
	req, _ := json.Marshal(map[string]any{
		"type":       "control_request",
		"request_id": reqID,
		"request":    map[string]any{"subtype": "initialize"},
	})
	if _, err := in.Write(append(req, '\n')); err != nil {
		return nil, err
	}
	sc := bufio.NewScanner(out)
	sc.Buffer(make([]byte, 0, 1<<20), 32<<20)
	for sc.Scan() {
		var msg struct {
			Type     string `json:"type"`
			Response struct {
				Subtype   string `json:"subtype"`
				RequestID string `json:"request_id"`
				Error     string `json:"error"`
				Response  struct {
					Models []claudeProbeModel `json:"models"`
				} `json:"response"`
			} `json:"response"`
		}
		if json.Unmarshal(sc.Bytes(), &msg) != nil || msg.Type != "control_response" || msg.Response.RequestID != reqID {
			continue
		}
		if msg.Response.Subtype == "error" {
			return nil, errors.New("claude initialize: " + msg.Response.Error)
		}
		if len(msg.Response.Response.Models) == 0 {
			return nil, errors.New("claude initialize returned no models")
		}
		return msg.Response.Response.Models, nil
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	return nil, errors.New("claude exited without answering initialize")
}

// applyClaudeProbe fills info from a probe result. The "default" entry is
// left out (the pickers offer their own 默认 choice); the alias it currently
// resolves to becomes info.Default unless settings already named one.
func applyClaudeProbe(info *ModelInfo, models []claudeProbeModel) {
	defResolved := ""
	for _, m := range models {
		if m.Value == "default" {
			defResolved = m.ResolvedModel
		}
	}
	seen := map[string]bool{}
	union := map[string]bool{}
	for _, m := range models {
		id := strings.TrimSpace(m.Value)
		if id == "" || id == "default" || seen[id] {
			continue
		}
		seen[id] = true
		var notes []string
		if m.ResolvedModel != "" && m.ResolvedModel != id {
			notes = append(notes, m.ResolvedModel)
		}
		if d := strings.TrimSpace(m.Description); d != "" {
			notes = append(notes, d)
		}
		o := ModelOpt{ID: id, Label: strings.TrimSpace(m.DisplayName), Note: strings.Join(notes, " · "), Efforts: []string{}}
		if m.SupportsEffort {
			for _, e := range m.EffortLevels {
				if e = strings.TrimSpace(e); e != "" {
					o.Efforts = append(o.Efforts, e)
					union[e] = true
				}
			}
		}
		info.Models = append(info.Models, o)
		if info.Default == "" && defResolved != "" && m.ResolvedModel == defResolved {
			info.Default = id
		}
	}
	if len(union) > 0 {
		info.Efforts = nil
		for _, e := range allEfforts {
			if union[e] {
				info.Efforts = append(info.Efforts, e)
			}
		}
		var extra []string
		for e := range union {
			if !contains(info.Efforts, e) {
				extra = append(extra, e)
			}
		}
		sort.Strings(extra)
		info.Efforts = append(info.Efforts, extra...)
	}
}
