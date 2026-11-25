package collie

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestGitHubProviderConditionalPollAnd304(t *testing.T) {
	var mu sync.Mutex
	var notifReqs int
	var gotIfNoneMatch, gotIfModSince string

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer gh_tok_test_123456" {
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"message":"Bad credentials"}`))
			return
		}
		w.Header().Set("X-RateLimit-Remaining", "4991")
		w.Header().Set("X-RateLimit-Limit", "5000")

		switch r.URL.Path {
		case "/user":
			_, _ = w.Write([]byte(`{"login":"octodev"}`))
		case "/user/repos":
			_, _ = w.Write([]byte(`[{"full_name":"octodev/collie"}]`))
		case "/notifications":
			mu.Lock()
			notifReqs++
			n := notifReqs
			gotIfNoneMatch = r.Header.Get("If-None-Match")
			gotIfModSince = r.Header.Get("If-Modified-Since")
			mu.Unlock()

			w.Header().Set("X-Poll-Interval", "90")
			w.Header().Set("ETag", `"notif-etag-1"`)
			w.Header().Set("Last-Modified", "Wed, 25 Nov 2026 12:00:00 GMT")
			if n >= 2 && r.Header.Get("If-None-Match") == `"notif-etag-1"` {
				w.WriteHeader(http.StatusNotModified)
				return
			}
			_, _ = w.Write([]byte(`[
				{
					"id": "notif-901",
					"reason": "review_requested",
					"updated_at": "2026-11-25T12:01:00Z",
					"repository": {"full_name": "octodev/collie"},
					"subject": {
						"title": "Add gitforge engine",
						"url": "https://api.github.com/repos/octodev/collie/pulls/214",
						"type": "PullRequest"
					}
				}
			]`))
		case "/repos/octodev/collie/events":
			w.Header().Set("ETag", `"evt-etag-1"`)
			if r.Header.Get("If-None-Match") == `"evt-etag-1"` {
				w.WriteHeader(http.StatusNotModified)
				return
			}
			_, _ = w.Write([]byte(`[
				{
					"id": "evt-777",
					"type": "IssueCommentEvent",
					"created_at": "2026-11-25T12:02:00Z",
					"actor": {"login": "reviewer1", "type": "User"},
					"payload": {
						"action": "created",
						"issue": {"number": 88, "title": "Need ETag caching", "html_url": "https://github.com/octodev/collie/issues/88"},
						"comment": {"body": "Looks good!", "html_url": "https://github.com/octodev/collie/issues/88#issuecomment-1"}
					}
				}
			]`))
		case "/repos/octodev/collie/actions/runs":
			w.Header().Set("ETag", `"ci-etag-1"`)
			if r.Header.Get("If-None-Match") == `"ci-etag-1"` {
				w.WriteHeader(http.StatusNotModified)
				return
			}
			_, _ = w.Write([]byte(`{
				"workflow_runs": [
					{
						"id": 55501,
						"run_attempt": 1,
						"name": "Go Test",
						"head_branch": "main",
						"conclusion": "failure",
						"html_url": "https://github.com/octodev/collie/actions/runs/55501",
						"updated_at": "2026-11-25T12:03:00Z",
						"pull_requests": [{"number": 214}]
					},
					{
						"id": 55502,
						"run_attempt": 1,
						"name": "Lint",
						"head_branch": "main",
						"conclusion": "success",
						"html_url": "https://github.com/octodev/collie/actions/runs/55502",
						"updated_at": "2026-11-25T12:03:30Z"
					}
				]
			}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	prov := newGitHubProvider()
	prov.baseURL = srv.URL
	prov.httpClient = srv.Client()
	prov.ghTokenFn = func(ctx context.Context) (string, error) { return "gh_tok_test_123456", nil }

	cfg := ForgeConfig{
		AuthMode:      "gh",
		WatchMentions: true,
		Repos:         []string{"octodev/collie"},
		Events:        map[string]bool{"pr": true, "issue": true, "ci": true},
		HandleMode:    forgeModeAdvise,
	}

	id, err := prov.Probe(context.Background(), cfg)
	if err != nil {
		t.Fatalf("Probe: %v", err)
	}
	if id.User != "octodev" || len(id.Repos) != 1 || id.Repos[0] != "octodev/collie" {
		t.Fatalf("unexpected identity: %+v", id)
	}

	// First Poll: receives 200 OK with notification, repo event, and workflow runs, plus X-Poll-Interval 90s.
	items1, cur1, interval1, err := prov.Poll(context.Background(), cfg, ForgeCursor{})
	if err != nil {
		t.Fatalf("Poll #1: %v", err)
	}
	if interval1 != 90*time.Second {
		t.Fatalf("X-Poll-Interval = %v, want 90s", interval1)
	}
	if cur1.ETag != `"notif-etag-1"` || cur1.LastModified != "Wed, 25 Nov 2026 12:00:00 GMT" {
		t.Fatalf("unexpected cursor after poll #1: %+v", cur1)
	}
	if len(items1) != 4 {
		t.Fatalf("expected 4 raw items (1 notif + 1 event + 2 runs), got %d: %+v", len(items1), items1)
	}
	if items1[0].DedupKey != "gh:notif:notif-901:2026-11-25T12:01:00Z" || items1[0].Number != 214 {
		t.Fatalf("unexpected notification item: %+v", items1[0])
	}

	// Second Poll with cursor: must send If-None-Match & If-Modified-Since and get 304 -> 0 items!
	items2, _, interval2, err := prov.Poll(context.Background(), cfg, cur1)
	if err != nil {
		t.Fatalf("Poll #2: %v", err)
	}
	if len(items2) != 0 {
		t.Fatalf("expected 0 items on 304 Not Modified, got %d", len(items2))
	}
	if interval2 != 90*time.Second {
		t.Fatalf("expected X-Poll-Interval preserved on 304, got %v", interval2)
	}
	mu.Lock()
	inm, ims := gotIfNoneMatch, gotIfModSince
	mu.Unlock()
	if inm != `"notif-etag-1"` || ims != "Wed, 25 Nov 2026 12:00:00 GMT" {
		t.Fatalf("conditional headers missing: If-None-Match=%q If-Modified-Since=%q", inm, ims)
	}
}

func TestGitHubProviderPATSecretStore0600AndAuthCircuitBreak(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"message":"Bad credentials"}`))
	}))
	defer srv.Close()

	prov := newGitHubProvider()
	prov.baseURL = srv.URL
	prov.httpClient = srv.Client()

	_, mgr, _, _ := newTestForgeEnv(t, prov)

	secretPAT := "github_pat_super_secret_token_987654"
	enabled := true
	view, err := mgr.Save("github", SaveRequest{
		Enabled: &enabled,
		Config:  json.RawMessage(`{"auth_mode":"pat","watch_mentions":true,"configured":true}`),
		Secrets: map[string]string{"pat": secretPAT},
	})
	if err != nil {
		t.Fatalf("Save github PAT: %v", err)
	}
	if !view.SecretSet {
		t.Fatalf("expected secret_set=true after saving PAT")
	}

	// Verify secrets.json permission is strictly 0600 and API view never leaks PAT.
	fi, err := os.Stat(mgr.secrets.Path())
	if err != nil {
		t.Fatalf("stat secrets.json: %v", err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Fatalf("secrets.json perm = %04o, want 0600", fi.Mode().Perm())
	}
	rawView, _ := json.Marshal(mgr.ListViews())
	if strings.Contains(string(rawView), secretPAT) {
		t.Fatalf("ListViews leaked PAT!")
	}

	// Probe with invalid PAT must return ErrAuthFailed so Manager circuit-breaks.
	_, err = prov.Probe(context.Background(), ForgeConfig{
		AuthMode: "pat",
		Secrets:  map[string]string{"pat": secretPAT},
	})
	if !errors.Is(err, ErrAuthFailed) {
		t.Fatalf("expected ErrAuthFailed on HTTP 401, got %v", err)
	}

	// Start manager and assert it immediately transitions to HealthError without leaking PAT.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	mgr.Start(ctx)
	defer mgr.Stop(time.Second)

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		h := mgr.Health("github")
		if h.State == HealthError {
			if strings.Contains(h.LastError, secretPAT) {
				t.Fatalf("health error leaked PAT: %q", h.LastError)
			}
			return
		}
		time.Sleep(15 * time.Millisecond)
	}
	t.Fatalf("expected github to circuit-break into HealthError, got %+v", mgr.Health("github"))
}
