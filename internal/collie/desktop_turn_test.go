package collie

import (
	"strings"
	"testing"
)

func TestLastTurnOpen(t *testing.T) {
	pad := strings.Repeat(`{"type":"event_msg","payload":{"type":"token_count"}}`+"\n", 40000) // ~2 MB
	start := `{"type":"event_msg","payload":{"type":"task_started"}}` + "\n"
	done := `{"type":"event_msg","payload":{"type":"task_complete"}}` + "\n"
	for _, c := range []struct {
		name, log string
		want      bool
	}{
		{"start far back", start + pad, true},
		{"ended far back", start + done + pad, false},
		{"new turn after end", start + done + start + pad, true},
		{"nothing", pad, false},
	} {
		if got := lastTurnOpen(strings.NewReader(c.log), int64(len(c.log))); got != c.want {
			t.Errorf("%s: got %v", c.name, got)
		}
	}
}
