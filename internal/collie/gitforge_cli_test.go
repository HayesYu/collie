package collie

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func writeFakeCLIScript(t *testing.T, dir, name, body string) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte("#!/bin/sh\n"+body), 0o755); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestCLIProviderDiscoverAndDeclarativeSources(t *testing.T) {
	t.Parallel()
	collieHome := t.TempDir()
	provDir := filepath.Join(collieHome, "providers")
	if err := os.MkdirAll(provDir, 0o700); err != nil {
		t.Fatal(err)
	}
	binDir := t.TempDir()
	fakeCLI := writeFakeCLIScript(t, binDir, "fake-forge", `
case "$1" in
  auth)
    echo '{"status":"success","data":{"authenticated":true,"identity":{"username":"alice"}}}'
    ;;
  repos)
    echo '{"status":"success","data":{"Repositories":[{"Path":"team/core-api"},{"Path":"team/web-ui"}]}}'
    ;;
  search-mr)
    echo '{"status":"success","data":{"merge_requests":[{"Id":"m1","Number":321,"Title":"Fix dedup race","URL":"https://git.example.test/team/core-api/merge_requests/321","UpdatedAt":"2026-11-25T10:15:00Z","CreatedBy":{"Username":"bob","Type":"personal"}}]}}'
    ;;
  comments)
    echo '{"status":"success","data":{"threads":[{"Comments":[{"Id":"c1","Content":"@alice could you double-check WAL locking?","UpdatedAt":"2026-11-25T10:20:00Z","CreatedBy":{"Username":"carol","Type":"personal"}},{"Id":"c2","Content":"@alice auto report","UpdatedAt":"2026-11-25T10:21:00Z","CreatedBy":{"Username":"scanner","Type":"app"}},{"Id":"c3","Content":"unrelated note","UpdatedAt":"2026-11-25T10:22:00Z","CreatedBy":{"Username":"dave","Type":"personal"}}]}]}}'
    ;;
  checks)
    echo '{"status":"success","data":{"check_runs":[{"Id":"chk1","Name":"unit-test","Description":"2 tests failed","Conclusion":"failed","DetailsURL":"https://git.example.test/team/core-api/jobs/1","UpdatedAt":"2026-11-25T10:25:00Z"},{"Id":"chk2","Name":"lint","Description":"ok","Conclusion":"succeeded","UpdatedAt":"2026-11-25T10:26:00Z"}]}}'
    ;;
  *)
    echo '{"status":"error","error":"unknown subcommand"}' >&2
    exit 1
    ;;
esac
`)

	spec := CLIProviderSpec{
		Kind:         "myforge",
		Name:         "MyForge",
		Subtitle:     "MR 评审 · 评论 @我 · CI 失败",
		TermPR:       "MR",
		CLILabel:     "fake-forge",
		TimeoutSec:   15,
		PollInterval: 60,
		Probe: CLIProbeSpec{
			Cmd:      []string{fakeCLI, "auth"},
			OKPath:   "data.authenticated",
			UserPath: "data.identity.username",
		},
		ReposCmd: &CLIReposSpec{
			Cmd:       []string{fakeCLI, "repos"},
			ItemsPath: "data.Repositories",
			RepoField: "Path",
		},
		Sources: []CLISourceSpec{
			{
				ID:        "mr_review",
				Scope:     "global",
				EventKind: "mr",
				Tag:       "指派给我评审",
				Cmd:       []string{fakeCLI, "search-mr"},
				ItemsPath: "data.merge_requests",
				Map: CLIFieldMap{
					ID:               "Id",
					Number:           "Number",
					RepoFromURLRegex: `https?://[^/]+/(.+?)/merge_requests/\d+`,
					Title:            "Title",
					Author:           "CreatedBy.Username",
					AuthorType:       "CreatedBy.Type",
					BotTypes:         []string{"app", "bot"},
					URL:              "URL",
					At:               "UpdatedAt",
					DedupTemplate:    "mf:mr:{{repo}}#{{number}}:{{at}}",
				},
			},
			{
				ID:            "mr_comments",
				Scope:         "item",
				EventKind:     "mention",
				Tag:           "评论 @我",
				FilterMention: true,
				Cmd:           []string{fakeCLI, "comments", "{{repo}}", "{{mr}}"},
				ItemsPath:     "data.threads[].Comments",
				Map: CLIFieldMap{
					ID:            "Id",
					Body:          "Content",
					Author:        "CreatedBy.Username",
					AuthorType:    "CreatedBy.Type",
					BotTypes:      []string{"app", "bot"},
					At:            "UpdatedAt",
					DedupTemplate: "mf:comment:{{repo}}#{{number}}:{{id}}",
				},
			},
			{
				ID:           "mr_checks",
				Scope:        "item",
				EventKind:    "ci",
				Tag:          "CI 失败",
				FilterFailed: true,
				Cmd:          []string{fakeCLI, "checks", "{{repo}}", "{{mr}}"},
				ItemsPath:    "data.check_runs",
				Map: CLIFieldMap{
					ID:            "Id",
					Title:         "Name",
					Body:          "Description",
					Conclusion:    "Conclusion",
					URL:           "DetailsURL",
					At:            "UpdatedAt",
					DedupTemplate: "mf:check:{{repo}}:{{id}}:{{conclusion}}",
				},
			},
		},
	}
	rawSpec, _ := json.MarshalIndent(spec, "", "  ")
	if err := os.WriteFile(filepath.Join(provDir, "myforge.json"), rawSpec, 0o600); err != nil {
		t.Fatal(err)
	}
	// Also place a malformed JSON and a reserved-kind ("github") JSON that must be skipped cleanly.
	_ = os.WriteFile(filepath.Join(provDir, "broken.json"), []byte("{not json"), 0o600)
	_ = os.WriteFile(filepath.Join(provDir, "dup.json"), []byte(`{"kind":"github","probe":{"cmd":["echo"]},"poll_cmd":["echo"]}`), 0o600)

	provs := DiscoverCLIProviders(collieHome)
	if len(provs) != 1 || provs[0].Name() != "myforge" {
		t.Fatalf("DiscoverCLIProviders = %d items, want 1 (myforge)", len(provs))
	}
	p := provs[0]

	id, err := p.Probe(context.Background(), ForgeConfig{})
	if err != nil {
		t.Fatalf("Probe: %v", err)
	}
	if id.User != "alice" || len(id.Repos) != 2 || id.Repos[0] != "team/core-api" {
		t.Fatalf("unexpected probe identity: %+v", id)
	}

	_, _, adapter, _ := newTestForgeEnv(t, p)
	samples, err := adapter.SamplePull(context.Background(), IntegrationConfig{
		Kind:   "myforge",
		Config: json.RawMessage(`{"configured":true,"watch_mentions":true,"handle_mode":"advise"}`),
	}, 5)
	if err != nil {
		t.Fatalf("SamplePull: %v", err)
	}
	// Expect 3 items: failing check (chk1), human @alice comment (c1), MR review (321).
	// Bot comment (c2), non-mention comment (c3), and passing check (chk2) must all be filtered out!
	if len(samples) != 3 {
		t.Fatalf("expected 3 filtered samples (ci + mention + mr), got %d: %+v", len(samples), samples)
	}
	if samples[0].Kind != "ci" || samples[1].Kind != "mention" || samples[2].Kind != "mr" {
		t.Fatalf("unexpected sample kinds order (newest first): %+v", samples)
	}
	if !strings.Contains(samples[2].Repo, "team/core-api !321") {
		t.Fatalf("expected MR repo label with !321, got %q", samples[2].Repo)
	}
}

func TestCLIProviderTimeoutConcurrencyAndAuthCircuitBreak(t *testing.T) {
	t.Parallel()
	binDir := t.TempDir()

	// 1) Timeout protection: script sleeps 2s while provider timeout is 120ms.
	slowCLI := writeFakeCLIScript(t, binDir, "slow-cli", `sleep 2; echo '{"user":"alice"}'`)
	slowProv := &CLIProvider{
		spec: CLIProviderSpec{
			Kind:  "slowforge",
			Name:  "SlowForge",
			Probe: CLIProbeSpec{Cmd: []string{slowCLI}},
		},
		timeout: 120 * time.Millisecond,
		sem:     make(chan struct{}, 2),
	}
	start := time.Now()
	_, err := slowProv.Probe(context.Background(), ForgeConfig{})
	if err == nil || !strings.Contains(err.Error(), "超时") {
		t.Fatalf("expected timeout error, got %v", err)
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Fatalf("timeout took too long: %v", elapsed)
	}

	// 2) Auth failure circuit-break: non-zero exit with "not logged in" -> ErrAuthFailed.
	unauthCLI := writeFakeCLIScript(t, binDir, "unauth-cli", `echo "error: not logged in, run login first" >&2; exit 1`)
	unauthProv := &CLIProvider{
		spec: CLIProviderSpec{
			Kind:    "unauthforge",
			Name:    "UnauthForge",
			Probe:   CLIProbeSpec{Cmd: []string{unauthCLI}},
			PollCmd: []string{unauthCLI},
		},
		timeout: 5 * time.Second,
		sem:     make(chan struct{}, 2),
	}
	_, err = unauthProv.Probe(context.Background(), ForgeConfig{})
	if !errors.Is(err, ErrAuthFailed) {
		t.Fatalf("expected ErrAuthFailed on unauthenticated CLI stderr, got %v", err)
	}

	// 3) Max concurrency cap (<= 2 concurrent subprocesses across 6 watched repos).
	lockDir := t.TempDir()
	maxFile := filepath.Join(lockDir, "max.log")
	concCLI := writeFakeCLIScript(t, binDir, "conc-cli", fmt.Sprintf(`
f="%s/run.$$"
touch "$f"
cnt=$(ls "%s"/run.* 2>/dev/null | wc -l)
echo "$cnt" >> "%s"
sleep 0.08
rm -f "$f"
echo '{"items":[]}'
`, lockDir, lockDir, maxFile))

	concProv := &CLIProvider{
		spec: CLIProviderSpec{
			Kind:  "concforge",
			Name:  "ConcForge",
			Probe: CLIProbeSpec{Cmd: []string{concCLI}},
			Sources: []CLISourceSpec{
				{
					ID:        "repo_ev",
					Scope:     "repo",
					EventKind: "mr",
					Cmd:       []string{concCLI, "{{repo}}"},
				},
			},
		},
		timeout: 5 * time.Second,
		sem:     make(chan struct{}, 2),
	}
	_, err = concProv.Sample(context.Background(), ForgeConfig{
		Repos:  []string{"o/r1", "o/r2", "o/r3", "o/r4", "o/r5", "o/r6"},
		Events: map[string]bool{"mr": true},
	}, 3)
	if err != nil {
		t.Fatalf("concurrent Sample: %v", err)
	}
	rawCounts, err := os.ReadFile(maxFile)
	if err != nil {
		t.Fatalf("read concurrency log: %v", err)
	}
	for _, line := range strings.Fields(string(rawCounts)) {
		if line != "1" && line != "2" {
			t.Fatalf("concurrency exceeded max 2! observed active count %q (log: %q)", line, string(rawCounts))
		}
	}
}
