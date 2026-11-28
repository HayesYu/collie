package collie

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

func TestMobileCollieAvatarInlineLayoutContract(t *testing.T) {
	t.Parallel()

	root := filepath.Join("..", "..")
	for _, rel := range []string{"web/app.css", "docs/demo/app.css"} {
		raw, err := os.ReadFile(filepath.Join(root, rel))
		if err != nil {
			t.Fatalf("read %s: %v", rel, err)
		}
		css := string(raw)

		// 1. Base desktop/tablet layout keeps 26px avatar + 12px gap via single-source custom properties,
		//    shared by .say, .avatar, .event, .event:has(> .wstate), and .working-row.say.
		for _, want := range []string{
			".say { display: grid; grid-template-columns: var(--say-av, 26px) minmax(0, 1fr); gap: var(--say-gap, 12px); align-items: start; }",
			".avatar { width: var(--say-av, 26px); height: var(--say-av, 26px); border-radius: 8px; border: 1px solid var(--line); display: grid; place-items: center; background: var(--bg); margin-top: 2px; }",
			".avatar img { display: block; width: 20px; height: 20px; }",
			"grid-template-columns: var(--say-av, 26px) auto minmax(0, 1fr); gap: var(--say-gap, 12px);",
			".working-row.say { display: grid; gap: var(--say-gap, 12px); align-items: center; }",
			".event:has(> .wstate) { grid-template-columns: var(--say-av, 26px) auto minmax(0, 1fr) auto; }",
		} {
			if !strings.Contains(css, want) {
				t.Fatalf("%s missing shared avatar-spine rule %q", rel, want)
			}
		}

		// 2. Mobile <=760px compacts the rail cleanly to 22px + 8px on #v-collie so the avatar stays inline
		//    beside line 1 of .msg.assistant and aligns with .say.cont, .event, and .working-row.say.
		mobileBlockIdx := strings.Index(css, "#v-collie {\n    --say-av: 22px;\n    --say-gap: 8px;\n  }")
		if mobileBlockIdx < 0 {
			t.Fatalf("%s missing mobile #v-collie compact avatar tokens (--say-av: 22px; --say-gap: 8px)", rel)
		}
		mediaBefore := css[:mobileBlockIdx]
		if !strings.Contains(mediaBefore[strings.LastIndex(mediaBefore, "@media"):], "@media (max-width: 760px)") {
			t.Fatalf("%s #v-collie compact avatar tokens must be scoped inside @media (max-width: 760px)", rel)
		}
		for _, want := range []string{
			"#v-collie .say > .avatar {\n    border-radius: 7px;\n    margin-top: 2px;\n  }",
			"#v-collie .say > .avatar img { width: 16px; height: 16px; }",
			"#v-collie .working-row.say > .avatar { margin-top: 0; }",
		} {
			if !strings.Contains(css, want) {
				t.Fatalf("%s missing mobile inline avatar rule %q", rel, want)
			}
		}

		// 3. Regression guards: never collapse #super-thread > .say into a 1-column stack (which strands
		//    the avatar on its own blank line above prose) and never hide .say.cont > .avatar in a 2-col grid
		//    (which would pull .msg.assistant into the narrow 22px avatar column).
		singleColRe := regexp.MustCompile(`#super-thread\s*>\s*\.say\s*\{[^}]*grid-template-columns:\s*minmax\(0,\s*1fr\)`)
		if singleColRe.MatchString(css) {
			t.Fatalf("%s must NOT collapse #super-thread > .say into a single-column vertical stack", rel)
		}
		hideContAvatarRe := regexp.MustCompile(`#super-thread\s*>\s*\.say\.cont\s*>\s*\.avatar\s*\{[^}]*display:\s*none`)
		if hideContAvatarRe.MatchString(css) {
			t.Fatalf("%s must NOT set display:none on #super-thread > .say.cont > .avatar in a 2-column grid", rel)
		}
	}
}
