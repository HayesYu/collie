package collie

import (
	"context"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// TestInstallScriptPortHelper is invoked as a subprocess by
// TestInstallScriptContainerFallback to act as a realistic long-lived server
// that binds a TCP port and fails fast with "address already in use" if an old
// process was not properly terminated.
func TestInstallScriptPortHelper(t *testing.T) {
	addr := os.Getenv("COLLIE_TEST_LISTEN_ADDR")
	if addr == "" {
		return
	}
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		fmt.Fprintf(os.Stderr, "listen %s: %v\n", addr, err)
		os.Exit(1)
	}
	defer ln.Close()
	for {
		c, err := ln.Accept()
		if err != nil {
			time.Sleep(50 * time.Millisecond)
			continue
		}
		_ = c.Close()
	}
}

func TestInstallScriptContainerFallback(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("Linux no-systemd container fallback test")
	}
	scriptPath, err := filepath.Abs(filepath.Join("..", "..", "docs", "install.sh"))
	if err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command("sh", "-n", scriptPath).CombinedOutput(); err != nil {
		t.Fatalf("sh -n docs/install.sh failed: %v\n%s", err, string(out))
	}

	testBin, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}

	writeStub := func(t *testing.T, path string) {
		t.Helper()
		body := fmt.Sprintf("#!/bin/sh\nif [ \"${1:-}\" = \"version\" ]; then echo 'collie v0.2.82-test'; exit 0; fi\nexec %q -test.run=^TestInstallScriptPortHelper$\n", testBin)
		if err := os.WriteFile(path, []byte(body), 0o755); err != nil {
			t.Fatal(err)
		}
	}

	readPID := func(t *testing.T, pidFile string) int {
		t.Helper()
		b, err := os.ReadFile(pidFile)
		if err != nil {
			t.Fatalf("read pid file %s: %v", pidFile, err)
		}
		pid, err := strconv.Atoi(strings.TrimSpace(string(b)))
		if err != nil || pid <= 0 {
			t.Fatalf("invalid pid %q in %s", string(b), pidFile)
		}
		return pid
	}

	killLater := func(t *testing.T, pid int) {
		t.Helper()
		t.Cleanup(func() {
			if pid > 0 {
				_ = syscall.Kill(pid, syscall.SIGKILL)
			}
		})
	}

	reservePort := func(t *testing.T) string {
		t.Helper()
		ln, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		addr := ln.Addr().String()
		_ = ln.Close()
		return addr
	}

	assertDetachedSession := func(t *testing.T, pid int) {
		t.Helper()
		if _, err := exec.LookPath("setsid"); err != nil {
			return
		}
		out, err := exec.Command("ps", "-o", "pgid=,sid=", "-p", strconv.Itoa(pid)).Output()
		if err != nil {
			t.Fatalf("ps for pid %d failed: %v", pid, err)
		}
		fields := strings.Fields(string(out))
		if len(fields) != 2 {
			t.Fatalf("unexpected ps output for pid %d: %q", pid, string(out))
		}
		want := strconv.Itoa(pid)
		if fields[0] != want || fields[1] != want {
			t.Fatalf("pid %d not detached into own session/pgid: pgid=%s sid=%s", pid, fields[0], fields[1])
		}
	}

	t.Run("systemctl_timeout_prevents_hang", func(t *testing.T) {
		if _, err := exec.LookPath("timeout"); err != nil {
			t.Skip("timeout command not available")
		}
		tmp := t.TempDir()
		homeDir := filepath.Join(tmp, "home")
		binDir := filepath.Join(tmp, "bin")
		mockDir := filepath.Join(tmp, "mock")
		stubBin := filepath.Join(tmp, "collie-stub")
		for _, d := range []string{homeDir, mockDir} {
			if err := os.MkdirAll(d, 0o755); err != nil {
				t.Fatal(err)
			}
		}
		writeStub(t, stubBin)
		// Simulate a container where `systemctl --user show-environment` hangs on D-Bus.
		if err := os.WriteFile(filepath.Join(mockDir, "systemctl"), []byte("#!/bin/sh\nexec sleep 30\n"), 0o755); err != nil {
			t.Fatal(err)
		}

		listenAddr := reservePort(t)
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		cmd := exec.CommandContext(ctx, "sh", scriptPath, "hub", "--addr", listenAddr)
		cmd.Env = []string{
			"PATH=" + mockDir + ":" + os.Getenv("PATH"),
			"HOME=" + homeDir,
			"COLLIE_BIN_DIR=" + binDir,
			"COLLIE_BINARY=" + stubBin,
			"COLLIE_NO_HERDR=1",
			"COLLIE_NO_OPEN=1",
			"COLLIE_TEST_LISTEN_ADDR=" + listenAddr,
		}
		start := time.Now()
		out, err := cmd.CombinedOutput()
		elapsed := time.Since(start)
		if err != nil {
			t.Fatalf("install.sh hub failed after %v: %v\n%s", elapsed, err, string(out))
		}
		if elapsed > 12*time.Second {
			t.Fatalf("install.sh took too long (%v), expected timeout 3s fallback", elapsed)
		}
		pid := readPID(t, filepath.Join(homeDir, ".collie", "hub.pid"))
		killLater(t, pid)
		if !strings.Contains(string(out), "没有 systemd 用户服务") {
			t.Fatalf("expected no-systemd fallback message:\n%s", string(out))
		}
		assertDetachedSession(t, pid)
	})

	t.Run("reinstall_kills_sigstopped_old_pid_and_rebinds_port", func(t *testing.T) {
		tmp := t.TempDir()
		homeDir := filepath.Join(tmp, "home")
		binDir := filepath.Join(tmp, "bin")
		mockDir := filepath.Join(tmp, "mock")
		stubBin := filepath.Join(tmp, "collie-stub")
		for _, d := range []string{homeDir, mockDir} {
			if err := os.MkdirAll(d, 0o755); err != nil {
				t.Fatal(err)
			}
		}
		writeStub(t, stubBin)
		if err := os.WriteFile(filepath.Join(mockDir, "systemctl"), []byte("#!/bin/sh\nexit 1\n"), 0o755); err != nil {
			t.Fatal(err)
		}

		listenAddr := reservePort(t)
		runHub := func() string {
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, "sh", scriptPath, "hub", "--addr", listenAddr)
			cmd.Env = []string{
				"PATH=" + mockDir + ":" + os.Getenv("PATH"),
				"HOME=" + homeDir,
				"COLLIE_BIN_DIR=" + binDir,
				"COLLIE_BINARY=" + stubBin,
				"COLLIE_NO_HERDR=1",
				"COLLIE_NO_OPEN=1",
				"COLLIE_TEST_LISTEN_ADDR=" + listenAddr,
			}
			out, err := cmd.CombinedOutput()
			if err != nil {
				logBytes, _ := os.ReadFile(filepath.Join(homeDir, ".collie", "hub.log"))
				t.Fatalf("install.sh hub failed: %v\noutput:\n%s\nhub.log:\n%s", err, string(out), string(logBytes))
			}
			return string(out)
		}

		// 1) Initial install starts hub binding listenAddr.
		runHub()
		pidFile := filepath.Join(homeDir, ".collie", "hub.pid")
		oldPID := readPID(t, pidFile)
		killLater(t, oldPID)
		assertDetachedSession(t, oldPID)

		// Verify the initial server really bound listenAddr.
		conn, err := net.DialTimeout("tcp", listenAddr, time.Second)
		if err != nil {
			t.Fatalf("initial hub not listening on %s: %v", listenAddr, err)
		}
		_ = conn.Close()

		// 2) Suspend oldPID with SIGSTOP (T state, ignores SIGTERM just like Ctrl+Z).
		if err := syscall.Kill(oldPID, syscall.SIGSTOP); err != nil {
			t.Fatalf("SIGSTOP old pid %d failed: %v", oldPID, err)
		}
		deadline := time.Now().Add(2 * time.Second)
		stopped := false
		for time.Now().Before(deadline) {
			statOut, _ := exec.Command("ps", "-o", "stat=", "-p", strconv.Itoa(oldPID)).Output()
			if strings.HasPrefix(strings.TrimSpace(string(statOut)), "T") {
				stopped = true
				break
			}
			time.Sleep(50 * time.Millisecond)
		}
		if !stopped {
			t.Fatalf("old pid %d did not enter T (stopped) state", oldPID)
		}

		// 3) Reinstall must escalate to kill -9, free listenAddr, and start a new PID.
		runHub()
		newPID := readPID(t, pidFile)
		killLater(t, newPID)
		if newPID == oldPID {
			t.Fatalf("expected a new pid after reinstall, still got %d", newPID)
		}
		if err := syscall.Kill(oldPID, 0); err == nil {
			t.Fatalf("old SIGSTOP'd pid %d is still alive after reinstall", oldPID)
		}
		if err := syscall.Kill(newPID, 0); err != nil {
			t.Fatalf("new pid %d is not alive: %v", newPID, err)
		}
		assertDetachedSession(t, newPID)

		conn2, err := net.DialTimeout("tcp", listenAddr, time.Second)
		if err != nil {
			t.Fatalf("reinstalled hub not listening on %s: %v", listenAddr, err)
		}
		_ = conn2.Close()
	})

	t.Run("fallback_without_setsid", func(t *testing.T) {
		tmp := t.TempDir()
		homeDir := filepath.Join(tmp, "home")
		binDir := filepath.Join(tmp, "bin")
		cleanPathDir := filepath.Join(tmp, "cleanbin")
		stubBin := filepath.Join(tmp, "collie-stub")
		for _, d := range []string{homeDir, cleanPathDir} {
			if err := os.MkdirAll(d, 0o755); err != nil {
				t.Fatal(err)
			}
		}
		writeStub(t, stubBin)

		// Build a PATH directory with all standard commands except setsid and systemctl.
		seen := map[string]bool{"setsid": true, "systemctl": true}
		for _, dir := range filepath.SplitList(os.Getenv("PATH")) {
			entries, err := os.ReadDir(dir)
			if err != nil {
				continue
			}
			for _, e := range entries {
				name := e.Name()
				if seen[name] {
					continue
				}
				seen[name] = true
				_ = os.Symlink(filepath.Join(dir, name), filepath.Join(cleanPathDir, name))
			}
		}

		listenAddr := reservePort(t)
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		cmd := exec.CommandContext(ctx, "sh", scriptPath, "hub", "--addr", listenAddr)
		cmd.Env = []string{
			"PATH=" + cleanPathDir,
			"HOME=" + homeDir,
			"COLLIE_BIN_DIR=" + binDir,
			"COLLIE_BINARY=" + stubBin,
			"COLLIE_NO_HERDR=1",
			"COLLIE_NO_OPEN=1",
			"COLLIE_TEST_LISTEN_ADDR=" + listenAddr,
		}
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("install.sh hub without setsid failed: %v\n%s", err, string(out))
		}
		pid := readPID(t, filepath.Join(homeDir, ".collie", "hub.pid"))
		killLater(t, pid)
		if err := syscall.Kill(pid, 0); err != nil {
			t.Fatalf("started pid %d not alive: %v", pid, err)
		}
	})
}

func TestInstallScriptDarwinLaunchdProcessTypeAndBootoutRetry(t *testing.T) {
	if runtime.GOOS != "linux" && runtime.GOOS != "darwin" {
		t.Skip("POSIX shell test")
	}
	scriptPath, err := filepath.Abs(filepath.Join("..", "..", "docs", "install.sh"))
	if err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command("sh", "-n", scriptPath).CombinedOutput(); err != nil {
		t.Fatalf("sh -n docs/install.sh failed: %v\n%s", err, string(out))
	}

	tmp := t.TempDir()
	homeDir := filepath.Join(tmp, "home")
	binDir := filepath.Join(tmp, "bin")
	mockDir := filepath.Join(tmp, "mock")
	stubBin := filepath.Join(tmp, "collie-stub")
	stateDir := filepath.Join(tmp, "state")
	for _, d := range []string{homeDir, mockDir, stateDir} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(stubBin, []byte("#!/bin/sh\necho collie v0.2.87-test\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	// Mock uname to report Darwin/arm64 so docs/install.sh exercises the macOS launchd branch.
	unameStub := "#!/bin/sh\ncase \"${1:-}\" in\n  -s) echo Darwin ;;\n  -m) echo arm64 ;;\n  *) echo Darwin ;;\nesac\n"
	if err := os.WriteFile(filepath.Join(mockDir, "uname"), []byte(unameStub), 0o755); err != nil {
		t.Fatal(err)
	}
	// Mock launchctl: print reports job still present twice after bootout (async teardown),
	// and bootstrap fails twice before succeeding on attempt 3.
	launchctlStub := fmt.Sprintf(`#!/bin/sh
set -eu
state=%q
log="${state}/calls.log"
printf '%%s\n' "$*" >> "${log}"
sub="${1:-}"
case "${sub}" in
  bootout)
    echo 0 > "${state}/print_count"
    exit 0
    ;;
  print)
    c=$(cat "${state}/print_count" 2>/dev/null || echo 0)
    c=$((c + 1))
    echo "${c}" > "${state}/print_count"
    if [ "${c}" -le 2 ]; then
      exit 0
    fi
    exit 113
    ;;
  bootstrap)
    c=$(cat "${state}/boot_count" 2>/dev/null || echo 0)
    c=$((c + 1))
    echo "${c}" > "${state}/boot_count"
    if [ "${c}" -le 2 ]; then
      echo "Bootstrap failed: 5: Input/output error" >&2
      exit 5
    fi
    exit 0
    ;;
  *)
    exit 0
    ;;
esac
`, stateDir)
	if err := os.WriteFile(filepath.Join(mockDir, "launchctl"), []byte(launchctlStub), 0o755); err != nil {
		t.Fatal(err)
	}

	// 1) Dry-run plist must use ProcessType=Interactive (not Background).
	dryCmd := exec.Command("sh", scriptPath, "hub")
	dryCmd.Env = []string{
		"PATH=" + mockDir + ":" + os.Getenv("PATH"),
		"HOME=" + homeDir,
		"COLLIE_BIN_DIR=" + binDir,
		"COLLIE_BINARY=" + stubBin,
		"COLLIE_DRY_SERVICE=1",
		"COLLIE_NO_HERDR=1",
		"COLLIE_NO_OPEN=1",
	}
	dryOut, err := dryCmd.CombinedOutput()
	if err != nil {
		t.Fatalf("dry darwin install.sh failed: %v\n%s", err, string(dryOut))
	}
	if !strings.Contains(string(dryOut), "<key>ProcessType</key><string>Interactive</string>") {
		t.Fatalf("expected ProcessType=Interactive in plist, got:\n%s", string(dryOut))
	}
	if strings.Contains(string(dryOut), "<string>Background</string>") {
		t.Fatalf("plist must not use Background ProcessType:\n%s", string(dryOut))
	}

	// 2) Full darwin service install waits for async bootout teardown via print and retries bootstrap.
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	runCmd := exec.CommandContext(ctx, "sh", scriptPath, "hub")
	runCmd.Env = []string{
		"PATH=" + mockDir + ":" + os.Getenv("PATH"),
		"HOME=" + homeDir,
		"COLLIE_BIN_DIR=" + binDir,
		"COLLIE_BINARY=" + stubBin,
		"COLLIE_NO_HERDR=1",
		"COLLIE_NO_OPEN=1",
	}
	out, err := runCmd.CombinedOutput()
	if err != nil {
		t.Fatalf("darwin install.sh failed: %v\n%s", err, string(out))
	}
	callsBytes, err := os.ReadFile(filepath.Join(stateDir, "calls.log"))
	if err != nil {
		t.Fatalf("read launchctl calls: %v", err)
	}
	calls := string(callsBytes)
	if !strings.Contains(calls, "bootout gui/") {
		t.Fatalf("expected launchctl bootout call, got:\n%s", calls)
	}
	if strings.Count(calls, "print gui/") != 3 {
		t.Fatalf("expected 3 launchctl print polls (2 alive + 1 gone), got:\n%s", calls)
	}
	if strings.Count(calls, "bootstrap gui/") != 3 {
		t.Fatalf("expected 3 launchctl bootstrap calls (2 failed + 1 succeeded), got:\n%s", calls)
	}
	plistPath := filepath.Join(homeDir, "Library", "LaunchAgents", "com.collie.hub.plist")
	plistBytes, err := os.ReadFile(plistPath)
	if err != nil {
		t.Fatalf("read generated plist: %v", err)
	}
	if !strings.Contains(string(plistBytes), "<key>ProcessType</key><string>Interactive</string>") {
		t.Fatalf("generated plist missing Interactive ProcessType:\n%s", string(plistBytes))
	}
}
