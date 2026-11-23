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

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
