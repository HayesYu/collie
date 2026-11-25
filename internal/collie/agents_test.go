package collie

import (
	"strings"
	"testing"
)

func TestAgentsHeader(t *testing.T) {
	if got := parseAgentsHeader(agentsHeader([]string{"claude", "agy", "doubao"})); len(got) != 3 || got[0] != "claude" || got[1] != "agy" || got[2] != "doubao" {
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
}

func TestAgentErrRe(t *testing.T) {
	for l, want := range map[string]bool{
		"● Please run /login · API Error: 403 LLMBox: quota exceeded: Daily Limits exceeded.": true,
		"⎿  API Error: 529 overloaded":         true,
		"■ unexpected status 401 Unauthorized": true,
		"Error: invalid api key":               true,
		"⚠ Credit balance is too low":          true,
		"○ Thought for 8.0s (The backup has failed twice, so I'm considering a different route)":        false,
		"purchase-ingest-run is waiting for the logs of the f2-vs-f4 comparison and the failed backup.": false,
		"purchase-ingest-run is pulling the comparison job's latest error output (python.stderr).":      false,
	} {
		if got := agentErrRe.MatchString(l); got != want {
			t.Errorf("%q: got %v, want %v", l, got, want)
		}
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
