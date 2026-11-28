package collie

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Account-level subscription quota collection & caching.
//
// Design guardrails:
//   - Zero model token consumption: never invokes inference endpoints or creates
//     agent conversations.
//   - Zero mutation of user agent configs (~/.claude/settings.json, ~/.codex/config.toml).
//   - Zero scraping of private OAuth APIs or credential/PII leakage (emails,
//     account IDs, org IDs, tokens, and local paths are never persisted or emitted).
//   - Zero idle polling: refreshes only on demand (GET /api/quota when stale > 3m),
//     explicit manual refresh (POST /api/quota/refresh with 30s cooldown), or
//     debounced workerSettled events.

const (
	quotaCacheTTL         = 3 * time.Minute
	quotaRefreshCooldown  = 30 * time.Second
	quotaWorkerDebounce   = 5 * time.Minute
	quotaCLITimeout       = 10 * time.Second
	quotaStatusTimeout    = 8 * time.Second
	quotaCursorTimeout    = 5 * time.Second
	quotaCursorConsoleURL = "https://cursor.com/settings"
)

var quotaAgentOrder = []string{"agy", "codex", "claude", "cursor"}

// QuotaSnapshot is the normalized, PII-free response returned by GET /api/quota,
// POST /api/quota/refresh, and pushed over /api/events ("event: quota").
type QuotaSnapshot struct {
	UpdatedAt          string       `json:"updated_at,omitempty"`
	Stale              bool         `json:"stale,omitempty"`
	Refreshing         bool         `json:"refreshing,omitempty"`
	RefreshCooldownSec int          `json:"refresh_cooldown_sec,omitempty"`
	Agents             []AgentQuota `json:"agents"`
}

// AgentQuota describes one installed agent's normalized subscription quota state.
type AgentQuota struct {
	Kind      string      `json:"kind"`
	Name      string      `json:"name"`
	Plan      string      `json:"plan,omitempty"`
	Status    string      `json:"status"` // "ok" | "degraded" | "optin" | "external" | "error"
	Source    string      `json:"source,omitempty"`
	UpdatedAt string      `json:"updated_at,omitempty"`
	Note      string      `json:"note,omitempty"`
	LinkText  string      `json:"link_text,omitempty"`
	LinkURL   string      `json:"link_url,omitempty"`
	Pools     []QuotaPool `json:"pools,omitempty"`
}

// QuotaPool groups related rolling windows (e.g. Antigravity Gemini vs Claude/GPT).
type QuotaPool struct {
	Group       string        `json:"group,omitempty"`
	CompactFull bool          `json:"compact_full,omitempty"`
	Windows     []QuotaWindow `json:"windows"`
}

// QuotaWindow is one rolling quota window normalized to remaining percentage (0..100).
type QuotaWindow struct {
	ID           string  `json:"id"`
	Label        string  `json:"label"`
	RemainingPct int     `json:"remaining_pct"`
	ResetsAt     *string `json:"resets_at,omitempty"`
}

type quotaCollectorFunc func(ctx context.Context) AgentQuota

type quotaFlight struct {
	done chan struct{}
}

type quotaManager struct {
	mu              sync.Mutex
	home            string
	cacheDir        string
	codexHome       string
	claudeConfigDir string
	now             func() time.Time
	detect          func() []string
	collectors      map[string]quotaCollectorFunc
	ttl             time.Duration
	cooldown        time.Duration
	workerDebounce  time.Duration

	loaded            bool
	snapshot          QuotaSnapshot
	fetchedAt         time.Time
	lastManualAt      time.Time
	lastWorkerRefresh map[string]time.Time
	inFlight          *quotaFlight
	agentFlights      map[string]*quotaFlight
	subs              map[chan struct{}]struct{}
}

func newQuotaManager(home, dataDir string) *quotaManager {
	cacheDir := ""
	if dataDir != "" {
		cacheDir = filepath.Join(dataDir, "cache")
	}
	qm := &quotaManager{
		home:              home,
		cacheDir:          cacheDir,
		codexHome:         envOr("CODEX_HOME", filepath.Join(home, ".codex")),
		claudeConfigDir:   envOr("CLAUDE_CONFIG_DIR", filepath.Join(home, ".claude")),
		now:               time.Now,
		ttl:               quotaCacheTTL,
		cooldown:          quotaRefreshCooldown,
		workerDebounce:    quotaWorkerDebounce,
		snapshot:          QuotaSnapshot{Agents: []AgentQuota{}},
		lastWorkerRefresh: map[string]time.Time{},
		agentFlights:      map[string]*quotaFlight{},
		subs:              map[chan struct{}]struct{}{},
	}
	qm.detect = func() []string {
		return detectQuotaAgents(qm.home)
	}
	qm.collectors = map[string]quotaCollectorFunc{
		"agy":    qm.collectAgy,
		"codex":  qm.collectCodex,
		"claude": qm.collectClaude,
		"cursor": qm.collectCursor,
	}
	return qm
}

func detectQuotaAgents(home string) []string {
	var out []string
	for _, k := range quotaAgentOrder {
		if findBin(binForKind(k), home) != "" {
			out = append(out, k)
		}
	}
	return out
}

func (s *Server) quotaMgr() *quotaManager {
	if s == nil {
		return nil
	}
	s.quotaOnce.Do(func() {
		if s.quota == nil {
			dir := s.quotaDir
			if dir == "" && s.sup != nil {
				dir = s.sup.dir
			}
			s.quota = newQuotaManager(s.home, dir)
		}
	})
	return s.quota
}

// ---------------------------------------------------------------- HTTP & SSE hooks

func (s *Server) getQuota(w http.ResponseWriter, r *http.Request) {
	qm := s.quotaMgr()
	if qm == nil {
		writeJSON(w, 200, QuotaSnapshot{Agents: []AgentQuota{}})
		return
	}
	syncMode := r.URL.Query().Get("sync") == "1"
	snap := qm.Get(r.Context(), syncMode)
	writeJSON(w, 200, snap)
}

func (s *Server) refreshQuota(w http.ResponseWriter, r *http.Request) {
	qm := s.quotaMgr()
	if qm == nil {
		writeJSON(w, 200, QuotaSnapshot{Agents: []AgentQuota{}})
		return
	}
	snap := qm.Refresh(r.Context())
	writeJSON(w, 200, snap)
}

func (s *Server) onWorkerSettledQuota(kind string) {
	if s == nil || s.quota == nil {
		return
	}
	s.quota.OnWorkerSettled(kind)
}

// subscribe returns a channel notified whenever the cached quota snapshot changes.
func (qm *quotaManager) subscribe() (<-chan struct{}, func()) {
	ch := make(chan struct{}, 1)
	qm.mu.Lock()
	if qm.subs == nil {
		qm.subs = map[chan struct{}]struct{}{}
	}
	qm.subs[ch] = struct{}{}
	qm.mu.Unlock()
	return ch, func() {
		qm.mu.Lock()
		delete(qm.subs, ch)
		qm.mu.Unlock()
	}
}

func (qm *quotaManager) notifyLocked() {
	for ch := range qm.subs {
		select {
		case ch <- struct{}{}:
		default:
		}
	}
}

// CachedSnapshot returns the currently cached snapshot without triggering any
// refresh or subprocesses (used by SSE streamState so idle connections never poll).
func (qm *quotaManager) CachedSnapshot() (QuotaSnapshot, bool) {
	qm.mu.Lock()
	defer qm.mu.Unlock()
	qm.ensureLoadedLocked()
	if qm.snapshot.UpdatedAt == "" && len(qm.snapshot.Agents) == 0 {
		return QuotaSnapshot{}, false
	}
	return qm.viewLocked(qm.now()), true
}

// Get returns the current cached snapshot immediately (0ms non-blocking).
// When the cache is missing or stale (> 3m or past a window reset), it starts a
// background singleflight refresh unless syncMode is true.
func (qm *quotaManager) Get(ctx context.Context, syncMode bool) QuotaSnapshot {
	qm.mu.Lock()
	qm.ensureLoadedLocked()
	now := qm.now()
	stale := qm.isStaleLocked(now)
	if !stale {
		snap := qm.viewLocked(now)
		qm.mu.Unlock()
		return snap
	}
	if syncMode {
		qm.mu.Unlock()
		return qm.runAllSingleflight(ctx)
	}
	if qm.inFlight == nil {
		fl := &quotaFlight{done: make(chan struct{})}
		qm.inFlight = fl
		go func() {
			bgCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			qm.executeAllRefresh(bgCtx, fl)
		}()
	}
	snap := qm.viewLocked(now)
	snap.Stale = true
	snap.Refreshing = true
	qm.mu.Unlock()
	return snap
}

// Refresh forces a singleflight refresh subject to the 30s manual cooldown.
// Within cooldown, returns the current cached snapshot with refresh_cooldown_sec
// without spawning subprocesses.
func (qm *quotaManager) Refresh(ctx context.Context) QuotaSnapshot {
	qm.mu.Lock()
	qm.ensureLoadedLocked()
	now := qm.now()
	if rem := qm.cooldownRemainingLocked(now); rem > 0 && qm.fetchedAt.After(time.Time{}) {
		snap := qm.viewLocked(now)
		snap.RefreshCooldownSec = rem
		qm.mu.Unlock()
		return snap
	}
	qm.lastManualAt = now
	qm.mu.Unlock()

	snap := qm.runAllSingleflight(ctx)
	qm.mu.Lock()
	snap.RefreshCooldownSec = qm.cooldownRemainingLocked(qm.now())
	qm.mu.Unlock()
	return snap
}

// OnWorkerSettled refreshes quota for the settled worker kind without blocking
// the caller. Codex and Claude try 0ms local reads first; Antigravity is
// debounced to at least quotaWorkerDebounce (5m) between automatic CLI probes.
func (qm *quotaManager) OnWorkerSettled(kind string) {
	k := normalizeQuotaKind(kind)
	if k == "" {
		return
	}
	qm.mu.Lock()
	qm.ensureLoadedLocked()
	now := qm.now()
	if _, ok := qm.collectors[k]; !ok {
		qm.mu.Unlock()
		return
	}
	// Fast 0ms local path for codex/claude, or debounced CLI probe for agy/codex.
	lastAuto := qm.lastWorkerRefresh[k]
	canProbeCLI := now.Sub(lastAuto) >= qm.workerDebounce
	if k == "agy" && !canProbeCLI {
		qm.mu.Unlock()
		return
	}
	if qm.agentFlights[k] != nil || qm.inFlight != nil {
		qm.mu.Unlock()
		return
	}
	fl := &quotaFlight{done: make(chan struct{})}
	qm.agentFlights[k] = fl
	if canProbeCLI {
		qm.lastWorkerRefresh[k] = now
	}
	qm.mu.Unlock()

	go func() {
		defer func() {
			qm.mu.Lock()
			if qm.agentFlights[k] == fl {
				delete(qm.agentFlights, k)
			}
			close(fl.done)
			qm.mu.Unlock()
		}()
		ctx, cancel := context.WithTimeout(context.Background(), 12*time.Second)
		defer cancel()
		var res AgentQuota
		switch k {
		case "codex":
			if local, ok := qm.collectCodexRolloutOnly(); ok {
				res = local
			} else if canProbeCLI {
				res = qm.collectors[k](ctx)
			} else {
				return
			}
		case "claude":
			if local, ok := qm.collectClaudeLocalOnly(); ok {
				res = local
			} else if canProbeCLI {
				res = qm.collectors[k](ctx)
			} else {
				return
			}
		default:
			res = qm.collectors[k](ctx)
		}
		qm.mergeAgentResult(k, res)
	}()
}

func (qm *quotaManager) runAllSingleflight(ctx context.Context) QuotaSnapshot {
	qm.mu.Lock()
	qm.ensureLoadedLocked()
	if fl := qm.inFlight; fl != nil {
		qm.mu.Unlock()
		select {
		case <-fl.done:
		case <-ctx.Done():
		}
		qm.mu.Lock()
		defer qm.mu.Unlock()
		return qm.viewLocked(qm.now())
	}
	fl := &quotaFlight{done: make(chan struct{})}
	qm.inFlight = fl
	qm.mu.Unlock()

	// Run with a bounded context decoupled from short client disconnects so
	// singleflight completes cleanly, while still respecting caller cancellation.
	runCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	done := make(chan struct{})
	go func() {
		defer cancel()
		defer close(done)
		qm.executeAllRefresh(runCtx, fl)
	}()

	select {
	case <-done:
	case <-ctx.Done():
	}

	qm.mu.Lock()
	defer qm.mu.Unlock()
	return qm.viewLocked(qm.now())
}

func (qm *quotaManager) executeAllRefresh(ctx context.Context, fl *quotaFlight) {
	defer func() {
		qm.mu.Lock()
		if qm.inFlight == fl {
			qm.inFlight = nil
		}
		close(fl.done)
		qm.mu.Unlock()
	}()

	installed := qm.detect()
	if len(installed) == 0 {
		now := qm.now().UTC()
		qm.mu.Lock()
		qm.snapshot = QuotaSnapshot{
			UpdatedAt: now.Format(time.RFC3339),
			Agents:    []AgentQuota{},
		}
		qm.fetchedAt = now
		qm.saveDiskLocked()
		qm.notifyLocked()
		qm.mu.Unlock()
		return
	}

	results := make([]AgentQuota, len(installed))
	var wg sync.WaitGroup
	for i, k := range installed {
		fn, ok := qm.collectors[k]
		if !ok {
			continue
		}
		wg.Add(1)
		go func(idx int, kind string, collect quotaCollectorFunc) {
			defer wg.Done()
			results[idx] = collect(ctx)
		}(i, k, fn)
	}
	wg.Wait()

	now := qm.now().UTC()
	qm.mu.Lock()
	prevByKind := make(map[string]AgentQuota, len(qm.snapshot.Agents))
	for _, a := range qm.snapshot.Agents {
		prevByKind[a.Kind] = a
	}
	merged := make([]AgentQuota, 0, len(results))
	for _, cur := range results {
		if cur.Kind == "" {
			continue
		}
		merged = append(merged, reconcileQuotaAgent(prevByKind[cur.Kind], cur, now))
	}
	qm.snapshot = QuotaSnapshot{
		UpdatedAt: now.Format(time.RFC3339),
		Agents:    merged,
	}
	qm.fetchedAt = now
	qm.saveDiskLocked()
	qm.notifyLocked()
	qm.mu.Unlock()
}

func (qm *quotaManager) mergeAgentResult(kind string, cur AgentQuota) {
	if cur.Kind == "" {
		return
	}
	now := qm.now().UTC()
	qm.mu.Lock()
	defer qm.mu.Unlock()
	qm.ensureLoadedLocked()
	idx := -1
	for i, a := range qm.snapshot.Agents {
		if a.Kind == kind {
			idx = i
			break
		}
	}
	var prev AgentQuota
	if idx >= 0 {
		prev = qm.snapshot.Agents[idx]
	}
	next := reconcileQuotaAgent(prev, cur, now)
	if idx >= 0 {
		qm.snapshot.Agents[idx] = next
	} else {
		qm.snapshot.Agents = append(qm.snapshot.Agents, next)
		sort.SliceStable(qm.snapshot.Agents, func(i, j int) bool {
			return quotaKindRank(qm.snapshot.Agents[i].Kind) < quotaKindRank(qm.snapshot.Agents[j].Kind)
		})
	}
	qm.snapshot.UpdatedAt = now.Format(time.RFC3339)
	qm.fetchedAt = now
	qm.saveDiskLocked()
	qm.notifyLocked()
}

// reconcileQuotaAgent preserves previous valid pools on transient errors so a
// CLI timeout never wipes known quota numbers.
func reconcileQuotaAgent(prev, cur AgentQuota, now time.Time) AgentQuota {
	if cur.UpdatedAt == "" {
		cur.UpdatedAt = now.Format(time.RFC3339)
	}
	if cur.Status == "error" && prev.Status == "ok" && len(prev.Pools) > 0 {
		kept := cloneAgentQuota(prev)
		if kept.Note == "" {
			kept.Note = "最近刷新超时，保留上次缓存"
		}
		return kept
	}
	return cur
}

func quotaKindRank(kind string) int {
	for i, k := range quotaAgentOrder {
		if k == kind {
			return i
		}
	}
	return len(quotaAgentOrder) + 1
}

func normalizeQuotaKind(kind string) string {
	switch strings.ToLower(strings.TrimSpace(kind)) {
	case "agy", "antigravity":
		return "agy"
	case "codex":
		return "codex"
	case "claude":
		return "claude"
	case "cursor", "cursor-agent":
		return "cursor"
	default:
		return ""
	}
}

func (qm *quotaManager) isStaleLocked(now time.Time) bool {
	if qm.fetchedAt.IsZero() {
		return true
	}
	if now.Sub(qm.fetchedAt) >= qm.ttl {
		return true
	}
	// If any window's reset timestamp has passed since we last fetched, treat as stale.
	for _, a := range qm.snapshot.Agents {
		for _, p := range a.Pools {
			for _, w := range p.Windows {
				if w.ResetsAt == nil || *w.ResetsAt == "" {
					continue
				}
				if rt, err := time.Parse(time.RFC3339, *w.ResetsAt); err == nil {
					if !rt.After(now) && rt.After(qm.fetchedAt) {
						return true
					}
				}
			}
		}
	}
	return false
}

func (qm *quotaManager) cooldownRemainingLocked(now time.Time) int {
	if qm.lastManualAt.IsZero() || qm.cooldown <= 0 {
		return 0
	}
	elapsed := now.Sub(qm.lastManualAt)
	if elapsed >= qm.cooldown {
		return 0
	}
	rem := int(math.Ceil((qm.cooldown - elapsed).Seconds()))
	if rem < 0 {
		return 0
	}
	return rem
}

func (qm *quotaManager) viewLocked(now time.Time) QuotaSnapshot {
	agents := make([]AgentQuota, len(qm.snapshot.Agents))
	for i, a := range qm.snapshot.Agents {
		agents[i] = cloneAgentQuota(a)
	}
	return QuotaSnapshot{
		UpdatedAt:          qm.snapshot.UpdatedAt,
		Stale:              qm.isStaleLocked(now),
		Refreshing:         qm.inFlight != nil,
		RefreshCooldownSec: qm.cooldownRemainingLocked(now),
		Agents:             agents,
	}
}

func cloneAgentQuota(a AgentQuota) AgentQuota {
	out := a
	if len(a.Pools) > 0 {
		out.Pools = make([]QuotaPool, len(a.Pools))
		for i, p := range a.Pools {
			cp := p
			if len(p.Windows) > 0 {
				cp.Windows = make([]QuotaWindow, len(p.Windows))
				for j, w := range p.Windows {
					cw := w
					if w.ResetsAt != nil {
						s := *w.ResetsAt
						cw.ResetsAt = &s
					}
					cp.Windows[j] = cw
				}
			}
			out.Pools[i] = cp
		}
	}
	return out
}

// ---------------------------------------------------------------- disk cache

func (qm *quotaManager) cacheFile() string {
	if qm.cacheDir == "" {
		return ""
	}
	return filepath.Join(qm.cacheDir, "quota.json")
}

func (qm *quotaManager) ensureLoadedLocked() {
	if qm.loaded {
		return
	}
	qm.loaded = true
	fp := qm.cacheFile()
	if fp == "" {
		return
	}
	b, err := os.ReadFile(fp)
	if err != nil {
		return
	}
	var snap QuotaSnapshot
	if json.Unmarshal(b, &snap) != nil {
		return
	}
	if snap.Agents == nil {
		snap.Agents = []AgentQuota{}
	}
	qm.snapshot = snap
	if snap.UpdatedAt != "" {
		if t, err := time.Parse(time.RFC3339, snap.UpdatedAt); err == nil {
			qm.fetchedAt = t
		}
	}
}

func (qm *quotaManager) saveDiskLocked() {
	fp := qm.cacheFile()
	if fp == "" {
		return
	}
	if err := os.MkdirAll(filepath.Dir(fp), 0o700); err != nil {
		return
	}
	disk := QuotaSnapshot{
		UpdatedAt: qm.snapshot.UpdatedAt,
		Agents:    qm.snapshot.Agents,
	}
	if disk.Agents == nil {
		disk.Agents = []AgentQuota{}
	}
	b, err := json.MarshalIndent(disk, "", "  ")
	if err != nil {
		return
	}
	tmp, err := os.CreateTemp(filepath.Dir(fp), ".quota-*.tmp")
	if err != nil {
		return
	}
	tmpName := tmp.Name()
	if _, err := tmp.Write(b); err != nil {
		tmp.Close()
		os.Remove(tmpName)
		return
	}
	_ = tmp.Chmod(0o600)
	if err := tmp.Close(); err != nil {
		os.Remove(tmpName)
		return
	}
	if err := os.Rename(tmpName, fp); err != nil {
		os.Remove(tmpName)
	}
}

// ---------------------------------------------------------------- Antigravity collector

func (qm *quotaManager) collectAgy(ctx context.Context) AgentQuota {
	nowStr := qm.now().UTC().Format(time.RFC3339)
	bin := findBin("agy", qm.home)
	if bin == "" {
		return AgentQuota{}
	}
	cctx, cancel := context.WithTimeout(ctx, quotaCLITimeout)
	defer cancel()
	cmd := exec.CommandContext(cctx, bin, "-p", "/usage", "--output-format", "json")
	cmd.WaitDelay = 500 * time.Millisecond
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	exitCode := 0
	if err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			exitCode = ee.ExitCode()
		} else {
			return AgentQuota{
				Kind: "agy", Name: "Antigravity", Status: "error", Source: "cli",
				UpdatedAt: nowStr, Note: "读取额度超时或命令不可用",
			}
		}
	}
	return parseAgyUsageOutput(stdout.Bytes(), stderr.String(), exitCode, nowStr)
}

func parseAgyUsageOutput(stdout []byte, stderr string, exitCode int, updatedAt string) AgentQuota {
	base := AgentQuota{
		Kind:      "agy",
		Name:      "Antigravity",
		Source:    "cli",
		UpdatedAt: updatedAt,
	}
	combinedLow := strings.ToLower(stderr + "\n" + string(stdout))
	if exitCode == 2 || strings.Contains(combinedLow, "api key") || strings.Contains(combinedLow, "api_key") {
		base.Status = "degraded"
		base.Plan = "API Key 模式"
		base.Note = "当前走 API Key 计费，无订阅周期窗口"
		return base
	}
	if exitCode != 0 && len(bytes.TrimSpace(stdout)) == 0 {
		base.Status = "error"
		base.Note = "无法读取本机 Antigravity 额度状态"
		return base
	}

	var env struct {
		Status   string `json:"status"`
		Response string `json:"response"`
		Command  *struct {
			Name string `json:"name"`
			Data *struct {
				Groups []struct {
					Name    string `json:"name"`
					Buckets []struct {
						ID                string   `json:"id"`
						Name              string   `json:"name"`
						Window            string   `json:"window"`
						RemainingFraction *float64 `json:"remaining_fraction"`
						ResetTime         string   `json:"reset_time"`
					} `json:"buckets"`
				} `json:"groups"`
			} `json:"data"`
		} `json:"command"`
	}
	if !decodeJSONFlexible(stdout, &env) {
		if strings.Contains(combinedLow, "not logged in") || strings.Contains(combinedLow, "unauthenticated") {
			base.Status = "degraded"
			base.Plan = "未登录"
			base.Note = "本机 Antigravity 尚未登录"
			return base
		}
		base.Status = "error"
		base.Note = "解析 Antigravity 额度输出失败"
		return base
	}

	var pools []QuotaPool
	if env.Command != nil && env.Command.Data != nil && len(env.Command.Data.Groups) > 0 {
		for _, g := range env.Command.Data.Groups {
			var wins []QuotaWindow
			for _, b := range g.Buckets {
				if b.RemainingFraction == nil {
					continue
				}
				id, lbl := normalizeAgyWindow(b.Window, b.ID, b.Name)
				pct := clampPct(int(math.Round(*b.RemainingFraction * 100)))
				wins = append(wins, QuotaWindow{
					ID:           id,
					Label:        lbl,
					RemainingPct: pct,
					ResetsAt:     normalizeResetISO(b.ResetTime),
				})
			}
			if len(wins) == 0 {
				continue
			}
			sortQuotaWindows(wins)
			pools = append(pools, QuotaPool{
				Group:   shortenAgyGroup(g.Name),
				Windows: wins,
			})
		}
	} else if strings.TrimSpace(env.Response) != "" {
		pools = parseAgyUsageTSV(env.Response)
	}

	if len(pools) == 0 {
		if strings.EqualFold(env.Status, "ERROR") || exitCode != 0 {
			base.Status = "degraded"
			base.Plan = "API Key 模式"
			base.Note = "未获取到订阅周期窗口"
			return base
		}
		base.Status = "degraded"
		base.Note = "暂无可用订阅窗口数据"
		return base
	}

	markCompactFullPools(pools)
	base.Status = "ok"
	base.Pools = pools
	return base
}

func parseAgyUsageTSV(resp string) []QuotaPool {
	order := []string{}
	byGroup := map[string][]QuotaWindow{}
	for _, raw := range strings.Split(resp, "\n") {
		line := strings.TrimSpace(raw)
		if line == "" {
			continue
		}
		parts := strings.Split(line, "\t")
		if len(parts) < 3 {
			continue
		}
		group := shortenAgyGroup(parts[0])
		id, lbl := normalizeAgyWindow("", "", parts[1])
		pctStr := strings.TrimSuffix(strings.TrimSpace(parts[2]), "%")
		pctFloat, err := strconv.ParseFloat(pctStr, 64)
		if err != nil {
			continue
		}
		var reset *string
		if len(parts) >= 4 {
			reset = normalizeResetISO(parts[3])
		}
		if _, exists := byGroup[group]; !exists {
			order = append(order, group)
		}
		byGroup[group] = append(byGroup[group], QuotaWindow{
			ID:           id,
			Label:        lbl,
			RemainingPct: clampPct(int(math.Round(pctFloat))),
			ResetsAt:     reset,
		})
	}
	pools := make([]QuotaPool, 0, len(order))
	for _, g := range order {
		wins := byGroup[g]
		sortQuotaWindows(wins)
		pools = append(pools, QuotaPool{Group: g, Windows: wins})
	}
	return pools
}

func shortenAgyGroup(name string) string {
	t := strings.TrimSpace(name)
	low := strings.ToLower(t)
	switch {
	case low == "gemini models" || low == "gemini":
		return "Gemini"
	case strings.Contains(low, "claude") && strings.Contains(low, "gpt"):
		return "Claude / GPT"
	default:
		return t
	}
}

func normalizeAgyWindow(win, id, name string) (string, string) {
	w := strings.ToLower(strings.TrimSpace(win))
	lowID := strings.ToLower(strings.TrimSpace(id))
	lowName := strings.ToLower(strings.TrimSpace(name))
	switch {
	case w == "5h" || strings.Contains(lowID, "5h") || strings.Contains(lowName, "five hour") || strings.Contains(lowName, "5-hour"):
		return "5h", "5 小时"
	case w == "weekly" || strings.Contains(lowID, "weekly") || strings.Contains(lowName, "weekly"):
		return "weekly", "周额度"
	case w != "":
		return w, w
	case id != "":
		return id, orStr(strings.TrimSpace(name), id)
	default:
		return "window", orStr(strings.TrimSpace(name), "周期额度")
	}
}

func markCompactFullPools(pools []QuotaPool) {
	if len(pools) <= 1 {
		return
	}
	for i := 1; i < len(pools); i++ {
		allFullUnused := len(pools[i].Windows) > 0
		for _, w := range pools[i].Windows {
			if w.RemainingPct < 100 || w.ResetsAt != nil {
				allFullUnused = false
				break
			}
		}
		if allFullUnused {
			pools[i].CompactFull = true
		}
	}
}

// ---------------------------------------------------------------- Codex collector

type codexRateWindowRaw struct {
	UsedPercent        *float64 `json:"usedPercent"`
	UsedPercentSnake   *float64 `json:"used_percent"`
	WindowDurationMins int      `json:"windowDurationMins"`
	WindowDurationSnk  int      `json:"window_duration_mins"`
	ResetsAt           int64    `json:"resetsAt"`
	ResetsAtSnake      int64    `json:"resets_at"`
}

func (w *codexRateWindowRaw) usedPct() (float64, bool) {
	if w == nil {
		return 0, false
	}
	if w.UsedPercent != nil {
		return *w.UsedPercent, true
	}
	if w.UsedPercentSnake != nil {
		return *w.UsedPercentSnake, true
	}
	return 0, false
}

func (w *codexRateWindowRaw) durationMins() int {
	if w == nil {
		return 0
	}
	if w.WindowDurationMins > 0 {
		return w.WindowDurationMins
	}
	return w.WindowDurationSnk
}

func (w *codexRateWindowRaw) resetsUnix() int64 {
	if w == nil {
		return 0
	}
	if w.ResetsAt > 0 {
		return w.ResetsAt
	}
	return w.ResetsAtSnake
}

type codexRateLimitsRaw struct {
	Primary   *codexRateWindowRaw `json:"primary"`
	Secondary *codexRateWindowRaw `json:"secondary"`
	Credits   *struct {
		HasCredits bool `json:"hasCredits"`
		Unlimited  bool `json:"unlimited"`
	} `json:"credits"`
	PlanType      string `json:"planType"`
	PlanTypeSnake string `json:"plan_type"`
}

func (r *codexRateLimitsRaw) plan() string {
	if r == nil {
		return ""
	}
	return formatPlanName(orStr(r.PlanType, r.PlanTypeSnake))
}

func (qm *quotaManager) collectCodex(ctx context.Context) AgentQuota {
	nowStr := qm.now().UTC().Format(time.RFC3339)
	bin := findBin("codex", qm.home)
	if bin == "" {
		return AgentQuota{}
	}
	cctx, cancel := context.WithTimeout(ctx, quotaCLITimeout)
	defer cancel()

	raw, err := fetchCodexAppServerRateLimits(cctx, bin)
	if err == nil && len(raw) > 0 {
		ag := parseCodexAppServerRateLimits(raw, nowStr)
		if ag.Status == "ok" {
			return ag
		}
		// If app-server returned null windows, try 0ms rollout JSONL fallback before degrading.
		if local, ok := qm.collectCodexRolloutOnly(); ok {
			return local
		}
		return ag
	}
	// app-server failed or timed out: try 0ms local rollout JSONL fallback.
	if local, ok := qm.collectCodexRolloutOnly(); ok {
		return local
	}
	return AgentQuota{
		Kind: "codex", Name: "Codex", Status: "error", Source: "app-server",
		UpdatedAt: nowStr, Note: "读取 Codex 额度超时或服务不可用",
	}
}

func (qm *quotaManager) collectCodexRolloutOnly() (AgentQuota, bool) {
	rl, modTime, ok := readCodexRolloutRateLimits(qm.codexHome)
	if !ok || rl == nil {
		return AgentQuota{}, false
	}
	updatedAt := qm.now().UTC().Format(time.RFC3339)
	if !modTime.IsZero() {
		updatedAt = modTime.UTC().Format(time.RFC3339)
	}
	ag := buildCodexAgentQuota(rl, "session", updatedAt)
	if ag.Status != "ok" {
		return AgentQuota{}, false
	}
	return ag, true
}

// fetchCodexAppServerRateLimits queries a short-lived `codex app-server --listen stdio://`
// for `account/rateLimits/read` (with excludeResetCreditDetails: true).
func fetchCodexAppServerRateLimits(ctx context.Context, bin string) (json.RawMessage, error) {
	cmd := exec.CommandContext(ctx, bin, "app-server", "--listen", "stdio://")
	cmd.WaitDelay = 500 * time.Millisecond
	in, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	out, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	defer func() {
		in.Close()
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
		}
		_ = cmd.Wait()
	}()

	send := func(v any) error {
		_, e := in.Write(append(jsonNoEscape(v), '\n'))
		return e
	}
	sc := bufio.NewScanner(out)
	sc.Buffer(make([]byte, 0, 256<<10), 4<<20)
	wait := func(id int) (json.RawMessage, error) {
		for sc.Scan() {
			var m struct {
				ID     *int            `json:"id"`
				Method string          `json:"method"`
				Result json.RawMessage `json:"result"`
				Error  *struct {
					Message string `json:"message"`
				} `json:"error"`
			}
			if json.Unmarshal(sc.Bytes(), &m) != nil || m.ID == nil || *m.ID != id || m.Method != "" {
				continue
			}
			if m.Error != nil {
				return nil, errors.New(m.Error.Message)
			}
			return m.Result, nil
		}
		if err := sc.Err(); err != nil {
			return nil, err
		}
		return nil, io.EOF
	}

	if err := send(map[string]any{
		"id":     1,
		"method": "initialize",
		"params": map[string]any{"clientInfo": map[string]any{"name": "collie", "version": "1"}},
	}); err != nil {
		return nil, err
	}
	if _, err := wait(1); err != nil {
		return nil, err
	}
	if err := send(map[string]any{"method": "initialized", "params": map[string]any{}}); err != nil {
		return nil, err
	}
	if err := send(map[string]any{
		"id":     2,
		"method": "account/rateLimits/read",
		"params": map[string]any{"excludeResetCreditDetails": true},
	}); err != nil {
		return nil, err
	}
	return wait(2)
}

func parseCodexAppServerRateLimits(resultRaw []byte, updatedAt string) AgentQuota {
	var env struct {
		RateLimits          *codexRateLimitsRaw            `json:"rateLimits"`
		RateLimitsByLimitID map[string]*codexRateLimitsRaw `json:"rateLimitsByLimitId"`
	}
	if json.Unmarshal(resultRaw, &env) != nil {
		return AgentQuota{
			Kind: "codex", Name: "Codex", Status: "error", Source: "app-server",
			UpdatedAt: updatedAt, Note: "解析 Codex 额度响应失败",
		}
	}
	rl := env.RateLimits
	if rl == nil || (rl.Primary == nil && rl.Secondary == nil) {
		for _, k := range sortedMapKeys(env.RateLimitsByLimitID) {
			cand := env.RateLimitsByLimitID[k]
			if cand != nil && (cand.Primary != nil || cand.Secondary != nil) {
				rl = cand
				break
			}
		}
	}
	return buildCodexAgentQuota(rl, "app-server", updatedAt)
}

func buildCodexAgentQuota(rl *codexRateLimitsRaw, source, updatedAt string) AgentQuota {
	base := AgentQuota{
		Kind:      "codex",
		Name:      "Codex",
		Source:    source,
		UpdatedAt: updatedAt,
	}
	if rl == nil {
		base.Status = "degraded"
		base.Plan = "API 模式"
		base.Note = "当前走 API 计费，无订阅周期窗口"
		return base
	}
	base.Plan = rl.plan()
	var wins []QuotaWindow
	addWin := func(rw *codexRateWindowRaw, fallbackID, fallbackLabel string) {
		used, ok := rw.usedPct()
		if !ok {
			return
		}
		id, label := codexWindowLabel(rw.durationMins(), fallbackID, fallbackLabel)
		var resets *string
		if ts := rw.resetsUnix(); ts > 0 {
			s := time.Unix(ts, 0).UTC().Format(time.RFC3339)
			resets = &s
		}
		wins = append(wins, QuotaWindow{
			ID:           id,
			Label:        label,
			RemainingPct: clampPct(100 - int(math.Round(used))),
			ResetsAt:     resets,
		})
	}
	addWin(rl.Primary, "primary", "主窗口")
	addWin(rl.Secondary, "secondary", "周额度")

	if len(wins) == 0 {
		base.Status = "degraded"
		if rl.Credits != nil && rl.Credits.Unlimited {
			base.Plan = orStr(base.Plan, "无限额度")
			base.Note = "当前套餐无周期窗口限制"
		} else {
			base.Plan = orStr(base.Plan, "API 模式")
			base.Note = "当前走 API 计费，无订阅周期窗口"
		}
		return base
	}

	sortQuotaWindows(wins)
	base.Status = "ok"
	base.Pools = []QuotaPool{{Group: "", Windows: wins}}
	return base
}

func codexWindowLabel(mins int, fallbackID, fallbackLabel string) (string, string) {
	switch {
	case mins <= 0:
		return fallbackID, fallbackLabel
	case mins >= 240 && mins <= 360:
		return "5h", "5 小时窗口"
	case mins >= 9000 && mins <= 11520:
		return "weekly", "周额度"
	case mins >= 40000 && mins <= 46080:
		return "30d", "30 天窗口"
	case mins < 60:
		return fmt.Sprintf("%dm", mins), fmt.Sprintf("%d 分钟窗口", mins)
	case mins < 1440:
		h := int(math.Round(float64(mins) / 60))
		return fmt.Sprintf("%dh", h), fmt.Sprintf("%d 小时窗口", h)
	default:
		d := int(math.Round(float64(mins) / 1440))
		return fmt.Sprintf("%dd", d), fmt.Sprintf("%d 天窗口", d)
	}
}

// readCodexRolloutRateLimits performs a 0ms local read of the newest
// ~/.codex/sessions/**/rollout-*.jsonl files looking for token_count.rate_limits.
func readCodexRolloutRateLimits(codexHome string) (*codexRateLimitsRaw, time.Time, bool) {
	if codexHome == "" {
		return nil, time.Time{}, false
	}
	sessionsRoot := filepath.Join(codexHome, "sessions")
	if !isDir(sessionsRoot) {
		return nil, time.Time{}, false
	}
	type cand struct {
		path string
		mod  time.Time
		size int64
	}
	var files []cand
	_ = filepath.WalkDir(sessionsRoot, func(p string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		name := d.Name()
		if !strings.HasPrefix(name, "rollout-") || !strings.HasSuffix(name, ".jsonl") {
			return nil
		}
		if fi, e := d.Info(); e == nil && fi.Size() > 0 {
			files = append(files, cand{path: p, mod: fi.ModTime(), size: fi.Size()})
		}
		return nil
	})
	if len(files) == 0 {
		return nil, time.Time{}, false
	}
	sort.Slice(files, func(i, j int) bool { return files[i].mod.After(files[j].mod) })
	limit := min(len(files), 5)
	for _, f := range files[:limit] {
		if rl, ts, ok := parseCodexRolloutFileRateLimits(f.path, f.size, f.mod); ok {
			return rl, ts, true
		}
	}
	return nil, time.Time{}, false
}

func parseCodexRolloutFileRateLimits(path string, size int64, mod time.Time) (*codexRateLimitsRaw, time.Time, bool) {
	f, err := os.Open(path)
	if err != nil {
		return nil, time.Time{}, false
	}
	defer f.Close()
	const tail = 512 << 10
	if size > tail {
		_, _ = f.Seek(size-tail, io.SeekStart)
	}
	data, err := io.ReadAll(f)
	if err != nil {
		return nil, time.Time{}, false
	}
	lines := bytes.Split(data, []byte{'\n'})
	for i := len(lines) - 1; i >= 0; i-- {
		line := bytes.TrimSpace(lines[i])
		if len(line) == 0 || !bytes.Contains(line, []byte(`"token_count"`)) || !bytes.Contains(line, []byte(`"rate_limits"`)) {
			continue
		}
		var ev struct {
			Timestamp string `json:"timestamp"`
			Payload   struct {
				Type       string              `json:"type"`
				RateLimits *codexRateLimitsRaw `json:"rate_limits"`
			} `json:"payload"`
		}
		if json.Unmarshal(line, &ev) != nil || ev.Payload.Type != "token_count" || ev.Payload.RateLimits == nil {
			continue
		}
		rl := ev.Payload.RateLimits
		if rl.Primary == nil && rl.Secondary == nil {
			continue
		}
		ts := mod
		if ev.Timestamp != "" {
			if parsed, err := time.Parse(time.RFC3339Nano, ev.Timestamp); err == nil {
				ts = parsed
			}
		}
		return rl, ts, true
	}
	return nil, time.Time{}, false
}

// ---------------------------------------------------------------- Claude Code collector

func (qm *quotaManager) collectClaude(ctx context.Context) AgentQuota {
	now := qm.now().UTC()
	nowStr := now.Format(time.RFC3339)
	bin := findBin("claude", qm.home)
	if bin == "" {
		return AgentQuota{}
	}

	// 1. Check optional read-only statusLine cache file (written if the user opted in).
	if ag, ok := qm.readClaudeStatuslineCache(now); ok {
		return ag
	}

	// 2. Non-invasive read-only auth check (`claude auth status`, discarding all PII).
	cctx, cancel := context.WithTimeout(ctx, quotaStatusTimeout)
	defer cancel()
	cmd := exec.CommandContext(cctx, bin, "auth", "status")
	cmd.WaitDelay = 500 * time.Millisecond
	out, err := cmd.Output()
	hasCustomAPI := claudeUsesCustomAPISettings(qm.claudeConfigDir)
	limitNote := scanClaudeRecentRateLimitNote(qm.claudeConfigDir, now)

	if err != nil && len(bytes.TrimSpace(out)) == 0 {
		if limitNote != "" {
			return AgentQuota{
				Kind: "claude", Name: "Claude Code", Plan: "订阅态", Status: "degraded",
				Source: "session", UpdatedAt: nowStr, Note: limitNote,
			}
		}
		if hasCustomAPI {
			return AgentQuota{
				Kind: "claude", Name: "Claude Code", Plan: "API / 网关模式", Status: "degraded",
				Source: "auth-status", UpdatedAt: nowStr, Note: "当前走 API 通道，无订阅周期窗口",
			}
		}
		return AgentQuota{
			Kind: "claude", Name: "Claude Code", Status: "optin", Source: "auth-status",
			UpdatedAt: nowStr, Note: "按对话被动同步 · 未开启状态行采集",
		}
	}
	return parseClaudeAuthStatus(out, hasCustomAPI, limitNote, nowStr)
}

func (qm *quotaManager) collectClaudeLocalOnly() (AgentQuota, bool) {
	now := qm.now().UTC()
	if ag, ok := qm.readClaudeStatuslineCache(now); ok {
		return ag, true
	}
	if note := scanClaudeRecentRateLimitNote(qm.claudeConfigDir, now); note != "" {
		return AgentQuota{
			Kind: "claude", Name: "Claude Code", Plan: "订阅态", Status: "degraded",
			Source: "session", UpdatedAt: now.Format(time.RFC3339), Note: note,
		}, true
	}
	return AgentQuota{}, false
}

// readClaudeStatuslineCache reads an optional local JSON cache written by a
// user-enabled Claude Code statusLine hook without modifying ~/.claude/settings.json.
func (qm *quotaManager) readClaudeStatuslineCache(now time.Time) (AgentQuota, bool) {
	if qm.cacheDir == "" {
		return AgentQuota{}, false
	}
	fp := filepath.Join(qm.cacheDir, "claude-statusline.json")
	b, err := os.ReadFile(fp)
	if err != nil {
		return AgentQuota{}, false
	}
	return parseClaudeStatuslineJSON(b, now)
}

func parseClaudeStatuslineJSON(raw []byte, now time.Time) (AgentQuota, bool) {
	var st struct {
		Plan       string `json:"plan"`
		UpdatedAt  string `json:"updated_at"`
		RateLimits *struct {
			FiveHour *struct {
				UsedPercentage *float64 `json:"used_percentage"`
				ResetsAt       int64    `json:"resets_at"`
			} `json:"five_hour"`
			SevenDay *struct {
				UsedPercentage *float64 `json:"used_percentage"`
				ResetsAt       int64    `json:"resets_at"`
			} `json:"seven_day"`
		} `json:"rate_limits"`
	}
	if json.Unmarshal(raw, &st) != nil || st.RateLimits == nil {
		return AgentQuota{}, false
	}
	var wins []QuotaWindow
	if fh := st.RateLimits.FiveHour; fh != nil && fh.UsedPercentage != nil {
		var resets *string
		if fh.ResetsAt > 0 {
			s := time.Unix(fh.ResetsAt, 0).UTC().Format(time.RFC3339)
			resets = &s
		}
		wins = append(wins, QuotaWindow{
			ID:           "5h",
			Label:        "5 小时窗口",
			RemainingPct: clampPct(100 - int(math.Round(*fh.UsedPercentage))),
			ResetsAt:     resets,
		})
	}
	if sd := st.RateLimits.SevenDay; sd != nil && sd.UsedPercentage != nil {
		var resets *string
		if sd.ResetsAt > 0 {
			s := time.Unix(sd.ResetsAt, 0).UTC().Format(time.RFC3339)
			resets = &s
		}
		wins = append(wins, QuotaWindow{
			ID:           "weekly",
			Label:        "7 天窗口",
			RemainingPct: clampPct(100 - int(math.Round(*sd.UsedPercentage))),
			ResetsAt:     resets,
		})
	}
	if len(wins) == 0 {
		return AgentQuota{}, false
	}
	updatedAt := st.UpdatedAt
	if updatedAt == "" {
		updatedAt = now.UTC().Format(time.RFC3339)
	}
	plan := formatPlanName(st.Plan)
	if plan != "" {
		plan += " · 状态行同步"
	} else {
		plan = "状态行同步"
	}
	return AgentQuota{
		Kind:      "claude",
		Name:      "Claude Code",
		Plan:      plan,
		Status:    "ok",
		Source:    "statusline",
		UpdatedAt: updatedAt,
		Pools:     []QuotaPool{{Group: "", Windows: wins}},
	}, true
}

func parseClaudeAuthStatus(raw []byte, hasCustomAPI bool, limitNote, updatedAt string) AgentQuota {
	base := AgentQuota{
		Kind:      "claude",
		Name:      "Claude Code",
		Source:    "auth-status",
		UpdatedAt: updatedAt,
	}
	var st struct {
		LoggedIn         bool   `json:"loggedIn"`
		AuthMethod       string `json:"authMethod"`
		APIProvider      string `json:"apiProvider"`
		SubscriptionType string `json:"subscriptionType"`
	}
	if !decodeJSONFlexible(raw, &st) {
		low := strings.ToLower(string(raw))
		if strings.Contains(low, "not logged in") || strings.Contains(low, "logged out") {
			base.Status = "degraded"
			base.Plan = "未登录"
			base.Note = "本机 Claude Code 尚未登录"
			return base
		}
		if hasCustomAPI {
			base.Status = "degraded"
			base.Plan = "API / 网关模式"
			base.Note = "当前走 API 通道，无订阅周期窗口"
			return base
		}
		base.Status = "optin"
		base.Plan = "订阅态"
		base.Note = orStr(limitNote, "按对话被动同步 · 未开启状态行采集")
		return base
	}

	if hasCustomAPI || (st.APIProvider != "" && !strings.EqualFold(st.APIProvider, "firstParty")) ||
		strings.Contains(strings.ToLower(st.AuthMethod), "api_key") {
		base.Status = "degraded"
		base.Plan = "API / 网关模式"
		base.Note = "当前走 API 通道，无订阅周期窗口"
		return base
	}
	if !st.LoggedIn {
		base.Status = "degraded"
		base.Plan = "未登录"
		base.Note = "本机 Claude Code 尚未登录"
		return base
	}

	plan := formatPlanName(st.SubscriptionType)
	if plan == "" {
		plan = "订阅态"
	}
	base.Plan = plan
	if limitNote != "" {
		base.Status = "degraded"
		base.Source = "session"
		base.Note = limitNote
		return base
	}
	base.Status = "optin"
	base.Note = "按对话被动同步 · 未开启状态行采集"
	return base
}

// claudeUsesCustomAPISettings inspects ~/.claude/settings.json read-only to check
// whether a custom gateway or API key env override is configured (never exposes values).
func claudeUsesCustomAPISettings(configDir string) bool {
	if os.Getenv("ANTHROPIC_BASE_URL") != "" || os.Getenv("ANTHROPIC_API_KEY") != "" {
		return true
	}
	if configDir == "" {
		return false
	}
	b, err := os.ReadFile(filepath.Join(configDir, "settings.json"))
	if err != nil {
		return false
	}
	var cfg struct {
		Env map[string]string `json:"env"`
	}
	if json.Unmarshal(b, &cfg) != nil {
		return false
	}
	return strings.TrimSpace(cfg.Env["ANTHROPIC_BASE_URL"]) != "" ||
		strings.TrimSpace(cfg.Env["ANTHROPIC_API_KEY"]) != ""
}

var claudeLimitHitRe = regexp.MustCompile(`(?i)(you'?ve hit your (?:usage |session |weekly )?limit[^"\n]{0,60}|usage limit reached[^"\n]{0,60}|rate limit[^"\n]{0,40}resets [^"\n]{0,32})`)

// scanClaudeRecentRateLimitNote scans the tails of the most recent local Claude
// session transcripts (within 5h) to detect an explicit rate-limit hit without
// modifying any files or hitting network endpoints.
func scanClaudeRecentRateLimitNote(configDir string, now time.Time) string {
	if configDir == "" {
		return ""
	}
	projectsDir := filepath.Join(configDir, "projects")
	if !isDir(projectsDir) {
		return ""
	}
	cutoff := now.Add(-5 * time.Hour)
	type cand struct {
		path string
		mod  time.Time
		size int64
	}
	var files []cand
	_ = filepath.WalkDir(projectsDir, func(p string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(d.Name(), ".jsonl") {
			return nil
		}
		if fi, e := d.Info(); e == nil && fi.Size() > 0 && fi.ModTime().After(cutoff) {
			files = append(files, cand{path: p, mod: fi.ModTime(), size: fi.Size()})
		}
		return nil
	})
	if len(files) == 0 {
		return ""
	}
	sort.Slice(files, func(i, j int) bool { return files[i].mod.After(files[j].mod) })
	limit := min(len(files), 3)
	for _, f := range files[:limit] {
		if note := extractClaudeLimitFromTranscript(f.path, f.size); note != "" {
			return note
		}
	}
	return ""
}

func extractClaudeLimitFromTranscript(path string, size int64) string {
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()
	const tail = 64 << 10
	if size > tail {
		_, _ = f.Seek(size-tail, io.SeekStart)
	}
	data, err := io.ReadAll(f)
	if err != nil {
		return ""
	}
	lines := bytes.Split(data, []byte{'\n'})
	// Only check the last few events; if the user already resumed successfully after a limit hit, ignore stale hits.
	checked := 0
	for i := len(lines) - 1; i >= 0 && checked < 10; i-- {
		line := bytes.TrimSpace(lines[i])
		if len(line) == 0 {
			continue
		}
		checked++
		var ev struct {
			Type      string `json:"type"`
			IsAPIWait bool   `json:"isApiErrorMessage"`
			Message   struct {
				Content json.RawMessage `json:"content"`
			} `json:"message"`
		}
		if json.Unmarshal(line, &ev) != nil {
			continue
		}
		// A normal non-error assistant message after the hit means quota recovered.
		if ev.Type == "assistant" && !ev.IsAPIWait && !bytes.Contains(bytes.ToLower(ev.Message.Content), []byte("limit")) {
			return ""
		}
		if m := claudeLimitHitRe.FindString(string(ev.Message.Content)); m != "" {
			clean := strings.TrimSpace(strings.ReplaceAll(m, `\n`, " "))
			if len([]rune(clean)) > 80 {
				clean = string([]rune(clean)[:80]) + "…"
			}
			return "近期会话触及限额：" + clean
		}
	}
	return ""
}

// ---------------------------------------------------------------- Cursor collector

func (qm *quotaManager) collectCursor(ctx context.Context) AgentQuota {
	nowStr := qm.now().UTC().Format(time.RFC3339)
	bin := findBin("cursor-agent", qm.home)
	if bin == "" {
		return AgentQuota{}
	}
	cctx, cancel := context.WithTimeout(ctx, quotaCursorTimeout)
	defer cancel()
	cmd := exec.CommandContext(cctx, bin, "status")
	cmd.Env = append(cursorEnv(os.Environ()), "NO_COLOR=1")
	cmd.WaitDelay = 500 * time.Millisecond
	out, err := cmd.CombinedOutput()
	return parseCursorStatusOutput(string(out), err == nil, nowStr)
}

func parseCursorStatusOutput(raw string, ok bool, updatedAt string) AgentQuota {
	clean := strings.ToLower(stripANSI(raw))
	plan := "CLI 已安装"
	switch {
	case strings.Contains(clean, "not logged in") || strings.Contains(clean, "logged out") || strings.Contains(clean, "please login"):
		plan = "未登录"
	case ok && (strings.Contains(clean, "logged in") || strings.Contains(clean, "authenticated") || strings.Contains(clean, "active")):
		plan = "CLI 已登录"
	}
	return AgentQuota{
		Kind:      "cursor",
		Name:      "Cursor",
		Plan:      plan,
		Status:    "external",
		Source:    "status",
		UpdatedAt: updatedAt,
		Note:      "CLI 未开放实时额度接口",
		LinkText:  "控制台 ↗",
		LinkURL:   quotaCursorConsoleURL,
	}
}

// ---------------------------------------------------------------- shared helpers

func clampPct(v int) int {
	if v < 0 {
		return 0
	}
	if v > 100 {
		return 100
	}
	return v
}

func normalizeResetISO(raw string) *string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	t, err := time.Parse(time.RFC3339Nano, raw)
	if err != nil {
		t, err = time.Parse(time.RFC3339, raw)
		if err != nil {
			return nil
		}
	}
	if t.Unix() <= 0 {
		return nil
	}
	s := t.UTC().Format(time.RFC3339)
	return &s
}

func sortQuotaWindows(wins []QuotaWindow) {
	rank := func(id string) int {
		switch id {
		case "5h", "primary":
			return 0
		case "weekly", "7d", "secondary":
			return 1
		case "30d", "monthly":
			return 2
		default:
			return 3
		}
	}
	sort.SliceStable(wins, func(i, j int) bool {
		return rank(wins[i].ID) < rank(wins[j].ID)
	})
}

func formatPlanName(raw string) string {
	s := strings.TrimSpace(raw)
	if s == "" {
		return ""
	}
	switch strings.ToLower(s) {
	case "free":
		return "Free"
	case "plus":
		return "Plus"
	case "pro":
		return "Pro"
	case "max":
		return "Max"
	case "ultra":
		return "Ultra"
	case "team":
		return "Team"
	case "enterprise":
		return "Enterprise"
	case "edu":
		return "Edu"
	default:
		if len(s) > 24 {
			s = s[:24]
		}
		return s
	}
}

func decodeJSONFlexible(raw []byte, dst any) bool {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 {
		return false
	}
	if json.Unmarshal(trimmed, dst) == nil {
		return true
	}
	if idx := bytes.IndexByte(trimmed, '{'); idx > 0 {
		if json.Unmarshal(trimmed[idx:], dst) == nil {
			return true
		}
	}
	lines := bytes.Split(trimmed, []byte{'\n'})
	for i := len(lines) - 1; i >= 0; i-- {
		l := bytes.TrimSpace(lines[i])
		if bytes.HasPrefix(l, []byte{'{'}) && json.Unmarshal(l, dst) == nil {
			return true
		}
	}
	return false
}

func sortedMapKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	return keys
}
