package collie

import (
	"database/sql"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func initFullAgySummaryDB(t *testing.T, root string, rows []struct {
	id, title, preview, uri, status, agentName, parentID string
	depth, killed                                        int
}) {
	t.Helper()
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", filepath.Join(root, "conversation_summaries.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	_, err = db.Exec(`CREATE TABLE conversation_summaries (
		conversation_id TEXT PRIMARY KEY,
		title TEXT NOT NULL DEFAULT '',
		preview TEXT NOT NULL DEFAULT '',
		workspace_uris TEXT NOT NULL,
		status TEXT NOT NULL DEFAULT '',
		not_fully_idle INTEGER NOT NULL DEFAULT 0,
		agent_name TEXT NOT NULL DEFAULT '',
		parent_conversation_id TEXT NOT NULL DEFAULT '',
		nesting_depth INTEGER NOT NULL DEFAULT 0,
		killed INTEGER NOT NULL DEFAULT 0,
		last_modified_time TEXT NOT NULL,
		last_user_input_time TEXT NOT NULL
	)`)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Format("2006-01-02 15:04:05")
	for _, r := range rows {
		uris, _ := json.Marshal([]string{r.uri})
		_, err := db.Exec(`INSERT INTO conversation_summaries VALUES (?, ?, ?, ?, ?, 0, ?, ?, ?, ?, ?, ?)`,
			r.id, r.title, r.preview, string(uris), r.status, r.agentName, r.parentID, r.depth, r.killed, now, now)
		if err != nil {
			t.Fatal(err)
		}
	}
}

func writeAgyTranscript(t *testing.T, brain, id, firstLine string) string {
	t.Helper()
	p := filepath.Join(brain, id, ".system_generated", "logs", "transcript.jsonl")
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(firstLine+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestAgyGuessExcludesSubagentsAndDisambiguatesWorkers(t *testing.T) {
	home := t.TempDir()
	cliRoot := filepath.Join(home, ".gemini", "antigravity-cli")
	brain := filepath.Join(cliRoot, "brain")
	cwd := filepath.Join(home, "workspace")
	if err := os.MkdirAll(cwd, 0o755); err != nil {
		t.Fatal(err)
	}

	claimed.Lock()
	origClaimed := claimed.m
	claimed.m = map[string]string{}
	claimed.Unlock()
	forgetAgyIndex()
	forgetDesktopRows()
	t.Cleanup(func() {
		claimed.Lock()
		claimed.m = origClaimed
		claimed.Unlock()
		forgetAgyIndex()
		forgetDesktopRows()
	})

	const (
		parentID    = "52fca17c-2887-4564-9d29-d4a20d7c9049"
		subDBID     = "fb4b2beb-3cdc-4f35-aab9-83bc4ffd703b"
		subUnmarkID = "b3cb6af3-853f-43cc-b2dc-4aa26b23bbee"
		workerBID   = "dab9b5f5-1111-2222-3333-444455556666"
		workerAID   = "fb56f1e5-1111-2222-3333-444455556666"
		routerID    = "847a7974-1111-2222-3333-444455556666"
	)

	now := time.Now().UTC()
	initFullAgySummaryDB(t, cliRoot, []struct {
		id, title, preview, uri, status, agentName, parentID string
		depth, killed                                        int
	}{
		{parentID, "Draw avatar", "Draw avatar", "file://" + cwd, "CASCADE_RUN_STATUS_RUNNING", "", "", 0, 0},
		// DB-marked subagent (mirrors real Antigravity schema: agent_name + parent_conversation_id + nesting_depth=1).
		{subDBID, "", "", "file://" + cwd, "CASCADE_RUN_STATUS_IDLE", "image-generator", parentID, 1, 0},
		// Defense-in-depth: subagent whose DB row somehow lacks parent/depth flags, only line-0 SYSTEM_MESSAGE.
		{subUnmarkID, "", "", "file://" + cwd, "CASCADE_RUN_STATUS_IDLE", "", "", 0, 0},
		{workerBID, "Worker B", "Worker B", "file://" + cwd, "CASCADE_RUN_STATUS_RUNNING", "", "", 0, 0},
		{routerID, "Router", "Router", "file://" + cwd, "CASCADE_RUN_STATUS_IDLE", "", "", 0, 0},
	})

	lineJSON := func(typ, src string, at time.Time, content string) string {
		b, _ := json.Marshal(map[string]any{
			"step_index": 0,
			"type":       typ,
			"source":     src,
			"created_at": at.Format(time.RFC3339),
			"content":    content,
		})
		return string(b)
	}

	parentLog := writeAgyTranscript(t, brain, parentID,
		lineJSON("USER_INPUT", "USER_EXPLICIT", now.Add(2*time.Second), `<USER_REQUEST>
[collie] 1 new event
  You are worker "design-avatar", managed by Collie (thread chat:main). Your task: draw
</USER_REQUEST>`))
	subDBLog := writeAgyTranscript(t, brain, subDBID,
		lineJSON("SYSTEM_MESSAGE", "SYSTEM", now.Add(1*time.Second), "<SYSTEM_MESSAGE>\n[Message] sender="+parentID+" content=Generate image"))
	subUnmarkLog := writeAgyTranscript(t, brain, subUnmarkID,
		lineJSON("SYSTEM_MESSAGE", "SYSTEM", now.Add(1*time.Second), "<SYSTEM_MESSAGE>\n[Message] sender="+parentID+" content=Generate image 2"))
	workerBLog := writeAgyTranscript(t, brain, workerBID,
		lineJSON("USER_INPUT", "USER_EXPLICIT", now.Add(3*time.Second), `<USER_REQUEST>
[collie] 1 new event
  You are worker "worker-b", managed by Collie (thread chat:main). Your task: B
</USER_REQUEST>`))
	routerLog := writeAgyTranscript(t, brain, routerID,
		lineJSON("USER_INPUT", "USER_EXPLICIT", now.Add(1*time.Second), "<USER_REQUEST>\n[collie] Session start. Read ROUTER.md\n</USER_REQUEST>"))

	// Simulate Linux dir mtime drift: writing generated images into parent's
	// brain dir pushes its ModTime later than the subagent dirs.
	earlier := now.Add(1 * time.Second)
	later := now.Add(30 * time.Second)
	_ = os.Chtimes(filepath.Join(brain, subDBID), earlier, earlier)
	_ = os.Chtimes(filepath.Join(brain, subUnmarkID), earlier, earlier)
	_ = os.Chtimes(filepath.Join(brain, parentID), later, later)

	h := &Herdr{named: map[string]string{}, names: map[string]string{}}
	h.Remember("w1:p1", "design-avatar")
	h.Remember("w2:p1", "worker-a")
	h.Remember("w3:p1", "worker-b")
	h.Remember("w4:p1", routerName)
	srv := &Server{
		home:  home,
		herdr: h,
		started: map[string]time.Time{
			"w1:p1": now,
			"w2:p1": now,
			"w3:p1": now,
			"w4:p1": now,
		},
	}

	// 1) Parent worker MUST match parentLog and NEVER match either subagent log.
	if got := srv.guessAgy(brain, "w1:p1", cwd); got != parentLog {
		t.Fatalf("guessAgy(design-avatar) = %q, want parent %q (subDB=%q subUnmark=%q)", got, parentLog, subDBLog, subUnmarkLog)
	}

	// 2) Concurrent sibling worker-a whose own transcript has not landed yet
	// MUST NOT steal worker-b's transcript or the router's transcript.
	if got := srv.guessAgy(brain, "w2:p1", cwd); got != "" {
		t.Fatalf("guessAgy(worker-a before own transcript) = %q, want empty (must not steal worker-b or router)", got)
	}

	// 3) worker-b pairs cleanly with workerBLog; router pairs cleanly with routerLog.
	if got := srv.guessAgy(brain, "w3:p1", cwd); got != workerBLog {
		t.Fatalf("guessAgy(worker-b) = %q, want %q", got, workerBLog)
	}
	if got := srv.guessAgy(brain, "w4:p1", cwd); got != routerLog {
		t.Fatalf("guessAgy(router) = %q, want %q", got, routerLog)
	}

	// 4) Once worker-a's transcript lands, worker-a pairs cleanly with it.
	db, err := sql.Open("sqlite", filepath.Join(cliRoot, "conversation_summaries.db"))
	if err != nil {
		t.Fatal(err)
	}
	uris, _ := json.Marshal([]string{"file://" + cwd})
	_, err = db.Exec(`INSERT INTO conversation_summaries VALUES (?, 'Worker A', 'Worker A', ?, 'CASCADE_RUN_STATUS_RUNNING', 0, '', '', 0, 0, ?, ?)`,
		workerAID, string(uris), now.Format("2006-01-02 15:04:05"), now.Format("2006-01-02 15:04:05"))
	db.Close()
	if err != nil {
		t.Fatal(err)
	}
	workerALog := writeAgyTranscript(t, brain, workerAID,
		lineJSON("USER_INPUT", "USER_EXPLICIT", now.Add(4*time.Second), `<USER_REQUEST>
[collie] 1 new event
  You are worker "worker-a", managed by Collie (thread chat:main). Your task: A
</USER_REQUEST>`))
	forgetAgyIndex()
	if got := srv.guessAgy(brain, "w2:p1", cwd); got != workerALog {
		t.Fatalf("guessAgy(worker-a after transcript created) = %q, want %q", got, workerALog)
	}

	// 5) recentAgyCLI() MUST exclude both DB-marked and line-0 SYSTEM_MESSAGE subagents.
	rows := srv.recentAgyCLI()
	for _, r := range rows {
		if r.session == subDBID || r.session == subUnmarkID {
			t.Fatalf("recentAgyCLI leaked subagent session %q", r.session)
		}
	}

	// 6) locateExternal() for an external agy CLI process MUST NOT select subagent transcripts.
	e := &External{Kind: "agy", Cwd: cwd, Started: now.Add(-time.Second)}
	srv.locateExternal(e, &proc{args: []string{"agy"}})
	if e.Session == subDBID || e.Session == subUnmarkID {
		t.Fatalf("locateExternal leaked subagent session %q", e.Session)
	}
}

func TestToolSummaryAndSubagentTranscriptParsing(t *testing.T) {
	// 1) Antigravity transcript: invoke_subagent, generate_image, send_message, update_steps, schedule, custom MCP tool.
	agyMsgs := parse(t, "agy", strings.Join([]string{
		`{"type":"USER_INPUT","source":"USER_EXPLICIT","content":"<USER_REQUEST>\ncreate icons\n</USER_REQUEST>"}`,
		`{"type":"PLANNER_RESPONSE","source":"MODEL","tool_calls":[` +
			`{"name":"invoke_subagent","args":{"TypeName":"\"image-generator\"","Role":"\"Avatar 1 Generator\"","Model":"\"inherit\"","Prompt":"\"Generate a square 1:1 avatar icon.\\nSecond line detail.\""}},` +
			`{"name":"generate_image","args":{"AspectRatio":"\"1:1\"","ImageName":"\"avatar_1\"","Prompt":"\"Clean flat editorial vector illustration of a collie\""}},` +
			`{"name":"send_message","args":{"Recipient":"\"52fca17c-2887-4564-9d29-d4a20d7c9049\"","Message":"\"Generated image: /tmp/avatar_1.png\\nDetails inside.\""}},` +
			`{"name":"update_steps","args":{"steps":"[{\"title\":\"Inspect repo\",\"status\":\"done\"},{\"title\":\"Render cards\",\"status\":\"in_progress\"},{\"title\":\"Run tests\",\"status\":\"pending\"}]"}}` +
			`]}`,
		`{"type":"GENERIC","source":"MODEL","content":"Created At: 2026-11-28T03:24:26+08:00\nCompleted At: 2026-11-28T03:24:34+08:00\nCreated the following subagent:\n{\n  \"conversationId\": \"fb4b2beb-3cdc-4f35-aab9-83bc4ffd703b\",\n  \"subagentDescriptor\": {\n    \"typeName\": \"image-generator\",\n    \"role\": \"Avatar 1 Generator\"\n  }\n}"}`,
		`{"type":"GENERIC","source":"MODEL","content":"Created At: x\nGenerated image is saved"}`,
		`{"type":"GENERIC","source":"MODEL","content":"Created At: x\nMessage sent"}`,
		`{"type":"GENERIC","source":"MODEL","content":"Created At: x\nOK"}`,
		`{"type":"PLANNER_RESPONSE","source":"MODEL","tool_calls":[` +
			`{"name":"schedule","args":{"DurationSeconds":30,"Prompt":"\"Check build status\""}},` +
			`{"name":"custom_sync_tool","args":{"DryRun":true,"Limit":50,"Mode":"\"fast\""}}` +
			`]}`,
		`{"type":"GENERIC","source":"MODEL","content":"Created At: x\nscheduled"}`,
		`{"type":"GENERIC","source":"MODEL","content":"Created At: x\nsynced"}`,
	}, "\n")+"\n")

	if len(agyMsgs) != 7 {
		t.Fatalf("got %d agy messages, want 7: %+v", len(agyMsgs), agyMsgs)
	}

	// invoke_subagent
	sub := agyMsgs[1]
	if sub.Subagent == nil {
		t.Fatalf("invoke_subagent missing Subagent metadata: %+v", sub)
	}
	if sub.Subagent.Type != "image-generator" || sub.Subagent.Role != "Avatar 1 Generator" || sub.Subagent.Model != "" {
		t.Fatalf("unexpected subagent meta: %+v", sub.Subagent)
	}
	if sub.Subagent.ID != "fb4b2beb-3cdc-4f35-aab9-83bc4ffd703b" {
		t.Fatalf("subagent ID = %q, want fb4b2beb-3cdc-4f35-aab9-83bc4ffd703b", sub.Subagent.ID)
	}
	if !strings.Contains(sub.Subagent.Prompt, "Second line detail.") || strings.HasPrefix(sub.Input, "{") || !strings.Contains(sub.Input, "Generate a square 1:1 avatar icon.") {
		t.Fatalf("unexpected subagent input/prompt: input=%q prompt=%q", sub.Input, sub.Subagent.Prompt)
	}

	// generate_image
	img := agyMsgs[2]
	if strings.HasPrefix(img.Input, "{") || !strings.Contains(img.Input, "avatar_1 (1:1)") || !strings.Contains(img.Input, "Clean flat editorial") {
		t.Fatalf("generate_image input = %q, want readable summary without raw JSON", img.Input)
	}
	if !strings.Contains(img.Detail, "AspectRatio: 1:1") || !strings.Contains(img.Detail, "ImageName: avatar_1") || !strings.Contains(img.Detail, "Clean flat editorial") {
		t.Fatalf("generate_image detail = %q, want formatted params", img.Detail)
	}

	// send_message
	snd := agyMsgs[3]
	if strings.HasPrefix(snd.Input, "{") || !strings.Contains(snd.Input, "→ 52fca17c") || !strings.Contains(snd.Input, "Generated image:") {
		t.Fatalf("send_message input = %q, want shortened recipient + preview", snd.Input)
	}
	if !strings.Contains(snd.Detail, "52fca17c-2887-4564-9d29-d4a20d7c9049") || !strings.Contains(snd.Detail, "Details inside.") {
		t.Fatalf("send_message detail = %q, want full recipient and message", snd.Detail)
	}

	// update_steps
	steps := agyMsgs[4]
	if steps.Input != "1/3 · Render cards" || !strings.Contains(steps.Detail, "[x] Inspect repo") || !strings.Contains(steps.Detail, "[>] Render cards") {
		t.Fatalf("update_steps = input:%q detail:%q", steps.Input, steps.Detail)
	}

	// schedule
	sch := agyMsgs[5]
	if sch.Input != "in 30s · Check build status" || !strings.Contains(sch.Detail, "DurationSeconds: 30") {
		t.Fatalf("schedule = input:%q detail:%q", sch.Input, sch.Detail)
	}

	// unknown generic tool: never raw JSON
	cust := agyMsgs[6]
	if strings.HasPrefix(cust.Input, "{") || !strings.Contains(cust.Input, "Mode: fast") || !strings.Contains(cust.Detail, "Limit: 50") {
		t.Fatalf("custom tool = input:%q detail:%q", cust.Input, cust.Detail)
	}

	// 2) Claude Code Agent subagent tool
	claudeMsgs := parse(t, "claude", `{"type":"assistant","message":{"id":"m1","content":[{"type":"tool_use","id":"t1","name":"Agent","input":{"subagent_type":"Explore","description":"Find auth middleware","prompt":"Search internal/ for auth middleware handlers.","model":"haiku"}}]}}
{"type":"user","message":{"content":[{"type":"tool_result","tool_use_id":"t1","content":"Found auth in server.go","is_error":false}]}}
`)
	if len(claudeMsgs) != 1 || claudeMsgs[0].Subagent == nil {
		t.Fatalf("claude Agent subagent = %+v", claudeMsgs)
	}
	if cs := claudeMsgs[0].Subagent; cs.Type != "Explore" || cs.Role != "Find auth middleware" || cs.Model != "haiku" || !strings.Contains(cs.Prompt, "auth middleware handlers") {
		t.Fatalf("claude SubagentCall = %+v", cs)
	}

	// 3) Codex spawn_agent subagent tool
	codexMsgs := parse(t, "codex", `{"type":"response_item","payload":{"type":"function_call","name":"spawn_agent","arguments":"{\"agent_type\":\"worker\",\"nickname\":\"parser\",\"message\":\"Audit parser edge cases\"}","call_id":"c1"}}
{"type":"response_item","payload":{"type":"function_call_output","call_id":"c1","output":"{\"agentId\":\"codex-sub-01\"}"}}
`)
	if len(codexMsgs) != 1 || codexMsgs[0].Subagent == nil {
		t.Fatalf("codex spawn_agent = %+v", codexMsgs)
	}
	if xs := codexMsgs[0].Subagent; xs.Type != "worker" || xs.Role != "parser" || xs.ID != "codex-sub-01" {
		t.Fatalf("codex SubagentCall = %+v", xs)
	}
}

func TestWebSubagentAndToolRenderingContracts(t *testing.T) {
	if _, err := exec.LookPath("node"); err != nil {
		t.Skip("node not installed")
	}
	script := `
const fs = require("fs");
const vm = require("vm");
const assert = require("assert");

const appSrc = fs.readFileSync("web/app.js", "utf8");
const cssSrc = fs.readFileSync("web/app.css", "utf8");
const demoSrc = fs.readFileSync("scripts/demo/demo.js", "utf8");

// 1) CSS contracts: monochrome design tokens, reduced-motion guard, no headings in summary
assert.ok(cssSrc.includes(".tool.subagent") && cssSrc.includes(".tool.subagent > summary.sub-sum"), "subagent card CSS rules must exist");
assert.ok(cssSrc.includes(".tool-detail") && cssSrc.includes(".tool-sec-h"), "generic tool detail CSS rules must exist");
assert.ok(cssSrc.includes(".tool.subagent::details-content") && cssSrc.includes("interpolate-size: allow-keywords"), "progressive details-content disclosure rule must exist");
assert.ok(cssSrc.includes("prefers-reduced-motion: no-preference") && cssSrc.includes("transition: rotate .16s ease"), "chevron & disclosure transitions must respect prefers-reduced-motion");
assert.ok(demoSrc.includes("invoke_subagent") && demoSrc.includes("Migration Auditor"), "demo.js should showcase subagent card");

// 2) Extract tool rendering functions from web/app.js and verify rendered HTML
const startIdx = appSrc.indexOf("  const SUBAGENT_TOOL_RE =");
const endIdx = appSrc.indexOf("  function drawThread(", startIdx);
assert.ok(startIdx > 0 && endIdx > startIdx, "renderToolItem block must be present in web/app.js");

const snippet = appSrc.slice(startIdx, endIdx);
const sandbox = {
  esc: (s) => String(s ?? "").replace(/&/g, "&amp;").replace(/</g, "&lt;").replace(/>/g, "&gt;").replace(/"/g, "&quot;"),
};
vm.runInNewContext(snippet + "\nthis.__exports = { normalizeToolView, renderToolItem };", sandbox);
const { renderToolItem } = sandbox.__exports;

// 2a) Structured subagent call
const subHtml = renderToolItem({
  kind: "tool",
  name: "invoke_subagent",
  input: "Generate a square 1:1 avatar icon.",
  output: 'Created the following subagent:\n{"conversationId":"fb4b2beb-3cdc-4f35-aab9-83bc4ffd703b"}',
  subagent: {
    type: "image-generator",
    role: "Avatar 1 Generator",
    prompt: "Generate a square 1:1 avatar icon.\nKeep faces centered.",
    id: "fb4b2beb-3cdc-4f35-aab9-83bc4ffd703b",
  },
}, 0, new Set());
assert.ok(subHtml.includes('class="tool subagent  has-body"'), "subagent renders as collapsible card");
assert.ok(subHtml.includes('<span class="sub-role">Avatar 1 Generator</span>'), "renders role title");
assert.ok(subHtml.includes('<span class="sub-tag">image-generator</span>'), "renders type tag");
assert.ok(subHtml.includes('<span class="sub-brief">Generate a square 1:1 avatar icon.</span>'), "renders 1-line brief");
assert.ok(subHtml.includes("Keep faces centered.") && subHtml.includes("fb4b2beb"), "expanded body holds full prompt and short id");
assert.ok(!/<summary[\s\S]*?<h[1-6]\b/i.test(subHtml), "must not place headings inside <summary>");
assert.ok(!subHtml.includes(' name='), "must not make subagent details mutually exclusive");

// 2b) Legacy raw-JSON fallback for invoke_subagent (e.g. older remote node)
const legacySubHtml = renderToolItem({
  kind: "tool",
  name: "invoke_subagent",
  input: JSON.stringify({ TypeName: "\"research\"", Role: "\"Code Auditor\"", Prompt: "\"Scan routes for auth checks\"" }),
  output: "",
}, 1, new Set());
assert.ok(legacySubHtml.includes("tool subagent pending has-body"), "legacy raw JSON invoke_subagent upgrades to pending subagent card");
assert.ok(legacySubHtml.includes("Code Auditor") && legacySubHtml.includes("research") && legacySubHtml.includes("运行中"), "legacy subagent shows role, type, running state");
assert.ok(!legacySubHtml.includes('{"TypeName"'), "raw JSON must never leak into summary");

// 2c) Legacy raw-JSON fallback for generate_image & send_message
const legacyImgHtml = renderToolItem({
  kind: "tool",
  name: "generate_image",
  input: JSON.stringify({ AspectRatio: "1:1", ImageName: "avatar_1", Prompt: "Minimal geometric emblem" }),
  output: "Generated image is saved",
}, 2, new Set());
assert.ok(legacyImgHtml.includes('<span class="targ">avatar_1 (1:1) · Minimal geometric emblem</span>'), "generate_image legacy raw JSON renders readable summary");
assert.ok(legacyImgHtml.includes("tool-detail") && legacyImgHtml.includes("AspectRatio: 1:1"), "generate_image expanded view preserves full parameters");
assert.ok(!legacyImgHtml.includes('{"AspectRatio"'), "raw JSON must never appear in generate_image row");

const legacySendHtml = renderToolItem({
  kind: "tool",
  name: "send_message",
  input: JSON.stringify({ Recipient: "52fca17c-2887-4564-9d29-d4a20d7c9049", Message: "Done; saved at /tmp/out.png" }),
  output: "Message sent",
}, 3, new Set());
assert.ok(legacySendHtml.includes("→ 52fca17c · Done; saved at /tmp/out.png"), "send_message legacy raw JSON renders readable summary");
assert.ok(legacySendHtml.includes("52fca17c-2887-4564-9d29-d4a20d7c9049"), "send_message expanded detail retains full recipient");

// 2d) Simple tool without detail keeps clean lightweight output <pre>
const simpleHtml = renderToolItem({ kind: "tool", name: "Bash", input: "go test ./...", output: "ok" }, 4, new Set());
assert.ok(simpleHtml.includes("<pre>ok</pre>") && !simpleHtml.includes("tool-detail"), "simple tool stays compact without redundant detail panel");
`
	cmd := exec.Command("node", "-e", script)
	cmd.Dir = filepath.Join("..", "..")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("web subagent/tool render contracts failed: %v\n%s", err, out)
	}
}
