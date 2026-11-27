package collie

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type stubForgeProvider struct {
	name      string
	termPR    string
	probeFn   func(ctx context.Context, cfg ForgeConfig) (ForgeIdentity, error)
	pollFn    func(ctx context.Context, cfg ForgeConfig, cursor ForgeCursor) ([]ForgeItem, ForgeCursor, time.Duration, error)
	sampleFn  func(ctx context.Context, cfg ForgeConfig, limit int) ([]ForgeItem, error)
	pollCalls atomic.Int32
}

func (s *stubForgeProvider) Name() string { return s.name }
func (s *stubForgeProvider) Meta() IntegrationMeta {
	return IntegrationMeta{Kind: s.name, Name: strings.ToUpper(s.name), Subtitle: "stub", Description: "stub", Order: 25}
}
func (s *stubForgeProvider) TermPR() string {
	if s.termPR != "" {
		return s.termPR
	}
	return "PR"
}
func (s *stubForgeProvider) DefaultAuthMode() string { return "cli" }
func (s *stubForgeProvider) DefaultEvents() map[string]bool {
	return map[string]bool{"pr": true, "issue": true, "ci": true}
}
func (s *stubForgeProvider) IsConfigured(_ IntegrationConfig, fc ForgeConfig) bool {
	return fc.Configured
}
func (s *stubForgeProvider) SecretSet(_ IntegrationConfig) bool { return false }
func (s *stubForgeProvider) ExtraView(_ IntegrationConfig) map[string]any {
	return map[string]any{"provider_type": "cli"}
}
func (s *stubForgeProvider) Probe(ctx context.Context, cfg ForgeConfig) (ForgeIdentity, error) {
	if s.probeFn != nil {
		return s.probeFn(ctx, cfg)
	}
	return ForgeIdentity{User: "alice"}, nil
}
func (s *stubForgeProvider) Poll(ctx context.Context, cfg ForgeConfig, cursor ForgeCursor) ([]ForgeItem, ForgeCursor, time.Duration, error) {
	s.pollCalls.Add(1)
	if s.pollFn != nil {
		return s.pollFn(ctx, cfg, cursor)
	}
	return nil, cursor, 15 * time.Millisecond, nil
}
func (s *stubForgeProvider) Sample(ctx context.Context, cfg ForgeConfig, limit int) ([]ForgeItem, error) {
	if s.sampleFn != nil {
		return s.sampleFn(ctx, cfg, limit)
	}
	return nil, nil
}

func newTestForgeEnv(t *testing.T, prov ForgeProvider) (*Super, *IntegrationManager, *GitForgeAdapter, string) {
	t.Helper()
	dir := t.TempDir()
	db, err := openDB(filepath.Join(dir, "herd.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	st, err := initState(db, dir, defaultSettings)
	if err != nil {
		t.Fatal(err)
	}
	sec, err := openSecretStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	sp := &Super{db: db, st: st, subs: map[chan struct{}]bool{}}
	mgr := newIntegrationManager(sp, db, sec)
	sp.integrations = mgr
	adapter := newGitForgeAdapter(sp, mgr, prov)
	adapter.minPollInterval = 10 * time.Millisecond
	mgr.Register(adapter)
	return sp, mgr, adapter, dir
}

func TestGitForgeColdStartWatermarkAndNoiseFiltering(t *testing.T) {
	t.Parallel()
	base := time.Date(2026, 11, 25, 12, 0, 0, 0, time.UTC)
	var curNow atomic.Int64
	curNow.Store(base.UnixNano())

	var mu sync.Mutex
	pollNum := 0
	secondPollDone := make(chan struct{})

	prov := &stubForgeProvider{
		name: "mockforge",
		pollFn: func(ctx context.Context, cfg ForgeConfig, cursor ForgeCursor) ([]ForgeItem, ForgeCursor, time.Duration, error) {
			mu.Lock()
			pollNum++
			n := pollNum
			mu.Unlock()
			if n == 1 {
				// Priming cold-start poll returns old backlog items that MUST be discarded.
				cursor.ETag = `"etag-prime"`
				return []ForgeItem{
					{DedupKey: "old:1", Kind: "pr", Repo: "acme/app", Number: 10, Title: "Ancient PR", Author: "bob", At: base.Add(-2 * time.Hour)},
				}, cursor, 15 * time.Millisecond, nil
			}
			if n == 2 {
				defer func() {
					select {
					case <-secondPollDone:
					default:
						close(secondPollDone)
					}
				}()
				return []ForgeItem{
					// Older than watermark -> skipped.
					{DedupKey: "stale:1", Kind: "pr", Repo: "acme/app", Number: 11, Title: "Old item", Author: "bob", At: base.Add(-5 * time.Minute)},
					// Self-authored PR comment -> skipped.
					{DedupKey: "self:1", Kind: "pr", Repo: "acme/app", Number: 12, Title: "My own comment", Author: "alice", At: base.Add(30 * time.Second)},
					// Bot comment -> skipped.
					{DedupKey: "bot:1", Kind: "pr", Repo: "acme/app", Number: 13, Title: "Deps bump", Author: "dependabot[bot]", At: base.Add(40 * time.Second)},
					// Passing CI -> skipped.
					{DedupKey: "ci:ok", Kind: "ci", Repo: "acme/app", Number: 14, Title: "Unit test", Author: "github-actions[bot]", Conclusion: "success", At: base.Add(50 * time.Second)},
					// Real human review -> accepted!
					{DedupKey: "real:pr", Kind: "pr", Repo: "acme/app", Number: 15, Title: "Fix race condition", Author: "bob", URL: "https://example.com/acme/app/pull/15", At: base.Add(60 * time.Second)},
					// Failing CI (even triggered by self or bot) -> accepted!
					{DedupKey: "real:ci", Kind: "ci", Repo: "acme/app", Number: 15, Title: "Go Test (main)", Author: "alice", Conclusion: "timed_out", URL: "https://example.com/acme/app/runs/99", At: base.Add(70 * time.Second)},
				}, cursor, 5 * time.Second, nil
			}
			return nil, cursor, 5 * time.Second, nil
		},
	}

	sp, mgr, adapter, _ := newTestForgeEnv(t, prov)
	adapter.nowFn = func() time.Time { return time.Unix(0, curNow.Load()).UTC() }

	enabled := true
	_, err := mgr.Save("mockforge", SaveRequest{
		Enabled: &enabled,
		Config:  json.RawMessage(`{"configured":true,"watch_mentions":true,"handle_mode":"advise","poll_interval":30}`),
	})
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	mgr.Start(ctx)
	defer mgr.Stop(time.Second)

	select {
	case <-secondPollDone:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for second poll")
	}
	// Give emit loop a moment to finish persisting.
	time.Sleep(40 * time.Millisecond)

	sp.mu.Lock()
	trigs := append([]*Trigger(nil), sp.st.Triggers...)
	sp.mu.Unlock()

	if len(trigs) != 2 {
		t.Fatalf("expected exactly 2 filtered triggers (real:pr + real:ci), got %d: %+v", len(trigs), trigs)
	}
	if trigs[0].Key != "real:pr" || trigs[1].Key != "real:ci" {
		t.Fatalf("unexpected trigger keys: %q, %q", trigs[0].Key, trigs[1].Key)
	}
	if trigs[0].Thread != "mockforge:acme/app#15" {
		t.Fatalf("thread = %q, want mockforge:acme/app#15", trigs[0].Thread)
	}
	// Advise mode must route to routerName and include explicit read-only guardrail.
	if len(trigs[0].Targets) != 1 || trigs[0].Targets[0] != routerName {
		t.Fatalf("advise mode targets = %v, want [%s]", trigs[0].Targets, routerName)
	}
	if !strings.Contains(trigs[0].Text, "严禁") || !strings.Contains(trigs[0].Text, "擅自") {
		t.Fatalf("advise mode text missing read-only guardrail: %q", trigs[0].Text)
	}
	// Cursor must have persisted ETag and advanced watermark.
	cur := parseForgeCursor(mgr.Cursor("mockforge"))
	if !cur.Initialized || cur.ETag != `"etag-prime"` || !cur.Watermark.Equal(base.Add(70*time.Second)) {
		t.Fatalf("unexpected persisted cursor: %+v", cur)
	}
}

func TestGitForgeThreeHandleModesAndSamplePullReadOnly(t *testing.T) {
	t.Parallel()
	now := time.Now().UTC()
	sampleItems := []ForgeItem{
		{DedupKey: "s:1", Kind: "pr", Repo: "acme/web", Number: 42, Tag: "PR 评审", Title: "Polish modal", Author: "carol", URL: "https://example.com/acme/web/pull/42", At: now.Add(-2 * time.Minute)},
		{DedupKey: "s:2", Kind: "ci", Repo: "acme/web", Number: 42, Tag: "CI 失败", Title: "lint job", Author: "ci-bot", Conclusion: "failure", URL: "https://example.com/acme/web/runs/1", At: now.Add(-5 * time.Minute)},
		{DedupKey: "s:3", Kind: "issue", Repo: "acme/web", Number: 7, Tag: "Issue 讨论", Title: "Dark mode contrast", Author: "dave", URL: "https://example.com/acme/web/issues/7", At: now.Add(-9 * time.Minute)},
		{DedupKey: "s:4", Kind: "issue", Repo: "acme/web", Number: 8, Tag: "Issue 讨论", Title: "Extra fourth item", Author: "erin", At: now.Add(-12 * time.Minute)},
		// Should be filtered out by SamplePull:
		{DedupKey: "s:self", Kind: "pr", Repo: "acme/web", Number: 99, Title: "My own PR", Author: "alice", At: now.Add(-1 * time.Minute)},
		{DedupKey: "s:okci", Kind: "ci", Repo: "acme/web", Number: 42, Title: "build", Conclusion: "success", At: now.Add(-1 * time.Minute)},
	}

	prov := &stubForgeProvider{
		name: "mockforge",
		sampleFn: func(ctx context.Context, cfg ForgeConfig, limit int) ([]ForgeItem, error) {
			return sampleItems, nil
		},
	}

	sp, mgr, adapter, _ := newTestForgeEnv(t, prov)
	// Attach an active subscribed worker to mockforge:acme/web#42.
	sp.mu.Lock()
	sp.st.Workers["pr-42-fix"] = &Worker{Name: "pr-42-fix", Thread: "mockforge:acme/web#42", Status: "idle"}
	sp.st.Threads["mockforge:acme/web#42"] = &Thread{ID: "mockforge:acme/web#42", Subscribers: []string{"pr-42-fix"}}
	sp.mu.Unlock()

	// 1. SamplePull(3) in agent mode: returns 3 filtered items, mentions pr-42-fix for #42,
	// and does NOT touch cursor, dedup table, or triggers.
	evs, err := adapter.SamplePull(context.Background(), IntegrationConfig{
		Kind:   "mockforge",
		Config: json.RawMessage(`{"configured":true,"handle_mode":"agent","watch_mentions":true}`),
	}, 3)
	if err != nil {
		t.Fatalf("SamplePull: %v", err)
	}
	if len(evs) != 3 {
		t.Fatalf("SamplePull len = %d, want 3", len(evs))
	}
	if !strings.Contains(evs[0].Action, "pr-42-fix") {
		t.Fatalf("expected SamplePull[0].Action to name subscribed worker pr-42-fix, got %q", evs[0].Action)
	}
	if strings.Contains(evs[2].Action, "pr-42-fix") || !strings.Contains(evs[2].Action, "Collie") {
		t.Fatalf("expected SamplePull[2].Action (unsubscribed #7) to mention fallback to Collie, got %q", evs[2].Action)
	}
	if len(sp.st.Triggers) != 0 {
		t.Fatalf("SamplePull must not emit triggers, got %d", len(sp.st.Triggers))
	}
	if string(mgr.Cursor("mockforge")) != "{}" {
		t.Fatalf("SamplePull must not advance cursor, got %s", string(mgr.Cursor("mockforge")))
	}
	var dedupCount int
	_ = sp.db.QueryRow(`SELECT COUNT(*) FROM integration_dedup`).Scan(&dedupCount)
	if dedupCount != 0 {
		t.Fatalf("SamplePull must not write integration_dedup, got %d rows", dedupCount)
	}

	sink := &managerSink{mgr: mgr, kind: "mockforge"}

	// 2. Mode 1: notify — writes inbox trigger with status="done" (never wakes Router) + posts summary entry.
	ok, err := adapter.emitItem(context.Background(), ForgeConfig{HandleMode: forgeModeNotify}, sampleItems[0], sink)
	if err != nil || !ok {
		t.Fatalf("emitItem notify: ok=%v err=%v", ok, err)
	}
	if got := len(sp.st.Triggers); got != 1 {
		t.Fatalf("notify triggers = %d, want 1", got)
	}
	if sp.st.Triggers[0].Status != "done" || sp.st.Triggers[0].Action != "notify" {
		t.Fatalf("notify trigger must not wake router: status=%q action=%q", sp.st.Triggers[0].Status, sp.st.Triggers[0].Action)
	}
	if len(sp.st.Entries) != 1 || sp.st.Entries[0].Role != "event" {
		t.Fatalf("notify mode must add human summary event entry, got %+v", sp.st.Entries)
	}
	// Duplicate dedup key must be rejected persistently.
	okDup, err := adapter.emitItem(context.Background(), ForgeConfig{HandleMode: forgeModeNotify}, sampleItems[0], sink)
	if err != nil || okDup {
		t.Fatalf("duplicate dedup key should be rejected: ok=%v err=%v", okDup, err)
	}

	// 3. Mode 3: agent — subscribed thread (#42) leaves Targets empty for fast-path subscriber routing;
	// unsubscribed thread (#7) degrades cleanly to advise mode targeting routerName with guardrail.
	ok, err = adapter.emitItem(context.Background(), ForgeConfig{HandleMode: forgeModeAgent}, sampleItems[1], sink)
	if err != nil || !ok {
		t.Fatalf("emitItem agent subscribed: ok=%v err=%v", ok, err)
	}
	subTrig := sp.st.Triggers[len(sp.st.Triggers)-1]
	if subTrig.Status != "queued" || subTrig.Action != "respond" || len(subTrig.Targets) != 0 {
		t.Fatalf("subscribed agent trigger should leave Targets empty for Thread.Subscribers fast path: %+v", subTrig)
	}

	ok, err = adapter.emitItem(context.Background(), ForgeConfig{HandleMode: forgeModeAgent}, sampleItems[2], sink)
	if err != nil || !ok {
		t.Fatalf("emitItem agent unsubscribed fallback: ok=%v err=%v", ok, err)
	}
	fbTrig := sp.st.Triggers[len(sp.st.Triggers)-1]
	if len(fbTrig.Targets) != 1 || fbTrig.Targets[0] != routerName {
		t.Fatalf("unsubscribed agent trigger should degrade to routerName, got targets=%v", fbTrig.Targets)
	}
	if !strings.Contains(fbTrig.Text, "严禁") {
		t.Fatalf("degraded agent trigger missing read-only guardrail: %q", fbTrig.Text)
	}
}

// TestPublicRepoNoInternalTerms is a static safety redline test ensuring the
// public repository source tree and demo bundle contain zero internal domains,
// internal hostnames/IPs, or proprietary internal CLI tool names.
func TestPublicRepoNoInternalTerms(t *testing.T) {
	t.Parallel()
	// Assemble forbidden markers from fragments so this test file stays clean too.
	forbidden := []string{
		strings.Join([]string{"byted", "cli"}, ""),
		strings.Join([]string{"code.", "byted", ".org"}, ""),
		strings.Join([]string{"byted", "ance.net"}, ""),
		strings.Join([]string{"byted", "ance.com"}, ""),
		strings.Join([]string{"10.37.", "125.77"}, ""),
		strings.Join([]string{"n37-", "125-077"}, ""),
	}

	root := filepath.Clean(filepath.Join("..", ".."))
	scanRoots := []string{
		filepath.Join(root, "internal"),
		filepath.Join(root, "web"),
		filepath.Join(root, "scripts"),
		filepath.Join(root, "docs", "demo"),
	}

	for _, sr := range scanRoots {
		if _, err := os.Stat(sr); err != nil {
			continue
		}
		err := filepath.WalkDir(sr, func(path string, d os.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			if d.IsDir() {
				if d.Name() == "vendor" || d.Name() == ".git" {
					return filepath.SkipDir
				}
				return nil
			}
			ext := strings.ToLower(filepath.Ext(path))
			switch ext {
			case ".go", ".js", ".css", ".html", ".sh", ".json", ".md":
			default:
				return nil
			}
			raw, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			lower := strings.ToLower(string(raw))
			for _, term := range forbidden {
				if strings.Contains(lower, term) {
					t.Errorf("public repo file %s contains forbidden internal term %q", path, term)
				}
			}
			return nil
		})
		if err != nil {
			t.Fatalf("walk %s: %v", sr, err)
		}
	}
}
