package collie

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	larkevent "github.com/larksuite/oapi-sdk-go/v3/event"
	larkcallback "github.com/larksuite/oapi-sdk-go/v3/event/dispatcher/callback"
	larkim "github.com/larksuite/oapi-sdk-go/v3/service/im/v1"
)

type fakeFeishuCall struct {
	Method string
	Path   string
	Query  string
	Body   string
}

type fakeFeishuServer struct {
	srv     *httptest.Server
	mu      sync.Mutex
	calls   []fakeFeishuCall
	msgSeq  int
	imgData []byte
}

func newFakeFeishuServer(t *testing.T) *fakeFeishuServer {
	t.Helper()
	ff := &fakeFeishuServer{
		imgData: []byte("\x89PNG\r\n\x1a\nfake-png-bytes"),
	}
	ff.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		ff.mu.Lock()
		ff.calls = append(ff.calls, fakeFeishuCall{
			Method: r.Method,
			Path:   r.URL.Path,
			Query:  r.URL.RawQuery,
			Body:   string(raw),
		})
		ff.mu.Unlock()

		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.URL.Path == "/open-apis/auth/v3/tenant_access_token/internal":
			var req struct {
				AppID     string `json:"app_id"`
				AppSecret string `json:"app_secret"`
			}
			_ = json.Unmarshal(raw, &req)
			if req.AppSecret != "sec_valid_feishu_test" {
				w.WriteHeader(200)
				_, _ = w.Write([]byte(`{"code":10003,"msg":"invalid app_secret"}`))
				return
			}
			_, _ = w.Write([]byte(`{"code":0,"msg":"ok","tenant_access_token":"t-fake-token","expire":7200}`))

		case r.URL.Path == "/open-apis/bot/v3/info":
			_, _ = w.Write([]byte(`{"code":0,"msg":"ok","bot":{"app_name":"Collie Test Bot","open_id":"ou_bot_collie"}}`))

		case r.Method == http.MethodPost && r.URL.Path == "/open-apis/im/v1/messages":
			ff.mu.Lock()
			ff.msgSeq++
			id := "om_card_" + string(rune('0'+ff.msgSeq))
			ff.mu.Unlock()
			_, _ = w.Write([]byte(`{"code":0,"msg":"ok","data":{"message_id":"` + id + `"}}`))

		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/reply"):
			ff.mu.Lock()
			ff.msgSeq++
			id := "om_reply_" + string(rune('0'+ff.msgSeq))
			ff.mu.Unlock()
			_, _ = w.Write([]byte(`{"code":0,"msg":"ok","data":{"message_id":"` + id + `"}}`))

		case r.Method == http.MethodPatch && strings.HasPrefix(r.URL.Path, "/open-apis/im/v1/messages/"):
			_, _ = w.Write([]byte(`{"code":0,"msg":"ok"}`))

		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/reactions"):
			_, _ = w.Write([]byte(`{"code":0,"msg":"ok","data":{"reaction_id":"r_1"}}`))

		case r.Method == http.MethodGet && strings.Contains(r.URL.Path, "/resources/"):
			w.Header().Set("Content-Type", "image/png")
			w.Header().Set("Content-Disposition", `attachment; filename="shot.png"`)
			_, _ = w.Write(ff.imgData)

		case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/open-apis/contact/v3/users/"):
			if r.URL.Query().Get("user_id_type") != "open_id" {
				w.WriteHeader(400)
				_, _ = w.Write([]byte(`{"code":400,"msg":"missing user_id_type=open_id"}`))
				return
			}
			uid := strings.TrimPrefix(r.URL.Path, "/open-apis/contact/v3/users/")
			switch uid {
			case "ou_colleague_named":
				_, _ = w.Write([]byte(`{"code":0,"msg":"ok","data":{"user":{"name":"林同学"}}}`))
			case "ou_colleague_noperm":
				_, _ = w.Write([]byte(`{"code":99991672,"msg":"no permission"}`))
			default:
				_, _ = w.Write([]byte(`{"code":0,"msg":"ok","data":{"user":{}}}`))
			}

		default:
			w.WriteHeader(404)
			_, _ = w.Write([]byte(`{"code":404,"msg":"not found"}`))
		}
	}))
	t.Cleanup(ff.srv.Close)
	return ff
}

func (ff *fakeFeishuServer) waitForCall(t *testing.T, pred func(fakeFeishuCall) bool) fakeFeishuCall {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		ff.mu.Lock()
		for _, c := range ff.calls {
			if pred(c) {
				ff.mu.Unlock()
				return c
			}
		}
		ff.mu.Unlock()
		time.Sleep(10 * time.Millisecond)
	}
	ff.mu.Lock()
	defer ff.mu.Unlock()
	t.Fatalf("timed out waiting for fake Feishu call; recorded calls: %+v", ff.calls)
	return fakeFeishuCall{}
}

func extractCardContent(body string) string {
	var env struct {
		Content string `json:"content"`
	}
	if json.Unmarshal([]byte(body), &env) == nil && env.Content != "" {
		return env.Content
	}
	return body
}

func strPtr(s string) *string { return &s }

func setupFeishuTestHarness(t *testing.T) (*Server, *Super, *FeishuAdapter, *fakeFeishuServer, EventSink) {
	t.Helper()
	dir := t.TempDir()
	ff := newFakeFeishuServer(t)
	s := &Server{token: "tok-feishu-test", home: dir}
	sp, err := newSuper(s, dir)
	if err != nil {
		t.Fatalf("newSuper: %v", err)
	}
	t.Cleanup(func() { sp.db.Close() })
	s.sup = sp
	sp.Seen("http://127.0.0.1:7788")

	fa := sp.feishu
	fa.baseURLOverride = ff.srv.URL
	fa.httpClient = ff.srv.Client()

	cfgJSON, _ := json.Marshal(FeishuConfig{
		Region:           "cn",
		AppID:            "cli_test_app",
		AllowOpenIDs:     []string{"ou_allowed_owner"},
		GroupMentionOnly: true,
		InstantChatSync:  true,
	})
	_ = sp.secrets.Update(feishuKind, map[string]string{
		"app_id":     "cli_test_app",
		"app_secret": "sec_valid_feishu_test",
	})
	rec := IntegrationRecord{Kind: feishuKind, Enabled: true, Config: cfgJSON}
	_ = saveIntegration(sp.db, rec)
	sp.integrations.mu.Lock()
	sp.integrations.records[feishuKind] = rec
	sp.integrations.mu.Unlock()

	client := fa.newSDKClient("cli_test_app", "sec_valid_feishu_test", "cn")
	sink := &managerSink{mgr: sp.integrations, kind: feishuKind}
	fa.mu.Lock()
	fa.activeCfg = parseFeishuConfig(cfgJSON)
	fa.activeSecret = "sec_valid_feishu_test"
	fa.activeClient = client
	fa.activeSink = sink
	fa.botOpenID = "ou_bot_collie"
	fa.botName = "Collie Test Bot"
	fa.mu.Unlock()

	return s, sp, fa, ff, sink
}

func TestFeishuAllowlistAndGroupMentionGuard(t *testing.T) {
	_, sp, fa, _, sink := setupFeishuTestHarness(t)
	ctx := context.Background()

	// 1) Unauthorized sender in p2p -> rejected, zero inbox/entry additions.
	unauthMsg := &larkim.P2MessageReceiveV1{
		EventV2Base: &larkevent.EventV2Base{Header: &larkevent.EventHeader{EventID: "evt_unauth_1"}},
		Event: &larkim.P2MessageReceiveV1Data{
			Sender: &larkim.EventSender{
				SenderType: strPtr("user"),
				SenderId:   &larkim.UserId{OpenId: strPtr("ou_stranger")},
			},
			Message: &larkim.EventMessage{
				MessageId:   strPtr("om_1"),
				ChatId:      strPtr("oc_p2p_stranger"),
				ChatType:    strPtr("p2p"),
				MessageType: strPtr("text"),
				Content:     strPtr(`{"text":"rm -rf everything"}`),
			},
		},
	}
	if err := fa.handleMessageReceive(ctx, unauthMsg, sink); err != nil {
		t.Fatal(err)
	}
	if len(sp.st.Entries) != 0 || len(sp.st.Triggers) != 0 {
		t.Fatalf("unauthorized sender polluted state: entries=%d triggers=%d", len(sp.st.Entries), len(sp.st.Triggers))
	}

	// 2) Unauthorized card click -> rejected with warning toast, zero additions.
	unauthCard, err := fa.handleCardAction(ctx, &larkcallback.CardActionTriggerEvent{
		Event: &larkcallback.CardActionTriggerRequest{
			Operator: &larkcallback.Operator{OpenID: "ou_stranger"},
			Action:   &larkcallback.CallBackAction{Value: map[string]any{"action": "pick_opt", "entry_id": "e1", "opt_idx": "0"}},
		},
	}, sink)
	if err != nil || unauthCard.Toast == nil || unauthCard.Toast.Type != "warning" {
		t.Fatalf("expected warning toast for unauthorized card click, got %+v err=%v", unauthCard, err)
	}

	// 3) Bot self message -> ignored.
	botMsg := &larkim.P2MessageReceiveV1{
		EventV2Base: &larkevent.EventV2Base{Header: &larkevent.EventHeader{EventID: "evt_bot_1"}},
		Event: &larkim.P2MessageReceiveV1Data{
			Sender: &larkim.EventSender{
				SenderType: strPtr("bot"),
				SenderId:   &larkim.UserId{OpenId: strPtr("ou_allowed_owner")},
			},
			Message: &larkim.EventMessage{
				MessageId:   strPtr("om_bot"),
				ChatId:      strPtr("oc_group_1"),
				ChatType:    strPtr("group"),
				MessageType: strPtr("text"),
				Content:     strPtr(`{"text":"echo"}`),
			},
		},
	}
	_ = fa.handleMessageReceive(ctx, botMsg, sink)
	if len(sp.st.Entries) != 0 {
		t.Fatal("bot self message should be ignored")
	}

	// 4) Group message without @Collie -> ignored.
	groupNoMention := &larkim.P2MessageReceiveV1{
		EventV2Base: &larkevent.EventV2Base{Header: &larkevent.EventHeader{EventID: "evt_grp_1"}},
		Event: &larkim.P2MessageReceiveV1Data{
			Sender: &larkim.EventSender{
				SenderType: strPtr("user"),
				SenderId:   &larkim.UserId{OpenId: strPtr("ou_allowed_owner")},
			},
			Message: &larkim.EventMessage{
				MessageId:   strPtr("om_grp_1"),
				ChatId:      strPtr("oc_group_1"),
				ChatType:    strPtr("group"),
				MessageType: strPtr("text"),
				Content:     strPtr(`{"text":"聊点别的"}`),
			},
		},
	}
	_ = fa.handleMessageReceive(ctx, groupNoMention, sink)
	if len(sp.st.Entries) != 0 {
		t.Fatal("group message without @Collie should be ignored")
	}

	// 5) Group message with @Collie -> strips @_user_1 placeholder and enqueues cleanly.
	groupMention := &larkim.P2MessageReceiveV1{
		EventV2Base: &larkevent.EventV2Base{Header: &larkevent.EventHeader{EventID: "evt_grp_2"}},
		Event: &larkim.P2MessageReceiveV1Data{
			Sender: &larkim.EventSender{
				SenderType: strPtr("user"),
				SenderId:   &larkim.UserId{OpenId: strPtr("ou_allowed_owner")},
			},
			Message: &larkim.EventMessage{
				MessageId:   strPtr("om_grp_2"),
				ChatId:      strPtr("oc_group_1"),
				ChatType:    strPtr("group"),
				MessageType: strPtr("text"),
				Content:     strPtr(`{"text":"@_user_1 帮我看下昨晚构建"}`),
				Mentions: []*larkim.MentionEvent{{
					Key:           strPtr("@_user_1"),
					Id:            &larkim.UserId{OpenId: strPtr("ou_bot_collie")},
					MentionedType: strPtr("bot"),
					Name:          strPtr("Collie Test Bot"),
				}},
			},
		},
	}
	if err := fa.handleMessageReceive(ctx, groupMention, sink); err != nil {
		t.Fatal(err)
	}
	if len(sp.st.Entries) != 1 || sp.st.Entries[0].Text != "帮我看下昨晚构建" {
		t.Fatalf("group mention text = %+v, want stripped '帮我看下昨晚构建'", sp.st.Entries)
	}
}

func TestFeishuFullBidirectionalLoopAndReverseSync(t *testing.T) {
	s, sp, fa, ff, sink := setupFeishuTestHarness(t)
	ctx := context.Background()

	// Stage A: Feishu p2p message enters chat:main with fromHuman priority.
	inMsg := &larkim.P2MessageReceiveV1{
		EventV2Base: &larkevent.EventV2Base{Header: &larkevent.EventHeader{EventID: "evt_p2p_1"}},
		Event: &larkim.P2MessageReceiveV1Data{
			Sender: &larkim.EventSender{
				SenderType: strPtr("user"),
				SenderId:   &larkim.UserId{OpenId: strPtr("ou_allowed_owner")},
			},
			Message: &larkim.EventMessage{
				MessageId:   strPtr("om_in_1"),
				ChatId:      strPtr("oc_p2p_owner"),
				ChatType:    strPtr("p2p"),
				MessageType: strPtr("text"),
				Content:     strPtr(`{"text":"查下昨晚单测失败原因"}`),
			},
		},
	}
	if err := fa.handleMessageReceive(ctx, inMsg, sink); err != nil {
		t.Fatalf("handleMessageReceive: %v", err)
	}
	if len(sp.st.Triggers) != 1 || len(fromHuman(sp.st.Triggers)) != 1 {
		t.Fatalf("expected Feishu message to have fromHuman priority, triggers=%+v", sp.st.Triggers)
	}
	// Verify async OK reaction was sent to incoming message.
	ff.waitForCall(t, func(c fakeFeishuCall) bool {
		return c.Method == http.MethodPost && c.Path == "/open-apis/im/v1/messages/om_in_1/reactions"
	})

	// Stage B: Collie replies with --opt options -> immediate Interactive Card 2.0 to Feishu.
	replyEntry, _ := sp.Reply(mainThread, "定位到了，要怎么修？", sp.st.Entries[0].ID, []string{"按方案一修复", "先看改动清单"}, false)
	sendCall := ff.waitForCall(t, func(c fakeFeishuCall) bool {
		return c.Method == http.MethodPost && c.Path == "/open-apis/im/v1/messages" && strings.Contains(extractCardContent(c.Body), "按方案一修复")
	})
	cardRaw := extractCardContent(sendCall.Body)
	for _, want := range []string{`"schema":"2.0"`, `"type":"primary"`, `"action":"pick_opt"`, `"entry_id":"` + replyEntry.ID + `"`, `lk_mobile_jump_to_browser=true`} {
		if !strings.Contains(cardRaw, want) {
			t.Errorf("outbound card missing %q in card:\n%s", want, cardRaw)
		}
	}
	// Wait until integration_messages mapping is persisted.
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if rec, _ := getIntegrationMessage(sp.db, replyEntry.ID, feishuKind); rec != nil && rec.ExternalID != "" {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if rec, _ := getIntegrationMessage(sp.db, replyEntry.ID, feishuKind); rec == nil || rec.ExternalID == "" {
		t.Fatal("integration_messages mapping was not saved")
	}

	// Confirm pending item exists before button click.
	sp.mu.Lock()
	pendBefore := sp.pendingLocked(time.Now())
	sp.mu.Unlock()
	if len(pendBefore) != 1 || pendBefore[0].ID != replyEntry.ID {
		t.Fatalf("expected 1 pending item before click, got %+v", pendBefore)
	}

	// Stage C: User clicks option 0 in Feishu (<3s response, updates card, clears Web pending).
	start := time.Now()
	cbResp, err := fa.handleCardAction(ctx, &larkcallback.CardActionTriggerEvent{
		EventV2Base: &larkevent.EventV2Base{Header: &larkevent.EventHeader{EventID: "evt_cb_1"}},
		Event: &larkcallback.CardActionTriggerRequest{
			Operator: &larkcallback.Operator{OpenID: "ou_allowed_owner"},
			Context:  &larkcallback.Context{OpenChatID: "oc_p2p_owner", OpenMessageID: "om_card_1"},
			Action:   &larkcallback.CallBackAction{Value: map[string]any{"action": "pick_opt", "entry_id": replyEntry.ID, "opt_idx": "0"}},
		},
	}, sink)
	if err != nil {
		t.Fatalf("handleCardAction: %v", err)
	}
	if elapsed := time.Since(start); elapsed > 3*time.Second {
		t.Fatalf("card callback took %v (>3s)", elapsed)
	}
	if cbResp.Toast == nil || !strings.Contains(cbResp.Toast.Content, "按方案一修复") {
		t.Fatalf("unexpected callback toast: %+v", cbResp.Toast)
	}
	if cbResp.Card == nil || cbResp.Card.Type != "raw" {
		t.Fatalf("expected raw updated card response, got %+v", cbResp.Card)
	}
	cardBytes, _ := json.Marshal(cbResp.Card.Data)
	if !strings.Contains(string(cardBytes), "已选择：按方案一修复") || strings.Contains(string(cardBytes), `"action":"pick_opt"`) {
		t.Fatalf("updated card should show resolved choice with buttons removed: %s", string(cardBytes))
	}

	// Pending bar must now be cleared, and chat:main must hold quoted user choice.
	sp.mu.Lock()
	pendAfter := sp.pendingLocked(time.Now())
	lastEntry := sp.st.Entries[len(sp.st.Entries)-1]
	sp.mu.Unlock()
	if len(pendAfter) != 0 {
		t.Fatalf("expected pending cleared after Feishu button click, got %+v", pendAfter)
	}
	if lastEntry.Role != "user" || lastEntry.Text != "按方案一修复" || lastEntry.Re == nil || lastEntry.Re.Entry != replyEntry.ID {
		t.Fatalf("unexpected user choice entry: %+v", lastEntry)
	}

	// Double-click must be idempotent (no second user message created).
	entriesBeforeDup := len(sp.st.Entries)
	dupResp, err := fa.handleCardAction(ctx, &larkcallback.CardActionTriggerEvent{
		EventV2Base: &larkevent.EventV2Base{Header: &larkevent.EventHeader{EventID: "evt_cb_2"}},
		Event: &larkcallback.CardActionTriggerRequest{
			Operator: &larkcallback.Operator{OpenID: "ou_allowed_owner"},
			Context:  &larkcallback.Context{OpenChatID: "oc_p2p_owner", OpenMessageID: "om_card_1"},
			Action:   &larkcallback.CallBackAction{Value: map[string]any{"action": "pick_opt", "entry_id": replyEntry.ID, "opt_idx": "0"}},
		},
	}, sink)
	if err != nil || dupResp.Toast == nil || dupResp.Toast.Type != "info" {
		t.Fatalf("expected info toast on double-click, got %+v err=%v", dupResp, err)
	}
	if len(sp.st.Entries) != entriesBeforeDup {
		t.Fatal("double-click created duplicate entry")
	}

	// Stage D1: Reverse sync on Web Dismiss -> PATCH old Feishu card to resolved gray.
	checkEntry, _ := sp.Reply(mainThread, "文档已更新，请过目", "", nil, true)
	ff.waitForCall(t, func(c fakeFeishuCall) bool {
		return c.Method == http.MethodPost && c.Path == "/open-apis/im/v1/messages" && strings.Contains(extractCardContent(c.Body), `"action":"ack"`)
	})
	deadline = time.Now().Add(time.Second)
	var checkMsgID string
	for time.Now().Before(deadline) {
		if rec, _ := getIntegrationMessage(sp.db, checkEntry.ID, feishuKind); rec != nil && rec.ExternalID != "" {
			checkMsgID = rec.ExternalID
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if checkMsgID == "" {
		t.Fatal("check entry mapping missing")
	}
	sp.Dismiss(checkEntry.ID)
	patchCall := ff.waitForCall(t, func(c fakeFeishuCall) bool {
		return c.Method == http.MethodPatch && c.Path == "/open-apis/im/v1/messages/"+checkMsgID
	})
	patchRaw := extractCardContent(patchCall.Body)
	if !strings.Contains(patchRaw, "已在网页端标记已看") || !strings.Contains(patchRaw, `"template":"grey"`) {
		t.Fatalf("unexpected reverse-sync PATCH payload on dismiss: %s", patchRaw)
	}

	// Stage D2: Reverse sync on Web POST /api/super/say -> PATCH old Feishu card.
	optEntry2, _ := sp.Reply(mainThread, "还要继续合并吗？", "", []string{"合入", "暂缓"}, false)
	ff.waitForCall(t, func(c fakeFeishuCall) bool {
		return c.Method == http.MethodPost && c.Path == "/open-apis/im/v1/messages" && strings.Contains(extractCardContent(c.Body), optEntry2.ID)
	})
	deadline = time.Now().Add(time.Second)
	var opt2MsgID string
	for time.Now().Before(deadline) {
		if rec, _ := getIntegrationMessage(sp.db, optEntry2.ID, feishuKind); rec != nil && rec.ExternalID != "" {
			opt2MsgID = rec.ExternalID
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	mux := http.NewServeMux()
	s.superRoutes(mux)
	sayBody, _ := json.Marshal(map[string]any{"text": "合入", "re": optEntry2.ID})
	req := httptest.NewRequest(http.MethodPost, "/api/super/say", bytes.NewReader(sayBody))
	req.Header.Set("Authorization", "Bearer tok-feishu-test")
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != 200 {
		t.Fatalf("superSay status=%d body=%s", w.Code, w.Body.String())
	}
	patchCall2 := ff.waitForCall(t, func(c fakeFeishuCall) bool {
		return c.Method == http.MethodPatch && c.Path == "/open-apis/im/v1/messages/"+opt2MsgID
	})
	if !strings.Contains(extractCardContent(patchCall2.Body), "已在网页端回复") {
		t.Fatalf("unexpected reverse-sync PATCH payload on web say: %s", extractCardContent(patchCall2.Body))
	}
}

func TestFeishuTestCardAndImageDownloadAndPairing(t *testing.T) {
	_, sp, fa, ff, sink := setupFeishuTestHarness(t)
	ctx := context.Background()

	// 1) One-click Test sends a real verification card without adding entries or triggers.
	res, err := sp.integrations.Test(ctx, feishuKind, SaveRequest{})
	if err != nil || !res.OK {
		t.Fatalf("Test failed: res=%+v err=%v", res, err)
	}
	if len(sp.st.Entries) != 0 || len(sp.st.Triggers) != 0 {
		t.Fatal("Test must not pollute chat:main or triggers")
	}
	if sent, _ := res.Detail["card_sent"].(bool); !sent {
		t.Fatalf("expected card_sent=true, detail=%+v", res.Detail)
	}
	// Clicking test_ping callback on the verification card must not pollute chat:main.
	pingResp, err := fa.handleCardAction(ctx, &larkcallback.CardActionTriggerEvent{
		Event: &larkcallback.CardActionTriggerRequest{
			Operator: &larkcallback.Operator{OpenID: "ou_allowed_owner"},
			Action:   &larkcallback.CallBackAction{Value: map[string]any{"action": "test_ping"}},
		},
	}, sink)
	if err != nil || pingResp.Toast == nil || pingResp.Toast.Type != "success" || len(sp.st.Entries) != 0 {
		t.Fatalf("unexpected test_ping callback result: %+v err=%v entries=%d", pingResp, err, len(sp.st.Entries))
	}

	// 2) Bad secret returns OK=false cleanly.
	badRes, err := sp.integrations.Test(ctx, feishuKind, SaveRequest{
		Secrets: map[string]string{"app_secret": "sec_wrong_secret"},
	})
	if err != nil || badRes.OK {
		t.Fatalf("expected OK=false for wrong secret, got %+v err=%v", badRes, err)
	}

	// 3) Inbound image message downloads via MessageResource.Get into uploads/ with 0600.
	imgMsg := &larkim.P2MessageReceiveV1{
		EventV2Base: &larkevent.EventV2Base{Header: &larkevent.EventHeader{EventID: "evt_img_1"}},
		Event: &larkim.P2MessageReceiveV1Data{
			Sender: &larkim.EventSender{
				SenderType: strPtr("user"),
				SenderId:   &larkim.UserId{OpenId: strPtr("ou_allowed_owner")},
			},
			Message: &larkim.EventMessage{
				MessageId:   strPtr("om_img_1"),
				ChatId:      strPtr("oc_p2p_owner"),
				ChatType:    strPtr("p2p"),
				MessageType: strPtr("image"),
				Content:     strPtr(`{"image_key":"img_v3_test_key"}`),
			},
		},
	}
	if err := fa.handleMessageReceive(ctx, imgMsg, sink); err != nil {
		t.Fatalf("image message: %v", err)
	}
	if len(sp.st.Entries) != 1 || !strings.HasPrefix(sp.st.Entries[0].Text, "附件：\n") {
		t.Fatalf("expected saved attachment entry, got %+v", sp.st.Entries)
	}
	savedPath := strings.TrimSpace(strings.TrimPrefix(sp.st.Entries[0].Text, "附件：\n"))
	b, err := os.ReadFile(savedPath)
	if err != nil || !bytes.Equal(b, ff.imgData) {
		t.Fatalf("downloaded image mismatch: err=%v len=%d", err, len(b))
	}

	// 4) Pairing mode captures new p2p OpenID and persists to allowlist.
	if _, err := fa.StartPairing(time.Minute); err != nil {
		t.Fatalf("StartPairing: %v", err)
	}
	pairMsg := &larkim.P2MessageReceiveV1{
		EventV2Base: &larkevent.EventV2Base{Header: &larkevent.EventHeader{EventID: "evt_pair_1"}},
		Event: &larkim.P2MessageReceiveV1Data{
			Sender: &larkim.EventSender{
				SenderType: strPtr("user"),
				SenderId:   &larkim.UserId{OpenId: strPtr("ou_newly_paired")},
			},
			Message: &larkim.EventMessage{
				MessageId:   strPtr("om_pair_1"),
				ChatId:      strPtr("oc_p2p_new"),
				ChatType:    strPtr("p2p"),
				MessageType: strPtr("text"),
				Content:     strPtr(`{"text":"配对"}`),
			},
		},
	}
	if err := fa.handleMessageReceive(ctx, pairMsg, sink); err != nil {
		t.Fatalf("pairing msg: %v", err)
	}
	v, _ := sp.integrations.View(feishuKind)
	allowIDs, _ := v.Config["allow_open_ids"].([]string)
	found := false
	for _, id := range allowIDs {
		if id == "ou_newly_paired" {
			found = true
		}
	}
	if !found {
		t.Fatalf("paired open_id not persisted in allowlist: %+v", v.Config)
	}
}

func TestFeishuFeedbackConfigParsing(t *testing.T) {
	// 1) Array form + whitespace/dedup.
	c1 := parseFeishuConfig(json.RawMessage(`{"app_id":"cli_x","allow_open_ids":["ou_1"],"feedback_chat_ids":["oc_1","oc_2, oc_1"]}`))
	if len(c1.FeedbackChatIDs) != 2 || c1.FeedbackChatIDs[0] != "oc_1" || c1.FeedbackChatIDs[1] != "oc_2" {
		t.Fatalf("array parse failed: %+v", c1.FeedbackChatIDs)
	}
	// 2) Single string form with comma, Chinese comma, and newline separators.
	c2 := parseFeishuConfig(json.RawMessage(`{"feedback_chat_ids":"oc_a, oc_b\noc_c，oc_a"}`))
	if len(c2.FeedbackChatIDs) != 3 || c2.FeedbackChatIDs[0] != "oc_a" || c2.FeedbackChatIDs[1] != "oc_b" || c2.FeedbackChatIDs[2] != "oc_c" {
		t.Fatalf("string parse failed: %+v", c2.FeedbackChatIDs)
	}
	// 3) Convenience singular feedback_chat_id alias.
	c3 := parseFeishuConfig(json.RawMessage(`{"feedback_chat_id":"oc_single\noc_second"}`))
	if len(c3.FeedbackChatIDs) != 2 || c3.FeedbackChatIDs[0] != "oc_single" || c3.FeedbackChatIDs[1] != "oc_second" {
		t.Fatalf("singular alias parse failed: %+v", c3.FeedbackChatIDs)
	}
	// 4) ValidateSave + SanitizeConfig roundtrip.
	fa := newFeishuAdapter(nil, nil)
	norm, err := fa.ValidateSave(json.RawMessage(`{"region":"cn","app_id":"cli_1","allow_open_id":"ou_owner","feedback_chat_ids":"oc_fb_1\noc_fb_2"}`))
	if err != nil {
		t.Fatalf("ValidateSave: %v", err)
	}
	san := fa.SanitizeConfig(IntegrationConfig{Config: norm})
	gotIDs, _ := san["feedback_chat_ids"].([]string)
	if len(gotIDs) != 2 || gotIDs[0] != "oc_fb_1" || gotIDs[1] != "oc_fb_2" || san["feedback_chat_id"] != "oc_fb_1, oc_fb_2" {
		t.Fatalf("SanitizeConfig feedback_chat_ids mismatch: %+v", san)
	}
}

func TestFeishuGroupFeedbackSecurityRouting(t *testing.T) {
	_, sp, fa, ff, sink := setupFeishuTestHarness(t)
	ctx := context.Background()

	// Configure feedback_chat_ids and intentionally set GroupMentionOnly=false to
	// verify feedback groups STILL strictly require @Collie for non-allowlisted users.
	cfgJSON, _ := json.Marshal(FeishuConfig{
		Region:           "cn",
		AppID:            "cli_test_app",
		AllowOpenIDs:     []string{"ou_allowed_owner"},
		FeedbackChatIDs:  []string{"oc_feedback_group"},
		GroupMentionOnly: false,
		InstantChatSync:  true,
	})
	rec := IntegrationRecord{Kind: feishuKind, Enabled: true, Config: cfgJSON}
	_ = saveIntegration(sp.db, rec)
	sp.integrations.mu.Lock()
	sp.integrations.records[feishuKind] = rec
	sp.integrations.mu.Unlock()
	fa.mu.Lock()
	fa.activeCfg = parseFeishuConfig(cfgJSON)
	fa.mu.Unlock()

	countIMWrites := func() int {
		ff.mu.Lock()
		defer ff.mu.Unlock()
		n := 0
		for _, c := range ff.calls {
			if c.Method == http.MethodPost && strings.HasPrefix(c.Path, "/open-apis/im/v1/messages") {
				n++
			}
		}
		return n
	}

	// 1) Feedback group, non-allowlisted colleague WITHOUT @Collie -> 100% silent drop.
	chatter := &larkim.P2MessageReceiveV1{
		EventV2Base: &larkevent.EventV2Base{Header: &larkevent.EventHeader{EventID: "evt_fb_chatter"}},
		Event: &larkim.P2MessageReceiveV1Data{
			Sender: &larkim.EventSender{
				SenderType: strPtr("user"),
				SenderId:   &larkim.UserId{OpenId: strPtr("ou_colleague_named")},
			},
			Message: &larkim.EventMessage{
				MessageId:   strPtr("om_fb_1"),
				ChatId:      strPtr("oc_feedback_group"),
				ChatType:    strPtr("group"),
				MessageType: strPtr("text"),
				Content:     strPtr(`{"text":"大家中午吃啥"}`),
			},
		},
	}
	if err := fa.handleMessageReceive(ctx, chatter, sink); err != nil {
		t.Fatal(err)
	}
	if len(sp.st.Entries) != 0 || len(sp.st.Triggers) != 0 || countIMWrites() != 0 {
		t.Fatalf("feedback group chatter must be dropped silently: entries=%d triggers=%d writes=%d", len(sp.st.Entries), len(sp.st.Triggers), countIMWrites())
	}

	// 2) Non-feedback group, non-allowlisted colleague WITH @Collie -> 100% ignored.
	otherGroupMention := &larkim.P2MessageReceiveV1{
		EventV2Base: &larkevent.EventV2Base{Header: &larkevent.EventHeader{EventID: "evt_other_grp"}},
		Event: &larkim.P2MessageReceiveV1Data{
			Sender: &larkim.EventSender{
				SenderType: strPtr("user"),
				SenderId:   &larkim.UserId{OpenId: strPtr("ou_colleague_named")},
			},
			Message: &larkim.EventMessage{
				MessageId:   strPtr("om_other_1"),
				ChatId:      strPtr("oc_other_group"),
				ChatType:    strPtr("group"),
				MessageType: strPtr("text"),
				Content:     strPtr(`{"text":"@_user_1 帮我删库"}`),
				Mentions: []*larkim.MentionEvent{{
					Key:           strPtr("@_user_1"),
					Id:            &larkim.UserId{OpenId: strPtr("ou_bot_collie")},
					MentionedType: strPtr("bot"),
					Name:          strPtr("Collie Test Bot"),
				}},
			},
		},
	}
	if err := fa.handleMessageReceive(ctx, otherGroupMention, sink); err != nil {
		t.Fatal(err)
	}
	if len(sp.st.Entries) != 0 || len(sp.st.Triggers) != 0 || countIMWrites() != 0 {
		t.Fatalf("non-feedback group stranger @Collie must be ignored: entries=%d triggers=%d writes=%d", len(sp.st.Entries), len(sp.st.Triggers), countIMWrites())
	}

	// 3) Feedback group, non-allowlisted colleague WITH @Collie (contact name resolved) ->
	//    enqueued as read-only advisory event in chat:main, zero fake user entry, zero IM writes to group.
	fbNamed := &larkim.P2MessageReceiveV1{
		EventV2Base: &larkevent.EventV2Base{Header: &larkevent.EventHeader{EventID: "evt_fb_named"}},
		Event: &larkim.P2MessageReceiveV1Data{
			Sender: &larkim.EventSender{
				SenderType: strPtr("user"),
				SenderId:   &larkim.UserId{OpenId: strPtr("ou_colleague_named")},
			},
			Message: &larkim.EventMessage{
				MessageId:   strPtr("om_fb_2"),
				ChatId:      strPtr("oc_feedback_group"),
				ChatType:    strPtr("group"),
				MessageType: strPtr("text"),
				Content:     strPtr(`{"text":"@_user_1 手机端未读角标暗色下太亮了，建议改成黑白灰"}`),
				Mentions: []*larkim.MentionEvent{{
					Key:           strPtr("@_user_1"),
					Id:            &larkim.UserId{OpenId: strPtr("ou_bot_collie")},
					MentionedType: strPtr("bot"),
					Name:          strPtr("Collie Test Bot"),
				}},
			},
		},
	}
	if err := fa.handleMessageReceive(ctx, fbNamed, sink); err != nil {
		t.Fatal(err)
	}
	if len(sp.st.Entries) != 0 {
		t.Fatalf("group feedback must not create fake owner user entry, got %+v", sp.st.Entries)
	}
	if len(sp.st.Triggers) != 1 {
		t.Fatalf("expected 1 feedback trigger, got %d", len(sp.st.Triggers))
	}
	if len(fromHuman(sp.st.Triggers)) != 0 {
		t.Fatalf("group feedback must not carry fromHuman interrupt priority")
	}
	trig := sp.st.Triggers[0]
	if trig.Thread != mainThread || trig.Source != "webhook:feishu" || trig.Action != "respond" || len(trig.Targets) != 1 || trig.Targets[0] != routerName {
		t.Fatalf("unexpected feedback trigger routing: %+v", trig)
	}
	for _, want := range []string{
		feishuGroupFeedbackTag,
		"林同学 (" + maskIdentifier("ou_colleague_named") + ")",
		"群 ID：oc_feedback_group",
		"手机端未读角标暗色下太亮了，建议改成黑白灰",
	} {
		if !strings.Contains(trig.Text, want) {
			t.Errorf("feedback trigger text missing %q:\n%s", want, trig.Text)
		}
	}
	if strings.Contains(trig.Text, "ou_colleague_named") || strings.Contains(trig.Text, "@_user_1") {
		t.Errorf("feedback trigger leaked raw open_id or mention key:\n%s", trig.Text)
	}
	time.Sleep(40 * time.Millisecond)
	if countIMWrites() != 0 {
		t.Fatalf("group feedback must never auto-reply or react into group; writes=%d", countIMWrites())
	}

	// 4) Feedback group, non-allowlisted colleague WITH @Collie when contact lookup is denied ->
	//    gracefully falls back to masked open_id without leaking raw open_id.
	fbNoPerm := &larkim.P2MessageReceiveV1{
		EventV2Base: &larkevent.EventV2Base{Header: &larkevent.EventHeader{EventID: "evt_fb_noperm"}},
		Event: &larkim.P2MessageReceiveV1Data{
			Sender: &larkim.EventSender{
				SenderType: strPtr("user"),
				SenderId:   &larkim.UserId{OpenId: strPtr("ou_colleague_noperm")},
			},
			Message: &larkim.EventMessage{
				MessageId:   strPtr("om_fb_3"),
				ChatId:      strPtr("oc_feedback_group"),
				ChatType:    strPtr("group"),
				MessageType: strPtr("text"),
				Content:     strPtr(`{"text":"@_user_1 列表滚动有点卡"}`),
				Mentions: []*larkim.MentionEvent{{
					Key:           strPtr("@_user_1"),
					Id:            &larkim.UserId{OpenId: strPtr("ou_bot_collie")},
					MentionedType: strPtr("bot"),
					Name:          strPtr("Collie Test Bot"),
				}},
			},
		},
	}
	if err := fa.handleMessageReceive(ctx, fbNoPerm, sink); err != nil {
		t.Fatal(err)
	}
	if len(sp.st.Triggers) != 2 {
		t.Fatalf("expected 2 feedback triggers, got %d", len(sp.st.Triggers))
	}
	trig2 := sp.st.Triggers[1]
	wantMasked := maskIdentifier("ou_colleague_noperm")
	if !strings.Contains(trig2.Text, "发送者："+wantMasked) || strings.Contains(trig2.Text, "ou_colleague_noperm") {
		t.Fatalf("expected masked sender fallback %q, got:\n%s", wantMasked, trig2.Text)
	}

	// 5) Non-allowlisted user clicking a card button (even inside feedback group) -> strictly rejected.
	unauthCard, err := fa.handleCardAction(ctx, &larkcallback.CardActionTriggerEvent{
		Event: &larkcallback.CardActionTriggerRequest{
			Operator: &larkcallback.Operator{OpenID: "ou_colleague_named"},
			Context:  &larkcallback.Context{OpenChatID: "oc_feedback_group", OpenMessageID: "om_card_x"},
			Action:   &larkcallback.CallBackAction{Value: map[string]any{"action": "pick_opt", "entry_id": "e1", "opt_idx": "0"}},
		},
	}, sink)
	if err != nil || unauthCard.Toast == nil || unauthCard.Toast.Type != "warning" {
		t.Fatalf("non-allowlisted card callback in feedback group must be rejected, got %+v err=%v", unauthCard, err)
	}

	// 6) Allowlisted owner behavior in feedback group remains 100% unchanged, AND a subsequent
	//    colleague feedback followed by Collie's reply routes strictly to owner DM (never the group!).
	ownerGrpMsg := &larkim.P2MessageReceiveV1{
		EventV2Base: &larkevent.EventV2Base{Header: &larkevent.EventHeader{EventID: "evt_owner_grp"}},
		Event: &larkim.P2MessageReceiveV1Data{
			Sender: &larkim.EventSender{
				SenderType: strPtr("user"),
				SenderId:   &larkim.UserId{OpenId: strPtr("ou_allowed_owner")},
			},
			Message: &larkim.EventMessage{
				MessageId:   strPtr("om_owner_grp_1"),
				ChatId:      strPtr("oc_feedback_group"),
				ChatType:    strPtr("group"),
				MessageType: strPtr("text"),
				Content:     strPtr(`{"text":"@_user_1 看下当前版本"}`),
				Mentions: []*larkim.MentionEvent{{
					Key:           strPtr("@_user_1"),
					Id:            &larkim.UserId{OpenId: strPtr("ou_bot_collie")},
					MentionedType: strPtr("bot"),
					Name:          strPtr("Collie Test Bot"),
				}},
			},
		},
	}
	if err := fa.handleMessageReceive(ctx, ownerGrpMsg, sink); err != nil {
		t.Fatal(err)
	}
	if len(sp.st.Entries) != 1 || sp.st.Entries[0].Role != "user" || sp.st.Entries[0].Text != "看下当前版本" {
		t.Fatalf("allowlisted owner group @Collie should keep normal behavior, entries=%+v", sp.st.Entries)
	}
	ff.waitForCall(t, func(c fakeFeishuCall) bool {
		return c.Method == http.MethodPost && c.Path == "/open-apis/im/v1/messages/om_owner_grp_1/reactions"
	})

	// Now a newer colleague feedback arrives AFTER owner's group message.
	fbAfterOwner := &larkim.P2MessageReceiveV1{
		EventV2Base: &larkevent.EventV2Base{Header: &larkevent.EventHeader{EventID: "evt_fb_after_owner"}},
		Event: &larkim.P2MessageReceiveV1Data{
			Sender: &larkim.EventSender{
				SenderType: strPtr("user"),
				SenderId:   &larkim.UserId{OpenId: strPtr("ou_colleague_named")},
			},
			Message: &larkim.EventMessage{
				MessageId:   strPtr("om_fb_4"),
				ChatId:      strPtr("oc_feedback_group"),
				ChatType:    strPtr("group"),
				MessageType: strPtr("text"),
				Content:     strPtr(`{"text":"@_user_1 导出按钮偶尔点不动"}`),
				Mentions: []*larkim.MentionEvent{{
					Key:           strPtr("@_user_1"),
					Id:            &larkim.UserId{OpenId: strPtr("ou_bot_collie")},
					MentionedType: strPtr("bot"),
					Name:          strPtr("Collie Test Bot"),
				}},
			},
		},
	}
	if err := fa.handleMessageReceive(ctx, fbAfterOwner, sink); err != nil {
		t.Fatal(err)
	}
	latestTrig := sp.st.Triggers[len(sp.st.Triggers)-1]

	// Collie replies quoting the feedback trigger with --opt buttons -> must route privately to owner DM!
	replyEntry, warn := sp.Reply(mainThread, "群里林同学反馈导出按钮偶尔点不动，要派 worker 排查吗？", latestTrig.ID, []string{"派 worker 排查", "先记下来"}, false)
	if warn != "" {
		t.Fatalf("unexpected reply warning for feedback trigger quote: %q", warn)
	}
	if replyEntry.Re == nil || replyEntry.Re.Who != "群反馈·林同学" || !strings.Contains(replyEntry.Re.Text, "导出按钮偶尔点不动") {
		t.Fatalf("unexpected feedback reply quote: %+v", replyEntry.Re)
	}
	dmCall := ff.waitForCall(t, func(c fakeFeishuCall) bool {
		return c.Method == http.MethodPost && c.Path == "/open-apis/im/v1/messages" && strings.Contains(extractCardContent(c.Body), "派 worker 排查")
	})
	if !strings.Contains(dmCall.Query, "receive_id_type=open_id") || !strings.Contains(dmCall.Body, `"receive_id":"ou_allowed_owner"`) {
		t.Fatalf("feedback reply must go to owner DM (open_id), got query=%q body=%s", dmCall.Query, dmCall.Body)
	}
	// Verify zero /reply calls were ever sent to the feedback group.
	ff.mu.Lock()
	for _, c := range ff.calls {
		if strings.HasSuffix(c.Path, "/reply") || strings.Contains(c.Body, `"receive_id":"oc_feedback_group"`) {
			ff.mu.Unlock()
			t.Fatalf("outbound message leaked into feedback group: %+v", c)
		}
	}
	ff.mu.Unlock()
}

func TestOpenSourceRepoZeroInternalTerms(t *testing.T) {
	// Guard against accidentally introducing internal tool names or internal corporate domains
	// into the public repository source files.
	forbidden := []string{
		"byte" + "dcli",
		"byte" + "dance.larkoffice.com",
		"code." + "byte" + "org.net",
		"byte" + "cloud",
	}
	roots := []string{"..", "../../web", "../../scripts"}
	for _, root := range roots {
		_ = filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
			if err != nil || d.IsDir() {
				return nil
			}
			ext := filepath.Ext(p)
			if ext != ".go" && ext != ".js" && ext != ".css" && ext != ".html" {
				return nil
			}
			raw, rerr := os.ReadFile(p)
			if rerr != nil {
				return nil
			}
			low := strings.ToLower(string(raw))
			for _, bad := range forbidden {
				if strings.Contains(low, bad) {
					t.Errorf("forbidden internal term %q found in %s", bad, p)
				}
			}
			return nil
		})
	}
}
