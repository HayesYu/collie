package collie

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// Workers inside the claude-sg container cannot run collie themselves: the
// collie on the Mac is a darwin binary, and the container has no route to the
// hub. A shim on the container's PATH (`collie`) drops each call as a request
// file under <state>/cli/<pane>/; the bridge of that pane runs it with the
// real collie on the Mac (with the pane's HERDR_PANE_ID, so `report` knows who
// is talking) and writes the result back next to it.

const (
	claudeSGCLITimeout = 45 * time.Second
	claudeSGCLIStale   = 2 * time.Minute
	claudeSGCLIMaxOut  = 1 << 20
)

// claudeSGContainerCollie is what Collie tells claude workers on a machine
// with claude-sg to run: the shim on the container's PATH.
const claudeSGContainerCollie = "collie"

type claudeSGCLIRequest struct {
	Args  []string `json:"args"`
	Stdin string   `json:"stdin,omitempty"`
}

type claudeSGCLIResponse struct {
	Code   int    `json:"code"`
	Stdout string `json:"stdout"`
	Stderr string `json:"stderr"`
}

func claudeSGCLIDir(stateDir, paneID string) string {
	if stateDir == "" || paneID == "" {
		return ""
	}
	return filepath.Join(stateDir, "cli", safePaneFileName(paneID))
}

// claudeSGCLIAllowed: the tool surface a session may use, not hub setup.
func claudeSGCLIAllowed(args []string) bool {
	if len(args) == 0 {
		return false
	}
	switch args[0] {
	case "hook", "pair":
		return false
	}
	_, ok := cliCommands[args[0]]
	return ok
}

func isSafeRequestID(id string) bool {
	if id == "" || len(id) > 64 {
		return false
	}
	for i := 0; i < len(id); i++ {
		c := id[i]
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-' || c == '_') {
			return false
		}
	}
	return true
}

// watchClaudeSGCLI serves the pane's requests until done closes.
func watchClaudeSGCLI(done <-chan struct{}, dir, collieBin string, env []string) {
	if dir == "" {
		<-done
		return
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		<-done
		return
	}
	defer os.RemoveAll(dir) // no bridge, no directory: the shim says so at once
	tick := time.NewTicker(200 * time.Millisecond)
	defer tick.Stop()
	for {
		serveClaudeSGCLIOnce(dir, collieBin, env)
		select {
		case <-done:
			return
		case <-tick.C:
		}
	}
}

func serveClaudeSGCLIOnce(dir, collieBin string, env []string) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, e := range entries {
		id, ok := strings.CutSuffix(e.Name(), ".req.json")
		if !ok || !isSafeRequestID(id) {
			continue
		}
		req := filepath.Join(dir, e.Name())
		claimed := filepath.Join(dir, id+".work")
		if os.Rename(req, claimed) != nil {
			continue
		}
		info, statErr := os.Stat(claimed)
		b, readErr := os.ReadFile(claimed)
		os.Remove(claimed)
		if statErr != nil || readErr != nil || time.Since(info.ModTime()) > claudeSGCLIStale {
			continue // its caller has given up
		}
		var r claudeSGCLIRequest
		var resp claudeSGCLIResponse
		if json.Unmarshal(b, &r) != nil {
			resp = claudeSGCLIResponse{Code: 2, Stderr: "collie: malformed request from the claude-sg shim\n"}
		} else {
			resp = runClaudeSGCLI(collieBin, env, r)
		}
		writeClaudeSGCLIResponse(dir, id, resp)
	}
}

func runClaudeSGCLI(collieBin string, env []string, r claudeSGCLIRequest) claudeSGCLIResponse {
	if !claudeSGCLIAllowed(r.Args) {
		name := ""
		if len(r.Args) > 0 {
			name = r.Args[0]
		}
		return claudeSGCLIResponse{Code: 2, Stderr: "collie: `" + name + "` is not available from inside claude-sg\n"}
	}
	ctx, cancel := context.WithTimeout(context.Background(), claudeSGCLITimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, collieBin, r.Args...)
	cmd.Env = env
	cmd.Stdin = strings.NewReader(r.Stdin)
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	err := cmd.Run()
	resp := claudeSGCLIResponse{Stdout: clipCLIOutput(out.String()), Stderr: clipCLIOutput(errb.String())}
	var ee *exec.ExitError
	switch {
	case err == nil:
	case errors.As(err, &ee):
		resp.Code = ee.ExitCode()
		if resp.Code < 0 { // killed by the timeout
			resp.Code = 1
			resp.Stderr += "collie: timed out on the Mac side\n"
		}
	default:
		resp.Code = 1
		resp.Stderr += "collie: " + err.Error() + "\n"
	}
	return resp
}

func clipCLIOutput(s string) string {
	if len(s) > claudeSGCLIMaxOut {
		return s[:claudeSGCLIMaxOut] + "\n…(truncated)\n"
	}
	return s
}

func writeClaudeSGCLIResponse(dir, id string, resp claudeSGCLIResponse) {
	b, _ := json.Marshal(resp)
	tmp := filepath.Join(dir, "."+id+".resp.tmp")
	if os.WriteFile(tmp, b, 0o600) != nil {
		return
	}
	if os.Rename(tmp, filepath.Join(dir, id+".resp.json")) != nil {
		os.Remove(tmp)
	}
}
