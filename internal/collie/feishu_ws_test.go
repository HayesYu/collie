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
	Items     []map[string]any
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
			msgID := strings.TrimPrefix(r.URL.Path[:idx], "/open-apis/im/v1/messages/")
			ff.mu.Lock()
			res, hasCustom := ff.resources[msgID+"/"+fileKey]
			if !hasCustom {
				res, hasCustom = ff.resources[fileKey]
			}
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
			if len(msg.Items) > 0 {
				_ = json.NewEncoder(w).Encode(map[string]any{
					"code": 0,
					"msg":  "ok",
					"data": map[string]any{"items": msg.Items},
				})
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
	if !strings.Contains(got2, "看下这张报错截图") {
		t.Fatalf("quoted image missing context:\n%s", got2)
	}
	if strings.Contains(got2, "[引用图片]") {
		t.Fatalf("clean quoted image should not prepend redundant [引用图片] banner:\n%s", got2)
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
	for _, want := range []string{feishuGroupFeedbackTag, "暗色模式下这里文字重叠了", "附件：\n"} {
		if !strings.Contains(fbTrig.Text, want) {
			t.Fatalf("feedback quoted trigger missing %q:\n%s", want, fbTrig.Text)
		}
	}
	if strings.Contains(fbTrig.Text, "[引用图片]") {
		t.Fatalf("clean feedback quoted image should not prepend redundant [引用图片]:\n%s", fbTrig.Text)
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

func TestCleanFeishuInboundHTML(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{
			name: "single_p_paragraph",
			in:   "<p>看看这个 case</p>",
			want: "看看这个 case",
		},
		{
			name: "multi_p_paragraphs",
			in:   "<p>第一段</p><p>第二段</p>",
			want: "第一段\n第二段",
		},
		{
			name: "multi_p_with_newlines_and_blank_br",
			in:   "<p>第一段</p>\n<p><br></p>\n<p>第二段</p>",
			want: "第一段\n\n第二段",
		},
		{
			name: "br_variants_and_trailing_br",
			in:   "<p>第一行<br>第二行<br/>第三行<br /></p>",
			want: "第一行\n第二行\n第三行",
		},
		{
			name: "html_entities_and_nbsp",
			in:   "<p>a &lt; b &amp;&amp; c &gt; d&nbsp;&quot;ok&quot;</p>",
			want: `a < b && c > d "ok"`,
		},
		{
			name: "escaped_html_code_inside_p_wrapper",
			in:   `<p>看看代码：&lt;div class="box"&gt;&lt;p&gt;hi&lt;/p&gt;&lt;/div&gt;</p>`,
			want: `看看代码：<div class="box"><p>hi</p></div>`,
		},
		{
			name: "uppercase_tags_with_dir_attr",
			in:   `<P dir="auto">大写标签<BR/>第二行</P>`,
			want: "大写标签\n第二行",
		},
		{
			name: "empty_p_only_nbsp",
			in:   "<p>&nbsp;  </p>",
			want: "",
		},
		{
			name: "preserve_plain_text",
			in:   "普通消息没有任何 HTML",
			want: "普通消息没有任何 HTML",
		},
		{
			name: "preserve_backtick_code_with_p",
			in:   "`<p>看看这个</p>`",
			want: "`<p>看看这个</p>`",
		},
		{
			name: "preserve_inline_mention_of_p_tag",
			in:   "把 <p> 标签去掉",
			want: "把 <p> 标签去掉",
		},
		{
			name: "preserve_mixed_text_outside_p",
			in:   "<p>a</p> 和 <p>b</p>",
			want: "<p>a</p> 和 <p>b</p>",
		},
		{
			name: "preserve_outer_div_html_snippet",
			in:   "<div><p>hello</p></div>",
			want: "<div><p>hello</p></div>",
		},
		{
			name: "preserve_p_with_class_attribute",
			in:   `<p class="card">hello</p>`,
			want: `<p class="card">hello</p>`,
		},
		{
			name: "preserve_raw_nested_html_tags",
			in:   "<p><div>raw html</div></p>",
			want: "<p><div>raw html</div></p>",
		},
		{
			name: "preserve_plain_entities_without_p_wrapper",
			in:   "foo &lt; bar &amp; baz",
			want: "foo &lt; bar &amp; baz",
		},
		{
			name: "preserve_unclosed_p",
			in:   "<p>unclosed paragraph",
			want: "<p>unclosed paragraph",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := cleanFeishuInboundHTML(tc.in); got != tc.want {
				t.Fatalf("cleanFeishuInboundHTML(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestFeishuInboundHTMLSanitizationInMessages(t *testing.T) {
	_, sp, fa, ff, sink := setupFeishuTestHarness(t)
	ctx := context.Background()

	ff.mu.Lock()
	ff.messages["om_html_parent"] = fakeFeishuMessage{
		MessageID: "om_html_parent",
		MsgType:   "text",
		Content:   `{"text":"<p>父消息第一行<br/>父消息 &lt;ok&gt;</p>"}`,
	}
	ff.mu.Unlock()

	sendMsg := func(evID, msgID, chatType, parentID, content string, mentions []*larkim.MentionEvent) string {
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
					ChatId:      strPtr("oc_html_chat"),
					ChatType:    strPtr(chatType),
					MessageType: strPtr("text"),
					Content:     strPtr(content),
					Mentions:    mentions,
				},
			},
		}
		if err := fa.handleMessageReceive(ctx, ev, sink); err != nil {
			t.Fatalf("handleMessageReceive(%s): %v", evID, err)
		}
		return sp.st.Entries[len(sp.st.Entries)-1].Text
	}

	gotDirect := sendMsg("evt_html_1", "om_html_1", "p2p", "", `{"text":"<p>看看这个 case</p>"}`, nil)
	if gotDirect != "看看这个 case" {
		t.Fatalf("direct <p> text = %q, want %q", gotDirect, "看看这个 case")
	}

	botMention := []*larkim.MentionEvent{{
		Key:           strPtr("@_user_1"),
		Name:          strPtr("Collie"),
		MentionedType: strPtr("bot"),
		Id:            &larkim.UserId{OpenId: strPtr("ou_bot_collie")},
	}}
	gotMentionInside := sendMsg("evt_html_2", "om_html_2", "group", "", `{"text":"<p>@_user_1 看看这个 case</p>"}`, botMention)
	if gotMentionInside != "看看这个 case" {
		t.Fatalf("group mention inside <p> = %q, want %q", gotMentionInside, "看看这个 case")
	}

	gotMentionOutside := sendMsg("evt_html_3", "om_html_3", "group", "", `{"text":"@_user_1 <p>再看一眼</p>"}`, botMention)
	if gotMentionOutside != "再看一眼" {
		t.Fatalf("group mention outside <p> = %q, want %q", gotMentionOutside, "再看一眼")
	}

	gotQuoted := sendMsg("evt_html_4", "om_html_4", "p2p", "om_html_parent", `{"text":"<p>回复它</p>"}`, nil)
	for _, want := range []string{"[引用消息]", "> 父消息第一行", "> 父消息 <ok>", "回复它"} {
		if !strings.Contains(gotQuoted, want) {
			t.Fatalf("quoted HTML text missing %q:\n%s", want, gotQuoted)
		}
	}
	if strings.Contains(gotQuoted, "<p>") || strings.Contains(gotQuoted, "</p>") {
		t.Fatalf("quoted HTML text leaked raw <p> tags:\n%s", gotQuoted)
	}
}

func TestFeishuMergeForwardInboundAndQuoted(t *testing.T) {
	_, sp, fa, ff, sink := setupFeishuTestHarness(t)
	ctx := context.Background()

	pngSubImg := []byte("\x89PNG\r\n\x1a\nsub-image-bytes")
	pngPostImg := []byte("\x89PNG\r\n\x1a\npost-image-bytes")

	ff.mu.Lock()
	// Direct merge_forward with out-of-order create_time, mixed sub-message types,
	// multiple sender formats, HTML-wrapped text, downloadable + failing images.
	ff.messages["om_mf_direct"] = fakeFeishuMessage{
		Items: []map[string]any{
			{
				"message_id":  "om_mf_direct",
				"msg_type":    "merge_forward",
				"create_time": "1732620009000",
				"body":        map[string]any{"content": "Merged and Forwarded Message"},
			},
			// Intentionally placed out of chronological order (t=3000 before t=1000).
			{
				"message_id":       "om_sub_post",
				"upper_message_id": "om_mf_direct",
				"msg_type":         "post",
				"create_time":      "1732620003000",
				"sender":           map[string]any{"id": "ou_colleague_named", "id_type": "open_id", "sender_type": "user"},
				"body": map[string]any{
					"content": `{"zh_cn":{"title":"排查日志","content":[[{"tag":"text","text":"附图如下"},{"tag":"img","image_key":"img_post_ok"},{"tag":"img","image_key":"img_post_fail"}]]}}`,
				},
			},
			{
				"message_id":       "om_sub_text",
				"upper_message_id": "om_mf_direct",
				"msg_type":         "text",
				"create_time":      "1732620001000",
				"sender":           map[string]any{"id": "ou_alice_123456", "id_type": "open_id", "sender_type": "user", "sender_name": "Alice"},
				"body":             map[string]any{"content": `{"text":"<p>先看这个报错 &lt;500&gt;</p>"}`},
			},
			{
				"message_id":       "om_sub_img",
				"upper_message_id": "om_mf_direct",
				"msg_type":         "image",
				"create_time":      "1732620002000",
				"sender":           map[string]any{"id": "ou_bob_123456", "id_type": "open_id", "sender_type": "user", "sender_i18n_names": map[string]any{"zh_cn": "鲍勃"}},
				"body":             map[string]any{"content": `{"image_key":"img_sub_direct"}`},
			},
			{
				"message_id":       "om_sub_file",
				"upper_message_id": "om_mf_direct",
				"msg_type":         "file",
				"create_time":      "1732620004000",
				"sender":           map[string]any{"id": "ou_colleague_noperm", "id_type": "open_id", "sender_type": "user"},
				"body":             map[string]any{"content": `{"file_name":"trace.log"}`},
			},
			{
				"message_id":       "om_sub_deleted",
				"upper_message_id": "om_mf_direct",
				"msg_type":         "text",
				"deleted":          true,
				"create_time":      "1732620005000",
				"sender":           map[string]any{"sender_name": "Charlie", "sender_type": "user"},
				"body":             map[string]any{"content": `{"text":"should not appear"}`},
			},
		},
	}
	// Bind resources strictly to their sub-message message_ids (NOT om_mf_direct).
	ff.resources["om_sub_img/img_sub_direct"] = fakeFeishuResource{Data: pngSubImg, FileName: "sub-err.png"}
	ff.resources["om_sub_post/img_post_ok"] = fakeFeishuResource{Data: pngPostImg, FileName: "post-log.png"}
	ff.resources["om_sub_post/img_post_fail"] = fakeFeishuResource{Code: 234001, Msg: "download failed"}
	// Ensure using the outer merge_forward ID would fail hard.
	ff.resources["om_mf_direct/img_sub_direct"] = fakeFeishuResource{Code: 234001, Msg: "wrong message_id"}
	ff.resources["om_mf_direct/img_post_ok"] = fakeFeishuResource{Code: 234001, Msg: "wrong message_id"}

	// Quoted merge_forward reproducing the exact screenshot case (quote merge_forward + <p>看看这个 case</p>).
	ff.messages["om_mf_quoted"] = fakeFeishuMessage{
		Items: []map[string]any{
			{
				"message_id":  "om_mf_quoted",
				"msg_type":    "merge_forward",
				"create_time": "1732620010000",
				"body":        map[string]any{"content": "Merged and Forwarded Message"},
			},
			{
				"message_id":       "om_qsub_1",
				"upper_message_id": "om_mf_quoted",
				"msg_type":         "text",
				"create_time":      "1732620011000",
				"sender":           map[string]any{"sender_name": "张三", "sender_type": "user"},
				"body":             map[string]any{"content": `{"text":"线上出现偶发超时"}`},
			},
			{
				"message_id":       "om_qsub_2",
				"upper_message_id": "om_mf_quoted",
				"msg_type":         "image",
				"create_time":      "1732620012000",
				"sender":           map[string]any{"sender_name": "李四", "sender_type": "user"},
				"body":             map[string]any{"content": `{"image_key":"img_qsub_2"}`},
			},
		},
	}
	ff.resources["om_qsub_2/img_qsub_2"] = fakeFeishuResource{Data: pngSubImg, FileName: "quoted-sub.png"}

	// Nested merge_forward with inline depth-1, lazy depth-2, depth-3 limit, and cycle protection.
	ff.messages["om_mf_nested_root"] = fakeFeishuMessage{
		Items: []map[string]any{
			{
				"message_id":  "om_mf_nested_root",
				"msg_type":    "merge_forward",
				"create_time": "1732620020000",
				"body":        map[string]any{"content": "Merged and Forwarded Message"},
			},
			{
				"message_id":       "om_mf_lvl1",
				"upper_message_id": "om_mf_nested_root",
				"msg_type":         "merge_forward",
				"create_time":      "1732620021000",
				"sender":           map[string]any{"sender_name": "L1", "sender_type": "user"},
				"body":             map[string]any{"content": "Merged and Forwarded Message"},
			},
			{
				"message_id":       "om_mf_lvl1_txt",
				"upper_message_id": "om_mf_lvl1",
				"msg_type":         "text",
				"create_time":      "1732620022000",
				"sender":           map[string]any{"sender_name": "内层用户", "sender_type": "user"},
				"body":             map[string]any{"content": `{"text":"来自一层嵌套"}`},
			},
			{
				"message_id":       "om_mf_lvl2_lazy",
				"upper_message_id": "om_mf_lvl1",
				"msg_type":         "merge_forward",
				"create_time":      "1732620023000",
				"sender":           map[string]any{"sender_name": "L2", "sender_type": "user"},
				"body":             map[string]any{"content": "Merged and Forwarded Message"},
			},
			// Direct cycle back to root!
			{
				"message_id":       "om_mf_nested_root",
				"upper_message_id": "om_mf_lvl1",
				"msg_type":         "merge_forward",
				"create_time":      "1732620024000",
				"sender":           map[string]any{"sender_name": "Cycle", "sender_type": "user"},
				"body":             map[string]any{"content": "Merged and Forwarded Message"},
			},
		},
	}
	ff.messages["om_mf_lvl2_lazy"] = fakeFeishuMessage{
		Items: []map[string]any{
			{
				"message_id":  "om_mf_lvl2_lazy",
				"msg_type":    "merge_forward",
				"create_time": "1732620023000",
				"body":        map[string]any{"content": "Merged and Forwarded Message"},
			},
			{
				"message_id":       "om_mf_lvl2_txt",
				"upper_message_id": "om_mf_lvl2_lazy",
				"msg_type":         "text",
				"create_time":      "1732620023100",
				"sender":           map[string]any{"sender_name": "二层用户", "sender_type": "user"},
				"body":             map[string]any{"content": `{"text":"来自二层懒加载"}`},
			},
			{
				"message_id":       "om_mf_lvl3_cap",
				"upper_message_id": "om_mf_lvl2_lazy",
				"msg_type":         "merge_forward",
				"create_time":      "1732620023200",
				"sender":           map[string]any{"sender_name": "L3", "sender_type": "user"},
				"body":             map[string]any{"content": "Merged and Forwarded Message"},
			},
		},
	}
	ff.messages["om_mf_api_err"] = fakeFeishuMessage{
		Code: 230011,
		Msg:  "no permission",
	}
	ff.mu.Unlock()

	sendEvent := func(evID, msgID, parentID, msgType, content string) string {
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
					ChatId:      strPtr("oc_mf_chat"),
					ChatType:    strPtr("p2p"),
					MessageType: strPtr(msgType),
					Content:     strPtr(content),
				},
			},
		}
		if err := fa.handleMessageReceive(ctx, ev, sink); err != nil {
			t.Fatalf("handleMessageReceive(%s): %v", evID, err)
		}
		return sp.st.Entries[len(sp.st.Entries)-1].Text
	}

	// 1) Direct merge_forward expansion.
	gotDirect := sendEvent("evt_mf_1", "om_mf_direct", "", "merge_forward", "Merged and Forwarded Message")
	wantParts := []string{
		"[合并转发]",
		"Alice：先看这个报错 <500>",
		"鲍勃：[图片]",
		"林同学：排查日志",
		"附图如下（含 1 张图片，1 张图片下载失败）",
		"[飞书文件：trace.log]",
		"Charlie：[原消息已撤回]",
		"\n\n附件：\n",
	}
	for _, want := range wantParts {
		if !strings.Contains(gotDirect, want) {
			t.Fatalf("direct merge_forward missing %q:\n%s", want, gotDirect)
		}
	}
	// Verify chronological ordering: Alice (1000) < 鲍勃 (2000) < 林同学 (3000) < trace.log (4000) < Charlie (5000).
	posAlice := strings.Index(gotDirect, "Alice：")
	posBob := strings.Index(gotDirect, "鲍勃：")
	posLin := strings.Index(gotDirect, "林同学：")
	posFile := strings.Index(gotDirect, "[飞书文件：trace.log]")
	posCharlie := strings.Index(gotDirect, "Charlie：")
	if !(posAlice < posBob && posBob < posLin && posLin < posFile && posFile < posCharlie) {
		t.Fatalf("direct merge_forward not sorted chronologically (alice=%d bob=%d lin=%d file=%d charlie=%d):\n%s",
			posAlice, posBob, posLin, posFile, posCharlie, gotDirect)
	}
	// Verify attachments saved to disk with expected bytes.
	attachIdx := strings.Index(gotDirect, "\n\n附件：\n")
	attachPaths := strings.Split(strings.TrimSpace(gotDirect[attachIdx+len("\n\n附件：\n"):]), "\n")
	if len(attachPaths) != 2 {
		t.Fatalf("expected 2 downloaded attachments, got %d (%v)\n%s", len(attachPaths), attachPaths, gotDirect)
	}
	b0, err := os.ReadFile(attachPaths[0])
	if err != nil || !bytes.Equal(b0, pngSubImg) {
		t.Fatalf("first attachment mismatch: err=%v bytes=%q", err, b0)
	}
	b1, err := os.ReadFile(attachPaths[1])
	if err != nil || !bytes.Equal(b1, pngPostImg) {
		t.Fatalf("second attachment mismatch: err=%v bytes=%q", err, b1)
	}
	// Verify sub-message resource URLs used sub-message IDs, never outer om_mf_direct.
	ff.mu.Lock()
	var sawSubImgCall, sawPostImgCall bool
	for _, c := range ff.calls {
		if c.Path == "/open-apis/im/v1/messages/om_sub_img/resources/img_sub_direct" {
			sawSubImgCall = true
		}
		if c.Path == "/open-apis/im/v1/messages/om_sub_post/resources/img_post_ok" {
			sawPostImgCall = true
		}
		if strings.HasPrefix(c.Path, "/open-apis/im/v1/messages/om_mf_direct/resources/") {
			ff.mu.Unlock()
			t.Fatalf("unexpected image download using outer merge_forward message_id: %s", c.Path)
		}
	}
	ff.mu.Unlock()
	if !sawSubImgCall || !sawPostImgCall {
		t.Fatalf("expected sub-message image resource downloads (sub=%v post=%v)", sawSubImgCall, sawPostImgCall)
	}

	// 2) Quoted merge_forward + HTML-wrapped text reply ("<p>看看这个 case</p>").
	callsBeforeQuote := 0
	ff.mu.Lock()
	callsBeforeQuote = len(ff.calls)
	ff.mu.Unlock()

	gotQuoted := sendEvent("evt_mf_2", "om_reply_mf", "om_mf_quoted", "text", `{"text":"<p>看看这个 case</p>"}`)
	for _, want := range []string{
		"[引用消息（含 1 张图片）]",
		"> [合并转发]",
		"> 张三：线上出现偶发超时",
		"> 李四：[图片]",
		"\n\n看看这个 case\n\n附件：\n",
	} {
		if !strings.Contains(gotQuoted, want) {
			t.Fatalf("quoted merge_forward missing %q:\n%s", want, gotQuoted)
		}
	}
	if strings.Contains(gotQuoted, "<p>") || strings.Contains(gotQuoted, "</p>") || strings.Contains(gotQuoted, "[飞书 merge_forward 消息]") {
		t.Fatalf("quoted merge_forward leaked raw placeholder or HTML:\n%s", gotQuoted)
	}
	// Verify GET /messages/om_mf_quoted was called only once (no duplicate fetch).
	getCount := 0
	ff.mu.Lock()
	for _, c := range ff.calls[callsBeforeQuote:] {
		if c.Method == http.MethodGet && c.Path == "/open-apis/im/v1/messages/om_mf_quoted" {
			getCount++
		}
	}
	ff.mu.Unlock()
	if getCount != 1 {
		t.Fatalf("expected exactly 1 GET for quoted merge_forward, got %d", getCount)
	}

	// 3) Nested merge_forward with depth cap (depth 0 -> 1 -> 2 -> cap at 3).
	gotNested := sendEvent("evt_mf_3", "om_mf_nested_root", "", "merge_forward", "Merged and Forwarded Message")
	for _, want := range []string{
		"[合并转发]",
		"L1：[合并转发]",
		"  内层用户：来自一层嵌套",
		"  L2：[合并转发]",
		"    二层用户：来自二层懒加载",
		"    L3：[嵌套合并转发已达展开深度上限]",
	} {
		if !strings.Contains(gotNested, want) {
			t.Fatalf("nested merge_forward missing %q:\n%s", want, gotNested)
		}
	}

	// 4) Graceful fallback when API fails.
	gotErr := sendEvent("evt_mf_4", "om_mf_api_err", "", "merge_forward", "Merged and Forwarded Message")
	if gotErr != "[飞书 merge_forward 消息]" {
		t.Fatalf("api error merge_forward = %q, want %q", gotErr, "[飞书 merge_forward 消息]")
	}
}

func TestFeishuQuotedLocalImageUsesNativeReplyWithoutDuplication(t *testing.T) {
	_, sp, fa, ff, sink := setupFeishuTestHarness(t)
	ctx := context.Background()

	pngBytes := []byte("\x89PNG\r\n\x1a\nlocal-quoted-screenshot")
	ff.mu.Lock()
	ff.resources["om_user_img_1/img_local_1"] = fakeFeishuResource{Data: pngBytes, FileName: "terminal.png"}
	ff.mu.Unlock()

	sendEv := func(evID, msgID, parentID, msgType, content string) {
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
					ChatId:      strPtr("oc_quote_img"),
					ChatType:    strPtr("p2p"),
					MessageType: strPtr(msgType),
					Content:     strPtr(content),
				},
			},
		}
		if err := fa.handleMessageReceive(ctx, ev, sink); err != nil {
			t.Fatalf("handleMessageReceive(%s): %v", evID, err)
		}
	}

	// Step 1: User sends an image in Feishu.
	sendEv("evt_img_local_1", "om_user_img_1", "", "image", `{"image_key":"img_local_1"}`)
	imgEntry := sp.st.Entries[len(sp.st.Entries)-1]
	savedPaths := extractAttachmentPaths(imgEntry.Text)
	if len(savedPaths) != 1 {
		t.Fatalf("expected 1 saved image attachment, got %q", imgEntry.Text)
	}

	ff.mu.Lock()
	callsBefore := len(ff.calls)
	ff.mu.Unlock()

	// Step 2: User quotes that exact image and sends "<p>merlin 上运行</p>".
	sendEv("evt_img_local_2", "om_user_reply_1", "om_user_img_1", "text", `{"text":"<p>merlin 上运行</p>"}`)
	replyEntry := sp.st.Entries[len(sp.st.Entries)-1]
	if replyEntry.Text != "merlin 上运行" {
		t.Fatalf("reply bubble should contain clean text without duplicate image or [引用图片], got:\n%s", replyEntry.Text)
	}
	if replyEntry.Re == nil || replyEntry.Re.Entry != imgEntry.ID {
		t.Fatalf("reply bubble should link natively to image entry %s via Re, got %+v", imgEntry.ID, replyEntry.Re)
	}
	trig := sp.st.Triggers[len(sp.st.Triggers)-1]
	if !strings.Contains(trig.Text, savedPaths[0]) || !strings.Contains(trig.Text, "merlin 上运行") {
		t.Fatalf("router trigger must still receive image path %q and reply text:\n%s", savedPaths[0], trig.Text)
	}
	// Verify zero redundant GET calls to Feishu for the already-downloaded local image.
	ff.mu.Lock()
	for _, c := range ff.calls[callsBefore:] {
		if c.Method == http.MethodGet {
			ff.mu.Unlock()
			t.Fatalf("unexpected Feishu GET when quoting local image entry: %s %s", c.Method, c.Path)
		}
	}
	ff.mu.Unlock()

	// Step 3: Simulate hub restart (clear in-memory msgEntries); DB fallback still links natively.
	fa.mu.Lock()
	fa.msgEntries = map[string]string{}
	fa.msgEntryOrder = nil
	fa.mu.Unlock()

	sendEv("evt_img_local_3", "om_user_reply_2", "om_user_img_1", "text", `{"text":"<p>重启后再引用</p>"}`)
	replyAfterRestart := sp.st.Entries[len(sp.st.Entries)-1]
	if replyAfterRestart.Text != "重启后再引用" || replyAfterRestart.Re == nil || replyAfterRestart.Re.Entry != imgEntry.ID {
		t.Fatalf("after restart expected native Re=%s clean text, got text=%q re=%+v", imgEntry.ID, replyAfterRestart.Text, replyAfterRestart.Re)
	}
	trig2 := sp.st.Triggers[len(sp.st.Triggers)-1]
	if !strings.Contains(trig2.Text, savedPaths[0]) {
		t.Fatalf("router trigger after restart missing image path %q:\n%s", savedPaths[0], trig2.Text)
	}
}

func TestFeishuNeverFallbackToGroupChat(t *testing.T) {
	ctx := context.Background()

	// Case 1: Owner previously DM'd (oc_p2p_owner), then @Collie in a public group,
	// then talks from the Web UI. Non-group replies must NEVER fall back to the group
	// or trigger group-based instant sync; they fall back to lastP2P. Only an explicit
	// reply quoting the owner's group message routes back into the group.
	t.Run("falls back to lastP2P and disables group instant-sync fallback", func(t *testing.T) {
		_, sp, fa, ff, sink := setupFeishuTestHarness(t)
		cfg := FeishuConfig{AllowOpenIDs: []string{"ou_allowed_owner"}, GroupMentionOnly: true, InstantChatSync: true}

		p2pMsg := &larkim.P2MessageReceiveV1{
			EventV2Base: &larkevent.EventV2Base{Header: &larkevent.EventHeader{EventID: "evt_nofb_p2p"}},
			Event: &larkim.P2MessageReceiveV1Data{
				Sender: &larkim.EventSender{
					SenderType: strPtr("user"),
					SenderId:   &larkim.UserId{OpenId: strPtr("ou_allowed_owner")},
				},
				Message: &larkim.EventMessage{
					MessageId:   strPtr("om_nofb_p2p_1"),
					ChatId:      strPtr("oc_p2p_owner"),
					ChatType:    strPtr("p2p"),
					MessageType: strPtr("text"),
					Content:     strPtr(`{"text":"私聊打个招呼"}`),
				},
			},
		}
		if err := fa.handleMessageReceive(ctx, p2pMsg, sink); err != nil {
			t.Fatal(err)
		}

		pubGrpMsg := &larkim.P2MessageReceiveV1{
			EventV2Base: &larkevent.EventV2Base{Header: &larkevent.EventHeader{EventID: "evt_nofb_grp"}},
			Event: &larkim.P2MessageReceiveV1Data{
				Sender: &larkim.EventSender{
					SenderType: strPtr("user"),
					SenderId:   &larkim.UserId{OpenId: strPtr("ou_allowed_owner")},
				},
				Message: &larkim.EventMessage{
					MessageId:   strPtr("om_owner_pub_1"),
					ChatId:      strPtr("oc_public_group"),
					ChatType:    strPtr("group"),
					MessageType: strPtr("text"),
					Content:     strPtr(`{"text":"@_user_1 群里问个问题"}`),
					Mentions: []*larkim.MentionEvent{{
						Key:           strPtr("@_user_1"),
						Id:            &larkim.UserId{OpenId: strPtr("ou_bot_collie")},
						MentionedType: strPtr("bot"),
						Name:          strPtr("Collie Test Bot"),
					}},
				},
			},
		}
		if err := fa.handleMessageReceive(ctx, pubGrpMsg, sink); err != nil {
			t.Fatal(err)
		}
		sp.mu.Lock()
		grpUserEntry := sp.st.Entries[len(sp.st.Entries)-1]
		sp.mu.Unlock()

		// Human now sends a message from the Web UI (not Feishu, Channel == "").
		sp.mu.Lock()
		webUserEntry := sp.addEntryLocked("user", mainThread, "网页端发的私密指令", "", "")
		sp.mu.Unlock()
		webReply, _ := sp.Reply(mainThread, "收到网页端私密指令，已开始处理", webUserEntry.ID, nil, false)
		unquotedReply, _ := sp.Reply(mainThread, "这是一条无引用后台汇报", "", nil, false)

		if fa.ShouldInstantSync(webReply, false) {
			t.Fatal("ShouldInstantSync must be false for web reply when lastInbound is a group chat")
		}
		if fa.ShouldInstantSync(unquotedReply, false) {
			t.Fatal("ShouldInstantSync must be false for unquoted reply when lastInbound is a group chat")
		}

		for label, entry := range map[string]*Entry{
			"web reply":      webReply,
			"unquoted reply": unquotedReply,
			"async notice":   nil,
		} {
			rt, rid, rmsg := fa.targetForEntry(entry, cfg)
			if rt != "chat_id" || rid != "oc_p2p_owner" || rmsg != "" {
				t.Fatalf("targetForEntry(%s) = (%q,%q,%q), want (chat_id, oc_p2p_owner, \"\")", label, rt, rid, rmsg)
			}
		}

		// Sending outbound card/notice for web reply must hit oc_p2p_owner, never oc_public_group.
		receipt, err := fa.SendReply(ctx, webReply, "http://127.0.0.1:7788")
		if err != nil {
			t.Fatalf("SendReply(webReply): %v", err)
		}
		if receipt.ChatID != "oc_p2p_owner" {
			t.Fatalf("webReply routed to %q, want oc_p2p_owner", receipt.ChatID)
		}

		time.Sleep(80 * time.Millisecond)
		ff.mu.Lock()
		for _, c := range ff.calls {
			if strings.HasSuffix(c.Path, "/reply") || strings.Contains(c.Body, `"receive_id":"oc_public_group"`) {
				ff.mu.Unlock()
				t.Fatalf("web/unquoted reply leaked into public group: %+v", c)
			}
		}
		ff.mu.Unlock()

		// Explicit reply quoting the owner's group message MUST still instant-sync back to the group.
		grpReply, _ := sp.Reply(mainThread, "群里这个问题已查清楚", grpUserEntry.ID, nil, false)
		if !fa.ShouldInstantSync(grpReply, true) {
			t.Fatal("ShouldInstantSync must stay true for explicit reply to group origin")
		}
		rt, rid, rmsg := fa.targetForEntry(grpReply, cfg)
		if rt != "chat_id" || rid != "oc_public_group" || rmsg != "om_owner_pub_1" {
			t.Fatalf("targetForEntry(grpReply) = (%q,%q,%q), want (chat_id, oc_public_group, om_owner_pub_1)", rt, rid, rmsg)
		}
		ff.waitForCall(t, func(c fakeFeishuCall) bool {
			return c.Method == http.MethodPost && c.Path == "/open-apis/im/v1/messages/om_owner_pub_1/reply" &&
				strings.Contains(extractCardContent(c.Body), "群里这个问题已查清楚")
		})
	})

	// Case 2: No prior P2P DM (only AllowOpenIDs configured). Owner @Collie in a public group,
	// then replies from Web UI (and even clicks a card in the group) -> fallback must go to
	// AllowOpenIDs[0] (owner DM open_id), never to the group.
	t.Run("falls back to AllowOpenIDs without prior P2P and resists group card click poisoning", func(t *testing.T) {
		_, sp, fa, ff, sink := setupFeishuTestHarness(t)
		cfg := FeishuConfig{AllowOpenIDs: []string{"ou_allowed_owner"}, GroupMentionOnly: true, InstantChatSync: true}

		pubGrpMsg := &larkim.P2MessageReceiveV1{
			EventV2Base: &larkevent.EventV2Base{Header: &larkevent.EventHeader{EventID: "evt_nofb2_grp"}},
			Event: &larkim.P2MessageReceiveV1Data{
				Sender: &larkim.EventSender{
					SenderType: strPtr("user"),
					SenderId:   &larkim.UserId{OpenId: strPtr("ou_allowed_owner")},
				},
				Message: &larkim.EventMessage{
					MessageId:   strPtr("om_owner_pub_2"),
					ChatId:      strPtr("oc_public_group_2"),
					ChatType:    strPtr("group"),
					MessageType: strPtr("text"),
					Content:     strPtr(`{"text":"@_user_1 帮我看下群消息"}`),
					Mentions: []*larkim.MentionEvent{{
						Key:           strPtr("@_user_1"),
						Id:            &larkim.UserId{OpenId: strPtr("ou_bot_collie")},
						MentionedType: strPtr("bot"),
						Name:          strPtr("Collie Test Bot"),
					}},
				},
			},
		}
		if err := fa.handleMessageReceive(ctx, pubGrpMsg, sink); err != nil {
			t.Fatal(err)
		}
		sp.mu.Lock()
		grpUserEntry := sp.st.Entries[len(sp.st.Entries)-1]
		sp.mu.Unlock()

		// Collie sends an --opt card explicitly replying to the group message, then owner clicks it inside the group.
		optCard, _ := sp.Reply(mainThread, "群里选一个方案", grpUserEntry.ID, []string{"方案A", "方案B"}, false)
		ff.waitForCall(t, func(c fakeFeishuCall) bool {
			return c.Method == http.MethodPost && c.Path == "/open-apis/im/v1/messages/om_owner_pub_2/reply"
		})
		if _, err := fa.handleCardAction(ctx, &larkcallback.CardActionTriggerEvent{
			EventV2Base: &larkevent.EventV2Base{Header: &larkevent.EventHeader{EventID: "evt_nofb2_card"}},
			Event: &larkcallback.CardActionTriggerRequest{
				Operator: &larkcallback.Operator{OpenID: "ou_allowed_owner"},
				Context:  &larkcallback.Context{OpenChatID: "oc_public_group_2", OpenMessageID: "om_card_in_grp"},
				Action:   &larkcallback.CallBackAction{Value: map[string]any{"action": "pick_opt", "entry_id": optCard.ID, "opt_idx": "0"}},
			},
		}, sink); err != nil {
			t.Fatalf("handleCardAction in group: %v", err)
		}

		// Now owner sends a Web UI message and Collie replies without Feishu origin.
		sp.mu.Lock()
		webUserEntry := sp.addEntryLocked("user", mainThread, "回到网页端继续私聊任务", "", "")
		sp.mu.Unlock()
		webReply, _ := sp.Reply(mainThread, "好的，网页端任务已安排", webUserEntry.ID, nil, false)
		if fa.ShouldInstantSync(webReply, false) {
			t.Fatal("ShouldInstantSync must stay false after group message + group card click")
		}
		rt, rid, rmsg := fa.targetForEntry(webReply, cfg)
		if rt != "open_id" || rid != "ou_allowed_owner" || rmsg != "" {
			t.Fatalf("targetForEntry(webReply) = (%q,%q,%q), want (open_id, ou_allowed_owner, \"\")", rt, rid, rmsg)
		}
		receipt, err := fa.SendReply(ctx, webReply, "http://127.0.0.1:7788")
		if err != nil {
			t.Fatalf("SendReply(webReply): %v", err)
		}
		if receipt.ChatID != "ou_allowed_owner" {
			t.Fatalf("webReply routed to %q, want ou_allowed_owner", receipt.ChatID)
		}

		// Also verify CanSendFallback does not count a group-only lastInbound when AllowOpenIDs is empty.
		fa.mgr.mu.Lock()
		emptyAllowCfg, _ := json.Marshal(FeishuConfig{Region: "cn", AppID: "cli_test_app", InstantChatSync: true})
		rec := fa.mgr.records[feishuKind]
		rec.Config = emptyAllowCfg
		fa.mgr.records[feishuKind] = rec
		fa.mgr.mu.Unlock()
		if fa.CanSendFallback() {
			t.Fatal("CanSendFallback must be false when AllowOpenIDs is empty and only a group chat was seen")
		}
	})
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
