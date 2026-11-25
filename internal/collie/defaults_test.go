package collie

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func writeFakeAgy(t *testing.T, script string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "agy")
	if err := os.WriteFile(p, []byte("#!/bin/sh\n"+script+"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestResolveDefaultSettings(t *testing.T) {
	const home = "/tmp/collie-home-test"

	t.Run("agy present with both target models", func(t *testing.T) {
		lookup := func(name, _ string) string {
			if name == "agy" {
				return "/usr/local/bin/agy"
			}
			return "/usr/local/bin/" + name
		}
		probe := func(ctx context.Context, bin string) ([]ModelOpt, error) {
			if bin != "/usr/local/bin/agy" {
				t.Fatalf("unexpected bin %q", bin)
			}
			return []ModelOpt{
				{ID: "gemini-4-neon-medium", Label: "Gemini 4 Neon (Medium)"},
				{ID: "gemini-4-neon-xhigh", Label: "Gemini 4 Neon (XHigh)"},
				{ID: "gemini-4-carbon-high", Label: "Gemini 4 Carbon (High)"},
			}, nil
		}

		got := resolveDefaultSettings(home, lookup, probe, time.Second)
		if got.RouterKind != "antigravity" || got.WorkerKind != "antigravity" {
			t.Fatalf("kinds = %q / %q, want antigravity / antigravity", got.RouterKind, got.WorkerKind)
		}
		if got.RouterModel != "gemini-4-neon-medium" || got.WorkerModel != "gemini-4-neon-xhigh" {
			t.Fatalf("models = %q / %q, want gemini-4-neon-medium / gemini-4-neon-xhigh", got.RouterModel, got.WorkerModel)
		}
		if got.RouterEffort != "" || got.WorkerEffort != "" {
			t.Fatalf("efforts = %q / %q, want empty", got.RouterEffort, got.WorkerEffort)
		}
		if got.WorkerCwd != home || got.NotifyLevel != "quiet" || got.StallMinutes != 12 || got.ReplyChars != 200 {
			t.Fatalf("baseline settings altered: %+v", got)
		}
	})

	t.Run("agy present via real CLI script with both target models", func(t *testing.T) {
		bin := writeFakeAgy(t, `printf 'gemini-4-neon-medium\tGemini 4 Neon (Medium)\ngemini-4-neon-xhigh\tGemini 4 Neon (XHigh)\n'`)
		lookup := func(name, _ string) string {
			if name == "agy" {
				return bin
			}
			return ""
		}
		got := resolveDefaultSettings(home, lookup, fetchAgyModels, 2*time.Second)
		if got.RouterKind != "antigravity" || got.WorkerKind != "antigravity" ||
			got.RouterModel != "gemini-4-neon-medium" || got.WorkerModel != "gemini-4-neon-xhigh" ||
			got.RouterEffort != "" || got.WorkerEffort != "" {
			t.Fatalf("got = %+v", got)
		}
	})

	t.Run("agy present with only one target model", func(t *testing.T) {
		lookup := func(name, _ string) string {
			if name == "agy" {
				return "/usr/local/bin/agy"
			}
			return ""
		}
		got := resolveDefaultSettings(home, lookup, func(context.Context, string) ([]ModelOpt, error) {
			return []ModelOpt{{ID: "gemini-4-neon-medium"}}, nil
		}, time.Second)
		if got.RouterModel != "gemini-4-neon-medium" || got.WorkerModel != "" {
			t.Fatalf("partial match models = %q / %q", got.RouterModel, got.WorkerModel)
		}
	})

	t.Run("agy present without target models falls back to empty model", func(t *testing.T) {
		lookup := func(name, _ string) string {
			if name == "agy" {
				return "/usr/local/bin/agy"
			}
			return ""
		}
		got := resolveDefaultSettings(home, lookup, func(context.Context, string) ([]ModelOpt, error) {
			return []ModelOpt{
				{ID: "gemini-4-carbon-medium"},
				{ID: "claude-opus-5-5-high"},
			}, nil
		}, time.Second)
		if got.RouterKind != "antigravity" || got.WorkerKind != "antigravity" {
			t.Fatalf("kinds = %q / %q, want antigravity", got.RouterKind, got.WorkerKind)
		}
		if got.RouterModel != "" || got.WorkerModel != "" || got.RouterEffort != "" || got.WorkerEffort != "" {
			t.Fatalf("expected empty models/efforts, got %+v", got)
		}
	})

	t.Run("agy present but models command errors", func(t *testing.T) {
		lookup := func(name, _ string) string {
			if name == "agy" {
				return "/usr/local/bin/agy"
			}
			return ""
		}
		got := resolveDefaultSettings(home, lookup, func(context.Context, string) ([]ModelOpt, error) {
			return nil, errors.New("exit status 1")
		}, time.Second)
		if got.RouterKind != "antigravity" || got.WorkerKind != "antigravity" {
			t.Fatalf("kinds = %q / %q, want antigravity", got.RouterKind, got.WorkerKind)
		}
		if got.RouterModel != "" || got.WorkerModel != "" || got.RouterEffort != "" || got.WorkerEffort != "" {
			t.Fatalf("expected empty models/efforts on error, got %+v", got)
		}
	})

	t.Run("agy present but models command times out promptly", func(t *testing.T) {
		bin := writeFakeAgy(t, `sleep 5`)
		lookup := func(name, _ string) string {
			if name == "agy" {
				return bin
			}
			return ""
		}
		start := time.Now()
		got := resolveDefaultSettings(home, lookup, fetchAgyModels, 150*time.Millisecond)
		elapsed := time.Since(start)
		if elapsed > 2*time.Second {
			t.Fatalf("timed-out agy probe blocked for %v, want < 2s", elapsed)
		}
		if got.RouterKind != "antigravity" || got.WorkerKind != "antigravity" {
			t.Fatalf("kinds = %q / %q, want antigravity", got.RouterKind, got.WorkerKind)
		}
		if got.RouterModel != "" || got.WorkerModel != "" || got.RouterEffort != "" || got.WorkerEffort != "" {
			t.Fatalf("expected empty models/efforts on timeout, got %+v", got)
		}
	})

	t.Run("agy missing falls back to claude over codex", func(t *testing.T) {
		lookup := func(name, _ string) string {
			if name == "claude" || name == "codex" {
				return "/usr/local/bin/" + name
			}
			return ""
		}
		probeCalled := false
		got := resolveDefaultSettings(home, lookup, func(context.Context, string) ([]ModelOpt, error) {
			probeCalled = true
			return nil, nil
		}, time.Second)
		if probeCalled {
			t.Fatal("agy probe must not run when agy is missing")
		}
		if got.RouterKind != "claude" || got.WorkerKind != "claude" {
			t.Fatalf("kinds = %q / %q, want claude / claude", got.RouterKind, got.WorkerKind)
		}
		if got.RouterModel != "" || got.WorkerModel != "" || got.RouterEffort != "" || got.WorkerEffort != "" {
			t.Fatalf("expected empty models/efforts for claude fallback, got %+v", got)
		}
	})

	t.Run("only codex present falls back to codex", func(t *testing.T) {
		lookup := func(name, _ string) string {
			if name == "codex" {
				return "/usr/local/bin/codex"
			}
			return ""
		}
		got := resolveDefaultSettings(home, lookup, nil, time.Second)
		if got.RouterKind != "codex" || got.WorkerKind != "codex" {
			t.Fatalf("kinds = %q / %q, want codex / codex", got.RouterKind, got.WorkerKind)
		}
		if got.RouterModel != "" || got.WorkerModel != "" || got.RouterEffort != "" || got.WorkerEffort != "" {
			t.Fatalf("expected empty models/efforts for codex fallback, got %+v", got)
		}
	})

	t.Run("no agents installed falls back to claude", func(t *testing.T) {
		lookup := func(string, string) string { return "" }
		got := resolveDefaultSettings(home, lookup, nil, time.Second)
		if got.RouterKind != "claude" || got.WorkerKind != "claude" {
			t.Fatalf("kinds = %q / %q, want claude / claude", got.RouterKind, got.WorkerKind)
		}
	})
}

func TestInitStatePersistsFreshAndNeverOverwritesSavedSettings(t *testing.T) {
	t.Run("fresh db detects once and persists to sqlite", func(t *testing.T) {
		dir := t.TempDir()
		db, err := openDB(filepath.Join(dir, "herd.db"))
		if err != nil {
			t.Fatal(err)
		}
		defer db.Close()

		detectCalls := 0
		detect := func(home string) Settings {
			detectCalls++
			s := defaultSettings(home)
			s.RouterKind = "antigravity"
			s.WorkerKind = "antigravity"
			s.RouterModel = defaultAgyRouterModel
			s.WorkerModel = defaultAgyWorkerModel
			return s
		}

		st, err := initState(db, dir, detect)
		if err != nil {
			t.Fatal(err)
		}
		if detectCalls != 1 {
			t.Fatalf("detectCalls = %d, want 1", detectCalls)
		}
		if st.Settings.RouterKind != "antigravity" || st.Settings.WorkerKind != "antigravity" ||
			st.Settings.RouterModel != defaultAgyRouterModel || st.Settings.WorkerModel != defaultAgyWorkerModel {
			t.Fatalf("unexpected fresh settings: %+v", st.Settings)
		}

		// Verify settings row is immediately durable in SQLite kv.
		var raw string
		if err := db.QueryRow(`SELECT v FROM kv WHERE k='settings'`).Scan(&raw); err != nil {
			t.Fatalf("settings not persisted to kv: %v", err)
		}
		var saved Settings
		if err := json.Unmarshal([]byte(raw), &saved); err != nil {
			t.Fatalf("unmarshal persisted settings: %v", err)
		}
		if saved.RouterKind != "antigravity" || saved.RouterModel != defaultAgyRouterModel ||
			saved.WorkerKind != "antigravity" || saved.WorkerModel != defaultAgyWorkerModel {
			t.Fatalf("persisted settings mismatch: %+v", saved)
		}

		// Subsequent restart must not call detector or alter persisted settings.
		reloaded, err := initState(db, dir, func(string) Settings {
			t.Fatal("detector must not run when settings are already persisted")
			return Settings{}
		})
		if err != nil {
			t.Fatal(err)
		}
		if reloaded.Settings.RouterKind != "antigravity" || reloaded.Settings.RouterModel != defaultAgyRouterModel ||
			reloaded.Settings.WorkerKind != "antigravity" || reloaded.Settings.WorkerModel != defaultAgyWorkerModel {
			t.Fatalf("reloaded settings mismatch: %+v", reloaded.Settings)
		}
	})

	t.Run("existing user settings with empty omitempty models are never overwritten", func(t *testing.T) {
		dir := t.TempDir()
		db, err := openDB(filepath.Join(dir, "herd.db"))
		if err != nil {
			t.Fatal(err)
		}
		defer db.Close()

		// Save existing user settings with custom kinds and intentionally empty models
		// (omitted from JSON due to omitempty).
		existing := defaultState(dir)
		existing.Settings.RouterKind = "claude"
		existing.Settings.WorkerKind = "codex"
		existing.Settings.RouterModel = ""
		existing.Settings.WorkerModel = ""
		existing.Settings.WorkerCwd = "/custom/workspace"
		existing.Settings.NotifyLevel = "normal"
		if err := saveState(db, existing, nil); err != nil {
			t.Fatal(err)
		}

		got, err := initState(db, dir, func(string) Settings {
			t.Fatal("detector must never run when persisted settings exist")
			return Settings{
				RouterKind: "antigravity", WorkerKind: "antigravity",
				RouterModel: defaultAgyRouterModel, WorkerModel: defaultAgyWorkerModel,
			}
		})
		if err != nil {
			t.Fatal(err)
		}
		if got.Settings.RouterKind != "claude" || got.Settings.WorkerKind != "codex" ||
			got.Settings.RouterModel != "" || got.Settings.WorkerModel != "" ||
			got.Settings.WorkerCwd != "/custom/workspace" || got.Settings.NotifyLevel != "normal" {
			t.Fatalf("existing settings were modified: %+v", got.Settings)
		}
	})

	t.Run("existing antigravity user with empty default models keeps them empty", func(t *testing.T) {
		dir := t.TempDir()
		db, err := openDB(filepath.Join(dir, "herd.db"))
		if err != nil {
			t.Fatal(err)
		}
		defer db.Close()

		existing := defaultState(dir)
		existing.Settings.RouterKind = "antigravity"
		existing.Settings.WorkerKind = "antigravity"
		existing.Settings.RouterModel = ""
		existing.Settings.WorkerModel = ""
		if err := saveState(db, existing, nil); err != nil {
			t.Fatal(err)
		}

		got, err := initState(db, dir, func(string) Settings {
			t.Fatal("detector must never run when persisted settings exist")
			return Settings{}
		})
		if err != nil {
			t.Fatal(err)
		}
		if got.Settings.RouterKind != "antigravity" || got.Settings.WorkerKind != "antigravity" ||
			got.Settings.RouterModel != "" || got.Settings.WorkerModel != "" {
			t.Fatalf("existing antigravity settings were overwritten: %+v", got.Settings)
		}
	})
}
