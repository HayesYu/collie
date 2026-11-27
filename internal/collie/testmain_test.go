package collie

import (
	"os"
	"testing"
)

func TestMain(m *testing.M) {
	// Keep unit tests hermetic and fast: never probe real host CLIs (`agy models`),
	// never spawn real host `herdr server` daemons, and skip artificial auth/pair
	// brute-force sleep delays. Dedicated tests (`defaults_test.go`,
	// `herdr_selfheal_test.go`) explicitly exercise those paths with stubs.
	detectInitialSettingsFn = func(_ *Server, home string) Settings {
		return defaultSettings(home)
	}
	startHerdrServerFn = func() bool { return false }
	authFailDelay = 0
	pairFailDelay = 0
	os.Exit(m.Run())
}
