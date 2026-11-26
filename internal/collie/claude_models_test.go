package collie

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

const sampleClaudeInit = `{"type":"control_response","response":{"subtype":"success","request_id":"collie-models","response":{"models":[` +
	`{"value":"default","resolvedModel":"claude-opus-5-5","displayName":"Default (recommended)","description":"Opus 5.5 · Best for everyday","supportsEffort":true,"supportedEffortLevels":["low","medium","high","xhigh","max"]},` +
	`{"value":"opus","resolvedModel":"claude-opus-5-5","displayName":"Opus 5.5","description":"For complex work","supportsEffort":true,"supportedEffortLevels":["low","medium","high","xhigh","max"]},` +
	`{"value":"fable","resolvedModel":"claude-fable-5-2","displayName":"Fable 5.2","description":"For your toughest challenges","supportsEffort":true,"supportedEffortLevels":["low","medium","high","xhigh","max"]},` +
	`{"value":"claude-haiku-4-5-20251001","resolvedModel":"claude-haiku-4-5-20251001","displayName":"Haiku 4.5","description":"Fastest"}` +
	`]}}}`

func TestApplyClaudeProbe(t *testing.T) {
	models := []claudeProbeModel{
		{Value: "default", ResolvedModel: "claude-opus-5-5", DisplayName: "Default", SupportsEffort: true, EffortLevels: []string{"low", "high"}},
		{Value: "opus", ResolvedModel: "claude-opus-5-5", DisplayName: "Opus 5.5", Description: "For complex work", SupportsEffort: true, EffortLevels: []string{"low", "medium", "high", "xhigh", "max"}},
		{Value: "claude-haiku-4-5-20251001", ResolvedModel: "claude-haiku-4-5-20251001", DisplayName: "Haiku 4.5", Description: "Fastest"},
		{Value: "opus", DisplayName: "dup"},
	}
	info := &ModelInfo{Kind: "claude", Efforts: allEfforts}
	applyClaudeProbe(info, models)
	if len(info.Models) != 2 {
		t.Fatalf("models = %+v", info.Models)
	}
	if m := info.Models[0]; m.ID != "opus" || m.Label != "Opus 5.5" || m.Note != "claude-opus-5-5 · For complex work" || len(m.Efforts) != 5 {
		t.Fatalf("opus = %+v", m)
	}
	if m := info.Models[1]; m.Efforts == nil || len(m.Efforts) != 0 || m.Note != "Fastest" {
		t.Fatalf("haiku 4.5 should explicitly reject effort: %+v", m)
	}
	if info.Default != "opus" {
		t.Fatalf("default = %q, want opus", info.Default)
	}
	if !reflect.DeepEqual(info.Efforts, allEfforts) {
		t.Fatalf("efforts = %v", info.Efforts)
	}
}

func TestProbeClaudeModelsThroughClaudeSGLauncher(t *testing.T) {
	dir := t.TempDir()
	argsFile := filepath.Join(dir, "args")
	envFile := filepath.Join(dir, "env")
	initFile := filepath.Join(dir, "init.json")
	if err := os.WriteFile(initFile, []byte(sampleClaudeInit+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	launcher := filepath.Join(dir, "claude-sg")
	script := "#!/bin/sh\n" +
		"echo \"$@\" > " + argsFile + "\n" +
		"env > " + envFile + "\n" +
		"read line\n" +
		"case \"$line\" in *initialize*) ;; *) exit 3;; esac\n" +
		"echo '{\"type\":\"system\",\"subtype\":\"hook_started\"}'\n" +
		"cat " + initFile + "\n" +
		"cat >/dev/null\n"
	if err := os.WriteFile(launcher, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv(claudeSGBinEnv, launcher)
	t.Setenv(claudeSGProjectEnv, "/Users/me/Code")
	t.Setenv("HERDR_PANE_ID", "w1:p1")
	t.Setenv("CLAUDE_CODE_SESSION_ID", "outer")

	srv := &Server{home: dir}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	info := &ModelInfo{Kind: "claude", Efforts: allEfforts}
	if !srv.claudeModels(ctx, info) {
		t.Fatalf("probe failed, got fallback %+v", info.Models)
	}
	var ids []string
	for _, m := range info.Models {
		ids = append(ids, m.ID)
	}
	if !reflect.DeepEqual(ids, []string{"opus", "fable", "claude-haiku-4-5-20251001"}) || info.Default != "opus" {
		t.Fatalf("ids = %v default = %q", ids, info.Default)
	}
	args, _ := os.ReadFile(argsFile)
	if got := strings.TrimSpace(string(args)); got != "run --project /Users/me/Code -- "+strings.Join(claudeProbeArgs, " ") {
		t.Fatalf("launcher args = %q", got)
	}
	env, _ := os.ReadFile(envFile)
	for _, k := range []string{"HERDR_PANE_ID=", "CLAUDE_CODE_SESSION_ID="} {
		if strings.Contains(string(env), "\n"+k) || strings.HasPrefix(string(env), k) {
			t.Fatalf("probe leaked %s", k)
		}
	}
}

func TestProbeClaudeModelsFailsWhenNoAnswer(t *testing.T) {
	dir := t.TempDir()
	bin := filepath.Join(dir, "claude")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\necho 'not json'\nexit 1\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if _, err := probeClaudeModels(ctx, []string{bin}); err == nil {
		t.Fatal("want error")
	}
}
