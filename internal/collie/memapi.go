package collie

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Collie's memory files, readable and editable from the web UI.

type memFile struct {
	Name        string    `json:"name"`
	Text        string    `json:"text"`
	Preview     string    `json:"preview,omitempty"`
	SummaryOnly bool      `json:"summary_only,omitempty"`
	Size        int64     `json:"size"`
	Mod         time.Time `json:"mod"`
	// The old single-file memory, waiting to be moved into memory/.
	Legacy bool `json:"legacy,omitempty"`
}

type memMetaEntry struct {
	size        int64
	modUnixNano int64
	preview     string
}

func readMemFile(path string) *memFile {
	fi, err := os.Stat(path)
	if err != nil || fi.IsDir() {
		return nil
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	return &memFile{Name: filepath.Base(path), Text: string(b), Size: fi.Size(), Mod: fi.ModTime()}
}

// memPreviewFromText extracts the first non-heading summary line matching the
// web UI's firstLine() helper so collapsed rows do not need full file bodies.
func memPreviewFromText(text string) string {
	for _, raw := range strings.Split(text, "\n") {
		t := strings.TrimSpace(raw)
		if t == "" || strings.HasPrefix(t, "#") {
			continue
		}
		t = strings.TrimLeft(t, ">*- \t")
		t = strings.ReplaceAll(t, "`", "")
		t = strings.ReplaceAll(t, "**", "")
		t = strings.TrimSpace(t)
		if t != "" {
			return clip(t, 180)
		}
	}
	return ""
}

func (sp *Super) memPreview(path string, fi os.FileInfo) string {
	if fi == nil {
		return ""
	}
	modNano := fi.ModTime().UnixNano()
	sp.memMetaMu.Lock()
	if e, ok := sp.memMeta[path]; ok && e.size == fi.Size() && e.modUnixNano == modNano {
		sp.memMetaMu.Unlock()
		return e.preview
	}
	sp.memMetaMu.Unlock()

	var preview string
	if f, err := os.Open(path); err == nil {
		b, _ := io.ReadAll(io.LimitReader(f, 4096))
		f.Close()
		preview = memPreviewFromText(string(b))
	}
	sp.memMetaMu.Lock()
	if sp.memMeta == nil {
		sp.memMeta = map[string]memMetaEntry{}
	}
	sp.memMeta[path] = memMetaEntry{size: fi.Size(), modUnixNano: modNano, preview: preview}
	sp.memMetaMu.Unlock()
	return preview
}

func (sp *Super) bumpMemoryVersion(clearCache bool) {
	sp.memGen.Add(1)
	if clearCache {
		sp.memMetaMu.Lock()
		sp.memMeta = nil
		sp.memMetaMu.Unlock()
	}
}

// isUpFrontMemPath reports whether rel (slash-separated under memory/) is
// shown eagerly on the memory page and should carry full text in summary mode.
func isUpFrontMemPath(rel string) bool {
	if rel == "instructions.md" || rel == "notes/index.md" || rel == "messages.md" {
		return true
	}
	if strings.HasPrefix(rel, "tasks/") && !strings.HasPrefix(rel, "tasks/done/") {
		rest := strings.TrimPrefix(rel, "tasks/")
		return !strings.Contains(rest, "/")
	}
	return false
}

func (sp *Super) listDreamPaths() []string {
	m, _ := filepath.Glob(filepath.Join(sp.routerDir(), "dreams", "*.md"))
	if len(m) == 0 {
		return nil
	}
	// Newest first: "2026-11-21-1840" after "2026-11-21", so compare without ".md".
	sort.Slice(m, func(i, j int) bool { return strings.TrimSuffix(m[i], ".md") > strings.TrimSuffix(m[j], ".md") })
	if len(m) > 30 {
		m = m[:30]
	}
	return m
}

func (sp *Super) countDoneMemFiles() (count int, dirMod int64, namesHash string) {
	doneDir := filepath.Join(sp.memDir(), "tasks", "done")
	if fi, err := os.Stat(doneDir); err == nil {
		dirMod = fi.ModTime().UnixNano()
	}
	entries, err := os.ReadDir(doneDir)
	if err != nil {
		return 0, dirMod, ""
	}
	h := sha256.New()
	for _, de := range entries {
		if de.IsDir() || !strings.HasSuffix(de.Name(), ".md") {
			continue
		}
		count++
		h.Write([]byte(de.Name()))
		h.Write([]byte{0})
	}
	return count, dirMod, hex.EncodeToString(h.Sum(nil)[:8])
}

func (sp *Super) memSummaryFiles(includeDone bool) (files []*memFile, doneCount int, doneSig string) {
	root := sp.memDir()
	files = []*memFile{}
	filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			if !includeDone {
				rel, relErr := filepath.Rel(root, p)
				if relErr == nil && filepath.ToSlash(rel) == "tasks/done" {
					var dirMod int64
					var namesHash string
					doneCount, dirMod, namesHash = sp.countDoneMemFiles()
					doneSig = fmt.Sprintf("%d:%d:%s", doneCount, dirMod, namesHash)
					return filepath.SkipDir
				}
			}
			return nil
		}
		if !strings.HasSuffix(p, ".md") {
			return nil
		}
		relRaw, err := filepath.Rel(root, p)
		if err != nil {
			return nil
		}
		rel := filepath.ToSlash(relRaw)
		if strings.HasPrefix(rel, "tasks/done/") {
			doneCount++
		}
		if isUpFrontMemPath(rel) {
			if f := readMemFile(p); f != nil {
				f.Name = rel
				files = append(files, f)
			}
			return nil
		}
		fi, err := d.Info()
		if err != nil {
			return nil
		}
		files = append(files, &memFile{
			Name:        rel,
			Preview:     sp.memPreview(p, fi),
			SummaryOnly: true,
			Size:        fi.Size(),
			Mod:         fi.ModTime(),
		})
		return nil
	})
	for _, name := range []string{"memory.md", "memory-archive.md"} {
		if f := readMemFile(filepath.Join(sp.routerDir(), name)); f != nil {
			f.Name, f.Legacy = name, true
			files = append(files, f)
		}
	}
	return files, doneCount, doneSig
}

func etagMatches(r *http.Request, etag string) bool {
	if r == nil || etag == "" {
		return false
	}
	inm := strings.TrimSpace(r.Header.Get("If-None-Match"))
	if inm == "" {
		return false
	}
	if inm == "*" {
		return true
	}
	for _, part := range strings.Split(inm, ",") {
		cand := strings.TrimSpace(part)
		cand = strings.TrimPrefix(cand, "W/")
		if cand == etag {
			return true
		}
	}
	return false
}

func (s *Server) superMemory(w http.ResponseWriter, r *http.Request) {
	sp := s.sup
	dir := sp.routerDir()
	q := r.URL.Query()
	summaryMode := q.Get("summary") == "1" || q.Get("summary") == "true"
	includeDone := true
	if raw := strings.TrimSpace(q.Get("include_done")); raw == "0" || raw == "false" {
		includeDone = false
	}

	upFront := sp.upFrontSize()
	msgs := sp.messagesSize()
	dreaming := sp.dreaming()
	lastDream := sp.lastDream()
	nextDream := sp.nextDream(time.Now())
	dreamPaths := sp.listDreamPaths()

	var files []*memFile
	var doneCount int
	var doneSig string
	if summaryMode {
		files, doneCount, doneSig = sp.memSummaryFiles(includeDone)
	} else {
		files = sp.memFiles()
		if files == nil {
			files = []*memFile{}
		}
		for _, f := range files {
			if strings.HasPrefix(f.Name, "tasks/done/") {
				doneCount++
			}
		}
	}

	// Build a fast deterministic version tag before reading deferred dream bodies.
	h := sha256.New()
	fmt.Fprintf(h, "v2|s=%v|d=%v|g=%d|u=%d|m=%d|dr=%v|ld=%d|nd=%d|dc=%d|ds=%s\n",
		summaryMode, includeDone, sp.memGen.Load(), upFront, msgs, dreaming,
		lastDream.UnixNano(), nextDream.UnixNano(), doneCount, doneSig)
	for _, f := range files {
		fmt.Fprintf(h, "f|%s|%d|%d|%v\n", f.Name, f.Size, f.Mod.UnixNano(), f.SummaryOnly)
	}
	dreamInfos := make([]os.FileInfo, len(dreamPaths))
	for i, p := range dreamPaths {
		if fi, err := os.Stat(p); err == nil && !fi.IsDir() {
			dreamInfos[i] = fi
			fmt.Fprintf(h, "r|%s|%d|%d\n", filepath.Base(p), fi.Size(), fi.ModTime().UnixNano())
		}
	}
	version := hex.EncodeToString(h.Sum(nil)[:12])
	etag := `"` + version + `"`
	w.Header().Set("ETag", etag)
	w.Header().Set("Cache-Control", "no-cache")
	if etagMatches(r, etag) {
		w.WriteHeader(http.StatusNotModified)
		return
	}

	dreams := make([]*memFile, 0, len(dreamPaths))
	for i, p := range dreamPaths {
		fi := dreamInfos[i]
		if fi == nil {
			continue
		}
		if summaryMode && i > 0 {
			dreams = append(dreams, &memFile{
				Name:        filepath.Base(p),
				Preview:     sp.memPreview(p, fi),
				SummaryOnly: true,
				Size:        fi.Size(),
				Mod:         fi.ModTime(),
			})
		} else if f := readMemFile(p); f != nil {
			dreams = append(dreams, f)
		}
	}

	out := map[string]any{
		"dir":        s.tilde(dir),
		"files":      files,
		"dreams":     dreams,
		"up_front":   upFront,
		"messages":   msgs,
		"loaded":     upFront + msgs,
		"budget":     upFrontBudget,
		"dreaming":   dreaming,
		"next_dream": nil,
		"last_dream": nil,
		"done_count": doneCount,
		"version":    version,
	}
	if summaryMode {
		out["summary_mode"] = true
	}
	if !lastDream.IsZero() {
		out["last_dream"] = lastDream
	}
	if !nextDream.IsZero() {
		out["next_dream"] = nextDream
	}
	writeJSON(w, 200, out)
}

// resolveMemoryReadPath validates and resolves a relative memory/dream path.
func (sp *Super) resolveMemoryReadPath(rawPath string) (absPath, normName string, legacy bool, ok bool) {
	cleaned := filepath.ToSlash(filepath.Clean(filepath.FromSlash(strings.TrimSpace(rawPath))))
	if cleaned == "" || cleaned == "." || strings.HasPrefix(cleaned, "/") || strings.HasPrefix(cleaned, "../") || cleaned == ".." || !strings.HasSuffix(cleaned, ".md") {
		return "", "", false, false
	}
	routerRoot := sp.routerDir()
	// Check dreams/<name>.md
	if strings.HasPrefix(cleaned, "dreams/") {
		base := strings.TrimPrefix(cleaned, "dreams/")
		if base == "" || strings.Contains(base, "/") {
			return "", "", false, false
		}
		dreamsRoot := filepath.Join(routerRoot, "dreams")
		cand := filepath.Join(dreamsRoot, base)
		if !pathWithinDir(dreamsRoot, cand) {
			return "", "", false, false
		}
		return cand, base, false, true
	}
	// Check legacy single-file memory if not present inside memory/
	memRoot := sp.memDir()
	cand := filepath.Join(memRoot, filepath.FromSlash(cleaned))
	if !pathWithinDir(memRoot, cand) {
		return "", "", false, false
	}
	if _, err := os.Stat(cand); err == nil {
		return cand, cleaned, false, true
	}
	if cleaned == "memory.md" || cleaned == "memory-archive.md" {
		leg := filepath.Join(routerRoot, cleaned)
		if pathWithinDir(routerRoot, leg) {
			if _, err := os.Stat(leg); err == nil {
				return leg, cleaned, true, true
			}
		}
	}
	return cand, cleaned, false, true
}

func pathWithinDir(root, target string) bool {
	absRoot, err1 := filepath.Abs(root)
	absTarget, err2 := filepath.Abs(target)
	if err1 != nil || err2 != nil {
		return false
	}
	rel, err := filepath.Rel(absRoot, absTarget)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(os.PathSeparator)) {
		return false
	}
	// Guard against symlink escapes if target exists.
	if realRoot, err := filepath.EvalSymlinks(absRoot); err == nil {
		if realTarget, err := filepath.EvalSymlinks(absTarget); err == nil {
			r, err := filepath.Rel(realRoot, realTarget)
			if err != nil || r == ".." || strings.HasPrefix(r, ".."+string(os.PathSeparator)) {
				return false
			}
		}
	}
	return true
}

func (s *Server) superMemoryFile(w http.ResponseWriter, r *http.Request) {
	rawPath := r.URL.Query().Get("path")
	absPath, normName, legacy, ok := s.sup.resolveMemoryReadPath(rawPath)
	if !ok {
		writeJSON(w, 400, map[string]any{"error": "invalid memory path"})
		return
	}
	f := readMemFile(absPath)
	if f == nil {
		writeJSON(w, 404, map[string]any{"error": "memory file not found"})
		return
	}
	f.Name = normName
	f.Legacy = legacy
	f.Preview = memPreviewFromText(f.Text)
	writeJSON(w, 200, map[string]any{"file": f})
}

func (s *Server) superMemoryDone(w http.ResponseWriter, r *http.Request) {
	sp := s.sup
	q := r.URL.Query()
	offset, _ := strconv.Atoi(strings.TrimSpace(q.Get("offset")))
	if offset < 0 {
		offset = 0
	}
	limit, _ := strconv.Atoi(strings.TrimSpace(q.Get("limit")))
	if limit <= 0 {
		limit = 60
	}
	if limit > 200 {
		limit = 200
	}

	doneDir := filepath.Join(sp.memDir(), "tasks", "done")
	entries, err := os.ReadDir(doneDir)
	if err != nil {
		writeJSON(w, 200, map[string]any{
			"files":    []*memFile{},
			"total":    0,
			"offset":   offset,
			"limit":    limit,
			"has_more": false,
		})
		return
	}
	type item struct {
		path string
		name string
		fi   os.FileInfo
	}
	all := make([]item, 0, len(entries))
	for _, de := range entries {
		if de.IsDir() || !strings.HasSuffix(de.Name(), ".md") {
			continue
		}
		fi, err := de.Info()
		if err != nil {
			continue
		}
		all = append(all, item{
			path: filepath.Join(doneDir, de.Name()),
			name: "tasks/done/" + de.Name(),
			fi:   fi,
		})
	}
	sort.Slice(all, func(i, j int) bool {
		ti, tj := all[i].fi.ModTime(), all[j].fi.ModTime()
		if !ti.Equal(tj) {
			return ti.After(tj)
		}
		return all[i].name > all[j].name
	})
	total := len(all)
	if offset > total {
		offset = total
	}
	end := offset + limit
	if end > total {
		end = total
	}
	page := make([]*memFile, 0, end-offset)
	for _, it := range all[offset:end] {
		page = append(page, &memFile{
			Name:        it.name,
			Preview:     sp.memPreview(it.path, it.fi),
			SummaryOnly: true,
			Size:        it.fi.Size(),
			Mod:         it.fi.ModTime(),
		})
	}
	writeJSON(w, 200, map[string]any{
		"files":    page,
		"total":    total,
		"offset":   offset,
		"limit":    limit,
		"has_more": end < total,
	})
}

func countSubstrHits(haystackLower string, terms []string) int {
	total := 0
	for _, term := range terms {
		if term == "" {
			continue
		}
		for pos := 0; pos < len(haystackLower); {
			idx := strings.Index(haystackLower[pos:], term)
			if idx < 0 {
				break
			}
			total++
			pos += idx + len(term)
		}
	}
	return total
}

func matchMemoryTerms(title, relPath, text string, terms []string) (matched bool, hits int) {
	if len(terms) == 0 {
		return false, 0
	}
	titleLow := strings.ToLower(title)
	pathLow := strings.ToLower(relPath)
	bodyLow := strings.ToLower(text)
	for _, term := range terms {
		if !strings.Contains(titleLow, term) && !strings.Contains(pathLow, term) && !strings.Contains(bodyLow, term) {
			return false, 0
		}
	}
	titleHits := countSubstrHits(titleLow, terms)
	if pathLow != titleLow {
		titleHits += countSubstrHits(pathLow, terms)
	}
	bodyHits := countSubstrHits(bodyLow, terms)
	h := titleHits + bodyHits
	if h < 1 {
		h = 1
	}
	return true, h
}

func formatDreamStampGo(name string) string {
	base := strings.TrimSuffix(name, ".md")
	if len(base) == 15 && base[4] == '-' && base[7] == '-' && base[10] == '-' {
		return base[:10] + " " + base[11:13] + ":" + base[13:15]
	}
	return base
}

func (s *Server) superMemorySearch(w http.ResponseWriter, r *http.Request) {
	sp := s.sup
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	terms := strings.Fields(strings.ToLower(q))
	limit, _ := strconv.Atoi(strings.TrimSpace(r.URL.Query().Get("limit")))
	if limit <= 0 {
		limit = 60
	}
	if limit > 200 {
		limit = 200
	}
	if len(terms) == 0 {
		writeJSON(w, 200, map[string]any{
			"query":       q,
			"files":       []*memFile{},
			"dreams":      []*memFile{},
			"total_files": 0,
			"total_hits":  0,
			"truncated":   false,
		})
		return
	}

	var activeMatches []*memFile
	var doneMatches []*memFile
	var dreamMatches []*memFile
	totalMatches := 0
	totalHits := 0

	root := sp.memDir()
	filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(p, ".md") {
			return nil
		}
		f := readMemFile(p)
		if f == nil {
			return nil
		}
		relRaw, err := filepath.Rel(root, p)
		if err != nil {
			return nil
		}
		rel := filepath.ToSlash(relRaw)
		f.Name = rel
		label := rel
		if strings.HasPrefix(rel, "notes/") {
			label = strings.TrimPrefix(rel, "notes/")
		} else if strings.HasPrefix(rel, "tasks/done/") {
			label = strings.TrimPrefix(rel, "tasks/done/")
		}
		ok, hits := matchMemoryTerms(label, rel, f.Text, terms)
		if !ok {
			return nil
		}
		totalMatches++
		totalHits += hits
		if strings.HasPrefix(rel, "tasks/done/") {
			doneMatches = append(doneMatches, f)
		} else {
			activeMatches = append(activeMatches, f)
		}
		return nil
	})
	for _, name := range []string{"memory.md", "memory-archive.md"} {
		if f := readMemFile(filepath.Join(sp.routerDir(), name)); f != nil {
			f.Name, f.Legacy = name, true
			if ok, hits := matchMemoryTerms(name, name, f.Text, terms); ok {
				totalMatches++
				totalHits += hits
				activeMatches = append(activeMatches, f)
			}
		}
	}
	for _, p := range sp.listDreamPaths() {
		if f := readMemFile(p); f != nil {
			title := formatDreamStampGo(f.Name)
			if ok, hits := matchMemoryTerms(title, "dreams/"+f.Name, f.Text, terms); ok {
				totalMatches++
				totalHits += hits
				dreamMatches = append(dreamMatches, f)
			}
		}
	}

	// Sort done matches newest-first before applying the response cap.
	sort.Slice(doneMatches, func(i, j int) bool {
		if !doneMatches[i].Mod.Equal(doneMatches[j].Mod) {
			return doneMatches[i].Mod.After(doneMatches[j].Mod)
		}
		return doneMatches[i].Name > doneMatches[j].Name
	})

	// Fill up to limit: prioritize active/notes/instructions, then dreams, then newest done tasks.
	outFiles := []*memFile{}
	outDreams := []*memFile{}
	rem := limit
	for _, f := range activeMatches {
		if rem <= 0 {
			break
		}
		outFiles = append(outFiles, f)
		rem--
	}
	for _, d := range dreamMatches {
		if rem <= 0 {
			break
		}
		outDreams = append(outDreams, d)
		rem--
	}
	for _, f := range doneMatches {
		if rem <= 0 {
			break
		}
		outFiles = append(outFiles, f)
		rem--
	}
	truncated := len(outFiles)+len(outDreams) < totalMatches

	writeJSON(w, 200, map[string]any{
		"query":       q,
		"files":       outFiles,
		"dreams":      outDreams,
		"total_files": totalMatches,
		"total_hits":  totalHits,
		"truncated":   truncated,
	})
}

func (s *Server) superSaveMemory(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Path string `json:"path"`
		Text string `json:"text"`
	}
	if !decode(w, r, &body) {
		return
	}
	rel := filepath.Clean(filepath.FromSlash(body.Path))
	if !strings.HasSuffix(rel, ".md") || filepath.IsAbs(rel) || strings.HasPrefix(rel, "..") || rel == "messages.md" {
		writeJSON(w, 400, map[string]any{"error": "只能改 memory/ 里的 .md 文件"})
		return
	}
	if len(body.Text) > 256<<10 {
		writeJSON(w, 400, map[string]any{"error": "太长了"})
		return
	}
	p := filepath.Join(s.sup.memDir(), rel)
	os.MkdirAll(filepath.Dir(p), 0o700)
	text := strings.TrimRight(body.Text, "\n") + "\n"
	if err := os.WriteFile(p, []byte(text), 0o600); err != nil {
		fail(w, err)
		return
	}
	s.sup.bumpMemoryVersion(false)
	writeJSON(w, 200, map[string]any{"ok": true})
}

func (s *Server) superDream(w http.ResponseWriter, r *http.Request) {
	if err := s.sup.StartDream(r.Context(), "manual"); err != nil {
		writeJSON(w, 409, map[string]any{"error": err.Error()})
		return
	}
	writeJSON(w, 200, map[string]any{"ok": true})
}
