package collie

import (
	"bufio"
	"database/sql"
	"encoding/json"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
)

// Antigravity does not tell herdr which conversation a pane is running, so we
// pair them ourselves. Showing the wrong conversation would leak an unrelated
// session (or a child subagent transcript) into this pane, so a candidate must
// be provably this pane's:
//
//   - Collie itself started the pane with --conversation/--resume <id>; or
//   - agy's own index (conversation_summaries.db) lists the pane's cwd as a
//     top-level (non-subagent, non-killed) conversation's workspace (after
//     resolving symlinks), the transcript's first entry is not a subagent
//     SYSTEM_MESSAGE and does not belong to another Collie worker/router, and
//     the conversation was born after the agent started (or within the last few
//     hours).
//
// Each transcript is claimed by at most one pane.

var agyIndex struct {
	sync.Mutex
	root string
	at   time.Time
	ws   map[string][]string // top-level conversation id → workspace paths
}

func forgetAgyIndex() {
	agyIndex.Lock()
	agyIndex.root = ""
	agyIndex.at = time.Time{}
	agyIndex.ws = nil
	agyIndex.Unlock()
}

type agyMeta struct {
	born       time.Time
	subagent   bool
	workerName string
	isRouter   bool
}

var agyWorkerLineRe = regexp.MustCompile(`You are worker "([^"]+)", managed by Collie`)

// agyTranscriptMeta reads only the first line of an Antigravity transcript to
// recover its immutable creation timestamp (dir mtime mutates on Linux whenever
// artifacts are saved in brain/<id>/), detect subagent transcripts started by a
// parent SYSTEM_MESSAGE, and identify Collie worker/router prompts.
func agyTranscriptMeta(path string) agyMeta {
	f, err := os.Open(path)
	if err != nil {
		return agyMeta{}
	}
	defer f.Close()
	r := bufio.NewReaderSize(f, 64<<10)
	line, err := r.ReadBytes('\n')
	if err != nil && len(line) == 0 {
		return agyMeta{}
	}
	var e struct {
		Type      string `json:"type"`
		Source    string `json:"source"`
		CreatedAt string `json:"created_at"`
		Content   string `json:"content"`
	}
	if json.Unmarshal(line, &e) != nil {
		return agyMeta{}
	}
	var m agyMeta
	if e.CreatedAt != "" {
		if t, err := time.Parse(time.RFC3339Nano, e.CreatedAt); err == nil {
			m.born = t
		}
	}
	if e.Type == "SYSTEM_MESSAGE" || (e.Source == "SYSTEM" && e.Type != "" && e.Type != "USER_INPUT") {
		m.subagent = true
	}
	if match := agyWorkerLineRe.FindStringSubmatch(e.Content); match != nil {
		m.workerName = match[1]
	}
	if strings.Contains(e.Content, "[collie] Session start.") {
		m.isRouter = true
	}
	return m
}

// agySummaryColumns inspects conversation_summaries columns so queries work
// across Antigravity versions (older schemas may omit parent_conversation_id or
// agent_name).
func agySummaryColumns(db *sql.DB) map[string]bool {
	cols := map[string]bool{}
	rows, err := db.Query(`PRAGMA table_info(conversation_summaries)`)
	if err != nil {
		return cols
	}
	defer rows.Close()
	for rows.Next() {
		var cid int
		var name, typ string
		var notNull, pk int
		var dflt sql.NullString
		if rows.Scan(&cid, &name, &typ, &notNull, &dflt, &pk) == nil {
			cols[name] = true
		}
	}
	return cols
}

func agyTopLevelWhere(cols map[string]bool) string {
	var parts []string
	if cols["nesting_depth"] {
		parts = append(parts, "COALESCE(nesting_depth, 0) = 0")
	}
	if cols["killed"] {
		parts = append(parts, "COALESCE(killed, 0) = 0")
	}
	if cols["parent_conversation_id"] {
		parts = append(parts, "COALESCE(parent_conversation_id, '') = ''")
	}
	if cols["agent_name"] {
		parts = append(parts, "COALESCE(agent_name, '') = ''")
	}
	if len(parts) == 0 {
		return ""
	}
	return " AND " + strings.Join(parts, " AND ")
}

func (s *Server) paneName(pane string) string {
	if s == nil || s.herdr == nil || pane == "" {
		return ""
	}
	return s.herdr.Name(pane)
}

// agyPaneSessions remembers the conversation id Collie explicitly passed to
// `agy --conversation <id>` (e.g., when taking over an idle Antigravity app
// session or resuming from history) so the pane maps directly to its transcript
// even when the conversation is older than the pane or lives in the app dir.
var agyPaneSessions = struct {
	sync.Mutex
	m map[string]string // pane id → conversation id
}{m: map[string]string{}}

func (s *Server) rememberAgySession(pane, session string) {
	if pane == "" || session == "" {
		return
	}
	local, _ := split(pane)
	agyPaneSessions.Lock()
	agyPaneSessions.m[local] = session
	if pane != local {
		agyPaneSessions.m[pane] = session
	}
	agyPaneSessions.Unlock()
}

func forgetAgySession(pane string) {
	local, _ := split(pane)
	agyPaneSessions.Lock()
	delete(agyPaneSessions.m, local)
	delete(agyPaneSessions.m, pane)
	agyPaneSessions.Unlock()
}

func agySessionForPane(pane string) string {
	if pane == "" {
		return ""
	}
	local, _ := split(pane)
	agyPaneSessions.Lock()
	defer agyPaneSessions.Unlock()
	if id := agyPaneSessions.m[pane]; id != "" {
		return id
	}
	return agyPaneSessions.m[local]
}

// normPath canonicalizes a filesystem path, resolving symlinks when possible
// so e.g. /home/<user>/... and /data00/home/<user>/... compare equal.
func normPath(p string) string {
	if p == "" {
		return ""
	}
	p = filepath.Clean(p)
	if r, err := filepath.EvalSymlinks(p); err == nil {
		p = r
	}
	return strings.TrimSuffix(p, string(filepath.Separator))
}

func fileURIToPath(raw string) string {
	if strings.HasPrefix(raw, "file://") {
		if u, err := url.Parse(raw); err == nil && u.Path != "" {
			if p, err := url.PathUnescape(u.Path); err == nil {
				return strings.TrimSuffix(p, "/")
			}
			return strings.TrimSuffix(u.Path, "/")
		}
		raw = strings.TrimPrefix(raw, "file://")
	}
	return strings.TrimSuffix(raw, "/")
}

// agyConversationArg extracts the conversation id from agy CLI arguments
// (--conversation, --resume, -r, and legacy -c when followed by an id).
func agyConversationArg(args []string) string {
	for i, a := range args {
		switch {
		case a == "--conversation" || a == "--resume" || a == "-r" || a == "-c":
			if i+1 < len(args) && conversationRe.MatchString(args[i+1]) {
				return args[i+1]
			}
		case strings.HasPrefix(a, "--conversation="):
			if v := strings.TrimPrefix(a, "--conversation="); conversationRe.MatchString(v) {
				return v
			}
		case strings.HasPrefix(a, "--resume="):
			if v := strings.TrimPrefix(a, "--resume="); conversationRe.MatchString(v) {
				return v
			}
		case strings.HasPrefix(a, "-r="):
			if v := strings.TrimPrefix(a, "-r="); conversationRe.MatchString(v) {
				return v
			}
		}
	}
	return ""
}

func agyWorkspaces(root string) map[string][]string {
	agyIndex.Lock()
	defer agyIndex.Unlock()
	if agyIndex.root == root && time.Since(agyIndex.at) < 3*time.Second && agyIndex.ws != nil {
		return agyIndex.ws
	}
	agyIndex.root, agyIndex.at = root, time.Now()
	m := map[string][]string{}
	path := filepath.Join(root, "conversation_summaries.db")
	if !isFile(path) {
		agyIndex.ws = m
		return m
	}
	db, err := sql.Open("sqlite", "file:"+path+"?mode=ro&_pragma=busy_timeout(2000)")
	if err != nil {
		agyIndex.ws = m
		return m
	}
	defer db.Close()
	cols := agySummaryColumns(db)
	q := `SELECT conversation_id, workspace_uris FROM conversation_summaries WHERE (last_modified_time > ? OR last_modified_time < '0002')` + agyTopLevelWhere(cols)
	rows, err := db.Query(q, time.Now().Add(-48*time.Hour).UTC().Format("2006-01-02 15:04:05"))
	if err != nil {
		agyIndex.ws = m
		return m
	}
	defer rows.Close()
	for rows.Next() {
		var id, raw string
		if rows.Scan(&id, &raw) != nil {
			continue
		}
		var uris []string
		json.Unmarshal([]byte(raw), &uris)
		for _, u := range uris {
			m[id] = append(m[id], fileURIToPath(u))
		}
	}
	agyIndex.ws = m
	return m
}

func (s *Server) agyTranscriptByID(brain, id string) string {
	if id == "" {
		return ""
	}
	for _, b := range []string{brain, filepath.Join(s.home, ".gemini", "antigravity", "brain")} {
		if p := filepath.Join(b, id, ".system_generated", "logs", "transcript.jsonl"); isFile(p) {
			return p
		}
	}
	return ""
}

func (s *Server) guessAgy(brain, pane, cwd string) string {
	if sess := agySessionForPane(pane); sess != "" {
		if p := s.agyTranscriptByID(brain, sess); p != "" {
			return p
		}
	}
	pane, _ = split(pane)
	if pane == "" {
		return ""
	}
	claimed.Lock()
	defer claimed.Unlock()
	for p, owner := range claimed.m {
		if owner == pane && isFile(p) {
			return p
		}
	}
	if cwd == "" {
		return ""
	}
	cwd = normPath(cwd)
	since, known := s.startedAt(pane)
	if !known {
		since = time.Now().Add(-6 * time.Hour)
	}
	wantName := s.paneName(pane)
	index := agyWorkspaces(filepath.Dir(brain))

	files, _ := filepath.Glob(filepath.Join(brain, "*", ".system_generated", "logs", "transcript.jsonl"))
	type cand struct {
		path  string
		born  time.Time
		mod   time.Time
		exact bool
	}
	var cs []cand
	cutoff := since.Add(-10 * time.Second)
	for _, f := range files {
		if _, taken := claimed.m[f]; taken {
			continue
		}
		dir := filepath.Dir(filepath.Dir(filepath.Dir(f)))
		match := false
		for _, w := range index[filepath.Base(dir)] {
			if normPath(w) == cwd {
				match = true
				break
			}
		}
		if !match {
			continue
		}
		meta := agyTranscriptMeta(f)
		// Never pair a child subagent transcript with a parent worker pane.
		if meta.subagent {
			continue
		}
		// Prevent cross-talk between sibling Collie workers (or router) running
		// concurrently in the same cwd.
		if wantName != "" {
			if meta.workerName != "" && meta.workerName != wantName {
				continue
			}
			if meta.isRouter && wantName != routerName {
				continue
			}
			if wantName == routerName && meta.workerName != "" {
				continue
			}
		}
		di, err := os.Stat(dir)
		if err != nil {
			continue
		}
		born := meta.born
		if born.IsZero() {
			born = di.ModTime()
			if bt := birthTime(di); !bt.IsZero() {
				born = bt
			}
		}
		if born.Before(cutoff) {
			continue
		}
		mod := di.ModTime()
		if fi, err := os.Stat(f); err == nil {
			mod = fi.ModTime()
		}
		exact := wantName != "" && (meta.workerName == wantName || (wantName == routerName && meta.isRouter))
		cs = append(cs, cand{path: f, born: born, mod: mod, exact: exact})
	}
	if len(cs) == 0 {
		return ""
	}
	sort.Slice(cs, func(i, j int) bool {
		if cs[i].exact != cs[j].exact {
			return cs[i].exact
		}
		if known {
			// Prefer conversations born at/after pane start (earliest first),
			// then those within the 10s pre-start clock-skew slack (closest first).
			ai, aj := !cs[i].born.Before(since), !cs[j].born.Before(since)
			if ai != aj {
				return ai
			}
			if ai {
				return cs[i].born.Before(cs[j].born)
			}
			return cs[i].born.After(cs[j].born)
		}
		// Unknown start time (e.g. after hub restart): prefer the most recently active.
		if !cs[i].mod.Equal(cs[j].mod) {
			return cs[i].mod.After(cs[j].mod)
		}
		return cs[i].born.After(cs[j].born)
	})
	claimed.m[cs[0].path] = pane
	return cs[0].path
}

// agyCLIHome is where the agy CLI keeps its conversations. The desktop app
// keeps its own in ~/.gemini/antigravity (desktop.go); the CLI, in a
// terminal or a herdr pane, uses antigravity-cli (older ones used the app's).
func (s *Server) agyCLIHome() string {
	if d := filepath.Join(s.home, ".gemini", "antigravity-cli"); isDir(d) {
		return d
	}
	return filepath.Join(s.home, ".gemini", "antigravity")
}

// Only resume in a local workspace. A file URI for another host may have
// the same path as an unrelated directory on this computer.
func agyCLIWorkspace(raw string) string {
	if filepath.IsAbs(raw) {
		return raw
	}
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "file" || u.User != nil ||
		(u.Host != "" && !strings.EqualFold(u.Host, "localhost")) || !filepath.IsAbs(u.Path) {
		return ""
	}
	return u.Path
}

func (s *Server) agyCLIResumeData(id string) bool {
	base := filepath.Join(s.home, ".gemini", "antigravity-cli", "conversations", id)
	return isFile(base+".db") || isFile(base+".pb")
}

// recentAgyCLI uses transcript activity because early CLI sessions can have
// zero timestamps in the summary index, even after the first user message.
func (s *Server) recentAgyCLI() []desktopRow {
	root := filepath.Join(s.home, ".gemini", "antigravity-cli")
	brain := filepath.Join(root, "brain")
	entries, err := os.ReadDir(brain)
	if err != nil {
		return nil
	}
	cutoff := time.Now().Add(-desktopWindow)
	var candidates []desktopRow
	for _, entry := range entries {
		id := entry.Name()
		if !entry.IsDir() || !conversationRe.MatchString(id) {
			continue
		}
		path := filepath.Join(brain, id, ".system_generated", "logs", "transcript.jsonl")
		info, err := os.Stat(path)
		if err != nil || !info.Mode().IsRegular() || info.Size() == 0 || info.ModTime().Before(cutoff) {
			continue
		}
		candidates = append(candidates, desktopRow{id: "ag-cli-" + id, session: id, kind: "agy", app: appAgyCLI,
			path: path, format: "agy", updated: info.ModTime()})
	}
	if len(candidates) == 0 {
		return nil
	}
	sort.SliceStable(candidates, func(i, j int) bool { return candidates[i].updated.After(candidates[j].updated) })
	var meta *sql.Stmt
	if index := filepath.Join(root, "conversation_summaries.db"); isFile(index) {
		db, err := sql.Open("sqlite", "file:"+index+"?mode=ro&_pragma=busy_timeout(2000)")
		if err != nil {
			return nil
		}
		defer db.Close()
		cols := agySummaryColumns(db)
		parentExpr := "''"
		if cols["parent_conversation_id"] {
			parentExpr = "COALESCE(parent_conversation_id, '')"
		}
		agentExpr := "''"
		if cols["agent_name"] {
			agentExpr = "COALESCE(agent_name, '')"
		}
		meta, err = db.Prepare(`SELECT title, preview, workspace_uris, nesting_depth, killed, ` + parentExpr + `, ` + agentExpr + `
			FROM conversation_summaries WHERE conversation_id = ?`)
		if err != nil {
			return nil
		}
		defer meta.Close()
	}
	var rows []desktopRow
	for _, d := range candidates {
		if agyTranscriptMeta(d.path).subagent {
			continue
		}
		if meta != nil {
			var title, preview, raw, parentID, agentName sql.NullString
			var depth, killed int
			err := meta.QueryRow(d.session).Scan(&title, &preview, &raw, &depth, &killed, &parentID, &agentName)
			if (err != nil && err != sql.ErrNoRows) || depth != 0 || killed != 0 || parentID.String != "" || agentName.String != "" {
				continue
			}
			d.title = messageTitle(title.String)
			var uris []string
			if json.Unmarshal([]byte(raw.String), &uris) == nil && len(uris) > 0 {
				d.cwd = agyCLIWorkspace(uris[0])
			}
			if d.title == "" {
				d.title = messageTitle(preview.String)
			}
		}
		if d.title == "" {
			d.title = firstUserLine(d.path, d.format)
		}
		rows = append(rows, d)
		if len(rows) == desktopMax {
			break
		}
	}
	return rows
}
