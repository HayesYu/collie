package main

import (
	"os"
	"testing"
	"time"
)

func TestParseEtime(t *testing.T) {
	for in, want := range map[string]time.Duration{
		"05:07":      5*time.Minute + 7*time.Second,
		"01:06:25":   time.Hour + 6*time.Minute + 25*time.Second,
		"2-03:00:01": 51*time.Hour + time.Second,
	} {
		if got := parseEtime(in); got != want {
			t.Errorf("%s: %v, want %v", in, got, want)
		}
	}
}

func TestProgName(t *testing.T) {
	for want, args := range map[string][]string{
		"claude": {"/Users/x/.local/bin/claude", "--resume", "id"},
		"codex":  {"node", "/usr/lib/node_modules/@openai/codex/bin/codex.js"},
		"zsh":    {"-zsh"},
	} {
		if got := progName(args); got != want {
			t.Errorf("%v: %q, want %q", args, got, want)
		}
	}
}

// COLLIE_EXTERNAL=1 go test -run TestExternalLive -v prints what this machine finds.
func TestExternalLive(t *testing.T) {
	if os.Getenv("COLLIE_EXTERNAL") == "" {
		t.Skip("set COLLIE_EXTERNAL=1")
	}
	home, _ := os.UserHomeDir()
	s := &Server{home: home}
	for _, e := range s.externalSessions() {
		t.Logf("%s app=%q cwd=%s session=%s title=%q path=%s", e.ID, e.App, e.Cwd, e.Session, e.Title, e.path)
	}
}
