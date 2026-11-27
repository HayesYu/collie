package collie

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
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
	if authFailDelay == 0 && pairFailDelay == 0 {
		return
	}
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
	t.Parallel()
	disableAuthDelaysForTest(t)

	hubA := newTestPeerHub(t, "Alice的边牧")
	hubB := newTestPeerHub(t, "Bob的边牧")

	// 1. Hub A generates a permanent business card & local QR PNG encoding the /join/# URL.
	resp, raw := hubA.doOwner("POST", "/api/super/peers/invite", map[string]any{"mode": "advise"})
	if resp.StatusCode != 200 {
		t.Fatalf("invite status=%d body=%s", resp.StatusCode, raw)
	}
	var inv PeerInviteResult
	if err := json.Unmarshal(raw, &inv); err != nil {
		t.Fatalf("unmarshal invite: %v", err)
	}
	if !strings.HasPrefix(inv.InviteCode, peerInvitePrefixV2) || !strings.HasPrefix(inv.JoinURL, peerJoinBaseURL+"2.") || !inv.Permanent || inv.ExpiresAt != nil || len(inv.Code) != 22 || len(inv.Addrs) == 0 {
		t.Fatalf("bad permanent card result: %+v", inv)
	}

	// Verify built-in local QR code renders valid PNG for the full /join/# URL.
	qrReq, _ := http.NewRequest("GET", hubA.http.URL+"/api/qr?text="+strings.ReplaceAll(strings.ReplaceAll(inv.JoinURL, "#", "%23"), "+", "%2B"), nil)
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
	resp, raw = hubA.doOwner("POST", "/api/super/peers/pair", map[string]any{"invite_code": inv.JoinURL})
	if resp.StatusCode != 400 {
		t.Fatalf("self-pair expected 400, got %d: %s", resp.StatusCode, raw)
	}

	// 2. Hub B submits a friend request using multi-line share text wrapping the /join/# URL.
	shareCopy := "来加我的 Collie 好友边牧「Alice的边牧」，点击链接即可发起申请：\n" + inv.JoinURL
	resp, raw = hubB.doOwner("POST", "/api/super/peers/pair", map[string]any{
		"invite_code": shareCopy,
		"name":        "Alice的边牧",
		"mode":        "advise",
	})
	if resp.StatusCode != 200 {
		t.Fatalf("pair B->A failed: status=%d body=%s", resp.StatusCode, raw)
	}
	var pairRes struct {
		OK      bool     `json:"ok"`
		Pending bool     `json:"pending"`
		Peer    PeerView `json:"peer"`
	}
	if err := json.Unmarshal(raw, &pairRes); err != nil || !pairRes.OK || !pairRes.Pending || pairRes.Peer.ID == "" || pairRes.Peer.Status != "pending_outgoing" {
		t.Fatalf("unexpected pending pair response: %s", raw)
	}
	peerAOnB := pairRes.Peer.ID

	// Before approval: Hub B has no outbound_token and cannot send to Hub A (403).
	if hubB.sup.secrets.HasKey("peer:"+peerAOnB, "outbound_token") {
		t.Fatalf("pending_outgoing peer must NOT hold an outbound_token before approval")
	}
	resp, raw = hubB.doOwner("POST", "/api/super/peers/"+peerAOnB+"/send", map[string]any{
		"topic_id": "before-approve",
		"kind":     "ask",
		"body":     "未通过前不允许发信",
		"confirm":  true,
	})
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("send before approval expected 403, got %d: %s", resp.StatusCode, raw)
	}

	// Verify Hub A now has Hub B as pending_incoming with ZERO inbound_hash, and exposes pending_incoming=1 in superView.
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
	if err := json.Unmarshal(raw, &listA); err != nil || len(listA.Peers) != 1 || listA.Peers[0].Status != "pending_incoming" {
		t.Fatalf("expected 1 pending_incoming peer on A, got %s", raw)
	}
	peerBOnA := listA.Peers[0].ID

	var preApproveHash string
	if err := hubA.sup.db.QueryRow(`SELECT inbound_hash FROM peers WHERE id=?`, peerBOnA).Scan(&preApproveHash); err != nil {
		t.Fatalf("query A inbound_hash: %v", err)
	}
	if preApproveHash != "" {
		t.Fatalf("pending_incoming peer on A must have empty inbound_hash before approval, got %q", preApproveHash)
	}
	// Hub A's handshake token for B MUST NOT be allowed to post to B's /api/peer/inbox before activation.
	preTokAtoB := hubA.sup.secrets.GetKey("peer:"+peerBOnA, "outbound_token")
	preInboxEnv, _ := json.Marshal(PeerInboxPayload{
		MsgID: newPeerMsgID(), TopicID: "pre-act", Kind: "ask", Body: "提前发信应被拒", SentAt: time.Now().Format(time.RFC3339),
	})
	preReq, _ := http.NewRequest("POST", hubB.http.URL+"/api/peer/inbox", bytes.NewReader(preInboxEnv))
	preReq.Header.Set("Authorization", "Bearer "+preTokAtoB)
	preReq.Header.Set("Content-Type", "application/json")
	preResp, _ := hubB.client.Do(preReq)
	preResp.Body.Close()
	if preResp.StatusCode != http.StatusForbidden {
		t.Fatalf("inbox call to pending_outgoing Hub B must return 403, got %d", preResp.StatusCode)
	}
	// Cannot bypass approval via PATCH status=active.
	resp, raw = hubA.doOwner("PATCH", "/api/super/peers/"+peerBOnA, map[string]any{"status": "active"})
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("PATCH pending_incoming to active expected 409, got %d: %s", resp.StatusCode, raw)
	}

	// Permanent card is multi-scan: Hub C can also submit a request with the EXACT SAME card!
	hubC := newTestPeerHub(t, "Charlie的边牧")
	resp, raw = hubC.doOwner("POST", "/api/super/peers/pair", map[string]any{"invite_code": inv.InviteCode})
	if resp.StatusCode != 200 {
		t.Fatalf("second requester C using permanent card expected 200, got %d: %s", resp.StatusCode, raw)
	}
	// Hub A rejects Hub C cleanly (never granting credentials).
	_, raw = hubA.doOwner("GET", "/api/super/peers", nil)
	var listAC struct{ Peers []PeerView }
	_ = json.Unmarshal(raw, &listAC)
	var peerCOnA string
	for _, x := range listAC.Peers {
		if x.ID != peerBOnA {
			peerCOnA = x.ID
		}
	}
	if peerCOnA == "" {
		t.Fatalf("expected Hub C pending_incoming on Hub A")
	}
	resp, raw = hubA.doOwner("POST", "/api/super/peers/"+peerCOnA+"/reject", map[string]any{"block": false})
	if resp.StatusCode != 200 {
		t.Fatalf("reject C failed: %d %s", resp.StatusCode, raw)
	}

	// 2b. Hub A approves Hub B -> activates both sides atomically!
	resp, raw = hubA.doOwner("POST", "/api/super/peers/"+peerBOnA+"/approve", map[string]any{"mode": "advise"})
	if resp.StatusCode != 200 {
		t.Fatalf("approve B failed: %d %s", resp.StatusCode, raw)
	}
	var approveRes struct {
		OK   bool     `json:"ok"`
		Peer PeerView `json:"peer"`
	}
	if err := json.Unmarshal(raw, &approveRes); err != nil || !approveRes.OK || approveRes.Peer.Status != "active" {
		t.Fatalf("unexpected approve response: %s", raw)
	}
	// Hub B must now be active automatically too!
	_, raw = hubB.doOwner("GET", "/api/super/peers", nil)
	var listB struct{ Peers []PeerView }
	_ = json.Unmarshal(raw, &listB)
	if len(listB.Peers) != 1 || listB.Peers[0].Status != "active" {
		t.Fatalf("expected Hub B to transition to active after A's approval, got %s", raw)
	}

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
	t.Parallel()
	disableAuthDelaysForTest(t)
	hubA := newTestPeerHub(t, "Alice")
	hubB := newTestPeerHub(t, "Bob")

	// Register a sensitive secret in Hub A's SecretStore.
	secretVal := "feishu_super_secret_value_987654321"
	if err := hubA.sup.secrets.Update("feishu", map[string]string{"app_secret": secretVal}); err != nil {
		t.Fatalf("update secret: %v", err)
	}

	// Pair A <-> B (request + approve).
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
	resp, raw = hubA.doOwner("POST", "/api/super/peers/"+peerB+"/approve", nil)
	if resp.StatusCode != 200 {
		t.Fatalf("approve: %d %s", resp.StatusCode, raw)
	}

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
	t.Parallel()
	disableAuthDelaysForTest(t)
	hubA := newTestPeerHub(t, "Alice")
	hubB := newTestPeerHub(t, "Bob")

	// Pair A <-> B (request + approve).
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
	resp, raw = hubA.doOwner("POST", "/api/super/peers/"+peerBOnA+"/approve", nil)
	if resp.StatusCode != 200 {
		t.Fatalf("approve: %d %s", resp.StatusCode, raw)
	}
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
	t.Parallel()
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

func TestPeerPermanentCardResetAndLegacyCompat(t *testing.T) {
	t.Parallel()
	disableAuthDelaysForTest(t)

	hubA := newTestPeerHub(t, "AliceCard")
	hubB := newTestPeerHub(t, "BobCard")
	hubC := newTestPeerHub(t, "CharlieCard")

	// 1. Permanent card persists same code across repeated fetches and mode changes, and rotates on reset=true.
	_, raw1 := hubA.doOwner("POST", "/api/super/peers/invite", map[string]any{"mode": "advise"})
	var card1 PeerInviteResult
	_ = json.Unmarshal(raw1, &card1)

	_, raw2 := hubA.doOwner("POST", "/api/super/peers/invite", map[string]any{"mode": "notify"})
	var card2 PeerInviteResult
	_ = json.Unmarshal(raw2, &card2)
	if card1.Code != card2.Code || card2.Mode != "notify" {
		t.Fatalf("non-reset card fetch should keep code stable: card1=%+v card2=%+v", card1, card2)
	}

	_, raw3 := hubA.doOwner("POST", "/api/super/peers/invite", map[string]any{"reset": true})
	var card3 PeerInviteResult
	_ = json.Unmarshal(raw3, &card3)
	if card3.Code == card1.Code || !strings.HasPrefix(card3.JoinURL, peerJoinBaseURL) {
		t.Fatalf("reset=true must rotate card secret: old=%s new=%s", card1.Code, card3.Code)
	}

	// Old card1 must now be rejected with 401.
	resp, raw := hubB.doOwner("POST", "/api/super/peers/pair", map[string]any{"invite_code": card1.JoinURL})
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("reset card old link expected 401, got %d: %s", resp.StatusCode, raw)
	}

	// Legacy expired invite (exp in the past) must still be rejected by decodePeerInvite.
	expiredLegacy, _ := encodePeerInvite(PeerInvitePayload{
		V: 1, Code: card3.Code, HubID: card3.HubID, HubName: "Old", Addrs: card3.Addrs,
		Exp: time.Now().Add(-5 * time.Minute).Unix(),
	})
	if _, err := decodePeerInvite(expiredLegacy, time.Now()); err == nil {
		t.Fatalf("expected expired legacy invite to fail decodePeerInvite")
	}
	// Extraction supports v2 (/join/#2.<b64>, collie-peer://v2/<b64>, web+collie://join/2.<b64>, ?peer_invite=)
	// as well as legacy v1 (/join/#<b64>, collie-peer://v1/<b64>).
	b64V2 := strings.TrimPrefix(card3.InviteCode, peerInvitePrefixV2)
	v1Code, err := encodePeerInviteV1(PeerInvitePayload{
		V: 1, Code: card3.Code, HubID: card3.HubID, HubName: card3.HubName, Addrs: card3.Addrs,
	})
	if err != nil {
		t.Fatalf("encodePeerInviteV1: %v", err)
	}
	b64V1 := strings.TrimPrefix(v1Code, peerInvitePrefix)
	for _, sample := range []string{
		card3.InviteCode,
		card3.JoinURL,
		"https://lihuanyu0w0.github.io/collie/join/#collie-peer://v2/" + b64V2,
		"web+collie://join/2." + b64V2,
		"web+collie://2." + b64V2,
		"http://127.0.0.1:17788/?peer_invite=web%2Bcollie%3A%2F%2Fjoin%2F2." + b64V2,
		"http://127.0.0.1:17788/?peer_invite=collie-peer%3A%2F%2Fv2%2F" + b64V2,
		v1Code,
		"https://lihuanyu0w0.github.io/collie/join/#" + b64V1,
		"https://lihuanyu0w0.github.io/collie/join/#collie-peer://v1/" + b64V1,
		"web+collie://join/" + b64V1,
		"http://127.0.0.1:17788/?peer_invite=collie-peer%3A%2F%2Fv1%2F" + b64V1,
		"加我好友\n" + card3.JoinURL + "\n谢谢",
	} {
		dec, err := decodePeerInvite(sample, time.Now())
		if err != nil {
			t.Fatalf("decodePeerInvite(%q) failed: %v", sample, err)
		}
		if dec.HubID != card3.HubID || dec.Code != card3.Code || len(dec.Addrs) == 0 {
			t.Fatalf("decodePeerInvite(%q) mismatch: %+v", sample, dec)
		}
	}

	// 2. Legacy v0.3.0-v0.3.2 requester (request_approval=false) -> New Hub A:
	//    still lands in pending_incoming and gated token is 403-blocked until Hub A approves.
	tokCIn := newPeerToken()
	hubC.sup.peers.registerPendingToken(tokCIn, card3.HubID)
	legacyBody, _ := json.Marshal(peerPairAcceptReq{
		Code:              card3.Code,
		HubID:             "hub_legacy_c",
		HubName:           "LegacyC",
		Addrs:             []string{hubC.http.URL},
		OutboundTokenForA: tokCIn,
		ProtocolVer:       1,
		RequestApproval:   false,
	})
	req, _ := http.NewRequest("POST", hubA.http.URL+"/api/peer/pair/accept", bytes.NewReader(legacyBody))
	req.Header.Set("Content-Type", "application/json")
	lr, err := hubA.client.Do(req)
	if err != nil {
		t.Fatalf("legacy accept: %v", err)
	}
	lraw, _ := io.ReadAll(lr.Body)
	lr.Body.Close()
	var acc peerPairAcceptResp
	_ = json.Unmarshal(lraw, &acc)
	if lr.StatusCode != 200 || !acc.OK || !isValidPeerTokenFormat(acc.OutboundTokenForB) {
		t.Fatalf("legacy accept expected gated token, got %d: %s", lr.StatusCode, lraw)
	}

	// Before approval, gated token MUST get 403 on Hub A's /api/peer/inbox and /api/peer/ping.
	pingReq, _ := http.NewRequest("GET", hubA.http.URL+"/api/peer/ping", nil)
	pingReq.Header.Set("Authorization", "Bearer "+acc.OutboundTokenForB)
	pr, _ := hubA.client.Do(pingReq)
	pr.Body.Close()
	if pr.StatusCode != http.StatusForbidden {
		t.Fatalf("legacy gated token before approval expected 403 on /ping, got %d", pr.StatusCode)
	}
	inboxBody, _ := json.Marshal(PeerInboxPayload{
		MsgID: newPeerMsgID(), TopicID: "legacy-gate", Kind: "ask", Body: "blocked", SentAt: time.Now().Format(time.RFC3339),
	})
	inReq, _ := http.NewRequest("POST", hubA.http.URL+"/api/peer/inbox", bytes.NewReader(inboxBody))
	inReq.Header.Set("Authorization", "Bearer "+acc.OutboundTokenForB)
	inReq.Header.Set("Content-Type", "application/json")
	ir, _ := hubA.client.Do(inReq)
	ir.Body.Close()
	if ir.StatusCode != http.StatusForbidden {
		t.Fatalf("legacy gated token before approval expected 403 on /inbox, got %d", ir.StatusCode)
	}

	// Once Hub A approves, legacy gated token immediately works!
	_, listRaw := hubA.doOwner("GET", "/api/super/peers", nil)
	var listA struct{ Peers []PeerView }
	_ = json.Unmarshal(listRaw, &listA)
	if len(listA.Peers) != 1 || listA.Peers[0].Status != "pending_incoming" {
		t.Fatalf("expected legacy peer pending_incoming on A, got %s", listRaw)
	}
	resp, raw = hubA.doOwner("POST", "/api/super/peers/"+listA.Peers[0].ID+"/approve", nil)
	if resp.StatusCode != 200 {
		t.Fatalf("approve legacy peer failed: %d %s", resp.StatusCode, raw)
	}
	pingReq2, _ := http.NewRequest("GET", hubA.http.URL+"/api/peer/ping", nil)
	pingReq2.Header.Set("Authorization", "Bearer "+acc.OutboundTokenForB)
	pr2, _ := hubA.client.Do(pingReq2)
	pr2.Body.Close()
	if pr2.StatusCode != 200 {
		t.Fatalf("legacy gated token after approval expected 200 on /ping, got %d", pr2.StatusCode)
	}

	// 3. New Hub B -> Legacy v0.3.0-v0.3.2 Hub A (no status:"pending", returns outbound_token_for_b immediately):
	//    New Hub B falls back cleanly to immediate active pairing.
	legacyTok := newPeerToken()
	legacyHubA := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/peer/pair/accept" {
			writeJSON(w, 200, map[string]any{
				"ok":                   true,
				"hub_id":               "hub_old_v031",
				"hub_name":             "OldV031",
				"addrs":                []string{},
				"chosen_addr":          hubB.http.URL,
				"outbound_token_for_b": legacyTok,
				"protocol_ver":         1,
			})
			return
		}
		w.WriteHeader(404)
	}))
	defer legacyHubA.Close()
	oldInv, _ := encodePeerInvite(PeerInvitePayload{
		V: 1, Code: "OLDV031CODE1", HubID: "hub_old_v031", HubName: "OldV031",
		Addrs: []string{legacyHubA.URL}, Exp: time.Now().Add(5 * time.Minute).Unix(),
	})
	resp, raw = hubB.doOwner("POST", "/api/super/peers/pair", map[string]any{"invite_code": oldInv})
	if resp.StatusCode != 200 {
		t.Fatalf("new B -> legacy A expected 200, got %d: %s", resp.StatusCode, raw)
	}
	var legPair struct {
		OK      bool     `json:"ok"`
		Pending bool     `json:"pending"`
		Peer    PeerView `json:"peer"`
	}
	if err := json.Unmarshal(raw, &legPair); err != nil || !legPair.OK || legPair.Pending || legPair.Peer.Status != "active" {
		t.Fatalf("expected immediate active fallback for legacy A, got %s", raw)
	}
}

func TestPeerInviteV2CompactAndCompat(t *testing.T) {
	t.Parallel()
	now := time.Now()
	secret := newPeerInviteCodeSecret()
	if len(secret) != 22 {
		t.Fatalf("expected 16-byte base64url card secret (22 chars), got len=%d (%q)", len(secret), secret)
	}

	// 1. Compact v2 omits http://, keeps https://, caps to top-2 non-IPv6 addrs, omits k for "card" and e=0.
	p := PeerInvitePayload{
		HubID:   "hub_a1b2c3d4e5f6",
		HubName: "Alice",
		Code:    secret,
		Addrs: []string{
			"http://100.64.1.2:7788",
			"https://hub.example.com",
			"http://192.168.1.9:7788",
			"http://[fd7a:115c:a1e0::1]:7788",
			"http://127.0.0.1:7788",
		},
		Kind: "card",
	}
	v2Code, err := encodePeerInvite(p)
	if err != nil {
		t.Fatalf("encodePeerInvite: %v", err)
	}
	if !strings.HasPrefix(v2Code, peerInvitePrefixV2) {
		t.Fatalf("encodePeerInvite must default to v2 prefix, got %q", v2Code)
	}
	v1Code, err := encodePeerInviteV1(p)
	if err != nil {
		t.Fatalf("encodePeerInviteV1: %v", err)
	}
	if len(v2Code) >= len(v1Code)*65/100 {
		t.Fatalf("expected v2 (%d chars) to be significantly shorter than v1 (%d chars): v2=%s", len(v2Code), len(v1Code), v2Code)
	}

	// Inspect raw compact JSON keys inside v2 base64url payload.
	b64V2 := strings.TrimPrefix(v2Code, peerInvitePrefixV2)
	rawJSON, err := base64.RawURLEncoding.DecodeString(b64V2)
	if err != nil {
		t.Fatalf("decode v2 base64: %v", err)
	}
	var m map[string]any
	if err := json.Unmarshal(rawJSON, &m); err != nil {
		t.Fatalf("unmarshal v2 json: %v", err)
	}
	if _, hasK := m["k"]; hasK {
		t.Fatalf("v2 permanent card must omit k, got %s", rawJSON)
	}
	if _, hasE := m["e"]; hasE {
		t.Fatalf("v2 permanent card with exp=0 must omit e, got %s", rawJSON)
	}
	if m["h"] != p.HubID || m["n"] != p.HubName || m["t"] != secret {
		t.Fatalf("unexpected v2 compact fields: %s", rawJSON)
	}
	rawAddrs, _ := m["a"].([]any)
	if len(rawAddrs) != 2 || rawAddrs[0] != "100.64.1.2:7788" || rawAddrs[1] != "https://hub.example.com" {
		t.Fatalf("unexpected v2 compact addrs (want http:// stripped, https:// kept, max 2): %s", rawJSON)
	}

	// 2. Round-trip decode for v2, /join/#2., web+collie://join/2., ?peer_invite=, and legacy v1.
	joinURL := peerJoinURL(v2Code)
	if joinURL != peerJoinBaseURL+"2."+b64V2 {
		t.Fatalf("peerJoinURL(v2) = %q, want %q", joinURL, peerJoinBaseURL+"2."+b64V2)
	}
	for _, sample := range []string{
		v2Code,
		joinURL,
		"2." + b64V2,
		"web+collie://join/2." + b64V2,
		"web+collie://2." + b64V2,
		"http://127.0.0.1:7788/?peer_invite=" + url.QueryEscape("web+collie://join/2."+b64V2),
		v1Code,
		peerJoinURL(v1Code),
	} {
		dec, err := decodePeerInvite(sample, now)
		if err != nil {
			t.Fatalf("decodePeerInvite(%q): %v", sample, err)
		}
		if dec.HubID != p.HubID || dec.HubName != p.HubName || dec.Code != secret || dec.Kind != "card" {
			t.Fatalf("decoded mismatch for %q: %+v", sample, dec)
		}
		if len(dec.Addrs) < 2 || dec.Addrs[0] != "http://100.64.1.2:7788" || dec.Addrs[1] != "https://hub.example.com" {
			t.Fatalf("expanded addrs mismatch for %q: %+v", sample, dec.Addrs)
		}
	}

	// 3. One-time invite preserves k="once" and e=exp, and rejects when expired.
	exp := now.Add(10 * time.Minute).Unix()
	onceCode, err := encodePeerInvite(PeerInvitePayload{
		HubID: "hub_once", HubName: "Once", Code: "ONCECODE1234",
		Addrs: []string{"http://[2001:db8::1]:7788"}, Kind: "once", Exp: exp,
	})
	if err != nil {
		t.Fatalf("encode once: %v", err)
	}
	decOnce, err := decodePeerInvite(onceCode, now)
	if err != nil {
		t.Fatalf("decode once: %v", err)
	}
	if decOnce.Kind != "once" || decOnce.Exp != exp || len(decOnce.Addrs) != 1 || decOnce.Addrs[0] != "http://[2001:db8::1]:7788" {
		t.Fatalf("unexpected decoded once invite: %+v", decOnce)
	}
	if _, err := decodePeerInvite(onceCode, time.Unix(exp+1, 0)); err == nil {
		t.Fatalf("expected expired v2 once invite to fail")
	}
}
