package collie

import (
	"bufio"
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

const (
	repo                 = "lihuanyu0w0/collie"
	relayUpdateTagHeader = "X-Collie-Update-Tag"
	relayUpdateSHAHeader = "X-Collie-Update-SHA256"
)

var (
	selfUpdateMu             sync.Mutex
	releaseRepoURL           = "https://github.com/" + repo
	selfUpdateTimeout        = 2*time.Minute + 30*time.Second
	selfUpdateForwardTimeout = 2*time.Minute + 50*time.Second
	selfUpdateRelayTimeout   = 2*time.Minute + 30*time.Second
	maxRelayBinarySize       = int64(128 << 20)
	relayCacheTTL            = 15 * time.Minute
	relayCacheMaxEntries     = 8
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
	errBadChecksum      = errors.New("checksum mismatch")
	errIdleReadTimeout  = errors.New("download stalled (read timeout)")
	errTruncatedBody    = errors.New("incomplete response body")
	errExecVerifyFailed = errors.New("updated binary verification failed")
	credURLRe           = regexp.MustCompile(`(https?|socks5h?)://[^/@\s]+@`)
	platformPartRe      = regexp.MustCompile(`^[a-z0-9_]{1,32}$`)
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
		writeJSON(w, 502, map[string]any{"error": "查不到最新版本：" + msg, "relayable": true})
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
		var fe *updateFileError
		relayable := !errors.As(err, &fe) && !errors.Is(err, errExecVerifyFailed)
		log.Printf("self-update failed (stage=download current=%s target=%s dur=%s): %s", version, latest, time.Since(start).Round(time.Millisecond), sanitizeUpdateErr(err.Error()))
		writeJSON(w, 502, map[string]any{"error": "更新失败：" + msg, "relayable": relayable})
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

func isValidSHA256Hex(s string) bool {
	if len(s) != 64 {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f')) {
			return false
		}
	}
	return true
}

func parseMachineOSArch(raw string) (goos, goarch string, ok bool) {
	raw = strings.TrimSpace(strings.ToLower(raw))
	parts := strings.Split(raw, "/")
	if len(parts) != 2 {
		return "", "", false
	}
	if !platformPartRe.MatchString(parts[0]) || !platformPartRe.MatchString(parts[1]) {
		return "", "", false
	}
	return parts[0], parts[1], true
}

type updateExecVerifyError struct {
	msg string
}

func (e *updateExecVerifyError) Error() string { return e.msg }
func (e *updateExecVerifyError) Unwrap() error { return errExecVerifyFailed }

func verifyBinaryVersion(ctx context.Context, binPath, tag string) error {
	verifyCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	out, rerr := exec.CommandContext(verifyCtx, binPath, "version").Output()
	cancel()
	if rerr != nil || !strings.Contains(string(out), tag) {
		return &updateExecVerifyError{
			msg: fmt.Sprintf("新版本在这台电脑上跑不起来：%v %s", rerr, strings.TrimSpace(string(out))),
		}
	}
	return nil
}

// relayUpdate runs on a remote node inside the authenticated Hub↔Node tunnel.
// It receives a Hub-fetched binary, verifies its SHA-256 and version output,
// atomically replaces the current executable (0755), and restarts in place.
func (s *Server) relayUpdate(w http.ResponseWriter, r *http.Request) {
	start := time.Now()
	if !s.node {
		writeJSON(w, 403, map[string]any{"error": "仅允许节点通过控制通道接收 Hub 中转升级"})
		return
	}
	if !selfUpdateMu.TryLock() {
		log.Printf("relay-update rejected (current=%s): already in progress", version)
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
		log.Printf("relay-update rejected (current=%s): source build", version)
		writeJSON(w, 400, map[string]any{"error": "这台的 collie 是从源码编译的（" + version + "），在源码里 git pull 后重新编译来更新"})
		return
	}
	tag := strings.TrimSpace(r.Header.Get(relayUpdateTagHeader))
	wantSHA := strings.ToLower(strings.TrimSpace(r.Header.Get(relayUpdateSHAHeader)))
	if parseVersion(tag) == nil {
		writeJSON(w, 400, map[string]any{"error": "中转升级目标版本号无效"})
		return
	}
	if !isValidSHA256Hex(wantSHA) {
		writeJSON(w, 400, map[string]any{"error": "中转升级缺少有效的 SHA-256 校验值"})
		return
	}
	if !outdated(version, tag) {
		_, _ = io.Copy(io.Discard, io.LimitReader(r.Body, 1<<20))
		log.Printf("relay-update noop (current=%s target=%s dur=%s): already up to date", version, tag, time.Since(start).Round(time.Millisecond))
		writeJSON(w, 200, map[string]any{"ok": true, "version": version, "latest": true, "relayed": true})
		return
	}
	exe, err := executablePath()
	if err != nil {
		log.Printf("relay-update failed (stage=exe current=%s target=%s dur=%s): %v", version, tag, time.Since(start).Round(time.Millisecond), err)
		writeJSON(w, 500, map[string]any{"error": "找不到自己的程序文件：" + err.Error()})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), selfUpdateTimeout)
	defer cancel()
	if err := installRelayedBinary(ctx, r.Body, r.ContentLength, tag, wantSHA, exe); err != nil {
		status := 502
		if errors.Is(err, errBadChecksum) {
			status = 422
		}
		msg := formatUpdateError(err)
		if errors.Is(err, errBadChecksum) {
			msg = "中转升级文件校验失败（SHA-256 不匹配），已拒绝安装"
		}
		log.Printf("relay-update failed (stage=install current=%s target=%s dur=%s): %s", version, tag, time.Since(start).Round(time.Millisecond), sanitizeUpdateErr(err.Error()))
		writeJSON(w, status, map[string]any{"error": "中转升级失败：" + msg})
		return
	}
	log.Printf("relay-updated %s → %s (dur=%s); restarting", version, tag, time.Since(start).Round(time.Millisecond))
	writeJSON(w, 200, map[string]any{"ok": true, "from": version, "version": tag, "relayed": true})
	if f, ok := w.(http.Flusher); ok {
		f.Flush()
	}
	restarting = true
	go execUpdated(exe)
}

func installRelayedBinary(ctx context.Context, body io.Reader, expectedLen int64, tag, wantSHA, exe string) error {
	dir := filepath.Dir(exe)
	f, err := os.CreateTemp(dir, ".collie.new.*")
	if err != nil {
		return &updateFileError{dir: dir, err: err}
	}
	tmp := f.Name()
	defer func() {
		if tmp != "" {
			os.Remove(tmp)
		}
	}()

	limit := maxRelayBinarySize
	if limit <= 0 {
		limit = 128 << 20
	}
	h := sha256.New()
	n, copyErr := io.Copy(io.MultiWriter(f, h), io.LimitReader(body, limit+1))
	chmodErr := f.Chmod(0o755)
	closeErr := f.Close()
	if copyErr != nil {
		return copyErr
	}
	if n == 0 {
		return fmt.Errorf("%w: received empty binary", errTruncatedBody)
	}
	if n > limit {
		return fmt.Errorf("升级包体积超出上限（%d MB）", limit>>20)
	}
	if expectedLen > 0 && n != expectedLen {
		return fmt.Errorf("%w: got %d of %d bytes", errTruncatedBody, n, expectedLen)
	}
	if chmodErr != nil {
		return &updateFileError{dir: dir, err: chmodErr}
	}
	if closeErr != nil {
		return &updateFileError{dir: dir, err: closeErr}
	}
	if hex.EncodeToString(h.Sum(nil)) != wantSHA {
		return errBadChecksum
	}
	if err := verifyBinaryVersion(ctx, tmp, tag); err != nil {
		return err
	}
	if err := os.Rename(tmp, exe); err != nil {
		return &updateFileError{dir: dir, err: err}
	}
	tmp = ""
	return nil
}

// ---------------------------------------------------------------- Hub-side remote update & relay fallback

type relayCacheKey struct {
	repo   string
	tag    string
	goos   string
	goarch string
}

type relayCacheEntry struct {
	asset     string
	sha256    string
	size      int64
	path      string
	expiresAt time.Time
}

type relayInflight struct {
	done chan struct{}
	ent  relayCacheEntry
	err  error
}

var relayCache = struct {
	mu       sync.Mutex
	dir      string
	entries  map[relayCacheKey]relayCacheEntry
	inflight map[relayCacheKey]*relayInflight
}{
	entries:  map[relayCacheKey]relayCacheEntry{},
	inflight: map[relayCacheKey]*relayInflight{},
}

func resetRelayCache() {
	relayCache.mu.Lock()
	defer relayCache.mu.Unlock()
	for k, ent := range relayCache.entries {
		_ = os.Remove(ent.path)
		delete(relayCache.entries, k)
	}
	if relayCache.dir != "" {
		_ = os.RemoveAll(relayCache.dir)
		relayCache.dir = ""
	}
}

func pruneRelayCacheLocked(now time.Time) {
	for k, ent := range relayCache.entries {
		if now.After(ent.expiresAt) {
			_ = os.Remove(ent.path)
			delete(relayCache.entries, k)
		}
	}
}

func evictOldestRelayCacheLocked(maxEntries int) {
	if maxEntries <= 0 {
		maxEntries = 1
	}
	for len(relayCache.entries) >= maxEntries {
		var oldestKey relayCacheKey
		var oldestExp time.Time
		first := true
		for k, ent := range relayCache.entries {
			if first || ent.expiresAt.Before(oldestExp) {
				oldestKey = k
				oldestExp = ent.expiresAt
				first = false
			}
		}
		if first {
			break
		}
		_ = os.Remove(relayCache.entries[oldestKey].path)
		delete(relayCache.entries, oldestKey)
	}
}

// getOrFetchRelayAsset returns a SHA-256-verified release binary for goos/goarch
// from the Hub's short-term temp cache, downloading from GitHub on cache miss.
func getOrFetchRelayAsset(ctx context.Context, tag, goos, goarch string, preferIPv4 bool) (relayCacheEntry, *os.File, bool, error) {
	key := relayCacheKey{repo: releaseRepoURL, tag: tag, goos: goos, goarch: goarch}

	for attempt := 0; attempt < 2; attempt++ {
		relayCache.mu.Lock()
		now := time.Now()
		pruneRelayCacheLocked(now)

		if ent, ok := relayCache.entries[key]; ok {
			f, err := os.Open(ent.path)
			if err == nil {
				if fi, serr := f.Stat(); serr == nil && fi.Size() == ent.size {
					relayCache.mu.Unlock()
					return ent, f, true, nil
				}
				f.Close()
			}
			_ = os.Remove(ent.path)
			delete(relayCache.entries, key)
		}

		if wait, ok := relayCache.inflight[key]; ok {
			relayCache.mu.Unlock()
			select {
			case <-wait.done:
			case <-ctx.Done():
				return relayCacheEntry{}, nil, false, ctx.Err()
			}
			if wait.err != nil {
				return relayCacheEntry{}, nil, false, wait.err
			}
			continue
		}

		if relayCache.dir == "" {
			dir, err := os.MkdirTemp("", "collie-relay-*")
			if err != nil {
				relayCache.mu.Unlock()
				return relayCacheEntry{}, nil, false, err
			}
			relayCache.dir = dir
		}
		dir := relayCache.dir
		inf := &relayInflight{done: make(chan struct{})}
		relayCache.inflight[key] = inf
		relayCache.mu.Unlock()

		ent, f, err := fetchRelayAssetFresh(ctx, dir, tag, goos, goarch, preferIPv4)

		relayCache.mu.Lock()
		delete(relayCache.inflight, key)
		if err != nil {
			inf.err = err
			close(inf.done)
			relayCache.mu.Unlock()
			return relayCacheEntry{}, nil, false, err
		}
		ttl := relayCacheTTL
		if ttl > 0 {
			evictOldestRelayCacheLocked(relayCacheMaxEntries)
			ent.expiresAt = time.Now().Add(ttl)
			if prev, exists := relayCache.entries[key]; exists && prev.path != ent.path {
				_ = os.Remove(prev.path)
			}
			relayCache.entries[key] = ent
		} else {
			// Caching disabled: remove path immediately while caller keeps open fd.
			_ = os.Remove(ent.path)
		}
		inf.ent = ent
		close(inf.done)
		relayCache.mu.Unlock()
		return ent, f, false, nil
	}
	return relayCacheEntry{}, nil, false, errors.New("relay cache unavailable")
}

func fetchRelayAssetFresh(ctx context.Context, cacheDir, tag, goos, goarch string, preferIPv4 bool) (relayCacheEntry, *os.File, error) {
	asset := "collie-" + goos + "-" + goarch
	base := releaseRepoURL + "/releases/download/" + tag + "/"
	wantSHA, usedIPv4, err := downloadChecksums(ctx, base, tag, asset, preferIPv4)
	if err != nil {
		return relayCacheEntry{}, nil, err
	}
	tmpPath, err := downloadAssetToDir(ctx, base, asset, cacheDir, wantSHA, usedIPv4, 0o600)
	if err != nil {
		return relayCacheEntry{}, nil, err
	}
	f, err := os.Open(tmpPath)
	if err != nil {
		_ = os.Remove(tmpPath)
		return relayCacheEntry{}, nil, err
	}
	fi, err := f.Stat()
	if err != nil {
		f.Close()
		_ = os.Remove(tmpPath)
		return relayCacheEntry{}, nil, err
	}
	return relayCacheEntry{
		asset:  asset,
		sha256: wantSHA,
		size:   fi.Size(),
		path:   tmpPath,
	}, f, nil
}

func (s *Server) machineOS(m Machine) string {
	if strings.TrimSpace(m.OS) != "" {
		return m.OS
	}
	if s.auths != nil {
		for _, d := range s.auths.list() {
			if d.ID == m.ID && d.Kind == "machine" && strings.TrimSpace(d.OS) != "" {
				return d.OS
			}
		}
	}
	return ""
}

func isLocalNodeUpdateError(msg string) bool {
	for _, needle := range []string{
		"无写权限",
		"磁盘空间不足",
		"写入新版本文件失败",
		"跑不起来",
		"源码编译",
		"升级已在进行中",
		"找不到自己的程序文件",
	} {
		if strings.Contains(msg, needle) {
			return true
		}
	}
	return false
}

func shouldTriggerHubRelay(statusCode int, errMsg string, relayable *bool) bool {
	if statusCode != http.StatusBadGateway {
		return false
	}
	if relayable != nil {
		return *relayable
	}
	return !isLocalNodeUpdateError(errMsg)
}

func (s *Server) updateRemote(w http.ResponseWriter, r *http.Request, machine string) {
	start := time.Now()
	log.Printf("remote-update start (machine=%s)", machine)
	if s.herdr == nil {
		log.Printf("remote-update failed (machine=%s dur=%s): node offline", machine, time.Since(start).Round(time.Millisecond))
		writeJSON(w, 503, map[string]any{"error": "这台电脑现在不在线（" + machine + "）"})
		return
	}
	m, client, err := s.herdr.MachineInfo(machine)
	if err != nil {
		log.Printf("remote-update failed (machine=%s dur=%s): node offline", machine, time.Since(start).Round(time.Millisecond))
		writeJSON(w, 503, map[string]any{"error": "这台电脑现在不在线（" + machine + "）"})
		return
	}

	reqCtx := r.Context()
	if selfUpdateForwardTimeout > 0 {
		var cancel context.CancelFunc
		reqCtx, cancel = context.WithTimeout(reqCtx, selfUpdateForwardTimeout)
		defer cancel()
	}

	q := r.URL.Query()
	q.Del("machine")
	nodeURL := "http://node/api/update"
	if enc := q.Encode(); enc != "" {
		nodeURL += "?" + enc
	}
	outReq, err := http.NewRequestWithContext(reqCtx, http.MethodPost, nodeURL, nil)
	if err != nil {
		writeJSON(w, 502, map[string]any{"error": "转发到 " + machine + " 失败：" + sanitizeUpdateErr(err.Error())})
		return
	}

	resp, err := client.Do(outReq)
	if err != nil {
		dur := time.Since(start).Round(time.Millisecond)
		log.Printf("remote-update failed (machine=%s dur=%s): %s", machine, dur, sanitizeUpdateErr(err.Error()))
		msg := "转发到 " + machine + " 失败：" + sanitizeUpdateErr(err.Error())
		switch {
		case isTimeoutErr(err):
			msg = "转发到 " + machine + " 更新超时：节点未在规定时间内返回，可能下载较慢或网络卡顿，请稍后刷新查看版本"
		case isConnResetOrEOF(err):
			msg = "转发到 " + machine + " 时连接中断：节点可能正在重启或暂时掉线，请稍候刷新查看版本；若未升级可重试"
		}
		writeJSON(w, 502, map[string]any{"error": msg})
		return
	}
	rawBody, readErr := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	resp.Body.Close()
	if readErr != nil {
		dur := time.Since(start).Round(time.Millisecond)
		log.Printf("remote-update failed (machine=%s dur=%s): %s", machine, dur, sanitizeUpdateErr(readErr.Error()))
		writeJSON(w, 502, map[string]any{"error": "转发到 " + machine + " 时连接中断：节点可能正在重启或暂时掉线，请稍候刷新查看版本；若未升级可重试"})
		return
	}

	dur := time.Since(start).Round(time.Millisecond)
	isJSON := strings.HasPrefix(resp.Header.Get("Content-Type"), "application/json")
	if resp.StatusCode == http.StatusOK {
		if isJSON {
			var res struct {
				OK      bool   `json:"ok"`
				From    string `json:"from"`
				Version string `json:"version"`
				Latest  bool   `json:"latest"`
			}
			_ = json.Unmarshal(rawBody, &res)
			log.Printf("remote-update ok (machine=%s from=%s version=%s latest=%v dur=%s)", machine, res.From, res.Version, res.Latest, dur)
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write(rawBody)
			return
		}
		writeJSON(w, 200, map[string]any{"ok": true})
		return
	}

	var res struct {
		Error     string `json:"error"`
		Relayable *bool  `json:"relayable"`
	}
	errMsg := ""
	if isJSON && json.Unmarshal(rawBody, &res) == nil && strings.TrimSpace(res.Error) != "" {
		errMsg = sanitizeUpdateErr(strings.TrimSpace(res.Error))
	} else {
		msg := strings.TrimSpace(string(rawBody))
		if msg == "" {
			msg = resp.Status
		}
		errMsg = "节点 " + machine + " 返回异常：" + sanitizeUpdateErr(msg)
	}
	log.Printf("remote-update failed (machine=%s status=%d dur=%s): %s", machine, resp.StatusCode, dur, errMsg)

	goos, goarch, hasOS := parseMachineOSArch(s.machineOS(m))
	if hasOS && shouldTriggerHubRelay(resp.StatusCode, errMsg, res.Relayable) {
		s.relayRemoteUpdate(w, r, m, client, goos, goarch, errMsg, start)
		return
	}

	writeJSON(w, resp.StatusCode, map[string]any{"error": errMsg})
}

func (s *Server) relayRemoteUpdate(w http.ResponseWriter, r *http.Request, m Machine, client *http.Client, goos, goarch, directErr string, start time.Time) {
	log.Printf("remote-update relay start (machine=%s os=%s/%s): %s", m.ID, goos, goarch, directErr)
	timeout := selfUpdateRelayTimeout
	if timeout <= 0 {
		timeout = selfUpdateTimeout
	}
	relayCtx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), timeout)
	defer cancel()

	tag, usedIPv4, err := fetchLatestTagErr(relayCtx)
	if err != nil || tag == "" {
		msg := formatUpdateError(err)
		if msg == "" {
			msg = "连不上 github.com"
		}
		log.Printf("remote-update relay failed (machine=%s stage=check-latest dur=%s): %s", m.ID, time.Since(start).Round(time.Millisecond), sanitizeUpdateErr(fmt.Sprint(err)))
		writeJSON(w, 502, map[string]any{"error": fmt.Sprintf("%s；Hub 中转查不到最新版本：%s", directErr, msg)})
		return
	}

	latestRel.Lock()
	latestRel.tag = tag
	latestRel.checked = time.Now()
	latestRel.Unlock()

	if parseVersion(m.Version) != nil && !outdated(m.Version, tag) {
		log.Printf("remote-update relay noop (machine=%s version=%s latest=%s dur=%s): already up to date", m.ID, m.Version, tag, time.Since(start).Round(time.Millisecond))
		writeJSON(w, 200, map[string]any{"ok": true, "version": m.Version, "latest": true, "relayed": true})
		return
	}

	ent, file, cached, err := getOrFetchRelayAsset(relayCtx, tag, goos, goarch, usedIPv4)
	if err != nil {
		msg := formatUpdateError(err)
		log.Printf("remote-update relay failed (machine=%s stage=hub-download target=%s os=%s/%s dur=%s): %s", m.ID, tag, goos, goarch, time.Since(start).Round(time.Millisecond), sanitizeUpdateErr(err.Error()))
		writeJSON(w, 502, map[string]any{"error": fmt.Sprintf("%s；Hub 中转下载 %s (%s/%s) 失败：%s", directErr, tag, goos, goarch, msg)})
		return
	}
	defer file.Close()

	pushReq, err := http.NewRequestWithContext(relayCtx, http.MethodPost, "http://node/api/update/relay", file)
	if err != nil {
		writeJSON(w, 502, map[string]any{"error": "构造中转升级请求失败：" + sanitizeUpdateErr(err.Error())})
		return
	}
	pushReq.ContentLength = ent.size
	pushReq.Header.Set("Content-Type", "application/octet-stream")
	pushReq.Header.Set(relayUpdateTagHeader, tag)
	pushReq.Header.Set(relayUpdateSHAHeader, ent.sha256)
	pushReq.Header.Set("Expect", "100-continue")

	resp, err := client.Do(pushReq)
	if err != nil {
		dur := time.Since(start).Round(time.Millisecond)
		log.Printf("remote-update relay failed (machine=%s stage=push target=%s dur=%s): %s", m.ID, tag, dur, sanitizeUpdateErr(err.Error()))
		msg := fmt.Sprintf("Hub 中转推送 %s 到 %s 失败：%s", tag, m.ID, sanitizeUpdateErr(err.Error()))
		switch {
		case isTimeoutErr(err):
			msg = fmt.Sprintf("Hub 中转推送 %s 到 %s 超时：请稍后刷新查看版本", tag, m.ID)
		case isConnResetOrEOF(err):
			msg = fmt.Sprintf("Hub 中转推送 %s 到 %s 时连接中断：节点可能正在重启或暂时掉线，请稍候刷新查看版本", tag, m.ID)
		}
		writeJSON(w, 502, map[string]any{"error": msg})
		return
	}
	rawBody, readErr := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	resp.Body.Close()
	dur := time.Since(start).Round(time.Millisecond)
	if readErr != nil {
		log.Printf("remote-update relay failed (machine=%s stage=push-read target=%s dur=%s): %s", m.ID, tag, dur, sanitizeUpdateErr(readErr.Error()))
		writeJSON(w, 502, map[string]any{"error": fmt.Sprintf("Hub 中转推送 %s 到 %s 时连接中断，请稍候刷新查看版本", tag, m.ID)})
		return
	}

	if resp.StatusCode == http.StatusNotFound || resp.StatusCode == http.StatusMethodNotAllowed {
		log.Printf("remote-update relay unsupported (machine=%s version=%s status=%d dur=%s)", m.ID, m.Version, resp.StatusCode, dur)
		verHint := "版本较旧"
		if strings.TrimSpace(m.Version) != "" {
			verHint = fmt.Sprintf("当前版本（%s）较旧", strings.TrimSpace(m.Version))
		}
		writeJSON(w, 502, map[string]any{
			"error": fmt.Sprintf("%s；已尝试由 Hub 中转分发 %s，但该节点%s，暂不支持接收 Hub 中转升级，请先在该节点手动升级一次后再使用一键更新", directErr, tag, verHint),
		})
		return
	}

	if resp.StatusCode == http.StatusOK {
		var out map[string]any
		if json.Unmarshal(rawBody, &out) != nil || out == nil {
			out = map[string]any{"ok": true, "version": tag}
		}
		out["relayed"] = true
		from, _ := out["from"].(string)
		ver, _ := out["version"].(string)
		latestFlag, _ := out["latest"].(bool)
		log.Printf("remote-update relay ok (machine=%s from=%s version=%s latest=%v cached=%v dur=%s)", m.ID, from, ver, latestFlag, cached, dur)
		writeJSON(w, 200, out)
		return
	}

	var nodeRes struct {
		Error string `json:"error"`
	}
	nodeErr := ""
	if strings.HasPrefix(resp.Header.Get("Content-Type"), "application/json") && json.Unmarshal(rawBody, &nodeRes) == nil && strings.TrimSpace(nodeRes.Error) != "" {
		nodeErr = sanitizeUpdateErr(strings.TrimSpace(nodeRes.Error))
	} else {
		msg := strings.TrimSpace(string(rawBody))
		if msg == "" {
			msg = resp.Status
		}
		nodeErr = sanitizeUpdateErr(msg)
	}
	log.Printf("remote-update relay failed (machine=%s target=%s status=%d dur=%s): %s", m.ID, tag, resp.StatusCode, dur, nodeErr)

	outStatus := http.StatusBadGateway
	if resp.StatusCode == http.StatusBadRequest || resp.StatusCode == http.StatusConflict {
		outStatus = resp.StatusCode
	}
	if !strings.HasPrefix(nodeErr, "中转升级失败：") && !strings.HasPrefix(nodeErr, "Hub 中转") {
		nodeErr = "Hub 中转分发失败：" + nodeErr
	}
	writeJSON(w, outStatus, map[string]any{"error": nodeErr})
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

	if err := verifyBinaryVersion(ctx, tmp, tag); err != nil {
		return err
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
					want = strings.ToLower(strings.TrimSpace(f[0]))
				}
			}
			if want == "" {
				return "", forceIPv4, fmt.Errorf("%s 没有 %s 的版本", tag, asset)
			}
			if !isValidSHA256Hex(want) {
				return "", forceIPv4, fmt.Errorf("%s 的 checksums.txt 中 %s 校验值格式无效", tag, asset)
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
	return downloadAssetToDir(ctx, base, asset, filepath.Dir(exe), wantSHA, preferIPv4, 0o755)
}

func downloadAssetToDir(ctx context.Context, base, asset, dir, wantSHA string, preferIPv4 bool, mode os.FileMode) (string, error) {
	maxAttempts := max(1, updateMaxAttempts)
	forceIPv4 := preferIPv4
	triedIPv4 := preferIPv4
	var lastErr error
	lastUsesProxy := false

	for attempt := 0; attempt < maxAttempts; attempt++ {
		if forceIPv4 {
			triedIPv4 = true
		}
		tmp, usedProxy, err := downloadAssetOnce(ctx, base+asset, asset, dir, wantSHA, forceIPv4, mode)
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

func downloadAssetOnce(parentCtx context.Context, targetURL, asset, dir, wantSHA string, forceIPv4 bool, mode os.FileMode) (string, bool, error) {
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
	chmodErr := f.Chmod(mode)
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
