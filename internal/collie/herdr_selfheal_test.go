package collie

import (
	"bufio"
	"context"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"testing/fstest"
	"time"
)

func startFakeHerdrSocket(t *testing.T, sock string, handler func(method string, params map[string]any) (any, *HerdrError, bool)) func() {
	t.Helper()
	_ = os.Remove(sock)
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatalf("listen unix %s: %v", sock, err)
	}
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func(conn net.Conn) {
				defer conn.Close()
				r := bufio.NewReader(conn)
				line, err := r.ReadBytes('\n')
				if err != nil {
					return
				}
				var req struct {
					ID     string         `json:"id"`
					Method string         `json:"method"`
					Params map[string]any `json:"params"`
				}
				if json.Unmarshal(line, &req) != nil {
					return
				}
				res, herr, drop := handler(req.Method, req.Params)
				if drop {
					return // simulate abrupt herdr crash mid-RPC (EOF)
				}
				env := map[string]any{"id": req.ID}
				if herr != nil {
					env["error"] = herr
				} else {
					env["result"] = res
				}
				b, _ := json.Marshal(env)
				_, _ = conn.Write(append(b, '\n'))
			}(c)
		}
	}()
	var once sync.Once
	stop := func() {
		once.Do(func() {
			_ = ln.Close()
			_ = os.Remove(sock)
			wg.Wait()
		})
	}
	t.Cleanup(stop)
	return stop
}

func TestHerdrDialSelfHealAndReadRetry(t *testing.T) {
	origBootWait := herdrBootWait
	origReconnWait := herdrReconnectWait
	origPoll := herdrDialPollInterval
	origRetry := herdrRetryDelay
	origStartFn := startHerdrServerFn
	origStarted := herdrStarted.Load()
	herdrBootMu.Lock()
	origBootSocket := herdrBootSocket
	origBootUntil := herdrBootUntil
	herdrBootSocket = ""
	herdrBootUntil = time.Time{}
	herdrBootMu.Unlock()
	t.Cleanup(func() {
		herdrBootWait = origBootWait
		herdrReconnectWait = origReconnWait
		herdrDialPollInterval = origPoll
		herdrRetryDelay = origRetry
		startHerdrServerFn = origStartFn
		herdrStarted.Store(origStarted)
		herdrBootMu.Lock()
		herdrBootSocket = origBootSocket
		herdrBootUntil = origBootUntil
		herdrBootMu.Unlock()
	})

	herdrBootWait = 2 * time.Second
	herdrReconnectWait = 2 * time.Second
	herdrDialPollInterval = 25 * time.Millisecond
	herdrRetryDelay = 25 * time.Millisecond
	herdrStarted.Store(0)

	sock := filepath.Join(t.TempDir(), "herdr.sock")

	// 1) Concurrent callers during a herdr boot share the boot wait window and both succeed.
	var launched atomic.Int32
	stopCh1 := make(chan func(), 1)
	startHerdrServerFn = func() bool {
		if !launched.CompareAndSwap(0, 1) {
			return false
		}
		go func() {
			time.Sleep(120 * time.Millisecond)
			stopCh1 <- startFakeHerdrSocket(t, sock, func(method string, params map[string]any) (any, *HerdrError, bool) {
				return map[string]any{"agents": []any{}}, nil, false
			})
		}()
		return true
	}

	hc := &herdrConn{dial: unixDialer(sock)}
	var wg sync.WaitGroup
	errs := make(chan error, 2)
	for range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			_, err := hc.call(ctx, "agent.list", nil)
			errs <- err
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("concurrent dial during herdr boot failed: %v", err)
		}
	}
	if launched.Load() != 1 {
		t.Fatalf("expected exactly 1 herdr launch attempt, got %d", launched.Load())
	}

	// 2) Previously connected socket survives a herdr restart (e.g. systemd restart)
	//    even when startHerdrServerFn declines to spawn a second process.
	stopFake1 := <-stopCh1
	stopFake1()
	startHerdrServerFn = func() bool { return false }
	var dropFirstRead atomic.Bool
	dropFirstRead.Store(true)
	var sendTextCount atomic.Int32
	stopCh2 := make(chan func(), 1)
	go func() {
		time.Sleep(120 * time.Millisecond)
		stopCh2 <- startFakeHerdrSocket(t, sock, func(method string, params map[string]any) (any, *HerdrError, bool) {
			if method == "agent.list" && dropFirstRead.Swap(false) {
				return nil, nil, true // EOF mid-flight on first read
			}
			if method == "pane.send_text" {
				sendTextCount.Add(1)
				return nil, nil, true // EOF on mutating RPC must NOT retry
			}
			return map[string]any{"agents": []any{}}, nil, false
		})
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if _, err := hc.call(ctx, "agent.list", nil); err != nil {
		t.Fatalf("read RPC failed to self-heal across restart + EOF: %v", err)
	}
	stopFake2 := <-stopCh2
	defer stopFake2()
	if err := func() error {
		_, e := hc.call(ctx, "pane.send_text", map[string]any{"pane_id": "w1:p1", "text": "hi"})
		return e
	}(); err == nil {
		t.Fatalf("expected EOF error on dropped mutating RPC")
	}
	if sendTextCount.Load() != 1 {
		t.Fatalf("mutating RPC pane.send_text was retried (%d calls), want 1", sendTextCount.Load())
	}
}

func TestSuperHerdrRestartSelfHealNoFalseExitAndAutoTrust(t *testing.T) {
	dir := t.TempDir()
	db, err := openDB(filepath.Join(dir, "herd.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	st, err := loadState(db, dir)
	if err != nil {
		t.Fatal(err)
	}
	st.Settings.AskPermissions = false // bypass permissions enabled

	routerDir := filepath.Join(dir, "router")
	workerDir := filepath.Join(dir, "work-repo")
	_ = os.MkdirAll(routerDir, 0o700)
	_ = os.MkdirAll(workerDir, 0o700)

	var (
		mu             sync.Mutex
		localDown      bool
		localAgentsVal []map[string]any
		screens        = map[string]string{}
		sentKeys       = map[string][]string{}
		sentTexts      = map[string][]string{}
	)

	makeConn := func(isLocal bool) *herdrConn {
		return &herdrConn{
			dial: func(ctx context.Context) (net.Conn, error) {
				mu.Lock()
				down := isLocal && localDown
				mu.Unlock()
				if down {
					return nil, os.ErrNotExist
				}
				c1, c2 := net.Pipe()
				go func() {
					defer c2.Close()
					r := bufio.NewReader(c2)
					line, err := r.ReadBytes('\n')
					if err != nil {
						return
					}
					var req struct {
						ID     string         `json:"id"`
						Method string         `json:"method"`
						Params map[string]any `json:"params"`
					}
					_ = json.Unmarshal(line, &req)
					var res any
					mu.Lock()
					switch req.Method {
					case "agent.list":
						if isLocal {
							res = map[string]any{"agents": localAgentsVal}
						} else {
							res = map[string]any{"agents": []any{}}
						}
					case "pane.read":
						paneID, _ := req.Params["pane_id"].(string)
						res = map[string]any{"read": map[string]any{"text": screens[paneID], "revision": 1}}
					case "pane.send_keys":
						paneID, _ := req.Params["pane_id"].(string)
						if ks, ok := req.Params["keys"].([]any); ok {
							for _, k := range ks {
								if s, ok := k.(string); ok {
									sentKeys[paneID] = append(sentKeys[paneID], s)
								}
							}
						}
						res = map[string]any{"ok": true}
					case "pane.send_text":
						paneID, _ := req.Params["pane_id"].(string)
						txt, _ := req.Params["text"].(string)
						sentTexts[paneID] = append(sentTexts[paneID], txt)
						res = map[string]any{"ok": true}
					default:
						res = map[string]any{}
					}
					mu.Unlock()
					b, _ := json.Marshal(map[string]any{"id": req.ID, "result": res})
					_, _ = c2.Write(append(b, '\n'))
				}()
				return c1, nil
			},
		}
	}

	herdr := &Herdr{
		local:      &Machine{ID: "", Name: "hub", Home: dir, Local: true, Online: true, conn: makeConn(true)},
		remote:     map[string]*Machine{"mac": {ID: "mac", Name: "mac", Online: true, conn: makeConn(false)}},
		names:      map[string]string{},
		named:      map[string]string{},
		runs:       map[string][2]string{"w1:p1": {"gpt-test", "high"}},
		asks:       map[string]askCheck{},
		lastListOK: map[string]bool{},
	}
	srv := &Server{herdr: herdr, token: "tok", home: dir, web: fstest.MapFS{}}
	sp := &Super{
		s:           srv,
		db:          db,
		dir:         dir,
		st:          st,
		subs:        map[chan struct{}]bool{},
		lastScreen:  map[string]screenMark{},
		transcripts: map[string]*transcript{},
		noAgent:     map[string]time.Time{},
		sayDedup:    map[string]sayReceipt{},
	}
	srv.sup = sp

	sp.st.Workers["wk-1"] = &Worker{
		Name:      "wk-1",
		Kind:      "claude",
		Cwd:       workerDir,
		Thread:    mainThread,
		PaneID:    "w1:p1",
		Status:    "working",
		CreatedAt: time.Now().Add(-time.Minute),
	}

	// Step 1: Healthy baseline tick sees router and wk-1 alive.
	mu.Lock()
	localAgentsVal = []map[string]any{
		{"name": routerName, "pane_id": "w0:p1", "agent": "claude", "agent_status": "idle", "interactive_ready": true},
		{"name": "wk-1", "pane_id": "w1:p1", "agent": "claude", "agent_status": "working", "interactive_ready": true},
	}
	mu.Unlock()
	sp.tick(context.Background())

	sp.mu.Lock()
	if !sp.routerWasAlive || sp.st.Workers["wk-1"].Ended {
		sp.mu.Unlock()
		t.Fatalf("baseline tick unexpected state: routerWasAlive=%v worker=%+v", sp.routerWasAlive, sp.st.Workers["wk-1"])
	}
	sp.mu.Unlock()

	// Step 2: Local herdr drops while remote node "mac" stays online.
	// Worker wk-1 must NOT be marked gone/ended, and local runs must NOT be pruned.
	mu.Lock()
	localDown = true
	mu.Unlock()
	sp.tick(context.Background())

	sp.mu.Lock()
	if !sp.herdrWasDown {
		sp.mu.Unlock()
		t.Fatal("expected herdrWasDown=true during local herdr outage")
	}
	if sp.st.Workers["wk-1"].Ended || sp.st.Workers["wk-1"].Status == "gone" {
		sp.mu.Unlock()
		t.Fatalf("local worker falsely marked gone during local herdr outage: %+v", sp.st.Workers["wk-1"])
	}
	sp.mu.Unlock()
	if herdr.MachineHerdrOK("") {
		t.Fatal("MachineHerdrOK(\"\") should be false while local herdr is down")
	}
	if _, ok := herdr.Run("w1:p1"); !ok {
		t.Fatal("local run metadata should survive temporary local herdr outage")
	}

	// Step 3: Local herdr recovers; both router and wk-1 come back directly in
	// "blocked" on folder-trust dialogs (no queued human trigger, no status transition
	// needed for wk-1). Collie must auto-trust both with zero manual approval.
	sp.mu.Lock()
	sp.st.Workers["wk-1"].Status = "blocked" // simulate worker starting/recovering already blocked
	sp.routerLaunchAt = time.Now()           // verify recovery clears stale cooldown
	sp.mu.Unlock()

	mu.Lock()
	localDown = false
	localAgentsVal = []map[string]any{
		{"name": routerName, "pane_id": "w0:p2", "agent": "claude", "agent_status": "blocked", "interactive_ready": true},
		{"name": "wk-1", "pane_id": "w2:p1", "agent": "claude", "agent_status": "blocked", "interactive_ready": true},
	}
	screens["w0:p2"] = "Accessing workspace:\n " + routerDir + "\nQuick safety check: Is this a project you trust?\n❯ 1. Yes, I trust this folder\n  2. No, exit\nEnter to confirm · Esc to cancel"
	screens["w2:p1"] = "Accessing workspace:\n " + workerDir + "\nQuick safety check: Is this a project you trust?\n❯ 1. Yes, I trust this folder\n  2. No, exit\nEnter to confirm · Esc to cancel"
	mu.Unlock()

	sp.tick(context.Background())

	sp.mu.Lock()
	if sp.herdrWasDown {
		sp.mu.Unlock()
		t.Fatal("herdrWasDown should clear once local herdr answers again")
	}
	if !sp.routerLaunchAt.IsZero() {
		sp.mu.Unlock()
		t.Fatal("routerLaunchAt cooldown should reset upon local herdr recovery")
	}
	if sp.st.Workers["wk-1"].Status != "idle" || sp.st.Workers["wk-1"].PaneID != "w2:p1" {
		sp.mu.Unlock()
		t.Fatalf("expected wk-1 auto-trusted to idle on restored pane w2:p1, got %+v", sp.st.Workers["wk-1"])
	}
	sp.mu.Unlock()

	mu.Lock()
	routerAnswered := len(sentKeys["w0:p2"]) > 0 || len(sentTexts["w0:p2"]) > 0
	workerAnswered := len(sentKeys["w2:p1"]) > 0 || len(sentTexts["w2:p1"]) > 0
	mu.Unlock()
	if !routerAnswered || !workerAnswered {
		t.Fatalf("expected auto-trust answers for both router (%v) and worker (%v)", routerAnswered, workerAnswered)
	}
	if !herdr.MachineHerdrOK("") {
		t.Fatal("MachineHerdrOK(\"\") should be true after recovery")
	}

	// Step 4: Worker that WAS marked gone restores cleanly even if herdr reassigned pane_id.
	sp.mu.Lock()
	sp.st.Workers["wk-gone"] = &Worker{
		Name:   "wk-gone",
		Kind:   "claude",
		Cwd:    workerDir,
		Thread: mainThread,
		PaneID: "w9:p1",
		Status: "gone",
		Ended:  true,
	}
	sp.mu.Unlock()
	mu.Lock()
	localAgentsVal = append(localAgentsVal, map[string]any{
		"name": "wk-gone", "pane_id": "w10:p1", "agent": "claude", "agent_status": "idle", "interactive_ready": true,
	})
	mu.Unlock()
	sp.tick(context.Background())

	sp.mu.Lock()
	wRestored := sp.st.Workers["wk-gone"]
	sp.mu.Unlock()
	if wRestored.Ended || wRestored.PaneID != "w10:p1" || wRestored.Status != "idle" {
		t.Fatalf("expected wk-gone restored on reassigned pane w10:p1, got %+v", wRestored)
	}
	if strings.Contains(wRestored.Note, "error") {
		t.Fatalf("unexpected error note on restored worker: %+v", wRestored)
	}
}
