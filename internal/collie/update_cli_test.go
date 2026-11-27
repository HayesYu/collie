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
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func saveUpdateCLIHooks(t *testing.T) {
	t.Helper()
	saveUpdateHooks(t)

	origGOOS := updateGOOS
	origTarget := updateCLITarget
	origHome := updateHomeDir
	origUID := updateUID
	origReadFile := updateReadFile
	origStat := updateStat
	origRunCmd := updateRunCmd
	origAllTimeout := cliUpdateAllTimeout
	origVerTimeout := cliUpdateVersionTimeout
	origVerPoll := cliUpdateVersionPoll
	origVerRetries := cliUpdateVersionRetries
	origHubDelay := cliUpdateHubDelay
	origReconnectWait := cliUpdateReconnectWait
	origReconnectPoll := cliUpdateReconnectPoll
	origBootoutPoll := launchdBootoutPoll
	origBootoutMax := launchdBootoutMaxTries
	origBootRetries := launchdBootstrapRetries
	origBootDelay := launchdBootstrapDelay

	cliUpdateVersionPoll = 5 * time.Millisecond
	cliUpdateHubDelay = 0
	cliUpdateReconnectWait = 200 * time.Millisecond
	cliUpdateReconnectPoll = 10 * time.Millisecond
	launchdBootoutPoll = 2 * time.Millisecond
	launchdBootoutMaxTries = 5
	launchdBootstrapRetries = 3
	launchdBootstrapDelay = 2 * time.Millisecond
	// Default: no active service so binary tests don't touch host systemd/launchd.
	updateRunCmd = func(ctx context.Context, name string, args ...string) ([]byte, error) {
		return nil, exec.ErrNotFound
	}

	t.Cleanup(func() {
		updateGOOS = origGOOS
		updateCLITarget = origTarget
		updateHomeDir = origHome
		updateUID = origUID
		updateReadFile = origReadFile
		updateStat = origStat
		updateRunCmd = origRunCmd
		cliUpdateAllTimeout = origAllTimeout
		cliUpdateVersionTimeout = origVerTimeout
		cliUpdateVersionPoll = origVerPoll
		cliUpdateVersionRetries = origVerRetries
		cliUpdateHubDelay = origHubDelay
		cliUpdateReconnectWait = origReconnectWait
		cliUpdateReconnectPoll = origReconnectPoll
		launchdBootoutPoll = origBootoutPoll
		launchdBootoutMaxTries = origBootoutMax
		launchdBootstrapRetries = origBootRetries
		launchdBootstrapDelay = origBootDelay
	})
}

func makeFakeReleaseServer(t *testing.T, tag, script string, overrideChecksum func() string) (*httptest.Server, *atomic.Int32, *atomic.Int32, *atomic.Int32) {
	t.Helper()
	sum := sha256.Sum256([]byte(script))
	goodSum := hex.EncodeToString(sum[:])
	asset := "collie-" + runtime.GOOS + "-" + runtime.GOARCH

	var latestHits, csHits, binHits atomic.Int32
	var ts *httptest.Server
	ts = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/releases/latest":
			latestHits.Add(1)
			w.Header().Set("Location", ts.URL+"/releases/tag/"+tag)
			w.WriteHeader(http.StatusFound)
		case "/releases/download/" + tag + "/checksums.txt":
			csHits.Add(1)
			cs := goodSum
			if overrideChecksum != nil {
				if v := overrideChecksum(); v != "" {
					cs = v
				}
			}
			fmt.Fprintf(w, "%s  %s\n", cs, asset)
		case "/releases/download/" + tag + "/" + asset:
			binHits.Add(1)
			_, _ = w.Write([]byte(script))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(ts.Close)
	return ts, &latestHits, &csHits, &binHits
}

func TestCLIUpdateParseAndHelpAndAlias(t *testing.T) {
	saveUpdateCLIHooks(t)

	if _, ok := cliCommands["update"]; !ok {
		t.Fatal("missing cliCommands[\"update\"]")
	}
	if _, ok := cliCommands["upgrade"]; !ok {
		t.Fatal("missing cliCommands[\"upgrade\"]")
	}

	var out, errOut bytes.Buffer
	if err := runUpdateCLI("update", []string{"--help"}, &out, &errOut); err != nil {
		t.Fatalf("update --help failed: %v", err)
	}
	for _, want := range []string{"usage: collie update", "alias: collie upgrade", "-check", "-all", "-force"} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("update --help missing %q:\n%s", want, out.String())
		}
	}

	out.Reset()
	if err := runUpdateCLI("upgrade", []string{"-h"}, &out, &errOut); err != nil {
		t.Fatalf("upgrade -h failed: %v", err)
	}
	if !strings.Contains(out.String(), "usage: collie upgrade") || !strings.Contains(out.String(), "alias for collie update") {
		t.Fatalf("upgrade -h usage mismatch:\n%s", out.String())
	}

	if err := runUpdateCLI("update", []string{"--check", "--force"}, &out, &errOut); err == nil || !strings.Contains(err.Error(), "--check") {
		t.Fatalf("--check --force = %v, want conflict error", err)
	}
	if err := runUpdateCLI("update", []string{"--all", "--force"}, &out, &errOut); err == nil || !strings.Contains(err.Error(), "--force") {
		t.Fatalf("--all --force = %v, want conflict error", err)
	}
	if err := runUpdateCLI("update", []string{"extra-arg"}, &out, &errOut); err == nil || !strings.Contains(err.Error(), "unexpected arguments") {
		t.Fatalf("extra positional arg = %v, want error", err)
	}
}

func TestCLIUpdateCheckReadOnly(t *testing.T) {
	saveUpdateCLIHooks(t)

	tag := "v0.3.4"
	script := "#!/bin/sh\necho collie " + tag + "\n"
	ts, latestHits, csHits, binHits := makeFakeReleaseServer(t, tag, script, nil)
	releaseRepoURL = ts.URL

	dir := t.TempDir()
	exe := filepath.Join(dir, "collie")
	oldScript := "#!/bin/sh\necho collie v0.3.2\n"
	if err := os.WriteFile(exe, []byte(oldScript), 0o755); err != nil {
		t.Fatal(err)
	}
	executablePath = func() (string, error) { return exe, nil }

	// 1) Outdated version reports update available without downloading or modifying binary.
	version = "v0.3.2"
	var out, errOut bytes.Buffer
	if err := runUpdateCLI("update", []string{"--check"}, &out, &errOut); err != nil {
		t.Fatalf("update --check (outdated) failed: %v", err)
	}
	for _, want := range []string{"Current version: v0.3.2", "Latest version:  v0.3.4", "update available (v0.3.2 → v0.3.4)"} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("outdated check output missing %q:\n%s", want, out.String())
		}
	}
	if latestHits.Load() != 1 || csHits.Load() != 0 || binHits.Load() != 0 {
		t.Fatalf("check downloaded files! latest=%d cs=%d bin=%d", latestHits.Load(), csHits.Load(), binHits.Load())
	}
	if got, _ := os.ReadFile(exe); string(got) != oldScript {
		t.Fatalf("exe modified during --check: %q", string(got))
	}

	// 2) Already up to date via `upgrade` alias.
	version = "v0.3.4"
	out.Reset()
	if err := runUpdateCLI("upgrade", []string{"--check"}, &out, &errOut); err != nil {
		t.Fatalf("upgrade --check (up-to-date) failed: %v", err)
	}
	if !strings.Contains(out.String(), "up to date") {
		t.Fatalf("expected up to date in output:\n%s", out.String())
	}

	// 3) Source build (`dev`) returns clean exit 0 with --force hint.
	version = "dev"
	out.Reset()
	if err := runUpdateCLI("update", []string{"--check"}, &out, &errOut); err != nil {
		t.Fatalf("update --check (dev) failed: %v", err)
	}
	if !strings.Contains(out.String(), "source build") || !strings.Contains(out.String(), "--force") {
		t.Fatalf("expected source build hint in output:\n%s", out.String())
	}
	if csHits.Load() != 0 || binHits.Load() != 0 {
		t.Fatalf("--check should never download assets: cs=%d bin=%d", csHits.Load(), binHits.Load())
	}
}

func TestCLIUpdateLocalAtomicChecksumAndForce(t *testing.T) {
	saveUpdateCLIHooks(t)

	dir := t.TempDir()
	exe := filepath.Join(dir, "collie")
	oldScript := "#!/bin/sh\necho collie v0.3.2\n"
	if err := os.WriteFile(exe, []byte(oldScript), 0o755); err != nil {
		t.Fatal(err)
	}
	executablePath = func() (string, error) { return exe, nil }

	tag := "v0.3.4"
	newScript := "#!/bin/sh\necho collie " + tag + "\n"
	var badChecksum atomic.Bool
	ts, _, csHits, binHits := makeFakeReleaseServer(t, tag, newScript, func() string {
		if badChecksum.Load() {
			return strings.Repeat("f", 64)
		}
		return ""
	})
	releaseRepoURL = ts.URL

	// 1) SHA-256 mismatch rejects install, leaves binary untouched and 0 temp files.
	version = "v0.3.2"
	badChecksum.Store(true)
	var out, errOut bytes.Buffer
	err := runUpdateCLI("update", nil, &out, &errOut)
	if err == nil || !strings.Contains(err.Error(), "校验失败") {
		t.Fatalf("bad checksum err = %v, want SHA-256 mismatch error", err)
	}
	if got, _ := os.ReadFile(exe); string(got) != oldScript {
		t.Fatalf("binary modified on bad checksum: %q", string(got))
	}
	if leftovers, _ := filepath.Glob(filepath.Join(dir, ".collie.new.*")); len(leftovers) != 0 {
		t.Fatalf("temp files leaked after bad checksum: %v", leftovers)
	}

	// 2) Happy path: downloads, verifies SHA-256 + version, atomically replaces exe with 0755.
	badChecksum.Store(false)
	out.Reset()
	if err := runUpdateCLI("update", nil, &out, &errOut); err != nil {
		t.Fatalf("local update failed: %v", err)
	}
	if !strings.Contains(out.String(), "Updated collie v0.3.2 → v0.3.4") {
		t.Fatalf("missing updated confirmation:\n%s", out.String())
	}
	if !strings.Contains(out.String(), "No active managed collie service") {
		t.Fatalf("expected manual restart hint when no service active:\n%s", out.String())
	}
	gotNew, err := os.ReadFile(exe)
	if err != nil || string(gotNew) != newScript {
		t.Fatalf("replaced binary = %q, err=%v", string(gotNew), err)
	}
	fi, err := os.Stat(exe)
	if err != nil || fi.Mode().Perm() != 0o755 {
		t.Fatalf("replaced binary perm = %o, want 0755", fi.Mode().Perm())
	}
	if leftovers, _ := filepath.Glob(filepath.Join(dir, ".collie.new.*")); len(leftovers) != 0 {
		t.Fatalf("temp files left after successful update: %v", leftovers)
	}

	// 3) Already up to date: skips download without --force.
	version = tag
	csBefore, binBefore := csHits.Load(), binHits.Load()
	out.Reset()
	if err := runUpdateCLI("update", nil, &out, &errOut); err != nil {
		t.Fatalf("up-to-date update failed: %v", err)
	}
	if !strings.Contains(out.String(), "already up to date") {
		t.Fatalf("expected already up to date message:\n%s", out.String())
	}
	if csHits.Load() != csBefore || binHits.Load() != binBefore {
		t.Fatalf("up-to-date update unexpectedly downloaded assets")
	}

	// 4) --force reinstalls even when already at latest.
	out.Reset()
	if err := runUpdateCLI("update", []string{"--force"}, &out, &errOut); err != nil {
		t.Fatalf("force reinstall failed: %v", err)
	}
	if !strings.Contains(out.String(), "Reinstalled collie v0.3.4") {
		t.Fatalf("expected reinstall output:\n%s", out.String())
	}
	if csHits.Load() != csBefore+1 || binHits.Load() != binBefore+1 {
		t.Fatalf("--force should re-download assets: cs=%d bin=%d", csHits.Load()-csBefore, binHits.Load()-binBefore)
	}

	// 5) Source build (`dev`) is rejected without --force, allowed with --force.
	version = "dev"
	out.Reset()
	if err := runUpdateCLI("update", nil, &out, &errOut); err == nil || !strings.Contains(err.Error(), "源码编译") {
		t.Fatalf("dev without --force = %v, want source-build rejection", err)
	}
	out.Reset()
	if err := runUpdateCLI("update", []string{"--force"}, &out, &errOut); err != nil {
		t.Fatalf("dev with --force failed: %v", err)
	}
	if !strings.Contains(out.String(), "Updated collie dev → v0.3.4") {
		t.Fatalf("expected dev → v0.3.4 confirmation:\n%s", out.String())
	}
}

func TestCLIUpdateBrokenBinaryVerificationBlocked(t *testing.T) {
	saveUpdateCLIHooks(t)

	dir := t.TempDir()
	exe := filepath.Join(dir, "collie")
	oldScript := "#!/bin/sh\necho collie v0.3.2\n"
	if err := os.WriteFile(exe, []byte(oldScript), 0o755); err != nil {
		t.Fatal(err)
	}
	executablePath = func() (string, error) { return exe, nil }
	version = "v0.3.2"

	// Valid SHA-256, but downloaded binary outputs wrong version / exits non-zero.
	ts, _, _, _ := makeFakeReleaseServer(t, "v0.3.4", "#!/bin/sh\nexit 1\n", nil)
	releaseRepoURL = ts.URL

	var out, errOut bytes.Buffer
	err := runUpdateCLI("update", nil, &out, &errOut)
	if err == nil || !strings.Contains(err.Error(), "跑不起来") {
		t.Fatalf("broken binary err = %v, want verify failure", err)
	}
	if got, _ := os.ReadFile(exe); string(got) != oldScript {
		t.Fatalf("original binary overwritten by broken binary: %q", string(got))
	}
	if leftovers, _ := filepath.Glob(filepath.Join(dir, ".collie.new.*")); len(leftovers) != 0 {
		t.Fatalf("temp files leaked after broken binary: %v", leftovers)
	}
}

func TestCLIUpdateProxyFailureFallsBackToDirectIPv4(t *testing.T) {
	saveUpdateCLIHooks(t)
	updateMaxAttempts = 1

	dir := t.TempDir()
	exe := filepath.Join(dir, "collie")
	if err := os.WriteFile(exe, []byte("#!/bin/sh\necho collie v0.3.2\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	executablePath = func() (string, error) { return exe, nil }
	version = "v0.3.2"

	tag := "v0.3.4"
	newScript := "#!/bin/sh\necho collie " + tag + "\n"
	ts, _, _, _ := makeFakeReleaseServer(t, tag, newScript, nil)
	releaseRepoURL = ts.URL

	// Configure a broken proxy; CLI must detect proxy failure and automatically retry direct over tcp4.
	deadProxy, _ := url.Parse("http://user:topsecret@127.0.0.1:1")
	updateProxyFunc = func(*http.Request) (*url.URL, error) { return deadProxy, nil }

	var mu sync.Mutex
	var dialed []string
	realDial := (&net.Dialer{Timeout: 5 * time.Second}).DialContext
	updateDialContext = func(ctx context.Context, network, addr string) (net.Conn, error) {
		mu.Lock()
		dialed = append(dialed, network+"->"+addr)
		mu.Unlock()
		if addr == "127.0.0.1:1" {
			return nil, errors.New("proxyconnect tcp: http://user:topsecret@127.0.0.1:1: connection refused")
		}
		return realDial(ctx, network, addr)
	}

	var out, errOut bytes.Buffer
	if err := runUpdateCLI("update", nil, &out, &errOut); err != nil {
		t.Fatalf("update with broken proxy fallback failed: %v (errOut=%s)", err, errOut.String())
	}
	if strings.Contains(errOut.String(), "topsecret") || strings.Contains(out.String(), "topsecret") {
		t.Fatalf("proxy credentials leaked! out=%s err=%s", out.String(), errOut.String())
	}
	if !strings.Contains(errOut.String(), "retrying direct over IPv4") {
		t.Fatalf("expected direct IPv4 fallback notice, got:\n%s", errOut.String())
	}
	got, _ := os.ReadFile(exe)
	if string(got) != newScript {
		t.Fatalf("binary not updated via direct fallback: %q", string(got))
	}
	mu.Lock()
	defer mu.Unlock()
	sawTCP4 := false
	for _, d := range dialed {
		if strings.HasPrefix(d, "tcp4->") {
			sawTCP4 = true
			break
		}
	}
	if !sawTCP4 {
		t.Fatalf("expected direct tcp4 dial after proxy failure, got %v", dialed)
	}
}

func TestCLIUpdateServiceRestartLinuxAndDarwin(t *testing.T) {
	t.Run("linux restarts active matching collie-hub and skips inactive or different-binary unit", func(t *testing.T) {
		saveUpdateCLIHooks(t)
		updateGOOS = func() string { return "linux" }

		dir := t.TempDir()
		exe := filepath.Join(dir, "collie")
		otherExe := filepath.Join(dir, "other-collie")
		_ = os.WriteFile(exe, []byte("x"), 0o755)
		_ = os.WriteFile(otherExe, []byte("x"), 0o755)

		var calls []string
		updateRunCmd = func(ctx context.Context, name string, args ...string) ([]byte, error) {
			cmd := name + " " + strings.Join(args, " ")
			calls = append(calls, cmd)
			switch cmd {
			case "systemctl --user is-active collie-hub":
				return []byte("active\n"), nil
			case "systemctl --user show -p ExecStart --value collie-hub":
				return []byte("{ path=" + exe + " ; argv[]=" + exe + " -addr :7788 }\n"), nil
			case "systemctl --user restart collie-hub":
				return nil, nil
			case "systemctl --user is-active collie-node":
				return []byte("active\n"), nil
			case "systemctl --user show -p ExecStart --value collie-node":
				return []byte("{ path=" + otherExe + " ; argv[]=" + otherExe + " node }\n"), nil
			default:
				return nil, fmt.Errorf("unexpected cmd: %s", cmd)
			}
		}

		var out bytes.Buffer
		restarted, err := restartLocalServices(context.Background(), exe, &out)
		if err != nil {
			t.Fatalf("restartLinuxServices failed: %v", err)
		}
		if len(restarted) != 1 || restarted[0] != "collie-hub" {
			t.Fatalf("restarted = %v, want [collie-hub]", restarted)
		}
		if !strings.Contains(out.String(), "Restarted service collie-hub (systemd --user)") {
			t.Fatalf("missing restart log: %s", out.String())
		}
		for _, c := range calls {
			if c == "systemctl --user restart collie-node" {
				t.Fatalf("must not restart unit pointing to a different binary; calls=%v", calls)
			}
		}
	})

	t.Run("darwin kickstart success", func(t *testing.T) {
		saveUpdateCLIHooks(t)
		updateGOOS = func() string { return "darwin" }
		home := t.TempDir()
		updateHomeDir = func() (string, error) { return home, nil }
		updateUID = func() string { return "501" }

		exe := filepath.Join(home, ".local/bin/collie")
		plistDir := filepath.Join(home, "Library/LaunchAgents")
		_ = os.MkdirAll(plistDir, 0o755)
		plist := filepath.Join(plistDir, "com.collie.node.plist")
		_ = os.WriteFile(plist, []byte("<plist><dict><key>ProgramArguments</key><array><string>"+exe+"</string><string>node</string></array></dict></plist>"), 0o644)

		var calls []string
		updateRunCmd = func(ctx context.Context, name string, args ...string) ([]byte, error) {
			cmd := name + " " + strings.Join(args, " ")
			calls = append(calls, cmd)
			switch cmd {
			case "launchctl print gui/501/com.collie.node":
				return []byte("gui/501/com.collie.node = {\n state = running\n}\n"), nil
			case "launchctl kickstart -k gui/501/com.collie.node":
				return nil, nil
			case "launchctl print gui/501/com.collie.hub":
				return []byte("Could not find service"), errors.New("exit status 113")
			default:
				return nil, fmt.Errorf("unexpected cmd: %s", cmd)
			}
		}

		var out bytes.Buffer
		restarted, err := restartLocalServices(context.Background(), exe, &out)
		if err != nil {
			t.Fatalf("darwin kickstart failed: %v", err)
		}
		if len(restarted) != 1 || restarted[0] != "com.collie.node" {
			t.Fatalf("restarted = %v, want [com.collie.node]", restarted)
		}
		if !strings.Contains(out.String(), "Restarted service com.collie.node (launchd)") {
			t.Fatalf("unexpected output: %s", out.String())
		}
	})

	t.Run("darwin LWCR/EX_CONFIG falls back to bootout + bootstrap", func(t *testing.T) {
		saveUpdateCLIHooks(t)
		updateGOOS = func() string { return "darwin" }
		home := t.TempDir()
		updateHomeDir = func() (string, error) { return home, nil }
		updateUID = func() string { return "501" }

		exe := filepath.Join(home, ".local/bin/collie")
		plistDir := filepath.Join(home, "Library/LaunchAgents")
		_ = os.MkdirAll(plistDir, 0o755)
		plist := filepath.Join(plistDir, "com.collie.node.plist")
		_ = os.WriteFile(plist, []byte("<plist><dict><key>ProgramArguments</key><array><string>"+exe+"</string><string>node</string></array></dict></plist>"), 0o644)

		bootedOut := false
		var calls []string
		updateRunCmd = func(ctx context.Context, name string, args ...string) ([]byte, error) {
			cmd := name + " " + strings.Join(args, " ")
			calls = append(calls, cmd)
			switch cmd {
			case "launchctl print gui/501/com.collie.node":
				if bootedOut {
					return []byte("Could not find service"), errors.New("exit status 113")
				}
				return []byte("state = running"), nil
			case "launchctl kickstart -k gui/501/com.collie.node":
				return []byte("Could not kickstart service \"gui/501/com.collie.node\": 78: EX_CONFIG (LWCR)"), errors.New("exit status 78")
			case "launchctl bootout gui/501/com.collie.node":
				bootedOut = true
				return nil, nil
			case "launchctl bootstrap gui/501 " + plist:
				return nil, nil
			case "launchctl print gui/501/com.collie.hub":
				return []byte("Could not find service"), errors.New("exit status 113")
			default:
				return nil, fmt.Errorf("unexpected cmd: %s", cmd)
			}
		}

		var out bytes.Buffer
		restarted, err := restartLocalServices(context.Background(), exe, &out)
		if err != nil {
			t.Fatalf("darwin bootout+bootstrap fallback failed: %v", err)
		}
		if len(restarted) != 1 || restarted[0] != "com.collie.node" {
			t.Fatalf("restarted = %v, want [com.collie.node]", restarted)
		}
		if !strings.Contains(out.String(), "launchd bootout+bootstrap") {
			t.Fatalf("expected bootout+bootstrap message, got: %s", out.String())
		}
		joined := strings.Join(calls, "\n")
		for _, want := range []string{
			"launchctl kickstart -k gui/501/com.collie.node",
			"launchctl bootout gui/501/com.collie.node",
			"launchctl bootstrap gui/501 " + plist,
		} {
			if !strings.Contains(joined, want) {
				t.Fatalf("missing call %q in:\n%s", want, joined)
			}
		}
	})
}

func TestCLIUpdateAllFleetOrderRelayAndSummary(t *testing.T) {
	saveUpdateCLIHooks(t)

	var mu sync.Mutex
	var updateOrder []string
	var hubUpgraded atomic.Bool

	hubTS := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer test-tok" {
			http.Error(w, `{"error":"unauthorized"}`, http.StatusUnauthorized)
			return
		}
		switch r.URL.Path {
		case "/api/version":
			hubVer := "v0.3.2"
			if hubUpgraded.Load() {
				hubVer = "v0.3.4"
			}
			writeJSON(w, 200, versionAPIResp{
				Version: hubVer,
				Latest:  "v0.3.4",
				Machines: []versionMachine{
					{ID: "", Name: "hub-box", Version: hubVer, Local: true, Online: true, Outdated: hubVer != "v0.3.4"},
					{ID: "mac", Name: "mac", Version: "v0.3.2", Local: false, Online: true, Outdated: true},
					{ID: "devbox", Name: "devbox", Version: "v0.3.4", Local: false, Online: true, Outdated: false},
					{ID: "linux-b", Name: "linux-b", Version: "v0.3.1", Local: false, Online: true, Outdated: true},
					{ID: "laptop-off", Name: "laptop-off", Version: "v0.3.0", Local: false, Online: false, Outdated: false},
				},
			})
		case "/api/update":
			m := r.URL.Query().Get("machine")
			mu.Lock()
			if m == "" {
				updateOrder = append(updateOrder, "hub")
			} else {
				updateOrder = append(updateOrder, m)
			}
			mu.Unlock()

			switch m {
			case "mac":
				writeJSON(w, 200, map[string]any{"ok": true, "from": "v0.3.2", "version": "v0.3.4", "relayed": true})
			case "linux-b":
				writeJSON(w, 200, map[string]any{"ok": true, "from": "v0.3.1", "version": "v0.3.4"})
			case "":
				hubUpgraded.Store(true)
				writeJSON(w, 200, map[string]any{"ok": true, "from": "v0.3.2", "version": "v0.3.4"})
			default:
				writeJSON(w, 400, map[string]any{"error": "unexpected machine " + m})
			}
		default:
			http.NotFound(w, r)
		}
	}))
	defer hubTS.Close()

	updateCLITarget = func() (string, string, error) { return hubTS.URL, "test-tok", nil }

	// 1) --all --check is strictly read-only.
	var out, errOut bytes.Buffer
	if err := runUpdateCLI("update", []string{"--all", "--check"}, &out, &errOut); err != nil {
		t.Fatalf("update --all --check failed: %v", err)
	}
	if len(updateOrder) != 0 {
		t.Fatalf("--all --check must not POST /api/update, got %v", updateOrder)
	}
	for _, want := range []string{"Latest release: v0.3.4", "hub-box", "mac", "offline", "update available"} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("--all --check missing %q:\n%s", want, out.String())
		}
	}

	// 2) --all updates all outdated online remote nodes FIRST, then Hub LAST; skips up-to-date & offline.
	out.Reset()
	if err := runUpdateCLI("update", []string{"--all"}, &out, &errOut); err != nil {
		t.Fatalf("update --all failed: %v\n%s", err, out.String())
	}
	wantOrder := []string{"mac", "linux-b", "hub"}
	if strings.Join(updateOrder, ",") != strings.Join(wantOrder, ",") {
		t.Fatalf("update order = %v, want %v (remote nodes first, hub last, skip up-to-date)", updateOrder, wantOrder)
	}
	for _, want := range []string{
		"mac", "(via Hub relay)",
		"linux-b", "v0.3.1 → v0.3.4",
		"devbox", "already up to date",
		"laptop-off", "offline (skipped, last: v0.3.0)",
		"hub-box", "v0.3.2 → v0.3.4 (restarted)",
		"Summary (3 updated, 1 up to date, 0 skipped, 1 offline, 0 failed)",
	} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("--all output missing %q:\n%s", want, out.String())
		}
	}
}

func TestCLIUpdateAllPartialFailureContinuesAndReturnsError(t *testing.T) {
	saveUpdateCLIHooks(t)

	hubTS := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/version":
			writeJSON(w, 200, versionAPIResp{
				Version: "v0.3.2",
				Latest:  "v0.3.4",
				Machines: []versionMachine{
					{ID: "", Name: "hub-box", Version: "v0.3.2", Local: true, Online: true, Outdated: true},
					{ID: "bad-node", Name: "bad-node", Version: "v0.3.2", Local: false, Online: true, Outdated: true},
					{ID: "good-node", Name: "good-node", Version: "v0.3.2", Local: false, Online: true, Outdated: true},
				},
			})
		case "/api/update":
			switch r.URL.Query().Get("machine") {
			case "bad-node":
				writeJSON(w, 502, map[string]any{"error": "更新失败：程序所在目录无写权限（/usr/local/bin）"})
			case "good-node":
				writeJSON(w, 200, map[string]any{"ok": true, "from": "v0.3.2", "version": "v0.3.4"})
			case "":
				writeJSON(w, 200, map[string]any{"ok": true, "from": "v0.3.2", "version": "v0.3.4"})
			}
		}
	}))
	defer hubTS.Close()

	updateCLITarget = func() (string, string, error) { return hubTS.URL, "tok", nil }
	cliUpdateReconnectWait = 0

	var out, errOut bytes.Buffer
	err := runUpdateCLI("update", []string{"--all"}, &out, &errOut)
	if err == nil || !strings.Contains(err.Error(), "1 machine(s) failed") {
		t.Fatalf("partial failure err = %v, want 1 machine(s) failed", err)
	}
	for _, want := range []string{"bad-node", "无写权限", "good-node", "hub-box", "1 failed"} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("partial failure output missing %q:\n%s", want, out.String())
		}
	}
}

func TestMachineTokenAuthForVersionAndUpdate(t *testing.T) {
	saveUpdateCLIHooks(t)
	const master = "master-secret-token"
	_, _, h := newTestAuthServer(t, master)

	// Mint a paired machine token.
	cRec := doJSONReq(t, h, http.MethodPost, "/api/pair/new", map[string]any{"kind": "machine"}, "Bearer "+master)
	if cRec.Code != 200 {
		t.Fatalf("pair/new = %d", cRec.Code)
	}
	var cRes struct{ Code string }
	_ = json.Unmarshal(cRec.Body.Bytes(), &cRes)
	pRec := doJSONReq(t, h, http.MethodPost, "/api/pair", map[string]any{"code": cRes.Code, "kind": "machine", "name": "mac"}, "")
	if pRec.Code != 200 {
		t.Fatalf("pair = %d: %s", pRec.Code, pRec.Body.String())
	}
	var pRes struct{ Token string }
	_ = json.Unmarshal(pRec.Body.Bytes(), &pRes)
	machineAuth := "Bearer " + pRes.Token

	// Prevent /api/version from hitting real GitHub.
	latestRel.once.Do(func() {})
	latestRel.Lock()
	origBusy := latestRel.busy
	latestRel.tag = "v0.3.4"
	latestRel.busy = true
	latestRel.Unlock()
	t.Cleanup(func() {
		latestRel.Lock()
		latestRel.busy = origBusy
		latestRel.Unlock()
	})
	version = "v0.3.4"

	// Machine token is allowed on GET /api/version and POST /api/update (for `collie update --all`),
	// while still blocked from web-only endpoints like GET /api/devices and POST /api/login.
	if r := doJSONReq(t, h, http.MethodGet, "/api/version", nil, machineAuth); r.Code != 200 {
		t.Fatalf("machine GET /api/version = %d, want 200: %s", r.Code, r.Body.String())
	}
	ts, _, _, _ := makeFakeReleaseServer(t, "v0.3.4", "#!/bin/sh\necho collie v0.3.4\n", nil)
	releaseRepoURL = ts.URL
	if r := doJSONReq(t, h, http.MethodPost, "/api/update", nil, machineAuth); r.Code != 200 {
		t.Fatalf("machine POST /api/update = %d, want 200: %s", r.Code, r.Body.String())
	}
	if r := doJSONReq(t, h, http.MethodGet, "/api/devices", nil, machineAuth); r.Code != 401 {
		t.Fatalf("machine GET /api/devices = %d, want 401", r.Code)
	}
	if r := doJSONReq(t, h, http.MethodPost, "/api/login", map[string]any{"token": pRes.Token}, ""); r.Code != 401 {
		t.Fatalf("machine POST /api/login = %d, want 401", r.Code)
	}

	// Sanity check: make sure io import stays used via unused-free pattern if needed.
	_ = io.Discard
}
