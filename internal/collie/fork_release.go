package collie

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"
)

const defaultForkRepo = "HayesYu/collie"

var (
	nodeRelayWaitTimeout  = 15 * time.Second
	nodeRelayPollInterval = 500 * time.Millisecond
)

func forkReleaseRepoURL(raw string) string {
	s := strings.TrimSpace(raw)
	if s == "" {
		s = defaultForkRepo
	}
	s = strings.TrimRight(s, "/")
	s = strings.TrimSuffix(s, ".git")
	if strings.HasPrefix(s, "http://") || strings.HasPrefix(s, "https://") {
		return s
	}
	return "https://github.com/" + strings.TrimPrefix(s, "/")
}

func init() {
	if v := strings.TrimSpace(os.Getenv("COLLIE_RELEASE_REPO")); v != "" {
		releaseRepoURL = forkReleaseRepoURL(v)
		return
	}
	releaseRepoURL = forkReleaseRepoURL(defaultForkRepo)
}

func preferNodeUpdate() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv("COLLIE_UPDATE_PREFER_NODE"))) {
	case "1", "true", "yes":
		return true
	default:
		return false
	}
}

// serveRelayAsset runs on a remote node inside the Hub↔Node tunnel. It fetches
// and SHA-256-verifies a GitHub Release binary for the Hub's platform and
// streams it back with X-Collie-Update-Tag and X-Collie-Update-SHA256 headers.
func (s *Server) serveRelayAsset(w http.ResponseWriter, r *http.Request) {
	if !s.node {
		writeJSON(w, 403, map[string]any{"error": "仅允许节点通过控制通道提供更新包代拉"})
		return
	}
	tag := strings.TrimSpace(r.URL.Query().Get("tag"))
	goos := strings.TrimSpace(strings.ToLower(r.URL.Query().Get("os")))
	goarch := strings.TrimSpace(strings.ToLower(r.URL.Query().Get("arch")))
	if parseVersion(tag) == nil {
		writeJSON(w, 400, map[string]any{"error": "代拉升级目标版本号无效"})
		return
	}
	if !platformPartRe.MatchString(goos) || !platformPartRe.MatchString(goarch) {
		writeJSON(w, 400, map[string]any{"error": "代拉升级平台参数无效"})
		return
	}

	timeout := selfUpdateRelayTimeout
	if timeout <= 0 {
		timeout = selfUpdateTimeout
	}
	fetchCtx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), timeout)
	defer cancel()

	start := time.Now()
	ent, f, cached, err := getOrFetchRelayAsset(fetchCtx, tag, goos, goarch, false)
	if err != nil {
		log.Printf("node-fetch-asset failed (target=%s os=%s/%s dur=%s): %s", tag, goos, goarch, time.Since(start).Round(time.Millisecond), sanitizeUpdateErr(err.Error()))
		writeJSON(w, 502, map[string]any{"error": formatUpdateError(err)})
		return
	}
	defer f.Close()

	log.Printf("node-fetch-asset ok (target=%s os=%s/%s size=%d cached=%v dur=%s)", tag, goos, goarch, ent.size, cached, time.Since(start).Round(time.Millisecond))
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Length", strconv.FormatInt(ent.size, 10))
	w.Header().Set(relayUpdateTagHeader, tag)
	w.Header().Set(relayUpdateSHAHeader, ent.sha256)
	w.WriteHeader(http.StatusOK)
	_, _ = io.Copy(w, f)
}

func (s *Server) hasConfiguredRemoteNode() bool {
	if s.herdr != nil {
		for _, m := range s.herdr.Machines() {
			if !m.Local {
				return true
			}
		}
	}
	if s.auths != nil {
		for _, d := range s.auths.list() {
			if d.Kind == "machine" {
				return true
			}
		}
	}
	return false
}

func (s *Server) onlineRemoteNodes() []Machine {
	if s.herdr == nil {
		return nil
	}
	var out []Machine
	for _, m := range s.herdr.Machines() {
		if !m.Local && m.Online {
			out = append(out, m)
		}
	}
	return out
}

func sleepRelayPoll(ctx context.Context, deadline time.Time) bool {
	wait := nodeRelayPollInterval
	if wait <= 0 {
		wait = 250 * time.Millisecond
	}
	rem := time.Until(deadline)
	if rem <= 0 {
		return false
	}
	if rem < wait {
		wait = rem
	}
	t := time.NewTimer(wait)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}

func (s *Server) waitOnlineRemoteNodes(ctx context.Context, deadline time.Time) []Machine {
	if nodes := s.onlineRemoteNodes(); len(nodes) > 0 {
		return nodes
	}
	if !s.hasConfiguredRemoteNode() {
		return nil
	}
	for time.Now().Before(deadline) {
		if !sleepRelayPoll(ctx, deadline) {
			return nil
		}
		if nodes := s.onlineRemoteNodes(); len(nodes) > 0 {
			return nodes
		}
	}
	return nil
}

func probeExecutableDirWritable(exe string) error {
	dir := filepath.Dir(exe)
	probe, err := os.CreateTemp(dir, ".collie.new.*")
	if err != nil {
		return &updateFileError{dir: dir, err: err}
	}
	p := probe.Name()
	_ = probe.Close()
	_ = os.Remove(p)
	return nil
}

func isFatalLocalUpdateErr(err error) bool {
	var fe *updateFileError
	return errors.As(err, &fe) || errors.Is(err, errExecVerifyFailed)
}

func (s *Server) fetchInstallFromNode(ctx context.Context, m Machine, tag, exe string) (transient bool, err error) {
	client, err := s.herdr.Client(m.ID)
	if err != nil {
		return true, err
	}
	u := fmt.Sprintf("http://node/api/update/fetch?tag=%s&os=%s&arch=%s",
		url.QueryEscape(tag), url.QueryEscape(runtime.GOOS), url.QueryEscape(runtime.GOARCH))
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return false, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return isConnResetOrEOF(err) || isTimeoutErr(err), err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<10))
		msg := strings.TrimSpace(string(raw))
		if msg == "" {
			msg = resp.Status
		}
		return false, fmt.Errorf("node %s returned %d: %s", m.ID, resp.StatusCode, sanitizeUpdateErr(msg))
	}
	gotTag := strings.TrimSpace(resp.Header.Get(relayUpdateTagHeader))
	wantSHA := strings.ToLower(strings.TrimSpace(resp.Header.Get(relayUpdateSHAHeader)))
	if gotTag != tag {
		return false, fmt.Errorf("node %s returned tag %q, want %q", m.ID, gotTag, tag)
	}
	if !isValidSHA256Hex(wantSHA) {
		return false, fmt.Errorf("node %s returned invalid SHA-256 header", m.ID)
	}
	if err := installRelayedBinary(ctx, resp.Body, resp.ContentLength, tag, wantSHA, exe); err != nil {
		return isConnResetOrEOF(err) && !isFatalLocalUpdateErr(err), err
	}
	return false, nil
}

// downloadReleaseViaNode downloads and installs release tag for this Hub by
// asking an online remote node to fetch the binary from GitHub over its network.
func (s *Server) downloadReleaseViaNode(ctx context.Context, tag, exe string) error {
	if s.herdr == nil || s.node {
		return errors.New("no remote node available")
	}
	if err := probeExecutableDirWritable(exe); err != nil {
		return err
	}

	waitDeadline := time.Now().Add(nodeRelayWaitTimeout)
	var errs []string
	for pass := 0; pass < 2; pass++ {
		nodes := s.waitOnlineRemoteNodes(ctx, waitDeadline)
		if len(nodes) == 0 {
			if len(errs) == 0 {
				return errors.New("没有在线节点可协助拉取更新包")
			}
			break
		}
		sawTransient := false
		for _, m := range nodes {
			transient, err := s.fetchInstallFromNode(ctx, m, tag, exe)
			if err == nil {
				log.Printf("hub self-update fetched via node (machine=%s target=%s os=%s/%s)", m.ID, tag, runtime.GOOS, runtime.GOARCH)
				return nil
			}
			if isFatalLocalUpdateErr(err) {
				return err
			}
			if transient {
				sawTransient = true
			}
			errs = append(errs, fmt.Sprintf("%s: %s", m.ID, sanitizeUpdateErr(err.Error())))
		}
		if !sawTransient || pass > 0 || !sleepRelayPoll(ctx, waitDeadline) {
			break
		}
	}
	return errors.New(strings.Join(errs, "; "))
}

func (s *Server) downloadSelfUpdate(ctx context.Context, latest, exe string, usedIPv4 bool) (bool, error) {
	if !s.node && preferNodeUpdate() {
		nodeErr := s.downloadReleaseViaNode(ctx, latest, exe)
		if nodeErr == nil {
			return true, nil
		}
		if isFatalLocalUpdateErr(nodeErr) {
			return false, nodeErr
		}
		log.Printf("hub self-update via preferred node failed (target=%s), falling back to direct: %s", latest, sanitizeUpdateErr(nodeErr.Error()))
		if err := downloadReleaseWithHint(ctx, latest, exe, usedIPv4); err != nil {
			return false, err
		}
		return false, nil
	}

	directErr := downloadReleaseWithHint(ctx, latest, exe, usedIPv4)
	if directErr == nil {
		return false, nil
	}
	if s.node || isFatalLocalUpdateErr(directErr) {
		return false, directErr
	}

	timeout := selfUpdateRelayTimeout
	if timeout <= 0 {
		timeout = selfUpdateTimeout
	}
	if timeout <= 0 {
		timeout = 2*time.Minute + 30*time.Second
	}
	fbCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), timeout)
	defer cancel()

	if nodeErr := s.downloadReleaseViaNode(fbCtx, latest, exe); nodeErr == nil {
		return true, nil
	} else if isFatalLocalUpdateErr(nodeErr) {
		return false, nodeErr
	} else {
		log.Printf("hub self-update node fallback unavailable (target=%s): %s", latest, sanitizeUpdateErr(nodeErr.Error()))
	}
	return false, directErr
}
