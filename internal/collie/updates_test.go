package collie

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
)

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

func TestSelfUpdateConcurrencyMutexAndTempCleanup(t *testing.T) {
	dir := t.TempDir()
	exe := filepath.Join(dir, "collie")
	oldScript := "#!/bin/sh\necho collie v0.2.41\n"
	if err := os.WriteFile(exe, []byte(oldScript), 0o755); err != nil {
		t.Fatal(err)
	}

	newTag := "v0.2.42"
	newScript := "#!/bin/sh\necho collie " + newTag + "\n"
	sum := sha256.Sum256([]byte(newScript))
	goodSum := hex.EncodeToString(sum[:])
	asset := "collie-" + runtime.GOOS + "-" + runtime.GOARCH

	var serveBadChecksum atomic.Bool
	inDownload := make(chan struct{})
	var signalOnce sync.Once
	releaseDownload := make(chan struct{})

	var ts *httptest.Server
	ts = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/releases/latest":
			w.Header().Set("Location", ts.URL+"/releases/tag/"+newTag)
			w.WriteHeader(http.StatusFound)
		case "/releases/download/" + newTag + "/checksums.txt":
			cs := goodSum
			if serveBadChecksum.Load() {
				cs = strings.Repeat("0", 64)
			}
			fmt.Fprintf(w, "%s  %s\n", cs, asset)
		case "/releases/download/" + newTag + "/" + asset:
			signalOnce.Do(func() { close(inDownload) })
			<-releaseDownload
			w.Write([]byte(newScript))
		default:
			http.NotFound(w, r)
		}
	}))
	defer ts.Close()

	origVer, origURL, origExe, origExec := version, releaseRepoURL, executablePath, execUpdated
	restarted := make(chan string, 1)
	version = "v0.2.41"
	releaseRepoURL = ts.URL
	executablePath = func() (string, error) { return exe, nil }
	execUpdated = func(p string) {
		restarted <- p
	}
	t.Cleanup(func() {
		version, releaseRepoURL, executablePath, execUpdated = origVer, origURL, origExe, origExec
		// Ensure the package-level lock is clean after test completion.
		if !selfUpdateMu.TryLock() {
			selfUpdateMu.Unlock()
		} else {
			selfUpdateMu.Unlock()
		}
	})

	srv := &Server{}

	// 1) Failed update (bad checksum) cleans up .collie.new.* and unlocks selfUpdateMu.
	serveBadChecksum.Store(true)
	close(releaseDownload)
	recFail := httptest.NewRecorder()
	srv.selfUpdate(recFail, httptest.NewRequest("POST", "/api/update", nil))
	if recFail.Code != 502 {
		t.Fatalf("bad checksum status = %d, want 502: %s", recFail.Code, recFail.Body.String())
	}
	if leftovers, _ := filepath.Glob(filepath.Join(dir, ".collie.new.*")); len(leftovers) != 0 {
		t.Fatalf("temp files left after failed update: %v", leftovers)
	}
	gotOld, _ := os.ReadFile(exe)
	if string(gotOld) != oldScript {
		t.Fatalf("exe modified on failed update: %q", string(gotOld))
	}

	// 2) Concurrent calls: first blocks mid-download; parallel callers get 409 "升级已在进行中"
	// without racing or deleting the first caller's temp file.
	serveBadChecksum.Store(false)
	inDownload = make(chan struct{})
	signalOnce = sync.Once{}
	releaseDownload = make(chan struct{})

	var wg sync.WaitGroup
	recPrimary := httptest.NewRecorder()
	wg.Add(1)
	go func() {
		defer wg.Done()
		srv.selfUpdate(recPrimary, httptest.NewRequest("POST", "/api/update", nil))
	}()

	<-inDownload

	const contenders = 4
	contenderCodes := make([]int, contenders)
	contenderBodies := make([]string, contenders)
	var cwg sync.WaitGroup
	for i := range contenders {
		cwg.Add(1)
		go func(idx int) {
			defer cwg.Done()
			rec := httptest.NewRecorder()
			srv.selfUpdate(rec, httptest.NewRequest("POST", "/api/update", nil))
			contenderCodes[idx] = rec.Code
			contenderBodies[idx] = rec.Body.String()
		}(i)
	}
	cwg.Wait()

	for i := range contenders {
		if contenderCodes[i] != 409 || !strings.Contains(contenderBodies[i], "升级已在进行中") {
			t.Fatalf("contender %d: status=%d body=%s, want 409 with 升级已在进行中", i, contenderCodes[i], contenderBodies[i])
		}
	}

	// Release primary download; it must finish cleanly, atomically replace exe, and leave 0 temp files.
	close(releaseDownload)
	wg.Wait()

	if recPrimary.Code != 200 {
		t.Fatalf("primary update status = %d: %s", recPrimary.Code, recPrimary.Body.String())
	}
	var res map[string]any
	if err := json.Unmarshal(recPrimary.Body.Bytes(), &res); err != nil {
		t.Fatal(err)
	}
	if res["ok"] != true || res["from"] != "v0.2.41" || res["version"] != newTag {
		t.Fatalf("unexpected update response: %+v", res)
	}
	if gotExe := <-restarted; gotExe != exe {
		t.Fatalf("execUpdated path = %q, want %q", gotExe, exe)
	}
	// While restarting is held (before syscall.Exec replaces process), duplicate calls still get 409.
	recDuringRestart := httptest.NewRecorder()
	srv.selfUpdate(recDuringRestart, httptest.NewRequest("POST", "/api/update", nil))
	if recDuringRestart.Code != 409 {
		t.Fatalf("call during restart window = %d, want 409", recDuringRestart.Code)
	}
	selfUpdateMu.Unlock()

	if leftovers, _ := filepath.Glob(filepath.Join(dir, ".collie.new.*")); len(leftovers) != 0 {
		t.Fatalf("temp files left after successful update: %v", leftovers)
	}
	if _, err := os.Stat(exe + ".new"); !os.IsNotExist(err) {
		t.Fatalf("legacy fixed exe+.new should never be created: %v", err)
	}
	gotNew, err := os.ReadFile(exe)
	if err != nil || string(gotNew) != newScript {
		t.Fatalf("updated binary content = %q, err = %v", string(gotNew), err)
	}
	fi, err := os.Stat(exe)
	if err != nil || fi.Mode().Perm()&0o111 == 0 {
		t.Fatalf("updated binary mode = %v, want executable", fi.Mode())
	}
}

func TestGetVersionAndStateMachinesVersion(t *testing.T) {
	origVer := version
	// Prevent background refresh from hitting GitHub during tests.
	latestRel.once.Do(func() {})
	latestRel.Lock()
	origTag, origChecked, origBusy := latestRel.tag, latestRel.checked, latestRel.busy
	latestRel.tag = "v0.2.51"
	latestRel.busy = true
	latestRel.Unlock()
	t.Cleanup(func() {
		version = origVer
		latestRel.Lock()
		latestRel.tag, latestRel.checked, latestRel.busy = origTag, origChecked, origBusy
		latestRel.Unlock()
	})

	h := newHerdr("/nonexistent.sock", "hubbox", t.TempDir())
	h.attach(&Machine{ID: "mac", Name: "mac", Version: "v0.2.50", Online: true})
	h.attach(&Machine{ID: "old-off", Name: "old-off", Version: "v0.2.40", Online: false})
	srv := &Server{herdr: h}

	// 1) Hub already up to date, one online node outdated, one offline node outdated.
	version = "v0.2.51"
	ms := h.Machines()
	if len(ms) != 3 || !ms[0].Local || ms[0].Version != "v0.2.51" {
		t.Fatalf("Machines() local version = %+v, want v0.2.51", ms)
	}

	rec := httptest.NewRecorder()
	srv.getVersion(rec, httptest.NewRequest("GET", "/api/version", nil))
	if rec.Code != 200 {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
	}
	var got struct {
		Version  string           `json:"version"`
		Latest   string           `json:"latest"`
		Outdated []string         `json:"outdated"`
		Machines []versionMachine `json:"machines"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.Version != "v0.2.51" || got.Latest != "v0.2.51" {
		t.Fatalf("version/latest = %q/%q", got.Version, got.Latest)
	}
	if len(got.Outdated) != 1 || got.Outdated[0] != "mac" {
		t.Fatalf("outdated = %v, want [mac]", got.Outdated)
	}
	if len(got.Machines) != 3 {
		t.Fatalf("machines len = %d, want 3", len(got.Machines))
	}
	byID := map[string]versionMachine{}
	for _, m := range got.Machines {
		byID[m.ID] = m
	}
	if !byID[""].Local || !byID[""].Online || byID[""].Outdated || byID[""].Version != "v0.2.51" {
		t.Fatalf("hub entry = %+v", byID[""])
	}
	if byID["mac"].Local || !byID["mac"].Online || !byID["mac"].Outdated || byID["mac"].Version != "v0.2.50" {
		t.Fatalf("mac entry = %+v", byID["mac"])
	}
	if byID["old-off"].Online || byID["old-off"].Outdated {
		t.Fatalf("offline node should not be outdated: %+v", byID["old-off"])
	}

	// 2) Both hub and online node outdated.
	version = "v0.2.50"
	rec2 := httptest.NewRecorder()
	srv.getVersion(rec2, httptest.NewRequest("GET", "/api/version", nil))
	got.Outdated = nil
	if err := json.Unmarshal(rec2.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if len(got.Outdated) != 2 || got.Outdated[0] != "hubbox" || got.Outdated[1] != "mac" {
		t.Fatalf("outdated = %v, want [hubbox mac]", got.Outdated)
	}

	// 3) All online machines up to date: outdated must marshal as JSON null for backwards compatibility.
	version = "v0.2.51"
	h.attach(&Machine{ID: "mac", Name: "mac", Version: "v0.2.51", Online: true})
	rec3 := httptest.NewRecorder()
	srv.getVersion(rec3, httptest.NewRequest("GET", "/api/version", nil))
	if !strings.Contains(rec3.Body.String(), `"outdated":null`) {
		t.Fatalf("expected outdated:null when up to date, got %s", rec3.Body.String())
	}
}
