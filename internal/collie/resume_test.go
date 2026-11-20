package collie

import (
	"os"
	"path/filepath"
	"slices"
	"testing"
)

func TestResumeFrom(t *testing.T) {
	sp := &Super{st: &State{Workers: map[string]*Worker{
		"done":  {Name: "done", Kind: "codex", Cwd: "/w", Machine: "m1", Ended: true, Session: "s-1"},
		"live":  {Name: "live", Session: "s-2"},
		"blank": {Name: "blank", Ended: true},
	}}}
	w, err := sp.resumeFrom("done")
	if err != nil || w.Session != "s-1" || w.Machine != "m1" || w.Cwd != "/w" {
		t.Fatalf("done: %+v %v", w, err)
	}
	for _, n := range []string{"live", "blank", "nope"} {
		if _, err := sp.resumeFrom(n); err == nil {
			t.Errorf("%s: want an error", n)
		}
	}
	if got := resumeArgs(kinds["antigravity"], "c"); !slices.Equal(got, []string{"--conversation", "c"}) {
		t.Errorf("agy: %v", got)
	}
	if got := resumeArgs("claude", "c"); !slices.Equal(got, []string{"--resume", "c"}) {
		t.Errorf("claude: %v", got)
	}
}

func TestRouterDocChanged(t *testing.T) {
	dir := t.TempDir()
	sp := &Super{st: &State{Workers: map[string]*Worker{}, Threads: map[string]*Thread{}}, subs: map[chan struct{}]bool{}}
	asks := func() int {
		n := 0
		for _, tr := range sp.st.Triggers {
			if tr.Source == "system" && tr.Status == "queued" {
				n++
			}
		}
		return n
	}
	sp.routerDocChanged(dir, []byte("v1")) // no session has read one yet
	if asks() != 0 {
		t.Fatal("asked before any session read it")
	}
	os.WriteFile(filepath.Join(dir, routerReadFile), []byte("v1"), 0o600)
	sp.routerDocChanged(dir, []byte("v1"))
	if asks() != 0 {
		t.Fatal("asked though nothing changed")
	}
	sp.routerDocChanged(dir, []byte("v2"))
	sp.routerDocChanged(dir, []byte("v3")) // still waiting on the first ask
	if asks() != 1 {
		t.Fatalf("want one ask, got %d", asks())
	}
}

func TestAgentStopLine(t *testing.T) {
	cases := []struct{ screen, summary, want string }{
		{"⏺ 正在读文件\n\n  ⎿  You've hit your usage limit · resets 5am\n\n> ", "正在读文件", "You've hit your usage limit · resets 5am"},
		{"■ stream error: unexpected status 429 Too Many Requests\n", "", "stream error: unexpected status 429 Too Many Requests"},
		{"⏺ 加了 rate limit 中间件，测试通过\n> ", "加了 rate limit 中间件，测试通过", ""}, // the work talking
		{"  ⎿  Error: ENOENT no such file\n⏺ 改好了\n> ", "改好了", ""},      // a tool's error, not the agent's
	}
	for _, c := range cases {
		if got := agentStopLine(c.screen, c.summary); got != c.want {
			t.Errorf("%q: got %q, want %q", c.screen, got, c.want)
		}
	}
}

func TestDreamSkills(t *testing.T) {
	p := filepath.Join(t.TempDir(), "d.md")
	os.WriteFile(p, []byte("归档了两个任务。\n可做成 skill：Dorado 只读查表、Spark 草稿读 HDFS。\n\n# 记录\n"), 0o600)
	if got := dreamSkills(p); got != "Dorado 只读查表、Spark 草稿读 HDFS" {
		t.Errorf("skills: %q", got)
	}
	if got := dreamSummary(p); got != "归档了两个任务。" {
		t.Errorf("summary: %q", got)
	}
	os.WriteFile(p, []byte("归档了两个任务。\n"), 0o600)
	if dreamSkills(p) != "" {
		t.Error("no line, no skills")
	}
}
