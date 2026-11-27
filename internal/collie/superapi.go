package collie

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

func (s *Server) superRoutes(mux *http.ServeMux) {
	api := func(pattern string, h http.HandlerFunc) { mux.Handle(pattern, s.auth(h)) }
	api("GET /api/super", s.superSnapshot)
	api("GET /api/super/events", s.superEvents)
	api("POST /api/super/say", s.superSay)
	api("POST /api/super/reply", s.superReply)
	api("POST /api/super/dismiss", s.superDismiss)
	api("POST /api/super/pin", s.superPin)
	api("POST /api/chat/read", s.superMarkRead)
	api("POST /api/super/spawn", s.superSpawn)
	api("POST /api/super/send", s.superSend)
	api("POST /api/super/report", s.superReport)
	api("POST /api/super/subscribe", s.superSubscribe)
	api("POST /api/super/stop", s.superStop)
	api("POST /api/super/answer", s.superAnswer)
	api("GET /api/super/show/{id}", s.superShow)
	api("GET /api/super/list", s.superList)
	api("GET /api/super/triggers", s.superTriggers)
	api("GET /api/super/timers", s.superTimers)
	api("POST /api/super/timers", s.superAddTimer)
	api("POST /api/super/timers/{id}/delete", s.superDeleteTimer)
	api("GET /api/super/settings", s.superSettings)
	api("POST /api/super/settings", s.superSaveSettings)
	api("POST /api/super/seen", s.superSeen)
	api("GET /api/super/lark", s.superLark)
	api("GET /api/super/earlier", s.superEarlier)
	api("GET /api/super/search", s.superSearch)
	api("GET /api/super/notebook", s.superNotebook)
	api("GET /api/super/memory", s.superMemory)
	api("POST /api/super/memory", s.superSaveMemory)
	api("GET /api/memory/export", s.memoryExport)
	api("POST /api/memory/import", s.memoryImport)
	api("POST /api/super/dream", s.superDream)
	api("POST /api/super/router/restart", s.superRestartRouter)
	api("GET /api/super/integrations", s.superIntegrations)
	api("POST /api/super/integrations/{kind}", s.superSaveIntegration)
	api("POST /api/super/integrations/{kind}/test", s.superTestIntegration)
	api("POST /api/super/integrations/{kind}/sample", s.superSampleIntegration)
	api("GET /api/super/integrations/{kind}/probe", s.superProbeIntegration)
	api("POST /api/super/integrations/{kind}/probe", s.superProbeIntegration)
	api("POST /api/super/integrations/feishu/pair", s.superFeishuPair)
	api("POST /api/super/integrations/feishu/onboard", s.superFeishuOnboardStart)
	api("GET /api/super/integrations/feishu/onboard", s.superFeishuOnboardStatus)
	api("DELETE /api/super/integrations/feishu/onboard", s.superFeishuOnboardCancel)
	api("POST /api/super/integrations/feishu/reply", s.superFeishuReply)
	api("POST /api/super/integrations/{kind}/comment", s.superForgeComment)
	api("POST /api/super/notify/test", s.superTestNotify)
	mux.HandleFunc("POST /hooks/{source}", s.webhook)
}

// ---------------------------------------------------------------- snapshot

type routerView struct {
	Alive  bool   `json:"alive"`
	Status string `json:"status"`
	PaneID string `json:"pane_id,omitempty"`
	Kind   string `json:"kind"`
	// How full its context is (percent), and when it rotates to a fresh session.
	Context  int  `json:"context,omitempty"`
	RotateAt int  `json:"rotate_at"`
	Handover bool `json:"handover,omitempty"`
	Dreaming bool `json:"dreaming,omitempty"`
	// It has already replied this turn, and is only wrapping up (memory…).
	Replied bool `json:"replied,omitempty"`
	// TurnKind distinguishes human-chat turns ("chat") from background worker/timer/system turns ("background").
	TurnKind string `json:"turn_kind,omitempty"`
	// The model and effort it runs with ("" = the agent's own default), and
	// whether the settings have changed since it started.
	Model   string `json:"model,omitempty"`
	Effort  string `json:"effort,omitempty"`
	Pending bool   `json:"pending,omitempty"`
}

type superView struct {
	Router  routerView `json:"router"`
	Entries []*Entry   `json:"entries"`
	Earlier bool       `json:"earlier,omitempty"` // older chat than Entries: /api/super/earlier
	// Pending: Collie's --opt and --check messages in chat:main still waiting on the human.
	Pending []PendingItem `json:"pending"`
	// Pinned: messages in chat:main manually pinned by the human until unpinned.
	Pinned []PinnedItem `json:"pinned"`
	// LastReadEntryID: persisted main-chat read watermark shared across devices.
	LastReadEntryID string    `json:"last_read_entry_id,omitempty"`
	Workers         []*Worker `json:"workers"`
	Queued          int       `json:"queued"`
	// QueuedRouter: those waiting for Collie itself, not for a worker.
	QueuedRouter int  `json:"queued_router"`
	Paused       bool `json:"paused"`
	// Follow: the next time Collie set itself to look at something again.
	Follow *followView `json:"follow,omitempty"`
	// TimersCount: total number of enabled scheduled timers for sidebar overview.
	TimersCount int `json:"timers_count,omitempty"`
	// IntegrationsSummary: live counts of connected/error integrations for sidebar & UI badges.
	IntegrationsSummary *IntegrationsSummary `json:"integrations_summary,omitempty"`
	// PeersSummary: live counts of paired peer hubs for UI card & SSE updates.
	PeersSummary *PeersSummary `json:"peers_summary,omitempty"`
}

type followView struct {
	Next     time.Time `json:"next"`
	NextText string    `json:"next_text"` // what it will look at then
}

const (
	// Ended workers stay in superView for 12h so sidebar/thread links keep their
	// short task summaries without shipping multi-kilobyte historical prompts or
	// final reports on every SSE tick. Full details remain in store and `collie show`.
	endedWorkerTaskMaxRunes       = 400
	endedWorkerLastReportMaxRunes = 240
)

func clipWorkerSummaryRunes(s string, maxRunes int) string {
	if maxRunes <= 0 || len(s) <= maxRunes || utf8.RuneCountInString(s) <= maxRunes {
		return s
	}
	n := 0
	for i := range s {
		if n == maxRunes {
			return strings.TrimRight(s[:i], " \t\r\n") + "…"
		}
		n++
	}
	return s
}

func slimEndedWorkerView(cp *Worker) {
	if cp == nil {
		return
	}
	if cp.Ended || cp.Status == "gone" || cp.Status == "stopped" {
		cp.Task = clipWorkerSummaryRunes(cp.Task, endedWorkerTaskMaxRunes)
		cp.LastReport = clipWorkerSummaryRunes(cp.LastReport, endedWorkerLastReportMaxRunes)
	}
}

func (s *Server) superState(ctx context.Context) superView {
	sp := s.sup
	live, _ := sp.liveAgents(ctx)
	sp.mu.Lock()
	defer sp.mu.Unlock()
	v := superView{Router: routerView{Kind: sp.st.Settings.RouterKind, Status: "asleep"}, Paused: sp.st.Settings.Paused}
	if a, ok := live[routerName]; ok {
		v.Router = routerView{Alive: true, Status: a.Status, PaneID: a.Pane, Kind: a.Kind}
		if asked := sp.routerAskedAt; a.Status == "working" && !asked.IsZero() {
			for i := len(sp.st.Entries) - 1; i >= 0 && !sp.st.Entries[i].At.Before(asked); i-- {
				if r := sp.st.Entries[i].Role; r == "collie" || r == "user" {
					v.Router.Replied = r == "collie" // unless the human has said more since
					break
				}
			}
		}
	} else if sp.routerLaunching {
		v.Router.Status = "starting"
	}
	v.Router.Context, v.Router.RotateAt, v.Router.Handover = sp.routerUsage.Pct(), sp.rotateAtLocked(), sp.handover != nil
	v.Router.Dreaming = sp.dream != nil
	st := sp.st.Settings
	routerEff := effectiveEffort(st.RouterKind, st.RouterModel, st.RouterEffort)
	v.Router.Model, v.Router.Effort = st.RouterModel, routerEff
	if v.Router.Alive {
		if run := sp.routerRunLocked(); run != nil {
			v.Router.Model, v.Router.Effort = run[0], run[1]
		} else if launch := sp.routerLaunchLocked(); launch != nil {
			v.Router.Model, v.Router.Effort = launch.Model, launch.Effort
		}
		v.Router.Pending = sp.routerPendingLocked(v.Router.Kind, st)
	}
	if sp.dream != nil {
		v.Router.Status = "dreaming"
	}
	const (
		superDefaultEntries      = 200
		superUnreadMaxEntries    = 500
		superUnreadContextBefore = 10
	)
	es := sp.st.Entries
	if len(es) > superDefaultEntries {
		start := len(es) - superDefaultEntries
		if curN := idNum(sp.st.LastReadEntryID); curN > 0 && idNum(es[start].ID) > curN {
			minStart := max(0, len(es)-superUnreadMaxEntries)
			target := start
			for i := start - 1; i >= minStart; i-- {
				target = i
				if idNum(es[i].ID) <= curN {
					break
				}
			}
			start = max(minStart, target-superUnreadContextBefore)
		}
		if start > 0 {
			es = es[start:]
			v.Earlier = true
		}
	}
	v.Entries = append([]*Entry{}, es...)
	v.Pending = sp.pendingLocked(time.Now())
	if v.Pending == nil {
		v.Pending = []PendingItem{}
	}
	v.Pinned = sp.pinnedLocked()
	if v.Pinned == nil {
		v.Pinned = []PinnedItem{}
	}
	v.LastReadEntryID = sp.st.LastReadEntryID
	v.Workers = []*Worker{}
	for _, w := range sp.st.Workers {
		if !w.Ended || time.Since(w.CreatedAt) < 12*time.Hour {
			cp := *w
			slimEndedWorkerView(&cp)
			v.Workers = append(v.Workers, &cp)
		}
	}
	sort.Slice(v.Workers, func(i, j int) bool { return v.Workers[i].CreatedAt.After(v.Workers[j].CreatedAt) })
	v.Follow = sp.followLocked()
	for _, t := range sp.st.Timers {
		if t.Enabled {
			v.TimersCount++
		}
	}
	if sp.integrations != nil {
		sum := sp.integrations.Summary()
		v.IntegrationsSummary = &sum
	}
	if sp.peers != nil {
		psum := sp.peers.Summary()
		v.PeersSummary = &psum
	}
	hasQueuedChat := false
	for _, t := range sp.st.Triggers {
		if t.Status == "queued" {
			v.Queued++
			if len(t.Targets) == 0 || slices.Contains(t.Targets, routerName) {
				v.QueuedRouter++
				if t.Source == "chat" {
					hasQueuedChat = true
				}
			}
		}
	}
	if v.Router.Status == "working" || v.Router.Status == "starting" {
		if !v.Router.Replied && (sp.routerAskedChat || hasQueuedChat) {
			v.Router.TurnKind = "chat"
		} else if !v.Router.Replied && (!sp.routerAskedAt.IsZero() || v.Router.Status == "starting") {
			v.Router.TurnKind = "background"
		}
	}
	return v
}

func (s *Server) superSnapshot(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, 200, s.superState(r.Context()))
}

func (s *Server) superEvents(w http.ResponseWriter, r *http.Request) {
	sse, ok := newSSE(w)
	if !ok {
		return
	}
	ch, done := s.sup.subscribe()
	defer done()
	var last string
	tick := time.NewTicker(2 * time.Second)
	defer tick.Stop()
	beat := time.Now()
	for {
		b, _ := json.Marshal(s.superState(r.Context()))
		if string(b) != last {
			last = string(b)
			if sse.send("super", b) != nil {
				return
			}
			beat = time.Now()
		} else if time.Since(beat) > 15*time.Second {
			sse.comment()
			beat = time.Now()
		}
		select {
		case <-r.Context().Done():
			return
		case <-ch:
		case <-tick.C:
		}
	}
}

// ---------------------------------------------------------------- conversation

func (s *Server) superSay(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Text        string `json:"text"`
		Machine     string `json:"machine"` // where to dispatch this time, "" = the default
		Kind        string `json:"kind"`
		Model       string `json:"model"`
		Effort      string `json:"effort"`
		Re          string `json:"re"`            // a message of Collie's this answers
		Pick        bool   `json:"pick"`          // true when tapping a --opt button: collapse card in-place without a user bubble
		ClientMsgID string `json:"client_msg_id"` // optional client idempotency token
	}
	if !decode(w, r, &body) || strings.TrimSpace(body.Text) == "" {
		if strings.TrimSpace(body.Text) == "" {
			writeJSON(w, 400, map[string]any{"error": "empty message"})
		}
		return
	}
	cid := strings.TrimSpace(body.ClientMsgID)
	if len(cid) > 128 {
		cid = cid[:128]
	}
	now := time.Now()
	sp := s.sup
	sp.mu.Lock()
	if prev, ok := sp.lookupSayDedupLocked(cid, now); ok {
		sp.mu.Unlock()
		writeJSON(w, 200, map[string]any{"entry": prev.entry, "trigger": prev.trigger, "duplicate": true})
		return
	}
	beforePending := sp.pendingLocked(now)
	re := sp.sayQuoteLocked(body.Re)
	var pickTarget *Entry
	if re != nil && re.Entry != "" {
		if cand := sp.findEntryLocked(re.Entry); cand != nil && cand.Role == "collie" && len(cand.Options) > 0 {
			pickTarget = cand
		}
	}
	isSilentPick := body.Pick && pickTarget != nil
	var entryID string
	text := body.Text
	if isSilentPick {
		pickedLabel := strings.TrimSpace(body.Text)
		pickTarget.Pick = &DecisionPick{State: "picked", Text: pickedLabel, At: now}
		entryID = pickTarget.ID
		sp.changed()
		text = "[The human picked option \"" + clip(oneLine(pickedLabel), 300) + "\" in reply to your message \"" + clip(pickTarget.Text, 240) + "\"]\n" + pickedLabel
	} else {
		e := sp.addEntryReLocked("user", mainThread, body.Text, "", "", re)
		if cid != "" {
			e.ClientMsgID = cid
		}
		entryID = e.ID
		if pickTarget != nil && pickTarget.Pick == nil {
			pickTarget.Pick = &DecisionPick{State: "custom", Text: strings.TrimSpace(body.Text), At: now}
			sp.changed()
		}
		if re != nil { // which message they mean: Collie need not guess
			if re.Who == "你" {
				text = "[In reply to what the human said earlier: \"" + clip(re.Text, 300) + "\"]\n" + text
			} else {
				text = "[In reply to what you said earlier: \"" + clip(re.Text, 300) + "\"]\n" + text
			}
		}
	}
	st := sp.st.Settings
	var hint []string
	if m := s.machineByName(body.Machine); m != "" || body.Machine == "local" {
		if m != st.WorkerMachine {
			name := "hub"
			if m != "" {
				name = m
			}
			hint = append(hint, "--machine "+name)
		}
	}
	if body.Kind == "agy" { // the composer's name for it
		body.Kind = "antigravity"
	} else if body.Kind == "cursor-agent" {
		body.Kind = "cursor"
	}
	_, known := kinds[body.Kind]
	if known && kinds[body.Kind] != kinds[st.WorkerKind] {
		hint = append(hint, "--kind "+body.Kind)
	}
	// Model choices only differ from the defaults for the default kind.
	defModel, defEffort := "", ""
	if !known || kinds[body.Kind] == kinds[st.WorkerKind] {
		defModel, defEffort = st.WorkerModel, st.WorkerEffort
	}
	effKind := body.Kind
	if !known {
		effKind = st.WorkerKind
	}
	effModel := strings.TrimSpace(body.Model)
	if effModel == "" {
		effModel = defModel
	}
	if m := strings.TrimSpace(body.Model); m != "" && m != defModel && modelNameRe.MatchString(m) {
		hint = append(hint, "--model "+m)
	}
	if e := effectiveEffort(effKind, effModel, body.Effort); e != "" && e != effectiveEffort(effKind, effModel, defEffort) && modelNameRe.MatchString(e) {
		hint = append(hint, "--effort "+e)
	}
	if len(hint) > 0 {
		text += "\n\n[The human picked where this runs: spawn with " + strings.Join(hint, " ") + "]"
	}
	t := sp.ingestLocked("chat", mainThread, "you", text, "", "", entryID)
	sp.recordSayDedupLocked(cid, entryID, t.ID, now)
	afterPending := sp.pendingLocked(now)
	mgr := sp.integrations
	sp.mu.Unlock()
	if mgr != nil {
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
		if re != nil && re.Entry != "" && re.Who == "Collie" {
			resolvedIDs[re.Entry] = true
		}
		if len(resolvedIDs) > 0 {
			prefix := "已在网页端回复："
			if isSilentPick {
				prefix = "已在网页端选择："
			}
			summary := prefix + clip(oneLine(body.Text), 40)
			for rid := range resolvedIDs {
				id := rid
				go mgr.SyncResolved(context.Background(), id, summary, "")
			}
		}
	}
	resp := map[string]any{"entry": entryID, "trigger": t.ID}
	if isSilentPick {
		resp["picked"] = true
	}
	writeJSON(w, 200, resp)
}

func (s *Server) superReply(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Text    string   `json:"text"`
		Thread  string   `json:"thread"`
		Re      string   `json:"re"`
		Options []string `json:"options"`
		Files   []string `json:"files"`
		Check   bool     `json:"check"`
	}
	if !decode(w, r, &body) {
		return
	}
	if strings.TrimSpace(body.Text) == "" {
		writeJSON(w, 400, map[string]any{"error": "empty reply"})
		return
	}
	e, warn := s.sup.ReplyFiles(body.Thread, body.Text, body.Re, body.Options, body.Check, body.Files)
	res := map[string]any{"ok": true, "entry": e.ID}
	if warn != "" {
		res["warning"] = warn
	}
	writeJSON(w, 200, res)
}

func (s *Server) superDismiss(w http.ResponseWriter, r *http.Request) {
	var body struct {
		ID string `json:"id"`
	}
	if !decode(w, r, &body) {
		return
	}
	id := strings.TrimSpace(body.ID)
	if id == "" {
		writeJSON(w, 400, map[string]any{"error": "id is required"})
		return
	}
	s.sup.Dismiss(id)
	writeJSON(w, 200, map[string]any{"ok": true})
}

func (s *Server) superPin(w http.ResponseWriter, r *http.Request) {
	var body struct {
		ID     string `json:"id"`
		Pinned bool   `json:"pinned"`
	}
	if !decode(w, r, &body) {
		return
	}
	id := strings.TrimSpace(body.ID)
	if id == "" {
		writeJSON(w, 400, map[string]any{"error": "id is required"})
		return
	}
	if err := s.sup.Pin(id, body.Pinned); err != nil {
		writeJSON(w, 404, map[string]any{"error": err.Error()})
		return
	}
	writeJSON(w, 200, map[string]any{"ok": true, "pinned": body.Pinned})
}

func (s *Server) superMarkRead(w http.ResponseWriter, r *http.Request) {
	var body struct {
		EntryID         string `json:"entry_id"`
		LastReadEntryID string `json:"last_read_entry_id"`
	}
	if !decode(w, r, &body) {
		return
	}
	id := strings.TrimSpace(body.EntryID)
	if id == "" {
		id = strings.TrimSpace(body.LastReadEntryID)
	}
	if id == "" {
		writeJSON(w, 400, map[string]any{"error": "entry_id is required"})
		return
	}
	cur, changed, err := s.sup.MarkChatRead(id)
	if err != nil {
		writeJSON(w, 400, map[string]any{"error": err.Error()})
		return
	}
	writeJSON(w, 200, map[string]any{"ok": true, "last_read_entry_id": cur, "changed": changed})
}

// replyLen is a reply's length as people read it: each Chinese/Japanese/Korean
// character counts one, and so does each run of Latin text (a word).
func replyLen(s string) int {
	n, cjk := 0, 0
	for _, f := range strings.Fields(s) {
		ascii := true
		for _, r := range f {
			if r > 0x2E80 {
				cjk++
				ascii = false
			}
		}
		if ascii {
			n++
		}
	}
	return n + cjk
}

// ---------------------------------------------------------------- workers

func (s *Server) superSpawn(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Kind      string  `json:"kind"`
		Cwd       string  `json:"cwd"`
		Task      string  `json:"task"`
		Thread    string  `json:"thread"`
		Name      string  `json:"name"`
		Label     string  `json:"label"`
		By        string  `json:"by"`
		Machine   *string `json:"machine"`
		Model     string  `json:"model"`
		Effort    string  `json:"effort"`
		Subscribe *bool   `json:"subscribe"`
		Resume    string  `json:"resume"` // a stopped worker to carry on from
	}
	if !decode(w, r, &body) {
		return
	}
	body.Label = cleanWorkerLabel(body.Label)
	session := ""
	if body.Resume != "" {
		old, err := s.sup.resumeFrom(body.Resume)
		if err != nil {
			writeJSON(w, 400, map[string]any{"error": err.Error()})
			return
		}
		// Its own computer, directory and agent: that is where the conversation is.
		body.Kind, body.Cwd, session = old.Kind, old.Cwd, old.Session
		m := old.Machine
		body.Machine = &m
		if body.Name == "" {
			body.Name = old.Name
		}
		if body.Label == "" {
			body.Label = old.Label
		}
		if body.Model == "" && body.Effort == "" {
			body.Model, body.Effort = old.Model, old.Effort
		}
		if body.Thread == "" {
			body.Thread = old.Thread
		}
	}
	if strings.TrimSpace(body.Task) == "" {
		writeJSON(w, 400, map[string]any{"error": "task is required"})
		return
	}
	if !modelNameRe.MatchString(body.Model) || !modelNameRe.MatchString(body.Effort) {
		writeJSON(w, 400, map[string]any{"error": "invalid model or effort"})
		return
	}
	if body.By == "" {
		body.By = routerName
	}
	if body.Thread == "" {
		body.Thread = mainThread
	}
	// Workers subscribe to their own thread unless it is the shared main chat,
	// which always stays with the router.
	sub := body.Thread != mainThread
	if body.Subscribe != nil {
		sub = *body.Subscribe
	}
	machine := s.sup.settings().WorkerMachine
	if session != "" {
		machine = *body.Machine // an id already
	} else if body.Machine != nil {
		machine = s.machineByName(*body.Machine)
	}
	wk, started, err := s.sup.Spawn(body.Kind, body.Cwd, body.Task, body.Thread, body.Name, body.Label, body.By, machine, body.Model, body.Effort, sub, session)
	if err != nil {
		writeJSON(w, 400, map[string]any{"error": err.Error()})
		return
	}
	// Most failures (the computer's herdr down, a bad directory) show in the
	// first seconds: say them now, so nobody hears "started" first.
	status := "starting"
	select {
	case err := <-started:
		if err != nil {
			writeJSON(w, 502, map[string]any{"error": "worker " + wk.Name + " failed to start: " + err.Error()})
			return
		}
		status = "started"
	case <-time.After(8 * time.Second):
	}
	res := map[string]any{"worker": wk.Name, "kind": wk.Kind, "status": status, "thread": wk.Thread,
		"note": "its agent is starting; you will get a [collie] event when it reports, finishes, gets blocked or fails. Tell the human it is on its way, not that it is done"}
	if wk.Label != "" {
		res["label"] = wk.Label
	}
	writeJSON(w, 202, res)
}

func (s *Server) superSend(w http.ResponseWriter, r *http.Request) {
	var body struct {
		To     string `json:"to"`
		Text   string `json:"text"`
		Thread string `json:"thread"`
	}
	if !decode(w, r, &body) {
		return
	}
	sp := s.sup
	sp.mu.Lock()
	wk := sp.st.Workers[body.To]
	if wk == nil || wk.Ended {
		sp.mu.Unlock()
		writeJSON(w, 404, map[string]any{"error": "no running worker named " + body.To})
		return
	}
	if body.Thread == "" {
		body.Thread = wk.Thread
	}
	t := sp.ingestLocked("router", body.Thread, routerName, body.Text, "", body.To, "")
	sp.mu.Unlock()
	writeJSON(w, 202, map[string]any{"trigger": t.ID, "note": "queued; delivered when the worker is free"})
}

func (s *Server) superReport(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Worker string `json:"worker"`
		Pane   string `json:"pane"`
		Text   string `json:"text"`
	}
	if !decode(w, r, &body) {
		return
	}
	name := body.Worker
	if name == "" && body.Pane != "" {
		// A worker on another computer reports through that computer's token.
		if _, d := s.identity(r); d != nil && d.Kind == "machine" {
			body.Pane = join(body.Pane, d.ID)
		}
		name = s.sup.workerByPane(body.Pane)
	}
	if name == "" {
		writeJSON(w, 400, map[string]any{"error": "could not tell which worker you are; pass --worker <name>"})
		return
	}
	if err := s.sup.Report(name, strings.TrimSpace(body.Text)); err != nil {
		writeJSON(w, 404, map[string]any{"error": err.Error()})
		return
	}
	writeJSON(w, 200, map[string]any{"ok": true})
}

func (s *Server) superSubscribe(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Worker string `json:"worker"`
		Thread string `json:"thread"`
		Off    bool   `json:"off"`
	}
	if !decode(w, r, &body) {
		return
	}
	sp := s.sup
	sp.mu.Lock()
	defer sp.mu.Unlock()
	if sp.st.Workers[body.Worker] == nil || body.Thread == "" {
		writeJSON(w, 400, map[string]any{"error": "worker and thread are required"})
		return
	}
	if body.Thread == mainThread && !body.Off {
		writeJSON(w, 400, map[string]any{"error": "chat:main always routes to the router"})
		return
	}
	th := sp.threadLocked(body.Thread)
	if body.Off {
		th.Subscribers = remove(th.Subscribers, body.Worker)
	} else if !contains(th.Subscribers, body.Worker) {
		th.Subscribers = append(th.Subscribers, body.Worker)
	}
	sp.changed()
	writeJSON(w, 200, map[string]any{"thread": th.ID, "subscribers": th.Subscribers})
}

func (s *Server) superStop(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Worker string `json:"worker"`
	}
	if !decode(w, r, &body) {
		return
	}
	sp := s.sup
	sp.mu.Lock()
	wk := sp.st.Workers[body.Worker]
	sp.mu.Unlock()
	if wk == nil {
		writeJSON(w, 404, map[string]any{"error": "unknown worker"})
		return
	}
	if wk.Session == "" && wk.PaneID != "" {
		// herdr does not always know (Antigravity, or Codex without hook): ask the pane's machine.
		local, m := split(wk.PaneID)
		sess := s.titlesFor(m, r.Context())[local].Session
		if sess == "" && m == "" && kinds[wk.Kind] == "codex" {
			if p, _ := s.findTranscript("codex", "", "", local, wk.Cwd); p != "" {
				sess = codexSessionID(p)
			}
		}
		if sess != "" {
			sp.mu.Lock()
			wk.Session = sess
			sp.mu.Unlock()
		}
	}
	if wk.PaneID != "" {
		// Esc stops the turn it is on, the way you would before closing it:
		// Codex's turn lives in a shared daemon and would carry on without
		// its window.
		s.herdr.Call(r.Context(), "pane.send_keys", map[string]any{"pane_id": wk.PaneID, "keys": []string{"esc"}}, nil)
		time.Sleep(700 * time.Millisecond)
	}
	if kinds[wk.Kind] == "codex" && wk.Session != "" {
		// Its turn runs in Codex's shared daemon and outlives the terminal.
		if err := s.stopCodexTurn(r.Context(), wk.Machine, wk.Session); err != nil {
			log.Printf("stop %s: interrupt its codex turn: %v", wk.Name, err)
		}
	}
	if wk.WorkspaceID != "" {
		s.herdr.Call(r.Context(), "workspace.close", map[string]any{"workspace_id": wk.WorkspaceID}, nil)
	}
	sp.mu.Lock()
	wk.Ended, wk.Status = true, "gone"
	for _, th := range sp.st.Threads {
		th.Subscribers = remove(th.Subscribers, wk.Name)
	}
	sp.addEntryLocked("event", wk.Thread, wk.Name+" 已停止", wk.Name, "info")
	sp.mu.Unlock()
	writeJSON(w, 200, map[string]any{"ok": true})
}

func (s *Server) superShow(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if out, ok := s.sup.Show(id); ok {
		if strings.HasPrefix(out, "thread ") {
			if older := threadHistory(s.sup.db, id, 1<<62, 40); len(older) > 15 {
				out += fmt.Sprintf("  … %d older triggers in history (SQLite)\n", len(older)-15)
			}
		}
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		io.WriteString(w, out)
		return
	}
	http.Error(w, "nothing found for "+id+" (ids look like t12, a thread like chat:main, or a worker name)", 404)
}

func (s *Server) superList(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	io.WriteString(w, s.sup.List())
}

func (s *Server) superTriggers(w http.ResponseWriter, r *http.Request) {
	sp := s.sup
	sp.mu.Lock()
	defer sp.mu.Unlock()
	ts := sp.st.Triggers
	if len(ts) > 120 {
		ts = ts[len(ts)-120:]
	}
	out := make([]*Trigger, 0, len(ts))
	for i := len(ts) - 1; i >= 0; i-- {
		cp := *ts[i]
		cp.Text = clip(cp.Text, 600)
		out = append(out, &cp)
	}
	ths := []*Thread{}
	for _, th := range sp.st.Threads {
		ths = append(ths, th)
	}
	sort.Slice(ths, func(i, j int) bool { return ths[i].LastAt.After(ths[j].LastAt) })
	if len(ths) > 40 {
		ths = ths[:40]
	}
	writeJSON(w, 200, map[string]any{"triggers": out, "threads": ths})
}

// ---------------------------------------------------------------- timers

func (s *Server) superTimers(w http.ResponseWriter, r *http.Request) {
	sp := s.sup
	sp.mu.Lock()
	defer sp.mu.Unlock()
	writeJSON(w, 200, map[string]any{"timers": sortedTimers(sp.st.Timers)})
}

func sortedTimers(ts []*Timer) []*Timer {
	out := slices.Clone(ts)
	endAt := func(t *Timer) time.Time {
		if t.LastRun != nil && !t.LastRun.IsZero() {
			return *t.LastRun
		}
		return t.NextRun
	}
	sort.SliceStable(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if a.Enabled != b.Enabled {
			return a.Enabled
		}
		if a.Enabled {
			if a.NextRun.IsZero() != b.NextRun.IsZero() {
				return !a.NextRun.IsZero()
			}
			if !a.NextRun.Equal(b.NextRun) {
				return a.NextRun.Before(b.NextRun)
			}
			return idNum(a.ID) < idNum(b.ID)
		}
		ta, tb := endAt(a), endAt(b)
		if ta.IsZero() != tb.IsZero() {
			return !ta.IsZero()
		}
		if !ta.Equal(tb) {
			return ta.After(tb)
		}
		return idNum(a.ID) > idNum(b.ID)
	})
	return out
}

func (s *Server) superAddTimer(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Spec   string `json:"spec"`
		Text   string `json:"text"`
		Thread string `json:"thread"`
		Target string `json:"target"`
		Owner  string `json:"owner"`
	}
	if !decode(w, r, &body) {
		return
	}
	next, err := parseSpec(body.Spec, time.Now())
	if err != nil || strings.TrimSpace(body.Text) == "" {
		msg := "text is required"
		if err != nil {
			msg = err.Error()
		}
		writeJSON(w, 400, map[string]any{"error": msg})
		return
	}
	if body.Thread == "" {
		body.Thread = mainThread
	}
	sp := s.sup
	sp.mu.Lock()
	t := &Timer{ID: sp.st.nextID("m"), Spec: strings.TrimSpace(body.Spec), Thread: body.Thread, Target: body.Target,
		Text: body.Text, NextRun: next, Enabled: true, Owner: body.Owner}
	sp.st.Timers = append(sp.st.Timers, t)
	sp.changed()
	sp.mu.Unlock()
	writeJSON(w, 200, t)
}

func (s *Server) superDeleteTimer(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	sp := s.sup
	sp.mu.Lock()
	defer sp.mu.Unlock()
	for i, t := range sp.st.Timers {
		if t.ID == id {
			sp.st.Timers = append(sp.st.Timers[:i], sp.st.Timers[i+1:]...)
			sp.deletedTimers = append(sp.deletedTimers, id)
			sp.changed()
			writeJSON(w, 200, map[string]any{"ok": true})
			return
		}
	}
	writeJSON(w, 404, map[string]any{"error": "no timer " + id})
}

// ---------------------------------------------------------------- settings

func (s *Server) hookKey() string {
	h := sha256.Sum256([]byte("collie-hooks:" + s.token))
	return hex.EncodeToString(h[:12])
}

func (s *Server) superSettings(w http.ResponseWriter, r *http.Request) {
	sp := s.sup
	sp.mu.Lock()
	defer sp.mu.Unlock()
	writeJSON(w, 200, map[string]any{
		"settings": sp.st.Settings, "policies": sp.st.Policies,
		"hook_key": s.hookKey(), "router_dir": sp.routerDir(), "bin": sp.bin,
	})
}

// superSeen: a page of Collie is open in front of you. Its origin goes in
// the notifications' link.
func (s *Server) superSeen(w http.ResponseWriter, r *http.Request) {
	var body struct {
		URL string `json:"url"`
	}
	if !decode(w, r, &body) {
		return
	}
	if !strings.HasPrefix(body.URL, "http://") && !strings.HasPrefix(body.URL, "https://") || len(body.URL) > 200 {
		body.URL = ""
	}
	s.sup.Seen(body.URL)
	writeJSON(w, 200, map[string]any{"ok": true})
}

// superEarlier is Collie's chat before a given entry, a page at a time,
// oldest first: what the page shows when you scroll back past what it has.
// Optional `until=<entryID>` jumps straight down to a target message (plus a
// small context cushion before it) in a single round-trip.
func (s *Server) superEarlier(w http.ResponseWriter, r *http.Request) {
	before := idNum(r.URL.Query().Get("before"))
	if before <= 0 {
		writeJSON(w, 400, map[string]any{"error": "before?"})
		return
	}
	sp := s.sup
	sp.flush() // what memory has but the DB not yet
	const page = 120
	var es []*Entry
	var more bool
	if until := idNum(r.URL.Query().Get("until")); until > 0 && until < before {
		es, more = entryHistoryUntil(sp.db, mainThread, before, until, 24, 10000)
	} else {
		es = entryHistory(sp.db, mainThread, before, page+1)
		more = len(es) > page
		if more {
			es = es[:page]
		}
	}
	if es == nil {
		es = []*Entry{}
	}
	for i, j := 0, len(es)-1; i < j; i, j = i+1, j-1 {
		es[i], es[j] = es[j], es[i]
	}
	writeJSON(w, 200, map[string]any{"entries": es, "more": more})
}

// superSearch searches full chat:main history for user/collie messages.
func (s *Server) superSearch(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query().Get("q")
	thread := r.URL.Query().Get("thread")
	limit := defaultSearchLimit
	if raw := strings.TrimSpace(r.URL.Query().Get("limit")); raw != "" {
		if n, err := strconv.Atoi(raw); err == nil && n > 0 {
			limit = n
		}
	}
	writeJSON(w, 200, s.sup.Search(thread, q, limit))
}

// superLark says whom the hub's lark-cli would write to.
func (s *Server) superLark(w http.ResponseWriter, r *http.Request) {
	_, _, who, problem := lark.get(r.URL.Query().Has("fresh"))
	writeJSON(w, 200, map[string]any{"ready": problem == "", "who": who, "problem": problem})
}

func (s *Server) superSaveSettings(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Settings *Settings `json:"settings"`
		Policies []Policy  `json:"policies"`
	}
	if !decode(w, r, &body) {
		return
	}
	sp := s.sup
	sp.mu.Lock()
	defer sp.mu.Unlock()
	if body.Settings != nil {
		prev := sp.st.Settings
		ns := *body.Settings
		if _, ok := kinds[ns.RouterKind]; !ok || kinds[ns.RouterKind] == "doubao" || kinds[ns.RouterKind] == "cursor" {
			ns.RouterKind = sp.st.Settings.RouterKind
		}
		if _, ok := kinds[ns.WorkerKind]; !ok {
			ns.WorkerKind = sp.st.Settings.WorkerKind
		}
		if ns.NotifyLevel != "normal" {
			ns.NotifyLevel = "quiet"
		}
		for _, p := range []*string{&ns.RouterModel, &ns.RouterEffort, &ns.WorkerModel, &ns.WorkerEffort} {
			if *p = strings.TrimSpace(*p); !modelNameRe.MatchString(*p) {
				*p = ""
			}
		}
		ns.RouterEffort = effectiveEffort(ns.RouterKind, ns.RouterModel, ns.RouterEffort)
		ns.WorkerEffort = effectiveEffort(ns.WorkerKind, ns.WorkerModel, ns.WorkerEffort)
		ns.migrateReply()
		if ns.DreamAt != "off" {
			var h, m int
			if _, err := fmt.Sscanf(ns.DreamAt, "%d:%d", &h, &m); err != nil || h < 0 || h > 23 || m < 0 || m > 59 {
				ns.DreamAt = ""
			} else {
				ns.DreamAt = fmt.Sprintf("%02d:%02d", h, m)
			}
		}
		if ns.RotatePct > 95 || (ns.RotatePct > 0 && ns.RotatePct < 20) {
			ns.RotatePct = 0
		} else if ns.RotatePct < 0 {
			ns.RotatePct = -1
		}
		ns.WorkerMachine = s.machineByName(ns.WorkerMachine)
		if ns.WorkerMachine == "" {
			// Paths on other computers are resolved there, against their home.
			ns.WorkerCwd = s.expand(ns.WorkerCwd)
		} else if ns.WorkerCwd == "" {
			ns.WorkerCwd = "~"
		}
		// If a pre-upgrade router is already running without a saved router_launch
		// snapshot, remember the settings it was running with so changes to
		// router_args or ask_permissions also surface router.pending accurately.
		if sp.routerLaunchLocked() == nil && !routerLaunchEqual(routerLaunchOf(prev), routerLaunchOf(ns)) {
			prevLaunch := routerLaunchOf(prev)
			if run := sp.routerRunLocked(); run != nil {
				prevLaunch.Model, prevLaunch.Effort = run[0], run[1]
			}
			sp.setRouterLaunch(prevLaunch)
		}
		sp.st.Settings = ns
	}
	if body.Policies != nil {
		valid := []Policy{}
		for _, p := range body.Policies {
			if p.Source != "" && (p.Action == "respond" || p.Action == "candidate" || p.Action == "ignore") {
				valid = append(valid, p)
			}
		}
		sp.st.Policies = valid
	}
	sp.changed()
	if body.Settings != nil {
		go func() { // ROUTER.md quotes some settings (reply length, where workers go)
			if err := sp.writeRouterFiles(); err != nil {
				log.Printf("router files: %v", err)
			}
		}()
	}
	writeJSON(w, 200, map[string]any{"settings": sp.st.Settings, "policies": sp.st.Policies})
}

func (s *Server) superRestartRouter(w http.ResponseWriter, r *http.Request) {
	s.sup.RestartRouter(r.Context())
	writeJSON(w, 202, map[string]any{"ok": true})
}

// ---------------------------------------------------------------- integrations

func (s *Server) superIntegrations(w http.ResponseWriter, r *http.Request) {
	sp := s.sup
	var list []IntegrationView
	var sum IntegrationsSummary
	if sp.integrations != nil {
		list = sp.integrations.ListViews()
		sum = sp.integrations.Summary()
	} else {
		list = []IntegrationView{}
	}
	sp.mu.Lock()
	st := sp.st.Settings
	sp.mu.Unlock()
	writeJSON(w, 200, map[string]any{
		"integrations": list,
		"summary":      sum,
		"hook_key":     s.hookKey(),
		"notify": map[string]any{
			"notify_webhook": st.NotifyWebhook,
			"notify_lark":    st.NotifyLark,
			"notify_level":   st.NotifyLevel,
		},
	})
}

func (s *Server) superSaveIntegration(w http.ResponseWriter, r *http.Request) {
	if s.sup.integrations == nil {
		writeJSON(w, 503, map[string]any{"error": "integrations manager not ready"})
		return
	}
	kind := strings.ToLower(strings.TrimSpace(r.PathValue("kind")))
	var req SaveRequest
	if !decode(w, r, &req) {
		return
	}
	view, err := s.sup.integrations.Save(kind, req)
	if err != nil {
		writeJSON(w, 400, map[string]any{"error": err.Error()})
		return
	}
	writeJSON(w, 200, map[string]any{
		"integration": view,
		"summary":     s.sup.integrations.Summary(),
	})
}

func (s *Server) superTestIntegration(w http.ResponseWriter, r *http.Request) {
	if s.sup.integrations == nil {
		writeJSON(w, 503, map[string]any{"error": "integrations manager not ready"})
		return
	}
	kind := strings.ToLower(strings.TrimSpace(r.PathValue("kind")))
	var req SaveRequest
	// Allow empty body or partial JSON overlay.
	raw, _ := io.ReadAll(http.MaxBytesReader(w, r.Body, 1<<20))
	if len(strings.TrimSpace(string(raw))) > 0 {
		if err := json.Unmarshal(raw, &req); err != nil {
			writeJSON(w, 400, map[string]any{"error": "invalid JSON body"})
			return
		}
	}
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	res, err := s.sup.integrations.Test(ctx, kind, req)
	if err != nil {
		writeJSON(w, 404, map[string]any{"error": err.Error()})
		return
	}
	writeJSON(w, 200, res)
}

func (s *Server) superSampleIntegration(w http.ResponseWriter, r *http.Request) {
	if s.sup.integrations == nil {
		writeJSON(w, 503, map[string]any{"error": "integrations manager not ready"})
		return
	}
	kind := strings.ToLower(strings.TrimSpace(r.PathValue("kind")))
	var req SaveRequest
	raw, _ := io.ReadAll(http.MaxBytesReader(w, r.Body, 1<<20))
	if len(strings.TrimSpace(string(raw))) > 0 {
		if err := json.Unmarshal(raw, &req); err != nil {
			writeJSON(w, 400, map[string]any{"error": "invalid JSON body"})
			return
		}
	}
	limit := forgeDefaultSampleLimit
	if q := strings.TrimSpace(r.URL.Query().Get("limit")); q != "" {
		if n, err := strconv.Atoi(q); err == nil && n > 0 && n <= 10 {
			limit = n
		}
	}
	ctx, cancel := context.WithTimeout(r.Context(), 25*time.Second)
	defer cancel()
	samples, err := s.sup.integrations.SamplePull(ctx, kind, req, limit)
	if err != nil {
		writeJSON(w, 200, map[string]any{"ok": false, "error": cleanAuthError(err), "samples": []SampleEvent{}})
		return
	}
	if samples == nil {
		samples = []SampleEvent{}
	}
	writeJSON(w, 200, map[string]any{"ok": true, "samples": samples})
}

func (s *Server) superProbeIntegration(w http.ResponseWriter, r *http.Request) {
	kind := strings.ToLower(strings.TrimSpace(r.PathValue("kind")))
	switch kind {
	case feishuKind:
		_, openID, who, problem := lark.get(true)
		writeJSON(w, 200, map[string]any{
			"ready":   problem == "" && openID != "",
			"open_id": openID,
			"who":     who,
			"problem": problem,
		})
	default:
		if s.sup.integrations == nil {
			writeJSON(w, 503, map[string]any{"error": "integrations manager not ready"})
			return
		}
		var req SaveRequest
		if r.Body != nil {
			raw, _ := io.ReadAll(http.MaxBytesReader(w, r.Body, 1<<20))
			if len(strings.TrimSpace(string(raw))) > 0 {
				_ = json.Unmarshal(raw, &req)
			}
		}
		ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
		defer cancel()
		res, err := s.sup.integrations.Probe(ctx, kind, req)
		if err != nil {
			writeJSON(w, 404, map[string]any{"error": err.Error()})
			return
		}
		writeJSON(w, 200, res)
	}
}

func (s *Server) superFeishuPair(w http.ResponseWriter, r *http.Request) {
	if s.sup.feishu == nil {
		writeJSON(w, 503, map[string]any{"error": "feishu integration not available"})
		return
	}
	var body struct {
		Cancel bool `json:"cancel"`
	}
	raw, _ := io.ReadAll(http.MaxBytesReader(w, r.Body, 4096))
	if len(strings.TrimSpace(string(raw))) > 0 {
		_ = json.Unmarshal(raw, &body)
	}
	if body.Cancel {
		s.sup.feishu.StopPairing()
		writeJSON(w, 200, map[string]any{"ok": true, "active": false})
		return
	}
	exp, err := s.sup.feishu.StartPairing(feishuDefaultPairWindow)
	if err != nil {
		writeJSON(w, 400, map[string]any{"error": err.Error()})
		return
	}
	writeJSON(w, 200, map[string]any{
		"ok":         true,
		"active":     true,
		"expires_at": exp,
		"message":    "请在 2 分钟内用飞书给机器人发任意一条私信，系统将自动锁定你的 OpenID。",
	})
}

func (s *Server) superFeishuOnboardStart(w http.ResponseWriter, r *http.Request) {
	if s.sup == nil || s.sup.feishuOnboard == nil {
		writeJSON(w, 503, map[string]any{"error": "feishu onboard not available"})
		return
	}
	var req FeishuOnboardStartRequest
	raw, _ := io.ReadAll(http.MaxBytesReader(w, r.Body, 8192))
	if len(strings.TrimSpace(string(raw))) > 0 {
		if err := json.Unmarshal(raw, &req); err != nil {
			writeJSON(w, 400, map[string]any{"error": "invalid JSON body"})
			return
		}
	}
	view, err := s.sup.feishuOnboard.Start(r.Context(), req)
	if err != nil {
		var bad *feishuOnboardBadRequestError
		if errors.As(err, &bad) {
			writeJSON(w, 400, map[string]any{"error": bad.Error()})
			return
		}
		writeJSON(w, 502, map[string]any{"error": err.Error()})
		return
	}
	writeJSON(w, 200, view)
}

func (s *Server) superFeishuOnboardStatus(w http.ResponseWriter, r *http.Request) {
	if s.sup == nil || s.sup.feishuOnboard == nil {
		writeJSON(w, 503, map[string]any{"error": "feishu onboard not available"})
		return
	}
	writeJSON(w, 200, s.sup.feishuOnboard.Status())
}

func (s *Server) superFeishuOnboardCancel(w http.ResponseWriter, r *http.Request) {
	if s.sup == nil || s.sup.feishuOnboard == nil {
		writeJSON(w, 503, map[string]any{"error": "feishu onboard not available"})
		return
	}
	s.sup.feishuOnboard.Cancel()
	writeJSON(w, 200, map[string]any{"ok": true, "state": feishuOnboardStateIdle})
}

func (s *Server) superFeishuReply(w http.ResponseWriter, r *http.Request) {
	if s.sup == nil || s.sup.feishu == nil {
		writeJSON(w, 503, map[string]any{"error": "feishu integration not available"})
		return
	}
	var body struct {
		ChatID           string `json:"chat_id"`
		ReplyToMessageID string `json:"reply_to_message_id"`
		Text             string `json:"text"`
		Confirm          bool   `json:"confirm"`
	}
	if !decode(w, r, &body) {
		return
	}
	if !body.Confirm {
		writeJSON(w, 400, map[string]any{"error": "必须先获主人明确确认并传入 confirm: true 才能向飞书群回复"})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	receipt, err := s.sup.feishu.ReplyFeedbackGroup(ctx, body.ChatID, body.ReplyToMessageID, body.Text)
	if err != nil {
		msg := err.Error()
		if s.sup.integrations != nil {
			msg = s.sup.integrations.redact(msg)
		}
		status := http.StatusBadRequest
		if strings.Contains(msg, "feishu feedback reply failed") {
			status = http.StatusBadGateway
		}
		writeJSON(w, status, map[string]any{"error": msg})
		return
	}
	writeJSON(w, 200, map[string]any{
		"ok":                  true,
		"kind":                feishuKind,
		"chat_id":             receipt.ChatID,
		"reply_to_message_id": strings.TrimSpace(body.ReplyToMessageID),
		"message_id":          receipt.ExternalID,
	})
}

func (s *Server) superForgeComment(w http.ResponseWriter, r *http.Request) {
	if s.sup == nil || s.sup.integrations == nil {
		writeJSON(w, 503, map[string]any{"error": "integrations manager not ready"})
		return
	}
	kind := strings.ToLower(strings.TrimSpace(r.PathValue("kind")))
	var req struct {
		Thread  string `json:"thread"`
		Repo    string `json:"repo"`
		Number  int    `json:"number"`
		Kind    string `json:"kind"`
		Body    string `json:"body"`
		Text    string `json:"text"`
		Confirm bool   `json:"confirm"`
	}
	if !decode(w, r, &req) {
		return
	}
	if !req.Confirm {
		writeJSON(w, 400, map[string]any{"error": "必须先获主人明确确认并传入 confirm: true 才能在远端发表评论"})
		return
	}
	repo := strings.TrimSpace(req.Repo)
	number := req.Number
	if strings.TrimSpace(req.Thread) != "" {
		prov, trRepo, trNum, ok := parseForgeThread(req.Thread)
		if !ok {
			writeJSON(w, 400, map[string]any{"error": "无效的 thread 格式，应为 <provider>:<owner>/<repo>#<number>"})
			return
		}
		if prov != kind {
			writeJSON(w, 400, map[string]any{"error": fmt.Sprintf("thread 所属平台 %q 与目标接入 %q 不一致", prov, kind)})
			return
		}
		if repo != "" && normalizeForgeRepo(repo) != trRepo {
			writeJSON(w, 400, map[string]any{"error": "repo 与 thread 中的仓库不一致"})
			return
		}
		if number > 0 && number != trNum {
			writeJSON(w, 400, map[string]any{"error": "number 与 thread 中的编号不一致"})
			return
		}
		repo = trRepo
		number = trNum
	}
	commentBody := strings.TrimSpace(req.Body)
	if commentBody == "" {
		commentBody = strings.TrimSpace(req.Text)
	}
	if commentBody == "" {
		writeJSON(w, 400, map[string]any{"error": "评论内容不能为空"})
		return
	}

	adapter := s.sup.integrations.Adapter(kind)
	if adapter == nil {
		writeJSON(w, 404, map[string]any{"error": fmt.Sprintf("未找到代码托管接入：%s", kind)})
		return
	}
	ga, ok := adapter.(*GitForgeAdapter)
	if !ok {
		writeJSON(w, 400, map[string]any{"error": fmt.Sprintf("接入 %s 不支持发表代码托管评论", kind)})
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	res, err := ga.PostComment(ctx, ForgeCommentTarget{
		Repo:   repo,
		Number: number,
		Kind:   strings.TrimSpace(req.Kind),
	}, commentBody)
	if err != nil {
		writeJSON(w, 400, map[string]any{"error": err.Error()})
		return
	}
	writeJSON(w, 200, map[string]any{
		"ok":         true,
		"provider":   res.Provider,
		"repo":       res.Repo,
		"number":     res.Number,
		"comment_id": res.CommentID,
		"url":        res.URL,
	})
}

func (s *Server) superTestNotify(w http.ResponseWriter, r *http.Request) {
	var body struct {
		URL string `json:"url"`
	}
	raw, _ := io.ReadAll(http.MaxBytesReader(w, r.Body, 16384))
	if len(strings.TrimSpace(string(raw))) > 0 {
		_ = json.Unmarshal(raw, &body)
	}
	targetURL := strings.TrimSpace(body.URL)
	if targetURL == "" {
		s.sup.mu.Lock()
		targetURL = strings.TrimSpace(s.sup.st.Settings.NotifyWebhook)
		s.sup.mu.Unlock()
	}
	if !strings.HasPrefix(targetURL, "http://") && !strings.HasPrefix(targetURL, "https://") {
		writeJSON(w, 400, map[string]any{"error": "请填写以 http:// 或 https:// 开头的推送地址"})
		return
	}
	msg := "Collie · 测试提醒：通用单向推送通路正常。"
	payload, _ := json.Marshal(map[string]any{
		"msg_type": "text", "content": map[string]string{"text": msg},
		"text": msg,
	})
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, targetURL, strings.NewReader(string(payload)))
	if err != nil {
		writeJSON(w, 400, map[string]any{"error": "推送地址格式无效"})
		return
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		writeJSON(w, 502, map[string]any{"error": "发送失败：" + err.Error()})
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		writeJSON(w, 502, map[string]any{"error": fmt.Sprintf("目标地址返回 HTTP %d", resp.StatusCode)})
		return
	}
	writeJSON(w, 200, map[string]any{"ok": true, "message": fmt.Sprintf("测试提醒已发送（HTTP %d）", resp.StatusCode)})
}

// ---------------------------------------------------------------- webhooks

// webhook ingests external events (GitHub, Feishu/Lark, anything that can
// POST JSON). It acknowledges immediately; filtering and routing happen in
// the inbox, never in the request path.
func (s *Server) webhook(w http.ResponseWriter, r *http.Request) {
	key := r.URL.Query().Get("key")
	want := s.hookKey()
	if subtle.ConstantTimeCompare([]byte(key), []byte(want)) != 1 && !s.authed(r) {
		writeJSON(w, 401, map[string]any{"error": "bad hook key"})
		return
	}
	src := strings.ToLower(r.PathValue("source"))
	if !nameRe.MatchString(src) {
		writeJSON(w, 400, map[string]any{"error": "bad source name"})
		return
	}
	raw, _ := io.ReadAll(http.MaxBytesReader(w, r.Body, 2<<20))
	var payload map[string]any
	json.Unmarshal(raw, &payload)

	// Feishu/Lark URL verification handshake.
	if payload["type"] == "url_verification" {
		writeJSON(w, 200, map[string]any{"challenge": payload["challenge"]})
		return
	}

	thread, from, text, idem := genericEvent(src, payload, raw, r)
	switch {
	case r.Header.Get("X-GitHub-Event") != "":
		thread, from, text = githubEvent(r.Header.Get("X-GitHub-Event"), payload)
		idem = r.Header.Get("X-GitHub-Delivery")
	case payload["header"] != nil && payload["event"] != nil:
		thread, from, text, idem = feishuEvent(payload)
	}
	if text == "" {
		writeJSON(w, 202, map[string]any{"ok": true, "ignored": "empty event"})
		return
	}
	t := s.sup.Ingest("webhook:"+src, thread, from, text, idem, "", "")
	res := map[string]any{"ok": true}
	if t != nil {
		res["trigger"], res["action"] = t.ID, t.Action
	} else {
		res["duplicate"] = true
	}
	writeJSON(w, 202, res)
}

func str(v any) string {
	switch x := v.(type) {
	case string:
		return x
	case float64:
		return fmt.Sprintf("%.0f", x)
	}
	return ""
}

func dig(m map[string]any, path ...string) any {
	var cur any = m
	for _, p := range path {
		mm, ok := cur.(map[string]any)
		if !ok {
			return nil
		}
		cur = mm[p]
	}
	return cur
}

func genericEvent(src string, p map[string]any, raw []byte, r *http.Request) (thread, from, text, idem string) {
	thread = r.Header.Get("X-Collie-Thread")
	if thread == "" {
		thread = str(p["thread"])
	}
	if thread == "" {
		thread = src
	}
	if !strings.Contains(thread, ":") {
		thread = src + ":" + thread
	}
	from = str(p["from"])
	text = str(p["text"])
	if text == "" {
		text = clip(string(raw), 4000)
	}
	idem = r.Header.Get("X-Idempotency-Key")
	if idem == "" {
		idem = str(p["id"])
	}
	return
}

func githubEvent(kind string, p map[string]any) (thread, from, text string) {
	repo := str(dig(p, "repository", "full_name"))
	num := str(dig(p, "issue", "number"))
	if num == "" {
		num = str(dig(p, "pull_request", "number"))
	}
	if num == "" {
		num = str(p["number"])
	}
	thread = "github:" + repo
	if num != "" {
		thread += "#" + num
	}
	from = str(dig(p, "sender", "login"))
	title := str(dig(p, "issue", "title"))
	if title == "" {
		title = str(dig(p, "pull_request", "title"))
	}
	body := str(dig(p, "comment", "body"))
	if body == "" {
		body = str(dig(p, "review", "body"))
	}
	if body == "" {
		body = str(dig(p, "issue", "body"))
	}
	if body == "" {
		body = str(dig(p, "pull_request", "body"))
	}
	url := str(dig(p, "comment", "html_url"))
	if url == "" {
		url = str(dig(p, "issue", "html_url"))
	}
	if url == "" {
		url = str(dig(p, "pull_request", "html_url"))
	}
	if kind == "check_run" || kind == "workflow_run" || kind == "check_suite" {
		name := str(dig(p, kind, "name"))
		concl := str(dig(p, kind, "conclusion"))
		branch := str(dig(p, kind, "head_branch"))
		url = str(dig(p, kind, "html_url"))
		if concl == "" {
			return thread, from, ""
		}
		return thread, from, fmt.Sprintf("GitHub %s %q on %s: %s\n%s", kind, name, branch, concl, url)
	}
	text = fmt.Sprintf("GitHub %s/%s on %s: %s\n%s\n%s", kind, str(p["action"]), thread, title, clip(body, 2500), url)
	return thread, from, strings.TrimSpace(text)
}

func feishuEvent(p map[string]any) (thread, from, text, idem string) {
	idem = str(dig(p, "header", "event_id"))
	msg, _ := dig(p, "event", "message").(map[string]any)
	if msg == nil {
		return "feishu:events", "", clip(fmt.Sprint(p["event"]), 2000), idem
	}
	chat := str(msg["chat_id"])
	root := str(msg["root_id"])
	if root == "" {
		root = str(msg["message_id"])
	}
	thread = "feishu:" + chat + ":" + root
	from = str(dig(p, "event", "sender", "sender_id", "open_id"))
	var content struct {
		Text string `json:"text"`
	}
	json.Unmarshal([]byte(str(msg["content"])), &content)
	text = content.Text
	if text == "" {
		text = "[" + str(msg["message_type"]) + " message]"
	}
	return thread, from, "Feishu message in " + chat + ":\n" + text, idem
}

// machineByName accepts a machine id or its display name ("" or "hub" = the
// hub's own machine).
func (s *Server) machineByName(n string) string {
	n = strings.TrimSpace(n)
	if n == "" || n == "hub" || n == "local" {
		return ""
	}
	for _, m := range s.herdr.Machines() {
		if m.ID == n || strings.EqualFold(m.Name, n) {
			return m.ID
		}
	}
	return n
}

// modelNameRe: model ids and effort names go onto a command line as one argument.
var modelNameRe = regexp.MustCompile(`^[A-Za-z0-9._:/@\[\]=,-]{0,120}$`)

// superAnswer: the router answers a dialog a worker is stuck on, by choice
// number, label words or "esc". The human sees what it picked.
func (s *Server) superAnswer(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Worker string `json:"worker"`
		Choice string `json:"choice"`
	}
	if !decode(w, r, &body) {
		return
	}
	sp := s.sup
	sp.mu.Lock()
	wk := sp.st.Workers[body.Worker]
	sp.mu.Unlock()
	if wk == nil || wk.Ended || wk.PaneID == "" {
		writeJSON(w, 404, map[string]any{"error": "no running worker " + body.Worker})
		return
	}
	rr, err := s.herdr.Read(r.Context(), wk.PaneID, "detection", "text", 0)
	if err != nil {
		fail(w, err)
		return
	}
	if !screenAsks(rr.Text) {
		writeJSON(w, 409, map[string]any{"error": body.Worker + " isn't showing a dialog now"})
		return
	}
	a := parseApproval(rr.Text)
	label := "Esc"
	if strings.EqualFold(strings.TrimSpace(body.Choice), "esc") {
		err = s.herdr.Call(r.Context(), "pane.send_keys", map[string]any{"pane_id": wk.PaneID, "keys": []string{"esc"}}, nil)
	} else {
		c, ok := pickChoice(a, body.Choice)
		if !ok {
			var list []string
			for i, c := range a.Choices {
				list = append(list, fmt.Sprintf("%d. %s", i+1, c.Label))
			}
			writeJSON(w, 400, map[string]any{"error": "no single choice matches " + strconv.Quote(body.Choice), "choices": list})
			return
		}
		label, err = strings.Join(strings.Fields(c.Label), " "), sp.choose(r.Context(), wk.PaneID, c)
	}
	if err != nil {
		fail(w, err)
		return
	}
	sp.mu.Lock()
	sp.noteAnsweredLocked(wk)
	sp.addEntryLocked("event", wk.Thread, "替 "+wk.Name+" 选了："+clip(label, 80), wk.Name, "info")
	sp.mu.Unlock()
	writeJSON(w, 200, map[string]any{"ok": true, "picked": label})
}
