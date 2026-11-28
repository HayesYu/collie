package collie

import (
	"html"
	"io"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

// Read-only notebook aggregation for the top-right floating white-paper card.
// Directly reads existing memory/tasks/*.md, today's deliverables from chat
// entries (--file or mentioned file paths), and upcoming enabled timers with
// zero extra state machine.

type notebookTask struct {
	ID        string    `json:"id"`
	Title     string    `json:"title"`
	Note      string    `json:"note,omitempty"`
	Tone      string    `json:"tone"` // active | attn | idle
	Worker    string    `json:"worker,omitempty"`
	EntryID   string    `json:"entry_id,omitempty"`
	Path      string    `json:"path,omitempty"`
	FilePath  string    `json:"file_path,omitempty"`
	UpdatedAt time.Time `json:"updated_at,omitempty"`
}

type notebookArtifact struct {
	Path    string    `json:"path"`
	Name    string    `json:"name"`
	Title   string    `json:"title"`
	Ext     string    `json:"ext"`
	EntryID string    `json:"entry_id,omitempty"`
	At      time.Time `json:"at,omitempty"`
}

type notebookTimer struct {
	ID      string    `json:"id"`
	Spec    string    `json:"spec"`
	Text    string    `json:"text"`
	NextRun time.Time `json:"next_run"`
	Target  string    `json:"target,omitempty"`
	Owner   string    `json:"owner,omitempty"`
}

type notebookView struct {
	Date      string             `json:"date"`
	Tasks     []notebookTask     `json:"tasks"`
	Artifacts []notebookArtifact `json:"artifacts"`
	Timers    []notebookTimer    `json:"timers"`
}

var (
	nbFilePathRe  = regexp.MustCompile(`(?:^|[\s（(【\[「『"'` + "`" + `,:;：；、])((?:~/|/[A-Za-z0-9._-]+/)[A-Za-z0-9._/@+\-\x{4e00}-\x{9fff}]+\.(?:html?|csv|tsv|md|markdown|mdx|pdf|json|ya?ml|txt|svg|png|jpe?g|webp|xlsx|docx|py|go|tsx?|jsx?|sql|sh))(?::\d+(?:-\d+)?)?`)
	nbHTMLTitleRe = regexp.MustCompile(`(?is)<title[^>]*>(.*?)</title>`)
	nbCheckPrefix = regexp.MustCompile(`^\s*(?:[-*+]\s*\[[ xX]\]\s*|[-*+•>]\s*|\d+[.)]\s+)`)
	nbCheckedLine = regexp.MustCompile(`^\s*[-*+]\s*\[[xX]\]\s*`)
	nbFieldLineRe = regexp.MustCompile(`^\s*(?:\*{1,2}|_{1,2})?([A-Za-z][A-Za-z0-9 _/-]{0,24}|[\p{Han}]{1,8})(?:\*{1,2}|_{1,2})?\s*[:：]\s*(.*)$`)
	nbWordBoundRe = regexp.MustCompile(`[^\p{L}\p{N}_-]+`)
)

func isIgnoredNotebookArtifact(p string) bool {
	low := strings.ToLower(strings.ReplaceAll(strings.TrimSpace(p), "\\", "/"))
	if low == "" || strings.HasPrefix(low, "http://") || strings.HasPrefix(low, "https://") {
		return true
	}
	if strings.Contains(low, "/.collie/uploads/") || strings.Contains(low, "/.collie/images/") ||
		strings.Contains(low, "/.collie/router/memory/") || strings.Contains(low, "/.collie/router/dreams/") {
		return true
	}
	if strings.HasPrefix(low, "/api/") || strings.HasPrefix(low, "/hooks/") {
		return true
	}
	return false
}

func trimLeadingDecorEmoji(s string) string {
	s = strings.TrimSpace(s)
	for s != "" {
		r, sz := utf8.DecodeRuneInString(s)
		if r == utf8.RuneError {
			break
		}
		isDecor := (r >= 0x1F300 && r <= 0x1FAFF) || // pictographs, transport, symbols, supplemental
			(r >= 0x2600 && r <= 0x27BF) || // misc symbols & dingbats (✅ ⚠️ ✨ etc.)
			(r >= 0x23E9 && r <= 0x23FA) || // media/clock symbols (⏳ ⏩ etc.)
			r == 0x25B6 || r == 0x25C0 || r == 0x2139 || // play / info
			r == 0xFE0F || r == 0xFE0E || r == 0x200D // variation selectors & ZWJ
		if !isDecor && !unicode.IsSpace(r) {
			break
		}
		s = s[sz:]
	}
	return strings.TrimSpace(s)
}

func stripMarkdownInline(s string) string {
	s = pendingLinkRe.ReplaceAllString(s, "$1")
	s = strings.NewReplacer("**", "", "__", "", "`", "", "~~", "").Replace(s)
	return trimLeadingDecorEmoji(oneLine(s))
}

func isTaskMetaFieldKey(k string) bool {
	switch strings.ToLower(strings.TrimSpace(k)) {
	case "thread", "worker", "workers", "timer", "timers", "session", "branch", "worktree", "repo", "path", "id", "created", "updated", "owner", "machine", "线程", "话题", "定时器", "分支", "仓库", "路径":
		return true
	}
	return false
}

func taskFieldRank(k string) int {
	switch strings.ToLower(strings.TrimSpace(k)) {
	case "status", "状态", "进展", "当前进展", "进行中", "进度":
		return 1
	case "next", "下一步", "待办", "blocked", "阻塞", "卡点":
		return 2
	case "goal", "目标", "摘要", "summary", "说明":
		return 3
	case "done", "已完成", "完成":
		return 4
	}
	return 0
}

func parseTaskMarkdown(stem, raw string) (title, note string) {
	lines := strings.Split(strings.ReplaceAll(raw, "\r\n", "\n"), "\n")
	inFence := false
	bestRank := 99
	bestNote := ""
	pendingRank := 0
	fallbackUnchecked := ""
	fallbackAny := ""

	consider := func(rank int, val string) {
		val = stripMarkdownInline(val)
		if val == "" {
			return
		}
		if rank < bestRank {
			bestRank = rank
			bestNote = val
		}
	}

	for _, l := range lines {
		trimmed := strings.TrimSpace(l)
		if strings.HasPrefix(trimmed, "```") {
			inFence = !inFence
			continue
		}
		if inFence || trimmed == "" || strings.HasPrefix(trimmed, "---") {
			continue
		}
		if strings.HasPrefix(trimmed, "#") {
			pendingRank = 0
			if title == "" {
				h := stripMarkdownInline(strings.TrimLeft(trimmed, "# "))
				if h != "" {
					title = clip(h, 64)
				}
			}
			continue
		}

		// Indented continuation under an empty labeled field like "- **Goal**:\n  1. ..."
		isIndented := strings.HasPrefix(l, "  ") || strings.HasPrefix(l, "\t")
		if isIndented && pendingRank > 0 {
			val := nbCheckPrefix.ReplaceAllString(trimmed, "")
			consider(pendingRank, val)
			pendingRank = 0
			continue
		}
		pendingRank = 0

		isChecked := nbCheckedLine.MatchString(l)
		unbulleted := nbCheckPrefix.ReplaceAllString(trimmed, "")
		if m := nbFieldLineRe.FindStringSubmatch(unbulleted); len(m) == 3 {
			key, val := m[1], strings.TrimSpace(m[2])
			if isTaskMetaFieldKey(key) {
				continue
			}
			if rk := taskFieldRank(key); rk > 0 {
				if val != "" {
					consider(rk, val)
				} else {
					pendingRank = rk
				}
				continue
			}
		}

		clean := stripMarkdownInline(unbulleted)
		if clean == "" {
			continue
		}
		if !isChecked && fallbackUnchecked == "" {
			fallbackUnchecked = clean
		}
		if fallbackAny == "" {
			fallbackAny = clean
		}
	}

	if bestNote != "" {
		note = clip(bestNote, 96)
	} else if fallbackUnchecked != "" {
		note = clip(fallbackUnchecked, 96)
	} else if fallbackAny != "" {
		note = clip(fallbackAny, 96)
	}

	if title == "" {
		title = strings.ReplaceAll(stem, "-", " ")
		title = strings.ReplaceAll(title, "_", " ")
		title = strings.TrimSpace(title)
		if title == "" {
			title = stem
		}
	}
	return title, note
}

func peekArtifactTitle(localPath, ext, fallbackName string) string {
	ext = strings.ToLower(ext)
	if localPath != "" && (ext == "html" || ext == "htm" || ext == "md" || ext == "markdown") {
		if f, err := os.Open(localPath); err == nil {
			buf, _ := io.ReadAll(io.LimitReader(f, 4096))
			f.Close()
			s := string(buf)
			if ext == "html" || ext == "htm" {
				if m := nbHTMLTitleRe.FindStringSubmatch(s); len(m) == 2 {
					t := strings.TrimSpace(oneLine(html.UnescapeString(m[1])))
					if t != "" {
						return clip(t, 64)
					}
				}
			} else {
				for _, line := range strings.Split(strings.ReplaceAll(s, "\r\n", "\n"), "\n") {
					t := strings.TrimSpace(line)
					if strings.HasPrefix(t, "#") {
						h := stripMarkdownInline(strings.TrimLeft(t, "# "))
						if h != "" {
							return clip(h, 64)
						}
					}
				}
			}
		}
	}
	return fallbackName
}

func sameLocalDay(a, b time.Time) bool {
	al, bl := a.Local(), b.Local()
	return al.Year() == bl.Year() && al.Month() == bl.Month() && al.Day() == bl.Day()
}

func textMentionsToken(haystack, token string) bool {
	if haystack == "" || token == "" {
		return false
	}
	for _, part := range nbWordBoundRe.Split(haystack, -1) {
		if part == token {
			return true
		}
	}
	return false
}

func (s *Server) superNotebook(w http.ResponseWriter, r *http.Request) {
	now := time.Now()
	sp := s.sup

	sp.mu.Lock()
	entries := append([]*Entry(nil), sp.st.Entries...)
	pending := sp.pendingLocked(now)
	workers := make([]*Worker, 0, len(sp.st.Workers))
	for _, wk := range sp.st.Workers {
		cp := *wk
		workers = append(workers, &cp)
	}
	timers := sortedTimers(sp.st.Timers)
	tasksDir := filepath.Join(sp.memDir(), "tasks")
	sp.mu.Unlock()

	sort.Slice(workers, func(i, j int) bool { return workers[i].CreatedAt.After(workers[j].CreatedAt) })
	activeWorkers := make([]*Worker, 0, len(workers))
	for _, wk := range workers {
		if !wk.Ended {
			activeWorkers = append(activeWorkers, wk)
		}
	}

	// 1. Tasks from memory/tasks/*.md (newest modified first), falling back to live workers if none.
	var taskItems []notebookTask
	type rawTaskFile struct {
		path string
		mod  time.Time
		text string
	}
	var rawTasks []rawTaskFile
	if matches, _ := filepath.Glob(filepath.Join(tasksDir, "*.md")); len(matches) > 0 {
		for _, p := range matches {
			if !pathWithinDir(tasksDir, p) {
				continue
			}
			fi, err := os.Stat(p)
			if err != nil || fi.IsDir() {
				continue
			}
			b, err := os.ReadFile(p)
			if err != nil {
				continue
			}
			rawTasks = append(rawTasks, rawTaskFile{path: p, mod: fi.ModTime(), text: string(b)})
		}
		sort.Slice(rawTasks, func(i, j int) bool {
			if !rawTasks[i].mod.Equal(rawTasks[j].mod) {
				return rawTasks[i].mod.After(rawTasks[j].mod)
			}
			return rawTasks[i].path < rawTasks[j].path
		})
	}
	for _, rf := range rawTasks {
		base := filepath.Base(rf.path)
		stem := strings.TrimSuffix(base, ".md")
		title, note := parseTaskMarkdown(stem, rf.text)
		tone := "idle"
		matchedWorker := ""
		matchedEntry := ""
		for _, wk := range activeWorkers {
			if textMentionsToken(rf.text, wk.Name) {
				matchedWorker = wk.Name
				if wk.Status == "blocked" {
					tone = "attn"
				} else if tone != "attn" {
					tone = "active"
				}
				break
			}
		}
		for _, p := range pending {
			if (matchedWorker != "" && strings.Contains(p.Summary, matchedWorker)) ||
				(title != "" && len([]rune(title)) >= 3 && strings.Contains(p.Summary, title)) {
				tone = "attn"
				matchedEntry = p.ID
				break
			}
		}
		taskFilePath := rf.path
		if abs, err := filepath.Abs(rf.path); err == nil {
			taskFilePath = abs
		}
		taskItems = append(taskItems, notebookTask{
			ID:        stem,
			Title:     title,
			Note:      note,
			Tone:      tone,
			Worker:    matchedWorker,
			EntryID:   matchedEntry,
			Path:      "tasks/" + base,
			FilePath:  s.tilde(filepath.ToSlash(taskFilePath)),
			UpdatedAt: rf.mod,
		})
	}
	if len(taskItems) == 0 {
		for _, wk := range activeWorkers {
			title := strings.TrimSpace(wk.Name)
			if g := strings.TrimSpace(oneLine(wk.Task)); g != "" {
				title = clip(stripMarkdownInline(g), 56)
			}
			note := strings.TrimSpace(oneLine(wk.Note))
			if note == "" {
				note = strings.TrimSpace(oneLine(wk.LastReport))
			}
			if note == "" && title != wk.Name {
				note = wk.Name
			}
			tone := "active"
			if wk.Status == "blocked" {
				tone = "attn"
			} else if wk.Status == "idle" || wk.Status == "waiting" {
				tone = "idle"
			}
			taskItems = append(taskItems, notebookTask{
				ID:        "w:" + wk.Name,
				Title:     title,
				Note:      clip(stripMarkdownInline(note), 96),
				Tone:      tone,
				Worker:    wk.Name,
				UpdatedAt: wk.CreatedAt,
			})
		}
	}
	if taskItems == nil {
		taskItems = []notebookTask{}
	}

	// 2. Today's artifacts from explicit Entry.Files and file paths mentioned in today's Collie replies.
	var artifacts []notebookArtifact
	seenPath := map[string]bool{}
	addArtifact := func(rawPath string, e *Entry) {
		p := strings.TrimSpace(rawPath)
		if p == "" || isIgnoredNotebookArtifact(p) {
			return
		}
		expanded := s.expand(p)
		disp := p
		if expanded != "" && !strings.HasPrefix(p, "~/") {
			disp = s.tilde(expanded)
		}
		normKey := strings.ToLower(disp)
		if seenPath[normKey] {
			return
		}
		seenPath[normKey] = true
		name := path.Base(strings.ReplaceAll(disp, "\\", "/"))
		ext := strings.ToLower(strings.TrimPrefix(path.Ext(name), "."))
		if ext == "" {
			return
		}
		title := peekArtifactTitle(expanded, ext, name)
		artifacts = append(artifacts, notebookArtifact{
			Path:    disp,
			Name:    name,
			Title:   title,
			Ext:     ext,
			EntryID: e.ID,
			At:      e.At,
		})
	}

	const maxNotebookArtifacts = 20
	for i := len(entries) - 1; i >= 0 && len(artifacts) < maxNotebookArtifacts; i-- {
		e := entries[i]
		if e == nil || (e.Thread != "" && e.Thread != mainThread) || e.At.IsZero() || !sameLocalDay(e.At, now) {
			continue
		}
		if e.Role != "collie" && !(e.Role == "event" && e.Tone == "done") {
			continue
		}
		for _, fp := range e.Files {
			addArtifact(fp, e)
			if len(artifacts) >= maxNotebookArtifacts {
				break
			}
		}
		if len(artifacts) >= maxNotebookArtifacts {
			break
		}
		for _, m := range nbFilePathRe.FindAllStringSubmatch(e.Text, -1) {
			if len(m) < 2 {
				continue
			}
			addArtifact(m[1], e)
			if len(artifacts) >= maxNotebookArtifacts {
				break
			}
		}
	}
	if artifacts == nil {
		artifacts = []notebookArtifact{}
	}

	// 3. Upcoming enabled timers (sorted by NextRun asc).
	var timerItems []notebookTimer
	const maxNotebookTimers = 20
	for _, tm := range timers {
		if tm == nil || !tm.Enabled || tm.NextRun.IsZero() {
			continue
		}
		timerItems = append(timerItems, notebookTimer{
			ID:      tm.ID,
			Spec:    tm.Spec,
			Text:    clip(oneLine(tm.Text), 96),
			NextRun: tm.NextRun,
			Target:  tm.Target,
			Owner:   tm.Owner,
		})
		if len(timerItems) >= maxNotebookTimers {
			break
		}
	}
	if timerItems == nil {
		timerItems = []notebookTimer{}
	}

	writeJSON(w, 200, notebookView{
		Date:      now.Format("2006-01-02"),
		Tasks:     taskItems,
		Artifacts: artifacts,
		Timers:    timerItems,
	})
}
