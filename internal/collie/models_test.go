package collie

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"
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
		{
			name:   "doubao empty defaults to Auto + high",
			kind:   "doubao",
			model:  "",
			effort: "",
			want:   []string{"--model", "Auto", "--effort", "high"},
		},
		{
			name:   "doubao Auto ID without effort defaults effort to high",
			kind:   "doubao",
			model:  "9",
			effort: "",
			want:   []string{"--model", "9", "--effort", "high"},
		},
		{
			name:   "doubao empty model with explicit effort uses Auto + effort",
			kind:   "doubao",
			model:  "",
			effort: "low",
			want:   []string{"--model", "Auto", "--effort", "low"},
		},
		{
			name:   "doubao explicit model and effort",
			kind:   "doubao",
			model:  "5101",
			effort: "max",
			want:   []string{"--model", "5101", "--effort", "max"},
		},
		{
			name:   "doubao explicit non-default model omits empty effort",
			kind:   "doubao",
			model:  "5101",
			effort: "",
			want:   []string{"--model", "5101"},
		},
		{
			name:   "cursor strips effort and passes model",
			kind:   "cursor",
			model:  "composer-2.5",
			effort: "high",
			want:   []string{"--model", "composer-2.5"},
		},
		{
			name:   "cursor parameterized model preserved without effort",
			kind:   "cursor-agent",
			model:  "claude-opus-4-8[context=1m,effort=high,fast=false]",
			effort: "max",
			want:   []string{"--model", "claude-opus-4-8[context=1m,effort=high,fast=false]"},
		},
		{
			name:   "cursor empty model omits flags",
			kind:   "cursor",
			model:  "",
			effort: "high",
			want:   nil,
		},
		{
			name:   "pi passes model and effort",
			kind:   "pi",
			model:  "anthropic/claude-sonnet-4-5",
			effort: "high",
			want:   []string{"--model", "anthropic/claude-sonnet-4-5", "--effort", "high"},
		},
		{
			name:   "pi drops effort when model carries :thinking suffix",
			kind:   "pi",
			model:  "anthropic/claude-sonnet-4-5:medium",
			effort: "high",
			want:   []string{"--model", "anthropic/claude-sonnet-4-5:medium"},
		},
		{
			name:   "omp alias passes model and effort",
			kind:   "oh-my-pi",
			model:  "openai/gpt-5.4",
			effort: "auto",
			want:   []string{"--model", "openai/gpt-5.4", "--effort", "auto"},
		},
		{
			name:   "omp preserves effort for non-thinking :free tag",
			kind:   "omp",
			model:  "openrouter/demo:free",
			effort: "high",
			want:   []string{"--model", "openrouter/demo:free", "--effort", "high"},
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
	if got := bypassArgs("doubao"); !reflect.DeepEqual(got, []string{"-y"}) {
		t.Fatalf("bypassArgs(doubao) = %#v, want [-y]", got)
	}
	if got := bypassArgs("cursor"); !reflect.DeepEqual(got, []string{"--force"}) {
		t.Fatalf("bypassArgs(cursor) = %#v, want [--force]", got)
	}
	if got := bypassArgs("pi"); !reflect.DeepEqual(got, []string{"--approve"}) {
		t.Fatalf("bypassArgs(pi) = %#v, want [--approve]", got)
	}
	if got := bypassArgs("omp"); !reflect.DeepEqual(got, []string{"--auto-approve"}) {
		t.Fatalf("bypassArgs(omp) = %#v, want [--auto-approve]", got)
	}
	if got := bypassArgs("oh-my-pi"); !reflect.DeepEqual(got, []string{"--auto-approve"}) {
		t.Fatalf("bypassArgs(oh-my-pi) = %#v, want [--auto-approve]", got)
	}
	if got := effectiveEffort("cursor", "auto", "high"); got != "" {
		t.Fatalf("effectiveEffort(cursor) = %q, want empty", got)
	}
	if got := effectiveEffort("omp", "openai/gpt-5.4:high", "medium"); got != "" {
		t.Fatalf("effectiveEffort(omp suffixed) = %q, want empty", got)
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

func TestParseAndFetchDoubaoModels(t *testing.T) {
	sample := `{"type":"result","subtype":"success","is_error":false,"result":{"type":"model_list","models":[{"id":"9","name":"Auto","is_default":true,"efforts":["low","medium","high"],"default_effort":"high"},{"id":"5","name":"Doubao 2.1 Pro","is_default":false,"efforts":["low","medium","high"],"default_effort":"high"},{"id":"5101","name":"DeepSeek V4.1 Flash","is_default":false,"efforts":["low","high","max"],"default_effort":"high"}]}}`

	models, defID, defEffort, defEfforts, ok := parseDoubaoModels([]byte(sample))
	if !ok {
		t.Fatal("parseDoubaoModels failed on valid output")
	}
	if defID != "9" || defEffort != "high" {
		t.Fatalf("default = %q / %q, want 9 / high", defID, defEffort)
	}
	if !reflect.DeepEqual(defEfforts, []string{"low", "medium", "high"}) {
		t.Fatalf("defEfforts = %#v", defEfforts)
	}
	if len(models) != 3 || models[0].Label != "Auto" || !reflect.DeepEqual(models[2].Efforts, []string{"low", "high", "max"}) {
		t.Fatalf("models = %#v", models)
	}

	// Error envelope must be rejected.
	if _, _, _, _, ok := parseDoubaoModels([]byte(`{"type":"result","subtype":"error","is_error":true}`)); ok {
		t.Fatal("expected error envelope to fail")
	}

	// End-to-end doubaoModels with noisy stderr (must only parse stdout).
	dir := t.TempDir()
	fakeBin := filepath.Join(dir, "doubao")
	script := "#!/bin/sh\necho '正在拉取必要资源，请稍后...' >&2\necho '[cli-chat] {\"level\":\"info\"}' >&2\nprintf '%s\\n' '" + sample + "'\n"
	if err := os.WriteFile(fakeBin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	prev, hadPrev := binCache.Load("doubao")
	binCache.Store("doubao", fakeBin)
	defer func() {
		if hadPrev {
			binCache.Store("doubao", prev)
		} else {
			binCache.Delete("doubao")
		}
	}()

	s := &Server{home: dir}
	info := &ModelInfo{Kind: "doubao", Efforts: allEfforts}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	s.doubaoModels(ctx, info)
	if info.Default != "9" || info.DefaultEffort != "high" || len(info.Models) != 3 {
		t.Fatalf("doubaoModels = %+v", info)
	}
	if !reflect.DeepEqual(info.Efforts, []string{"low", "medium", "high"}) {
		t.Fatalf("info.Efforts = %#v, want [low medium high]", info.Efforts)
	}
}

func TestDesktopCodexFallback(t *testing.T) {
	home := t.TempDir()
	dir := filepath.Join(home, "Applications/ChatGPT.app/Contents/Resources/codex-cli/CodexCLI.app/Contents/MacOS")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	bundled := writeFakeCLIScript(t, dir, "codex", "exit 0")
	if runtime.GOOS != "darwin" {
		if got := desktopCodex(home); got != "" {
			t.Fatalf("unexpected macOS bundle on %s: %s", runtime.GOOS, got)
		}
		return
	}
	if got := desktopCodex(home); got != bundled {
		t.Fatalf("desktopCodex = %q, want %q", got, bundled)
	}
	pathDir := t.TempDir()
	t.Setenv("PATH", pathDir)
	t.Setenv("SHELL", "/usr/bin/false")
	old, ok := binCache.LoadAndDelete("codex")
	t.Cleanup(func() {
		binCache.Delete("codex")
		if ok {
			binCache.Store("codex", old)
		}
	})
	// Developer machines may already have a system-wide standalone installation.
	want := bundled
	for _, system := range []string{"/opt/homebrew/bin/codex", "/usr/local/bin/codex"} {
		if isFile(system) {
			want = system
			break
		}
	}
	if got := findBin("codex", home); got != want {
		t.Fatalf("findBin with launchd PATH = %q, want %q", got, want)
	}
	binCache.Delete("codex")
	standalone := writeFakeCLIScript(t, pathDir, "codex", "exit 0")
	if got := findBin("codex", home); got != standalone {
		t.Fatalf("findBin = %q, want standalone CLI %q", got, standalone)
	}
}

func TestParseAndFetchCursorModels(t *testing.T) {
	raw := "\x1b[1mAvailable models\x1b[0m\n\n" +
		"\x1b[32mauto\x1b[0m \x1b[2m- Auto (current, default)\x1b[0m\n" +
		"\x1b[36mcomposer-2.5\x1b[0m \x1b[2m- Composer 2.5\x1b[0m\n" +
		"\x1b[36mclaude-4.6-opus-max\x1b[0m \x1b[2m- Claude 4.6 Opus (Max) (current)\x1b[0m\n" +
		"- claude-opus-4-8[context=1m,effort=high,fast=false] - Claude Opus 4.8\n" +
		"gpt-5.4-high\n\n" +
		"\x1b[2mTip: use --model <id> (or /model <id> in interactive mode) to switch. Parameterized models also accept quoted overrides, e.g. --model 'claude-opus-4-8[context=1m,effort=high,fast=false]'.\x1b[0m\n"

	models, defID := parseCursorModels(raw)
	if defID != "auto" {
		t.Fatalf("defID = %q, want auto", defID)
	}
	if len(models) != 5 {
		t.Fatalf("got %d models: %+v", len(models), models)
	}
	if models[0].ID != "auto" || models[0].Label != "Auto" {
		t.Errorf("models[0] = %+v", models[0])
	}
	if models[2].ID != "claude-4.6-opus-max" || models[2].Label != "Claude 4.6 Opus (Max)" {
		t.Errorf("models[2] display parenthetical disturbed: %+v", models[2])
	}
	if models[3].ID != "claude-opus-4-8[context=1m,effort=high,fast=false]" || models[3].Label != "Claude Opus 4.8" {
		t.Errorf("models[3] parameterized mismatch: %+v", models[3])
	}
	if models[4].ID != "gpt-5.4-high" || models[4].Label != "" {
		t.Errorf("models[4] bare id mismatch: %+v", models[4])
	}
	for _, m := range models {
		if m.Efforts == nil || len(m.Efforts) != 0 {
			t.Fatalf("model %s Efforts = %#v, want explicit empty []string{}", m.ID, m.Efforts)
		}
	}

	// Empty account / error output must yield zero models.
	if ms, def := parseCursorModels("\x1b[33mNo models available for this account.\x1b[0m\n"); len(ms) != 0 || def != "" {
		t.Fatalf("expected empty on no models, got %+v / %q", ms, def)
	}
	if ms, def := parseCursorModels("\x1b[33m⚠ Warning: The provided API key is invalid.\x1b[0m\n"); len(ms) != 0 || def != "" {
		t.Fatalf("expected empty on warning, got %+v / %q", ms, def)
	}

	// End-to-end cursorModels with ANSI stderr noise and explicit empty Efforts JSON.
	dir := t.TempDir()
	fakeBin := filepath.Join(dir, "cursor-agent")
	script := "#!/bin/sh\n" +
		"if [ \"$1\" != \"--list-models\" ]; then exit 2; fi\n" +
		"printf '\\033[33m⚠ stderr notice\\033[0m\\n' >&2\n" +
		"printf 'Available models\\n\\n\\033[32mauto\\033[0m - Auto (default)\\ncomposer-2.5 - Composer 2.5\\n\\nTip: use --model <id>\\n'\n"
	if err := os.WriteFile(fakeBin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	prev, hadPrev := binCache.Load("cursor-agent")
	binCache.Store("cursor-agent", fakeBin)
	defer func() {
		if hadPrev {
			binCache.Store("cursor-agent", prev)
		} else {
			binCache.Delete("cursor-agent")
		}
	}()

	s := &Server{home: dir}
	info := &ModelInfo{Kind: "cursor", Efforts: allEfforts}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	s.cursorModels(ctx, info)
	if info.Default != "auto" || len(info.Models) != 2 {
		t.Fatalf("cursorModels = %+v", info)
	}
	if info.Efforts == nil || len(info.Efforts) != 0 {
		t.Fatalf("info.Efforts = %#v, want explicit empty []string{}", info.Efforts)
	}
	b, _ := json.Marshal(info)
	var wire struct {
		Efforts json.RawMessage `json:"efforts"`
	}
	_ = json.Unmarshal(b, &wire)
	if string(wire.Efforts) != "[]" {
		t.Fatalf("cursor ModelInfo JSON efforts = %s, want []", string(wire.Efforts))
	}
}

func TestParseAndFetchPiModels(t *testing.T) {
	raw := "provider   model              context  max-out  thinking  images\n" +
		"anthropic  claude-sonnet-4-5  200K     64K      yes       yes\n" +
		"deepseek   deepseek-chat      128K     8K       no        no\n"
	models := parsePiModels(raw)
	if len(models) != 2 {
		t.Fatalf("got %d models: %+v", len(models), models)
	}
	if models[0].ID != "anthropic/claude-sonnet-4-5" || models[0].Label != "claude-sonnet-4-5" || models[0].Note != "anthropic · 200K" {
		t.Fatalf("models[0] = %+v", models[0])
	}
	if !reflect.DeepEqual(models[0].Efforts, piEfforts) {
		t.Fatalf("models[0].Efforts = %#v, want %#v", models[0].Efforts, piEfforts)
	}
	if models[1].ID != "deepseek/deepseek-chat" || models[1].Efforts == nil || len(models[1].Efforts) != 0 {
		t.Fatalf("non-thinking model should have explicit empty efforts: %+v", models[1])
	}

	dir := t.TempDir()
	piDir := filepath.Join(dir, ".pi", "agent")
	if err := os.MkdirAll(piDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(piDir, "settings.json"), []byte(`{"defaultProvider":"anthropic","defaultModel":"claude-sonnet-4-5","defaultThinkingLevel":"high"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PI_CODING_AGENT_DIR", piDir)

	fakeBin := filepath.Join(dir, "pi")
	script := "#!/bin/sh\nif [ \"$1\" != \"--list-models\" ] || [ \"$2\" != \"--offline\" ]; then exit 2; fi\n" +
		"printf 'provider   model              context  max-out  thinking  images\\n" +
		"anthropic  claude-sonnet-4-5  200K     64K      yes       yes\\n" +
		"deepseek   deepseek-chat      128K     8K       no        no\\n'\n"
	if err := os.WriteFile(fakeBin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	prev, hadPrev := binCache.Load("pi")
	binCache.Store("pi", fakeBin)
	defer func() {
		if hadPrev {
			binCache.Store("pi", prev)
		} else {
			binCache.Delete("pi")
		}
	}()

	s := &Server{home: dir}
	info := &ModelInfo{Kind: "pi", Efforts: allEfforts}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	s.piModels(ctx, info)
	if info.Default != "anthropic/claude-sonnet-4-5" || info.DefaultEffort != "high" || len(info.Models) != 2 {
		t.Fatalf("piModels = %+v", info)
	}
	b, _ := json.Marshal(info.Models[1])
	if !strings.Contains(string(b), `"efforts":[]`) {
		t.Fatalf("non-thinking pi model JSON = %s, want efforts:[]", string(b))
	}
}

func TestParseAndFetchOmpModels(t *testing.T) {
	rawJSON := `{
		"models": [
			{"provider":"deepseek","id":"deepseek-flash","selector":"deepseek/deepseek-flash","name":"DeepSeek Flash","reasoning":false,"thinking":null},
			{"provider":"openai","id":"gpt-5.4","selector":"openai/gpt-5.4","name":"GPT-5.4","reasoning":true,"thinking":["medium","high","xhigh"]}
		]
	}`
	models := parseOmpModels([]byte(rawJSON))
	if len(models) != 2 {
		t.Fatalf("got %d models: %+v", len(models), models)
	}
	if models[0].ID != "deepseek/deepseek-flash" || models[0].Efforts == nil || len(models[0].Efforts) != 0 {
		t.Fatalf("non-reasoning omp model = %+v", models[0])
	}
	wantEff := []string{"off", "medium", "high", "xhigh", "auto"}
	if models[1].ID != "openai/gpt-5.4" || models[1].Label != "GPT-5.4" || !reflect.DeepEqual(models[1].Efforts, wantEff) {
		t.Fatalf("reasoning omp model = %+v, want efforts %#v", models[1], wantEff)
	}

	defM, defE := parseOmpConfig("theme:\n  dark: titanium\nmodelRoles:\n  default: openai/gpt-5.4:medium\n  smol: deepseek/deepseek-flash\ndefaultThinkingLevel: auto\n")
	if defM != "openai/gpt-5.4" || defE != "medium" {
		t.Fatalf("parseOmpConfig = %q / %q, want openai/gpt-5.4 / medium", defM, defE)
	}
	defM2, defE2 := parseOmpConfig("modelRoles:\n  default: openai/gpt-5.4\ndefaultThinkingLevel: auto\n")
	if defM2 != "openai/gpt-5.4" || defE2 != "auto" {
		t.Fatalf("parseOmpConfig fallback = %q / %q, want openai/gpt-5.4 / auto", defM2, defE2)
	}

	dir := t.TempDir()
	ompDir := filepath.Join(dir, ".omp", "agent")
	if err := os.MkdirAll(ompDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(ompDir, "config.yml"), []byte("modelRoles:\n  default: openai/gpt-5.4:high\ndefaultThinkingLevel: auto\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PI_CODING_AGENT_DIR", "")

	fakeBin := filepath.Join(dir, "omp")
	script := "#!/bin/sh\nif [ \"$1\" != \"models\" ] || [ \"$2\" != \"--json\" ]; then exit 2; fi\ncat <<'EOF'\n" + rawJSON + "\nEOF\n"
	if err := os.WriteFile(fakeBin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	prev, hadPrev := binCache.Load("omp")
	binCache.Store("omp", fakeBin)
	defer func() {
		if hadPrev {
			binCache.Store("omp", prev)
		} else {
			binCache.Delete("omp")
		}
	}()

	s := &Server{home: dir}
	info := &ModelInfo{Kind: "omp", Efforts: allEfforts}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	s.ompModels(ctx, info)
	if info.Default != "openai/gpt-5.4" || info.DefaultEffort != "high" || len(info.Models) != 2 {
		t.Fatalf("ompModels = %+v", info)
	}
	if !reflect.DeepEqual(info.Efforts, wantEff) {
		t.Fatalf("info.Efforts = %#v, want %#v", info.Efforts, wantEff)
	}
	b, _ := json.Marshal(info.Models[0])
	if !strings.Contains(string(b), `"efforts":[]`) {
		t.Fatalf("non-reasoning omp model JSON = %s, want efforts:[]", string(b))
	}
}
