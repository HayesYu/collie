package collie

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type fakeIntegration struct {
	kind       string
	configured bool
	runFn      func(ctx context.Context, cfg IntegrationConfig, sink EventSink) error
	testFn     func(ctx context.Context, cfg IntegrationConfig) TestResult

	starts atomic.Int32
}

func (f *fakeIntegration) Kind() string { return f.kind }
func (f *fakeIntegration) Run(ctx context.Context, cfg IntegrationConfig, sink EventSink) error {
	f.starts.Add(1)
	if f.runFn != nil {
		return f.runFn(ctx, cfg, sink)
	}
	sink.ReportHealth(HealthConnected, "ok")
	<-ctx.Done()
	return ctx.Err()
}
func (f *fakeIntegration) Health() IntegrationHealth {
	return IntegrationHealth{State: HealthConnected}
}
func (f *fakeIntegration) Test(ctx context.Context, cfg IntegrationConfig) TestResult {
	if f.testFn != nil {
		return f.testFn(ctx, cfg)
	}
	return TestResult{OK: true, Message: "ok"}
}
func (f *fakeIntegration) Meta() IntegrationMeta {
	return IntegrationMeta{Kind: f.kind, Name: f.kind}
}
func (f *fakeIntegration) IsConfigured(cfg IntegrationConfig) bool { return f.configured }
func (f *fakeIntegration) SanitizeConfig(cfg IntegrationConfig) map[string]any {
	return map[string]any{}
}
func (f *fakeIntegration) SecretSet(cfg IntegrationConfig) bool { return len(cfg.Secrets) > 0 }
func (f *fakeIntegration) ValidateSave(raw json.RawMessage) (json.RawMessage, error) {
	return raw, nil
}

func TestIntegrationPersistentDedupAcrossRestart(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "herd.db")
	db, err := openDB(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	st, err := initState(db, dir, defaultSettings)
	if err != nil {
		t.Fatal(err)
	}
	sec, _ := openSecretStore(dir)
	sp := &Super{db: db, st: st, subs: map[chan struct{}]bool{}}
	mgr := newIntegrationManager(sp, db, sec)
	sink := &managerSink{mgr: mgr, kind: "feishu"}

	ev := HumanEvent{
		Source:   "feishu",
		DedupKey: "evt_unique_001",
		Thread:   mainThread,
		From:     "feishu",
		Text:     "hello once",
	}
	if err := sink.EmitHuman(context.Background(), ev); err != nil {
		t.Fatalf("first EmitHuman: %v", err)
	}
	if err := sink.EmitHuman(context.Background(), ev); err != nil {
		t.Fatalf("second EmitHuman: %v", err)
	}
	if len(sp.st.Entries) != 1 || len(sp.st.Triggers) != 1 {
		t.Fatalf("expected 1 entry & 1 trigger after duplicate emit, got entries=%d triggers=%d", len(sp.st.Entries), len(sp.st.Triggers))
	}
	if sp.st.Entries[0].Channel != "feishu" || sp.st.Triggers[0].Source != "chat" {
		t.Fatalf("unexpected entry/trigger metadata: channel=%q source=%q", sp.st.Entries[0].Channel, sp.st.Triggers[0].Source)
	}
	sp.flush()
	db.Close()

	// Reopen SQLite (simulating Hub restart) and replay the exact same event_id.
	db2, err := openDB(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db2.Close()
	st2, err := loadState(db2, dir)
	if err != nil {
		t.Fatal(err)
	}
	sp2 := &Super{db: db2, st: st2, subs: map[chan struct{}]bool{}}
	mgr2 := newIntegrationManager(sp2, db2, sec)
	sink2 := &managerSink{mgr: mgr2, kind: "feishu"}

	if err := sink2.EmitHuman(context.Background(), ev); err != nil {
		t.Fatalf("replayed EmitHuman after restart: %v", err)
	}
	if len(sp2.st.Entries) != 1 || len(sp2.st.Triggers) != 1 {
		t.Fatalf("replay after restart created duplicate! entries=%d triggers=%d", len(sp2.st.Entries), len(sp2.st.Triggers))
	}

	// Prune records older than 30 days while keeping recent keys.
	_, _ = dedupInsert(db2, "feishu", "evt_old_001", time.Now().Add(-35*24*time.Hour))
	pruned, err := dedupPrune(db2, time.Now().Add(-30*24*time.Hour))
	if err != nil || pruned != 1 {
		t.Fatalf("dedupPrune = (%d, %v), want (1, nil)", pruned, err)
	}
}

func TestIntegrationBackoffAndAuthCircuitBreaker(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	db, err := openDB(filepath.Join(dir, "herd.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	st, _ := initState(db, dir, defaultSettings)
	sec, _ := openSecretStore(dir)
	_ = sec.Update("authfail", map[string]string{"app_secret": "sec_top_secret_xyz"})
	sp := &Super{db: db, st: st, subs: map[chan struct{}]bool{}}
	mgr := newIntegrationManager(sp, db, sec)
	mgr.backoffSchedule = []time.Duration{15 * time.Millisecond, 35 * time.Millisecond, 70 * time.Millisecond}
	mgr.backoffJitter = 0
	mgr.stableReset = 80 * time.Millisecond

	// 1) Transient errors back off with increasing intervals, then reset after stable uptime.
	var mu sync.Mutex
	var times []time.Time
	doneTransient := make(chan struct{})
	transient := &fakeIntegration{
		kind:       "flaky",
		configured: true,
		runFn: func(ctx context.Context, cfg IntegrationConfig, sink EventSink) error {
			mu.Lock()
			times = append(times, time.Now())
			n := len(times)
			mu.Unlock()
			switch n {
			case 1, 2:
				return errors.New("temporary network glitch")
			case 3:
				sink.ReportHealth(HealthConnected, "connected")
				// Stay connected longer than stableReset so backoff resets to attempt 0.
				select {
				case <-time.After(95 * time.Millisecond):
					return errors.New("drop after stable run")
				case <-ctx.Done():
					return ctx.Err()
				}
			default:
				select {
				case <-doneTransient:
				default:
					close(doneTransient)
				}
				<-ctx.Done()
				return ctx.Err()
			}
		},
	}

	// 2) Permanent ErrAuthFailed must immediately circuit-break without retrying and redact secret.
	authFailed := make(chan struct{})
	authAdapter := &fakeIntegration{
		kind:       "authfail",
		configured: true,
		runFn: func(ctx context.Context, cfg IntegrationConfig, sink EventSink) error {
			defer func() {
				select {
				case <-authFailed:
				default:
					close(authFailed)
				}
			}()
			return fmt.Errorf("%w: invalid secret sec_top_secret_xyz", ErrAuthFailed)
		},
	}

	mgr.Register(transient)
	mgr.Register(authAdapter)
	_ = saveIntegration(db, IntegrationRecord{Kind: "flaky", Enabled: true, Config: json.RawMessage(`{}`)})
	_ = saveIntegration(db, IntegrationRecord{Kind: "authfail", Enabled: true, Config: json.RawMessage(`{}`)})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	mgr.Start(ctx)
	defer mgr.Stop(time.Second)

	select {
	case <-doneTransient:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for transient backoff sequence")
	}
	select {
	case <-authFailed:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for authfail run")
	}
	// Give authfail plenty of time to prove it does NOT retry.
	time.Sleep(80 * time.Millisecond)

	if got := authAdapter.starts.Load(); got != 1 {
		t.Fatalf("authfail retried %d times, want 1 (circuit-broken)", got)
	}
	ah := mgr.Health("authfail")
	if ah.State != HealthError {
		t.Fatalf("authfail health state = %q, want error", ah.State)
	}
	if strings.Contains(ah.LastError, "sec_top_secret_xyz") || !strings.Contains(ah.LastError, "***") {
		t.Fatalf("authfail error leaked or failed to redact secret: %q", ah.LastError)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(times) < 4 {
		t.Fatalf("expected 4 runs of flaky adapter, got %d", len(times))
	}
	gap1 := times[1].Sub(times[0])
	gap2 := times[2].Sub(times[1])
	// gap2 (~35ms) should exceed gap1 (~15ms).
	if gap2 <= gap1 {
		t.Fatalf("expected increasing backoff: gap1=%v gap2=%v", gap1, gap2)
	}
	// Run 3 lasted >= 95ms (>= stableReset), so gap3 after run 3's end should reset to ~15ms (< 30ms).
	gap3 := times[3].Sub(times[2]) - 95*time.Millisecond
	if gap3 > 32*time.Millisecond {
		t.Fatalf("expected backoff reset after stable run, got extra gap3=%v", gap3)
	}
}

func TestIntegrationSinglePointHotReload(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	db, _ := openDB(filepath.Join(dir, "herd.db"))
	defer db.Close()
	st, _ := initState(db, dir, defaultSettings)
	sec, _ := openSecretStore(dir)
	sp := &Super{db: db, st: st, subs: map[chan struct{}]bool{}}
	mgr := newIntegrationManager(sp, db, sec)

	a := &fakeIntegration{kind: "alpha", configured: true}
	b := &fakeIntegration{kind: "beta", configured: true}
	mgr.Register(a)
	mgr.Register(b)
	_ = saveIntegration(db, IntegrationRecord{Kind: "alpha", Enabled: true, Config: json.RawMessage(`{}`)})
	_ = saveIntegration(db, IntegrationRecord{Kind: "beta", Enabled: true, Config: json.RawMessage(`{}`)})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	mgr.Start(ctx)
	defer mgr.Stop(time.Second)

	deadline := time.Now().Add(time.Second)
	for (a.starts.Load() < 1 || b.starts.Load() < 1) && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if a.starts.Load() != 1 || b.starts.Load() != 1 {
		t.Fatalf("initial starts a=%d b=%d, want 1,1", a.starts.Load(), b.starts.Load())
	}

	// Reload only alpha; beta must NOT restart.
	mgr.Reload("alpha")
	deadline = time.Now().Add(time.Second)
	for a.starts.Load() < 2 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if a.starts.Load() != 2 || b.starts.Load() != 1 {
		t.Fatalf("after single-point reload: a=%d b=%d, want 2,1", a.starts.Load(), b.starts.Load())
	}
}

func TestIntegrationRateLimitAntiStorm(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	db, _ := openDB(filepath.Join(dir, "herd.db"))
	defer db.Close()
	st, _ := initState(db, dir, defaultSettings)
	sec, _ := openSecretStore(dir)
	sp := &Super{db: db, st: st, subs: map[chan struct{}]bool{}}
	mgr := newIntegrationManager(sp, db, sec)
	mgr.eventBurst = 8
	mgr.eventRatePerMin = 20
	mgr.foldWindow = time.Minute
	fixed := time.Now()
	mgr.nowFn = func() time.Time { return fixed }

	sink := &managerSink{mgr: mgr, kind: "gitforge"}
	accepted := 0
	for i := 0; i < 30; i++ {
		ok, err := sink.EmitEvent(context.Background(), ExternalEvent{
			Source:   "gitforge",
			DedupKey: fmt.Sprintf("evt:%d", i),
			Thread:   "gitforge:org/repo#1",
			From:     "bot",
			Text:     fmt.Sprintf("event #%d", i),
		})
		if err != nil {
			t.Fatalf("EmitEvent %d: %v", i, err)
		}
		if ok {
			accepted++
		}
	}
	if accepted != 8 {
		t.Fatalf("accepted = %d, want 8 (burst cap)", accepted)
	}
	// 8 normal triggers + 1 folded storm summary trigger = 9 total (NOT 30!).
	if got := len(sp.st.Triggers); got != 9 {
		t.Fatalf("triggers = %d, want 9 (8 burst + 1 folded summary)", got)
	}
	last := sp.st.Triggers[len(sp.st.Triggers)-1]
	if !strings.Contains(last.Text, "已折叠") {
		t.Fatalf("expected folded storm summary, got %q", last.Text)
	}
}
