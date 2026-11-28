package collie

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"testing/fstest"
	"time"
)

type gcMockPane struct {
	name   string
	paneID string
	wsID   string
	kind   string
	status string
	cwd    string
	sess   string
	screen string
}

type gcMockNode struct {
	mu           sync.Mutex
	panes        []gcMockPane
	closedWS     []string
	prompted     []string
	startedArgs  [][]string
	startedCwds  []string
	readErr      error
	closeErr     error
	onClose      func(wsID string) (dropPane bool, err error)
	nextWS, next int
}

func (n *gcMockNode) conn() *herdrConn {
	return &herdrConn{
		dial: func(ctx context.Context) (net.Conn, error) {
			c1, c2 := net.Pipe()
			go func() {
				defer c2.Close()
				line, err := bufio.NewReader(c2).ReadBytes('\n')
				if err != nil {
					return
				}
				var req struct {
					ID     string         `json:"id"`
					Method string         `json:"method"`
					Params map[string]any `json:"params"`
				}
				_ = json.Unmarshal(line, &req)
				res, herr := n.handle(req.Method, req.Params)
				env := map[string]any{"id": req.ID}
				if herr != nil {
					if he, ok := herr.(*HerdrError); ok {
						env["error"] = he
					} else {
						// Transport failure: close pipe without response.
						return
					}
				} else {
					env["result"] = res
				}
				b, _ := json.Marshal(env)
				_, _ = c2.Write(append(b, '\n'))
			}()
			return c1, nil
		},
	}
}

func (n *gcMockNode) handle(method string, params map[string]any) (any, error) {
	n.mu.Lock()
	defer n.mu.Unlock()
	switch method {
	case "agent.list":
		as := make([]map[string]any, 0, len(n.panes))
		for _, p := range n.panes {
			item := map[string]any{
				"name":              p.name,
				"pane_id":           p.paneID,
				"workspace_id":      p.wsID,
				"agent":             p.kind,
				"agent_status":      p.status,
				"cwd":               p.cwd,
				"interactive_ready": true,
			}
			if p.kind == "" {
				item["agent"] = nil
			}
			if p.sess != "" {
				item["agent_session"] = map[string]any{"kind": "id", "value": p.sess}
			}
			as = append(as, item)
		}
		return map[string]any{"agents": as}, nil
	case "pane.list":
		ps := make([]map[string]any, 0, len(n.panes))
		for _, p := range n.panes {
			item := map[string]any{
				"pane_id":      p.paneID,
				"workspace_id": p.wsID,
				"agent_status": p.status,
				"cwd":          p.cwd,
			}
			if p.kind != "" {
				item["agent"] = p.kind
			}
			ps = append(ps, item)
		}
		return map[string]any{"panes": ps}, nil
	case "pane.read":
		if n.readErr != nil {
			return nil, n.readErr
		}
		pid, _ := params["pane_id"].(string)
		for _, p := range n.panes {
			if p.paneID == pid {
				return map[string]any{"read": map[string]any{"text": p.screen, "revision": 1}}, nil
			}
		}
		return nil, &HerdrError{Code: "pane_not_found", Message: "pane not found"}
	case "workspace.close":
		wsID, _ := params["workspace_id"].(string)
		n.closedWS = append(n.closedWS, wsID)
		drop := true
		err := n.closeErr
		if n.onClose != nil {
			cb := n.onClose
			n.mu.Unlock()
			drop, err = cb(wsID)
			n.mu.Lock()
		}
		if drop && (err == nil || isTransportSimErr(err)) {
			kept := n.panes[:0]
			for _, p := range n.panes {
				if p.wsID != wsID {
					kept = append(kept, p)
				}
			}
			n.panes = kept
		}
		if err != nil {
			return nil, err
		}
		return map[string]any{"ok": true}, nil
	case "agent.prompt":
		target, _ := params["target"].(string)
		n.prompted = append(n.prompted, target)
		return map[string]any{"ok": true}, nil
	case "workspace.create":
		n.nextWS++
		n.next++
		wsID := fmt.Sprintf("ws-new-%d", n.nextWS)
		paneID := fmt.Sprintf("%s:p%d", wsID, n.next)
		cwd, _ := params["cwd"].(string)
		n.startedCwds = append(n.startedCwds, cwd)
		n.panes = append(n.panes, gcMockPane{paneID: paneID, wsID: wsID, cwd: cwd, status: "idle"})
		return map[string]any{
			"workspace": map[string]any{"workspace_id": wsID},
			"root_pane": map[string]any{"pane_id": paneID},
		}, nil
	case "agent.start":
		pid, _ := params["pane_id"].(string)
		name, _ := params["name"].(string)
		kind, _ := params["kind"].(string)
		var args []string
		if raw, ok := params["args"].([]any); ok {
			for _, v := range raw {
				if s, ok := v.(string); ok {
					args = append(args, s)
				}
			}
		}
		n.startedArgs = append(n.startedArgs, args)
		for i := range n.panes {
			if n.panes[i].paneID == pid {
				n.panes[i].name = name
				n.panes[i].kind = kind
				n.panes[i].status = "idle"
			}
		}
		return map[string]any{"ok": true}, nil
	default:
		return map[string]any{"ok": true}, nil
	}
}

func isTransportSimErr(err error) bool {
	if err == nil {
		return false
	}
	var he *HerdrError
	return !errorsAsHerdr(err, &he)
}

func errorsAsHerdr(err error, target **HerdrError) bool {
	if he, ok := err.(*HerdrError); ok {
		*target = he
		return true
	}
	return false
}

func resetHistoryForTest(t *testing.T) {
	t.Helper()
	history.Lock()
	prevList, prevLoaded := history.list, history.loaded
	history.list, history.loaded = nil, false
	history.Unlock()
	t.Cleanup(func() {
		history.Lock()
		history.list, history.loaded = prevList, prevLoaded
		history.Unlock()
	})
}

func newWorkerGCTestEnv(t *testing.T, localNode *gcMockNode, remotes map[string]*gcMockNode) (*Server, *Super) {
	t.Helper()
	dir := t.TempDir()
	db, err := openDB(filepath.Join(dir, "herd.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	st := defaultState(dir)
	h := &Herdr{
		local:      &Machine{ID: "", Name: "hub", Home: dir, Local: true, Online: true, Agents: []string{"claude", "codex", "agy"}, conn: localNode.conn()},
		remote:     map[string]*Machine{},
		names:      map[string]string{},
		named:      map[string]string{},
		runs:       map[string][2]string{},
		asks:       map[string]askCheck{},
		lastListOK: map[string]bool{"": true},
	}
	for id, rn := range remotes {
		h.remote[id] = &Machine{ID: id, Name: id, Home: dir, Online: true, Agents: []string{"claude", "codex", "agy"}, conn: rn.conn()}
		h.lastListOK[id] = true
	}
	srv := &Server{
		herdr: h,
		token: "test-tok",
		home:  dir,
		web:   fstest.MapFS{},
		auths: newAuth(db, "test-tok"),
	}
	sp := &Super{
		s:           srv,
		db:          db,
		dir:         dir,
		bin:         "collie",
		st:          st,
		subs:        map[chan struct{}]bool{},
		lastScreen:  map[string]screenMark{},
		transcripts: map[string]*transcript{},
		noAgent:     map[string]time.Time{},
		sayDedup:    map[string]sayReceipt{},
		gc: workerGCState{
			startedAt:        time.Now().Add(-10 * time.Minute),
			gracePeriod:      -1,
			activityCooldown: -1,
			interval:         -1,
		},
	}
	srv.sup = sp
	return srv, sp
}

// TestRemoteSameNamePaneIsolation verifies that leftover panes named "collie"
// or "collie-dream" on remote nodes never overwrite Hub routing or live maps.
func TestRemoteSameNamePaneIsolation(t *testing.T) {
	t.Parallel()
	local := &gcMockNode{panes: []gcMockPane{
		{name: routerName, paneID: "w1:p1", wsID: "w1", kind: "claude", status: "idle", sess: "hub-router-sess"},
		{name: dreamName, paneID: "w2:p1", wsID: "w2", kind: "claude", status: "working", sess: "hub-dream-sess"},
	}}
	mac := &gcMockNode{panes: []gcMockPane{
		{name: routerName, paneID: "w9:p1", wsID: "w9", kind: "claude", status: "blocked", sess: "remote-router-sess"},
		{name: dreamName, paneID: "w9:p2", wsID: "w9", kind: "claude", status: "idle", sess: "remote-dream-sess"},
		{name: "remote-wk", paneID: "w9:p3", wsID: "w9", kind: "codex", status: "idle", sess: "remote-wk-sess"},
	}}
	srv, sp := newWorkerGCTestEnv(t, local, map[string]*gcMockNode{"mac": mac})
	ctx := context.Background()

	live, answered, ok := sp.liveAgentsWithStatus(ctx)
	if !ok || !answered[""] || !answered["mac"] {
		t.Fatalf("liveAgentsWithStatus ok=%v answered=%v", ok, answered)
	}
	if got := live[routerName]; got.Pane != "w1:p1" || got.Status != "idle" {
		t.Fatalf("router hijacked: got %+v, want Hub w1:p1 idle", got)
	}
	if got := live[dreamName]; got.Pane != "w2:p1" || got.Status != "working" {
		t.Fatalf("dream hijacked: got %+v, want Hub w2:p1 working", got)
	}
	if got := live["remote-wk"]; got.Pane != "w9:p3@mac" {
		t.Fatalf("remote worker broken: got %+v, want w9:p3@mac", got)
	}

	// Verify Fleet name index & Prompt routing stay pinned to Hub's local pane.
	srv.herdr.Remember("w9:p1@mac", routerName)
	srv.herdr.Remember("w9:p2@mac", dreamName)
	if p := srv.herdr.paneOfName(ctx, routerName); p != "w1:p1" {
		t.Fatalf("paneOfName(router) = %q, want w1:p1", p)
	}
	if p := srv.herdr.paneOfName(ctx, dreamName); p != "w2:p1" {
		t.Fatalf("paneOfName(dream) = %q, want w2:p1", p)
	}
	if err := srv.herdr.Prompt(ctx, routerName, "ping hub"); err != nil {
		t.Fatalf("Prompt(router): %v", err)
	}
	local.mu.Lock()
	localPrompts := append([]string(nil), local.prompted...)
	local.mu.Unlock()
	mac.mu.Lock()
	macPrompts := append([]string(nil), mac.prompted...)
	mac.mu.Unlock()
	if !slices.Equal(localPrompts, []string{"w1:p1"}) || len(macPrompts) != 0 {
		t.Fatalf("prompt routed wrong: local=%v mac=%v", localPrompts, macPrompts)
	}

	// Even when Hub has no local router/dream running, remote same-name panes must stay invisible.
	local.mu.Lock()
	local.panes = nil
	local.mu.Unlock()
	live2, _, ok := sp.liveAgentsWithStatus(ctx)
	if !ok {
		t.Fatal("liveAgentsWithStatus failed")
	}
	if _, exists := live2[routerName]; exists {
		t.Fatalf("remote collie leaked when hub router absent: %+v", live2[routerName])
	}
	if _, exists := live2[dreamName]; exists {
		t.Fatalf("remote dream leaked when hub dream absent: %+v", live2[dreamName])
	}
	if p := srv.herdr.paneOfName(ctx, routerName); p != "" {
		t.Fatalf("paneOfName(router) = %q when hub router absent, want empty", p)
	}

	// Spawn must reject both routerName and dreamName.
	for _, reserved := range []string{routerName, dreamName} {
		if _, _, err := sp.Spawn("claude", srv.home, "hi", mainThread, reserved, "", routerName, "", "", "", false, ""); err == nil {
			t.Fatalf("Spawn allowed reserved system name %q", reserved)
		}
	}
}

func TestWorkerGCIdleTimeoutScopeAndMaxPerCycle(t *testing.T) {
	resetHistoryForTest(t)
	now := time.Now().UTC().Truncate(time.Second)
	mkTime := func(d time.Duration) *time.Time { ts := now.Add(-d); return &ts }

	workDir := t.TempDir()
	transcriptFile := filepath.Join(workDir, "keep-me-transcript.jsonl")
	if err := os.WriteFile(transcriptFile, []byte("{\"ok\":true}\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	local := &gcMockNode{panes: []gcMockPane{
		{name: "wk-old-1", paneID: "w1:p1", wsID: "w1", kind: "claude", status: "idle", cwd: workDir, sess: "s-old-1", screen: "> "},
		{name: "wk-old-2", paneID: "w2:p1", wsID: "w2", kind: "codex", status: "done", cwd: workDir, sess: "s-old-2", screen: "> "},
		{name: "wk-old-3", paneID: "w3:p1", wsID: "w3", kind: "agy", status: "idle", cwd: workDir, sess: "s-old-3", screen: "> "},
		{name: "wk-fresh", paneID: "w4:p1", wsID: "w4", kind: "claude", status: "idle", cwd: workDir, sess: "s-fresh", screen: "> "},
		{name: "wk-nosess", paneID: "w5:p1", wsID: "w5", kind: "claude", status: "idle", cwd: workDir, sess: "", screen: "> "},
		{name: "wk-manual", paneID: "w6:p1", wsID: "w6", kind: "claude", status: "idle", cwd: workDir, sess: "s-manual", screen: "> "},
		{name: "wk-adopted", paneID: "w7:p1", wsID: "w7", kind: "claude", status: "idle", cwd: workDir, sess: "s-adopted", screen: "> "},
		{name: "wk-keep", paneID: "w8:p1", wsID: "w8", kind: "claude", status: "idle", cwd: workDir, sess: "s-keep", screen: "> "},
	}}
	srv, sp := newWorkerGCTestEnv(t, local, nil)
	sp.st.Settings.WorkerMaxLivePerMachine = -1 // isolate idle timeout rule

	sp.st.Workers = map[string]*Worker{
		"wk-old-1":   {Name: "wk-old-1", Label: "老任务1", Kind: "claude", Model: "claude-opus-4-6", Effort: "high", Cwd: workDir, Thread: "github:org/r#1", PaneID: "w1:p1", WorkspaceID: "w1", CreatedBy: routerName, CreatedAt: now.Add(-180 * time.Minute), Status: "idle", IdleSince: mkTime(150 * time.Minute), Session: "s-old-1"},
		"wk-old-2":   {Name: "wk-old-2", Label: "老任务2", Kind: "codex", Cwd: workDir, Thread: mainThread, PaneID: "w2:p1", WorkspaceID: "w2", CreatedBy: "router", CreatedAt: now.Add(-170 * time.Minute), Status: "done", IdleSince: mkTime(140 * time.Minute), Session: "s-old-2"},
		"wk-old-3":   {Name: "wk-old-3", Label: "老任务3", Kind: "antigravity", Cwd: workDir, Thread: mainThread, PaneID: "w3:p1", WorkspaceID: "w3", CreatedBy: routerName, CreatedAt: now.Add(-160 * time.Minute), Status: "idle", IdleSince: mkTime(130 * time.Minute), Session: "s-old-3"},
		"wk-fresh":   {Name: "wk-fresh", Kind: "claude", Cwd: workDir, Thread: mainThread, PaneID: "w4:p1", WorkspaceID: "w4", CreatedBy: routerName, CreatedAt: now.Add(-80 * time.Minute), Status: "idle", IdleSince: mkTime(60 * time.Minute), Session: "s-fresh"},
		"wk-nosess":  {Name: "wk-nosess", Kind: "claude", Cwd: workDir, Thread: mainThread, PaneID: "w5:p1", WorkspaceID: "w5", CreatedBy: routerName, CreatedAt: now.Add(-180 * time.Minute), Status: "idle", IdleSince: mkTime(150 * time.Minute)},
		"wk-manual":  {Name: "wk-manual", Kind: "claude", Cwd: workDir, Thread: mainThread, PaneID: "w6:p1", WorkspaceID: "w6", CreatedBy: "you", CreatedAt: now.Add(-200 * time.Minute), Status: "idle", IdleSince: mkTime(180 * time.Minute), Session: "s-manual"},
		"wk-adopted": {Name: "wk-adopted", Kind: "claude", Cwd: workDir, Thread: mainThread, PaneID: "w7:p1", WorkspaceID: "w7", CreatedBy: "adopted", CreatedAt: now.Add(-200 * time.Minute), Status: "idle", IdleSince: mkTime(180 * time.Minute), Session: "s-adopted"},
		"wk-keep":    {Name: "wk-keep", Kind: "claude", Cwd: workDir, Thread: mainThread, PaneID: "w8:p1", WorkspaceID: "w8", CreatedBy: routerName, Keep: true, CreatedAt: now.Add(-200 * time.Minute), Status: "idle", IdleSince: mkTime(180 * time.Minute), Session: "s-keep"},
	}
	sp.st.Threads["github:org/r#1"] = &Thread{ID: "github:org/r#1", Subscribers: []string{"wk-old-1"}}

	ctx := context.Background()
	live, answered, ok := sp.liveAgentsWithStatus(ctx)
	if !ok {
		t.Fatal("liveAgentsWithStatus failed")
	}
	// Cycle 1: max 2 per cycle -> wk-old-1 and wk-old-2 hibernated.
	if got := sp.runWorkerGCSweep(ctx, live, answered, now); got != 2 {
		t.Fatalf("cycle 1 hibernated %d, want 2", got)
	}
	if !sp.st.Workers["wk-old-1"].Ended || sp.st.Workers["wk-old-1"].Status != "done" || sp.st.Workers["wk-old-1"].Hibernating {
		t.Fatalf("wk-old-1 bad state: %+v", sp.st.Workers["wk-old-1"])
	}
	if !sp.st.Workers["wk-old-2"].Ended || sp.st.Workers["wk-old-3"].Ended {
		t.Fatalf("cycle 1 cap violated: old2.Ended=%v old3.Ended=%v", sp.st.Workers["wk-old-2"].Ended, sp.st.Workers["wk-old-3"].Ended)
	}
	if len(sp.st.Threads["github:org/r#1"].Subscribers) != 0 {
		t.Fatalf("subscribers not cleaned: %v", sp.st.Threads["github:org/r#1"].Subscribers)
	}

	// Cycle 2: wk-old-3 hibernated, everyone else strictly untouched.
	live, answered, _ = sp.liveAgentsWithStatus(ctx)
	if got := sp.runWorkerGCSweep(ctx, live, answered, now.Add(3*time.Minute)); got != 1 {
		t.Fatalf("cycle 2 hibernated %d, want 1", got)
	}
	for _, keep := range []string{"wk-fresh", "wk-nosess", "wk-manual", "wk-adopted", "wk-keep"} {
		if sp.st.Workers[keep].Ended {
			t.Fatalf("worker %s must NOT be hibernated", keep)
		}
	}
	// Worktree directory & transcript file must remain completely untouched.
	if _, err := os.Stat(transcriptFile); err != nil {
		t.Fatalf("transcript/worktree touched: %v", err)
	}
	// Rich Ended archive preserved for wk-old-1.
	e := srv.findEndedByName("wk-old-1")
	if e == nil || e.Cwd != workDir || e.Kind != "claude" || e.Model != "claude-opus-4-6" ||
		e.Effort != "high" || e.Label != "老任务1" || e.Thread != "github:org/r#1" ||
		e.Session != "s-old-1" || e.Reason != "hibernated" || !slices.Equal(e.Subscribers, []string{"github:org/r#1"}) {
		t.Fatalf("rich Ended metadata mismatch: %+v", e)
	}
}

func TestWorkerGCStartupEmptyShellAndSoftCapLRU(t *testing.T) {
	resetHistoryForTest(t)
	now := time.Now().UTC().Truncate(time.Second)
	mkTime := func(d time.Duration) *time.Time { ts := now.Add(-d); return &ts }

	// Part A: Startup empty-shell failure >= 30m (even without Session).
	local := &gcMockNode{panes: []gcMockPane{
		{name: "fault-old", paneID: "wf1:p1", wsID: "wf1", kind: "", status: "idle", screen: "$ "},
		{name: "fault-new", paneID: "wf2:p1", wsID: "wf2", kind: "", status: "idle", screen: "$ "},
	}}
	_, sp := newWorkerGCTestEnv(t, local, nil)
	sp.st.Workers["fault-old"] = &Worker{Name: "fault-old", Kind: "claude", PaneID: "wf1:p1", WorkspaceID: "wf1", CreatedBy: routerName, CreatedAt: now.Add(-40 * time.Minute), Status: "failed", Ended: true, StartupFailed: true, IdleSince: mkTime(35 * time.Minute)}
	sp.st.Workers["fault-new"] = &Worker{Name: "fault-new", Kind: "claude", PaneID: "wf2:p1", WorkspaceID: "wf2", CreatedBy: routerName, CreatedAt: now.Add(-20 * time.Minute), Status: "failed", Ended: true, StartupFailed: true, IdleSince: mkTime(15 * time.Minute)}

	ctx := context.Background()
	live, answered, _ := sp.liveAgentsWithStatus(ctx)
	if got := sp.runWorkerGCSweep(ctx, live, answered, now); got != 1 {
		t.Fatalf("fault sweep = %d, want 1", got)
	}
	if sp.st.Workers["fault-old"].WorkspaceID != "" || sp.st.Workers["fault-new"].WorkspaceID == "" {
		t.Fatalf("fault cleanup mismatch: old=%+v new=%+v", sp.st.Workers["fault-old"], sp.st.Workers["fault-new"])
	}

	// Part B: Per-machine soft cap (6) LRU hibernation for workers idle >= 15m.
	local2 := &gcMockNode{}
	mac := &gcMockNode{panes: []gcMockPane{
		{name: "mac-w1", paneID: "m1:p1", wsID: "m1", kind: "claude", status: "idle", sess: "ms1", screen: "> "},
	}}
	_, sp2 := newWorkerGCTestEnv(t, local2, map[string]*gcMockNode{"mac": mac})
	addLocal := func(name, status string, idleMin int, sess string) {
		ws := "ws-" + name
		pid := ws + ":p1"
		local2.panes = append(local2.panes, gcMockPane{name: name, paneID: pid, wsID: ws, kind: "claude", status: status, sess: sess, screen: "> "})
		w := &Worker{Name: name, Kind: "claude", PaneID: pid, WorkspaceID: ws, CreatedBy: routerName, CreatedAt: now.Add(-90 * time.Minute), Status: status, Session: sess}
		if idleMin > 0 {
			w.IdleSince = mkTime(time.Duration(idleMin) * time.Minute)
		}
		sp2.st.Workers[name] = w
	}
	// 8 live managed workers on Hub: 4 working, 1 idle 10m (<15m), 3 idle >= 15m -> excess = 2 -> hibernate oldest 2 (lru-1, lru-2).
	addLocal("work-1", "working", 0, "s1")
	addLocal("work-2", "working", 0, "s2")
	addLocal("work-3", "working", 0, "s3")
	addLocal("work-4", "working", 0, "s4")
	addLocal("idle-short", "idle", 10, "s5")
	addLocal("lru-1", "idle", 50, "s6")
	addLocal("lru-2", "idle", 35, "s7")
	addLocal("lru-3", "idle", 20, "s8")
	// 1 idle worker on remote mac (well under cap=6, idle 40m < 120m): must stay alive.
	sp2.st.Workers["mac-w1"] = &Worker{Name: "mac-w1", Kind: "claude", Machine: "mac", PaneID: "m1:p1@mac", WorkspaceID: "m1@mac", CreatedBy: routerName, CreatedAt: now.Add(-60 * time.Minute), Status: "idle", IdleSince: mkTime(40 * time.Minute), Session: "ms1"}

	live2, answered2, _ := sp2.liveAgentsWithStatus(ctx)
	if got := sp2.runWorkerGCSweep(ctx, live2, answered2, now); got != 2 {
		t.Fatalf("LRU sweep = %d, want 2", got)
	}
	if !sp2.st.Workers["lru-1"].Ended || !sp2.st.Workers["lru-2"].Ended {
		t.Fatalf("expected lru-1 & lru-2 hibernated, got lru1=%v lru2=%v", sp2.st.Workers["lru-1"].Ended, sp2.st.Workers["lru-2"].Ended)
	}
	for _, alive := range []string{"lru-3", "idle-short", "work-1", "mac-w1"} {
		if sp2.st.Workers[alive].Ended {
			t.Fatalf("worker %s should remain live", alive)
		}
	}
	// Setting WorkerIdleGCMinutes=-1 disables GC completely.
	sp2.st.Settings.WorkerIdleGCMinutes = -1
	sp2.st.Workers["lru-3"].IdleSince = mkTime(200 * time.Minute)
	live2, answered2, _ = sp2.liveAgentsWithStatus(ctx)
	if got := sp2.runWorkerGCSweep(ctx, live2, answered2, now); got != 0 {
		t.Fatalf("GC disabled (-1) still closed %d workers", got)
	}
}

func TestWorkerGCSafetyVetoes(t *testing.T) {
	t.Parallel()
	now := time.Now().UTC().Truncate(time.Second)
	oldIdle := now.Add(-150 * time.Minute)

	// 1) Screen background task / running / timer / dialog vetoes.
	for _, tc := range []struct {
		name   string
		screen string
	}{
		{"bg_task", "done\n1 background task\n> "},
		{"task_s", "done\n2 task(s)\n> "},
		{"shell_running", "done\n1 shell running\n> "},
		{"timer_running", "status: 1 timer running\n> "},
		{"esc_interrupt", "Working (12s • esc to interrupt)\n"},
		{"dialog", "Allow bash?\n❯ 1. Yes\n  2. No\nEnter to confirm"},
	} {
		t.Run("screen_"+tc.name, func(t *testing.T) {
			local := &gcMockNode{panes: []gcMockPane{
				{name: "wk", paneID: "w1:p1", wsID: "w1", kind: "claude", status: "idle", sess: "s1", screen: tc.screen},
			}}
			_, sp := newWorkerGCTestEnv(t, local, nil)
			sp.st.Workers["wk"] = &Worker{Name: "wk", Kind: "claude", PaneID: "w1:p1", WorkspaceID: "w1", CreatedBy: routerName, CreatedAt: now.Add(-180 * time.Minute), Status: "idle", IdleSince: &oldIdle, Session: "s1"}
			live, answered, _ := sp.liveAgentsWithStatus(context.Background())
			if got := sp.runWorkerGCSweep(context.Background(), live, answered, now); got != 0 || sp.st.Workers["wk"].Ended {
				t.Fatalf("screen %q should veto hibernation", tc.name)
			}
		})
	}

	// 2) Queued trigger veto (direct target & subscribed thread).
	t.Run("queued_trigger", func(t *testing.T) {
		local := &gcMockNode{panes: []gcMockPane{
			{name: "wk-direct", paneID: "w1:p1", wsID: "w1", kind: "claude", status: "idle", sess: "s1", screen: "> "},
			{name: "wk-sub", paneID: "w2:p1", wsID: "w2", kind: "claude", status: "idle", sess: "s2", screen: "> "},
		}}
		_, sp := newWorkerGCTestEnv(t, local, nil)
		sp.st.Workers["wk-direct"] = &Worker{Name: "wk-direct", Kind: "claude", PaneID: "w1:p1", WorkspaceID: "w1", CreatedBy: routerName, CreatedAt: now.Add(-180 * time.Minute), Status: "idle", IdleSince: &oldIdle, Session: "s1"}
		sp.st.Workers["wk-sub"] = &Worker{Name: "wk-sub", Kind: "claude", Thread: "gh:1", PaneID: "w2:p1", WorkspaceID: "w2", CreatedBy: routerName, CreatedAt: now.Add(-180 * time.Minute), Status: "idle", IdleSince: &oldIdle, Session: "s2"}
		sp.st.Threads["gh:1"] = &Thread{ID: "gh:1", Subscribers: []string{"wk-sub"}}
		sp.st.Triggers = []*Trigger{
			{ID: "t1", Source: "router", Status: "queued", Targets: []string{"wk-direct"}},
			{ID: "t2", Source: "webhook:github", Thread: "gh:1", Status: "queued"},
		}
		live, answered, _ := sp.liveAgentsWithStatus(context.Background())
		if got := sp.runWorkerGCSweep(context.Background(), live, answered, now); got != 0 {
			t.Fatalf("queued trigger should veto, hibernated=%d", got)
		}
	})

	// 3) Split-pane (non-exclusive) workspace veto.
	t.Run("split_pane_workspace", func(t *testing.T) {
		local := &gcMockNode{panes: []gcMockPane{
			{name: "wk-split", paneID: "w1:p1", wsID: "w1", kind: "claude", status: "idle", sess: "s1", screen: "> "},
			{name: "user-shell", paneID: "w1:p2", wsID: "w1", kind: "", status: "idle", screen: "$ "},
		}}
		_, sp := newWorkerGCTestEnv(t, local, nil)
		sp.st.Workers["wk-split"] = &Worker{Name: "wk-split", Kind: "claude", PaneID: "w1:p1", WorkspaceID: "w1", CreatedBy: routerName, CreatedAt: now.Add(-180 * time.Minute), Status: "idle", IdleSince: &oldIdle, Session: "s1"}
		live, answered, _ := sp.liveAgentsWithStatus(context.Background())
		if got := sp.runWorkerGCSweep(context.Background(), live, answered, now); got != 0 || len(local.closedWS) != 0 {
			t.Fatalf("split-pane workspace must not be closed: got=%d closed=%v", got, local.closedWS)
		}
	})

	// 4) Screen read failure veto.
	t.Run("screen_read_error", func(t *testing.T) {
		local := &gcMockNode{
			panes:   []gcMockPane{{name: "wk-err", paneID: "w1:p1", wsID: "w1", kind: "claude", status: "idle", sess: "s1"}},
			readErr: &HerdrError{Code: "timeout", Message: "read timeout"},
		}
		_, sp := newWorkerGCTestEnv(t, local, nil)
		sp.st.Workers["wk-err"] = &Worker{Name: "wk-err", Kind: "claude", PaneID: "w1:p1", WorkspaceID: "w1", CreatedBy: routerName, CreatedAt: now.Add(-180 * time.Minute), Status: "idle", IdleSince: &oldIdle, Session: "s1"}
		live, answered, _ := sp.liveAgentsWithStatus(context.Background())
		if got := sp.runWorkerGCSweep(context.Background(), live, answered, now); got != 0 {
			t.Fatalf("screen read error must veto, got=%d", got)
		}
	})
}

func TestWorkerGCTwoPhaseHibernatingAndCrashReconcile(t *testing.T) {
	resetHistoryForTest(t)
	now := time.Now().UTC().Truncate(time.Second)
	oldIdle := now.Add(-150 * time.Minute)

	// Subtest 1: Verify Hibernating=true is persisted to SQLite BEFORE workspace.close runs,
	// and an ambiguous transport failure reconciles quietly in tick() without a "gone"/exited alarm.
	local := &gcMockNode{panes: []gcMockPane{
		{name: "wk-2pc", paneID: "w1:p1", wsID: "w1", kind: "claude", status: "idle", sess: "s-2pc", screen: "> "},
	}}
	srv, sp := newWorkerGCTestEnv(t, local, nil)
	sp.st.Workers["wk-2pc"] = &Worker{
		Name: "wk-2pc", Label: "两阶段验证", Kind: "claude", Cwd: srv.home, Thread: mainThread,
		PaneID: "w1:p1", WorkspaceID: "w1", CreatedBy: routerName, CreatedAt: now.Add(-180 * time.Minute),
		Status: "idle", IdleSince: &oldIdle, Session: "s-2pc",
	}
	sawPersistedHibernating := false
	local.onClose = func(wsID string) (bool, error) {
		reloaded, err := loadState(sp.db, srv.home)
		if err == nil && reloaded.Workers["wk-2pc"] != nil && reloaded.Workers["wk-2pc"].Hibernating {
			sawPersistedHibernating = true
		}
		// Simulate pane actually closed, but RPC reply lost (transport EOF).
		return true, io.EOF
	}

	ctx := context.Background()
	live, answered, _ := sp.liveAgentsWithStatus(ctx)
	if got := sp.runWorkerGCSweep(ctx, live, answered, now); got != 0 {
		t.Fatalf("ambiguous transport error should defer finalization, got=%d", got)
	}
	if !sawPersistedHibernating {
		t.Fatal("Hibernating=true was NOT persisted to SQLite before workspace.close")
	}
	if !sp.st.Workers["wk-2pc"].Hibernating || sp.st.Workers["wk-2pc"].Ended {
		t.Fatalf("after transport error want Hibernating=true Ended=false, got %+v", sp.st.Workers["wk-2pc"])
	}

	// Next tick observes pane gone -> must reconcile quietly without "exited" trigger or "gone" status.
	sp.tick(ctx)
	w := sp.st.Workers["wk-2pc"]
	if !w.Ended || w.Hibernating || w.Status != "done" {
		t.Fatalf("reconciled worker state = %+v, want Ended=true Hibernating=false Status=done", w)
	}
	for _, tr := range sp.st.Triggers {
		if strings.Contains(tr.Text, "exited") {
			t.Fatalf("false unexpected-exit trigger emitted: %+v", tr)
		}
	}
	if e := srv.findEndedByName("wk-2pc"); e == nil || e.Session != "s-2pc" || e.Label != "两阶段验证" {
		t.Fatalf("reconciled worker not archived cleanly: %+v", e)
	}
	if !sp.isPaneGCProtected("w1:p1", time.Now()) {
		t.Fatal("recentlyClosed protection window should be active right after finalization")
	}
	if sp.isPaneGCProtected("w1:p1", time.Now().Add(defaultWorkerGCRecentCloseWindow+time.Second)) {
		t.Fatal("recentlyClosed protection window should expire after TTL")
	}

	// Subtest 2: Structured HerdrError rejection rolls back Hibernating=false cleanly.
	local2 := &gcMockNode{
		panes:    []gcMockPane{{name: "wk-rej", paneID: "w2:p1", wsID: "w2", kind: "claude", status: "idle", sess: "s-rej", screen: "> "}},
		closeErr: &HerdrError{Code: "busy", Message: "workspace busy"},
	}
	_, sp2 := newWorkerGCTestEnv(t, local2, nil)
	sp2.st.Workers["wk-rej"] = &Worker{Name: "wk-rej", Kind: "claude", PaneID: "w2:p1", WorkspaceID: "w2", CreatedBy: routerName, CreatedAt: now.Add(-180 * time.Minute), Status: "idle", IdleSince: &oldIdle, Session: "s-rej"}
	live2, answered2, _ := sp2.liveAgentsWithStatus(ctx)
	if got := sp2.runWorkerGCSweep(ctx, live2, answered2, now); got != 0 {
		t.Fatalf("rejected close should return 0, got=%d", got)
	}
	if sp2.st.Workers["wk-rej"].Hibernating || sp2.st.Workers["wk-rej"].Ended {
		t.Fatalf("structured rejection must revert Hibernating=false, got %+v", sp2.st.Workers["wk-rej"])
	}
}

func TestEndedArchiveCap200AndLosslessResume(t *testing.T) {
	resetHistoryForTest(t)
	now := time.Now().UTC().Truncate(time.Second)
	workDir := t.TempDir()

	local := &gcMockNode{panes: []gcMockPane{
		{name: "fix-auth", paneID: "w1:p1", wsID: "w1", kind: "claude", status: "idle", cwd: workDir, sess: "sess-auth-999", screen: "> "},
	}}
	srv, sp := newWorkerGCTestEnv(t, local, nil)

	// 1) Archive cap expanded from 40 to 200.
	for i := 1; i <= 225; i++ {
		srv.remember(&Ended{
			Kind: "claude", Cwd: workDir, Name: fmt.Sprintf("w-%03d", i),
			Session: fmt.Sprintf("sess-%03d", i), Ended: now.Add(time.Duration(i) * time.Second),
		})
	}
	history.Lock()
	count := len(history.list)
	history.Unlock()
	if count != 200 {
		t.Fatalf("archive cap = %d, want 200", count)
	}

	// 2) Dedupe preserves rich metadata if a sparse watchEndings event arrives later.
	srv.remember(&Ended{
		Kind: "claude", Machine: "mac", Cwd: workDir, Name: "meta-wk", Label: "保留标签",
		Model: "claude-opus-4-6", Effort: "high", Thread: "gh:9", CreatedBy: routerName,
		Subscribers: []string{"gh:9"}, Reason: "hibernated", Session: "sess-meta", Ended: now,
	})
	srv.remember(&Ended{
		Kind: "claude", Machine: "mac", Cwd: workDir, Name: "meta-wk",
		Session: "sess-meta", Ended: now.Add(time.Second),
	})
	merged := srv.findEndedByName("meta-wk")
	if merged == nil || merged.Label != "保留标签" || merged.Model != "claude-opus-4-6" ||
		merged.Effort != "high" || merged.Thread != "gh:9" || merged.Reason != "hibernated" {
		t.Fatalf("sparse remember clobbered rich metadata: %+v", merged)
	}

	// 3) End-to-end: hibernate -> spawn --resume restores dir, machine, kind, model, effort, label, thread & session.
	oldIdle := now.Add(-150 * time.Minute)
	sp.st.Workers["fix-auth"] = &Worker{
		Name: "fix-auth", Label: "修鉴权", Kind: "claude", Model: "claude-opus-4-6", Effort: "high",
		Cwd: workDir, Thread: "github:org/repo#12", PaneID: "w1:p1", WorkspaceID: "w1",
		CreatedBy: routerName, CreatedAt: now.Add(-180 * time.Minute),
		Status: "idle", IdleSince: &oldIdle, Session: "sess-auth-999",
	}
	sp.st.Threads["github:org/repo#12"] = &Thread{ID: "github:org/repo#12", Subscribers: []string{"fix-auth"}}
	ctx := context.Background()
	live, answered, _ := sp.liveAgentsWithStatus(ctx)
	if got := sp.runWorkerGCSweep(ctx, live, answered, now); got != 1 {
		t.Fatalf("hibernate fix-auth got %d, want 1", got)
	}

	// Also prove resumeFrom works even if st.Workers entry were evicted, via Ended archive fallback.
	delete(sp.st.Workers, "fix-auth")
	resumed, err := sp.resumeFrom("fix-auth")
	if err != nil {
		t.Fatalf("resumeFrom archive fallback failed: %v", err)
	}
	if resumed.Kind != "claude" || resumed.Cwd != workDir || resumed.Model != "claude-opus-4-6" ||
		resumed.Effort != "high" || resumed.Label != "修鉴权" || resumed.Thread != "github:org/repo#12" ||
		resumed.Session != "sess-auth-999" {
		t.Fatalf("resumeFrom metadata mismatch: %+v", resumed)
	}

	// Drive POST /api/super/spawn with {"resume": "fix-auth"} (same endpoint `collie spawn --resume` uses).
	handler := srv.Routes()
	body, _ := json.Marshal(map[string]any{"resume": "fix-auth", "task": "继续补完单测"})
	req := httptest.NewRequest("POST", "/api/super/spawn", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer test-tok")
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != 202 {
		t.Fatalf("spawn --resume status=%d body=%s", rec.Code, rec.Body.String())
	}
	if w := sp.st.Workers["fix-auth"]; w == nil || w.Ended || w.Session != "sess-auth-999" ||
		w.Cwd != workDir || w.Model != "claude-opus-4-6" || w.Effort != "high" || w.Label != "修鉴权" {
		t.Fatalf("resumed worker state mismatch: %+v", w)
	}
	if !slices.Contains(sp.st.Threads["github:org/repo#12"].Subscribers, "fix-auth") {
		t.Fatalf("thread subscription not restored: %v", sp.st.Threads["github:org/repo#12"].Subscribers)
	}
	local.mu.Lock()
	argsCopy := append([][]string(nil), local.startedArgs...)
	cwdsCopy := append([]string(nil), local.startedCwds...)
	local.mu.Unlock()
	if len(argsCopy) == 0 || !slices.Contains(argsCopy[0], "--resume") || !slices.Contains(argsCopy[0], "sess-auth-999") {
		t.Fatalf("agent.start missing --resume args: %v", argsCopy)
	}
	if len(cwdsCopy) == 0 || cwdsCopy[0] != workDir {
		t.Fatalf("agent.start wrong cwd: %v, want %s", cwdsCopy, workDir)
	}

	// 4) Cross-machine hibernate -> --resume preserves remote Machine and Cwd.
	mac := &gcMockNode{panes: []gcMockPane{
		{name: "mac-job", paneID: "m1:p1", wsID: "m1", kind: "codex", status: "idle", cwd: "/Users/dev/proj", sess: "sess-mac-77", screen: "> "},
	}}
	srvR, spR := newWorkerGCTestEnv(t, &gcMockNode{}, map[string]*gcMockNode{"mac": mac})
	spR.st.Workers["mac-job"] = &Worker{
		Name: "mac-job", Label: "Mac 联调", Kind: "codex", Machine: "mac", Cwd: "/Users/dev/proj",
		Model: "o3", Effort: "high", Thread: mainThread, PaneID: "m1:p1@mac", WorkspaceID: "m1@mac",
		CreatedBy: routerName, CreatedAt: now.Add(-180 * time.Minute),
		Status: "idle", IdleSince: &oldIdle, Session: "sess-mac-77",
	}
	liveR, answeredR, _ := spR.liveAgentsWithStatus(ctx)
	if got := spR.runWorkerGCSweep(ctx, liveR, answeredR, now); got != 1 {
		t.Fatalf("remote hibernate got %d, want 1", got)
	}
	handlerR := srvR.Routes()
	bodyR, _ := json.Marshal(map[string]any{"resume": "mac-job", "task": "继续 Mac 联调"})
	reqR := httptest.NewRequest("POST", "/api/super/spawn", bytes.NewReader(bodyR))
	reqR.Header.Set("Authorization", "Bearer test-tok")
	reqR.Header.Set("Content-Type", "application/json")
	recR := httptest.NewRecorder()
	handlerR.ServeHTTP(recR, reqR)
	if recR.Code != 202 {
		t.Fatalf("remote spawn --resume status=%d body=%s", recR.Code, recR.Body.String())
	}
	if w := spR.st.Workers["mac-job"]; w == nil || w.Machine != "mac" || w.Cwd != "/Users/dev/proj" || w.Session != "sess-mac-77" || w.Label != "Mac 联调" {
		t.Fatalf("remote resumed worker mismatch: %+v", w)
	}
	mac.mu.Lock()
	macArgs := append([][]string(nil), mac.startedArgs...)
	macCwds := append([]string(nil), mac.startedCwds...)
	mac.mu.Unlock()
	if len(macArgs) == 0 || !slices.Contains(macArgs[0], "resume") || !slices.Contains(macArgs[0], "sess-mac-77") || len(macCwds) == 0 || macCwds[0] != "/Users/dev/proj" {
		t.Fatalf("remote agent.start mismatch: args=%v cwds=%v", macArgs, macCwds)
	}
}

// TestWorkerGCAsyncTickSurvivesPerTickCancel ensures maybeRunWorkerGC uses the
// long-lived parent context rather than tick()'s short-lived deferred-cancel ctx.
func TestWorkerGCAsyncTickSurvivesPerTickCancel(t *testing.T) {
	resetHistoryForTest(t)
	now := time.Now()
	oldIdle := now.Add(-150 * time.Minute)
	doneCh := make(chan struct{}, 1)
	local := &gcMockNode{
		panes: []gcMockPane{
			{name: "wk-async", paneID: "w1:p1", wsID: "w1", kind: "claude", status: "idle", sess: "s-async", screen: "> "},
		},
		onClose: func(wsID string) (bool, error) {
			time.Sleep(30 * time.Millisecond)
			select {
			case doneCh <- struct{}{}:
			default:
			}
			return true, nil
		},
	}
	_, sp := newWorkerGCTestEnv(t, local, nil)
	sp.st.Workers["wk-async"] = &Worker{
		Name: "wk-async", Kind: "claude", PaneID: "w1:p1", WorkspaceID: "w1",
		CreatedBy: routerName, CreatedAt: now.Add(-180 * time.Minute),
		Status: "idle", IdleSince: &oldIdle, Session: "s-async",
	}

	parentCtx, cancel := context.WithCancel(context.Background())
	defer cancel()
	sp.tick(parentCtx)

	select {
	case <-doneCh:
	case <-time.After(3 * time.Second):
		t.Fatal("async GC goroutine did not close workspace after tick() returned")
	}
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		sp.mu.Lock()
		ended := sp.st.Workers["wk-async"].Ended && !sp.gc.running
		sp.mu.Unlock()
		if ended {
			return
		}
		time.Sleep(15 * time.Millisecond)
	}
	t.Fatal("async GC did not finalize worker in time")
}
