package collie

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"testing"
	"testing/fstest"
	"time"
)

func TestTmuxCodexIdentity(t *testing.T) {
	resetTitlesForTest(t)
	root := t.TempDir()
	t.Setenv("CODEX_HOME", root)
	const idA = "11111111-1111-1111-1111-111111111111"
	const idB = "22222222-2222-2222-2222-222222222222"
	const missing = "33333333-3333-3333-3333-333333333333"
	now := time.Now()
	pathA := writeRollout(t, root, idA, "/work", now.Add(-time.Hour), now, "")
	pathB := writeRollout(t, root, idB, "/work", now, now.Add(time.Minute), "")
	idx := `{"id":"` + idA + `","thread_name":"First"}` + "\n" +
		`{"id":"` + idB + `","thread_name":"Duplicate"}` + "\n" +
		`{"id":"` + missing + `","thread_name":"Duplicate"}` + "\n"
	if err := os.WriteFile(filepath.Join(root, "session_index.jsonl"), []byte(idx), 0600); err != nil {
		t.Fatal(err)
	}
	s := &Server{}
	names := s.uniqueCodexThreadNames()
	p := &proc{pid: 20, ppid: 19}
	title := tmuxProcessTitle(map[int]*proc{19: {pid: 19, ppid: 1}}, p, map[int]string{19: "First | user"})
	for _, tc := range []struct {
		name, title, cwd string
		args             []string
		wantID, wantPath string
	}{
		{"named pane", title, "/work", []string{"codex"}, idA, pathA},
		{"resume overrides title", title, "/work", []string{"node", "codex.js", "resume", "--no-alt-screen", idB}, idB, pathB},
		{"removed rollout", title, "/work", []string{"codex", "resume", missing}, missing, ""},
		{"unmatched", "Untitled", "/work", []string{"codex"}, "", ""},
		{"duplicate", "Duplicate", "/work", []string{"codex"}, "", ""},
		{"wrong directory", title, "/other", []string{"codex"}, "", ""},
		{"UUID in prompt", "", "/work", []string{"codex", "explain", "resume", idA}, "", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := &External{Kind: "codex", Cwd: tc.cwd, Started: now}
			s.locateTmuxCodex(e, &proc{args: tc.args}, tc.title, names, true)
			if e.Session != tc.wantID || e.path != tc.wantPath {
				t.Fatalf("got session %q and path %q, want %q and %q", e.Session, e.path, tc.wantID, tc.wantPath)
			}
		})
	}
}

func TestExternalSnapshotDuringDiscovery(t *testing.T) {
	root := t.TempDir()
	path := writeRollout(t, root, "test", "/work", time.Now(), time.Now(),
		`{"type":"response_item","payload":{"type":"message","role":"assistant","content":[{"text":"hello"}]}}`+"\n")
	e := &External{ID: "codex-42", path: path, format: "codex"}
	prev := externalCache.snapshot.Swap(&externalSnapshot{at: time.Now(), list: []*External{e}})
	defer externalCache.snapshot.Store(prev)
	s := &Server{}
	h := s.NodeRoutes()
	res := httptest.NewRecorder()
	done := make(chan struct{})
	externalCache.Lock() // another viewer is refreshing the directory
	go func() {
		h.ServeHTTP(res, httptest.NewRequest("GET", "/api/external/codex-42/snapshot", nil))
		close(done)
	}()
	select {
	case <-done:
		externalCache.Unlock()
	case <-time.After(2 * time.Second):
		externalCache.Unlock()
		<-done
		t.Fatal("cached transcript waited for discovery")
	}
	var patch msgPatch
	if err := json.Unmarshal(res.Body.Bytes(), &patch); err != nil || res.Code != 200 || len(patch.Msgs) != 1 || patch.Msgs[0].Text != "hello" {
		t.Fatalf("unexpected snapshot (%d): %s", res.Code, res.Body.String())
	}
}

func TestHubForwardsExternalSnapshotToRemoteNode(t *testing.T) {
	root := t.TempDir()
	path := writeRollout(t, root, "remote-test", "/work", time.Now(), time.Now(),
		`{"type":"response_item","payload":{"type":"message","role":"assistant","content":[{"text":"from remote node"}]}}`+"\n")
	e := &External{ID: "codex-42", path: path, format: "codex"}
	prev := externalCache.snapshot.Swap(&externalSnapshot{at: time.Now(), list: []*External{e}})
	defer externalCache.snapshot.Store(prev)

	nodeSrv := &Server{home: root, node: true, web: fstest.MapFS{}}
	var gotPath, gotAuth string
	nodeHTTP := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotAuth = r.Header.Get("Authorization")
		nodeSrv.NodeRoutes().ServeHTTP(w, r)
	}))
	defer nodeHTTP.Close()
	nodeURL, _ := url.Parse(nodeHTTP.URL)

	herdr := newHerdr(filepath.Join(root, "missing.sock"), "hub", root)
	herdr.attach(&Machine{
		ID:     "mac",
		Name:   "mac",
		Home:   root,
		Online: true,
		client: &http.Client{
			Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
				cp := req.Clone(req.Context())
				cp.URL.Scheme = nodeURL.Scheme
				cp.URL.Host = nodeURL.Host
				cp.RequestURI = ""
				return http.DefaultTransport.RoundTrip(cp)
			}),
		},
	})
	herdr.attach(&Machine{ID: "offline-mac", Name: "offline-mac", Online: false})

	hubSrv := &Server{
		herdr: herdr,
		token: "secret-tok",
		home:  root,
		web:   fstest.MapFS{"index.html": {Data: []byte("ok")}, "app.js": {Data: []byte("")}, "app.css": {Data: []byte("")}},
	}
	handler := hubSrv.Routes()

	// Encoded "@mac" as sent by web/app.js encodeURIComponent(id).
	req := httptest.NewRequest(http.MethodGet, "/api/external/codex-42%40mac/snapshot", nil)
	req.Header.Set("Authorization", "Bearer secret-tok")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != 200 {
		t.Fatalf("remote snapshot status = %d: %s", rec.Code, rec.Body.String())
	}
	if gotPath != "/api/external/codex-42/snapshot" {
		t.Fatalf("forwarded path = %q, want /api/external/codex-42/snapshot", gotPath)
	}
	if gotAuth != "" {
		t.Fatalf("Authorization header leaked to node: %q", gotAuth)
	}
	var patch msgPatch
	if err := json.Unmarshal(rec.Body.Bytes(), &patch); err != nil || len(patch.Msgs) != 1 || patch.Msgs[0].Text != "from remote node" {
		t.Fatalf("unexpected proxied snapshot (%d): %s", rec.Code, rec.Body.String())
	}

	// Offline remote machine returns 503.
	offReq := httptest.NewRequest(http.MethodGet, "/api/external/codex-42%40offline-mac/snapshot", nil)
	offReq.Header.Set("Authorization", "Bearer secret-tok")
	offRec := httptest.NewRecorder()
	handler.ServeHTTP(offRec, offReq)
	if offRec.Code != 503 {
		t.Fatalf("offline remote snapshot status = %d, want 503: %s", offRec.Code, offRec.Body.String())
	}
}
