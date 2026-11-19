package collie

import (
	"context"
	"regexp"
	"strings"
	"sync"
	"time"
)

// An agent that has stopped may still have work going: Antigravity sets a
// timer and ends its turn, Claude leaves a shell running in the background.
// herdr calls that done or idle; the agent's footer says "1 task(s)" or
// "1 background task". Such an agent shows as waiting.

var bgFooter = regexp.MustCompile(`(?i)\b[1-9]\d*\s+(background\s+(task|shell|job)s?\b|task\(s\)|shells?\s+running\b)`)

// hasBackground looks at the bottom few lines of a screen, where the footer is.
func hasBackground(screen string) bool {
	lines := strings.Split(strings.TrimRight(screen, "\n "), "\n")
	seen := 0
	for i := len(lines) - 1; i >= 0 && seen < 3; i-- {
		l := strings.TrimSpace(lines[i])
		if l == "" {
			continue
		}
		seen++
		if bgFooter.MatchString(l) {
			return true
		}
	}
	return false
}

var bgSeen = struct {
	sync.Mutex
	m map[string]bgEntry
}{m: map[string]bgEntry{}}

type bgEntry struct {
	on   bool
	at   time.Time
	busy bool
}

// waiting says whether a stopped agent has background work, from a look at
// its screen at most every few seconds; the look itself happens off the
// caller's path, so the state stream never waits on a screen read.
func (s *Server) waiting(pane, status string) bool {
	if status != "done" && status != "idle" {
		return false
	}
	bgSeen.Lock()
	defer bgSeen.Unlock()
	e := bgSeen.m[pane]
	if !e.busy && time.Since(e.at) > 4*time.Second {
		e.busy = true
		bgSeen.m[pane] = e
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			on := false
			if r, err := s.herdr.Read(ctx, pane, "detection", "text", 0); err == nil {
				on = hasBackground(r.Text)
			}
			bgSeen.Lock()
			bgSeen.m[pane] = bgEntry{on: on, at: time.Now()}
			if len(bgSeen.m) > 500 {
				for k, v := range bgSeen.m {
					if time.Since(v.at) > 10*time.Minute {
						delete(bgSeen.m, k)
					}
				}
			}
			bgSeen.Unlock()
		}()
	}
	return e.on
}
