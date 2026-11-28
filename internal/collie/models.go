package collie

import (
	"bufio"
	"context"
	"encoding/json"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"
)

// Which models (and thinking efforts) each agent can run here, so the human can
// pick from a list instead of typing launch flags. Each agent is asked in its
// own way: Claude Code's settings (a gateway lists its models and what they
// support), Codex's app-server model/list, `agy models`.

type ModelOpt struct {
	ID    string `json:"id"`
	Label string `json:"label,omitempty"`
	Note  string `json:"note,omitempty"`
	// Efforts lists the thinking efforts this model accepts independently.
	// nil (JSON null) inherits ModelInfo.Efforts; an empty non-nil slice
	// (JSON []) explicitly means this model does not accept --effort.
	Efforts []string `json:"efforts"`
}

type ModelInfo struct {
	Kind          string     `json:"kind"`
	Models        []ModelOpt `json:"models"`
	Efforts       []string   `json:"efforts"`
	Default       string     `json:"default,omitempty"` // what the agent uses when nothing is chosen
	DefaultEffort string     `json:"default_effort,omitempty"`
}

var (
	allEfforts = []string{"low", "medium", "high", "xhigh", "max"}
	piEfforts  = []string{"off", "minimal", "low", "medium", "high", "xhigh", "max"}
	ompEfforts = []string{"off", "minimal", "low", "medium", "high", "xhigh", "max", "auto"}
)

// splitPiModelThinking splits an explicit `:<thinking>` suffix from a Pi/Omp
// model selector (e.g. "openai/gpt-5:high" -> "openai/gpt-5", "high", true),
// leaving non-thinking suffixes such as ":free" or ":latest" untouched.
func splitPiModelThinking(model string) (base, thinking string, ok bool) {
	m := strings.TrimSpace(model)
	idx := strings.LastIndexByte(m, ':')
	if idx <= 0 || idx == len(m)-1 {
		return m, "", false
	}
	suf := strings.ToLower(strings.TrimSpace(m[idx+1:]))
	switch suf {
	case "off", "minimal", "low", "medium", "high", "xhigh", "max", "auto":
		return m[:idx], suf, true
	}
	return m, "", false
}

const (
	defaultDoubaoModel   = "Auto"
	defaultDoubaoModelID = "9"
	defaultDoubaoEffort  = "high"
)

// splitAgyModelEffort splits an agy model ID that bakes the thinking effort
// into its suffix (e.g. "gemini-4-neon-medium" -> "gemini-4-neon", "medium").
func splitAgyModelEffort(model string) (base, effort string, ok bool) {
	m := strings.TrimSpace(model)
	low := strings.ToLower(m)
	for _, e := range allEfforts {
		suf := "-" + e
		if len(low) > len(suf) && strings.HasSuffix(low, suf) {
			return m[:len(m)-len(suf)], e, true
		}
	}
	return m, "", false
}

// agyRejectsEffort reports whether agy rejects an independent --effort flag
// for this model ID (suffixed models conflict with --effort; orchestrator
// models explicitly reject --effort).
func agyRejectsEffort(model string) bool {
	if _, _, ok := splitAgyModelEffort(model); ok {
		return true
	}
	return strings.Contains(strings.ToLower(strings.TrimSpace(model)), "orchestrator")
}

// effectiveEffort drops effort when the chosen agent/model cannot accept an
// independent effort flag, preventing startup errors from stale settings or
// CLI arguments.
func effectiveEffort(kind, model, effort string) string {
	effort = strings.TrimSpace(effort)
	if effort == "" {
		return ""
	}
	canon := kinds[kind]
	if canon == "cursor" {
		return ""
	}
	if canon == "agy" && agyRejectsEffort(model) {
		return ""
	}
	if canon == "pi" || canon == "omp" {
		if _, _, hasThinking := splitPiModelThinking(model); hasThinking {
			return ""
		}
	}
	return effort
}

// modelArgs are the launch flags for a model and effort ("" = the agent's own default).
func modelArgs(kind, model, effort string) []string {
	effort = effectiveEffort(kind, model, effort)
	var a []string
	switch kinds[kind] {
	case "codex":
		if model != "" {
			a = append(a, "-m", model)
		}
		if effort != "" {
			a = append(a, "-c", "model_reasoning_effort="+strconvQuote(effort))
		}
	case "claude", "agy", "pi", "omp":
		if model != "" {
			a = append(a, "--model", model)
		}
		if effort != "" {
			a = append(a, "--effort", effort)
		}
	case "doubao":
		if model == "" {
			model = defaultDoubaoModel
		}
		if effort == "" && (strings.EqualFold(model, defaultDoubaoModel) || model == defaultDoubaoModelID) {
			effort = defaultDoubaoEffort
		}
		a = append(a, "--model", model)
		if effort != "" {
			a = append(a, "--effort", effort)
		}
	case "cursor":
		if model != "" {
			a = append(a, "--model", model)
		}
	}
	return a
}

// bypassArgs: the launch flag that turns off an agent's permission prompts.
func bypassArgs(kind string) []string {
	switch kinds[kind] {
	case "claude", "agy":
		return []string{"--dangerously-skip-permissions"}
	case "codex":
		return []string{"--dangerously-bypass-approvals-and-sandbox"}
	case "doubao":
		return []string{"-y"}
	case "cursor":
		return []string{"--force"}
	case "pi":
		return []string{"--approve"}
	case "omp":
		return []string{"--auto-approve"}
	}
	return nil
}

// withBypass adds bypassArgs to args unless permissions are asked for or the
// flag is already there.
func withBypass(args []string, kind string, ask bool) []string {
	if ask {
		return args
	}
	for _, f := range bypassArgs(kind) {
		if !contains(args, f) {
			args = append(args, f)
		}
	}
	return args
}

func strconvQuote(s string) string { b, _ := json.Marshal(s); return string(b) }

var modelCache = struct {
	sync.Mutex
	m map[string]modelMark
}{m: map[string]modelMark{}}

type modelMark struct {
	at   time.Time
	info *ModelInfo
	// fallback marks a list that could not be read from the agent itself;
	// it is retried sooner than a real one.
	fallback bool
}

func (s *Server) listModels(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query().Get("kind")
	if q == "agy" { // the web app's short name
		q = "antigravity"
	}
	kind := kinds[q]
	if kind == "" {
		writeJSON(w, 400, map[string]any{"error": "unknown agent kind"})
		return
	}
	modelCache.Lock()
	mk := modelCache.m[kind]
	modelCache.Unlock()
	ttl := 10 * time.Minute
	if mk.fallback {
		ttl = time.Minute
	}
	if mk.info == nil || time.Since(mk.at) > ttl || r.URL.Query().Get("refresh") != "" {
		ctx, cancel := context.WithTimeout(r.Context(), 25*time.Second)
		defer cancel()
		info := &ModelInfo{Kind: kind, Efforts: allEfforts}
		fallback := false
		switch kind {
		case "claude":
			fallback = !s.claudeModels(ctx, info)
		case "codex":
			s.codexModels(ctx, info)
		case "agy":
			s.agyModels(ctx, info)
		case "doubao":
			s.doubaoModels(ctx, info)
		case "cursor":
			s.cursorModels(ctx, info)
		case "pi":
			s.piModels(ctx, info)
		case "omp":
			s.ompModels(ctx, info)
		}
		if info.Models == nil {
			info.Models = []ModelOpt{}
		}
		if info.Efforts == nil {
			info.Efforts = []string{}
		}
		mk = modelMark{at: time.Now(), info: info, fallback: fallback}
		modelCache.Lock()
		modelCache.m[kind] = mk
		modelCache.Unlock()
	}
	writeJSON(w, 200, mk.info)
}

// claudeSettings is the part of Claude Code's settings.json that matters here.
type claudeSettings struct {
	Model  string            `json:"model"`
	Effort string            `json:"effortLevel"`
	Env    map[string]string `json:"env"`
}

func (s *Server) readClaudeSettings() claudeSettings {
	var c claudeSettings
	root := envOr("CLAUDE_CONFIG_DIR", filepath.Join(s.home, ".claude"))
	if b, err := os.ReadFile(filepath.Join(root, "settings.json")); err == nil {
		json.Unmarshal(b, &c)
	}
	return c
}

// claudeEnv is a variable from Claude's settings, else the process environment.
func (c claudeSettings) env(k string) string {
	if v := c.Env[k]; v != "" {
		return v
	}
	return os.Getenv(k)
}

// claudeModels reports whether the list came from Claude Code itself (true)
// or from the static fallback below (false).
func (s *Server) claudeModels(ctx context.Context, info *ModelInfo) bool {
	sg := claudeSGLauncher() != ""
	var cfg claudeSettings
	if !sg { // claude-sg's settings live inside the container
		cfg = s.readClaudeSettings()
		info.Default, info.DefaultEffort = cfg.Model, cfg.Effort
	}
	if models, err := claudeModelProbe(ctx, s.claudeProbeCommand()); err == nil {
		applyClaudeProbe(info, models)
		if cfg.Model != "" {
			found := false
			for _, m := range info.Models {
				found = found || m.ID == cfg.Model
			}
			if !found {
				info.Models = append(info.Models, ModelOpt{ID: cfg.Model, Note: "当前默认"})
			}
		}
		return true
	}
	if sg {
		for _, id := range []string{"opus", "sonnet", "haiku"} {
			info.Models = append(info.Models, ModelOpt{ID: id, Label: strings.Title(id)})
		}
		return false
	}
	seen := map[string]bool{}
	add := func(o ModelOpt) {
		if o.ID != "" && !seen[o.ID] {
			seen[o.ID] = true
			info.Models = append(info.Models, o)
		}
	}
	// Aliases first, showing what each points to when a gateway remaps them.
	for _, a := range []struct{ id, env string }{{"opus", "ANTHROPIC_DEFAULT_OPUS_MODEL"}, {"sonnet", "ANTHROPIC_DEFAULT_SONNET_MODEL"}, {"haiku", "ANTHROPIC_DEFAULT_HAIKU_MODEL"}} {
		add(ModelOpt{ID: a.id, Label: strings.Title(a.id), Note: cfg.env(a.env)})
	}
	// A gateway declares its models and what they support:
	// "model=effort,xhigh_effort,max_effort;other=effort".
	var gw []ModelOpt
	for _, part := range strings.Split(cfg.env("CLAUDE_CODE_MODEL_CAPABILITIES"), ";") {
		id, caps, hasCaps := strings.Cut(strings.TrimSpace(part), "=")
		if id == "" {
			continue
		}
		o := ModelOpt{ID: id}
		if hasCaps {
			o.Efforts = []string{}
			for _, c := range strings.Split(caps, ",") {
				switch strings.TrimSpace(c) {
				case "effort":
					o.Efforts = append(o.Efforts, "low", "medium", "high")
				case "xhigh_effort":
					o.Efforts = append(o.Efforts, "xhigh")
				case "max_effort":
					o.Efforts = append(o.Efforts, "max")
				}
			}
		}
		gw = append(gw, o)
	}
	sort.SliceStable(gw, func(i, j int) bool { return gw[i].ID < gw[j].ID })
	for _, o := range gw {
		add(o)
	}
	add(ModelOpt{ID: cfg.Model})
	return false
}

func (s *Server) codexModels(ctx context.Context, info *ModelInfo) {
	home := s.codexHome()
	// The configured default (config.toml's top-level model / model_reasoning_effort).
	if b, err := os.ReadFile(filepath.Join(home, "config.toml")); err == nil {
		for _, l := range strings.Split(string(b), "\n") {
			l = strings.TrimSpace(l)
			if strings.HasPrefix(l, "[") {
				break
			}
			k, v, ok := strings.Cut(l, "=")
			if !ok {
				continue
			}
			v = strings.Trim(strings.TrimSpace(v), `"'`)
			switch strings.TrimSpace(k) {
			case "model":
				info.Default = v
			case "model_reasoning_effort":
				info.DefaultEffort = v
			}
		}
	}
	bin := findBin("codex", s.home)
	if bin != "" {
		for _, m := range codexModelList(ctx, bin) {
			info.Models = append(info.Models, m)
		}
	}
	if info.Default != "" {
		found := false
		for _, m := range info.Models {
			found = found || m.ID == info.Default
		}
		if !found {
			info.Models = append([]ModelOpt{{ID: info.Default, Note: "当前默认"}}, info.Models...)
		}
	}
	union := map[string]bool{}
	for _, m := range info.Models {
		for _, e := range m.Efforts {
			union[e] = true
		}
	}
	if len(union) > 0 {
		info.Efforts = nil
		for _, e := range []string{"minimal", "low", "medium", "high", "xhigh", "max", "ultra"} {
			if union[e] {
				info.Efforts = append(info.Efforts, e)
			}
		}
	}
}

// codexModelList asks a short-lived `codex app-server` (stdio, JSON lines).
func codexModelList(ctx context.Context, bin string) []ModelOpt {
	cmd := exec.CommandContext(ctx, bin, "app-server")
	in, _ := cmd.StdinPipe()
	out, _ := cmd.StdoutPipe()
	if cmd.Start() != nil {
		return nil
	}
	defer func() { in.Close(); cmd.Process.Kill(); cmd.Wait() }()
	send := func(v any) { in.Write(append(jsonNoEscape(v), '\n')) }
	sc := bufio.NewScanner(out)
	sc.Buffer(make([]byte, 0, 1<<20), 16<<20)
	wait := func(id int) json.RawMessage {
		for sc.Scan() {
			var m struct {
				ID     *int            `json:"id"`
				Method string          `json:"method"`
				Result json.RawMessage `json:"result"`
			}
			if json.Unmarshal(sc.Bytes(), &m) == nil && m.ID != nil && *m.ID == id && m.Method == "" {
				return m.Result
			}
		}
		return nil
	}
	send(map[string]any{"id": 1, "method": "initialize", "params": map[string]any{"clientInfo": map[string]any{"name": "collie", "version": "1"}}})
	if wait(1) == nil {
		return nil
	}
	send(map[string]any{"method": "initialized", "params": map[string]any{}})
	send(map[string]any{"id": 2, "method": "model/list", "params": map[string]any{}})
	var res struct {
		Data []struct {
			ID      string `json:"id"`
			Model   string `json:"model"`
			Display string `json:"displayName"`
			Desc    string `json:"description"`
			Hidden  bool   `json:"hidden"`
			Efforts []struct {
				Effort string `json:"reasoningEffort"`
			} `json:"supportedReasoningEfforts"`
		} `json:"data"`
	}
	json.Unmarshal(wait(2), &res)
	var list []ModelOpt
	for _, d := range res.Data {
		if d.Hidden {
			continue
		}
		id := d.Model
		if id == "" {
			id = d.ID
		}
		o := ModelOpt{ID: id, Label: d.Display, Note: d.Desc}
		if d.Efforts != nil {
			o.Efforts = make([]string, 0, len(d.Efforts))
			for _, e := range d.Efforts {
				o.Efforts = append(o.Efforts, e.Effort)
			}
		}
		list = append(list, o)
	}
	return list
}

var agyModelRe = regexp.MustCompile(`^(\S+)\t(.+)$`)

func orderedAgyEfforts(set map[string]bool) []string {
	out := []string{}
	for _, e := range allEfforts {
		if set[e] {
			out = append(out, e)
		}
	}
	return out
}

func (s *Server) readAgyDefaultModel() string {
	var cfg struct {
		Model string `json:"model"`
	}
	if b, err := os.ReadFile(filepath.Join(s.agyCLIHome(), "settings.json")); err == nil {
		json.Unmarshal(b, &cfg)
	}
	return strings.TrimSpace(cfg.Model)
}

func parseAgyModels(raw string) []ModelOpt {
	var out []ModelOpt
	for _, l := range strings.Split(raw, "\n") {
		if m := agyModelRe.FindStringSubmatch(strings.TrimSpace(l)); m != nil {
			out = append(out, ModelOpt{ID: m[1], Label: strings.TrimSpace(m[2])})
		}
	}
	return out
}

// applyAgyModelCapabilities annotates agy models and default-model efforts.
// Concrete agy models either bake their thinking level into the ID suffix
// (conflicting with --effort) or are fixed profiles like orchestrators (which
// reject --effort). Omitting --model lets agy apply --effort to the configured
// default model's base family, limited to the efforts that family actually has.
func applyAgyModelCapabilities(info *ModelInfo, defaultSetting string) {
	familyEfforts := map[string]map[string]bool{}
	for _, m := range info.Models {
		if base, eff, ok := splitAgyModelEffort(m.ID); ok {
			if familyEfforts[base] == nil {
				familyEfforts[base] = map[string]bool{}
			}
			familyEfforts[base][eff] = true
		}
	}
	for i := range info.Models {
		m := &info.Models[i]
		if agyRejectsEffort(m.ID) {
			m.Efforts = []string{}
		} else if fam := orderedAgyEfforts(familyEfforts[m.ID]); len(fam) > 0 {
			m.Efforts = fam
		} else {
			m.Efforts = []string{}
		}
	}

	info.Default = ""
	info.DefaultEffort = ""
	info.Efforts = []string{}
	defaultSetting = strings.TrimSpace(defaultSetting)
	if defaultSetting == "" {
		return
	}
	var matched *ModelOpt
	for i := range info.Models {
		if info.Models[i].ID == defaultSetting || strings.EqualFold(info.Models[i].Label, defaultSetting) {
			matched = &info.Models[i]
			break
		}
	}
	defID := defaultSetting
	if matched != nil {
		defID = matched.ID
	}
	info.Default = defID
	if base, eff, ok := splitAgyModelEffort(defID); ok {
		info.DefaultEffort = eff
		info.Efforts = orderedAgyEfforts(familyEfforts[base])
	} else if matched != nil && len(matched.Efforts) > 0 {
		info.Efforts = append([]string{}, matched.Efforts...)
	} else if fam := orderedAgyEfforts(familyEfforts[defID]); len(fam) > 0 {
		info.Efforts = fam
	}
}

// fetchAgyModels runs `<bin> models` within ctx and parses its tab-separated
// model list.
func fetchAgyModels(ctx context.Context, bin string) ([]ModelOpt, error) {
	if bin == "" {
		return nil, nil
	}
	cmd := exec.CommandContext(ctx, bin, "models")
	cmd.WaitDelay = 500 * time.Millisecond
	out, err := cmd.Output()
	if err != nil {
		return nil, err
	}
	return parseAgyModels(string(out)), nil
}

func (s *Server) agyModels(ctx context.Context, info *ModelInfo) {
	info.Efforts = []string{}
	bin := findBin("agy", s.home)
	if bin == "" {
		return
	}
	models, err := fetchAgyModels(ctx, bin)
	if err != nil {
		return
	}
	info.Models = models
	applyAgyModelCapabilities(info, s.readAgyDefaultModel())
}

// parseDoubaoModels parses `doubao model list --json` stdout into Collie's
// ModelInfo fields, aligning default model/effort with the `is_default: true`
// entry (Auto + high).
func parseDoubaoModels(raw []byte) (models []ModelOpt, defID, defEffort string, defEfforts []string, ok bool) {
	var env struct {
		Type    string `json:"type"`
		Subtype string `json:"subtype"`
		IsError bool   `json:"is_error"`
		Result  struct {
			Type   string `json:"type"`
			Models []struct {
				ID            string   `json:"id"`
				Name          string   `json:"name"`
				IsDefault     bool     `json:"is_default"`
				Efforts       []string `json:"efforts"`
				DefaultEffort string   `json:"default_effort"`
			} `json:"models"`
		} `json:"result"`
	}
	decode := func(b []byte) bool {
		return json.Unmarshal(b, &env) == nil && env.Subtype == "success" && !env.IsError && len(env.Result.Models) > 0
	}
	if !decode(raw) {
		lines := strings.Split(string(raw), "\n")
		matched := false
		for i := len(lines) - 1; i >= 0; i-- {
			l := strings.TrimSpace(lines[i])
			if strings.HasPrefix(l, "{") && decode([]byte(l)) {
				matched = true
				break
			}
		}
		if !matched {
			return nil, "", "", nil, false
		}
	}

	models = make([]ModelOpt, 0, len(env.Result.Models))
	autoIdx := -1
	defIdx := -1
	for i, m := range env.Result.Models {
		if m.ID == "" {
			continue
		}
		effs := make([]string, 0, len(m.Efforts))
		for _, e := range m.Efforts {
			if e = strings.TrimSpace(e); e != "" {
				effs = append(effs, e)
			}
		}
		models = append(models, ModelOpt{
			ID:      m.ID,
			Label:   strings.TrimSpace(m.Name),
			Efforts: effs,
		})
		idx := len(models) - 1
		if m.IsDefault && defIdx < 0 {
			defIdx = idx
			defID = m.ID
			defEffort = strings.TrimSpace(m.DefaultEffort)
			defEfforts = effs
		}
		if autoIdx < 0 && (strings.EqualFold(strings.TrimSpace(m.Name), defaultDoubaoModel) || m.ID == defaultDoubaoModelID) {
			autoIdx = idx
		}
		_ = i
	}
	if len(models) == 0 {
		return nil, "", "", nil, false
	}
	if defIdx < 0 {
		pick := autoIdx
		if pick < 0 {
			pick = 0
		}
		defID = models[pick].ID
		defEfforts = models[pick].Efforts
		for _, m := range env.Result.Models {
			if m.ID == defID && strings.TrimSpace(m.DefaultEffort) != "" {
				defEffort = strings.TrimSpace(m.DefaultEffort)
				break
			}
		}
	}
	if defEffort == "" {
		defEffort = defaultDoubaoEffort
	}
	return models, defID, defEffort, defEfforts, true
}

func (s *Server) doubaoModels(ctx context.Context, info *ModelInfo) {
	// Default alignment ("Auto" + "high") even if the probe fails or times out.
	info.Default = defaultDoubaoModel
	info.DefaultEffort = defaultDoubaoEffort
	info.Efforts = []string{"low", "medium", "high"}

	bin := findBin("doubao", s.home)
	if bin == "" {
		return
	}
	dctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	cmd := exec.CommandContext(dctx, bin, "model", "list", "--json")
	cmd.Env = doubaoEnv(os.Environ())
	cmd.WaitDelay = 500 * time.Millisecond
	// Read stdout only: `doubao model list --json` prints progress/telemetry on stderr.
	out, err := cmd.Output()
	if err != nil {
		return
	}
	models, defID, defEffort, defEfforts, ok := parseDoubaoModels(out)
	if !ok {
		return
	}
	info.Models = models
	info.Default = defID
	info.DefaultEffort = defEffort
	if len(defEfforts) > 0 {
		info.Efforts = append([]string(nil), defEfforts...)
	}
}

var ansiEscapeRe = regexp.MustCompile(`\x1b(?:\[[0-?]*[ -/]*[@-~]|\][^\x07\x1b]*(?:\x07|\x1b\\))`)

func stripANSI(s string) string {
	return ansiEscapeRe.ReplaceAllString(s, "")
}

// stripCursorModelMarkers strips trailing "(default)", "(current)", or
// "(current, default)" tags without disturbing display-name parentheticals
// such as "(Max)" or "(High)".
func stripCursorModelMarkers(s string) (clean string, isDefault, isCurrent bool) {
	clean = strings.TrimSpace(s)
	for strings.HasSuffix(clean, ")") {
		open := strings.LastIndex(clean, "(")
		if open < 0 {
			break
		}
		inner := strings.TrimSpace(clean[open+1 : len(clean)-1])
		if inner == "" {
			break
		}
		parts := strings.Split(inner, ",")
		allMarker := true
		var foundDef, foundCur bool
		for _, p := range parts {
			switch strings.ToLower(strings.TrimSpace(p)) {
			case "default":
				foundDef = true
			case "current":
				foundCur = true
			default:
				allMarker = false
			}
		}
		if !allMarker || (!foundDef && !foundCur) {
			break
		}
		isDefault = isDefault || foundDef
		isCurrent = isCurrent || foundCur
		clean = strings.TrimSpace(clean[:open])
	}
	return clean, isDefault, isCurrent
}

func isValidCursorModelID(id string) bool {
	if id == "" || strings.ContainsAny(id, " \t") || !modelNameRe.MatchString(id) {
		return false
	}
	for i := 0; i < len(id); i++ {
		c := id[i]
		if (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') {
			return true
		}
	}
	return false
}

// parseCursorModels parses `cursor-agent --list-models` stdout after stripping
// ANSI escape sequences. Supports the native "<id> - <displayName> (current, default)"
// format as well as optional leading list bullets or bare model ID lines.
func parseCursorModels(raw string) (models []ModelOpt, defID string) {
	cleaned := strings.ReplaceAll(stripANSI(raw), "\r\n", "\n")
	seen := map[string]bool{}
	var defaultID, currentID string

	for _, line := range strings.Split(cleaned, "\n") {
		l := strings.TrimSpace(line)
		if l == "" {
			continue
		}
		low := strings.ToLower(l)
		if low == "available models" || low == "available models:" ||
			strings.HasPrefix(low, "tip:") ||
			strings.HasPrefix(low, "no models available") ||
			strings.HasPrefix(low, "error:") ||
			strings.HasPrefix(low, "warning:") ||
			strings.HasPrefix(l, "⚠") ||
			strings.HasPrefix(low, "loading") {
			continue
		}
		for _, bullet := range []string{"- ", "* ", "• "} {
			if strings.HasPrefix(l, bullet) {
				l = strings.TrimSpace(strings.TrimPrefix(l, bullet))
				break
			}
		}

		var id, label string
		var isDef, isCur bool
		if left, right, hasSep := strings.Cut(l, " - "); hasSep {
			id = strings.TrimSpace(left)
			label, isDef, isCur = stripCursorModelMarkers(right)
			if !isDef && !isCur {
				var idDef, idCur bool
				id, idDef, idCur = stripCursorModelMarkers(id)
				isDef = isDef || idDef
				isCur = isCur || idCur
			}
		} else {
			id, isDef, isCur = stripCursorModelMarkers(l)
		}
		if !isValidCursorModelID(id) || seen[id] {
			continue
		}
		seen[id] = true
		models = append(models, ModelOpt{
			ID:      id,
			Label:   label,
			Efforts: []string{},
		})
		if isDef && defaultID == "" {
			defaultID = id
		}
		if isCur && currentID == "" {
			currentID = id
		}
	}
	return models, orStr(defaultID, currentID)
}

func (s *Server) cursorModels(ctx context.Context, info *ModelInfo) {
	// Cursor CLI embeds thinking variants in model IDs / bracket params and
	// has no independent --effort flag.
	info.Efforts = []string{}

	bin := findBin("cursor-agent", s.home)
	if bin == "" {
		return
	}
	cctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	cmd := exec.CommandContext(cctx, bin, "--list-models")
	cmd.Env = append(cursorEnv(os.Environ()), "NO_COLOR=1")
	cmd.WaitDelay = 500 * time.Millisecond
	out, err := cmd.Output()
	if err != nil {
		return
	}
	models, defID := parseCursorModels(string(out))
	if len(models) == 0 {
		return
	}
	info.Models = models
	info.Default = defID
}

// parsePiModels parses `pi --list-models --offline` table output:
// `provider  model  context  max-out  thinking  images`
func parsePiModels(raw string) []ModelOpt {
	cleaned := strings.ReplaceAll(stripANSI(raw), "\r\n", "\n")
	seen := map[string]bool{}
	var out []ModelOpt
	for _, line := range strings.Split(cleaned, "\n") {
		fields := strings.Fields(strings.TrimSpace(line))
		if len(fields) < 6 {
			continue
		}
		if strings.EqualFold(fields[0], "provider") && strings.EqualFold(fields[1], "model") {
			continue
		}
		provider := strings.TrimSpace(fields[0])
		model := strings.TrimSpace(fields[1])
		ctxCol := strings.TrimSpace(fields[2])
		thinking := strings.TrimSpace(fields[len(fields)-2])
		if provider == "" || model == "" {
			continue
		}
		id := provider + "/" + model
		if base, _, ok := splitPiModelThinking(id); ok {
			id = base
		}
		if !modelNameRe.MatchString(id) || seen[id] {
			continue
		}
		seen[id] = true
		note := provider
		if ctxCol != "" && ctxCol != "-" {
			note += " · " + ctxCol
		}
		effs := []string{}
		if strings.EqualFold(thinking, "yes") || strings.EqualFold(thinking, "true") {
			effs = append([]string(nil), piEfforts...)
		}
		out = append(out, ModelOpt{
			ID:      id,
			Label:   model,
			Note:    note,
			Efforts: effs,
		})
	}
	return out
}

func readPiSettings(home string) (defModel, defEffort string) {
	root := envOr("PI_CODING_AGENT_DIR", filepath.Join(home, ".pi", "agent"))
	b, err := os.ReadFile(filepath.Join(root, "settings.json"))
	if err != nil {
		return "", ""
	}
	var cfg struct {
		DefaultProvider      string `json:"defaultProvider"`
		DefaultModel         string `json:"defaultModel"`
		DefaultThinkingLevel string `json:"defaultThinkingLevel"`
	}
	if json.Unmarshal(b, &cfg) != nil {
		return "", ""
	}
	m := strings.TrimSpace(cfg.DefaultModel)
	p := strings.TrimSpace(cfg.DefaultProvider)
	eff := strings.TrimSpace(cfg.DefaultThinkingLevel)
	if m != "" {
		if base, suf, ok := splitPiModelThinking(m); ok {
			m = base
			if eff == "" {
				eff = suf
			}
		}
		if !strings.Contains(m, "/") && p != "" {
			m = p + "/" + m
		}
	}
	return m, eff
}

func (s *Server) piModels(ctx context.Context, info *ModelInfo) {
	info.Efforts = append([]string(nil), piEfforts...)
	info.Default, info.DefaultEffort = readPiSettings(s.home)

	bin := findBin("pi", s.home)
	if bin == "" {
		return
	}
	pctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	cmd := exec.CommandContext(pctx, bin, "--list-models", "--offline")
	cmd.Env = append(piEnv(os.Environ()), "NO_COLOR=1")
	cmd.WaitDelay = 500 * time.Millisecond
	out, err := cmd.Output()
	if err != nil {
		return
	}
	models := parsePiModels(string(out))
	if len(models) == 0 {
		return
	}
	info.Models = models
	if info.Default != "" {
		for _, m := range models {
			if m.ID == info.Default {
				info.Efforts = append([]string{}, m.Efforts...)
				break
			}
		}
	}
}

func normalizeOmpModelEfforts(thinking []string) []string {
	have := map[string]bool{}
	var extras []string
	for _, e := range thinking {
		e = strings.ToLower(strings.TrimSpace(e))
		if e == "" || have[e] {
			continue
		}
		have[e] = true
		known := false
		for _, k := range ompEfforts {
			if k == e {
				known = true
				break
			}
		}
		if !known {
			extras = append(extras, e)
		}
	}
	if len(have) == 0 {
		return []string{}
	}
	// Reasoning models accept "off", their declared thinking levels, and "auto".
	have["off"] = true
	have["auto"] = true
	out := make([]string, 0, len(have))
	for _, k := range ompEfforts {
		if have[k] {
			out = append(out, k)
		}
	}
	out = append(out, extras...)
	return out
}

// parseOmpModels parses `omp models --json` stdout into Collie's ModelOpt list.
func parseOmpModels(raw []byte) []ModelOpt {
	var env struct {
		Models []struct {
			Provider  string   `json:"provider"`
			ID        string   `json:"id"`
			Selector  string   `json:"selector"`
			Name      string   `json:"name"`
			Reasoning bool     `json:"reasoning"`
			Thinking  []string `json:"thinking"`
		} `json:"models"`
	}
	if json.Unmarshal(raw, &env) != nil {
		// Fall back to the first JSON object if a banner preceded stdout.
		if idx := strings.IndexByte(string(raw), '{'); idx >= 0 {
			if json.Unmarshal(raw[idx:], &env) != nil {
				return nil
			}
		} else {
			return nil
		}
	}
	seen := map[string]bool{}
	out := make([]ModelOpt, 0, len(env.Models))
	for _, m := range env.Models {
		id := strings.TrimSpace(m.Selector)
		if id == "" {
			p := strings.TrimSpace(m.Provider)
			mid := strings.TrimSpace(m.ID)
			if p != "" && mid != "" && !strings.Contains(mid, "/") {
				id = p + "/" + mid
			} else {
				id = mid
			}
		}
		if base, _, ok := splitPiModelThinking(id); ok {
			id = base
		}
		if id == "" || !modelNameRe.MatchString(id) || seen[id] {
			continue
		}
		seen[id] = true
		effs := []string{}
		if m.Reasoning && len(m.Thinking) > 0 {
			effs = normalizeOmpModelEfforts(m.Thinking)
		}
		out = append(out, ModelOpt{
			ID:      id,
			Label:   orStr(strings.TrimSpace(m.Name), strings.TrimSpace(m.ID)),
			Note:    strings.TrimSpace(m.Provider),
			Efforts: effs,
		})
	}
	return out
}

// parseOmpConfig extracts `modelRoles.default` (stripping optional `:effort`)
// and top-level `defaultThinkingLevel` from `~/.omp/agent/config.yml`.
func parseOmpConfig(raw string) (defModel, defEffort string) {
	cleanYAMLVal := func(v string) string {
		v = strings.TrimSpace(v)
		if v == "" {
			return ""
		}
		if (strings.HasPrefix(v, "\"") && strings.HasSuffix(v, "\"")) ||
			(strings.HasPrefix(v, "'") && strings.HasSuffix(v, "'")) {
			if len(v) >= 2 {
				return strings.TrimSpace(v[1 : len(v)-1])
			}
		}
		if idx := strings.Index(v, " #"); idx >= 0 {
			v = v[:idx]
		}
		return strings.TrimSpace(v)
	}
	inRoles := false
	roleEffort := ""
	topEffort := ""
	for _, line := range strings.Split(strings.ReplaceAll(raw, "\r\n", "\n"), "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		indented := strings.HasPrefix(line, " ") || strings.HasPrefix(line, "\t")
		if !indented {
			inRoles = false
			k, v, ok := strings.Cut(trimmed, ":")
			if !ok {
				continue
			}
			key := strings.TrimSpace(k)
			val := cleanYAMLVal(v)
			switch key {
			case "modelRoles":
				if val == "" {
					inRoles = true
				}
			case "defaultThinkingLevel":
				topEffort = val
			}
			continue
		}
		if inRoles {
			k, v, ok := strings.Cut(trimmed, ":")
			if !ok {
				continue
			}
			if strings.TrimSpace(k) == "default" {
				val := cleanYAMLVal(v)
				if val != "" {
					if base, suf, has := splitPiModelThinking(val); has {
						defModel = base
						roleEffort = suf
					} else {
						defModel = val
					}
				}
			}
		}
	}
	defEffort = orStr(roleEffort, topEffort)
	return defModel, defEffort
}

func readOmpConfig(home string) (defModel, defEffort string) {
	var paths []string
	if dir := strings.TrimSpace(os.Getenv("PI_CODING_AGENT_DIR")); dir != "" {
		paths = append(paths, filepath.Join(dir, "config.yml"))
	}
	paths = append(paths, filepath.Join(home, ".omp", "agent", "config.yml"))
	for _, p := range paths {
		if b, err := os.ReadFile(p); err == nil {
			return parseOmpConfig(string(b))
		}
	}
	return "", ""
}

func (s *Server) ompModels(ctx context.Context, info *ModelInfo) {
	info.Efforts = append([]string(nil), ompEfforts...)
	info.Default, info.DefaultEffort = readOmpConfig(s.home)

	bin := findBin("omp", s.home)
	if bin == "" {
		return
	}
	octx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	cmd := exec.CommandContext(octx, bin, "models", "--json")
	cmd.Env = append(ompEnv(os.Environ()), "NO_COLOR=1")
	cmd.WaitDelay = 500 * time.Millisecond
	out, err := cmd.Output()
	if err != nil {
		return
	}
	models := parseOmpModels(out)
	if len(models) == 0 {
		return
	}
	info.Models = models
	if info.Default != "" {
		for _, m := range models {
			if m.ID == info.Default {
				info.Efforts = append([]string{}, m.Efforts...)
				return
			}
		}
	}
	union := map[string]bool{}
	for _, m := range models {
		for _, e := range m.Efforts {
			union[e] = true
		}
	}
	if len(union) > 0 {
		info.Efforts = make([]string, 0, len(union))
		for _, e := range ompEfforts {
			if union[e] {
				info.Efforts = append(info.Efforts, e)
			}
		}
	}
}

// detectInitialSettings probes local agents for a brand-new installation and,
// when `agy models` succeeds, warms modelCache so the first settings/composer
// load does not pay for a second CLI round-trip.
func (s *Server) detectInitialSettings(home string) Settings {
	return resolveDefaultSettings(home, findBin, func(ctx context.Context, bin string) ([]ModelOpt, error) {
		models, err := fetchAgyModels(ctx, bin)
		if err == nil && len(models) > 0 && s != nil {
			info := &ModelInfo{
				Kind:    "agy",
				Models:  append([]ModelOpt(nil), models...),
				Efforts: []string{},
			}
			applyAgyModelCapabilities(info, s.readAgyDefaultModel())
			modelCache.Lock()
			modelCache.m["agy"] = modelMark{at: time.Now(), info: info}
			modelCache.Unlock()
		}
		return models, err
	}, defaultAgyProbeTimeout)
}

// findBin locates an agent's program: PATH, the usual install places, then
// the user's login shell (which knows nvm and friends).
var binCache sync.Map

func findBin(name, home string) string {
	if v, ok := binCache.Load(name); ok {
		return v.(string)
	}
	found := ""
	if p, err := exec.LookPath(name); err == nil {
		found = p
	}
	for _, d := range []string{".local/bin", ".npm-global/bin", ".bun/bin", "bin"} {
		if found == "" && isFile(filepath.Join(home, d, name)) {
			found = filepath.Join(home, d, name)
		}
	}
	for _, d := range []string{"/opt/homebrew/bin", "/usr/local/bin"} {
		if found == "" && isFile(filepath.Join(d, name)) {
			found = filepath.Join(d, name)
		}
	}
	if found == "" {
		sh := os.Getenv("SHELL")
		if sh == "" {
			sh = "/bin/bash"
		}
		ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
		out, _ := exec.CommandContext(ctx, sh, "-lic", "command -v "+name).Output()
		cancel()
		for _, l := range strings.Split(string(out), "\n") {
			if l = strings.TrimSpace(l); filepath.IsAbs(l) && isFile(l) {
				found = l
			}
		}
	}
	if found == "" && name == "codex" {
		found = desktopCodex(home)
	}
	if found != "" {
		binCache.Store(name, found)
	}
	return found
}

// The ChatGPT desktop app bundles Codex without installing it on PATH.
func desktopCodex(home string) string {
	if runtime.GOOS != "darwin" {
		return ""
	}
	for _, dir := range []string{filepath.Join(home, "Applications"), "/Applications"} {
		path := filepath.Join(dir, "ChatGPT.app/Contents/Resources/codex-cli/CodexCLI.app/Contents/MacOS/codex")
		if bin, err := exec.LookPath(path); err == nil && filepath.IsAbs(bin) {
			return bin
		}
	}
	return ""
}
