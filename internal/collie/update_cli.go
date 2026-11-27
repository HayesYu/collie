package collie

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"
)

var (
	cliUpdateAllTimeout     = 5 * time.Minute
	cliUpdateVersionTimeout = 20 * time.Second
	cliUpdateVersionPoll    = 2 * time.Second
	cliUpdateVersionRetries = 3
	cliUpdateHubDelay       = 1 * time.Second
	cliUpdateReconnectWait  = 15 * time.Second
	cliUpdateReconnectPoll  = 1 * time.Second
	launchdBootoutPoll      = 250 * time.Millisecond
	launchdBootoutMaxTries  = 20
	launchdBootstrapRetries = 4
	launchdBootstrapDelay   = 1 * time.Second

	updateGOOS      = func() string { return runtime.GOOS }
	updateCLITarget = cliTarget
	updateHomeDir   = os.UserHomeDir
	updateUID       = func() string { return strconv.Itoa(os.Getuid()) }
	updateReadFile  = os.ReadFile
	updateStat      = os.Stat
	updateRunCmd    = func(ctx context.Context, name string, args ...string) ([]byte, error) {
		return exec.CommandContext(ctx, name, args...).CombinedOutput()
	}
)

type updateCLIOptions struct {
	check bool
	all   bool
	force bool
}

func cliUpdate(args []string) error {
	return runUpdateCLI("update", args, os.Stdout, os.Stderr)
}

func cliUpgrade(args []string) error {
	return runUpdateCLI("upgrade", args, os.Stdout, os.Stderr)
}

func parseUpdateArgs(cmdName string, args []string, out io.Writer) (updateCLIOptions, bool, error) {
	if cmdName == "" {
		cmdName = "update"
	}
	fs := flag.NewFlagSet(cmdName, flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	check := fs.Bool("check", false, "check current and latest version without downloading or replacing")
	all := fs.Bool("all", false, "update all online fleet machines via the connected Hub (remote nodes first, then Hub)")
	force := fs.Bool("force", false, "reinstall the latest release even if already up to date or built from source")
	printUsage := func() {
		aliasNote := "(alias: collie upgrade)"
		if cmdName == "upgrade" {
			aliasNote = "(alias for collie update)"
		}
		fmt.Fprintf(out, "usage: collie %s [--check | --all | --force]   %s\n\n", cmdName, aliasNote)
		fmt.Fprintf(out, "Check or update the collie binary from GitHub Releases (SHA-256 verified)\n")
		fmt.Fprintf(out, "and smoothly restart active local collie services (systemd --user / launchd).\n\n")
		fmt.Fprintf(out, "Flags:\n")
		fs.SetOutput(out)
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			printUsage()
			return updateCLIOptions{}, true, nil
		}
		return updateCLIOptions{}, false, err
	}
	if fs.NArg() > 0 {
		return updateCLIOptions{}, false, fmt.Errorf("unexpected arguments: %s", strings.Join(fs.Args(), " "))
	}
	if *check && *force {
		return updateCLIOptions{}, false, fmt.Errorf("--check cannot be combined with --force")
	}
	if *all && *force {
		return updateCLIOptions{}, false, fmt.Errorf("--force is only supported for local update (without --all)")
	}
	return updateCLIOptions{check: *check, all: *all, force: *force}, false, nil
}

func runUpdateCLI(cmdName string, args []string, out, errOut io.Writer) error {
	opts, helped, err := parseUpdateArgs(cmdName, args, out)
	if err != nil {
		return err
	}
	if helped {
		return nil
	}
	if opts.all {
		return runUpdateAllCLI(opts.check, out, errOut)
	}
	if opts.check {
		return runUpdateCheckCLI(out, errOut)
	}
	return runUpdateLocalCLI(opts.force, out, errOut)
}

func fetchLatestTagCLI(ctx context.Context, errOut io.Writer) (tag string, usedIPv4, usedNoProxy bool, err error) {
	tag, usedIPv4, err = fetchLatestTagOpts(ctx, false, false)
	if err == nil {
		return tag, usedIPv4, false, nil
	}
	if shouldFallbackNoProxy(err) {
		if errOut != nil {
			fmt.Fprintf(errOut, "Proxy check failed (%s); retrying direct over IPv4...\n", formatUpdateError(err))
		}
		tag2, ipv4_2, err2 := fetchLatestTagOpts(ctx, true, true)
		if err2 == nil {
			return tag2, ipv4_2, true, nil
		}
		return "", ipv4_2, true, err2
	}
	return "", usedIPv4, false, err
}

func downloadReleaseCLI(ctx context.Context, tag, exe string, preferIPv4, disableProxy bool, errOut io.Writer) error {
	err := downloadReleaseOpts(ctx, tag, exe, preferIPv4, disableProxy)
	if err == nil {
		return nil
	}
	if !disableProxy && shouldFallbackNoProxy(err) {
		if errOut != nil {
			fmt.Fprintf(errOut, "Proxy download failed (%s); retrying direct over IPv4...\n", formatUpdateError(err))
		}
		return downloadReleaseOpts(ctx, tag, exe, true, true)
	}
	return err
}

func runUpdateCheckCLI(out, errOut io.Writer) error {
	ctx, cancel := context.WithTimeout(context.Background(), selfUpdateTimeout)
	defer cancel()

	latest, _, _, err := fetchLatestTagCLI(ctx, errOut)
	if err != nil || latest == "" {
		msg := formatUpdateError(err)
		if msg == "" {
			msg = "连不上 github.com，请检查网络或代理设置后重试"
		}
		return fmt.Errorf("检查最新版本失败：%s", msg)
	}

	cur := strings.TrimSpace(version)
	if cur == "" {
		cur = "unknown"
	}
	if parseVersion(cur) == nil {
		fmt.Fprintf(out, "Current version: %s (source build)\n", cur)
		fmt.Fprintf(out, "Latest version:  %s\n", latest)
		fmt.Fprintf(out, "Status:          source build (run `collie update --force` to install %s)\n", latest)
		return nil
	}
	fmt.Fprintf(out, "Current version: %s\n", cur)
	fmt.Fprintf(out, "Latest version:  %s\n", latest)
	if outdated(cur, latest) {
		fmt.Fprintf(out, "Status:          update available (%s → %s)\n", cur, latest)
		fmt.Fprintf(out, "Run `collie update` to update this machine, or `collie update --all` for the whole fleet.\n")
		return nil
	}
	if cur == latest {
		fmt.Fprintf(out, "Status:          up to date\n")
	} else {
		fmt.Fprintf(out, "Status:          ahead of latest release (%s > %s)\n", cur, latest)
	}
	return nil
}

func runUpdateLocalCLI(force bool, out, errOut io.Writer) error {
	if !selfUpdateMu.TryLock() {
		return fmt.Errorf("升级已在进行中，请稍候")
	}
	defer selfUpdateMu.Unlock()

	cur := strings.TrimSpace(version)
	if parseVersion(cur) == nil && !force {
		return fmt.Errorf("当前 collie 是从源码编译的（%s）；请 git pull 后重新编译，或运行 `collie update --force` 安装最新正式版", cur)
	}

	ctx, cancel := context.WithTimeout(context.Background(), selfUpdateTimeout)
	defer cancel()

	fmt.Fprintln(out, "Checking latest release...")
	latest, usedIPv4, usedNoProxy, err := fetchLatestTagCLI(ctx, errOut)
	if err != nil || latest == "" {
		msg := formatUpdateError(err)
		if msg == "" {
			msg = "连不上 github.com，请检查网络或代理设置后重试"
		}
		return fmt.Errorf("查不到最新版本：%s", msg)
	}

	if !force && parseVersion(cur) != nil && !outdated(cur, latest) {
		if cur == latest {
			fmt.Fprintf(out, "collie %s is already up to date.\n", cur)
		} else {
			fmt.Fprintf(out, "collie %s is already newer than latest release %s.\n", cur, latest)
		}
		fmt.Fprintln(out, "Use `collie update --force` to reinstall.")
		return nil
	}

	exe, err := executablePath()
	if err != nil {
		return fmt.Errorf("找不到当前程序文件：%w", err)
	}

	if cur == latest {
		fmt.Fprintf(out, "Reinstalling collie %s (%s/%s) and verifying SHA-256...\n", latest, runtime.GOOS, runtime.GOARCH)
	} else {
		fmt.Fprintf(out, "Updating collie %s → %s (%s/%s) and verifying SHA-256...\n", cur, latest, runtime.GOOS, runtime.GOARCH)
	}
	if err := downloadReleaseCLI(ctx, latest, exe, usedIPv4, usedNoProxy, errOut); err != nil {
		return fmt.Errorf("更新失败：%s", formatUpdateError(err))
	}

	if cur == latest {
		fmt.Fprintf(out, "✓ Reinstalled collie %s (%s)\n", latest, exe)
	} else {
		fmt.Fprintf(out, "✓ Updated collie %s → %s (%s)\n", cur, latest, exe)
	}

	restarted, restartErr := restartLocalServices(ctx, exe, out)
	if restartErr != nil {
		return restartErr
	}
	if len(restarted) == 0 {
		fmt.Fprintf(out, "• No active managed collie service detected; if collie is running manually, restart it to use %s.\n", latest)
	}
	return nil
}

// ---------------------------------------------------------------- local service auto-restart

func restartLocalServices(ctx context.Context, exe string, out io.Writer) ([]string, error) {
	switch updateGOOS() {
	case "linux":
		return restartLinuxServices(ctx, exe, out)
	case "darwin":
		return restartDarwinServices(ctx, exe, out)
	default:
		return nil, nil
	}
}

func sameExecutablePath(a, b string) bool {
	a, b = strings.TrimSpace(a), strings.TrimSpace(b)
	if a == "" || b == "" {
		return false
	}
	if ra, err := filepath.EvalSymlinks(a); err == nil {
		a = ra
	} else {
		a = filepath.Clean(a)
	}
	if rb, err := filepath.EvalSymlinks(b); err == nil {
		b = rb
	} else {
		b = filepath.Clean(b)
	}
	return a == b
}

func parseSystemdExecPath(raw string) string {
	idx := strings.Index(raw, "path=")
	if idx < 0 {
		return ""
	}
	rest := raw[idx+len("path="):]
	for i, ch := range rest {
		if ch == ';' || ch == '}' || ch == '\n' || ch == '\r' {
			rest = rest[:i]
			break
		}
	}
	return strings.TrimSpace(rest)
}

func parsePlistProgram(raw []byte) string {
	s := string(raw)
	idx := strings.Index(s, "<key>ProgramArguments</key>")
	if idx < 0 {
		return ""
	}
	s = s[idx:]
	start := strings.Index(s, "<string>")
	if start < 0 {
		return ""
	}
	s = s[start+len("<string>"):]
	end := strings.Index(s, "</string>")
	if end < 0 {
		return ""
	}
	p := strings.TrimSpace(s[:end])
	r := strings.NewReplacer("&amp;", "&", "&lt;", "<", "&gt;", ">", "&quot;", `"`, "&apos;", "'")
	return r.Replace(p)
}

func isSystemdUnavailableErr(out []byte, err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, exec.ErrNotFound) || errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	low := strings.ToLower(string(out) + " " + err.Error())
	return strings.Contains(low, "failed to connect to bus") ||
		strings.Contains(low, "no medium found") ||
		strings.Contains(low, "system has not been booted with systemd") ||
		strings.Contains(low, "executable file not found")
}

func restartLinuxServices(ctx context.Context, exe string, out io.Writer) ([]string, error) {
	var restarted []string
	var errs []string
	for _, unit := range []string{"collie-hub", "collie-node"} {
		checkCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
		raw, err := updateRunCmd(checkCtx, "systemctl", "--user", "is-active", unit)
		cancel()
		if err != nil {
			if isSystemdUnavailableErr(raw, err) {
				return restarted, nil
			}
			continue
		}
		state := strings.TrimSpace(string(raw))
		if state != "active" && state != "activating" && state != "reloading" {
			continue
		}

		showCtx, showCancel := context.WithTimeout(ctx, 5*time.Second)
		execOut, showErr := updateRunCmd(showCtx, "systemctl", "--user", "show", "-p", "ExecStart", "--value", unit)
		showCancel()
		if showErr == nil {
			if svcBin := parseSystemdExecPath(string(execOut)); svcBin != "" && !sameExecutablePath(svcBin, exe) {
				continue
			}
		}

		rCtx, rCancel := context.WithTimeout(ctx, 20*time.Second)
		rOut, rErr := updateRunCmd(rCtx, "systemctl", "--user", "restart", unit)
		rCancel()
		if rErr != nil {
			detail := strings.TrimSpace(string(rOut))
			if detail == "" {
				detail = rErr.Error()
			}
			errs = append(errs, fmt.Sprintf("重启服务 %s 失败（%s）；请手动运行：systemctl --user restart %s", unit, detail, unit))
			continue
		}
		restarted = append(restarted, unit)
		fmt.Fprintf(out, "✓ Restarted service %s (systemd --user)\n", unit)
	}
	if len(errs) > 0 {
		return restarted, errors.New(strings.Join(errs, "; "))
	}
	return restarted, nil
}

func restartDarwinServices(ctx context.Context, exe string, out io.Writer) ([]string, error) {
	home, _ := updateHomeDir()
	uid := updateUID()
	domain := "gui/" + uid
	var restarted []string
	var errs []string

	for _, label := range []string{"com.collie.node", "com.collie.hub"} {
		target := domain + "/" + label
		pCtx, pCancel := context.WithTimeout(ctx, 5*time.Second)
		raw, err := updateRunCmd(pCtx, "launchctl", "print", target)
		pCancel()
		if err != nil {
			if errors.Is(err, exec.ErrNotFound) {
				return restarted, nil
			}
			_ = raw
			continue
		}

		plist := filepath.Join(home, "Library", "LaunchAgents", label+".plist")
		hasPlist := false
		if _, serr := updateStat(plist); serr == nil {
			hasPlist = true
			if pb, rerr := updateReadFile(plist); rerr == nil {
				if svcBin := parsePlistProgram(pb); svcBin != "" && !sameExecutablePath(svcBin, exe) {
					continue
				}
			}
		}

		kCtx, kCancel := context.WithTimeout(ctx, 15*time.Second)
		kOut, kErr := updateRunCmd(kCtx, "launchctl", "kickstart", "-k", target)
		kCancel()
		if kErr == nil {
			restarted = append(restarted, label)
			fmt.Fprintf(out, "✓ Restarted service %s (launchd)\n", label)
			continue
		}

		if !hasPlist {
			detail := strings.TrimSpace(string(kOut))
			if detail == "" {
				detail = kErr.Error()
			}
			errs = append(errs, fmt.Sprintf("重启服务 %s 失败（%s），且未找到 %s", label, detail, plist))
			continue
		}

		// macOS LaunchConstraints (LWCR / EX_CONFIG 78) fallback: bootout + wait + bootstrap.
		bCtx, bCancel := context.WithTimeout(ctx, 10*time.Second)
		_, _ = updateRunCmd(bCtx, "launchctl", "bootout", target)
		bCancel()

		for i := 0; i < launchdBootoutMaxTries; i++ {
			wCtx, wCancel := context.WithTimeout(ctx, 3*time.Second)
			_, perr := updateRunCmd(wCtx, "launchctl", "print", target)
			wCancel()
			if perr != nil {
				break
			}
			select {
			case <-ctx.Done():
				return restarted, ctx.Err()
			case <-time.After(launchdBootoutPoll):
			}
		}

		var bootOut []byte
		var bootErr error
		attempts := max(1, launchdBootstrapRetries)
		for i := 0; i < attempts; i++ {
			bsCtx, bsCancel := context.WithTimeout(ctx, 15*time.Second)
			bootOut, bootErr = updateRunCmd(bsCtx, "launchctl", "bootstrap", domain, plist)
			bsCancel()
			if bootErr == nil {
				break
			}
			if i+1 < attempts {
				select {
				case <-ctx.Done():
					return restarted, ctx.Err()
				case <-time.After(launchdBootstrapDelay):
				}
			}
		}
		if bootErr != nil {
			detail := strings.TrimSpace(string(bootOut))
			if detail == "" {
				detail = bootErr.Error()
			}
			errs = append(errs, fmt.Sprintf("重启服务 %s 失败（kickstart 失败后 bootout+bootstrap 仍失败：%s）；请手动运行：launchctl bootout %s ; launchctl bootstrap %s %s", label, detail, target, domain, plist))
			continue
		}
		restarted = append(restarted, label)
		fmt.Fprintf(out, "✓ Restarted service %s (launchd bootout+bootstrap)\n", label)
	}
	if len(errs) > 0 {
		return restarted, errors.New(strings.Join(errs, "; "))
	}
	return restarted, nil
}

// ---------------------------------------------------------------- fleet update (`collie update --all`)

type versionAPIResp struct {
	Version  string           `json:"version"`
	Latest   string           `json:"latest"`
	Outdated []string         `json:"outdated"`
	Machines []versionMachine `json:"machines"`
}

type updateAPIResp struct {
	OK      bool   `json:"ok"`
	From    string `json:"from"`
	Version string `json:"version"`
	Latest  bool   `json:"latest"`
	Relayed bool   `json:"relayed"`
	Error   string `json:"error"`
}

type fleetUpdateResult struct {
	name   string
	role   string
	status string // "updated" | "up-to-date" | "skipped" | "offline" | "failed"
	detail string
}

func cliHubRequest(ctx context.Context, base, tok, method, path string, timeout time.Duration) ([]byte, int, error) {
	reqCtx := ctx
	var cancel context.CancelFunc
	if timeout > 0 {
		reqCtx, cancel = context.WithTimeout(ctx, timeout)
		defer cancel()
	}
	req, err := http.NewRequestWithContext(reqCtx, method, base+path, nil)
	if err != nil {
		return nil, 0, fmt.Errorf("invalid collie server address %q: %w", base, err)
	}
	req.Header.Set("Authorization", "Bearer "+tok)
	req.Header.Set("Content-Type", "application/json")
	client := &http.Client{Timeout: timeout}
	resp, err := client.Do(req)
	if err != nil {
		return nil, 0, fmt.Errorf("collie Hub unreachable (%s): %w", base, err)
	}
	defer resp.Body.Close()
	out, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, resp.StatusCode, err
	}
	if resp.StatusCode >= 300 {
		var e struct {
			Error string `json:"error"`
		}
		if json.Unmarshal(out, &e) == nil && strings.TrimSpace(e.Error) != "" {
			return out, resp.StatusCode, errors.New(strings.TrimSpace(e.Error))
		}
		msg := strings.TrimSpace(string(out))
		if msg == "" {
			msg = resp.Status
		}
		return out, resp.StatusCode, errors.New(msg)
	}
	return out, resp.StatusCode, nil
}

func fetchFleetVersion(ctx context.Context, base, tok string, errOut io.Writer) (versionAPIResp, error) {
	var vr versionAPIResp
	tries := max(1, cliUpdateVersionRetries)
	for i := 0; i < tries; i++ {
		path := "/api/version"
		if i == 0 {
			path = "/api/version?refresh=1"
		}
		raw, _, err := cliHubRequest(ctx, base, tok, http.MethodGet, path, cliUpdateVersionTimeout)
		if err != nil {
			return vr, err
		}
		if err := json.Unmarshal(raw, &vr); err != nil {
			return vr, fmt.Errorf("invalid /api/version response: %w", err)
		}
		if vr.Latest != "" || i+1 >= tries {
			break
		}
		select {
		case <-ctx.Done():
			return vr, ctx.Err()
		case <-time.After(cliUpdateVersionPoll):
		}
	}
	if vr.Latest == "" {
		if latest, _, _, err := fetchLatestTagCLI(ctx, errOut); err == nil && latest != "" {
			vr.Latest = latest
			for i := range vr.Machines {
				m := &vr.Machines[i]
				m.Outdated = m.Online && outdated(m.Version, latest)
			}
		}
	}
	return vr, nil
}

func waitHubVersionAfterRestart(ctx context.Context, base, tok, wantVersion string) bool {
	if cliUpdateReconnectWait <= 0 {
		return false
	}
	deadline := time.Now().Add(cliUpdateReconnectWait)
	poll := cliUpdateReconnectPoll
	if poll <= 0 {
		poll = 500 * time.Millisecond
	}
	// Brief head start so Hub's delayed syscall.Exec (800ms) begins.
	select {
	case <-ctx.Done():
		return false
	case <-time.After(min(poll, time.Second)):
	}
	for time.Now().Before(deadline) {
		raw, _, err := cliHubRequest(ctx, base, tok, http.MethodGet, "/api/version", 3*time.Second)
		if err == nil {
			var vr versionAPIResp
			if json.Unmarshal(raw, &vr) == nil && vr.Version != "" && !outdated(vr.Version, wantVersion) {
				return true
			}
		}
		select {
		case <-ctx.Done():
			return false
		case <-time.After(poll):
		}
	}
	return false
}

func runUpdateAllCLI(checkOnly bool, out, errOut io.Writer) error {
	base, tok, err := updateCLITarget()
	if err != nil {
		return fmt.Errorf("未检测到运行中或已连接的 Hub（%v）；如需仅更新本机二进制，请直接运行 `collie update`", err)
	}

	ctx := context.Background()
	vr, err := fetchFleetVersion(ctx, base, tok, errOut)
	if err != nil {
		return fmt.Errorf("获取舰队版本信息失败：%w", err)
	}

	machines := vr.Machines
	if len(machines) == 0 {
		machines = []versionMachine{{
			ID:       "",
			Name:     "hub",
			Version:  vr.Version,
			Local:    true,
			Online:   true,
			Outdated: outdated(vr.Version, vr.Latest),
		}}
	}

	latestLabel := vr.Latest
	if latestLabel == "" {
		latestLabel = "(unknown)"
	}

	if checkOnly {
		fmt.Fprintf(out, "Latest release: %s (via %s)\n", latestLabel, base)
		outdatedCount := 0
		for _, m := range machines {
			role := "node"
			if m.Local {
				role = "hub"
			}
			ver := m.Version
			if ver == "" {
				ver = "unknown"
			}
			status := "up to date"
			switch {
			case !m.Online:
				status = "offline"
			case parseVersion(m.Version) == nil && m.Version != "":
				status = "source build"
			case vr.Latest != "" && (m.Outdated || outdated(m.Version, vr.Latest)):
				status = fmt.Sprintf("update available (%s → %s)", ver, vr.Latest)
				outdatedCount++
			case vr.Latest == "":
				status = "unknown latest"
			}
			fmt.Fprintf(out, "  %-16s %-5s %-10s %s\n", m.Name, role, ver, status)
		}
		if outdatedCount > 0 {
			fmt.Fprintf(out, "Run `collie update --all` to upgrade %d outdated online machine(s).\n", outdatedCount)
		}
		return nil
	}

	var remoteOnline []versionMachine
	var remoteOffline []versionMachine
	var localHub *versionMachine
	for i := range machines {
		m := machines[i]
		if m.Local {
			mc := m
			localHub = &mc
			continue
		}
		if m.Online {
			remoteOnline = append(remoteOnline, m)
		} else {
			remoteOffline = append(remoteOffline, m)
		}
	}

	fmt.Fprintf(out, "Fleet update via %s (target: %s)\n", base, latestLabel)
	var results []fleetUpdateResult
	updatedAnyRemote := false

	runOne := func(m versionMachine) fleetUpdateResult {
		role := "node"
		if m.Local {
			role = "hub"
		}
		if m.Version != "" && parseVersion(m.Version) == nil {
			res := fleetUpdateResult{
				name:   m.Name,
				role:   role,
				status: "skipped",
				detail: fmt.Sprintf("source build (%s)", m.Version),
			}
			fmt.Fprintf(out, "  • %s (%s): skipped (%s)\n", m.Name, role, res.detail)
			return res
		}
		if vr.Latest != "" && parseVersion(m.Version) != nil && !outdated(m.Version, vr.Latest) {
			res := fleetUpdateResult{
				name:   m.Name,
				role:   role,
				status: "up-to-date",
				detail: fmt.Sprintf("%s (already up to date)", m.Version),
			}
			fmt.Fprintf(out, "  ✓ %s (%s): %s\n", m.Name, role, res.detail)
			return res
		}

		fromLabel := m.Version
		if fromLabel == "" {
			fromLabel = "unknown"
		}
		fmt.Fprintf(out, "  • %s (%s): updating from %s...\n", m.Name, role, fromLabel)

		apiPath := "/api/update"
		if !m.Local && m.ID != "" {
			apiPath += "?machine=" + url.QueryEscape(m.ID)
		}
		raw, _, uerr := cliHubRequest(ctx, base, tok, http.MethodPost, apiPath, cliUpdateAllTimeout)
		if uerr != nil {
			res := fleetUpdateResult{
				name:   m.Name,
				role:   role,
				status: "failed",
				detail: sanitizeUpdateErr(uerr.Error()),
			}
			fmt.Fprintf(out, "  ✗ %s (%s): %s\n", m.Name, role, res.detail)
			return res
		}

		var ur updateAPIResp
		_ = json.Unmarshal(raw, &ur)
		if ur.Latest {
			ver := ur.Version
			if ver == "" {
				ver = fromLabel
			}
			suffix := "already up to date"
			if ur.Relayed {
				suffix += ", checked via Hub relay"
			}
			res := fleetUpdateResult{
				name:   m.Name,
				role:   role,
				status: "up-to-date",
				detail: fmt.Sprintf("%s (%s)", ver, suffix),
			}
			fmt.Fprintf(out, "  ✓ %s (%s): %s\n", m.Name, role, res.detail)
			return res
		}

		from := ur.From
		if from == "" {
			from = fromLabel
		}
		to := ur.Version
		if to == "" {
			to = vr.Latest
		}
		note := ""
		if m.Local {
			if to != "" && waitHubVersionAfterRestart(ctx, base, tok, to) {
				note = " (restarted)"
			} else {
				note = " (restart triggered)"
			}
		} else {
			updatedAnyRemote = true
			if ur.Relayed {
				note = " (via Hub relay)"
			}
		}
		res := fleetUpdateResult{
			name:   m.Name,
			role:   role,
			status: "updated",
			detail: fmt.Sprintf("%s → %s%s", from, to, note),
		}
		fmt.Fprintf(out, "  ✓ %s (%s): %s\n", m.Name, role, res.detail)
		return res
	}

	// 1) Remote online nodes first (Hub yamux relay fallback works automatically).
	for _, m := range remoteOnline {
		results = append(results, runOne(m))
	}
	// 2) Record offline nodes in summary without failing the run.
	for _, m := range remoteOffline {
		detail := "offline (skipped)"
		if m.Version != "" {
			detail = fmt.Sprintf("offline (skipped, last: %s)", m.Version)
		}
		results = append(results, fleetUpdateResult{
			name:   m.Name,
			role:   "node",
			status: "offline",
			detail: detail,
		})
	}
	// 3) Hub itself last so tunnels stay alive while remote nodes upgrade.
	if localHub != nil {
		if updatedAnyRemote && cliUpdateHubDelay > 0 && (vr.Latest == "" || outdated(localHub.Version, vr.Latest)) {
			time.Sleep(cliUpdateHubDelay)
		}
		results = append(results, runOne(*localHub))
	}

	var updated, upToDate, skipped, offline, failed int
	for _, r := range results {
		switch r.status {
		case "updated":
			updated++
		case "up-to-date":
			upToDate++
		case "skipped":
			skipped++
		case "offline":
			offline++
		case "failed":
			failed++
		}
	}

	var buf bytes.Buffer
	fmt.Fprintf(&buf, "\nSummary (%d updated, %d up to date, %d skipped, %d offline, %d failed):\n",
		updated, upToDate, skipped, offline, failed)
	for _, r := range results {
		icon := "•"
		switch r.status {
		case "updated", "up-to-date":
			icon = "✓"
		case "offline", "skipped":
			icon = "-"
		case "failed":
			icon = "✗"
		}
		fmt.Fprintf(&buf, "  %s %-16s %-5s %s\n", icon, r.name, r.role, r.detail)
	}
	_, _ = out.Write(buf.Bytes())

	if failed > 0 {
		return fmt.Errorf("%d machine(s) failed to update", failed)
	}
	return nil
}
