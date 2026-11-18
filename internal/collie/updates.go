package collie

import (
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

// The hub looks up the latest release now and then, so the devices page can
// say which computers run an older Collie. The release page's redirect
// (…/releases/latest → …/tag/vX.Y.Z) needs no API token or rate limit.

const releasesLatest = "https://github.com/" + repo + "/releases/latest"
const repo = "lihuanyu0w0/collie"

var latestRel struct {
	sync.Mutex
	once sync.Once
	tag  string
}

// latestVersion returns the newest release tag seen so far ("" until known)
// and starts checking every few hours on first use.
func latestVersion() string {
	latestRel.once.Do(func() {
		go func() {
			for {
				if t := fetchLatestTag(); t != "" {
					latestRel.Lock()
					latestRel.tag = t
					latestRel.Unlock()
				}
				time.Sleep(6 * time.Hour)
			}
		}()
	})
	latestRel.Lock()
	defer latestRel.Unlock()
	return latestRel.tag
}

func fetchLatestTag() string {
	c := &http.Client{
		Timeout:       20 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	resp, err := c.Head(releasesLatest)
	if err != nil {
		return ""
	}
	resp.Body.Close()
	loc := resp.Header.Get("Location")
	i := strings.LastIndex(loc, "/tag/")
	if i < 0 {
		return ""
	}
	if t := loc[i+5:]; parseVersion(t) != nil {
		return t
	}
	return ""
}

// parseVersion reads "v1.2.3" (nil for "dev" and anything else).
func parseVersion(v string) []int {
	v, ok := strings.CutPrefix(v, "v")
	if !ok {
		return nil
	}
	parts := strings.Split(v, ".")
	if len(parts) != 3 {
		return nil
	}
	out := make([]int, 3)
	for i, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil {
			return nil
		}
		out[i] = n
	}
	return out
}

// outdated: have is a release older than latest. Builds from source ("dev")
// and unknown versions (nodes from before v0.2.5 don't say) aren't judged.
func outdated(have, latest string) bool {
	a, b := parseVersion(have), parseVersion(latest)
	if a == nil || b == nil {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return a[i] < b[i]
		}
	}
	return false
}

func (s *Server) getVersion(w http.ResponseWriter, r *http.Request) {
	latest := latestVersion()
	var old []string
	for _, m := range s.herdr.Machines() {
		v := m.Version
		if m.Local {
			v = version
		}
		if (m.Local || m.Online) && outdated(v, latest) {
			old = append(old, m.Name)
		}
	}
	writeJSON(w, 200, map[string]any{"version": version, "latest": latest, "outdated": old})
}
