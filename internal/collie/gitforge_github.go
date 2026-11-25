package collie

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	githubKind        = "github"
	githubDefaultBase = "https://api.github.com"
)

// GitHubProvider implements ForgeProvider and ForgeProviderMeta using GitHub
// REST API with conditional polling (If-Modified-Since / ETag / 304) and
// X-Poll-Interval adherence. It defaults to reusing local `gh auth token`
// (zero credential persistence) and optionally falls back to a read-only PAT
// stored in SecretStore (~/.collie/secrets.json, 0600).
type GitHubProvider struct {
	baseURL    string
	httpClient *http.Client
	ghTokenFn  func(ctx context.Context) (string, error)

	mu        sync.RWMutex
	lastRate  map[string]any
	lastLogin string
}

func newGitHubProvider() *GitHubProvider {
	return &GitHubProvider{
		baseURL:    githubDefaultBase,
		httpClient: &http.Client{Timeout: 15 * time.Second},
	}
}

func (p *GitHubProvider) Name() string { return githubKind }

func (p *GitHubProvider) Meta() IntegrationMeta {
	return IntegrationMeta{
		Kind:        githubKind,
		Name:        "GitHub",
		Subtitle:    "PR · Issue · CI 主动追踪",
		Description: "零公网增量拉取关注仓库与 @我 的动态，支持只提醒、自动调研出建议，或交给负责该 PR 的 Agent。",
		Order:       20,
	}
}

func (p *GitHubProvider) TermPR() string          { return "PR" }
func (p *GitHubProvider) DefaultAuthMode() string { return "gh" }
func (p *GitHubProvider) DefaultEvents() map[string]bool {
	return map[string]bool{"pr": true, "issue": true, "ci": true}
}

func (p *GitHubProvider) IsConfigured(cfg IntegrationConfig, fc ForgeConfig) bool {
	if !fc.Configured {
		return false
	}
	if fc.AuthMode == "pat" {
		return strings.TrimSpace(cfg.Secrets["pat"]) != ""
	}
	return true
}

func (p *GitHubProvider) SecretSet(cfg IntegrationConfig) bool {
	return strings.TrimSpace(cfg.Secrets["pat"]) != ""
}

func (p *GitHubProvider) ExtraView(cfg IntegrationConfig) map[string]any {
	out := map[string]any{
		"supports_pat": true,
		"supports_gh":  true,
		"auth_modes":   []string{"gh", "pat"},
	}
	p.mu.RLock()
	if p.lastLogin != "" {
		out["user"] = p.lastLogin
	}
	if len(p.lastRate) > 0 {
		out["rate_limit"] = p.lastRate
	}
	p.mu.RUnlock()
	return out
}

func (p *GitHubProvider) apiBase() string {
	if strings.TrimSpace(p.baseURL) != "" {
		return strings.TrimRight(p.baseURL, "/")
	}
	return githubDefaultBase
}

func (p *GitHubProvider) client() *http.Client {
	if p.httpClient != nil {
		return p.httpClient
	}
	return &http.Client{Timeout: 15 * time.Second}
}

func (p *GitHubProvider) resolveToken(ctx context.Context, cfg ForgeConfig) (string, error) {
	mode := strings.ToLower(strings.TrimSpace(cfg.AuthMode))
	if mode == "" {
		mode = "gh"
	}
	if mode == "pat" {
		tok := strings.TrimSpace(cfg.Secrets["pat"])
		if tok == "" {
			return "", fmt.Errorf("%w: 请填写只读个人令牌（PAT）或切换为复用本机 gh 登录", ErrAuthFailed)
		}
		return tok, nil
	}
	fn := p.ghTokenFn
	if fn == nil {
		fn = defaultGHToken
	}
	tok, err := fn(ctx)
	if err != nil {
		return "", fmt.Errorf("%w: %v", ErrAuthFailed, err)
	}
	tok = strings.TrimSpace(tok)
	if tok == "" {
		return "", fmt.Errorf("%w: 本机 gh 未返回有效令牌，请先执行 gh auth login 或切换为 PAT", ErrAuthFailed)
	}
	return tok, nil
}

// defaultGHToken invokes GitHub CLI (`gh auth token`) safely. Some hosts carry
// an unrelated binary also named `gh`, so we verify both exit code and a clean
// single-line token shape before accepting the output.
func defaultGHToken(ctx context.Context) (string, error) {
	bin, err := exec.LookPath("gh")
	if err != nil {
		return "", errors.New("本机未安装 GitHub CLI (gh)，可安装并登录 gh，或切换为个人令牌（PAT）")
	}
	cctx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	cmd := exec.CommandContext(cctx, bin, "auth", "token")
	var outBuf, errBuf bytes.Buffer
	cmd.Stdout = &outBuf
	cmd.Stderr = &errBuf
	if err := cmd.Run(); err != nil {
		errTxt := strings.ToLower(errBuf.String() + " " + outBuf.String())
		if strings.Contains(errTxt, "unknown command") || strings.Contains(errTxt, "unknown flag") || strings.Contains(errTxt, "usage:") {
			return "", errors.New("本机 PATH 中的 gh 不是 GitHub CLI，请切换为个人令牌（PAT）或安装官方 GitHub CLI")
		}
		return "", errors.New("本机 gh 尚未登录 GitHub，请先运行 gh auth login 或切换为个人令牌（PAT）")
	}
	tok := strings.TrimSpace(outBuf.String())
	if tok == "" || strings.ContainsAny(tok, " \t\r\n") || len(tok) < 10 {
		return "", errors.New("本机 gh 未返回有效的 GitHub 令牌，请改用个人令牌（PAT）")
	}
	return tok, nil
}

type ghHTTPResp struct {
	Status       int
	Body         []byte
	ETag         string
	LastModified string
	PollInterval time.Duration
	RateLimit    map[string]any
}

func (p *GitHubProvider) doGET(ctx context.Context, tok, path, etag, lastMod string) (ghHTTPResp, error) {
	url := p.apiBase() + path
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return ghHTTPResp{}, err
	}
	req.Header.Set("Authorization", "Bearer "+tok)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	req.Header.Set("User-Agent", "collie-gitforge")
	if etag != "" {
		req.Header.Set("If-None-Match", etag)
	}
	if lastMod != "" {
		req.Header.Set("If-Modified-Since", lastMod)
	}

	resp, err := p.client().Do(req)
	if err != nil {
		return ghHTTPResp{}, err
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	out := ghHTTPResp{
		Status:       resp.StatusCode,
		Body:         body,
		ETag:         strings.TrimSpace(resp.Header.Get("ETag")),
		LastModified: strings.TrimSpace(resp.Header.Get("Last-Modified")),
	}
	if sec, err := strconv.Atoi(strings.TrimSpace(resp.Header.Get("X-Poll-Interval"))); err == nil && sec > 0 {
		out.PollInterval = time.Duration(sec) * time.Second
	}
	if rem := strings.TrimSpace(resp.Header.Get("X-RateLimit-Remaining")); rem != "" {
		rl := map[string]any{"remaining": rem}
		if n, err := strconv.Atoi(rem); err == nil {
			rl["remaining"] = n
		}
		if lim := strings.TrimSpace(resp.Header.Get("X-RateLimit-Limit")); lim != "" {
			if n, err := strconv.Atoi(lim); err == nil {
				rl["limit"] = n
			} else {
				rl["limit"] = lim
			}
		}
		out.RateLimit = rl
		p.mu.Lock()
		p.lastRate = rl
		p.mu.Unlock()
	}

	if resp.StatusCode == http.StatusUnauthorized {
		return out, fmt.Errorf("%w: GitHub 凭据无效或已过期（HTTP 401），请更新 PAT 或重新登录 gh", ErrAuthFailed)
	}
	if resp.StatusCode == http.StatusForbidden {
		lower := strings.ToLower(string(body))
		if strings.Contains(lower, "rate limit") {
			return out, errors.New("GitHub API 触发频率限制，稍后自动重试")
		}
		if strings.Contains(lower, "bad credentials") || strings.Contains(lower, "token") || strings.Contains(lower, "resource not accessible") {
			return out, fmt.Errorf("%w: GitHub 令牌权限不足或已失效（HTTP 403）", ErrAuthFailed)
		}
		return out, fmt.Errorf("GitHub API 返回 HTTP 403")
	}
	if resp.StatusCode == http.StatusTooManyRequests {
		return out, errors.New("GitHub API 请求过多（HTTP 429），稍后自动重试")
	}
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusNotModified {
		return out, fmt.Errorf("GitHub API 返回 HTTP %d", resp.StatusCode)
	}
	return out, nil
}

func (p *GitHubProvider) Probe(ctx context.Context, cfg ForgeConfig) (ForgeIdentity, error) {
	tok, err := p.resolveToken(ctx, cfg)
	if err != nil {
		return ForgeIdentity{}, err
	}
	res, err := p.doGET(ctx, tok, "/user", "", "")
	if err != nil {
		return ForgeIdentity{}, err
	}
	var u struct {
		Login string `json:"login"`
	}
	if err := json.Unmarshal(res.Body, &u); err != nil || strings.TrimSpace(u.Login) == "" {
		return ForgeIdentity{}, fmt.Errorf("%w: 无法识别 GitHub 登录身份", ErrAuthFailed)
	}
	login := strings.TrimSpace(u.Login)
	p.mu.Lock()
	p.lastLogin = login
	p.mu.Unlock()

	var repos []string
	if rres, rerr := p.doGET(ctx, tok, "/user/repos?sort=pushed&per_page=8", "", ""); rerr == nil && rres.Status == http.StatusOK {
		var list []struct {
			FullName string `json:"full_name"`
		}
		if json.Unmarshal(rres.Body, &list) == nil {
			for _, r := range list {
				if nr := normalizeForgeRepo(r.FullName); nr != "" {
					repos = append(repos, nr)
				}
			}
		}
	}
	return ForgeIdentity{
		User:      login,
		Repos:     repos,
		RateLimit: res.RateLimit,
		Detail:    "304 条件请求就绪",
	}, nil
}

func (p *GitHubProvider) Poll(ctx context.Context, cfg ForgeConfig, cursor ForgeCursor) ([]ForgeItem, ForgeCursor, time.Duration, error) {
	tok, err := p.resolveToken(ctx, cfg)
	if err != nil {
		return nil, cursor, 0, err
	}
	next := cursor
	if next.RepoETags == nil {
		next.RepoETags = map[string]string{}
	}
	if next.RepoLastMod == nil {
		next.RepoLastMod = map[string]string{}
	}

	var items []ForgeItem
	var pollInterval time.Duration

	// 1. Participating notifications (@mention, review_requested, assign, etc.).
	if cfg.WatchMentions {
		lastMod := next.LastModified
		if lastMod == "" && !next.Watermark.IsZero() {
			lastMod = next.Watermark.UTC().Format(http.TimeFormat)
		}
		res, err := p.doGET(ctx, tok, "/notifications?participating=true&per_page=30", next.ETag, lastMod)
		if err != nil {
			return nil, cursor, 0, err
		}
		if res.PollInterval > pollInterval {
			pollInterval = res.PollInterval
		}
		if res.ETag != "" {
			next.ETag = res.ETag
		}
		if res.LastModified != "" {
			next.LastModified = res.LastModified
		}
		if res.Status == http.StatusOK {
			items = append(items, parseGitHubNotifications(res.Body)...)
		}
	}

	// 2. Watched repository events & failed Actions runs.
	wantEvents := eventEnabled(cfg.Events, "pr") || eventEnabled(cfg.Events, "issue")
	wantCI := eventEnabled(cfg.Events, "ci")
	for _, repo := range cfg.Repos {
		repo = normalizeForgeRepo(repo)
		if repo == "" {
			continue
		}
		if wantEvents {
			etag := next.RepoETags["evt:"+repo]
			res, err := p.doGET(ctx, tok, "/repos/"+repo+"/events?per_page=20", etag, "")
			if err != nil {
				if errors.Is(err, ErrAuthFailed) {
					return nil, cursor, 0, err
				}
				continue
			}
			if res.PollInterval > pollInterval {
				pollInterval = res.PollInterval
			}
			if res.ETag != "" {
				next.RepoETags["evt:"+repo] = res.ETag
			}
			if res.Status == http.StatusOK {
				items = append(items, parseGitHubRepoEvents(repo, res.Body)...)
			}
		}
		if wantCI {
			etag := next.RepoETags["ci:"+repo]
			res, err := p.doGET(ctx, tok, "/repos/"+repo+"/actions/runs?status=completed&per_page=10", etag, "")
			if err != nil {
				if errors.Is(err, ErrAuthFailed) {
					return nil, cursor, 0, err
				}
				continue
			}
			if res.ETag != "" {
				next.RepoETags["ci:"+repo] = res.ETag
			}
			if res.Status == http.StatusOK {
				items = append(items, parseGitHubActionRuns(repo, res.Body)...)
			}
		}
	}

	return items, next, pollInterval, nil
}

func (p *GitHubProvider) Sample(ctx context.Context, cfg ForgeConfig, limit int) ([]ForgeItem, error) {
	tok, err := p.resolveToken(ctx, cfg)
	if err != nil {
		return nil, err
	}
	if limit <= 0 {
		limit = 9
	}
	var items []ForgeItem

	if cfg.WatchMentions {
		if res, err := p.doGET(ctx, tok, "/notifications?participating=true&all=true&per_page=12", "", ""); err == nil && res.Status == http.StatusOK {
			items = append(items, parseGitHubNotifications(res.Body)...)
		} else if errors.Is(err, ErrAuthFailed) {
			return nil, err
		}
	}

	wantEvents := eventEnabled(cfg.Events, "pr") || eventEnabled(cfg.Events, "issue")
	wantCI := eventEnabled(cfg.Events, "ci")
	for _, repo := range cfg.Repos {
		repo = normalizeForgeRepo(repo)
		if repo == "" {
			continue
		}
		if wantEvents {
			if res, err := p.doGET(ctx, tok, "/repos/"+repo+"/events?per_page=10", "", ""); err == nil && res.Status == http.StatusOK {
				items = append(items, parseGitHubRepoEvents(repo, res.Body)...)
			} else if errors.Is(err, ErrAuthFailed) {
				return nil, err
			}
		}
		if wantCI {
			if res, err := p.doGET(ctx, tok, "/repos/"+repo+"/actions/runs?status=completed&per_page=8", "", ""); err == nil && res.Status == http.StatusOK {
				items = append(items, parseGitHubActionRuns(repo, res.Body)...)
			} else if errors.Is(err, ErrAuthFailed) {
				return nil, err
			}
		}
	}
	return items, nil
}

// ---------------------------------------------------------------- GitHub JSON parsers

func parseGitHubNotifications(raw []byte) []ForgeItem {
	var list []struct {
		ID         string `json:"id"`
		Reason     string `json:"reason"`
		UpdatedAt  string `json:"updated_at"`
		Repository struct {
			FullName string `json:"full_name"`
		} `json:"repository"`
		Subject struct {
			Title string `json:"title"`
			URL   string `json:"url"`
			Type  string `json:"type"`
		} `json:"subject"`
	}
	if json.Unmarshal(raw, &list) != nil {
		return nil
	}
	out := make([]ForgeItem, 0, len(list))
	for _, n := range list {
		repo := normalizeForgeRepo(n.Repository.FullName)
		if repo == "" {
			continue
		}
		at, _ := time.Parse(time.RFC3339, n.UpdatedAt)
		num := trailingNumber(n.Subject.URL)
		kind := "issue"
		webSeg := "issues"
		switch n.Subject.Type {
		case "PullRequest":
			kind = "pr"
			webSeg = "pull"
		case "CheckSuite":
			kind = "ci"
		case "Issue":
			kind = "issue"
		default:
			kind = "mention"
		}
		webURL := "https://github.com/" + repo
		if num > 0 && kind != "ci" {
			webURL = fmt.Sprintf("https://github.com/%s/%s/%d", repo, webSeg, num)
		}
		tag := ghReasonTag(n.Reason, kind)
		out = append(out, ForgeItem{
			DedupKey: fmt.Sprintf("gh:notif:%s:%s", n.ID, n.UpdatedAt),
			Kind:     kind,
			Repo:     repo,
			Number:   num,
			Tag:      tag,
			Title:    strings.TrimSpace(n.Subject.Title),
			URL:      webURL,
			At:       at,
		})
	}
	return out
}

func ghReasonTag(reason, kind string) string {
	switch reason {
	case "review_requested":
		return "PR 评审"
	case "mention", "team_mention":
		return "评论 @我"
	case "assign":
		return "指派给我"
	case "ci_activity":
		return "CI 动态"
	case "comment":
		if kind == "pr" {
			return "PR 新评论"
		}
		return "Issue 新评论"
	default:
		return defaultItemTag(kind, "PR")
	}
}

func parseGitHubRepoEvents(repo string, raw []byte) []ForgeItem {
	var list []struct {
		ID        string `json:"id"`
		Type      string `json:"type"`
		CreatedAt string `json:"created_at"`
		Actor     struct {
			Login string `json:"login"`
			Type  string `json:"type"`
		} `json:"actor"`
		Payload map[string]any `json:"payload"`
	}
	if json.Unmarshal(raw, &list) != nil {
		return nil
	}
	out := make([]ForgeItem, 0, len(list))
	for _, ev := range list {
		at, _ := time.Parse(time.RFC3339, ev.CreatedAt)
		author := strings.TrimSpace(ev.Actor.Login)
		isBot := strings.EqualFold(ev.Actor.Type, "Bot")
		p := ev.Payload
		action := str(p["action"])

		switch ev.Type {
		case "PullRequestEvent":
			num, _ := strconv.Atoi(str(dig(p, "pull_request", "number")))
			if num == 0 {
				num, _ = strconv.Atoi(str(p["number"]))
			}
			title := str(dig(p, "pull_request", "title"))
			url := str(dig(p, "pull_request", "html_url"))
			body := str(dig(p, "pull_request", "body"))
			tag := "PR " + action
			if action == "opened" {
				tag = "新建 PR"
			} else if action == "review_requested" {
				tag = "PR 评审"
			}
			out = append(out, ForgeItem{
				DedupKey:  "gh:evt:" + ev.ID,
				Kind:      "pr",
				Repo:      repo,
				Number:    num,
				Tag:       tag,
				Title:     title,
				Body:      body,
				Author:    author,
				AuthorBot: isBot,
				URL:       url,
				At:        at,
			})
		case "PullRequestReviewEvent", "PullRequestReviewCommentEvent":
			num, _ := strconv.Atoi(str(dig(p, "pull_request", "number")))
			title := str(dig(p, "pull_request", "title"))
			body := str(dig(p, "comment", "body"))
			if body == "" {
				body = str(dig(p, "review", "body"))
			}
			url := str(dig(p, "comment", "html_url"))
			if url == "" {
				url = str(dig(p, "review", "html_url"))
			}
			if url == "" {
				url = str(dig(p, "pull_request", "html_url"))
			}
			out = append(out, ForgeItem{
				DedupKey:  "gh:evt:" + ev.ID,
				Kind:      "pr",
				Repo:      repo,
				Number:    num,
				Tag:       "PR 评审评论",
				Title:     title,
				Body:      body,
				Author:    author,
				AuthorBot: isBot,
				URL:       url,
				At:        at,
			})
		case "IssueCommentEvent", "IssuesEvent":
			num, _ := strconv.Atoi(str(dig(p, "issue", "number")))
			title := str(dig(p, "issue", "title"))
			body := str(dig(p, "comment", "body"))
			if body == "" {
				body = str(dig(p, "issue", "body"))
			}
			url := str(dig(p, "comment", "html_url"))
			if url == "" {
				url = str(dig(p, "issue", "html_url"))
			}
			kind := "issue"
			tag := "Issue 新评论"
			if dig(p, "issue", "pull_request") != nil {
				kind = "pr"
				tag = "PR 新评论"
			} else if ev.Type == "IssuesEvent" {
				tag = "Issue " + action
			}
			out = append(out, ForgeItem{
				DedupKey:  "gh:evt:" + ev.ID,
				Kind:      kind,
				Repo:      repo,
				Number:    num,
				Tag:       tag,
				Title:     title,
				Body:      body,
				Author:    author,
				AuthorBot: isBot,
				URL:       url,
				At:        at,
			})
		}
	}
	return out
}

func parseGitHubActionRuns(repo string, raw []byte) []ForgeItem {
	var resp struct {
		Runs []struct {
			ID           int64  `json:"id"`
			RunAttempt   int    `json:"run_attempt"`
			Name         string `json:"name"`
			HeadBranch   string `json:"head_branch"`
			Conclusion   string `json:"conclusion"`
			HTMLURL      string `json:"html_url"`
			UpdatedAt    string `json:"updated_at"`
			PullRequests []struct {
				Number int `json:"number"`
			} `json:"pull_requests"`
		} `json:"workflow_runs"`
	}
	if json.Unmarshal(raw, &resp) != nil {
		return nil
	}
	out := make([]ForgeItem, 0, len(resp.Runs))
	for _, r := range resp.Runs {
		concl := strings.TrimSpace(r.Conclusion)
		if concl == "" {
			continue
		}
		at, _ := time.Parse(time.RFC3339, r.UpdatedAt)
		num := 0
		if len(r.PullRequests) > 0 {
			num = r.PullRequests[0].Number
		}
		attempt := r.RunAttempt
		if attempt <= 0 {
			attempt = 1
		}
		title := strings.TrimSpace(r.Name)
		if r.HeadBranch != "" {
			title = fmt.Sprintf("%s (%s) · %s", title, r.HeadBranch, concl)
		}
		out = append(out, ForgeItem{
			DedupKey:   fmt.Sprintf("gh:run:%d:%d:%s", r.ID, attempt, concl),
			Kind:       "ci",
			Repo:       repo,
			Number:     num,
			Tag:        "CI 失败",
			Title:      title,
			Conclusion: concl,
			URL:        strings.TrimSpace(r.HTMLURL),
			At:         at,
		})
	}
	return out
}

func trailingNumber(u string) int {
	u = strings.TrimRight(strings.TrimSpace(u), "/")
	idx := strings.LastIndexByte(u, '/')
	if idx < 0 || idx == len(u)-1 {
		return 0
	}
	n, _ := strconv.Atoi(u[idx+1:])
	return n
}
