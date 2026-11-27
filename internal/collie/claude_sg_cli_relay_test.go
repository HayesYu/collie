package collie

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// fakeCollie writes a script that echoes its args, stdin and pane, and exits
// with the code in $FAKE_EXIT.
func fakeCollie(t *testing.T) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "collie")
	script := "#!/bin/sh\n" +
		"echo \"args=$*\"\n" +
		"echo \"pane=$HERDR_PANE_ID\"\n" +
		"echo \"stdin=$(cat)\"\n" +
		"echo oops >&2\n" +
		"[ \"$1\" = answer ] && exit 4\n" +
		"exit ${FAKE_EXIT:-0}\n"
	if err := os.WriteFile(p, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestRunClaudeSGCLI(t *testing.T) {
	bin := fakeCollie(t)
	env := []string{"PATH=" + os.Getenv("PATH"), "HERDR_PANE_ID=wA:p1"}
	r := runClaudeSGCLI(bin, env, claudeSGCLIRequest{Args: []string{"report", "done", "ok"}, Stdin: "from stdin"})
	if r.Code != 0 || !strings.Contains(r.Stdout, "args=report done ok") || !strings.Contains(r.Stdout, "pane=wA:p1") ||
		!strings.Contains(r.Stdout, "stdin=from stdin") || r.Stderr != "oops\n" {
		t.Fatalf("resp = %+v", r)
	}
	r = runClaudeSGCLI(bin, append(env, "FAKE_EXIT=3"), claudeSGCLIRequest{Args: []string{"show", "x"}})
	if r.Code != 3 {
		t.Fatalf("exit code = %d, want 3", r.Code)
	}
	for _, args := range [][]string{nil, {"hook", "x"}, {"pair"}, {"_bridge", "claude-sg"}, {"rm", "-rf"}} {
		if r := runClaudeSGCLI(bin, env, claudeSGCLIRequest{Args: args}); r.Code != 2 || r.Stdout != "" {
			t.Fatalf("%v must be refused: %+v", args, r)
		}
	}
}

func TestServeClaudeSGCLIRequestFiles(t *testing.T) {
	bin := fakeCollie(t)
	dir := claudeSGCLIDir(t.TempDir(), "wA:p1")
	if filepath.Base(dir) != "wA_p1" {
		t.Fatalf("dir = %s", dir)
	}
	done := make(chan struct{})
	served := make(chan struct{})
	go func() {
		watchClaudeSGCLI(done, dir, bin, []string{"PATH=" + os.Getenv("PATH"), "HERDR_PANE_ID=wA:p1"})
		close(served)
	}()
	waitFor(t, func() bool { return isDir(dir) })

	req, _ := json.Marshal(claudeSGCLIRequest{Args: []string{"report", "hi"}})
	os.WriteFile(filepath.Join(dir, "abc123.req.json"), req, 0o600)
	os.WriteFile(filepath.Join(dir, "../../evil.req.json"), req, 0o600) // outside: never looked at
	os.WriteFile(filepath.Join(dir, "bad id.req.json"), req, 0o600)     // unsafe id: ignored
	stale := filepath.Join(dir, "old1.req.json")
	os.WriteFile(stale, req, 0o600)
	old := time.Now().Add(-10 * time.Minute)
	os.Chtimes(stale, old, old)

	respPath := filepath.Join(dir, "abc123.resp.json")
	waitFor(t, func() bool { return isFile(respPath) })
	var resp claudeSGCLIResponse
	b, _ := os.ReadFile(respPath)
	if json.Unmarshal(b, &resp) != nil || resp.Code != 0 || !strings.Contains(resp.Stdout, "args=report hi") {
		t.Fatalf("resp = %s", b)
	}
	time.Sleep(300 * time.Millisecond)
	if isFile(filepath.Join(dir, "old1.resp.json")) {
		t.Fatal("stale request must not be run")
	}
	if !isFile(filepath.Join(dir, "bad id.req.json")) {
		t.Fatal("unsafe id should be left alone")
	}

	close(done)
	<-served
	if isDir(dir) {
		t.Fatal("bridge exit should remove its request dir")
	}
}

// TestClaudeSGShimRoundTrip runs the container-side shim against the bridge
// when COLLIE_TEST_SHIM points at it (claude-sg/bin/collie-shim).
func TestClaudeSGShimRoundTrip(t *testing.T) {
	shim := os.Getenv("COLLIE_TEST_SHIM")
	if shim == "" {
		t.Skip("COLLIE_TEST_SHIM not set")
	}
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("no python3")
	}
	bin := fakeCollie(t)
	state := t.TempDir()
	dir := claudeSGCLIDir(state, "w9:p2")
	done := make(chan struct{})
	defer close(done)
	go watchClaudeSGCLI(done, dir, bin, []string{"PATH=" + os.Getenv("PATH"), "HERDR_PANE_ID=w9:p2"})
	waitFor(t, func() bool { return isDir(dir) })

	cmd := exec.Command("python3", shim, "report", "all good")
	cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "HERDR_PANE_ID=w9:p2", "COLLIE_CLAUDE_SG_DIR=" + state}
	cmd.Stdin = strings.NewReader("")
	out, err := cmd.Output()
	if err != nil || !strings.Contains(string(out), "args=report all good") || !strings.Contains(string(out), "pane=w9:p2") {
		t.Fatalf("shim: err=%v out=%s", err, out)
	}

	cmd = exec.Command("python3", shim, "answer", "-")
	cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "HERDR_PANE_ID=w9:p2", "COLLIE_CLAUDE_SG_DIR=" + state}
	cmd.Stdin = strings.NewReader("piped text")
	out, err = cmd.Output()
	var ee *exec.ExitError
	if !strings.Contains(string(out), "stdin=piped text") || !errorsAs(err, &ee) || ee.ExitCode() != 4 {
		t.Fatalf("shim stdin/exit: err=%v out=%s", err, out)
	}

	cmd = exec.Command("python3", shim, "report", "x")
	cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "HERDR_PANE_ID=w0:p0", "COLLIE_CLAUDE_SG_DIR=" + state}
	if b, err := cmd.CombinedOutput(); err == nil || !strings.Contains(string(b), "no Collie bridge") {
		t.Fatalf("unserved pane: err=%v out=%s", err, b)
	}
}

func errorsAs(err error, target **exec.ExitError) bool {
	e, ok := err.(*exec.ExitError)
	if ok {
		*target = e
	}
	return ok
}

func waitFor(t *testing.T, ok func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !ok() {
		if time.Now().After(deadline) {
			t.Fatal("timed out waiting")
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func TestWorkerCollieBin(t *testing.T) {
	t.Setenv(claudeSGBinEnv, "")
	mac := &Machine{ID: "m1", Bin: "/Users/me/.local/bin/collie", ClaudeBin: "collie"}
	plain := &Machine{ID: "m2", Bin: "/home/x/.local/bin/collie"}
	local := &Machine{Local: true}
	for _, c := range []struct {
		m    *Machine
		kind string
		want string
	}{
		{mac, "claude", "collie"},
		{mac, "codex", "/Users/me/.local/bin/collie"},
		{plain, "claude", "/home/x/.local/bin/collie"},
		{&Machine{ID: "m3"}, "claude", "collie"},
		{local, "claude", "/hub/collie"},
	} {
		if got := workerCollieBin("/hub/collie", c.m, c.kind); got != c.want {
			t.Errorf("%s on %s = %q, want %q", c.kind, c.m.ID, got, c.want)
		}
	}
	t.Setenv(claudeSGBinEnv, "/usr/local/bin/claude-sg")
	if got := workerCollieBin("/hub/collie", local, "claude"); got != "collie" {
		t.Errorf("claude on a claude-sg hub = %q, want collie", got)
	}
	if got := workerCollieBin("/hub/collie", local, "codex"); got != "/hub/collie" {
		t.Errorf("codex on a claude-sg hub = %q", got)
	}
}
