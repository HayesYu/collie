package collie

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"
)

func TestReadFileAsHTMLSandboxLimitsAndNodeForwarding(t *testing.T) {
	t.Parallel()
	hubHome := t.TempDir()
	nodeHome := t.TempDir()

	htmlContent := "<!doctype html><html><body><h1>Interactive Chart</h1><script>console.log(1)</script></body></html>"
	if err := os.WriteFile(filepath.Join(hubHome, "report.html"), []byte(htmlContent), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(hubHome, "legacy.HTM"), []byte("<p>htm ok</p>"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(hubHome, "notes.txt"), []byte("<script>alert(1)</script>"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(hubHome, "vector.svg"), []byte("<svg onload='alert(1)'/>"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(hubHome, "binary.html"), []byte{'<', 'h', '1', '>', 0, 'x'}, 0o600); err != nil {
		t.Fatal(err)
	}

	// 2 MiB HTML (> fileTextMax 1 MiB, <= fileHTMLMax 5 MiB) must NOT be truncated.
	twoMiB := bytes.Repeat([]byte("a"), 2<<20)
	if err := os.WriteFile(filepath.Join(hubHome, "large2m.html"), twoMiB, 0o600); err != nil {
		t.Fatal(err)
	}
	// 6 MiB HTML (> fileHTMLMax 5 MiB) is capped at fileHTMLMax (5 MiB).
	sixMiB := bytes.Repeat([]byte("b"), 6<<20)
	if err := os.WriteFile(filepath.Join(hubHome, "huge6m.html"), sixMiB, 0o600); err != nil {
		t.Fatal(err)
	}

	// Remote node file (only exists on nodeHome, NOT on hubHome).
	nodeHTML := "<!doctype html><title>Mac Visualization</title><div id='mac'>ok</div>"
	if err := os.WriteFile(filepath.Join(nodeHome, "mac-only.html"), []byte(nodeHTML), 0o600); err != nil {
		t.Fatal(err)
	}

	nodeSrv := &Server{home: nodeHome, node: true, web: fstest.MapFS{}}
	nodeHTTP := httptest.NewServer(nodeSrv.NodeRoutes())
	defer nodeHTTP.Close()
	nodeURL, _ := url.Parse(nodeHTTP.URL)

	herdr := newHerdr(filepath.Join(hubHome, "missing.sock"), "hub", hubHome)
	herdr.attach(&Machine{
		ID:     "mac",
		Name:   "mac",
		Home:   nodeHome,
		Online: true,
		client: &http.Client{
			Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
				cp := req.Clone(req.Context())
				cp.URL.Scheme = nodeURL.Scheme
				cp.URL.Host = nodeURL.Host
				cp.RequestURI = ""
				return http.DefaultTransport.RoundTrip(cp)
			}),
		},
	})

	hubSrv := &Server{
		herdr: herdr,
		token: "secret-tok",
		home:  hubHome,
		web:   fstest.MapFS{"index.html": {Data: []byte("ok")}, "app.js": {Data: []byte("")}, "app.css": {Data: []byte("")}},
	}
	handler := hubSrv.Routes()

	get := func(target string, extraHeaders map[string]string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, target, nil)
		if _, hasAuth := extraHeaders["Authorization"]; !hasAuth {
			req.AddCookie(&http.Cookie{Name: cookieName, Value: "secret-tok"})
		}
		for k, v := range extraHeaders {
			if v != "" {
				req.Header.Set(k, v)
			}
		}
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		return rec
	}

	// 1. Default mode (no as=html) stays an opaque attachment download.
	rec := get("/api/file?path=~/report.html", nil)
	if rec.Code != 200 {
		t.Fatalf("default mode status = %d", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/octet-stream" {
		t.Fatalf("default Content-Type = %q, want application/octet-stream", ct)
	}
	if cd := rec.Header().Get("Content-Disposition"); cd != "attachment" {
		t.Fatalf("default Content-Disposition = %q, want attachment", cd)
	}
	if csp := rec.Header().Get("Content-Security-Policy"); csp != "sandbox" {
		t.Fatalf("default CSP = %q, want sandbox", csp)
	}

	// 2. ?as=html renders inline with strict CSP sandbox and no Content-Disposition.
	for _, p := range []string{"~/report.html", "~/legacy.HTM"} {
		rec = get("/api/file?path="+url.QueryEscape(p)+"&as=html", nil)
		if rec.Code != 200 {
			t.Fatalf("as=html %s status = %d: %s", p, rec.Code, rec.Body.String())
		}
		if ct := rec.Header().Get("Content-Type"); ct != "text/html; charset=utf-8" {
			t.Fatalf("%s Content-Type = %q, want text/html; charset=utf-8", p, ct)
		}
		if cd := rec.Header().Get("Content-Disposition"); cd != "" {
			t.Fatalf("%s unexpected Content-Disposition = %q", p, cd)
		}
		if cc := rec.Header().Get("Cache-Control"); cc != "no-store" {
			t.Fatalf("%s Cache-Control = %q, want no-store", p, cc)
		}
		if csp := rec.Header().Get("Content-Security-Policy"); csp != htmlSandboxCSP {
			t.Fatalf("%s CSP = %q, want %q", p, csp, htmlSandboxCSP)
		}
		if strings.Contains(rec.Header().Get("Content-Security-Policy"), "allow-same-origin") {
			t.Fatal("CSP sandbox must NEVER include allow-same-origin")
		}
	}

	// 3. Non-HTML suffixes and binary disguised as .html are rejected on ?as=html.
	for _, bad := range []string{"~/notes.txt", "~/vector.svg", "~/binary.html"} {
		rec = get("/api/file?path="+url.QueryEscape(bad)+"&as=html", nil)
		if rec.Code != 400 {
			t.Fatalf("bad as=html %s status = %d, want 400", bad, rec.Code)
		}
		if strings.HasPrefix(rec.Header().Get("Content-Type"), "text/html") {
			t.Fatalf("bad as=html %s returned text/html Content-Type", bad)
		}
	}

	// 4. Size limits: 2 MiB reads in full (not cut at 1 MiB); 6 MiB caps at 5 MiB.
	rec = get("/api/file?path=~/large2m.html&as=html", nil)
	if rec.Code != 200 || rec.Body.Len() != 2<<20 {
		t.Fatalf("2 MiB as=html status=%d len=%d, want 200 len=%d", rec.Code, rec.Body.Len(), 2<<20)
	}
	rec = get("/api/file?path=~/huge6m.html&as=html", nil)
	if rec.Code != 200 || rec.Body.Len() != fileHTMLMax {
		t.Fatalf("6 MiB as=html status=%d len=%d, want 200 len=%d", rec.Code, rec.Body.Len(), fileHTMLMax)
	}
	if rec.Header().Get("X-File-Size") != "6291456" {
		t.Fatalf("X-File-Size = %q, want 6291456", rec.Header().Get("X-File-Size"))
	}

	// 5. Cross-machine: explicit machine=mac AND implicit hub-404 fallback to online node.
	for _, q := range []string{
		"/api/file?path=~/mac-only.html&machine=mac&as=html",
		"/api/file?path=~/mac-only.html&as=html",
	} {
		rec = get(q, nil)
		if rec.Code != 200 {
			t.Fatalf("remote %s status = %d: %s", q, rec.Code, rec.Body.String())
		}
		if rec.Body.String() != nodeHTML {
			t.Fatalf("remote %s body = %q, want %q", q, rec.Body.String(), nodeHTML)
		}
		if ct := rec.Header().Get("Content-Type"); ct != "text/html; charset=utf-8" {
			t.Fatalf("remote %s Content-Type = %q", q, ct)
		}
		if csp := rec.Header().Get("Content-Security-Policy"); csp != htmlSandboxCSP {
			t.Fatalf("remote %s CSP = %q, want %q", q, csp, htmlSandboxCSP)
		}
		if cd := rec.Header().Get("Content-Disposition"); cd != "" {
			t.Fatalf("remote %s unexpected Content-Disposition = %q", q, cd)
		}
	}

	// 6. Sandboxed opaque origin ("Origin: null") gets neither auth nor CORS grant on /api/*.
	unauthReq := httptest.NewRequest(http.MethodGet, "/api/version", nil)
	unauthReq.Header.Set("Origin", "null")
	unauthRec := httptest.NewRecorder()
	handler.ServeHTTP(unauthRec, unauthReq)
	if unauthRec.Code != 401 {
		t.Fatalf("Origin: null unauthenticated /api/version = %d, want 401", unauthRec.Code)
	}
	if acao := unauthRec.Header().Get("Access-Control-Allow-Origin"); acao != "" {
		t.Fatalf("Origin: null must not receive Access-Control-Allow-Origin, got %q", acao)
	}
}

func TestReadFileDownloadFullUntruncatedAndCrossMachine(t *testing.T) {
	t.Parallel()
	hubHome := t.TempDir()
	nodeHome := t.TempDir()

	// 2.4 MiB Markdown report (> fileTextMax 1 MiB): preview truncates, download=1 returns full bytes.
	bigMD := append([]byte("# Full Report\n\n"), bytes.Repeat([]byte("row-data-0123456789abcdef\n"), 100_000)...)
	if len(bigMD) <= fileTextMax {
		t.Fatalf("expected test fixture > %d bytes, got %d", fileTextMax, len(bigMD))
	}
	if err := os.WriteFile(filepath.Join(hubHome, "SUMMARY.md"), bigMD, 0o600); err != nil {
		t.Fatal(err)
	}

	// UTF-8 filename & binary file containing NUL bytes.
	utf8Name := "季度复盘报告.md"
	utf8Body := []byte("# 季度复盘\n完整原文内容\n")
	if err := os.WriteFile(filepath.Join(hubHome, utf8Name), utf8Body, 0o600); err != nil {
		t.Fatal(err)
	}
	binBody := []byte{0x89, 'P', 'K', 0x00, 0x01, 0x02, 0xff, 'E', 'N', 'D'}
	if err := os.WriteFile(filepath.Join(hubHome, "archive.bin"), binBody, 0o600); err != nil {
		t.Fatal(err)
	}

	// Remote node >1 MiB file & HTML file downloaded as raw attachment.
	nodeBig := append([]byte("package main\n"), bytes.Repeat([]byte("// remote line padding 0123456789\n"), 40_000)...)
	if err := os.WriteFile(filepath.Join(nodeHome, "remote_gen.go"), nodeBig, 0o600); err != nil {
		t.Fatal(err)
	}
	nodeHTML := []byte("<!doctype html><script>alert(1)</script>")
	if err := os.WriteFile(filepath.Join(nodeHome, "chart.html"), nodeHTML, 0o600); err != nil {
		t.Fatal(err)
	}

	nodeSrv := &Server{home: nodeHome, node: true, web: fstest.MapFS{}}
	nodeHTTP := httptest.NewServer(nodeSrv.NodeRoutes())
	defer nodeHTTP.Close()
	nodeURL, _ := url.Parse(nodeHTTP.URL)

	herdr := newHerdr(filepath.Join(hubHome, "missing.sock"), "hub", hubHome)
	herdr.attach(&Machine{
		ID:     "mac",
		Name:   "mac",
		Home:   nodeHome,
		Online: true,
		client: &http.Client{
			Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
				cp := req.Clone(req.Context())
				cp.URL.Scheme = nodeURL.Scheme
				cp.URL.Host = nodeURL.Host
				cp.RequestURI = ""
				return http.DefaultTransport.RoundTrip(cp)
			}),
		},
	})

	hubSrv := &Server{
		herdr: herdr,
		token: "secret-tok",
		home:  hubHome,
		web:   fstest.MapFS{"index.html": {Data: []byte("ok")}, "app.js": {Data: []byte("")}, "app.css": {Data: []byte("")}},
	}
	handler := hubSrv.Routes()

	get := func(target string, extraHeaders map[string]string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, target, nil)
		if _, hasAuth := extraHeaders["Authorization"]; !hasAuth {
			req.AddCookie(&http.Cookie{Name: cookieName, Value: "secret-tok"})
		}
		for k, v := range extraHeaders {
			if v != "" {
				req.Header.Set(k, v)
			}
		}
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		return rec
	}

	// 1. Preview truncates >1 MiB text, while download=1 streams the complete file.
	prev := get("/api/file?path=~/SUMMARY.md", nil)
	if prev.Code != 200 || prev.Body.Len() != fileTextMax {
		t.Fatalf("preview SUMMARY.md status=%d len=%d, want 200 len=%d", prev.Code, prev.Body.Len(), fileTextMax)
	}
	dl := get("/api/file?path=~/SUMMARY.md&download=1", nil)
	if dl.Code != 200 {
		t.Fatalf("download SUMMARY.md status=%d: %s", dl.Code, dl.Body.String())
	}
	if !bytes.Equal(dl.Body.Bytes(), bigMD) {
		t.Fatalf("download SUMMARY.md len=%d, want exact %d bytes", dl.Body.Len(), len(bigMD))
	}
	if cd := dl.Header().Get("Content-Disposition"); !strings.Contains(cd, `filename="SUMMARY.md"`) {
		t.Fatalf("Content-Disposition = %q, want SUMMARY.md", cd)
	}
	if csp := dl.Header().Get("Content-Security-Policy"); csp != "sandbox" {
		t.Fatalf("CSP = %q, want sandbox", csp)
	}

	// 2. UTF-8 filename & binary file download in full.
	dlUTF8 := get("/api/file?path="+url.QueryEscape("~/"+utf8Name)+"&download=1", nil)
	if dlUTF8.Code != 200 || !bytes.Equal(dlUTF8.Body.Bytes(), utf8Body) {
		t.Fatalf("UTF-8 download failed: code=%d len=%d", dlUTF8.Code, dlUTF8.Body.Len())
	}
	if cd := dlUTF8.Header().Get("Content-Disposition"); !strings.Contains(cd, "filename*=UTF-8''"+url.PathEscape(utf8Name)) {
		t.Fatalf("UTF-8 Content-Disposition = %q", cd)
	}

	prevBin := get("/api/file?path=~/archive.bin", nil)
	if prevBin.Header().Get("X-File-Kind") != "binary" || prevBin.Body.Len() != 0 {
		t.Fatalf("preview archive.bin kind=%q len=%d, want binary len=0", prevBin.Header().Get("X-File-Kind"), prevBin.Body.Len())
	}
	dlBin := get("/api/file?path=~/archive.bin&download=1", nil)
	if dlBin.Code != 200 || !bytes.Equal(dlBin.Body.Bytes(), binBody) {
		t.Fatalf("download archive.bin code=%d body=%v, want full binary", dlBin.Code, dlBin.Body.Bytes())
	}

	// 3. Cross-machine >1 MiB download (explicit machine=mac and implicit fallback) + HTML attachment safety.
	for _, q := range []string{
		"/api/file?path=~/remote_gen.go&machine=mac&download=1",
		"/api/file?path=~/remote_gen.go&download=1",
	} {
		rec := get(q, nil)
		if rec.Code != 200 || !bytes.Equal(rec.Body.Bytes(), nodeBig) {
			t.Fatalf("remote download %s code=%d len=%d, want %d", q, rec.Code, rec.Body.Len(), len(nodeBig))
		}
		if cd := rec.Header().Get("Content-Disposition"); !strings.Contains(cd, `filename="remote_gen.go"`) {
			t.Fatalf("remote %s Content-Disposition = %q", q, cd)
		}
	}
	dlHTML := get("/api/file?path=~/chart.html&machine=mac&download=1&as=html", nil)
	if dlHTML.Code != 200 || dlHTML.Header().Get("Content-Type") != "application/octet-stream" || !strings.HasPrefix(dlHTML.Header().Get("Content-Disposition"), "attachment;") {
		t.Fatalf("download=1 on .html must stay attachment, got ct=%q cd=%q", dlHTML.Header().Get("Content-Type"), dlHTML.Header().Get("Content-Disposition"))
	}

	// 4. Multi-Hub cross-origin Bearer request exposes Content-Disposition and streams full bytes.
	corsDL := get("/api/file?path=~/SUMMARY.md&download=1", map[string]string{
		"Authorization": "Bearer secret-tok",
		"Origin":        "http://other-hub.local:7788",
	})
	if corsDL.Code != 200 || corsDL.Body.Len() != len(bigMD) {
		t.Fatalf("multi-hub Bearer download code=%d len=%d", corsDL.Code, corsDL.Body.Len())
	}
	if exp := corsDL.Header().Get("Access-Control-Expose-Headers"); !strings.Contains(exp, "Content-Disposition") {
		t.Fatalf("Access-Control-Expose-Headers = %q, want Content-Disposition", exp)
	}
}

func TestAttachmentDispositionSanitizesQuotesAndCRLF(t *testing.T) {
	t.Parallel()
	got := attachmentDisposition("/tmp/bad\"name\r\n;x.txt")
	if strings.ContainsAny(got, "\r\n") || strings.Count(got, `"`) != 2 {
		t.Fatalf("unsafe Content-Disposition = %q", got)
	}
}

func TestReadFileAsVideoRangeAndNodeForwarding(t *testing.T) {
	t.Parallel()
	hubHome := t.TempDir()
	nodeHome := t.TempDir()

	mp4Data := []byte("0000ftypisom0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZ")
	movData := []byte("0000ftypqt  REMOTE-NODE-QUICKTIME-VIDEO-PAYLOAD")
	webmData := []byte{0x1a, 0x45, 0xdf, 0xa3, 'w', 'e', 'b', 'm', '1', '2', '3', '4'}
	m4vData := []byte("0000ftypM4V 9876543210abcdef")

	for name, body := range map[string][]byte{
		"clip.mp4":   mp4Data,
		"demo.webm":  webmData,
		"screen.M4V": m4vData,
		"evil.html":  []byte("<script>alert(1)</script>"),
		"notes.txt":  []byte("plain text"),
		"vector.svg": []byte("<svg/>"),
	} {
		if err := os.WriteFile(filepath.Join(hubHome, name), body, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(nodeHome, "mac-rec.mov"), movData, 0o600); err != nil {
		t.Fatal(err)
	}

	nodeSrv := &Server{home: nodeHome, node: true, web: fstest.MapFS{}}
	nodeHTTP := httptest.NewServer(nodeSrv.NodeRoutes())
	defer nodeHTTP.Close()
	nodeURL, _ := url.Parse(nodeHTTP.URL)

	herdr := newHerdr(filepath.Join(hubHome, "missing.sock"), "hub", hubHome)
	herdr.attach(&Machine{
		ID:     "mac",
		Name:   "mac",
		Home:   nodeHome,
		Online: true,
		client: &http.Client{
			Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
				cp := req.Clone(req.Context())
				cp.URL.Scheme = nodeURL.Scheme
				cp.URL.Host = nodeURL.Host
				cp.RequestURI = ""
				return http.DefaultTransport.RoundTrip(cp)
			}),
		},
	})

	hubSrv := &Server{
		herdr: herdr,
		token: "secret-tok",
		home:  hubHome,
		web:   fstest.MapFS{"index.html": {Data: []byte("ok")}, "app.js": {Data: []byte("")}, "app.css": {Data: []byte("")}},
	}
	handler := hubSrv.Routes()

	doReq := func(method, target string, extraHeaders map[string]string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, target, nil)
		if _, hasAuth := extraHeaders["Authorization"]; !hasAuth && method != http.MethodOptions {
			req.AddCookie(&http.Cookie{Name: cookieName, Value: "secret-tok"})
		}
		for k, v := range extraHeaders {
			if v != "" {
				req.Header.Set(k, v)
			}
		}
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		return rec
	}

	// 1. Supported video extensions return strict video MIME, Accept-Ranges, sandbox CSP, and no Content-Disposition.
	for _, tc := range []struct {
		file string
		mime string
		want []byte
	}{
		{"~/clip.mp4", "video/mp4", mp4Data},
		{"~/screen.M4V", "video/mp4", m4vData},
		{"~/demo.webm", "video/webm", webmData},
	} {
		rec := doReq(http.MethodGet, "/api/file?path="+url.QueryEscape(tc.file)+"&as=video", nil)
		if rec.Code != http.StatusOK {
			t.Fatalf("%s status=%d body=%s", tc.file, rec.Code, rec.Body.String())
		}
		if got := rec.Header().Get("Content-Type"); got != tc.mime {
			t.Fatalf("%s Content-Type=%q, want %q", tc.file, got, tc.mime)
		}
		if got := rec.Header().Get("Accept-Ranges"); got != "bytes" {
			t.Fatalf("%s Accept-Ranges=%q, want bytes", tc.file, got)
		}
		if got := rec.Header().Get("Content-Disposition"); got != "" {
			t.Fatalf("%s unexpected Content-Disposition=%q", tc.file, got)
		}
		if got := rec.Header().Get("Content-Security-Policy"); got != "sandbox" {
			t.Fatalf("%s CSP=%q, want sandbox", tc.file, got)
		}
		if got := rec.Header().Get("X-File-Kind"); got != "video" {
			t.Fatalf("%s X-File-Kind=%q, want video", tc.file, got)
		}
		if !bytes.Equal(rec.Body.Bytes(), tc.want) {
			t.Fatalf("%s body mismatch: got %d bytes, want %d", tc.file, rec.Body.Len(), len(tc.want))
		}
	}

	// 2. HTTP Byte Range (Safari / iOS <video> probe bytes=0-1 and slice bytes=4-15) -> 206 Partial Content.
	probe := doReq(http.MethodGet, "/api/file?path=~/clip.mp4&as=video", map[string]string{"Range": "bytes=0-1"})
	if probe.Code != http.StatusPartialContent {
		t.Fatalf("Range 0-1 status=%d, want 206", probe.Code)
	}
	if !bytes.Equal(probe.Body.Bytes(), mp4Data[:2]) {
		t.Fatalf("Range 0-1 body=%q, want %q", probe.Body.Bytes(), mp4Data[:2])
	}
	if cr := probe.Header().Get("Content-Range"); cr != "bytes 0-1/48" {
		t.Fatalf("Range 0-1 Content-Range=%q, want bytes 0-1/48", cr)
	}

	badRange := doReq(http.MethodGet, "/api/file?path=~/clip.mp4&as=video", map[string]string{"Range": "bytes=9999-"})
	if badRange.Code != http.StatusRequestedRangeNotSatisfiable {
		t.Fatalf("out-of-bounds Range status=%d, want 416", badRange.Code)
	}

	// 3. HEAD request returns video headers and zero body bytes (used by drawer probe).
	head := doReq(http.MethodHead, "/api/file?path=~/clip.mp4&as=video", nil)
	if head.Code != http.StatusOK || head.Body.Len() != 0 {
		t.Fatalf("HEAD as=video status=%d bodyLen=%d, want 200 len=0", head.Code, head.Body.Len())
	}
	if head.Header().Get("X-File-Kind") != "video" || head.Header().Get("X-File-Size") != "48" {
		t.Fatalf("HEAD headers kind=%q size=%q", head.Header().Get("X-File-Kind"), head.Header().Get("X-File-Size"))
	}

	// 4. Non-video files are rejected on ?as=video, and default mode stays opaque attachment.
	for _, bad := range []string{"~/evil.html", "~/notes.txt", "~/vector.svg"} {
		rec := doReq(http.MethodGet, "/api/file?path="+url.QueryEscape(bad)+"&as=video", nil)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("bad as=video %s status=%d, want 400", bad, rec.Code)
		}
		if strings.HasPrefix(rec.Header().Get("Content-Type"), "video/") || strings.HasPrefix(rec.Header().Get("Content-Type"), "text/html") {
			t.Fatalf("bad as=video %s leaked Content-Type=%q", bad, rec.Header().Get("Content-Type"))
		}
	}
	defRec := doReq(http.MethodGet, "/api/file?path=~/clip.mp4", nil)
	if defRec.Code != 200 || defRec.Header().Get("Content-Type") != "application/octet-stream" || defRec.Header().Get("Content-Disposition") != "attachment" {
		t.Fatalf("default .mp4 mode must remain opaque attachment, got code=%d ct=%q cd=%q", defRec.Code, defRec.Header().Get("Content-Type"), defRec.Header().Get("Content-Disposition"))
	}

	// 5. Cross-machine video Range & HEAD forwarding (both explicit ?machine=mac and hub-404 fallback).
	for _, q := range []string{
		"/api/file?path=~/mac-rec.mov&machine=mac&as=video",
		"/api/file?path=~/mac-rec.mov&as=video",
	} {
		rec := doReq(http.MethodGet, q, map[string]string{"Range": "bytes=4-15"})
		if rec.Code != http.StatusPartialContent {
			t.Fatalf("remote %s Range status=%d body=%s", q, rec.Code, rec.Body.String())
		}
		if !bytes.Equal(rec.Body.Bytes(), movData[4:16]) {
			t.Fatalf("remote %s Range body=%q, want %q", q, rec.Body.Bytes(), movData[4:16])
		}
		if ct := rec.Header().Get("Content-Type"); ct != "video/quicktime" {
			t.Fatalf("remote %s Content-Type=%q, want video/quicktime", q, ct)
		}
		if csp := rec.Header().Get("Content-Security-Policy"); csp != "sandbox" {
			t.Fatalf("remote %s CSP=%q, want sandbox", q, csp)
		}
		if cd := rec.Header().Get("Content-Disposition"); cd != "" {
			t.Fatalf("remote %s unexpected Content-Disposition=%q", q, cd)
		}
		hRec := doReq(http.MethodHead, q, nil)
		if hRec.Code != http.StatusOK || hRec.Body.Len() != 0 || hRec.Header().Get("X-File-Kind") != "video" {
			t.Fatalf("remote HEAD %s status=%d len=%d kind=%q", q, hRec.Code, hRec.Body.Len(), hRec.Header().Get("X-File-Kind"))
		}
	}

	// 6. CORS preflight & Range header exposure for multi-hub video access.
	opt := doReq(http.MethodOptions, "/api/file?path=~/clip.mp4&as=video", map[string]string{
		"Origin": "http://other-hub.local:7788",
	})
	if opt.Code != http.StatusNoContent {
		t.Fatalf("CORS preflight status=%d, want 204", opt.Code)
	}
	if allowH := opt.Header().Get("Access-Control-Allow-Headers"); !strings.Contains(allowH, "Range") {
		t.Fatalf("Access-Control-Allow-Headers=%q, want Range", allowH)
	}
	if allowM := opt.Header().Get("Access-Control-Allow-Methods"); !strings.Contains(allowM, "HEAD") {
		t.Fatalf("Access-Control-Allow-Methods=%q, want HEAD", allowM)
	}
	if exp := opt.Header().Get("Access-Control-Expose-Headers"); !strings.Contains(exp, "Content-Range") || !strings.Contains(exp, "Accept-Ranges") {
		t.Fatalf("Access-Control-Expose-Headers=%q, want Content-Range & Accept-Ranges", exp)
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
