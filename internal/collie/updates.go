package collie

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
)

// The hub looks up the latest release now and then, so the devices page can
// say which computers run an older Collie. The release page's redirect
// (…/releases/latest → …/tag/vX.Y.Z) needs no API token or rate limit.

const repo = "lihuanyu0w0/collie"

var (
	selfUpdateMu             sync.Mutex
	releaseRepoURL           = "https://github.com/" + repo
	selfUpdateTimeout        = 2*time.Minute + 30*time.Second
	selfUpdateForwardTimeout = 2*time.Minute + 50*time.Second
	updateMaxAttempts        = 3
	updateRetryBaseDelay     = 600 * time.Millisecond
	updateDialTimeout        = 12 * time.Second
	updateTLSTimeout         = 12 * time.Second
	updateHeaderTimeout      = 20 * time.Second
	updateIdleReadTimeout    = 25 * time.Second
	updateTagAttemptTimeout  = 15 * time.Second
	updateFileAttemptTimeout = 20 * time.Second
	updateBinAttemptTimeout  = 90 * time.Second
	updateProxyFunc          = http.ProxyFromEnvironment
	updateDialContext        = func(ctx context.Context, network, addr string) (net.Conn, error) {
		d := &net.Dialer{Timeout: updateDialTimeout, KeepAlive: 30 * time.Second}
		return d.DialContext(ctx, network, addr)
	}
	executablePath = func() (string, error) {
		exe, err := os.Executable()
		if err == nil {
			exe, err = filepath.EvalSymlinks(exe)
		}
		return exe, err
	}
	execUpdated = func(exe string) {
		time.Sleep(800 * time.Millisecond)
		err := syscall.Exec(exe, os.Args, os.Environ())
		log.Printf("restart after update: %v", err) // only reached if exec failed
		os.Exit(1)
	}
)

var (
	errBadChecksum     = errors.New("checksum mismatch")
	errIdleReadTimeout = errors.New("download stalled (read timeout)")
	errTruncatedBody   = errors.New("incomplete response body")
	credURLRe          = regexp.MustCompile(`(https?|socks5h?)://[^/@\s]+@`)
)

type httpStatusError struct {
	file       string
	statusCode int
	status     string
}

func (e *httpStatusError) Error() string {
	return fmt.Sprintf("GET %s: %s", e.file, e.status)
}

type updateFileError struct {
	dir string
	err error
}

func (e *updateFileError) Error() string { return e.err.Error() }
func (e *updateFileError) Unwrap() error { return e.err }

type updateOpError struct {
	err       error
	usedProxy bool
	triedIPv4 bool
}

func (e *updateOpError) Error() string { return e.err.Error() }
func (e *updateOpError) Unwrap() error { return e.err }

func sanitizeUpdateErr(s string) string {
	return credURLRe.ReplaceAllString(s, "$1://***@")
}

func requestUsesProxy(req *http.Request) bool {
	if updateProxyFunc == nil || req == nil {
		return false
	}
	u, err := updateProxyFunc(req)
	return err == nil && u != nil
}

func newUpdateTransport(forceIPv4 bool) *http.Transport {
	dial := updateDialContext
	proxy := updateProxyFunc
	return &http.Transport{
		Proxy: proxy,
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			if forceIPv4 && (network == "tcp" || network == "tcp6") {
				network = "tcp4"
			}
			return dial(ctx, network, addr)
		},
		ForceAttemptHTTP2:     true,
		MaxIdleConns:          4,
		IdleConnTimeout:       30 * time.Second,
		TLSHandshakeTimeout:   updateTLSTimeout,
		ResponseHeaderTimeout: updateHeaderTimeout,
		ExpectContinueTimeout: 1 * time.Second,
	}
}

func isTimeoutErr(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, errIdleReadTimeout) || errors.Is(err, syscall.ETIMEDOUT) {
		return true
	}
	var ne net.Error
	if errors.As(err, &ne) && ne.Timeout() {
		return true
	}
	low := strings.ToLower(err.Error())
	return strings.Contains(low, "timeout") || strings.Contains(low, "timed out") || strings.Contains(low, "deadline exceeded")
}

func isConnResetOrEOF(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) || errors.Is(err, errTruncatedBody) ||
		errors.Is(err, syscall.ECONNRESET) || errors.Is(err, syscall.ECONNABORTED) || errors.Is(err, syscall.EPIPE) {
		return true
	}
	low := strings.ToLower(err.Error())
	return strings.Contains(low, "connection reset") ||
		strings.Contains(low, "broken pipe") ||
		strings.Contains(low, "unexpected eof") ||
		strings.Contains(low, "eof") ||
		strings.Contains(low, "client connection lost") ||
		strings.Contains(low, "stream error") ||
		strings.Contains(low, "use of closed network connection")
}

func isProxyErr(err error, usedProxy bool) bool {
	if err == nil {
		return false
	}
	low := strings.ToLower(err.Error())
	if strings.Contains(low, "proxyconnect") ||
		strings.Contains(low, "proxy authentication required") ||
		strings.Contains(low, "407 ") ||
		strings.Contains(low, "invalid proxy") ||
		strings.Contains(low, "malformed http response") && usedProxy {
		return true
	}
	if usedProxy && (errors.Is(err, syscall.ECONNREFUSED) || strings.Contains(low, "connection refused")) {
		return true
	}
	return false
}

func isIPv4Unreachable(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, syscall.ENETUNREACH) || errors.Is(err, syscall.EHOSTUNREACH) || errors.Is(err, syscall.EADDRNOTAVAIL) {
		return true
	}
	low := strings.ToLower(err.Error())
	return strings.Contains(low, "network is unreachable") || strings.Contains(low, "no route to host")
}

func isRetryableUpdateErr(parentCtx context.Context, err error) bool {
	if err == nil || parentCtx.Err() != nil {
		return false
	}
	var fe *updateFileError
	if errors.As(err, &fe) {
		return false
	}
	if errors.Is(err, errBadChecksum) {
		return false
	}
	var he *httpStatusError
	if errors.As(err, &he) {
		return he.statusCode >= 500 || he.statusCode == http.StatusTooManyRequests || he.statusCode == http.StatusRequestTimeout
	}
	if isTimeoutErr(err) || isConnResetOrEOF(err) {
		return true
	}
	if errors.Is(err, syscall.ECONNREFUSED) || errors.Is(err, syscall.ENETUNREACH) || errors.Is(err, syscall.EHOSTUNREACH) {
		return true
	}
	var opErr *net.OpError
	if errors.As(err, &opErr) {
		return true
	}
	var urlErr *url.Error
	if errors.As(err, &urlErr) {
		return isTimeoutErr(urlErr.Err) || isConnResetOrEOF(urlErr.Err)
	}
	return false
}

func shouldFallbackIPv4(err error, usedProxy bool) bool {
	if err == nil || usedProxy {
		return false
	}
	var he *httpStatusError
	if errors.As(err, &he) {
		return false
	}
	var fe *updateFileError
	if errors.As(err, &fe) {
		return false
	}
	if isTimeoutErr(err) || isConnResetOrEOF(err) {
		return true
	}
	var opErr *net.OpError
	return errors.As(err, &opErr)
}

func sleepBackoff(ctx context.Context, attempt int) error {
	delay := updateRetryBaseDelay * time.Duration(1<<attempt)
	if delay <= 0 {
		return ctx.Err()
	}
	t := time.NewTimer(delay)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

func formatUpdateError(err error) string {
	if err == nil {
		return ""
	}
	var usedProxy, triedIPv4 bool
	var oe *updateOpError
	if errors.As(err, &oe) {
		usedProxy = oe.usedProxy
		triedIPv4 = oe.triedIPv4
	}

	var fe *updateFileError
	if errors.As(err, &fe) {
		if errors.Is(fe.err, os.ErrPermission) || errors.Is(fe.err, syscall.EACCES) || errors.Is(fe.err, syscall.EPERM) || errors.Is(fe.err, syscall.EROFS) {
			return fmt.Sprintf("程序所在目录无写权限（%s），请检查文件权限或用安装命令手动更新", fe.dir)
		}
		if errors.Is(fe.err, syscall.ENOSPC) {
			return fmt.Sprintf("磁盘空间不足，无法写入新版本（%s）", fe.dir)
		}
		return fmt.Sprintf("写入新版本文件失败（%s）：%s", fe.dir, sanitizeUpdateErr(fe.err.Error()))
	}

	if errors.Is(err, errBadChecksum) {
		return "下载的文件校验失败（SHA-256 不匹配），请重试"
	}

	var he *httpStatusError
	if errors.As(err, &he) {
		switch {
		case he.statusCode == http.StatusNotFound:
			return fmt.Sprintf("Release 中未找到 %s（HTTP 404），版本可能仍在发布中，请稍后重试", he.file)
		case he.statusCode == http.StatusForbidden || he.statusCode == http.StatusTooManyRequests:
			return fmt.Sprintf("访问 GitHub 被限流或拒绝（HTTP %d），请稍后重试", he.statusCode)
		case he.statusCode >= 500:
			return fmt.Sprintf("GitHub 下载服务暂时异常（HTTP %d，已自动重试），请稍后重试", he.statusCode)
		default:
			return fmt.Sprintf("下载 %s 失败（%s）", he.file, he.status)
		}
	}

	if isProxyErr(err, usedProxy) {
		return "无法连接代理服务器，请检查 HTTP_PROXY / HTTPS_PROXY 配置或网络后重试"
	}

	var dnsErr *net.DNSError
	if errors.As(err, &dnsErr) {
		if usedProxy {
			return "代理或域名解析失败（DNS 错误），请检查 HTTP_PROXY / HTTPS_PROXY 配置后重试"
		}
		return "无法解析 GitHub 域名（DNS 失败），请检查网络或配置 HTTPS_PROXY 后重试"
	}

	low := strings.ToLower(err.Error())
	if (strings.Contains(low, "x509:") || strings.Contains(low, "tls:")) && !isTimeoutErr(err) {
		return "访问 GitHub TLS 握手失败（证书或网络拦截），请检查代理或系统证书后重试"
	}

	if isTimeoutErr(err) {
		switch {
		case usedProxy:
			return "经代理访问 GitHub 超时（已自动重试），请检查代理状态或网络后重试"
		case triedIPv4:
			return "连接 GitHub 超时（已自动重试并尝试 IPv4），请检查网络或配置 HTTPS_PROXY 后重试"
		default:
			return "连接 GitHub 超时（已自动重试），请检查网络或配置 HTTPS_PROXY 后重试"
		}
	}

	if isConnResetOrEOF(err) {
		switch {
		case usedProxy:
			return "经代理下载中断（连接被重置或提前断开，已自动重试），请检查代理或网络后重试"
		case triedIPv4:
			return "连接 GitHub 中断（连接被重置或提前断开，已自动重试并尝试 IPv4），请检查网络或配置 HTTPS_PROXY 后重试"
		default:
			return "连接 GitHub 中断（连接被重置或提前断开，已自动重试），请检查网络或代理后重试"
		}
	}

	if errors.Is(err, syscall.ECONNREFUSED) {
		return "连接 GitHub 被拒绝，请检查网络或代理设置后重试"
	}

	return sanitizeUpdateErr(err.Error())
}

// idleTimeoutReader cancels the attempt context if no bytes arrive within timeout.
type idleTimeoutReader struct {
	r        io.ReadCloser
	timeout  time.Duration
	cancel   context.CancelFunc
	mu       sync.Mutex
	closed   bool
	timer    *time.Timer
	timedOut atomic.Bool
}

func newIdleTimeoutReader(rc io.ReadCloser, timeout time.Duration, cancel context.CancelFunc) io.ReadCloser {
	if timeout <= 0 {
		return rc
	}
	ir := &idleTimeoutReader{r: rc, timeout: timeout, cancel: cancel}
	ir.timer = time.AfterFunc(timeout, func() {
		ir.timedOut.Store(true)
		cancel()
	})
	return ir
}

func (ir *idleTimeoutReader) Read(p []byte) (int, error) {
	n, err := ir.r.Read(p)
	if n > 0 {
		ir.mu.Lock()
		if !ir.closed && ir.timer != nil {
			ir.timer.Reset(ir.timeout)
		}
		ir.mu.Unlock()
	}
	if err != nil && ir.timedOut.Load() {
		return n, fmt.Errorf("%w: %v", errIdleReadTimeout, err)
	}
	return n, err
}

func (ir *idleTimeoutReader) Close() error {
	ir.mu.Lock()
	ir.closed = true
	if ir.timer != nil {
		ir.timer.Stop()
	}
	ir.mu.Unlock()
	return ir.r.Close()
}

var latestRel struct {
	sync.Mutex
	once    sync.Once
	tag     string
	checked time.Time
	busy    bool
}

// latestVersion returns the newest release tag seen so far ("" until known).
// It checks every hour; soon asks for another check in the background when the
// last one is more than a few minutes old (the devices page, where updates are
// looked at).
func latestVersion(soon bool) string {
	latestRel.once.Do(func() {
		go func() {
			for {
				refreshLatest()
				time.Sleep(time.Hour)
			}
		}()
	})
	latestRel.Lock()
	defer latestRel.Unlock()
	if soon && !latestRel.busy && !latestRel.checked.IsZero() && time.Since(latestRel.checked) > 10*time.Minute {
		go refreshLatest()
	}
	return latestRel.tag
}

func refreshLatest() {
	latestRel.Lock()
	if latestRel.busy {
		latestRel.Unlock()
		return
	}
	latestRel.busy = true
	latestRel.Unlock()

	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	t, _, err := fetchLatestTagErr(ctx)
	cancel()
	if err != nil {
		log.Printf("update check latest failed: %s", sanitizeUpdateErr(err.Error()))
	}

	latestRel.Lock()
	defer latestRel.Unlock()
	latestRel.busy, latestRel.checked = false, time.Now()
	if t != "" {
		latestRel.tag = t
	}
}

func fetchLatestTag() string {
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	t, _, _ := fetchLatestTagErr(ctx)
	return t
}

func fetchLatestTagErr(ctx context.Context) (string, bool, error) {
	maxAttempts := max(1, updateMaxAttempts)
	forceIPv4 := false
	triedIPv4 := false
	var lastErr error
	lastUsesProxy := false

	for attempt := 0; attempt < maxAttempts; attempt++ {
		if forceIPv4 {
			triedIPv4 = true
		}
		tag, usedProxy, err := fetchLatestTagOnce(ctx, forceIPv4)
		lastUsesProxy = usedProxy
		if err == nil {
			return tag, forceIPv4, nil
		}
		lastErr = err
		if attempt+1 >= maxAttempts || !isRetryableUpdateErr(ctx, err) {
			break
		}
		nextIPv4 := forceIPv4
		if !forceIPv4 && shouldFallbackIPv4(err, usedProxy) {
			nextIPv4 = true
		} else if forceIPv4 && isIPv4Unreachable(err) {
			nextIPv4 = false
		}
		log.Printf("update check latest retry %d/%d (ipv4=%v proxy=%v): %s", attempt+1, maxAttempts, nextIPv4, usedProxy, sanitizeUpdateErr(err.Error()))
		forceIPv4 = nextIPv4
		if berr := sleepBackoff(ctx, attempt); berr != nil {
			lastErr = berr
			break
		}
	}
	return "", triedIPv4, &updateOpError{err: lastErr, usedProxy: lastUsesProxy, triedIPv4: triedIPv4}
}

func fetchLatestTagOnce(parentCtx context.Context, forceIPv4 bool) (string, bool, error) {
	ctx, cancel := context.WithTimeout(parentCtx, updateTagAttemptTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, "HEAD", releaseRepoURL+"/releases/latest", nil)
	if err != nil {
		return "", false, err
	}
	usedProxy := requestUsesProxy(req)
	tr := newUpdateTransport(forceIPv4)
	defer tr.CloseIdleConnections()
	c := &http.Client{
		Transport:     tr,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	resp, err := c.Do(req)
	if err != nil {
		return "", usedProxy, err
	}
	resp.Body.Close()
	if resp.StatusCode >= 400 {
		return "", usedProxy, &httpStatusError{file: "releases/latest", statusCode: resp.StatusCode, status: resp.Status}
	}
	loc := resp.Header.Get("Location")
	i := strings.LastIndex(loc, "/tag/")
	if i < 0 {
		return "", usedProxy, fmt.Errorf("未从 github.com 获取到有效版本重定向（HTTP %d）", resp.StatusCode)
	}
	if t := loc[i+5:]; parseVersion(t) != nil {
		return t, usedProxy, nil
	}
	return "", usedProxy, fmt.Errorf("最新版本标签格式异常（%s）", loc[i+5:])
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

type versionMachine struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Version  string `json:"version,omitempty"`
	Local    bool   `json:"local"`
	Online   bool   `json:"online"`
	Outdated bool   `json:"outdated"`
}

func (s *Server) getVersion(w http.ResponseWriter, r *http.Request) {
	latest := latestVersion(r.URL.Query().Get("refresh") == "1")
	var old []string
	machines := []versionMachine{}
	if s.herdr != nil {
		for _, m := range s.herdr.Machines() {
			v := m.Version
			if m.Local {
				v = version
			}
			online := m.Local || m.Online
			isOutdated := online && outdated(v, latest)
			if isOutdated {
				old = append(old, m.Name)
			}
			machines = append(machines, versionMachine{
				ID:       m.ID,
				Name:     m.Name,
				Version:  v,
				Local:    m.Local,
				Online:   online,
				Outdated: isOutdated,
			})
		}
	}
	writeJSON(w, 200, map[string]any{"version": version, "latest": latest, "outdated": old, "machines": machines})
}

// selfUpdate replaces this computer's collie with the latest release and
// restarts it in place (same process id, so launchd, systemd or nohup keep
// tracking it). herdr and the agents in it keep running meanwhile.
func (s *Server) selfUpdate(w http.ResponseWriter, r *http.Request) {
	start := time.Now()
	if !selfUpdateMu.TryLock() {
		log.Printf("self-update rejected (current=%s): already in progress", version)
		writeJSON(w, 409, map[string]any{"error": "升级已在进行中，请稍候"})
		return
	}
	restarting := false
	defer func() {
		if !restarting {
			selfUpdateMu.Unlock()
		}
	}()
	if parseVersion(version) == nil {
		log.Printf("self-update rejected (current=%s): source build", version)
		writeJSON(w, 400, map[string]any{"error": "这台的 collie 是从源码编译的（" + version + "），在源码里 git pull 后重新编译来更新"})
		return
	}
	log.Printf("self-update start (current=%s)", version)
	// Shield the download from transient client disconnects or short browser
	// timeouts while still bounding total update runtime.
	ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), selfUpdateTimeout)
	defer cancel()

	latest, usedIPv4, err := fetchLatestTagErr(ctx)
	if err != nil || latest == "" {
		msg := formatUpdateError(err)
		if msg == "" {
			msg = "连不上 github.com，请检查网络或代理设置后重试"
		}
		log.Printf("self-update failed (stage=check-latest current=%s dur=%s): %s", version, time.Since(start).Round(time.Millisecond), sanitizeUpdateErr(fmt.Sprint(err)))
		writeJSON(w, 502, map[string]any{"error": "查不到最新版本：" + msg})
		return
	}
	if !outdated(version, latest) {
		log.Printf("self-update noop (current=%s latest=%s dur=%s): already up to date", version, latest, time.Since(start).Round(time.Millisecond))
		writeJSON(w, 200, map[string]any{"ok": true, "version": version, "latest": true})
		return
	}
	exe, err := executablePath()
	if err != nil {
		log.Printf("self-update failed (stage=exe current=%s target=%s dur=%s): %v", version, latest, time.Since(start).Round(time.Millisecond), err)
		writeJSON(w, 500, map[string]any{"error": "找不到自己的程序文件：" + err.Error()})
		return
	}
	if err := downloadReleaseWithHint(ctx, latest, exe, usedIPv4); err != nil {
		msg := formatUpdateError(err)
		log.Printf("self-update failed (stage=download current=%s target=%s dur=%s): %s", version, latest, time.Since(start).Round(time.Millisecond), sanitizeUpdateErr(err.Error()))
		writeJSON(w, 502, map[string]any{"error": "更新失败：" + msg})
		return
	}
	log.Printf("updated %s → %s (dur=%s); restarting", version, latest, time.Since(start).Round(time.Millisecond))
	writeJSON(w, 200, map[string]any{"ok": true, "from": version, "version": latest})
	if f, ok := w.(http.Flusher); ok {
		f.Flush()
	}
	restarting = true
	go execUpdated(exe)
}

// downloadRelease puts release tag's binary for this platform at exe,
// checked against the release's checksums and by running it once.
func downloadRelease(ctx context.Context, tag, exe string) error {
	return downloadReleaseWithHint(ctx, tag, exe, false)
}

func downloadReleaseWithHint(ctx context.Context, tag, exe string, preferIPv4 bool) error {
	dir := filepath.Dir(exe)
	if probe, err := os.CreateTemp(dir, ".collie.new.*"); err != nil {
		return &updateFileError{dir: dir, err: err}
	} else {
		p := probe.Name()
		probe.Close()
		os.Remove(p)
	}

	asset := "collie-" + runtime.GOOS + "-" + runtime.GOARCH
	base := releaseRepoURL + "/releases/download/" + tag + "/"

	want, usedIPv4, err := downloadChecksums(ctx, base, tag, asset, preferIPv4)
	if err != nil {
		return err
	}
	tmp, err := downloadAsset(ctx, base, asset, exe, want, usedIPv4)
	if err != nil {
		return err
	}
	defer func() {
		if tmp != "" {
			os.Remove(tmp)
		}
	}()

	verifyCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	out, rerr := exec.CommandContext(verifyCtx, tmp, "version").Output()
	cancel()
	if rerr != nil || !strings.Contains(string(out), tag) {
		return fmt.Errorf("新版本在这台电脑上跑不起来：%v %s", rerr, strings.TrimSpace(string(out)))
	}
	if err := os.Rename(tmp, exe); err != nil {
		return &updateFileError{dir: dir, err: err}
	}
	tmp = ""
	return nil
}

func downloadChecksums(ctx context.Context, base, tag, asset string, preferIPv4 bool) (string, bool, error) {
	maxAttempts := max(1, updateMaxAttempts)
	forceIPv4 := preferIPv4
	triedIPv4 := preferIPv4
	var lastErr error
	lastUsesProxy := false

	for attempt := 0; attempt < maxAttempts; attempt++ {
		if forceIPv4 {
			triedIPv4 = true
		}
		data, usedProxy, err := fetchSmallFileOnce(ctx, base+"checksums.txt", "checksums.txt", forceIPv4)
		lastUsesProxy = usedProxy
		if err == nil {
			want := ""
			sc := bufio.NewScanner(bytes.NewReader(data))
			for sc.Scan() {
				if f := strings.Fields(sc.Text()); len(f) == 2 && f[1] == asset {
					want = f[0]
				}
			}
			if want == "" {
				return "", forceIPv4, fmt.Errorf("%s 没有 %s 的版本", tag, asset)
			}
			return want, forceIPv4, nil
		}
		lastErr = err
		if attempt+1 >= maxAttempts || !isRetryableUpdateErr(ctx, err) {
			break
		}
		nextIPv4 := forceIPv4
		if !forceIPv4 && shouldFallbackIPv4(err, usedProxy) {
			nextIPv4 = true
		} else if forceIPv4 && isIPv4Unreachable(err) {
			nextIPv4 = false
		}
		log.Printf("update download checksums.txt retry %d/%d (ipv4=%v proxy=%v): %s", attempt+1, maxAttempts, nextIPv4, usedProxy, sanitizeUpdateErr(err.Error()))
		forceIPv4 = nextIPv4
		if berr := sleepBackoff(ctx, attempt); berr != nil {
			lastErr = berr
			break
		}
	}
	return "", triedIPv4, &updateOpError{err: lastErr, usedProxy: lastUsesProxy, triedIPv4: triedIPv4}
}

func fetchSmallFileOnce(parentCtx context.Context, targetURL, name string, forceIPv4 bool) ([]byte, bool, error) {
	ctx, cancel := context.WithTimeout(parentCtx, updateFileAttemptTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, "GET", targetURL, nil)
	if err != nil {
		return nil, false, err
	}
	usedProxy := requestUsesProxy(req)
	tr := newUpdateTransport(forceIPv4)
	defer tr.CloseIdleConnections()
	c := &http.Client{
		Transport: tr,
		CheckRedirect: func(next *http.Request, via []*http.Request) error {
			if len(via) >= 10 {
				return errors.New("stopped after 10 redirects")
			}
			usedProxy = requestUsesProxy(next)
			return nil
		},
	}
	resp, err := c.Do(req)
	if err != nil {
		return nil, usedProxy, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, usedProxy, &httpStatusError{file: name, statusCode: resp.StatusCode, status: resp.Status}
	}
	body := newIdleTimeoutReader(resp.Body, updateIdleReadTimeout, cancel)
	defer body.Close()
	data, err := io.ReadAll(io.LimitReader(body, 1<<20))
	if err != nil {
		return nil, usedProxy, err
	}
	if resp.ContentLength > 0 && int64(len(data)) != resp.ContentLength {
		return nil, usedProxy, fmt.Errorf("%w: got %d of %d bytes", errTruncatedBody, len(data), resp.ContentLength)
	}
	return data, usedProxy, nil
}

func downloadAsset(ctx context.Context, base, asset, exe, wantSHA string, preferIPv4 bool) (string, error) {
	maxAttempts := max(1, updateMaxAttempts)
	forceIPv4 := preferIPv4
	triedIPv4 := preferIPv4
	var lastErr error
	lastUsesProxy := false

	for attempt := 0; attempt < maxAttempts; attempt++ {
		if forceIPv4 {
			triedIPv4 = true
		}
		tmp, usedProxy, err := downloadAssetOnce(ctx, base+asset, asset, exe, wantSHA, forceIPv4)
		lastUsesProxy = usedProxy
		if err == nil {
			return tmp, nil
		}
		lastErr = err
		if attempt+1 >= maxAttempts || !isRetryableUpdateErr(ctx, err) {
			break
		}
		nextIPv4 := forceIPv4
		if !forceIPv4 && shouldFallbackIPv4(err, usedProxy) {
			nextIPv4 = true
		} else if forceIPv4 && isIPv4Unreachable(err) {
			nextIPv4 = false
		}
		log.Printf("update download %s retry %d/%d (ipv4=%v proxy=%v): %s", asset, attempt+1, maxAttempts, nextIPv4, usedProxy, sanitizeUpdateErr(err.Error()))
		forceIPv4 = nextIPv4
		if berr := sleepBackoff(ctx, attempt); berr != nil {
			lastErr = berr
			break
		}
	}
	var fe *updateFileError
	if errors.As(lastErr, &fe) || errors.Is(lastErr, errBadChecksum) {
		return "", lastErr
	}
	return "", &updateOpError{err: lastErr, usedProxy: lastUsesProxy, triedIPv4: triedIPv4}
}

func downloadAssetOnce(parentCtx context.Context, targetURL, asset, exe, wantSHA string, forceIPv4 bool) (string, bool, error) {
	ctx, cancel := context.WithTimeout(parentCtx, updateBinAttemptTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, "GET", targetURL, nil)
	if err != nil {
		return "", false, err
	}
	usedProxy := requestUsesProxy(req)
	tr := newUpdateTransport(forceIPv4)
	defer tr.CloseIdleConnections()
	c := &http.Client{
		Transport: tr,
		CheckRedirect: func(next *http.Request, via []*http.Request) error {
			if len(via) >= 10 {
				return errors.New("stopped after 10 redirects")
			}
			usedProxy = requestUsesProxy(next)
			return nil
		},
	}
	resp, err := c.Do(req)
	if err != nil {
		return "", usedProxy, err
	}
	body := newIdleTimeoutReader(resp.Body, updateIdleReadTimeout, cancel)
	defer body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", usedProxy, &httpStatusError{file: asset, statusCode: resp.StatusCode, status: resp.Status}
	}

	dir := filepath.Dir(exe)
	f, err := os.CreateTemp(dir, ".collie.new.*")
	if err != nil {
		return "", usedProxy, &updateFileError{dir: dir, err: err}
	}
	tmp := f.Name()
	defer func() {
		if tmp != "" {
			os.Remove(tmp)
		}
	}()

	h := sha256.New()
	n, copyErr := io.Copy(io.MultiWriter(f, h), body)
	chmodErr := f.Chmod(0o755)
	closeErr := f.Close()
	if copyErr != nil {
		return "", usedProxy, copyErr
	}
	if resp.ContentLength > 0 && n != resp.ContentLength {
		return "", usedProxy, fmt.Errorf("%w: got %d of %d bytes", errTruncatedBody, n, resp.ContentLength)
	}
	if chmodErr != nil {
		return "", usedProxy, &updateFileError{dir: dir, err: chmodErr}
	}
	if closeErr != nil {
		return "", usedProxy, &updateFileError{dir: dir, err: closeErr}
	}
	if hex.EncodeToString(h.Sum(nil)) != wantSHA {
		return "", usedProxy, errBadChecksum
	}
	out := tmp
	tmp = ""
	return out, usedProxy, nil
}
