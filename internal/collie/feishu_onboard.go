package collie

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	qrcode "github.com/skip2/go-qrcode"
)

const (
	feishuOnboardCNDomain            = "https://accounts.feishu.cn"
	feishuOnboardIntlDomain          = "https://accounts.larksuite.com"
	feishuOnboardEndpoint            = "/oauth/v1/app/registration"
	feishuOnboardSource              = "go-sdk/collie"
	feishuOnboardNameSuffix          = "的边牧 · Collie"
	feishuOnboardDefaultNameTemplate = "{user}" + feishuOnboardNameSuffix
	feishuOnboardFallbackName        = "我的边牧 · Collie"
	feishuOnboardDefaultName         = feishuOnboardDefaultNameTemplate
	feishuOnboardDefaultDesc         = "帮你放牧一群 Agent 的边牧｜盯进度、收结论、只把关键决策端给你 · " + collieHomepageURL
	feishuOnboardAvatarBaseURL       = collieHomepageURL + "avatars/"
	feishuOnboardDefaultAvatarID     = "cobalt"
	feishuOnboardMinNameRunes        = 2
	feishuOnboardCNMaxNameRunes      = 32
	feishuOnboardIntlMaxNameRunes    = 64
	feishuOnboardCNMaxDescRunes      = 120
	feishuOnboardIntlMaxDescRunes    = 240
	feishuOnboardDefaultExpireSec    = 600
	feishuOnboardMaxTTL              = 10 * time.Minute
	feishuOnboardDefaultInterval     = 5 * time.Second
	feishuOnboardSlowDownStep        = 5 * time.Second
	feishuOnboardMaxPollInterval     = 60 * time.Second
	feishuOnboardDefaultReqTimeout   = 15 * time.Second
	feishuOnboardQRSize              = 400
	feishuOnboardMaxBodyBytes        = 1 << 20

	feishuOnboardModeCreate   = "create"
	feishuOnboardModeExisting = "existing"

	feishuOnboardStateIdle           = "idle"
	feishuOnboardStateWaiting        = "waiting"
	feishuOnboardStateDomainSwitched = "domain_switched"
	feishuOnboardStateCompleted      = "completed"
	feishuOnboardStateDenied         = "denied"
	feishuOnboardStateExpired        = "expired"
	feishuOnboardStateError          = "error"
)

// FeishuOnboardAvatarPreset describes one high-contrast circular Border Collie
// bot avatar bundled for both local Web UI preview and Feishu /page/launcher.
type FeishuOnboardAvatarPreset struct {
	ID      string `json:"id"`
	Label   string `json:"label"`
	Color   string `json:"color"`
	Preview string `json:"preview"`
	URL     string `json:"url"`
}

var feishuOnboardAvatarPresets = []FeishuOnboardAvatarPreset{
	{ID: "cobalt", Label: "钴蓝领巾", Color: "#1d4ed8", Preview: "/avatars/cobalt.png", URL: feishuOnboardAvatarBaseURL + "cobalt.png"},
	{ID: "terracotta", Label: "赤陶领巾", Color: "#c2410c", Preview: "/avatars/terracotta.png", URL: feishuOnboardAvatarBaseURL + "terracotta.png"},
	{ID: "pine", Label: "松绿领巾", Color: "#15803d", Preview: "/avatars/pine.png", URL: feishuOnboardAvatarBaseURL + "pine.png"},
	{ID: "plum", Label: "梅紫领巾", Color: "#7e22ce", Preview: "/avatars/plum.png", URL: feishuOnboardAvatarBaseURL + "plum.png"},
}

type feishuOnboardLimits struct {
	NameMin int `json:"name_min"`
	NameMax int `json:"name_max"`
	DescMax int `json:"desc_max"`
}

func feishuOnboardLimitsForRegion(region string) feishuOnboardLimits {
	if normalizeFeishuOnboardRegion(region) == "intl" {
		return feishuOnboardLimits{
			NameMin: feishuOnboardMinNameRunes,
			NameMax: feishuOnboardIntlMaxNameRunes,
			DescMax: feishuOnboardIntlMaxDescRunes,
		}
	}
	return feishuOnboardLimits{
		NameMin: feishuOnboardMinNameRunes,
		NameMax: feishuOnboardCNMaxNameRunes,
		DescMax: feishuOnboardCNMaxDescRunes,
	}
}

type feishuOnboardPersona struct {
	Name       string
	Desc       string
	AvatarID   string
	AvatarURLs []string
}

func sanitizeFeishuOwnerName(raw string) string {
	s := strings.TrimSpace(oneLine(raw))
	if s == "" {
		return ""
	}
	// Never treat masked IDs, raw OpenIDs, or email addresses as display names.
	lower := strings.ToLower(s)
	if strings.HasPrefix(lower, "ou_") || strings.Contains(s, "@") || strings.Contains(s, "...") || strings.Contains(s, "…") {
		return ""
	}
	return s
}

func formatFeishuOwnerBotName(ownerName string, maxRunes int) string {
	owner := sanitizeFeishuOwnerName(ownerName)
	if owner == "" {
		return feishuOnboardFallbackName
	}
	if maxRunes <= 0 {
		maxRunes = feishuOnboardCNMaxNameRunes
	}
	suffixRunes := utf8.RuneCountInString(feishuOnboardNameSuffix)
	avail := maxRunes - suffixRunes
	if avail <= 0 {
		return feishuOnboardFallbackName
	}
	r := []rune(owner)
	if len(r) > avail {
		owner = strings.TrimSpace(string(r[:avail]))
	}
	if owner == "" {
		return feishuOnboardFallbackName
	}
	return owner + feishuOnboardNameSuffix
}

func resolveFeishuOnboardAvatar(rawID string) (string, []string, error) {
	id := strings.ToLower(strings.TrimSpace(rawID))
	if id == "" {
		id = feishuOnboardDefaultAvatarID
	}
	selectedIdx := -1
	for i, p := range feishuOnboardAvatarPresets {
		if p.ID == id {
			selectedIdx = i
			break
		}
	}
	if selectedIdx < 0 {
		return "", nil, &feishuOnboardBadRequestError{msg: "不支持的预设头像，仅支持 cobalt、terracotta、pine 或 plum"}
	}
	// Put selected avatar first so Feishu /page/launcher pre-selects it,
	// followed by the remaining presets as ready-to-pick candidates (<= 6 max).
	urls := make([]string, 0, len(feishuOnboardAvatarPresets))
	urls = append(urls, feishuOnboardAvatarPresets[selectedIdx].URL)
	for i, p := range feishuOnboardAvatarPresets {
		if i != selectedIdx {
			urls = append(urls, p.URL)
		}
	}
	return feishuOnboardAvatarPresets[selectedIdx].ID, urls, nil
}

func resolveFeishuOnboardPersona(region, rawName, rawDesc, rawAvatar, ownerName string) (feishuOnboardPersona, error) {
	limits := feishuOnboardLimitsForRegion(region)
	avatarID, avatarURLs, err := resolveFeishuOnboardAvatar(rawAvatar)
	if err != nil {
		return feishuOnboardPersona{}, err
	}

	name := strings.TrimSpace(oneLine(rawName))
	if name == "" {
		if owner := sanitizeFeishuOwnerName(ownerName); owner != "" {
			name = formatFeishuOwnerBotName(owner, limits.NameMax)
		} else {
			name = feishuOnboardDefaultNameTemplate
		}
	} else {
		n := utf8.RuneCountInString(name)
		if n < limits.NameMin {
			return feishuOnboardPersona{}, &feishuOnboardBadRequestError{
				msg: fmt.Sprintf("机器人名字至少需要 %d 个字符", limits.NameMin),
			}
		}
		if n > limits.NameMax {
			return feishuOnboardPersona{}, &feishuOnboardBadRequestError{
				msg: fmt.Sprintf("机器人名字不能超过 %d 个字符", limits.NameMax),
			}
		}
	}

	desc := strings.TrimSpace(oneLine(rawDesc))
	if desc == "" {
		desc = feishuOnboardDefaultDesc
	} else if utf8.RuneCountInString(desc) > limits.DescMax {
		return feishuOnboardPersona{}, &feishuOnboardBadRequestError{
			msg: fmt.Sprintf("机器人简介不能超过 %d 个字符", limits.DescMax),
		}
	}

	return feishuOnboardPersona{
		Name:       name,
		Desc:       desc,
		AvatarID:   avatarID,
		AvatarURLs: avatarURLs,
	}, nil
}

func feishuOnboardPresetsView(ownerName string) map[string]any {
	owner := sanitizeFeishuOwnerName(ownerName)
	defaultName := feishuOnboardFallbackName
	if owner != "" {
		defaultName = formatFeishuOwnerBotName(owner, feishuOnboardCNMaxNameRunes)
	}
	avatars := make([]FeishuOnboardAvatarPreset, len(feishuOnboardAvatarPresets))
	copy(avatars, feishuOnboardAvatarPresets)
	return map[string]any{
		"owner_name":     owner,
		"default_name":   defaultName,
		"name_template":  feishuOnboardDefaultNameTemplate,
		"fallback_name":  feishuOnboardFallbackName,
		"default_desc":   feishuOnboardDefaultDesc,
		"homepage_url":   collieHomepageURL,
		"default_avatar": feishuOnboardDefaultAvatarID,
		"limits": map[string]feishuOnboardLimits{
			"cn":   feishuOnboardLimitsForRegion("cn"),
			"intl": feishuOnboardLimitsForRegion("intl"),
		},
		"avatars": avatars,
	}
}

// feishuOnboardTenantScopes declares the minimal 7 IM tenant scopes Collie
// requires for bidirectional chat, interactive cards, emoji ACK, and image
// attachments (zero docs/drive/calendar over-privilege).
var feishuOnboardTenantScopes = []string{
	"im:message:send_as_bot",
	"im:message:update",
	"im:message:readonly",
	"im:message.p2p_msg:readonly",
	"im:message.group_at_msg:readonly",
	"im:message.reactions:write_only",
	"im:resource",
}

// FeishuOnboardStartRequest is the JSON body for POST /api/super/integrations/feishu/onboard.
type FeishuOnboardStartRequest struct {
	Region   string `json:"region"`           // "cn" (default) | "intl"
	Mode     string `json:"mode"`             // "create" (default) | "existing"
	ClientID string `json:"client_id"`        // optional existing App ID when mode="existing"
	Name     string `json:"name,omitempty"`   // optional bot display name override (create mode)
	Desc     string `json:"desc,omitempty"`   // optional bot description override (create mode)
	Avatar   string `json:"avatar,omitempty"` // optional avatar preset id: cobalt | terracotta | pine | plum
}

// FeishuOnboardView is the sanitized public status snapshot returned by
// POST/GET /api/super/integrations/feishu/onboard. It never contains secrets
// or device_code.
type FeishuOnboardView struct {
	SessionID    string     `json:"session_id,omitempty"`
	State        string     `json:"state"`
	Mode         string     `json:"mode,omitempty"`
	Region       string     `json:"region,omitempty"`
	ClientID     string     `json:"client_id,omitempty"`
	Name         string     `json:"name,omitempty"`
	Desc         string     `json:"desc,omitempty"`
	Avatar       string     `json:"avatar,omitempty"`
	URL          string     `json:"url,omitempty"`
	UserCode     string     `json:"user_code,omitempty"`
	QRDataURL    string     `json:"qr_data_url,omitempty"`
	ExpireIn     int        `json:"expire_in,omitempty"`
	RemainingSec int        `json:"remaining_sec,omitempty"`
	ExpiresAt    *time.Time `json:"expires_at,omitempty"`
	AppID        string     `json:"app_id,omitempty"`
	OpenID       string     `json:"open_id,omitempty"`
	Error        string     `json:"error,omitempty"`
}

type feishuOnboardBeginResp struct {
	DeviceCode              string `json:"device_code"`
	UserCode                string `json:"user_code"`
	VerificationURI         string `json:"verification_uri"`
	VerificationURIComplete string `json:"verification_uri_complete"`
	ExpiresIn               int    `json:"expires_in"`
	ExpireIn                int    `json:"expire_in"`
	Interval                int    `json:"interval"`
	Error                   string `json:"error"`
	ErrorDesc               string `json:"error_description"`
}

type feishuOnboardUserInfo struct {
	OpenID      string `json:"open_id"`
	TenantBrand string `json:"tenant_brand"`
}

type feishuOnboardPollResp struct {
	ClientID     string                 `json:"client_id"`
	ClientSecret string                 `json:"client_secret"`
	UserInfo     *feishuOnboardUserInfo `json:"user_info"`
	Error        string                 `json:"error"`
	ErrorDesc    string                 `json:"error_description"`
}

type feishuOnboardSession struct {
	id        string
	state     string
	mode      string
	region    string
	clientID  string
	name      string
	desc      string
	avatar    string
	url       string
	userCode  string
	qrDataURL string
	expireIn  int
	expiresAt time.Time
	appID     string
	openID    string
	errMsg    string
	cancel    context.CancelFunc
}

func (s *feishuOnboardSession) view(now time.Time) FeishuOnboardView {
	if s == nil {
		return FeishuOnboardView{State: feishuOnboardStateIdle}
	}
	v := FeishuOnboardView{
		SessionID: s.id,
		State:     s.state,
		Mode:      s.mode,
		Region:    s.region,
		ClientID:  s.clientID,
		Name:      s.name,
		Desc:      s.desc,
		Avatar:    s.avatar,
		UserCode:  s.userCode,
		AppID:     s.appID,
		OpenID:    s.openID,
		Error:     s.errMsg,
	}
	if s.state == feishuOnboardStateWaiting || s.state == feishuOnboardStateDomainSwitched {
		v.URL = s.url
		v.QRDataURL = s.qrDataURL
		v.ExpireIn = s.expireIn
		if !s.expiresAt.IsZero() {
			exp := s.expiresAt
			v.ExpiresAt = &exp
			rem := int(s.expiresAt.Sub(now).Seconds())
			if rem < 0 {
				rem = 0
			}
			v.RemainingSec = rem
		}
	}
	return v
}

// feishuOnboarder manages one active Feishu/Lark OAuth Device Flow app
// registration / existing-app patching session at a time.
type feishuOnboarder struct {
	sp  *Super
	mgr *IntegrationManager

	// Optional overrides for deterministic unit tests (never set in production).
	feishuDomainOverride string
	larkDomainOverride   string
	ownerNameOverride    *string
	httpClient           *http.Client
	maxTTL               time.Duration
	reqTimeout           time.Duration
	slowDownStep         time.Duration
	maxPollInterval      time.Duration
	nowFn                func() time.Time
	waitFn               func(ctx context.Context, d time.Duration) error

	mu      sync.Mutex
	rootCtx context.Context
	rootEnd context.CancelFunc
	closed  bool
	cur     *feishuOnboardSession
	wg      sync.WaitGroup
}

func newFeishuOnboarder(sp *Super, mgr *IntegrationManager) *feishuOnboarder {
	ctx, cancel := context.WithCancel(context.Background())
	return &feishuOnboarder{
		sp:      sp,
		mgr:     mgr,
		rootCtx: ctx,
		rootEnd: cancel,
	}
}

func (o *feishuOnboarder) now() time.Time {
	if o.nowFn != nil {
		return o.nowFn()
	}
	return time.Now()
}

func (o *feishuOnboarder) client() *http.Client {
	if o.httpClient != nil {
		return o.httpClient
	}
	return &http.Client{Timeout: o.requestTimeout()}
}

func (o *feishuOnboarder) requestTimeout() time.Duration {
	if o.reqTimeout > 0 {
		return o.reqTimeout
	}
	return feishuOnboardDefaultReqTimeout
}

func (o *feishuOnboarder) sessionMaxTTL() time.Duration {
	if o.maxTTL > 0 {
		return o.maxTTL
	}
	return feishuOnboardMaxTTL
}

func (o *feishuOnboarder) pollSlowDownStep() time.Duration {
	if o.slowDownStep > 0 {
		return o.slowDownStep
	}
	return feishuOnboardSlowDownStep
}

func (o *feishuOnboarder) pollMaxInterval() time.Duration {
	if o.maxPollInterval > 0 {
		return o.maxPollInterval
	}
	return feishuOnboardMaxPollInterval
}

func (o *feishuOnboarder) feishuDomain() string {
	if strings.TrimSpace(o.feishuDomainOverride) != "" {
		return strings.TrimRight(strings.TrimSpace(o.feishuDomainOverride), "/")
	}
	return feishuOnboardCNDomain
}

func (o *feishuOnboarder) larkDomain() string {
	if strings.TrimSpace(o.larkDomainOverride) != "" {
		return strings.TrimRight(strings.TrimSpace(o.larkDomainOverride), "/")
	}
	return feishuOnboardIntlDomain
}

func (o *feishuOnboarder) domainForRegion(region string) string {
	if region == "intl" {
		return o.larkDomain()
	}
	return o.feishuDomain()
}

func (o *feishuOnboarder) wait(ctx context.Context, d time.Duration) error {
	if o.waitFn != nil {
		return o.waitFn(ctx, d)
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

func (o *feishuOnboarder) redact(s string) string {
	if o.mgr != nil {
		return o.mgr.redact(s)
	}
	return s
}

func (o *feishuOnboarder) notifyChanged() {
	if o.mgr != nil {
		o.mgr.notifyChanged()
		return
	}
	if o.sp != nil {
		o.sp.mu.Lock()
		o.sp.changed()
		o.sp.mu.Unlock()
	}
}

// Status returns the current sanitized onboard session snapshot.
func (o *feishuOnboarder) Status() FeishuOnboardView {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.cur == nil {
		return FeishuOnboardView{State: feishuOnboardStateIdle}
	}
	return o.cur.view(o.now())
}

// Cancel stops any active onboard session and resets the state to idle.
func (o *feishuOnboarder) Cancel() {
	o.mu.Lock()
	prev := o.cur
	o.cur = nil
	o.mu.Unlock()
	if prev != nil && prev.cancel != nil {
		prev.cancel()
	}
	o.notifyChanged()
}

// Close cancels any active onboard session and waits for background polling to exit.
func (o *feishuOnboarder) Close() {
	o.mu.Lock()
	o.closed = true
	prev := o.cur
	o.cur = nil
	end := o.rootEnd
	o.mu.Unlock()
	if prev != nil && prev.cancel != nil {
		prev.cancel()
	}
	if end != nil {
		end()
	}
	o.wg.Wait()
}

type feishuOnboardBadRequestError struct {
	msg string
}

func (e *feishuOnboardBadRequestError) Error() string { return e.msg }

func normalizeFeishuOnboardRegion(raw string) string {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "intl", "lark":
		return "intl"
	default:
		return "cn"
	}
}

func normalizeFeishuOnboardMode(rawMode, clientID string) (string, error) {
	m := strings.ToLower(strings.TrimSpace(rawMode))
	switch m {
	case "":
		if clientID != "" {
			return feishuOnboardModeExisting, nil
		}
		return feishuOnboardModeCreate, nil
	case feishuOnboardModeCreate, feishuOnboardModeExisting:
		return m, nil
	default:
		return "", &feishuOnboardBadRequestError{msg: "不支持的开通模式，仅支持 create 或 existing"}
	}
}

// parseBeginExpireIn honors both RFC 8628 "expires_in" and SDK legacy "expire_in".
func parseBeginExpireIn(resp *feishuOnboardBeginResp) int {
	if resp != nil {
		if resp.ExpiresIn > 0 {
			return resp.ExpiresIn
		}
		if resp.ExpireIn > 0 {
			return resp.ExpireIn
		}
	}
	return feishuOnboardDefaultExpireSec
}

// encodeFeishuOnboardAddons builds the minimal Collie app addons payload
// (preset: false + 7 IM tenant scopes + im.message.receive_v1 + card.action.trigger)
// encoded as RawURLEncoding(gzip(json)).
func encodeFeishuOnboardAddons() (string, error) {
	payload := map[string]any{
		"preset": false,
		"scopes": map[string]any{
			"tenant": feishuOnboardTenantScopes,
		},
		"events": map[string]any{
			"items": map[string]any{
				"tenant": []string{"im.message.receive_v1"},
			},
		},
		"callbacks": map[string]any{
			"items": []string{"card.action.trigger"},
		},
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	if _, err := gz.Write(raw); err != nil {
		_ = gz.Close()
		return "", err
	}
	if err := gz.Close(); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(buf.Bytes()), nil
}

func (o *feishuOnboarder) ownerName() string {
	if o.ownerNameOverride != nil {
		return sanitizeFeishuOwnerName(*o.ownerNameOverride)
	}
	if o.sp != nil && o.sp.feishu != nil {
		return o.sp.feishu.ownerDisplayName()
	}
	return sanitizeFeishuOwnerName(lark.cachedWho())
}

// buildFeishuOnboardURL enriches Feishu's verification_uri_complete (/page/launcher)
// with official SDK provenance, minimal Collie addons, preset metadata (name,
// desc, circular Collie avatars), and createOnly / clientID mode selectors.
func buildFeishuOnboardURL(rawURL, mode, clientID string, persona feishuOnboardPersona) (string, error) {
	u, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil || u.Scheme == "" || u.Host == "" {
		return "", errors.New("飞书返回的确认链接格式无效")
	}
	addons, err := encodeFeishuOnboardAddons()
	if err != nil {
		return "", fmt.Errorf("编码权限模板失败：%w", err)
	}
	q := u.Query()
	q.Set("from", "sdk")
	q.Set("tp", "sdk")
	q.Set("source", feishuOnboardSource)
	q.Set("addons", addons)

	applyPersona := func() {
		if persona.Name != "" {
			q.Set("name", persona.Name)
		}
		if persona.Desc != "" {
			q.Set("desc", persona.Desc)
		}
		q.Del("avatar")
		for _, av := range persona.AvatarURLs {
			if strings.TrimSpace(av) != "" {
				q.Add("avatar", av)
			}
		}
	}

	clientID = strings.TrimSpace(clientID)
	if mode == feishuOnboardModeExisting {
		q.Del("createOnly")
		if clientID != "" {
			q.Set("clientID", clientID)
		} else {
			applyPersona()
		}
	} else {
		q.Del("clientID")
		q.Set("createOnly", "true")
		applyPersona()
	}
	u.RawQuery = q.Encode()
	return u.String(), nil
}

// Start initiates a new Feishu/Lark app registration or existing-app patching
// session, cancels any prior session, generates an inline PNG QR code Data URL,
// and launches background polling.
func (o *feishuOnboarder) Start(ctx context.Context, req FeishuOnboardStartRequest) (FeishuOnboardView, error) {
	region := normalizeFeishuOnboardRegion(req.Region)
	clientID := strings.TrimSpace(req.ClientID)
	mode, err := normalizeFeishuOnboardMode(req.Mode, clientID)
	if err != nil {
		return FeishuOnboardView{}, err
	}
	if mode == feishuOnboardModeCreate {
		clientID = ""
	}
	var persona feishuOnboardPersona
	if mode == feishuOnboardModeCreate || clientID == "" {
		persona, err = resolveFeishuOnboardPersona(region, req.Name, req.Desc, req.Avatar, o.ownerName())
		if err != nil {
			return FeishuOnboardView{}, err
		}
	}

	// Cancel any existing in-flight session immediately so at most one session runs.
	o.mu.Lock()
	if o.closed {
		o.mu.Unlock()
		return FeishuOnboardView{}, errors.New("服务正在关闭，无法发起飞书一键开通")
	}
	old := o.cur
	o.cur = nil
	baseCtx := o.rootCtx
	o.mu.Unlock()
	if old != nil && old.cancel != nil {
		old.cancel()
	}

	initialDomain := o.domainForRegion(region)
	beginCtx, beginCancel := context.WithTimeout(ctx, o.requestTimeout())
	defer beginCancel()

	beginResp, err := o.begin(beginCtx, initialDomain)
	if err != nil {
		return FeishuOnboardView{}, err
	}

	verifyURL := strings.TrimSpace(beginResp.VerificationURIComplete)
	if verifyURL == "" && strings.TrimSpace(beginResp.VerificationURI) != "" && strings.TrimSpace(beginResp.UserCode) != "" {
		if u, parseErr := url.Parse(strings.TrimSpace(beginResp.VerificationURI)); parseErr == nil {
			q := u.Query()
			q.Set("user_code", strings.TrimSpace(beginResp.UserCode))
			u.RawQuery = q.Encode()
			verifyURL = u.String()
		}
	}
	if verifyURL == "" {
		return FeishuOnboardView{}, errors.New("飞书接口未返回有效的确认页链接")
	}

	launchURL, err := buildFeishuOnboardURL(verifyURL, mode, clientID, persona)
	if err != nil {
		return FeishuOnboardView{}, err
	}
	pngBytes, err := qrcode.Encode(launchURL, qrcode.Low, feishuOnboardQRSize)
	if err != nil {
		return FeishuOnboardView{}, fmt.Errorf("生成二维码失败：%w", err)
	}
	qrDataURL := "data:image/png;base64," + base64.StdEncoding.EncodeToString(pngBytes)

	serverExpireSec := parseBeginExpireIn(beginResp)
	ttl := time.Duration(serverExpireSec) * time.Second
	if maxTTL := o.sessionMaxTTL(); ttl > maxTTL {
		ttl = maxTTL
	}
	if ttl <= 0 {
		ttl = time.Second
	}

	interval := time.Duration(beginResp.Interval) * time.Second
	if interval <= 0 {
		interval = feishuOnboardDefaultInterval
	}
	if maxI := o.pollMaxInterval(); interval > maxI {
		interval = maxI
	}

	now := o.now()
	pollCtx, pollCancel := context.WithTimeout(baseCtx, ttl)
	sess := &feishuOnboardSession{
		id:        randomToken()[:16],
		state:     feishuOnboardStateWaiting,
		mode:      mode,
		region:    region,
		clientID:  clientID,
		name:      persona.Name,
		desc:      persona.Desc,
		avatar:    persona.AvatarID,
		url:       launchURL,
		userCode:  strings.TrimSpace(beginResp.UserCode),
		qrDataURL: qrDataURL,
		expireIn:  int(ttl.Seconds()),
		expiresAt: now.Add(ttl),
		cancel:    pollCancel,
	}

	o.mu.Lock()
	if o.closed {
		o.mu.Unlock()
		pollCancel()
		return FeishuOnboardView{}, errors.New("服务正在关闭，无法发起飞书一键开通")
	}
	if o.cur != nil && o.cur.cancel != nil {
		o.cur.cancel()
	}
	o.cur = sess
	view := sess.view(now)
	o.wg.Add(1)
	o.mu.Unlock()

	go o.pollLoop(sess, pollCtx, beginResp.DeviceCode, initialDomain, region, interval)
	o.notifyChanged()
	return view, nil
}

func (o *feishuOnboarder) begin(ctx context.Context, domain string) (*feishuOnboardBeginResp, error) {
	form := url.Values{
		"action":            {"begin"},
		"archetype":         {"PersonalAgent"},
		"auth_method":       {"client_secret"},
		"request_user_info": {"open_id tenant_brand"},
	}
	var out feishuOnboardBeginResp
	status, err := o.doFormPost(ctx, domain, form, &out)
	if err != nil {
		return nil, fmt.Errorf("连接飞书开放平台失败：%w", err)
	}
	if out.Error != "" {
		msg := strings.TrimSpace(out.ErrorDesc)
		if msg == "" {
			msg = out.Error
		}
		return nil, fmt.Errorf("飞书发起开通失败：%s", o.redact(msg))
	}
	if status >= 400 {
		return nil, fmt.Errorf("飞书发起开通返回 HTTP %d", status)
	}
	if strings.TrimSpace(out.DeviceCode) == "" {
		return nil, errors.New("飞书接口未返回 device_code")
	}
	return &out, nil
}

func (o *feishuOnboarder) pollOnce(ctx context.Context, domain, deviceCode string) (*feishuOnboardPollResp, int, error) {
	reqCtx, cancel := context.WithTimeout(ctx, o.requestTimeout())
	defer cancel()
	form := url.Values{
		"action":      {"poll"},
		"device_code": {deviceCode},
	}
	var out feishuOnboardPollResp
	status, err := o.doFormPost(reqCtx, domain, form, &out)
	if err != nil {
		return nil, status, err
	}
	return &out, status, nil
}

func (o *feishuOnboarder) doFormPost(ctx context.Context, domain string, form url.Values, out any) (int, error) {
	endpoint := strings.TrimRight(domain, "/") + feishuOnboardEndpoint
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return 0, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := o.client().Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, feishuOnboardMaxBodyBytes))
	if err != nil {
		return resp.StatusCode, err
	}
	trimmed := bytes.TrimSpace(body)
	if len(trimmed) == 0 {
		return resp.StatusCode, fmt.Errorf("empty response body (HTTP %d)", resp.StatusCode)
	}
	if err := json.Unmarshal(trimmed, out); err != nil {
		return resp.StatusCode, fmt.Errorf("invalid JSON response (HTTP %d): %w", resp.StatusCode, err)
	}
	return resp.StatusCode, nil
}

func (o *feishuOnboarder) pollLoop(sess *feishuOnboardSession, pollCtx context.Context, deviceCode, initialDomain, initialRegion string, interval time.Duration) {
	defer o.wg.Done()
	defer sess.cancel()

	currentDomain := initialDomain
	currentRegion := initialRegion
	switchedDomain := false
	waitBeforePoll := false
	nextWait := interval

	for {
		if waitBeforePoll {
			if err := o.wait(pollCtx, nextWait); err != nil {
				if errors.Is(err, context.DeadlineExceeded) || errors.Is(pollCtx.Err(), context.DeadlineExceeded) {
					o.finishError(sess, feishuOnboardStateExpired, "等待确认超时，请重新生成二维码")
				}
				return
			}
		}
		waitBeforePoll = true
		nextWait = interval

		resp, status, err := o.pollOnce(pollCtx, currentDomain, deviceCode)
		if pollCtx.Err() != nil {
			if errors.Is(pollCtx.Err(), context.DeadlineExceeded) {
				o.finishError(sess, feishuOnboardStateExpired, "等待确认超时，请重新生成二维码")
			}
			return
		}
		if err != nil || status >= 500 {
			// Transient network glitch or 5xx: preserve session and retry after interval+1s.
			retryWait := interval + time.Second
			if maxI := o.pollMaxInterval(); retryWait > maxI {
				retryWait = maxI
			}
			nextWait = retryWait
			if err != nil {
				log.Printf("feishu-onboard: transient poll error (retrying): %s", o.redact(err.Error()))
			} else {
				log.Printf("feishu-onboard: transient poll HTTP %d (retrying)", status)
			}
			continue
		}

		// 1. Completed credentials received.
		appID := strings.TrimSpace(resp.ClientID)
		appSecret := strings.TrimSpace(resp.ClientSecret)
		if appID != "" && appSecret != "" {
			finalRegion := currentRegion
			openID := ""
			if resp.UserInfo != nil {
				openID = strings.TrimSpace(resp.UserInfo.OpenID)
				switch strings.ToLower(strings.TrimSpace(resp.UserInfo.TenantBrand)) {
				case "lark":
					finalRegion = "intl"
				case "feishu":
					finalRegion = "cn"
				}
			}
			if err := o.applyCredentials(appID, appSecret, finalRegion, openID); err != nil {
				msg := strings.ReplaceAll(o.redact(err.Error()), appSecret, "***")
				o.finishError(sess, feishuOnboardStateError, "保存飞书配置失败："+msg)
				return
			}
			o.finishCompleted(sess, appID, openID, finalRegion)
			return
		}

		// 2. Cross-brand tenant discovery (Feishu <-> Lark).
		if resp.UserInfo != nil && !switchedDomain {
			brand := strings.ToLower(strings.TrimSpace(resp.UserInfo.TenantBrand))
			if brand == "lark" && currentDomain != o.larkDomain() {
				currentDomain = o.larkDomain()
				currentRegion = "intl"
				switchedDomain = true
				o.markDomainSwitched(sess, "intl")
				waitBeforePoll = false
				continue
			}
			if brand == "feishu" && currentDomain != o.feishuDomain() {
				currentDomain = o.feishuDomain()
				currentRegion = "cn"
				switchedDomain = true
				o.markDomainSwitched(sess, "cn")
				waitBeforePoll = false
				continue
			}
		}

		// 3. RFC 8628 Device Flow poll status codes.
		switch strings.TrimSpace(resp.Error) {
		case "", "authorization_pending":
			// Keep polling.
		case "slow_down":
			interval += o.pollSlowDownStep()
			if maxI := o.pollMaxInterval(); interval > maxI {
				interval = maxI
			}
			nextWait = interval
		case "access_denied":
			o.finishError(sess, feishuOnboardStateDenied, "你已取消或拒绝在飞书中确认授权")
			return
		case "expired_token", "invalid_grant":
			o.finishError(sess, feishuOnboardStateExpired, "确认链接已过期，请重新生成二维码")
			return
		default:
			desc := strings.TrimSpace(resp.ErrorDesc)
			if desc == "" {
				desc = strings.TrimSpace(resp.Error)
			}
			o.finishError(sess, feishuOnboardStateError, "飞书确认异常："+o.redact(desc))
			return
		}
	}
}

func (o *feishuOnboarder) markDomainSwitched(sess *feishuOnboardSession, region string) {
	o.mu.Lock()
	active := o.cur == sess
	if active {
		sess.state = feishuOnboardStateDomainSwitched
		sess.region = region
	}
	o.mu.Unlock()
	if active {
		o.notifyChanged()
	}
}

func (o *feishuOnboarder) finishCompleted(sess *feishuOnboardSession, appID, openID, region string) {
	o.mu.Lock()
	active := o.cur == sess
	if active {
		sess.state = feishuOnboardStateCompleted
		sess.appID = appID
		sess.openID = openID
		sess.region = region
		sess.url = ""
		sess.qrDataURL = ""
		sess.errMsg = ""
	}
	o.mu.Unlock()
	if active {
		o.notifyChanged()
	}
}

func (o *feishuOnboarder) finishError(sess *feishuOnboardSession, state, msg string) {
	o.mu.Lock()
	active := o.cur == sess
	if active {
		sess.state = state
		sess.errMsg = msg
		sess.url = ""
		sess.qrDataURL = ""
	}
	o.mu.Unlock()
	if active {
		o.notifyChanged()
	}
}

func (o *feishuOnboarder) applyCredentials(appID, appSecret, region, openID string) error {
	if o.mgr == nil {
		return errors.New("integrations manager not ready")
	}
	snap := o.mgr.ConfigSnapshot(feishuKind)
	fc := parseFeishuConfig(snap.Config)
	prevAppID := strings.TrimSpace(fc.AppID)
	fc.Region = normalizeFeishuOnboardRegion(region)
	fc.AppID = appID
	if openID != "" {
		if prevAppID != "" && prevAppID != appID {
			// Feishu open_ids are app-scoped. When atomically swapping to a
			// different bot, preserve all previously allowlisted open_ids while
			// placing the newly confirmed bot's owner open_id first so self-test
			// and fallback outbound cards target a valid open_id immediately.
			rest := make([]string, 0, len(fc.AllowOpenIDs))
			for _, id := range fc.AllowOpenIDs {
				if id != openID {
					rest = append(rest, id)
				}
			}
			fc.AllowOpenIDs = append([]string{openID}, rest...)
		} else if !slices.Contains(fc.AllowOpenIDs, openID) {
			fc.AllowOpenIDs = append(fc.AllowOpenIDs, openID)
		}
	}
	cfgJSON, err := json.Marshal(fc)
	if err != nil {
		return err
	}
	enabled := true
	_, err = o.mgr.Save(feishuKind, SaveRequest{
		Enabled: &enabled,
		Config:  cfgJSON,
		Secrets: map[string]string{
			"app_id":     appID,
			"app_secret": appSecret,
		},
	})
	return err
}
