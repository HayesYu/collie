package collie

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestModelArgs(t *testing.T) {
	cases := []struct {
		name   string
		kind   string
		model  string
		effort string
		want   []string
	}{
		{
			name:   "agy suffixed model drops conflicting effort",
			kind:   "agy",
			model:  "gemini-4-neon-medium",
			effort: "xhigh",
			want:   []string{"--model", "gemini-4-neon-medium"},
		},
		{
			name:   "antigravity alias suffixed model drops effort",
			kind:   "antigravity",
			model:  "claude-opus-5-5-high",
			effort: "low",
			want:   []string{"--model", "claude-opus-5-5-high"},
		},
		{
			name:   "agy all effort suffixes strip --effort",
			kind:   "agy",
			model:  "gemini-4-neon-max",
			effort: "high",
			want:   []string{"--model", "gemini-4-neon-max"},
		},
		{
			name:   "agy xhigh suffix strips --effort case-insensitively",
			kind:   "agy",
			model:  "gemini-4-hydrogen-XHigh",
			effort: "medium",
			want:   []string{"--model", "gemini-4-hydrogen-XHigh"},
		},
		{
			name:   "agy orchestrator drops unsupported effort",
			kind:   "agy",
			model:  "gemini-pro-orchestrator",
			effort: "high",
			want:   []string{"--model", "gemini-pro-orchestrator"},
		},
		{
			name:   "agy orchestrator variant drops unsupported effort",
			kind:   "antigravity",
			model:  "gemini-pro-orchestrator-neon",
			effort: "medium",
			want:   []string{"--model", "gemini-pro-orchestrator-neon"},
		},
		{
			name:   "agy default model keeps effort",
			kind:   "agy",
			model:  "",
			effort: "low",
			want:   []string{"--effort", "low"},
		},
		{
			name:   "agy base family model without suffix keeps effort",
			kind:   "agy",
			model:  "gemini-4-neon",
			effort: "high",
			want:   []string{"--model", "gemini-4-neon", "--effort", "high"},
		},
		{
			name:   "claude keeps model and effort",
			kind:   "claude",
			model:  "sonnet",
			effort: "high",
			want:   []string{"--model", "sonnet", "--effort", "high"},
		},
		{
			name:   "claude model ending with -high still keeps effort",
			kind:   "claude",
			model:  "custom-high",
			effort: "medium",
			want:   []string{"--model", "custom-high", "--effort", "medium"},
		},
		{
			name:   "codex keeps model and effort",
			kind:   "codex",
			model:  "gpt-6.2-sol",
			effort: "xhigh",
			want:   []string{"-m", "gpt-6.2-sol", "-c", `model_reasoning_effort="xhigh"`},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := modelArgs(tc.kind, tc.model, tc.effort)
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("modelArgs(%q, %q, %q) = %#v, want %#v", tc.kind, tc.model, tc.effort, got, tc.want)
			}
		})
	}
}

func TestParseAndApplyAgyModels(t *testing.T) {
	raw := `
gemini-4-neon-high	Gemini 4 Neon (High)
gemini-4-neon-xhigh	Gemini 4 Neon (XHigh)
gemini-4-neon-medium	Gemini 4 Neon (Medium)
gemini-4-neon-low	Gemini 4 Neon (Low)
gemini-4-carbon-medium	Gemini 4 Carbon (Medium)
gemini-4-carbon-high	Gemini 4 Carbon (High)
gemini-4-carbon-low	Gemini 4 Carbon (Low)
gemini-pro-orchestrator-neon	Orchestrator (Neon)
gemini-pro-orchestrator	Orchestrator (Carbon)
gpt-oss-120b-medium	GPT-OSS 120B (Medium)
`
	t.Run("default configured to carbon high", func(t *testing.T) {
		info := &ModelInfo{Kind: "agy", Models: parseAgyModels(raw)}
		applyAgyModelCapabilities(info, "Gemini 4 Carbon (High)")

		if len(info.Models) != 10 {
			t.Fatalf("parsed %d models, want 10", len(info.Models))
		}
		if info.Default != "gemini-4-carbon-high" {
			t.Fatalf("default = %q, want gemini-4-carbon-high", info.Default)
		}
		if info.DefaultEffort != "high" {
			t.Fatalf("default_effort = %q, want high", info.DefaultEffort)
		}
		wantDefEffs := []string{"low", "medium", "high"}
		if !reflect.DeepEqual(info.Efforts, wantDefEffs) {
			t.Fatalf("info.Efforts = %#v, want %#v", info.Efforts, wantDefEffs)
		}
		for _, m := range info.Models {
			if m.Efforts == nil || len(m.Efforts) != 0 {
				t.Fatalf("model %s: Efforts = %#v, want explicit empty []string{}", m.ID, m.Efforts)
			}
		}

		// Verify JSON preserves empty arrays ("efforts": []) rather than omitting them.
		b, err := json.Marshal(info)
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		var wire struct {
			Efforts []string `json:"efforts"`
			Models  []struct {
				ID      string          `json:"id"`
				Efforts json.RawMessage `json:"efforts"`
			} `json:"models"`
		}
		if err := json.Unmarshal(b, &wire); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		for _, m := range wire.Models {
			if string(m.Efforts) != "[]" {
				t.Fatalf("model %s JSON efforts = %s, want []", m.ID, string(m.Efforts))
			}
		}
	})

	t.Run("default configured to orchestrator has no efforts", func(t *testing.T) {
		info := &ModelInfo{Kind: "agy", Models: parseAgyModels(raw)}
		applyAgyModelCapabilities(info, "Orchestrator (Carbon)")

		if info.Default != "gemini-pro-orchestrator" {
			t.Fatalf("default = %q, want gemini-pro-orchestrator", info.Default)
		}
		if info.DefaultEffort != "" {
			t.Fatalf("default_effort = %q, want empty", info.DefaultEffort)
		}
		if info.Efforts == nil || len(info.Efforts) != 0 {
			t.Fatalf("info.Efforts = %#v, want empty []string{}", info.Efforts)
		}
	})

	t.Run("no default setting yields empty kind efforts", func(t *testing.T) {
		info := &ModelInfo{Kind: "agy", Models: parseAgyModels(raw)}
		applyAgyModelCapabilities(info, "")

		if info.Default != "" || info.DefaultEffort != "" {
			t.Fatalf("unexpected default %q / %q", info.Default, info.DefaultEffort)
		}
		if info.Efforts == nil || len(info.Efforts) != 0 {
			t.Fatalf("info.Efforts = %#v, want empty []string{}", info.Efforts)
		}
	})
}

func TestModelOptEffortsJSONDistinguishesInheritAndNone(t *testing.T) {
	opts := []ModelOpt{
		{ID: "opus", Label: "Opus"}, // nil -> inherit
		{ID: "gemini-4-neon-medium", Label: "Neon (Med)", Efforts: []string{}}, // [] -> none
		{ID: "sonnet", Efforts: []string{"low", "medium", "high"}},
	}
	b, err := json.Marshal(opts)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var wire []struct {
		ID      string          `json:"id"`
		Efforts json.RawMessage `json:"efforts"`
	}
	if err := json.Unmarshal(b, &wire); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if string(wire[0].Efforts) != "null" {
		t.Fatalf("nil Efforts serialized as %s, want null", string(wire[0].Efforts))
	}
	if string(wire[1].Efforts) != "[]" {
		t.Fatalf("empty Efforts serialized as %s, want []", string(wire[1].Efforts))
	}
	if string(wire[2].Efforts) != `["low","medium","high"]` {
		t.Fatalf("populated Efforts serialized as %s", string(wire[2].Efforts))
	}
}

func TestClaudeModelsPreservesInheritAndExplicitEmptyEfforts(t *testing.T) {
	home := t.TempDir()
	claudeDir := filepath.Join(home, ".claude")
	if err := os.MkdirAll(claudeDir, 0o755); err != nil {
		t.Fatal(err)
	}
	settings := `{"model":"sonnet","env":{"CLAUDE_CODE_MODEL_CAPABILITIES":"gw/full=effort,xhigh_effort;gw/noeff=;gw/bare"}}`
	if err := os.WriteFile(filepath.Join(claudeDir, "settings.json"), []byte(settings), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CLAUDE_CONFIG_DIR", claudeDir)
	t.Setenv("CLAUDE_CODE_MODEL_CAPABILITIES", "")

	s := &Server{home: home}
	info := &ModelInfo{Kind: "claude", Efforts: allEfforts}
	s.claudeModels(info)

	byID := map[string]ModelOpt{}
	for _, m := range info.Models {
		byID[m.ID] = m
	}
	if byID["opus"].Efforts != nil {
		t.Fatalf("alias opus should inherit (nil), got %#v", byID["opus"].Efforts)
	}
	if !reflect.DeepEqual(byID["gw/full"].Efforts, []string{"low", "medium", "high", "xhigh"}) {
		t.Fatalf("gw/full efforts = %#v", byID["gw/full"].Efforts)
	}
	if byID["gw/noeff"].Efforts == nil || len(byID["gw/noeff"].Efforts) != 0 {
		t.Fatalf("gw/noeff should be explicit empty [], got %#v", byID["gw/noeff"].Efforts)
	}
	if byID["gw/bare"].Efforts != nil {
		t.Fatalf("gw/bare without '=' should inherit (nil), got %#v", byID["gw/bare"].Efforts)
	}
}
