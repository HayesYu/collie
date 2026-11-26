package collie

import (
	"errors"
	"fmt"
	"strings"
)

// Cursor CLI ACP adapter for `collie _bridge cursor [flags]` driving
// `cursor-agent [--force] [--approve-mcps] [--model <id>] acp` through the
// shared acpBridge runtime.

const (
	cursorBridgeSource      = "collie:cursor"
	cursorAskQuestionSkipID = "__ask_question_skip__"
)

// cursorEnv prepares the environment for invoking `cursor-agent`: sets
// NO_OPEN_BROWSER=1 so headless or background workers never trigger interactive
// browser OAuth popups, while preserving credentials and proxy settings.
func cursorEnv(base []string) []string {
	out := make([]string, 0, len(base)+1)
	hasNoBrowser := false
	for _, kv := range base {
		k, _, ok := strings.Cut(kv, "=")
		if ok && k == "NO_OPEN_BROWSER" {
			hasNoBrowser = true
			out = append(out, "NO_OPEN_BROWSER=1")
			continue
		}
		out = append(out, kv)
	}
	if !hasNoBrowser {
		out = append(out, "NO_OPEN_BROWSER=1")
	}
	return out
}

func parseCursorBridgeArgs(args []string) (acpBridgeConfig, error) {
	return parseACPBridgeArgs(args, "--cursor-bin")
}

// cursorACPArgs builds the argv for `cursor-agent ... acp`.
// Global flags are placed before `acp`; ACP only honors `--force` (not `--yolo`)
// and does not accept an independent `--effort` flag.
func cursorACPArgs(cfg acpBridgeConfig) []string {
	var args []string
	if cfg.Bypass {
		args = append(args, "--force", "--approve-mcps")
	}
	if cfg.Model != "" {
		args = append(args, "--model", cfg.Model)
	}
	args = append(args, "acp")
	return args
}

type cursorProvider struct{}

func (cursorProvider) Kind() string                 { return "cursor" }
func (cursorProvider) DisplayName() string          { return "Cursor" }
func (cursorProvider) Source() string               { return cursorBridgeSource }
func (cursorProvider) LogPrefix() string            { return "[cursor-bridge]" }
func (cursorProvider) BinName() string              { return "cursor-agent" }
func (cursorProvider) BinFlag() string              { return "--cursor-bin" }
func (cursorProvider) BypassFlag() string           { return "--force" }
func (cursorProvider) SupportsEffort() bool         { return false }
func (cursorProvider) PlaceholderToolTitle() string { return "" }

func (cursorProvider) BuildACPArgs(cfg acpBridgeConfig) []string {
	return cursorACPArgs(cfg)
}

func (cursorProvider) BuildEnv(base []string) []string {
	return cursorEnv(base)
}

func (cursorProvider) FilterStderrLine(line string) (string, bool) {
	cleaned := strings.TrimSpace(stripANSI(line))
	if cleaned == "" {
		return "", false
	}
	return cleaned, true
}

func (cursorProvider) DefaultBannerModel(cfg acpBridgeConfig) string {
	return orStr(cfg.Model, "auto")
}

func (cursorProvider) DefaultBannerEffort(cfg acpBridgeConfig) string {
	return ""
}

func (cursorProvider) FormatSessionOpenError(op string, err error) error {
	var rpcErr *rpcError
	if errors.As(err, &rpcErr) {
		switch rpcErr.Code {
		case -32000:
			return fmt.Errorf("cursor authentication required: run 'cursor-agent login' (or 'NO_OPEN_BROWSER=1 cursor-agent login' on headless Linux) or set CURSOR_API_KEY (%v)", err)
		case -32603:
			return fmt.Errorf("cursor failed to initialize session services (check cursor-agent login credentials or CURSOR_API_KEY): %v", err)
		}
	}
	return fmt.Errorf("acp %s: %w", op, err)
}
