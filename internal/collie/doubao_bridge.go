package collie

import (
	"fmt"
	"strings"
)

// Doubao ACP adapter for `collie _bridge doubao [flags]` driving
// `doubao acp serve` through the shared acpBridge runtime.

const (
	doubaoBridgeSource    = "collie:doubao"
	doubaoPlaceholderTool = "继续处理任务中"
)

var doubaoPublicNoProxy = []string{
	"doubao.com",
	".doubao.com",
	"byteimg.com",
	".byteimg.com",
}

// doubaoEnv prepares the environment for invoking `doubao`: disables
// interactive update prompts and appends Doubao's public domains to
// no_proxy / NO_PROXY without clobbering existing entries.
func doubaoEnv(base []string) []string {
	out := make([]string, 0, len(base)+3)
	var curLower, curUpper string
	hasLower, hasUpper, hasNoUpdate := false, false, false
	for _, kv := range base {
		k, v, ok := strings.Cut(kv, "=")
		if !ok {
			out = append(out, kv)
			continue
		}
		switch k {
		case "DOUBAO_CLI_NO_UPDATE_CHECK":
			hasNoUpdate = true
			out = append(out, "DOUBAO_CLI_NO_UPDATE_CHECK=1")
		case "no_proxy":
			hasLower = true
			curLower = v
			out = append(out, "no_proxy="+mergeNoProxy(v, doubaoPublicNoProxy))
		case "NO_PROXY":
			hasUpper = true
			curUpper = v
			out = append(out, "NO_PROXY="+mergeNoProxy(v, doubaoPublicNoProxy))
		default:
			out = append(out, kv)
		}
	}
	if !hasNoUpdate {
		out = append(out, "DOUBAO_CLI_NO_UPDATE_CHECK=1")
	}
	if !hasLower {
		out = append(out, "no_proxy="+mergeNoProxy(curUpper, doubaoPublicNoProxy))
	}
	if !hasUpper {
		out = append(out, "NO_PROXY="+mergeNoProxy(curLower, doubaoPublicNoProxy))
	}
	return out
}

func mergeNoProxy(cur string, add []string) string {
	seen := map[string]bool{}
	var parts []string
	for _, p := range strings.Split(cur, ",") {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		low := strings.ToLower(p)
		if !seen[low] {
			seen[low] = true
			parts = append(parts, p)
		}
	}
	for _, p := range add {
		low := strings.ToLower(p)
		if !seen[low] {
			seen[low] = true
			parts = append(parts, p)
		}
	}
	return strings.Join(parts, ",")
}

func parseDoubaoBridgeArgs(args []string) (acpBridgeConfig, error) {
	return parseACPBridgeArgs(args, "--doubao-bin")
}

// doubaoACPArgs builds the argv for `doubao acp serve`.
// When neither model nor effort is explicitly set on a fresh session, it aligns
// with the default "Auto + high" profile.
func doubaoACPArgs(cfg acpBridgeConfig) []string {
	args := []string{"acp", "serve"}
	if cfg.Bypass {
		args = append(args, "-y")
	} else {
		mode := cfg.PermissionMode
		if mode == "" {
			mode = "default"
		}
		args = append(args, "--permission-mode", mode)
	}
	model := cfg.Model
	effort := cfg.Effort
	if cfg.Resume == "" {
		if model == "" {
			model = defaultDoubaoModel
		}
		if effort == "" && (strings.EqualFold(model, defaultDoubaoModel) || model == defaultDoubaoModelID) {
			effort = defaultDoubaoEffort
		}
	}
	if model != "" {
		args = append(args, "--model", model)
	}
	if effort != "" {
		args = append(args, "--effort", effort)
	}
	if cfg.Cwd != "" {
		args = append(args, "-C", cfg.Cwd)
	}
	return args
}

type doubaoProvider struct{}

func (doubaoProvider) Kind() string                 { return "doubao" }
func (doubaoProvider) DisplayName() string          { return "Doubao" }
func (doubaoProvider) Source() string               { return doubaoBridgeSource }
func (doubaoProvider) LogPrefix() string            { return "[doubao-bridge]" }
func (doubaoProvider) BinName() string              { return "doubao" }
func (doubaoProvider) BinFlag() string              { return "--doubao-bin" }
func (doubaoProvider) BypassFlag() string           { return "-y" }
func (doubaoProvider) SupportsEffort() bool         { return true }
func (doubaoProvider) PlaceholderToolTitle() string { return doubaoPlaceholderTool }

func (doubaoProvider) BuildACPArgs(cfg acpBridgeConfig) []string {
	return doubaoACPArgs(cfg)
}

func (doubaoProvider) BuildEnv(base []string) []string {
	return doubaoEnv(base)
}

func (doubaoProvider) FilterStderrLine(line string) (string, bool) {
	// Ignore noisy telemetry lines while surfacing real errors.
	if strings.HasPrefix(line, "[cli-chat] ") {
		return "", false
	}
	return line, true
}

func (doubaoProvider) DefaultBannerModel(cfg acpBridgeConfig) string {
	return orStr(cfg.Model, defaultDoubaoModel)
}

func (doubaoProvider) DefaultBannerEffort(cfg acpBridgeConfig) string {
	return orStr(cfg.Effort, defaultDoubaoEffort)
}

func (doubaoProvider) FormatSessionOpenError(op string, err error) error {
	return fmt.Errorf("acp %s: %w", op, err)
}
