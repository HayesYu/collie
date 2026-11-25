package collie

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
)

// SecretStore keeps integration credentials outside SQLite (herd.db) in a
// dedicated ~/.collie/secrets.json file with strict 0600 permissions.
// Values registered here are never returned in HTTP API responses and are
// redacted from logs and health diagnostics.
type SecretStore struct {
	mu   sync.RWMutex
	path string
	data map[string]map[string]string
}

func openSecretStore(dir string) (*SecretStore, error) {
	p := filepath.Join(dir, "secrets.json")
	ss := &SecretStore{
		path: p,
		data: map[string]map[string]string{},
	}
	b, err := os.ReadFile(p)
	if err != nil {
		if os.IsNotExist(err) {
			return ss, nil
		}
		return nil, err
	}
	if len(strings.TrimSpace(string(b))) > 0 {
		if err := json.Unmarshal(b, &ss.data); err != nil {
			return nil, fmt.Errorf("parse %s: %w", p, err)
		}
		if ss.data == nil {
			ss.data = map[string]map[string]string{}
		}
	}
	// Tighten file mode if an older/manual file had looser permissions.
	if fi, err := os.Stat(p); err == nil && fi.Mode().Perm() != 0o600 {
		_ = os.Chmod(p, 0o600)
	}
	return ss, nil
}

// Path returns the backing secrets.json path.
func (s *SecretStore) Path() string {
	if s == nil {
		return ""
	}
	return s.path
}

// Get returns a defensive copy of all stored values for one integration kind.
func (s *SecretStore) Get(kind string) map[string]string {
	out := map[string]string{}
	if s == nil {
		return out
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	for k, v := range s.data[kind] {
		out[k] = v
	}
	return out
}

// GetKey returns a single key for kind, or "".
func (s *SecretStore) GetKey(kind, key string) string {
	if s == nil {
		return ""
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.data[kind][key]
}

// HasKey reports whether a non-empty value exists for (kind, key).
func (s *SecretStore) HasKey(kind, key string) bool {
	return strings.TrimSpace(s.GetKey(kind, key)) != ""
}

// Update merges non-empty values into kind's secret map and writes atomically
// with mode 0600. Empty values in vals leave existing keys untouched.
func (s *SecretStore) Update(kind string, vals map[string]string) error {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.data[kind] == nil {
		s.data[kind] = map[string]string{}
	}
	for k, v := range vals {
		v = strings.TrimSpace(v)
		if v == "" {
			continue
		}
		s.data[kind][k] = v
	}
	if len(s.data[kind]) == 0 {
		delete(s.data, kind)
	}
	return s.saveLocked()
}

// Delete removes all stored credentials for kind and writes atomically.
func (s *SecretStore) Delete(kind string) error {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.data, kind)
	return s.saveLocked()
}

// DeleteKeys removes specific keys for kind and writes atomically.
func (s *SecretStore) DeleteKeys(kind string, keys ...string) error {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	m := s.data[kind]
	if m == nil {
		return nil
	}
	for _, k := range keys {
		delete(m, k)
	}
	if len(m) == 0 {
		delete(s.data, kind)
	}
	return s.saveLocked()
}

func (s *SecretStore) saveLocked() error {
	dir := filepath.Dir(s.path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	b, err := json.MarshalIndent(s.data, "", "  ")
	if err != nil {
		return err
	}
	b = append(b, '\n')
	tmp, err := os.CreateTemp(dir, ".secrets-*.tmp")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	cleanup := true
	defer func() {
		if cleanup {
			_ = os.Remove(tmpPath)
		}
	}()
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return err
	}
	if _, err := tmp.Write(b); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmpPath, 0o600); err != nil {
		return err
	}
	if err := os.Rename(tmpPath, s.path); err != nil {
		return err
	}
	cleanup = false
	return nil
}

// sensitiveSecretKey reports whether a key stored in secrets.json should be
// scrubbed from logs and diagnostics. Non-secret identifiers like app_id stay
// readable; all secrets/tokens/URLs are scrubbed.
func sensitiveSecretKey(k string) bool {
	switch strings.ToLower(strings.TrimSpace(k)) {
	case "app_id":
		return false
	default:
		return true
	}
}

// Redact replaces every registered sensitive credential value appearing in text
// with "***", matching longer values first to prevent partial leaks.
func (s *SecretStore) Redact(text string) string {
	if s == nil || text == "" {
		return text
	}
	s.mu.RLock()
	var vals []string
	seen := map[string]bool{}
	for _, m := range s.data {
		for k, v := range m {
			if !sensitiveSecretKey(k) {
				continue
			}
			v = strings.TrimSpace(v)
			if len(v) < 6 || seen[v] {
				continue
			}
			seen[v] = true
			vals = append(vals, v)
		}
	}
	s.mu.RUnlock()
	if len(vals) == 0 {
		return text
	}
	sort.Slice(vals, func(i, j int) bool { return len(vals[i]) > len(vals[j]) })
	out := text
	for _, v := range vals {
		out = strings.ReplaceAll(out, v, "***")
	}
	return out
}

// maskIdentifier shortens a non-secret external identifier (such as an open_id)
// for safe logging without exposing full personal IDs.
func maskIdentifier(id string) string {
	id = strings.TrimSpace(id)
	r := []rune(id)
	if len(r) <= 8 {
		return "***"
	}
	return string(r[:4]) + "…" + string(r[len(r)-3:])
}
