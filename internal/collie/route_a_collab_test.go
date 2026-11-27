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
	"sync/atomic"
	"testing"

	larkim "github.com/larksuite/oapi-sdk-go/v3/service/im/v1"
)

func TestFeishuFeedbackControlledReplyGuardrailsAndSuccess(t *testing.T) {
	t.Parallel()
	var replyHits atomic.Int32
	var gotPath, gotMsgType, gotContentText string

	feishuSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.URL.Path == "/open-apis/auth/v3/tenant_access_token/internal":
			_, _ = w.Write([]byte(`{"code":0,"tenant_access_token":"t-test-token","expire":7200}`))
		case strings.HasPrefix(r.URL.Path, "/open-apis/im/v1/messages/") && strings.HasSuffix(r.URL.Path, "/reply"):
			replyHits.Add(1)
			gotPath = r.URL.Path
			raw, _ := io.ReadAll(r.Body)
			var reqBody struct {
				MsgType string `json:"msg_type"`
				Content string `json:"content"`
			}
			_ = json.Unmarshal(raw, &reqBody)
			gotMsgType = reqBody.MsgType
			var inner struct {
				Text string `json:"text"`
			}
			_ = json.Unmarshal([]byte(reqBody.Content), &inner)
			gotContentText = inner.Text
			_, _ = w.Write([]byte(`{"code":0,"msg":"success","data":{"message_id":"om_reply_sent_99"}}`))
		default:
			_, _ = w.Write([]byte(`{"code":0,"data":{}}`))
		}
	}))
	defer feishuSrv.Close()

	dir := t.TempDir()
	s := &Server{token: "tok-routea", home: dir}
	sp, err := newSuper(s, dir)
	if err != nil {
		t.Fatalf("newSuper: %v", err)
	}
	defer func() {
		sp.feishuOnboard.Close()
		sp.db.Close()
	}()
	s.sup = sp
	sp.feishu.baseURLOverride = feishuSrv.URL
	sp.feishu.httpClient = feishuSrv.Client()

	cfgJSON, _ := json.Marshal(FeishuConfig{
		Region:           "cn",
		AppID:            "cli_routea_01",
		AllowOpenIDs:     []string{"ou_owner_01"},
		FeedbackChatIDs:  []string{"oc_feedback_01", "oc_feedback_02"},
		GroupMentionOnly: true,
		InstantChatSync:  true,
	})
	en := true
	if _, err := sp.integrations.Save(feishuKind, SaveRequest{
		Enabled: &en,
		Config:  cfgJSON,
		Secrets: map[string]string{"app_id": "cli_routea_01", "app_secret": "sec_routea_secret"},
	}); err != nil {
		t.Fatalf("save feishu config: %v", err)
	}

	// Simulate an inbound read-only feedback group @mention and verify event carries
	// message_id + controlled reply guidance without recording group route.
	sp.feishu.mu.Lock()
	sp.feishu.activeCfg = parseFeishuConfig(cfgJSON)
	sp.feishu.botOpenID = "ou_bot_01"
	sp.feishu.botName = "Collie"
	sp.feishu.mu.Unlock()

	chatID := "oc_feedback_01"
	chatType := "group"
	msgID := "om_colleague_msg_42"
	msgType := "text"
	content := `{"text":"@_user_1 线上搜索有偶发慢查询，帮忙看下"}`
	senderType := "user"
	senderOID := "ou_colleague_99"
	mentionKey := "@_user_1"
	mentionOID := "ou_bot_01"
	mentionType := "bot"
	ev := &larkim.P2MessageReceiveV1{
		Event: &larkim.P2MessageReceiveV1Data{
			Sender: &larkim.EventSender{
				SenderType: &senderType,
				SenderId:   &larkim.UserId{OpenId: &senderOID},
			},
			Message: &larkim.EventMessage{
				ChatId:      &chatID,
				ChatType:    &chatType,
				MessageId:   &msgID,
				MessageType: &msgType,
				Content:     &content,
				Mentions: []*larkim.MentionEvent{{
					Key:           &mentionKey,
					MentionedType: &mentionType,
					Id:            &larkim.UserId{OpenId: &mentionOID},
				}},
			},
		},
	}
	if err := sp.feishu.handleMessageReceive(context.Background(), ev, &managerSink{mgr: sp.integrations, kind: feishuKind}); err != nil {
		t.Fatalf("handleMessageReceive feedback: %v", err)
	}

	sp.mu.Lock()
	var fbTrigger *Trigger
	for _, tr := range sp.st.Triggers {
		if strings.Contains(tr.Text, feishuGroupFeedbackTag) {
			fbTrigger = tr
		}
	}
	sp.mu.Unlock()
	if fbTrigger == nil {
		t.Fatalf("expected feedback trigger in store")
	}
	for _, want := range []string{"群 ID：oc_feedback_01", "消息 ID：om_colleague_msg_42", "collie feishu reply --chat oc_feedback_01 --reply-to om_colleague_msg_42 --confirm"} {
		if !strings.Contains(fbTrigger.Text, want) {
			t.Fatalf("feedback trigger missing %q:\n%s", want, fbTrigger.Text)
		}
	}
	if replyHits.Load() != 0 {
		t.Fatalf("read-only feedback event must NEVER auto-reply, hits=%d", replyHits.Load())
	}

	mux := http.NewServeMux()
	s.superRoutes(mux)

	// 1. Unauthenticated -> 401.
	recUnauth := httptest.NewRecorder()
	mux.ServeHTTP(recUnauth, authedReq(http.MethodPost, "/api/super/integrations/feishu/reply", `{"chat_id":"oc_feedback_01","reply_to_message_id":"om_colleague_msg_42","text":"收到","confirm":true}`, ""))
	if recUnauth.Code != http.StatusUnauthorized {
		t.Fatalf("unauth status=%d, want 401", recUnauth.Code)
	}

	// 2. Guardrail rejections: missing confirm, missing reply_to, non-feedback chat, cross-chat mismatch.
	for _, tc := range []struct {
		name string
		body string
	}{
		{"missing_confirm", `{"chat_id":"oc_feedback_01","reply_to_message_id":"om_colleague_msg_42","text":"已定位"}`},
		{"missing_reply_to", `{"chat_id":"oc_feedback_01","text":"广播消息","confirm":true}`},
		{"non_feedback_chat", `{"chat_id":"oc_random_group_99","reply_to_message_id":"om_any_1","text":"已定位","confirm":true}`},
		{"cross_chat_mismatch", `{"chat_id":"oc_feedback_02","reply_to_message_id":"om_colleague_msg_42","text":"串群测试","confirm":true}`},
		{"empty_text", `{"chat_id":"oc_feedback_01","reply_to_message_id":"om_colleague_msg_42","text":"   ","confirm":true}`},
	} {
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, authedReq(http.MethodPost, "/api/super/integrations/feishu/reply", tc.body, s.token))
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("%s: status=%d, want 400 (body=%s)", tc.name, rec.Code, rec.Body.String())
		}
	}
	if replyHits.Load() != 0 {
		t.Fatalf("rejected requests must not hit Feishu API, hits=%d", replyHits.Load())
	}

	// 3. Confirmed valid reply succeeds and sends clean text quote-reply without mutating routing.
	recOK := httptest.NewRecorder()
	mux.ServeHTTP(recOK, authedReq(http.MethodPost, "/api/super/integrations/feishu/reply", `{"chat_id":"oc_feedback_01","reply_to_message_id":"om_colleague_msg_42","text":"已排查：索引缺少联合排序键，修复 PR 已提。","confirm":true}`, s.token))
	if recOK.Code != http.StatusOK {
		t.Fatalf("confirmed reply status=%d body=%s", recOK.Code, recOK.Body.String())
	}
	if replyHits.Load() != 1 {
		t.Fatalf("replyHits=%d, want 1", replyHits.Load())
	}
	if gotPath != "/open-apis/im/v1/messages/om_colleague_msg_42/reply" {
		t.Fatalf("gotPath=%q", gotPath)
	}
	if gotMsgType != "text" || gotContentText != "已排查：索引缺少联合排序键，修复 PR 已提。" {
		t.Fatalf("unexpected sent payload: type=%q text=%q", gotMsgType, gotContentText)
	}
	var resp struct {
		OK               bool   `json:"ok"`
		ChatID           string `json:"chat_id"`
		ReplyToMessageID string `json:"reply_to_message_id"`
		MessageID        string `json:"message_id"`
	}
	if err := json.Unmarshal(recOK.Body.Bytes(), &resp); err != nil || !resp.OK || resp.MessageID != "om_reply_sent_99" || resp.ChatID != "oc_feedback_01" {
		t.Fatalf("unexpected response: %s", recOK.Body.String())
	}
	sp.feishu.mu.RLock()
	lastChat := sp.feishu.lastInbound.chatID
	lastP2P := sp.feishu.lastP2P.chatID
	sp.feishu.mu.RUnlock()
	if lastChat != "" || lastP2P != "" {
		t.Fatalf("confirmed feedback reply must not pollute routing: lastInbound=%q lastP2P=%q", lastChat, lastP2P)
	}
}

func TestGitForgeControlledCommentGitHubAndCLIProvider(t *testing.T) {
	t.Parallel()
	// 1. Thread parser checks.
	if p, r, n, ok := parseForgeThread("github:octo/collie#42"); !ok || p != "github" || r != "octo/collie" || n != 42 {
		t.Fatalf("parseForgeThread github failed: %q %q %d %v", p, r, n, ok)
	}
	if p, r, n, ok := parseForgeThread("myforge:team/sub/repo!18"); !ok || p != "myforge" || r != "team/sub/repo" || n != 18 {
		t.Fatalf("parseForgeThread bang failed: %q %q %d %v", p, r, n, ok)
	}
	for _, bad := range []string{"", "github:octo/collie", "github:badrepo#1", "github:octo/collie#0", "no-colon#1"} {
		if _, _, _, ok := parseForgeThread(bad); ok {
			t.Fatalf("parseForgeThread(%q) should fail", bad)
		}
	}

	// 2. GitHubProvider controlled comment via HTTP API.
	const testPAT = "ghp_secret_comment_token_12345"
	var ghHits atomic.Int32
	var gotPath, gotAuth, gotBody string
	var forceForbidden atomic.Bool
	ghSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Method == http.MethodPost && r.URL.Path == "/repos/octo/collie/issues/42/comments" {
			ghHits.Add(1)
			gotPath = r.URL.Path
			gotAuth = r.Header.Get("Authorization")
			if forceForbidden.Load() {
				w.WriteHeader(http.StatusForbidden)
				_, _ = w.Write([]byte(`{"message":"Resource not accessible by personal access token"}`))
				return
			}
			var payload struct {
				Body string `json:"body"`
			}
			_ = json.NewDecoder(r.Body).Decode(&payload)
			gotBody = payload.Body
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"id":987654,"html_url":"https://github.com/octo/collie/pull/42#issuecomment-987654"}`))
			return
		}
		_, _ = w.Write([]byte(`{"login":"octo"}`))
	}))
	defer ghSrv.Close()

	dir := t.TempDir()
	// Also place a local CLIProvider manifest with comment_cmd (stdin_body: true).
	provDir := filepath.Join(dir, "providers")
	if err := os.MkdirAll(provDir, 0o700); err != nil {
		t.Fatalf("mkdir providers: %v", err)
	}
	scriptPath := filepath.Join(dir, "mock-forge-cli.sh")
	capturePath := filepath.Join(dir, "captured-comment.txt")
	script := `#!/bin/sh
if [ "$1" = "probe" ]; then
  echo '{"user":"alice","repos":["team/service"]}'
  exit 0
fi
if [ "$1" = "comment" ]; then
  BODY="$(cat)"
  printf '%s|%s|%s|%s\n' "$2" "$3" "$4" "$BODY" > "` + capturePath + `"
  echo '{"id":"cmt-77","url":"https://git.example.com/team/service/merge_requests/19#note_77"}'
  exit 0
fi
echo '[]'
`
	if err := os.WriteFile(scriptPath, []byte(script), 0o755); err != nil {
		t.Fatalf("write mock cli: %v", err)
	}
	manifest := map[string]any{
		"kind":     "myforge",
		"name":     "MyForge",
		"term_pr":  "MR",
		"probe":    map[string]any{"cmd": []string{scriptPath, "probe"}, "user_path": "user"},
		"poll_cmd": []string{scriptPath, "poll"},
		"comment_cmd": map[string]any{
			"cmd":        []string{scriptPath, "comment", "{{repo}}", "{{mr}}", "{{kind}}"},
			"stdin_body": true,
			"id_path":    "id",
			"url_path":   "url",
		},
	}
	mBytes, _ := json.Marshal(manifest)
	if err := os.WriteFile(filepath.Join(provDir, "myforge.json"), mBytes, 0o600); err != nil {
		t.Fatalf("write manifest: %v", err)
	}

	s := &Server{token: "tok-forge", home: dir}
	sp, err := newSuper(s, dir)
	if err != nil {
		t.Fatalf("newSuper: %v", err)
	}
	defer func() {
		sp.feishuOnboard.Close()
		sp.db.Close()
	}()
	s.sup = sp

	ghAdapter := sp.integrations.Adapter(githubKind).(*GitForgeAdapter)
	ghProv := ghAdapter.provider.(*GitHubProvider)
	ghProv.baseURL = ghSrv.URL
	ghProv.httpClient = ghSrv.Client()

	en := true
	if _, err := sp.integrations.Save(githubKind, SaveRequest{
		Enabled: &en,
		Config:  json.RawMessage(`{"auth_mode":"pat","repos":["octo/collie"],"handle_mode":"advise","configured":true}`),
		Secrets: map[string]string{"pat": testPAT},
	}); err != nil {
		t.Fatalf("save github config: %v", err)
	}
	if _, err := sp.integrations.Save("myforge", SaveRequest{
		Enabled: &en,
		Config:  json.RawMessage(`{"auth_mode":"cli","repos":["team/service"],"handle_mode":"advise","configured":true}`),
	}); err != nil {
		t.Fatalf("save myforge config: %v", err)
	}

	mux := http.NewServeMux()
	s.superRoutes(mux)

	// Guardrails: unauth, missing confirm, provider mismatch.
	recUnauth := httptest.NewRecorder()
	mux.ServeHTTP(recUnauth, authedReq(http.MethodPost, "/api/super/integrations/github/comment", `{"thread":"github:octo/collie#42","body":"LGTM","confirm":true}`, ""))
	if recUnauth.Code != http.StatusUnauthorized {
		t.Fatalf("unauth status=%d, want 401", recUnauth.Code)
	}
	recNoConfirm := httptest.NewRecorder()
	mux.ServeHTTP(recNoConfirm, authedReq(http.MethodPost, "/api/super/integrations/github/comment", `{"thread":"github:octo/collie#42","body":"LGTM"}`, s.token))
	if recNoConfirm.Code != http.StatusBadRequest {
		t.Fatalf("no confirm status=%d, want 400", recNoConfirm.Code)
	}
	recMismatch := httptest.NewRecorder()
	mux.ServeHTTP(recMismatch, authedReq(http.MethodPost, "/api/super/integrations/github/comment", `{"thread":"myforge:team/service#19","body":"LGTM","confirm":true}`, s.token))
	if recMismatch.Code != http.StatusBadRequest {
		t.Fatalf("provider mismatch status=%d, want 400", recMismatch.Code)
	}
	if ghHits.Load() != 0 {
		t.Fatalf("rejected comments must not call GitHub, hits=%d", ghHits.Load())
	}

	// Confirmed GitHub comment succeeds.
	recGH := httptest.NewRecorder()
	mux.ServeHTTP(recGH, authedReq(http.MethodPost, "/api/super/integrations/github/comment", `{"thread":"github:octo/collie#42","body":"已核对单测与边界，建议合并。","confirm":true}`, s.token))
	if recGH.Code != http.StatusOK {
		t.Fatalf("github comment status=%d body=%s", recGH.Code, recGH.Body.String())
	}
	if ghHits.Load() != 1 || gotPath != "/repos/octo/collie/issues/42/comments" || gotAuth != "Bearer "+testPAT || gotBody != "已核对单测与边界，建议合并。" {
		t.Fatalf("unexpected github call: hits=%d path=%q body=%q", ghHits.Load(), gotPath, gotBody)
	}
	if strings.Contains(recGH.Body.String(), testPAT) {
		t.Fatalf("response leaked PAT!")
	}
	var ghResp struct {
		OK        bool   `json:"ok"`
		CommentID string `json:"comment_id"`
		URL       string `json:"url"`
	}
	_ = json.Unmarshal(recGH.Body.Bytes(), &ghResp)
	if !ghResp.OK || ghResp.CommentID != "987654" || !strings.Contains(ghResp.URL, "#issuecomment-987654") {
		t.Fatalf("unexpected github response: %s", recGH.Body.String())
	}

	// Read-only PAT 403 yields clear actionable error without leaking secret.
	forceForbidden.Store(true)
	rec403 := httptest.NewRecorder()
	mux.ServeHTTP(rec403, authedReq(http.MethodPost, "/api/super/integrations/github/comment", `{"thread":"github:octo/collie#42","body":"retry","confirm":true}`, s.token))
	if rec403.Code != http.StatusBadRequest || !strings.Contains(rec403.Body.String(), "评论写入权限") || strings.Contains(rec403.Body.String(), testPAT) {
		t.Fatalf("403 error handling unexpected: code=%d body=%s", rec403.Code, rec403.Body.String())
	}

	// Confirmed CLIProvider comment succeeds with multiline stdin_body.
	recCLI := httptest.NewRecorder()
	mux.ServeHTTP(recCLI, authedReq(http.MethodPost, "/api/super/integrations/myforge/comment", `{"thread":"myforge:team/service#19","body":"第一行建议\n第二行确认","confirm":true}`, s.token))
	if recCLI.Code != http.StatusOK {
		t.Fatalf("cli comment status=%d body=%s", recCLI.Code, recCLI.Body.String())
	}
	var cliResp struct {
		OK        bool   `json:"ok"`
		Provider  string `json:"provider"`
		CommentID string `json:"comment_id"`
		URL       string `json:"url"`
	}
	_ = json.Unmarshal(recCLI.Body.Bytes(), &cliResp)
	if !cliResp.OK || cliResp.Provider != "myforge" || cliResp.CommentID != "cmt-77" || !strings.Contains(cliResp.URL, "note_77") {
		t.Fatalf("unexpected cli comment response: %s", recCLI.Body.String())
	}
	captured, err := os.ReadFile(capturePath)
	if err != nil {
		t.Fatalf("read captured cli comment: %v", err)
	}
	if strings.TrimSpace(string(captured)) != "team/service|19|mr|第一行建议\n第二行确认" {
		t.Fatalf("captured cli comment=%q", string(captured))
	}
}

func TestCLIFeishuReplyAndForgeCommentParsers(t *testing.T) {
	t.Parallel()
	// feishu reply refuses without --confirm.
	if _, err := parseFeishuReply([]string{"--chat", "oc_1", "--reply-to", "om_1", "hello"}, nil, true); err == nil {
		t.Fatalf("parseFeishuReply should fail without --confirm")
	}
	// feishu reply succeeds with --confirm and piped stdin.
	body, err := parseFeishuReply([]string{"--chat", "oc_1", "--reply-to", "om_1", "--confirm", "-"}, bytes.NewBufferString("多行\n回群草稿"), false)
	if err != nil {
		t.Fatalf("parseFeishuReply stdin: %v", err)
	}
	if body["chat_id"] != "oc_1" || body["reply_to_message_id"] != "om_1" || body["confirm"] != true || body["text"] != "多行\n回群草稿" {
		t.Fatalf("unexpected parseFeishuReply body: %+v", body)
	}

	// forge comment refuses without --confirm.
	if _, _, err := parseForgeComment([]string{"--thread", "github:octo/collie#7", "LGTM"}, nil, true); err == nil {
		t.Fatalf("parseForgeComment should fail without --confirm")
	}
	// forge comment parses --thread and piped stdin.
	prov, fbody, err := parseForgeComment([]string{"--thread", "github:octo/collie#7", "--confirm", "-"}, bytes.NewBufferString("确认后发表的评论"), false)
	if err != nil {
		t.Fatalf("parseForgeComment thread: %v", err)
	}
	if prov != "github" || fbody["repo"] != "octo/collie" || fbody["number"] != 7 || fbody["confirm"] != true || fbody["body"] != "确认后发表的评论" {
		t.Fatalf("unexpected parseForgeComment result: prov=%q body=%+v", prov, fbody)
	}
	// forge comment parses explicit --provider/--repo/--number.
	prov2, fbody2, err := parseForgeComment([]string{"--provider", "myforge", "--repo", "org/app", "--number", "15", "--kind", "mr", "--confirm", "looks good"}, nil, true)
	if err != nil {
		t.Fatalf("parseForgeComment explicit flags: %v", err)
	}
	if prov2 != "myforge" || fbody2["repo"] != "org/app" || fbody2["number"] != 15 || fbody2["kind"] != "mr" || fbody2["body"] != "looks good" {
		t.Fatalf("unexpected explicit parseForgeComment: prov=%q body=%+v", prov2, fbody2)
	}
	// Conflicting --provider and --thread fails.
	if _, _, err := parseForgeComment([]string{"--provider", "github", "--thread", "myforge:org/app#15", "--confirm", "hi"}, nil, true); err == nil {
		t.Fatalf("conflicting provider and thread should fail")
	}
}

func TestRouterDocIncludesRouteAConfirmationGuardrails(t *testing.T) {
	t.Parallel()
	for _, want := range []string{
		"feishu reply --chat",
		"forge comment --thread",
		"--confirm",
		"NEVER call without explicit human confirmation",
	} {
		if !strings.Contains(routerDoc, want) {
			t.Fatalf("routerDoc missing Route A guidance %q", want)
		}
	}
}
