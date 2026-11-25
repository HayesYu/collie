package collie

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func resetClaimedForTest(t *testing.T) {
	t.Helper()
	claimed.Lock()
	orig := claimed.m
	claimed.m = map[string]string{}
	claimed.Unlock()
	t.Cleanup(func() {
		claimed.Lock()
		claimed.m = orig
		claimed.Unlock()
	})
}

func resetTitlesForTest(t *testing.T) {
	t.Helper()
	titles.Lock()
	orig := titles.by
	titles.by = map[string]*titleCache{}
	titles.Unlock()
	codexNames.Lock()
	origMod, origM, origByName := codexNames.mod, codexNames.m, codexNames.byName
	codexNames.mod, codexNames.m, codexNames.byName = time.Time{}, nil, nil
	codexNames.Unlock()
	t.Cleanup(func() {
		titles.Lock()
		titles.by = orig
		titles.Unlock()
		codexNames.Lock()
		codexNames.mod, codexNames.m, codexNames.byName = origMod, origM, origByName
		codexNames.Unlock()
	})
}

func writeRollout(t *testing.T, root, id, cwd string, born, mod time.Time, extra string) string {
	t.Helper()
	dir := filepath.Join(root, "sessions", "2026", "11", "25")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	name := fmt.Sprintf("rollout-%s-%s.jsonl", born.UTC().Format("2006-01-02T15-04-05"), id)
	p := filepath.Join(dir, name)
	meta := map[string]any{
		"timestamp": born.UTC().Format(time.RFC3339Nano),
		"type":      "session_meta",
		"payload": map[string]any{
			"id":        id,
			"cwd":       cwd,
			"timestamp": born.UTC().Format(time.RFC3339Nano),
		},
	}
	b, _ := json.Marshal(meta)
	content := string(b) + "\n" + extra
	if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(p, mod, mod); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestGuessCodexMultiPaneSameDirAndExclusiveClaim(t *testing.T) {
	resetClaimedForTest(t)
	root := t.TempDir()
	realDir := filepath.Join(root, "work")
	if err := os.MkdirAll(realDir, 0o755); err != nil {
		t.Fatal(err)
	}
	linkDir := filepath.Join(root, "work-link")
	if err := os.Symlink(realDir, linkDir); err != nil {
		t.Fatal(err)
	}

	now := time.Now().UTC()
	id1 := "11111111-1111-1111-1111-111111111111"
	id2 := "22222222-2222-2222-2222-222222222222"
	idOther := "33333333-3333-3333-3333-333333333333"

	p1 := writeRollout(t, root, id1, realDir, now.Add(1*time.Second), now.Add(1*time.Second), "")
	p2 := writeRollout(t, root, id2, realDir, now.Add(6*time.Second), now.Add(6*time.Second), "")
	writeRollout(t, root, idOther, filepath.Join(root, "other"), now.Add(2*time.Second), now.Add(2*time.Second), "")

	s := &Server{
		started: map[string]time.Time{
			"w1:p1": now,
			"w2:p1": now.Add(5 * time.Second),
		},
	}

	// Even if pane 2 polls FIRST (within 10s of pane 1), it prefers the rollout
	// created after its own start time (p2) rather than stealing pane 1's (p1).
	if got := s.guessCodex(root, "w2:p1", linkDir); got != p2 {
		t.Fatalf("pane 2 got %q, want %q", got, p2)
	}
	if got := s.guessCodex(root, "w1:p1", realDir); got != p1 {
		t.Fatalf("pane 1 got %q, want %q", got, p1)
	}
	// Repeated lookups stay pinned to the claimed file.
	if got := s.guessCodex(root, "w1:p1", realDir); got != p1 {
		t.Fatalf("pane 1 repeat got %q, want %q", got, p1)
	}
	// A third pane in the same dir cannot steal already-claimed rollouts.
	s.startMu.Lock()
	s.started["w3:p1"] = now
	s.startMu.Unlock()
	if got := s.guessCodex(root, "w3:p1", realDir); got != "" {
		t.Fatalf("pane 3 stole claimed rollout %q", got)
	}
}

func TestGuessCodexTimeWindowAndReuseRetirement(t *testing.T) {
	resetClaimedForTest(t)
	root := t.TempDir()
	cwd := filepath.Join(root, "proj")
	now := time.Now().UTC()

	// An older active session (born 3m ago, still written now) must not be
	// matched by a brand-new pane in the same directory.
	oldActive := writeRollout(t, root, "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa", cwd, now.Add(-3*time.Minute), now, "")
	// A rollout within the 10s pre-start clock-skew slack IS eligible.
	slack := writeRollout(t, root, "bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb", cwd, now.Add(-4*time.Second), now, "")

	s := &Server{
		started: map[string]time.Time{
			"w1:p1": now,
		},
	}
	if got := s.guessCodex(root, "w1:p1", cwd); got != slack {
		t.Fatalf("got %q, want slack rollout %q (must reject oldActive %q)", got, slack, oldActive)
	}

	// When herdr reuses pane w1:p1, markStarted retires the previous claim so
	// w1:p1 neither returns the old file nor lets another pane steal it.
	s.markStarted("w1:p1")
	if got := s.guessCodex(root, "w1:p1", cwd); got != "" {
		t.Fatalf("reused pane got stale rollout %q, want empty", got)
	}
	s.startMu.Lock()
	s.started["w9:p1"] = now.Add(-5 * time.Second)
	s.startMu.Unlock()
	if got := s.guessCodex(root, "w9:p1", cwd); got != "" {
		t.Fatalf("another pane stole retired rollout %q", got)
	}

	// Unknown start time: ignores >6h old files and picks the most recently active one.
	cwdUnknown := filepath.Join(root, "proj-unknown")
	writeRollout(t, root, "cccccccc-cccc-cccc-cccc-cccccccccccc", cwdUnknown, now.Add(-8*time.Hour), now.Add(-8*time.Hour), "")
	recent := writeRollout(t, root, "dddddddd-dddd-dddd-dddd-dddddddddddd", cwdUnknown, now.Add(-2*time.Hour), now.Add(-1*time.Minute), "")
	writeRollout(t, root, "eeeeeeee-eeee-eeee-eeee-eeeeeeeeeeee", cwdUnknown, now.Add(-1*time.Hour), now.Add(-30*time.Minute), "")
	if got := s.guessCodex(root, "w4:p1", cwdUnknown); got != recent {
		t.Fatalf("unknown start time got %q, want most recently modified %q", got, recent)
	}

	// Explicit claimTranscript (e.g. `codex resume`) attaches an arbitrarily old rollout.
	veryOld := writeRollout(t, root, "ffffffff-ffff-ffff-ffff-ffffffffffff", cwd, now.Add(-48*time.Hour), now.Add(-48*time.Hour), "")
	claimTranscript(veryOld, "w5:p1")
	if got := s.guessCodex(root, "w5:p1", cwd); got != veryOld {
		t.Fatalf("resumed claim got %q, want %q", got, veryOld)
	}

	// Empty pane id or explicit missing session id never guesses.
	t.Setenv("CODEX_HOME", root)
	if p, _ := s.findTranscript("codex", "", "", "", cwd); p != "" {
		t.Fatalf("empty pane guessed %q", p)
	}
	if p, _ := s.findTranscript("codex", "id", "00000000-0000-0000-0000-000000000000", "w6:p1", cwd); p != "" {
		t.Fatalf("explicit missing session id guessed %q", p)
	}
}

func TestZeroHookCodexSessionBackfillAndHandover(t *testing.T) {
	resetClaimedForTest(t)
	resetTitlesForTest(t)

	codexHome := t.TempDir()
	t.Setenv("CODEX_HOME", codexHome)
	cwd := filepath.Join(codexHome, "router")
	if err := os.MkdirAll(cwd, 0o755); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	sid := "01a1fa68-5cc0-7561-9652-081fb93ad0f9"
	tokenLine := `{"type":"event_msg","payload":{"type":"token_count","info":{"last_token_usage":{"total_tokens":75000},"model_context_window":100000}}}` + "\n"
	writeRollout(t, codexHome, sid, cwd, now, now, tokenLine)

	// Fake local herdr reporting a Codex agent with agent_session = null (no hook).
	h := &Herdr{
		local: &Machine{
			ID: "", Name: "test", Local: true, Online: true,
			conn: &herdrConn{dial: func(ctx context.Context) (net.Conn, error) {
				c1, c2 := net.Pipe()
				go func() {
					defer c2.Close()
					sc := bufio.NewScanner(c2)
					for sc.Scan() {
						var req struct {
							ID     string `json:"id"`
							Method string `json:"method"`
						}
						if json.Unmarshal(sc.Bytes(), &req) != nil {
							return
						}
						var resp map[string]any
						switch req.Method {
						case "agent.list":
							resp = map[string]any{
								"id": req.ID,
								"result": map[string]any{
									"agents": []map[string]any{{
										"name":              routerName,
										"pane_id":           "w1:p1",
										"agent":             "codex",
										"agent_status":      "idle",
										"interactive_ready": true,
										"cwd":               cwd,
										"agent_session":     nil,
									}},
								},
							}
						case "pane.list":
							resp = map[string]any{
								"id": req.ID,
								"result": map[string]any{
									"panes": []map[string]any{{"pane_id": "w1:p1"}},
								},
							}
						case "pane.read":
							resp = map[string]any{
								"id": req.ID,
								"result": map[string]any{
									"read": map[string]any{"text": "› \n"},
								},
							}
						case "agent.prompt":
							resp = map[string]any{
								"id":     req.ID,
								"result": map[string]any{"ok": true},
							}
						}
						b, _ := json.Marshal(resp)
						c2.Write(append(b, '\n'))
					}
				}()
				return c1, nil
			}},
		},
		remote: map[string]*Machine{},
		names:  map[string]string{},
		named:  map[string]string{},
		runs:   map[string][2]string{},
		asks:   map[string]askCheck{},
	}

	s := &Server{
		herdr:   h,
		home:    codexHome,
		started: map[string]time.Time{"w1:p1": now.Add(-1 * time.Second)},
	}
	sp := &Super{
		s:              s,
		dir:            codexHome,
		st:             &State{Settings: Settings{RotatePct: 60}, Workers: map[string]*Worker{}, Threads: map[string]*Thread{}},
		subs:           map[chan struct{}]bool{},
		routerLaunchAt: time.Now(), // recent launch so checkHandover computes usage without prompting
	}

	// 1) titles.go localSessions backfills Codex session without hook.
	ls := s.localSessions(context.Background())
	if got := ls["w1:p1"].Session; got != sid {
		t.Fatalf("localSessions Session = %q, want %q", got, sid)
	}

	// 2) super.go liveAgents backfills Codex session without hook.
	live, ok := sp.liveAgents(context.Background())
	if !ok || live[routerName].Session != sid {
		t.Fatalf("liveAgents = %+v (ok=%v), want session %q", live, ok, sid)
	}

	// 3) handover.go checkHandover computes Codex context usage and starts handover
	// with the resolved session id even when a.Session == "".
	sp.routerLaunchAt = time.Now().Add(-10 * time.Minute)
	sp.checkHandover(context.Background(), map[string]liveAgent{
		routerName: {Pane: "w1:p1", Status: "idle", Kind: "codex", Session: ""},
	}, false)
	sp.mu.Lock()
	usage := sp.routerUsage
	ho := sp.handover
	sp.mu.Unlock()
	if usage.Used != 75000 || usage.Window != 100000 || usage.Pct() != 75 {
		t.Fatalf("routerUsage = %+v (pct=%d), want 75000/100000 (75%%)", usage, usage.Pct())
	}
	if ho == nil || ho.session != sid || ho.kind != "codex" {
		t.Fatalf("handover = %+v, want codex session %q", ho, sid)
	}
}
