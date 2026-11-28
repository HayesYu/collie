package collie

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestAgentsHeader(t *testing.T) {
	if got := parseAgentsHeader(agentsHeader([]string{"claude", "agy", "doubao", "cursor", "pi", "omp"})); len(got) != 6 || got[0] != "claude" || got[1] != "agy" || got[2] != "doubao" || got[3] != "cursor" || got[4] != "pi" || got[5] != "omp" {
		t.Fatalf("round trip: %v", got)
	}
	if got := parseAgentsHeader(agentsHeader(nil)); got == nil || len(got) != 0 {
		t.Fatalf("none should be known and empty: %#v", got)
	}
	if parseAgentsHeader("") != nil {
		t.Fatal("an older node's missing header should be unknown")
	}
	if got := parseAgentsHeader("claude, rm -rf ,codex"); len(got) != 2 {
		t.Fatalf("unknown names should be dropped: %v", got)
	}
}

func TestAgentMissingRemote(t *testing.T) {
	h := &Herdr{local: &Machine{Local: true}, remote: map[string]*Machine{
		"old": {ID: "old", Name: "old", Online: true},
		"mac": {ID: "mac", Name: "mac", Online: true, Agents: []string{"claude"}},
	}}
	s := &Server{herdr: h}
	if err := s.agentMissing("old", "codex"); err != nil {
		t.Fatalf("unknown list should let it try: %v", err)
	}
	if err := s.agentMissing("mac", "claude"); err != nil {
		t.Fatalf("installed: %v", err)
	}
	if err := s.agentMissing("mac", "codex"); err == nil || !strings.Contains(err.Error(), "mac 上没装 Codex") {
		t.Fatalf("missing: %v", err)
	}
	if err := s.agentMissing("mac", "cursor"); err == nil || !strings.Contains(err.Error(), "找不到 cursor-agent 命令") {
		t.Fatalf("missing cursor: %v", err)
	}
	if err := s.agentMissing("mac", "pi"); err == nil || !strings.Contains(err.Error(), "mac 上没装 Pi（找不到 pi 命令）") {
		t.Fatalf("missing pi: %v", err)
	}
	if err := s.agentMissing("mac", "omp"); err == nil || !strings.Contains(err.Error(), "mac 上没装 Oh My Pi（找不到 omp 命令）") {
		t.Fatalf("missing omp: %v", err)
	}
}

func TestCursorAgentBinMapping(t *testing.T) {
	if got := binForKind("cursor"); got != "cursor-agent" {
		t.Fatalf("binForKind(cursor) = %q, want cursor-agent", got)
	}
	if got := binForKind("claude"); got != "claude" {
		t.Fatalf("binForKind(claude) = %q, want claude", got)
	}

	// Ensure a generic `agent` binary does not falsely detect Cursor, while
	// `cursor-agent` in ~/.local/bin does.
	home := t.TempDir()
	localBin := filepath.Join(home, ".local", "bin")
	if err := os.MkdirAll(localBin, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(localBin, "agent"), []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	prev, hadPrev := binCache.Load("cursor-agent")
	binCache.Delete("cursor-agent")
	defer func() {
		if hadPrev {
			binCache.Store("cursor-agent", prev)
		} else {
			binCache.Delete("cursor-agent")
		}
	}()

	t.Setenv("PATH", localBin)
	if got := findBin("cursor-agent", home); got != "" {
		t.Fatalf("generic agent must not satisfy cursor-agent lookup, got %q", got)
	}

	cursorBin := filepath.Join(localBin, "cursor-agent")
	if err := os.WriteFile(cursorBin, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	binCache.Delete("cursor-agent")
	if got := detectAgents(home); !slices.Contains(got, "cursor") {
		t.Fatalf("detectAgents with cursor-agent = %v, want cursor included", got)
	}
}

func TestDetectAgentsPiAndOmp(t *testing.T) {
	if got := binForKind("pi"); got != "pi" {
		t.Fatalf("binForKind(pi) = %q, want pi", got)
	}
	if got := binForKind("omp"); got != "omp" {
		t.Fatalf("binForKind(omp) = %q, want omp", got)
	}
	home := t.TempDir()
	localBin := filepath.Join(home, ".local", "bin")
	if err := os.MkdirAll(localBin, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"pi", "omp"} {
		if err := os.WriteFile(filepath.Join(localBin, name), []byte("#!/bin/sh\n"), 0o755); err != nil {
			t.Fatal(err)
		}
		prev, hadPrev := binCache.Load(name)
		binCache.Delete(name)
		n := name
		t.Cleanup(func() {
			if hadPrev {
				binCache.Store(n, prev)
			} else {
				binCache.Delete(n)
			}
		})
	}
	t.Setenv("PATH", localBin)
	got := detectAgents(home)
	if !slices.Contains(got, "pi") || !slices.Contains(got, "omp") {
		t.Fatalf("detectAgents = %v, want pi and omp included", got)
	}
}

func TestAgentErrRe(t *testing.T) {
	for l, want := range map[string]bool{
		// Fatal API / auth / quota / prefix errors -> true
		"● Please run /login · API Error: 403 LLMBox: quota exceeded: Daily Limits exceeded.": true,
		"⎿  API Error: 529 overloaded":         true,
		"■ unexpected status 401 Unauthorized": true,
		"■ stream disconnected unexpectedly":   true,
		"Error: invalid api key":               true,
		"error: connection refused":            true,
		"Rate limit exceeded, try again later": true,
		"unexpected status 502 Bad Gateway":    true,
		"⚠ Credit balance is too low":          true,
		"⚠ Error: upstream request failed":     true,
		"⚠ Connection timed out":               true,
		"⚠ Warning: API Error: 529 overloaded": true,

		// Non-fatal CLI warnings (including model fallback) -> false
		`⚠ Warning`: false,
		`⚠ Warning └ "gemini-4-neon-xhigh" is no longer available. Using "Gemini 4 Neon (High)"`:         false,
		`⚠ Warning: "gemini-4-neon-xhigh" is no longer available. Using "Gemini 4 Neon (High)" instead.`: false,
		`  │ ⚠ warning: deprecated flag ignored`:                                                         false,
		`⚠ Warn: a newer CLI version is available`:                                                       false,
		`⚠ [Warning] experimental feature enabled`:                                                       false,
		`⚠ 警告：当前模型已自动回退`:                                                                                 false,
		`⚠ "gemini-4-neon-xhigh" is no longer available. Using "Gemini 4 Neon (High)"`:                   false,
		`└ "gemini-4-neon-xhigh" is no longer available. Using "Gemini 4 Neon (High)"`:                   false,

		// Normal agent reasoning / task text -> false
		"○ Thought for 8.0s (The backup has failed twice, so I'm considering a different route)":        false,
		"purchase-ingest-run is waiting for the logs of the f2-vs-f4 comparison and the failed backup.": false,
		"purchase-ingest-run is pulling the comparison job's latest error output (python.stderr).":      false,
	} {
		if got := isAgentErrLine(l); got != want {
			t.Errorf("isAgentErrLine(%q): got %v, want %v", l, got, want)
		}
	}
}

func TestRouterScreenError(t *testing.T) {
	cases := []struct {
		name   string
		screen string
		want   string
	}{
		{
			name: "model fallback warning block is ignored on silent turn",
			screen: strings.Join([]string{
				"reply with collie reply if needed.",
				"⚠ Warning",
				`└ "gemini-4-neon-xhigh" is no longer available. Using "Gemini 4 Neon (High)" instead.`,
				"",
				"❯ ",
			}, "\n"),
			want: "",
		},
		{
			name: "warning block with error-prefixed continuation line is ignored",
			screen: strings.Join([]string{
				"reply with collie reply if needed.",
				"⚠ Warning",
				"└ Error loading optional plugin config; continuing with defaults.",
				"",
				"❯ ",
			}, "\n"),
			want: "",
		},
		{
			name: "real API error after non-fatal warning is still caught",
			screen: strings.Join([]string{
				"reply with collie reply if needed.",
				"⚠ Warning",
				`└ "gemini-4-neon-xhigh" is no longer available. Using "Gemini 4 Neon (High)" instead.`,
				"",
				"⎿  API Error: 529 overloaded",
				"❯ ",
			}, "\n"),
			want: "⎿  API Error: 529 overloaded ❯",
		},
		{
			name: "real warning-symbol error is still caught",
			screen: strings.Join([]string{
				"reply with collie reply if needed.",
				"⚠ Credit balance is too low",
				"Please top up your account.",
				"",
			}, "\n"),
			want: "⚠ Credit balance is too low Please top up your account.",
		},
		{
			name: "fatal API error inside warning continuation is still caught",
			screen: strings.Join([]string{
				"reply with collie reply if needed.",
				"⚠ Warning",
				"└ unexpected status 429 Too Many Requests",
				"",
			}, "\n"),
			want: "└ unexpected status 429 Too Many Requests",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := routerScreenError(tc.screen); got != tc.want {
				t.Errorf("routerScreenError() = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestHasBackground(t *testing.T) {
	for screen, want := range map[string]bool{
		" How's the CLI experience so far?\n [1] Good  [2] Fine\n? for shortcuts      auto · Gemini 4 Neon · xhigh · 1 task(s) · /tasks\n": true,
		"❯ \n  ⏵⏵ bypass permissions on · 1 background task · ↓ to manage":                                                                 true,
		"❯ \n  ⏵⏵ bypass permissions on (shift+tab to cycle)":                                                                              false,
		"● Created 3 tasks for the release\n\n❯ \n  ? for shortcuts":                                                                       false,
		"? for shortcuts      auto · Gemini 4 Neon · xhigh":                                                                                false,
	} {
		if got := hasBackground(screen); got != want {
			t.Errorf("%q: got %v, want %v", screen, got, want)
		}
	}
}
