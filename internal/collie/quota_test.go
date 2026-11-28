package collie

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestParseAgyUsageOutput(t *testing.T) {
	nowStr := "2026-11-28T09:20:00Z"

	t.Run("structured json groups sorted and secondary full compacted", func(t *testing.T) {
		raw := `{
			"conversation_id": "",
			"status": "SUCCESS",
			"num_turns": 0,
			"command": {
				"name": "usage",
				"data": {
					"groups": [
						{
							"name": "Gemini Models",
							"buckets": [
								{"id":"gemini-weekly","name":"Weekly Limit Remaining","window":"weekly","remaining_fraction":0.3865,"reset_time":"2026-12-03T06:57:06Z"},
								{"id":"gemini-5h","name":"Five Hour Limit Remaining","window":"5h","remaining_fraction":0.8586,"reset_time":"2026-11-28T03:57:06Z"}
							]
						},
						{
							"name": "Claude and GPT models",
							"buckets": [
								{"id":"3p-weekly","name":"Weekly Limit Remaining","window":"weekly","remaining_fraction":1,"reset_time":"1970-01-01T00:00:00Z"},
								{"id":"3p-5h","name":"Five Hour Limit Remaining","window":"5h","remaining_fraction":1,"reset_time":"1970-01-01T00:00:00Z"}
							]
						}
					]
				}
			}
		}`
		ag := parseAgyUsageOutput([]byte(raw), "", 0, nowStr)
		if ag.Status != "ok" || ag.Kind != "agy" || len(ag.Pools) != 2 {
			t.Fatalf("unexpected agy result: %+v", ag)
		}
		if ag.Pools[0].Group != "Gemini" || ag.Pools[0].CompactFull {
			t.Fatalf("expected expanded Gemini pool, got %+v", ag.Pools[0])
		}
		if len(ag.Pools[0].Windows) != 2 || ag.Pools[0].Windows[0].ID != "5h" || ag.Pools[0].Windows[0].RemainingPct != 86 {
			t.Fatalf("expected 5h first with 86%%, got %+v", ag.Pools[0].Windows)
		}
		if ag.Pools[0].Windows[1].ID != "weekly" || ag.Pools[0].Windows[1].RemainingPct != 39 {
			t.Fatalf("expected weekly second with 39%%, got %+v", ag.Pools[0].Windows)
		}
		if ag.Pools[1].Group != "Claude / GPT" || !ag.Pools[1].CompactFull {
			t.Fatalf("expected compact secondary Claude / GPT pool, got %+v", ag.Pools[1])
		}
		if ag.Pools[1].Windows[0].ResetsAt != nil {
			t.Fatalf("expected epoch-zero reset_time normalized to nil, got %v", *ag.Pools[1].Windows[0].ResetsAt)
		}
	})

	t.Run("tsv fallback when command.data is missing", func(t *testing.T) {
		raw := `{"status":"SUCCESS","response":"Gemini Models\tWeekly Limit Remaining\t14%\t2026-12-03T06:57:06Z\nGemini Models\tFive Hour Limit Remaining\t72%\t2026-11-28T11:57:06Z\n"}`
		ag := parseAgyUsageOutput([]byte(raw), "", 0, nowStr)
		if ag.Status != "ok" || len(ag.Pools) != 1 || len(ag.Pools[0].Windows) != 2 {
			t.Fatalf("expected TSV fallback ok, got %+v", ag)
		}
		if ag.Pools[0].Windows[0].ID != "5h" || ag.Pools[0].Windows[0].RemainingPct != 72 {
			t.Fatalf("expected 5h=72%%, got %+v", ag.Pools[0].Windows[0])
		}
		if ag.Pools[0].Windows[1].ID != "weekly" || ag.Pools[0].Windows[1].RemainingPct != 14 {
			t.Fatalf("expected weekly=14%%, got %+v", ag.Pools[0].Windows[1])
		}
	})

	t.Run("exit code 2 api key mode gracefully degrades", func(t *testing.T) {
		ag := parseAgyUsageOutput(nil, "Usage command is unavailable in API key mode", 2, nowStr)
		if ag.Status != "degraded" || ag.Plan != "API Key 模式" || len(ag.Pools) != 0 {
			t.Fatalf("expected degraded API key mode, got %+v", ag)
		}
	})

	t.Run("group models metadata preserved and deep-cloned", func(t *testing.T) {
		raw := `{
			"status": "SUCCESS",
			"command": {
				"name": "usage",
				"data": {
					"groups": [
						{
							"name": "Gemini 4 Neon",
							"models": ["gemini-4-neon-xhigh", "gemini-4-neon-high", "gemini-4-neon-xhigh"],
							"buckets": [
								{"id":"neon-5h","name":"Five Hour Limit Remaining","window":"5h","remaining_fraction":0.42,"reset_time":"2026-11-28T13:00:00Z"}
							]
						}
					]
				}
			}
		}`
		ag := parseAgyUsageOutput([]byte(raw), "", 0, nowStr)
		if ag.Status != "ok" || len(ag.Pools) != 1 || len(ag.Pools[0].Models) != 2 {
			t.Fatalf("expected deduplicated pool models, got %+v", ag.Pools)
		}
		cloned := cloneAgentQuota(ag)
		cloned.Pools[0].Models[0] = "mutated"
		if ag.Pools[0].Models[0] != "gemini-4-neon-xhigh" {
			t.Fatalf("cloneAgentQuota did not deep-copy pool models")
		}
	})
}

func TestParseCodexRateLimitsAndRolloutFallback(t *testing.T) {
	nowStr := "2026-11-28T09:20:00Z"

	t.Run("app-server camelCase converts usedPercent to remaining_pct", func(t *testing.T) {
		raw := `{
			"rateLimits": {
				"limitId": "codex",
				"primary": {"usedPercent": 18.4, "windowDurationMins": 300, "resetsAt": 1795850000},
				"secondary": {"usedPercent": 36.2, "windowDurationMins": 10080, "resetsAt": 1796200000},
				"planType": "plus"
			},
			"accountId": "acct_secret_should_never_leak"
		}`
		ag := parseCodexAppServerRateLimits([]byte(raw), nowStr)
		if ag.Status != "ok" || ag.Plan != "Plus" || len(ag.Pools) != 1 || len(ag.Pools[0].Windows) != 2 {
			t.Fatalf("unexpected codex result: %+v", ag)
		}
		if ag.Pools[0].Windows[0].ID != "5h" || ag.Pools[0].Windows[0].RemainingPct != 82 {
			t.Fatalf("expected 5h remaining=82%%, got %+v", ag.Pools[0].Windows[0])
		}
		if ag.Pools[0].Windows[1].ID != "weekly" || ag.Pools[0].Windows[1].RemainingPct != 64 {
			t.Fatalf("expected weekly remaining=64%%, got %+v", ag.Pools[0].Windows[1])
		}
		b, _ := json.Marshal(ag)
		if strings.Contains(string(b), "acct_secret") {
			t.Fatalf("PII leaked into serialized output: %s", string(b))
		}
	})

	t.Run("app-server 30-day free window", func(t *testing.T) {
		raw := `{
			"rateLimits": {
				"primary": {"usedPercent": 0, "windowDurationMins": 43200, "resetsAt": 1798422615},
				"secondary": null,
				"planType": "free"
			}
		}`
		ag := parseCodexAppServerRateLimits([]byte(raw), nowStr)
		if ag.Status != "ok" || ag.Plan != "Free" || len(ag.Pools[0].Windows) != 1 {
			t.Fatalf("unexpected 30d codex result: %+v", ag)
		}
		if ag.Pools[0].Windows[0].ID != "30d" || ag.Pools[0].Windows[0].Label != "30 天窗口" || ag.Pools[0].Windows[0].RemainingPct != 100 {
			t.Fatalf("expected 30d 100%%, got %+v", ag.Pools[0].Windows[0])
		}
	})

	t.Run("app-server preserves model-specific rateLimitsByLimitId pools alongside default pool", func(t *testing.T) {
		raw := `{
			"rateLimits": {
				"limitId": "codex",
				"primary": {"usedPercent": 20, "windowDurationMins": 300, "resetsAt": 1795850000},
				"secondary": {"usedPercent": 45, "windowDurationMins": 10080, "resetsAt": 1796200000},
				"planType": "plus"
			},
			"rateLimitsByLimitId": {
				"codex": {
					"limitId": "codex",
					"primary": {"usedPercent": 20, "windowDurationMins": 300, "resetsAt": 1795850000},
					"secondary": {"usedPercent": 45, "windowDurationMins": 10080, "resetsAt": 1796200000},
					"planType": "plus"
				},
				"codex_spark": {
					"limitId": "codex_spark",
					"limitName": "GPT-5.3-Codex-Spark",
					"normalModelSlug": "gpt-5.3-codex-spark",
					"primary": {"usedPercent": 88, "windowDurationMins": 300, "resetsAt": 1795850000},
					"secondary": {"usedPercent": 10, "windowDurationMins": 10080, "resetsAt": 1796200000},
					"planType": "plus"
				}
			}
		}`
		ag := parseCodexAppServerRateLimits([]byte(raw), nowStr)
		if ag.Status != "ok" || len(ag.Pools) != 2 {
			t.Fatalf("expected 2 codex pools (default + model-specific), got %+v", ag.Pools)
		}
		if ag.Pools[0].Group != "" || ag.Pools[0].Windows[0].RemainingPct != 80 || ag.Pools[0].Windows[1].RemainingPct != 55 {
			t.Fatalf("unexpected default codex pool: %+v", ag.Pools[0])
		}
		if ag.Pools[1].Group != "GPT-5.3-Codex-Spark" || len(ag.Pools[1].Models) != 1 || ag.Pools[1].Models[0] != "gpt-5.3-codex-spark" {
			t.Fatalf("unexpected model-specific codex pool: %+v", ag.Pools[1])
		}
		if ag.Pools[1].Windows[0].RemainingPct != 12 || ag.Pools[1].Windows[1].RemainingPct != 90 {
			t.Fatalf("unexpected spark windows: %+v", ag.Pools[1].Windows)
		}
	})

	t.Run("null primary and secondary degrades cleanly without fabricating 0 or 100", func(t *testing.T) {
		raw := `{"rateLimits":{"primary":null,"secondary":null,"credits":null,"planType":null}}`
		ag := parseCodexAppServerRateLimits([]byte(raw), nowStr)
		if ag.Status != "degraded" || len(ag.Pools) != 0 {
			t.Fatalf("expected degraded without pools, got %+v", ag)
		}
	})

	t.Run("0ms rollout jsonl snake_case fallback", func(t *testing.T) {
		dir := t.TempDir()
		sessDir := filepath.Join(dir, "sessions", "2026", "11", "28")
		if err := os.MkdirAll(sessDir, 0o755); err != nil {
			t.Fatal(err)
		}
		fp := filepath.Join(sessDir, "rollout-2026-11-28T09-00-00-test.jsonl")
		lines := strings.Join([]string{
			`{"timestamp":"2026-11-28T09:05:00Z","payload":{"type":"token_count","rate_limits":{"primary":null,"secondary":null}}}`,
			`{"timestamp":"2026-11-28T09:10:00Z","payload":{"type":"token_count","rate_limits":{"plan_type":"pro","primary":{"used_percent":85,"window_duration_mins":300,"resets_at":1795850000},"secondary":{"used_percent":40,"window_duration_mins":10080,"resets_at":1796200000}}}}`,
		}, "\n") + "\n"
		if err := os.WriteFile(fp, []byte(lines), 0o600); err != nil {
			t.Fatal(err)
		}
		rl, ts, ok := readCodexRolloutRateLimits(dir)
		if !ok || rl == nil {
			t.Fatal("expected rollout rate_limits to be found")
		}
		if ts.Format(time.RFC3339) != "2026-11-28T09:10:00Z" {
			t.Fatalf("unexpected rollout timestamp: %s", ts.Format(time.RFC3339))
		}
		ag := buildCodexAgentQuota(rl, "session", ts.Format(time.RFC3339))
		if ag.Status != "ok" || ag.Plan != "Pro" || ag.Pools[0].Windows[0].RemainingPct != 15 {
			t.Fatalf("expected remaining 15%% Pro from rollout, got %+v", ag)
		}
	})
}

func TestParseClaudeAndCursorDegradation(t *testing.T) {
	now := time.Date(2026, 11, 28, 9, 20, 0, 0, time.UTC)
	nowStr := now.Format(time.RFC3339)

	t.Run("claude oauth subscription defaults to non-invasive optin and drops PII", func(t *testing.T) {
		raw := `{"loggedIn":true,"authMethod":"oauth_token","apiProvider":"firstParty","subscriptionType":"max","email":"alice@example.com","orgId":"org_secret_123"}`
		ag := parseClaudeAuthStatus([]byte(raw), false, "", nowStr)
		if ag.Status != "optin" || ag.Plan != "Max" {
			t.Fatalf("unexpected claude optin: %+v", ag)
		}
		b, _ := json.Marshal(ag)
		if strings.Contains(string(b), "alice@example.com") || strings.Contains(string(b), "org_secret_123") {
			t.Fatalf("PII leaked in claude quota: %s", string(b))
		}
	})

	t.Run("claude custom api gateway degrades cleanly", func(t *testing.T) {
		raw := `{"loggedIn":true,"authMethod":"oauth_token","apiProvider":"firstParty"}`
		ag := parseClaudeAuthStatus([]byte(raw), true, "", nowStr)
		if ag.Status != "degraded" || !strings.Contains(ag.Plan, "API") {
			t.Fatalf("expected degraded API gateway mode, got %+v", ag)
		}
	})

	t.Run("claude optional statusline cache gives exact remaining percentages", func(t *testing.T) {
		raw := `{"plan":"max","updated_at":"2026-11-28T09:19:00Z","rate_limits":{"five_hour":{"used_percentage":29,"resets_at":1795850000},"seven_day":{"used_percentage":42,"resets_at":1796200000}}}`
		ag, ok := parseClaudeStatuslineJSON([]byte(raw), now)
		if !ok || ag.Status != "ok" || ag.Source != "statusline" || len(ag.Pools[0].Windows) != 2 {
			t.Fatalf("unexpected claude statusline result: ok=%v ag=%+v", ok, ag)
		}
		if ag.Pools[0].Windows[0].RemainingPct != 71 || ag.Pools[0].Windows[1].RemainingPct != 58 {
			t.Fatalf("expected 71%% / 58%%, got %+v", ag.Pools[0].Windows)
		}
	})

	t.Run("claude local transcript detects recent limit hit and never modifies settings.json", func(t *testing.T) {
		cfgDir := t.TempDir()
		settingsPath := filepath.Join(cfgDir, "settings.json")
		origSettings := `{"model":"sonnet","env":{"FOO":"bar"}}`
		if err := os.WriteFile(settingsPath, []byte(origSettings), 0o600); err != nil {
			t.Fatal(err)
		}
		projDir := filepath.Join(cfgDir, "projects", "demo")
		if err := os.MkdirAll(projDir, 0o755); err != nil {
			t.Fatal(err)
		}
		txPath := filepath.Join(projDir, "session.jsonl")
		if err := os.WriteFile(txPath, []byte(`{"type":"assistant","isApiErrorMessage":true,"message":{"content":"You've hit your limit · resets 14:00"}}`+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		_ = claudeUsesCustomAPISettings(cfgDir)
		note := scanClaudeRecentRateLimitNote(cfgDir, time.Now())
		if !strings.Contains(note, "hit your limit") {
			t.Fatalf("expected limit hit note, got %q", note)
		}
		after, _ := os.ReadFile(settingsPath)
		if string(after) != origSettings {
			t.Fatalf("settings.json was unexpectedly modified")
		}
	})

	t.Run("cursor always degrades honestly with console link and zero progress pools", func(t *testing.T) {
		ag := parseCursorStatusOutput("\x1b[32mLogged in as alice@example.com\x1b[0m", true, nowStr)
		if ag.Status != "external" || ag.Plan != "CLI 已登录" || ag.LinkURL != quotaCursorConsoleURL || len(ag.Pools) != 0 {
			t.Fatalf("unexpected cursor result: %+v", ag)
		}
		b, _ := json.Marshal(ag)
		if strings.Contains(string(b), "alice@example.com") {
			t.Fatalf("cursor leaked email: %s", string(b))
		}
	})
}

func TestQuotaManagerCacheCooldownSingleflightAndWorkerHook(t *testing.T) {
	dataDir := t.TempDir()
	nowMu := sync.Mutex{}
	curTime := time.Date(2026, 11, 28, 10, 0, 0, 0, time.UTC)
	getNow := func() time.Time {
		nowMu.Lock()
		defer nowMu.Unlock()
		return curTime
	}
	advance := func(d time.Duration) {
		nowMu.Lock()
		curTime = curTime.Add(d)
		nowMu.Unlock()
	}

	qm := newQuotaManager(t.TempDir(), dataDir)
	qm.now = getNow
	qm.detect = func() []string { return []string{"agy", "codex"} }

	var agyCalls, codexCalls atomic.Int32
	var agyFail atomic.Bool
	blockCh := make(chan struct{})
	var blockOnce atomic.Bool

	qm.collectors = map[string]quotaCollectorFunc{
		"agy": func(ctx context.Context) AgentQuota {
			agyCalls.Add(1)
			if blockOnce.Swap(false) {
				<-blockCh
			}
			if agyFail.Load() {
				return AgentQuota{Kind: "agy", Name: "Antigravity", Status: "error", Note: "timeout"}
			}
			return AgentQuota{
				Kind: "agy", Name: "Antigravity", Status: "ok", Source: "cli",
				UpdatedAt: getNow().Format(time.RFC3339),
				Pools: []QuotaPool{{
					Group:   "Gemini",
					Windows: []QuotaWindow{{ID: "5h", Label: "5 小时", RemainingPct: 78}},
				}},
			}
		},
		"codex": func(ctx context.Context) AgentQuota {
			codexCalls.Add(1)
			return AgentQuota{
				Kind: "codex", Name: "Codex", Status: "ok", Source: "app-server",
				UpdatedAt: getNow().Format(time.RFC3339),
				Pools: []QuotaPool{{
					Windows: []QuotaWindow{{ID: "5h", Label: "5 小时窗口", RemainingPct: 62}},
				}},
			}
		},
	}

	// 1. Singleflight coalesces concurrent synchronous refreshes into 1 probe.
	blockOnce.Store(true)
	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			snap := qm.Get(context.Background(), true)
			if len(snap.Agents) != 2 {
				t.Errorf("expected 2 agents, got %d", len(snap.Agents))
			}
		}()
	}
	time.Sleep(25 * time.Millisecond)
	close(blockCh)
	wg.Wait()
	if agyCalls.Load() != 1 || codexCalls.Load() != 1 {
		t.Fatalf("expected singleflight 1 call each, got agy=%d codex=%d", agyCalls.Load(), codexCalls.Load())
	}

	// 2. Within 3m TTL, Get returns fresh cache without re-running collectors.
	advance(2 * time.Minute)
	snap := qm.Get(context.Background(), false)
	if snap.Stale || snap.Refreshing || agyCalls.Load() != 1 {
		t.Fatalf("expected fresh cached snapshot within 3m TTL, got stale=%v refreshing=%v calls=%d", snap.Stale, snap.Refreshing, agyCalls.Load())
	}

	// 3. Past 3m TTL, non-blocking Get returns stale immediately and refreshes in background.
	subCh, unsub := qm.subscribe()
	defer unsub()
	advance(2 * time.Minute)
	snap = qm.Get(context.Background(), false)
	if !snap.Stale || !snap.Refreshing {
		t.Fatalf("expected non-blocking stale+refreshing snapshot past 3m TTL, got %+v", snap)
	}
	select {
	case <-subCh:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for background refresh notification")
	}
	if agyCalls.Load() != 2 {
		t.Fatalf("expected 2nd agy call after TTL expiry, got %d", agyCalls.Load())
	}

	// 4. Manual refresh enforces 30s cooldown; transient error preserves previous ok pools.
	agyFail.Store(true)
	r1 := qm.Refresh(context.Background())
	if r1.RefreshCooldownSec != 30 {
		t.Fatalf("expected 30s cooldown after manual refresh, got %d", r1.RefreshCooldownSec)
	}
	if r1.Agents[0].Status != "ok" || r1.Agents[0].Pools[0].Windows[0].RemainingPct != 78 {
		t.Fatalf("expected previous valid agy pool preserved on transient error, got %+v", r1.Agents[0])
	}
	callsAfterManual := agyCalls.Load()

	advance(10 * time.Second)
	r2 := qm.Refresh(context.Background())
	if r2.RefreshCooldownSec != 20 || agyCalls.Load() != callsAfterManual {
		t.Fatalf("expected manual refresh blocked during cooldown (rem=20), got rem=%d calls=%d vs %d", r2.RefreshCooldownSec, agyCalls.Load(), callsAfterManual)
	}

	// 5. After cooldown expires, manual refresh runs again.
	agyFail.Store(false)
	advance(25 * time.Second)
	_ = qm.Refresh(context.Background())
	if agyCalls.Load() != callsAfterManual+1 {
		t.Fatalf("expected manual refresh after 30s cooldown, got %d", agyCalls.Load())
	}

	// 6. Disk cache reloads cleanly in a new manager without triggering subprocesses.
	qm2 := newQuotaManager(t.TempDir(), dataDir)
	qm2.now = getNow
	cached, ok := qm2.CachedSnapshot()
	if !ok || len(cached.Agents) != 2 || cached.Agents[0].Pools[0].Windows[0].RemainingPct != 78 {
		t.Fatalf("expected disk cache reload, got ok=%v snap=%+v", ok, cached)
	}

	// 7. Worker settled debounce: agy respects 5m debounce.
	advance(1 * time.Minute)
	beforeWorker := agyCalls.Load()
	qm.OnWorkerSettled("antigravity")
	time.Sleep(40 * time.Millisecond)
	if agyCalls.Load() != beforeWorker+1 {
		t.Fatalf("expected first workerSettled to trigger agy refresh, got %d vs %d", agyCalls.Load(), beforeWorker)
	}
	// Second settle 1m later is debounced.
	advance(1 * time.Minute)
	qm.OnWorkerSettled("agy")
	time.Sleep(40 * time.Millisecond)
	if agyCalls.Load() != beforeWorker+1 {
		t.Fatalf("expected second workerSettled within 5m to be debounced, got %d", agyCalls.Load())
	}
}

func TestQuotaHTTPAPIContract(t *testing.T) {
	home := t.TempDir()
	dataDir := t.TempDir()
	s := &Server{
		herdr:    newHerdr(filepath.Join(home, "missing.sock"), "test-host", home),
		token:    "test-secret-token",
		home:     home,
		quotaDir: dataDir,
	}
	qm := newQuotaManager(home, dataDir)
	qm.detect = func() []string { return []string{"agy", "codex"} }
	qm.collectors = map[string]quotaCollectorFunc{
		"agy": func(ctx context.Context) AgentQuota {
			return AgentQuota{
				Kind: "agy", Name: "Antigravity", Plan: "Pro", Status: "ok", Source: "cli",
				UpdatedAt: "2026-11-28T09:20:00Z",
				Pools: []QuotaPool{{
					Group: "Gemini",
					Windows: []QuotaWindow{
						{ID: "5h", Label: "5 小时", RemainingPct: 86},
						{ID: "weekly", Label: "周额度", RemainingPct: 39},
					},
				}},
			}
		},
		"codex": func(ctx context.Context) AgentQuota {
			return AgentQuota{
				Kind: "codex", Name: "Codex", Plan: "Plus", Status: "ok", Source: "app-server",
				UpdatedAt: "2026-11-28T09:20:00Z",
				Pools: []QuotaPool{{
					Windows: []QuotaWindow{
						{ID: "5h", Label: "5 小时窗口", RemainingPct: 82},
					},
				}},
			}
		},
	}
	s.quota = qm
	routes := s.Routes()

	// Unauthenticated request must return 401.
	req := httptest.NewRequest(http.MethodGet, "/api/quota", nil)
	rec := httptest.NewRecorder()
	routes.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 without auth, got %d", rec.Code)
	}

	// Authenticated sync GET returns normalized snapshot.
	req = httptest.NewRequest(http.MethodGet, "/api/quota?sync=1", nil)
	req.Header.Set("Authorization", "Bearer test-secret-token")
	rec = httptest.NewRecorder()
	routes.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 on GET /api/quota, got %d: %s", rec.Code, rec.Body.String())
	}
	var snap QuotaSnapshot
	if err := json.Unmarshal(rec.Body.Bytes(), &snap); err != nil {
		t.Fatalf("invalid JSON response: %v", err)
	}
	if len(snap.Agents) != 2 || snap.Agents[0].Kind != "agy" || snap.Agents[1].Kind != "codex" {
		t.Fatalf("unexpected API snapshot: %+v", snap)
	}

	// POST /api/quota/refresh returns 200 with 30s cooldown.
	req = httptest.NewRequest(http.MethodPost, "/api/quota/refresh", nil)
	req.Header.Set("Authorization", "Bearer test-secret-token")
	rec = httptest.NewRecorder()
	routes.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 on POST /api/quota/refresh, got %d", rec.Code)
	}
	var refreshed QuotaSnapshot
	if err := json.Unmarshal(rec.Body.Bytes(), &refreshed); err != nil {
		t.Fatalf("invalid refresh JSON: %v", err)
	}
	if refreshed.RefreshCooldownSec <= 0 || refreshed.RefreshCooldownSec > 30 {
		t.Fatalf("expected refresh_cooldown_sec in 1..30, got %d", refreshed.RefreshCooldownSec)
	}
}

func TestWebQuotaBadgeContracts(t *testing.T) {
	if _, err := exec.LookPath("node"); err != nil {
		t.Skip("node not installed")
	}
	script := `
const fs = require("fs");
const vm = require("vm");
const assert = require("assert");

const htmlSrc = fs.readFileSync("web/index.html", "utf8");
const cssSrc = fs.readFileSync("web/quota.css", "utf8");
const jsSrc = fs.readFileSync("web/quota.js", "utf8");
const appSrc = fs.readFileSync("web/app.js", "utf8");
const demoSrc = fs.readFileSync("scripts/demo/demo.js", "utf8");

// ─── 1. Static Contracts (Zero height increase, zero mobile header bloat, Modern Web Guidance) ───
const sideFootMatch = htmlSrc.match(/<footer class="side-foot">[\s\S]*?<\/footer>/);
assert.ok(sideFootMatch, ".side-foot must exist");
assert.ok(!sideFootMatch[0].includes("quota-foot-btn"), "must NOT add extra 32px text row in .side-foot (pure icon inside .sf-acts only)");
assert.ok(sideFootMatch[0].includes('id="nav-quota"') && sideFootMatch[0].includes('class="icon-btn sf-btn sf-quota-btn"'), "pure icon button #nav-quota must live inside .sf-acts");

const railFootMatch = htmlSrc.match(/<div class="sr-foot">[\s\S]*?<\/div>\s*<\/nav>/);
assert.ok(railFootMatch && railFootMatch[0].includes('id="rail-quota"'), "pure icon button #rail-quota must live in 54px .sr-foot");

const collieHeadMatch = htmlSrc.match(/<section class="view" id="v-collie"[\s\S]*?<\/header>/);
assert.ok(collieHeadMatch, "#v-collie header must exist");
assert.ok(!collieHeadMatch[0].includes("mob-quota-chip"), "mobile main chat header must have ZERO extra chips (#t14082)");
assert.ok(collieHeadMatch[0].includes('id="collie-more-quota-dot"'), "mobile low-quota indicator must be 5px corner dot on ⋯ button");
assert.ok(collieHeadMatch[0].includes('data-act="quota"') && collieHeadMatch[0].includes('id="menu-open-quota"'), "⋯ menu must expose 订阅额度 shortcut");

assert.ok(htmlSrc.includes('id="quota-popover"') && htmlSrc.includes('popover="auto"'), "native popover=auto container must exist");
assert.ok(htmlSrc.includes('<link rel="stylesheet" href="/quota.css">') && htmlSrc.includes('<script src="/quota.js"></script>'), "standalone quota.css and quota.js must be linked in web/index.html");

// Modern Web Guidance checks
assert.ok(cssSrc.includes("@property --q-val"), "ring animation must register @property --q-val");
assert.ok(cssSrc.includes("background-clip: border-area") && cssSrc.includes("@supports not (background-clip: border-area)"), "ring must use border-area with mask-image fallback");
assert.ok(cssSrc.includes("forced-colors: active") && cssSrc.includes("forced-color-adjust: none"), "ring must support forced-colors mode");
assert.ok(cssSrc.includes("@supports (position-area: block-start)"), "anchor positioning inset:auto must be gated inside @supports");
assert.ok(cssSrc.includes("transition-behavior: allow-discrete") && cssSrc.includes("overlay") && cssSrc.includes("@starting-style"), "top-layer popover must use allow-discrete + overlay + @starting-style");
assert.ok(cssSrc.includes("progress.q-bar::-webkit-progress-value") && cssSrc.includes("progress.q-bar::-moz-progress-bar"), "progress bar pseudo-elements must be styled separately");
assert.ok(!cssSrc.includes("::-webkit-progress-value,") && !cssSrc.includes("::-moz-progress-bar,"), "never group -webkit- and -moz- progress pseudo-elements");
assert.ok(!htmlSrc.includes('role="menu"') && !htmlSrc.includes('aria-haspopup="menu"'), "popover trigger/panel must not falsely claim ARIA menu semantics");

// Minimal bridge in app.js & demo.js sync
assert.ok(appSrc.includes('ev.addEventListener("quota"'), "app.js must bridge SSE quota event");
assert.ok(appSrc.includes('window.__collieQuota?.init?.'), "app.js must initialize quota module on boot");
assert.ok(appSrc.includes('getContext: resolveActiveQuotaContext') && appSrc.includes('window.__collieQuota?.onContextChange?.()'), "app.js must bridge active agent/model context to quota module");
assert.ok(demoSrc.includes('"/api/quota"') && demoSrc.includes('"/api/quota/refresh"'), "demo.js must mock /api/quota and /api/quota/refresh");

// i18n bilingual parity for quota keys
const i18nSrc = fs.readFileSync("web/i18n.js", "utf8");
for (const k of ["quota.activeMinTag", "quota.minTag", "quota.progressBottleneck"]) {
  const count = i18nSrc.split('"' + k + '":').length - 1;
  assert.strictEqual(count, 2, "i18n.js must define both zh and en for " + k);
}

// ─── 2. Runtime VM Tests for web/quota.js ───
function makeClassList() {
  const set = new Set();
  return {
    add: (...cs) => cs.forEach((c) => c && set.add(c)),
    remove: (...cs) => cs.forEach((c) => set.delete(c)),
    toggle: (c, f) => { const on = f === undefined ? !set.has(c) : !!f; if (on) set.add(c); else set.delete(c); return on; },
    contains: (c) => set.has(c),
    toString: () => [...set].join(" "),
  };
}
function makeEl(id = "") {
  const listeners = new Map(), attrs = new Map(), styles = new Map();
  let popOpen = false;
  const el = {
    id, textContent: "", innerHTML: "", title: "", hidden: false, disabled: false,
    classList: makeClassList(),
    style: {
      setProperty(k, v) { styles.set(k, String(v)); },
      getPropertyValue(k) { return styles.get(k) || ""; },
      left: "", right: "", top: "", bottom: "",
    },
    addEventListener(type, fn) { if (!listeners.has(type)) listeners.set(type, []); listeners.get(type).push(fn); },
    dispatch(type, ev = {}) {
      const e = { type, target: el, stopPropagation() {}, preventDefault() {}, ...ev };
      for (const fn of [...(listeners.get(type) || [])]) fn(e);
    },
    setAttribute(k, v) { attrs.set(k, String(v)); },
    getAttribute(k) { return attrs.get(k) ?? ""; },
    matches(sel) { return sel === ":popover-open" ? popOpen : false; },
    showPopover() { popOpen = true; el.dispatch("toggle", { newState: "open" }); },
    hidePopover() { popOpen = false; el.dispatch("toggle", { newState: "closed" }); },
    getBoundingClientRect() { return { left: 160, right: 192, top: 840, bottom: 872, width: 32, height: 32 }; },
    focus() {},
    contains() { return false; },
  };
  return el;
}
const els = new Map();
const getEl = (sel) => { if (!els.has(sel)) els.set(sel, makeEl(sel)); return els.get(sel); };
let narrowMode = false;
const docListeners = new Map();
const doc = {
  querySelector: (sel) => getEl(sel),
  querySelectorAll: () => [],
  createElement: (t) => makeEl(t),
  body: { append() {} },
  addEventListener(t, fn) { if (!docListeners.has(t)) docListeners.set(t, []); docListeners.get(t).push(fn); },
  dispatch(type, ev = {}) {
    const e = { type, target: null, stopPropagation() {}, preventDefault() {}, ...ev };
    for (const fn of [...(docListeners.get(type) || [])]) fn(e);
  },
};
const winListeners = new Map();
const ctx = {
  console, Math, Date, JSON, Set, Map, Array, Object, Number, String, Boolean, URL, Promise,
  setInterval: () => 1, clearInterval() {}, setTimeout: (fn) => { fn(); return 1; }, clearTimeout() {},
  CSS: { supports: (q) => q === "position-area: block-start" },
  matchMedia: () => ({ matches: narrowMode }),
  document: doc,
};
ctx.window = {
  innerWidth: 1440, innerHeight: 900,
  addEventListener(t, fn) { if (!winListeners.has(t)) winListeners.set(t, []); winListeners.get(t).push(fn); },
  dispatch(type, ev = {}) {
    const e = { type, ...ev };
    for (const fn of [...(winListeners.get(type) || [])]) fn(e);
  },
};
vm.createContext(ctx);
vm.runInContext(jsSrc, ctx);

const Q = ctx.window.__collieQuota;
assert.ok(Q, "window.__collieQuota must be registered");

const nowMs = Date.parse("2026-11-28T10:00:00Z");
const normalSnap = {
  updated_at: "2026-11-28T09:58:00Z",
  agents: [
    { kind: "agy", name: "Antigravity", plan: "Pro", status: "ok", pools: [
      { group: "Gemini", windows: [
        { id: "5h", label: "5 小时", remaining_pct: 88, resets_at: "2026-11-28T12:30:00Z" },
        { id: "weekly", label: "周额度", remaining_pct: 78, resets_at: "2026-12-03T10:00:00Z" },
      ]},
      { group: "Claude / GPT", compact_full: true, windows: [{ id: "5h", label: "5 小时", remaining_pct: 100 }] },
    ]},
    { kind: "codex", name: "Codex", plan: "Plus", status: "ok", pools: [
      { group: "", windows: [{ id: "5h", label: "5 小时窗口", remaining_pct: 62, resets_at: "2026-11-28T13:15:00Z" }] },
    ]},
    { kind: "claude", name: "Claude Code", plan: "Max", status: "optin", note: "按对话被动同步 · 未开启状态行采集" },
    { kind: "cursor", name: "Cursor", plan: "CLI 已登录", status: "external", note: "CLI 未开放实时额度接口", link_text: "控制台 ↗", link_url: "https://cursor.com/settings" },
  ],
};

// Pure helper assertions
const sumNorm = Q.summarizeQuota(normalSnap, nowMs);
assert.strictEqual(sumNorm.level, "normal");
assert.strictEqual(sumNorm.min, 62);
assert.strictEqual(sumNorm.tooltip, "订阅额度 · agy 78% · codex 62%");
assert.strictEqual(Q.formatResetCountdown("2026-11-28T12:30:00Z", 88, nowMs), "2h 30m 后重置");
assert.strictEqual(Q.formatResetCountdown("2026-11-28T09:00:00Z", 10, nowMs), "已到重置点，待刷新");
assert.strictEqual(Q.formatUpdatedAgo("2026-11-28T09:58:00Z", nowMs), "2 分钟前");

// Init & normal state UI checks (quiet grayscale, dots hidden)
const calls = [];
Q.init({
  api: async (path, opts) => {
    calls.push({ path, method: opts?.method || "GET" });
    if (path === "/api/quota/refresh") return { ...normalSnap, updated_at: "2026-11-28T10:00:00Z", refresh_cooldown_sec: 30 };
    return normalSnap;
  },
  toast() {},
});
Q.onEvent(normalSnap);
assert.strictEqual(getEl("#nav-quota-dot").hidden, true, "normal state must hide footer dot");
assert.strictEqual(getEl("#rail-quota-dot").hidden, true, "normal state must hide rail dot");
assert.strictEqual(getEl("#collie-more-quota-dot").hidden, true, "normal state must hide mobile ⋯ dot");
assert.strictEqual(getEl("#nav-quota-ring").classList.contains("is-low"), false);
assert.strictEqual(getEl("#nav-quota-ring").style.getPropertyValue("--q-val"), "62");
assert.strictEqual(getEl("#nav-quota").title, "订阅额度 · agy 78% · codex 62%");

// Low quota (<20%) turns ring amber and reveals corner dots (including 5px mobile ⋯ dot)
const lowSnap = {
  updated_at: "2026-11-28T09:59:00Z",
  agents: [
    { kind: "agy", name: "Antigravity", status: "ok", pools: [
      { group: "Gemini", windows: [{ id: "5h", label: "5 小时", remaining_pct: 14, resets_at: "2026-11-28T11:00:00Z" }] },
    ]},
  ],
};
Q.onEvent(lowSnap);
assert.strictEqual(getEl("#nav-quota-ring").classList.contains("is-low"), true);
assert.strictEqual(getEl("#nav-quota-dot").hidden, false);
assert.strictEqual(getEl("#rail-quota-dot").hidden, false);
assert.strictEqual(getEl("#collie-more-quota-dot").hidden, false);
assert.strictEqual(getEl("#menu-quota-sub").textContent, "最低 14%");

// Open desktop popover from #nav-quota -> anchored upward (pos-anchor-full)
Q.onEvent(normalSnap);
narrowMode = false;
Q.open(getEl("#nav-quota"));
const pop = getEl("#quota-popover");
assert.strictEqual(pop.matches(":popover-open"), true);
assert.strictEqual(pop.classList.contains("pos-anchor-full"), true);
assert.ok(getEl("#qp-body").innerHTML.includes("Claude / GPT 池 · 100% 满额"));
assert.ok(getEl("#qp-body").innerHTML.includes("https://cursor.com/settings"));

// Open from 54px rail -> anchored rightward (pos-anchor-rail)
Q.open(getEl("#rail-quota"));
assert.strictEqual(pop.classList.contains("pos-anchor-rail"), true);

// Open on mobile -> bottom sheet (is-sheet)
narrowMode = true;
Q.open(getEl("#nav-quota"));
assert.strictEqual(pop.classList.contains("is-sheet"), true);

// XSS / javascript: link URL guard
Q.onEvent({
  updated_at: "2026-11-28T10:00:00Z",
  agents: [{ kind: "cursor", name: "<img src=x onerror=1>", status: "external", note: "<b>test</b>", link_text: "bad", link_url: "javascript:alert(1)" }],
});
const htmlOut = getEl("#qp-body").innerHTML;
assert.ok(!htmlOut.includes("javascript:alert"), "unsafe link_url must be rejected");
assert.ok(!htmlOut.includes("<img src=x"), "agent name must be HTML escaped");

// ─── 3. Model-Aware Bottleneck Selection & Live Switching Regressions ───
const multiSnap = {
  updated_at: "2026-11-28T09:59:00Z",
  agents: [
    {
      kind: "agy", name: "Antigravity", plan: "Pro", status: "ok",
      pools: [
        {
          group: "Gemini",
          windows: [
            { id: "5h", label: "5 小时", remaining_pct: 86, resets_at: "2026-11-28T12:30:00Z" },
            { id: "weekly", label: "周额度", remaining_pct: 39, resets_at: "2026-12-03T10:00:00Z" },
          ],
        },
        {
          group: "Claude / GPT", compact_full: true,
          windows: [
            { id: "5h", label: "5 小时", remaining_pct: 100 },
            { id: "weekly", label: "周额度", remaining_pct: 100 },
          ],
        },
      ],
    },
    {
      kind: "codex", name: "Codex", plan: "Plus", status: "ok",
      pools: [
        {
          group: "",
          windows: [
            { id: "5h", label: "5 小时窗口", remaining_pct: 72, resets_at: "2026-11-28T13:15:00Z" },
            { id: "weekly", label: "周额度", remaining_pct: 55, resets_at: "2026-12-02T10:00:00Z" },
          ],
        },
        {
          group: "GPT-5.3-Codex-Spark",
          models: ["gpt-5.3-codex-spark"],
          windows: [
            { id: "5h", label: "5 小时窗口", remaining_pct: 8, resets_at: "2026-11-28T11:30:00Z" },
            { id: "weekly", label: "周额度", remaining_pct: 90, resets_at: "2026-12-02T10:00:00Z" },
          ],
        },
      ],
    },
    {
      kind: "claude", name: "Claude Code", plan: "Max", status: "ok",
      pools: [
        {
          group: "",
          windows: [
            { id: "5h", label: "5 小时窗口", remaining_pct: 10, resets_at: "2026-11-28T12:00:00Z" },
            { id: "weekly", label: "7 天窗口", remaining_pct: 80, resets_at: "2026-12-04T10:00:00Z" },
          ],
        },
      ],
    },
  ],
};

// 3a. Antigravity Gemini model (with -xhigh effort suffix) picks Gemini min(86, 39) = 39%, ignoring codex 8% and claude 10%
const sumAgyGem = Q.summarizeQuota(multiSnap, nowMs, { kind: "antigravity", model: "gemini-4-neon-xhigh" });
assert.strictEqual(sumAgyGem.min, 39);
assert.strictEqual(sumAgyGem.level, "normal");
assert.strictEqual(sumAgyGem.scope, "model");
assert.strictEqual(sumAgyGem.bottleneck.kind, "agy");
assert.strictEqual(sumAgyGem.bottleneck.poolIndex, 0);
assert.strictEqual(sumAgyGem.bottleneck.winIndex, 1);
assert.ok(sumAgyGem.tooltip.includes("agy") && sumAgyGem.tooltip.includes("Gemini") && sumAgyGem.tooltip.includes("39%"));

// 3b. Antigravity Claude/GPT model (with -medium suffix) picks Claude / GPT pool = 100%
const sumAgyClaude = Q.summarizeQuota(multiSnap, nowMs, { kind: "agy", model: "claude-opus-5-5-medium" });
assert.strictEqual(sumAgyClaude.min, 100);
assert.strictEqual(sumAgyClaude.level, "normal");
assert.strictEqual(sumAgyClaude.scope, "model");
assert.strictEqual(sumAgyClaude.bottleneck.poolIndex, 1);
assert.strictEqual(sumAgyClaude.bottleneck.compact, true);

const sumAgyGpt = Q.summarizeQuota(multiSnap, nowMs, { kind: "agy", model: "gpt-oss-120b-medium" });
assert.strictEqual(sumAgyGpt.min, 100);
assert.strictEqual(sumAgyGpt.bottleneck.poolIndex, 1);

// 3c. Per-model pools: exact / effort-stripped / same-series prefix / token subset matching
const perModelSnap = {
  updated_at: "2026-11-28T09:59:00Z",
  agents: [
    {
      kind: "agy", name: "Antigravity", status: "ok",
      pools: [
        { group: "Gemini 4 Neon", windows: [{ id: "5h", label: "5 小时", remaining_pct: 60 }, { id: "weekly", label: "周额度", remaining_pct: 18 }] },
        { group: "Gemini 4", windows: [{ id: "5h", label: "5 小时", remaining_pct: 75 }, { id: "weekly", label: "周额度", remaining_pct: 70 }] },
        { group: "Gemini 3.8 Flash", windows: [{ id: "5h", label: "5 小时", remaining_pct: 95 }] },
        { group: "Claude Opus", windows: [{ id: "5h", label: "5 小时", remaining_pct: 42 }] },
      ],
    },
  ],
};
assert.strictEqual(Q.normalizeModelKey("anthropic/claude-4.6-opus-max"), "claude-4-6-opus");
assert.strictEqual(Q.normalizeModelKey("gemini-4-neon-xhigh"), "gemini-4-neon");
assert.strictEqual(Q.normalizeModelKey("openai/gpt-5.4:high"), "gpt-5-4");

const mNeon = Q.summarizeQuota(perModelSnap, nowMs, { kind: "agy", model: "gemini-4-neon-xhigh" });
assert.strictEqual(mNeon.min, 18, "effort-stripped exact match Gemini 4 Neon must beat broader Gemini 4");
assert.strictEqual(mNeon.level, "low");
assert.strictEqual(mNeon.bottleneck.poolIndex, 0);
assert.strictEqual(mNeon.bottleneck.winIndex, 1);

const mCarbon = Q.summarizeQuota(perModelSnap, nowMs, { kind: "agy", model: "gemini-4-carbon-high" });
assert.strictEqual(mCarbon.min, 70, "same-series prefix must match Gemini 4");
assert.strictEqual(mCarbon.bottleneck.poolIndex, 1);

const mFlash = Q.summarizeQuota(perModelSnap, nowMs, { kind: "agy", model: "gemini-3.8-flash-low" });
assert.strictEqual(mFlash.min, 95, "dot/dash normalized exact match must match Gemini 3.8 Flash");
assert.strictEqual(mFlash.bottleneck.poolIndex, 2);

const mOpus = Q.summarizeQuota(perModelSnap, nowMs, { kind: "agy", model: "claude-4.6-opus-max" });
assert.strictEqual(mOpus.min, 42, "token subset match must match Claude Opus");
assert.strictEqual(mOpus.bottleneck.poolIndex, 3);

// 3d. Unknown model under active agent falls back to min across all valid pools of that agent
const mUnknown = Q.summarizeQuota(multiSnap, nowMs, { kind: "agy", model: "experimental-v1-high" });
assert.strictEqual(mUnknown.min, 39, "unmatched model must fall back to current agent's all pools min (not codex/claude)");
assert.strictEqual(mUnknown.scope, "agent");
assert.strictEqual(mUnknown.bottleneck.kind, "agy");

// 3e. Claude 5h + 7d multi-window: short-window bottleneck 10% must NOT be masked by 7d 80%
const sumClaude = Q.summarizeQuota(multiSnap, nowMs, { kind: "claude", model: "sonnet" });
assert.strictEqual(sumClaude.min, 10);
assert.strictEqual(sumClaude.level, "low");
assert.strictEqual(sumClaude.bottleneck.kind, "claude");
assert.strictEqual(sumClaude.bottleneck.winIndex, 0);

// 3f. Mixed unscoped 5h + model-scoped 7d windows in one pool
const mixedClaudeSnap = {
  updated_at: "2026-11-28T09:59:00Z",
  agents: [{
    kind: "claude", name: "Claude Code", status: "ok",
    pools: [{
      group: "",
      windows: [
        { id: "5h", label: "5 小时窗口", remaining_pct: 45 },
        { id: "7d-sonnet", label: "Sonnet 7 天", remaining_pct: 15 },
        { id: "7d-opus", label: "Opus 7 天", remaining_pct: 90 },
      ],
    }],
  }],
};
assert.strictEqual(Q.summarizeQuota(mixedClaudeSnap, nowMs, { kind: "claude", model: "sonnet" }).min, 15);
assert.strictEqual(Q.summarizeQuota(mixedClaudeSnap, nowMs, { kind: "claude", model: "claude-opus-4-6" }).min, 45);

// 3g. Codex default pool vs model-specific pool
const sumCodexSpark = Q.summarizeQuota(multiSnap, nowMs, { kind: "codex", model: "gpt-5.3-codex-spark" });
assert.strictEqual(sumCodexSpark.min, 8);
assert.strictEqual(sumCodexSpark.level, "low");
assert.strictEqual(sumCodexSpark.bottleneck.poolIndex, 1);

const sumCodexMain = Q.summarizeQuota(multiSnap, nowMs, { kind: "codex", model: "gpt-5.4" });
assert.strictEqual(sumCodexMain.min, 55, "non-spark codex model must use default codex pool min(72, 55)=55, not spark 8%");
assert.strictEqual(sumCodexMain.level, "normal");
assert.strictEqual(sumCodexMain.bottleneck.poolIndex, 0);
assert.strictEqual(sumCodexMain.bottleneck.winIndex, 1);

// 3h. Fallback when active agent has no numeric quota (optin/external/untracked)
const sumFallbackOptin = Q.summarizeQuota(normalSnap, nowMs, { kind: "claude", model: "sonnet" });
assert.strictEqual(sumFallbackOptin.min, 62, "optin active agent must safely fall back to global numeric min");
assert.strictEqual(sumFallbackOptin.scope, "fallback");

const sumFallbackUntracked = Q.summarizeQuota(normalSnap, nowMs, { kind: "doubao", model: "Auto" });
assert.strictEqual(sumFallbackUntracked.min, 62);
assert.strictEqual(sumFallbackUntracked.scope, "fallback");

// 3i. Live switching via setContext / onContextChange updates ring, warning dots, and open popover highlight immediately
narrowMode = false;
Q.onEvent(multiSnap);
Q.close();
Q.open(getEl("#nav-quota"));
assert.strictEqual(pop.matches(":popover-open"), true, "popover must be open for live switching test");
Q.setContext({ kind: "agy", model: "gemini-4-neon-xhigh", source: "router" });
assert.strictEqual(getEl("#nav-quota-ring").style.getPropertyValue("--q-val"), "39");
assert.strictEqual(getEl("#nav-quota-dot").hidden, true);
assert.ok(getEl("#qp-body").innerHTML.includes('data-quota-bottleneck="true"'), "popover must mark bottleneck bucket");
assert.ok(getEl("#qp-body").innerHTML.includes("当前最低"), "popover must label active bottleneck");
assert.ok(getEl("#qp-body").innerHTML.includes("gemini-4-neon-xhigh"), "active agent header must reflect current model");

// Switch to Claude (5h = 10% -> amber warning dot turns on, bottleneck shifts to Claude 5h)
Q.setContext({ kind: "claude", model: "sonnet", source: "dispatch" });
assert.strictEqual(getEl("#nav-quota-ring").style.getPropertyValue("--q-val"), "10");
assert.strictEqual(getEl("#nav-quota-ring").classList.contains("is-low"), true);
assert.strictEqual(getEl("#nav-quota-dot").hidden, false);
assert.strictEqual(getEl("#menu-quota-sub").textContent, "最低 10%");

// Switch to Antigravity Claude/GPT model (compact_full 100% -> warning clears, compact pool highlighted)
Q.setContext({ kind: "agy", model: "claude-opus-5-5-high", source: "dispatch" });
assert.strictEqual(getEl("#nav-quota-ring").style.getPropertyValue("--q-val"), "100");
assert.strictEqual(getEl("#nav-quota-ring").classList.contains("is-low"), false);
assert.strictEqual(getEl("#nav-quota-dot").hidden, true);
assert.ok(getEl("#qp-body").innerHTML.includes('class="qm-compact is-bottleneck"'), "matched compact_full pool must be highlighted");

// Expired window inside matched pool is ignored
const expiredSnap = {
  updated_at: "2026-11-28T09:59:00Z",
  agents: [{
    kind: "agy", name: "Antigravity", status: "ok",
    pools: [{
      group: "Gemini",
      windows: [
        { id: "5h", label: "5 小时", remaining_pct: 0, resets_at: "2026-11-28T09:30:00Z" },
        { id: "weekly", label: "周额度", remaining_pct: 48, resets_at: "2026-12-03T10:00:00Z" },
      ],
    }],
  }],
};
const sumExp = Q.summarizeQuota(expiredSnap, nowMs, { kind: "agy", model: "gemini-4-neon-high" });
assert.strictEqual(sumExp.min, 48, "expired 0% window must be skipped");

// ─── 4. Native popover="auto" Light-Dismiss + Click Toggle Race Regressions ───
const navBtn = getEl("#nav-quota");
const railBtn = getEl("#rail-quota");
const outsideEl = makeEl("#chat-area");
narrowMode = false;
Q.onEvent(normalSnap);
Q.close();

// Helper simulating browser native light-dismiss sequence on pointer click:
// pointerdown (popover still open) -> browser light-dismiss hides popover -> pointerup -> click
function pointerClick(btn, pointerType = "mouse") {
  btn.dispatch("pointerdown", { pointerType, button: 0, isPrimary: true, target: btn });
  if (pop.matches(":popover-open")) pop.hidePopover();
  doc.dispatch("pointerup", { pointerType, button: 0, target: btn });
  btn.dispatch("click", { detail: 1, target: btn });
}

// 4a. #nav-quota desktop mouse: click opens -> click again cleanly closes (no flash/reopen) -> click again opens
pointerClick(navBtn, "mouse");
assert.strictEqual(pop.matches(":popover-open"), true, "#nav-quota first click must open popover");
assert.strictEqual(navBtn.getAttribute("aria-expanded"), "true");

pointerClick(navBtn, "mouse");
assert.strictEqual(pop.matches(":popover-open"), false, "#nav-quota second click after native light-dismiss must stay closed");
assert.strictEqual(navBtn.getAttribute("aria-expanded"), "false");

pointerClick(navBtn, "mouse");
assert.strictEqual(pop.matches(":popover-open"), true, "#nav-quota third click must reopen cleanly");

// 4b. Clicking the other trigger (#rail-quota) while #nav-quota opened it must switch cleanly to #rail-quota
pointerClick(railBtn, "touch");
assert.strictEqual(pop.matches(":popover-open"), true, "clicking #rail-quota while open on #nav-quota must switch anchor and stay open");
assert.strictEqual(pop.classList.contains("pos-anchor-rail"), true);
assert.strictEqual(railBtn.getAttribute("aria-expanded"), "true");
assert.strictEqual(navBtn.getAttribute("aria-expanded"), "false");

// 4c. Tapping #rail-quota again (touch / iPad) closes cleanly without reopening
pointerClick(railBtn, "touch");
assert.strictEqual(pop.matches(":popover-open"), false, "#rail-quota second tap after native light-dismiss must stay closed");
assert.strictEqual(railBtn.getAttribute("aria-expanded"), "false");

// 4d. Keyboard Enter / Space (no pointerdown) toggles cleanly on both triggers
navBtn.dispatch("keydown", { key: "Enter" });
navBtn.dispatch("click", { detail: 0 });
assert.strictEqual(pop.matches(":popover-open"), true, "Enter on #nav-quota must open");
navBtn.dispatch("keydown", { key: "Enter" });
navBtn.dispatch("click", { detail: 0 });
assert.strictEqual(pop.matches(":popover-open"), false, "Enter again on #nav-quota must close");

railBtn.dispatch("keydown", { key: " " });
railBtn.dispatch("click", { detail: 0 });
assert.strictEqual(pop.matches(":popover-open"), true, "Space on #rail-quota must open");
railBtn.dispatch("keydown", { key: " " });
railBtn.dispatch("click", { detail: 0 });
assert.strictEqual(pop.matches(":popover-open"), false, "Space again on #rail-quota must close");

// 4e. Abandoned pointerdown (drag-off, pointercancel, right-click, window blur) must not swallow next open
pointerClick(navBtn, "mouse");
assert.strictEqual(pop.matches(":popover-open"), true);
navBtn.dispatch("pointerdown", { pointerType: "mouse", button: 0, isPrimary: true, target: navBtn });
pop.hidePopover();
doc.dispatch("pointerup", { pointerType: "mouse", button: 0, target: outsideEl });
// Next keyboard activation without pointerdown must open immediately
navBtn.dispatch("keydown", { key: "Enter" });
navBtn.dispatch("click", { detail: 0 });
assert.strictEqual(pop.matches(":popover-open"), true, "dragged-off pointerdown must not leave stale close intent");

// pointercancel clears pending close intent
navBtn.dispatch("pointerdown", { pointerType: "touch", isPrimary: true, target: navBtn });
pop.hidePopover();
doc.dispatch("pointercancel", {});
navBtn.dispatch("click", { detail: 0 });
assert.strictEqual(pop.matches(":popover-open"), true, "pointercancel must clear pending close intent");

// right-click does not arm close intent
navBtn.dispatch("pointerdown", { pointerType: "mouse", button: 2, isPrimary: true, target: navBtn });
pop.hidePopover();
navBtn.dispatch("click", { detail: 0 });
assert.strictEqual(pop.matches(":popover-open"), true, "right-click pointerdown must not arm close intent");

// window blur clears pending close intent
navBtn.dispatch("pointerdown", { pointerType: "mouse", button: 0, isPrimary: true, target: navBtn });
pop.hidePopover();
ctx.window.dispatch("blur", {});
navBtn.dispatch("click", { detail: 0 });
assert.strictEqual(pop.matches(":popover-open"), true, "window blur must clear pending close intent");

// 4f. Outside click light-dismiss followed by clicking trigger reopens cleanly
pop.hidePopover();
assert.strictEqual(pop.matches(":popover-open"), false);
pointerClick(navBtn, "mouse");
assert.strictEqual(pop.matches(":popover-open"), true, "click after outside light-dismiss must reopen");

// 4g. Fallback browser (no native light-dismiss between pointerdown and click) still closes cleanly
navBtn.dispatch("pointerdown", { pointerType: "mouse", button: 0, isPrimary: true, target: navBtn });
navBtn.dispatch("click", { detail: 1, target: navBtn });
assert.strictEqual(pop.matches(":popover-open"), false, "click without prior native light-dismiss must still close");
assert.strictEqual(navBtn.getAttribute("aria-expanded"), "false");

console.log("quota web contracts ok");
`
	cmd := exec.Command("node", "-e", script)
	cmd.Dir = filepath.Join("..", "..")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("quota web contract tests failed: %v\n%s", err, string(out))
	}
}
