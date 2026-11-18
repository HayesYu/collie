package collie

import "testing"

func TestOutdated(t *testing.T) {
	for _, c := range []struct {
		have, latest string
		want         bool
	}{
		{"v0.2.4", "v0.2.5", true},
		{"v0.2.10", "v0.2.9", false},
		{"v0.3.0", "v0.2.9", false},
		{"v1.0.0", "v1.0.0", false},
		{"dev", "v0.2.5", false},
		{"", "v0.2.5", false},
		{"v0.2.4", "", false},
	} {
		if got := outdated(c.have, c.latest); got != c.want {
			t.Errorf("outdated(%q, %q) = %v", c.have, c.latest, got)
		}
	}
}
