package collie

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type testPeerHub struct {
	t      *testing.T
	dir    string
	token  string
	srv    *Server
	sup    *Super
	http   *httptest.Server
	client *http.Client
}

func newTestPeerHub(t *testing.T, name string) *testPeerHub {
	t.Helper()
	dir := t.TempDir()
	tok := "master-" + name + "-0123456789abcdef"
	srv := &Server{
		herdr: newHerdr(filepath.Join(dir, "herdr.sock"), name, dir),
		token: tok,
		home:  dir,
	}
	sup, err := newSuper(srv, dir)
	if err != nil {
		t.Fatalf("newSuper(%s): %v", name, err)
	}
	t.Cleanup(func() { _ = sup.db.Close() })
	srv.sup = sup
	srv.auths = newAuth(sup.db, tok)
	_, _, err = sup.peers.UpdateSettings(nil, &name)
	if err != nil {
		t.Fatalf("set hub name: %v", err)
	}

	ts := httptest.NewServer(srv.Routes())
	t.Cleanup(ts.Close)
	srv.publicURLs = []string{ts.URL}

	return &testPeerHub{
		t:      t,
		dir:    dir,
		token:  tok,
		srv:    srv,
		sup:    sup,
		http:   ts,
		client: ts.Client(),
	}
}

func (h *testPeerHub) doOwner(method, path string, body any) (*http.Response, []byte) {
	h.t.Helper()
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			h.t.Fatalf("marshal: %v", err)
		}
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, h.http.URL+path, rd)
	if err != nil {
		h.t.Fatalf("new request: %v", err)
	}
	req.Header.Set("Authorization", "Bearer "+h.token)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := h.client.Do(req)
	if err != nil {
		h.t.Fatalf("%s %s: %v", method, path, err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	return resp, raw
}

func disableAuthDelaysForTest(t *testing.T) {
	t.Helper()
	oldAuth := authFailDelay
	oldPair := pairFailDelay
	authFailDelay = 0
	pairFailDelay = 0
	t.Cleanup(func() {
		authFailDelay = oldAuth
		pairFailDelay = oldPair
	})
}

func TestPeerDualHubEndToEndLoopback(t *testing.T) {
	disableAuthDelaysForTest(t)

	hubA := newTestPeerHub(t, "Alice的边牧")
	hubB := newTestPeerHub(t, "Bob的边牧")

	// 1. Hub A generates a 10-minute one-time invite code & local QR PNG.
	resp, raw := hubA.doOwner("POST", "/api/super/peers/invite", map[string]any{"mode": "advise"})
	if resp.StatusCode != 200 {
		t.Fatalf("invite status=%d body=%s", resp.StatusCode, raw)
	}
	var inv PeerInviteResult
	if err := json.Unmarshal(raw, &inv); err != nil {
		t.Fatalf("unmarshal invite: %v", err)
	}
	if !strings.HasPrefix(inv.InviteCode, peerInvitePrefix) || inv.Code == "" || len(inv.Addrs) == 0 {
		t.Fatalf("bad invite result: %+v", inv)
	}

	// Verify built-in local QR code renders valid PNG for the full invite string.
	qrReq, _ := http.NewRequest("GET", hubA.http.URL+"/api/qr?text="+strings.ReplaceAll(inv.InviteCode, "+", "%2B"), nil)
	qrReq.Header.Set("Authorization", "Bearer "+hubA.token)
	qrResp, err := hubA.client.Do(qrReq)
	if err != nil {
		t.Fatalf("GET /api/qr: %v", err)
	}
	qrBytes, _ := io.ReadAll(qrResp.Body)
	qrResp.Body.Close()
	if qrResp.StatusCode != 200 || !bytes.HasPrefix(qrBytes, []byte{0x89, 'P', 'N', 'G'}) {
		t.Fatalf("expected valid PNG QR, got status=%d len=%d", qrResp.StatusCode, len(qrBytes))
	}

	// Self-pairing must be rejected.
	resp, raw = hubA.doOwner("POST", "/api/super/peers/pair", map[string]any{"invite_code": inv.InviteCode})
	if resp.StatusCode != 400 {
		t.Fatalf("self-pair expected 400, got %d: %s", resp.StatusCode, raw)
	}

	// 2. Hub B pairs with Hub A's invite (atomic bidirectional handshake + reverse ping).
	resp, raw = hubB.doOwner("POST", "/api/super/peers/pair", map[string]any{
		"invite_code": inv.InviteCode,
		"name":        "Alice的边牧",
		"mode":        "advise",
	})
	if resp.StatusCode != 200 {
		t.Fatalf("pair B->A failed: status=%d body=%s", resp.StatusCode, raw)
	}
	var pairRes struct {
		OK   bool     `json:"ok"`
		Peer PeerView `json:"peer"`
	}
	if err := json.Unmarshal(raw, &pairRes); err != nil || !pairRes.OK || pairRes.Peer.ID == "" {
		t.Fatalf("unexpected pair response: %s", raw)
	}
	peerAOnB := pairRes.Peer.ID

	// Replaying the same one-time invite code must fail with 401.
	hubC := newTestPeerHub(t, "Charlie的边牧")
	resp, raw = hubC.doOwner("POST", "/api/super/peers/pair", map[string]any{"invite_code": inv.InviteCode})
	if resp.StatusCode != 401 {
		t.Fatalf("replayed invite expected 401, got %d: %s", resp.StatusCode, raw)
	}

	// Verify Hub A now has Hub B in its peers list (and no secret token leaks in JSON).
	resp, raw = hubA.doOwner("GET", "/api/super/peers", nil)
	if resp.StatusCode != 200 {
		t.Fatalf("GET /api/super/peers A: %d %s", resp.StatusCode, raw)
	}
	if strings.Contains(string(raw), peerTokenPrefix) {
		t.Fatalf("GET /api/super/peers leaked plaintext clp_ token: %s", raw)
	}
	var listA struct {
		HubID string     `json:"hub_id"`
		Peers []PeerView `json:"peers"`
	}
	if err := json.Unmarshal(raw, &listA); err != nil || len(listA.Peers) != 1 {
		t.Fatalf("expected 1 peer on A, got %s", raw)
	}
	peerBOnA := listA.Peers[0].ID

	// Verify SQLite isolation: inbound_hash is SHA-256 (64 hex), no plaintext clp_, Auth.devices empty, secrets.json is 0600.
	var storedHash string
	if err := hubB.sup.db.QueryRow(`SELECT inbound_hash FROM peers WHERE id=?`, peerAOnB).Scan(&storedHash); err != nil {
		t.Fatalf("query peers.inbound_hash: %v", err)
	}
	if len(storedHash) != 64 || strings.HasPrefix(storedHash, peerTokenPrefix) {
		t.Fatalf("inbound_hash must be 64-char sha256 hex, got %q", storedHash)
	}
	tokAtoB := hubA.sup.secrets.GetKey("peer:"+peerBOnA, "outbound_token")
	if !strings.HasPrefix(tokAtoB, peerTokenPrefix) || len(tokAtoB) != 4+64 {
		t.Fatalf("outbound_token must be 256-bit clp_ token, got len=%d", len(tokAtoB))
	}
	if hashToken(tokAtoB) != storedHash {
		t.Fatalf("A's outbound token hash does not match B's stored inbound_hash")
	}
	fi, err := os.Stat(hubA.sup.secrets.Path())
	if err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("secrets.json must be 0600, err=%v mode=%v", err, fi.Mode().Perm())
	}
	if devs := hubB.srv.auths.list(); len(devs) != 0 {
		t.Fatalf("peer token must NEVER enter Auth.devices, got %+v", devs)
	}

	// Verify superView exposes peers_summary cleanly.
	resp, raw = hubA.doOwner("GET", "/api/super", nil)
	if resp.StatusCode != 200 {
		t.Fatalf("GET /api/super: %d", resp.StatusCode)
	}
	var sv superView
	_ = json.Unmarshal(raw, &sv)
	if sv.PeersSummary == nil || sv.PeersSummary.Total != 1 || sv.PeersSummary.Active != 1 || !sv.PeersSummary.Enabled {
		t.Fatalf("unexpected peers_summary: %+v", sv.PeersSummary)
	}

	// 3. Ping A -> B.
	resp, raw = hubA.doOwner("POST", "/api/super/peers/"+peerBOnA+"/ping", nil)
	if resp.StatusCode != 200 {
		t.Fatalf("ping status=%d body=%s", resp.StatusCode, raw)
	}
	var pingRes PeerPingResult
	_ = json.Unmarshal(raw, &pingRes)
	if !pingRes.OK || pingRes.ProtocolVer != 1 {
		t.Fatalf("ping failed: %+v", pingRes)
	}

	// 4. Send ask A -> B: double-latch confirm guard + L1 advise mode + anti-injection + priority.
	resp, raw = hubA.doOwner("POST", "/api/super/peers/"+peerBOnA+"/send", map[string]any{
		"topic_id": "checkout-timeout",
		"kind":     "ask",
		"subject":  "确认超时阈值调整原因",
		"body":     "请帮忙只读核对 checkout 超时配置是在哪次提交改的。",
		"confirm":  false,
	})
	if resp.StatusCode != 400 {
		t.Fatalf("send without confirm expected 400, got %d: %s", resp.StatusCode, raw)
	}

	resp, raw = hubA.doOwner("POST", "/api/super/peers/"+peerBOnA+"/send", map[string]any{
		"topic_id": "checkout-timeout",
		"kind":     "ask",
		"subject":  "确认超时阈值调整原因",
		"body":     "请帮忙只读核对 checkout 超时配置是在哪次提交改的。",
		"confirm":  true,
	})
	if resp.StatusCode != 200 {
		t.Fatalf("send A->B failed: %d %s", resp.StatusCode, raw)
	}
	var sendRes PeerSendResult
	_ = json.Unmarshal(raw, &sendRes)
	if !sendRes.OK || sendRes.Thread != "peer:"+peerBOnA+":checkout-timeout" || sendRes.Delivery != "advise" {
		t.Fatalf("unexpected send result: %+v", sendRes)
	}

	// Verify Hub B queued a guarded trigger with non-chat source (never interrupts human chat).
	hubB.sup.mu.Lock()
	var peerTrig *Trigger
	for _, tr := range hubB.sup.st.Triggers {
		if tr.Source == "peer:"+peerAOnB && tr.Thread == "peer:"+peerAOnB+":checkout-timeout" {
			peerTrig = tr
		}
	}
	quote := (*Quote)(nil)
	if peerTrig != nil {
		quote = hubB.sup.quoteLocked(peerTrig.ID)
	}
	hubB.sup.mu.Unlock()
	if peerTrig == nil {
		t.Fatalf("expected queued peer trigger on Hub B")
	}
	if peerTrig.Status != "queued" || peerTrig.Action != "respond" {
		t.Fatalf("unexpected trigger state: status=%s action=%s", peerTrig.Status, peerTrig.Action)
	}
	if !strings.Contains(peerTrig.Text, peerGuardTagPrefix) || !strings.Contains(peerTrig.Text, "不可信输入") {
		t.Fatalf("trigger missing anti-prompt-injection guard: %s", peerTrig.Text)
	}
	if len(fromHuman([]*Trigger{peerTrig})) != 0 {
		t.Fatalf("peer trigger MUST NOT match fromHuman() or interrupt human chat")
	}
	if quote == nil || quote.Who != "Alice的边牧" || !strings.Contains(quote.Text, "确认超时阈值调整原因") {
		t.Fatalf("unexpected formatted peer quote: %+v", quote)
	}

	// 5. Idempotent dedup: replaying the exact same msg_id to Hub B returns 200 duplicate=true with no extra trigger.
	dupEnv, _ := json.Marshal(PeerInboxPayload{
		MsgID:   sendRes.MsgID,
		TopicID: "checkout-timeout",
		Kind:    "ask",
		Subject: "确认超时阈值调整原因",
		Body:    "重复投递",
		SentAt:  time.Now().Format(time.RFC3339),
	})
	dupReq, _ := http.NewRequest("POST", hubB.http.URL+"/api/peer/inbox", bytes.NewReader(dupEnv))
	dupReq.Header.Set("Authorization", "Bearer "+tokAtoB)
	dupReq.Header.Set("Content-Type", "application/json")
	dupResp, err := hubB.client.Do(dupReq)
	if err != nil {
		t.Fatalf("duplicate inbox req: %v", err)
	}
	dupRaw, _ := io.ReadAll(dupResp.Body)
	dupResp.Body.Close()
	if dupResp.StatusCode != 200 || !strings.Contains(string(dupRaw), `"duplicate":true`) {
		t.Fatalf("expected 200 duplicate=true, got %d: %s", dupResp.StatusCode, dupRaw)
	}
	hubB.sup.mu.Lock()
	trigCount := len(hubB.sup.st.Triggers)
	hubB.sup.mu.Unlock()
	if trigCount != 1 {
		t.Fatalf("duplicate delivery must not create extra trigger, count=%d", trigCount)
	}

	// 6. Reply back B -> A on same topic via in_reply_to (inherits topic_id).
	resp, raw = hubB.doOwner("POST", "/api/super/peers/"+peerAOnB+"/send", map[string]any{
		"kind":        "reply",
		"in_reply_to": sendRes.MsgID,
		"body":        "查到了，是提交 a4c91f2 仅调了异步重试超时，同步下单接口未变。",
		"confirm":     true,
	})
	if resp.StatusCode != 200 {
		t.Fatalf("reply B->A failed: %d %s", resp.StatusCode, raw)
	}
	var replyRes PeerSendResult
	_ = json.Unmarshal(raw, &replyRes)
	if replyRes.TopicID != "checkout-timeout" {
		t.Fatalf("expected inherited topic checkout-timeout, got %q", replyRes.TopicID)
	}

	// 7. L0 notify mode on Hub B: records event summary without waking Router (status=done).
	resp, raw = hubB.doOwner("PATCH", "/api/super/peers/"+peerAOnB, map[string]any{"mode": "notify"})
	if resp.StatusCode != 200 {
		t.Fatalf("PATCH mode notify: %d %s", resp.StatusCode, raw)
	}
	resp, raw = hubA.doOwner("POST", "/api/super/peers/"+peerBOnA+"/send", map[string]any{
		"topic_id": "release-fyi",
		"kind":     "handoff",
		"subject":  "今晚 22:00 窗口已排期",
		"body":     "仅知会：窗口排到今晚 22:00。",
		"confirm":  true,
	})
	if resp.StatusCode != 200 {
		t.Fatalf("send L0 notify failed: %d %s", resp.StatusCode, raw)
	}
	var notifyRes PeerSendResult
	_ = json.Unmarshal(raw, &notifyRes)
	if notifyRes.Delivery != "notify" {
		t.Fatalf("expected delivery=notify, got %q", notifyRes.Delivery)
	}
	hubB.sup.mu.Lock()
	var notifyTrig *Trigger
	for _, tr := range hubB.sup.st.Triggers {
		if tr.Thread == "peer:"+peerAOnB+":release-fyi" {
			notifyTrig = tr
		}
	}
	hubB.sup.mu.Unlock()
	if notifyTrig == nil || notifyTrig.Status != "done" || notifyTrig.Action != "notify" {
		t.Fatalf("L0 notify must not wake Router (want status=done action=notify), got %+v", notifyTrig)
	}

	// 8. Narrow-token authorization hard-isolation matrix:
	//    peer clp_ token MUST be rejected with 401/403 across all privileged & non-peer routes.
	privileged := [][2]string{
		{"GET", "/api/super"},
		{"POST", "/api/super/spawn"},
		{"GET", "/api/super/peers"},
		{"GET", "/api/super/memory"},
		{"GET", "/api/memory/export"},
		{"GET", "/api/state"},
		{"GET", "/api/file?path=/etc/hosts"},
		{"GET", "/api/files"},
		{"GET", "/api/term"},
		{"GET", "/api/panes/w1:p1/stream"},
		{"GET", "/api/devices"},
		{"GET", "/api/node/connect"},
	}
	for _, ep := range privileged {
		req, _ := http.NewRequest(ep[0], hubB.http.URL+ep[1], nil)
		req.Header.Set("Authorization", "Bearer "+tokAtoB)
		r, err := hubB.client.Do(req)
		if err != nil {
			t.Fatalf("%s %s: %v", ep[0], ep[1], err)
		}
		r.Body.Close()
		if r.StatusCode != http.StatusUnauthorized && r.StatusCode != http.StatusForbidden {
			t.Fatalf("expected 401/403 for peer token on %s %s, got %d", ep[0], ep[1], r.StatusCode)
		}
	}
	// Cookie or query parameter MUST NOT authenticate /api/peer/ping.
	cookieReq, _ := http.NewRequest("GET", hubB.http.URL+"/api/peer/ping?access_token="+tokAtoB, nil)
	cookieReq.AddCookie(&http.Cookie{Name: cookieName, Value: tokAtoB})
	cookieResp, _ := hubB.client.Do(cookieReq)
	cookieResp.Body.Close()
	if cookieResp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("peerAuth must ignore cookies/query params, got %d", cookieResp.StatusCode)
	}
	// CORS must NOT be granted on /api/peer/* routes.
	corsReq, _ := http.NewRequest("OPTIONS", hubB.http.URL+"/api/peer/inbox", nil)
	corsReq.Header.Set("Origin", "https://evil.example")
	corsResp, _ := hubB.client.Do(corsReq)
	corsResp.Body.Close()
	if acao := corsResp.Header.Get("Access-Control-Allow-Origin"); acao != "" {
		t.Fatalf("/api/peer/* must not emit CORS headers, got %q", acao)
	}

	// 9. Pause -> immediate 403 block -> resume -> succeeds again.
	resp, raw = hubB.doOwner("PATCH", "/api/super/peers/"+peerAOnB, map[string]any{"status": "paused"})
	if resp.StatusCode != 200 {
		t.Fatalf("pause peer failed: %d %s", resp.StatusCode, raw)
	}
	resp, raw = hubA.doOwner("POST", "/api/super/peers/"+peerBOnA+"/send", map[string]any{
		"topic_id": "paused-check",
		"kind":     "ask",
		"body":     "暂停期间不应送达",
		"confirm":  true,
	})
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("paused peer must return 403 immediately, got %d: %s", resp.StatusCode, raw)
	}
	resp, raw = hubB.doOwner("PATCH", "/api/super/peers/"+peerAOnB, map[string]any{"status": "active"})
	if resp.StatusCode != 200 {
		t.Fatalf("resume peer failed: %d %s", resp.StatusCode, raw)
	}
	resp, raw = hubA.doOwner("POST", "/api/super/peers/"+peerBOnA+"/send", map[string]any{
		"topic_id": "resumed-check",
		"kind":     "ack",
		"subject":  "恢复连通确认",
		"confirm":  true,
	})
	if resp.StatusCode != 200 {
		t.Fatalf("resumed peer send failed: %d %s", resp.StatusCode, raw)
	}

	// 10. Block -> immediate 403 on old token, secrets wiped, re-pair rejected, then unblock clears blocklist.
	resp, raw = hubB.doOwner("DELETE", "/api/super/peers/"+peerAOnB+"?block=1", nil)
	if resp.StatusCode != 200 || !strings.Contains(string(raw), `"blocked":true`) {
		t.Fatalf("block peer failed: %d %s", resp.StatusCode, raw)
	}
	if hubB.sup.secrets.HasKey("peer:"+peerAOnB, "outbound_token") {
		t.Fatalf("outbound_token must be erased from secrets.json immediately on block")
	}

	// Old clp_ token hitting Hub B's /api/peer/inbox must immediately get 403.
	blockedEnv, _ := json.Marshal(PeerInboxPayload{
		MsgID:   newPeerMsgID(),
		TopicID: "after-block",
		Kind:    "ask",
		Body:    "被拉黑后应立即 403",
		SentAt:  time.Now().Format(time.RFC3339),
	})
	bReq, _ := http.NewRequest("POST", hubB.http.URL+"/api/peer/inbox", bytes.NewReader(blockedEnv))
	bReq.Header.Set("Authorization", "Bearer "+tokAtoB)
	bReq.Header.Set("Content-Type", "application/json")
	bResp, err := hubB.client.Do(bReq)
	if err != nil {
		t.Fatalf("blocked inbox req: %v", err)
	}
	bRaw, _ := io.ReadAll(bResp.Body)
	bResp.Body.Close()
	if bResp.StatusCode != http.StatusForbidden {
		t.Fatalf("blocked peer token expected 403, got %d: %s", bResp.StatusCode, bRaw)
	}

	// Hub A trying to re-pair with a brand-new invite from Hub B must be blocked (403).
	resp, raw = hubB.doOwner("POST", "/api/super/peers/invite", map[string]any{"mode": "advise"})
	var invB PeerInviteResult
	_ = json.Unmarshal(raw, &invB)
	resp, raw = hubA.doOwner("POST", "/api/super/peers/pair", map[string]any{"invite_code": invB.InviteCode})
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("blocked hub re-pair expected 403, got %d: %s", resp.StatusCode, raw)
	}

	// Unblock Hub A on Hub B: old token must STILL be dead (401), while fresh pairing succeeds again.
	resp, raw = hubB.doOwner("DELETE", "/api/super/peers/blocklist/"+listA.HubID, nil)
	if resp.StatusCode != 200 {
		t.Fatalf("unblock failed: %d %s", resp.StatusCode, raw)
	}
	deadReq, _ := http.NewRequest("GET", hubB.http.URL+"/api/peer/ping", nil)
	deadReq.Header.Set("Authorization", "Bearer "+tokAtoB)
	deadResp, _ := hubB.client.Do(deadReq)
	deadResp.Body.Close()
	if deadResp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("revoked token after unblock must remain dead (401), got %d", deadResp.StatusCode)
	}
}

func TestPeerOutboundSecretLeakBlocked(t *testing.T) {
	disableAuthDelaysForTest(t)
	hubA := newTestPeerHub(t, "Alice")
	hubB := newTestPeerHub(t, "Bob")

	// Register a sensitive secret in Hub A's SecretStore.
	secretVal := "feishu_super_secret_value_987654321"
	if err := hubA.sup.secrets.Update("feishu", map[string]string{"app_secret": secretVal}); err != nil {
		t.Fatalf("update secret: %v", err)
	}

	// Pair A <-> B.
	_, raw := hubA.doOwner("POST", "/api/super/peers/invite", map[string]any{"mode": "advise"})
	var inv PeerInviteResult
	_ = json.Unmarshal(raw, &inv)
	resp, raw := hubB.doOwner("POST", "/api/super/peers/pair", map[string]any{"invite_code": inv.InviteCode})
	if resp.StatusCode != 200 {
		t.Fatalf("pair: %d %s", resp.StatusCode, raw)
	}
	_, raw = hubA.doOwner("GET", "/api/super/peers", nil)
	var listA struct{ Peers []PeerView }
	_ = json.Unmarshal(raw, &listA)
	peerB := listA.Peers[0].ID

	leaks := []struct {
		name string
		body string
	}{
		{"registered SecretStore value", "配置里的密钥是 " + secretVal + " 帮我看看"},
		{"master hub token", "我的 token 是 " + hubA.token},
		{"PEM private key", "-----BEGIN RSA PRIVATE KEY-----\nMIIEowIBAAKCAQEA"},
		{"peer clp token", "令牌 clp_0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"},
		{"GitHub PAT", "token ghp_ABCDEFGHIJKLMNOPQRSTUVWXYZ1234567890"},
	}
	for _, tc := range leaks {
		resp, raw := hubA.doOwner("POST", "/api/super/peers/"+peerB+"/send", map[string]any{
			"topic_id": "leak-test",
			"kind":     "ask",
			"body":     tc.body,
			"confirm":  true,
		})
		if resp.StatusCode != http.StatusBadRequest {
			t.Fatalf("%s: expected 400 leak block, got %d: %s", tc.name, resp.StatusCode, raw)
		}
	}

	// Verify nothing ever reached Hub B.
	hubB.sup.mu.Lock()
	got := len(hubB.sup.st.Triggers)
	hubB.sup.mu.Unlock()
	if got != 0 {
		t.Fatalf("blocked leaks must never reach remote Hub, got %d triggers", got)
	}
}

func TestPeerInboundValidationRateLimitAndRedirectBlock(t *testing.T) {
	disableAuthDelaysForTest(t)
	hubA := newTestPeerHub(t, "Alice")
	hubB := newTestPeerHub(t, "Bob")

	// Pair A <-> B.
	_, raw := hubA.doOwner("POST", "/api/super/peers/invite", map[string]any{"mode": "notify"})
	var inv PeerInviteResult
	_ = json.Unmarshal(raw, &inv)
	resp, raw := hubB.doOwner("POST", "/api/super/peers/pair", map[string]any{"invite_code": inv.InviteCode})
	if resp.StatusCode != 200 {
		t.Fatalf("pair: %d %s", resp.StatusCode, raw)
	}
	_, raw = hubA.doOwner("GET", "/api/super/peers", nil)
	var listA struct{ Peers []PeerView }
	_ = json.Unmarshal(raw, &listA)
	peerBOnA := listA.Peers[0].ID
	tokAtoB := hubA.sup.secrets.GetKey("peer:"+peerBOnA, "outbound_token")

	postInbox := func(env PeerInboxPayload) (*http.Response, []byte) {
		b, _ := json.Marshal(env)
		req, _ := http.NewRequest("POST", hubB.http.URL+"/api/peer/inbox", bytes.NewReader(b))
		req.Header.Set("Authorization", "Bearer "+tokAtoB)
		req.Header.Set("Content-Type", "application/json")
		r, err := hubB.client.Do(req)
		if err != nil {
			t.Fatalf("post inbox: %v", err)
		}
		out, _ := io.ReadAll(r.Body)
		r.Body.Close()
		return r, out
	}

	// Clock skew > 10m rejected.
	r, _ := postInbox(PeerInboxPayload{
		MsgID: newPeerMsgID(), TopicID: "skew", Kind: "ask", Body: "old",
		SentAt: time.Now().Add(-15 * time.Minute).Format(time.RFC3339),
	})
	if r.StatusCode != http.StatusBadRequest {
		t.Fatalf("clock skew expected 400, got %d", r.StatusCode)
	}

	// Hop > 2 rejected.
	r, _ = postInbox(PeerInboxPayload{
		MsgID: newPeerMsgID(), TopicID: "loop", Kind: "ask", Hop: 3, Body: "loop",
		SentAt: time.Now().Format(time.RFC3339),
	})
	if r.StatusCode != http.StatusBadRequest {
		t.Fatalf("hop>2 expected 400, got %d", r.StatusCode)
	}

	// Body > 16 KB rejected.
	r, _ = postInbox(PeerInboxPayload{
		MsgID: newPeerMsgID(), TopicID: "big", Kind: "ask", Body: strings.Repeat("x", peerMaxBodyBytes+10),
		SentAt: time.Now().Format(time.RFC3339),
	})
	if r.StatusCode != http.StatusRequestEntityTooLarge {
		t.Fatalf("oversized body expected 413, got %d", r.StatusCode)
	}

	// Token bucket burst=3: first 3 succeed, 4th returns 429 with Retry-After.
	for i := 0; i < 3; i++ {
		r, b := postInbox(PeerInboxPayload{
			MsgID: newPeerMsgID(), TopicID: "burst", Kind: "ask", Body: "msg",
			SentAt: time.Now().Format(time.RFC3339),
		})
		if r.StatusCode != http.StatusAccepted {
			t.Fatalf("burst #%d expected 202, got %d: %s", i+1, r.StatusCode, b)
		}
	}
	r, b := postInbox(PeerInboxPayload{
		MsgID: newPeerMsgID(), TopicID: "burst", Kind: "ask", Body: "msg4",
		SentAt: time.Now().Format(time.RFC3339),
	})
	if r.StatusCode != http.StatusTooManyRequests || r.Header.Get("Retry-After") == "" {
		t.Fatalf("4th burst msg expected 429 + Retry-After, got %d header=%q body=%s", r.StatusCode, r.Header.Get("Retry-After"), b)
	}

	// HTTP redirect must be blocked during pairing (SSRF / port-probe guard).
	redirectHits := 0
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		redirectHits++
		w.WriteHeader(200)
	}))
	defer target.Close()
	redirSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL+"/secret", http.StatusFound)
	}))
	defer redirSrv.Close()

	malInvite, _ := encodePeerInvite(PeerInvitePayload{
		V: 1, Code: "REDIRTEST123", HubID: "hub_evilredir", HubName: "Evil",
		Addrs: []string{redirSrv.URL}, Exp: time.Now().Add(5 * time.Minute).Unix(),
	})
	resp, raw = hubA.doOwner("POST", "/api/super/peers/pair", map[string]any{"invite_code": malInvite})
	if resp.StatusCode == 200 || redirectHits != 0 {
		t.Fatalf("redirects must be blocked: status=%d redirectHits=%d body=%s", resp.StatusCode, redirectHits, raw)
	}
}

func TestPeerCLIAndRouterDoc(t *testing.T) {
	// parsePeerSend must reject without --confirm.
	if _, _, err := parsePeerSend([]string{"--to", "sam", "hello"}, strings.NewReader(""), true); err == nil {
		t.Fatalf("parsePeerSend must fail without --confirm")
	}

	// parsePeerSend accepts flags in any order and stdin "-".
	pid, body, err := parsePeerSend(
		[]string{"--to", "sam", "--topic", "checkout", "--kind", "reply", "--in-reply-to", "pm_123", "--subject", "回执", "--confirm", "-"},
		strings.NewReader("确认内容"),
		false,
	)
	if err != nil {
		t.Fatalf("parsePeerSend valid: %v", err)
	}
	if pid != "sam" || body["confirm"] != true || body["topic_id"] != "checkout" || body["kind"] != "reply" || body["body"] != "确认内容" {
		t.Fatalf("unexpected parsePeerSend payload: pid=%q body=%+v", pid, body)
	}

	// parsePeerPair & parsePeerRevoke support flags after positional argument.
	inv, name, mode, err := parsePeerPair([]string{"collie-peer://v1/abc", "--name", "Sam", "--mode", "notify"})
	if err != nil || inv != "collie-peer://v1/abc" || name != "Sam" || mode != "notify" {
		t.Fatalf("parsePeerPair mismatch: %q %q %q err=%v", inv, name, mode, err)
	}
	rpid, block, err := parsePeerRevoke([]string{"sam", "--block"})
	if err != nil || rpid != "sam" || !block {
		t.Fatalf("parsePeerRevoke mismatch: %q block=%v err=%v", rpid, block, err)
	}

	// Verify routerDoc covers Peer Hub CLI & safety rules.
	for _, want := range []string{"peer ls", "peer send", "--confirm", "[好友边牧来信"} {
		if !strings.Contains(routerDoc, want) {
			t.Fatalf("routerDoc missing %q", want)
		}
	}
}
