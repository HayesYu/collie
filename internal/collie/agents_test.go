package collie

import (
	"strings"
	"testing"
)

func TestAgentsHeader(t *testing.T) {
	if got := parseAgentsHeader(agentsHeader([]string{"claude", "agy"})); len(got) != 2 || got[0] != "claude" || got[1] != "agy" {
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
