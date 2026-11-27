package collie

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"mime"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Memory export / import limits and safety guards.
const (
	memoryMaxUploadBytes       = 20 << 20 // 20 MiB max .zip upload size
	memoryMaxUncompressedBytes = 50 << 20 // 50 MiB max total extracted bytes
	memoryMaxSingleFileBytes   = 5 << 20  // 5 MiB max per .md file
	memoryMaxFiles             = 2000     // max entries allowed in a .zip archive
)

// MemoryImportResult summarizes a completed memory import.
type MemoryImportResult struct {
	OK       bool   `json:"ok"`
	Mode     string `json:"mode"`
	Imported int    `json:"imported"`
	Backup   string `json:"backup"`
}

type memoryIOError struct {
	status int
	msg    string
}

func (e *memoryIOError) Error() string { return e.msg }

func memoryExportFilename(now time.Time) string {
	return "collie-memory-" + now.Format("20060102-150405") + ".zip"
}

func isHiddenOrTempName(name string) bool {
	if name == "" || strings.HasPrefix(name, ".") || strings.HasPrefix(name, "~") || strings.HasPrefix(name, "#") {
		return true
	}
	low := strings.ToLower(name)
	for _, suf := range []string{"~", ".tmp", ".temp", ".swp", ".swo", ".bak", ".part", ".orig"} {
		if strings.HasSuffix(low, suf) {
			return true
		}
	}
	return false
}

func isASCIIAlpha(b byte) bool {
	return (b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z')
}

// exportMemoryZip packs valid .md files under memDir into w as a standard ZIP.
// Symlinks, hidden paths, non-regular files and temporary files are skipped.
func exportMemoryZip(memDir string, w io.Writer) (int, error) {
	zw := zip.NewWriter(w)
	type item struct {
		abs string
		rel string
		mod time.Time
	}
	var items []item

	fi, err := os.Lstat(memDir)
	if err != nil {
		if os.IsNotExist(err) {
			if err := zw.Close(); err != nil {
				return 0, err
			}
			return 0, nil
		}
		zw.Close()
		return 0, err
	}
	if fi.Mode()&os.ModeSymlink != 0 {
		zw.Close()
		return 0, &memoryIOError{status: 400, msg: "memory 目录不能是软链接"}
	}
	if !fi.IsDir() {
		zw.Close()
		return 0, &memoryIOError{status: 400, msg: "memory 路径不是目录"}
	}

	err = filepath.WalkDir(memDir, func(p string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			if os.IsNotExist(walkErr) {
				return nil
			}
			return walkErr
		}
		if p == memDir {
			return nil
		}
		name := d.Name()
		// Never follow or pack symlinks (file or dir).
		if d.Type()&fs.ModeSymlink != 0 {
			return nil
		}
		if d.IsDir() {
			if isHiddenOrTempName(name) {
				return filepath.SkipDir
			}
			return nil
		}
		if !d.Type().IsRegular() {
			return nil
		}
		if isHiddenOrTempName(name) || !strings.HasSuffix(name, ".md") {
			return nil
		}
		lfi, err := os.Lstat(p)
		if err != nil {
			if os.IsNotExist(err) {
				return nil
			}
			return err
		}
		if !lfi.Mode().IsRegular() {
			return nil
		}
		rel, err := filepath.Rel(memDir, p)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		for _, seg := range strings.Split(rel, "/") {
			if seg == "" || seg == "." || seg == ".." || isHiddenOrTempName(seg) {
				return nil
			}
		}
		items = append(items, item{abs: p, rel: rel, mod: lfi.ModTime()})
		return nil
	})
	if err != nil {
		zw.Close()
		return 0, err
	}

	sort.Slice(items, func(i, j int) bool { return items[i].rel < items[j].rel })

	for _, it := range items {
		f, err := os.Open(it.abs)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			zw.Close()
			return 0, err
		}
		st, statErr := f.Stat()
		if statErr != nil || !st.Mode().IsRegular() {
			f.Close()
			continue
		}
		fh := &zip.FileHeader{
			Name:     it.rel,
			Method:   zip.Deflate,
			Modified: it.mod,
		}
		fh.SetMode(0o600)
		ew, err := zw.CreateHeader(fh)
		if err != nil {
			f.Close()
			zw.Close()
			return 0, err
		}
		if _, err := io.Copy(ew, f); err != nil {
			f.Close()
			zw.Close()
			return 0, err
		}
		f.Close()
	}
	if err := zw.Close(); err != nil {
		return 0, err
	}
	return len(items), nil
}

// exportMemoryToFile packs routerDir/memory/ into dstPath atomically.
func exportMemoryToFile(routerDir, dstPath string) (int, error) {
	memDir := filepath.Join(routerDir, "memory")
	var buf bytes.Buffer
	count, err := exportMemoryZip(memDir, &buf)
	if err != nil {
		return 0, err
	}
	parent := filepath.Dir(dstPath)
	if parent != "" && parent != "." {
		if err := os.MkdirAll(parent, 0o755); err != nil {
			return 0, err
		}
	}
	tmp, err := os.CreateTemp(parent, ".collie-mem-export-*.tmp")
	if err != nil {
		return 0, err
	}
	tmpName := tmp.Name()
	cleanup := true
	defer func() {
		if cleanup {
			os.Remove(tmpName)
		}
	}()
	if _, err := tmp.Write(buf.Bytes()); err != nil {
		tmp.Close()
		return 0, err
	}
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return 0, err
	}
	if err := tmp.Close(); err != nil {
		return 0, err
	}
	if err := os.Rename(tmpName, dstPath); err != nil {
		return 0, err
	}
	cleanup = false
	return count, nil
}

type memoryZipEntry struct {
	rel  string
	data []byte
	mod  time.Time
}

// parseMemoryZip validates and extracts all .md files from zipBytes in memory
// before any disk writes occur.
func parseMemoryZip(zipBytes []byte, memDir string) ([]memoryZipEntry, error) {
	if len(zipBytes) == 0 {
		return nil, &memoryIOError{status: 400, msg: "上传的压缩包为空"}
	}
	if int64(len(zipBytes)) > memoryMaxUploadBytes {
		return nil, &memoryIOError{status: 413, msg: "压缩包太大了（最多 20 MiB）"}
	}
	zr, err := zip.NewReader(bytes.NewReader(zipBytes), int64(len(zipBytes)))
	if err != nil {
		return nil, &memoryIOError{status: 400, msg: "无效的 .zip 压缩包"}
	}
	if len(zr.File) > memoryMaxFiles {
		return nil, &memoryIOError{status: 400, msg: fmt.Sprintf("压缩包条目过多（最多 %d 个）", memoryMaxFiles)}
	}

	seen := make(map[string]bool, len(zr.File))
	var entries []memoryZipEntry
	var totalBytes int64

	for _, f := range zr.File {
		mode := f.Mode()
		unixType := (f.ExternalAttrs >> 16) & 0o170000
		if mode&fs.ModeSymlink != 0 || unixType == 0o120000 {
			return nil, &memoryIOError{status: 400, msg: "压缩包包含禁止的软链接：" + f.Name}
		}
		if mode&(fs.ModeDevice|fs.ModeCharDevice|fs.ModeNamedPipe|fs.ModeSocket) != 0 {
			return nil, &memoryIOError{status: 400, msg: "压缩包包含非法文件类型：" + f.Name}
		}
		if strings.ContainsRune(f.Name, 0) {
			return nil, &memoryIOError{status: 400, msg: "压缩包包含非法路径"}
		}

		raw := strings.ReplaceAll(f.Name, "\\", "/")
		if raw == "" {
			return nil, &memoryIOError{status: 400, msg: "压缩包包含空路径"}
		}
		if strings.HasPrefix(raw, "/") || (len(raw) >= 2 && isASCIIAlpha(raw[0]) && raw[1] == ':') {
			return nil, &memoryIOError{status: 400, msg: "禁止导入绝对路径：" + f.Name}
		}

		isDir := f.FileInfo().IsDir() || strings.HasSuffix(raw, "/")
		trimmed := strings.TrimSuffix(raw, "/")
		if trimmed == "" {
			return nil, &memoryIOError{status: 400, msg: "压缩包包含非法路径：" + f.Name}
		}
		for _, seg := range strings.Split(trimmed, "/") {
			if seg == ".." {
				return nil, &memoryIOError{status: 400, msg: "检测到路径穿越（..），已拦截：" + f.Name}
			}
			if seg == "" || seg == "." {
				return nil, &memoryIOError{status: 400, msg: "压缩包包含非法路径：" + f.Name}
			}
			if isHiddenOrTempName(seg) {
				return nil, &memoryIOError{status: 400, msg: "压缩包包含隐藏或临时文件，已拦截：" + f.Name}
			}
		}

		clean := path.Clean(trimmed)
		if clean != trimmed || strings.HasPrefix(clean, "/") || clean == "." || clean == ".." || strings.HasPrefix(clean, "../") {
			return nil, &memoryIOError{status: 400, msg: "压缩包包含非法路径：" + f.Name}
		}

		if isDir {
			if f.UncompressedSize64 > 0 {
				return nil, &memoryIOError{status: 400, msg: "压缩包包含非法目录条目：" + f.Name}
			}
			continue
		}

		if !strings.HasSuffix(clean, ".md") {
			return nil, &memoryIOError{status: 400, msg: "仅允许导入 .md 文件，已拦截：" + f.Name}
		}
		if seen[clean] {
			return nil, &memoryIOError{status: 400, msg: "压缩包包含重复文件：" + clean}
		}
		seen[clean] = true

		// Belt-and-suspenders check that resolved filesystem path stays strictly under memDir.
		dst := filepath.Join(memDir, filepath.FromSlash(clean))
		relCheck, err := filepath.Rel(memDir, dst)
		if err != nil || relCheck == "." || relCheck == ".." || strings.HasPrefix(relCheck, ".."+string(filepath.Separator)) || filepath.IsAbs(relCheck) {
			return nil, &memoryIOError{status: 400, msg: "检测到路径越界，已拦截：" + f.Name}
		}

		if f.UncompressedSize64 > uint64(memoryMaxSingleFileBytes) {
			return nil, &memoryIOError{status: 413, msg: "单个文件超过 5 MiB 限制：" + clean}
		}

		rc, err := f.Open()
		if err != nil {
			return nil, &memoryIOError{status: 400, msg: "无法读取压缩包条目：" + clean}
		}
		data, readErr := io.ReadAll(io.LimitReader(rc, memoryMaxSingleFileBytes+1))
		rc.Close()
		if readErr != nil {
			return nil, &memoryIOError{status: 400, msg: "解压失败：" + clean}
		}
		if int64(len(data)) > memoryMaxSingleFileBytes {
			return nil, &memoryIOError{status: 413, msg: "单个文件超过 5 MiB 限制：" + clean}
		}
		totalBytes += int64(len(data))
		if totalBytes > memoryMaxUncompressedBytes {
			return nil, &memoryIOError{status: 413, msg: "解压后总大小超过 50 MiB 限制"}
		}

		entries = append(entries, memoryZipEntry{rel: clean, data: data, mod: f.Modified})
	}

	if len(entries) == 0 {
		return nil, &memoryIOError{status: 400, msg: "压缩包里没有可导入的 .md 记忆文件"}
	}
	return entries, nil
}

// backupMemoryZip snapshots routerDir/memory/ to routerDir/memory-backups/pre-import-YYYYMMDD-HHMMSS.zip.
func backupMemoryZip(routerDir string, now time.Time) (string, error) {
	backupDir := filepath.Join(routerDir, "memory-backups")
	if err := os.MkdirAll(backupDir, 0o700); err != nil {
		return "", err
	}
	if fi, err := os.Lstat(backupDir); err != nil {
		return "", err
	} else if fi.Mode()&os.ModeSymlink != 0 || !fi.IsDir() {
		return "", &memoryIOError{status: 400, msg: "memory-backups 路径异常"}
	}

	stamp := now.Format("20060102-150405")
	var (
		f    *os.File
		name string
		err  error
	)
	for i := 0; i < 100; i++ {
		if i == 0 {
			name = "pre-import-" + stamp + ".zip"
		} else {
			name = fmt.Sprintf("pre-import-%s-%d.zip", stamp, i)
		}
		f, err = os.OpenFile(filepath.Join(backupDir, name), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if err == nil {
			break
		}
		if !os.IsExist(err) {
			return "", err
		}
	}
	if f == nil {
		return "", fmt.Errorf("create memory backup: too many backups in the same second")
	}

	dst := filepath.Join(backupDir, name)
	if _, err := exportMemoryZip(filepath.Join(routerDir, "memory"), f); err != nil {
		f.Close()
		os.Remove(dst)
		return "", err
	}
	if err := f.Close(); err != nil {
		os.Remove(dst)
		return "", err
	}
	return name, nil
}

func ensureNoSymlinkPath(baseDir, relSlash string) error {
	cur := baseDir
	if fi, err := os.Lstat(cur); err == nil {
		if fi.Mode()&os.ModeSymlink != 0 || !fi.IsDir() {
			return &memoryIOError{status: 400, msg: "memory 目录异常，已拒绝写入"}
		}
	} else if !os.IsNotExist(err) {
		return err
	}
	for _, seg := range strings.Split(relSlash, "/") {
		cur = filepath.Join(cur, seg)
		fi, err := os.Lstat(cur)
		if err == nil {
			if fi.Mode()&os.ModeSymlink != 0 {
				return &memoryIOError{status: 400, msg: "目标路径包含软链接，已拒绝写入：" + relSlash}
			}
		} else if !os.IsNotExist(err) {
			return err
		}
	}
	return nil
}

func writeMemoryFileAtomic(dst string, data []byte, mod time.Time) error {
	dir := filepath.Dir(dst)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".mem-import-*.tmp")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	cleanup := true
	defer func() {
		if cleanup {
			os.Remove(tmpName)
		}
	}()
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if !mod.IsZero() {
		_ = os.Chtimes(tmpName, mod, mod)
	}
	if err := os.Rename(tmpName, dst); err != nil {
		return err
	}
	cleanup = false
	return nil
}

// importMemoryZip validates zipBytes, snapshots routerDir/memory/ into
// routerDir/memory-backups/, then writes files in merge or replace mode.
func importMemoryZip(routerDir string, zipBytes []byte, mode string, now time.Time) (*MemoryImportResult, error) {
	mode = strings.TrimSpace(strings.ToLower(mode))
	if mode == "" {
		mode = "merge"
	}
	if mode != "merge" && mode != "replace" {
		return nil, &memoryIOError{status: 400, msg: "不支持的导入模式，仅支持 merge 或 replace"}
	}

	memDir := filepath.Join(routerDir, "memory")
	entries, err := parseMemoryZip(zipBytes, memDir)
	if err != nil {
		return nil, err
	}

	if err := os.MkdirAll(routerDir, 0o700); err != nil {
		return nil, err
	}
	if fi, err := os.Lstat(memDir); err == nil {
		if fi.Mode()&os.ModeSymlink != 0 || !fi.IsDir() {
			return nil, &memoryIOError{status: 400, msg: "memory 目录异常，已拒绝导入"}
		}
	} else if !os.IsNotExist(err) {
		return nil, err
	}

	if mode == "merge" {
		// Pre-check target paths for pre-existing symlinks before creating backup or writing.
		for _, ent := range entries {
			if err := ensureNoSymlinkPath(memDir, ent.rel); err != nil {
				return nil, err
			}
		}
	}

	backupName, err := backupMemoryZip(routerDir, now)
	if err != nil {
		return nil, fmt.Errorf("自动备份当前记忆失败：%w", err)
	}

	if mode == "merge" {
		if err := os.MkdirAll(memDir, 0o700); err != nil {
			return nil, err
		}
		for _, ent := range entries {
			dst := filepath.Join(memDir, filepath.FromSlash(ent.rel))
			if err := writeMemoryFileAtomic(dst, ent.data, ent.mod); err != nil {
				return nil, err
			}
		}
	} else {
		// Atomic staging swap for replace mode.
		staging, err := os.MkdirTemp(routerDir, ".memory-import-*")
		if err != nil {
			return nil, err
		}
		cleanupStaging := true
		defer func() {
			if cleanupStaging {
				os.RemoveAll(staging)
			}
		}()
		if err := os.Chmod(staging, 0o700); err != nil {
			return nil, err
		}
		_ = os.MkdirAll(filepath.Join(staging, "notes"), 0o700)
		_ = os.MkdirAll(filepath.Join(staging, "tasks", "done"), 0o700)
		_ = os.WriteFile(filepath.Join(staging, "tasks", ".keep"), nil, 0o600)
		_ = os.WriteFile(filepath.Join(staging, "tasks", "done", ".keep"), nil, 0o600)
		for _, ent := range entries {
			dst := filepath.Join(staging, filepath.FromSlash(ent.rel))
			if err := os.MkdirAll(filepath.Dir(dst), 0o700); err != nil {
				return nil, err
			}
			if err := os.WriteFile(dst, ent.data, 0o600); err != nil {
				return nil, err
			}
			if !ent.mod.IsZero() {
				_ = os.Chtimes(dst, ent.mod, ent.mod)
			}
		}

		if _, statErr := os.Lstat(memDir); statErr == nil {
			oldDir := filepath.Join(routerDir, ".memory-old-"+randomToken()[:8])
			if err := os.Rename(memDir, oldDir); err != nil {
				return nil, err
			}
			if err := os.Rename(staging, memDir); err != nil {
				_ = os.Rename(oldDir, memDir)
				return nil, err
			}
			cleanupStaging = false
			_ = os.RemoveAll(oldDir)
		} else if os.IsNotExist(statErr) {
			if err := os.Rename(staging, memDir); err != nil {
				return nil, err
			}
			cleanupStaging = false
		} else {
			return nil, statErr
		}
	}

	return &MemoryImportResult{
		OK:       true,
		Mode:     mode,
		Imported: len(entries),
		Backup:   backupName,
	}, nil
}

// importMemoryFromFile reads a local .zip file (enforcing size limit) and imports it.
func importMemoryFromFile(routerDir, srcPath, mode string, now time.Time) (*MemoryImportResult, error) {
	fi, err := os.Stat(srcPath)
	if err != nil {
		return nil, err
	}
	if fi.IsDir() {
		return nil, &memoryIOError{status: 400, msg: "请指定 .zip 文件而不是目录"}
	}
	if fi.Size() > memoryMaxUploadBytes {
		return nil, &memoryIOError{status: 413, msg: "压缩包太大了（最多 20 MiB）"}
	}
	f, err := os.Open(srcPath)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, memoryMaxUploadBytes+1))
	if err != nil {
		return nil, err
	}
	return importMemoryZip(routerDir, data, mode, now)
}

// ---------------------------------------------------------------- HTTP handlers

func writeMemoryIOErr(w http.ResponseWriter, err error) {
	var me *memoryIOError
	if errors.As(err, &me) {
		writeJSON(w, me.status, map[string]any{"error": me.msg})
		return
	}
	var tooBig *http.MaxBytesError
	if errors.As(err, &tooBig) {
		writeJSON(w, 413, map[string]any{"error": "压缩包太大了（最多 20 MiB）"})
		return
	}
	fail(w, err)
}

func (s *Server) memoryExport(w http.ResponseWriter, r *http.Request) {
	sp := s.sup
	sp.memIOMu.Lock()
	var buf bytes.Buffer
	count, err := exportMemoryZip(sp.memDir(), &buf)
	sp.memIOMu.Unlock()
	if err != nil {
		writeMemoryIOErr(w, err)
		return
	}
	filename := memoryExportFilename(time.Now())
	w.Header().Set("Content-Type", "application/zip")
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s"`, filename))
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Memory-Files", strconv.Itoa(count))
	w.WriteHeader(200)
	_, _ = w.Write(buf.Bytes())
}

func readMemoryImportPayload(w http.ResponseWriter, r *http.Request) (zipBytes []byte, mode string, err error) {
	mode = strings.TrimSpace(r.URL.Query().Get("mode"))
	mediaType, _, _ := mime.ParseMediaType(r.Header.Get("Content-Type"))

	if mediaType == "multipart/form-data" {
		// Allow small multipart framing overhead on top of the 20 MiB file cap.
		r.Body = http.MaxBytesReader(w, r.Body, memoryMaxUploadBytes+(1<<20))
		mr, err := r.MultipartReader()
		if err != nil {
			return nil, "", &memoryIOError{status: 400, msg: "无法解析上传表单"}
		}
		var gotFile bool
		for {
			part, err := mr.NextPart()
			if err == io.EOF {
				break
			}
			if err != nil {
				var tooBig *http.MaxBytesError
				if errors.As(err, &tooBig) {
					return nil, "", &memoryIOError{status: 413, msg: "压缩包太大了（最多 20 MiB）"}
				}
				return nil, "", &memoryIOError{status: 400, msg: "读取上传表单失败"}
			}
			switch part.FormName() {
			case "mode":
				if mode == "" {
					b, _ := io.ReadAll(io.LimitReader(part, 64))
					mode = strings.TrimSpace(string(b))
				}
			case "file":
				if gotFile {
					part.Close()
					continue
				}
				b, readErr := io.ReadAll(io.LimitReader(part, memoryMaxUploadBytes+1))
				part.Close()
				if readErr != nil {
					var tooBig *http.MaxBytesError
					if errors.As(readErr, &tooBig) {
						return nil, "", &memoryIOError{status: 413, msg: "压缩包太大了（最多 20 MiB）"}
					}
					return nil, "", &memoryIOError{status: 400, msg: "读取上传文件失败"}
				}
				if int64(len(b)) > memoryMaxUploadBytes {
					return nil, "", &memoryIOError{status: 413, msg: "压缩包太大了（最多 20 MiB）"}
				}
				zipBytes = b
				gotFile = true
				continue
			}
			part.Close()
		}
		if !gotFile {
			return nil, "", &memoryIOError{status: 400, msg: "请选择要导入的 .zip 文件"}
		}
		return zipBytes, mode, nil
	}

	// Raw .zip upload body (used by CLI on remote nodes and API callers).
	r.Body = http.MaxBytesReader(w, r.Body, memoryMaxUploadBytes)
	b, err := io.ReadAll(r.Body)
	if err != nil {
		return nil, "", err
	}
	return b, mode, nil
}

func (s *Server) memoryImport(w http.ResponseWriter, r *http.Request) {
	sp := s.sup
	if sp.dreaming() {
		writeJSON(w, 409, map[string]any{"error": "Collie 正在整理记忆，请等做梦结束后再导入"})
		return
	}
	zipBytes, mode, err := readMemoryImportPayload(w, r)
	if err != nil {
		writeMemoryIOErr(w, err)
		return
	}
	sp.memIOMu.Lock()
	res, err := importMemoryZip(sp.routerDir(), zipBytes, mode, time.Now())
	sp.memIOMu.Unlock()
	if err != nil {
		writeMemoryIOErr(w, err)
		return
	}
	sp.bumpMemoryVersion(true)
	sp.changed()
	writeJSON(w, 200, res)
}

// ---------------------------------------------------------------- CLI subcommand

func cliMemory(args []string) error {
	const usage = "usage: collie memory export [file.zip]\n       collie memory import [--replace] <file.zip>"
	if len(args) == 0 {
		return fmt.Errorf("%s", usage)
	}
	switch args[0] {
	case "export":
		return cliMemoryExport(args[1:])
	case "import":
		return cliMemoryImport(args[1:])
	case "-h", "--help", "help":
		fmt.Println(usage)
		return nil
	default:
		return fmt.Errorf("unknown memory command %q\n%s", args[0], usage)
	}
}

func cliMemoryExport(args []string) error {
	if len(args) > 1 {
		return fmt.Errorf("usage: collie memory export [file.zip]")
	}
	if len(args) == 1 && (args[0] == "-h" || args[0] == "--help") {
		fmt.Println("usage: collie memory export [file.zip]")
		return nil
	}
	now := time.Now()
	outPath := memoryExportFilename(now)
	if len(args) == 1 && strings.TrimSpace(args[0]) != "" {
		outPath = args[0]
		if fi, err := os.Stat(outPath); err == nil && fi.IsDir() {
			outPath = filepath.Join(outPath, memoryExportFilename(now))
		}
	}

	// On a computer joined via `collie node`, memory lives on the hub: download via API.
	if _, isNode := loadNodeConfig(); isNode {
		data, hdr, err := cliRawCall("GET", "/api/memory/export", nil, "")
		if err != nil {
			return err
		}
		if err := os.MkdirAll(filepath.Dir(outPath), 0o755); err != nil && filepath.Dir(outPath) != "." {
			return err
		}
		if err := writeMemoryFileAtomic(outPath, data, time.Time{}); err != nil {
			return err
		}
		count := hdr.Get("X-Memory-Files")
		if count != "" {
			fmt.Printf("exported %s memory files -> %s\n", count, outPath)
		} else {
			fmt.Printf("exported memory -> %s\n", outPath)
		}
		return nil
	}

	routerDir := filepath.Join(collieDir(), "router")
	count, err := exportMemoryToFile(routerDir, outPath)
	if err != nil {
		return err
	}
	fmt.Printf("exported %d memory files -> %s\n", count, outPath)
	return nil
}

func cliMemoryImport(args []string) error {
	replace := false
	var pos []string
	for _, a := range args {
		switch a {
		case "--replace", "-replace":
			replace = true
		case "-h", "--help":
			fmt.Println("usage: collie memory import [--replace] <file.zip>")
			return nil
		default:
			if strings.HasPrefix(a, "-") {
				return fmt.Errorf("unknown flag %q\nusage: collie memory import [--replace] <file.zip>", a)
			}
			pos = append(pos, a)
		}
	}
	if len(pos) != 1 {
		return fmt.Errorf("usage: collie memory import [--replace] <file.zip>")
	}
	inPath := pos[0]
	mode := "merge"
	if replace {
		mode = "replace"
	}

	// On a paired remote node, upload to the hub's import API.
	if _, isNode := loadNodeConfig(); isNode {
		fi, err := os.Stat(inPath)
		if err != nil {
			return err
		}
		if fi.IsDir() {
			return fmt.Errorf("请指定 .zip 文件而不是目录")
		}
		if fi.Size() > memoryMaxUploadBytes {
			return fmt.Errorf("压缩包太大了（最多 20 MiB）")
		}
		f, err := os.Open(inPath)
		if err != nil {
			return err
		}
		defer f.Close()
		raw, _, err := cliRawCall("POST", "/api/memory/import?mode="+mode, io.LimitReader(f, memoryMaxUploadBytes), "application/zip")
		if err != nil {
			return err
		}
		var res MemoryImportResult
		if json.Unmarshal(raw, &res) == nil && res.OK {
			fmt.Printf("imported %d memory files (mode=%s, backup=%s)\n", res.Imported, res.Mode, res.Backup)
			return nil
		}
		os.Stdout.Write(raw)
		return nil
	}

	routerDir := filepath.Join(collieDir(), "router")
	res, err := importMemoryFromFile(routerDir, inPath, mode, time.Now())
	if err != nil {
		return err
	}
	fmt.Printf("imported %d memory files (mode=%s, backup=%s)\n", res.Imported, res.Mode, res.Backup)
	return nil
}

func cliRawCall(method, reqPath string, body io.Reader, contentType string) ([]byte, http.Header, error) {
	base, tok, err := cliTarget()
	if err != nil {
		return nil, nil, err
	}
	req, err := http.NewRequest(method, base+reqPath, body)
	if err != nil {
		return nil, nil, err
	}
	req.Header.Set("Authorization", "Bearer "+tok)
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	resp, err := (&http.Client{Timeout: 60 * time.Second}).Do(req)
	if err != nil {
		return nil, nil, fmt.Errorf("collie server unreachable: %w", err)
	}
	defer resp.Body.Close()
	out, err := io.ReadAll(io.LimitReader(resp.Body, memoryMaxUploadBytes+1))
	if err != nil {
		return nil, nil, err
	}
	if resp.StatusCode >= 300 {
		var e struct{ Error string }
		if json.Unmarshal(out, &e) == nil && e.Error != "" {
			return nil, resp.Header, fmt.Errorf("%s", e.Error)
		}
		return nil, resp.Header, fmt.Errorf("%s", strings.TrimSpace(string(out)))
	}
	return out, resp.Header, nil
}
