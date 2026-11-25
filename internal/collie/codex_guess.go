package collie

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// Codex does not need a global ~/.codex/hooks.json integration for Collie to
// drive it; without the hook, herdr leaves agent_session empty. Like guessAgy,
// guessCodex pairs a pane with its rollout-*.jsonl under $CODEX_HOME/sessions
// using cwd + creation time and claims each rollout exclusively per pane so
// multiple panes in the same directory never cross-talk.

func sameDir(a, b string) bool {
	if a == "" || b == "" {
		return false
	}
	a = strings.TrimSuffix(filepath.Clean(a), "/")
	b = strings.TrimSuffix(filepath.Clean(b), "/")
	if a == b {
		return true
	}
	ra, err1 := filepath.EvalSymlinks(a)
	rb, err2 := filepath.EvalSymlinks(b)
	if err1 == nil && err2 == nil {
		return strings.TrimSuffix(filepath.Clean(ra), "/") == strings.TrimSuffix(filepath.Clean(rb), "/")
	}
	return false
}

// codexRolloutMeta reads a rollout's session id, cwd, and creation timestamp
// from its session_meta header line, falling back to birthTime / ModTime.
func codexRolloutMeta(path string, fi os.FileInfo) (id, cwd string, born time.Time) {
	if f, err := os.Open(path); err == nil {
		defer f.Close()
		r := bufio.NewReaderSize(f, 64<<10)
		line, _ := r.ReadBytes('\n')
		var m struct {
			Timestamp string `json:"timestamp"`
			Payload   struct {
				ID        string `json:"id"`
				Cwd       string `json:"cwd"`
				Timestamp string `json:"timestamp"`
			} `json:"payload"`
		}
		if json.Unmarshal(line, &m) == nil {
			id, cwd = m.Payload.ID, m.Payload.Cwd
			for _, ts := range []string{m.Payload.Timestamp, m.Timestamp} {
				if ts != "" {
					if t, err := time.Parse(time.RFC3339Nano, ts); err == nil {
						born = t
						break
					}
				}
			}
		}
	}
	if born.IsZero() && fi != nil {
		if bt := birthTime(fi); !bt.IsZero() {
			born = bt
		} else {
			born = fi.ModTime()
		}
	}
	return id, cwd, born
}

// codexSessionID returns the resume id for a Codex rollout path.
func codexSessionID(path string) string {
	if id := sessionID("path", path); id != "" {
		return id
	}
	id, _ := codexMeta(path)
	return id
}

// codexTitlePart strips the " | <user>" suffix Codex appends to terminal titles.
func codexTitlePart(tt string) string {
	if i := strings.LastIndex(tt, " | "); i > 0 {
		tt = tt[:i]
	}
	return strings.TrimSpace(tt)
}

// claimTranscript explicitly binds a known transcript path (e.g. from
// `codex resume <id>` or a terminal-title match in session_index.jsonl) to pane.
func claimTranscript(path, pane string) {
	pane, _ = split(pane)
	if path == "" || pane == "" {
		return
	}
	claimed.Lock()
	defer claimed.Unlock()
	for p, owner := range claimed.m {
		if owner == pane && p != path {
			delete(claimed.m, p)
		}
	}
	claimed.m[path] = pane
}

func (s *Server) guessCodex(root, pane, cwd string) string {
	pane, _ = split(pane)
	if pane == "" {
		return ""
	}
	claimed.Lock()
	defer claimed.Unlock()
	for p, owner := range claimed.m {
		if owner == pane && strings.HasPrefix(filepath.Base(p), "rollout-") && isFile(p) {
			return p
		}
	}
	if cwd == "" {
		return ""
	}
	since, known := s.startedAt(pane)
	if !known {
		since = time.Now().Add(-6 * time.Hour)
	}
	cutoff := since.Add(-10 * time.Second)

	files, _ := filepath.Glob(filepath.Join(root, "sessions", "*", "*", "*", "rollout-*.jsonl"))
	type cand struct {
		path string
		born time.Time
		mod  time.Time
	}
	var cs []cand
	for _, f := range files {
		if _, taken := claimed.m[f]; taken {
			continue
		}
		fi, err := os.Stat(f)
		if err != nil || fi.ModTime().Before(cutoff) {
			continue
		}
		_, metaCwd, born := codexRolloutMeta(f, fi)
		if !sameDir(metaCwd, cwd) || born.Before(cutoff) {
			continue
		}
		cs = append(cs, cand{path: f, born: born, mod: fi.ModTime()})
	}
	if len(cs) == 0 {
		return ""
	}
	if known {
		// Prefer rollouts born at/after start (earliest first), then those in
		// the 10s pre-start clock-skew slack (closest to start first).
		sort.Slice(cs, func(i, j int) bool {
			ai, aj := !cs[i].born.Before(since), !cs[j].born.Before(since)
			if ai != aj {
				return ai
			}
			if ai {
				return cs[i].born.Before(cs[j].born)
			}
			return cs[i].born.After(cs[j].born)
		})
	} else {
		// Unknown start time: prefer the most recently active rollout.
		sort.Slice(cs, func(i, j int) bool {
			if !cs[i].mod.Equal(cs[j].mod) {
				return cs[i].mod.After(cs[j].mod)
			}
			return cs[i].born.After(cs[j].born)
		})
	}
	claimed.m[cs[0].path] = pane
	return cs[0].path
}
