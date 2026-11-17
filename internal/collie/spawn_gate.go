package collie

import (
	"regexp"
	"strings"
)

// What a worker runs is the human's call, made in settings. The router (a
// model) may only pass along a choice the human actually made in words: its
// --kind / --model / --effort count when the human's latest message names
// them, and are dropped otherwise, so the settings apply.

// kindWords: how people name each agent in a message.
var kindWords = map[string][]string{
	"claude":      {"claude", "克劳德"},
	"codex":       {"codex"},
	"antigravity": {"antigravity", "agy", "gemini", "反重力"},
}

// Parts of a model id that say nothing on their own ("claude-opus-5-5-medium":
// "opus" names it, "claude" and "medium" don't).
var genericModelWords = map[string]bool{
	"claude": true, "gemini": true, "gpt": true, "codex": true, "model": true, "preview": true,
	"latest": true, "low": true, "medium": true, "high": true, "xhigh": true, "max": true,
	"mini": true, "thinking": true, "exp": true,
}

var (
	modelSplit  = regexp.MustCompile(`[-_.:/ ]+`)
	alnumOnly   = regexp.MustCompile(`[^a-z0-9]+`)
	effortTopic = regexp.MustCompile(`(?i)思考|推理|强度|effort|reasoning|think`)
)

// effortWords: English levels anywhere; Chinese ones only next to a word about
// thinking ("思考强度高一点"), since 高/低 alone are everywhere.
var effortWords = map[string]struct{ en, zh []string }{
	"low":    {[]string{"low"}, []string{"低"}},
	"medium": {[]string{"medium"}, []string{"中等", "中"}},
	"high":   {[]string{"high"}, []string{"高"}},
	"xhigh":  {[]string{"xhigh", "extra high"}, []string{"超高", "很高"}},
	"max":    {[]string{"max", "maximum"}, []string{"最高", "最大"}},
}

func mentionsKind(text, kind string) bool {
	t := strings.ToLower(text)
	for _, w := range kindWords[kinds[kind]] {
		if strings.Contains(t, w) {
			return true
		}
	}
	for _, w := range kindWords[kind] {
		if strings.Contains(t, w) {
			return true
		}
	}
	return false
}

func mentionsModel(text, model string) bool {
	t := strings.ToLower(text)
	flat := alnumOnly.ReplaceAllString(t, "")
	parts := modelSplit.Split(strings.ToLower(model), -1)
	// The id, or its start ("gpt-5.5" for gpt-5.5-codex), written any way.
	for n := len(parts); n >= 2; n-- {
		if p := alnumOnly.ReplaceAllString(strings.Join(parts[:n], ""), ""); len(p) >= 4 && strings.Contains(flat, p) {
			return true
		}
	}
	// A distinctive word of it ("opus", "sonnet", "flash").
	for _, p := range parts {
		if len(p) >= 4 && !genericModelWords[p] && strings.Trim(p, "0123456789") != "" && strings.Contains(t, p) {
			return true
		}
	}
	return false
}

func mentionsEffort(text, effort string) bool {
	t := strings.ToLower(text)
	w, ok := effortWords[effort]
	if !ok {
		return strings.Contains(t, strings.ToLower(effort))
	}
	for _, e := range w.en {
		if regexp.MustCompile(`\b` + regexp.QuoteMeta(e) + `\b`).MatchString(t) {
			return true
		}
	}
	if effortTopic.MatchString(t) {
		for _, z := range w.zh {
			if strings.Contains(t, z) {
				return true
			}
		}
	}
	return false
}

// humanWordsLocked: the human's latest message (a chat line or one of their
// timers), which is what the router is acting on.
func (sp *Super) humanWordsLocked() string {
	for i := len(sp.st.Triggers) - 1; i >= 0; i-- {
		if t := sp.st.Triggers[i]; t.Source == "chat" || t.Source == "timer" {
			return t.Text
		}
	}
	return ""
}

// gateSpawn drops the launch choices the human didn't ask for. It returns
// what is left and a note for each dropped one.
func (sp *Super) gateSpawn(kind, model, effort string) (string, string, string, []string) {
	sp.mu.Lock()
	words := sp.humanWordsLocked()
	def := sp.st.Settings.WorkerKind
	sp.mu.Unlock()
	var dropped []string
	if kind != "" && kinds[kind] != kinds[def] && !mentionsKind(words, kind) {
		dropped = append(dropped, "--kind "+kind+" ignored: the human didn't ask for it; using the default ("+def+")")
		kind = ""
	}
	if model != "" && !mentionsModel(words, model) {
		dropped = append(dropped, "--model "+model+" ignored: the human didn't name it; using the default")
		model = ""
	}
	if effort != "" && !mentionsEffort(words, effort) {
		dropped = append(dropped, "--effort "+effort+" ignored: the human didn't ask for it; using the default")
		effort = ""
	}
	return kind, model, effort, dropped
}
