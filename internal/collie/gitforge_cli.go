package collie

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	cliDefaultTimeout        = 15 * time.Second
	cliMaxTimeout            = 30 * time.Second
	cliDefaultMaxConcurrency = 2
	cliDefaultMaxParents     = 5
)

var (
	cliKindRe = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,31}$`)
	cliRepoRe = regexp.MustCompile(`^[A-Za-z0-9._-]+(/[A-Za-z0-9._-]+)+$`)
)

// CLIProviderSpec is the declarative JSON schema loaded from
// ~/.collie/providers/*.json. It lets Collie integrate with any local Git
// hosting CLI cleanly without hardcoding platform-specific code in Collie.
type CLIProviderSpec struct {
	Kind              string            `json:"kind"`
	Name              string            `json:"name"`
	Subtitle          string            `json:"subtitle,omitempty"`
	Description       string            `json:"description,omitempty"`
	CLILabel          string            `json:"cli_label,omitempty"`
	TermPR            string            `json:"term_pr,omitempty"` // "MR" or "PR"
	DefaultEvents     map[string]bool   `json:"default_events,omitempty"`
	PollInterval      int               `json:"poll_interval,omitempty"`
	TimeoutSec        int               `json:"timeout_sec,omitempty"`
	MaxConcurrency    int               `json:"max_concurrency,omitempty"`
	AuthErrorPatterns []string          `json:"auth_error_patterns,omitempty"`
	Probe             CLIProbeSpec      `json:"probe"`
	ReposCmd          *CLIReposSpec     `json:"repos_cmd,omitempty"`
	PollCmd           []string          `json:"poll_cmd,omitempty"`
	SampleCmd         []string          `json:"sample_cmd,omitempty"`
	CommentCmd        *CLICommentSpec   `json:"comment_cmd,omitempty"`
	Sources           []CLISourceSpec   `json:"sources,omitempty"`
	Env               map[string]string `json:"env,omitempty"`
}

// CLICommentSpec optionally configures how a local CLI provider posts a
// human-confirmed comment back to a PR/MR or Issue. Supported template vars:
// {{repo}}, {{number}}, {{mr}}, {{pr}}, {{kind}}, {{body}}, {{user}}.
type CLICommentSpec struct {
	Cmd       []string `json:"cmd"`
	StdinBody bool     `json:"stdin_body,omitempty"`
	IDPath    string   `json:"id_path,omitempty"`
	URLPath   string   `json:"url_path,omitempty"`
}

type CLIProbeSpec struct {
	Cmd            []string `json:"cmd"`
	UserPath       string   `json:"user_path,omitempty"`
	ReposPath      string   `json:"repos_path,omitempty"`
	RepoField      string   `json:"repo_field,omitempty"`
	OKPath         string   `json:"ok_path,omitempty"`
	AuthFailedPath string   `json:"auth_failed_path,omitempty"`
}

type CLIReposSpec struct {
	Cmd       []string `json:"cmd"`
	ItemsPath string   `json:"items_path,omitempty"`
	RepoField string   `json:"repo_field,omitempty"`
}

type CLISourceSpec struct {
	ID              string      `json:"id"`
	Scope           string      `json:"scope"` // "global" | "repo" | "item"
	ParentSource    string      `json:"parent_source,omitempty"`
	MaxParents      int         `json:"max_parents,omitempty"`
	EventKind       string      `json:"event_kind"` // "mr" | "pr" | "mention" | "issue" | "ci"
	Tag             string      `json:"tag,omitempty"`
	Cmd             []string    `json:"cmd"`
	PollExtraArgs   []string    `json:"poll_extra_args,omitempty"`
	SampleExtraArgs []string    `json:"sample_extra_args,omitempty"`
	ItemsPath       string      `json:"items_path,omitempty"`
	FilterMention   bool        `json:"filter_mention,omitempty"`
	FilterFailed    bool        `json:"filter_failed,omitempty"`
	Map             CLIFieldMap `json:"map,omitempty"`
}

type CLIFieldMap struct {
	ID               string   `json:"id,omitempty"`
	Number           string   `json:"number,omitempty"`
	Repo             string   `json:"repo,omitempty"`
	RepoFromURLRegex string   `json:"repo_from_url_regex,omitempty"`
	Title            string   `json:"title,omitempty"`
	Body             string   `json:"body,omitempty"`
	Author           string   `json:"author,omitempty"`
	AuthorType       string   `json:"author_type,omitempty"`
	BotTypes         []string `json:"bot_types,omitempty"`
	URL              string   `json:"url,omitempty"`
	At               string   `json:"at,omitempty"`
	Conclusion       string   `json:"conclusion,omitempty"`
	DedupTemplate    string   `json:"dedup_template,omitempty"`
}

var defaultAuthErrorPatterns = []string{
	"unauthorized",
	"unauthenticated",
	"not logged in",
	"login required",
	"token expired",
	"session expired",
	"authentication failed",
	"invalid token",
	"credential",
	"http 401",
	"status 401",
	"code 401",
}

// CLIProvider executes read-only subprocess queries described by a local
// ~/.collie/providers/*.json manifest, bounding per-call timeout (15s) and
// concurrency (max 2).
type CLIProvider struct {
	spec       CLIProviderSpec
	sourcePath string
	timeout    time.Duration
	sem        chan struct{}

	mu       sync.RWMutex
	lastUser string
}

// DiscoverCLIProviders scans <collieDir>/providers/*.json and returns all valid
// CLIProvider instances. Missing directory or malformed files are ignored with
// a log line so a bad user plugin never prevents Collie from starting.
func DiscoverCLIProviders(collieDir string) []*CLIProvider {
	dir := filepath.Join(collieDir, "providers")
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })

	seen := map[string]bool{feishuKind: true, githubKind: true}
	var out []*CLIProvider
	for _, ent := range entries {
		if ent.IsDir() || strings.HasPrefix(ent.Name(), ".") || !strings.HasSuffix(strings.ToLower(ent.Name()), ".json") {
			continue
		}
		full := filepath.Join(dir, ent.Name())
		p, err := loadCLIProvider(full)
		if err != nil {
			log.Printf("skip provider %s: %v", full, err)
			continue
		}
		if seen[p.Name()] {
			log.Printf("skip duplicate provider kind %q from %s", p.Name(), full)
			continue
		}
		seen[p.Name()] = true
		out = append(out, p)
	}
	return out
}

func loadCLIProvider(path string) (*CLIProvider, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var spec CLIProviderSpec
	if err := json.Unmarshal(raw, &spec); err != nil {
		return nil, fmt.Errorf("parse json: %w", err)
	}
	spec.Kind = strings.ToLower(strings.TrimSpace(spec.Kind))
	if !cliKindRe.MatchString(spec.Kind) {
		return nil, fmt.Errorf("invalid kind %q", spec.Kind)
	}
	if strings.TrimSpace(spec.Name) == "" {
		spec.Name = spec.Kind
	}
	if len(spec.Probe.Cmd) == 0 {
		return nil, errors.New("probe.cmd is required")
	}
	if len(spec.PollCmd) == 0 && len(spec.SampleCmd) == 0 && len(spec.Sources) == 0 {
		return nil, errors.New("at least one of poll_cmd, sample_cmd, or sources is required")
	}
	timeout := cliDefaultTimeout
	if spec.TimeoutSec > 0 {
		timeout = time.Duration(spec.TimeoutSec) * time.Second
		if timeout > cliMaxTimeout {
			timeout = cliMaxTimeout
		}
	}
	conc := cliDefaultMaxConcurrency
	if spec.MaxConcurrency == 1 {
		conc = 1
	}
	return &CLIProvider{
		spec:       spec,
		sourcePath: path,
		timeout:    timeout,
		sem:        make(chan struct{}, conc),
	}, nil
}

func (p *CLIProvider) Name() string { return p.spec.Kind }

func (p *CLIProvider) Meta() IntegrationMeta {
	sub := strings.TrimSpace(p.spec.Subtitle)
	if sub == "" {
		sub = fmt.Sprintf("%s 评审 · 评论 @我 · CI 追踪", p.TermPR())
	}
	desc := strings.TrimSpace(p.spec.Description)
	if desc == "" {
		desc = "通过本机声明式 CLI 插件复用已登录态，免公网增量追踪待评审变更、评论 @我 与失败检查。"
	}
	return IntegrationMeta{
		Kind:        p.spec.Kind,
		Name:        p.spec.Name,
		Subtitle:    sub,
		Description: desc,
		Order:       30,
	}
}

func (p *CLIProvider) TermPR() string {
	if t := strings.TrimSpace(p.spec.TermPR); t != "" {
		return t
	}
	return "MR"
}

func (p *CLIProvider) DefaultAuthMode() string { return "cli" }

func (p *CLIProvider) DefaultEvents() map[string]bool {
	if len(p.spec.DefaultEvents) > 0 {
		cp := make(map[string]bool, len(p.spec.DefaultEvents))
		for k, v := range p.spec.DefaultEvents {
			cp[k] = v
		}
		return cp
	}
	if strings.EqualFold(p.TermPR(), "MR") {
		return map[string]bool{"mr": true, "mention": true, "ci": true}
	}
	return map[string]bool{"pr": true, "issue": true, "ci": true}
}

func (p *CLIProvider) IsConfigured(_ IntegrationConfig, fc ForgeConfig) bool {
	return fc.Configured
}

func (p *CLIProvider) SecretSet(_ IntegrationConfig) bool { return false }

func (p *CLIProvider) ExtraView(_ IntegrationConfig) map[string]any {
	cliLabel := strings.TrimSpace(p.spec.CLILabel)
	if cliLabel == "" && len(p.spec.Probe.Cmd) > 0 {
		cliLabel = p.spec.Probe.Cmd[0]
	}
	displayPath := p.sourcePath
	if home, err := os.UserHomeDir(); err == nil && home != "" && strings.HasPrefix(displayPath, home+string(os.PathSeparator)) {
		displayPath = "~" + displayPath[len(home):]
	}
	out := map[string]any{
		"supports_pat":     false,
		"supports_gh":      false,
		"supports_comment": p.spec.CommentCmd != nil && len(p.spec.CommentCmd.Cmd) > 0,
		"provider_type":    "cli",
		"cli_label":        cliLabel,
		"provider_path":    displayPath,
	}
	p.mu.RLock()
	if p.lastUser != "" {
		out["user"] = p.lastUser
	}
	p.mu.RUnlock()
	return out
}

// ---------------------------------------------------------------- subprocess execution

func (p *CLIProvider) runCmd(ctx context.Context, argv []string) ([]byte, error) {
	return p.runCmdWithInput(ctx, argv, nil)
}

func (p *CLIProvider) runCmdWithInput(ctx context.Context, argv []string, stdin []byte) ([]byte, error) {
	if len(argv) == 0 || strings.TrimSpace(argv[0]) == "" {
		return nil, errors.New("empty command")
	}
	select {
	case p.sem <- struct{}{}:
		defer func() { <-p.sem }()
	case <-ctx.Done():
		return nil, ctx.Err()
	}

	cctx, cancel := context.WithTimeout(ctx, p.timeout)
	defer cancel()

	cmd := exec.CommandContext(cctx, argv[0], argv[1:]...)
	configureCLIProcGroup(cmd)
	cmd.WaitDelay = 200 * time.Millisecond
	if len(p.spec.Env) > 0 {
		env := os.Environ()
		for k, v := range p.spec.Env {
			env = append(env, k+"="+v)
		}
		cmd.Env = env
	}
	if len(stdin) > 0 {
		cmd.Stdin = bytes.NewReader(stdin)
	}
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	if err != nil {
		if errors.Is(cctx.Err(), context.DeadlineExceeded) {
			return nil, fmt.Errorf("本机 CLI 执行超时（>%s）", p.timeout)
		}
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		combined := strings.TrimSpace(stderr.String() + "\n" + stdout.String())
		if p.isAuthErrorText(combined) {
			return nil, fmt.Errorf("%w: %s 登录态已失效，请重新登录本机 CLI", ErrAuthFailed, p.spec.Name)
		}
		if errors.Is(err, exec.ErrNotFound) {
			return nil, fmt.Errorf("%w: 未找到本机 CLI 可执行文件 %q", ErrAuthFailed, argv[0])
		}
		if combined != "" {
			return nil, fmt.Errorf("CLI 执行失败：%s", clip(oneLine(combined), 160))
		}
		return nil, fmt.Errorf("CLI 执行失败：%v", err)
	}
	return stdout.Bytes(), nil
}

// PostComment implements ForgeCommenter by running the provider's declarative
// comment_cmd template after explicit human confirmation.
func (p *CLIProvider) PostComment(ctx context.Context, _ ForgeConfig, target ForgeCommentTarget, body string) (ForgeCommentResult, error) {
	if p.spec.CommentCmd == nil || len(p.spec.CommentCmd.Cmd) == 0 {
		displayPath := p.sourcePath
		if home, err := os.UserHomeDir(); err == nil && home != "" && strings.HasPrefix(displayPath, home+string(os.PathSeparator)) {
			displayPath = "~" + displayPath[len(home):]
		}
		return ForgeCommentResult{}, fmt.Errorf("插件 %s 未配置 comment_cmd，无法自动回评（请在 %s 中补充 comment_cmd）", p.spec.Name, displayPath)
	}
	repo := normalizeForgeRepo(target.Repo)
	if repo == "" || !cliRepoRe.MatchString(repo) {
		return ForgeCommentResult{}, errors.New("无效的仓库名称（需形如 owner/repo）")
	}
	if target.Number <= 0 {
		return ForgeCommentResult{}, errors.New("缺少有效的 PR/MR 编号")
	}
	body = strings.TrimSpace(body)
	if body == "" {
		return ForgeCommentResult{}, errors.New("评论内容不能为空")
	}

	kind := strings.ToLower(strings.TrimSpace(target.Kind))
	if kind == "" {
		kind = strings.ToLower(p.TermPR())
	}
	p.mu.RLock()
	selfUser := p.lastUser
	p.mu.RUnlock()

	numStr := strconv.Itoa(target.Number)
	vars := map[string]string{
		"repo":   repo,
		"number": numStr,
		"mr":     numStr,
		"pr":     numStr,
		"kind":   kind,
		"user":   selfUser,
	}
	var stdin []byte
	if p.spec.CommentCmd.StdinBody {
		stdin = []byte(body)
	} else {
		vars["body"] = body
	}
	argv := renderArgTemplate(p.spec.CommentCmd.Cmd, vars)
	raw, err := p.runCmdWithInput(ctx, argv, stdin)
	if err != nil {
		return ForgeCommentResult{}, err
	}

	res := ForgeCommentResult{
		Provider: p.spec.Kind,
		Repo:     repo,
		Number:   target.Number,
	}
	var doc any
	if json.Unmarshal(bytes.TrimSpace(raw), &doc) == nil && doc != nil {
		if authFailed, ok := jsonPath(doc, "auth_failed").(bool); ok && authFailed {
			return ForgeCommentResult{}, fmt.Errorf("%w: %s 登录态已失效，请重新登录本机 CLI", ErrAuthFailed, p.spec.Name)
		}
		if st := strings.ToLower(anyString(jsonPath(doc, "status"))); st == "error" || st == "failed" {
			errTxt := anyString(jsonPath(doc, "error"))
			if p.isAuthErrorText(errTxt) {
				return ForgeCommentResult{}, fmt.Errorf("%w: %s 登录态已失效", ErrAuthFailed, p.spec.Name)
			}
			return ForgeCommentResult{}, fmt.Errorf("回评命令返回错误：%s", clip(errTxt, 160))
		}
		idPaths := []string{p.spec.CommentCmd.IDPath, "id", "data.id", "data.Id", "comment_id"}
		for _, path := range idPaths {
			if path == "" {
				continue
			}
			if v := strings.TrimSpace(anyString(jsonPath(doc, path))); v != "" {
				res.CommentID = v
				break
			}
		}
		urlPaths := []string{p.spec.CommentCmd.URLPath, "html_url", "url", "data.html_url", "data.url", "data.URL"}
		for _, path := range urlPaths {
			if path == "" {
				continue
			}
			if v := strings.TrimSpace(anyString(jsonPath(doc, path))); v != "" {
				res.URL = v
				break
			}
		}
	}
	return res, nil
}

func (p *CLIProvider) isAuthErrorText(txt string) bool {
	lower := strings.ToLower(txt)
	if lower == "" {
		return false
	}
	for _, pat := range p.spec.AuthErrorPatterns {
		if pat = strings.ToLower(strings.TrimSpace(pat)); pat != "" && strings.Contains(lower, pat) {
			return true
		}
	}
	for _, pat := range defaultAuthErrorPatterns {
		if strings.Contains(lower, pat) {
			return true
		}
	}
	return false
}

func renderArgTemplate(tpl []string, vars map[string]string) []string {
	out := make([]string, 0, len(tpl))
	for _, arg := range tpl {
		s := arg
		for k, v := range vars {
			s = strings.ReplaceAll(s, "{{"+k+"}}", v)
		}
		out = append(out, s)
	}
	return out
}

// ---------------------------------------------------------------- Probe / Poll / Sample

func (p *CLIProvider) Probe(ctx context.Context, _ ForgeConfig) (ForgeIdentity, error) {
	raw, err := p.runCmd(ctx, p.spec.Probe.Cmd)
	if err != nil {
		return ForgeIdentity{}, err
	}
	var doc any
	if err := json.Unmarshal(raw, &doc); err != nil {
		return ForgeIdentity{}, fmt.Errorf("CLI probe 返回非法 JSON：%v", err)
	}

	if p.spec.Probe.AuthFailedPath != "" {
		if v := jsonPath(doc, p.spec.Probe.AuthFailedPath); truthy(v) {
			return ForgeIdentity{}, fmt.Errorf("%w: %s 登录态已失效，请重新登录本机 CLI", ErrAuthFailed, p.spec.Name)
		}
	}
	if authFailed, ok := jsonPath(doc, "auth_failed").(bool); ok && authFailed {
		return ForgeIdentity{}, fmt.Errorf("%w: %s 登录态已失效，请重新登录本机 CLI", ErrAuthFailed, p.spec.Name)
	}
	if p.spec.Probe.OKPath != "" {
		if v := jsonPath(doc, p.spec.Probe.OKPath); v != nil && !truthy(v) {
			return ForgeIdentity{}, fmt.Errorf("%w: %s 尚未完成本机登录", ErrAuthFailed, p.spec.Name)
		}
	}
	if st := strings.ToLower(anyString(jsonPath(doc, "status"))); st == "error" || st == "failed" {
		errTxt := anyString(jsonPath(doc, "error"))
		if p.isAuthErrorText(errTxt) {
			return ForgeIdentity{}, fmt.Errorf("%w: %s 登录态已失效", ErrAuthFailed, p.spec.Name)
		}
		return ForgeIdentity{}, fmt.Errorf("CLI 探测返回错误：%s", clip(errTxt, 120))
	}

	userPath := p.spec.Probe.UserPath
	if userPath == "" {
		userPath = "user"
	}
	user := strings.TrimSpace(anyString(jsonPath(doc, userPath)))
	if user == "" && userPath == "user" {
		user = strings.TrimSpace(anyString(jsonPath(doc, "username")))
	}
	if user == "" {
		// If neither user nor explicit ok_path succeeded, check if auth failed.
		if p.spec.Probe.OKPath == "" {
			return ForgeIdentity{}, fmt.Errorf("%w: 未能识别 %s 本机登录用户，请先完成本机 CLI 登录", ErrAuthFailed, p.spec.Name)
		}
	}

	var repos []string
	if p.spec.Probe.ReposPath != "" {
		repos = extractStringList(jsonPath(doc, p.spec.Probe.ReposPath), p.spec.Probe.RepoField)
	} else if rawRepos := jsonPath(doc, "repos"); rawRepos != nil {
		repos = extractStringList(rawRepos, p.spec.Probe.RepoField)
	}
	if len(repos) == 0 && p.spec.ReposCmd != nil && len(p.spec.ReposCmd.Cmd) > 0 {
		if rraw, rerr := p.runCmd(ctx, p.spec.ReposCmd.Cmd); rerr == nil {
			var rdoc any
			if json.Unmarshal(rraw, &rdoc) == nil {
				target := rdoc
				if p.spec.ReposCmd.ItemsPath != "" {
					target = jsonPath(rdoc, p.spec.ReposCmd.ItemsPath)
				}
				repos = extractStringList(target, p.spec.ReposCmd.RepoField)
			}
		}
	}

	p.mu.Lock()
	if user != "" {
		p.lastUser = user
	}
	p.mu.Unlock()

	return ForgeIdentity{
		User:   user,
		Repos:  repos,
		Detail: "本机 CLI 就绪",
	}, nil
}

func (p *CLIProvider) Poll(ctx context.Context, cfg ForgeConfig, cursor ForgeCursor) ([]ForgeItem, ForgeCursor, time.Duration, error) {
	items, err := p.fetch(ctx, cfg, cursor.Watermark, 20, true)
	if err != nil {
		return nil, cursor, 0, err
	}
	interval := time.Duration(p.spec.PollInterval) * time.Second
	return items, cursor, interval, nil
}

func (p *CLIProvider) Sample(ctx context.Context, cfg ForgeConfig, limit int) ([]ForgeItem, error) {
	if limit <= 0 {
		limit = 9
	}
	return p.fetch(ctx, cfg, time.Time{}, limit, false)
}

func (p *CLIProvider) fetch(ctx context.Context, cfg ForgeConfig, since time.Time, limit int, isPoll bool) ([]ForgeItem, error) {
	p.mu.RLock()
	selfUser := p.lastUser
	p.mu.RUnlock()

	sinceStr := ""
	if !since.IsZero() {
		sinceStr = since.UTC().Format(time.RFC3339)
	}

	// Mode A: canonical poll_cmd / sample_cmd returning ForgeItem JSON.
	if len(p.spec.Sources) == 0 {
		tpl := p.spec.PollCmd
		if !isPoll && len(p.spec.SampleCmd) > 0 {
			tpl = p.spec.SampleCmd
		}
		if len(tpl) == 0 {
			tpl = p.spec.SampleCmd
		}
		vars := map[string]string{
			"since": sinceStr,
			"limit": strconv.Itoa(limit),
			"repos": strings.Join(validCLIRepos(cfg.Repos), ","),
			"user":  selfUser,
		}
		raw, err := p.runCmd(ctx, renderArgTemplate(tpl, vars))
		if err != nil {
			return nil, err
		}
		return parseCanonicalForgeItems(raw)
	}

	// Mode B: declarative multi-source queries.
	var all []ForgeItem
	bySource := map[string][]ForgeItem{}

	// First pass: global & repo scoped sources.
	for _, src := range p.spec.Sources {
		if src.Scope == "item" {
			continue
		}
		if !eventEnabled(cfg.Events, normalizeEventKind(src.EventKind)) {
			continue
		}
		if src.Scope == "global" {
			if !cfg.WatchMentions {
				continue
			}
			its, err := p.runSource(ctx, src, map[string]string{
				"since": sinceStr,
				"limit": strconv.Itoa(limit),
				"user":  selfUser,
			}, isPoll, selfUser)
			if err != nil {
				if errors.Is(err, ErrAuthFailed) {
					return nil, err
				}
				continue
			}
			bySource[src.ID] = append(bySource[src.ID], its...)
			all = append(all, its...)
		} else if src.Scope == "repo" {
			repos := validCLIRepos(cfg.Repos)
			if len(repos) == 0 {
				continue
			}
			var wg sync.WaitGroup
			var mu sync.Mutex
			var authErr error
			for _, repo := range repos {
				repo := repo
				wg.Add(1)
				go func() {
					defer wg.Done()
					its, err := p.runSource(ctx, src, map[string]string{
						"repo":  repo,
						"since": sinceStr,
						"limit": strconv.Itoa(limit),
						"user":  selfUser,
					}, isPoll, selfUser)
					mu.Lock()
					defer mu.Unlock()
					if err != nil {
						if errors.Is(err, ErrAuthFailed) && authErr == nil {
							authErr = err
						}
						return
					}
					bySource[src.ID] = append(bySource[src.ID], its...)
					all = append(all, its...)
				}()
			}
			wg.Wait()
			if authErr != nil {
				return nil, authErr
			}
		}
	}

	// Second pass: item-scoped fan-out sources (e.g. comments or checks on active MRs).
	for _, src := range p.spec.Sources {
		if src.Scope != "item" {
			continue
		}
		if !eventEnabled(cfg.Events, normalizeEventKind(src.EventKind)) {
			continue
		}
		var parents []ForgeItem
		if src.ParentSource != "" {
			parents = append(parents, bySource[src.ParentSource]...)
		} else {
			for _, it := range all {
				k := normalizeEventKind(it.Kind)
				if (k == "mr" || k == "pr") && it.Repo != "" && it.Number > 0 {
					parents = append(parents, it)
				}
			}
		}
		// Deduplicate parent (repo, number) pairs and cap fan-out.
		seenParent := map[string]bool{}
		var uniq []ForgeItem
		for _, par := range parents {
			if !cliRepoRe.MatchString(par.Repo) || par.Number <= 0 {
				continue
			}
			pk := fmt.Sprintf("%s#%d", par.Repo, par.Number)
			if seenParent[pk] {
				continue
			}
			seenParent[pk] = true
			uniq = append(uniq, par)
		}
		sort.SliceStable(uniq, func(i, j int) bool { return uniq[i].At.After(uniq[j].At) })
		maxP := src.MaxParents
		if maxP <= 0 {
			maxP = cliDefaultMaxParents
		}
		if len(uniq) > maxP {
			uniq = uniq[:maxP]
		}

		var wg sync.WaitGroup
		var mu sync.Mutex
		var authErr error
		for _, par := range uniq {
			par := par
			wg.Add(1)
			go func() {
				defer wg.Done()
				its, err := p.runSource(ctx, src, map[string]string{
					"repo":   par.Repo,
					"mr":     strconv.Itoa(par.Number),
					"number": strconv.Itoa(par.Number),
					"title":  par.Title,
					"url":    par.URL,
					"since":  sinceStr,
					"limit":  strconv.Itoa(limit),
					"user":   selfUser,
				}, isPoll, selfUser)
				mu.Lock()
				defer mu.Unlock()
				if err != nil {
					if errors.Is(err, ErrAuthFailed) && authErr == nil {
						authErr = err
					}
					return
				}
				for i := range its {
					if its[i].Repo == "" {
						its[i].Repo = par.Repo
					}
					if its[i].Number == 0 {
						its[i].Number = par.Number
					}
					if its[i].URL == "" {
						its[i].URL = par.URL
					}
				}
				all = append(all, its...)
			}()
		}
		wg.Wait()
		if authErr != nil {
			return nil, authErr
		}
	}

	return all, nil
}

func validCLIRepos(repos []string) []string {
	var out []string
	for _, r := range repos {
		nr := normalizeForgeRepo(r)
		if nr != "" && cliRepoRe.MatchString(nr) && !slices.Contains(out, nr) {
			out = append(out, nr)
		}
	}
	return out
}

func (p *CLIProvider) runSource(ctx context.Context, src CLISourceSpec, vars map[string]string, isPoll bool, selfUser string) ([]ForgeItem, error) {
	argv := renderArgTemplate(src.Cmd, vars)
	if isPoll && len(src.PollExtraArgs) > 0 && vars["since"] != "" {
		argv = append(argv, renderArgTemplate(src.PollExtraArgs, vars)...)
	} else if !isPoll && len(src.SampleExtraArgs) > 0 {
		argv = append(argv, renderArgTemplate(src.SampleExtraArgs, vars)...)
	}

	raw, err := p.runCmd(ctx, argv)
	if err != nil {
		return nil, err
	}

	var doc any
	if err := json.Unmarshal(raw, &doc); err != nil {
		return nil, fmt.Errorf("source %s invalid JSON: %w", src.ID, err)
	}
	if authFailed, ok := jsonPath(doc, "auth_failed").(bool); ok && authFailed {
		return nil, fmt.Errorf("%w: %s 登录态已失效", ErrAuthFailed, p.spec.Name)
	}
	if st := strings.ToLower(anyString(jsonPath(doc, "status"))); st == "error" || st == "failed" {
		errTxt := anyString(jsonPath(doc, "error"))
		if p.isAuthErrorText(errTxt) {
			return nil, fmt.Errorf("%w: %s 登录态已失效", ErrAuthFailed, p.spec.Name)
		}
		return nil, fmt.Errorf("source %s error: %s", src.ID, errTxt)
	}

	// If no custom mapping and no items_path, accept canonical ForgeItem JSON.
	if src.ItemsPath == "" && src.Map.ID == "" && src.Map.Title == "" && src.Map.Body == "" {
		return parseCanonicalForgeItems(raw)
	}

	rows := jsonExtractArray(doc, src.ItemsPath)
	var repoURLRe *regexp.Regexp
	if src.Map.RepoFromURLRegex != "" {
		repoURLRe, _ = regexp.Compile(src.Map.RepoFromURLRegex)
	}

	out := make([]ForgeItem, 0, len(rows))
	for _, row := range rows {
		it, ok := mapCLIRow(p.spec.Kind, src, row, vars, repoURLRe, selfUser)
		if !ok {
			continue
		}
		out = append(out, it)
	}
	return out, nil
}

func mapCLIRow(kind string, src CLISourceSpec, row any, vars map[string]string, repoURLRe *regexp.Regexp, selfUser string) (ForgeItem, bool) {
	m := src.Map
	id := strings.TrimSpace(anyString(jsonPath(row, m.ID)))
	num := anyInt(jsonPath(row, m.Number))
	if num == 0 && vars["number"] != "" {
		num, _ = strconv.Atoi(vars["number"])
	}
	url := strings.TrimSpace(anyString(jsonPath(row, m.URL)))
	if url == "" {
		url = vars["url"]
	}
	repo := strings.TrimSpace(anyString(jsonPath(row, m.Repo)))
	if repo == "" && repoURLRe != nil && url != "" {
		if sub := repoURLRe.FindStringSubmatch(url); len(sub) > 1 {
			repo = sub[1]
		}
	}
	if repo == "" {
		repo = vars["repo"]
	}
	repo = normalizeForgeRepo(repo)

	title := strings.TrimSpace(anyString(jsonPath(row, m.Title)))
	body := strings.TrimSpace(anyString(jsonPath(row, m.Body)))
	if title == "" && body != "" {
		title = clip(oneLine(body), 120)
	}
	if title == "" && vars["title"] != "" {
		title = vars["title"]
	}
	author := strings.TrimSpace(anyString(jsonPath(row, m.Author)))
	authorType := strings.ToLower(strings.TrimSpace(anyString(jsonPath(row, m.AuthorType))))
	isBot := false
	if authorType != "" {
		botTypes := m.BotTypes
		if len(botTypes) == 0 {
			botTypes = []string{"bot", "app"}
		}
		for _, bt := range botTypes {
			if strings.EqualFold(authorType, bt) {
				isBot = true
				break
			}
		}
	}

	concl := strings.TrimSpace(anyString(jsonPath(row, m.Conclusion)))
	if src.FilterFailed && !isCIFailureConclusion(concl) {
		return ForgeItem{}, false
	}
	if src.FilterMention && selfUser != "" {
		hay := strings.ToLower(title + "\n" + body)
		if !strings.Contains(hay, "@"+strings.ToLower(selfUser)) {
			return ForgeItem{}, false
		}
	}

	atStr := strings.TrimSpace(anyString(jsonPath(row, m.At)))
	at := parseFlexibleTime(atStr)

	dedup := ""
	if m.DedupTemplate != "" {
		dedup = renderTemplateString(m.DedupTemplate, map[string]string{
			"kind":       kind,
			"id":         id,
			"repo":       repo,
			"number":     strconv.Itoa(num),
			"mr":         strconv.Itoa(num),
			"at":         atStr,
			"conclusion": concl,
		})
	}
	if dedup == "" {
		if id != "" {
			dedup = fmt.Sprintf("%s:%s:%s:%s:%s", kind, src.ID, repo, id, atStr)
		} else {
			dedup = fmt.Sprintf("%s:%s:%s#%d:%s", kind, src.ID, repo, num, atStr)
		}
	}

	return ForgeItem{
		DedupKey:   dedup,
		Kind:       normalizeEventKind(src.EventKind),
		Repo:       repo,
		Number:     num,
		Tag:        strings.TrimSpace(src.Tag),
		Title:      title,
		Body:       body,
		Author:     author,
		AuthorBot:  isBot,
		Conclusion: concl,
		URL:        url,
		At:         at,
	}, true
}

func renderTemplateString(tpl string, vars map[string]string) string {
	s := tpl
	for k, v := range vars {
		s = strings.ReplaceAll(s, "{{"+k+"}}", v)
	}
	return s
}

func parseCanonicalForgeItems(raw []byte) ([]ForgeItem, error) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 {
		return nil, nil
	}
	if trimmed[0] == '[' {
		var list []ForgeItem
		if err := json.Unmarshal(trimmed, &list); err != nil {
			return nil, err
		}
		return list, nil
	}
	var env struct {
		AuthFailed bool        `json:"auth_failed"`
		Error      string      `json:"error"`
		Items      []ForgeItem `json:"items"`
	}
	if err := json.Unmarshal(trimmed, &env); err != nil {
		return nil, err
	}
	if env.AuthFailed {
		msg := strings.TrimSpace(env.Error)
		if msg == "" {
			msg = "本机 CLI 登录态已失效"
		}
		return nil, fmt.Errorf("%w: %s", ErrAuthFailed, msg)
	}
	if env.Error != "" {
		return nil, errors.New(env.Error)
	}
	return env.Items, nil
}

// ---------------------------------------------------------------- JSON path helpers

// jsonPath resolves a simple dot-delimited path (e.g. "data.user.login") on a
// decoded JSON structure, returning nil if any segment is missing.
func jsonPath(doc any, path string) any {
	if doc == nil || strings.TrimSpace(path) == "" {
		return nil
	}
	cur := doc
	for _, seg := range strings.Split(path, ".") {
		seg = strings.TrimSpace(seg)
		if seg == "" {
			continue
		}
		m, ok := cur.(map[string]any)
		if !ok {
			return nil
		}
		cur = m[seg]
		if cur == nil {
			return nil
		}
	}
	return cur
}

// jsonExtractArray resolves a dot path that may include array-flattening "[]"
// segments (e.g. "data.threads[].Comments") and returns the flattened slice.
func jsonExtractArray(doc any, path string) []any {
	if doc == nil {
		return nil
	}
	if strings.TrimSpace(path) == "" {
		if arr, ok := doc.([]any); ok {
			return arr
		}
		return nil
	}
	cur := []any{doc}
	for _, rawSeg := range strings.Split(path, ".") {
		seg := strings.TrimSpace(rawSeg)
		if seg == "" {
			continue
		}
		flatten := strings.HasSuffix(seg, "[]")
		key := strings.TrimSuffix(seg, "[]")
		var next []any
		for _, node := range cur {
			var val any = node
			if key != "" {
				m, ok := node.(map[string]any)
				if !ok {
					continue
				}
				val = m[key]
			}
			if val == nil {
				continue
			}
			if flatten {
				if arr, ok := val.([]any); ok {
					next = append(next, arr...)
				}
			} else {
				next = append(next, val)
			}
		}
		cur = next
		if len(cur) == 0 {
			return nil
		}
	}
	var out []any
	for _, v := range cur {
		if arr, ok := v.([]any); ok {
			out = append(out, arr...)
		} else if v != nil {
			out = append(out, v)
		}
	}
	return out
}

func extractStringList(val any, field string) []string {
	arr, ok := val.([]any)
	if !ok {
		return nil
	}
	var out []string
	for _, elem := range arr {
		var s string
		if field != "" {
			s = strings.TrimSpace(anyString(jsonPath(elem, field)))
		} else {
			s = strings.TrimSpace(anyString(elem))
		}
		if s != "" && !slices.Contains(out, s) {
			out = append(out, s)
		}
	}
	return out
}

func anyString(v any) string {
	switch x := v.(type) {
	case string:
		return x
	case float64:
		if x == float64(int64(x)) {
			return strconv.FormatInt(int64(x), 10)
		}
		return strconv.FormatFloat(x, 'f', -1, 64)
	case bool:
		return strconv.FormatBool(x)
	case map[string]any:
		if msg, ok := x["message"].(string); ok {
			return msg
		}
		b, _ := json.Marshal(x)
		return string(b)
	default:
		return ""
	}
}

func anyInt(v any) int {
	switch x := v.(type) {
	case float64:
		return int(x)
	case int:
		return x
	case string:
		n, _ := strconv.Atoi(strings.TrimSpace(x))
		return n
	default:
		return 0
	}
}

func truthy(v any) bool {
	switch x := v.(type) {
	case bool:
		return x
	case string:
		s := strings.ToLower(strings.TrimSpace(x))
		return s == "true" || s == "1" || s == "yes" || s == "ok" || s == "ready" || s == "success"
	case float64:
		return x != 0
	default:
		return false
	}
}

func parseFlexibleTime(s string) time.Time {
	s = strings.TrimSpace(s)
	if s == "" {
		return time.Time{}
	}
	for _, layout := range []string{time.RFC3339Nano, time.RFC3339, "2006-01-02 15:04:05", "2006-01-02T15:04:05"} {
		if t, err := time.Parse(layout, s); err == nil {
			return t
		}
	}
	if sec, err := strconv.ParseInt(s, 10, 64); err == nil && sec > 0 {
		if sec > 1e12 {
			return time.UnixMilli(sec)
		}
		return time.Unix(sec, 0)
	}
	return time.Time{}
}
