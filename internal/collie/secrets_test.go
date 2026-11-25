package collie

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSecretStorePermissionsAndRedact(t *testing.T) {
	dir := t.TempDir()
	ss, err := openSecretStore(dir)
	if err != nil {
		t.Fatalf("openSecretStore: %v", err)
	}
	const secretVal = "sec_super_secret_value_987654321"
	const appIDVal = "cli_example_public_id"

	if err := ss.Update("feishu", map[string]string{
		"app_id":     appIDVal,
		"app_secret": secretVal,
	}); err != nil {
		t.Fatalf("Update: %v", err)
	}

	fi, err := os.Stat(filepath.Join(dir, "secrets.json"))
	if err != nil {
		t.Fatalf("stat secrets.json: %v", err)
	}
	if mode := fi.Mode().Perm(); mode != 0o600 {
		t.Fatalf("secrets.json mode = %o, want 0600", mode)
	}

	// Empty value update must preserve existing secret.
	if err := ss.Update("feishu", map[string]string{"app_secret": ""}); err != nil {
		t.Fatalf("empty update: %v", err)
	}
	if got := ss.GetKey("feishu", "app_secret"); got != secretVal {
		t.Fatalf("empty update clobbered secret: got %q", got)
	}

	// Reload from disk and verify tightening of looser perms.
	if err := os.Chmod(filepath.Join(dir, "secrets.json"), 0o644); err != nil {
		t.Fatal(err)
	}
	ss2, err := openSecretStore(dir)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	fi2, _ := os.Stat(filepath.Join(dir, "secrets.json"))
	if mode := fi2.Mode().Perm(); mode != 0o600 {
		t.Fatalf("reopened secrets.json mode = %o, want 0600", mode)
	}
	if got := ss2.GetKey("feishu", "app_secret"); got != secretVal {
		t.Fatalf("reloaded secret = %q, want %q", got, secretVal)
	}

	// Redact must scrub sensitive secrets while keeping non-secret app_id intact.
	rawLog := "connect failed for " + appIDVal + " with secret=" + secretVal + "!"
	scrubbed := ss2.Redact(rawLog)
	if strings.Contains(scrubbed, secretVal) {
		t.Fatalf("Redact leaked secret: %q", scrubbed)
	}
	if !strings.Contains(scrubbed, appIDVal) || !strings.Contains(scrubbed, "***") {
		t.Fatalf("unexpected Redact output: %q", scrubbed)
	}

	// Delete removes the kind cleanly.
	if err := ss2.Delete("feishu"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if ss2.HasKey("feishu", "app_secret") {
		t.Fatal("secret still present after Delete")
	}
}

func TestIntegrationsAPINeverLeaksSecrets(t *testing.T) {
	dir := t.TempDir()
	s := &Server{token: "tok-test", home: dir}
	sp, err := newSuper(s, dir)
	if err != nil {
		t.Fatalf("newSuper: %v", err)
	}
	t.Cleanup(func() { sp.db.Close() })
	s.sup = sp

	const secretVal = "sec_never_return_in_http_or_db_12345"
	mux := http.NewServeMux()
	s.superRoutes(mux)

	// Save config + secret with enabled=false so no network dial is attempted.
	payload := map[string]any{
		"enabled": false,
		"config": map[string]any{
			"region":             "cn",
			"app_id":             "cli_unit_test_123",
			"allow_open_id":      "ou_example_user_1",
			"group_mention_only": true,
			"instant_chat_sync":  true,
		},
		"secrets": map[string]string{
			"app_id":     "cli_unit_test_123",
			"app_secret": secretVal,
		},
	}
	b, _ := json.Marshal(payload)
	req := httptest.NewRequest(http.MethodPost, "/api/super/integrations/feishu", bytes.NewReader(b))
	req.Header.Set("Authorization", "Bearer tok-test")
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != 200 {
		t.Fatalf("POST save status=%d body=%s", w.Code, w.Body.String())
	}
	if strings.Contains(w.Body.String(), secretVal) {
		t.Fatalf("POST save response leaked secret: %s", w.Body.String())
	}

	// GET /api/super/integrations must report secret_set=true and zero plaintext secret.
	reqGet := httptest.NewRequest(http.MethodGet, "/api/super/integrations", nil)
	reqGet.Header.Set("Authorization", "Bearer tok-test")
	wGet := httptest.NewRecorder()
	mux.ServeHTTP(wGet, reqGet)
	if wGet.Code != 200 {
		t.Fatalf("GET integrations status=%d body=%s", wGet.Code, wGet.Body.String())
	}
	if strings.Contains(wGet.Body.String(), secretVal) {
		t.Fatalf("GET integrations leaked secret: %s", wGet.Body.String())
	}
	var got struct {
		Integrations []IntegrationView `json:"integrations"`
	}
	if err := json.Unmarshal(wGet.Body.Bytes(), &got); err != nil {
		t.Fatalf("unmarshal GET integrations: %v", err)
	}
	if len(got.Integrations) == 0 || !got.Integrations[0].SecretSet || !got.Integrations[0].Configured {
		t.Fatalf("unexpected integration view: %+v", got.Integrations)
	}

	// Verify SQLite herd.db (both kv and integrations) contains ZERO secretVal.
	sp.flush()
	var kvJSON, intJSON string
	_ = sp.db.QueryRow(`SELECT v FROM kv WHERE k='settings'`).Scan(&kvJSON)
	_ = sp.db.QueryRow(`SELECT config FROM integrations WHERE kind='feishu'`).Scan(&intJSON)
	if strings.Contains(kvJSON, secretVal) || strings.Contains(intJSON, secretVal) {
		t.Fatalf("SQLite leaked secret! kv=%q integrations=%q", kvJSON, intJSON)
	}

	// Verify secrets.json exists with 0600 permissions.
	fi, err := os.Stat(filepath.Join(dir, "secrets.json"))
	if err != nil {
		t.Fatalf("stat secrets.json: %v", err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Fatalf("secrets.json perm = %o, want 0600", fi.Mode().Perm())
	}

	// GET /api/super/settings must also never contain the secret.
	reqSet := httptest.NewRequest(http.MethodGet, "/api/super/settings", nil)
	reqSet.Header.Set("Authorization", "Bearer tok-test")
	wSet := httptest.NewRecorder()
	mux.ServeHTTP(wSet, reqSet)
	if strings.Contains(wSet.Body.String(), secretVal) {
		t.Fatalf("GET /api/super/settings leaked secret!")
	}
}
