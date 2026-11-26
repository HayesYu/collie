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
	"strconv"
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

type fakeFeishuMessage struct {
	MessageID string
	MsgType   string
	Content   string
	Deleted   bool
	Mentions  []map[string]any
	Status    int
	Code      int
	Msg       string
	Delay     time.Duration
}

type fakeFeishuResource struct {
	Data     []byte
	FileName string
	Status   int
	Code     int
	Msg      string
	Delay    time.Duration
}

type fakeFeishuServer struct {
	srv       *httptest.Server
	mu        sync.Mutex
	calls     []fakeFeishuCall
	msgSeq    int
	imgData   []byte
	messages  map[string]fakeFeishuMessage
	resources map[string]fakeFeishuResource
}

func newFakeFeishuServer(t *testing.T) *fakeFeishuServer {
	t.Helper()
	ff := &fakeFeishuServer{
		imgData:   []byte("\x89PNG\r\n\x1a\nfake-png-bytes"),
		messages:  map[string]fakeFeishuMessage{},
		resources: map[string]fakeFeishuResource{},
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
			idx := strings.LastIndex(r.URL.Path, "/resources/")
			fileKey := r.URL.Path[idx+len("/resources/"):]
			ff.mu.Lock()
			res, hasCustom := ff.resources[fileKey]
			defaultData := ff.imgData
			ff.mu.Unlock()
			if hasCustom {
				if res.Delay > 0 {
					select {
					case <-time.After(res.Delay):
					case <-r.Context().Done():
						return
					}
				}
				if res.Code != 0 {
					if res.Status > 0 {
						w.WriteHeader(res.Status)
					}
					_ = json.NewEncoder(w).Encode(map[string]any{"code": res.Code, "msg": res.Msg})
					return
				}
				if res.Status >= 400 {
					w.WriteHeader(res.Status)
					_, _ = w.Write([]byte(`{"code":500,"msg":"resource error"}`))
					return
				}
				fname := res.FileName
				if fname == "" {
					fname = fileKey + ".png"
				}
				w.Header().Set("Content-Type", "image/png")
				w.Header().Set("Content-Disposition", `attachment; filename="`+fname+`"`)
				_, _ = w.Write(res.Data)
				return
			}
			w.Header().Set("Content-Type", "image/png")
			w.Header().Set("Content-Disposition", `attachment; filename="shot.png"`)
			_, _ = w.Write(defaultData)

		case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/open-apis/im/v1/messages/"):
			msgID := strings.TrimPrefix(r.URL.Path, "/open-apis/im/v1/messages/")
			ff.mu.Lock()
			msg, ok := ff.messages[msgID]
			ff.mu.Unlock()
			if !ok {
				w.WriteHeader(404)
				_, _ = w.Write([]byte(`{"code":230011,"msg":"message not found"}`))
				return
			}
			if msg.Delay > 0 {
				select {
				case <-time.After(msg.Delay):
				case <-r.Context().Done():
					return
				}
			}
			if msg.Code != 0 {
				if msg.Status > 0 {
					w.WriteHeader(msg.Status)
				}
				_ = json.NewEncoder(w).Encode(map[string]any{"code": msg.Code, "msg": msg.Msg})
				return
			}
			if msg.Status >= 400 {
				w.WriteHeader(msg.Status)
				_, _ = w.Write([]byte(`{"code":500,"msg":"internal error"}`))
				return
			}
			id := msg.MessageID
			if id == "" {
				id = msgID
			}
			item := map[string]any{
				"message_id": id,
				"msg_type":   msg.MsgType,
				"deleted":    msg.Deleted,
				"body":       map[string]any{"content": msg.Content},
			}
			if len(msg.Mentions) > 0 {
				item["mentions"] = msg.Mentions
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"code": 0,
				"msg":  "ok",
				"data": map[string]any{"items": []any{item}},
			})

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
	checkEntry, _ := sp.Reply(mainThread, "测试环境证书明天到期，请手动续签", "", nil, true)
	checkSendCall := ff.waitForCall(t, func(c fakeFeishuCall) bool {
		return c.Method == http.MethodPost && c.Path == "/open-apis/im/v1/messages" && strings.Contains(extractCardContent(c.Body), `"action":"ack"`)
	})
	checkCardRaw := extractCardContent(checkSendCall.Body)
	if !strings.Contains(checkCardRaw, `"content":"已处理"`) || strings.Contains(checkCardRaw, `"content":"已看"`) {
		t.Fatalf("outbound --check card must label button 已处理 (not 已看): %s", checkCardRaw)
	}
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
	if !strings.Contains(patchRaw, "已在网页端标记已处理") || !strings.Contains(patchRaw, `"template":"grey"`) {
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

func TestFeishuReadCursorAckAndUnquotedReplySync(t *testing.T) {
	_, sp, fa, ff, sink := setupFeishuTestHarness(t)
	ctx := context.Background()

	waitMapping := func(entryID string) string {
		t.Helper()
		deadline := time.Now().Add(time.Second)
		for time.Now().Before(deadline) {
			if rec, _ := getIntegrationMessage(sp.db, entryID, feishuKind); rec != nil && rec.ExternalID != "" {
				return rec.ExternalID
			}
			time.Sleep(10 * time.Millisecond)
		}
		t.Fatalf("integration_messages mapping missing for %s", entryID)
		return ""
	}
	sendP2P := func(eventID, msgID, text string) {
		t.Helper()
		body, _ := json.Marshal(map[string]string{"text": text})
		ev := &larkim.P2MessageReceiveV1{
			EventV2Base: &larkevent.EventV2Base{Header: &larkevent.EventHeader{EventID: eventID}},
			Event: &larkim.P2MessageReceiveV1Data{
				Sender: &larkim.EventSender{
					SenderType: strPtr("user"),
					SenderId:   &larkim.UserId{OpenId: strPtr("ou_allowed_owner")},
				},
				Message: &larkim.EventMessage{
					MessageId:   strPtr(msgID),
					ChatId:      strPtr("oc_p2p_owner"),
					ChatType:    strPtr("p2p"),
					MessageType: strPtr("text"),
					Content:     strPtr(string(body)),
				},
			},
		}
		if err := fa.handleMessageReceive(ctx, ev, sink); err != nil {
			t.Fatalf("handleMessageReceive(%s): %v", eventID, err)
		}
	}

	// 1) Feishu p2p message advances chat:main read cursor monotonically and persists to SQLite.
	sendP2P("evt_rc_1", "om_rc_1", "帮我看下发布状态")
	sp.mu.Lock()
	u1 := sp.st.Entries[len(sp.st.Entries)-1]
	sp.mu.Unlock()
	if got := sp.LastReadEntryID(); got != u1.ID {
		t.Fatalf("LastReadEntryID after Feishu p2p = %q, want %q", got, u1.ID)
	}
	reloaded, err := loadState(sp.db, sp.dir)
	if err != nil || reloaded.LastReadEntryID != u1.ID {
		t.Fatalf("persisted LastReadEntryID = %q (err=%v), want %q", reloaded.LastReadEntryID, err, u1.ID)
	}

	// 2) Feishu card ack click: unified「已标记处理」copy, clears pending, advances read cursor monotonically.
	chkOld, _ := sp.Reply(mainThread, "请手动审批旧工单 #1", "", nil, true)
	chkNew, _ := sp.Reply(mainThread, "请手动审批新工单 #2", "", nil, true)
	waitMapping(chkOld.ID)
	waitMapping(chkNew.ID)

	ackRespNew, err := fa.handleCardAction(ctx, &larkcallback.CardActionTriggerEvent{
		EventV2Base: &larkevent.EventV2Base{Header: &larkevent.EventHeader{EventID: "evt_ack_new"}},
		Event: &larkcallback.CardActionTriggerRequest{
			Operator: &larkcallback.Operator{OpenID: "ou_allowed_owner"},
			Context:  &larkcallback.Context{OpenChatID: "oc_p2p_owner", OpenMessageID: "om_chk_new"},
			Action:   &larkcallback.CallBackAction{Value: map[string]any{"action": "ack", "entry_id": chkNew.ID}},
		},
	}, sink)
	if err != nil {
		t.Fatalf("ack chkNew: %v", err)
	}
	if ackRespNew.Toast == nil || ackRespNew.Toast.Content != "已标记处理" {
		t.Fatalf("ack toast = %+v, want 已标记处理", ackRespNew.Toast)
	}
	ackCardRaw, _ := json.Marshal(ackRespNew.Card.Data)
	if !strings.Contains(string(ackCardRaw), "已标记处理") || strings.Contains(string(ackCardRaw), "已看") {
		t.Fatalf("ack updated card unexpected: %s", string(ackCardRaw))
	}
	if got := sp.LastReadEntryID(); got != chkNew.ID {
		t.Fatalf("LastReadEntryID after ack chkNew = %q, want %q", got, chkNew.ID)
	}

	// Clicking an older ack card must NOT regress LastReadEntryID backward.
	if _, err := fa.handleCardAction(ctx, &larkcallback.CardActionTriggerEvent{
		EventV2Base: &larkevent.EventV2Base{Header: &larkevent.EventHeader{EventID: "evt_ack_old"}},
		Event: &larkcallback.CardActionTriggerRequest{
			Operator: &larkcallback.Operator{OpenID: "ou_allowed_owner"},
			Context:  &larkcallback.Context{OpenChatID: "oc_p2p_owner", OpenMessageID: "om_chk_old"},
			Action:   &larkcallback.CallBackAction{Value: map[string]any{"action": "ack", "entry_id": chkOld.ID}},
		},
	}, sink); err != nil {
		t.Fatalf("ack chkOld: %v", err)
	}
	if got := sp.LastReadEntryID(); got != chkNew.ID {
		t.Fatalf("LastReadEntryID regressed on older ack: got %q, want %q", got, chkNew.ID)
	}

	// 3) Feishu pick_opt callback advances read cursor to the new user choice entry.
	optCard, _ := sp.Reply(mainThread, "要现在切流量吗？", "", []string{"切 10%", "先观望"}, false)
	waitMapping(optCard.ID)
	if _, err := fa.handleCardAction(ctx, &larkcallback.CardActionTriggerEvent{
		EventV2Base: &larkevent.EventV2Base{Header: &larkevent.EventHeader{EventID: "evt_opt_1"}},
		Event: &larkcallback.CardActionTriggerRequest{
			Operator: &larkcallback.Operator{OpenID: "ou_allowed_owner"},
			Context:  &larkcallback.Context{OpenChatID: "oc_p2p_owner", OpenMessageID: "om_opt_1"},
			Action:   &larkcallback.CallBackAction{Value: map[string]any{"action": "pick_opt", "entry_id": optCard.ID, "opt_idx": "0"}},
		},
	}, sink); err != nil {
		t.Fatalf("pick_opt: %v", err)
	}
	sp.mu.Lock()
	optUserEntry := sp.st.Entries[len(sp.st.Entries)-1]
	sp.mu.Unlock()
	if got := sp.LastReadEntryID(); got != optUserEntry.ID || idNum(got) <= idNum(optCard.ID) {
		t.Fatalf("LastReadEntryID after pick_opt = %q, want %q (> %q)", got, optUserEntry.ID, optCard.ID)
	}

	// 4) Unquoted Feishu text reply right after an --opt card resolves it AND gray-patches the card.
	optDirect, _ := sp.Reply(mainThread, "日志级别要调回 info 吗？", "", []string{"调回 info", "保持 debug"}, false)
	optDirectMsgID := waitMapping(optDirect.ID)
	sendP2P("evt_rc_direct_opt", "om_rc_direct_opt", "调回 info 就行")
	patchOpt := ff.waitForCall(t, func(c fakeFeishuCall) bool {
		return c.Method == http.MethodPatch && c.Path == "/open-apis/im/v1/messages/"+optDirectMsgID
	})
	if raw := extractCardContent(patchOpt.Body); !strings.Contains(raw, "已回复：调回 info 就行") || !strings.Contains(raw, `"template":"grey"`) {
		t.Fatalf("expected unquoted Feishu reply to gray-patch --opt card, got: %s", raw)
	}

	// 5) Unquoted Feishu text reply right after a pure --check card must NOT clear pending
	// or gray-patch the card, while still advancing LastReadEntryID.
	chkGuard, _ := sp.Reply(mainThread, "请手动给测试集群续签证书（办完点已处理）", "", nil, true)
	chkGuardMsgID := waitMapping(chkGuard.ID)
	sendP2P("evt_rc_unrelated", "om_rc_unrelated", "顺便帮我查一下今天构建数")
	sp.mu.Lock()
	uUnrelated := sp.st.Entries[len(sp.st.Entries)-1]
	pendNow := sp.pendingLocked(time.Now())
	sp.mu.Unlock()
	if got := sp.LastReadEntryID(); got != uUnrelated.ID {
		t.Fatalf("LastReadEntryID after unrelated Feishu msg = %q, want %q", got, uUnrelated.ID)
	}
	if len(pendNow) != 1 || pendNow[0].ID != chkGuard.ID {
		t.Fatalf("unrelated Feishu text must keep pure --check pending, got %+v", pendNow)
	}
	time.Sleep(80 * time.Millisecond)
	ff.mu.Lock()
	for _, c := range ff.calls {
		if c.Method == http.MethodPatch && c.Path == "/open-apis/im/v1/messages/"+chkGuardMsgID {
			ff.mu.Unlock()
			t.Fatalf("unrelated Feishu text must NOT gray-patch pure --check card: %s", extractCardContent(c.Body))
		}
	}
	ff.mu.Unlock()
}

func TestFeishuQuotedMessageTextImagePostAndFeedback(t *testing.T) {
	_, sp, fa, ff, sink := setupFeishuTestHarness(t)
	sp.integrations.humanBurst = 20
	ctx := context.Background()

	// Configure a feedback group too so we verify all three inbound paths:
	// p2p, owner group @Collie, and monitored feedback group @Collie.
	cfgJSON, _ := json.Marshal(FeishuConfig{
		Region:           "cn",
		AppID:            "cli_test_app",
		AllowOpenIDs:     []string{"ou_allowed_owner"},
		FeedbackChatIDs:  []string{"oc_feedback_group"},
		GroupMentionOnly: true,
		InstantChatSync:  true,
	})
	fa.mu.Lock()
	fa.activeCfg = parseFeishuConfig(cfgJSON)
	fa.mu.Unlock()

	png1 := []byte("\x89PNG\r\n\x1a\nquoted-image-one")
	png2 := []byte("\x89PNG\r\n\x1a\nquoted-image-two")
	png3 := []byte("\x89PNG\r\n\x1a\nquoted-image-three")
	png4 := []byte("\x89PNG\r\n\x1a\nquoted-image-four")
	png5 := []byte("\x89PNG\r\n\x1a\nquoted-image-five")
	pngMain := []byte("\x89PNG\r\n\x1a\nmain-image-bytes")

	ff.mu.Lock()
	ff.messages["om_parent_text"] = fakeFeishuMessage{
		MessageID: "om_parent_text",
		MsgType:   "text",
		Content:   `{"text":"@_user_1 昨晚发布后 QPS 掉了一半，日志见集群 A"}`,
		Mentions: []map[string]any{
			{"key": "@_user_1", "name": "林同学"},
		},
	}
	ff.messages["om_parent_img"] = fakeFeishuMessage{
		MessageID: "om_parent_img",
		MsgType:   "image",
		Content:   `{"image_key":"img_parent_1"}`,
	}
	ff.messages["om_parent_post"] = fakeFeishuMessage{
		MessageID: "om_parent_post",
		MsgType:   "post",
		Content:   `{"zh_cn":{"title":"巡检汇总","content":[[{"tag":"text","text":"发现两处异常"},{"tag":"img","image_key":"img_post_1"}],[{"tag":"a","text":"监控链接","href":"https://example.com/dash"},{"tag":"img","image_key":"img_post_2"}]]}}`,
	}
	ff.messages["om_parent_many_imgs"] = fakeFeishuMessage{
		MessageID: "om_parent_many_imgs",
		MsgType:   "post",
		Content:   `{"title":"多图报告","content":[[{"tag":"img","image_key":"img_cap_1"},{"tag":"img","image_key":"img_cap_2"},{"tag":"img","image_key":"img_cap_3"},{"tag":"img","image_key":"img_cap_4"},{"tag":"img","image_key":"img_cap_5"},{"tag":"img","image_key":"img_cap_1"}]]}`,
	}
	ff.messages["om_parent_deleted"] = fakeFeishuMessage{
		MessageID: "om_parent_deleted",
		MsgType:   "text",
		Deleted:   true,
		Content:   `{"text":"should not leak"}`,
	}
	ff.resources["img_parent_1"] = fakeFeishuResource{Data: png1, FileName: "err-shot.png"}
	ff.resources["img_post_1"] = fakeFeishuResource{Data: png1, FileName: "p1.png"}
	ff.resources["img_post_2"] = fakeFeishuResource{Data: png2, FileName: "p2.png"}
	ff.resources["img_cap_1"] = fakeFeishuResource{Data: png1, FileName: "c1.png"}
	ff.resources["img_cap_2"] = fakeFeishuResource{Data: png2, FileName: "c2.png"}
	ff.resources["img_cap_3"] = fakeFeishuResource{Data: png3, FileName: "c3.png"}
	ff.resources["img_cap_4"] = fakeFeishuResource{Data: png4, FileName: "c4.png"}
	ff.resources["img_cap_5"] = fakeFeishuResource{Data: png5, FileName: "c5.png"}
	ff.resources["img_main_1"] = fakeFeishuResource{Data: pngMain, FileName: "main.png"}
	ff.mu.Unlock()

	// 1) Quoted plain text in p2p: mention placeholder resolved + quoted block prepended.
	evText := &larkim.P2MessageReceiveV1{
		EventV2Base: &larkevent.EventV2Base{Header: &larkevent.EventHeader{EventID: "evt_q_text"}},
		Event: &larkim.P2MessageReceiveV1Data{
			Sender: &larkim.EventSender{
				SenderType: strPtr("user"),
				SenderId:   &larkim.UserId{OpenId: strPtr("ou_allowed_owner")},
			},
			Message: &larkim.EventMessage{
				MessageId:   strPtr("om_child_1"),
				ParentId:    strPtr("om_parent_text"),
				ChatId:      strPtr("oc_p2p_owner"),
				ChatType:    strPtr("p2p"),
				MessageType: strPtr("text"),
				Content:     strPtr(`{"text":"帮我先查下是不是配置变更导致的"}`),
			},
		},
	}
	if err := fa.handleMessageReceive(ctx, evText, sink); err != nil {
		t.Fatalf("quoted text: %v", err)
	}
	got1 := sp.st.Entries[len(sp.st.Entries)-1].Text
	for _, want := range []string{"[引用消息]", "> @林同学 昨晚发布后 QPS 掉了一半，日志见集群 A", "帮我先查下是不是配置变更导致的"} {
		if !strings.Contains(got1, want) {
			t.Fatalf("quoted text entry missing %q:\n%s", want, got1)
		}
	}
	if strings.Contains(got1, "@_user_1") {
		t.Fatalf("raw mention key leaked in quoted text:\n%s", got1)
	}

	// 2) Quoted image in owner group @Collie: fetches resource using parent_id and attaches path.
	evImg := &larkim.P2MessageReceiveV1{
		EventV2Base: &larkevent.EventV2Base{Header: &larkevent.EventHeader{EventID: "evt_q_img"}},
		Event: &larkim.P2MessageReceiveV1Data{
			Sender: &larkim.EventSender{
				SenderType: strPtr("user"),
				SenderId:   &larkim.UserId{OpenId: strPtr("ou_allowed_owner")},
			},
			Message: &larkim.EventMessage{
				MessageId:   strPtr("om_child_2"),
				ParentId:    strPtr("om_parent_img"),
				ChatId:      strPtr("oc_owner_group"),
				ChatType:    strPtr("group"),
				MessageType: strPtr("text"),
				Content:     strPtr(`{"text":"@_user_1 看下这张报错截图"}`),
				Mentions: []*larkim.MentionEvent{{
					Key:           strPtr("@_user_1"),
					Id:            &larkim.UserId{OpenId: strPtr("ou_bot_collie")},
					MentionedType: strPtr("bot"),
					Name:          strPtr("Collie Test Bot"),
				}},
			},
		},
	}
	if err := fa.handleMessageReceive(ctx, evImg, sink); err != nil {
		t.Fatalf("quoted image: %v", err)
	}
	got2 := sp.st.Entries[len(sp.st.Entries)-1].Text
	if !strings.Contains(got2, "[引用图片]") || !strings.Contains(got2, "看下这张报错截图") {
		t.Fatalf("quoted image missing context:\n%s", got2)
	}
	attIdx := strings.LastIndex(got2, "附件：\n")
	if attIdx < 0 {
		t.Fatalf("quoted image missing 附件： block:\n%s", got2)
	}
	savedPath := strings.TrimSpace(got2[attIdx+len("附件：\n"):])
	if !strings.Contains(savedPath, filepath.Join("uploads", time.Now().Format("20060102"))) {
		t.Fatalf("quoted image saved in unexpected dir: %q", savedPath)
	}
	rawImg, err := os.ReadFile(savedPath)
	if err != nil || !bytes.Equal(rawImg, png1) {
		t.Fatalf("quoted image bytes mismatch: err=%v got=%q", err, string(rawImg))
	}
	// Verify MessageResource.Get targeted parent_id (om_parent_img), NOT child message_id.
	ff.mu.Lock()
	foundParentResCall := false
	for _, c := range ff.calls {
		if c.Method == http.MethodGet && c.Path == "/open-apis/im/v1/messages/om_parent_img/resources/img_parent_1" {
			foundParentResCall = true
		}
	}
	ff.mu.Unlock()
	if !foundParentResCall {
		t.Fatal("expected MessageResource.Get to use parent_id om_parent_img")
	}

	// 3) Quoted rich-text post with locale wrapper (zh_cn) + 2 images, while current message ALSO has an image:
	//    all image paths must merge into ONE trailing 附件：\n block.
	evPost := &larkim.P2MessageReceiveV1{
		EventV2Base: &larkevent.EventV2Base{Header: &larkevent.EventHeader{EventID: "evt_q_post"}},
		Event: &larkim.P2MessageReceiveV1Data{
			Sender: &larkim.EventSender{
				SenderType: strPtr("user"),
				SenderId:   &larkim.UserId{OpenId: strPtr("ou_allowed_owner")},
			},
			Message: &larkim.EventMessage{
				MessageId:   strPtr("om_child_3"),
				ParentId:    strPtr("om_parent_post"),
				ChatId:      strPtr("oc_p2p_owner"),
				ChatType:    strPtr("p2p"),
				MessageType: strPtr("image"),
				Content:     strPtr(`{"image_key":"img_main_1"}`),
			},
		},
	}
	if err := fa.handleMessageReceive(ctx, evPost, sink); err != nil {
		t.Fatalf("quoted post: %v", err)
	}
	got3 := sp.st.Entries[len(sp.st.Entries)-1].Text
	for _, want := range []string{"[引用消息（含 2 张图片）]", "> 巡检汇总", "> 发现两处异常", "[监控链接](https://example.com/dash)"} {
		if !strings.Contains(got3, want) {
			t.Fatalf("quoted post missing %q:\n%s", want, got3)
		}
	}
	if strings.Count(got3, "附件：\n") != 1 {
		t.Fatalf("expected single trailing 附件： block, got:\n%s", got3)
	}
	paths3 := strings.Split(strings.TrimSpace(got3[strings.LastIndex(got3, "附件：\n")+len("附件：\n"):]), "\n")
	if len(paths3) != 3 {
		t.Fatalf("expected 3 merged attachment paths (2 quoted + 1 main), got %d: %v", len(paths3), paths3)
	}
	for i, wantBytes := range [][]byte{png1, png2, pngMain} {
		b, rerr := os.ReadFile(paths3[i])
		if rerr != nil || !bytes.Equal(b, wantBytes) {
			t.Fatalf("attachment[%d] (%s) mismatch: err=%v", i, paths3[i], rerr)
		}
	}

	// 4) Max 4 images cap: post with 5 unique (+1 dup) images downloads only first 4 and notes 1 omitted.
	evCap := &larkim.P2MessageReceiveV1{
		EventV2Base: &larkevent.EventV2Base{Header: &larkevent.EventHeader{EventID: "evt_q_cap"}},
		Event: &larkim.P2MessageReceiveV1Data{
			Sender: &larkim.EventSender{
				SenderType: strPtr("user"),
				SenderId:   &larkim.UserId{OpenId: strPtr("ou_allowed_owner")},
			},
			Message: &larkim.EventMessage{
				MessageId:   strPtr("om_child_4"),
				ParentId:    strPtr("om_parent_many_imgs"),
				ChatId:      strPtr("oc_p2p_owner"),
				ChatType:    strPtr("p2p"),
				MessageType: strPtr("text"),
				Content:     strPtr(`{"text":"全看一遍"}`),
			},
		},
	}
	if err := fa.handleMessageReceive(ctx, evCap, sink); err != nil {
		t.Fatalf("quoted max images: %v", err)
	}
	got4 := sp.st.Entries[len(sp.st.Entries)-1].Text
	if !strings.Contains(got4, "含 4 张图片") || !strings.Contains(got4, "另略去 1 张") {
		t.Fatalf("expected max-4 image cap notice, got:\n%s", got4)
	}
	paths4 := strings.Split(strings.TrimSpace(got4[strings.LastIndex(got4, "附件：\n")+len("附件：\n"):]), "\n")
	if len(paths4) != 4 {
		t.Fatalf("expected exactly 4 saved images, got %d: %v", len(paths4), paths4)
	}
	ff.mu.Lock()
	for _, c := range ff.calls {
		if strings.HasSuffix(c.Path, "/resources/img_cap_5") {
			ff.mu.Unlock()
			t.Fatal("5th unique quoted image should not have been downloaded")
		}
	}
	ff.mu.Unlock()

	// 5) Monitored feedback group: colleague @Collie quoting a screenshot passes quoted context + image path
	//    into read-only feedback trigger without creating fake owner entry or replying to group.
	entriesBeforeFB := len(sp.st.Entries)
	evFB := &larkim.P2MessageReceiveV1{
		EventV2Base: &larkevent.EventV2Base{Header: &larkevent.EventHeader{EventID: "evt_q_fb"}},
		Event: &larkim.P2MessageReceiveV1Data{
			Sender: &larkim.EventSender{
				SenderType: strPtr("user"),
				SenderId:   &larkim.UserId{OpenId: strPtr("ou_colleague_named")},
			},
			Message: &larkim.EventMessage{
				MessageId:   strPtr("om_fb_quote"),
				ParentId:    strPtr("om_parent_img"),
				ChatId:      strPtr("oc_feedback_group"),
				ChatType:    strPtr("group"),
				MessageType: strPtr("text"),
				Content:     strPtr(`{"text":"@_user_1 暗色模式下这里文字重叠了"}`),
				Mentions: []*larkim.MentionEvent{{
					Key:           strPtr("@_user_1"),
					Id:            &larkim.UserId{OpenId: strPtr("ou_bot_collie")},
					MentionedType: strPtr("bot"),
					Name:          strPtr("Collie Test Bot"),
				}},
			},
		},
	}
	if err := fa.handleMessageReceive(ctx, evFB, sink); err != nil {
		t.Fatalf("feedback quoted image: %v", err)
	}
	if len(sp.st.Entries) != entriesBeforeFB {
		t.Fatal("feedback group quoted message must not create owner user entry")
	}
	fbTrig := sp.st.Triggers[len(sp.st.Triggers)-1]
	for _, want := range []string{feishuGroupFeedbackTag, "[引用图片]", "暗色模式下这里文字重叠了", "附件：\n"} {
		if !strings.Contains(fbTrig.Text, want) {
			t.Fatalf("feedback quoted trigger missing %q:\n%s", want, fbTrig.Text)
		}
	}

	// 6) Recalled/deleted parent message shows clean placeholder without leaking content.
	evDel := &larkim.P2MessageReceiveV1{
		EventV2Base: &larkevent.EventV2Base{Header: &larkevent.EventHeader{EventID: "evt_q_del"}},
		Event: &larkim.P2MessageReceiveV1Data{
			Sender: &larkim.EventSender{
				SenderType: strPtr("user"),
				SenderId:   &larkim.UserId{OpenId: strPtr("ou_allowed_owner")},
			},
			Message: &larkim.EventMessage{
				MessageId:   strPtr("om_child_del"),
				ParentId:    strPtr("om_parent_deleted"),
				ChatId:      strPtr("oc_p2p_owner"),
				ChatType:    strPtr("p2p"),
				MessageType: strPtr("text"),
				Content:     strPtr(`{"text":"忽略刚才那条"}`),
			},
		},
	}
	if err := fa.handleMessageReceive(ctx, evDel, sink); err != nil {
		t.Fatalf("quoted deleted: %v", err)
	}
	gotDel := sp.st.Entries[len(sp.st.Entries)-1].Text
	if !strings.Contains(gotDel, "[原消息已撤回]") || strings.Contains(gotDel, "should not leak") {
		t.Fatalf("unexpected deleted parent formatting:\n%s", gotDel)
	}
}

func TestFeishuQuotedMessageFailureDegradationAndLimits(t *testing.T) {
	_, sp, fa, ff, sink := setupFeishuTestHarness(t)
	sp.integrations.humanBurst = 20
	ctx := context.Background()

	origFetchTimeout := feishuQuoteFetchTimeout
	origImgTimeout := feishuQuoteImageTimeout
	origTotalTimeout := feishuQuoteTotalTimeout
	origMaxBytes := feishuQuoteMaxImageBytes
	t.Cleanup(func() {
		feishuQuoteFetchTimeout = origFetchTimeout
		feishuQuoteImageTimeout = origImgTimeout
		feishuQuoteTotalTimeout = origTotalTimeout
		feishuQuoteMaxImageBytes = origMaxBytes
	})

	ff.mu.Lock()
	ff.messages["om_parent_err"] = fakeFeishuMessage{
		MessageID: "om_parent_err",
		Code:      99991672,
		Msg:       "no permission to read message",
	}
	ff.messages["om_parent_slow"] = fakeFeishuMessage{
		MessageID: "om_parent_slow",
		MsgType:   "text",
		Content:   `{"text":"too slow"}`,
		Delay:     400 * time.Millisecond,
	}
	ff.messages["om_parent_img_fail"] = fakeFeishuMessage{
		MessageID: "om_parent_img_fail",
		MsgType:   "image",
		Content:   `{"image_key":"img_broken"}`,
	}
	ff.messages["om_parent_img_big"] = fakeFeishuMessage{
		MessageID: "om_parent_img_big",
		MsgType:   "post",
		Content:   `{"title":"含超大图","content":[[{"tag":"img","image_key":"img_oversized"},{"tag":"img","image_key":"img_small_ok"}]]}`,
	}
	ff.resources["img_broken"] = fakeFeishuResource{Code: 234001, Msg: "resource expired"}
	ff.resources["img_oversized"] = fakeFeishuResource{Data: bytes.Repeat([]byte("X"), 128), FileName: "huge.png"}
	ff.resources["img_small_ok"] = fakeFeishuResource{Data: []byte("tiny-png"), FileName: "ok.png"}
	ff.mu.Unlock()

	// 1) im.v1.message.get permission error degrades cleanly without blocking main message.
	evErr := &larkim.P2MessageReceiveV1{
		EventV2Base: &larkevent.EventV2Base{Header: &larkevent.EventHeader{EventID: "evt_deg_err"}},
		Event: &larkim.P2MessageReceiveV1Data{
			Sender: &larkim.EventSender{
				SenderType: strPtr("user"),
				SenderId:   &larkim.UserId{OpenId: strPtr("ou_allowed_owner")},
			},
			Message: &larkim.EventMessage{
				MessageId:   strPtr("om_deg_1"),
				ParentId:    strPtr("om_parent_err"),
				ChatId:      strPtr("oc_p2p_owner"),
				ChatType:    strPtr("p2p"),
				MessageType: strPtr("text"),
				Content:     strPtr(`{"text":"拉取父消息失败也要收到我"}`),
			},
		},
	}
	if err := fa.handleMessageReceive(ctx, evErr, sink); err != nil {
		t.Fatalf("degraded parent error failed: %v", err)
	}
	if got := sp.st.Entries[len(sp.st.Entries)-1].Text; got != "拉取父消息失败也要收到我" {
		t.Fatalf("expected clean main text on parent API error, got %q", got)
	}

	// 2) im.v1.message.get timeout degrades quickly without blocking main message.
	feishuQuoteFetchTimeout = 60 * time.Millisecond
	start := time.Now()
	evSlow := &larkim.P2MessageReceiveV1{
		EventV2Base: &larkevent.EventV2Base{Header: &larkevent.EventHeader{EventID: "evt_deg_slow"}},
		Event: &larkim.P2MessageReceiveV1Data{
			Sender: &larkim.EventSender{
				SenderType: strPtr("user"),
				SenderId:   &larkim.UserId{OpenId: strPtr("ou_allowed_owner")},
			},
			Message: &larkim.EventMessage{
				MessageId:   strPtr("om_deg_2"),
				ParentId:    strPtr("om_parent_slow"),
				ChatId:      strPtr("oc_p2p_owner"),
				ChatType:    strPtr("p2p"),
				MessageType: strPtr("text"),
				Content:     strPtr(`{"text":"超时也不能卡死主消息"}`),
			},
		},
	}
	if err := fa.handleMessageReceive(ctx, evSlow, sink); err != nil {
		t.Fatalf("degraded timeout failed: %v", err)
	}
	if elapsed := time.Since(start); elapsed > 300*time.Millisecond {
		t.Fatalf("quote fetch timeout took too long: %v", elapsed)
	}
	if got := sp.st.Entries[len(sp.st.Entries)-1].Text; got != "超时也不能卡死主消息" {
		t.Fatalf("expected clean main text on timeout, got %q", got)
	}
	feishuQuoteFetchTimeout = origFetchTimeout

	// 3) Parent image download fails: degrades gracefully with fallback label and delivers main message.
	evImgFail := &larkim.P2MessageReceiveV1{
		EventV2Base: &larkevent.EventV2Base{Header: &larkevent.EventHeader{EventID: "evt_deg_img"}},
		Event: &larkim.P2MessageReceiveV1Data{
			Sender: &larkim.EventSender{
				SenderType: strPtr("user"),
				SenderId:   &larkim.UserId{OpenId: strPtr("ou_allowed_owner")},
			},
			Message: &larkim.EventMessage{
				MessageId:   strPtr("om_deg_3"),
				ParentId:    strPtr("om_parent_img_fail"),
				ChatId:      strPtr("oc_p2p_owner"),
				ChatType:    strPtr("p2p"),
				MessageType: strPtr("text"),
				Content:     strPtr(`{"text":"这张图过期了但文字得送到"}`),
			},
		},
	}
	if err := fa.handleMessageReceive(ctx, evImgFail, sink); err != nil {
		t.Fatalf("degraded image download failed: %v", err)
	}
	gotImgFail := sp.st.Entries[len(sp.st.Entries)-1].Text
	if !strings.Contains(gotImgFail, "[引用图片（下载失败）]") || !strings.Contains(gotImgFail, "这张图过期了但文字得送到") {
		t.Fatalf("unexpected degraded image message:\n%s", gotImgFail)
	}

	// 4) Single-image size limit enforcement: oversized image rejected & deleted, sibling image & main text delivered.
	feishuQuoteMaxImageBytes = 64
	evBig := &larkim.P2MessageReceiveV1{
		EventV2Base: &larkevent.EventV2Base{Header: &larkevent.EventHeader{EventID: "evt_deg_big"}},
		Event: &larkim.P2MessageReceiveV1Data{
			Sender: &larkim.EventSender{
				SenderType: strPtr("user"),
				SenderId:   &larkim.UserId{OpenId: strPtr("ou_allowed_owner")},
			},
			Message: &larkim.EventMessage{
				MessageId:   strPtr("om_deg_4"),
				ParentId:    strPtr("om_parent_img_big"),
				ChatId:      strPtr("oc_p2p_owner"),
				ChatType:    strPtr("p2p"),
				MessageType: strPtr("text"),
				Content:     strPtr(`{"text":"看下正常那张图"}`),
			},
		},
	}
	if err := fa.handleMessageReceive(ctx, evBig, sink); err != nil {
		t.Fatalf("size-limited quoted image failed: %v", err)
	}
	gotBig := sp.st.Entries[len(sp.st.Entries)-1].Text
	if !strings.Contains(gotBig, "含 1 张图片") || !strings.Contains(gotBig, "1 张图片下载失败") || !strings.Contains(gotBig, "看下正常那张图") {
		t.Fatalf("unexpected size-limited message:\n%s", gotBig)
	}
	savedOK := strings.TrimSpace(gotBig[strings.LastIndex(gotBig, "附件：\n")+len("附件：\n"):])
	if b, err := os.ReadFile(savedOK); err != nil || string(b) != "tiny-png" {
		t.Fatalf("expected only tiny-png saved, got %q err=%v", string(b), err)
	}

	// 5) Unauthorized sender quoting a parent message must never trigger im.v1.message.get.
	ff.mu.Lock()
	callsBeforeUnauth := len(ff.calls)
	ff.mu.Unlock()
	evUnauth := &larkim.P2MessageReceiveV1{
		EventV2Base: &larkevent.EventV2Base{Header: &larkevent.EventHeader{EventID: "evt_deg_unauth"}},
		Event: &larkim.P2MessageReceiveV1Data{
			Sender: &larkim.EventSender{
				SenderType: strPtr("user"),
				SenderId:   &larkim.UserId{OpenId: strPtr("ou_stranger")},
			},
			Message: &larkim.EventMessage{
				MessageId:   strPtr("om_deg_5"),
				ParentId:    strPtr("om_parent_err"),
				ChatId:      strPtr("oc_p2p_stranger"),
				ChatType:    strPtr("p2p"),
				MessageType: strPtr("text"),
				Content:     strPtr(`{"text":"probe"}`),
			},
		},
	}
	if err := fa.handleMessageReceive(ctx, evUnauth, sink); err != nil {
		t.Fatalf("unauthorized quote failed: %v", err)
	}
	ff.mu.Lock()
	callsAfterUnauth := len(ff.calls)
	ff.mu.Unlock()
	if callsAfterUnauth != callsBeforeUnauth {
		t.Fatalf("unauthorized sender triggered %d Feishu API calls", callsAfterUnauth-callsBeforeUnauth)
	}
}

func TestFeishuQuotedInteractiveCardsAndOwnCardRecovery(t *testing.T) {
	_, sp, fa, ff, sink := setupFeishuTestHarness(t)
	sp.integrations.humanBurst = 30
	ctx := context.Background()

	// 1) Collie sends its own Card JSON 2.0 via SendReply; Feishu im.v1.message.get
	//    returns the downgraded client-upgrade placeholder + non-downloadable system icon.
	//    Quoting that card must recover Collie's real original text without bogus image errors.
	replyEntry, _ := sp.Reply(mainThread, "已完成接口改造，准备切 v0.2.79 发布。", "", nil)
	sp.integrations.SendReply(ctx, replyEntry, "http://127.0.0.1:7788/#/collie")

	rec, err := getIntegrationMessage(sp.db, replyEntry.ID, feishuKind)
	if err != nil || rec == nil || rec.ExternalID == "" {
		t.Fatalf("expected persisted outbound card mapping, got rec=%+v err=%v", rec, err)
	}
	ownCardMsgID := rec.ExternalID

	ff.mu.Lock()
	ff.messages[ownCardMsgID] = fakeFeishuMessage{
		MessageID: ownCardMsgID,
		MsgType:   "interactive",
		Content:   `{"title":"Collie","elements":[[{"tag":"img","image_key":"img_v3_02ad_system_placeholder"},{"tag":"text","text":"请升级至最新版本客户端，以查看内容"}]]}`,
	}
	ff.resources["img_v3_02ad_system_placeholder"] = fakeFeishuResource{Code: 14005, Msg: "Resource Has Been Deleted"}
	callsBeforeOwnQuote := len(ff.calls)
	ff.mu.Unlock()

	sendQuote := func(evID, msgID, parentID, text string) string {
		t.Helper()
		ev := &larkim.P2MessageReceiveV1{
			EventV2Base: &larkevent.EventV2Base{Header: &larkevent.EventHeader{EventID: evID}},
			Event: &larkim.P2MessageReceiveV1Data{
				Sender: &larkim.EventSender{
					SenderType: strPtr("user"),
					SenderId:   &larkim.UserId{OpenId: strPtr("ou_allowed_owner")},
				},
				Message: &larkim.EventMessage{
					MessageId:   strPtr(msgID),
					ParentId:    strPtr(parentID),
					ChatId:      strPtr("oc_p2p_owner"),
					ChatType:    strPtr("p2p"),
					MessageType: strPtr("text"),
					Content:     strPtr(`{"text":` + strconv.Quote(text) + `}`),
				},
			},
		}
		if err := fa.handleMessageReceive(ctx, ev, sink); err != nil {
			t.Fatalf("handleMessageReceive(%s): %v", evID, err)
		}
		return sp.st.Entries[len(sp.st.Entries)-1].Text
	}

	gotOwn := sendQuote("evt_q_own_1", "om_q_own_1", ownCardMsgID, "顺便在卡片底部加一个官网链接")
	if !strings.Contains(gotOwn, "已完成接口改造，准备切 v0.2.79 发布。") || !strings.Contains(gotOwn, "顺便在卡片底部加一个官网链接") {
		t.Fatalf("quoted own card missing real text:\n%s", gotOwn)
	}
	for _, bad := range []string{"请升级至最新版本客户端", "图片下载失败"} {
		if strings.Contains(gotOwn, bad) {
			t.Fatalf("quoted own card leaked %q:\n%s", bad, gotOwn)
		}
	}
	// Simulate hub restart (clear in-memory cache); DB mapping must still recover real text.
	fa.mu.Lock()
	fa.sentCards = map[string]string{}
	fa.sentCardOrder = nil
	fa.mu.Unlock()

	gotOwnAfterRestart := sendQuote("evt_q_own_2", "om_q_own_2", ownCardMsgID, "重启后引用也要恢复原文")
	if !strings.Contains(gotOwnAfterRestart, "已完成接口改造，准备切 v0.2.79 发布。") {
		t.Fatalf("quoted own card after restart missing real text:\n%s", gotOwnAfterRestart)
	}
	ff.mu.Lock()
	if len(ff.calls) != callsBeforeOwnQuote {
		// Only async reaction calls (POST .../reactions) are expected, never GET message or resource for own card.
		for _, c := range ff.calls[callsBeforeOwnQuote:] {
			if c.Method == http.MethodGet {
				ff.mu.Unlock()
				t.Fatalf("unexpected Feishu GET call for own quoted card: %s %s", c.Method, c.Path)
			}
		}
	}
	ff.mu.Unlock()

	// 2) External Card 2.0 payload with real body.elements (markdown, column_set, non-downloadable card image,
	//    button, and homepage footer): extracts real text cleanly without false image download failure.
	card20JSON, err := marshalCardJSON(map[string]any{
		"schema": "2.0",
		"header": map[string]any{
			"title": map[string]string{"tag": "plain_text", "content": "发布提醒"},
		},
		"body": map[string]any{
			"elements": []any{
				map[string]any{"tag": "markdown", "content": "流水线已通过，等待审批。"},
				map[string]any{
					"tag": "column_set",
					"columns": []any{
						map[string]any{
							"tag": "column",
							"elements": []any{
								map[string]any{"tag": "markdown", "content": "版本：v0.2.79"},
							},
						},
					},
				},
				map[string]any{"tag": "img", "img_key": "img_v3_card_logo"},
				map[string]any{"tag": "button", "text": map[string]string{"tag": "plain_text", "content": "打开详情"}},
				map[string]any{"tag": "hr"},
				map[string]any{"tag": "markdown", "text_size": "notation", "content": larkFooterMarkdown},
			},
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	pngCard := []byte("\x89PNG\r\n\x1a\ncard-real-image")
	ff.mu.Lock()
	ff.messages["om_ext_card_20"] = fakeFeishuMessage{
		MessageID: "om_ext_card_20",
		MsgType:   "interactive",
		Content:   card20JSON,
	}
	ff.messages["om_ext_card_downgraded"] = fakeFeishuMessage{
		MessageID: "om_ext_card_downgraded",
		MsgType:   "interactive",
		Content:   `{"title":"Collie","elements":[[{"tag":"img","image_key":"img_v3_02ad_downgrade_icon"},{"tag":"text","text":"请升级至最新版本客户端，以查看内容"}]]}`,
	}
	ff.messages["om_ext_card_downgraded_en"] = fakeFeishuMessage{
		MessageID: "om_ext_card_downgraded_en",
		MsgType:   "interactive",
		Content:   `{"elements":[[{"tag":"text","text":"Please upgrade to the latest version of Lark to view the content."}]]}`,
	}
	ff.messages["om_ext_card_img_only_fail"] = fakeFeishuMessage{
		MessageID: "om_ext_card_img_only_fail",
		MsgType:   "interactive",
		Content:   `{"elements":[[{"tag":"img","image_key":"img_v3_card_logo"}]]}`,
	}
	ff.messages["om_ext_card_img_ok"] = fakeFeishuMessage{
		MessageID: "om_ext_card_img_ok",
		MsgType:   "interactive",
		Content:   `{"title":"截图卡片","elements":[[{"tag":"text","text":"请看附图"},{"tag":"img","image_key":"img_card_ok"}]]}`,
	}
	ff.resources["img_v3_card_logo"] = fakeFeishuResource{Code: 14005, Msg: "Resource Has Been Deleted"}
	ff.resources["img_v3_02ad_downgrade_icon"] = fakeFeishuResource{Code: 14005, Msg: "Resource Has Been Deleted"}
	ff.resources["img_card_ok"] = fakeFeishuResource{Data: pngCard, FileName: "card.png"}
	ff.mu.Unlock()

	gotExt20 := sendQuote("evt_q_ext_20", "om_q_ext_20", "om_ext_card_20", "批准发版")
	for _, want := range []string{"[引用消息]", "> 发布提醒", "> 流水线已通过，等待审批。", "> 版本：v0.2.79", "批准发版"} {
		if !strings.Contains(gotExt20, want) {
			t.Fatalf("quoted Card 2.0 missing %q:\n%s", want, gotExt20)
		}
	}
	for _, bad := range []string{"图片下载失败", "打开详情", collieHomepageURL} {
		if strings.Contains(gotExt20, bad) {
			t.Fatalf("quoted Card 2.0 should not contain %q:\n%s", bad, gotExt20)
		}
	}

	// 3) External downgraded Card 2.0: strips upgrade placeholder and skips downgrade icon completely.
	gotDown := sendQuote("evt_q_ext_down", "om_q_ext_down", "om_ext_card_downgraded", "看下这条")
	if !strings.Contains(gotDown, "> [飞书卡片：Collie]") || !strings.Contains(gotDown, "看下这条") {
		t.Fatalf("unexpected downgraded card formatting:\n%s", gotDown)
	}
	for _, bad := range []string{"请升级至最新版本客户端", "图片下载失败"} {
		if strings.Contains(gotDown, bad) {
			t.Fatalf("downgraded card leaked %q:\n%s", bad, gotDown)
		}
	}
	ff.mu.Lock()
	for _, c := range ff.calls {
		if strings.Contains(c.Path, "img_v3_02ad_downgrade_icon") {
			ff.mu.Unlock()
			t.Fatalf("downgraded card should never attempt downloading placeholder icon: %s", c.Path)
		}
	}
	ff.mu.Unlock()

	// 4) English downgrade placeholder without title falls back to clean generic card label.
	gotDownEn := sendQuote("evt_q_ext_down_en", "om_q_ext_down_en", "om_ext_card_downgraded_en", "check this")
	if !strings.Contains(gotDownEn, "> [飞书卡片消息]") || strings.Contains(strings.ToLower(gotDownEn), "please upgrade") {
		t.Fatalf("unexpected english downgraded card formatting:\n%s", gotDownEn)
	}

	// 5) Card with only a non-downloadable card image key falls back cleanly without false image error.
	gotImgOnlyFail := sendQuote("evt_q_ext_img_fail", "om_q_ext_img_fail", "om_ext_card_img_only_fail", "这张卡片")
	if !strings.Contains(gotImgOnlyFail, "> [飞书卡片消息]") || strings.Contains(gotImgOnlyFail, "图片下载失败") || strings.Contains(gotImgOnlyFail, "下载失败") {
		t.Fatalf("unexpected image-only card fallback:\n%s", gotImgOnlyFail)
	}

	// 6) Card with a downloadable image still attaches it cleanly alongside real text.
	gotImgOK := sendQuote("evt_q_ext_img_ok", "om_q_ext_img_ok", "om_ext_card_img_ok", "收到")
	if !strings.Contains(gotImgOK, "含 1 张图片") || !strings.Contains(gotImgOK, "> 请看附图") || !strings.Contains(gotImgOK, "附件：\n") {
		t.Fatalf("unexpected downloadable card image formatting:\n%s", gotImgOK)
	}
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
