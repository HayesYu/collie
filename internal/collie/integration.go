package collie

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"math/rand"
	"sort"
	"strings"
	"sync"
	"time"
)

// ErrAuthFailed signals a permanent credential/permission failure. When an
// Integration.Run returns an error matching ErrAuthFailed, IntegrationManager
// immediately stops retrying and marks the integration as HealthError until
// the user updates its configuration.
var ErrAuthFailed = errors.New("authentication failed")

type HealthState string

const (
	HealthUnconfigured HealthState = "unconfigured"
	HealthDisabled     HealthState = "disabled"
	HealthConnecting   HealthState = "connecting"
	HealthConnected    HealthState = "connected"
	HealthError        HealthState = "error"
)

// IntegrationConfig passes an integration's persisted config, cursor, and
// in-memory secrets to Run/Test/SamplePull. Secrets is json:"-" so it can
// never be serialized into HTTP API responses or SQLite.
type IntegrationConfig struct {
	Kind    string            `json:"kind"`
	Enabled bool              `json:"enabled"`
	Config  json.RawMessage   `json:"config"`
	Cursor  json.RawMessage   `json:"cursor,omitempty"`
	Secrets map[string]string `json:"-"`
}

// IntegrationHealth is a thread-safe runtime snapshot surfaced in HTTP/SSE.
type IntegrationHealth struct {
	State        HealthState `json:"state"`
	Detail       string      `json:"detail,omitempty"`
	LastError    string      `json:"last_error,omitempty"`
	LastOKAt     *time.Time  `json:"last_ok_at,omitempty"`
	LastActiveAt *time.Time  `json:"last_active_at,omitempty"`
	TodayCount   int         `json:"today_count"`
}

// TestResult is returned by Integration.Test without polluting chat or inbox.
type TestResult struct {
	OK        bool           `json:"ok"`
	Message   string         `json:"message"`
	LatencyMs int64          `json:"latency_ms,omitempty"`
	Detail    map[string]any `json:"detail,omitempty"`
}

// OutboundReceipt identifies an outbound message/card posted to an external
// chat platform so later resolution can update it in place.
type OutboundReceipt struct {
	Kind       string `json:"kind"`
	ChatID     string `json:"chat_id"`
	ExternalID string `json:"external_id"`
}

// SampleEvent is one read-only preview item returned by PullSampler.
type SampleEvent struct {
	Kind   string    `json:"kind"`
	Repo   string    `json:"repo,omitempty"`
	Tag    string    `json:"tag,omitempty"`
	Title  string    `json:"title"`
	URL    string    `json:"url,omitempty"`
	At     time.Time `json:"at"`
	Action string    `json:"action,omitempty"`
}

// IntegrationMeta describes an integration for the UI store grid.
type IntegrationMeta struct {
	Kind        string `json:"kind"`
	Name        string `json:"name"`
	Subtitle    string `json:"subtitle"`
	Description string `json:"description"`
	Order       int    `json:"-"`
}

// Integration is the minimal interface every external integration implements.
type Integration interface {
	Kind() string
	Run(ctx context.Context, cfg IntegrationConfig, sink EventSink) error
	Health() IntegrationHealth
	Test(ctx context.Context, cfg IntegrationConfig) TestResult
}

// OutboundIntegration is implemented by bidirectional chat channels (Feishu,
// Telegram, Slack) that mirror Collie replies and sync resolved pending items.
type OutboundIntegration interface {
	Integration
	SendReply(ctx context.Context, e *Entry, link string) (OutboundReceipt, error)
	SyncResolved(ctx context.Context, receipt OutboundReceipt, summary string) error
}

// PullSampler is implemented by active polling integrations (GitHub, CLI
// code-forge providers, RSS/alerts) for read-only "sample recent N" checks.
type PullSampler interface {
	Integration
	SamplePull(ctx context.Context, cfg IntegrationConfig, limit int) ([]SampleEvent, error)
}

// IntegrationDescriptor optionally provides UI metadata and config helpers.
type IntegrationDescriptor interface {
	Meta() IntegrationMeta
	IsConfigured(cfg IntegrationConfig) bool
	SanitizeConfig(cfg IntegrationConfig) map[string]any
	SecretSet(cfg IntegrationConfig) bool
	ValidateSave(raw json.RawMessage) (json.RawMessage, error)
}

// IntegrationProber optionally inspects local environment readiness (such as
// local CLI login state or suggested repositories) without mutating state.
type IntegrationProber interface {
	ProbeView(ctx context.Context, cfg IntegrationConfig) (map[string]any, error)
}

// ForgeProvider defines the Phase 2 pluggable code-hosting data source contract.
type ForgeProvider interface {
	Name() string
	Probe(ctx context.Context, cfg ForgeConfig) (ForgeIdentity, error)
	Poll(ctx context.Context, cfg ForgeConfig, cursor ForgeCursor) ([]ForgeItem, ForgeCursor, time.Duration, error)
	Sample(ctx context.Context, cfg ForgeConfig, limit int) ([]ForgeItem, error)
}

type ForgeConfig struct {
	Repos         []string          `json:"repos,omitempty"`
	WatchMentions bool              `json:"watch_mentions"`
	Events        map[string]bool   `json:"events,omitempty"`
	HandleMode    string            `json:"handle_mode,omitempty"`
	AuthMode      string            `json:"auth_mode,omitempty"`
	PollInterval  int               `json:"poll_interval,omitempty"`
	IgnoreBots    *bool             `json:"ignore_bots,omitempty"`
	Configured    bool              `json:"configured,omitempty"`
	Secrets       map[string]string `json:"-"`
}

type ForgeIdentity struct {
	User      string         `json:"user,omitempty"`
	Repos     []string       `json:"repos,omitempty"`
	RateLimit map[string]any `json:"rate_limit,omitempty"`
	Detail    string         `json:"detail,omitempty"`
}

type ForgeCursor struct {
	ETag         string            `json:"etag,omitempty"`
	LastModified string            `json:"last_modified,omitempty"`
	Watermark    time.Time         `json:"watermark,omitempty"`
	RepoETags    map[string]string `json:"repo_etags,omitempty"`
	RepoLastMod  map[string]string `json:"repo_last_mod,omitempty"`
	Initialized  bool              `json:"initialized,omitempty"`
}

type ForgeItem struct {
	DedupKey   string    `json:"dedup_key"`
	Kind       string    `json:"kind"`
	Repo       string    `json:"repo"`
	Number     int       `json:"number,omitempty"`
	Tag        string    `json:"tag,omitempty"`
	Title      string    `json:"title"`
	Body       string    `json:"body,omitempty"`
	Author     string    `json:"author,omitempty"`
	AuthorBot  bool      `json:"author_bot,omitempty"`
	Conclusion string    `json:"conclusion,omitempty"`
	URL        string    `json:"url,omitempty"`
	At         time.Time `json:"at"`
}

// HumanEvent represents a trusted human message or card action from an external
// chat channel that enters chat:main with full human-interrupt priority.
type HumanEvent struct {
	Source    string
	DedupKey  string
	Thread    string
	From      string
	Text      string
	Prompt    string
	ReEntryID string
	ChatID    string
	MessageID string
	ChatType  string
}

// ExternalEvent represents an automated event from an external platform (PR/MR,
// CI, issue) routed through durable deduplication and rate limiting.
type ExternalEvent struct {
	Source     string
	DedupKey   string
	Thread     string
	From       string
	Text       string
	Ref        string
	Action     string // optional override: "respond" | "candidate" | "notify"
	Target     string // optional explicit target (e.g. "collie"); empty uses thread subscribers fast path
	NotifyOnly bool   // write inbox + user summary without waking Router
	Summary    string // concise human-facing summary when NotifyOnly is true
	Tone       string // event tone when NotifyOnly is true ("info" | "error")
}

// EventSink is passed by IntegrationManager to each running integration.
type EventSink interface {
	EmitHuman(ctx context.Context, ev HumanEvent) error
	EmitEvent(ctx context.Context, ev ExternalEvent) (accepted bool, err error)
	ReportHealth(state HealthState, detail string)
}

// IntegrationsSummary is embedded in superView so the sidebar & UI update over SSE.
type IntegrationsSummary struct {
	Connected int `json:"connected"`
	Error     int `json:"error"`
	Total     int `json:"total"`
}

// IntegrationView is the sanitized API payload returned by GET/POST /api/super/integrations.
type IntegrationView struct {
	Kind        string            `json:"kind"`
	Name        string            `json:"name"`
	Subtitle    string            `json:"subtitle"`
	Description string            `json:"description"`
	Enabled     bool              `json:"enabled"`
	Configured  bool              `json:"configured"`
	SecretSet   bool              `json:"secret_set"`
	Config      map[string]any    `json:"config"`
	Health      IntegrationHealth `json:"health"`
	Extra       map[string]any    `json:"extra,omitempty"`
}

// ---------------------------------------------------------------- token bucket

type tokenBucket struct {
	mu          sync.Mutex
	ratePerSec  float64
	burst       float64
	tokens      float64
	last        time.Time
	dropped     int
	lastSummary time.Time
}

func newTokenBucket(ratePerSec, burst float64) *tokenBucket {
	return &tokenBucket{
		ratePerSec: ratePerSec,
		burst:      burst,
		tokens:     burst,
		last:       time.Now(),
	}
}

func (b *tokenBucket) allow(now time.Time) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.last.IsZero() {
		b.last = now
		b.tokens = b.burst
	}
	elapsed := now.Sub(b.last).Seconds()
	if elapsed > 0 {
		b.tokens += elapsed * b.ratePerSec
		if b.tokens > b.burst {
			b.tokens = b.burst
		}
		b.last = now
	}
	if b.tokens >= 1 {
		b.tokens -= 1
		return true
	}
	b.dropped++
	return false
}

// flushFolded returns the count of dropped events if a storm summary should be
// emitted now (at most once per window).
func (b *tokenBucket) takeFolded(now time.Time, window time.Duration) int {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.dropped == 0 {
		return 0
	}
	if !b.lastSummary.IsZero() && now.Sub(b.lastSummary) < window {
		return 0
	}
	n := b.dropped
	b.dropped = 0
	b.lastSummary = now
	return n
}

// ---------------------------------------------------------------- manager

type managedIntegration struct {
	adapter Integration
	cancel  context.CancelFunc
	done    chan struct{}
	health  IntegrationHealth
	dayKey  string
}

type IntegrationManager struct {
	sp      *Super
	db      *sql.DB
	secrets *SecretStore

	mu      sync.Mutex
	baseCtx context.Context
	started bool
	items   map[string]*managedIntegration
	records map[string]IntegrationRecord
	buckets map[string]*tokenBucket
	wg      sync.WaitGroup
	stopCh  chan struct{}

	// Tunables (overridden in tests for fast deterministic runs).
	backoffSchedule []time.Duration
	backoffJitter   float64
	stableReset     time.Duration
	eventRatePerMin float64
	eventBurst      float64
	humanRatePerSec float64
	humanBurst      float64
	foldWindow      time.Duration
	nowFn           func() time.Time
}

var defaultBackoffSchedule = []time.Duration{
	2 * time.Second,
	4 * time.Second,
	8 * time.Second,
	16 * time.Second,
	32 * time.Second,
	60 * time.Second,
}

func newIntegrationManager(sp *Super, db *sql.DB, secrets *SecretStore) *IntegrationManager {
	return &IntegrationManager{
		sp:              sp,
		db:              db,
		secrets:         secrets,
		items:           map[string]*managedIntegration{},
		records:         map[string]IntegrationRecord{},
		buckets:         map[string]*tokenBucket{},
		backoffSchedule: defaultBackoffSchedule,
		backoffJitter:   0.25,
		stableReset:     2 * time.Minute,
		eventRatePerMin: 20,
		eventBurst:      8,
		humanRatePerSec: 2,
		humanBurst:      4,
		foldWindow:      time.Minute,
		nowFn:           time.Now,
	}
}

func (m *IntegrationManager) now() time.Time {
	if m.nowFn != nil {
		return m.nowFn()
	}
	return time.Now()
}

func (m *IntegrationManager) Register(adapter Integration) {
	if adapter == nil || adapter.Kind() == "" {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.items[adapter.Kind()] = &managedIntegration{
		adapter: adapter,
		health:  IntegrationHealth{State: HealthUnconfigured},
	}
}

func (m *IntegrationManager) Adapter(kind string) Integration {
	m.mu.Lock()
	defer m.mu.Unlock()
	if item := m.items[kind]; item != nil {
		return item.adapter
	}
	return nil
}

// Start loads saved configurations from SQLite, starts enabled integrations,
// and begins periodic 30-day dedup cleanup.
func (m *IntegrationManager) Start(ctx context.Context) {
	m.mu.Lock()
	if m.started {
		m.mu.Unlock()
		return
	}
	m.baseCtx = ctx
	m.started = true
	m.stopCh = make(chan struct{})
	if recs, err := loadIntegrations(m.db); err == nil {
		m.records = recs
	} else {
		log.Printf("integrations load: %v", err)
	}
	kinds := make([]string, 0, len(m.items))
	for k := range m.items {
		kinds = append(kinds, k)
	}
	m.mu.Unlock()

	for _, k := range kinds {
		m.Reload(k)
	}

	// Daily prune of integration_dedup entries older than 30 days.
	m.wg.Add(1)
	go func() {
		defer m.wg.Done()
		m.pruneDedup()
		tk := time.NewTicker(24 * time.Hour)
		defer tk.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-m.stopCh:
				return
			case <-tk.C:
				m.pruneDedup()
			}
		}
	}()
}

func (m *IntegrationManager) pruneDedup() {
	cutoff := m.now().Add(-30 * 24 * time.Hour)
	if _, err := dedupPrune(m.db, cutoff); err != nil {
		log.Printf("integration dedup prune: %v", err)
	}
}

// Stop cancels every running integration and waits up to timeout for graceful
// disconnection.
func (m *IntegrationManager) Stop(timeout time.Duration) {
	if timeout <= 0 {
		timeout = 3 * time.Second
	}
	m.mu.Lock()
	if !m.started {
		m.mu.Unlock()
		return
	}
	if m.stopCh != nil {
		select {
		case <-m.stopCh:
		default:
			close(m.stopCh)
		}
	}
	for _, item := range m.items {
		if item.cancel != nil {
			item.cancel()
		}
	}
	m.mu.Unlock()

	done := make(chan struct{})
	go func() {
		m.wg.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(timeout):
		log.Printf("integrations: graceful shutdown timed out after %v", timeout)
	}
}

func (m *IntegrationManager) configLocked(kind string) IntegrationConfig {
	rec := m.records[kind]
	cfg := IntegrationConfig{
		Kind:    kind,
		Enabled: rec.Enabled,
		Config:  rec.Config,
		Cursor:  rec.Cursor,
		Secrets: m.secrets.Get(kind),
	}
	if len(cfg.Config) == 0 {
		cfg.Config = json.RawMessage(`{}`)
	}
	if len(cfg.Cursor) == 0 {
		cfg.Cursor = json.RawMessage(`{}`)
	}
	return cfg
}

func (m *IntegrationManager) isConfiguredLocked(item *managedIntegration, cfg IntegrationConfig) bool {
	if d, ok := item.adapter.(IntegrationDescriptor); ok {
		return d.IsConfigured(cfg)
	}
	return len(cfg.Config) > 2 || len(cfg.Secrets) > 0
}

// Reload stops and restarts only the single integration named kind without
// disturbing any other integration or Super's main loop.
func (m *IntegrationManager) Reload(kind string) {
	m.mu.Lock()
	item := m.items[kind]
	if item == nil {
		m.mu.Unlock()
		return
	}
	oldCancel := item.cancel
	oldDone := item.done
	item.cancel = nil
	item.done = nil
	m.mu.Unlock()

	if oldCancel != nil {
		oldCancel()
	}
	if oldDone != nil {
		select {
		case <-oldDone:
		case <-time.After(2 * time.Second):
		}
	}

	m.mu.Lock()
	item = m.items[kind]
	if item == nil {
		m.mu.Unlock()
		return
	}
	cfg := m.configLocked(kind)
	configured := m.isConfiguredLocked(item, cfg)
	if !configured {
		item.health.State = HealthUnconfigured
		item.health.Detail = ""
		item.health.LastError = ""
		m.mu.Unlock()
		m.notifyChanged()
		return
	}
	if !cfg.Enabled {
		item.health.State = HealthDisabled
		item.health.Detail = ""
		item.health.LastError = ""
		m.mu.Unlock()
		m.notifyChanged()
		return
	}
	if !m.started || m.baseCtx == nil || m.baseCtx.Err() != nil {
		item.health.State = HealthConnecting
		m.mu.Unlock()
		m.notifyChanged()
		return
	}

	runCtx, cancel := context.WithCancel(m.baseCtx)
	done := make(chan struct{})
	item.cancel = cancel
	item.done = done
	item.health.State = HealthConnecting
	item.health.Detail = "正在建立连接…"
	item.health.LastError = ""
	m.wg.Add(1)
	m.mu.Unlock()
	m.notifyChanged()

	go m.runLoop(runCtx, done, item, cfg)
}

func (m *IntegrationManager) runLoop(ctx context.Context, done chan struct{}, item *managedIntegration, cfg IntegrationConfig) {
	defer m.wg.Done()
	defer close(done)

	kind := item.adapter.Kind()
	sink := &managerSink{mgr: m, kind: kind}
	attempt := 0

	for {
		if ctx.Err() != nil {
			return
		}
		startAt := m.now()
		err := item.adapter.Run(ctx, cfg, sink)
		if ctx.Err() != nil {
			return
		}

		// Reset backoff if the adapter remained healthy for long enough.
		if m.stableReset > 0 && m.now().Sub(startAt) >= m.stableReset {
			attempt = 0
		}

		if err == nil {
			// Clean return without context cancellation: reconnect after brief delay.
			attempt = 0
			m.reportHealth(kind, HealthConnecting, "连接已断开，正在重连…")
		} else if errors.Is(err, ErrAuthFailed) {
			msg := m.redact(cleanAuthError(err))
			m.reportHealthError(kind, msg)
			log.Printf("integration %s auth circuit-broken: %s", kind, msg)
			return
		} else {
			msg := m.redact(err.Error())
			wait := m.nextBackoff(attempt)
			attempt++
			m.reportHealthTransient(kind, fmt.Sprintf("连接中断，%s 后重试：%s", formatShortWait(wait), clip(msg, 120)), msg)
			log.Printf("integration %s error (retry in %v): %s", kind, wait, msg)
			select {
			case <-ctx.Done():
				return
			case <-time.After(wait):
			}
			continue
		}

		wait := m.nextBackoff(attempt)
		attempt++
		select {
		case <-ctx.Done():
			return
		case <-time.After(wait):
		}
	}
}

func cleanAuthError(err error) string {
	s := strings.TrimSpace(err.Error())
	s = strings.TrimPrefix(s, ErrAuthFailed.Error()+":")
	s = strings.TrimSpace(s)
	if s == "" {
		return "凭据校验失败，请检查配置后重试"
	}
	return s
}

func formatShortWait(d time.Duration) string {
	if d < time.Second {
		return fmt.Sprintf("%dms", d.Milliseconds())
	}
	return fmt.Sprintf("%d 秒", int(d.Round(time.Second).Seconds()))
}

func (m *IntegrationManager) nextBackoff(attempt int) time.Duration {
	sched := m.backoffSchedule
	if len(sched) == 0 {
		sched = defaultBackoffSchedule
	}
	idx := attempt
	if idx >= len(sched) {
		idx = len(sched) - 1
	}
	base := sched[idx]
	if m.backoffJitter <= 0 {
		return base
	}
	// Uniform random jitter in [1-j, 1+j].
	factor := 1 + (rand.Float64()*2-1)*m.backoffJitter
	out := time.Duration(float64(base) * factor)
	if out < time.Millisecond {
		out = time.Millisecond
	}
	return out
}

func (m *IntegrationManager) redact(s string) string {
	if m.secrets == nil {
		return s
	}
	return m.secrets.Redact(s)
}

func (m *IntegrationManager) notifyChanged() {
	if m.sp != nil {
		m.sp.mu.Lock()
		m.sp.changed()
		m.sp.mu.Unlock()
	}
}

func (m *IntegrationManager) reportHealth(kind string, state HealthState, detail string) {
	detail = m.redact(strings.TrimSpace(detail))
	now := m.now()
	m.mu.Lock()
	item := m.items[kind]
	if item == nil {
		m.mu.Unlock()
		return
	}
	item.health.State = state
	item.health.Detail = detail
	if state == HealthConnected {
		item.health.LastError = ""
		t := now
		item.health.LastOKAt = &t
	} else if state == HealthError && detail != "" {
		item.health.LastError = detail
	}
	m.mu.Unlock()
	m.notifyChanged()
}

func (m *IntegrationManager) reportHealthError(kind, msg string) {
	msg = m.redact(strings.TrimSpace(msg))
	m.mu.Lock()
	item := m.items[kind]
	if item != nil {
		item.health.State = HealthError
		item.health.Detail = msg
		item.health.LastError = msg
	}
	m.mu.Unlock()
	m.notifyChanged()
}

func (m *IntegrationManager) reportHealthTransient(kind, detail, lastErr string) {
	m.mu.Lock()
	item := m.items[kind]
	if item != nil {
		item.health.State = HealthConnecting
		item.health.Detail = m.redact(detail)
		item.health.LastError = m.redact(lastErr)
	}
	m.mu.Unlock()
	m.notifyChanged()
}

func (m *IntegrationManager) recordActivity(kind string) {
	now := m.now()
	day := now.Format("2006-01-02")
	m.mu.Lock()
	item := m.items[kind]
	if item != nil {
		if item.dayKey != day {
			item.dayKey = day
			item.health.TodayCount = 0
		}
		item.health.TodayCount++
		t := now
		item.health.LastActiveAt = &t
	}
	m.mu.Unlock()
	m.notifyChanged()
}

// Health returns a safe copy of one integration's health snapshot.
func (m *IntegrationManager) Health(kind string) IntegrationHealth {
	m.mu.Lock()
	defer m.mu.Unlock()
	item := m.items[kind]
	if item == nil {
		return IntegrationHealth{State: HealthUnconfigured}
	}
	h := item.health
	if day := m.now().Format("2006-01-02"); item.dayKey != "" && item.dayKey != day {
		h.TodayCount = 0
	}
	return h
}

// Summary aggregates connected and error counts for superView SSE snapshots.
func (m *IntegrationManager) Summary() IntegrationsSummary {
	m.mu.Lock()
	defer m.mu.Unlock()
	var s IntegrationsSummary
	s.Total = len(m.items)
	for _, item := range m.items {
		switch item.health.State {
		case HealthConnected:
			s.Connected++
		case HealthError:
			s.Error++
		}
	}
	return s
}

// ListViews returns sanitized metadata, non-sensitive config, and runtime health
// for all registered integrations (ordered deterministically).
func (m *IntegrationManager) ListViews() []IntegrationView {
	m.mu.Lock()
	kinds := make([]string, 0, len(m.items))
	for k := range m.items {
		kinds = append(kinds, k)
	}
	m.mu.Unlock()

	views := make([]IntegrationView, 0, len(kinds))
	orders := map[string]int{}
	for _, k := range kinds {
		v, order, ok := m.viewWithOrder(k)
		if ok {
			views = append(views, v)
			orders[v.Kind] = order
		}
	}
	sort.Slice(views, func(i, j int) bool {
		oi, oj := orders[views[i].Kind], orders[views[j].Kind]
		if oi != oj {
			return oi < oj
		}
		return views[i].Kind < views[j].Kind
	})
	return views
}

// View returns the sanitized view for one integration kind.
func (m *IntegrationManager) View(kind string) (IntegrationView, bool) {
	v, _, ok := m.viewWithOrder(kind)
	return v, ok
}

func (m *IntegrationManager) viewWithOrder(kind string) (IntegrationView, int, bool) {
	m.mu.Lock()
	item := m.items[kind]
	if item == nil {
		m.mu.Unlock()
		return IntegrationView{}, 0, false
	}
	cfg := m.configLocked(kind)
	configured := m.isConfiguredLocked(item, cfg)
	health := item.health
	if day := m.now().Format("2006-01-02"); item.dayKey != "" && item.dayKey != day {
		health.TodayCount = 0
	}
	adapter := item.adapter
	m.mu.Unlock()

	meta := IntegrationMeta{Kind: kind, Name: kind}
	secretSet := len(cfg.Secrets) > 0
	sanitized := map[string]any{}
	if d, ok := adapter.(IntegrationDescriptor); ok {
		meta = d.Meta()
		secretSet = d.SecretSet(cfg)
		sanitized = d.SanitizeConfig(cfg)
	} else {
		_ = json.Unmarshal(cfg.Config, &sanitized)
	}
	if sanitized == nil {
		sanitized = map[string]any{}
	}

	var extra map[string]any
	if ep, ok := adapter.(interface{ ExtraView() map[string]any }); ok {
		extra = ep.ExtraView()
	}

	return IntegrationView{
		Kind:        kind,
		Name:        meta.Name,
		Subtitle:    meta.Subtitle,
		Description: meta.Description,
		Enabled:     cfg.Enabled,
		Configured:  configured,
		SecretSet:   secretSet,
		Config:      sanitized,
		Health:      health,
		Extra:       extra,
	}, meta.Order, true
}

// SaveRequest carries an integration update from POST /api/super/integrations/{kind}.
type SaveRequest struct {
	Enabled     *bool             `json:"enabled,omitempty"`
	Config      json.RawMessage   `json:"config,omitempty"`
	Secrets     map[string]string `json:"secrets,omitempty"`
	ClearSecret bool              `json:"clear_secret,omitempty"`
	ResetCursor bool              `json:"reset_cursor,omitempty"`
}

// ConfigSnapshot returns the latest persisted config + secrets for kind.
func (m *IntegrationManager) ConfigSnapshot(kind string) IntegrationConfig {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.configLocked(kind)
}

// Cursor returns the persisted cursor JSON for kind.
func (m *IntegrationManager) Cursor(kind string) json.RawMessage {
	m.mu.Lock()
	defer m.mu.Unlock()
	if cur := m.records[kind].Cursor; len(cur) > 0 {
		return append(json.RawMessage(nil), cur...)
	}
	return json.RawMessage(`{}`)
}

// SaveCursor persists cursor JSON to SQLite and refreshes the in-memory record
// so single-point reloads preserve watermarks and ETags.
func (m *IntegrationManager) SaveCursor(kind string, cursor json.RawMessage) error {
	if len(cursor) == 0 {
		cursor = json.RawMessage(`{}`)
	}
	if err := saveIntegrationCursor(m.db, kind, cursor); err != nil {
		return err
	}
	m.mu.Lock()
	rec := m.records[kind]
	rec.Kind = kind
	rec.Cursor = append(json.RawMessage(nil), cursor...)
	rec.UpdatedAt = m.now().Unix()
	m.records[kind] = rec
	m.mu.Unlock()
	return nil
}

// Save persists non-sensitive config to SQLite and sensitive secrets to 0600
// secrets.json, then triggers a single-point hot reload for kind.
func (m *IntegrationManager) Save(kind string, req SaveRequest) (IntegrationView, error) {
	m.mu.Lock()
	item := m.items[kind]
	if item == nil {
		m.mu.Unlock()
		return IntegrationView{}, fmt.Errorf("未知的接入类型：%s", kind)
	}
	rec := m.records[kind]
	rec.Kind = kind
	if len(rec.Config) == 0 {
		rec.Config = json.RawMessage(`{}`)
	}
	if len(rec.Cursor) == 0 {
		rec.Cursor = json.RawMessage(`{}`)
	}
	adapter := item.adapter
	m.mu.Unlock()

	if len(req.Config) > 0 && string(req.Config) != "null" {
		if d, ok := adapter.(IntegrationDescriptor); ok {
			norm, err := d.ValidateSave(req.Config)
			if err != nil {
				return IntegrationView{}, err
			}
			rec.Config = norm
		} else {
			rec.Config = req.Config
		}
	}
	if req.Enabled != nil {
		rec.Enabled = *req.Enabled
	}
	rec.UpdatedAt = m.now().Unix()

	// Persist secrets to 0600 secrets.json first (never into SQLite).
	if req.ClearSecret {
		if err := m.secrets.Delete(kind); err != nil {
			return IntegrationView{}, fmt.Errorf("清空本机凭据失败：%w", err)
		}
	}
	if len(req.Secrets) > 0 {
		if err := m.secrets.Update(kind, req.Secrets); err != nil {
			return IntegrationView{}, fmt.Errorf("保存本机凭据失败：%w", err)
		}
	}

	if err := saveIntegration(m.db, rec); err != nil {
		return IntegrationView{}, fmt.Errorf("保存接入配置失败：%w", err)
	}
	if req.ResetCursor || req.ClearSecret {
		rec.Cursor = json.RawMessage(`{}`)
		_ = saveIntegrationCursor(m.db, kind, rec.Cursor)
	}

	m.mu.Lock()
	m.records[kind] = rec
	m.mu.Unlock()

	m.Reload(kind)
	v, _ := m.View(kind)
	return v, nil
}

func (m *IntegrationManager) overlayTempConfig(kind string, req SaveRequest) (Integration, IntegrationConfig, error) {
	m.mu.Lock()
	item := m.items[kind]
	if item == nil {
		m.mu.Unlock()
		return nil, IntegrationConfig{}, fmt.Errorf("未知的接入类型：%s", kind)
	}
	cfg := m.configLocked(kind)
	adapter := item.adapter
	m.mu.Unlock()

	if len(req.Config) > 0 && string(req.Config) != "null" {
		if d, ok := adapter.(IntegrationDescriptor); ok {
			norm, err := d.ValidateSave(req.Config)
			if err != nil {
				return adapter, cfg, err
			}
			cfg.Config = norm
		} else {
			cfg.Config = req.Config
		}
	}
	if req.ClearSecret {
		cfg.Secrets = map[string]string{}
	}
	for k, v := range req.Secrets {
		if strings.TrimSpace(v) != "" {
			cfg.Secrets[k] = strings.TrimSpace(v)
		}
	}
	return adapter, cfg, nil
}

// Test executes adapter.Test using temporary form values overlaid onto the
// saved config and secrets (so blank password fields reuse saved secrets)
// without persisting or polluting the conversation/inbox.
func (m *IntegrationManager) Test(ctx context.Context, kind string, req SaveRequest) (TestResult, error) {
	adapter, cfg, err := m.overlayTempConfig(kind, req)
	if err != nil {
		if adapter == nil {
			return TestResult{}, err
		}
		return TestResult{OK: false, Message: err.Error()}, nil
	}
	res := adapter.Test(ctx, cfg)
	res.Message = m.redact(res.Message)
	return res, nil
}

// Probe executes adapter.ProbeView using temporary form values overlaid onto
// saved config and secrets without mutating state.
func (m *IntegrationManager) Probe(ctx context.Context, kind string, req SaveRequest) (map[string]any, error) {
	adapter, cfg, err := m.overlayTempConfig(kind, req)
	if err != nil {
		return nil, err
	}
	p, ok := adapter.(IntegrationProber)
	if !ok {
		return nil, fmt.Errorf("不支持探测该接入：%s", kind)
	}
	res, err := p.ProbeView(ctx, cfg)
	if err != nil {
		return nil, errors.New(m.redact(err.Error()))
	}
	if prob, ok := res["problem"].(string); ok {
		res["problem"] = m.redact(prob)
	}
	return res, nil
}

// SamplePull executes PullSampler.SamplePull with temporary form overlay.
func (m *IntegrationManager) SamplePull(ctx context.Context, kind string, req SaveRequest, limit int) ([]SampleEvent, error) {
	adapter, cfg, err := m.overlayTempConfig(kind, req)
	if err != nil {
		return nil, err
	}
	ps, ok := adapter.(PullSampler)
	if !ok {
		return nil, fmt.Errorf("该接入不支持试拉预览：%s", kind)
	}
	evs, err := ps.SamplePull(ctx, cfg, limit)
	if err != nil {
		return nil, errors.New(m.redact(err.Error()))
	}
	return evs, nil
}

// ---------------------------------------------------------------- outbound hooks

// SendReply dispatches Collie's reply to any active OutboundIntegration.
func (m *IntegrationManager) SendReply(ctx context.Context, e *Entry, link string) {
	if m == nil || e == nil {
		return
	}
	m.mu.Lock()
	var outs []OutboundIntegration
	for k, item := range m.items {
		rec := m.records[k]
		if !rec.Enabled {
			continue
		}
		if out, ok := item.adapter.(OutboundIntegration); ok {
			outs = append(outs, out)
		}
	}
	m.mu.Unlock()

	for _, out := range outs {
		receipt, err := out.SendReply(ctx, e, link)
		if err != nil {
			log.Printf("integration %s send reply: %s", out.Kind(), m.redact(err.Error()))
			continue
		}
		if receipt.ExternalID != "" {
			if err := saveIntegrationMessage(m.db, IntegrationMessage{
				EntryID:    e.ID,
				Kind:       out.Kind(),
				ChatID:     receipt.ChatID,
				ExternalID: receipt.ExternalID,
				CreatedAt:  m.now().Unix(),
			}); err != nil {
				log.Printf("save integration message (%s/%s): %v", out.Kind(), e.ID, err)
			}
			m.recordActivity(out.Kind())
		}
	}
}

// SyncResolved updates any external card/message linked to entryID when it is
// resolved from Web UI or another channel.
func (m *IntegrationManager) SyncResolved(ctx context.Context, entryID, summary, skipKind string) {
	if m == nil || strings.TrimSpace(entryID) == "" {
		return
	}
	m.mu.Lock()
	var outs []OutboundIntegration
	for _, item := range m.items {
		if item.adapter.Kind() == skipKind {
			continue
		}
		if out, ok := item.adapter.(OutboundIntegration); ok {
			outs = append(outs, out)
		}
	}
	m.mu.Unlock()

	for _, out := range outs {
		rec, err := getIntegrationMessage(m.db, entryID, out.Kind())
		if err != nil || rec == nil || rec.Resolved || rec.ExternalID == "" {
			continue
		}
		changed, err := markIntegrationMessageResolved(m.db, entryID, out.Kind())
		if err != nil || !changed {
			continue
		}
		receipt := OutboundReceipt{
			Kind:       out.Kind(),
			ChatID:     rec.ChatID,
			ExternalID: rec.ExternalID,
		}
		if err := out.SyncResolved(ctx, receipt, summary); err != nil {
			log.Printf("integration %s sync resolved (%s): %s", out.Kind(), entryID, m.redact(err.Error()))
		}
	}
}

// ---------------------------------------------------------------- EventSink

type managerSink struct {
	mgr  *IntegrationManager
	kind string
}

func (s *managerSink) bucket(key string, ratePerSec, burst float64) *tokenBucket {
	s.mgr.mu.Lock()
	defer s.mgr.mu.Unlock()
	b := s.mgr.buckets[key]
	if b == nil {
		b = newTokenBucket(ratePerSec, burst)
		s.mgr.buckets[key] = b
	}
	return b
}

func (s *managerSink) ReportHealth(state HealthState, detail string) {
	s.mgr.reportHealth(s.kind, state, detail)
}

func (s *managerSink) EmitHuman(ctx context.Context, ev HumanEvent) error {
	src := ev.Source
	if src == "" {
		src = s.kind
	}
	now := s.mgr.now()
	if ev.DedupKey != "" {
		ok, err := dedupInsert(s.mgr.db, src, ev.DedupKey, now)
		if err != nil {
			return err
		}
		if !ok {
			return nil
		}
	}
	hb := s.bucket("human:"+src, s.mgr.humanRatePerSec, s.mgr.humanBurst)
	if !hb.allow(now) {
		return nil
	}
	text := strings.TrimSpace(ev.Text)
	if text == "" {
		return nil
	}
	thread := ev.Thread
	if thread == "" {
		thread = mainThread
	}
	from := ev.From
	if from == "" {
		from = "you"
	}

	sp := s.mgr.sp
	if sp == nil {
		return nil
	}
	sp.mu.Lock()
	var beforePending []PendingItem
	if thread == mainThread {
		beforePending = sp.pendingLocked(now)
	}
	re := sp.sayQuoteLocked(ev.ReEntryID)
	e := sp.addEntryReLocked("user", thread, text, "", "", re)
	e.Channel = src
	promptText := strings.TrimSpace(ev.Prompt)
	if promptText == "" {
		promptText = text
	}
	if re != nil {
		if re.Who == "你" {
			promptText = "[In reply to what the human said earlier: \"" + clip(re.Text, 300) + "\"]\n" + promptText
		} else {
			promptText = "[In reply to what you said earlier: \"" + clip(re.Text, 300) + "\"]\n" + promptText
		}
	}
	// Source "chat" guarantees human-interrupt priority in Super.tick() (fromHuman).
	sp.ingestLocked("chat", thread, from, promptText, "", "", e.ID)
	var afterPending []PendingItem
	if thread == mainThread {
		afterPending = sp.pendingLocked(now)
	}
	sp.mu.Unlock()

	if thread == mainThread && e != nil && e.ID != "" {
		_, _, _ = sp.MarkChatRead(e.ID)
	}
	s.mgr.recordActivity(s.kind)
	if thread == mainThread {
		stillPending := make(map[string]bool, len(afterPending))
		for _, p := range afterPending {
			stillPending[p.ID] = true
		}
		resolvedIDs := map[string]bool{}
		for _, p := range beforePending {
			if !stillPending[p.ID] {
				resolvedIDs[p.ID] = true
			}
		}
		if ev.ReEntryID != "" {
			resolvedIDs[ev.ReEntryID] = true
		}
		if len(resolvedIDs) > 0 {
			directSummary := "已回复：" + clip(oneLine(text), 40)
			for rid := range resolvedIDs {
				id := rid
				if id == ev.ReEntryID {
					go s.mgr.SyncResolved(context.Background(), id, "已回复处理", src)
				} else {
					go s.mgr.SyncResolved(context.Background(), id, directSummary, "")
				}
			}
		}
	} else if ev.ReEntryID != "" {
		go s.mgr.SyncResolved(context.Background(), ev.ReEntryID, "已回复处理", src)
	}
	return nil
}

func (s *managerSink) EmitEvent(ctx context.Context, ev ExternalEvent) (bool, error) {
	src := ev.Source
	if src == "" {
		src = s.kind
	}
	now := s.mgr.now()
	if ev.DedupKey != "" {
		ok, err := dedupInsert(s.mgr.db, src, ev.DedupKey, now)
		if err != nil {
			return false, err
		}
		if !ok {
			return false, nil
		}
	}
	eb := s.bucket("event:"+src, s.mgr.eventRatePerMin/60.0, s.mgr.eventBurst)
	if !eb.allow(now) {
		if folded := eb.takeFolded(now, s.mgr.foldWindow); folded > 0 && s.mgr.sp != nil {
			summary := fmt.Sprintf("%s 短时间内涌入 %d 条动态，已折叠防刷屏，可去收件箱查看或回复展开。", src, folded)
			s.mgr.sp.Ingest("webhook:"+src, mainThread, src, summary, "", "", "")
		}
		return false, nil
	}
	if s.mgr.sp != nil {
		thread := ev.Thread
		if thread == "" {
			thread = src + ":events"
		}
		if ev.NotifyOnly {
			s.mgr.sp.IngestNotify("webhook:"+src, thread, ev.From, ev.Text, ev.DedupKey, ev.Ref, ev.Summary, ev.Tone)
		} else if ev.Action != "" {
			s.mgr.sp.IngestIntegration("webhook:"+src, thread, ev.From, ev.Text, ev.DedupKey, ev.Target, ev.Ref, ev.Action)
		} else {
			s.mgr.sp.Ingest("webhook:"+src, thread, ev.From, ev.Text, ev.DedupKey, ev.Target, ev.Ref)
		}
	}
	s.mgr.recordActivity(s.kind)
	return true, nil
}
