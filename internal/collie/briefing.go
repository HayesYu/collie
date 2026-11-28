package collie

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Official optional routines: Morning Briefing, Weekly Review, and Stale Follow-up.
// All three default to disabled, persist inside Settings, and run a pure-Go
// zero-token gate before ever waking the Router.

const (
	routineMorningBrief = "morning_brief"
	routineWeeklyReview = "weekly_review"
	routineStaleFollow  = "stale_follow"

	defaultMorningBriefAt  = "09:00"
	defaultWeeklyReviewDay = "mon"
	defaultWeeklyReviewAt  = "09:00"
	defaultStaleFollowAt   = "17:30"

	routineCatchupWindow = 3 * time.Hour
	routineKVPrefix      = "routine_state_"
)

// RoutineConfig configures one official optional routine.
type RoutineConfig struct {
	Enabled bool   `json:"enabled"`
	At      string `json:"at,omitempty"`  // local "HH:MM"
	Day     string `json:"day,omitempty"` // "mon".."sun" (weekly_review only)
}

type routineState struct {
	Date    string    `json:"date"`              // local "YYYY-MM-DD"
	Outcome string    `json:"outcome,omitempty"` // "triggered" | "skipped"
	At      time.Time `json:"at,omitempty"`
}

type routineSignal struct {
	Fired   bool
	Summary []string
}

func cloneRoutineConfig(c *RoutineConfig) *RoutineConfig {
	if c == nil {
		return nil
	}
	cp := *c
	return &cp
}

func parseRoutineClock(raw string) (h, m int, ok bool) {
	raw = strings.TrimSpace(raw)
	parts := strings.Split(raw, ":")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" || len(parts[0]) > 2 || len(parts[1]) != 2 {
		return 0, 0, false
	}
	hv, err1 := strconv.Atoi(parts[0])
	mv, err2 := strconv.Atoi(parts[1])
	if err1 != nil || err2 != nil || hv < 0 || hv > 23 || mv < 0 || mv > 59 {
		return 0, 0, false
	}
	return hv, mv, true
}

func normalizeRoutineClock(raw, fallback string) string {
	if h, m, ok := parseRoutineClock(raw); ok {
		return fmt.Sprintf("%02d:%02d", h, m)
	}
	return fallback
}

func normalizeRoutineDay(raw, fallback string) string {
	v := strings.ToLower(strings.TrimSpace(raw))
	if _, ok := weekdayNames[v]; ok {
		return v
	}
	return fallback
}

// effectiveRoutines returns freshly allocated, normalized copies of all three
// official routine configs so callers and JSON encoders always see valid defaults.
func (s Settings) effectiveRoutines() (morning, weekly, stale *RoutineConfig) {
	morning = &RoutineConfig{
		Enabled: s.MorningBrief != nil && s.MorningBrief.Enabled,
		At:      defaultMorningBriefAt,
	}
	if s.MorningBrief != nil {
		morning.At = normalizeRoutineClock(s.MorningBrief.At, defaultMorningBriefAt)
	}

	weekly = &RoutineConfig{
		Enabled: s.WeeklyReview != nil && s.WeeklyReview.Enabled,
		Day:     defaultWeeklyReviewDay,
		At:      defaultWeeklyReviewAt,
	}
	if s.WeeklyReview != nil {
		weekly.Day = normalizeRoutineDay(s.WeeklyReview.Day, defaultWeeklyReviewDay)
		weekly.At = normalizeRoutineClock(s.WeeklyReview.At, defaultWeeklyReviewAt)
	}

	stale = &RoutineConfig{
		Enabled: s.StaleFollow != nil && s.StaleFollow.Enabled,
		At:      defaultStaleFollowAt,
	}
	if s.StaleFollow != nil {
		stale.At = normalizeRoutineClock(s.StaleFollow.At, defaultStaleFollowAt)
	}
	return morning, weekly, stale
}

// normalizeRoutines validates incoming routine configs and preserves previous
// values whenever a field is omitted (nil) by an older client.
func (s *Settings) normalizeRoutines(prev Settings) {
	if s.MorningBrief == nil {
		s.MorningBrief = cloneRoutineConfig(prev.MorningBrief)
	} else {
		s.MorningBrief = &RoutineConfig{
			Enabled: s.MorningBrief.Enabled,
			At:      normalizeRoutineClock(s.MorningBrief.At, defaultMorningBriefAt),
		}
	}
	if s.WeeklyReview == nil {
		s.WeeklyReview = cloneRoutineConfig(prev.WeeklyReview)
	} else {
		s.WeeklyReview = &RoutineConfig{
			Enabled: s.WeeklyReview.Enabled,
			Day:     normalizeRoutineDay(s.WeeklyReview.Day, defaultWeeklyReviewDay),
			At:      normalizeRoutineClock(s.WeeklyReview.At, defaultWeeklyReviewAt),
		}
	}
	if s.StaleFollow == nil {
		s.StaleFollow = cloneRoutineConfig(prev.StaleFollow)
	} else {
		s.StaleFollow = &RoutineConfig{
			Enabled: s.StaleFollow.Enabled,
			At:      normalizeRoutineClock(s.StaleFollow.At, defaultStaleFollowAt),
		}
	}
}

func (sp *Super) loadRoutineState(kind string) routineState {
	if sp.db == nil {
		return routineState{}
	}
	var raw string
	if sp.db.QueryRow(`SELECT v FROM kv WHERE k=?`, routineKVPrefix+kind).Scan(&raw) != nil || strings.TrimSpace(raw) == "" {
		return routineState{}
	}
	var rs routineState
	if err := json.Unmarshal([]byte(raw), &rs); err != nil {
		// Tolerate plain date string if ever stored directly.
		return routineState{Date: strings.TrimSpace(raw)}
	}
	return rs
}

func (sp *Super) saveRoutineState(kind string, rs routineState) {
	sp.mu.Lock()
	if sp.routineDates == nil {
		sp.routineDates = map[string]string{}
	}
	sp.routineDates[kind] = rs.Date
	sp.mu.Unlock()
	if sp.db == nil {
		return
	}
	b, err := json.Marshal(rs)
	if err != nil {
		return
	}
	_, _ = sp.db.Exec(`INSERT INTO kv(k,v) VALUES(?,?) ON CONFLICT(k) DO UPDATE SET v=excluded.v`, routineKVPrefix+kind, string(b))
}

func (sp *Super) routineDoneOnDate(kind, today string) (done bool, prev routineState) {
	sp.mu.Lock()
	if sp.routineDates != nil {
		if d, ok := sp.routineDates[kind]; ok && d == today {
			sp.mu.Unlock()
			return true, routineState{Date: d}
		}
	}
	sp.mu.Unlock()

	rs := sp.loadRoutineState(kind)
	sp.mu.Lock()
	if sp.routineDates == nil {
		sp.routineDates = map[string]string{}
	}
	if sp.routineDates[kind] == "" {
		sp.routineDates[kind] = rs.Date
	}
	cur := sp.routineDates[kind]
	sp.mu.Unlock()
	return cur == today, rs
}

func routineDueAt(kind string, cfg *RoutineConfig, now time.Time) bool {
	if cfg == nil || !cfg.Enabled {
		return false
	}
	switch kind {
	case routineMorningBrief:
		// Every day.
	case routineWeeklyReview:
		wd, ok := weekdayNames[normalizeRoutineDay(cfg.Day, defaultWeeklyReviewDay)]
		if !ok || now.Weekday() != wd {
			return false
		}
	case routineStaleFollow:
		wd := now.Weekday()
		if wd == time.Saturday || wd == time.Sunday {
			return false
		}
	default:
		return false
	}
	h, m, ok := parseRoutineClock(cfg.At)
	if !ok {
		return false
	}
	sched := time.Date(now.Year(), now.Month(), now.Day(), h, m, 0, 0, now.Location())
	return !now.Before(sched) && now.Before(sched.Add(routineCatchupWindow))
}

// checkRoutines runs on every Super tick: fast no-op unless an enabled routine
// enters its daily/weekly local-time window and has not yet been evaluated today.
func (sp *Super) checkRoutines(ctx context.Context, live map[string]liveAgent, queued bool) {
	sp.checkRoutinesAt(ctx, time.Now(), live, queued)
}

func (sp *Super) checkRoutinesAt(_ context.Context, now time.Time, _ map[string]liveAgent, _ bool) {
	sp.mu.Lock()
	if sp.dream != nil || sp.handover != nil || sp.hubUpdating || sp.st.Settings.Paused {
		sp.mu.Unlock()
		return
	}
	morningCfg, weeklyCfg, staleCfg := sp.st.Settings.effectiveRoutines()
	sp.mu.Unlock()

	type item struct {
		kind string
		cfg  *RoutineConfig
	}
	items := []item{
		{routineMorningBrief, morningCfg},
		{routineWeeklyReview, weeklyCfg},
		{routineStaleFollow, staleCfg},
	}

	today := now.Format("2006-01-02")
	for _, it := range items {
		if !routineDueAt(it.kind, it.cfg, now) {
			continue
		}
		done, prev := sp.routineDoneOnDate(it.kind, today)
		if done {
			continue
		}

		var sig routineSignal
		switch it.kind {
		case routineMorningBrief:
			var prevTriggered time.Time
			if prev.Outcome == "triggered" {
				prevTriggered = prev.At
			}
			sig = sp.evalMorningBriefGate(now, prevTriggered)
		case routineWeeklyReview:
			sig = sp.evalWeeklyReviewGate(now)
		case routineStaleFollow:
			sig = sp.evalStaleFollowGate(now)
		}

		if !sig.Fired {
			sp.saveRoutineState(it.kind, routineState{Date: today, Outcome: "skipped", At: now})
			continue
		}

		prompt := buildRoutinePrompt(it.kind, now, sig.Summary)
		sp.mu.Lock()
		if sp.routineDates != nil && sp.routineDates[it.kind] == today {
			sp.mu.Unlock()
			continue
		}
		if sp.routineDates == nil {
			sp.routineDates = map[string]string{}
		}
		sp.routineDates[it.kind] = today
		sp.ingestLocked("system", mainThread, "routine:"+it.kind, prompt, "routine:"+it.kind+":"+today, "", "routine:"+it.kind)
		sp.mu.Unlock()
		sp.saveRoutineState(it.kind, routineState{Date: today, Outcome: "triggered", At: now})
	}
}

// ---------------------------------------------------------------- pure-Go zero-token gates

type datedFile struct {
	Name string
	Mod  time.Time
}

func listNonEmptyMarkdown(dir string) []datedFile {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var out []datedFile
	for _, de := range entries {
		if de.IsDir() {
			continue
		}
		name := de.Name()
		if strings.HasPrefix(name, ".") || !strings.HasSuffix(name, ".md") {
			continue
		}
		p := filepath.Join(dir, name)
		fi, err := de.Info()
		if err != nil || fi.Size() == 0 {
			continue
		}
		b, err := os.ReadFile(p)
		if err != nil || strings.TrimSpace(string(b)) == "" {
			continue
		}
		out = append(out, datedFile{Name: name, Mod: fi.ModTime()})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

func parseDreamJournalTime(name string, fallback time.Time, loc *time.Location) time.Time {
	base := strings.TrimSuffix(name, ".md")
	if t, err := time.ParseInLocation("2006-01-02-1504", base, loc); err == nil {
		return t
	}
	if t, err := time.ParseInLocation("2006-01-02", base, loc); err == nil {
		return t
	}
	return fallback
}

var dreamNegationTokens = []string{
	"无异常", "没有异常", "未发现异常", "未发现环境异常", "无环境异常",
	"无待关注", "无告警", "无风险", "无报错", "无失败", "无需处理",
	"一切正常", "均正常", "0 error", "no anomalies", "no anomaly",
	"no issues", "no warnings", "nothing needs attention",
}

var dreamAttentionTokens = []string{
	"待关注", "环境异常", "需关注", "需处理", "待处理", "待拍板", "待确认",
	"告警", "告急", "空间不足", "磁盘", "占盘", "异常", "警告", "失败",
	"报错", "超时", "泄漏", "孤儿", "遗留", "风险",
	"warning", "alert", "anomaly", "needs attention", "action required",
	"disk full", "failed", "error", "todo", "fixme", "⚠",
}

func dreamAttentionLines(content string) []string {
	var hits []string
	for _, raw := range strings.Split(content, "\n") {
		l := strings.TrimSpace(strings.TrimLeft(strings.TrimSpace(raw), ">-*• "))
		if l == "" || strings.HasPrefix(l, "#") {
			continue
		}
		if strings.HasPrefix(l, dreamSkillsPrefix) || strings.HasPrefix(l, dreamFixPrefix) {
			hits = append(hits, clip(oneLine(l), 140))
			if len(hits) >= 3 {
				break
			}
			continue
		}
		low := strings.ToLower(l)
		negated := false
		for _, neg := range dreamNegationTokens {
			if strings.Contains(low, neg) {
				negated = true
				break
			}
		}
		if negated {
			continue
		}
		matched := false
		for _, tok := range dreamAttentionTokens {
			if strings.Contains(low, tok) {
				matched = true
				break
			}
		}
		if matched {
			hits = append(hits, clip(oneLine(l), 140))
			if len(hits) >= 3 {
				break
			}
		}
	}
	return hits
}

func (sp *Super) latestDreamAttention(since time.Time, loc *time.Location) (string, []string) {
	dreamsDir := filepath.Join(sp.routerDir(), "dreams")
	entries, err := os.ReadDir(dreamsDir)
	if err != nil {
		return "", nil
	}
	type cand struct {
		name string
		path string
		at   time.Time
	}
	var newest *cand
	for _, de := range entries {
		if de.IsDir() || strings.HasPrefix(de.Name(), ".") || !strings.HasSuffix(de.Name(), ".md") {
			continue
		}
		fi, err := de.Info()
		if err != nil || fi.Size() == 0 {
			continue
		}
		at := parseDreamJournalTime(de.Name(), fi.ModTime(), loc)
		if newest == nil || at.After(newest.at) || (at.Equal(newest.at) && de.Name() > newest.name) {
			newest = &cand{name: de.Name(), path: filepath.Join(dreamsDir, de.Name()), at: at}
		}
	}
	if newest == nil || newest.at.Before(since) {
		return "", nil
	}
	b, err := os.ReadFile(newest.path)
	if err != nil {
		return "", nil
	}
	hits := dreamAttentionLines(string(b))
	if len(hits) == 0 {
		return "", nil
	}
	return newest.name, hits
}

func joinClipNames(names []string, limit int) string {
	if len(names) <= limit {
		return strings.Join(names, "、")
	}
	return fmt.Sprintf("%s 等 %d 项", strings.Join(names[:limit], "、"), len(names))
}

func formatAgeZH(d time.Duration) string {
	if d < time.Hour {
		return fmt.Sprintf("%d 分钟", max(1, int(d/time.Minute)))
	}
	if d < 48*time.Hour {
		return fmt.Sprintf("%d 小时", int(d/time.Hour))
	}
	return fmt.Sprintf("%d 天", int(d/(24*time.Hour)))
}

// evalMorningBriefGate checks near-24h signals purely in Go (0 tokens):
// (a) open tasks in memory/tasks/,
// (b) recent 24h worker activity (running/finished/errored/reported) or active main chat,
// (c) latest dreams/ journal (within 24h) containing attention items or env anomalies.
func (sp *Super) evalMorningBriefGate(now time.Time, prevTriggeredAt time.Time) routineSignal {
	since := now.Add(-24 * time.Hour)
	var summary []string

	// (a) Open tasks.
	openFiles := listNonEmptyMarkdown(filepath.Join(sp.memDir(), "tasks"))
	if len(openFiles) > 0 {
		names := make([]string, 0, len(openFiles))
		for _, f := range openFiles {
			names = append(names, f.Name)
		}
		summary = append(summary, fmt.Sprintf("未关闭任务（%d 项）：%s", len(names), joinClipNames(names, 6)))
	}

	// (b) Recent 24h worker activity or main session activity.
	sp.mu.Lock()
	var workerNotes []string
	workerNames := make([]string, 0, len(sp.st.Workers))
	for name := range sp.st.Workers {
		workerNames = append(workerNames, name)
	}
	sort.Strings(workerNames)
	for _, name := range workerNames {
		w := sp.st.Workers[name]
		if w == nil {
			continue
		}
		active := !w.Ended
		recent := (!w.CreatedAt.IsZero() && !w.CreatedAt.Before(since)) ||
			(!w.ReportAt.IsZero() && !w.ReportAt.Before(since))
		if active || recent {
			lbl := w.Name
			if w.Label != "" {
				lbl = fmt.Sprintf("%s(%s)", w.Label, w.Name)
			}
			workerNotes = append(workerNotes, fmt.Sprintf("%s[%s]", lbl, w.Status))
		}
	}

	userMsgs := 0
	collieMsgs := 0
	workerEvents := 0
	sawUserAfterPrev := false
	if !prevTriggeredAt.IsZero() {
		for i := len(sp.st.Entries) - 1; i >= 0; i-- {
			e := sp.st.Entries[i]
			if e.At.Before(prevTriggeredAt) {
				break
			}
			if e.Role == "user" {
				sawUserAfterPrev = true
				break
			}
		}
	}
	for i := len(sp.st.Entries) - 1; i >= 0; i-- {
		e := sp.st.Entries[i]
		if e.At.Before(since) {
			break
		}
		switch e.Role {
		case "user":
			userMsgs++
		case "collie":
			// Skip self-echo from yesterday's morning briefing reply if no human spoke after it.
			if !sawUserAfterPrev && !prevTriggeredAt.IsZero() && !e.At.Before(prevTriggeredAt) && e.At.Sub(prevTriggeredAt) < 5*time.Minute {
				continue
			}
			collieMsgs++
		case "event":
			if e.Tone == "done" || e.Tone == "error" || e.Tone == "blocked" || e.Tone == "spawn" {
				workerEvents++
			}
		}
	}
	workerTriggers := 0
	for i := len(sp.st.Triggers) - 1; i >= 0; i-- {
		t := sp.st.Triggers[i]
		if t.At.Before(since) {
			break
		}
		if t.Source == "worker" {
			workerTriggers++
		}
	}
	pendingNow := len(sp.pendingLocked(now))
	sp.mu.Unlock()

	if len(workerNotes) > 0 || workerEvents > 0 || workerTriggers > 0 {
		if len(workerNotes) > 0 {
			summary = append(summary, fmt.Sprintf("近 24h worker 动态：%s", joinClipNames(workerNotes, 6)))
		} else {
			summary = append(summary, fmt.Sprintf("近 24h 有 %d 条 worker 完工/汇报/异常事件", max(workerEvents, workerTriggers)))
		}
	}
	if userMsgs > 0 || collieMsgs > 0 {
		summary = append(summary, fmt.Sprintf("近 24h 主会话活跃（用户 %d 条、Collie %d 条）", userMsgs, collieMsgs))
	}

	// (c) Latest dreams/ journal attention or environment anomalies.
	if dreamName, hits := sp.latestDreamAttention(since, now.Location()); dreamName != "" {
		summary = append(summary, fmt.Sprintf("做梦日志 dreams/%s 记录待关注/异常：%s", dreamName, strings.Join(hits, "；")))
	}

	if len(summary) == 0 {
		return routineSignal{Fired: false}
	}
	if pendingNow > 0 {
		summary = append(summary, fmt.Sprintf("当前有 %d 项待拍板/待确认事项", pendingNow))
	}
	return routineSignal{Fired: true, Summary: summary}
}

// evalWeeklyReviewGate checks weekly signals purely in Go (0 tokens):
// (a) completed tasks archived in memory/tasks/done/ within the last 7 days,
// (b) open tasks in memory/tasks/ untouched for >= 3 days.
func (sp *Super) evalWeeklyReviewGate(now time.Time) routineSignal {
	doneCutoff := now.Add(-7 * 24 * time.Hour)
	staleCutoff := now.Add(-3 * 24 * time.Hour)
	var summary []string

	doneFiles := listNonEmptyMarkdown(filepath.Join(sp.memDir(), "tasks", "done"))
	var recentDone []datedFile
	for _, f := range doneFiles {
		if !f.Mod.Before(doneCutoff) {
			recentDone = append(recentDone, f)
		}
	}
	sort.Slice(recentDone, func(i, j int) bool { return recentDone[i].Mod.After(recentDone[j].Mod) })
	if len(recentDone) > 0 {
		names := make([]string, 0, len(recentDone))
		for _, f := range recentDone {
			names = append(names, f.Name)
		}
		summary = append(summary, fmt.Sprintf("近 7 天完工归档（%d 项）：%s", len(names), joinClipNames(names, 8)))
	}

	openFiles := listNonEmptyMarkdown(filepath.Join(sp.memDir(), "tasks"))
	var hanging []datedFile
	for _, f := range openFiles {
		if !f.Mod.After(staleCutoff) {
			hanging = append(hanging, f)
		}
	}
	sort.Slice(hanging, func(i, j int) bool { return hanging[i].Mod.Before(hanging[j].Mod) })
	if len(hanging) > 0 {
		parts := make([]string, 0, len(hanging))
		for _, f := range hanging {
			parts = append(parts, fmt.Sprintf("%s（%s未更新）", f.Name, formatAgeZH(now.Sub(f.Mod))))
		}
		summary = append(summary, fmt.Sprintf("挂起超 3 天任务（%d 项）：%s", len(parts), joinClipNames(parts, 6)))
	}

	if len(summary) == 0 {
		return routineSignal{Fired: false}
	}
	return routineSignal{Fired: true, Summary: summary}
}

// evalStaleFollowGate checks stale items purely in Go (0 tokens):
// (a) pending --opt / --check items unanswered for >= 24 hours,
// (b) open tasks in memory/tasks/ untouched for >= 48 hours.
func (sp *Super) evalStaleFollowGate(now time.Time) routineSignal {
	pendingCutoff := now.Add(-24 * time.Hour)
	taskCutoff := now.Add(-48 * time.Hour)
	var summary []string

	sp.mu.Lock()
	pending := sp.pendingLocked(now)
	sp.mu.Unlock()
	var stalePending []PendingItem
	for _, p := range pending {
		if !p.At.IsZero() && !p.At.After(pendingCutoff) {
			stalePending = append(stalePending, p)
		}
	}
	if len(stalePending) > 0 {
		parts := make([]string, 0, min(len(stalePending), 5))
		for i, p := range stalePending {
			if i >= 5 {
				break
			}
			parts = append(parts, fmt.Sprintf("%s（已等 %s：%s）", p.ID, formatAgeZH(now.Sub(p.At)), clip(oneLine(p.Summary), 48)))
		}
		summary = append(summary, fmt.Sprintf("超 24h 待拍板/待确认（%d 项）：%s", len(stalePending), strings.Join(parts, "；")))
	}

	openFiles := listNonEmptyMarkdown(filepath.Join(sp.memDir(), "tasks"))
	var staleTasks []datedFile
	for _, f := range openFiles {
		if !f.Mod.After(taskCutoff) {
			staleTasks = append(staleTasks, f)
		}
	}
	sort.Slice(staleTasks, func(i, j int) bool { return staleTasks[i].Mod.Before(staleTasks[j].Mod) })
	if len(staleTasks) > 0 {
		parts := make([]string, 0, len(staleTasks))
		for _, f := range staleTasks {
			parts = append(parts, fmt.Sprintf("%s（%s未更新）", f.Name, formatAgeZH(now.Sub(f.Mod))))
		}
		summary = append(summary, fmt.Sprintf("超 48h 未推进任务（%d 项）：%s", len(parts), joinClipNames(parts, 6)))
	}

	if len(summary) == 0 {
		return routineSignal{Fired: false}
	}
	return routineSignal{Fired: true, Summary: summary}
}

func buildRoutinePrompt(kind string, now time.Time, signals []string) string {
	stamp := now.Format("2006-01-02 15:04")
	var b strings.Builder
	switch kind {
	case routineMorningBrief:
		fmt.Fprintf(&b, "[官方定时 · 每日晨报] 现在是 %s，门控检测到近 24h 动态：\n", stamp)
		for _, s := range slices.Compact(signals) {
			b.WriteString("- " + s + "\n")
		}
		b.WriteString("请结合 memory/tasks/ 与最新 dreams/ 日志，用 `collie reply` 给人类端一份 **5 行以内**的精简早报（过夜进展 + 待拍板项 + 做梦盘点出的环境异常，无则略去）。若核实后并无需关注的事项，可保持静默。")
	case routineWeeklyReview:
		fmt.Fprintf(&b, "[官方定时 · 每周复盘] 现在是 %s，门控检测到本周可复盘事项：\n", stamp)
		for _, s := range slices.Compact(signals) {
			b.WriteString("- " + s + "\n")
		}
		b.WriteString("请结合 memory/tasks/done/、memory/tasks/ 与近期 dreams/ 日志，用 `collie reply` 给人类汇总本周产出、滞留旧坑以及可沉淀为 Skill 的建议（若有建议可附 `--opt` 一键决策）。若核实无实质内容，可保持静默。")
	case routineStaleFollow:
		fmt.Fprintf(&b, "[官方定时 · 滞留事项轻提醒] 现在是 %s，门控检测到滞留事项：\n", stamp)
		for _, s := range slices.Compact(signals) {
			b.WriteString("- " + s + "\n")
		}
		b.WriteString("请核对这些事项是否仍需推进，用 `collie reply` 轻轻提醒人类一句，并附一键选项 `--opt \"继续\" --opt \"延到明天\" --opt \"关掉\"` 方便单点决定。若核实均已处理，可保持静默。")
	}
	return b.String()
}
