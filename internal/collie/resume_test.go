package collie

import (
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
