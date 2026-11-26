package collie

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"
)

func saveUpdateHooks(t *testing.T) {
	t.Helper()
	origVer := version
	origURL := releaseRepoURL
	origExe := executablePath
	origExec := execUpdated
	origTimeout := selfUpdateTimeout
	origFwdTimeout := selfUpdateForwardTimeout
	origRelayTimeout := selfUpdateRelayTimeout
	origMaxRelaySize := maxRelayBinarySize
	origRelayTTL := relayCacheTTL
	origRelayMax := relayCacheMaxEntries
	origMaxAttempts := updateMaxAttempts
	origRetryDelay := updateRetryBaseDelay
	origDialTimeout := updateDialTimeout
	origTLSTimeout := updateTLSTimeout
	origHeaderTimeout := updateHeaderTimeout
	origIdleTimeout := updateIdleReadTimeout
	origTagTimeout := updateTagAttemptTimeout
	origFileTimeout := updateFileAttemptTimeout
	origBinTimeout := updateBinAttemptTimeout
	origProxy := updateProxyFunc
	origDial := updateDialContext

	latestRel.Lock()
	origLatestTag, origLatestChecked := latestRel.tag, latestRel.checked
	latestRel.Unlock()
	resetRelayCache()

	// Tests should run fast and deterministically without inheriting host proxies.
	updateRetryBaseDelay = 5 * time.Millisecond
	updateProxyFunc = func(*http.Request) (*url.URL, error) { return nil, nil }

	t.Cleanup(func() {
		version = origVer
		releaseRepoURL = origURL
		executablePath = origExe
		execUpdated = origExec
		selfUpdateTimeout = origTimeout
		selfUpdateForwardTimeout = origFwdTimeout
		selfUpdateRelayTimeout = origRelayTimeout
		maxRelayBinarySize = origMaxRelaySize
		relayCacheTTL = origRelayTTL
		relayCacheMaxEntries = origRelayMax
		updateMaxAttempts = origMaxAttempts
		updateRetryBaseDelay = origRetryDelay
		updateDialTimeout = origDialTimeout
		updateTLSTimeout = origTLSTimeout
		updateHeaderTimeout = origHeaderTimeout
		updateIdleReadTimeout = origIdleTimeout
		updateTagAttemptTimeout = origTagTimeout
		updateFileAttemptTimeout = origFileTimeout
		updateBinAttemptTimeout = origBinTimeout
		updateProxyFunc = origProxy
		updateDialContext = origDial
		latestRel.Lock()
		latestRel.tag, latestRel.checked = origLatestTag, origLatestChecked
		latestRel.Unlock()
		resetRelayCache()
		if !selfUpdateMu.TryLock() {
			selfUpdateMu.Unlock()
		} else {
			selfUpdateMu.Unlock()
		}
	})
}

func TestOutdated(t *testing.T) {
	for _, c := range []struct {
		have, latest string
		want         bool
	}{
		{"v0.2.4", "v0.2.5", true},
		{"v0.2.10", "v0.2.9", false},
		{"v0.3.0", "v0.2.9", false},
		{"v1.0.0", "v1.0.0", false},
		{"dev", "v0.2.5", false},
		{"", "v0.2.5", false},
		{"v0.2.4", "", false},
	} {
		if got := outdated(c.have, c.latest); got != c.want {
			t.Errorf("outdated(%q, %q) = %v", c.have, c.latest, got)
		}
	}
}

func TestSelfUpdateConcurrencyMutexAndTempCleanup(t *testing.T) {
	saveUpdateHooks(t)

	dir := t.TempDir()
	exe := filepath.Join(dir, "collie")
	oldScript := "#!/bin/sh\necho collie v0.2.41\n"
	if err := os.WriteFile(exe, []byte(oldScript), 0o755); err != nil {
		t.Fatal(err)
	}

	newTag := "v0.2.42"
	newScript := "#!/bin/sh\necho collie " + newTag + "\n"
	sum := sha256.Sum256([]byte(newScript))
	goodSum := hex.EncodeToString(sum[:])
	asset := "collie-" + runtime.GOOS + "-" + runtime.GOARCH

	var serveBadChecksum atomic.Bool
	inDownload := make(chan struct{})
	var signalOnce sync.Once
	releaseDownload := make(chan struct{})

	var ts *httptest.Server
	ts = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/releases/latest":
			w.Header().Set("Location", ts.URL+"/releases/tag/"+newTag)
			w.WriteHeader(http.StatusFound)
		case "/releases/download/" + newTag + "/checksums.txt":
			cs := goodSum
			if serveBadChecksum.Load() {
				cs = strings.Repeat("0", 64)
			}
			fmt.Fprintf(w, "%s  %s\n", cs, asset)
		case "/releases/download/" + newTag + "/" + asset:
			signalOnce.Do(func() { close(inDownload) })
			<-releaseDownload
			w.Write([]byte(newScript))
		default:
			http.NotFound(w, r)
		}
	}))
	defer ts.Close()

	restarted := make(chan string, 1)
	version = "v0.2.41"
	releaseRepoURL = ts.URL
	executablePath = func() (string, error) { return exe, nil }
	execUpdated = func(p string) {
		restarted <- p
	}

	srv := &Server{}

	// 1) Failed update (bad checksum) cleans up .collie.new.* and unlocks selfUpdateMu.
	serveBadChecksum.Store(true)
	close(releaseDownload)
	recFail := httptest.NewRecorder()
	srv.selfUpdate(recFail, httptest.NewRequest("POST", "/api/update", nil))
	if recFail.Code != 502 {
		t.Fatalf("bad checksum status = %d, want 502: %s", recFail.Code, recFail.Body.String())
	}
	if !strings.Contains(recFail.Body.String(), "校验失败") {
		t.Fatalf("expected clear checksum error message, got %s", recFail.Body.String())
	}
	if leftovers, _ := filepath.Glob(filepath.Join(dir, ".collie.new.*")); len(leftovers) != 0 {
		t.Fatalf("temp files left after failed update: %v", leftovers)
	}
	gotOld, _ := os.ReadFile(exe)
	if string(gotOld) != oldScript {
		t.Fatalf("exe modified on failed update: %q", string(gotOld))
	}

	// 2) Concurrent calls: first blocks mid-download; parallel callers get 409 "升级已在进行中"
	// without racing or deleting the first caller's temp file.
	serveBadChecksum.Store(false)
	inDownload = make(chan struct{})
	signalOnce = sync.Once{}
	releaseDownload = make(chan struct{})

	var wg sync.WaitGroup
	recPrimary := httptest.NewRecorder()
	wg.Add(1)
	go func() {
		defer wg.Done()
		srv.selfUpdate(recPrimary, httptest.NewRequest("POST", "/api/update", nil))
	}()

	<-inDownload

	const contenders = 4
	contenderCodes := make([]int, contenders)
	contenderBodies := make([]string, contenders)
	var cwg sync.WaitGroup
	for i := range contenders {
		cwg.Add(1)
		go func(idx int) {
			defer cwg.Done()
			rec := httptest.NewRecorder()
			srv.selfUpdate(rec, httptest.NewRequest("POST", "/api/update", nil))
			contenderCodes[idx] = rec.Code
			contenderBodies[idx] = rec.Body.String()
		}(i)
	}
	cwg.Wait()

	for i := range contenders {
		if contenderCodes[i] != 409 || !strings.Contains(contenderBodies[i], "升级已在进行中") {
			t.Fatalf("contender %d: status=%d body=%s, want 409 with 升级已在进行中", i, contenderCodes[i], contenderBodies[i])
		}
	}

	// Release primary download; it must finish cleanly, atomically replace exe, and leave 0 temp files.
	close(releaseDownload)
	wg.Wait()

	if recPrimary.Code != 200 {
		t.Fatalf("primary update status = %d: %s", recPrimary.Code, recPrimary.Body.String())
	}
	var res map[string]any
	if err := json.Unmarshal(recPrimary.Body.Bytes(), &res); err != nil {
		t.Fatal(err)
	}
	if res["ok"] != true || res["from"] != "v0.2.41" || res["version"] != newTag {
		t.Fatalf("unexpected update response: %+v", res)
	}
	if gotExe := <-restarted; gotExe != exe {
		t.Fatalf("execUpdated path = %q, want %q", gotExe, exe)
	}
	// While restarting is held (before syscall.Exec replaces process), duplicate calls still get 409.
	recDuringRestart := httptest.NewRecorder()
	srv.selfUpdate(recDuringRestart, httptest.NewRequest("POST", "/api/update", nil))
	if recDuringRestart.Code != 409 {
		t.Fatalf("call during restart window = %d, want 409", recDuringRestart.Code)
	}
	selfUpdateMu.Unlock()

	if leftovers, _ := filepath.Glob(filepath.Join(dir, ".collie.new.*")); len(leftovers) != 0 {
		t.Fatalf("temp files left after successful update: %v", leftovers)
	}
	if _, err := os.Stat(exe + ".new"); !os.IsNotExist(err) {
		t.Fatalf("legacy fixed exe+.new should never be created: %v", err)
	}
	gotNew, err := os.ReadFile(exe)
	if err != nil || string(gotNew) != newScript {
		t.Fatalf("updated binary content = %q, err = %v", string(gotNew), err)
	}
	fi, err := os.Stat(exe)
	if err != nil || fi.Mode().Perm()&0o111 == 0 {
		t.Fatalf("updated binary mode = %v, want executable", fi.Mode())
	}
}

func TestFetchLatestTagRetryAndIPv4Fallback(t *testing.T) {
	t.Run("retries transient 5xx without forcing ipv4", func(t *testing.T) {
		saveUpdateHooks(t)
		var hits atomic.Int32
		var mu sync.Mutex
		var networks []string
		realDial := (&net.Dialer{Timeout: 5 * time.Second}).DialContext
		updateDialContext = func(ctx context.Context, network, addr string) (net.Conn, error) {
			mu.Lock()
			networks = append(networks, network)
			mu.Unlock()
			return realDial(ctx, network, addr)
		}

		var ts *httptest.Server
		ts = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			n := hits.Add(1)
			if n < 3 {
				w.WriteHeader(http.StatusBadGateway)
				return
			}
			w.Header().Set("Location", ts.URL+"/releases/tag/v0.2.57")
			w.WriteHeader(http.StatusFound)
		}))
		defer ts.Close()
		releaseRepoURL = ts.URL

		tag, usedIPv4, err := fetchLatestTagErr(context.Background())
		if err != nil {
			t.Fatalf("fetchLatestTagErr failed: %v", err)
		}
		if tag != "v0.2.57" || usedIPv4 {
			t.Fatalf("got tag=%q usedIPv4=%v, want v0.2.57 false", tag, usedIPv4)
		}
		if hits.Load() != 3 {
			t.Fatalf("hits = %d, want 3", hits.Load())
		}
		for _, netw := range networks {
			if netw == "tcp4" {
				t.Fatalf("5xx should not force tcp4, got dial networks %v", networks)
			}
		}
	})

	t.Run("falls back to tcp4 on direct connection reset", func(t *testing.T) {
		saveUpdateHooks(t)
		var mu sync.Mutex
		var networks []string
		realDial := (&net.Dialer{Timeout: 5 * time.Second}).DialContext
		updateDialContext = func(ctx context.Context, network, addr string) (net.Conn, error) {
			mu.Lock()
			networks = append(networks, network)
			n := len(networks)
			mu.Unlock()
			if n == 1 {
				return nil, &net.OpError{Op: "dial", Net: network, Err: syscall.ECONNRESET}
			}
			return realDial(ctx, network, addr)
		}

		var ts *httptest.Server
		ts = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Location", ts.URL+"/releases/tag/v0.2.58")
			w.WriteHeader(http.StatusFound)
		}))
		defer ts.Close()
		releaseRepoURL = ts.URL

		tag, usedIPv4, err := fetchLatestTagErr(context.Background())
		if err != nil {
			t.Fatalf("fetchLatestTagErr failed: %v", err)
		}
		if tag != "v0.2.58" || !usedIPv4 {
			t.Fatalf("got tag=%q usedIPv4=%v, want v0.2.58 true", tag, usedIPv4)
		}
		if len(networks) != 2 || networks[0] != "tcp" || networks[1] != "tcp4" {
			t.Fatalf("dial networks = %v, want [tcp tcp4]", networks)
		}
	})

	t.Run("keeps standard dial when proxy is configured", func(t *testing.T) {
		saveUpdateHooks(t)
		var ts *httptest.Server
		ts = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Location", "https://github.com/lihuanyu0w0/collie/releases/tag/v0.2.59")
			w.WriteHeader(http.StatusFound)
		}))
		defer ts.Close()
		proxyURL, err := url.Parse(ts.URL)
		if err != nil {
			t.Fatal(err)
		}
		updateProxyFunc = func(*http.Request) (*url.URL, error) { return proxyURL, nil }
		releaseRepoURL = "http://example.invalid/repo"

		var mu sync.Mutex
		var networks []string
		realDial := (&net.Dialer{Timeout: 5 * time.Second}).DialContext
		updateDialContext = func(ctx context.Context, network, addr string) (net.Conn, error) {
			mu.Lock()
			networks = append(networks, network)
			n := len(networks)
			mu.Unlock()
			if n == 1 {
				return nil, &net.OpError{Op: "dial", Net: network, Err: syscall.ETIMEDOUT}
			}
			return realDial(ctx, network, addr)
		}

		tag, usedIPv4, err := fetchLatestTagErr(context.Background())
		if err != nil {
			t.Fatalf("fetchLatestTagErr via proxy failed: %v", err)
		}
		if tag != "v0.2.59" || usedIPv4 {
			t.Fatalf("got tag=%q usedIPv4=%v, want v0.2.59 false", tag, usedIPv4)
		}
		if len(networks) != 2 || networks[0] != "tcp" || networks[1] != "tcp" {
			t.Fatalf("proxied dial networks = %v, want [tcp tcp]", networks)
		}
	})

	t.Run("does not retry 404", func(t *testing.T) {
		saveUpdateHooks(t)
		var hits atomic.Int32
		ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			hits.Add(1)
			http.NotFound(w, r)
		}))
		defer ts.Close()
		releaseRepoURL = ts.URL

		_, _, err := fetchLatestTagErr(context.Background())
		if err == nil {
			t.Fatal("expected error on 404")
		}
		if hits.Load() != 1 {
			t.Fatalf("hits = %d, want 1 (no retry on 404)", hits.Load())
		}
		if msg := formatUpdateError(err); !strings.Contains(msg, "HTTP 404") {
			t.Fatalf("formatUpdateError = %q, want HTTP 404 hint", msg)
		}
	})
}

func TestDownloadReleaseRetryEOFIdleTimeoutAndPermissions(t *testing.T) {
	t.Run("retries checksum 503 and truncated binary EOF then succeeds", func(t *testing.T) {
		saveUpdateHooks(t)
		dir := t.TempDir()
		exe := filepath.Join(dir, "collie")
		if err := os.WriteFile(exe, []byte("#!/bin/sh\necho collie v0.2.55\n"), 0o755); err != nil {
			t.Fatal(err)
		}

		tag := "v0.2.57"
		newScript := "#!/bin/sh\necho collie " + tag + "\n"
		sum := sha256.Sum256([]byte(newScript))
		goodSum := hex.EncodeToString(sum[:])
		asset := "collie-" + runtime.GOOS + "-" + runtime.GOARCH

		var csHits, binHits atomic.Int32
		ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			switch r.URL.Path {
			case "/releases/download/" + tag + "/checksums.txt":
				if csHits.Add(1) == 1 {
					w.WriteHeader(http.StatusServiceUnavailable)
					return
				}
				fmt.Fprintf(w, "%s  %s\n", goodSum, asset)
			case "/releases/download/" + tag + "/" + asset:
				if binHits.Add(1) == 1 {
					// Advertise full length but abruptly close connection after partial bytes.
					w.Header().Set("Content-Length", fmt.Sprintf("%d", len(newScript)+100))
					w.WriteHeader(http.StatusOK)
					w.Write([]byte(newScript[:8]))
					if f, ok := w.(http.Flusher); ok {
						f.Flush()
					}
					if hj, ok := w.(http.Hijacker); ok {
						if conn, _, err := hj.Hijack(); err == nil {
							conn.Close()
						}
					}
					return
				}
				w.Write([]byte(newScript))
			default:
				http.NotFound(w, r)
			}
		}))
		defer ts.Close()
		releaseRepoURL = ts.URL

		if err := downloadRelease(context.Background(), tag, exe); err != nil {
			t.Fatalf("downloadRelease failed: %v", err)
		}
		if csHits.Load() != 2 || binHits.Load() != 2 {
			t.Fatalf("hits cs=%d bin=%d, want 2 and 2", csHits.Load(), binHits.Load())
		}
		if leftovers, _ := filepath.Glob(filepath.Join(dir, ".collie.new.*")); len(leftovers) != 0 {
			t.Fatalf("leftover temp files: %v", leftovers)
		}
		got, err := os.ReadFile(exe)
		if err != nil || string(got) != newScript {
			t.Fatalf("exe content = %q err=%v", string(got), err)
		}
	})

	t.Run("aborts stalled body via idle timeout and recovers on tcp4", func(t *testing.T) {
		saveUpdateHooks(t)
		updateIdleReadTimeout = 80 * time.Millisecond

		dir := t.TempDir()
		exe := filepath.Join(dir, "collie")
		if err := os.WriteFile(exe, []byte("#!/bin/sh\necho collie v0.2.55\n"), 0o755); err != nil {
			t.Fatal(err)
		}
		tag := "v0.2.58"
		newScript := "#!/bin/sh\necho collie " + tag + "\n"
		sum := sha256.Sum256([]byte(newScript))
		goodSum := hex.EncodeToString(sum[:])
		asset := "collie-" + runtime.GOOS + "-" + runtime.GOARCH

		var mu sync.Mutex
		var binNetworks []string
		realDial := (&net.Dialer{Timeout: 5 * time.Second}).DialContext
		var inBin atomic.Bool
		updateDialContext = func(ctx context.Context, network, addr string) (net.Conn, error) {
			if inBin.Load() {
				mu.Lock()
				binNetworks = append(binNetworks, network)
				mu.Unlock()
			}
			return realDial(ctx, network, addr)
		}

		var binHits atomic.Int32
		ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			switch r.URL.Path {
			case "/releases/download/" + tag + "/checksums.txt":
				fmt.Fprintf(w, "%s  %s\n", goodSum, asset)
				inBin.Store(true)
			case "/releases/download/" + tag + "/" + asset:
				if binHits.Add(1) == 1 {
					w.Header().Set("Content-Length", fmt.Sprintf("%d", len(newScript)))
					w.WriteHeader(http.StatusOK)
					w.Write([]byte(newScript[:4]))
					if f, ok := w.(http.Flusher); ok {
						f.Flush()
					}
					select {
					case <-r.Context().Done():
					case <-time.After(2 * time.Second):
					}
					return
				}
				w.Write([]byte(newScript))
			default:
				http.NotFound(w, r)
			}
		}))
		defer ts.Close()
		releaseRepoURL = ts.URL

		if err := downloadRelease(context.Background(), tag, exe); err != nil {
			t.Fatalf("downloadRelease after idle stall failed: %v", err)
		}
		if binHits.Load() != 2 {
			t.Fatalf("binHits = %d, want 2", binHits.Load())
		}
		mu.Lock()
		gotNets := append([]string(nil), binNetworks...)
		mu.Unlock()
		if len(gotNets) < 2 || gotNets[0] != "tcp" || gotNets[1] != "tcp4" {
			t.Fatalf("binary dial networks = %v, want [tcp tcp4]", gotNets)
		}
	})

	t.Run("fails fast on unwritable directory with clear permission message", func(t *testing.T) {
		if os.Geteuid() == 0 {
			t.Skip("root ignores chmod 0500")
		}
		saveUpdateHooks(t)
		dir := t.TempDir()
		exe := filepath.Join(dir, "collie")
		if err := os.WriteFile(exe, []byte("#!/bin/sh\necho collie v0.2.55\n"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(dir, 0o555); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.Chmod(dir, 0o755) })

		var hits atomic.Int32
		ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			hits.Add(1)
			http.NotFound(w, r)
		}))
		defer ts.Close()
		releaseRepoURL = ts.URL

		err := downloadRelease(context.Background(), "v0.2.57", exe)
		if err == nil {
			t.Fatal("expected permission error")
		}
		if hits.Load() != 0 {
			t.Fatalf("expected 0 HTTP requests for unwritable dir, got %d", hits.Load())
		}
		msg := formatUpdateError(err)
		if !strings.Contains(msg, "无写权限") {
			t.Fatalf("formatUpdateError = %q, want 无写权限", msg)
		}
	})
}

func TestSelfUpdateContextShieldAndLogs(t *testing.T) {
	saveUpdateHooks(t)

	dir := t.TempDir()
	exe := filepath.Join(dir, "collie")
	if err := os.WriteFile(exe, []byte("#!/bin/sh\necho collie v0.2.55\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	tag := "v0.2.57"
	newScript := "#!/bin/sh\necho collie " + tag + "\n"
	sum := sha256.Sum256([]byte(newScript))
	goodSum := hex.EncodeToString(sum[:])
	asset := "collie-" + runtime.GOOS + "-" + runtime.GOARCH

	inBin := make(chan struct{})
	releaseBin := make(chan struct{})
	var ts *httptest.Server
	ts = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/releases/latest":
			w.Header().Set("Location", ts.URL+"/releases/tag/"+tag)
			w.WriteHeader(http.StatusFound)
		case "/releases/download/" + tag + "/checksums.txt":
			fmt.Fprintf(w, "%s  %s\n", goodSum, asset)
		case "/releases/download/" + tag + "/" + asset:
			close(inBin)
			<-releaseBin
			w.Write([]byte(newScript))
		default:
			http.NotFound(w, r)
		}
	}))
	defer ts.Close()

	var logBuf bytes.Buffer
	origOut := log.Writer()
	log.SetOutput(&logBuf)
	t.Cleanup(func() { log.SetOutput(origOut) })

	restarted := make(chan string, 1)
	version = "v0.2.55"
	releaseRepoURL = ts.URL
	executablePath = func() (string, error) { return exe, nil }
	execUpdated = func(p string) { restarted <- p }

	// 1) Request context canceled mid-download must NOT abort downloadRelease.
	reqCtx, cancelReq := context.WithCancel(context.Background())
	req := httptest.NewRequest("POST", "/api/update", nil).WithContext(reqCtx)
	rec := httptest.NewRecorder()
	done := make(chan struct{})
	srv := &Server{}
	go func() {
		defer close(done)
		srv.selfUpdate(rec, req)
	}()

	<-inBin
	cancelReq()
	close(releaseBin)
	<-done

	if rec.Code != 200 {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	if got := <-restarted; got != exe {
		t.Fatalf("restarted = %q, want %q", got, exe)
	}
	selfUpdateMu.Unlock()

	if !strings.Contains(logBuf.String(), "self-update start") || !strings.Contains(logBuf.String(), "updated v0.2.55 → "+tag) {
		t.Fatalf("expected start and success log lines, got:\n%s", logBuf.String())
	}

	// 2) Failure logs must record stage/reason and sanitize credentials.
	logBuf.Reset()
	badProxy, _ := url.Parse("http://alice:supersecret@127.0.0.1:1")
	updateProxyFunc = func(*http.Request) (*url.URL, error) { return badProxy, nil }
	updateDialContext = func(ctx context.Context, network, addr string) (net.Conn, error) {
		return nil, errors.New("proxyconnect tcp: http://alice:supersecret@127.0.0.1:1: connection refused")
	}
	recFail := httptest.NewRecorder()
	srv.selfUpdate(recFail, httptest.NewRequest("POST", "/api/update", nil))
	if recFail.Code != 502 {
		t.Fatalf("proxy failure status = %d, want 502", recFail.Code)
	}
	if !strings.Contains(recFail.Body.String(), "代理") {
		t.Fatalf("expected proxy error in response, got %s", recFail.Body.String())
	}
	if strings.Contains(recFail.Body.String(), "supersecret") || strings.Contains(logBuf.String(), "supersecret") {
		t.Fatalf("proxy credentials leaked! resp=%s log=%s", recFail.Body.String(), logBuf.String())
	}
	if !strings.Contains(logBuf.String(), "self-update failed") {
		t.Fatalf("expected failure log entry, got:\n%s", logBuf.String())
	}
}

type updateRoundTripFunc func(*http.Request) (*http.Response, error)

func (f updateRoundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestForwardUpdateErrorPropagationAndTimeout(t *testing.T) {
	saveUpdateHooks(t)

	var logBuf bytes.Buffer
	origOut := log.Writer()
	log.SetOutput(&logBuf)
	t.Cleanup(func() { log.SetOutput(origOut) })

	h := newHerdr("/nonexistent.sock", "hubbox", t.TempDir())
	srv := &Server{herdr: h}
	handler := srv.routed(srv.selfUpdate)

	// 1) Offline node returns 503 JSON and logs failure.
	recOff := httptest.NewRecorder()
	handler.ServeHTTP(recOff, httptest.NewRequest("POST", "/api/update?machine=ghost", nil))
	if recOff.Code != 503 || !strings.Contains(recOff.Body.String(), "不在线") {
		t.Fatalf("offline node: status=%d body=%s", recOff.Code, recOff.Body.String())
	}
	if !strings.Contains(logBuf.String(), "remote-update failed (machine=ghost") {
		t.Fatalf("missing offline remote-update log: %s", logBuf.String())
	}

	// 2) Remote node returns 502 JSON error: hub preserves real message and logs it.
	logBuf.Reset()
	h.attach(&Machine{
		ID: "mac", Name: "mac", Online: true,
		client: &http.Client{Transport: updateRoundTripFunc(func(r *http.Request) (*http.Response, error) {
			body := `{"error":"更新失败：连接 GitHub 超时（已自动重试并尝试 IPv4）"}`
			return &http.Response{
				StatusCode: 502,
				Status:     "502 Bad Gateway",
				Header:     http.Header{"Content-Type": {"application/json"}},
				Body:       io.NopCloser(strings.NewReader(body)),
			}, nil
		})},
	})
	recErr := httptest.NewRecorder()
	handler.ServeHTTP(recErr, httptest.NewRequest("POST", "/api/update?machine=mac", nil))
	if recErr.Code != 502 || !strings.Contains(recErr.Body.String(), "已自动重试并尝试 IPv4") {
		t.Fatalf("remote JSON error: status=%d body=%s", recErr.Code, recErr.Body.String())
	}
	if !strings.Contains(logBuf.String(), "remote-update failed (machine=mac status=502") {
		t.Fatalf("missing remote JSON failure log: %s", logBuf.String())
	}

	// 3) Remote node hangs past selfUpdateForwardTimeout: hub times out cleanly with JSON error.
	logBuf.Reset()
	selfUpdateForwardTimeout = 60 * time.Millisecond
	h.attach(&Machine{
		ID: "slow", Name: "slow", Online: true,
		client: &http.Client{Transport: updateRoundTripFunc(func(r *http.Request) (*http.Response, error) {
			<-r.Context().Done()
			return nil, r.Context().Err()
		})},
	})
	recSlow := httptest.NewRecorder()
	handler.ServeHTTP(recSlow, httptest.NewRequest("POST", "/api/update?machine=slow", nil))
	if recSlow.Code != 502 || !strings.Contains(recSlow.Body.String(), "更新超时") {
		t.Fatalf("remote timeout: status=%d body=%s", recSlow.Code, recSlow.Body.String())
	}
	if !strings.Contains(logBuf.String(), "remote-update failed (machine=slow") {
		t.Fatalf("missing remote timeout log: %s", logBuf.String())
	}
}

func TestFormatUpdateErrorMessages(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want string
	}{
		{"404", &httpStatusError{file: "checksums.txt", statusCode: 404, status: "404 Not Found"}, "HTTP 404"},
		{"503", &httpStatusError{file: "collie-linux-amd64", statusCode: 503, status: "503 Service Unavailable"}, "暂时异常"},
		{"429", &httpStatusError{file: "releases/latest", statusCode: 429, status: "429 Too Many Requests"}, "限流"},
		{"checksum", errBadChecksum, "校验失败"},
		{"perm", &updateFileError{dir: "/usr/local/bin", err: os.ErrPermission}, "无写权限"},
		{"enospc", &updateFileError{dir: "/tmp", err: syscall.ENOSPC}, "磁盘空间不足"},
		{"proxy", &updateOpError{err: errors.New("proxyconnect tcp: connection refused"), usedProxy: true}, "代理服务器"},
		{"dns", &updateOpError{err: &net.DNSError{Err: "no such host", Name: "github.com"}}, "DNS"},
		{"tls", &updateOpError{err: errors.New("x509: certificate signed by unknown authority")}, "TLS"},
		{"timeout-ipv4", &updateOpError{err: context.DeadlineExceeded, triedIPv4: true}, "尝试 IPv4"},
		{"timeout-proxy", &updateOpError{err: context.DeadlineExceeded, usedProxy: true}, "经代理访问 GitHub 超时"},
		{"eof-ipv4", &updateOpError{err: io.ErrUnexpectedEOF, triedIPv4: true}, "尝试 IPv4"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := formatUpdateError(tc.err)
			if !strings.Contains(got, tc.want) {
				t.Errorf("formatUpdateError(%v) = %q, want substring %q", tc.err, got, tc.want)
			}
		})
	}
	if got := sanitizeUpdateErr("http://user:pass@proxy.example:8080 failed"); strings.Contains(got, "pass") || !strings.Contains(got, "http://***@proxy.example:8080") {
		t.Errorf("sanitizeUpdateErr failed: %q", got)
	}
}

func TestGetVersionAndStateMachinesVersion(t *testing.T) {
	origVer := version
	// Prevent background refresh from hitting GitHub during tests.
	latestRel.once.Do(func() {})
	latestRel.Lock()
	origTag, origChecked, origBusy := latestRel.tag, latestRel.checked, latestRel.busy
	latestRel.tag = "v0.2.51"
	latestRel.busy = true
	latestRel.Unlock()
	t.Cleanup(func() {
		version = origVer
		latestRel.Lock()
		latestRel.tag, latestRel.checked, latestRel.busy = origTag, origChecked, origBusy
		latestRel.Unlock()
	})

	h := newHerdr("/nonexistent.sock", "hubbox", t.TempDir())
	h.attach(&Machine{ID: "mac", Name: "mac", Version: "v0.2.50", Online: true})
	h.attach(&Machine{ID: "old-off", Name: "old-off", Version: "v0.2.40", Online: false})
	srv := &Server{herdr: h}

	// 1) Hub already up to date, one online node outdated, one offline node outdated.
	version = "v0.2.51"
	ms := h.Machines()
	if len(ms) != 3 || !ms[0].Local || ms[0].Version != "v0.2.51" {
		t.Fatalf("Machines() local version = %+v, want v0.2.51", ms)
	}

	rec := httptest.NewRecorder()
	srv.getVersion(rec, httptest.NewRequest("GET", "/api/version", nil))
	if rec.Code != 200 {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
	}
	var got struct {
		Version  string           `json:"version"`
		Latest   string           `json:"latest"`
		Outdated []string         `json:"outdated"`
		Machines []versionMachine `json:"machines"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.Version != "v0.2.51" || got.Latest != "v0.2.51" {
		t.Fatalf("version/latest = %q/%q", got.Version, got.Latest)
	}
	if len(got.Outdated) != 1 || got.Outdated[0] != "mac" {
		t.Fatalf("outdated = %v, want [mac]", got.Outdated)
	}
	if len(got.Machines) != 3 {
		t.Fatalf("machines len = %d, want 3", len(got.Machines))
	}
	byID := map[string]versionMachine{}
	for _, m := range got.Machines {
		byID[m.ID] = m
	}
	if !byID[""].Local || !byID[""].Online || byID[""].Outdated || byID[""].Version != "v0.2.51" {
		t.Fatalf("hub entry = %+v", byID[""])
	}
	if byID["mac"].Local || !byID["mac"].Online || !byID["mac"].Outdated || byID["mac"].Version != "v0.2.50" {
		t.Fatalf("mac entry = %+v", byID["mac"])
	}
	if byID["old-off"].Online || byID["old-off"].Outdated {
		t.Fatalf("offline node should not be outdated: %+v", byID["old-off"])
	}

	// 2) Both hub and online node outdated.
	version = "v0.2.50"
	rec2 := httptest.NewRecorder()
	srv.getVersion(rec2, httptest.NewRequest("GET", "/api/version", nil))
	got.Outdated = nil
	if err := json.Unmarshal(rec2.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if len(got.Outdated) != 2 || got.Outdated[0] != "hubbox" || got.Outdated[1] != "mac" {
		t.Fatalf("outdated = %v, want [hubbox mac]", got.Outdated)
	}

	// 3) All online machines up to date: outdated must marshal as JSON null for backwards compatibility.
	version = "v0.2.51"
	h.attach(&Machine{ID: "mac", Name: "mac", Version: "v0.2.51", Online: true})
	rec3 := httptest.NewRecorder()
	srv.getVersion(rec3, httptest.NewRequest("GET", "/api/version", nil))
	if !strings.Contains(rec3.Body.String(), `"outdated":null`) {
		t.Fatalf("expected outdated:null when up to date, got %s", rec3.Body.String())
	}
}

func TestHubRelayRemoteUpdateFallbackAndCache(t *testing.T) {
	saveUpdateHooks(t)

	newTag := "v0.2.76"
	newScript := "#!/bin/sh\necho collie " + newTag + "\n"
	sum := sha256.Sum256([]byte(newScript))
	goodSum := hex.EncodeToString(sum[:])
	goos, goarch := runtime.GOOS, runtime.GOARCH
	asset := "collie-" + goos + "-" + goarch

	var latestHits, csHits, binHits atomic.Int32
	var gh *httptest.Server
	gh = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/releases/latest":
			latestHits.Add(1)
			w.Header().Set("Location", gh.URL+"/releases/tag/"+newTag)
			w.WriteHeader(http.StatusFound)
		case "/releases/download/" + newTag + "/checksums.txt":
			csHits.Add(1)
			fmt.Fprintf(w, "%s  %s\n", goodSum, asset)
		case "/releases/download/" + newTag + "/" + asset:
			binHits.Add(1)
			w.Write([]byte(newScript))
		default:
			http.NotFound(w, r)
		}
	}))
	defer gh.Close()
	releaseRepoURL = gh.URL

	// Prepare two isolated fake node binaries on disk.
	dirA := t.TempDir()
	exeA := filepath.Join(dirA, "collie")
	oldScript := "#!/bin/sh\necho collie v0.2.72\n"
	if err := os.WriteFile(exeA, []byte(oldScript), 0o755); err != nil {
		t.Fatal(err)
	}
	dirB := t.TempDir()
	exeB := filepath.Join(dirB, "collie")
	if err := os.WriteFile(exeB, []byte(oldScript), 0o755); err != nil {
		t.Fatal(err)
	}

	var curExe atomic.Value
	curExe.Store(exeA)
	executablePath = func() (string, error) { return curExe.Load().(string), nil }

	restarted := make(chan string, 4)
	execUpdated = func(p string) { restarted <- p }
	version = "v0.2.72"

	// Build a real NodeRoutes handler; direct /api/update simulates a node without
	// internet access (returns 502 timeout), while /api/update/relay executes the
	// real node relay handler.
	nodeSrv := &Server{node: true}
	nodeRoutes := nodeSrv.NodeRoutes()
	makeNoInternetNodeClient := func() *http.Client {
		return &http.Client{Transport: updateRoundTripFunc(func(r *http.Request) (*http.Response, error) {
			if r.URL.Path == "/api/update" {
				body := `{"error":"查不到最新版本：连接 GitHub 超时（已自动重试并尝试 IPv4）","relayable":true}`
				return &http.Response{
					StatusCode: 502,
					Status:     "502 Bad Gateway",
					Header:     http.Header{"Content-Type": {"application/json"}},
					Body:       io.NopCloser(strings.NewReader(body)),
				}, nil
			}
			rec := httptest.NewRecorder()
			nodeRoutes.ServeHTTP(rec, r)
			return rec.Result(), nil
		})}
	}

	h := newHerdr("/nonexistent.sock", "hubbox", t.TempDir())
	h.attach(&Machine{
		ID: "devbox-a", Name: "devbox-a", OS: goos + "/" + goarch, Version: "v0.2.72", Online: true,
		client: makeNoInternetNodeClient(),
	})
	h.attach(&Machine{
		ID: "devbox-b", Name: "devbox-b", OS: goos + "/" + goarch, Version: "v0.2.72", Online: true,
		client: makeNoInternetNodeClient(),
	})
	var pushToLatest atomic.Int32
	h.attach(&Machine{
		ID: "devbox-up2date", Name: "devbox-up2date", OS: goos + "/" + goarch, Version: newTag, Online: true,
		client: &http.Client{Transport: updateRoundTripFunc(func(r *http.Request) (*http.Response, error) {
			if r.URL.Path == "/api/update/relay" {
				pushToLatest.Add(1)
			}
			body := `{"error":"查不到最新版本：连接 GitHub 超时","relayable":true}`
			return &http.Response{
				StatusCode: 502,
				Status:     "502 Bad Gateway",
				Header:     http.Header{"Content-Type": {"application/json"}},
				Body:       io.NopCloser(strings.NewReader(body)),
			}, nil
		})},
	})

	hubSrv := &Server{herdr: h}
	handler := hubSrv.routed(hubSrv.selfUpdate)

	// 1) Upgrade devbox-a: direct fails -> Hub downloads, verifies SHA-256, pushes to node,
	//    node verifies SHA-256, runs `version`, atomically replaces exe (0755), and restarts.
	recA := httptest.NewRecorder()
	handler.ServeHTTP(recA, httptest.NewRequest("POST", "/api/update?machine=devbox-a", nil))
	if recA.Code != 200 {
		t.Fatalf("devbox-a relay status=%d body=%s", recA.Code, recA.Body.String())
	}
	var resA map[string]any
	if err := json.Unmarshal(recA.Body.Bytes(), &resA); err != nil {
		t.Fatal(err)
	}
	if resA["ok"] != true || resA["from"] != "v0.2.72" || resA["version"] != newTag || resA["relayed"] != true {
		t.Fatalf("unexpected devbox-a relay response: %+v", resA)
	}
	if got := <-restarted; got != exeA {
		t.Fatalf("restarted = %q, want %q", got, exeA)
	}
	selfUpdateMu.Unlock()

	gotBinA, err := os.ReadFile(exeA)
	if err != nil || string(gotBinA) != newScript {
		t.Fatalf("exeA content = %q err=%v", string(gotBinA), err)
	}
	fiA, err := os.Stat(exeA)
	if err != nil || fiA.Mode().Perm() != 0o755 {
		t.Fatalf("exeA mode = %o, want 0755", fiA.Mode().Perm())
	}
	if leftovers, _ := filepath.Glob(filepath.Join(dirA, ".collie.new.*")); len(leftovers) != 0 {
		t.Fatalf("leftover temp files in dirA: %v", leftovers)
	}
	if csHits.Load() != 1 || binHits.Load() != 1 {
		t.Fatalf("after first relay: csHits=%d binHits=%d, want 1 and 1", csHits.Load(), binHits.Load())
	}

	// 2) Upgrade devbox-b (same os/arch): must reuse Hub short-term cache without re-downloading.
	curExe.Store(exeB)
	recB := httptest.NewRecorder()
	handler.ServeHTTP(recB, httptest.NewRequest("POST", "/api/update?machine=devbox-b", nil))
	if recB.Code != 200 {
		t.Fatalf("devbox-b relay status=%d body=%s", recB.Code, recB.Body.String())
	}
	if got := <-restarted; got != exeB {
		t.Fatalf("restarted = %q, want %q", got, exeB)
	}
	selfUpdateMu.Unlock()

	gotBinB, err := os.ReadFile(exeB)
	if err != nil || string(gotBinB) != newScript {
		t.Fatalf("exeB content = %q err=%v", string(gotBinB), err)
	}
	if csHits.Load() != 1 || binHits.Load() != 1 {
		t.Fatalf("expected Hub cache hit on second node: csHits=%d binHits=%d, want 1 and 1", csHits.Load(), binHits.Load())
	}

	// 3) Upgrade devbox-up2date (already at latest, just couldn't reach GitHub): returns noop 200 without pushing.
	recC := httptest.NewRecorder()
	handler.ServeHTTP(recC, httptest.NewRequest("POST", "/api/update?machine=devbox-up2date", nil))
	if recC.Code != 200 {
		t.Fatalf("devbox-up2date status=%d body=%s", recC.Code, recC.Body.String())
	}
	var resC map[string]any
	_ = json.Unmarshal(recC.Body.Bytes(), &resC)
	if resC["ok"] != true || resC["latest"] != true || resC["relayed"] != true || pushToLatest.Load() != 0 {
		t.Fatalf("unexpected noop relay result: %+v pushCount=%d", resC, pushToLatest.Load())
	}
}

func TestHubRelayChecksumRejectionAndOldNodeDegradation(t *testing.T) {
	saveUpdateHooks(t)

	newTag := "v0.2.76"
	newScript := "#!/bin/sh\necho collie " + newTag + "\n"
	sum := sha256.Sum256([]byte(newScript))
	goodSum := hex.EncodeToString(sum[:])
	badSum := strings.Repeat("a", 64)
	goos, goarch := runtime.GOOS, runtime.GOARCH
	asset := "collie-" + goos + "-" + goarch

	var serveBadChecksum atomic.Bool
	var ghHits atomic.Int32
	var gh *httptest.Server
	gh = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ghHits.Add(1)
		switch r.URL.Path {
		case "/releases/latest":
			w.Header().Set("Location", gh.URL+"/releases/tag/"+newTag)
			w.WriteHeader(http.StatusFound)
		case "/releases/download/" + newTag + "/checksums.txt":
			cs := goodSum
			if serveBadChecksum.Load() {
				cs = badSum
			}
			fmt.Fprintf(w, "%s  %s\n", cs, asset)
		case "/releases/download/" + newTag + "/" + asset:
			w.Write([]byte(newScript))
		default:
			http.NotFound(w, r)
		}
	}))
	defer gh.Close()
	releaseRepoURL = gh.URL

	// 1) Hub-side SHA-256 mismatch rejects before ever pushing to the remote node.
	serveBadChecksum.Store(true)
	var relayPushCount atomic.Int32
	h := newHerdr("/nonexistent.sock", "hubbox", t.TempDir())
	h.attach(&Machine{
		ID: "devbox", Name: "devbox", OS: goos + "/" + goarch, Version: "v0.2.72", Online: true,
		client: &http.Client{Transport: updateRoundTripFunc(func(r *http.Request) (*http.Response, error) {
			if r.URL.Path == "/api/update/relay" {
				relayPushCount.Add(1)
			}
			body := `{"error":"查不到最新版本：连接 GitHub 超时","relayable":true}`
			return &http.Response{
				StatusCode: 502,
				Status:     "502 Bad Gateway",
				Header:     http.Header{"Content-Type": {"application/json"}},
				Body:       io.NopCloser(strings.NewReader(body)),
			}, nil
		})},
	})
	hubSrv := &Server{herdr: h}
	handler := hubSrv.routed(hubSrv.selfUpdate)

	recBadHub := httptest.NewRecorder()
	handler.ServeHTTP(recBadHub, httptest.NewRequest("POST", "/api/update?machine=devbox", nil))
	if recBadHub.Code != 502 || !strings.Contains(recBadHub.Body.String(), "校验失败") {
		t.Fatalf("hub bad checksum: status=%d body=%s", recBadHub.Code, recBadHub.Body.String())
	}
	if relayPushCount.Load() != 0 {
		t.Fatalf("hub pushed binary despite checksum mismatch (count=%d)", relayPushCount.Load())
	}

	// 2) Node-side SHA-256 mismatch: tampered body rejected with 422, binary intact, 0 temp files, no restart.
	dir := t.TempDir()
	exe := filepath.Join(dir, "collie")
	origScript := "#!/bin/sh\necho collie v0.2.72\n"
	if err := os.WriteFile(exe, []byte(origScript), 0o755); err != nil {
		t.Fatal(err)
	}
	version = "v0.2.72"
	executablePath = func() (string, error) { return exe, nil }
	var restarted atomic.Bool
	execUpdated = func(string) { restarted.Store(true) }

	nodeSrv := &Server{node: true}
	nodeRoutes := nodeSrv.NodeRoutes()
	tamperedReq := httptest.NewRequest(http.MethodPost, "http://node/api/update/relay", strings.NewReader("#!/bin/sh\necho tampered\n"))
	tamperedReq.Header.Set(relayUpdateTagHeader, newTag)
	tamperedReq.Header.Set(relayUpdateSHAHeader, goodSum)
	recTampered := httptest.NewRecorder()
	nodeRoutes.ServeHTTP(recTampered, tamperedReq)
	if recTampered.Code != 422 || !strings.Contains(recTampered.Body.String(), "SHA-256 不匹配") {
		t.Fatalf("tampered relay status=%d body=%s, want 422 with SHA-256 mismatch", recTampered.Code, recTampered.Body.String())
	}
	if restarted.Load() {
		t.Fatal("node must not restart on checksum mismatch")
	}
	if got, _ := os.ReadFile(exe); string(got) != origScript {
		t.Fatalf("exe was modified on checksum mismatch: %q", string(got))
	}
	if leftovers, _ := filepath.Glob(filepath.Join(dir, ".collie.new.*")); len(leftovers) != 0 {
		t.Fatalf("leftover temp files after rejected relay: %v", leftovers)
	}

	// 3) Non-node server refuses relayUpdate with 403.
	hubDirectRec := httptest.NewRecorder()
	(&Server{node: false}).relayUpdate(hubDirectRec, tamperedReq)
	if hubDirectRec.Code != 403 {
		t.Fatalf("non-node relayUpdate status=%d, want 403", hubDirectRec.Code)
	}

	// 4) Old node degradation: direct /api/update returns 502, /api/update/relay returns 404.
	serveBadChecksum.Store(false)
	h.attach(&Machine{
		ID: "legacy-node", Name: "legacy-node", OS: goos + "/" + goarch, Version: "v0.2.68", Online: true,
		client: &http.Client{Transport: updateRoundTripFunc(func(r *http.Request) (*http.Response, error) {
			if r.URL.Path == "/api/update" {
				body := `{"error":"查不到最新版本：连接 GitHub 超时"}`
				return &http.Response{
					StatusCode: 502,
					Status:     "502 Bad Gateway",
					Header:     http.Header{"Content-Type": {"application/json"}},
					Body:       io.NopCloser(strings.NewReader(body)),
				}, nil
			}
			return &http.Response{
				StatusCode: 404,
				Status:     "404 Not Found",
				Header:     http.Header{"Content-Type": {"text/plain; charset=utf-8"}},
				Body:       io.NopCloser(strings.NewReader("404 page not found\n")),
			}, nil
		})},
	})
	recOld := httptest.NewRecorder()
	handler.ServeHTTP(recOld, httptest.NewRequest("POST", "/api/update?machine=legacy-node", nil))
	if recOld.Code != 502 {
		t.Fatalf("old node relay status=%d body=%s", recOld.Code, recOld.Body.String())
	}
	for _, want := range []string{"v0.2.68", "暂不支持接收 Hub 中转升级", "手动升级"} {
		if !strings.Contains(recOld.Body.String(), want) {
			t.Fatalf("old node message missing %q: %s", want, recOld.Body.String())
		}
	}

	// 5) Non-relayable local node error (e.g., unwritable directory): Hub does NOT invoke GitHub or relay.
	ghHitsBefore := ghHits.Load()
	h.attach(&Machine{
		ID: "ro-node", Name: "ro-node", OS: goos + "/" + goarch, Version: "v0.2.72", Online: true,
		client: &http.Client{Transport: updateRoundTripFunc(func(r *http.Request) (*http.Response, error) {
			if r.URL.Path == "/api/update/relay" {
				t.Errorf("unexpected relay push on local permission error")
			}
			body := `{"error":"更新失败：程序所在目录无写权限（/usr/local/bin）","relayable":false}`
			return &http.Response{
				StatusCode: 502,
				Status:     "502 Bad Gateway",
				Header:     http.Header{"Content-Type": {"application/json"}},
				Body:       io.NopCloser(strings.NewReader(body)),
			}, nil
		})},
	})
	recRO := httptest.NewRecorder()
	handler.ServeHTTP(recRO, httptest.NewRequest("POST", "/api/update?machine=ro-node", nil))
	if recRO.Code != 502 || !strings.Contains(recRO.Body.String(), "无写权限") {
		t.Fatalf("local permission error status=%d body=%s", recRO.Code, recRO.Body.String())
	}
	if ghHits.Load() != ghHitsBefore {
		t.Fatalf("non-relayable error should not hit GitHub (before=%d after=%d)", ghHitsBefore, ghHits.Load())
	}
}
