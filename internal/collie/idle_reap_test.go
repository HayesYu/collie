package collie

import (
	"bufio"
	"context"
	"encoding/json"
	"net"
	"strings"
	"sync"
	"testing"
	"time"
)

// recordingHerdr is a local herdr that answers every call with ok and keeps
// the methods and params it was sent.
func recordingHerdr(t *testing.T) (*Herdr, func() []string) {
	t.Helper()
	var mu sync.Mutex
	var calls []string
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
							ID     string          `json:"id"`
							Method string          `json:"method"`
							Params json.RawMessage `json:"params"`
						}
						if json.Unmarshal(sc.Bytes(), &req) != nil {
							return
						}
						mu.Lock()
						calls = append(calls, req.Method+" "+string(req.Params))
						mu.Unlock()
						b, _ := json.Marshal(map[string]any{"id": req.ID, "result": map[string]any{"ok": true}})
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
	return h, func() []string {
		mu.Lock()
		defer mu.Unlock()
		return append([]string(nil), calls...)
	}
}

func newReapSuper(t *testing.T) (*Super, func() []string) {
	t.Helper()
	dir := t.TempDir()
	h, calls := recordingHerdr(t)
	s := &Server{herdr: h, home: dir}
	sp := &Super{s: s, dir: dir, st: defaultState(dir), subs: map[chan struct{}]bool{}}
	s.sup = sp
	return sp, calls
}

func addReapWorker(sp *Super, name, pane, ws string) *Worker {
	w := &Worker{Name: name, Kind: "claude", Thread: mainThread, PaneID: pane, WorkspaceID: ws, Status: "idle", Session: "sess-" + name, CreatedAt: time.Now().Add(-3 * time.Hour)}
	sp.st.Workers[name] = w
	return w
}

func TestReapIdleStopsLongIdleWorkers(t *testing.T) {
	sp, calls := newReapSuper(t)
	old := addReapWorker(sp, "old", "w1:p1", "w1")
	busy := addReapWorker(sp, "busy", "w2:p1", "w2")
	queued := addReapWorker(sp, "queued", "w3:p1", "w3")
	sub := addReapWorker(sp, "sub", "w4:p1", "w4")
	fresh := addReapWorker(sp, "fresh", "w5:p1", "w5")
	handed := addReapWorker(sp, "handed", "w6:p1", "w6")
	sp.st.Triggers = append(sp.st.Triggers, &Trigger{ID: "t1", Status: "queued", Targets: []string{"queued"}})
	th := sp.threadLocked("pr-1")
	th.Subscribers = append(th.Subscribers, "sub")

	live := map[string]liveAgent{
		"old":    {Pane: "w1:p1", Status: "idle"},
		"busy":   {Pane: "w2:p1", Status: "working"},
		"queued": {Pane: "w3:p1", Status: "done"},
		"sub":    {Pane: "w4:p1", Status: "idle"},
		"fresh":  {Pane: "w5:p1", Status: "idle"},
		"handed": {Pane: "w6:p1", Status: "idle"},
	}
	start := time.Now()
	sp.idleSince = map[string]time.Time{
		"old": start.Add(-61 * time.Minute), "busy": start.Add(-2 * time.Hour),
		"queued": start.Add(-2 * time.Hour), "sub": start.Add(-2 * time.Hour),
		"fresh": start.Add(-59 * time.Minute), "handed": start.Add(-2 * time.Hour),
	}
	delivered := map[string][]*Trigger{"handed": {{ID: "t2"}}}

	sp.reapIdle(context.Background(), live, delivered, start)

	if !old.Ended || old.Status != "gone" {
		t.Fatalf("old should be stopped: %+v", old)
	}
	for _, w := range []*Worker{busy, queued, sub, fresh, handed} {
		if w.Ended {
			t.Fatalf("%s must not be stopped", w.Name)
		}
	}
	if _, ok := sp.idleSince["busy"]; ok {
		t.Fatal("a working worker's idle clock must reset")
	}
	if _, ok := sp.idleSince["handed"]; ok {
		t.Fatal("a worker handed work this tick must have its idle clock reset")
	}
	got := strings.Join(calls(), "\n")
	if !strings.Contains(got, `workspace.close {"workspace_id":"w1"}`) {
		t.Fatalf("old's workspace not closed; calls:\n%s", got)
	}
	for _, ws := range []string{"w2", "w3", "w4", "w5", "w6"} {
		if strings.Contains(got, `"workspace_id":"`+ws+`"`) {
			t.Fatalf("workspace %s closed by mistake; calls:\n%s", ws, got)
		}
	}
	if old.Session != "sess-old" {
		t.Fatalf("session must be kept for --resume, got %q", old.Session)
	}
	last := sp.st.Entries[len(sp.st.Entries)-1]
	if !strings.Contains(last.Text, "old 空闲超过 60 分钟") {
		t.Fatalf("entry = %q", last.Text)
	}
}

func TestReapIdleStartsClockAndHonoursSetting(t *testing.T) {
	sp, calls := newReapSuper(t)
	w := addReapWorker(sp, "a", "w1:p1", "w1")
	live := map[string]liveAgent{"a": {Pane: "w1:p1", Status: "idle"}}
	now := time.Now()

	// First sighting only starts the clock, however long ago it went idle.
	sp.reapIdle(context.Background(), live, nil, now)
	if w.Ended || !sp.idleSince["a"].Equal(now) {
		t.Fatalf("first sighting: ended=%v since=%v", w.Ended, sp.idleSince["a"])
	}

	// Disabled: never stopped.
	sp.st.Settings.IdleStopMinutes = -1
	sp.reapIdle(context.Background(), live, nil, now.Add(10*time.Hour))
	if w.Ended {
		t.Fatal("idle stop disabled, worker stopped anyway")
	}

	// Custom limit.
	sp.st.Settings.IdleStopMinutes = 5
	sp.reapIdle(context.Background(), live, nil, now.Add(4*time.Minute))
	if w.Ended {
		t.Fatal("stopped before the 5 min limit")
	}
	sp.reapIdle(context.Background(), live, nil, now.Add(5*time.Minute))
	if !w.Ended {
		t.Fatalf("not stopped after 5 min; calls: %v", calls())
	}
	if _, ok := sp.idleSince["a"]; ok {
		t.Fatal("idle clock of a stopped worker should be dropped")
	}
}

func TestReapIdleSkipsWorkersWithoutWorkspace(t *testing.T) {
	sp, calls := newReapSuper(t)
	w := addReapWorker(sp, "a", "w1:p1", "")
	live := map[string]liveAgent{"a": {Pane: "w1:p1", Status: "idle"}}
	now := time.Now()
	sp.idleSince = map[string]time.Time{"a": now.Add(-3 * time.Hour)}
	sp.reapIdle(context.Background(), live, nil, now)
	if w.Ended || len(calls()) != 0 {
		t.Fatalf("worker without a workspace must be left alone: ended=%v calls=%v", w.Ended, calls())
	}
}

func TestIdleStopSetting(t *testing.T) {
	for _, c := range []struct {
		in   int
		want time.Duration
	}{{0, time.Hour}, {-1, 0}, {-7, 0}, {90, 90 * time.Minute}} {
		if got := (Settings{IdleStopMinutes: c.in}).idleStop(); got != c.want {
			t.Errorf("idleStop(%d) = %v, want %v", c.in, got, c.want)
		}
	}
}
