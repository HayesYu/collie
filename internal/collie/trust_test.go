package collie

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestTrustAgyFolder(t *testing.T) {
	home := t.TempDir()
	real := filepath.Join(home, "real")
	os.Mkdir(real, 0o755)
	link := filepath.Join(home, "link")
	os.Symlink(real, link)
	file := agySettings(home)
	os.MkdirAll(filepath.Dir(file), 0o755)
	os.WriteFile(file, []byte(`{"model":"x","trustedWorkspaces":["/old"]}`), 0o600)

	for i := 0; i < 2; i++ { // the second call changes nothing
		if err := trustAgyFolder(home, link); err != nil {
			t.Fatal(err)
		}
	}
	var got struct {
		Model   string   `json:"model"`
		Trusted []string `json:"trustedWorkspaces"`
	}
	b, _ := os.ReadFile(file)
	json.Unmarshal(b, &got)
	realResolved, _ := filepath.EvalSymlinks(real)
	if got.Model != "x" || len(got.Trusted) != 3 || got.Trusted[0] != "/old" || got.Trusted[1] != link || got.Trusted[2] != realResolved {
		t.Fatalf("settings = %s", b)
	}
	if err := trustAgyFolder(t.TempDir(), "/tmp"); err != nil { // no settings file yet
		t.Fatal(err)
	}
}
