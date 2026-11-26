package collie

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	larksdk "github.com/larksuite/oapi-sdk-go/v3"
	larkcore "github.com/larksuite/oapi-sdk-go/v3/core"
	larkdispatcher "github.com/larksuite/oapi-sdk-go/v3/event/dispatcher"
	larkcallback "github.com/larksuite/oapi-sdk-go/v3/event/dispatcher/callback"
	larkim "github.com/larksuite/oapi-sdk-go/v3/service/im/v1"
	larkws "github.com/larksuite/oapi-sdk-go/v3/ws"
)

const (
	feishuKind              = "feishu"
	feishuInstantSyncWindow = 15 * time.Minute
	feishuDefaultPairWindow = 2 * time.Minute
	feishuMaxTrackedOrigins = 200
	feishuReactionEmoji     = "OK"
	feishuGroupFeedbackTag  = "[群只读反馈 · 仅调研出建议，严禁未经确认直接执行或向群内回复]"
	feishuSenderCacheTTL    = time.Hour
	feishuMaxSenderCache    = 256
	feishuQuoteMaxRunes     = 1000
)

var (
	feishuQuoteFetchTimeout  = 8 * time.Second
	feishuQuoteImageTimeout  = 15 * time.Second
	feishuQuoteTotalTimeout  = 25 * time.Second
	feishuQuoteMaxImageBytes = int64(20 << 20)
	feishuQuoteMaxImages     = 4
)

// FeishuConfig holds the non-sensitive settings for Feishu/Lark WebSocket mode.
// AppSecret is strictly stored in SecretStore (~/.collie/secrets.json, 0600)
// and never appears in this struct or SQLite.
type FeishuConfig struct {
	Region           string   `json:"region"` // "cn" (default) | "intl"
	AppID            string   `json:"app_id"`
	AllowOpenIDs     []string `json:"allow_open_ids"`
	FeedbackChatIDs  []string `json:"feedback_chat_ids,omitempty"`
	GroupMentionOnly bool     `json:"group_mention_only"`
	InstantChatSync  bool     `json:"instant_chat_sync"`
}

func defaultFeishuConfig() FeishuConfig {
	return FeishuConfig{
		Region:           "cn",
		GroupMentionOnly: true,
		InstantChatSync:  true,
	}
}

func splitFeishuIDs(s string, dst []string) []string {
	for _, part := range strings.FieldsFunc(s, func(r rune) bool {
		return r == ',' || r == '，' || r == ';' || r == '；' || r == '\n' || r == '\r' || r == ' ' || r == '\t'
	}) {
		part = strings.TrimSpace(part)
		if part != "" && !slices.Contains(dst, part) {
			dst = append(dst, part)
		}
	}
	return dst
}

func parseFeishuIDList(raw json.RawMessage, extra string) []string {
	var ids []string
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) > 0 && string(trimmed) != "null" {
		var list []string
		if err := json.Unmarshal(trimmed, &list); err == nil {
			for _, item := range list {
				ids = splitFeishuIDs(item, ids)
			}
		} else {
			var single string
			if err := json.Unmarshal(trimmed, &single); err == nil {
				ids = splitFeishuIDs(single, ids)
			}
		}
	}
	if strings.TrimSpace(extra) != "" {
		ids = splitFeishuIDs(extra, ids)
	}
	return ids
}

func parseFeishuConfig(raw json.RawMessage) FeishuConfig {
	cfg := defaultFeishuConfig()
	if len(bytes.TrimSpace(raw)) == 0 || string(bytes.TrimSpace(raw)) == "null" {
		return cfg
	}
	// Accept both array ([]string) and single comma/newline-separated string forms.
	var wire struct {
		Region           *string         `json:"region"`
		AppID            *string         `json:"app_id"`
		AllowOpenIDs     json.RawMessage `json:"allow_open_ids"`
		AllowOpenID      string          `json:"allow_open_id"`
		FeedbackChatIDs  json.RawMessage `json:"feedback_chat_ids"`
		FeedbackChatID   string          `json:"feedback_chat_id"`
		GroupMentionOnly *bool           `json:"group_mention_only"`
		InstantChatSync  *bool           `json:"instant_chat_sync"`
	}
	if err := json.Unmarshal(raw, &wire); err != nil {
		return cfg
	}
	if wire.Region != nil {
		if r := strings.ToLower(strings.TrimSpace(*wire.Region)); r == "intl" || r == "lark" {
			cfg.Region = "intl"
		} else {
			cfg.Region = "cn"
		}
	}
	if wire.AppID != nil {
		cfg.AppID = strings.TrimSpace(*wire.AppID)
	}
	cfg.AllowOpenIDs = parseFeishuIDList(wire.AllowOpenIDs, wire.AllowOpenID)
	cfg.FeedbackChatIDs = parseFeishuIDList(wire.FeedbackChatIDs, wire.FeedbackChatID)
	if wire.GroupMentionOnly != nil {
		cfg.GroupMentionOnly = *wire.GroupMentionOnly
	}
	if wire.InstantChatSync != nil {
		cfg.InstantChatSync = *wire.InstantChatSync
	}
	return cfg
}

func feishuBaseURL(region, override string) string {
	if strings.TrimSpace(override) != "" {
		return strings.TrimRight(override, "/")
	}
	if strings.EqualFold(strings.TrimSpace(region), "intl") {
		return larksdk.LarkBaseUrl
	}
	return larksdk.FeishuBaseUrl
}

type feishuRoute struct {
	at        time.Time
	chatID    string
	chatType  string // "p2p" | "group"
	messageID string
	openID    string
}

type feishuSenderCache struct {
	name string
	at   time.Time
}

// FeishuAdapter implements Integration, OutboundIntegration, and
// IntegrationDescriptor using the official Feishu/Lark WebSocket SDK.
type FeishuAdapter struct {
	sp  *Super
	mgr *IntegrationManager

	// Test-only overrides (never set in production).
	baseURLOverride string
	httpClient      *http.Client

	mu           sync.RWMutex
	activeCfg    FeishuConfig
	activeSecret string
	activeClient *larksdk.Client
	activeSink   EventSink
	botOpenID    string
	botName      string

	lastInbound    feishuRoute
	lastP2P        feishuRoute
	lastFeedbackAt time.Time
	origins        map[string]feishuRoute // entry_id -> originating Feishu chat/message
	originOrder    []string
	senderNames    map[string]feishuSenderCache

	pairUntil    time.Time
	pairCaptured string
}

func newFeishuAdapter(sp *Super, mgr *IntegrationManager) *FeishuAdapter {
	return &FeishuAdapter{
		sp:          sp,
		mgr:         mgr,
		origins:     map[string]feishuRoute{},
		senderNames: map[string]feishuSenderCache{},
	}
}

func (f *FeishuAdapter) Kind() string { return feishuKind }

func (f *FeishuAdapter) Meta() IntegrationMeta {
	return IntegrationMeta{
		Kind:        feishuKind,
		Name:        "飞书机器人",
		Subtitle:    "长连接双向对话 · 零公网门槛",
		Description: "在飞书私信或群里 @Collie 直接派活，回复与选项按钮自动同步，点一下就能拍板。",
		Order:       10,
	}
}

func (f *FeishuAdapter) IsConfigured(cfg IntegrationConfig) bool {
	fc := parseFeishuConfig(cfg.Config)
	appID := fc.AppID
	if appID == "" {
		appID = strings.TrimSpace(cfg.Secrets["app_id"])
	}
	secret := strings.TrimSpace(cfg.Secrets["app_secret"])
	return appID != "" && secret != ""
}

func (f *FeishuAdapter) SecretSet(cfg IntegrationConfig) bool {
	return strings.TrimSpace(cfg.Secrets["app_secret"]) != ""
}

func (f *FeishuAdapter) SanitizeConfig(cfg IntegrationConfig) map[string]any {
	fc := parseFeishuConfig(cfg.Config)
	if fc.AppID == "" {
		fc.AppID = strings.TrimSpace(cfg.Secrets["app_id"])
	}
	allowSingle := ""
	if len(fc.AllowOpenIDs) > 0 {
		allowSingle = strings.Join(fc.AllowOpenIDs, ", ")
	}
	feedbackSingle := ""
	if len(fc.FeedbackChatIDs) > 0 {
		feedbackSingle = strings.Join(fc.FeedbackChatIDs, ", ")
	}
	return map[string]any{
		"region":             fc.Region,
		"app_id":             fc.AppID,
		"allow_open_ids":     fc.AllowOpenIDs,
		"allow_open_id":      allowSingle,
		"feedback_chat_ids":  fc.FeedbackChatIDs,
		"feedback_chat_id":   feedbackSingle,
		"group_mention_only": fc.GroupMentionOnly,
		"instant_chat_sync":  fc.InstantChatSync,
	}
}

func (f *FeishuAdapter) ValidateSave(raw json.RawMessage) (json.RawMessage, error) {
	fc := parseFeishuConfig(raw)
	b, err := json.Marshal(fc)
	if err != nil {
		return nil, err
	}
	return b, nil
}

func (f *FeishuAdapter) Health() IntegrationHealth {
	if f.mgr != nil {
		return f.mgr.Health(feishuKind)
	}
	return IntegrationHealth{State: HealthUnconfigured}
}

func (f *FeishuAdapter) ExtraView() map[string]any {
	now := time.Now()
	f.mu.RLock()
	active := !f.pairUntil.IsZero() && now.Before(f.pairUntil)
	exp := f.pairUntil
	captured := f.pairCaptured
	botName := f.botName
	f.mu.RUnlock()
	p := map[string]any{"active": active}
	if active {
		p["expires_at"] = exp
		p["remaining_sec"] = int(exp.Sub(now).Seconds())
	}
	if captured != "" {
		p["captured_open_id"] = captured
	}
	out := map[string]any{"pairing": p}
	if botName != "" {
		out["bot_name"] = botName
	}
	return out
}

// StartPairing opens a temporary window during which the next private message
// sent to the bot automatically binds the sender's OpenID to the allowlist.
func (f *FeishuAdapter) StartPairing(d time.Duration) (time.Time, error) {
	if d <= 0 {
		d = feishuDefaultPairWindow
	}
	if f.mgr != nil {
		v, ok := f.mgr.View(feishuKind)
		if !ok || !v.Configured {
			return time.Time{}, errors.New("请先填写并保存 App ID 与 App Secret")
		}
		if !v.Enabled {
			return time.Time{}, errors.New("请先开启「飞书机器人」开关并等待长连接在线")
		}
	}
	exp := time.Now().Add(d)
	f.mu.Lock()
	f.pairUntil = exp
	f.pairCaptured = ""
	f.mu.Unlock()
	if f.mgr != nil {
		f.mgr.notifyChanged()
	}
	return exp, nil
}

// StopPairing cancels an active pairing window.
func (f *FeishuAdapter) StopPairing() {
	f.mu.Lock()
	f.pairUntil = time.Time{}
	f.mu.Unlock()
	if f.mgr != nil {
		f.mgr.notifyChanged()
	}
}

func (f *FeishuAdapter) newSDKClient(appID, appSecret, region string) *larksdk.Client {
	opts := []larksdk.ClientOptionFunc{
		larksdk.WithOpenBaseUrl(feishuBaseURL(region, f.baseURLOverride)),
		larksdk.WithLogger(f.newLogger()),
		larksdk.WithLogLevel(larkcore.LogLevelWarn),
		larksdk.WithReqTimeout(15 * time.Second),
	}
	if f.httpClient != nil {
		opts = append(opts, larksdk.WithHttpClient(f.httpClient))
	}
	return larksdk.NewClient(appID, appSecret, opts...)
}

func (f *FeishuAdapter) newLogger() larkcore.Logger {
	return &redactedLarkLogger{secrets: f.secretsStore()}
}

func (f *FeishuAdapter) secretsStore() *SecretStore {
	if f.mgr != nil {
		return f.mgr.secrets
	}
	return nil
}

// redactedLarkLogger ensures the Feishu SDK never prints raw secrets or tokens.
type redactedLarkLogger struct {
	secrets *SecretStore
}

func (l *redactedLarkLogger) scrub(args ...interface{}) string {
	s := fmt.Sprint(args...)
	if l.secrets != nil {
		s = l.secrets.Redact(s)
	}
	return s
}

func (l *redactedLarkLogger) Debug(ctx context.Context, args ...interface{}) {}
func (l *redactedLarkLogger) Info(ctx context.Context, args ...interface{})  {}
func (l *redactedLarkLogger) Warn(ctx context.Context, args ...interface{}) {
	log.Printf("feishu-ws warn: %s", l.scrub(args...))
}
func (l *redactedLarkLogger) Error(ctx context.Context, args ...interface{}) {
	log.Printf("feishu-ws error: %s", l.scrub(args...))
}

// Run establishes a single outbound Feishu WebSocket session and blocks until
// ctx is canceled or the connection ends. Transient drops return a regular
// error (driven by IntegrationManager backoff); permanent auth/config errors
// wrap ErrAuthFailed to trip the circuit breaker immediately.
func (f *FeishuAdapter) Run(ctx context.Context, cfg IntegrationConfig, sink EventSink) error {
	fc := parseFeishuConfig(cfg.Config)
	if fc.AppID == "" {
		fc.AppID = strings.TrimSpace(cfg.Secrets["app_id"])
	}
	secret := strings.TrimSpace(cfg.Secrets["app_secret"])
	if fc.AppID == "" || secret == "" {
		return fmt.Errorf("%w: 请先填写 App ID 和 App Secret", ErrAuthFailed)
	}

	apiClient := f.newSDKClient(fc.AppID, secret, fc.Region)
	f.mu.Lock()
	f.activeCfg = fc
	f.activeSecret = secret
	f.activeClient = apiClient
	f.activeSink = sink
	f.mu.Unlock()

	defer func() {
		f.mu.Lock()
		if f.activeClient == apiClient {
			f.activeClient = nil
			f.activeSink = nil
		}
		f.mu.Unlock()
	}()

	disp := f.buildDispatcher(sink)
	wsOpts := []larkws.ClientOption{
		larkws.WithDomain(feishuBaseURL(fc.Region, f.baseURLOverride)),
		larkws.WithEventHandler(disp),
		larkws.WithAutoReconnect(false),
		larkws.WithLogger(f.newLogger()),
		larkws.WithLogLevel(larkcore.LogLevelWarn),
		larkws.WithOnReady(func() {
			sink.ReportHealth(HealthConnected, "长连接在线")
			go f.refreshBotInfo(ctx, apiClient)
		}),
		larkws.WithOnDisconnected(func() {
			if ctx.Err() == nil {
				sink.ReportHealth(HealthConnecting, "长连接断开，准备重连…")
			}
		}),
	}
	if f.httpClient != nil {
		wsOpts = append(wsOpts, larkws.WithHttpClient(f.httpClient))
	}

	wsClient := larkws.NewClient(fc.AppID, secret, wsOpts...)
	sink.ReportHealth(HealthConnecting, "正在握手飞书长连接…")

	err := wsClient.Start(ctx)
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if err == nil {
		return nil
	}
	return classifyFeishuWSError(err)
}

func classifyFeishuWSError(err error) error {
	if err == nil {
		return nil
	}
	var ce *larkws.ClientError
	if errors.As(err, &ce) {
		switch ce.Code {
		case larkws.ExceedConnLimit:
			return fmt.Errorf("%w: 该飞书应用的并发长连接数已达上限，请关闭多余实例后重试", ErrAuthFailed)
		case larkws.Forbidden:
			return fmt.Errorf("%w: 飞书拒绝长连接握手（403），请核对应用权限与事件订阅方式（需选择「使用长连接接收事件」）", ErrAuthFailed)
		default:
			msg := strings.TrimSpace(ce.Msg)
			if msg == "" {
				msg = "请核对 App ID、App Secret 与平台区域"
			}
			return fmt.Errorf("%w: 飞书长连接鉴权失败（错误码 %d）：%s", ErrAuthFailed, ce.Code, msg)
		}
	}
	return err
}

func (f *FeishuAdapter) buildDispatcher(sink EventSink) *larkdispatcher.EventDispatcher {
	disp := larkdispatcher.NewEventDispatcher("", "")
	disp.OnP2MessageReceiveV1(func(ctx context.Context, event *larkim.P2MessageReceiveV1) error {
		return f.handleMessageReceive(ctx, event, sink)
	})
	disp.OnP2CardActionTrigger(func(ctx context.Context, event *larkcallback.CardActionTriggerEvent) (*larkcallback.CardActionTriggerResponse, error) {
		return f.handleCardAction(ctx, event, sink)
	})
	return disp
}

type feishuBotInfo struct {
	Code int    `json:"code"`
	Msg  string `json:"msg"`
	Bot  struct {
		AppName string `json:"app_name"`
		OpenID  string `json:"open_id"`
	} `json:"bot"`
}

func (f *FeishuAdapter) fetchBotInfo(ctx context.Context, client *larksdk.Client) (openID, name string, err error) {
	reqCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	resp, err := client.Do(reqCtx, &larkcore.ApiReq{
		HttpMethod:                http.MethodGet,
		ApiPath:                   "/open-apis/bot/v3/info",
		SupportedAccessTokenTypes: []larkcore.AccessTokenType{larkcore.AccessTokenTypeTenant},
	})
	if err != nil {
		return "", "", err
	}
	if resp == nil {
		return "", "", errors.New("empty response from Feishu bot info API")
	}
	var info feishuBotInfo
	if err := json.Unmarshal(resp.RawBody, &info); err != nil {
		return "", "", fmt.Errorf("invalid bot info response (HTTP %d)", resp.StatusCode)
	}
	if resp.StatusCode >= 400 || info.Code != 0 {
		msg := strings.TrimSpace(info.Msg)
		if msg == "" {
			msg = fmt.Sprintf("HTTP %d / code %d", resp.StatusCode, info.Code)
		}
		return "", "", fmt.Errorf("%w: %s", ErrAuthFailed, msg)
	}
	return strings.TrimSpace(info.Bot.OpenID), strings.TrimSpace(info.Bot.AppName), nil
}

func (f *FeishuAdapter) refreshBotInfo(ctx context.Context, client *larksdk.Client) {
	openID, name, err := f.fetchBotInfo(ctx, client)
	if err != nil {
		return
	}
	f.mu.Lock()
	if openID != "" {
		f.botOpenID = openID
	}
	if name != "" {
		f.botName = name
	}
	f.mu.Unlock()
}

// ---------------------------------------------------------------- inbound messages

func (f *FeishuAdapter) allowSender(cfg FeishuConfig, openID string) bool {
	if len(cfg.AllowOpenIDs) == 0 {
		return true
	}
	return openID != "" && slices.Contains(cfg.AllowOpenIDs, openID)
}

func (f *FeishuAdapter) isFeedbackChat(cfg FeishuConfig, chatID string) bool {
	return chatID != "" && slices.Contains(cfg.FeedbackChatIDs, chatID)
}

func (f *FeishuAdapter) fetchSenderName(ctx context.Context, client *larksdk.Client, openID string) string {
	if client == nil || openID == "" {
		return ""
	}
	reqCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	resp, err := client.Do(reqCtx, &larkcore.ApiReq{
		HttpMethod:                http.MethodGet,
		ApiPath:                   "/open-apis/contact/v3/users/" + url.PathEscape(openID),
		QueryParams:               larkcore.QueryParams{"user_id_type": []string{"open_id"}},
		SupportedAccessTokenTypes: []larkcore.AccessTokenType{larkcore.AccessTokenTypeTenant},
	})
	if err != nil || resp == nil || resp.StatusCode >= 400 {
		return ""
	}
	var body struct {
		Code int `json:"code"`
		Data struct {
			User struct {
				Name     string `json:"name"`
				Nickname string `json:"nickname"`
				EnName   string `json:"en_name"`
			} `json:"user"`
		} `json:"data"`
	}
	if err := json.Unmarshal(resp.RawBody, &body); err != nil || body.Code != 0 {
		return ""
	}
	for _, cand := range []string{body.Data.User.Name, body.Data.User.Nickname, body.Data.User.EnName} {
		if s := strings.TrimSpace(cand); s != "" {
			return s
		}
	}
	return ""
}

func (f *FeishuAdapter) resolveSenderLabel(ctx context.Context, client *larksdk.Client, openID string) (label, from string) {
	openID = strings.TrimSpace(openID)
	if openID == "" {
		return "匿名用户", "feishu-group"
	}
	masked := maskIdentifier(openID)
	now := time.Now()

	f.mu.RLock()
	cached, ok := f.senderNames[openID]
	f.mu.RUnlock()

	var name string
	if ok && now.Sub(cached.at) < feishuSenderCacheTTL {
		name = cached.name
	} else {
		name = f.fetchSenderName(ctx, client, openID)
		f.mu.Lock()
		if f.senderNames == nil {
			f.senderNames = map[string]feishuSenderCache{}
		}
		if len(f.senderNames) >= feishuMaxSenderCache {
			for k, v := range f.senderNames {
				if now.Sub(v.at) >= feishuSenderCacheTTL || len(f.senderNames) >= feishuMaxSenderCache {
					delete(f.senderNames, k)
					if len(f.senderNames) < feishuMaxSenderCache {
						break
					}
				}
			}
		}
		f.senderNames[openID] = feishuSenderCache{name: name, at: now}
		f.mu.Unlock()
	}
	if name != "" {
		return fmt.Sprintf("%s (%s)", name, masked), name
	}
	return masked, masked
}

func formatFeishuGroupFeedback(senderLabel, chatID, text string) string {
	var b strings.Builder
	b.WriteString(feishuGroupFeedbackTag)
	fmt.Fprintf(&b, "\n发送者：%s", senderLabel)
	if chatID != "" {
		fmt.Fprintf(&b, "\n群 ID：%s", chatID)
	}
	fmt.Fprintf(&b, "\n反馈内容：\n%s", strings.TrimSpace(text))
	return b.String()
}

func (f *FeishuAdapter) isBotMentioned(mentions []*larkim.MentionEvent, botOpenID, botName string) bool {
	for _, m := range mentions {
		if m == nil {
			continue
		}
		if botOpenID != "" && m.Id != nil && deref(m.Id.OpenId) == botOpenID {
			return true
		}
		if m.MentionedType != nil && strings.EqualFold(*m.MentionedType, "bot") {
			if botOpenID == "" || (m.Id != nil && deref(m.Id.OpenId) == botOpenID) {
				return true
			}
		}
		if botOpenID == "" && m.Name != nil {
			n := strings.TrimSpace(*m.Name)
			if (botName != "" && strings.EqualFold(n, botName)) || strings.EqualFold(n, "collie") {
				return true
			}
		}
	}
	return false
}

func stripFeishuMentions(text string, mentions []*larkim.MentionEvent) string {
	out := text
	for _, m := range mentions {
		if m == nil || m.Key == nil || *m.Key == "" {
			continue
		}
		out = strings.ReplaceAll(out, *m.Key, " ")
	}
	lines := strings.Split(out, "\n")
	for i, l := range lines {
		lines[i] = strings.TrimSpace(l)
	}
	return strings.TrimSpace(strings.Join(lines, "\n"))
}

func (f *FeishuAdapter) handleMessageReceive(ctx context.Context, event *larkim.P2MessageReceiveV1, sink EventSink) error {
	if event == nil || event.Event == nil || event.Event.Message == nil {
		return nil
	}
	sender := event.Event.Sender
	if sender != nil && sender.SenderType != nil && strings.EqualFold(*sender.SenderType, "bot") {
		return nil // ignore bot messages to prevent loops
	}
	senderOpenID := ""
	if sender != nil && sender.SenderId != nil {
		senderOpenID = strings.TrimSpace(deref(sender.SenderId.OpenId))
	}
	msg := event.Event.Message
	chatID := strings.TrimSpace(deref(msg.ChatId))
	chatType := strings.ToLower(strings.TrimSpace(deref(msg.ChatType)))
	messageID := strings.TrimSpace(deref(msg.MessageId))
	parentID := strings.TrimSpace(deref(msg.ParentId))
	msgType := strings.ToLower(strings.TrimSpace(deref(msg.MessageType)))
	eventID := ""
	if event.EventV2Base != nil && event.EventV2Base.Header != nil {
		eventID = strings.TrimSpace(event.EventV2Base.Header.EventID)
	}
	if eventID == "" {
		eventID = messageID
	}

	f.mu.RLock()
	cfg := f.activeCfg
	botOpenID := f.botOpenID
	botName := f.botName
	client := f.activeClient
	pairingActive := !f.pairUntil.IsZero() && time.Now().Before(f.pairUntil)
	f.mu.RUnlock()

	// 1. Temporary 2-minute pairing capture (private chat only).
	if pairingActive && chatType == "p2p" && senderOpenID != "" {
		f.completePairing(ctx, client, chatID, messageID, senderOpenID)
		return nil
	}

	// 2. OpenID allowlist & read-only feedback group routing.
	allowed := f.allowSender(cfg, senderOpenID)
	isFeedback := !allowed && chatType == "group" && f.isFeedbackChat(cfg, chatID)
	if !allowed && !isFeedback {
		log.Printf("feishu-ws: ignored message from non-allowlisted sender %s", maskIdentifier(senderOpenID))
		return nil
	}
	if isFeedback {
		// Non-allowlisted members in feedback groups must explicitly @Collie;
		// normal group chatter is silently dropped to prevent context explosion.
		if !f.isBotMentioned(msg.Mentions, botOpenID, botName) {
			return nil
		}
		text, err := f.extractInboundText(ctx, client, messageID, parentID, msgType, deref(msg.Content), msg.Mentions)
		if err != nil {
			log.Printf("feishu-ws: extract feedback content (%s): %v", msgType, err)
		}
		text = strings.TrimSpace(text)
		if text == "" {
			return nil
		}
		senderLabel, senderFrom := f.resolveSenderLabel(ctx, client, senderOpenID)
		f.recordFeedbackEvent(time.Now())
		if sink != nil {
			if _, err := sink.EmitEvent(ctx, ExternalEvent{
				Source:   feishuKind,
				DedupKey: eventID,
				Thread:   mainThread,
				From:     senderFrom,
				Text:     formatFeishuGroupFeedback(senderLabel, chatID, text),
				Ref:      chatID,
				Action:   "respond",
				Target:   routerName,
			}); err != nil {
				return err
			}
		}
		// Never record group route, bind origin, react, or auto-reply into the feedback group.
		return nil
	}

	// 3. Group chat @Collie guard.
	if chatType == "group" && cfg.GroupMentionOnly {
		if !f.isBotMentioned(msg.Mentions, botOpenID, botName) {
			return nil
		}
	}

	// 4. Extract message text, attachments, and optional quoted parent message.
	text, err := f.extractInboundText(ctx, client, messageID, parentID, msgType, deref(msg.Content), msg.Mentions)
	if err != nil {
		log.Printf("feishu-ws: extract content (%s): %v", msgType, err)
	}
	text = strings.TrimSpace(text)
	if text == "" {
		return nil
	}

	route := feishuRoute{
		at:        time.Now(),
		chatID:    chatID,
		chatType:  chatType,
		messageID: messageID,
		openID:    senderOpenID,
	}
	f.recordInboundRoute(route)

	if sink != nil {
		if err := sink.EmitHuman(ctx, HumanEvent{
			Source:    feishuKind,
			DedupKey:  eventID,
			Thread:    mainThread,
			From:      "feishu",
			Text:      text,
			ChatID:    chatID,
			MessageID: messageID,
			ChatType:  chatType,
		}); err != nil {
			return err
		}
		if eid := f.latestFeishuUserEntryID(); eid != "" {
			f.bindEntryOrigin(eid, route)
		}
	}

	// 5. Async lightweight emoji reaction so the sender knows Collie received it.
	if client != nil && messageID != "" {
		go f.addReceivedReaction(client, messageID)
	}
	return nil
}

func (f *FeishuAdapter) completePairing(ctx context.Context, client *larksdk.Client, chatID, messageID, senderOpenID string) {
	f.mu.Lock()
	if !slices.Contains(f.activeCfg.AllowOpenIDs, senderOpenID) {
		f.activeCfg.AllowOpenIDs = append(f.activeCfg.AllowOpenIDs, senderOpenID)
	}
	updatedCfg := f.activeCfg
	f.pairUntil = time.Time{}
	f.pairCaptured = senderOpenID
	f.mu.Unlock()

	// Persist allowlist without restarting the active WebSocket connection.
	if f.mgr != nil {
		b, _ := json.Marshal(updatedCfg)
		f.mgr.mu.Lock()
		rec := f.mgr.records[feishuKind]
		rec.Kind = feishuKind
		rec.Config = b
		rec.UpdatedAt = f.mgr.now().Unix()
		f.mgr.records[feishuKind] = rec
		f.mgr.mu.Unlock()
		_ = saveIntegration(f.mgr.db, rec)
		f.mgr.notifyChanged()
	}

	if client != nil && chatID != "" {
		go func() {
			cctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			card := buildFeishuNoticeCard("Collie · 配对成功", "green", "已将你的飞书账号加入 Collie 安全白名单，现在可以直接私信或在群里 @Collie 对话。", f.jumpLink())
			_, _ = f.sendCard(cctx, client, "chat_id", chatID, "", card)
		}()
	}
}

type feishuQuotedParent struct {
	text          string
	attachments   []string
	failedImages  int
	omittedImages int
}

func (f *FeishuAdapter) extractInboundText(ctx context.Context, client *larksdk.Client, messageID, parentID, msgType, rawContent string, mentions []*larkim.MentionEvent) (string, error) {
	mainText, mainPaths, err := f.extractMessageContentParts(ctx, client, messageID, msgType, rawContent, mentions)
	var quote *feishuQuotedParent
	if parentID != "" && parentID != messageID && client != nil {
		quote = f.fetchQuotedParent(ctx, client, parentID)
	}
	return composeFeishuInboundText(mainText, mainPaths, quote), err
}

func (f *FeishuAdapter) extractMessageContent(ctx context.Context, client *larksdk.Client, messageID, msgType, rawContent string, mentions []*larkim.MentionEvent) (string, error) {
	mainText, mainPaths, err := f.extractMessageContentParts(ctx, client, messageID, msgType, rawContent, mentions)
	return composeFeishuInboundText(mainText, mainPaths, nil), err
}

func (f *FeishuAdapter) extractMessageContentParts(ctx context.Context, client *larksdk.Client, messageID, msgType, rawContent string, mentions []*larkim.MentionEvent) (string, []string, error) {
	switch msgType {
	case "text":
		var body struct {
			Text string `json:"text"`
		}
		if err := json.Unmarshal([]byte(rawContent), &body); err != nil {
			return "", nil, err
		}
		return stripFeishuMentions(body.Text, mentions), nil, nil
	case "image":
		var body struct {
			ImageKey string `json:"image_key"`
		}
		if err := json.Unmarshal([]byte(rawContent), &body); err != nil || body.ImageKey == "" {
			return "[飞书图片]", nil, err
		}
		saved, err := f.downloadImage(ctx, client, messageID, body.ImageKey)
		if err != nil {
			return "[飞书图片（下载失败）]", nil, err
		}
		return "", []string{saved}, nil
	case "post":
		text, imgKeys := parseFeishuPostContent(rawContent)
		text = stripFeishuMentions(text, mentions)
		var paths []string
		for _, k := range imgKeys {
			if p, err := f.downloadImage(ctx, client, messageID, k); err == nil && p != "" {
				paths = append(paths, p)
			}
		}
		return text, paths, nil
	default:
		if msgType == "" {
			return "", nil, nil
		}
		return fmt.Sprintf("[飞书 %s 消息]", msgType), nil, nil
	}
}

func replaceFeishuMessageMentions(text string, mentions []*larkim.Mention) string {
	out := text
	for _, m := range mentions {
		if m == nil || m.Key == nil || *m.Key == "" {
			continue
		}
		repl := " "
		if name := strings.TrimSpace(deref(m.Name)); name != "" {
			repl = "@" + name
		}
		out = strings.ReplaceAll(out, *m.Key, repl)
	}
	lines := strings.Split(out, "\n")
	for i, l := range lines {
		lines[i] = strings.TrimSpace(l)
	}
	return strings.TrimSpace(strings.Join(lines, "\n"))
}

func dedupNonEmpty(items []string) []string {
	var out []string
	for _, it := range items {
		it = strings.TrimSpace(it)
		if it != "" && !slices.Contains(out, it) {
			out = append(out, it)
		}
	}
	return out
}

func (f *FeishuAdapter) fetchQuotedParent(ctx context.Context, client *larksdk.Client, parentID string) *feishuQuotedParent {
	if client == nil || parentID == "" {
		return nil
	}
	totalTimeout := feishuQuoteTotalTimeout
	if totalTimeout <= 0 {
		totalTimeout = 25 * time.Second
	}
	totalCtx, totalCancel := context.WithTimeout(ctx, totalTimeout)
	defer totalCancel()

	fetchTimeout := feishuQuoteFetchTimeout
	if fetchTimeout <= 0 {
		fetchTimeout = 8 * time.Second
	}
	getCtx, getCancel := context.WithTimeout(totalCtx, fetchTimeout)
	req := larkim.NewGetMessageReqBuilder().MessageId(parentID).Build()
	resp, err := client.Im.V1.Message.Get(getCtx, req)
	getCancel()
	if err != nil {
		log.Printf("feishu-ws: fetch quoted parent %s failed: %v", parentID, err)
		return nil
	}
	if resp == nil || !resp.Success() {
		if resp != nil {
			log.Printf("feishu-ws: fetch quoted parent %s api error: code=%d msg=%s", parentID, resp.Code, resp.Msg)
		}
		return nil
	}
	if resp.Data == nil || len(resp.Data.Items) == 0 {
		return nil
	}

	var item *larkim.Message
	for _, cand := range resp.Data.Items {
		if cand == nil {
			continue
		}
		if strings.TrimSpace(deref(cand.MessageId)) == parentID {
			item = cand
			break
		}
		if item == nil {
			item = cand
		}
	}
	if item == nil {
		return nil
	}
	if item.Deleted != nil && *item.Deleted {
		return &feishuQuotedParent{text: "[原消息已撤回]"}
	}

	parentType := strings.ToLower(strings.TrimSpace(deref(item.MsgType)))
	rawContent := ""
	if item.Body != nil {
		rawContent = deref(item.Body.Content)
	}

	q := &feishuQuotedParent{}
	var imgKeys []string
	switch parentType {
	case "text":
		var body struct {
			Text string `json:"text"`
		}
		if err := json.Unmarshal([]byte(rawContent), &body); err != nil {
			log.Printf("feishu-ws: decode quoted text %s: %v", parentID, err)
			return nil
		}
		q.text = replaceFeishuMessageMentions(body.Text, item.Mentions)
	case "image":
		var body struct {
			ImageKey string `json:"image_key"`
		}
		if err := json.Unmarshal([]byte(rawContent), &body); err != nil || strings.TrimSpace(body.ImageKey) == "" {
			q.text = "[飞书图片]"
		} else {
			imgKeys = append(imgKeys, strings.TrimSpace(body.ImageKey))
		}
	case "post":
		text, keys := parseFeishuPostContent(rawContent)
		q.text = replaceFeishuMessageMentions(text, item.Mentions)
		imgKeys = append(imgKeys, keys...)
	case "interactive":
		text, keys := parseFeishuInteractiveContent(rawContent)
		q.text = replaceFeishuMessageMentions(text, item.Mentions)
		imgKeys = append(imgKeys, keys...)
		if q.text == "" && len(imgKeys) == 0 {
			q.text = "[飞书卡片消息]"
		}
	case "file":
		var body struct {
			FileName string `json:"file_name"`
		}
		if json.Unmarshal([]byte(rawContent), &body) == nil && strings.TrimSpace(body.FileName) != "" {
			q.text = fmt.Sprintf("[飞书文件：%s]", strings.TrimSpace(body.FileName))
		} else {
			q.text = "[飞书 file 消息]"
		}
	default:
		if parentType != "" {
			q.text = fmt.Sprintf("[飞书 %s 消息]", parentType)
		}
	}

	imgKeys = dedupNonEmpty(imgKeys)
	maxImgs := feishuQuoteMaxImages
	if maxImgs <= 0 {
		maxImgs = 4
	}
	if len(imgKeys) > maxImgs {
		q.omittedImages = len(imgKeys) - maxImgs
		imgKeys = imgKeys[:maxImgs]
	}

	maxBytes := feishuQuoteMaxImageBytes
	if maxBytes <= 0 {
		maxBytes = 20 << 20
	}
	imgTimeout := feishuQuoteImageTimeout
	if imgTimeout <= 0 {
		imgTimeout = 15 * time.Second
	}
	for _, key := range imgKeys {
		if totalCtx.Err() != nil {
			q.failedImages++
			log.Printf("feishu-ws: quoted image download budget exceeded for parent %s", parentID)
			continue
		}
		saved, derr := f.downloadImageWithLimit(totalCtx, client, parentID, key, maxBytes, imgTimeout)
		if derr != nil || saved == "" {
			q.failedImages++
			log.Printf("feishu-ws: download quoted image %s/%s failed: %v", parentID, key, derr)
			continue
		}
		q.attachments = append(q.attachments, saved)
	}

	if strings.TrimSpace(q.text) == "" && len(q.attachments) == 0 && q.failedImages == 0 {
		return nil
	}
	return q
}

func formatQuotedParentBlock(q *feishuQuotedParent) string {
	if q == nil {
		return ""
	}
	text := strings.TrimSpace(q.text)
	if text != "" {
		text = clip(text, feishuQuoteMaxRunes)
	}
	imgCount := len(q.attachments)
	if text == "" {
		switch {
		case imgCount > 0 && q.omittedImages > 0 && q.failedImages > 0:
			return fmt.Sprintf("[引用图片（已保存 %d 张，%d 张下载失败，另略去 %d 张）]", imgCount, q.failedImages, q.omittedImages)
		case imgCount > 0 && q.omittedImages > 0:
			return fmt.Sprintf("[引用图片（已保存前 %d 张，另略去 %d 张）]", imgCount, q.omittedImages)
		case imgCount > 0 && q.failedImages > 0:
			return fmt.Sprintf("[引用图片（已保存 %d 张，%d 张下载失败）]", imgCount, q.failedImages)
		case imgCount > 1:
			return fmt.Sprintf("[引用图片（%d 张）]", imgCount)
		case imgCount == 1:
			return "[引用图片]"
		case q.failedImages > 0:
			return "[引用图片（下载失败）]"
		default:
			return ""
		}
	}

	var notes []string
	if imgCount > 0 {
		notes = append(notes, fmt.Sprintf("含 %d 张图片", imgCount))
	}
	if q.failedImages > 0 {
		notes = append(notes, fmt.Sprintf("%d 张图片下载失败", q.failedImages))
	}
	if q.omittedImages > 0 {
		notes = append(notes, fmt.Sprintf("另略去 %d 张", q.omittedImages))
	}
	header := "[引用消息]"
	if len(notes) > 0 {
		header = "[引用消息（" + strings.Join(notes, "，") + "）]"
	}
	var lines []string
	for _, line := range strings.Split(text, "\n") {
		lines = append(lines, "> "+line)
	}
	return header + "\n" + strings.Join(lines, "\n")
}

func composeFeishuInboundText(mainText string, mainPaths []string, quote *feishuQuotedParent) string {
	mainText = strings.TrimSpace(mainText)
	quoteBlock := formatQuotedParentBlock(quote)

	var body string
	switch {
	case quoteBlock != "" && mainText != "":
		body = quoteBlock + "\n\n" + mainText
	case quoteBlock != "":
		body = quoteBlock
	default:
		body = mainText
	}

	var paths []string
	if quote != nil {
		paths = append(paths, quote.attachments...)
	}
	paths = append(paths, mainPaths...)
	paths = dedupNonEmpty(paths)

	if len(paths) > 0 {
		if body != "" {
			return body + "\n\n附件：\n" + strings.Join(paths, "\n")
		}
		return "附件：\n" + strings.Join(paths, "\n")
	}
	return body
}

type feishuPostBody struct {
	Title   string `json:"title"`
	Content [][]struct {
		Tag      string `json:"tag"`
		Text     string `json:"text"`
		Href     string `json:"href"`
		ImageKey string `json:"image_key"`
		UserName string `json:"user_name"`
	} `json:"content"`
}

func parseFeishuPostContent(raw string) (string, []string) {
	var body feishuPostBody
	if err := json.Unmarshal([]byte(raw), &body); err != nil {
		return "", nil
	}
	// Feishu im.v1.message.get may wrap post payloads under a locale key (e.g. "zh_cn").
	if strings.TrimSpace(body.Title) == "" && len(body.Content) == 0 {
		var localized map[string]json.RawMessage
		if json.Unmarshal([]byte(raw), &localized) == nil {
			tryKeys := []string{"zh_cn", "en_us", "ja_jp"}
			for k := range localized {
				if !slices.Contains(tryKeys, k) {
					tryKeys = append(tryKeys, k)
				}
			}
			for _, k := range tryKeys {
				sub, ok := localized[k]
				if !ok || len(bytes.TrimSpace(sub)) == 0 {
					continue
				}
				var candidate feishuPostBody
				if json.Unmarshal(sub, &candidate) == nil && (strings.TrimSpace(candidate.Title) != "" || len(candidate.Content) > 0) {
					body = candidate
					break
				}
			}
		}
	}

	var lines []string
	var imgs []string
	if t := strings.TrimSpace(body.Title); t != "" {
		lines = append(lines, t)
	}
	for _, row := range body.Content {
		var parts []string
		for _, cell := range row {
			switch cell.Tag {
			case "text", "md", "code_block":
				parts = append(parts, cell.Text)
			case "at":
				if name := strings.TrimSpace(cell.UserName); name != "" {
					parts = append(parts, "@"+name)
				} else if cell.Text != "" {
					parts = append(parts, cell.Text)
				}
			case "a":
				if cell.Text != "" && cell.Href != "" {
					parts = append(parts, fmt.Sprintf("[%s](%s)", cell.Text, cell.Href))
				} else if cell.Text != "" {
					parts = append(parts, cell.Text)
				} else if cell.Href != "" {
					parts = append(parts, cell.Href)
				}
			case "img":
				if cell.ImageKey != "" {
					imgs = append(imgs, cell.ImageKey)
				}
			}
		}
		if line := strings.TrimSpace(strings.Join(parts, "")); line != "" {
			lines = append(lines, line)
		}
	}
	return strings.Join(lines, "\n"), imgs
}

// parseFeishuInteractiveContent extracts readable text and image keys from
// Feishu interactive card JSON (both im.v1.message.get downgraded format and
// Card JSON 2.0 payloads).
func parseFeishuInteractiveContent(raw string) (string, []string) {
	var doc map[string]any
	if err := json.Unmarshal([]byte(raw), &doc); err != nil || doc == nil {
		return "", nil
	}
	var lines []string
	var imgs []string
	if title, _ := doc["title"].(string); strings.TrimSpace(title) != "" {
		lines = append(lines, strings.TrimSpace(title))
	}
	if header, ok := doc["header"].(map[string]any); ok {
		if titleObj, ok := header["title"].(map[string]any); ok {
			if content, _ := titleObj["content"].(string); strings.TrimSpace(content) != "" {
				lines = append(lines, strings.TrimSpace(content))
			}
		}
	}
	var walk func(any)
	walk = func(node any) {
		switch v := node.(type) {
		case []any:
			var rowParts []string
			hasInline := false
			for _, it := range v {
				if m, ok := it.(map[string]any); ok {
					tag, _ := m["tag"].(string)
					switch tag {
					case "text":
						if s, _ := m["text"].(string); s != "" {
							rowParts = append(rowParts, s)
							hasInline = true
						}
						continue
					case "a":
						txt, _ := m["text"].(string)
						href, _ := m["href"].(string)
						if txt != "" && href != "" {
							rowParts = append(rowParts, fmt.Sprintf("[%s](%s)", txt, href))
							hasInline = true
						} else if txt != "" {
							rowParts = append(rowParts, txt)
							hasInline = true
						} else if href != "" {
							rowParts = append(rowParts, href)
							hasInline = true
						}
						continue
					case "img":
						if k, _ := m["image_key"].(string); strings.TrimSpace(k) != "" {
							imgs = append(imgs, strings.TrimSpace(k))
						} else if k, _ := m["img_key"].(string); strings.TrimSpace(k) != "" {
							imgs = append(imgs, strings.TrimSpace(k))
						}
						continue
					}
				}
				walk(it)
			}
			if hasInline {
				if line := strings.TrimSpace(strings.Join(rowParts, "")); line != "" {
					lines = append(lines, line)
				}
			}
		case map[string]any:
			tag, _ := v["tag"].(string)
			switch tag {
			case "markdown", "lark_md":
				if content, _ := v["content"].(string); strings.TrimSpace(content) != "" {
					lines = append(lines, strings.TrimSpace(content))
				}
			case "plain_text":
				if content, _ := v["content"].(string); strings.TrimSpace(content) != "" {
					lines = append(lines, strings.TrimSpace(content))
				}
			case "img":
				if k, _ := v["image_key"].(string); strings.TrimSpace(k) != "" {
					imgs = append(imgs, strings.TrimSpace(k))
				} else if k, _ := v["img_key"].(string); strings.TrimSpace(k) != "" {
					imgs = append(imgs, strings.TrimSpace(k))
				}
			case "button":
				return
			default:
				if els, ok := v["elements"]; ok {
					walk(els)
				}
				if txt, ok := v["text"]; ok {
					walk(txt)
				}
			}
		}
	}
	if els, ok := doc["elements"]; ok {
		walk(els)
	}
	if body, ok := doc["body"].(map[string]any); ok {
		if els, ok := body["elements"]; ok {
			walk(els)
		}
	}
	return strings.Join(lines, "\n"), imgs
}

func (f *FeishuAdapter) downloadImage(ctx context.Context, client *larksdk.Client, messageID, imageKey string) (string, error) {
	return f.downloadImageWithLimit(ctx, client, messageID, imageKey, maxUpload, 20*time.Second)
}

func (f *FeishuAdapter) downloadImageWithLimit(ctx context.Context, client *larksdk.Client, messageID, imageKey string, maxBytes int64, timeout time.Duration) (string, error) {
	if client == nil || messageID == "" || imageKey == "" {
		return "", errors.New("missing client or image key")
	}
	if maxBytes <= 0 {
		maxBytes = maxUpload
	}
	if timeout <= 0 {
		timeout = 20 * time.Second
	}
	dctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	req := larkim.NewGetMessageResourceReqBuilder().
		MessageId(messageID).
		FileKey(imageKey).
		Type("image").
		Build()
	resp, err := client.Im.V1.MessageResource.Get(dctx, req)
	if err != nil {
		return "", err
	}
	if !resp.Success() {
		return "", fmt.Errorf("feishu resource get failed: code=%d msg=%s", resp.Code, resp.Msg)
	}
	if resp.ApiResp != nil && strings.Contains(strings.ToLower(resp.ApiResp.Header.Get("Content-Type")), "application/json") {
		var apiErr struct {
			Code int    `json:"code"`
			Msg  string `json:"msg"`
		}
		if json.Unmarshal(resp.ApiResp.RawBody, &apiErr) == nil && apiErr.Code != 0 {
			return "", fmt.Errorf("feishu resource get failed: code=%d msg=%s", apiErr.Code, apiErr.Msg)
		}
	}
	if resp.File == nil {
		return "", errors.New("empty image body")
	}
	if closer, ok := resp.File.(io.Closer); ok {
		defer closer.Close()
	}
	baseDir := collieDir()
	if f.sp != nil && f.sp.dir != "" {
		baseDir = f.sp.dir
	}
	dir := filepath.Join(baseDir, "uploads", time.Now().Format("20060102"))
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	name := resp.FileName
	if strings.TrimSpace(name) == "" {
		name = "feishu-image.png"
	}
	name = uploadName(name)
	if filepath.Ext(name) == "" {
		name += ".png"
	}
	dst := filepath.Join(dir, randomToken()[:6]+"-"+name)
	file, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return "", err
	}
	limited := io.LimitReader(resp.File, maxBytes+1)
	n, err := io.Copy(file, limited)
	file.Close()
	if err != nil || n > maxBytes {
		_ = os.Remove(dst)
		if n > maxBytes {
			if maxBytes >= 1<<20 {
				return "", fmt.Errorf("image exceeds %dMB limit", maxBytes>>20)
			}
			return "", fmt.Errorf("image exceeds %d byte limit", maxBytes)
		}
		return "", err
	}
	return dst, nil
}

func (f *FeishuAdapter) addReceivedReaction(client *larksdk.Client, messageID string) {
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	req := larkim.NewCreateMessageReactionReqBuilder().
		MessageId(messageID).
		Body(larkim.NewCreateMessageReactionReqBodyBuilder().
			ReactionType(larkim.NewEmojiBuilder().EmojiType(feishuReactionEmoji).Build()).
			Build()).
		Build()
	_, _ = client.Im.V1.MessageReaction.Create(ctx, req)
}

// ---------------------------------------------------------------- card callback (<3s)

func (f *FeishuAdapter) handleCardAction(ctx context.Context, event *larkcallback.CardActionTriggerEvent, sink EventSink) (*larkcallback.CardActionTriggerResponse, error) {
	if event == nil || event.Event == nil || event.Event.Action == nil {
		return &larkcallback.CardActionTriggerResponse{}, nil
	}
	opOpenID := ""
	if event.Event.Operator != nil {
		opOpenID = strings.TrimSpace(event.Event.Operator.OpenID)
	}
	f.mu.RLock()
	cfg := f.activeCfg
	f.mu.RUnlock()

	if !f.allowSender(cfg, opOpenID) {
		log.Printf("feishu-ws: rejected card action from non-allowlisted sender %s", maskIdentifier(opOpenID))
		return &larkcallback.CardActionTriggerResponse{
			Toast: &larkcallback.Toast{Type: "warning", Content: "未在 Collie 安全白名单中，已拒绝操作"},
		}, nil
	}

	val := event.Event.Action.Value
	action := valueString(val, "action")
	entryID := valueString(val, "entry_id")
	openChatID := ""
	openMsgID := ""
	if event.Event.Context != nil {
		openChatID = strings.TrimSpace(event.Event.Context.OpenChatID)
		openMsgID = strings.TrimSpace(event.Event.Context.OpenMessageID)
	}

	// Verification card ping: acknowledge immediately without touching chat:main.
	if action == "test_ping" {
		card := buildFeishuNoticeCard("Collie · 连通验证", "green", "✓ 按钮回调校验通过（本次测试不会写入正式对话）。", f.jumpLink())
		return &larkcallback.CardActionTriggerResponse{
			Toast: &larkcallback.Toast{Type: "success", Content: "按钮回调正常 ✓"},
			Card:  &larkcallback.Card{Type: "raw", Data: card},
		}, nil
	}

	if f.sp == nil || entryID == "" {
		return &larkcallback.CardActionTriggerResponse{
			Toast: &larkcallback.Toast{Type: "warning", Content: "未找到关联的 Collie 消息"},
		}, nil
	}

	f.sp.mu.Lock()
	entry := f.sp.findEntryLocked(entryID)
	var entryCopy Entry
	if entry != nil {
		entryCopy = *entry
	}
	alreadyDismissed := f.sp.st.Dismissed[entryID]
	f.sp.mu.Unlock()
	if entry == nil {
		return &larkcallback.CardActionTriggerResponse{
			Toast: &larkcallback.Toast{Type: "warning", Content: "该消息已过期或不存在"},
		}, nil
	}

	// Idempotent resolution check to guard against double-clicks.
	alreadyResolved := alreadyDismissed
	if f.mgr != nil && f.mgr.db != nil {
		if rec, _ := getIntegrationMessage(f.mgr.db, entryID, feishuKind); rec != nil && rec.Resolved {
			alreadyResolved = true
		}
		if !alreadyResolved {
			changed, _ := markIntegrationMessageResolved(f.mgr.db, entryID, feishuKind)
			if !changed {
				// If a record exists and wasn't flipped, it was already resolved concurrently;
				// if no record existed yet, fall back to a dedicated dedup key.
				if rec, _ := getIntegrationMessage(f.mgr.db, entryID, feishuKind); rec != nil {
					alreadyResolved = true
				} else if ok, _ := dedupInsert(f.mgr.db, "feishu_card", entryID, time.Now()); !ok {
					alreadyResolved = true
				}
			}
		}
	}
	if alreadyResolved {
		card := buildFeishuEntryCard(&entryCopy, f.jumpLink(), "已处理")
		return &larkcallback.CardActionTriggerResponse{
			Toast: &larkcallback.Toast{Type: "info", Content: "这条消息已经处理过了"},
			Card:  &larkcallback.Card{Type: "raw", Data: card},
		}, nil
	}

	if openChatID != "" {
		f.recordInboundRoute(feishuRoute{
			at:        time.Now(),
			chatID:    openChatID,
			chatType:  "p2p",
			messageID: openMsgID,
			openID:    opOpenID,
		})
	}

	switch action {
	case "pick_opt":
		optIdx, ok := valueInt(val, "opt_idx")
		opts := cleanOptions(entryCopy.Options)
		if !ok || optIdx < 0 || optIdx >= len(opts) {
			return &larkcallback.CardActionTriggerResponse{
				Toast: &larkcallback.Toast{Type: "warning", Content: "选项索引无效"},
			}, nil
		}
		choice := opts[optIdx]
		dedupID := "card:" + entryID + ":" + strconv.Itoa(optIdx)
		if event.EventV2Base != nil && event.EventV2Base.Header != nil && event.EventV2Base.Header.EventID != "" {
			dedupID = "card:" + entryID
		}
		if sink != nil {
			_ = sink.EmitHuman(ctx, HumanEvent{
				Source:    feishuKind,
				DedupKey:  dedupID,
				Thread:    mainThread,
				From:      "feishu",
				Text:      choice,
				ReEntryID: entryID,
				ChatID:    openChatID,
				MessageID: openMsgID,
			})
		}
		if entryCopy.Thread == "" || entryCopy.Thread == mainThread {
			_, _, _ = f.sp.MarkChatRead(entryID)
		}
		summary := "已选择：" + choice
		card := buildFeishuEntryCard(&entryCopy, f.jumpLink(), summary)
		return &larkcallback.CardActionTriggerResponse{
			Toast: &larkcallback.Toast{Type: "success", Content: clip(summary, 50)},
			Card:  &larkcallback.Card{Type: "raw", Data: card},
		}, nil

	case "ack":
		f.sp.dismissWithoutSync(entryID)
		if entryCopy.Thread == "" || entryCopy.Thread == mainThread {
			_, _, _ = f.sp.MarkChatRead(entryID)
		}
		card := buildFeishuEntryCard(&entryCopy, f.jumpLink(), "已标记处理")
		return &larkcallback.CardActionTriggerResponse{
			Toast: &larkcallback.Toast{Type: "success", Content: "已标记处理"},
			Card:  &larkcallback.Card{Type: "raw", Data: card},
		}, nil

	default:
		return &larkcallback.CardActionTriggerResponse{
			Toast: &larkcallback.Toast{Type: "info", Content: "未识别的卡片操作"},
		}, nil
	}
}

func valueString(m map[string]any, k string) string {
	if m == nil {
		return ""
	}
	switch v := m[k].(type) {
	case string:
		return strings.TrimSpace(v)
	case float64:
		return strconv.FormatInt(int64(v), 10)
	case int:
		return strconv.Itoa(v)
	default:
		return ""
	}
}

func valueInt(m map[string]any, k string) (int, bool) {
	if m == nil {
		return 0, false
	}
	switch v := m[k].(type) {
	case float64:
		return int(v), true
	case int:
		return v, true
	case int64:
		return int(v), true
	case string:
		n, err := strconv.Atoi(strings.TrimSpace(v))
		return n, err == nil
	default:
		return 0, false
	}
}

// ---------------------------------------------------------------- outbound cards & reverse sync

func (f *FeishuAdapter) recordInboundRoute(r feishuRoute) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if r.chatID != "" {
		f.lastInbound = r
		if r.chatType == "p2p" {
			f.lastP2P = r
		}
	}
}

func (f *FeishuAdapter) recordFeedbackEvent(at time.Time) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if at.After(f.lastFeedbackAt) {
		f.lastFeedbackAt = at
	}
}

func (f *FeishuAdapter) bindEntryOrigin(entryID string, r feishuRoute) {
	if entryID == "" || r.chatID == "" {
		return
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.origins == nil {
		f.origins = map[string]feishuRoute{}
	}
	if _, exists := f.origins[entryID]; !exists {
		f.originOrder = append(f.originOrder, entryID)
		if len(f.originOrder) > feishuMaxTrackedOrigins {
			drop := f.originOrder[0]
			f.originOrder = f.originOrder[1:]
			delete(f.origins, drop)
		}
	}
	f.origins[entryID] = r
}

func (f *FeishuAdapter) latestFeishuUserEntryID() string {
	if f.sp == nil {
		return ""
	}
	f.sp.mu.Lock()
	defer f.sp.mu.Unlock()
	for i := len(f.sp.st.Entries) - 1; i >= 0; i-- {
		e := f.sp.st.Entries[i]
		if e.Role == "user" && e.Channel == feishuKind {
			return e.ID
		}
	}
	return ""
}

// ShouldInstantSync reports whether Collie's reply should be mirrored immediately
// to Feishu (bypassing the 20s notification batch and 75s browser-seen silence).
// Callers holding sp.mu pass reFromFeishu so this method never locks sp.mu.
func (f *FeishuAdapter) ShouldInstantSync(e *Entry, reFromFeishu bool) bool {
	if f == nil || e == nil || f.mgr == nil {
		return false
	}
	f.mgr.mu.Lock()
	rec := f.mgr.records[feishuKind]
	f.mgr.mu.Unlock()
	if !rec.Enabled {
		return false
	}
	fc := parseFeishuConfig(rec.Config)
	if !fc.InstantChatSync {
		return false
	}
	if !f.IsConfigured(IntegrationConfig{Kind: feishuKind, Config: rec.Config, Secrets: f.mgr.secrets.Get(feishuKind)}) {
		return false
	}

	if reFromFeishu {
		return true
	}

	f.mu.RLock()
	last := f.lastInbound
	var reOrigin feishuRoute
	hasReOrigin := false
	if e.Re != nil && e.Re.Entry != "" {
		reOrigin, hasReOrigin = f.origins[e.Re.Entry]
	}
	f.mu.RUnlock()

	if hasReOrigin && reOrigin.chatID != "" {
		return true
	}
	if !last.at.IsZero() && time.Since(last.at) <= feishuInstantSyncWindow {
		return true
	}
	return false
}

// CanSendFallback reports whether Feishu WS is enabled and configured to take
// over outbound async notifications from legacy lark-cli.
func (f *FeishuAdapter) CanSendFallback() bool {
	if f == nil || f.mgr == nil {
		return false
	}
	f.mgr.mu.Lock()
	rec := f.mgr.records[feishuKind]
	f.mgr.mu.Unlock()
	if !rec.Enabled {
		return false
	}
	cfg := IntegrationConfig{Kind: feishuKind, Config: rec.Config, Secrets: f.mgr.secrets.Get(feishuKind)}
	if !f.IsConfigured(cfg) {
		return false
	}
	fc := parseFeishuConfig(rec.Config)
	f.mu.RLock()
	hasChat := f.lastInbound.chatID != ""
	f.mu.RUnlock()
	return len(fc.AllowOpenIDs) > 0 || hasChat
}

func (f *FeishuAdapter) sdkClientForOutbound() (*larksdk.Client, FeishuConfig, error) {
	f.mu.RLock()
	c := f.activeClient
	cfg := f.activeCfg
	f.mu.RUnlock()
	if c != nil {
		return c, cfg, nil
	}
	if f.mgr == nil {
		return nil, cfg, errors.New("feishu integration not initialized")
	}
	f.mgr.mu.Lock()
	rec := f.mgr.records[feishuKind]
	f.mgr.mu.Unlock()
	cfg = parseFeishuConfig(rec.Config)
	sec := f.mgr.secrets.Get(feishuKind)
	if cfg.AppID == "" {
		cfg.AppID = strings.TrimSpace(sec["app_id"])
	}
	secret := strings.TrimSpace(sec["app_secret"])
	if cfg.AppID == "" || secret == "" {
		return nil, cfg, errors.New("feishu credentials not configured")
	}
	return f.newSDKClient(cfg.AppID, secret, cfg.Region), cfg, nil
}

func (f *FeishuAdapter) targetForEntry(e *Entry, cfg FeishuConfig) (recvType, recvID, replyMsgID string) {
	f.mu.RLock()
	var origin feishuRoute
	if e != nil && e.Re != nil && e.Re.Entry != "" {
		origin = f.origins[e.Re.Entry]
	}
	last := f.lastInbound
	lastP2P := f.lastP2P
	lastFB := f.lastFeedbackAt
	f.mu.RUnlock()

	if origin.chatID != "" {
		if origin.chatType == "group" && origin.messageID != "" {
			return "chat_id", origin.chatID, origin.messageID
		}
		return "chat_id", origin.chatID, ""
	}
	// Never route unsolicited notices or post-feedback replies into a group chat
	// (especially a configured feedback group); fall back to owner's private DM.
	allowLast := last.chatID != ""
	if allowLast && last.chatType == "group" {
		if lastFB.After(last.at) || (e == nil && f.isFeedbackChat(cfg, last.chatID)) {
			allowLast = false
		}
	}
	if allowLast {
		if last.chatType == "group" && last.messageID != "" {
			return "chat_id", last.chatID, last.messageID
		}
		return "chat_id", last.chatID, ""
	}
	if lastP2P.chatID != "" {
		return "chat_id", lastP2P.chatID, ""
	}
	if len(cfg.AllowOpenIDs) > 0 {
		return "open_id", cfg.AllowOpenIDs[0], ""
	}
	return "", "", ""
}

// SendReply mirrors one Collie reply (including --opt buttons and --check) to
// Feishu as an Interactive Card 2.0.
func (f *FeishuAdapter) SendReply(ctx context.Context, e *Entry, link string) (OutboundReceipt, error) {
	if e == nil {
		return OutboundReceipt{}, nil
	}
	client, cfg, err := f.sdkClientForOutbound()
	if err != nil {
		return OutboundReceipt{}, err
	}
	recvType, recvID, replyMsgID := f.targetForEntry(e, cfg)
	if recvID == "" {
		return OutboundReceipt{}, errors.New("no target Feishu chat_id or allow_open_id available")
	}
	card := buildFeishuEntryCard(e, link, "")
	msgID, err := f.sendCard(ctx, client, recvType, recvID, replyMsgID, card)
	if err != nil {
		return OutboundReceipt{}, err
	}
	return OutboundReceipt{
		Kind:       feishuKind,
		ChatID:     recvID,
		ExternalID: msgID,
	}, nil
}

// SendNotices sends batched asynchronous notifications (when the human is away
// from the web page) using the active Feishu OpenAPI credentials.
func (f *FeishuAdapter) SendNotices(ctx context.Context, ns []notice, link string) error {
	if len(ns) == 0 {
		return nil
	}
	client, cfg, err := f.sdkClientForOutbound()
	if err != nil {
		return err
	}
	recvType, recvID, _ := f.targetForEntry(nil, cfg)
	if recvID == "" {
		return errors.New("no target Feishu chat_id or allow_open_id available")
	}
	// If there is a single notice backed by an interactive Entry, send with real
	// callback buttons and persist the mapping for bidirectional sync.
	if len(ns) == 1 && ns[0].entryID != "" && (len(ns[0].options) > 0 || ns[0].check) && f.sp != nil {
		f.sp.mu.Lock()
		e := f.sp.findEntryLocked(ns[0].entryID)
		var cp Entry
		if e != nil {
			cp = *e
		}
		f.sp.mu.Unlock()
		if e != nil {
			receipt, err := f.SendReply(ctx, &cp, link)
			if err != nil {
				return err
			}
			if receipt.ExternalID != "" && f.mgr != nil {
				_ = saveIntegrationMessage(f.mgr.db, IntegrationMessage{
					EntryID:    cp.ID,
					Kind:       feishuKind,
					ChatID:     receipt.ChatID,
					ExternalID: receipt.ExternalID,
					CreatedAt:  time.Now().Unix(),
				})
				f.mgr.recordActivity(feishuKind)
			}
			return nil
		}
	}
	raw := larkCard(ns, link)
	_, err = f.sendRawCard(ctx, client, recvType, recvID, "", raw)
	if err == nil && f.mgr != nil {
		f.mgr.recordActivity(feishuKind)
	}
	return err
}

// SyncResolved updates a previously sent Feishu interactive card in place
// (PATCH /im/v1/messages/{message_id}) when resolved on the Web UI.
func (f *FeishuAdapter) SyncResolved(ctx context.Context, receipt OutboundReceipt, summary string) error {
	if receipt.ExternalID == "" {
		return nil
	}
	client, _, err := f.sdkClientForOutbound()
	if err != nil {
		return err
	}
	var entry *Entry
	if f.sp != nil && f.mgr != nil {
		if rec, _ := getIntegrationMessageByExternal(f.mgr.db, feishuKind, receipt.ExternalID); rec != nil {
			f.sp.mu.Lock()
			if e := f.sp.findEntryLocked(rec.EntryID); e != nil {
				cp := *e
				entry = &cp
			}
			f.sp.mu.Unlock()
		}
	}
	if summary == "" {
		summary = "已在网页端处理"
	}
	var card map[string]any
	if entry != nil {
		card = buildFeishuEntryCard(entry, f.jumpLink(), summary)
	} else {
		card = buildFeishuNoticeCard("Collie · 已处理", "grey", "✓ "+summary, f.jumpLink())
	}
	cardJSON, err := marshalCardJSON(card)
	if err != nil {
		return err
	}
	pctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	req := larkim.NewPatchMessageReqBuilder().
		MessageId(receipt.ExternalID).
		Body(larkim.NewPatchMessageReqBodyBuilder().Content(cardJSON).Build()).
		Build()
	resp, err := client.Im.V1.Message.Patch(pctx, req)
	if err != nil {
		return err
	}
	if !resp.Success() {
		return fmt.Errorf("feishu patch card failed: code=%d msg=%s", resp.Code, resp.Msg)
	}
	return nil
}

func (f *FeishuAdapter) sendCard(ctx context.Context, client *larksdk.Client, recvType, recvID, replyMsgID string, card map[string]any) (string, error) {
	raw, err := marshalCardJSON(card)
	if err != nil {
		return "", err
	}
	return f.sendRawCard(ctx, client, recvType, recvID, replyMsgID, raw)
}

func (f *FeishuAdapter) sendRawCard(ctx context.Context, client *larksdk.Client, recvType, recvID, replyMsgID, cardJSON string) (string, error) {
	sctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	if replyMsgID != "" {
		req := larkim.NewReplyMessageReqBuilder().
			MessageId(replyMsgID).
			Body(larkim.NewReplyMessageReqBodyBuilder().
				MsgType("interactive").
				Content(cardJSON).
				Build()).
			Build()
		resp, err := client.Im.V1.Message.Reply(sctx, req)
		if err != nil {
			return "", err
		}
		if !resp.Success() {
			return "", fmt.Errorf("feishu reply failed: code=%d msg=%s", resp.Code, resp.Msg)
		}
		if resp.Data != nil && resp.Data.MessageId != nil {
			return *resp.Data.MessageId, nil
		}
		return "", nil
	}
	req := larkim.NewCreateMessageReqBuilder().
		ReceiveIdType(recvType).
		Body(larkim.NewCreateMessageReqBodyBuilder().
			ReceiveId(recvID).
			MsgType("interactive").
			Content(cardJSON).
			Build()).
		Build()
	resp, err := client.Im.V1.Message.Create(sctx, req)
	if err != nil {
		return "", err
	}
	if !resp.Success() {
		return "", fmt.Errorf("feishu send failed: code=%d msg=%s", resp.Code, resp.Msg)
	}
	if resp.Data != nil && resp.Data.MessageId != nil {
		return *resp.Data.MessageId, nil
	}
	return "", nil
}

// ---------------------------------------------------------------- one-click self-test

// Test verifies credentials against Feishu OpenAPI (bot info) and, when a
// target OpenID is configured, sends a real verification Interactive Card 2.0
// without polluting chat:main or waking the Router.
func (f *FeishuAdapter) Test(ctx context.Context, cfg IntegrationConfig) TestResult {
	fc := parseFeishuConfig(cfg.Config)
	if fc.AppID == "" {
		fc.AppID = strings.TrimSpace(cfg.Secrets["app_id"])
	}
	secret := strings.TrimSpace(cfg.Secrets["app_secret"])
	if fc.AppID == "" || secret == "" {
		return TestResult{
			OK:      false,
			Message: "请先填写 App ID 和 App Secret 再测试连通",
		}
	}

	start := time.Now()
	client := f.newSDKClient(fc.AppID, secret, fc.Region)
	botOpenID, botName, err := f.fetchBotInfo(ctx, client)
	latency := time.Since(start).Milliseconds()
	if err != nil {
		return TestResult{
			OK:        false,
			LatencyMs: latency,
			Message:   "凭据校验失败，请核对 App ID、App Secret 与平台区域（" + clip(cleanAuthError(err), 120) + "）",
		}
	}

	detail := map[string]any{
		"bot_name":  botName,
		"card_sent": false,
	}
	if botOpenID != "" {
		f.mu.Lock()
		f.botOpenID = botOpenID
		if botName != "" {
			f.botName = botName
		}
		f.mu.Unlock()
	}

	// If an OpenID is configured, send a real verification interactive card.
	if len(fc.AllowOpenIDs) > 0 {
		card := buildFeishuTestCard(f.jumpLink())
		msgID, sendErr := f.sendCard(ctx, client, "open_id", fc.AllowOpenIDs[0], "", card)
		latency = time.Since(start).Milliseconds()
		if sendErr != nil {
			return TestResult{
				OK:        false,
				LatencyMs: latency,
				Message:   fmt.Sprintf("凭据校验通过（机器人「%s」），但发送验证卡片失败：%s", nonEmpty(botName, "飞书机器人"), clip(sendErr.Error(), 140)),
				Detail:    detail,
			}
		}
		detail["card_sent"] = true
		detail["message_id"] = msgID
		return TestResult{
			OK:        true,
			LatencyMs: latency,
			Message:   fmt.Sprintf("已连通（%dms），验证卡片已发至飞书（机器人「%s」）", latency, nonEmpty(botName, "飞书机器人")),
			Detail:    detail,
		}
	}

	return TestResult{
		OK:        true,
		LatencyMs: latency,
		Message:   fmt.Sprintf("凭据校验通过（机器人「%s」，%dms）；填入本人 OpenID 后即可接收测试验证卡片", nonEmpty(botName, "飞书机器人"), latency),
		Detail:    detail,
	}
}

func (f *FeishuAdapter) jumpLink() string {
	if f.sp == nil {
		return ""
	}
	return f.sp.jumpLink()
}

func nonEmpty(a, b string) string {
	if strings.TrimSpace(a) != "" {
		return a
	}
	return b
}

// ---------------------------------------------------------------- card builders (JSON 2.0)

func marshalCardJSON(card map[string]any) (string, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(card); err != nil {
		return "", err
	}
	return strings.TrimSuffix(buf.String(), "\n"), nil
}

func buildFeishuEntryCard(e *Entry, link string, resolvedSummary string) map[string]any {
	link = larkJumpURL(link)
	opts := cleanOptions(e.Options)
	needsAction := len(opts) > 0 || e.Check || e.Tone == "blocked"

	title, color := "Collie", "default"
	switch {
	case resolvedSummary != "":
		title, color = "Collie · 已处理", "grey"
	case needsAction:
		title, color = "Collie · 需要你", "orange"
	case e.Tone == "error":
		title, color = "Collie · 出错了", "red"
	}

	var els []any
	bodyText := clip(e.Text, 3500)
	if e.Re != nil {
		bodyText = "<font color='grey'>↩ " + larkEscape(e.Re.line()) + "</font>\n" + bodyText
	}
	els = append(els, map[string]any{"tag": "markdown", "content": bodyText})

	if resolvedSummary != "" {
		els = append(els, map[string]any{
			"tag":     "markdown",
			"content": "<font color='grey'>✓ " + larkEscape(resolvedSummary) + "</font>",
		})
	} else if len(opts) > 0 {
		for i, opt := range opts {
			btnType := "default"
			if i == 0 {
				btnType = "primary"
			}
			els = append(els, map[string]any{
				"tag":   "button",
				"type":  btnType,
				"width": "default",
				"text":  map[string]string{"tag": "plain_text", "content": clip(opt, 80)},
				"behaviors": []any{
					map[string]any{
						"type": "callback",
						"value": map[string]string{
							"action":   "pick_opt",
							"entry_id": e.ID,
							"opt_idx":  strconv.Itoa(i),
						},
					},
				},
			})
		}
	} else if e.Check {
		els = append(els, map[string]any{
			"tag":   "button",
			"type":  "primary",
			"width": "default",
			"text":  map[string]string{"tag": "plain_text", "content": "已处理"},
			"behaviors": []any{
				map[string]any{
					"type": "callback",
					"value": map[string]string{
						"action":   "ack",
						"entry_id": e.ID,
					},
				},
			},
		})
	}

	if link != "" {
		els = append(els, map[string]any{
			"tag":   "button",
			"type":  "default",
			"width": "default",
			"text":  map[string]string{"tag": "plain_text", "content": "打开 Collie"},
			"behaviors": []any{
				map[string]string{"type": "open_url", "default_url": link},
			},
		})
	}

	summary := cardSummary([]notice{{tone: e.Tone, text: e.Text, options: opts, check: e.Check}})
	return map[string]any{
		"schema": "2.0",
		"config": map[string]any{
			"update_multi": true,
			"summary":      map[string]string{"content": clip(summary, 60)},
		},
		"header": map[string]any{
			"title":    map[string]string{"tag": "plain_text", "content": title},
			"template": color,
		},
		"body": map[string]any{"elements": els},
	}
}

func buildFeishuTestCard(link string) map[string]any {
	link = larkJumpURL(link)
	els := []any{
		map[string]any{
			"tag":     "markdown",
			"content": "飞书主动长连接验证成功！之后可以直接在这里私信或在群里 @Collie 派活，Collie 的选项按钮也会直接推送到这里。",
		},
		map[string]any{
			"tag":   "button",
			"type":  "primary",
			"width": "default",
			"text":  map[string]string{"tag": "plain_text", "content": "点我验证按钮回调"},
			"behaviors": []any{
				map[string]any{
					"type":  "callback",
					"value": map[string]string{"action": "test_ping"},
				},
			},
		},
	}
	if link != "" {
		els = append(els, map[string]any{
			"tag":   "button",
			"type":  "default",
			"width": "default",
			"text":  map[string]string{"tag": "plain_text", "content": "打开 Collie"},
			"behaviors": []any{
				map[string]string{"type": "open_url", "default_url": link},
			},
		})
	}
	return map[string]any{
		"schema": "2.0",
		"config": map[string]any{
			"update_multi": true,
			"summary":      map[string]string{"content": "Collie 飞书长连接验证卡片"},
		},
		"header": map[string]any{
			"title":    map[string]string{"tag": "plain_text", "content": "Collie · 连通验证"},
			"template": "green",
		},
		"body": map[string]any{"elements": els},
	}
}

func buildFeishuNoticeCard(title, template, markdownBody, link string) map[string]any {
	link = larkJumpURL(link)
	els := []any{
		map[string]any{"tag": "markdown", "content": markdownBody},
	}
	if link != "" {
		els = append(els, map[string]any{
			"tag":   "button",
			"type":  "default",
			"width": "default",
			"text":  map[string]string{"tag": "plain_text", "content": "打开 Collie"},
			"behaviors": []any{
				map[string]string{"type": "open_url", "default_url": link},
			},
		})
	}
	return map[string]any{
		"schema": "2.0",
		"config": map[string]any{
			"update_multi": true,
			"summary":      map[string]string{"content": clip(title, 60)},
		},
		"header": map[string]any{
			"title":    map[string]string{"tag": "plain_text", "content": title},
			"template": template,
		},
		"body": map[string]any{"elements": els},
	}
}
