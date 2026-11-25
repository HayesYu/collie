package collie

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"
)

const (
	forgeModeNotify = "notify"
	forgeModeAdvise = "advise"
	forgeModeAgent  = "agent"

	forgeDefaultPollInterval = 60 * time.Second
	forgeMinPollInterval     = 30 * time.Second
	forgeMaxPollInterval     = time.Hour
	forgeDefaultSampleLimit  = 3
)

// ForgeProviderMeta lets a ForgeProvider supply store UI metadata, terminology
// (PR vs MR), auth options, and configuration checks for GitForgeAdapter.
type ForgeProviderMeta interface {
	Meta() IntegrationMeta
	DefaultAuthMode() string
	DefaultEvents() map[string]bool
	IsConfigured(cfg IntegrationConfig, fc ForgeConfig) bool
	SecretSet(cfg IntegrationConfig) bool
	ExtraView(cfg IntegrationConfig) map[string]any
	TermPR() string // "PR" or "MR"
}

// GitForgeAdapter is the generic active-polling code-hosting engine shared by
// GitHubProvider and any locally discovered CLIProvider.
//
// It implements cold-start watermarking (never backfills historical backlog),
// noise filtering (self/bot/non-failing CI), three natural-language dispatch
// modes, durable deduplication, and read-only SamplePull(limit) previews.
type GitForgeAdapter struct {
	sp       *Super
	mgr      *IntegrationManager
	provider ForgeProvider

	// Optional test override to accelerate poll ticks.
	minPollInterval time.Duration
	nowFn           func() time.Time

	mu       sync.RWMutex
	selfUser string
	lastMeta map[string]any
}

func newGitForgeAdapter(sp *Super, mgr *IntegrationManager, provider ForgeProvider) *GitForgeAdapter {
	return &GitForgeAdapter{
		sp:              sp,
		mgr:             mgr,
		provider:        provider,
		minPollInterval: forgeMinPollInterval,
		nowFn:           time.Now,
	}
}

func (g *GitForgeAdapter) now() time.Time {
	if g.nowFn != nil {
		return g.nowFn()
	}
	return time.Now()
}

func (g *GitForgeAdapter) Kind() string {
	return g.provider.Name()
}

func (g *GitForgeAdapter) Meta() IntegrationMeta {
	if m, ok := g.provider.(ForgeProviderMeta); ok {
		return m.Meta()
	}
	k := g.Kind()
	return IntegrationMeta{
		Kind:        k,
		Name:        k,
		Subtitle:    "代码托管主动追踪",
		Description: "零公网增量拉取关注仓库与 @我 的动态，支持只提醒、自动调研出建议或交给已订阅 Agent。",
		Order:       50,
	}
}

func (g *GitForgeAdapter) termPR() string {
	if m, ok := g.provider.(ForgeProviderMeta); ok && strings.TrimSpace(m.TermPR()) != "" {
		return strings.TrimSpace(m.TermPR())
	}
	return "PR"
}

func (g *GitForgeAdapter) defaultAuthMode() string {
	if m, ok := g.provider.(ForgeProviderMeta); ok && strings.TrimSpace(m.DefaultAuthMode()) != "" {
		return strings.TrimSpace(m.DefaultAuthMode())
	}
	return "cli"
}

func (g *GitForgeAdapter) defaultEvents() map[string]bool {
	if m, ok := g.provider.(ForgeProviderMeta); ok {
		if evs := m.DefaultEvents(); len(evs) > 0 {
			cp := make(map[string]bool, len(evs))
			for k, v := range evs {
				cp[k] = v
			}
			return cp
		}
	}
	return map[string]bool{"pr": true, "issue": true, "ci": true}
}

func (g *GitForgeAdapter) parseConfig(raw json.RawMessage, secrets map[string]string) ForgeConfig {
	fc := ForgeConfig{
		WatchMentions: true,
		Events:        g.defaultEvents(),
		HandleMode:    forgeModeAdvise,
		AuthMode:      g.defaultAuthMode(),
		PollInterval:  int(forgeDefaultPollInterval / time.Second),
		Secrets:       secrets,
	}
	ignoreBots := true
	fc.IgnoreBots = &ignoreBots

	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || string(trimmed) == "null" || string(trimmed) == "{}" {
		return fc
	}

	var wire struct {
		Repos         []string        `json:"repos"`
		WatchMentions *bool           `json:"watch_mentions"`
		Events        map[string]bool `json:"events"`
		HandleMode    *string         `json:"handle_mode"`
		AuthMode      *string         `json:"auth_mode"`
		PollInterval  *int            `json:"poll_interval"`
		IgnoreBots    *bool           `json:"ignore_bots"`
		Configured    *bool           `json:"configured"`
	}
	if err := json.Unmarshal(trimmed, &wire); err != nil {
		return fc
	}

	var repos []string
	for _, r := range wire.Repos {
		if nr := normalizeForgeRepo(r); nr != "" && !slices.Contains(repos, nr) {
			repos = append(repos, nr)
		}
	}
	fc.Repos = repos
	if wire.WatchMentions != nil {
		fc.WatchMentions = *wire.WatchMentions
	}
	if len(wire.Events) > 0 {
		evs := make(map[string]bool, len(wire.Events))
		for k, v := range wire.Events {
			k = strings.ToLower(strings.TrimSpace(k))
			if k != "" {
				evs[k] = v
			}
		}
		if len(evs) > 0 {
			fc.Events = evs
		}
	}
	if wire.HandleMode != nil {
		fc.HandleMode = normalizeHandleMode(*wire.HandleMode)
	}
	if wire.AuthMode != nil && strings.TrimSpace(*wire.AuthMode) != "" {
		fc.AuthMode = strings.ToLower(strings.TrimSpace(*wire.AuthMode))
	}
	if wire.PollInterval != nil && *wire.PollInterval > 0 {
		pi := *wire.PollInterval
		if pi < 30 {
			pi = 30
		}
		if pi > 3600 {
			pi = 3600
		}
		fc.PollInterval = pi
	}
	if wire.IgnoreBots != nil {
		b := *wire.IgnoreBots
		fc.IgnoreBots = &b
	}
	if wire.Configured != nil {
		fc.Configured = *wire.Configured
	}
	return fc
}

func normalizeHandleMode(m string) string {
	switch strings.ToLower(strings.TrimSpace(m)) {
	case forgeModeNotify:
		return forgeModeNotify
	case forgeModeAgent:
		return forgeModeAgent
	default:
		return forgeModeAdvise
	}
}

func normalizeForgeRepo(raw string) string {
	s := strings.TrimSpace(raw)
	if s == "" {
		return ""
	}
	if idx := strings.Index(s, "://"); idx >= 0 {
		rest := s[idx+3:]
		if slash := strings.IndexByte(rest, '/'); slash >= 0 {
			s = rest[slash+1:]
		}
	}
	s = strings.TrimPrefix(s, "git@")
	if colon := strings.IndexByte(s, ':'); colon > 0 && !strings.Contains(s[:colon], "/") {
		s = s[colon+1:]
	}
	s = strings.TrimSuffix(s, ".git")
	s = strings.Trim(s, "/ \t\r\n")
	parts := strings.Split(s, "/")
	if len(parts) < 2 {
		return ""
	}
	for _, p := range parts {
		if strings.TrimSpace(p) == "" || strings.ContainsAny(p, " \t\r\n?#") {
			return ""
		}
	}
	return strings.Join(parts, "/")
}

func (g *GitForgeAdapter) IsConfigured(cfg IntegrationConfig) bool {
	fc := g.parseConfig(cfg.Config, cfg.Secrets)
	if m, ok := g.provider.(ForgeProviderMeta); ok {
		return m.IsConfigured(cfg, fc)
	}
	return fc.Configured
}

func (g *GitForgeAdapter) SecretSet(cfg IntegrationConfig) bool {
	if m, ok := g.provider.(ForgeProviderMeta); ok {
		return m.SecretSet(cfg)
	}
	return len(cfg.Secrets) > 0
}

func (g *GitForgeAdapter) SanitizeConfig(cfg IntegrationConfig) map[string]any {
	fc := g.parseConfig(cfg.Config, cfg.Secrets)
	repos := fc.Repos
	if repos == nil {
		repos = []string{}
	}
	ignoreBots := true
	if fc.IgnoreBots != nil {
		ignoreBots = *fc.IgnoreBots
	}
	return map[string]any{
		"repos":          repos,
		"watch_mentions": fc.WatchMentions,
		"events":         fc.Events,
		"handle_mode":    fc.HandleMode,
		"auth_mode":      fc.AuthMode,
		"poll_interval":  fc.PollInterval,
		"ignore_bots":    ignoreBots,
		"configured":     fc.Configured,
	}
}

func (g *GitForgeAdapter) ValidateSave(raw json.RawMessage) (json.RawMessage, error) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || string(trimmed) == "null" || string(trimmed) == "{}" {
		return json.RawMessage(`{}`), nil
	}
	fc := g.parseConfig(raw, nil)
	// Respect explicit configured:false (e.g. reset), otherwise mark configured when saved.
	var probe struct {
		Configured *bool `json:"configured"`
	}
	_ = json.Unmarshal(trimmed, &probe)
	if probe.Configured != nil {
		fc.Configured = *probe.Configured
	} else {
		fc.Configured = true
	}
	fc.Secrets = nil
	out, err := json.Marshal(fc)
	if err != nil {
		return nil, err
	}
	return json.RawMessage(out), nil
}

func (g *GitForgeAdapter) ExtraView() map[string]any {
	out := map[string]any{
		"engine":  "gitforge",
		"term_pr": g.termPR(),
	}
	if m, ok := g.provider.(ForgeProviderMeta); ok {
		var cfg IntegrationConfig
		if g.mgr != nil {
			cfg = g.mgr.ConfigSnapshot(g.Kind())
		}
		for k, v := range m.ExtraView(cfg) {
			out[k] = v
		}
	}
	g.mu.RLock()
	if g.selfUser != "" {
		out["user"] = g.selfUser
	}
	for k, v := range g.lastMeta {
		if _, exists := out[k]; !exists {
			out[k] = v
		}
	}
	g.mu.RUnlock()
	return out
}

func (g *GitForgeAdapter) Health() IntegrationHealth {
	if g.mgr != nil {
		return g.mgr.Health(g.Kind())
	}
	return IntegrationHealth{State: HealthUnconfigured}
}

func (g *GitForgeAdapter) ProbeView(ctx context.Context, cfg IntegrationConfig) (map[string]any, error) {
	fc := g.parseConfig(cfg.Config, cfg.Secrets)
	id, err := g.provider.Probe(ctx, fc)
	if err != nil {
		msg := err.Error()
		if g.mgr != nil {
			msg = g.mgr.redact(msg)
		}
		return map[string]any{
			"ready":   false,
			"auth":    !errors.Is(err, ErrAuthFailed),
			"problem": cleanAuthError(errors.New(msg)),
		}, nil
	}
	g.mu.Lock()
	if id.User != "" {
		g.selfUser = id.User
	}
	g.mu.Unlock()
	repos := id.Repos
	if repos == nil {
		repos = []string{}
	}
	out := map[string]any{
		"ready":  true,
		"user":   id.User,
		"repos":  repos,
		"detail": id.Detail,
	}
	if len(id.RateLimit) > 0 {
		out["rate_limit"] = id.RateLimit
	}
	return out, nil
}

// Run executes the active polling loop until ctx is canceled or ErrAuthFailed
// triggers immediate circuit-breaking in IntegrationManager.
func (g *GitForgeAdapter) Run(ctx context.Context, cfg IntegrationConfig, sink EventSink) error {
	fc := g.parseConfig(cfg.Config, cfg.Secrets)
	if !g.IsConfigured(cfg) {
		sink.ReportHealth(HealthUnconfigured, "")
		<-ctx.Done()
		return ctx.Err()
	}

	sink.ReportHealth(HealthConnecting, "正在校验凭据…")
	id, err := g.provider.Probe(ctx, fc)
	if err != nil {
		return err
	}
	selfUser := strings.TrimSpace(id.User)
	g.mu.Lock()
	g.selfUser = selfUser
	if len(id.RateLimit) > 0 {
		g.lastMeta = map[string]any{"rate_limit": id.RateLimit}
	}
	g.mu.Unlock()

	cursor := parseForgeCursor(cfg.Cursor)
	if g.mgr != nil {
		if latest := g.mgr.Cursor(g.Kind()); len(latest) > 2 {
			cursor = parseForgeCursor(latest)
		}
	}

	interval := g.clampInterval(time.Duration(fc.PollInterval) * time.Second)

	// Cold-start watermark: record current time & prime conditional headers
	// without emitting any historical backlog.
	if !cursor.Initialized && cursor.Watermark.IsZero() {
		now := g.now().UTC()
		cursor.Watermark = now
		cursor.Initialized = true
		// Best-effort priming poll to capture ETag / Last-Modified; discard items.
		if _, nextCur, nextInt, perr := g.provider.Poll(ctx, fc, cursor); perr == nil {
			if nextCur.Watermark.Before(now) {
				nextCur.Watermark = now
			}
			nextCur.Initialized = true
			cursor = nextCur
			if nextInt > 0 {
				interval = g.clampInterval(nextInt)
			}
		} else if errors.Is(perr, ErrAuthFailed) {
			return perr
		}
		g.persistCursor(cursor)
	}

	detail := "轮询就绪"
	if selfUser != "" {
		detail = fmt.Sprintf("已连通 · @%s", selfUser)
	}
	sink.ReportHealth(HealthConnected, detail)

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(interval):
		}

		items, nextCur, nextInt, err := g.provider.Poll(ctx, fc, cursor)
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return err
		}

		prevWatermark := cursor.Watermark
		cursor = nextCur
		cursor.Initialized = true

		filtered := filterForgeItems(items, fc, selfUser, prevWatermark, true)
		// Emit oldest-first so timeline order matches actual chronology.
		sort.SliceStable(filtered, func(i, j int) bool {
			return filtered[i].At.Before(filtered[j].At)
		})

		maxAt := cursor.Watermark
		for _, item := range filtered {
			if item.At.After(maxAt) {
				maxAt = item.At
			}
			if _, err := g.emitItem(ctx, fc, item, sink); err != nil {
				log.Printf("gitforge %s emit %s: %v", g.Kind(), item.DedupKey, err)
			}
		}
		if maxAt.IsZero() {
			maxAt = g.now().UTC()
		}
		cursor.Watermark = maxAt
		g.persistCursor(cursor)

		if nextInt > 0 {
			interval = g.clampInterval(nextInt)
		} else {
			interval = g.clampInterval(time.Duration(fc.PollInterval) * time.Second)
		}

		okDetail := "轮询正常"
		if selfUser != "" {
			okDetail = fmt.Sprintf("已连通 · @%s", selfUser)
		}
		sink.ReportHealth(HealthConnected, okDetail)
	}
}

func (g *GitForgeAdapter) clampInterval(d time.Duration) time.Duration {
	min := g.minPollInterval
	if min <= 0 {
		min = forgeMinPollInterval
	}
	if d <= 0 {
		d = forgeDefaultPollInterval
	}
	if d < min {
		d = min
	}
	if d > forgeMaxPollInterval {
		d = forgeMaxPollInterval
	}
	return d
}

func maxDuration(a, b time.Duration) time.Duration {
	if b > a {
		return b
	}
	return a
}

func parseForgeCursor(raw json.RawMessage) ForgeCursor {
	var c ForgeCursor
	if len(bytes.TrimSpace(raw)) > 0 {
		_ = json.Unmarshal(raw, &c)
	}
	if c.RepoETags == nil {
		c.RepoETags = map[string]string{}
	}
	if c.RepoLastMod == nil {
		c.RepoLastMod = map[string]string{}
	}
	return c
}

func (g *GitForgeAdapter) persistCursor(c ForgeCursor) {
	if g.mgr == nil {
		return
	}
	b, err := json.Marshal(c)
	if err != nil {
		return
	}
	if err := g.mgr.SaveCursor(g.Kind(), json.RawMessage(b)); err != nil {
		log.Printf("gitforge %s save cursor: %v", g.Kind(), err)
	}
}

// SamplePull implements PullSampler: strictly read-only preview of up to limit
// recent matching events without advancing watermark, writing dedup, or waking Collie.
func (g *GitForgeAdapter) SamplePull(ctx context.Context, cfg IntegrationConfig, limit int) ([]SampleEvent, error) {
	if limit <= 0 {
		limit = forgeDefaultSampleLimit
	}
	fc := g.parseConfig(cfg.Config, cfg.Secrets)
	id, err := g.provider.Probe(ctx, fc)
	if err != nil {
		return nil, err
	}
	selfUser := strings.TrimSpace(id.User)
	g.mu.Lock()
	if selfUser != "" {
		g.selfUser = selfUser
	}
	g.mu.Unlock()

	rawItems, err := g.provider.Sample(ctx, fc, limit*3)
	if err != nil {
		return nil, err
	}
	filtered := filterForgeItems(rawItems, fc, selfUser, time.Time{}, false)
	sort.SliceStable(filtered, func(i, j int) bool {
		return filtered[i].At.After(filtered[j].At)
	})
	if len(filtered) > limit {
		filtered = filtered[:limit]
	}

	out := make([]SampleEvent, 0, len(filtered))
	for _, it := range filtered {
		out = append(out, g.toSampleEvent(fc, it))
	}
	return out, nil
}

// Test verifies identity via Probe and pulls up to 3 read-only sample events.
func (g *GitForgeAdapter) Test(ctx context.Context, cfg IntegrationConfig) TestResult {
	start := g.now()
	fc := g.parseConfig(cfg.Config, cfg.Secrets)
	id, err := g.provider.Probe(ctx, fc)
	if err != nil {
		return TestResult{
			OK:        false,
			LatencyMs: g.now().Sub(start).Milliseconds(),
			Message:   cleanAuthError(err),
		}
	}
	selfUser := strings.TrimSpace(id.User)
	rawItems, serr := g.provider.Sample(ctx, fc, forgeDefaultSampleLimit*3)
	latency := g.now().Sub(start).Milliseconds()
	if serr != nil {
		return TestResult{
			OK:        false,
			LatencyMs: latency,
			Message:   cleanAuthError(serr),
		}
	}
	filtered := filterForgeItems(rawItems, fc, selfUser, time.Time{}, false)
	sort.SliceStable(filtered, func(i, j int) bool {
		return filtered[i].At.After(filtered[j].At)
	})
	if len(filtered) > forgeDefaultSampleLimit {
		filtered = filtered[:forgeDefaultSampleLimit]
	}
	samples := make([]SampleEvent, 0, len(filtered))
	for _, it := range filtered {
		samples = append(samples, g.toSampleEvent(fc, it))
	}

	msg := fmt.Sprintf("已连通（%dms），", latency)
	if selfUser != "" {
		msg = fmt.Sprintf("已连通 @%s（%dms），", selfUser, latency)
	}
	if len(samples) == 0 {
		msg += "当前筛选条件下暂无近期动态"
	} else {
		msg += fmt.Sprintf("试拉到最近 %d 条动态", len(samples))
	}
	if rl := id.RateLimit; rl != nil {
		if rem, ok := rl["remaining"]; ok {
			if lim, ok2 := rl["limit"]; ok2 {
				msg += fmt.Sprintf("（剩余额度 %v/%v）", rem, lim)
			}
		}
	}

	detail := map[string]any{
		"user":    selfUser,
		"repos":   id.Repos,
		"samples": samples,
	}
	if len(id.RateLimit) > 0 {
		detail["rate_limit"] = id.RateLimit
	}
	return TestResult{
		OK:        true,
		LatencyMs: latency,
		Message:   msg,
		Detail:    detail,
	}
}

func (g *GitForgeAdapter) threadID(it ForgeItem) string {
	repo := strings.TrimSpace(it.Repo)
	if repo == "" {
		return g.Kind() + ":events"
	}
	if it.Number > 0 {
		return fmt.Sprintf("%s:%s#%d", g.Kind(), repo, it.Number)
	}
	return fmt.Sprintf("%s:%s", g.Kind(), repo)
}

func (g *GitForgeAdapter) activeSubscribers(thread string) []string {
	if g.sp == nil || thread == "" {
		return nil
	}
	g.sp.mu.Lock()
	defer g.sp.mu.Unlock()
	th := g.sp.st.Threads[thread]
	if th == nil || len(th.Subscribers) == 0 {
		return nil
	}
	var active []string
	for _, sub := range th.Subscribers {
		if w := g.sp.st.Workers[sub]; w != nil && !w.Ended {
			active = append(active, sub)
		}
	}
	return active
}

func (g *GitForgeAdapter) toSampleEvent(fc ForgeConfig, it ForgeItem) SampleEvent {
	repoLabel := it.Repo
	if it.Number > 0 {
		sep := "#"
		if g.termPR() == "MR" {
			sep = "!"
		}
		repoLabel = fmt.Sprintf("%s %s%d", it.Repo, sep, it.Number)
	}
	tag := strings.TrimSpace(it.Tag)
	if tag == "" {
		tag = defaultItemTag(it.Kind, g.termPR())
	}
	return SampleEvent{
		Kind:   normalizeEventKind(it.Kind),
		Repo:   repoLabel,
		Tag:    tag,
		Title:  strings.TrimSpace(it.Title),
		URL:    strings.TrimSpace(it.URL),
		At:     it.At,
		Action: g.describeAction(fc.HandleMode, it),
	}
}

func (g *GitForgeAdapter) describeAction(mode string, it ForgeItem) string {
	pr := g.termPR()
	kind := normalizeEventKind(it.Kind)
	switch normalizeHandleMode(mode) {
	case forgeModeNotify:
		switch kind {
		case "ci":
			return "按当前模式：仅推送失败摘要和直达链接，不唤醒 Collie"
		case "issue", "mention":
			return "按当前模式：仅把评论/讨论摘要提醒你，不唤醒 Collie"
		default:
			return fmt.Sprintf("按当前模式：仅把 %s 摘要发给你，不唤醒 Collie", pr)
		}
	case forgeModeAgent:
		thread := g.threadID(it)
		if subs := g.activeSubscribers(thread); len(subs) > 0 {
			return fmt.Sprintf("按当前模式：直接投递给已订阅该 %s 的 Agent（%s）", pr, strings.Join(subs, "、"))
		}
		return fmt.Sprintf("按当前模式：当前无已订阅该 %s 的在线 Agent，自动转交 Collie 先只读调研汇报", pr)
	default: // advise
		switch kind {
		case "ci":
			return "按当前模式：Collie 自动拉取失败日志定位原因，给修复拍板选项（不擅自修改远端）"
		case "issue", "mention":
			return "按当前模式：Collie 结合上下文调研并拟好建议选项，严禁擅自评论"
		default:
			return fmt.Sprintf("按当前模式：Collie 自动调研 %s 改动与评审意见并给拍板选项，严禁擅自评论或合并", pr)
		}
	}
}

func (g *GitForgeAdapter) emitItem(ctx context.Context, fc ForgeConfig, it ForgeItem, sink EventSink) (bool, error) {
	dedup := strings.TrimSpace(it.DedupKey)
	if dedup == "" {
		dedup = fmt.Sprintf("%s:%s:%s#%d:%d", g.Kind(), normalizeEventKind(it.Kind), it.Repo, it.Number, it.At.Unix())
	}
	thread := g.threadID(it)
	from := strings.TrimSpace(it.Author)
	if from == "" {
		from = g.Kind()
	}
	tag := strings.TrimSpace(it.Tag)
	if tag == "" {
		tag = defaultItemTag(it.Kind, g.termPR())
	}

	var head strings.Builder
	fmt.Fprintf(&head, "[%s · %s] %s", g.Meta().Name, tag, it.Repo)
	if it.Number > 0 {
		fmt.Fprintf(&head, "#%d", it.Number)
	}
	if t := strings.TrimSpace(it.Title); t != "" {
		fmt.Fprintf(&head, ": %s", t)
	}
	var full strings.Builder
	full.WriteString(head.String())
	if from != "" && from != g.Kind() {
		fmt.Fprintf(&full, "\n作者：%s", from)
	}
	if it.Conclusion != "" {
		fmt.Fprintf(&full, "\n结果：%s", it.Conclusion)
	}
	if b := strings.TrimSpace(it.Body); b != "" {
		fmt.Fprintf(&full, "\n内容：%s", clip(b, 1200))
	}
	if u := strings.TrimSpace(it.URL); u != "" {
		fmt.Fprintf(&full, "\n链接：%s", u)
	}

	mode := normalizeHandleMode(fc.HandleMode)
	switch mode {
	case forgeModeNotify:
		summary := head.String()
		if u := strings.TrimSpace(it.URL); u != "" {
			summary += " " + u
		}
		tone := "info"
		if normalizeEventKind(it.Kind) == "ci" {
			tone = "error"
		}
		return sink.EmitEvent(ctx, ExternalEvent{
			Source:     g.Kind(),
			DedupKey:   dedup,
			Thread:     thread,
			From:       from,
			Text:       full.String(),
			Ref:        it.URL,
			Action:     "notify",
			NotifyOnly: true,
			Summary:    summary,
			Tone:       tone,
		})

	case forgeModeAgent:
		subs := g.activeSubscribers(thread)
		if len(subs) > 0 {
			text := full.String() + fmt.Sprintf("\n\n[处理模式：已订阅 %s 的 Agent 跟进 · 线程 %s]", g.termPR(), thread)
			return sink.EmitEvent(ctx, ExternalEvent{
				Source:   g.Kind(),
				DedupKey: dedup,
				Thread:   thread,
				From:     from,
				Text:     text,
				Ref:      it.URL,
				Action:   "respond",
				Target:   "", // empty -> Super.tick() fast-path delivers directly to Thread.Subscribers
			})
		}
		// No active subscribed worker: degrade cleanly to advise mode.
		guardrail := fmt.Sprintf("\n\n[处理模式：未找到订阅 %s 的在线 Agent，自动降级为只读调研建议 · 严禁未经人类确认擅自评论、审批、关闭或合并；仅做只读调研后通过 collie reply 汇报结论并附带 --opt 拍板选项]", thread)
		return sink.EmitEvent(ctx, ExternalEvent{
			Source:   g.Kind(),
			DedupKey: dedup,
			Thread:   thread,
			From:     from,
			Text:     full.String() + guardrail,
			Ref:      it.URL,
			Action:   "respond",
			Target:   routerName,
		})

	default: // advise
		guardrail := fmt.Sprintf("\n\n[处理模式：自动调研出建议 · 严禁未经人类确认擅自在远端发表评论、审批、关闭或合并；仅做只读调研（看 diff / 评论 / 失败日志）后通过 collie reply 汇报结论并附带 --opt 拍板选项；稳定线程：%s]", thread)
		return sink.EmitEvent(ctx, ExternalEvent{
			Source:   g.Kind(),
			DedupKey: dedup,
			Thread:   thread,
			From:     from,
			Text:     full.String() + guardrail,
			Ref:      it.URL,
			Action:   "respond",
			Target:   routerName,
		})
	}
}

// ---------------------------------------------------------------- noise filters

func filterForgeItems(items []ForgeItem, fc ForgeConfig, selfUser string, watermark time.Time, enforceWatermark bool) []ForgeItem {
	ignoreBots := true
	if fc.IgnoreBots != nil {
		ignoreBots = *fc.IgnoreBots
	}
	seenKey := make(map[string]bool, len(items))
	out := make([]ForgeItem, 0, len(items))
	for _, it := range items {
		kind := normalizeEventKind(it.Kind)
		if !eventEnabled(fc.Events, kind) {
			continue
		}
		if enforceWatermark && !watermark.IsZero() && !it.At.IsZero() && !it.At.After(watermark) {
			continue
		}
		if kind == "ci" {
			if it.Conclusion != "" && !isCIFailureConclusion(it.Conclusion) {
				continue
			}
		} else {
			if selfUser != "" && it.Author != "" && strings.EqualFold(strings.TrimSpace(it.Author), selfUser) {
				continue
			}
			if ignoreBots && (it.AuthorBot || isCommonBot(it.Author)) {
				continue
			}
		}
		key := strings.TrimSpace(it.DedupKey)
		if key != "" {
			if seenKey[key] {
				continue
			}
			seenKey[key] = true
		}
		out = append(out, it)
	}
	return out
}

func normalizeEventKind(k string) string {
	switch strings.ToLower(strings.TrimSpace(k)) {
	case "pr", "pull_request", "pullrequest":
		return "pr"
	case "mr", "merge_request", "mergerequest":
		return "mr"
	case "ci", "check", "checks", "check_run", "check_suite", "workflow_run", "pipeline":
		return "ci"
	case "mention", "comment":
		return "mention"
	case "issue", "issues":
		return "issue"
	default:
		return "pr"
	}
}

func eventEnabled(events map[string]bool, kind string) bool {
	if len(events) == 0 {
		return true
	}
	switch kind {
	case "pr", "mr":
		v1, ok1 := events["pr"]
		v2, ok2 := events["mr"]
		if !ok1 && !ok2 {
			return true
		}
		return v1 || v2
	case "issue", "mention":
		v1, ok1 := events["issue"]
		v2, ok2 := events["mention"]
		if !ok1 && !ok2 {
			return true
		}
		return v1 || v2
	case "ci":
		v, ok := events["ci"]
		if !ok {
			return true
		}
		return v
	default:
		return true
	}
}

func isCIFailureConclusion(concl string) bool {
	switch strings.ToLower(strings.TrimSpace(concl)) {
	case "failure", "failed", "fail", "timed_out", "timeout", "startup_failure", "action_required", "error", "errored":
		return true
	default:
		return false
	}
}

func isCommonBot(author string) bool {
	a := strings.ToLower(strings.TrimSpace(author))
	if a == "" {
		return false
	}
	if strings.HasSuffix(a, "[bot]") || strings.HasSuffix(a, "-bot") || strings.HasSuffix(a, "_bot") {
		return true
	}
	switch a {
	case "dependabot", "renovate", "github-actions", "codecov", "sonarcloud",
		"mergify", "bors", "kodiak", "vercel", "netlify", "allcontributors",
		"pre-commit-ci", "greenkeeper", "imgbot", "stale", "ghost":
		return true
	}
	return false
}

func defaultItemTag(kind, termPR string) string {
	switch normalizeEventKind(kind) {
	case "ci":
		return "CI 失败"
	case "mention":
		return "评论 @我"
	case "issue":
		return "Issue 讨论"
	case "mr":
		return "MR 评审"
	default:
		return termPR + " 动态"
	}
}
