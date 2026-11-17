package collie

import (
	"bufio"
	"bytes"
	"encoding/json"
	"io"
	"os"
	"sync"
	"time"
)

// How full an agent's context window is, read from the tail of its transcript.
// Claude Code logs usage on every assistant turn; Codex logs token_count events
// that also carry the model's window.

type ctxUsage struct {
	Used   int `json:"used"`
	Window int `json:"window"`
}

func (u ctxUsage) Pct() int {
	if u.Window <= 0 || u.Used <= 0 {
		return 0
	}
	return u.Used * 100 / u.Window
}

var usageCache = struct {
	sync.Mutex
	m map[string]usageMark
}{m: map[string]usageMark{}}

type usageMark struct {
	mod  time.Time
	size int64
	u    ctxUsage
}

// contextUsage returns the latest usage in a transcript; claudeWindow is the
// window to assume for Claude (its logs don't say).
func contextUsage(path, format string, claudeWindow int) ctxUsage {
	fi, err := os.Stat(path)
	if err != nil {
		return ctxUsage{}
	}
	usageCache.Lock()
	m, ok := usageCache.m[path]
	usageCache.Unlock()
	if !ok || !m.mod.Equal(fi.ModTime()) || m.size != fi.Size() {
		m = usageMark{mod: fi.ModTime(), size: fi.Size(), u: readUsage(path, format, fi.Size())}
		usageCache.Lock()
		usageCache.m[path] = m
		usageCache.Unlock()
	}
	u := m.u
	if format == "claude" {
		if claudeWindow <= 0 {
			claudeWindow = 200000
		}
		u.Window = claudeWindow
	}
	return u
}

func readUsage(path, format string, size int64) ctxUsage {
	f, err := os.Open(path)
	if err != nil {
		return ctxUsage{}
	}
	defer f.Close()
	const tail = 1 << 20
	if size > tail {
		f.Seek(size-tail, io.SeekStart)
	}
	var last ctxUsage
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64<<10), 16<<20)
	for sc.Scan() {
		line := sc.Bytes()
		switch format {
		case "claude":
			if !bytes.Contains(line, []byte(`"usage"`)) {
				continue
			}
			var e struct {
				Type      string `json:"type"`
				Sidechain bool   `json:"isSidechain"`
				Message   struct {
					Usage *struct {
						In     int `json:"input_tokens"`
						Read   int `json:"cache_read_input_tokens"`
						Create int `json:"cache_creation_input_tokens"`
						Out    int `json:"output_tokens"`
					} `json:"usage"`
				} `json:"message"`
			}
			if json.Unmarshal(line, &e) != nil || e.Type != "assistant" || e.Sidechain || e.Message.Usage == nil {
				continue
			}
			u := e.Message.Usage
			if n := u.In + u.Read + u.Create + u.Out; n > 0 {
				last.Used = n
			}
		case "codex":
			if !bytes.Contains(line, []byte(`"token_count"`)) {
				continue
			}
			var e struct {
				Payload struct {
					Type string `json:"type"`
					Info *struct {
						Last struct {
							Total int `json:"total_tokens"`
						} `json:"last_token_usage"`
						Window int `json:"model_context_window"`
					} `json:"info"`
				} `json:"payload"`
			}
			if json.Unmarshal(line, &e) != nil || e.Payload.Type != "token_count" || e.Payload.Info == nil {
				continue
			}
			if e.Payload.Info.Last.Total > 0 {
				last = ctxUsage{Used: e.Payload.Info.Last.Total, Window: e.Payload.Info.Window}
			}
		}
	}
	return last
}
