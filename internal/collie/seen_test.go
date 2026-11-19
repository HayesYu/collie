package collie

import (
	"testing"
	"time"
)

func TestSeenPanes(t *testing.T) {
	var sp seenPanes
	if got := sp.status("p1", "done"); got != "done" {
		t.Fatalf("unseen done: %s", got)
	}
	time.Sleep(time.Millisecond)
	sp.mark("p1")
	if got := sp.status("p1", "done"); got != "idle" {
		t.Fatalf("seen done: %s", got)
	}
	// It works again and finishes again: new news, not seen yet.
	sp.status("p1", "working")
	time.Sleep(time.Millisecond)
	if got := sp.status("p1", "done"); got != "done" {
		t.Fatalf("done again: %s", got)
	}
	// Open while it is still working: finishing after that is still news.
	sp.status("p2", "working")
	sp.mark("p2")
	time.Sleep(time.Millisecond)
	if got := sp.status("p2", "done"); got != "done" {
		t.Fatalf("seen before it finished: %s", got)
	}
}
