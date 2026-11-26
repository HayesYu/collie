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
