package collie

import (
	"errors"
	"fmt"
	"strings"
)

// Oh My Pi (omp) ACP adapter for `collie _bridge omp [flags]` driving
// `omp --allow-home [--approval-mode yolo|always-ask] [--model <provider/id>] [--thinking <level>] acp`
// through the shared acpBridge runtime.

const ompBridgeSource = "collie:omp"

// ompEnv preserves the user's proxy, provider API keys, and ~/.omp state.
func ompEnv(base []string) []string {
	return append([]string(nil), base...)
}

func parseOmpBridgeArgs(args []string) (acpBridgeConfig, error) {
	return parseACPBridgeArgs(args, "--omp-bin")
}

// ompACPArgs builds the argv for `omp ... acp`.
// All global flags are placed before the `acp` subcommand:
//   - Always passes `--allow-home` so launching in `~` never switches cwd to /tmp.
//   - Explicitly passes `--approval-mode yolo` when Bypass=true, or
//     `--approval-mode always-ask` (or cfg.PermissionMode) when Bypass=false
//     since omp itself defaults to yolo.
//   - Translates Collie's unified `--effort` into omp's `--thinking`, skipping it
//     if the model already carries a `:<thinking>` selector suffix.
func ompACPArgs(cfg acpBridgeConfig) []string {
	args := []string{"--allow-home"}
	if cfg.Bypass {
		args = append(args, "--approval-mode", "yolo")
	} else {
		mode := strings.TrimSpace(cfg.PermissionMode)
		if mode == "" {
			mode = "always-ask"
		}
		args = append(args, "--approval-mode", mode)
	}
	if cfg.Model != "" {
		args = append(args, "--model", cfg.Model)
	}
	if eff := effectiveEffort("omp", cfg.Model, cfg.Effort); eff != "" {
		args = append(args, "--thinking", eff)
	}
	args = append(args, "acp")
	return args
}

type ompProvider struct{}

func (ompProvider) Kind() string                 { return "omp" }
func (ompProvider) DisplayName() string          { return "Oh My Pi" }
func (ompProvider) Source() string               { return ompBridgeSource }
func (ompProvider) LogPrefix() string            { return "[omp-bridge]" }
func (ompProvider) BinName() string              { return "omp" }
func (ompProvider) BinFlag() string              { return "--omp-bin" }
func (ompProvider) BypassFlag() string           { return "--auto-approve" }
func (ompProvider) SupportsEffort() bool         { return true }
func (ompProvider) PlaceholderToolTitle() string { return "" }

func (ompProvider) BuildACPArgs(cfg acpBridgeConfig) []string {
	return ompACPArgs(cfg)
}

func (ompProvider) BuildEnv(base []string) []string {
	return ompEnv(base)
}

func (ompProvider) FilterStderrLine(line string) (string, bool) {
	cleaned := strings.TrimSpace(stripANSI(line))
	if cleaned == "" {
		return "", false
	}
	return cleaned, true
}

func (ompProvider) DefaultBannerModel(cfg acpBridgeConfig) string {
	return orStr(cfg.Model, "default")
}

func (ompProvider) DefaultBannerEffort(cfg acpBridgeConfig) string {
	return effectiveEffort("omp", cfg.Model, cfg.Effort)
}

func (ompProvider) FormatSessionOpenError(op string, err error) error {
	var rpcErr *rpcError
	if errors.As(err, &rpcErr) {
		low := strings.ToLower(rpcErr.Error())
		if rpcErr.Code == -32000 || strings.Contains(low, "auth") || strings.Contains(low, "api key") || strings.Contains(low, "credential") || strings.Contains(low, "no model") {
			return fmt.Errorf("omp authentication or model error (check ~/.omp login state or provider API key): %w", err)
		}
	}
	return fmt.Errorf("acp %s: %w", op, err)
}
