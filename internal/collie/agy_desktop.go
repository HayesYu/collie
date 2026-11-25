package collie

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

// The Antigravity desktop app (locally via hub_*.json, or on a remote devbox
// via AG App + SSH's ls_*.json) runs one or more language servers on loopback.
// Their local Connect-RPC API takes a message into a conversation the same way
// the app's own input box does (SendUserCascadeMessage), guarded by a CSRF
// token. Collie discovers all live servers, routes each conversation to the
// matching instance by cascadeId, and sends through it.
type agyServer struct {
	scheme string // "http" or "https"
	addr   string // "127.0.0.1:<port>"
	csrf   string
	hub    bool // true for hub_*.json (local desktop hub), false for ls_*.json
}

var (
	agyHTTPClient = &http.Client{
		Transport: &http.Transport{Proxy: nil},
	}
	agyHTTPSClient = &http.Client{
		Transport: &http.Transport{
			Proxy:           nil,
			TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
		},
	}
)

var agyServerCache struct {
	sync.Mutex
	home string
	at   time.Time
	list []*agyServer
}

const agyService = "/exa.language_server_pb.LanguageServerService/"

// agyDesktopServers returns all reachable Antigravity language servers for
// this server's home directory.
func (s *Server) agyDesktopServers() []*agyServer {
	home := ""
	if s != nil {
		home = s.home
	}
	return agyDesktopServersFor(home)
}

func agyDesktopServersFor(home string) []*agyServer {
	if home == "" {
		home, _ = os.UserHomeDir()
	}
	agyServerCache.Lock()
	defer agyServerCache.Unlock()
	if agyServerCache.home == home && time.Since(agyServerCache.at) < 20*time.Second {
		return agyServerCache.list
	}
	agyServerCache.home, agyServerCache.at, agyServerCache.list = home, time.Now(), findAgyServers(home)
	return agyServerCache.list
}

// agyDesktopServer returns the first reachable server, or nil.
func agyDesktopServer() *agyServer {
	if list := agyDesktopServersFor(""); len(list) > 0 {
		return list[0]
	}
	return nil
}

func forgetAgyServer() {
	agyServerCache.Lock()
	agyServerCache.at = time.Time{}
	agyServerCache.list = nil
	agyServerCache.Unlock()
}

func findAgyServers(home string) []*agyServer {
	if list := findAgyDaemonServers(home); len(list) > 0 {
		return list
	}
	return findAgyProcessServers()
}

// findAgyDaemonServers reads discovery files written by both the local desktop
// app (hub_*.json, 2.34+) and remote SSH / hub-server language servers
// (ls_*.json) under ~/.gemini/antigravity/daemon/.
func findAgyDaemonServers(home string) []*agyServer {
	if home == "" {
		return nil
	}
	daemonDir := filepath.Join(home, ".gemini", "antigravity", "daemon")
	hubFiles, _ := filepath.Glob(filepath.Join(daemonDir, "hub_*.json"))
	lsFiles, _ := filepath.Glob(filepath.Join(daemonDir, "ls_*.json"))
	sort.Strings(hubFiles)
	sort.Strings(lsFiles)

	type entry struct {
		path string
		hub  bool
	}
	var entries []entry
	for _, f := range hubFiles {
		entries = append(entries, entry{path: f, hub: true})
	}
	for _, f := range lsFiles {
		entries = append(entries, entry{path: f, hub: false})
	}

	var out []*agyServer
	seen := map[string]bool{}
	for _, e := range entries {
		b, err := os.ReadFile(e.path)
		if err != nil {
			continue
		}
		var h struct {
			PID       int    `json:"pid"`
			HTTPPort  int    `json:"httpPort"`
			HTTPSPort int    `json:"httpsPort"`
			CSRF      string `json:"csrfToken"`
		}
		if json.Unmarshal(b, &h) != nil || h.CSRF == "" || (h.HTTPPort <= 0 && h.HTTPSPort <= 0) {
			continue
		}
		if h.PID > 0 {
			if err := syscall.Kill(h.PID, 0); err != nil && !errors.Is(err, syscall.EPERM) {
				continue // left behind by a server that has quit
			}
		}
		if ls := probeAgyCandidate(h.HTTPPort, h.HTTPSPort, h.CSRF, e.hub); ls != nil {
			key := ls.scheme + "://" + ls.addr
			if !seen[key] {
				seen[key] = true
				out = append(out, ls)
			}
		}
	}
	return out
}

func probeAgyCandidate(httpPort, httpsPort int, csrf string, hub bool) *agyServer {
	if httpPort > 0 {
		ls := &agyServer{scheme: "http", addr: "127.0.0.1:" + strconv.Itoa(httpPort), csrf: csrf, hub: hub}
		if ls.probe() {
			return ls
		}
	}
	if httpsPort > 0 {
		ls := &agyServer{scheme: "https", addr: "127.0.0.1:" + strconv.Itoa(httpsPort), csrf: csrf, hub: hub}
		if ls.probe() {
			return ls
		}
	}
	return nil
}

// findAgyProcessServers is the pre-2.34 fallback when the token was passed on
// the language server's command line.
func findAgyProcessServers() []*agyServer {
	out, err := exec.Command("ps", "-axww", "-o", "pid=,command=").Output()
	if err != nil {
		return nil
	}
	var list []*agyServer
	seen := map[string]bool{}
	for _, line := range strings.Split(string(out), "\n") {
		f := strings.Fields(line)
		if len(f) < 2 || !strings.HasPrefix(filepath.Base(f[1]), "language_server") {
			continue
		}
		args := map[string]string{}
		for i := 2; i+1 < len(f); i++ {
			if strings.HasPrefix(f[i], "--") {
				args[f[i]] = f[i+1]
			}
		}
		// The desktop app's server (the CLI keeps its own elsewhere).
		if args["--app_data_dir"] != "antigravity" || args["--csrf_token"] == "" {
			continue
		}
		pid, err := strconv.Atoi(f[0])
		if err != nil {
			continue
		}
		for _, port := range listenPorts(pid) {
			ls := &agyServer{scheme: "http", addr: "127.0.0.1:" + port, csrf: args["--csrf_token"]}
			key := ls.scheme + "://" + ls.addr
			if !seen[key] && ls.probe() {
				seen[key] = true
				list = append(list, ls)
				break
			}
		}
	}
	return list
}

// listenPorts: the loopback TCP ports a process listens on.
func listenPorts(pid int) []string {
	out, err := exec.Command("lsof", "-nP", "-a", "-p", strconv.Itoa(pid), "-iTCP", "-sTCP:LISTEN", "-Fn").Output()
	if err != nil {
		return nil
	}
	var ports []string
	for _, l := range strings.Split(string(out), "\n") {
		if strings.HasPrefix(l, "n") {
			if i := strings.LastIndex(l, ":"); i > 0 {
				ports = append(ports, l[i+1:])
			}
		}
	}
	return ports
}

// probe: this port answers the API over its scheme with this token.
func (ls *agyServer) probe() bool {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_, err := ls.call(ctx, "GetCascadeTrajectorySteps", map[string]any{"cascadeId": "00000000-0000-0000-0000-000000000000"})
	var api *agyError
	return err == nil || errors.As(err, &api)
}

func (ls *agyServer) trajectories(ctx context.Context) map[string]bool {
	cctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	raw, err := ls.call(cctx, "GetAllCascadeTrajectories", map[string]any{})
	if err != nil {
		return nil
	}
	var res struct {
		Summaries      map[string]json.RawMessage `json:"trajectorySummaries"`
		SummariesSnake map[string]json.RawMessage `json:"trajectory_summaries"`
	}
	if json.Unmarshal(raw, &res) != nil {
		return nil
	}
	out := map[string]bool{}
	for id := range res.Summaries {
		out[id] = true
	}
	for id := range res.SummariesSnake {
		out[id] = true
	}
	return out
}

func (ls *agyServer) hasCascade(ctx context.Context, conversation string) bool {
	cctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	_, err := ls.call(cctx, "GetCascadeTrajectorySteps", map[string]any{"cascadeId": conversation})
	return err == nil
}

type agyError struct{ Code, Message string }

func (e *agyError) Error() string { return e.Message }

func (ls *agyServer) call(ctx context.Context, method string, body any) (json.RawMessage, error) {
	scheme := ls.scheme
	if scheme == "" {
		scheme = "http"
	}
	b, _ := json.Marshal(body)
	req, err := http.NewRequestWithContext(ctx, "POST", scheme+"://"+ls.addr+agyService+method, bytes.NewReader(b))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Connect-Protocol-Version", "1")
	req.Header.Set("x-codeium-csrf-token", ls.csrf)
	client := agyHTTPClient
	if scheme == "https" {
		client = agyHTTPSClient
	}
	res, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	out, _ := io.ReadAll(io.LimitReader(res.Body, 4<<20))
	if !strings.HasPrefix(res.Header.Get("Content-Type"), "application/json") {
		return nil, fmt.Errorf("Antigravity: HTTP %d", res.StatusCode)
	}
	if res.StatusCode != 200 {
		var e agyError
		json.Unmarshal(out, &e)
		if e.Message == "" {
			e.Message = fmt.Sprintf("HTTP %d", res.StatusCode)
		}
		return nil, &e
	}
	return out, nil
}

// matchAgyServer picks the language server instance that owns conversation
// when multiple instances (e.g., multiple AG App + SSH workspaces) are running.
func matchAgyServer(ctx context.Context, servers []*agyServer, conversation string) *agyServer {
	if len(servers) == 0 {
		return nil
	}
	if len(servers) == 1 {
		return servers[0]
	}
	for _, ls := range servers {
		if ids := ls.trajectories(ctx); ids[conversation] {
			return ls
		}
	}
	for _, ls := range servers {
		if ls.hasCascade(ctx, conversation) {
			return ls
		}
	}
	return nil
}

// agyAppSend puts a message into an Antigravity desktop or remote SSH
// conversation, as if typed in the app.
func (s *Server) agyAppSend(ctx context.Context, conversation, text string) error {
	servers := s.agyDesktopServers()
	if len(servers) == 0 {
		return errors.New("Antigravity 没有打开：打开它之后就能从这里发消息")
	}
	payload := map[string]any{
		"cascadeId": conversation,
		"items":     []map[string]string{{"text": text}},
	}
	if target := matchAgyServer(ctx, servers, conversation); target != nil {
		if _, err := target.call(ctx, "SendUserCascadeMessage", payload); err != nil {
			var api *agyError
			if !errors.As(err, &api) {
				forgetAgyServer() // the server restarted: look again next time
			}
			return fmt.Errorf("Antigravity 没收下这条消息：%w", err)
		}
		forgetDesktopRows()
		return nil
	}
	// Multiple instances and none reported owning this cascade yet: try hub
	// servers first, then ls servers, continuing only on not_found or transport
	// errors so a delivered message is never duplicated.
	var lastErr error
	for _, ls := range servers {
		_, err := ls.call(ctx, "SendUserCascadeMessage", payload)
		if err == nil {
			forgetDesktopRows()
			return nil
		}
		lastErr = err
		var api *agyError
		if errors.As(err, &api) {
			if api.Code != "not_found" {
				return fmt.Errorf("Antigravity 没收下这条消息：%w", err)
			}
		} else {
			forgetAgyServer()
		}
	}
	return fmt.Errorf("Antigravity 没收下这条消息：%w", lastErr)
}
