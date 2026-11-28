package collie

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func mustWriteFileWithMod(t *testing.T, path, content string, mod time.Time) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	if !mod.IsZero() {
		if err := os.Chtimes(path, mod, mod); err != nil {
			t.Fatal(err)
		}
	}
}

func routineTriggers(sp *Super, from string) []*Trigger {
	sp.mu.Lock()
	defer sp.mu.Unlock()
	var out []*Trigger
	for _, t := range sp.st.Triggers {
		if from == "" || t.From == from {
			out = append(out, t)
		}
	}
	return out
}

func TestRoutinesSettingsAPIAndBackwardsCompat(t *testing.T) {
	t.Parallel()
	sp, db, call := newPendingTestEnv(t)

	// 1) GET /api/super/settings on fresh DB exposes all 3 official routines disabled with defaults.
	rec := call("GET", "/api/super/settings", nil)
	if rec.Code != 200 {
		t.Fatalf("GET /api/super/settings = %d: %s", rec.Code, rec.Body.String())
	}
	var got struct {
		Settings Settings `json:"settings"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.Settings.MorningBrief == nil || got.Settings.MorningBrief.Enabled || got.Settings.MorningBrief.At != "09:00" || got.Settings.MorningBrief.Day != "" {
		t.Fatalf("default morning_brief = %+v, want disabled 09:00", got.Settings.MorningBrief)
	}
	if got.Settings.WeeklyReview == nil || got.Settings.WeeklyReview.Enabled || got.Settings.WeeklyReview.Day != "mon" || got.Settings.WeeklyReview.At != "09:00" {
		t.Fatalf("default weekly_review = %+v, want disabled mon 09:00", got.Settings.WeeklyReview)
	}
	if got.Settings.StaleFollow == nil || got.Settings.StaleFollow.Enabled || got.Settings.StaleFollow.At != "17:30" || got.Settings.StaleFollow.Day != "" {
		t.Fatalf("default stale_follow = %+v, want disabled 17:30", got.Settings.StaleFollow)
	}

	// 2) POST /api/super/settings saves and normalizes custom schedules (including malformed fallbacks).
	savePayload := map[string]any{
		"settings": map[string]any{
			"router_kind":   "claude",
			"worker_kind":   "claude",
			"worker_cwd":    "~",
			"notify_level":  "quiet",
			"reply_chars":   200,
			"morning_brief": map[string]any{"enabled": true, "at": "8:05", "day": "sun"},
			"weekly_review": map[string]any{"enabled": true, "day": "FRI", "at": "10:15"},
			"stale_follow":  map[string]any{"enabled": true, "at": "99:99"},
		},
	}
	rec = call("POST", "/api/super/settings", savePayload)
	if rec.Code != 200 {
		t.Fatalf("POST /api/super/settings = %d: %s", rec.Code, rec.Body.String())
	}
	var saved struct {
		Settings Settings `json:"settings"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &saved); err != nil {
		t.Fatal(err)
	}
	if !saved.Settings.MorningBrief.Enabled || saved.Settings.MorningBrief.At != "08:05" || saved.Settings.MorningBrief.Day != "" {
		t.Fatalf("saved morning_brief = %+v, want enabled 08:05 (no day)", saved.Settings.MorningBrief)
	}
	if !saved.Settings.WeeklyReview.Enabled || saved.Settings.WeeklyReview.Day != "fri" || saved.Settings.WeeklyReview.At != "10:15" {
		t.Fatalf("saved weekly_review = %+v, want enabled fri 10:15", saved.Settings.WeeklyReview)
	}
	if !saved.Settings.StaleFollow.Enabled || saved.Settings.StaleFollow.At != "17:30" {
		t.Fatalf("saved stale_follow = %+v, want enabled fallback 17:30", saved.Settings.StaleFollow)
	}

	// 3) Backwards compatibility: older client omitting routine fields must preserve saved configs.
	legacyPayload := map[string]any{
		"settings": map[string]any{
			"router_kind":  "claude",
			"worker_kind":  "claude",
			"worker_cwd":   "~",
			"notify_level": "normal",
			"reply_chars":  240,
			"dream_at":     "03:30",
		},
	}
	rec = call("POST", "/api/super/settings", legacyPayload)
	if rec.Code != 200 {
		t.Fatalf("legacy POST /api/super/settings = %d: %s", rec.Code, rec.Body.String())
	}
	var afterLegacy struct {
		Settings Settings `json:"settings"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &afterLegacy); err != nil {
		t.Fatal(err)
	}
	if !afterLegacy.Settings.MorningBrief.Enabled || afterLegacy.Settings.MorningBrief.At != "08:05" {
		t.Fatalf("legacy POST clobbered morning_brief: %+v", afterLegacy.Settings.MorningBrief)
	}
	if !afterLegacy.Settings.WeeklyReview.Enabled || afterLegacy.Settings.WeeklyReview.Day != "fri" {
		t.Fatalf("legacy POST clobbered weekly_review: %+v", afterLegacy.Settings.WeeklyReview)
	}
	if !afterLegacy.Settings.StaleFollow.Enabled || afterLegacy.Settings.StaleFollow.At != "17:30" {
		t.Fatalf("legacy POST clobbered stale_follow: %+v", afterLegacy.Settings.StaleFollow)
	}

	// 4) Explicit disable persists across SQLite flush + reload.
	disablePayload := map[string]any{
		"settings": map[string]any{
			"router_kind":   "claude",
			"worker_kind":   "claude",
			"worker_cwd":    "~",
			"notify_level":  "normal",
			"reply_chars":   240,
			"morning_brief": map[string]any{"enabled": false, "at": "08:05"},
		},
	}
	if rec = call("POST", "/api/super/settings", disablePayload); rec.Code != 200 {
		t.Fatalf("disable POST = %d", rec.Code)
	}
	sp.flush()
	reloaded, err := loadState(db, sp.dir)
	if err != nil {
		t.Fatal(err)
	}
	mb, wr, sf := reloaded.Settings.effectiveRoutines()
	if mb.Enabled || mb.At != "08:05" {
		t.Fatalf("reloaded morning_brief = %+v, want disabled 08:05", mb)
	}
	if !wr.Enabled || wr.Day != "fri" || wr.At != "10:15" {
		t.Fatalf("reloaded weekly_review = %+v, want enabled fri 10:15", wr)
	}
	if !sf.Enabled || sf.At != "17:30" {
		t.Fatalf("reloaded stale_follow = %+v, want enabled 17:30", sf)
	}
}

func TestMorningBriefZeroTokenGateSignalsAndDedup(t *testing.T) {
	t.Parallel()
	loc := time.FixedZone("CST", 8*3600)
	sp, db, _ := newPendingTestEnv(t)

	// Disabled by default: even with open tasks at 09:05, must do nothing.
	day1 := time.Date(2026, 11, 23, 9, 5, 0, 0, loc)
	taskPath := filepath.Join(sp.memDir(), "tasks", "release-v1.md")
	mustWriteFileWithMod(t, taskPath, "# Release v1\n- finish checks\n", day1.Add(-2*time.Hour))
	sp.checkRoutinesAt(context.Background(), day1, nil, false)
	if ts := routineTriggers(sp, "routine:morning_brief"); len(ts) != 0 {
		t.Fatalf("disabled morning_brief unexpectedly triggered: %+v", ts)
	}

	// Enable morning_brief at 09:00, remove open task -> empty state must silently skip (0 tokens).
	_ = os.Remove(taskPath)
	sp.mu.Lock()
	sp.st.Settings.MorningBrief = &RoutineConfig{Enabled: true, At: "09:00"}
	sp.mu.Unlock()

	// Before scheduled minute (08:59): not evaluated yet.
	sp.checkRoutinesAt(context.Background(), time.Date(2026, 11, 23, 8, 59, 0, 0, loc), nil, false)
	if rs := sp.loadRoutineState(routineMorningBrief); rs.Date != "" {
		t.Fatalf("evaluated before scheduled time: %+v", rs)
	}

	// Past catchup window (12:01 = >3h after 09:00): does not fire.
	sp.checkRoutinesAt(context.Background(), time.Date(2026, 11, 23, 12, 1, 0, 0, loc), nil, false)
	if rs := sp.loadRoutineState(routineMorningBrief); rs.Date != "" {
		t.Fatalf("evaluated past catchup window: %+v", rs)
	}

	// At 09:01 with zero signals: silently skip and record date so it does not re-check today.
	sp.checkRoutinesAt(context.Background(), time.Date(2026, 11, 23, 9, 1, 0, 0, loc), nil, false)
	if ts := routineTriggers(sp, "routine:morning_brief"); len(ts) != 0 {
		t.Fatalf("empty state triggered morning_brief: %+v", ts)
	}
	if rs := sp.loadRoutineState(routineMorningBrief); rs.Date != "2026-11-23" || rs.Outcome != "skipped" {
		t.Fatalf("expected skipped state for 2026-11-23, got %+v", rs)
	}

	// Even if a task is created later on the same day (09:30), same-day dedup holds.
	mustWriteFileWithMod(t, taskPath, "# Release v1\n- open item\n", time.Date(2026, 11, 23, 9, 20, 0, 0, loc))
	sp.checkRoutinesAt(context.Background(), time.Date(2026, 11, 23, 9, 30, 0, 0, loc), nil, false)
	if ts := routineTriggers(sp, "routine:morning_brief"); len(ts) != 0 {
		t.Fatalf("same-day dedup failed after skip: %+v", ts)
	}

	// Day 2 (2026-11-24 09:05): signal (a) open task triggers morning brief once.
	day2 := time.Date(2026, 11, 24, 9, 5, 0, 0, loc)
	// Defer while dreaming; do not skip or lose the day's run.
	sp.mu.Lock()
	sp.dream = &dreamRun{at: day2.Add(-5 * time.Minute)}
	sp.mu.Unlock()
	sp.checkRoutinesAt(context.Background(), day2, nil, false)
	if ts := routineTriggers(sp, "routine:morning_brief"); len(ts) != 0 {
		t.Fatalf("triggered while dreaming: %+v", ts)
	}
	sp.mu.Lock()
	sp.dream = nil
	sp.mu.Unlock()

	sp.checkRoutinesAt(context.Background(), day2.Add(time.Minute), nil, false)
	ts := routineTriggers(sp, "routine:morning_brief")
	if len(ts) != 1 {
		t.Fatalf("day2 open task triggers = %d, want 1", len(ts))
	}
	if ts[0].Source != "system" || ts[0].Thread != mainThread || !strings.Contains(ts[0].Text, "release-v1.md") || !strings.Contains(ts[0].Text, "5 行以内") {
		t.Fatalf("unexpected morning brief trigger: %+v", ts[0])
	}
	// Calling again same day (even after simulated Hub restart with fresh in-memory cache) must NOT double-fire.
	sp.flush()
	st2, err := loadState(db, sp.dir)
	if err != nil {
		t.Fatal(err)
	}
	restarted := &Super{s: sp.s, db: db, dir: sp.dir, st: st2, subs: map[chan struct{}]bool{}}
	restarted.checkRoutinesAt(context.Background(), day2.Add(15*time.Minute), nil, false)
	if got := routineTriggers(restarted, "routine:morning_brief"); len(got) != 1 {
		t.Fatalf("same-day dedup failed after restart: len=%d, want 1", len(got))
	}

	// Day 3 (2026-11-25 09:05): simulate Day 2's own morning brief reply at Day 2 09:06:20,
	// with open task removed and NO user/worker activity -> anti-self-echo must skip (0 tokens).
	_ = os.Remove(taskPath)
	sp.mu.Lock()
	sp.st.Entries = append(sp.st.Entries, &Entry{
		ID: sp.st.nextID("e"), Role: "collie", Thread: mainThread,
		Text: "早安，昨晚进展汇总…", At: day2.Add(time.Minute + 20*time.Second),
	})
	sp.mu.Unlock()
	day3 := time.Date(2026, 11, 25, 9, 5, 0, 0, loc)
	sp.checkRoutinesAt(context.Background(), day3, nil, false)
	if got := routineTriggers(sp, "routine:morning_brief"); len(got) != 1 {
		t.Fatalf("self-echo caused unwanted morning brief on day3: len=%d", len(got))
	}
	if rs := sp.loadRoutineState(routineMorningBrief); rs.Date != "2026-11-25" || rs.Outcome != "skipped" {
		t.Fatalf("expected day3 skipped, got %+v", rs)
	}

	// Day 4 (2026-11-26 09:05): signal (b) recent 24h worker report triggers morning brief.
	day4 := time.Date(2026, 11, 26, 9, 5, 0, 0, loc)
	sp.mu.Lock()
	sp.st.Workers["nightly-bench"] = &Worker{
		Name: "nightly-bench", Label: "压测回归", Status: "done", Ended: true,
		CreatedAt: day4.Add(-8 * time.Hour), ReportAt: day4.Add(-7 * time.Hour),
	}
	sp.mu.Unlock()
	sp.checkRoutinesAt(context.Background(), day4, nil, false)
	if got := routineTriggers(sp, "routine:morning_brief"); len(got) != 2 {
		t.Fatalf("day4 worker report triggers = %d, want 2", len(got))
	} else if !strings.Contains(got[1].Text, "nightly-bench") {
		t.Fatalf("day4 trigger missing worker hint: %s", got[1].Text)
	}

	// Day 5 (2026-11-27 09:05): clean dream journal (negated anomaly) must silently skip.
	day5 := time.Date(2026, 11, 27, 9, 5, 0, 0, loc)
	sp.mu.Lock()
	sp.st.Workers = map[string]*Worker{}
	sp.mu.Unlock()
	dreamsDir := filepath.Join(sp.routerDir(), "dreams")
	mustWriteFileWithMod(t, filepath.Join(dreamsDir, "2026-11-27-0400.md"),
		"精简了 instructions.md，归档 1 个已完成任务。\n\n- 巡检临时目录一切正常，未发现环境异常。\n",
		day5.Add(-5*time.Hour))
	sp.checkRoutinesAt(context.Background(), day5, nil, false)
	if got := routineTriggers(sp, "routine:morning_brief"); len(got) != 2 {
		t.Fatalf("clean dream journal falsely triggered morning brief: len=%d", len(got))
	}

	// Day 6 (2026-11-28 09:05): signal (c) dream journal with environment anomaly triggers!
	day6 := time.Date(2026, 11, 28, 9, 5, 0, 0, loc)
	mustWriteFileWithMod(t, filepath.Join(dreamsDir, "2026-11-28-0400.md"),
		"整理了近期笔记并巡检运行环境。\n\n- 环境异常：工作盘剩余空间不足 8%，存在 3 个孤儿临时目录待关注。\n",
		day6.Add(-5*time.Hour))
	sp.checkRoutinesAt(context.Background(), day6, nil, false)
	if got := routineTriggers(sp, "routine:morning_brief"); len(got) != 3 {
		t.Fatalf("anomaly dream journal did not trigger morning brief: len=%d", len(got))
	} else if !strings.Contains(got[2].Text, "dreams/2026-11-28-0400.md") || !strings.Contains(got[2].Text, "环境异常") {
		t.Fatalf("day6 trigger missing dream anomaly detail: %s", got[2].Text)
	}
}

func TestWeeklyReviewZeroTokenGateAndWeekday(t *testing.T) {
	t.Parallel()
	loc := time.FixedZone("CST", 8*3600)
	sp, _, _ := newPendingTestEnv(t)
	sp.mu.Lock()
	sp.st.Settings.WeeklyReview = &RoutineConfig{Enabled: true, Day: "wed", At: "09:30"}
	sp.mu.Unlock()

	// Tuesday 09:35: wrong weekday -> does not evaluate.
	tue := time.Date(2026, 11, 24, 9, 35, 0, 0, loc)
	mustWriteFileWithMod(t, filepath.Join(sp.memDir(), "tasks", "done", "ship-api.md"), "# Ship API\nDone.\n", tue.Add(-24*time.Hour))
	sp.checkRoutinesAt(context.Background(), tue, nil, false)
	if got := routineTriggers(sp, "routine:weekly_review"); len(got) != 0 {
		t.Fatalf("weekly_review fired on wrong weekday: %+v", got)
	}

	// Wednesday 09:35 with only an old done task (>7d) and a fresh open task (1d): silently skip (0 tokens).
	wed1 := time.Date(2026, 11, 25, 9, 35, 0, 0, loc)
	_ = os.Remove(filepath.Join(sp.memDir(), "tasks", "done", "ship-api.md"))
	mustWriteFileWithMod(t, filepath.Join(sp.memDir(), "tasks", "done", "ancient.md"), "# Old\nDone.\n", wed1.Add(-10*24*time.Hour))
	mustWriteFileWithMod(t, filepath.Join(sp.memDir(), "tasks", "fresh.md"), "# Fresh\nIn progress.\n", wed1.Add(-24*time.Hour))
	sp.checkRoutinesAt(context.Background(), wed1, nil, false)
	if got := routineTriggers(sp, "routine:weekly_review"); len(got) != 0 {
		t.Fatalf("weekly_review fired without recent done or >3d hanging task: %+v", got)
	}
	if rs := sp.loadRoutineState(routineWeeklyReview); rs.Date != "2026-11-25" || rs.Outcome != "skipped" {
		t.Fatalf("expected skipped state for wed1, got %+v", rs)
	}

	// Next Wednesday: recent done task (<7d) triggers weekly review!
	wed2 := time.Date(2026, 12, 2, 9, 35, 0, 0, loc)
	mustWriteFileWithMod(t, filepath.Join(sp.memDir(), "tasks", "done", "ship-search.md"), "# Ship Search\nMerged.\n", wed2.Add(-2*24*time.Hour))
	sp.checkRoutinesAt(context.Background(), wed2, nil, false)
	if got := routineTriggers(sp, "routine:weekly_review"); len(got) != 1 {
		t.Fatalf("wed2 recent done task triggers = %d, want 1", len(got))
	} else if !strings.Contains(got[0].Text, "ship-search.md") || !strings.Contains(got[0].Text, "Skill") {
		t.Fatalf("unexpected weekly_review prompt: %s", got[0].Text)
	}

	// Next Wednesday: no recent done, but open task untouched >3 days triggers weekly review!
	wed3 := time.Date(2026, 12, 9, 9, 35, 0, 0, loc)
	_ = os.Remove(filepath.Join(sp.memDir(), "tasks", "done", "ship-search.md"))
	mustWriteFileWithMod(t, filepath.Join(sp.memDir(), "tasks", "stale-migration.md"), "# Migration\nBlocked.\n", wed3.Add(-5*24*time.Hour))
	sp.checkRoutinesAt(context.Background(), wed3, nil, false)
	if got := routineTriggers(sp, "routine:weekly_review"); len(got) != 2 {
		t.Fatalf("wed3 >3d hanging task triggers = %d, want 2", len(got))
	} else if !strings.Contains(got[1].Text, "stale-migration.md") {
		t.Fatalf("wed3 trigger missing hanging task: %s", got[1].Text)
	}
}

func TestStaleFollowZeroTokenGateAndWeekdays(t *testing.T) {
	t.Parallel()
	loc := time.FixedZone("CST", 8*3600)
	sp, _, _ := newPendingTestEnv(t)
	sp.mu.Lock()
	sp.st.Settings.StaleFollow = &RoutineConfig{Enabled: true, At: "17:30"}
	sp.mu.Unlock()

	// Seed a stale pending option (>24h) to verify weekend is strictly skipped.
	sat := time.Date(2026, 11, 28, 17, 35, 0, 0, loc)
	sp.mu.Lock()
	staleOpt := &Entry{
		ID: sp.st.nextID("e"), Role: "collie", Thread: mainThread,
		Text: "要合并这个 PR 吗？", Options: []string{"合并", "先等等"},
		At: sat.Add(-30 * time.Hour),
	}
	sp.st.Entries = append(sp.st.Entries, staleOpt)
	sp.mu.Unlock()

	sp.checkRoutinesAt(context.Background(), sat, nil, false)
	sp.checkRoutinesAt(context.Background(), sat.AddDate(0, 0, 1), nil, false) // Sunday
	if got := routineTriggers(sp, "routine:stale_follow"); len(got) != 0 {
		t.Fatalf("stale_follow fired on weekend: %+v", got)
	}

	// Monday 17:35: resolve old pending item and add fresh ones (<24h option, <48h task) -> silent skip!
	mon := time.Date(2026, 11, 30, 17, 35, 0, 0, loc)
	sp.mu.Lock()
	sp.st.Dismissed[staleOpt.ID] = true
	freshOpt := &Entry{
		ID: sp.st.nextID("e"), Role: "collie", Thread: mainThread,
		Text: "刚问的选项", Options: []string{"好", "不用"},
		At: mon.Add(-3 * time.Hour),
	}
	sp.st.Entries = append(sp.st.Entries, freshOpt)
	sp.mu.Unlock()
	mustWriteFileWithMod(t, filepath.Join(sp.memDir(), "tasks", "active-work.md"), "# Active\nMoving.\n", mon.Add(-12*time.Hour))

	sp.checkRoutinesAt(context.Background(), mon, nil, false)
	if got := routineTriggers(sp, "routine:stale_follow"); len(got) != 0 {
		t.Fatalf("stale_follow fired for fresh items: %+v", got)
	}
	if rs := sp.loadRoutineState(routineStaleFollow); rs.Date != "2026-11-30" || rs.Outcome != "skipped" {
		t.Fatalf("expected skipped state for mon, got %+v", rs)
	}

	// Tuesday 17:35: freshOpt is now 27h old (>24h) -> fires with one-tap options guidance!
	tue := time.Date(2026, 12, 1, 17, 35, 0, 0, loc)
	sp.checkRoutinesAt(context.Background(), tue, nil, false)
	if got := routineTriggers(sp, "routine:stale_follow"); len(got) != 1 {
		t.Fatalf("tue stale pending triggers = %d, want 1", len(got))
	} else {
		for _, want := range []string{freshOpt.ID, `--opt "继续"`, `--opt "延到明天"`, `--opt "关掉"`} {
			if !strings.Contains(got[0].Text, want) {
				t.Fatalf("stale_follow trigger missing %q:\n%s", want, got[0].Text)
			}
		}
	}

	// Wednesday 17:35: dismiss pending item, active-work.md is now 60h untouched (>48h) -> fires!
	wed := time.Date(2026, 12, 2, 17, 35, 0, 0, loc)
	sp.mu.Lock()
	sp.st.Dismissed[freshOpt.ID] = true
	sp.mu.Unlock()
	sp.checkRoutinesAt(context.Background(), wed, nil, false)
	if got := routineTriggers(sp, "routine:stale_follow"); len(got) != 2 {
		t.Fatalf("wed >48h stale task triggers = %d, want 2", len(got))
	} else if !strings.Contains(got[1].Text, "active-work.md") {
		t.Fatalf("wed trigger missing stale task name: %s", got[1].Text)
	}
}
