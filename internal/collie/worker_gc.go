package collie

import (
	"context"
	"errors"
	"fmt"
	"log"
	"maps"
	"regexp"
	"slices"
	"strings"
	"time"
)

const (
	defaultWorkerGCInterval          = 2 * time.Minute
	defaultWorkerGCGracePeriod       = 2 * time.Minute
	defaultWorkerGCActivityCooldown  = 2 * time.Minute
	defaultWorkerGCFaultIdle         = 30 * time.Minute
	defaultWorkerGCIdle              = 120 * time.Minute
	defaultWorkerGCCapIdle           = 15 * time.Minute
	defaultWorkerGCMaxLivePerMachine = 6
	defaultWorkerGCFailBackoff       = 10 * time.Minute
	defaultWorkerGCRecentCloseWindow = 15 * time.Second
	workerGCMaxPerCycle              = 2
	workerGCCloseTimeout             = 30 * time.Second
)

// workerGCState holds in-memory scheduling and anti-thrash guards for lossless
// managed-worker hibernation GC. All fields are protected by Super.mu.
type workerGCState struct {
	startedAt, lastSweep time.Time
	running              bool
	closing              map[string]bool      // worker name -> close in flight
	closingPane          map[string]string    // paneID -> worker name
	activity             map[string]time.Time // worker name -> last interaction
	backoff              map[string]time.Time // worker name -> retry-after
	recentlyClosed       map[string]time.Time // paneID -> closedAt

	// Test overrides (0 = default, <0 = 0).
	interval, gracePeriod, activityCooldown, recentCloseTTL time.Duration
}

type gcReconcile struct {
	name  string
	alive bool
}

type workerGCCandidate struct {
	name, paneID, workspaceID, machine, session, rule, reason string
	fault                                                     bool
	lastAct, createdAt                                        time.Time
}

var gcActiveFooterRe = regexp.MustCompile(
	`(?i)(\b[1-9]\d*\s+(background\s+(task|shell|job|agent|subagent|timer)s?\b|task\(s\)|timer\(s\)|(tasks?|jobs?|shells?|agents?|subagents?|timers?)\s+running\b|running\b|active\s+timers?\b)` +
		`|\besc\s+to\s+(interrupt|cancel)\b|\bbackground\s+(task|shell|job|timer)s?\s+running\b|\btimer\s+(running|active|pending)\b|\bwaiting\s+for\s+(timer|background|subagent))`,
)

func gcDur(v, def time.Duration) time.Duration {
	if v < 0 {
		return 0
	}
	if v > 0 {
		return v
	}
	return def
}

func gcInt(v, def int) int {
	if v < 0 {
		return 0
	}
	if v > 0 {
		return v
	}
	return def
}

// isManagedWorker reports whether w was spawned by Collie/Router and is eligible
// for lossless hibernation GC. Manual, adopted, system, legacy, and Keep workers are exempt.
func isManagedWorker(w *Worker) bool {
	return w != nil && !isHubSystemName(w.Name) && !w.Keep && (w.CreatedBy == routerName || w.CreatedBy == "router")
}

func workerMachineID(w *Worker) string {
	if w == nil {
		return ""
	}
	if w.Machine != "" {
		return w.Machine
	}
	return machineOf(w.PaneID)
}

func isStartupEmptyShell(w *Worker) bool {
	return w != nil && strings.TrimSpace(w.Session) == "" && w.PaneID != "" && w.WorkspaceID != "" &&
		(w.StartupFailed || w.Status == "failed" || (!w.Ended && w.Status == "starting"))
}

func isHerdrNotFoundErr(err error) bool {
	var he *HerdrError
	return errors.As(err, &he) && strings.Contains(strings.ToLower(he.Code), "not_found")
}

func screenHasActiveWork(screen string) bool {
	if hasBackground(screen) || screenAsks(screen) {
		return true
	}
	lines := strings.Split(strings.TrimRight(screen, "\n "), "\n")
	for i, seen := len(lines)-1, 0; i >= 0 && seen < 5; i-- {
		if l := strings.TrimSpace(lines[i]); l != "" {
			seen++
			if gcActiveFooterRe.MatchString(l) {
				return true
			}
		}
	}
	return false
}

func (sp *Super) touchWorkerActivityLocked(name string, now time.Time, clearIdle bool) {
	if name == "" {
		return
	}
	if sp.gc.activity == nil {
		sp.gc.activity = map[string]time.Time{}
	}
	sp.gc.activity[name] = now
	if w := sp.st.Workers[name]; w != nil && !w.Ended && !w.Hibernating {
		if clearIdle {
			w.IdleSince = nil
		} else if w.IdleSince != nil {
			t := now
			w.IdleSince = &t
		}
	}
}

func (sp *Super) touchPaneActivity(paneOrName string) {
	if sp == nil || paneOrName == "" {
		return
	}
	now := time.Now()
	sp.mu.Lock()
	defer sp.mu.Unlock()
	if w := sp.st.Workers[paneOrName]; w != nil && !w.Ended {
		sp.touchWorkerActivityLocked(w.Name, now, true)
		return
	}
	for _, w := range sp.st.Workers {
		if w != nil && !w.Ended && w.PaneID == paneOrName {
			sp.touchWorkerActivityLocked(w.Name, now, true)
			return
		}
	}
}

func (sp *Super) isWorkerGCClosingLocked(name string) bool { return name != "" && sp.gc.closing[name] }

func (sp *Super) recordRecentlyClosedPaneLocked(paneID string, now time.Time) {
	if paneID == "" {
		return
	}
	ttl := gcDur(sp.gc.recentCloseTTL, defaultWorkerGCRecentCloseWindow)
	if sp.gc.recentlyClosed == nil {
		sp.gc.recentlyClosed = map[string]time.Time{}
	}
	maps.DeleteFunc(sp.gc.recentlyClosed, func(_ string, at time.Time) bool { return now.Sub(at) > ttl })
	sp.gc.recentlyClosed[paneID] = now
}

func (sp *Super) isRecentlyClosedPaneLocked(paneID string, now time.Time) bool {
	at, ok := sp.gc.recentlyClosed[paneID]
	if !ok || paneID == "" {
		return false
	}
	if now.Sub(at) > gcDur(sp.gc.recentCloseTTL, defaultWorkerGCRecentCloseWindow) {
		delete(sp.gc.recentlyClosed, paneID)
		return false
	}
	return true
}

func (sp *Super) isPaneGCProtected(paneID string, now time.Time) bool {
	if sp == nil || paneID == "" {
		return false
	}
	sp.mu.Lock()
	defer sp.mu.Unlock()
	return sp.gc.closingPane[paneID] != "" || sp.isRecentlyClosedPaneLocked(paneID, now)
}

func (sp *Super) workerSubscribersLocked(name string) []string {
	var subs []string
	for _, id := range slices.Sorted(maps.Keys(sp.st.Threads)) {
		if th := sp.st.Threads[id]; th != nil && slices.Contains(th.Subscribers, name) {
			subs = append(subs, id)
		}
	}
	return subs
}

func (sp *Super) workerVetoedLocked(name string, now time.Time) bool {
	if until := sp.gc.backoff[name]; !until.IsZero() && now.Before(until) {
		return true
	}
	if cd := gcDur(sp.gc.activityCooldown, defaultWorkerGCActivityCooldown); cd > 0 && !sp.gc.activity[name].IsZero() && now.Sub(sp.gc.activity[name]) < cd {
		return true
	}
	for _, t := range sp.st.Triggers {
		if t == nil || t.Status != "queued" {
			continue
		}
		if slices.Contains(t.Targets, name) {
			return true
		}
		if len(t.Targets) == 0 && t.Source != "worker" && t.Source != "chat" {
			if th := sp.st.Threads[t.Thread]; th != nil && slices.Contains(th.Subscribers, name) && t.From != name {
				return true
			}
		}
	}
	return false
}

func (sp *Super) workerIdleLocked(w *Worker, now time.Time) (time.Duration, time.Time, bool) {
	lastAct := w.CreatedAt
	for _, ts := range []time.Time{w.ReportAt, sp.gc.activity[w.Name]} {
		if ts.After(lastAct) {
			lastAct = ts
		}
	}
	if w.IdleSince == nil {
		return 0, lastAct, false
	}
	if w.IdleSince.After(lastAct) {
		lastAct = *w.IdleSince
	}
	return max(0, now.Sub(lastAct)), lastAct, true
}

func (sp *Super) gcReadyLocked(now time.Time) (time.Duration, bool) {
	idleMin := gcInt(sp.st.Settings.WorkerIdleGCMinutes, int(defaultWorkerGCIdle/time.Minute))
	if idleMin <= 0 || sp.st.Settings.Paused || sp.handover != nil || sp.dream != nil || sp.hubUpdating {
		return 0, false
	}
	if !sp.gc.startedAt.IsZero() && now.Sub(sp.gc.startedAt) < gcDur(sp.gc.gracePeriod, defaultWorkerGCGracePeriod) {
		return 0, false
	}
	return time.Duration(idleMin) * time.Minute, true
}

func cmpCandLRU(a, b workerGCCandidate) int {
	if !a.lastAct.Equal(b.lastAct) {
		return a.lastAct.Compare(b.lastAct)
	}
	if !a.createdAt.Equal(b.createdAt) {
		return a.createdAt.Compare(b.createdAt)
	}
	return strings.Compare(a.name, b.name)
}

func (sp *Super) selectWorkerGCCandidatesLocked(live map[string]liveAgent, answered map[string]bool, now time.Time) []workerGCCandidate {
	idleLimit, ok := sp.gcReadyLocked(now)
	if !ok {
		return nil
	}
	maxLive := gcInt(sp.st.Settings.WorkerMaxLivePerMachine, defaultWorkerGCMaxLivePerMachine)
	liveByM, pickedByM := map[string]int{}, map[string]int{}
	var tierFault, tierIdle, tierLRU []workerGCCandidate
	lruPool := map[string][]workerGCCandidate{}

	for _, n := range slices.Sorted(maps.Keys(sp.st.Workers)) {
		w := sp.st.Workers[n]
		if !isManagedWorker(w) || w.Hibernating || sp.gc.closing[w.Name] {
			continue
		}
		m := workerMachineID(w)
		if answered != nil && !answered[m] {
			continue
		}
		if !w.Ended {
			liveByM[m]++
		}
		if sp.workerVetoedLocked(w.Name, now) {
			continue
		}
		a, hasLive := live[w.Name]
		if isStartupEmptyShell(w) {
			if hasLive && (a.Status == "working" || strings.TrimSpace(a.Session) != "") {
				continue
			}
			ref := w.CreatedAt
			if w.IdleSince != nil && !w.IdleSince.IsZero() {
				ref = *w.IdleSince
			}
			if now.Sub(ref) >= defaultWorkerGCFaultIdle {
				tierFault = append(tierFault, workerGCCandidate{
					name: w.Name, paneID: w.PaneID, workspaceID: w.WorkspaceID, machine: m,
					rule: "fault", reason: "启动空壳故障超时", fault: true, lastAct: ref, createdAt: w.CreatedAt,
				})
				if !w.Ended {
					pickedByM[m]++
				}
			}
			continue
		}
		if w.Ended || !hasLive || w.PaneID == "" || w.WorkspaceID == "" || a.Pane != w.PaneID ||
			(w.Status != "idle" && w.Status != "done") || (a.Status != "idle" && a.Status != "done") {
			continue
		}
		sess := strings.TrimSpace(w.Session)
		if sess == "" {
			sess = strings.TrimSpace(a.Session)
		}
		idleDur, lastAct, hasIdle := sp.workerIdleLocked(w, now)
		if sess == "" || !hasIdle {
			continue
		}
		cand := workerGCCandidate{
			name: w.Name, paneID: w.PaneID, workspaceID: w.WorkspaceID, machine: m,
			session: sess, lastAct: lastAct, createdAt: w.CreatedAt,
		}
		if idleDur >= idleLimit {
			cand.rule, cand.reason = "idle", fmt.Sprintf("空闲 %d 分钟", int(idleDur.Minutes()))
			tierIdle = append(tierIdle, cand)
			pickedByM[m]++
		} else if maxLive > 0 && idleDur >= defaultWorkerGCCapIdle {
			cand.rule = "lru"
			lruPool[m] = append(lruPool[m], cand)
		}
	}
	slices.SortFunc(tierFault, cmpCandLRU)
	slices.SortFunc(tierIdle, cmpCandLRU)
	if maxLive > 0 {
		for _, m := range slices.Sorted(maps.Keys(liveByM)) {
			excess := liveByM[m] - maxLive - pickedByM[m]
			if excess <= 0 || len(lruPool[m]) == 0 {
				continue
			}
			slices.SortFunc(lruPool[m], cmpCandLRU)
			for i := 0; i < min(len(lruPool[m]), excess); i++ {
				c := lruPool[m][i]
				c.reason = fmt.Sprintf("单机活体超软上限(%d/%d) LRU", liveByM[m], maxLive)
				tierLRU = append(tierLRU, c)
			}
		}
	}
	out := append(append(tierFault, tierIdle...), tierLRU...)
	if len(out) > workerGCMaxPerCycle {
		out = out[:workerGCMaxPerCycle]
	}
	return out
}

func (sp *Super) maybeRunWorkerGC(parent context.Context, live map[string]liveAgent, answered map[string]bool, now time.Time) {
	if sp == nil || sp.s == nil || sp.s.herdr == nil {
		return
	}
	sp.mu.Lock()
	if sp.gc.running || (!sp.gc.lastSweep.IsZero() && now.Sub(sp.gc.lastSweep) < gcDur(sp.gc.interval, defaultWorkerGCInterval)) {
		sp.mu.Unlock()
		return
	}
	sp.gc.lastSweep = now
	cands := sp.selectWorkerGCCandidatesLocked(live, answered, now)
	if len(cands) == 0 {
		sp.mu.Unlock()
		return
	}
	sp.gc.running = true
	sp.mu.Unlock()
	go func() {
		defer func() { sp.mu.Lock(); sp.gc.running = false; sp.mu.Unlock() }()
		for _, cand := range cands {
			cctx, cancel := context.WithTimeout(parent, workerGCCloseTimeout)
			sp.hibernateWorker(cctx, cand, time.Now())
			cancel()
		}
	}()
}

// runWorkerGCSweep executes one GC sweep synchronously (used by tests).
func (sp *Super) runWorkerGCSweep(ctx context.Context, live map[string]liveAgent, answered map[string]bool, now time.Time) int {
	sp.mu.Lock()
	cands := sp.selectWorkerGCCandidatesLocked(live, answered, now)
	sp.mu.Unlock()
	closed := 0
	for _, cand := range cands {
		if sp.hibernateWorker(ctx, cand, now) {
			closed++
		}
	}
	return closed
}

func (sp *Super) reverifyCandidateLocked(cand workerGCCandidate, now time.Time) (*Worker, bool) {
	idleLimit, ok := sp.gcReadyLocked(now)
	if !ok {
		return nil, false
	}
	w := sp.st.Workers[cand.name]
	if !isManagedWorker(w) || w.Hibernating || w.PaneID != cand.paneID || w.WorkspaceID != cand.workspaceID || w.PaneID == "" || w.WorkspaceID == "" || sp.workerVetoedLocked(w.Name, now) {
		return nil, false
	}
	if cand.fault {
		return w, isStartupEmptyShell(w)
	}
	if w.Ended || (w.Status != "idle" && w.Status != "done") {
		return nil, false
	}
	if cand.session != "" && strings.TrimSpace(w.Session) == "" {
		w.Session = cand.session
	}
	idleDur, _, hasIdle := sp.workerIdleLocked(w, now)
	if strings.TrimSpace(w.Session) == "" || !hasIdle || (cand.rule == "idle" && idleDur < idleLimit) || (cand.rule == "lru" && idleDur < defaultWorkerGCCapIdle) {
		return nil, false
	}
	return w, true
}

func (sp *Super) abortWorkerHibernation(name string, revert bool, now time.Time) {
	sp.mu.Lock()
	changed := false
	if revert {
		if cur := sp.st.Workers[name]; cur != nil && cur.Hibernating {
			cur.Hibernating = false
			sp.changed()
			changed = true
		}
	}
	if sp.gc.backoff == nil {
		sp.gc.backoff = map[string]time.Time{}
	}
	sp.gc.backoff[name] = now.Add(defaultWorkerGCFailBackoff)
	hasDB := sp.db != nil
	sp.mu.Unlock()
	if changed && hasDB {
		sp.flush()
	}
}

func (sp *Super) hibernateWorker(ctx context.Context, cand workerGCCandidate, now time.Time) bool {
	if sp == nil || sp.s == nil || sp.s.herdr == nil || cand.name == "" {
		return false
	}
	sp.mu.Lock()
	if sp.gc.closing[cand.name] {
		sp.mu.Unlock()
		return false
	}
	if sp.gc.closing == nil {
		sp.gc.closing, sp.gc.closingPane = map[string]bool{}, map[string]string{}
	}
	sp.gc.closing[cand.name], sp.gc.closingPane[cand.paneID] = true, cand.name
	_, ok := sp.reverifyCandidateLocked(cand, now)
	sp.mu.Unlock()
	defer func() {
		sp.mu.Lock()
		delete(sp.gc.closing, cand.name)
		if sp.gc.closingPane[cand.paneID] == cand.name {
			delete(sp.gc.closingPane, cand.paneID)
		}
		sp.mu.Unlock()
	}()
	if !ok {
		return false
	}
	if rr, err := sp.s.herdr.Read(ctx, cand.paneID, "recent", "text", 20); err != nil {
		if !(cand.fault && isHerdrNotFoundErr(err)) {
			log.Printf("worker gc: screen read failed for %s (%s): %v", cand.name, cand.paneID, err)
			return false
		}
	} else if screenHasActiveWork(rr.Text) {
		return false
	}
	panes, err := sp.s.herdr.PanesOnMachine(ctx, cand.machine)
	if err != nil {
		sp.abortWorkerHibernation(cand.name, false, now)
		return false
	}
	wsCount := 0
	var target *Pane
	for i := range panes {
		if panes[i].WorkspaceID == cand.workspaceID {
			wsCount++
			if panes[i].ID == cand.paneID {
				target = &panes[i]
			}
		}
	}
	if cand.fault && target == nil {
		sp.finalizeHibernatedWorker(cand.name, cand.reason, true, now)
		return true
	}
	if wsCount != 1 || target == nil || (cand.fault && target.AgentStatus == "working") || (!cand.fault && target.AgentStatus != "idle" && target.AgentStatus != "done") {
		return false
	}

	// Phase 1: re-verify & persist Hibernating=true BEFORE closing workspace.
	sp.mu.Lock()
	w, ok := sp.reverifyCandidateLocked(cand, now)
	if !ok {
		sp.mu.Unlock()
		return false
	}
	w.Hibernating = true
	sp.changed()
	hasDB := sp.db != nil
	sp.mu.Unlock()
	if hasDB {
		sp.flush()
	}

	// Phase 2: close herdr workspace (never deletes worktree or transcript).
	if closeErr := sp.s.herdr.Call(ctx, "workspace.close", map[string]any{"workspace_id": cand.workspaceID}, nil); closeErr != nil && !isHerdrNotFoundErr(closeErr) {
		var he *HerdrError
		sp.abortWorkerHibernation(cand.name, errors.As(closeErr, &he), now)
		return false
	}
	sp.finalizeHibernatedWorker(cand.name, cand.reason, cand.fault, now)
	return true
}

func (sp *Super) archiveWorkerSession(wk *Worker, subs []string, reason string, now time.Time) {
	if sp == nil || sp.s == nil || wk == nil || strings.TrimSpace(wk.Session) == "" {
		return
	}
	kind := kinds[wk.Kind]
	if kind == "" {
		kind = wk.Kind
	}
	model, effort := wk.Model, wk.Effort
	if model == "" && effort == "" && wk.Run != nil {
		model, effort = wk.Run[0], wk.Run[1]
	}
	title := wk.Label
	if title == "" {
		title = clip(firstLine(wk.Task), 80)
	}
	sp.s.remember(&Ended{
		Kind: kind, Machine: workerMachineID(wk), Cwd: wk.Cwd, Title: title,
		Name: wk.Name, Label: wk.Label, Model: model, Effort: effort,
		Thread: wk.Thread, CreatedBy: wk.CreatedBy, Subscribers: subs,
		Reason: reason, Session: wk.Session, Ended: now,
	})
}

func (sp *Super) finalizeHibernatedWorker(name, reason string, fault bool, now time.Time) {
	sp.mu.Lock()
	w := sp.st.Workers[name]
	if w == nil {
		sp.mu.Unlock()
		return
	}
	wkCopy, subs := *w, sp.workerSubscribersLocked(name)
	sp.recordRecentlyClosedPaneLocked(w.PaneID, now)
	sp.mu.Unlock()

	if !fault && strings.TrimSpace(wkCopy.Session) != "" {
		sp.archiveWorkerSession(&wkCopy, subs, "hibernated", now)
	}

	sp.mu.Lock()
	w = sp.st.Workers[name]
	if w == nil {
		sp.mu.Unlock()
		return
	}
	w.Ended, w.Hibernating, w.StartupFailed, w.IdleSince = true, false, false, nil
	if fault {
		w.Status, w.PaneID, w.WorkspaceID = "failed", "", ""
	} else {
		w.Status = "done"
	}
	for _, th := range sp.st.Threads {
		if th != nil {
			th.Subscribers = remove(th.Subscribers, name)
		}
	}
	note := "上下文与目录已保留，可随时 --resume 续聊"
	if fault || strings.TrimSpace(w.Session) == "" {
		note = "空壳进程已回收"
	}
	msg := fmt.Sprintf("已自动休眠 %s（%s）", name, note)
	if reason != "" {
		msg = fmt.Sprintf("已自动休眠 %s（%s，%s）", name, reason, note)
	}
	sp.addEntryLocked("event", w.Thread, msg, name, "info")
	hasDB := sp.db != nil
	sp.mu.Unlock()
	if hasDB {
		sp.flush()
	}
}

func (sp *Super) reconcileHibernatingWorker(ctx context.Context, name string, alive bool, now time.Time) {
	sp.mu.Lock()
	w := sp.st.Workers[name]
	if w == nil || !w.Hibernating || sp.gc.closing[name] {
		sp.mu.Unlock()
		return
	}
	fault, paneID, machine := isStartupEmptyShell(w), w.PaneID, workerMachineID(w)
	sp.mu.Unlock()

	if !alive && fault && paneID != "" && sp.s != nil && sp.s.herdr != nil {
		if panes, err := sp.s.herdr.PanesOnMachine(ctx, machine); err == nil {
			for _, p := range panes {
				if p.ID == paneID {
					alive = true
					break
				}
			}
		}
	}
	if !alive {
		sp.finalizeHibernatedWorker(name, "休眠对账完成", fault, now)
		return
	}
	sp.abortWorkerHibernation(name, true, now)
}
