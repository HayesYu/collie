package collie

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// SubagentCall carries structured metadata for a subagent invocation tool call
// (Antigravity invoke_subagent/swarm, Claude Agent/Task, Codex spawn_agent).
type SubagentCall struct {
	Type   string `json:"type,omitempty"`
	Role   string `json:"role,omitempty"`
	Prompt string `json:"prompt,omitempty"`
	Model  string `json:"model,omitempty"`
	ID     string `json:"id,omitempty"`
}

// Message is one entry of the conversation view that wraps an agent terminal.
type Message struct {
	Kind string `json:"kind"` // user | assistant | tool | ask
	Text string `json:"text,omitempty"`
	Name string `json:"name,omitempty"`
	// ask: the agent's question to the human (Claude's AskUserQuestion, or
	// the Claude app's end-of-turn box); Options are its answers' labels,
	// Output the answer once given, Input a recap that came with it.
	Options  []string      `json:"options,omitempty"`
	Input    string        `json:"input,omitempty"`
	Detail   string        `json:"detail,omitempty"`
	Output   string        `json:"output,omitempty"`
	Error    bool          `json:"error,omitempty"`
	Subagent *SubagentCall `json:"subagent,omitempty"`
	at       int64         // where in the log its line starts
}

// transcript incrementally parses an agent's JSONL session log.
type transcript struct {
	path    string
	format  string // claude | codex | agy
	offset  int64
	msgs    []*Message
	tools   map[string]*Message
	pending []*Message // agy: tool calls waiting for their output, in order
	lastID  string
	dropped int   // messages trimmed off the front, so positions stay stable
	partial bool  // offset may be mid-line: skip to the next line first
	began   int64 // where reading started (0: the top of the log)
	lineAt  int64 // where the line being parsed starts
	limit   int64 // stop reading here (0: at the end)
}

const (
	maxMessages = 60 // what a viewer gets first; older ones load on demand
	maxOutput   = 1600
	maxInput    = 400
	maxDetail   = 2400
)

func newTranscript(path, format string) *transcript {
	if format == "agy" {
		// transcript.jsonl cuts long messages ("<truncated 3142 bytes>");
		// Antigravity keeps the whole of them, line for line, next to it.
		if full := strings.TrimSuffix(path, "transcript.jsonl") + "transcript_full.jsonl"; full != path && isFile(full) {
			path = full
		}
	}
	return &transcript{path: path, format: format, tools: map[string]*Message{}}
}

func clip(s string, n int) string {
	s = strings.TrimSpace(s)
	if len(s) <= n {
		return s
	}
	cut := n
	for cut > 0 && (s[cut]&0xC0) == 0x80 { // stay on a UTF-8 boundary
		cut--
	}
	return s[:cut] + " …"
}

// update reads any complete lines appended since the last call. It reports
// whether the conversation changed.
func (t *transcript) update() bool {
	f, err := os.Open(t.path)
	if err != nil {
		return false
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil || fi.Size() == t.offset {
		return false
	}
	if fi.Size() < t.offset { // rewritten
		*t = *newTranscript(t.path, t.format)
	}
	f.Seek(t.offset, io.SeekStart)
	r := bufio.NewReaderSize(f, 1<<20)
	if t.partial {
		skip, err := r.ReadBytes('\n')
		if err != nil {
			return false
		}
		t.offset += int64(len(skip))
		t.partial = false
		t.began = t.offset
	}
	changed := false
	for t.limit == 0 || t.offset < t.limit {
		line, err := r.ReadBytes('\n')
		if err != nil { // incomplete trailing line: retry next tick
			break
		}
		t.lineAt = t.offset
		t.offset += int64(len(line))
		changed = t.line(line) || changed
	}
	if len(t.msgs) > maxMessages*2 && t.limit == 0 { // a bounded read keeps all it reads
		t.dropped += len(t.msgs) - maxMessages
		t.msgs = append([]*Message(nil), t.msgs[len(t.msgs)-maxMessages:]...)
	}
	return changed
}

// openTail reads only the end of a long log: enough of it to fill the view,
// growing the window when the last few megabytes hold too few messages. A
// 40 MB Claude session otherwise takes most of a second to open.
func (t *transcript) openTail() {
	fi, err := os.Stat(t.path)
	if err != nil {
		return
	}
	for win := int64(1 << 20); ; win *= 4 {
		*t = *newTranscript(t.path, t.format)
		if win >= fi.Size() {
			t.update()
			return
		}
		t.offset, t.partial = fi.Size()-win, true
		t.update()
		if len(t.msgs) >= maxMessages/2 {
			return
		}
	}
}

// msgSync sends a transcript to one viewer as patches: the first carries the
// whole view, later ones only the messages from the first one that changed
// (a tool call gets its output, a new message lands), so a long conversation
// is not sent again on every line.
type msgSync struct {
	sent map[int]string // position → the message as last sent
}

type msgPatch struct {
	Base    int        `json:"base"` // position of the first message in the view
	From    int        `json:"from"` // position of msgs[0]
	Msgs    []*Message `json:"msgs"`
	Earlier int64      `json:"earlier,omitempty"` // cursor for older messages (see earlier)
}

// patch: what changed since the last call, or nil.
func (m *msgSync) patch(t *transcript) *msgPatch {
	view := t.view()
	base := t.dropped + len(t.msgs) - len(view)
	enc := make([]string, len(view))
	for i, msg := range view {
		b, _ := json.Marshal(msg)
		enc[i] = string(b)
	}
	from := base + len(view)
	if m.sent == nil {
		from = base
	} else {
		for i, s := range enc {
			if m.sent[base+i] != s {
				from = base + i
				break
			}
		}
		if from == base+len(view) && len(m.sent) == len(view) {
			if _, ok := m.sent[base]; ok {
				return nil
			}
		}
	}
	m.sent = make(map[int]string, len(enc))
	for i, s := range enc {
		m.sent[base+i] = s
	}
	return &msgPatch{Base: base, From: from, Msgs: append([]*Message{}, view[from-base:]...), Earlier: t.earlierCursor()}
}

func (t *transcript) add(m *Message) { m.at = t.lineAt; t.msgs = append(t.msgs, m) }

// earlierCursor: where the messages before the view end, or 0 when the view
// starts at the top of the log.
func (t *transcript) earlierCursor() int64 {
	view := t.view()
	if len(view) == 0 || (t.began == 0 && len(view) == len(t.msgs) && t.dropped == 0) {
		return 0
	}
	return view[0].at
}

// earlierMessages reads the messages whose lines start before `before`: the
// last `want` of them, and the cursor for the ones before those (0: none).
// It reads a little past `before` too, so a tool call there gets its output.
func earlierMessages(path, format string, before int64, want int) ([]*Message, int64) {
	fi, err := os.Stat(path)
	if err != nil || before <= 0 {
		return nil, 0
	}
	end := min(fi.Size(), before+1<<20)
	for win := int64(512 << 10); ; win *= 4 {
		t := newTranscript(path, format)
		if start := before - win; start > 0 {
			t.offset, t.partial = start, true
		}
		t.limit = end
		t.update()
		var keep []*Message
		for _, m := range t.msgs {
			if m.at < before {
				keep = append(keep, m)
			}
		}
		// Read from the top of the log: nothing is older than what we have.
		top := t.began == 0
		if len(keep) > want {
			keep = keep[len(keep)-want:]
			return keep, keep[0].at
		}
		if top {
			if t.dropped > 0 && len(keep) > 0 {
				return keep, keep[0].at
			}
			return keep, 0
		}
	}
}

// line parses one complete log line.
func (t *transcript) line(line []byte) bool {
	switch t.format {
	case "codex":
		return t.codexLine(line)
	case "agy":
		return t.agyLine(line)
	case "doubao", "cursor":
		return t.acpLine(line)
	}
	return t.claudeLine(line)
}

func (t *transcript) view() []*Message {
	if len(t.msgs) > maxMessages {
		return t.msgs[len(t.msgs)-maxMessages:]
	}
	return t.msgs
}

// noise filters harness-injected text that isn't part of the human conversation.
func noise(s string) bool {
	s = strings.TrimSpace(s)
	return s == "" || strings.HasPrefix(s, "<") || strings.HasPrefix(s, "# AGENTS.md") ||
		strings.HasPrefix(s, "Caveat: ")
}

var (
	subagentIDRe = regexp.MustCompile(`"(?:conversationId|agentId|conversation_id|agent_id)"\s*:\s*"([^"]+)"`)
	uuidPrefixRe = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)
)

// unwrapArgValue decodes Antigravity's JSON-encoded string parameter values
// (e.g. "\"ls -la\"" → "ls -la", "[{...}]" → []any{...}).
func unwrapArgValue(v any) any {
	s, ok := v.(string)
	if !ok || len(s) < 2 {
		return v
	}
	trimmed := strings.TrimSpace(s)
	if len(trimmed) < 2 {
		return v
	}
	if trimmed[0] == '"' && trimmed[len(trimmed)-1] == '"' {
		var inner string
		if json.Unmarshal([]byte(trimmed), &inner) == nil {
			return inner
		}
	}
	if (trimmed[0] == '[' && trimmed[len(trimmed)-1] == ']') || (trimmed[0] == '{' && trimmed[len(trimmed)-1] == '}') {
		var inner any
		if json.Unmarshal([]byte(trimmed), &inner) == nil {
			return inner
		}
	}
	return v
}

func parseToolArgs(raw json.RawMessage) (map[string]any, string) {
	if len(raw) == 0 {
		return nil, ""
	}
	var m map[string]any
	if json.Unmarshal(raw, &m) != nil {
		var s string
		if json.Unmarshal(raw, &s) == nil {
			if json.Unmarshal([]byte(s), &m) != nil {
				return nil, strings.TrimSpace(s)
			}
		} else {
			return nil, ""
		}
	}
	if m == nil {
		return nil, ""
	}
	out := make(map[string]any, len(m))
	for k, v := range m {
		out[k] = unwrapArgValue(v)
	}
	return out, ""
}

func lookupArg(m map[string]any, keys ...string) (any, bool) {
	for _, k := range keys {
		if v, ok := m[k]; ok {
			return v, true
		}
	}
	for _, k := range keys {
		for mk, mv := range m {
			if strings.EqualFold(mk, k) {
				return mv, true
			}
		}
	}
	return nil, false
}

func scalarString(v any) string {
	switch x := v.(type) {
	case string:
		return strings.TrimSpace(x)
	case float64:
		if x == float64(int64(x)) {
			return strconv.FormatInt(int64(x), 10)
		}
		return strconv.FormatFloat(x, 'f', -1, 64)
	case bool:
		return strconv.FormatBool(x)
	case []any:
		parts := make([]string, 0, len(x))
		for _, p := range x {
			if s := scalarString(p); s != "" {
				parts = append(parts, s)
			}
		}
		return strings.Join(parts, ", ")
	}
	return ""
}

func argString(m map[string]any, keys ...string) string {
	v, ok := lookupArg(m, keys...)
	if !ok {
		return ""
	}
	return scalarString(v)
}

func shortRef(s string) string {
	s = strings.TrimSpace(s)
	if uuidPrefixRe.MatchString(s) {
		return s[:8]
	}
	return s
}

func firstMeaningfulLine(s string) string {
	s = strings.ReplaceAll(s, "\r\n", "\n")
	for _, l := range strings.Split(s, "\n") {
		l = strings.TrimSpace(l)
		if l == "" {
			continue
		}
		l = strings.TrimLeft(l, "#>-*• ")
		l = strings.TrimSpace(l)
		if l != "" {
			return oneLine(l)
		}
	}
	return ""
}

func joinNonEmpty(sep string, parts ...string) string {
	var out []string
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return strings.Join(out, sep)
}

func isLongParamKey(k string) bool {
	switch strings.ToLower(k) {
	case "prompt", "message", "brief", "systemprompt", "system_prompt", "description", "content", "replacement", "target", "script", "text", "body":
		return true
	}
	return false
}

// formatToolDetail renders structured tool parameters as clean readable lines
// (scalar metadata first, multi-line prose fields last) instead of raw escaped JSON.
func formatToolDetail(m map[string]any) string {
	if len(m) == 0 {
		return ""
	}
	keys := make([]string, 0, len(m))
	for k := range m {
		if strings.EqualFold(k, "toolSummary") {
			continue
		}
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		li, lj := isLongParamKey(keys[i]), isLongParamKey(keys[j])
		if li != lj {
			return !li
		}
		return strings.ToLower(keys[i]) < strings.ToLower(keys[j])
	})
	var b strings.Builder
	for _, k := range keys {
		v := m[k]
		if v == nil {
			continue
		}
		var val string
		var multiline bool
		switch x := v.(type) {
		case string:
			val = strings.TrimSpace(x)
			multiline = strings.Contains(val, "\n") || len(val) > 100
		case float64, bool:
			val = scalarString(x)
		default:
			if jb, err := json.MarshalIndent(x, "", "  "); err == nil {
				val = string(jb)
				multiline = strings.Contains(val, "\n")
			}
		}
		if val == "" {
			continue
		}
		if b.Len() > 0 {
			if multiline {
				b.WriteString("\n\n")
			} else {
				b.WriteString("\n")
			}
		}
		if multiline {
			b.WriteString(k)
			b.WriteString(":\n")
			b.WriteString(val)
		} else {
			b.WriteString(k)
			b.WriteString(": ")
			b.WriteString(val)
		}
		if b.Len() >= maxDetail {
			break
		}
	}
	return clip(b.String(), maxDetail)
}

func subagentOf(name string, m map[string]any) (*SubagentCall, string) {
	lower := strings.ToLower(strings.TrimSpace(name))
	var typ, role, prompt, model string
	switch lower {
	case "invoke_subagent":
		typ = argString(m, "TypeName", "type_name", "subagent_type", "type")
		role = argString(m, "Role", "role", "description", "name")
		prompt = argString(m, "Prompt", "prompt", "message", "brief")
		model = argString(m, "Model", "model")
	case "agent", "task":
		typ = argString(m, "subagent_type", "TypeName", "type")
		role = argString(m, "description", "name", "Role", "role")
		prompt = argString(m, "prompt", "Prompt", "message")
		model = argString(m, "model", "Model")
	case "spawn_agent":
		typ = argString(m, "agent_type", "subagent_type", "type")
		role = argString(m, "nickname", "role", "name", "description")
		prompt = argString(m, "message", "prompt", "Prompt")
		model = argString(m, "model")
	case "swarm":
		if !strings.EqualFold(argString(m, "action"), "add") {
			return nil, ""
		}
		typ = "swarm"
		role = argString(m, "name")
		prompt = argString(m, "brief", "prompt")
		model = argString(m, "model")
	default:
		return nil, ""
	}
	if strings.EqualFold(model, "inherit") {
		model = ""
	}
	if typ == "" && role == "" && prompt == "" {
		return nil, ""
	}
	sub := &SubagentCall{
		Type:   clip(typ, 64),
		Role:   clip(role, 120),
		Prompt: clip(prompt, maxDetail),
		Model:  clip(model, 64),
	}
	summary := firstMeaningfulLine(prompt)
	if summary == "" {
		summary = joinNonEmpty(" · ", role, typ)
	}
	return sub, clip(summary, 180)
}

func formatStepsSummary(m map[string]any) (string, string) {
	rawSteps, ok := lookupArg(m, "steps")
	if !ok {
		return "", ""
	}
	arr, ok := rawSteps.([]any)
	if !ok || len(arr) == 0 {
		return "", ""
	}
	done := 0
	active := ""
	var lines []string
	for _, item := range arr {
		step, ok := item.(map[string]any)
		if !ok {
			continue
		}
		title := argString(step, "title")
		st := strings.ToLower(argString(step, "status"))
		mark := "[ ]"
		switch st {
		case "done", "completed":
			done++
			mark = "[x]"
		case "in_progress", "running":
			mark = "[>]"
			if active == "" {
				active = title
			}
		case "skipped":
			mark = "[-]"
		case "failed", "error":
			mark = "[!]"
		}
		if title != "" {
			lines = append(lines, mark+" "+title)
		}
	}
	if len(lines) == 0 {
		return "", ""
	}
	head := fmt.Sprintf("%d/%d", done, len(lines))
	if active != "" {
		head += " · " + active
	} else if done == len(lines) {
		head += " · 全部完成"
	}
	return clip(head, maxInput), clip(strings.Join(lines, "\n"), maxDetail)
}

func buildToolMessage(name string, raw json.RawMessage) *Message {
	msg := &Message{Kind: "tool", Name: name}
	m, plain := parseToolArgs(raw)
	if m == nil {
		msg.Input = clip(plain, maxInput)
		return msg
	}
	if sub, summary := subagentOf(name, m); sub != nil {
		msg.Input = summary
		msg.Subagent = sub
		return msg
	}

	lower := strings.ToLower(strings.TrimSpace(name))
	switch lower {
	case "generate_image":
		imgName := argString(m, "ImageName", "image_name", "name")
		ratio := argString(m, "AspectRatio", "aspect_ratio")
		prompt := firstMeaningfulLine(argString(m, "Prompt", "prompt"))
		head := imgName
		if ratio != "" {
			if head != "" {
				head += " (" + ratio + ")"
			} else {
				head = ratio
			}
		}
		msg.Input = clip(joinNonEmpty(" · ", head, prompt), maxInput)
		msg.Detail = formatToolDetail(m)
		return msg
	case "send_message":
		rcpt := shortRef(argString(m, "Recipient", "recipient", "to", "target"))
		body := firstMeaningfulLine(argString(m, "Message", "message", "content", "text"))
		if rcpt != "" {
			rcpt = "→ " + rcpt
		}
		msg.Input = clip(joinNonEmpty(" · ", rcpt, body), maxInput)
		msg.Detail = formatToolDetail(m)
		return msg
	case "define_subagent":
		n := argString(m, "Name", "name")
		desc := firstMeaningfulLine(argString(m, "Description", "description"))
		msg.Input = clip(joinNonEmpty(" · ", n, desc), maxInput)
		msg.Detail = formatToolDetail(m)
		return msg
	case "manage_subagents":
		act := argString(m, "Action", "action")
		var ids []string
		if rawIDs, ok := lookupArg(m, "ConversationIds", "conversation_ids", "ids"); ok {
			if arr, ok := rawIDs.([]any); ok {
				for _, x := range arr {
					if s := shortRef(scalarString(x)); s != "" {
						ids = append(ids, s)
					}
				}
			}
		}
		msg.Input = clip(joinNonEmpty(" · ", act, strings.Join(ids, ", ")), maxInput)
		if len(m) > 1 {
			msg.Detail = formatToolDetail(m)
		}
		return msg
	case "manage_task":
		act := argString(m, "Action", "action")
		tid := shortRef(argString(m, "TaskId", "task_id"))
		inp := firstMeaningfulLine(argString(m, "Input", "input"))
		msg.Input = clip(joinNonEmpty(" · ", act, tid, inp), maxInput)
		if inp != "" || len(m) > 2 {
			msg.Detail = formatToolDetail(m)
		}
		return msg
	case "schedule":
		cron := argString(m, "CronExpression", "cron_expression")
		dur := argString(m, "DurationSeconds", "duration_seconds")
		prompt := firstMeaningfulLine(argString(m, "Prompt", "prompt"))
		when := cron
		if when == "" && dur != "" {
			when = "in " + dur + "s"
		}
		msg.Input = clip(joinNonEmpty(" · ", when, prompt), maxInput)
		msg.Detail = formatToolDetail(m)
		return msg
	case "update_steps":
		if sum, det := formatStepsSummary(m); sum != "" {
			msg.Input = sum
			msg.Detail = det
			return msg
		}
	case "ask_question":
		if rawQs, ok := lookupArg(m, "questions"); ok {
			if arr, ok := rawQs.([]any); ok && len(arr) > 0 {
				if q0, ok := arr[0].(map[string]any); ok {
					if q := firstMeaningfulLine(argString(q0, "question")); q != "" {
						msg.Input = clip(q, maxInput)
						msg.Detail = formatToolDetail(m)
						return msg
					}
				}
			}
		}
	case "search_web":
		q := argString(m, "query", "Query")
		dom := argString(m, "domain", "Domain")
		if dom != "" && q != "" {
			q += " (" + dom + ")"
		}
		if q != "" {
			msg.Input = clip(q, maxInput)
			return msg
		}
	case "swarm":
		act := argString(m, "action")
		nm := argString(m, "name", "title")
		mod := argString(m, "model")
		msg.Input = clip(joinNonEmpty(" · ", act, nm, mod), maxInput)
		if len(m) > 2 || argString(m, "brief") != "" {
			msg.Detail = formatToolDetail(m)
		}
		return msg
	}

	// Tier 1: concrete command / path / search target / URL.
	for _, k := range []string{"command", "cmd", "CommandLine", "file_path", "path", "Path", "AbsolutePath", "TargetFile", "pattern", "Query", "query", "url", "Url"} {
		v, ok := m[k]
		if !ok {
			continue
		}
		var s string
		switch x := v.(type) {
		case string:
			s = strings.TrimSpace(x)
		case []any:
			parts := make([]string, 0, len(x))
			for _, p := range x {
				if ps, ok := p.(string); ok {
					parts = append(parts, ps)
				}
			}
			s = strings.Join(parts, " ")
		}
		if s == "" {
			continue
		}
		msg.Input = clip(s, maxInput)
		if (k == "command" || k == "cmd" || k == "CommandLine") && (len(s) > maxInput || strings.Contains(s, "\n")) {
			msg.Detail = clip(s, maxDetail)
		}
		return msg
	}

	// Tier 2: human-readable descriptive parameter (case-insensitive).
	for _, k := range []string{"toolSummary", "description", "title", "summary", "message", "prompt", "brief", "text", "task", "question", "action", "name"} {
		if v := argString(m, k); v != "" {
			msg.Input = clip(firstMeaningfulLine(v), maxInput)
			if len(m) > 1 || strings.Contains(v, "\n") || len(v) > maxInput {
				msg.Detail = formatToolDetail(m)
			}
			return msg
		}
	}

	// Fallback: synthesize a compact key: value preview instead of dumping raw JSON.
	keys := make([]string, 0, len(m))
	for k := range m {
		if strings.EqualFold(k, "toolSummary") {
			continue
		}
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		li, lj := isLongParamKey(keys[i]), isLongParamKey(keys[j])
		if li != lj {
			return !li
		}
		return strings.ToLower(keys[i]) < strings.ToLower(keys[j])
	})
	var parts []string
	for _, k := range keys {
		val := shortRef(firstMeaningfulLine(scalarString(m[k])))
		if val == "" {
			continue
		}
		parts = append(parts, k+": "+clip(val, 60))
		if len(parts) >= 3 {
			break
		}
	}
	if len(parts) > 0 {
		msg.Input = clip(strings.Join(parts, " · "), maxInput)
		msg.Detail = formatToolDetail(m)
		return msg
	}
	msg.Detail = formatToolDetail(m)
	return msg
}

func attachToolOutput(m *Message, output string, isErr bool) {
	if m == nil {
		return
	}
	m.Output = clip(output, maxOutput)
	m.Error = isErr
	if m.Subagent != nil && m.Subagent.ID == "" {
		if match := subagentIDRe.FindStringSubmatch(output); match != nil {
			m.Subagent.ID = match[1]
		}
	}
}

func toolSummary(name string, raw json.RawMessage) string {
	return buildToolMessage(name, raw).Input
}

// textOf flattens string-or-blocks content into plain text.
func textOf(raw json.RawMessage) string {
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	var blocks []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	json.Unmarshal(raw, &blocks)
	var b strings.Builder
	for _, bl := range blocks {
		if bl.Text != "" {
			if b.Len() > 0 {
				b.WriteString("\n")
			}
			b.WriteString(bl.Text)
		} else if bl.Type == "image" {
			b.WriteString("[image]")
		}
	}
	return b.String()
}

func (t *transcript) claudeLine(line []byte) bool {
	var e struct {
		Type      string `json:"type"`
		IsMeta    bool   `json:"isMeta"`
		Sidechain bool   `json:"isSidechain"`
		Message   struct {
			ID      string          `json:"id"`
			Content json.RawMessage `json:"content"`
		} `json:"message"`
		Origin *struct {
			Kind string `json:"kind"`
			Name string `json:"name"`
			Body string `json:"body"`
		} `json:"origin"`
		Attachment *struct {
			Type   string          `json:"type"`
			Prompt json.RawMessage `json:"prompt"`
			Origin *struct {
				Kind string `json:"kind"`
				Name string `json:"name"`
			} `json:"origin"`
		} `json:"attachment"`
	}
	if json.Unmarshal(line, &e) != nil || e.Sidechain {
		return false
	}
	// What the human typed while the agent was busy is logged as a queued
	// command once the agent takes it, not as a user line.
	if a := e.Attachment; e.Type == "attachment" && a != nil && a.Type == "queued_command" {
		kind := ""
		if a.Origin != nil {
			kind = a.Origin.Kind
		}
		text := promptText(a.Prompt)
		if body, ok := collieBody(text); ok {
			text, kind = body, "human"
		}
		if kind != "human" || strings.TrimSpace(text) == "" || noise(text) {
			return false
		}
		t.add(&Message{Kind: "user", Text: strings.TrimSpace(text)})
		t.lastID = ""
		return true
	}
	if len(e.Message.Content) == 0 {
		return false
	}
	if e.IsMeta {
		// A message Collie delivered through the peer inbox is logged as meta; it is the human's.
		if e.Type != "user" || e.Origin == nil || e.Origin.Kind != "peer" || e.Origin.Name != "Collie" {
			return false
		}
		body := stripPeerHead(e.Origin.Body)
		if body == "" {
			return false
		}
		t.add(&Message{Kind: "user", Text: body})
		t.lastID = ""
		return true
	}
	switch e.Type {
	case "user":
		var s string
		if json.Unmarshal(e.Message.Content, &s) == nil {
			if body, ok := collieBody(s); ok {
				s = body // a message Collie delivered through the peer inbox
			}
			if noise(s) {
				return false
			}
			t.add(&Message{Kind: "user", Text: s})
			t.lastID = ""
			return true
		}
		var blocks []struct {
			Type      string          `json:"type"`
			Text      string          `json:"text"`
			ToolUseID string          `json:"tool_use_id"`
			Content   json.RawMessage `json:"content"`
			IsError   bool            `json:"is_error"`
			Source    imageSource     `json:"source"`
		}
		json.Unmarshal(e.Message.Content, &blocks)
		changed := false
		var text []string
		for _, b := range blocks {
			switch b.Type {
			case "tool_result":
				if m := t.tools[b.ToolUseID]; m != nil {
					attachToolOutput(m, textOf(b.Content), b.IsError)
					changed = true
				}
			case "text":
				if !noise(b.Text) {
					text = append(text, b.Text)
				}
			case "image":
				text = append(text, imageToken(b.Source))
			}
		}
		if len(text) > 0 {
			t.add(&Message{Kind: "user", Text: strings.Join(text, "\n")})
			t.lastID = ""
			changed = true
		}
		return changed
	case "assistant":
		var blocks []struct {
			Type  string          `json:"type"`
			Text  string          `json:"text"`
			ID    string          `json:"id"`
			Name  string          `json:"name"`
			Input json.RawMessage `json:"input"`
		}
		json.Unmarshal(e.Message.Content, &blocks)
		changed := false
		for _, b := range blocks {
			switch b.Type {
			case "text":
				if strings.TrimSpace(b.Text) == "" {
					continue
				}
				// Claude Code streams one API message as several entries; merge them.
				if n := len(t.msgs); n > 0 && t.lastID == e.Message.ID && t.msgs[n-1].Kind == "assistant" {
					t.msgs[n-1].Text += "\n\n" + b.Text
				} else {
					t.add(&Message{Kind: "assistant", Text: b.Text})
				}
				changed = true
			case "tool_use":
				if m := askOf(b.Name, b.Input); m != nil {
					if m.Name == "AskUserQuestion" {
						t.tools[b.ID] = m // its result is the answer
					}
					t.add(m)
					changed = true
					continue
				}
				m := buildToolMessage(b.Name, b.Input)
				t.tools[b.ID] = m
				t.add(m)
				changed = true
			}
		}
		t.lastID = e.Message.ID
		return changed
	}
	return false
}

func (t *transcript) codexLine(line []byte) bool {
	var e struct {
		Type    string `json:"type"`
		Payload struct {
			Type      string          `json:"type"`
			Role      string          `json:"role"`
			Content   json.RawMessage `json:"content"`
			Name      string          `json:"name"`
			Arguments json.RawMessage `json:"arguments"`
			Input     json.RawMessage `json:"input"`
			CallID    string          `json:"call_id"`
			Output    json.RawMessage `json:"output"`
		} `json:"payload"`
	}
	if json.Unmarshal(line, &e) != nil || e.Type != "response_item" {
		return false
	}
	p := e.Payload
	switch p.Type {
	case "message":
		if p.Role != "user" && p.Role != "assistant" {
			return false
		}
		var blocks []struct {
			Text string `json:"text"`
		}
		json.Unmarshal(p.Content, &blocks)
		var parts []string
		for _, b := range blocks {
			if p.Role == "assistant" || !noise(b.Text) {
				if strings.TrimSpace(b.Text) != "" {
					parts = append(parts, b.Text)
				}
			}
		}
		if len(parts) == 0 {
			return false
		}
		t.add(&Message{Kind: p.Role, Text: strings.Join(parts, "\n\n")})
		return true
	case "function_call", "custom_tool_call", "local_shell_call":
		args := p.Arguments
		if len(args) == 0 {
			args = p.Input
		}
		toolName := p.Name
		if toolName == "" {
			toolName = "shell"
		}
		m := buildToolMessage(toolName, args)
		t.tools[p.CallID] = m
		t.add(m)
		return true
	case "function_call_output", "custom_tool_call_output":
		if m := t.tools[p.CallID]; m != nil {
			attachToolOutput(m, textOf(p.Output), false)
			return true
		}
	}
	return false
}

var agyRequestRe = regexp.MustCompile(`(?s)<USER_REQUEST>\s*(.*?)\s*</USER_REQUEST>`)

// agyLine parses Antigravity CLI's brain/<id>/.system_generated/logs/transcript.jsonl.
func (t *transcript) agyLine(line []byte) bool {
	var e struct {
		Type      string `json:"type"`
		Source    string `json:"source"`
		Content   string `json:"content"`
		ToolCalls []struct {
			Name string          `json:"name"`
			Args json.RawMessage `json:"args"`
		} `json:"tool_calls"`
	}
	if json.Unmarshal(line, &e) != nil {
		return false
	}
	switch e.Type {
	case "USER_INPUT":
		text := e.Content
		if m := agyRequestRe.FindStringSubmatch(text); m != nil {
			text = m[1]
		} else if noise(text) {
			return false
		}
		if strings.TrimSpace(text) == "" {
			return false
		}
		t.add(&Message{Kind: "user", Text: text})
		return true
	case "PLANNER_RESPONSE":
		changed := false
		if strings.TrimSpace(e.Content) != "" {
			t.add(&Message{Kind: "assistant", Text: e.Content})
			changed = true
		}
		for _, c := range e.ToolCalls {
			m := buildToolMessage(c.Name, c.Args)
			t.add(m)
			t.pending = append(t.pending, m)
			changed = true
		}
		return changed
	case "GENERIC":
		if len(t.pending) == 0 {
			return false
		}
		m := t.pending[0]
		t.pending = t.pending[1:]
		isErr := strings.Contains(e.Content, "exited with code") && !strings.Contains(e.Content, "exited with code 0")
		attachToolOutput(m, agyOutput(e.Content), isErr)
		return true
	case "ERROR_MESSAGE":
		if strings.TrimSpace(e.Content) == "" {
			return false
		}
		t.add(&Message{Kind: "assistant", Text: "⚠ " + e.Content})
		return true
	}
	return false
}

func agyToolSummary(raw json.RawMessage) string {
	return buildToolMessage("", raw).Input
}

// agyOutput drops agy's timing preamble from a tool result.
func agyOutput(s string) string {
	lines := strings.Split(strings.ReplaceAll(s, "\r\n", "\n"), "\n")
	out := lines[:0]
	for _, l := range lines {
		if strings.HasPrefix(l, "Created At:") || strings.HasPrefix(l, "Completed At:") {
			continue
		}
		out = append(out, l)
	}
	return strings.TrimSpace(strings.Join(out, "\n"))
}

// acpLine parses one incremental event line persisted by `collie _bridge <kind>`.
func (t *transcript) acpLine(line []byte) bool {
	var e acpLogEntry
	if json.Unmarshal(line, &e) != nil {
		return false
	}
	switch e.Type {
	case "user":
		if e.Text == "" {
			return false
		}
		if e.ID != "" && len(t.msgs) > 0 && t.lastID == "u:"+e.ID && t.msgs[len(t.msgs)-1].Kind == "user" {
			t.msgs[len(t.msgs)-1].Text += e.Text
			return true
		}
		if strings.TrimSpace(e.Text) == "" {
			return false
		}
		t.add(&Message{Kind: "user", Text: e.Text})
		if e.ID != "" {
			t.lastID = "u:" + e.ID
		} else {
			t.lastID = ""
		}
		return true
	case "assistant_chunk", "assistant":
		if e.Text == "" {
			return false
		}
		if e.ID != "" && len(t.msgs) > 0 && t.lastID == e.ID && t.msgs[len(t.msgs)-1].Kind == "assistant" {
			t.msgs[len(t.msgs)-1].Text += e.Text
			return true
		}
		t.add(&Message{Kind: "assistant", Text: e.Text})
		t.lastID = e.ID
		return true
	case "tool":
		var m *Message
		if e.ID != "" {
			m = t.tools[e.ID]
		}
		if m == nil {
			m = &Message{Kind: "tool"}
			if e.ID != "" {
				t.tools[e.ID] = m
			}
			t.add(m)
			t.lastID = ""
		}
		title := strings.TrimSpace(e.Title)
		if title != "" && (m.Name == "" || title != doubaoPlaceholderTool) {
			m.Name = title
		}
		if m.Name == "" {
			m.Name = orStr(strings.TrimSpace(e.Kind), "Tool")
		}
		if inp := strings.TrimSpace(e.Input); inp != "" {
			m.Input = clip(inp, maxInput)
		} else if len(e.Locations) > 0 {
			var paths []string
			for _, p := range e.Locations {
				if p = strings.TrimSpace(p); p != "" {
					paths = append(paths, p)
				}
			}
			if len(paths) > 0 {
				m.Input = clip(strings.Join(paths, ", "), maxInput)
			}
		}
		if out := strings.TrimSpace(e.Output); out != "" {
			m.Output = clip(out, maxOutput)
		}
		if e.Error || strings.EqualFold(e.Status, "failed") || strings.EqualFold(e.Status, "error") {
			m.Error = true
		}
		return true
	case "error":
		if strings.TrimSpace(e.Text) == "" {
			return false
		}
		t.add(&Message{Kind: "assistant", Text: "⚠ " + strings.TrimSpace(e.Text)})
		t.lastID = ""
		return true
	}
	return false
}

// ---------------------------------------------------------------- locating

type agentDetail struct {
	Agent   *string `json:"agent"`
	Status  string  `json:"agent_status"`
	Cwd     *string `json:"cwd"`
	Session *struct {
		Kind  string `json:"kind"`
		Value string `json:"value"`
	} `json:"agent_session"`
}

var claimed = struct {
	sync.Mutex
	m map[string]string // transcript path → pane
}{m: map[string]string{}}

// transcriptFor resolves (and caches) the transcript for the agent in a pane.
func (s *Server) transcriptFor(key, pane string, a agentDetail, cache map[string]*transcript) *transcript {
	kind := deref(a.Agent)
	sessKind, value := "", ""
	if a.Session != nil {
		sessKind, value = a.Session.Kind, a.Session.Value
	}
	ck := key + "|" + kind + "|" + sessKind + ":" + value
	if tr := cache[ck]; tr != nil {
		return tr
	}
	path, format := s.findTranscript(kind, sessKind, value, pane, deref(a.Cwd))
	if path == "" {
		return nil
	}
	tr := newTranscript(path, format)
	cache[ck] = tr
	return tr
}

func (s *Server) findTranscript(kind, sessKind, value, pane, cwd string) (path, format string) {
	format = kind
	if kind != "codex" && kind != "agy" && kind != "doubao" && kind != "cursor" {
		format = "claude"
	}
	if sessKind == "path" {
		if isFile(value) {
			return value, format
		}
		return "", ""
	}
	if strings.ContainsAny(value, "/*?[") {
		return "", ""
	}
	switch kind {
	case "claude":
		if claudeSGLauncher() != "" {
			dir := claudeSGStateDir(s.home)
			if isSafeSessionID(value) {
				if p := filepath.Join(dir, "transcripts", value+".jsonl"); isFile(p) {
					return p, "claude"
				}
			}
			if value == "" && pane != "" {
				local, _ := split(pane)
				if ev, ok := readClaudeSGEvent(claudeSGEventFile(dir, local)); ok && isSafeSessionID(ev.SessionID) {
					if p := filepath.Join(dir, "transcripts", ev.SessionID+".jsonl"); isFile(p) {
						return p, "claude"
					}
				}
			}
		}
		if value == "" {
			return "", ""
		}
		root := envOr("CLAUDE_CONFIG_DIR", filepath.Join(s.home, ".claude"))
		if m, _ := filepath.Glob(filepath.Join(root, "projects", "*", value+".jsonl")); len(m) > 0 {
			return m[0], "claude"
		}
	case "codex":
		root := envOr("CODEX_HOME", filepath.Join(s.home, ".codex"))
		if value != "" {
			if m, _ := filepath.Glob(filepath.Join(root, "sessions", "*", "*", "*", "rollout-*"+value+".jsonl")); len(m) > 0 {
				return m[len(m)-1], "codex"
			}
			return "", ""
		}
		if p := s.guessCodex(root, pane, cwd); p != "" {
			return p, "codex"
		}
	case "agy":
		brain := filepath.Join(s.agyCLIHome(), "brain")
		if value != "" {
			for _, b := range []string{brain, filepath.Join(s.home, ".gemini", "antigravity", "brain")} {
				if p := filepath.Join(b, value, ".system_generated", "logs", "transcript.jsonl"); isFile(p) {
					return p, "agy"
				}
			}
		}
		return s.guessAgy(brain, pane, cwd), "agy"
	case "doubao", "cursor":
		dir := s.bridgeTranscriptDir(kind)
		if value != "" {
			if p := filepath.Join(dir, value+".jsonl"); isFile(p) {
				return p, kind
			}
		}
		if pane != "" {
			local, _ := split(pane)
			if meta, ok := readACPPaneMeta(dir, local); ok {
				if meta.Path != "" && isFile(meta.Path) {
					return meta.Path, kind
				}
				if meta.SessionID != "" {
					if p := filepath.Join(dir, meta.SessionID+".jsonl"); isFile(p) {
						return p, kind
					}
				}
			}
		}
	}
	return "", ""
}

func isFile(p string) bool { fi, err := os.Stat(p); return err == nil && fi.Mode().IsRegular() }

// ---------------------------------------------------------------- approvals

type Choice struct {
	Label   string   `json:"label"`
	Keys    []string `json:"keys,omitempty"`
	Text    string   `json:"text,omitempty"`
	Desc    string   `json:"desc,omitempty"` // a line under the option that explains it
	Current bool     `json:"current,omitempty"`
}

type Approval struct {
	Title     string   `json:"title"`
	Question  string   `json:"question"`
	Choices   []Choice `json:"choices"`
	Hint      string   `json:"hint,omitempty"`
	Screen    string   `json:"screen"`
	after     int      // lines of other text below the menu
	cursors   int      // choices marked with a selection cursor glyph
	idleBelow bool     // an idle input prompt or shortcut footer appears below the menu
}

var (
	numberedRe  = regexp.MustCompile(`^\s*([❯›>▸●➜→]\s*)?(\d)[.)]\s+(.{1,90}?)\s*$`)
	cursorRe    = regexp.MustCompile(`^\s*[❯›>▸●➜→]\s+(.{1,90}?)\s*$`)
	hintRe      = regexp.MustCompile(`(?i)((enter|esc|tab)\b|↑|↓).*(confirm|cancel|select|navigate|continue|back|skip|close|review|amend|dismiss|submit)`)
	idleBelowRe = regexp.MustCompile(`(?i)^\s*[❯›>]\s*$|\?\s*for shortcuts\b|\bshift\+tab to cycle\b|⏎\s*send\b`)
	frameRe     = regexp.MustCompile(`^[\s│┃|╭╮╰╯─━═┌┐└┘╌┄┈]*$`)
	ruleRe      = regexp.MustCompile(`[─━═]{12,}`)
)

// moveKeys builds the arrow+Enter keystrokes from cur to target idx.
func moveKeys(cur, idx int) []string {
	var keys []string
	for d := cur; d < idx; d++ {
		keys = append(keys, "down")
	}
	for d := cur; d > idx; d-- {
		keys = append(keys, "up")
	}
	return append(keys, "enter")
}

// parseApproval turns the bottom of a blocked agent's screen into a question
// and a set of choices. Menus with a cursor ("❯ 1. Yes" / "› 1. Trust and
// continue" / "❯ Yes") answer by moving the cursor and pressing Enter (Codex's
// numbered Folder access menu does not confirm on a bare digit); cursorless
// numbered menus answer with the digit.
func parseApproval(screen string) Approval {
	// Blank and frame lines become paragraph breaks (""), so the card can
	// reflow the dialog's text; a long rule marks where the dialog begins.
	var lines []string
	top, width := 0, 0
	for _, l := range strings.Split(screen, "\n") {
		width = max(width, cells(strings.TrimRight(l, " ")))
		l = strings.TrimRight(strings.Trim(strings.TrimRight(l, " "), "│┃"), " ")
		if frameRe.MatchString(l) {
			if ruleRe.MatchString(l) {
				top = len(lines)
			}
			if len(lines) > 0 && lines[len(lines)-1] != "" {
				lines = append(lines, "")
			}
			continue
		}
		lines = append(lines, l)
	}
	a := Approval{Screen: strings.Join(tailSlice(lines, 18), "\n")}

	// Find the menu block: the last run of numbered or cursor-style options.
	end := -1
	for i := len(lines) - 1; i >= 0; i-- {
		if numberedRe.MatchString(lines[i]) || cursorRe.MatchString(lines[i]) {
			end = i
			break
		}
		if hintRe.MatchString(lines[i]) && a.Hint == "" {
			a.Hint = strings.TrimSpace(lines[i])
		}
	}
	if end < 0 {
		a.Question = strings.Join(tailSlice(trimBlank(lines), 6), "\n")
		return a
	}
	start := end
	numbered := numberedRe.MatchString(lines[end])
	// A narrow pane wraps long options onto deeper-indented lines.
	wrapped := func(l string) bool {
		return strings.TrimSpace(l) != "" && len(l)-len(strings.TrimLeft(l, " ")) >= 4 && !numberedRe.MatchString(l)
	}
	digit := func(l string) int {
		if m := numberedRe.FindStringSubmatch(l); m != nil {
			return int(m[2][0] - '0')
		}
		return -1
	}
	first := digit(lines[end]) // the lowest option number found so far
	for start > 0 {
		prev := lines[start-1]
		if numbered && numberedRe.MatchString(prev) {
			if d := digit(prev); d >= 0 && d == first-1 {
				first = d
				start--
				continue
			}
			break
		}
		if numbered && first > 1 && wrapped(prev) {
			start--
			continue
		}
		// Claude's question box sets its last options apart with a rule
		// ("4. Type something." ── "5. Chat about this"): one menu if the
		// numbers carry on across the break.
		if numbered && prev == "" && start >= 2 {
			j := start - 2
			for j > 0 && wrapped(lines[j]) {
				j--
			}
			if d := digit(lines[j]); d >= 0 && d == first-1 {
				start--
				continue
			}
		}
		// cursor menus: siblings are indented plain lines next to the ❯ line
		if !numbered && strings.TrimSpace(prev) != "" && (cursorRe.MatchString(prev) || strings.HasPrefix(prev, "  ")) &&
			len(strings.TrimSpace(prev)) <= 90 && !strings.HasSuffix(strings.TrimSpace(prev), "?") && !strings.HasSuffix(strings.TrimSpace(prev), "？") {
			start--
			continue
		}
		break
	}
	for numbered && start < end && !numberedRe.MatchString(lines[start]) {
		start++
	}
	// Cursor menus may continue below the ❯ line.
	for !numbered && end+1 < len(lines) && strings.HasPrefix(lines[end+1], "  ") && strings.TrimSpace(lines[end+1]) != "" &&
		!hintRe.MatchString(lines[end+1]) && !idleBelowRe.MatchString(lines[end+1]) {
		end++
	}
	for _, l := range lines[end+1:] {
		if idleBelowRe.MatchString(l) {
			a.idleBelow = true
		}
		if strings.TrimSpace(l) != "" && !hintRe.MatchString(l) {
			a.after++
		}
	}

	cur := -1
	opts := lines[start : end+1]
	for i, l := range opts {
		if cursorRe.MatchString(l) {
			cur = i
		}
	}
	for i, l := range opts {
		if numbered {
			m := numberedRe.FindStringSubmatch(l)
			if m == nil {
				if n := len(a.Choices); n > 0 && wrapped(l) {
					// The terminal wraps a line only when the next word does
					// not fit; a line that would have fit explains the option.
					c, t := &a.Choices[n-1], strings.TrimSpace(l)
					above := strings.TrimRight(opts[i-1], " ")
					switch {
					case c.Desc != "":
						c.Desc += " " + t
					case cells(above)+1+cells(strings.Fields(t)[0]) <= width:
						c.Desc = t
					default:
						c.Label += " " + t
					}
				}
				continue
			}
			if m[1] != "" {
				a.cursors++
			}
			a.Choices = append(a.Choices, Choice{Label: m[3], Text: m[2], Current: m[1] != ""})
			continue
		}
		if cursorRe.MatchString(l) {
			a.cursors++
		}
		label := strings.TrimSpace(strings.TrimLeft(strings.TrimSpace(l), "❯›>▸●➜→"))
		if label == "" {
			continue
		}
		var keys []string
		if cur >= 0 {
			keys = moveKeys(cur, i)
		} else {
			keys = []string{"enter"}
		}
		a.Choices = append(a.Choices, Choice{Label: label, Keys: keys, Current: i == cur})
	}
	if numbered {
		curChoice := -1
		for i, c := range a.Choices {
			if c.Current {
				curChoice = i
				break
			}
		}
		if curChoice >= 0 {
			for i := range a.Choices {
				a.Choices[i].Keys = moveKeys(curChoice, i)
			}
		}
	}
	if len(a.Choices) < 2 || len(a.Choices) > 9 {
		a.Choices = nil
	}
	from := start - 18
	if top > from && top < start {
		from = top
	}
	if from < 0 {
		from = 0
	}
	a.Question = strings.Join(trimBlank(lines[from:start]), "\n")
	return a
}

// cells: how many terminal columns s takes (CJK and full-width take two).
func cells(s string) int {
	n := 0
	for _, r := range s {
		if r >= 0x1100 && (r <= 0x115f || (r >= 0x2e80 && r <= 0xa4cf) || (r >= 0xac00 && r <= 0xd7a3) ||
			(r >= 0xf900 && r <= 0xfaff) || (r >= 0xfe30 && r <= 0xfe4f) || (r >= 0xff00 && r <= 0xff60) || (r >= 0xffe0 && r <= 0xffe6)) {
			n += 2
		} else {
			n++
		}
	}
	return n
}

func trimBlank(ls []string) []string {
	for len(ls) > 0 && strings.TrimSpace(ls[0]) == "" {
		ls = ls[1:]
	}
	for len(ls) > 0 && strings.TrimSpace(ls[len(ls)-1]) == "" {
		ls = ls[:len(ls)-1]
	}
	return ls
}

func tailSlice(ls []string, n int) []string {
	if len(ls) > n {
		return ls[len(ls)-n:]
	}
	return ls
}

func tailLines(s string, n int) string {
	var keep []string
	for _, l := range strings.Split(s, "\n") {
		l = strings.TrimRight(l, " \t")
		if strings.TrimSpace(strings.Trim(l, "─━═│╭╮╰╯")) == "" {
			continue
		}
		keep = append(keep, l)
	}
	return strings.Join(tailSlice(keep, n), "\n")
}

// activityLine is what a working agent says it is doing ("Thinking…",
// "Running tests…"): the bottom of its screen without the input box, the
// key hints and the spinner, which mean nothing outside the terminal. The
// status line's own counters ("42s · ↓ 1.8k tokens") come back as meta.
var (
	liveHint    = regexp.MustCompile(`\s*[(（][^()（）]*(esc to (interrupt|cancel)|ctrl\+c)[^()（）]*[)）]`)
	liveParen   = regexp.MustCompile(`\s*[(（]([^()（）]*)[)）]\s*$`)
	liveKey     = regexp.MustCompile(`(?i)\besc\b|ctrl\+|to (interrupt|cancel|expand)`)
	liveDigit   = regexp.MustCompile(`\d`)
	liveChrome  = regexp.MustCompile(`(?i)esc to (interrupt|cancel)|shift\+tab|for shortcuts|auto mode|bypass permissions|accept edits|plan mode|ctrl\+[a-z] to |^[❯>›]( |$)|^[⏵▸►»⏎]|⏎ send`)
	liveBullet  = regexp.MustCompile(`^[✻✽✢✶✳✺✹✸✷·*•◐◓◑◒◔◕○●⠋⠙⠹⠸⠼⠴⠦⠧⠇⠏⣾⣽⣻⢿⡿⣟⣯⣷]+\s*`)
	liveSpinner = regexp.MustCompile(`^[✻✽✢✶✳✺✹✸✷·*◐◓◑◒◔◕⠋⠙⠹⠸⠼⠴⠦⠧⠇⠏⣾⣽⣻⢿⡿⣟⣯⣷]`) // animated: a status line
)

func activityLine(screen string) (text, meta string) {
	var keep []string
	status, statusMeta := "", ""
	for _, l := range strings.Split(screen, "\n") {
		l = strings.TrimSpace(strings.Trim(strings.TrimSpace(l), "│┃|"))
		hinted := liveHint.MatchString(l)
		m := ""
		if liveSpinner.MatchString(l) || hinted {
			l, m = splitCounters(l)
		}
		l = strings.TrimSpace(liveHint.ReplaceAllString(l, ""))
		if l == "" || strings.Trim(l, "─━═╭╮╰╯ ") == "" || liveChrome.MatchString(l) {
			continue
		}
		spun := liveSpinner.MatchString(l)
		l = strings.TrimSpace(liveBullet.ReplaceAllString(l, ""))
		if hinted || spun {
			status, statusMeta = l, m // the agent's own status line wins
		}
		keep = append(keep, l)
	}
	if status != "" {
		return status, statusMeta
	}
	return strings.Join(tailSlice(keep, 2), "\n"), ""
}

// splitCounters takes a status line's closing parenthetical apart:
// "Thinking… (42s · ↓ 1.8k tokens · esc to interrupt)" is "Thinking…" with
// "42s · ↓ 1.8k tokens"; the key hints go.
func splitCounters(l string) (string, string) {
	m := liveParen.FindStringSubmatchIndex(l)
	if m == nil {
		return l, ""
	}
	var counters []string
	for _, p := range strings.FieldsFunc(l[m[2]:m[3]], func(r rune) bool { return r == '·' || r == '•' }) {
		p = strings.TrimSpace(p)
		switch {
		case p == "" || liveKey.MatchString(p):
		case liveDigit.MatchString(p):
			counters = append(counters, p)
		default:
			return l, "" // words, not counters: leave the line as it is
		}
	}
	return strings.TrimSpace(l[:m[0]]), strings.Join(counters, " · ")
}

// ---------------------------------------------------------------- streaming

// streamChat wraps an agent terminal as a conversation: structured messages
// from the agent's own transcript, plus a live card with what the terminal is
// showing right now (progress, or the question it is blocked on).
func (s *Server) streamChat(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	sse, ok := newSSE(w)
	if !ok {
		return
	}
	cache := map[string]*transcript{}
	var (
		tr                *transcript
		lastLive, lastLog string
		lastMeta          string
		beat              = time.Now()
		ms                msgSync
	)
	send := func(ev string, v any) bool {
		b, _ := json.Marshal(v)
		beat = time.Now()
		return sse.send(ev, b) == nil
	}
	for {
		ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
		var info struct {
			Agent agentDetail `json:"agent"`
		}
		err := s.herdr.Call(ctx, "agent.get", map[string]any{"target": id}, &info)
		kind, status := "", "shell"
		if err == nil && info.Agent.Agent != nil {
			kind, status = *info.Agent.Agent, info.Agent.Status
			// herdr says "idle" while a startup dialog is up (Codex's hooks
			// review, a folder trust); the header already shows it as needing
			// you, and the card with its choices belongs here too.
			if status == "idle" && s.herdr.idleButAsking(ctx, id) {
				status = "blocked"
			}
		} else if err != nil {
			if _, e := s.herdr.Read(ctx, id, "visible", "text", 1); e != nil {
				cancel()
				send("gone", map[string]any{"error": e.Error()})
				return
			}
		}

		if err == nil && kind != "" {
			if next := s.transcriptFor(id, id, info.Agent, cache); next != tr && next != nil {
				tr, lastLog = next, ""
				if !send("reset", map[string]any{}) {
					cancel()
					return
				}
			}
		}
		meta := kind + "|" + status + "|" + boolStr(tr != nil)
		if meta != lastMeta {
			lastMeta = meta
			if !send("meta", map[string]any{"kind": kind, "status": status, "structured": tr != nil}) {
				cancel()
				return
			}
		}
		if tr != nil {
			if lastLog == "" {
				tr.openTail()
				ms = msgSync{}
			}
			if tr.update() || lastLog == "" {
				lastLog = "-"
				if p := ms.patch(tr); p != nil && !send("patch", p) {
					cancel()
					return
				}
			}
		} else if res, e := s.herdr.Read(ctx, id, "recent_unwrapped", "text", 400); e == nil && res.Text != lastLog {
			// No transcript (plain shell, unknown agent): fall back to the terminal log.
			lastLog = res.Text
			if !send("log", map[string]any{"text": res.Text}) {
				cancel()
				return
			}
		}

		// Live card: progress while working, the question while blocked.
		live := map[string]any{"status": status}
		if status == "working" || status == "blocked" {
			if res, e := s.herdr.Read(ctx, id, "detection", "text", 0); e == nil {
				if status == "blocked" {
					live["approval"] = parseApproval(res.Text)
				} else {
					live["tail"], live["meta"] = activityLine(res.Text)
				}
			}
		}
		cancel()
		if b, _ := json.Marshal(live); string(b) != lastLive {
			lastLive = string(b)
			if sse.send("live", b) != nil {
				return
			}
			beat = time.Now()
		}

		if time.Since(beat) > 15*time.Second {
			sse.comment()
			beat = time.Now()
		}
		select {
		case <-r.Context().Done():
			return
		case <-time.After(700 * time.Millisecond):
		}
	}
}

func boolStr(b bool) string {
	if b {
		return "1"
	}
	return "0"
}

// lastMessage returns the agent's latest reply (or its screen), for the hub's
// worker summaries.
func (s *Server) lastMessage(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var info struct {
		Agent agentDetail `json:"agent"`
	}
	if s.herdr.Call(r.Context(), "agent.get", map[string]any{"target": id}, &info) == nil {
		if tr := s.transcriptFor("last:"+id, id, info.Agent, map[string]*transcript{}); tr != nil {
			tr.update()
			msgs := tr.view()
			for i := len(msgs) - 1; i >= 0; i-- {
				if msgs[i].Kind == "assistant" {
					writeJSON(w, 200, map[string]any{"text": msgs[i].Text})
					return
				}
			}
		}
	}
	if res, err := s.herdr.Read(r.Context(), id, "detection", "text", 0); err == nil {
		writeJSON(w, 200, map[string]any{"text": tailLines(res.Text, 14)})
		return
	}
	writeJSON(w, 404, map[string]any{"error": "pane not found"})
}

// paneAsk: the dialog a pane is showing, if any, so it can be answered from
// Collie's conversation without opening the pane.
func (s *Server) paneAsk(w http.ResponseWriter, r *http.Request) {
	res, err := s.herdr.Read(r.Context(), r.PathValue("id"), "detection", "text", 0)
	if err != nil {
		fail(w, err)
		return
	}
	if !screenAsks(res.Text) {
		writeJSON(w, 200, map[string]any{"asking": false})
		return
	}
	writeJSON(w, 200, map[string]any{"asking": true, "approval": parseApproval(res.Text)})
}

// askOf turns a question to the human into an ask item: Claude's
// AskUserQuestion, or the Claude app's end-of-turn box (its wrap_up tool,
// whose buttons just send their label back as the human's reply).
func askOf(name string, raw json.RawMessage) *Message {
	switch {
	case name == "AskUserQuestion":
		var in struct {
			Questions []struct {
				Question string `json:"question"`
				Header   string `json:"header"`
				Options  []struct {
					Label string `json:"label"`
				} `json:"options"`
			} `json:"questions"`
		}
		if json.Unmarshal(raw, &in) != nil || len(in.Questions) == 0 {
			return nil
		}
		m := &Message{Kind: "ask", Name: name}
		var qs []string
		for _, q := range in.Questions {
			qs = append(qs, q.Question)
		}
		m.Text = strings.Join(qs, "\n")
		if len(in.Questions) == 1 {
			for _, o := range in.Questions[0].Options {
				m.Options = append(m.Options, o.Label)
			}
		}
		return m
	case strings.HasSuffix(name, "__wrap_up"):
		var in struct {
			Recap   string `json:"recap"`
			Ask     string `json:"ask"`
			Actions []struct {
				Label  string `json:"label"`
				Action string `json:"action"`
			} `json:"actions"`
		}
		if json.Unmarshal(raw, &in) != nil || (in.Ask == "" && in.Recap == "") {
			return nil
		}
		if in.Ask == "" {
			return &Message{Kind: "ask", Name: "recap", Input: in.Recap} // where the turn left things
		}
		m := &Message{Kind: "ask", Name: "wrap_up", Text: in.Ask, Input: in.Recap}
		for _, a := range in.Actions {
			if a.Action == "" && a.Label != "" {
				m.Options = append(m.Options, a.Label)
			}
		}
		return m
	}
	return nil
}

// promptText reads a queued prompt: a string, or blocks of text and images.
func promptText(raw json.RawMessage) string {
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	var blocks []struct {
		Type   string      `json:"type"`
		Text   string      `json:"text"`
		Source imageSource `json:"source"`
	}
	json.Unmarshal(raw, &blocks)
	var out []string
	for _, b := range blocks {
		switch b.Type {
		case "text":
			if !noise(b.Text) {
				out = append(out, b.Text)
			}
		case "image":
			out = append(out, imageToken(b.Source))
		}
	}
	return strings.Join(out, "\n")
}
