package collie

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestLanAddressesIPv4AndIPv6Priority(t *testing.T) {
	ifaces := []lanIface{
		{
			Name:  "lo",
			Flags: net.FlagUp | net.FlagLoopback,
			IPs:   []net.IP{net.ParseIP("127.0.0.1"), net.ParseIP("::1")},
		},
		{
			Name:  "docker0",
			Flags: net.FlagUp,
			IPs:   []net.IP{net.ParseIP("172.17.0.1"), net.ParseIP("2001:db8:dead::1")},
		},
		{
			Name:  "eth1",
			Flags: 0, // down
			IPs:   []net.IP{net.ParseIP("10.99.99.99"), net.ParseIP("2001:db8:down::1")},
		},
		{
			Name:  "eth0",
			Flags: net.FlagUp,
			IPs: []net.IP{
				net.ParseIP("fe80::1"),         // link-local IPv6 -> excluded
				net.ParseIP("ff02::1"),         // multicast IPv6 -> excluded
				net.ParseIP("169.254.10.20"),   // link-local IPv4 -> excluded
				net.ParseIP("::1"),             // loopback IPv6 -> excluded
				net.ParseIP("127.0.0.2"),       // loopback IPv4 -> excluded
				net.ParseIP("fd00::42"),        // ULA IPv6 -> rank 6
				net.ParseIP("2001:db8::1"),     // global IPv6 -> rank 5
				net.ParseIP("203.0.113.5"),     // public IPv4 -> rank 4
				net.ParseIP("100.64.1.2"),      // CGNAT IPv4 -> rank 3
				net.ParseIP("172.20.1.2"),      // 172.16/12 -> rank 2
				net.ParseIP("192.168.1.5"),     // 192.168/16 -> rank 1
				net.ParseIP("10.1.2.3"),        // 10/8 -> rank 0
				net.ParseIP("2001:db8::1"),     // duplicate -> deduped
				net.ParseIP("::ffff:10.1.2.3"), // IPv4-mapped duplicate -> deduped
			},
		},
	}

	got := lanAddressesFrom(7788, []string{"http://[2001:db8::99]:7788/"}, ifaces)
	want := []string{
		"http://[2001:db8::99]:7788",
		"http://10.1.2.3:7788",
		"http://192.168.1.5:7788",
		"http://172.20.1.2:7788",
		"http://100.64.1.2:7788",
		"http://203.0.113.5:7788",
		"http://[2001:db8::1]:7788",
		"http://[fd00::42]:7788",
		"http://127.0.0.1:7788",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("lanAddressesFrom mismatch\ngot:  %v\nwant: %v", got, want)
	}
}

func TestLanAddressesPureIPv6(t *testing.T) {
	t.Run("global_and_ula_with_ipv6_only_loopback", func(t *testing.T) {
		ifaces := []lanIface{
			{
				Name:  "lo",
				Flags: net.FlagUp | net.FlagLoopback,
				IPs:   []net.IP{net.ParseIP("::1")},
			},
			{
				Name:  "eth0",
				Flags: net.FlagUp,
				IPs: []net.IP{
					net.ParseIP("fe80::1234"),
					net.ParseIP("fd12:3456::10"),
					net.ParseIP("2001:db8::42"),
					net.ParseIP("ff05::2"),
				},
			},
		}
		got := lanAddressesFrom(7788, nil, ifaces)
		want := []string{
			"http://[2001:db8::42]:7788",
			"http://[fd12:3456::10]:7788",
			"http://[::1]:7788",
		}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("pure IPv6 mismatch\ngot:  %v\nwant: %v", got, want)
		}
		if u := pairURL(got[0], "ABCD1234"); u != "http://[2001:db8::42]:7788/?pair=ABCD1234" {
			t.Fatalf("pairURL = %q", u)
		}
	})

	t.Run("ula_only_without_loopback_iface", func(t *testing.T) {
		ifaces := []lanIface{
			{
				Name:  "eth0",
				Flags: net.FlagUp,
				IPs:   []net.IP{net.ParseIP("fe80::1"), net.ParseIP("fd00::7")},
			},
		}
		got := lanAddressesFrom(8899, nil, ifaces)
		want := []string{
			"http://[fd00::7]:8899",
			"http://[::1]:8899",
		}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("ULA-only mismatch\ngot:  %v\nwant: %v", got, want)
		}
	})

	t.Run("ipv6_eth0_with_dual_loopback", func(t *testing.T) {
		ifaces := []lanIface{
			{
				Name:  "lo",
				Flags: net.FlagUp | net.FlagLoopback,
				IPs:   []net.IP{net.ParseIP("127.0.0.1"), net.ParseIP("::1")},
			},
			{
				Name:  "eth0",
				Flags: net.FlagUp,
				IPs:   []net.IP{net.ParseIP("fe80::1"), net.ParseIP("2001:db8::42")},
			},
		}
		got := lanAddressesFrom(7788, nil, ifaces)
		want := []string{
			"http://[2001:db8::42]:7788",
			"http://127.0.0.1:7788",
		}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("IPv6 eth0 + dual lo mismatch\ngot:  %v\nwant: %v", got, want)
		}
	})

	t.Run("link_local_only_falls_back_to_ipv6_loopback", func(t *testing.T) {
		ifaces := []lanIface{
			{
				Name:  "lo",
				Flags: net.FlagUp | net.FlagLoopback,
				IPs:   []net.IP{net.ParseIP("::1")},
			},
			{
				Name:  "eth0",
				Flags: net.FlagUp,
				IPs:   []net.IP{net.ParseIP("fe80::1")},
			},
		}
		got := lanAddressesFrom(7788, nil, ifaces)
		want := []string{"http://[::1]:7788"}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("link-local-only mismatch\ngot:  %v\nwant: %v", got, want)
		}
	})
}

func TestLocalDialAddrAndBanner(t *testing.T) {
	yesV4 := func() bool { return true }
	noV4 := func() bool { return false }

	cases := []struct {
		name string
		addr *net.TCPAddr
		v4   func() bool
		want string
	}{
		{"ipv6_loopback", &net.TCPAddr{IP: net.ParseIP("::1"), Port: 7788}, yesV4, "[::1]:7788"},
		{"ipv6_unicast", &net.TCPAddr{IP: net.ParseIP("2001:db8::1"), Port: 7788}, yesV4, "[2001:db8::1]:7788"},
		{"ipv4_loopback", &net.TCPAddr{IP: net.ParseIP("127.0.0.1"), Port: 7788}, noV4, "127.0.0.1:7788"},
		{"ipv4_unicast", &net.TCPAddr{IP: net.ParseIP("10.0.0.5"), Port: 7788}, yesV4, "10.0.0.5:7788"},
		{"ipv4_any", &net.TCPAddr{IP: net.IPv4zero, Port: 7788}, noV4, "127.0.0.1:7788"},
		{"dual_stack_with_v4_lo", &net.TCPAddr{IP: net.IPv6unspecified, Port: 7788}, yesV4, "127.0.0.1:7788"},
		{"dual_stack_without_v4_lo", &net.TCPAddr{IP: net.IPv6unspecified, Port: 7788}, noV4, "[::1]:7788"},
		{"nil_ip_without_v4_lo", &net.TCPAddr{Port: 7788}, noV4, "[::1]:7788"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := localDialAddr(tc.addr, tc.v4); got != tc.want {
				t.Fatalf("localDialAddr(%v) = %q, want %q", tc.addr, got, tc.want)
			}
		})
	}

	s := &Server{port: 7788}
	if got := bannerAddresses(s, &net.TCPAddr{IP: net.ParseIP("::1"), Port: 7788}); !reflect.DeepEqual(got, []string{"http://[::1]:7788"}) {
		t.Fatalf("bannerAddresses([::1]) = %v", got)
	}
	if got := bannerAddresses(s, &net.TCPAddr{IP: net.ParseIP("2001:db8::1"), Port: 7788}); !reflect.DeepEqual(got, []string{"http://[2001:db8::1]:7788"}) {
		t.Fatalf("bannerAddresses(2001:db8::1) = %v", got)
	}
}

func TestIPv6HelpersAndHandlers(t *testing.T) {
	for _, h := range []string{"localhost", "localhost:7788", "127.0.0.1", "127.0.0.1:7788", "[::1]", "[::1]:7788", "::1", "[::ffff:127.0.0.1]:7788"} {
		if !isLoopbackHost(h) {
			t.Fatalf("expected %q to be loopback", h)
		}
	}
	for _, h := range []string{"[2001:db8::1]:7788", "2001:db8::1", "10.0.0.5:7788", "example.com:7788", ""} {
		if isLoopbackHost(h) {
			t.Fatalf("expected %q not to be loopback", h)
		}
	}

	for _, u := range []string{"http://127.0.0.1:17788", "http://localhost:17788", "http://[::1]:17788"} {
		if !isLocalUIURL(u) {
			t.Fatalf("expected %q to be valid local UI URL", u)
		}
	}
	for _, u := range []string{"http://10.0.0.5:17788", "http://[::1]:17788/evil", "http://127.0.0.1@evil.example:80", "https://[::1]:17788", "http://[::1]"} {
		if isLocalUIURL(u) {
			t.Fatalf("expected %q to be rejected as local UI URL", u)
		}
	}

	dialCases := []struct {
		raw  string
		want string
	}{
		{"http://[2001:db8::1]:7788/", "[2001:db8::1]:7788"},
		{"http://[2001:db8::1]", "[2001:db8::1]:80"},
		{"https://[2001:db8::1]", "[2001:db8::1]:443"},
		{"[::1]:7788", "[::1]:7788"},
		{"2001:db8::1", "[2001:db8::1]:80"},
		{"10.0.0.5:7788", "10.0.0.5:7788"},
	}
	for _, tc := range dialCases {
		_, got, err := hubDialAddr(tc.raw)
		if err != nil || got != tc.want {
			t.Fatalf("hubDialAddr(%q) = (%q, %v), want %q", tc.raw, got, err, tc.want)
		}
	}

	// s.addresses(r) should skip [::1] host and keep global IPv6 host.
	srv := &Server{port: 7788, publicURLs: []string{"http://[2001:db8::1]:7788"}}
	rLoop := httptest.NewRequest("GET", "http://[::1]:7788/api/devices", nil)
	addrsLoop := srv.addresses(rLoop)
	if len(addrsLoop) == 0 || addrsLoop[0] != "http://[2001:db8::1]:7788" {
		t.Fatalf("addresses with [::1] Host polluted front: %v", addrsLoop)
	}
	rV6 := httptest.NewRequest("GET", "http://[2001:db8::9]:7788/api/devices", nil)
	addrsV6 := srv.addresses(rV6)
	if len(addrsV6) == 0 || addrsV6[0] != "http://[2001:db8::9]:7788" {
		t.Fatalf("addresses with IPv6 Host did not preserve current URL: %v", addrsV6)
	}

	// setHubs accepts IPv6 URL and defaults name to hostname without brackets.
	t.Setenv("COLLIE_HOME", t.TempDir())
	body := `{"hubs":[{"key":"v6","url":"http://[2001:db8::1]:7788/","token":"secret"}]}`
	rw := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/api/hubs", strings.NewReader(body))
	srv.setHubs(rw, req)
	if rw.Code != 200 {
		t.Fatalf("setHubs IPv6 status = %d body=%s", rw.Code, rw.Body.String())
	}
	var hubsResp struct {
		Hubs []remoteHub `json:"hubs"`
	}
	if err := json.Unmarshal(rw.Body.Bytes(), &hubsResp); err != nil || len(hubsResp.Hubs) != 1 || hubsResp.Hubs[0].Name != "2001:db8::1" || hubsResp.Hubs[0].URL != "http://[2001:db8::1]:7788" {
		t.Fatalf("unexpected setHubs response: %s", rw.Body.String())
	}

	// setHubs rejects unbracketed IPv6 URL.
	rwBad := httptest.NewRecorder()
	srv.setHubs(rwBad, httptest.NewRequest("POST", "/api/hubs", strings.NewReader(`{"hubs":[{"key":"bad","url":"http://2001:db8::1:7788","token":"secret"}]}`)))
	if rwBad.Code != 400 {
		t.Fatalf("setHubs unbracketed IPv6 expected 400, got %d", rwBad.Code)
	}

	// localApp matches IPv6 and IPv4-mapped IPv6 node RemoteAddr and ignores ::1.
	h := newHerdr(filepath.Join(t.TempDir(), "no.sock"), "hub", t.TempDir())
	h.attach(&Machine{ID: "m6", Name: "v6node", Online: true, ip: "2001:db8::2", ui: "http://[::1]:17788"})
	h.attach(&Machine{ID: "m4", Name: "v4node", Online: true, ip: "192.168.1.9", ui: "http://127.0.0.1:17788"})
	srvApp := &Server{herdr: h, port: 7788, localAddr: "[::1]:7788"}

	// Loopback [::1] returns empty object.
	w1 := httptest.NewRecorder()
	r1 := httptest.NewRequest("GET", "/api/local-app", nil)
	r1.RemoteAddr = "[::1]:54321"
	srvApp.localApp(w1, r1)
	if strings.TrimSpace(w1.Body.String()) != "{}" {
		t.Fatalf("localApp for [::1] = %s, want {}", w1.Body.String())
	}

	// IPv6 remote node match.
	w2 := httptest.NewRecorder()
	r2 := httptest.NewRequest("GET", "/api/local-app", nil)
	r2.RemoteAddr = "[2001:0db8::2]:54321"
	srvApp.localApp(w2, r2)
	var appResp struct {
		URL     string `json:"url"`
		Machine string `json:"machine"`
	}
	if err := json.Unmarshal(w2.Body.Bytes(), &appResp); err != nil || appResp.URL != "http://[::1]:17788" || appResp.Machine != "v6node" {
		t.Fatalf("localApp for IPv6 node = %s", w2.Body.String())
	}

	// IPv4-mapped IPv6 RemoteAddr matches IPv4 node.
	w3 := httptest.NewRecorder()
	r3 := httptest.NewRequest("GET", "/api/local-app", nil)
	r3.RemoteAddr = "[::ffff:192.168.1.9]:54321"
	srvApp.localApp(w3, r3)
	appResp = struct {
		URL     string `json:"url"`
		Machine string `json:"machine"`
	}{}
	if err := json.Unmarshal(w3.Body.Bytes(), &appResp); err != nil || appResp.URL != "http://127.0.0.1:17788" || appResp.Machine != "v4node" {
		t.Fatalf("localApp for IPv4-mapped IPv6 = %s", w3.Body.String())
	}
}

func TestIPv6LoopbackListenCLIAndLocalUI(t *testing.T) {
	ln, err := net.Listen("tcp", "[::1]:0")
	if err != nil {
		t.Skipf("IPv6 loopback unavailable: %v", err)
	}
	defer ln.Close()

	dir := t.TempDir()
	t.Setenv("COLLIE_HOME", dir)
	tok := "test-ipv6-token"

	tcp := ln.Addr().(*net.TCPAddr)
	dialAddr := localDialAddr(tcp, hasIPv4Loopback)
	if !strings.HasPrefix(dialAddr, "[::1]:") {
		t.Fatalf("localDialAddr([::1]:0) = %q, want [::1]:<port>", dialAddr)
	}
	if err := os.WriteFile(filepath.Join(dir, "token"), []byte(tok+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "addr"), []byte(dialAddr+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	srv := &Server{
		herdr:     newHerdr(filepath.Join(dir, "isolated.sock"), "v6hub", dir),
		token:     tok,
		home:      dir,
		port:      tcp.Port,
		localAddr: dialAddr,
		auths:     newAuth(nil, tok),
	}
	hs := &http.Server{Handler: srv.Routes(), ReadHeaderTimeout: 5 * time.Second}
	go hs.Serve(ln)
	defer hs.Close()

	base, gotTok, err := cliTarget()
	if err != nil {
		t.Fatalf("cliTarget: %v", err)
	}
	if base != "http://"+dialAddr || gotTok != tok {
		t.Fatalf("cliTarget = (%q, %q), want (%q, %q)", base, gotTok, "http://"+dialAddr, tok)
	}

	// CLI call to /api/version over IPv6 loopback.
	out, err := cliCall("GET", "/api/version", nil)
	if err != nil {
		t.Fatalf("cliCall /api/version over [::1]: %v", err)
	}
	var vr struct {
		Version string `json:"version"`
	}
	if err := json.Unmarshal(out, &vr); err != nil || vr.Version == "" {
		t.Fatalf("unexpected /api/version output: %s", string(out))
	}

	// startLocalUI bound to [::1]:0 forwarding to IPv6 hub.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	uiURL := startLocalUI(ctx, "[::1]:0", base)
	if !strings.HasPrefix(uiURL, "http://[::1]:") {
		t.Fatalf("startLocalUI([::1]:0) = %q", uiURL)
	}
	req, err := http.NewRequest("GET", uiURL+"/api/version", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+tok)
	resp, err := (&http.Client{Timeout: 5 * time.Second, Transport: &http.Transport{Proxy: nil}}).Do(req)
	if err != nil {
		t.Fatalf("GET via IPv6 local UI: %v", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 || !bytes.Contains(body, []byte(`"version"`)) {
		t.Fatalf("unexpected IPv6 local UI response: status=%d body=%s", resp.StatusCode, string(body))
	}
}

func TestInstallScriptIPv6(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX shell test")
	}
	scriptPath, err := filepath.Abs(filepath.Join("..", "..", "docs", "install.sh"))
	if err != nil {
		t.Fatal(err)
	}
	// 1) Strict POSIX /bin/sh syntax check.
	if out, err := exec.Command("sh", "-n", scriptPath).CombinedOutput(); err != nil {
		t.Fatalf("sh -n docs/install.sh failed: %v\n%s", err, string(out))
	}

	runInstall := func(t *testing.T, mockBinDir string, args ...string) (string, string) {
		t.Helper()
		tmp := t.TempDir()
		homeDir := filepath.Join(tmp, "home")
		binDir := filepath.Join(tmp, "bin")
		stubBin := filepath.Join(tmp, "collie-stub")
		pairLog := filepath.Join(tmp, "pair.log")
		if err := os.MkdirAll(homeDir, 0o755); err != nil {
			t.Fatal(err)
		}
		stub := "#!/bin/sh\nif [ \"${1:-}\" = \"version\" ]; then echo 'collie v0.2.68-test'; exit 0; fi\nprintf '%s\\n' \"$*\" >> \"" + pairLog + "\"\n"
		if err := os.WriteFile(stubBin, []byte(stub), 0o755); err != nil {
			t.Fatal(err)
		}
		cmd := exec.Command("sh", append([]string{scriptPath}, args...)...)
		pathVal := os.Getenv("PATH")
		if mockBinDir != "" {
			pathVal = mockBinDir + ":" + pathVal
		}
		// Keep a clean environment without SSH_* so loopback vs LAN output is exercised.
		cmd.Env = []string{
			"PATH=" + pathVal,
			"HOME=" + homeDir,
			"COLLIE_BIN_DIR=" + binDir,
			"COLLIE_BINARY=" + stubBin,
			"COLLIE_DRY_SERVICE=1",
			"COLLIE_NO_HERDR=1",
			"COLLIE_NO_OPEN=1",
		}
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("install.sh %v failed: %v\n%s", args, err, string(out))
		}
		logged, _ := os.ReadFile(pairLog)
		return string(out), string(logged)
	}

	t.Run("explicit_ipv6_url_and_loopback", func(t *testing.T) {
		out, _ := runInstall(t, "", "hub", "--url", "http://[2001:db8::1]:7788")
		if !strings.Contains(out, "打开 hub：http://[2001:db8::1]:7788") {
			t.Fatalf("missing bracketed --url output:\n%s", out)
		}
		if !strings.Contains(out, "本机访问：http://127.0.0.1:7788") {
			t.Fatalf("expected local_url hint for non-loopback --url:\n%s", out)
		}

		outLoop, _ := runInstall(t, "", "hub", "--url", "http://[::1]:7788")
		if !strings.Contains(outLoop, "打开 hub：http://[::1]:7788") {
			t.Fatalf("missing [::1] --url output:\n%s", outLoop)
		}
		if strings.Contains(outLoop, "本机访问：") {
			t.Fatalf("should not print separate local_url for [::1] loopback:\n%s", outLoop)
		}

		outAddrLoop, _ := runInstall(t, "", "hub", "--addr", "[::1]:8899")
		if !strings.Contains(outAddrLoop, "打开 hub：http://[::1]:8899") {
			t.Fatalf("missing [::1]:8899 output:\n%s", outAddrLoop)
		}
		if strings.Contains(outAddrLoop, "本机访问：") {
			t.Fatalf("should not print separate local_url for --addr [::1]:8899:\n%s", outAddrLoop)
		}

		outAddrV6, _ := runInstall(t, "", "hub", "--addr=[2001:db8::9]:9000")
		if !strings.Contains(outAddrV6, "打开 hub：http://[2001:db8::9]:9000") {
			t.Fatalf("missing [2001:db8::9]:9000 output:\n%s", outAddrV6)
		}
	})

	t.Run("node_with_ipv6_hub", func(t *testing.T) {
		out, logged := runInstall(t, "", "node", "--hub", "http://[2001:db8::1]:7788", "--code", "TESTCODE", "--name", "box6")
		if !strings.Contains(logged, "node --pair-only --hub http://[2001:db8::1]:7788 --code TESTCODE --name box6") {
			t.Fatalf("pair-only args mismatch: %q", logged)
		}
		if strings.Contains(out, "TESTCODE") || strings.Contains(out, "--hub") {
			t.Fatalf("service unit should strip --hub and --code:\n%s", out)
		}
		if !strings.Contains(out, `"--name" "box6"`) && !strings.Contains(out, "<string>box6</string>") {
			t.Fatalf("service unit should preserve --name:\n%s", out)
		}
	})

	if runtime.GOOS == "linux" {
		writeMockTools := func(t *testing.T, ipScript, hostnameScript string) string {
			t.Helper()
			dir := t.TempDir()
			if err := os.WriteFile(filepath.Join(dir, "ip"), []byte("#!/bin/sh\n"+ipScript+"\n"), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(dir, "hostname"), []byte("#!/bin/sh\n"+hostnameScript+"\n"), 0o755); err != nil {
				t.Fatal(err)
			}
			return dir
		}

		t.Run("linux_pure_ipv6_global_detection", func(t *testing.T) {
			mockDir := writeMockTools(t, `
case "$*" in
  "route get 1.1.1.1") exit 2 ;;
  "-4 -o addr show scope global up") exit 0 ;;
  "-6 route get 2606:4700:4700::1111") echo "2606:4700:4700::1111 from :: via fe80::1 dev eth0 src 2001:db8::42 metric 100"; exit 0 ;;
  "-o addr show dev lo") echo "1: lo    inet6 ::1/128 scope host"; exit 0 ;;
esac
exit 0`, `echo "2001:db8::42"`)
			out, _ := runInstall(t, mockDir, "hub")
			if !strings.Contains(out, "打开 hub：http://[2001:db8::42]:7788") {
				t.Fatalf("pure IPv6 global detection failed:\n%s", out)
			}
			if !strings.Contains(out, "本机访问：http://[::1]:7788") {
				t.Fatalf("pure IPv6 loopback local_url failed:\n%s", out)
			}
		})

		t.Run("linux_pure_ipv6_skips_link_local_picks_ula", func(t *testing.T) {
			mockDir := writeMockTools(t, `
case "$*" in
  "route get 1.1.1.1") exit 2 ;;
  "-4 -o addr show scope global up") exit 0 ;;
  "-6 route get 2606:4700:4700::1111") exit 2 ;;
  "-6 -o addr show scope global up")
    echo "2: eth0    inet6 fe80::1/64 scope link"
    echo "2: eth0    inet6 fd00::7/64 scope global"
    exit 0 ;;
  "-o addr show dev lo") echo "1: lo    inet6 ::1/128 scope host"; exit 0 ;;
esac
exit 0`, `echo "fe80::1 fd00::7"`)
			out, _ := runInstall(t, mockDir, "hub")
			if !strings.Contains(out, "打开 hub：http://[fd00::7]:7788") {
				t.Fatalf("pure IPv6 ULA detection failed:\n%s", out)
			}
			if strings.Contains(out, "fe80::1") {
				t.Fatalf("link-local fe80::1 leaked into output:\n%s", out)
			}
		})

		t.Run("linux_dual_stack_prefers_ipv4", func(t *testing.T) {
			mockDir := writeMockTools(t, `
case "$*" in
  "route get 1.1.1.1") echo "1.1.1.1 via 10.0.0.1 dev eth0 src 10.2.3.4 uid 1000"; exit 0 ;;
  "-o addr show dev lo")
    echo "1: lo    inet 127.0.0.1/8 scope host lo"
    echo "1: lo    inet6 ::1/128 scope host"
    exit 0 ;;
esac
exit 0`, `echo "10.2.3.4 2001:db8::42"`)
			out, _ := runInstall(t, mockDir, "hub")
			if !strings.Contains(out, "打开 hub：http://10.2.3.4:7788") {
				t.Fatalf("dual-stack should prefer IPv4:\n%s", out)
			}
		})

		t.Run("linux_ipv6_only_loopback_fallback", func(t *testing.T) {
			mockDir := writeMockTools(t, `
case "$*" in
  "route get 1.1.1.1") exit 2 ;;
  "-4 -o addr show scope global up") exit 0 ;;
  "-6 route get 2606:4700:4700::1111") exit 2 ;;
  "-6 -o addr show scope global up") echo "2: eth0    inet6 fe80::1/64 scope link"; exit 0 ;;
  "-o addr show dev lo") echo "1: lo    inet6 ::1/128 scope host"; exit 0 ;;
esac
exit 0`, `echo "fe80::1"`)
			out, _ := runInstall(t, mockDir, "hub")
			if !strings.Contains(out, "打开 hub：http://[::1]:7788") {
				t.Fatalf("pure IPv6 loopback fallback failed:\n%s", out)
			}
			if strings.Contains(out, "本机访问：") {
				t.Fatalf("loopback fallback should not print duplicate local_url:\n%s", out)
			}
		})
	}
}
