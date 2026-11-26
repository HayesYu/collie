package collie

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"
)

func newMemoryTestServer(t *testing.T) (*Server, *Super, http.Handler) {
	t.Helper()
	dir := t.TempDir()
	srv := &Server{
		herdr: newHerdr(filepath.Join(dir, "herdr.sock"), "test-hub", dir),
		token: "test-master-token",
		home:  dir,
	}
	sup, err := newSuper(srv, dir)
	if err != nil {
		t.Fatalf("newSuper: %v", err)
	}
	t.Cleanup(func() { sup.db.Close() })
	srv.sup = sup
	srv.auths = newAuth(sup.db, srv.token)
	return srv, sup, srv.Routes()
}

func makeTestZip(t *testing.T, entries []struct {
	name string
	body []byte
	mode fs.FileMode
}) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for _, e := range entries {
		fh := &zip.FileHeader{
			Name:     e.name,
			Method:   zip.Deflate,
			Modified: time.Date(2026, 11, 26, 9, 0, 0, 0, time.UTC),
		}
		if e.mode != 0 {
			fh.SetMode(e.mode)
		} else if !strings.HasSuffix(e.name, "/") {
			fh.SetMode(0o600)
		}
		w, err := zw.CreateHeader(fh)
		if err != nil {
			t.Fatalf("zip create %q: %v", e.name, err)
		}
		if len(e.body) > 0 {
			if _, err := w.Write(e.body); err != nil {
				t.Fatalf("zip write %q: %v", e.name, err)
			}
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("zip close: %v", err)
	}
	return buf.Bytes()
}

func readZipMap(t *testing.T, data []byte) map[string]string {
	t.Helper()
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		t.Fatalf("open zip: %v", err)
	}
	out := make(map[string]string, len(zr.File))
	for _, f := range zr.File {
		rc, err := f.Open()
		if err != nil {
			t.Fatalf("open entry %q: %v", f.Name, err)
		}
		b, err := io.ReadAll(rc)
		rc.Close()
		if err != nil {
			t.Fatalf("read entry %q: %v", f.Name, err)
		}
		out[f.Name] = string(b)
	}
	return out
}

func listMemoryFilesMap(t *testing.T, memDir string) map[string]string {
	t.Helper()
	out := map[string]string{}
	if _, err := os.Stat(memDir); os.IsNotExist(err) {
		return out
	}
	err := filepath.WalkDir(memDir, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(d.Name(), ".md") {
			return nil
		}
		rel, _ := filepath.Rel(memDir, p)
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		out[filepath.ToSlash(rel)] = string(b)
		return nil
	})
	if err != nil {
		t.Fatalf("walk %s: %v", memDir, err)
	}
	return out
}

func postMultipartMemoryImport(t *testing.T, h http.Handler, token, mode, filename string, payload []byte) *httptest.ResponseRecorder {
	t.Helper()
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	if mode != "" {
		_ = mw.WriteField("mode", mode)
	}
	fw, err := mw.CreateFormFile("file", filename)
	if err != nil {
		t.Fatalf("CreateFormFile: %v", err)
	}
	if _, err := fw.Write(payload); err != nil {
		t.Fatalf("write part: %v", err)
	}
	if err := mw.Close(); err != nil {
		t.Fatalf("close multipart: %v", err)
	}
	url := "/api/memory/import"
	if mode != "" {
		url += "?mode=" + mode
	}
	req := httptest.NewRequest("POST", url, &body)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestMemoryExportFilterAndRoundTrip(t *testing.T) {
	srv1, sp1, h1 := newMemoryTestServer(t)
	mem1 := sp1.memDir()

	want := map[string]string{
		"instructions.md":       "# Instructions\nKeep replies crisp.\n",
		"messages.md":           "# Messages\n- hello\n",
		"notes/index.md":        "# Notes\n- topic-a.md: overview\n",
		"notes/topic-a.md":      "# Topic A\n中文内容测试\n",
		"tasks/open-1.md":       "# Open Task\nIn progress.\n",
		"tasks/done/archive.md": "# Done Task\nCompleted.\n",
	}
	for rel, content := range want {
		p := filepath.Join(mem1, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	// Add files/symlinks/dirs that export MUST skip.
	outside := t.TempDir()
	secretFile := filepath.Join(outside, "secret.md")
	_ = os.WriteFile(secretFile, []byte("top-secret"), 0o600)
	secretDir := filepath.Join(outside, "dir")
	_ = os.MkdirAll(secretDir, 0o700)
	_ = os.WriteFile(filepath.Join(secretDir, "leaked.md"), []byte("leaked"), 0o600)

	_ = os.Symlink(secretFile, filepath.Join(mem1, "notes", "symlink.md"))
	_ = os.Symlink(secretDir, filepath.Join(mem1, "notes", "symlink-dir"))
	_ = os.WriteFile(filepath.Join(mem1, "tasks", ".keep"), nil, 0o600)
	_ = os.WriteFile(filepath.Join(mem1, ".hidden.md"), []byte("hidden"), 0o600)
	_ = os.MkdirAll(filepath.Join(mem1, ".git"), 0o700)
	_ = os.WriteFile(filepath.Join(mem1, ".git", "info.md"), []byte("git"), 0o600)
	_ = os.WriteFile(filepath.Join(mem1, "notes", "draft.md.tmp"), []byte("tmp"), 0o600)
	_ = os.WriteFile(filepath.Join(mem1, "notes", "backup.md~"), []byte("bak"), 0o600)
	_ = os.WriteFile(filepath.Join(mem1, "notes", "~temp.md"), []byte("tilde"), 0o600)
	_ = os.WriteFile(filepath.Join(mem1, "notes", "script.sh"), []byte("echo hi"), 0o600)

	// Unauthenticated request must be 401.
	unauthRec := httptest.NewRecorder()
	h1.ServeHTTP(unauthRec, httptest.NewRequest("GET", "/api/memory/export", nil))
	if unauthRec.Code != 401 {
		t.Fatalf("unauth GET /api/memory/export = %d, want 401", unauthRec.Code)
	}

	req := httptest.NewRequest("GET", "/api/memory/export", nil)
	req.Header.Set("Authorization", "Bearer "+srv1.token)
	rec := httptest.NewRecorder()
	h1.ServeHTTP(rec, req)
	if rec.Code != 200 {
		t.Fatalf("GET /api/memory/export = %d: %s", rec.Code, rec.Body.String())
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/zip" {
		t.Fatalf("Content-Type = %q, want application/zip", ct)
	}
	cd := rec.Header().Get("Content-Disposition")
	if !strings.HasPrefix(cd, `attachment; filename="collie-memory-`) || !strings.HasSuffix(cd, `.zip"`) {
		t.Fatalf("unexpected Content-Disposition = %q", cd)
	}
	if gotCnt := rec.Header().Get("X-Memory-Files"); gotCnt != "6" {
		t.Fatalf("X-Memory-Files = %q, want 6", gotCnt)
	}

	zipMap := readZipMap(t, rec.Body.Bytes())
	if len(zipMap) != len(want) {
		t.Fatalf("zip entries = %v, want %v", zipMap, want)
	}
	for k, v := range want {
		if zipMap[k] != v {
			t.Fatalf("zip[%q] = %q, want %q", k, zipMap[k], v)
		}
	}

	// Round-trip import into a second clean server via multipart upload.
	srv2, sp2, h2 := newMemoryTestServer(t)
	impRec := postMultipartMemoryImport(t, h2, srv2.token, "merge", "export.zip", rec.Body.Bytes())
	if impRec.Code != 200 {
		t.Fatalf("POST /api/memory/import = %d: %s", impRec.Code, impRec.Body.String())
	}
	var res MemoryImportResult
	if err := json.Unmarshal(impRec.Body.Bytes(), &res); err != nil {
		t.Fatalf("decode import result: %v", err)
	}
	if !res.OK || res.Mode != "merge" || res.Imported != 6 || !strings.HasPrefix(res.Backup, "pre-import-") || !strings.HasSuffix(res.Backup, ".zip") {
		t.Fatalf("unexpected import result: %+v", res)
	}
	if _, err := os.Stat(filepath.Join(sp2.routerDir(), "memory-backups", res.Backup)); err != nil {
		t.Fatalf("backup file missing: %v", err)
	}

	gotFiles := listMemoryFilesMap(t, sp2.memDir())
	if len(gotFiles) != len(want) {
		t.Fatalf("imported files = %v, want %v", gotFiles, want)
	}
	for k, v := range want {
		if gotFiles[k] != v {
			t.Fatalf("imported[%q] = %q, want %q", k, gotFiles[k], v)
		}
	}
}

func TestMemoryImportMergeReplaceAndBackup(t *testing.T) {
	srv, sp, h := newMemoryTestServer(t)
	memDir := sp.memDir()

	seed := map[string]string{
		"instructions.md": "old-instructions\n",
		"notes/keep.md":   "keep-me\n",
		"notes/shared.md": "old-shared\n",
	}
	for rel, content := range seed {
		p := filepath.Join(memDir, filepath.FromSlash(rel))
		_ = os.MkdirAll(filepath.Dir(p), 0o700)
		_ = os.WriteFile(p, []byte(content), 0o600)
	}

	// 1) Merge import (default mode omitted -> merge).
	mergeZip := makeTestZip(t, []struct {
		name string
		body []byte
		mode fs.FileMode
	}{
		{name: "notes/", mode: fs.ModeDir | 0o700},
		{name: "notes/shared.md", body: []byte("new-shared\n")},
		{name: "notes/added.md", body: []byte("added\n")},
	})
	rec := postMultipartMemoryImport(t, h, srv.token, "", "merge.zip", mergeZip)
	if rec.Code != 200 {
		t.Fatalf("merge import = %d: %s", rec.Code, rec.Body.String())
	}
	var res1 MemoryImportResult
	_ = json.Unmarshal(rec.Body.Bytes(), &res1)
	if res1.Mode != "merge" || res1.Imported != 2 {
		t.Fatalf("merge result = %+v", res1)
	}

	// Verify backup #1 holds the exact pre-merge state.
	bak1Bytes, err := os.ReadFile(filepath.Join(sp.routerDir(), "memory-backups", res1.Backup))
	if err != nil {
		t.Fatalf("read backup 1: %v", err)
	}
	bak1 := readZipMap(t, bak1Bytes)
	if len(bak1) != 3 || bak1["notes/shared.md"] != "old-shared\n" || bak1["notes/keep.md"] != "keep-me\n" {
		t.Fatalf("backup 1 contents unexpected: %v", bak1)
	}

	// Verify merged state keeps untouched files and updates shared/added.
	afterMerge := listMemoryFilesMap(t, memDir)
	if len(afterMerge) != 4 || afterMerge["instructions.md"] != "old-instructions\n" ||
		afterMerge["notes/keep.md"] != "keep-me\n" || afterMerge["notes/shared.md"] != "new-shared\n" ||
		afterMerge["notes/added.md"] != "added\n" {
		t.Fatalf("after merge = %v", afterMerge)
	}

	// 2) Replace import via raw application/zip body.
	replaceZip := makeTestZip(t, []struct {
		name string
		body []byte
		mode fs.FileMode
	}{
		{name: "instructions.md", body: []byte("replaced-instructions\n")},
		{name: "notes/fresh.md", body: []byte("fresh\n")},
	})
	req := httptest.NewRequest("POST", "/api/memory/import?mode=replace", bytes.NewReader(replaceZip))
	req.Header.Set("Content-Type", "application/zip")
	req.Header.Set("Authorization", "Bearer "+srv.token)
	rec2 := httptest.NewRecorder()
	h.ServeHTTP(rec2, req)
	if rec2.Code != 200 {
		t.Fatalf("replace import = %d: %s", rec2.Code, rec2.Body.String())
	}
	var res2 MemoryImportResult
	_ = json.Unmarshal(rec2.Body.Bytes(), &res2)
	if res2.Mode != "replace" || res2.Imported != 2 {
		t.Fatalf("replace result = %+v", res2)
	}
	if res2.Backup == res1.Backup {
		t.Fatalf("expected distinct backup filenames on same-second imports, both %q", res2.Backup)
	}

	// Verify backup #2 holds the exact 4-file pre-replace state.
	bak2Bytes, err := os.ReadFile(filepath.Join(sp.routerDir(), "memory-backups", res2.Backup))
	if err != nil {
		t.Fatalf("read backup 2: %v", err)
	}
	bak2 := readZipMap(t, bak2Bytes)
	if len(bak2) != 4 || bak2["notes/added.md"] != "added\n" || bak2["notes/shared.md"] != "new-shared\n" {
		t.Fatalf("backup 2 contents unexpected: %v", bak2)
	}

	// Verify replaced state only has the 2 new files.
	afterReplace := listMemoryFilesMap(t, memDir)
	if len(afterReplace) != 2 || afterReplace["instructions.md"] != "replaced-instructions\n" || afterReplace["notes/fresh.md"] != "fresh\n" {
		t.Fatalf("after replace = %v", afterReplace)
	}
}

func TestMemoryImportSecurityGuards(t *testing.T) {
	srv, sp, h := newMemoryTestServer(t)
	memDir := sp.memDir()
	_ = os.MkdirAll(memDir, 0o700)
	_ = os.WriteFile(filepath.Join(memDir, "instructions.md"), []byte("untouched\n"), 0o600)

	cases := []struct {
		name       string
		mode       string
		payload    []byte
		wantStatus int
	}{
		{
			name: "zip-slip-parent",
			payload: makeTestZip(t, []struct {
				name string
				body []byte
				mode fs.FileMode
			}{{name: "../escape.md", body: []byte("boom")}}),
			wantStatus: 400,
		},
		{
			name: "zip-slip-nested",
			payload: makeTestZip(t, []struct {
				name string
				body []byte
				mode fs.FileMode
			}{{name: "notes/../../escape.md", body: []byte("boom")}}),
			wantStatus: 400,
		},
		{
			name: "zip-slip-backslash",
			payload: makeTestZip(t, []struct {
				name string
				body []byte
				mode fs.FileMode
			}{{name: `notes\..\..\escape.md`, body: []byte("boom")}}),
			wantStatus: 400,
		},
		{
			name: "abs-unix-path",
			payload: makeTestZip(t, []struct {
				name string
				body []byte
				mode fs.FileMode
			}{{name: "/etc/passwd.md", body: []byte("boom")}}),
			wantStatus: 400,
		},
		{
			name: "abs-windows-path",
			payload: makeTestZip(t, []struct {
				name string
				body []byte
				mode fs.FileMode
			}{{name: "C:/evil.md", body: []byte("boom")}}),
			wantStatus: 400,
		},
		{
			name: "symlink-entry",
			payload: makeTestZip(t, []struct {
				name string
				body []byte
				mode fs.FileMode
			}{{name: "notes/link.md", body: []byte("/etc/passwd"), mode: fs.ModeSymlink | 0o777}}),
			wantStatus: 400,
		},
		{
			name: "non-markdown-file",
			payload: makeTestZip(t, []struct {
				name string
				body []byte
				mode fs.FileMode
			}{
				{name: "instructions.md", body: []byte("ok")},
				{name: "notes/evil.sh", body: []byte("rm -rf /")},
			}),
			wantStatus: 400,
		},
		{
			name: "hidden-file",
			payload: makeTestZip(t, []struct {
				name string
				body []byte
				mode fs.FileMode
			}{{name: "notes/.secret.md", body: []byte("hidden")}}),
			wantStatus: 400,
		},
		{
			name: "empty-zip-no-md",
			payload: makeTestZip(t, []struct {
				name string
				body []byte
				mode fs.FileMode
			}{{name: "notes/", mode: fs.ModeDir | 0o700}}),
			wantStatus: 400,
		},
		{
			name:       "corrupt-zip",
			payload:    []byte("not a valid zip payload"),
			wantStatus: 400,
		},
		{
			name: "invalid-mode",
			mode: "nuke",
			payload: makeTestZip(t, []struct {
				name string
				body []byte
				mode fs.FileMode
			}{{name: "instructions.md", body: []byte("ok")}}),
			wantStatus: 400,
		},
		{
			name: "single-file-zip-bomb",
			payload: makeTestZip(t, []struct {
				name string
				body []byte
				mode fs.FileMode
			}{{name: "notes/huge.md", body: bytes.Repeat([]byte("a"), memoryMaxSingleFileBytes+1024)}}),
			wantStatus: 413,
		},
		{
			name: "total-size-zip-bomb",
			payload: func() []byte {
				chunk := bytes.Repeat([]byte("b"), memoryMaxSingleFileBytes-1024)
				var entries []struct {
					name string
					body []byte
					mode fs.FileMode
				}
				for i := 0; i < 12; i++ { // ~60 MiB uncompressed, <100 KB compressed
					entries = append(entries, struct {
						name string
						body []byte
						mode fs.FileMode
					}{name: fmt.Sprintf("notes/part-%02d.md", i), body: chunk})
				}
				return makeTestZip(t, entries)
			}(),
			wantStatus: 413,
		},
		{
			name: "too-many-files",
			payload: func() []byte {
				entries := make([]struct {
					name string
					body []byte
					mode fs.FileMode
				}, memoryMaxFiles+1)
				for i := range entries {
					entries[i] = struct {
						name string
						body []byte
						mode fs.FileMode
					}{name: fmt.Sprintf("notes/f-%04d.md", i), body: []byte("x")}
				}
				return makeTestZip(t, entries)
			}(),
			wantStatus: 400,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := postMultipartMemoryImport(t, h, srv.token, tc.mode, tc.name+".zip", tc.payload)
			if rec.Code != tc.wantStatus {
				t.Fatalf("%s: status = %d, want %d (body: %s)", tc.name, rec.Code, tc.wantStatus, rec.Body.String())
			}
		})
	}

	// Oversized raw upload (> 20 MiB) must return 413.
	big := make([]byte, memoryMaxUploadBytes+1024)
	req := httptest.NewRequest("POST", "/api/memory/import", bytes.NewReader(big))
	req.Header.Set("Content-Type", "application/zip")
	req.Header.Set("Authorization", "Bearer "+srv.token)
	bigRec := httptest.NewRecorder()
	h.ServeHTTP(bigRec, req)
	if bigRec.Code != 413 {
		t.Fatalf("oversized upload = %d, want 413: %s", bigRec.Code, bigRec.Body.String())
	}

	// Ensure original memory stayed completely untouched and zero backups were created on rejected imports.
	got := listMemoryFilesMap(t, memDir)
	if len(got) != 1 || got["instructions.md"] != "untouched\n" {
		t.Fatalf("memory modified after rejected imports: %v", got)
	}
	if entries, err := os.ReadDir(filepath.Join(sp.routerDir(), "memory-backups")); err == nil && len(entries) > 0 {
		t.Fatalf("expected no backup files from rejected imports, got %d", len(entries))
	}

	// Pre-existing symlink target in memory/ must be rejected in merge mode.
	outside := t.TempDir()
	_ = os.Symlink(outside, filepath.Join(memDir, "notes"))
	goodZip := makeTestZip(t, []struct {
		name string
		body []byte
		mode fs.FileMode
	}{{name: "notes/pwn.md", body: []byte("nope")}})
	symRec := postMultipartMemoryImport(t, h, srv.token, "merge", "good.zip", goodZip)
	if symRec.Code != 400 {
		t.Fatalf("pre-existing symlink target = %d, want 400: %s", symRec.Code, symRec.Body.String())
	}
	if _, err := os.Stat(filepath.Join(outside, "pwn.md")); !os.IsNotExist(err) {
		t.Fatalf("symlink traversal wrote outside memory/")
	}
	_ = os.Remove(filepath.Join(memDir, "notes"))

	// Dreaming conflict must return 409.
	sp.mu.Lock()
	sp.dream = &dreamRun{at: time.Now(), reason: "test"}
	sp.mu.Unlock()
	dreamRec := postMultipartMemoryImport(t, h, srv.token, "merge", "good.zip", goodZip)
	sp.mu.Lock()
	sp.dream = nil
	sp.mu.Unlock()
	if dreamRec.Code != 409 {
		t.Fatalf("dreaming import = %d, want 409: %s", dreamRec.Code, dreamRec.Body.String())
	}

	// Paired machine token is permitted on /api/memory/export and /api/memory/import.
	code, _ := srv.auths.newCode("machine")
	_, machineTok, err := srv.auths.redeem(code, "machine", "laptop", "darwin")
	if err != nil {
		t.Fatal(err)
	}
	mReq := httptest.NewRequest("GET", "/api/memory/export", nil)
	mReq.Header.Set("Authorization", "Bearer "+machineTok)
	mRec := httptest.NewRecorder()
	h.ServeHTTP(mRec, mReq)
	if mRec.Code != 200 {
		t.Fatalf("machine GET /api/memory/export = %d, want 200: %s", mRec.Code, mRec.Body.String())
	}
}

func TestCLIMemoryExportImport(t *testing.T) {
	homeA := t.TempDir()
	t.Setenv("COLLIE_HOME", homeA)
	memA := filepath.Join(homeA, "router", "memory")
	_ = os.MkdirAll(filepath.Join(memA, "notes"), 0o700)
	_ = os.WriteFile(filepath.Join(memA, "instructions.md"), []byte("# CLI Ins\n"), 0o600)
	_ = os.WriteFile(filepath.Join(memA, "notes", "dev.md"), []byte("# Dev Note\n"), 0o600)
	_ = os.WriteFile(filepath.Join(memA, "notes", ".hidden.md"), []byte("skip"), 0o600)

	outZip := filepath.Join(t.TempDir(), "snap.zip")
	if err := cliMemory([]string{"export", outZip}); err != nil {
		t.Fatalf("cliMemory export: %v", err)
	}
	raw, err := os.ReadFile(outZip)
	if err != nil {
		t.Fatalf("read exported zip: %v", err)
	}
	gotZip := readZipMap(t, raw)
	keys := make([]string, 0, len(gotZip))
	for k := range gotZip {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	if strings.Join(keys, ",") != "instructions.md,notes/dev.md" {
		t.Fatalf("cli export keys = %v", keys)
	}

	// Import into homeB (merge, then replace).
	homeB := t.TempDir()
	t.Setenv("COLLIE_HOME", homeB)
	memB := filepath.Join(homeB, "router", "memory")
	_ = os.MkdirAll(filepath.Join(memB, "notes"), 0o700)
	_ = os.WriteFile(filepath.Join(memB, "notes", "existing.md"), []byte("keep\n"), 0o600)

	if err := cliMemory([]string{"import", outZip}); err != nil {
		t.Fatalf("cliMemory import merge: %v", err)
	}
	merged := listMemoryFilesMap(t, memB)
	if len(merged) != 3 || merged["notes/existing.md"] != "keep\n" || merged["instructions.md"] != "# CLI Ins\n" {
		t.Fatalf("cli merge result = %v", merged)
	}

	// Replace with flag after positional arg too.
	if err := cliMemory([]string{"import", outZip, "--replace"}); err != nil {
		t.Fatalf("cliMemory import replace: %v", err)
	}
	replaced := listMemoryFilesMap(t, memB)
	if len(replaced) != 2 || replaced["instructions.md"] != "# CLI Ins\n" || replaced["notes/dev.md"] != "# Dev Note\n" {
		t.Fatalf("cli replace result = %v", replaced)
	}

	backups, err := os.ReadDir(filepath.Join(homeB, "router", "memory-backups"))
	if err != nil || len(backups) != 2 {
		t.Fatalf("expected 2 backups in homeB, got %d (err=%v)", len(backups), err)
	}

	// Invalid invocations fail cleanly.
	if err := cliMemory(nil); err == nil {
		t.Fatalf("expected error on empty args")
	}
	if err := cliMemory([]string{"import"}); err == nil {
		t.Fatalf("expected error on missing import file")
	}
}
