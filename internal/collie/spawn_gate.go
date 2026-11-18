package collie

import (
	"regexp"
	"strings"
	"time"
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
	// A run of its parts, as people shorten it ("6.1sol" for gpt-6.1-sol),
	// with at least one real word in it, not just numbers or "medium".
	for i := range parts {
		for j := i + 2; j <= len(parts); j++ {
			word := false
			for _, p := range parts[i:j] {
				word = word || (!genericModelWords[p] && strings.Trim(p, "0123456789") != "")
			}
			if p := alnumOnly.ReplaceAllString(strings.Join(parts[i:j], ""), ""); word && len(p) >= 4 && strings.Contains(flat, p) {
				return true
			}
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

// humanWordsLocked: what the human said lately (chat lines and their timers),
// which the router is acting on. A choice often comes over a couple of
// messages ("use codex", then "with 6.1sol"), so it is the latest message
// plus the ones within a few minutes before it.
func (sp *Super) humanWordsLocked() string {
	var words []string
	var last time.Time
	for i := len(sp.st.Triggers) - 1; i >= 0 && len(words) < humanWindowMsgs; i-- {
		t := sp.st.Triggers[i]
		if t.Source != "chat" && t.Source != "timer" {
			continue
		}
		if last.IsZero() {
			last = t.At
		} else if last.Sub(t.At) > humanWindow {
			break
		}
		words = append(words, t.Text)
	}
	// A short answer ("可以", "好") takes up what Collie just asked ("要不要改用
	// codex？"): the question counts as the human's words.
	if len(words) > 0 && len([]rune(strings.TrimSpace(words[0]))) <= shortAnswer {
		if q := sp.lastQuestionLocked(last); q != "" {
			words = append(words, q)
		}
	}
	return strings.Join(words, "\n")
}

// lastQuestionLocked: Collie's latest reply before the human's message at
// `before`, if it asked something.
func (sp *Super) lastQuestionLocked(before time.Time) string {
	for i := len(sp.st.Entries) - 1; i >= 0; i-- {
		e := sp.st.Entries[i]
		if e.Role != "collie" || e.At.After(before) {
			continue
		}
		if before.Sub(e.At) > humanWindow || !strings.ContainsAny(e.Text, "?？") {
			return ""
		}
		return e.Text
	}
	return ""
}

// earlierTriesLocked: workers started for the same job in the last hours,
// named like it with a number added or changed. Names Collie made up
// ("codex-2") say nothing about the job.
func (sp *Super) earlierTriesLocked(name string) []*Worker {
	stem := strings.TrimRight(strings.ToLower(strings.TrimSpace(name)), "-0123456789")
	if len(stem) < 3 || kinds[stem] != "" || stem == "agy" {
		return nil
	}
	var out []*Worker
	for n, w := range sp.st.Workers {
		if strings.TrimRight(n, "-0123456789") == stem && time.Since(w.CreatedAt) < 12*time.Hour {
			out = append(out, w)
		}
	}
	return out
}

const (
	humanWindow     = 10 * time.Minute
	humanWindowMsgs = 6
	shortAnswer     = 12 // runes
)

// gateSpawn drops the launch choices the human didn't ask for. It returns
// what is left and a note for each dropped one.
func (sp *Super) gateSpawn(kind, model, effort, name string) (string, string, string, []string) {
	sp.mu.Lock()
	words := sp.humanWordsLocked()
	def := sp.st.Settings.WorkerKind
	tries := sp.earlierTriesLocked(name)
	sp.mu.Unlock()
	// A retry of the same job (persona-off-mac → persona-off-mac3) keeps what
	// the human chose for it, however long ago they said so.
	triedWith := func(f func(*Worker) bool) bool {
		for _, w := range tries {
			if f(w) {
				return true
			}
		}
		return false
	}
	var dropped []string
	if kind != "" && kinds[kind] != kinds[def] && !mentionsKind(words, kind) &&
		!triedWith(func(w *Worker) bool { return kinds[w.Kind] == kinds[kind] }) {
		dropped = append(dropped, "--kind "+kind+" ignored: the human didn't ask for it; using the default ("+def+")")
		kind = ""
	}
	if model != "" && !mentionsModel(words, model) && !triedWith(func(w *Worker) bool { return w.Model == model }) {
		dropped = append(dropped, "--model "+model+" ignored: the human didn't name it; using the default")
		model = ""
	}
	if effort != "" && !mentionsEffort(words, effort) && !triedWith(func(w *Worker) bool { return w.Effort == effort }) {
		dropped = append(dropped, "--effort "+effort+" ignored: the human didn't ask for it; using the default")
		effort = ""
	}
	return kind, model, effort, dropped
}
