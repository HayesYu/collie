package collie

import (
	"encoding/json"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// Other hubs this hub's page also shows. Hubs on networks that can't reach
// each other (a devbox, a cloud computer, a Mac mini at home) can all be
// reached from the browser: the page talks to each one directly with the
// token kept here, so every browser signed in to this hub sees the same set.
type remoteHub struct {
	Key   string `json:"key"`
	Name  string `json:"name"`
	URL   string `json:"url"`
	Token string `json:"token"`
}

var hubsMu sync.Mutex

func hubsPath() string { return filepath.Join(collieDir(), "hubs.json") }

func loadHubs() []remoteHub {
	var hs []remoteHub
	if b, err := os.ReadFile(hubsPath()); err == nil {
		json.Unmarshal(b, &hs)
	}
	if hs == nil {
		hs = []remoteHub{}
	}
	return hs
}

func (s *Server) getHubs(w http.ResponseWriter, r *http.Request) {
	hubsMu.Lock()
	defer hubsMu.Unlock()
	writeJSON(w, 200, map[string]any{"hubs": loadHubs()})
}

func (s *Server) setHubs(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Hubs []remoteHub `json:"hubs"`
	}
	if !decode(w, r, &body) {
		return
	}
	seen := map[string]bool{}
	out := []remoteHub{}
	for _, h := range body.Hubs {
		h.Key, h.Name = strings.TrimSpace(h.Key), strings.TrimSpace(h.Name)
		h.URL = strings.TrimRight(strings.TrimSpace(h.URL), "/")
		u, err := url.Parse(h.URL)
		if h.Key == "" || seen[h.Key] || strings.ContainsAny(h.Key, "!/?#&@ ") || err != nil ||
			(u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || h.Token == "" {
			writeJSON(w, 400, map[string]any{"error": "hub 信息不完整：" + h.Name})
			return
		}
		if h.Name == "" {
			h.Name = u.Hostname()
		}
		seen[h.Key] = true
		out = append(out, h)
	}
	b, _ := json.MarshalIndent(out, "", "  ")
	hubsMu.Lock()
	defer hubsMu.Unlock()
	os.MkdirAll(collieDir(), 0o700)
	if err := os.WriteFile(hubsPath(), b, 0o600); err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, 200, map[string]any{"hubs": out})
}
