package collie

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

const (
	dirMaxResults     = 200
	dirMaxScanEntries = 4000
	dirScanChunk      = 256
	dirScanTimeout    = 1500 * time.Millisecond
	dirRemoteTimeout  = 3500 * time.Millisecond
	dirMaxRecent      = 12
)

// DirShortcut is one suggested or recently used directory on a machine.
type DirShortcut struct {
	Path    string `json:"path"`
	Display string `json:"display"`
	Label   string `json:"label,omitempty"`
}

// DirListResponse is returned by GET /api/dirs.
// Existing fields (path, display, parent, dirs) remain backward-compatible.
type DirListResponse struct {
	Machine       string        `json:"machine,omitempty"`
	Home          string        `json:"home,omitempty"`
	Path          string        `json:"path"`
	Display       string        `json:"display"`
	BrowsePath    string        `json:"browse_path,omitempty"`
	BrowseDisplay string        `json:"browse_display,omitempty"`
	Parent        string        `json:"parent,omitempty"`
	ParentDisplay string        `json:"parent_display,omitempty"`
	Prefix        string        `json:"prefix,omitempty"`
	Exists        bool          `json:"exists"`
	Offline       bool          `json:"offline,omitempty"`
	Truncated     bool          `json:"truncated,omitempty"`
	Hidden        bool          `json:"hidden,omitempty"`
	Dirs          []string      `json:"dirs"`
	Recent        []DirShortcut `json:"recent"`
	Note          string        `json:"note,omitempty"`
}

// listDirs powers every directory picker in the web UI (hub, remote nodes, and
// multi-hub). On a hub it also enriches remote node answers with hub-tracked
// recent directories and degrades gracefully when a machine is offline.
func (s *Server) listDirs(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	rawPath := q.Get("path")
	showHidden := q.Get("hidden") == "1" || strings.EqualFold(q.Get("hidden"), "true")

	if s.node {
		resp := s.readLocalDirs(r.Context(), rawPath, showHidden)
		writeJSON(w, 200, resp)
		return
	}

	rawMachine := strings.TrimSpace(q.Get("machine"))
	machine := s.resolveDirMachine(rawMachine)
	if machine == "" {
		resp := s.readLocalDirs(r.Context(), rawPath, showHidden)
		resp.Recent = s.mergeRecentDirs("", s.home, s.realHome(), resp.Recent)
		writeJSON(w, 200, resp)
		return
	}

	resp := s.readRemoteDirs(r.Context(), machine, rawPath, showHidden)
	writeJSON(w, 200, resp)
}

func (s *Server) resolveDirMachine(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" || raw == "hub" || raw == "local" {
		return ""
	}
	if s.herdr != nil {
		return s.machineByName(raw)
	}
	return raw
}

func (s *Server) realHome() string {
	if s.home == "" {
		return ""
	}
	if r, err := filepath.EvalSymlinks(s.home); err == nil && r != "" {
		return filepath.Clean(r)
	}
	return ""
}

func tildeFor(home, realHome, p string) string {
	p = strings.TrimSpace(p)
	if p == "" {
		return "~"
	}
	clean := path.Clean(p)
	for _, h := range []string{home, realHome} {
		h = strings.TrimRight(strings.TrimSpace(h), "/")
		if h == "" {
			continue
		}
		if clean == h {
			return "~"
		}
		if strings.HasPrefix(clean, h+"/") {
			return "~" + clean[len(h):]
		}
	}
	return clean
}

func canonicalDirFor(home, realHome, p string) string {
	expanded := expandIn(home, p)
	clean := path.Clean(expanded)
	if realHome != "" && home != "" {
		rh := strings.TrimRight(realHome, "/")
		h := strings.TrimRight(home, "/")
		if clean == rh {
			return h
		}
		if strings.HasPrefix(clean, rh+"/") {
			return h + clean[len(rh):]
		}
	}
	return clean
}

func parentOfDir(dir string) string {
	clean := path.Clean(dir)
	if clean == "" || clean == "/" || clean == "." {
		return ""
	}
	parent := path.Dir(clean)
	if parent == clean {
		return ""
	}
	return parent
}

// parseDirHint extracts whether the raw user input explicitly asks for hidden
// dot-entries (e.g. "~/.c", "~/foo/.", ".") and whether it ends with "/".
func parseDirHint(raw string) (trailingSlash, dotRequested bool) {
	t := strings.TrimSpace(raw)
	if t == "" {
		return false, false
	}
	trailingSlash = strings.HasSuffix(t, "/")
	if t == "." || strings.HasSuffix(t, "/.") {
		return false, true
	}
	if !trailingSlash {
		base := t
		if i := strings.LastIndexByte(t, '/'); i >= 0 {
			base = t[i+1:]
		}
		if strings.HasPrefix(base, ".") && base != ".." {
			dotRequested = true
		}
	}
	return trailingSlash, dotRequested
}

func (s *Server) readLocalDirs(ctx context.Context, rawPath string, showHidden bool) DirListResponse {
	home := s.home
	realHome := s.realHome()
	trailingSlash, dotRequested := parseDirHint(rawPath)
	rawTrim := strings.TrimSpace(rawPath)
	target := canonicalDirFor(home, realHome, rawTrim)

	resp := DirListResponse{
		Home:    home,
		Path:    target,
		Display: tildeFor(home, realHome, target),
		Dirs:    []string{},
		Recent:  localDirShortcuts(home, realHome),
	}

	browseDir := target
	prefix := ""
	wantHidden := showHidden

	// Explicit "/." means "list hidden directories inside this folder".
	if rawTrim == "." || strings.HasSuffix(rawTrim, "/.") {
		wantHidden = true
	}

	if isDir(target) {
		resp.Exists = true
	} else {
		resp.Exists = false
		parent := parentOfDir(target)
		if !trailingSlash && parent != "" && isDir(parent) {
			browseDir = parent
			prefix = path.Base(target)
			if strings.HasPrefix(prefix, ".") && prefix != ".." {
				wantHidden = true
			}
		} else {
			// Walk up to the nearest existing ancestor so breadcrumbs stay useful.
			anc := parent
			for steps := 0; steps < 16 && anc != ""; steps++ {
				if isDir(anc) {
					browseDir = anc
					break
				}
				next := parentOfDir(anc)
				if next == anc {
					break
				}
				anc = next
			}
			if !isDir(browseDir) {
				if home != "" && isDir(home) {
					browseDir = home
				} else {
					browseDir = "/"
				}
			}
			resp.Note = "路径暂不存在，仍可直接手输使用"
		}
	}

	if dotRequested && !resp.Exists {
		wantHidden = true
	}

	resp.BrowsePath = browseDir
	resp.BrowseDisplay = tildeFor(home, realHome, browseDir)
	resp.Parent = parentOfDir(browseDir)
	if resp.Parent != "" {
		resp.ParentDisplay = tildeFor(home, realHome, resp.Parent)
	}
	resp.Prefix = prefix
	resp.Hidden = wantHidden

	dirs, truncated, note := scanSubdirs(ctx, browseDir, prefix, wantHidden)
	resp.Dirs = dirs
	resp.Truncated = truncated
	if note != "" {
		resp.Note = note
	} else if !resp.Exists && resp.Note == "" {
		if len(dirs) == 0 {
			resp.Note = "路径暂不存在，仍可直接手输使用"
		}
	}
	return resp
}

func scanSubdirs(ctx context.Context, dir, prefix string, showHidden bool) ([]string, bool, string) {
	f, err := os.Open(dir)
	if err != nil {
		if os.IsPermission(err) {
			return []string{}, false, "无权限读取该目录，可直接手动输入"
		}
		return []string{}, false, "无法读取该目录，可直接手动输入"
	}
	defer f.Close()

	deadline := time.Now().Add(dirScanTimeout)
	lowerPrefix := strings.ToLower(prefix)
	found := make([]string, 0, 32)
	scanned := 0
	truncated := false
	timedOut := false

	for {
		if ctx.Err() != nil || time.Now().After(deadline) {
			timedOut = true
			truncated = true
			break
		}
		entries, err := f.ReadDir(dirScanChunk)
		for _, e := range entries {
			scanned++
			name := e.Name()
			if name == "" || name == "." || name == ".." {
				continue
			}
			if strings.HasPrefix(name, ".") && !showHidden {
				continue
			}
			if lowerPrefix != "" && !strings.HasPrefix(strings.ToLower(name), lowerPrefix) {
				continue
			}
			if e.IsDir() {
				found = append(found, name)
			} else if e.Type()&os.ModeSymlink != 0 {
				if time.Now().After(deadline) {
					timedOut = true
					truncated = true
					break
				}
				if isDir(filepath.Join(dir, name)) {
					found = append(found, name)
				}
			}
			if scanned >= dirMaxScanEntries {
				truncated = true
				break
			}
		}
		if truncated || err == io.EOF {
			break
		}
		if err != nil {
			break
		}
	}

	sort.Slice(found, func(i, j int) bool {
		li, lj := strings.ToLower(found[i]), strings.ToLower(found[j])
		if li != lj {
			return li < lj
		}
		return found[i] < found[j]
	})

	if len(found) > dirMaxResults {
		found = found[:dirMaxResults]
		truncated = true
	}

	note := ""
	if timedOut {
		note = "目录条目较多，已先展示部分结果，可继续输入前缀筛选"
	} else if truncated {
		note = "目录较多，仅显示前 200 项，可继续输入前缀筛选"
	}
	return found, truncated, note
}

func localDirShortcuts(home, realHome string) []DirShortcut {
	var out []DirShortcut
	if home == "" {
		return out
	}
	cleanHome := path.Clean(home)
	out = append(out, DirShortcut{
		Path:    cleanHome,
		Display: "~",
		Label:   "主目录",
	})
	ws := filepath.Join(cleanHome, "agent-workspace")
	if isDir(ws) {
		out = append(out, DirShortcut{
			Path:    path.Clean(ws),
			Display: tildeFor(cleanHome, realHome, ws),
			Label:   "工作区",
		})
	}
	return out
}

func (s *Server) readRemoteDirs(ctx context.Context, machine, rawPath string, showHidden bool) DirListResponse {
	var mHome string
	if s.herdr != nil {
		for _, m := range s.herdr.Machines() {
			if m.ID == machine {
				mHome = m.Home
				break
			}
		}
	}

	offlineResp := func(note string) DirListResponse {
		target := strings.TrimSpace(rawPath)
		display := target
		if mHome != "" {
			target = expandIn(mHome, target)
			display = tildeFor(mHome, "", target)
		} else if target == "" {
			target = "~"
			display = "~"
		}
		parent := ""
		parentDisp := ""
		if strings.HasPrefix(target, "/") {
			parent = parentOfDir(target)
			if parent != "" {
				parentDisp = tildeFor(mHome, "", parent)
			}
		}
		return DirListResponse{
			Machine:       machine,
			Home:          mHome,
			Path:          target,
			Display:       display,
			BrowsePath:    target,
			BrowseDisplay: display,
			Parent:        parent,
			ParentDisplay: parentDisp,
			Exists:        false,
			Offline:       true,
			Dirs:          []string{},
			Recent:        s.mergeRecentDirs(machine, mHome, "", nil),
			Note:          note,
		}
	}

	if s.herdr == nil {
		return offlineResp("这台电脑当前离线，仍可直接手动输入路径")
	}
	client, err := s.herdr.Client(machine)
	if err != nil {
		return offlineResp("这台电脑当前离线，仍可直接手动输入路径")
	}

	rctx, cancel := context.WithTimeout(ctx, dirRemoteTimeout)
	defer cancel()

	q := url.Values{}
	q.Set("path", rawPath)
	if showHidden {
		q.Set("hidden", "1")
	}
	req, err := http.NewRequestWithContext(rctx, http.MethodGet, "http://node/api/dirs?"+q.Encode(), nil)
	if err != nil {
		return offlineResp("无法连接这台电脑，仍可直接手动输入路径")
	}
	resp, err := client.Do(req)
	if err != nil {
		return offlineResp("读取远端目录超时或连接中断，仍可直接手动输入路径")
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return offlineResp("读取远端目录失败，仍可直接手动输入路径")
	}

	if resp.StatusCode != http.StatusOK {
		// Backward compatibility: older nodes return 400 when a typed path
		// does not exist yet. Degrade gracefully instead of surfacing an error.
		res := offlineResp("路径暂不存在，仍可直接手输使用")
		res.Offline = false
		return res
	}

	var out DirListResponse
	if err := json.Unmarshal(body, &out); err != nil {
		return offlineResp("远端目录返回异常，仍可直接手动输入路径")
	}
	out.Machine = machine
	if out.Home == "" {
		out.Home = mHome
	}
	if out.Dirs == nil {
		out.Dirs = []string{}
	}
	// Compatibility with older nodes that only return path/display/parent/dirs.
	if out.BrowsePath == "" {
		out.BrowsePath = out.Path
		out.BrowseDisplay = out.Display
		out.Exists = true
		if out.Parent == out.Path {
			out.Parent = ""
		}
	}
	if out.BrowseDisplay == "" && out.BrowsePath != "" {
		out.BrowseDisplay = tildeFor(out.Home, "", out.BrowsePath)
	}
	if out.ParentDisplay == "" && out.Parent != "" {
		out.ParentDisplay = tildeFor(out.Home, "", out.Parent)
	}
	out.Recent = s.mergeRecentDirs(machine, out.Home, "", out.Recent)
	return out
}

func (s *Server) mergeRecentDirs(machine, home, realHome string, base []DirShortcut) []DirShortcut {
	seen := map[string]bool{}
	out := make([]DirShortcut, 0, dirMaxRecent)

	add := func(raw, label string) {
		raw = strings.TrimSpace(raw)
		if raw == "" {
			return
		}
		var full, disp string
		if home != "" {
			full = canonicalDirFor(home, realHome, raw)
			disp = tildeFor(home, realHome, full)
		} else {
			full = path.Clean(raw)
			disp = full
		}
		key := disp
		if key == "" {
			key = full
		}
		if seen[key] {
			return
		}
		seen[key] = true
		out = append(out, DirShortcut{Path: full, Display: disp, Label: label})
	}

	for _, sc := range base {
		p := sc.Path
		if p == "" {
			p = sc.Display
		}
		add(p, sc.Label)
	}
	if len(out) == 0 {
		if home != "" {
			add(home, "主目录")
		} else {
			add("~", "主目录")
		}
	}

	type timedDir struct {
		dir string
		at  time.Time
	}
	var recents []timedDir

	if s.sup != nil {
		s.sup.mu.Lock()
		for _, w := range s.sup.st.Workers {
			if w == nil || w.Machine != machine || strings.TrimSpace(w.Cwd) == "" {
				continue
			}
			at := w.CreatedAt
			if w.ReportAt.After(at) {
				at = w.ReportAt
			}
			recents = append(recents, timedDir{dir: w.Cwd, at: at})
		}
		st := s.sup.st.Settings
		if st.WorkerMachine == machine && strings.TrimSpace(st.WorkerCwd) != "" {
			recents = append(recents, timedDir{dir: st.WorkerCwd, at: time.Time{}})
		}
		s.sup.mu.Unlock()
	}

	history.Lock()
	s.loadHistory()
	for _, h := range history.list {
		if h == nil || h.Machine != machine || strings.TrimSpace(h.Cwd) == "" {
			continue
		}
		recents = append(recents, timedDir{dir: h.Cwd, at: h.Ended})
	}
	history.Unlock()

	sort.SliceStable(recents, func(i, j int) bool {
		return recents[i].at.After(recents[j].at)
	})

	for _, r := range recents {
		if len(out) >= dirMaxRecent {
			break
		}
		add(r.dir, "")
	}
	return out
}
