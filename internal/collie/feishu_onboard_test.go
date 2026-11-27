package collie

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
	"unicode/utf8"

	collieWeb "github.com/lihuanyu0w0/collie/web"
)

func decodeAddonsParam(t *testing.T, raw string) map[string]any {
	t.Helper()
	gzBytes, err := base64.RawURLEncoding.DecodeString(raw)
	if err != nil {
		t.Fatalf("base64url decode addons: %v", err)
	}
	zr, err := gzip.NewReader(bytes.NewReader(gzBytes))
	if err != nil {
		t.Fatalf("gzip reader: %v", err)
	}
	defer zr.Close()
	plain, err := io.ReadAll(zr)
	if err != nil {
		t.Fatalf("gzip read: %v", err)
	}
	var out map[string]any
	if err := json.Unmarshal(plain, &out); err != nil {
		t.Fatalf("json unmarshal addons: %v (raw=%s)", err, string(plain))
	}
	return out
}

func assertMinimalCollieAddons(t *testing.T, launchURL string) *url.URL {
	t.Helper()
	u, err := url.Parse(launchURL)
	if err != nil {
		t.Fatalf("parse launch url %q: %v", launchURL, err)
	}
	q := u.Query()
	if got := q.Get("from"); got != "sdk" {
		t.Fatalf("from=%q, want sdk", got)
	}
	if got := q.Get("tp"); got != "sdk" {
		t.Fatalf("tp=%q, want sdk", got)
	}
	if got := q.Get("source"); got != "go-sdk/collie" {
		t.Fatalf("source=%q, want go-sdk/collie", got)
	}
	addonsRaw := q.Get("addons")
	if addonsRaw == "" {
		t.Fatalf("missing addons query param in %q", launchURL)
	}
	addons := decodeAddonsParam(t, addonsRaw)
	if preset, ok := addons["preset"].(bool); !ok || preset {
		t.Fatalf("addons.preset=%v (ok=%v), want false", addons["preset"], ok)
	}
	scopesMap, _ := addons["scopes"].(map[string]any)
	rawTenantScopes, _ := scopesMap["tenant"].([]any)
	var gotScopes []string
	for _, s := range rawTenantScopes {
		gotScopes = append(gotScopes, s.(string))
	}
	if !reflect.DeepEqual(gotScopes, feishuOnboardTenantScopes) {
		t.Fatalf("addons.scopes.tenant=%v, want %v", gotScopes, feishuOnboardTenantScopes)
	}
	eventsMap, _ := addons["events"].(map[string]any)
	eventItems, _ := eventsMap["items"].(map[string]any)
	rawEvs, _ := eventItems["tenant"].([]any)
	if len(rawEvs) != 1 || rawEvs[0] != "im.message.receive_v1" {
		t.Fatalf("addons.events.items.tenant=%v, want [im.message.receive_v1]", rawEvs)
	}
	cbMap, _ := addons["callbacks"].(map[string]any)
	rawCBs, _ := cbMap["items"].([]any)
	if len(rawCBs) != 1 || rawCBs[0] != "card.action.trigger" {
		t.Fatalf("addons.callbacks.items=%v, want [card.action.trigger]", rawCBs)
	}
	return u
}

func setupOnboardHarness(t *testing.T, cnHandler, larkHandler http.HandlerFunc) (*Server, *Super, *http.ServeMux) {
	t.Helper()
	dir := t.TempDir()
	s := &Server{token: "tok-onboard-test", home: dir}
	sp, err := newSuper(s, dir)
	if err != nil {
		t.Fatalf("newSuper: %v", err)
	}
	t.Cleanup(func() {
		sp.feishuOnboard.Close()
		sp.db.Close()
	})
	s.sup = sp

	if cnHandler != nil {
		cnSrv := httptest.NewServer(cnHandler)
		t.Cleanup(cnSrv.Close)
		sp.feishuOnboard.feishuDomainOverride = cnSrv.URL
		sp.feishuOnboard.httpClient = cnSrv.Client()
	}
	if larkHandler != nil {
		larkSrv := httptest.NewServer(larkHandler)
		t.Cleanup(larkSrv.Close)
		sp.feishuOnboard.larkDomainOverride = larkSrv.URL
	}
	sp.feishuOnboard.waitFn = func(ctx context.Context, d time.Duration) error {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(5 * time.Millisecond):
			return nil
		}
	}
	emptyOwner := ""
	sp.feishuOnboard.ownerNameOverride = &emptyOwner

	mux := http.NewServeMux()
	s.superRoutes(mux)
	return s, sp, mux
}

func authedReq(method, target, body, token string) *http.Request {
	var r io.Reader
	if body != "" {
		r = strings.NewReader(body)
	}
	req := httptest.NewRequest(method, target, r)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	return req
}

func waitOnboardState(t *testing.T, ob *feishuOnboarder, want string) FeishuOnboardView {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		v := ob.Status()
		if v.State == want {
			return v
		}
		time.Sleep(10 * time.Millisecond)
	}
	got := ob.Status()
	t.Fatalf("timed out waiting for onboard state %q, got %+v", want, got)
	return got
}

func TestFeishuOnboardParseExpireIn(t *testing.T) {
	t.Parallel()
	if got := parseBeginExpireIn(&feishuOnboardBeginResp{ExpiresIn: 3600}); got != 3600 {
		t.Fatalf("expires_in=3600 -> got %d, want 3600", got)
	}
	if got := parseBeginExpireIn(&feishuOnboardBeginResp{ExpireIn: 900}); got != 900 {
		t.Fatalf("expire_in=900 -> got %d, want 900", got)
	}
	if got := parseBeginExpireIn(&feishuOnboardBeginResp{}); got != feishuOnboardDefaultExpireSec {
		t.Fatalf("empty -> got %d, want %d", got, feishuOnboardDefaultExpireSec)
	}
}

func TestFeishuOnboardCreateSuccessAndZeroSecretLeak(t *testing.T) {
	t.Parallel()
	const (
		secretVal  = "sec_top_secret_never_leak_999"
		deviceCode = "v1:devcode_never_expose_888"
		appID      = "cli_created_new_001"
		ownerOID   = "ou_owner_onboard_001"
	)

	var logBuf bytes.Buffer
	origOut := log.Writer()
	log.SetOutput(&logBuf)
	t.Cleanup(func() { log.SetOutput(origOut) })

	var beginForm url.Values
	var pollCount atomic.Int32
	s, sp, mux := setupOnboardHarness(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != feishuOnboardEndpoint {
			http.NotFound(w, r)
			return
		}
		_ = r.ParseForm()
		switch r.Form.Get("action") {
		case "begin":
			beginForm = r.Form
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{
				"device_code": "` + deviceCode + `",
				"user_code": "ABCD-1234",
				"verification_uri": "https://open.feishu.cn/page/launcher",
				"verification_uri_complete": "https://open.feishu.cn/page/launcher?user_code=ABCD-1234",
				"expires_in": 3600,
				"interval": 1
			}`))
		case "poll":
			if r.Form.Get("device_code") != deviceCode {
				t.Errorf("poll device_code=%q, want %q", r.Form.Get("device_code"), deviceCode)
			}
			n := pollCount.Add(1)
			w.Header().Set("Content-Type", "application/json")
			if n == 1 {
				// RFC 8628 returns HTTP 400 + authorization_pending while waiting.
				w.WriteHeader(http.StatusBadRequest)
				_, _ = w.Write([]byte(`{"error":"authorization_pending"}`))
				return
			}
			_, _ = w.Write([]byte(`{
				"client_id": "` + appID + `",
				"client_secret": "` + secretVal + `",
				"user_info": {"open_id": "` + ownerOID + `", "tenant_brand": "feishu"}
			}`))
		default:
			w.WriteHeader(400)
		}
	}, nil)

	// 1. Auth protection check on all three routes.
	for _, tc := range []struct{ method, body string }{
		{http.MethodPost, `{}`},
		{http.MethodGet, ""},
		{http.MethodDelete, ""},
	} {
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, authedReq(tc.method, "/api/super/integrations/feishu/onboard", tc.body, ""))
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("unauthenticated %s returned %d, want 401", tc.method, rec.Code)
		}
	}

	// 2. Start create-mode onboard session.
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, authedReq(http.MethodPost, "/api/super/integrations/feishu/onboard", `{"region":"cn","mode":"create"}`, s.token))
	if rec.Code != http.StatusOK {
		t.Fatalf("POST onboard: status=%d body=%s", rec.Code, rec.Body.String())
	}
	postBody := rec.Body.String()
	if strings.Contains(postBody, deviceCode) || strings.Contains(postBody, secretVal) {
		t.Fatalf("POST response leaked sensitive value: %s", postBody)
	}

	var startView FeishuOnboardView
	if err := json.Unmarshal(rec.Body.Bytes(), &startView); err != nil {
		t.Fatalf("decode startView: %v", err)
	}
	if startView.State != feishuOnboardStateWaiting || startView.Mode != feishuOnboardModeCreate || startView.Region != "cn" {
		t.Fatalf("unexpected startView: %+v", startView)
	}
	if startView.ExpireIn != int(feishuOnboardMaxTTL.Seconds()) {
		t.Fatalf("expire_in=%d, want capped %d", startView.ExpireIn, int(feishuOnboardMaxTTL.Seconds()))
	}
	if beginForm.Get("archetype") != "PersonalAgent" || beginForm.Get("auth_method") != "client_secret" || beginForm.Get("request_user_info") != "open_id tenant_brand" {
		t.Fatalf("unexpected begin form: %v", beginForm)
	}

	// Verify launcher URL parameters + addons payload.
	u := assertMinimalCollieAddons(t, startView.URL)
	if u.Query().Get("createOnly") != "true" {
		t.Fatalf("create mode missing createOnly=true: %s", startView.URL)
	}
	if u.Query().Get("clientID") != "" {
		t.Fatalf("create mode should not set clientID: %s", startView.URL)
	}
	if u.Query().Get("name") != feishuOnboardDefaultName {
		t.Fatalf("name=%q, want %q", u.Query().Get("name"), feishuOnboardDefaultName)
	}
	if gotDesc := u.Query().Get("desc"); gotDesc != feishuOnboardDefaultDesc || !strings.Contains(gotDesc, collieHomepageURL) {
		t.Fatalf("desc=%q, want default desc containing %q", gotDesc, collieHomepageURL)
	}
	if utf8.RuneCountInString(feishuOnboardDefaultDesc) > feishuOnboardCNMaxDescRunes {
		t.Fatalf("default desc exceeds CN 120-char limit: %d", utf8.RuneCountInString(feishuOnboardDefaultDesc))
	}
	gotAvatars := u.Query()["avatar"]
	if len(gotAvatars) != 4 || !strings.HasSuffix(gotAvatars[0], "/avatars/cobalt.png") {
		t.Fatalf("avatar query=%v, want 4 presets starting with cobalt.png", gotAvatars)
	}
	if startView.Avatar != "cobalt" || startView.Name != feishuOnboardDefaultName || startView.Desc != feishuOnboardDefaultDesc {
		t.Fatalf("startView persona mismatch: name=%q desc=%q avatar=%q", startView.Name, startView.Desc, startView.Avatar)
	}

	// Verify PNG QR Data URL.
	const pngPrefix = "data:image/png;base64,"
	if !strings.HasPrefix(startView.QRDataURL, pngPrefix) {
		t.Fatalf("qr_data_url missing PNG prefix: %q", startView.QRDataURL[:min(len(startView.QRDataURL), 40)])
	}
	pngBytes, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(startView.QRDataURL, pngPrefix))
	if err != nil || len(pngBytes) < 8 || !bytes.Equal(pngBytes[:8], []byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n'}) {
		t.Fatalf("qr_data_url did not decode to valid PNG (err=%v, len=%d)", err, len(pngBytes))
	}

	// 3. Wait for completion and verify status endpoint leaks nothing.
	doneView := waitOnboardState(t, sp.feishuOnboard, feishuOnboardStateCompleted)
	if doneView.AppID != appID || doneView.OpenID != ownerOID || doneView.Region != "cn" {
		t.Fatalf("unexpected completed view: %+v", doneView)
	}

	getRec := httptest.NewRecorder()
	mux.ServeHTTP(getRec, authedReq(http.MethodGet, "/api/super/integrations/feishu/onboard", "", s.token))
	if getRec.Code != http.StatusOK {
		t.Fatalf("GET onboard: status=%d", getRec.Code)
	}
	if strings.Contains(getRec.Body.String(), secretVal) || strings.Contains(getRec.Body.String(), deviceCode) {
		t.Fatalf("GET onboard leaked sensitive value: %s", getRec.Body.String())
	}

	// 4. Verify secrets.json (0600) + IntegrationManager persisted config + zero API/log leak.
	fi, err := os.Stat(sp.secrets.Path())
	if err != nil {
		t.Fatalf("stat secrets.json: %v", err)
	}
	if perm := fi.Mode().Perm(); perm != 0o600 {
		t.Fatalf("secrets.json perm=%o, want 0600", perm)
	}
	if gotSec := sp.secrets.GetKey(feishuKind, "app_secret"); gotSec != secretVal {
		t.Fatalf("stored app_secret mismatch")
	}

	listRec := httptest.NewRecorder()
	mux.ServeHTTP(listRec, authedReq(http.MethodGet, "/api/super/integrations", "", s.token))
	if strings.Contains(listRec.Body.String(), secretVal) {
		t.Fatalf("GET /api/super/integrations leaked app_secret!")
	}
	if strings.Contains(logBuf.String(), secretVal) || strings.Contains(logBuf.String(), deviceCode) {
		t.Fatalf("logs leaked secret or device_code: %s", logBuf.String())
	}

	iv, ok := sp.integrations.View(feishuKind)
	if !ok || !iv.Configured || !iv.Enabled || !iv.SecretSet {
		t.Fatalf("feishu integration view not active: %+v", iv)
	}
	if iv.Config["app_id"] != appID || iv.Config["region"] != "cn" {
		t.Fatalf("unexpected saved config: %+v", iv.Config)
	}
	oids, _ := iv.Config["allow_open_ids"].([]string)
	if !slices.Contains(oids, ownerOID) {
		t.Fatalf("allow_open_ids=%v missing %q", oids, ownerOID)
	}
}

func TestFeishuOnboardExistingAppModesAndConfigMerge(t *testing.T) {
	t.Parallel()
	const existingID = "cli_existing_bot_42"
	const existingSecret = "sec_existing_patched_777"

	var pollCalls atomic.Int32
	s, sp, mux := setupOnboardHarness(t, func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		w.Header().Set("Content-Type", "application/json")
		switch r.Form.Get("action") {
		case "begin":
			_, _ = w.Write([]byte(`{
				"device_code": "dev_existing_1",
				"user_code": "EXST-0001",
				"verification_uri_complete": "https://open.feishu.cn/page/launcher?user_code=EXST-0001",
				"expires_in": 600,
				"interval": 1
			}`))
		case "poll":
			if pollCalls.Add(1) == 1 {
				w.WriteHeader(http.StatusBadRequest)
				_, _ = w.Write([]byte(`{"error":"authorization_pending"}`))
				return
			}
			_, _ = w.Write([]byte(`{
				"client_id": "` + existingID + `",
				"client_secret": "` + existingSecret + `",
				"user_info": {"open_id": "ou_second_owner", "tenant_brand": "feishu"}
			}`))
		}
	}, nil)

	// Pre-populate existing preferences and an existing allowlisted OpenID to ensure merge preserves them.
	preCfg, _ := json.Marshal(FeishuConfig{
		Region:           "cn",
		AppID:            existingID,
		AllowOpenIDs:     []string{"ou_first_owner"},
		FeedbackChatIDs:  []string{"oc_feedback_group_01"},
		GroupMentionOnly: false,
		InstantChatSync:  false,
	})
	en := true
	if _, err := sp.integrations.Save(feishuKind, SaveRequest{
		Enabled: &en,
		Config:  preCfg,
		Secrets: map[string]string{"app_id": existingID, "app_secret": "old_secret_123456"},
	}); err != nil {
		t.Fatalf("pre-save feishu: %v", err)
	}

	// Case 1: existing mode WITHOUT client_id (user picks app on launcher page).
	rec1 := httptest.NewRecorder()
	mux.ServeHTTP(rec1, authedReq(http.MethodPost, "/api/super/integrations/feishu/onboard", `{"region":"cn","mode":"existing"}`, s.token))
	if rec1.Code != http.StatusOK {
		t.Fatalf("existing(no client_id) status=%d body=%s", rec1.Code, rec1.Body.String())
	}
	var v1 FeishuOnboardView
	_ = json.Unmarshal(rec1.Body.Bytes(), &v1)
	u1 := assertMinimalCollieAddons(t, v1.URL)
	if u1.Query().Has("createOnly") {
		t.Fatalf("existing mode must not set createOnly: %s", v1.URL)
	}
	if u1.Query().Has("clientID") {
		t.Fatalf("existing mode without client_id must not set clientID: %s", v1.URL)
	}

	// Cancel before testing Case 2.
	delRec := httptest.NewRecorder()
	mux.ServeHTTP(delRec, authedReq(http.MethodDelete, "/api/super/integrations/feishu/onboard", "", s.token))
	if delRec.Code != http.StatusOK {
		t.Fatalf("DELETE onboard: %d", delRec.Code)
	}

	// Case 2: existing mode WITH client_id (one-click repair / patch callback & scopes).
	pollCalls.Store(0)
	rec2 := httptest.NewRecorder()
	mux.ServeHTTP(rec2, authedReq(http.MethodPost, "/api/super/integrations/feishu/onboard", `{"region":"cn","mode":"existing","client_id":"`+existingID+`"}`, s.token))
	if rec2.Code != http.StatusOK {
		t.Fatalf("existing(with client_id) status=%d body=%s", rec2.Code, rec2.Body.String())
	}
	var v2 FeishuOnboardView
	_ = json.Unmarshal(rec2.Body.Bytes(), &v2)
	u2 := assertMinimalCollieAddons(t, v2.URL)
	if u2.Query().Has("createOnly") {
		t.Fatalf("existing+clientID must not set createOnly: %s", v2.URL)
	}
	if got := u2.Query().Get("clientID"); got != existingID {
		t.Fatalf("clientID=%q, want %q", got, existingID)
	}

	waitOnboardState(t, sp.feishuOnboard, feishuOnboardStateCompleted)

	// Verify existing preferences and first OpenID were preserved while merging second OpenID.
	snap := sp.integrations.ConfigSnapshot(feishuKind)
	fc := parseFeishuConfig(snap.Config)
	if fc.GroupMentionOnly || fc.InstantChatSync {
		t.Fatalf("existing boolean prefs were overwritten: %+v", fc)
	}
	if !slices.Contains(fc.AllowOpenIDs, "ou_first_owner") || !slices.Contains(fc.AllowOpenIDs, "ou_second_owner") {
		t.Fatalf("allow_open_ids=%v, want both ou_first_owner and ou_second_owner", fc.AllowOpenIDs)
	}
	if !slices.Contains(fc.FeedbackChatIDs, "oc_feedback_group_01") {
		t.Fatalf("feedback_chat_ids=%v, want preserved oc_feedback_group_01", fc.FeedbackChatIDs)
	}
	if sp.secrets.GetKey(feishuKind, "app_secret") != existingSecret {
		t.Fatalf("updated secret not persisted")
	}
}

func TestFeishuOnboardAtomicSwapPreservesAllowlistAndFeedbackChats(t *testing.T) {
	t.Parallel()
	const (
		oldAppID  = "cli_old_bot_001"
		oldSecret = "sec_old_bot_secret_001"
		oldOwner  = "ou_old_bot_owner_001"
		fbChatID  = "oc_feedback_keep_001"
		newAppID  = "cli_new_bot_002"
		newSecret = "sec_new_bot_secret_002"
		newOwner  = "ou_new_bot_owner_002"
	)

	var mode atomic.Int32 // 0 = pending, 1 = denied, 2 = complete
	s, sp, mux := setupOnboardHarness(t,
		func(w http.ResponseWriter, r *http.Request) {
			_ = r.ParseForm()
			w.Header().Set("Content-Type", "application/json")
			if r.Form.Get("action") == "begin" {
				_, _ = w.Write([]byte(`{"device_code":"dev_swap","user_code":"SWAP-01","verification_uri_complete":"https://open.feishu.cn/page/launcher?user_code=SWAP-01","expires_in":600,"interval":1}`))
				return
			}
			switch mode.Load() {
			case 0:
				w.WriteHeader(http.StatusBadRequest)
				_, _ = w.Write([]byte(`{"error":"authorization_pending"}`))
			case 1:
				w.WriteHeader(http.StatusBadRequest)
				_, _ = w.Write([]byte(`{"error":"access_denied"}`))
			default:
				_, _ = w.Write([]byte(`{"client_id":"` + newAppID + `","client_secret":"` + newSecret + `","user_info":{"open_id":"` + newOwner + `","tenant_brand":"feishu"}}`))
			}
		},
		nil,
	)

	// Pre-populate configured Bot A with allowlist, feedback chat, and preferences.
	preCfg, _ := json.Marshal(FeishuConfig{
		Region:           "cn",
		AppID:            oldAppID,
		AllowOpenIDs:     []string{oldOwner},
		FeedbackChatIDs:  []string{fbChatID},
		GroupMentionOnly: true,
		InstantChatSync:  false,
	})
	en := true
	if _, err := sp.integrations.Save(feishuKind, SaveRequest{
		Enabled: &en,
		Config:  preCfg,
		Secrets: map[string]string{"app_id": oldAppID, "app_secret": oldSecret},
	}); err != nil {
		t.Fatalf("pre-save bot A: %v", err)
	}

	assertBotAIntact := func(stage string) {
		t.Helper()
		snap := sp.integrations.ConfigSnapshot(feishuKind)
		fc := parseFeishuConfig(snap.Config)
		if !snap.Enabled || fc.AppID != oldAppID || fc.Region != "cn" || sp.secrets.GetKey(feishuKind, "app_secret") != oldSecret {
			t.Fatalf("%s mutated original bot A credentials: enabled=%v fc=%+v", stage, snap.Enabled, fc)
		}
		if !slices.Equal(fc.AllowOpenIDs, []string{oldOwner}) || !slices.Equal(fc.FeedbackChatIDs, []string{fbChatID}) {
			t.Fatalf("%s mutated allowlist/feedback: allow=%v fb=%v", stage, fc.AllowOpenIDs, fc.FeedbackChatIDs)
		}
	}

	// 1) Start swap and cancel midway -> Bot A must remain untouched.
	mode.Store(0)
	rec1 := httptest.NewRecorder()
	mux.ServeHTTP(rec1, authedReq(http.MethodPost, "/api/super/integrations/feishu/onboard", `{"region":"cn","mode":"create","avatar":"pine"}`, s.token))
	if rec1.Code != http.StatusOK {
		t.Fatalf("start swap 1: %d %s", rec1.Code, rec1.Body.String())
	}
	assertBotAIntact("while swap waiting")
	delRec := httptest.NewRecorder()
	mux.ServeHTTP(delRec, authedReq(http.MethodDelete, "/api/super/integrations/feishu/onboard", "", s.token))
	if delRec.Code != http.StatusOK {
		t.Fatalf("cancel swap: %d", delRec.Code)
	}
	assertBotAIntact("after swap cancel")

	// 2) Start swap and get denied -> Bot A must remain untouched.
	mode.Store(1)
	rec2 := httptest.NewRecorder()
	mux.ServeHTTP(rec2, authedReq(http.MethodPost, "/api/super/integrations/feishu/onboard", `{"region":"cn","mode":"create"}`, s.token))
	if rec2.Code != http.StatusOK {
		t.Fatalf("start swap 2: %d", rec2.Code)
	}
	waitOnboardState(t, sp.feishuOnboard, feishuOnboardStateDenied)
	assertBotAIntact("after swap denied")

	// 3) Start swap and confirm -> atomically replaces app_id/app_secret while
	//    preserving allow_open_ids (new owner first) and feedback_chat_ids.
	mode.Store(2)
	rec3 := httptest.NewRecorder()
	mux.ServeHTTP(rec3, authedReq(http.MethodPost, "/api/super/integrations/feishu/onboard", `{"region":"cn","mode":"create","avatar":"terracotta"}`, s.token))
	if rec3.Code != http.StatusOK {
		t.Fatalf("start swap 3: %d", rec3.Code)
	}
	done := waitOnboardState(t, sp.feishuOnboard, feishuOnboardStateCompleted)
	if done.AppID != newAppID || done.OpenID != newOwner {
		t.Fatalf("unexpected swap completion view: %+v", done)
	}
	snap := sp.integrations.ConfigSnapshot(feishuKind)
	fc := parseFeishuConfig(snap.Config)
	if !snap.Enabled || fc.AppID != newAppID || sp.secrets.GetKey(feishuKind, "app_secret") != newSecret {
		t.Fatalf("swap did not atomically apply new credentials: enabled=%v fc=%+v", snap.Enabled, fc)
	}
	if !slices.Equal(fc.AllowOpenIDs, []string{newOwner, oldOwner}) {
		t.Fatalf("swap allow_open_ids = %v, want [%s %s]", fc.AllowOpenIDs, newOwner, oldOwner)
	}
	if !slices.Equal(fc.FeedbackChatIDs, []string{fbChatID}) {
		t.Fatalf("swap feedback_chat_ids = %v, want [%s]", fc.FeedbackChatIDs, fbChatID)
	}
	if !fc.GroupMentionOnly || fc.InstantChatSync {
		t.Fatalf("swap overwrote boolean prefs: %+v", fc)
	}
}

func TestFeishuOnboardCrossBrandSwitchToLark(t *testing.T) {
	t.Parallel()
	var cnPolls, larkPolls atomic.Int32
	var sawSwitched atomic.Bool

	s, sp, mux := setupOnboardHarness(t,
		func(w http.ResponseWriter, r *http.Request) {
			_ = r.ParseForm()
			w.Header().Set("Content-Type", "application/json")
			if r.Form.Get("action") == "begin" {
				_, _ = w.Write([]byte(`{
					"device_code": "dev_cross_1",
					"user_code": "LARK-9999",
					"verification_uri_complete": "https://open.feishu.cn/page/launcher?user_code=LARK-9999",
					"expires_in": 600,
					"interval": 1
				}`))
				return
			}
			cnPolls.Add(1)
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"error":"authorization_pending","user_info":{"tenant_brand":"lark"}}`))
		},
		func(w http.ResponseWriter, r *http.Request) {
			_ = r.ParseForm()
			n := larkPolls.Add(1)
			w.Header().Set("Content-Type", "application/json")
			if n == 1 {
				// Observe domain_switched state while waiting on Lark domain.
				w.WriteHeader(http.StatusBadRequest)
				_, _ = w.Write([]byte(`{"error":"authorization_pending"}`))
				return
			}
			_, _ = w.Write([]byte(`{
				"client_id": "cli_lark_intl_007",
				"client_secret": "sec_lark_intl_secret_007",
				"user_info": {"open_id": "ou_lark_user_007", "tenant_brand": "lark"}
			}`))
		},
	)

	origWait := sp.feishuOnboard.waitFn
	sp.feishuOnboard.waitFn = func(ctx context.Context, d time.Duration) error {
		if sp.feishuOnboard.Status().State == feishuOnboardStateDomainSwitched {
			sawSwitched.Store(true)
		}
		return origWait(ctx, d)
	}

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, authedReq(http.MethodPost, "/api/super/integrations/feishu/onboard", `{"region":"cn","mode":"create"}`, s.token))
	if rec.Code != http.StatusOK {
		t.Fatalf("POST onboard: %d %s", rec.Code, rec.Body.String())
	}

	done := waitOnboardState(t, sp.feishuOnboard, feishuOnboardStateCompleted)
	if !sawSwitched.Load() {
		t.Fatalf("never observed domain_switched state during cross-brand handoff")
	}
	if cnPolls.Load() != 1 || larkPolls.Load() < 2 {
		t.Fatalf("unexpected poll counts: cn=%d lark=%d", cnPolls.Load(), larkPolls.Load())
	}
	if done.Region != "intl" || done.AppID != "cli_lark_intl_007" {
		t.Fatalf("unexpected final view after Lark switch: %+v", done)
	}
	fc := parseFeishuConfig(sp.integrations.ConfigSnapshot(feishuKind).Config)
	if fc.Region != "intl" {
		t.Fatalf("saved region=%q, want intl", fc.Region)
	}
}

func TestFeishuOnboardSlowDownAndNetworkRetry(t *testing.T) {
	t.Parallel()
	var pollStep atomic.Int32
	var waitsMu sync.Mutex
	var waits []time.Duration

	_, sp, mux := setupOnboardHarness(t, func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		if r.Form.Get("action") == "begin" {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{
				"device_code": "dev_retry_1",
				"user_code": "RTRY-0001",
				"verification_uri_complete": "https://open.feishu.cn/page/launcher?user_code=RTRY-0001",
				"expires_in": 600,
				"interval": 2
			}`))
			return
		}
		switch pollStep.Add(1) {
		case 1:
			// Simulate abrupt TCP EOF drop.
			if hj, ok := w.(http.Hijacker); ok {
				if conn, _, err := hj.Hijack(); err == nil {
					_ = conn.Close()
					return
				}
			}
			w.WriteHeader(http.StatusBadGateway)
		case 2:
			// Simulate transient 502 with empty body.
			w.WriteHeader(http.StatusBadGateway)
		case 3:
			// Trigger slow_down (+5s -> 7s).
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"error":"slow_down"}`))
		case 4:
			// Trigger second slow_down (+5s -> 12s).
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"error":"slow_down"}`))
		default:
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{
				"client_id": "cli_resilient_01",
				"client_secret": "sec_resilient_01_secret",
				"user_info": {"open_id": "ou_resilient", "tenant_brand": "feishu"}
			}`))
		}
	}, nil)

	sp.feishuOnboard.waitFn = func(ctx context.Context, d time.Duration) error {
		waitsMu.Lock()
		waits = append(waits, d)
		waitsMu.Unlock()
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(5 * time.Millisecond):
			return nil
		}
	}

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, authedReq(http.MethodPost, "/api/super/integrations/feishu/onboard", `{"region":"cn"}`, "tok-onboard-test"))
	if rec.Code != http.StatusOK {
		t.Fatalf("POST onboard: %d %s", rec.Code, rec.Body.String())
	}

	done := waitOnboardState(t, sp.feishuOnboard, feishuOnboardStateCompleted)
	if done.AppID != "cli_resilient_01" {
		t.Fatalf("unexpected app_id after retries: %+v", done)
	}

	waitsMu.Lock()
	gotWaits := append([]time.Duration(nil), waits...)
	waitsMu.Unlock()
	// Expect: retry1 (2+1=3s), retry2 (2+1=3s), slow_down1 (2+5=7s), slow_down2 (7+5=12s).
	wantWaits := []time.Duration{3 * time.Second, 3 * time.Second, 7 * time.Second, 12 * time.Second}
	if !reflect.DeepEqual(gotWaits, wantWaits) {
		t.Fatalf("wait durations=%v, want %v", gotWaits, wantWaits)
	}
}

func TestFeishuOnboardDeniedExpiredAndCancel(t *testing.T) {
	t.Parallel()
	t.Run("access_denied", func(t *testing.T) {
		s, sp, mux := setupOnboardHarness(t, func(w http.ResponseWriter, r *http.Request) {
			_ = r.ParseForm()
			w.Header().Set("Content-Type", "application/json")
			if r.Form.Get("action") == "begin" {
				_, _ = w.Write([]byte(`{"device_code":"d1","user_code":"DENY-1","verification_uri_complete":"https://open.feishu.cn/page/launcher?user_code=DENY-1","expires_in":600,"interval":1}`))
				return
			}
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"error":"access_denied","error_description":"user cancelled"}`))
		}, nil)

		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, authedReq(http.MethodPost, "/api/super/integrations/feishu/onboard", `{}`, s.token))
		if rec.Code != http.StatusOK {
			t.Fatalf("POST onboard: %d", rec.Code)
		}
		v := waitOnboardState(t, sp.feishuOnboard, feishuOnboardStateDenied)
		if v.Error == "" || v.QRDataURL != "" || v.URL != "" {
			t.Fatalf("unexpected denied view: %+v", v)
		}
	})

	t.Run("server_expired_token", func(t *testing.T) {
		s, sp, mux := setupOnboardHarness(t, func(w http.ResponseWriter, r *http.Request) {
			_ = r.ParseForm()
			w.Header().Set("Content-Type", "application/json")
			if r.Form.Get("action") == "begin" {
				_, _ = w.Write([]byte(`{"device_code":"d2","user_code":"EXP-1","verification_uri_complete":"https://open.feishu.cn/page/launcher?user_code=EXP-1","expires_in":600,"interval":1}`))
				return
			}
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"error":"expired_token"}`))
		}, nil)

		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, authedReq(http.MethodPost, "/api/super/integrations/feishu/onboard", `{}`, s.token))
		if rec.Code != http.StatusOK {
			t.Fatalf("POST onboard: %d", rec.Code)
		}
		v := waitOnboardState(t, sp.feishuOnboard, feishuOnboardStateExpired)
		if v.Error == "" || v.QRDataURL != "" {
			t.Fatalf("unexpected expired view: %+v", v)
		}
	})

	t.Run("local_deadline_expired", func(t *testing.T) {
		s, sp, mux := setupOnboardHarness(t, func(w http.ResponseWriter, r *http.Request) {
			_ = r.ParseForm()
			w.Header().Set("Content-Type", "application/json")
			if r.Form.Get("action") == "begin" {
				_, _ = w.Write([]byte(`{"device_code":"d3","user_code":"TTL-1","verification_uri_complete":"https://open.feishu.cn/page/launcher?user_code=TTL-1","expires_in":1,"interval":1}`))
				return
			}
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"error":"authorization_pending"}`))
		}, nil)
		sp.feishuOnboard.maxTTL = 40 * time.Millisecond
		sp.feishuOnboard.waitFn = func(ctx context.Context, d time.Duration) error {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(20 * time.Millisecond):
				return nil
			}
		}

		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, authedReq(http.MethodPost, "/api/super/integrations/feishu/onboard", `{}`, s.token))
		if rec.Code != http.StatusOK {
			t.Fatalf("POST onboard: %d", rec.Code)
		}
		waitOnboardState(t, sp.feishuOnboard, feishuOnboardStateExpired)
	})

	t.Run("delete_cancel_and_invalid_mode", func(t *testing.T) {
		s, sp, mux := setupOnboardHarness(t, func(w http.ResponseWriter, r *http.Request) {
			_ = r.ParseForm()
			w.Header().Set("Content-Type", "application/json")
			if r.Form.Get("action") == "begin" {
				_, _ = w.Write([]byte(`{"device_code":"d4","user_code":"CNCL-1","verification_uri_complete":"https://open.feishu.cn/page/launcher?user_code=CNCL-1","expires_in":600,"interval":5}`))
				return
			}
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"error":"authorization_pending"}`))
		}, nil)

		// Invalid mode returns 400.
		badRec := httptest.NewRecorder()
		mux.ServeHTTP(badRec, authedReq(http.MethodPost, "/api/super/integrations/feishu/onboard", `{"mode":"bogus"}`, s.token))
		if badRec.Code != http.StatusBadRequest {
			t.Fatalf("invalid mode status=%d, want 400", badRec.Code)
		}

		// Start then cancel via DELETE.
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, authedReq(http.MethodPost, "/api/super/integrations/feishu/onboard", `{}`, s.token))
		if rec.Code != http.StatusOK {
			t.Fatalf("POST onboard: %d", rec.Code)
		}
		if st := sp.feishuOnboard.Status().State; st != feishuOnboardStateWaiting {
			t.Fatalf("state=%q, want waiting", st)
		}

		delRec := httptest.NewRecorder()
		mux.ServeHTTP(delRec, authedReq(http.MethodDelete, "/api/super/integrations/feishu/onboard", "", s.token))
		if delRec.Code != http.StatusOK {
			t.Fatalf("DELETE onboard: %d", delRec.Code)
		}
		if st := sp.feishuOnboard.Status().State; st != feishuOnboardStateIdle {
			t.Fatalf("state after DELETE=%q, want idle", st)
		}
	})
}

func TestFeishuOnboardPersonaPresetsAndValidation(t *testing.T) {
	t.Parallel()
	// 1. Default desc contains homepage URL and respects Feishu CN 120-char limit cleanly.
	if !strings.Contains(feishuOnboardDefaultDesc, "https://lihuanyu0w0.github.io/collie/") {
		t.Fatalf("default desc must contain homepage URL: %q", feishuOnboardDefaultDesc)
	}
	if n := utf8.RuneCountInString(feishuOnboardDefaultDesc); n > feishuOnboardCNMaxDescRunes {
		t.Fatalf("default desc rune count=%d > %d", n, feishuOnboardCNMaxDescRunes)
	}

	// 2. Owner name resolution & fallback rules.
	if got := formatFeishuOwnerBotName("Alex", feishuOnboardCNMaxNameRunes); got != "Alex的边牧 · Collie" {
		t.Fatalf("owner name -> %q", got)
	}
	if got := formatFeishuOwnerBotName("", feishuOnboardCNMaxNameRunes); got != "我的边牧 · Collie" {
		t.Fatalf("empty owner -> %q", got)
	}
	if got := formatFeishuOwnerBotName("ou_123456", feishuOnboardCNMaxNameRunes); got != "我的边牧 · Collie" {
		t.Fatalf("open_id owner -> %q", got)
	}
	if got := formatFeishuOwnerBotName("alice@example.com", feishuOnboardCNMaxNameRunes); got != "我的边牧 · Collie" {
		t.Fatalf("email owner -> %q", got)
	}
	longOwner := strings.Repeat("很长的名字", 10)
	if got := formatFeishuOwnerBotName(longOwner, feishuOnboardCNMaxNameRunes); utf8.RuneCountInString(got) > feishuOnboardCNMaxNameRunes || !strings.HasSuffix(got, feishuOnboardNameSuffix) {
		t.Fatalf("long owner truncation failed: %q (runes=%d)", got, utf8.RuneCountInString(got))
	}

	// 3. Embedded 4 preset avatar PNG assets exist and are valid PNGs.
	for _, av := range feishuOnboardAvatarPresets {
		rel := strings.TrimPrefix(av.Preview, "/")
		b, err := collieWeb.FS.ReadFile(rel)
		if err != nil {
			t.Fatalf("embedded avatar %s missing: %v", rel, err)
		}
		if len(b) < 8 || !bytes.Equal(b[:8], []byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n'}) {
			t.Fatalf("embedded avatar %s is not a valid PNG", rel)
		}
	}

	// 4. End-to-end HTTP checks for owner default, custom persona, avatar ordering, and CN/Intl limits.
	s, sp, mux := setupOnboardHarness(t, func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		w.Header().Set("Content-Type", "application/json")
		if r.Form.Get("action") == "begin" {
			_, _ = w.Write([]byte(`{
				"device_code": "dev_persona_1",
				"user_code": "PERS-0001",
				"verification_uri_complete": "https://open.feishu.cn/page/launcher?user_code=PERS-0001",
				"expires_in": 600,
				"interval": 5
			}`))
			return
		}
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":"authorization_pending"}`))
	}, func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		w.Header().Set("Content-Type", "application/json")
		if r.Form.Get("action") == "begin" {
			_, _ = w.Write([]byte(`{
				"device_code": "dev_persona_intl",
				"user_code": "INTL-0001",
				"verification_uri_complete": "https://open.larksuite.com/page/launcher?user_code=INTL-0001",
				"expires_in": 600,
				"interval": 5
			}`))
			return
		}
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":"authorization_pending"}`))
	})

	// ExtraView exposes onboard_presets with 4 avatars and homepage URL.
	iv, ok := sp.integrations.View(feishuKind)
	if !ok {
		t.Fatalf("feishu integration missing")
	}
	presets, _ := iv.Extra["onboard_presets"].(map[string]any)
	if presets == nil || presets["fallback_name"] != "我的边牧 · Collie" || presets["default_desc"] != feishuOnboardDefaultDesc {
		t.Fatalf("unexpected onboard_presets: %+v", presets)
	}

	// Known owner name resolves automatically into launch URL when request name is omitted.
	owner := "小明"
	sp.feishuOnboard.ownerNameOverride = &owner
	recOwner := httptest.NewRecorder()
	mux.ServeHTTP(recOwner, authedReq(http.MethodPost, "/api/super/integrations/feishu/onboard", `{"region":"cn","mode":"create","avatar":"terracotta"}`, s.token))
	if recOwner.Code != http.StatusOK {
		t.Fatalf("owner onboard status=%d body=%s", recOwner.Code, recOwner.Body.String())
	}
	var vOwner FeishuOnboardView
	_ = json.Unmarshal(recOwner.Body.Bytes(), &vOwner)
	uOwner := assertMinimalCollieAddons(t, vOwner.URL)
	if uOwner.Query().Get("name") != "小明的边牧 · Collie" {
		t.Fatalf("owner resolved name=%q, want 小明的边牧 · Collie", uOwner.Query().Get("name"))
	}
	avs := uOwner.Query()["avatar"]
	if len(avs) != 4 || !strings.HasSuffix(avs[0], "/avatars/terracotta.png") {
		t.Fatalf("avatar order=%v, want terracotta first among 4 presets", avs)
	}

	// Custom name/desc/avatar overrides work cleanly.
	recCustom := httptest.NewRecorder()
	mux.ServeHTTP(recCustom, authedReq(http.MethodPost, "/api/super/integrations/feishu/onboard", `{"region":"cn","mode":"create","name":"阿白的边牧","desc":"团队协查边牧 · https://lihuanyu0w0.github.io/collie/","avatar":"pine"}`, s.token))
	if recCustom.Code != http.StatusOK {
		t.Fatalf("custom onboard status=%d body=%s", recCustom.Code, recCustom.Body.String())
	}
	var vCustom FeishuOnboardView
	_ = json.Unmarshal(recCustom.Body.Bytes(), &vCustom)
	uCustom, _ := url.Parse(vCustom.URL)
	if uCustom.Query().Get("name") != "阿白的边牧" || !strings.HasSuffix(uCustom.Query()["avatar"][0], "/avatars/pine.png") {
		t.Fatalf("custom persona url mismatch: %s", vCustom.URL)
	}

	// Validation rejections (400) for short name, >32 CN name, >120 CN desc, and invalid avatar.
	for _, tc := range []struct {
		label string
		body  string
	}{
		{"short_name", `{"region":"cn","mode":"create","name":"牧"}`},
		{"cn_name_over_32", `{"region":"cn","mode":"create","name":"` + strings.Repeat("牧", 33) + `"}`},
		{"cn_desc_over_120", `{"region":"cn","mode":"create","desc":"` + strings.Repeat("简", 121) + `"}`},
		{"bad_avatar", `{"region":"cn","mode":"create","avatar":"neon"}`},
		{"intl_desc_over_240", `{"region":"intl","mode":"create","desc":"` + strings.Repeat("a", 241) + `"}`},
	} {
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, authedReq(http.MethodPost, "/api/super/integrations/feishu/onboard", tc.body, s.token))
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("%s: status=%d, want 400 (body=%s)", tc.label, rec.Code, rec.Body.String())
		}
	}

	// Intl accepts longer name (40) & desc (150) within Lark's 64/240 bounds.
	recIntl := httptest.NewRecorder()
	mux.ServeHTTP(recIntl, authedReq(http.MethodPost, "/api/super/integrations/feishu/onboard", `{"region":"intl","mode":"create","name":"`+strings.Repeat("a", 40)+`","desc":"`+strings.Repeat("b", 150)+`","avatar":"plum"}`, s.token))
	if recIntl.Code != http.StatusOK {
		t.Fatalf("intl longer persona status=%d body=%s", recIntl.Code, recIntl.Body.String())
	}
}

func TestFeishuOnboardPersonaJSContract(t *testing.T) {
	t.Parallel()
	nodeBin, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node not installed")
	}
	root := filepath.Join("..", "..")
	jsTest := `
const fs = require("fs");
const path = require("path");
const root = process.argv[1];
const appJs = fs.readFileSync(path.join(root, "web/app.js"), "utf8");
const appCss = fs.readFileSync(path.join(root, "web/app.css"), "utf8");
const demoJs = fs.readFileSync(path.join(root, "scripts/demo/demo.js"), "utf8");

function assert(cond, msg) {
  if (!cond) throw new Error(msg);
}

// 1. Default persona fallback presets in web/app.js match Go backend & homepage requirement.
assert(appJs.includes("FEISHU_ONBOARD_FALLBACK_PRESETS"), "missing FEISHU_ONBOARD_FALLBACK_PRESETS");
assert(appJs.includes("https://lihuanyu0w0.github.io/collie/"), "missing homepage URL in web/app.js");
assert(appJs.includes("我的边牧 · Collie"), "missing fallback bot name");
assert(appJs.includes("{user}的边牧 · Collie"), "missing {user} template");
for (const id of ["cobalt", "terracotta", "pine", "plum"]) {
  assert(appJs.includes('id: "' + id + '"'), "missing avatar preset " + id + " in web/app.js");
  assert(demoJs.includes('id: "' + id + '"'), "missing avatar preset " + id + " in demo.js");
  assert(fs.existsSync(path.join(root, "web/avatars", id + ".png")), "missing web/avatars/" + id + ".png");
  assert(fs.existsSync(path.join(root, "docs/avatars", id + ".png")), "missing docs/avatars/" + id + ".png");
}

// 2. Interactive controls & accessibility contract in web/app.js + web/app.css.
for (const marker of [
  'name="fs_ob_avatar"',
  'id="fs-ob-avatar-rand"',
  'id="fs-ob-persona-toggle"',
  'id="fs-ob-persona-edit"',
  'id="fs-ob-name"',
  'id="fs-ob-desc"',
  'id="fs-ob-persona-reset"',
  'aria-controls="fs-ob-persona-edit"',
  'aria-invalid="true"',
  'if (trimmedName) payload.name = trimmedName',
  'if (trimmedDesc) payload.desc = trimmedDesc',
  'if (d.onboardAvatar) payload.avatar = d.onboardAvatar',
]) {
  assert(appJs.includes(marker), "web/app.js missing contract marker: " + marker);
}
for (const cssSel of [".int-persona-box", ".int-avatar-picker", ".int-avatar-opt.is-selected", "@media (forced-colors: active)"]) {
  assert(appCss.includes(cssSel), "web/app.css missing selector: " + cssSel);
}

// 3. Extract FEISHU_ONBOARD_FALLBACK_PRESETS and verify CN desc length <= 120 runes.
const m = appJs.match(/const FEISHU_ONBOARD_FALLBACK_PRESETS = (\{[\s\S]*?\n  \});/);
assert(m, "could not parse FEISHU_ONBOARD_FALLBACK_PRESETS");
const presets = new Function("return " + m[1])();
assert(presets.avatars.length === 4, "expected 4 avatar presets, got " + presets.avatars.length);
assert(Array.from(presets.default_desc).length <= presets.limits.cn.desc_max, "default_desc exceeds CN limit");
assert(presets.default_desc.includes("https://lihuanyu0w0.github.io/collie/"), "default_desc missing homepage link");
console.log("feishu-persona-js-contract OK (desc runes=" + Array.from(presets.default_desc).length + ")");
`
	cmd := exec.Command(nodeBin, "-e", jsTest, root)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("JS contract failed: %v\n%s", err, string(out))
	}
}
