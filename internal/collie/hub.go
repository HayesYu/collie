package collie

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/base32"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/hashicorp/yamux"
	qrcode "github.com/skip2/go-qrcode"
)

// ---------------------------------------------------------------- forwarding to nodes

// routed sends a request about a pane, workspace or agent on another machine
// to that machine's node; everything about this machine is handled here.
func (s *Server) routed(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		machine, path := "", r.URL.Path
		for _, key := range []string{"id", "target"} {
			if v := r.PathValue(key); v != "" {
				local, m := split(v)
				machine = m
				path = strings.Replace(path, "/"+v+"/", "/"+local+"/", 1)
			}
		}
		var body []byte
		switch {
		case r.URL.Path == "/api/file", r.URL.Path == "/api/uploads", r.URL.Path == "/api/transcript", r.URL.Path == "/api/models", r.URL.Path == "/api/quota", r.URL.Path == "/api/quota/refresh", r.URL.Path == "/api/trust", r.URL.Path == "/api/update":
			machine = r.URL.Query().Get("machine")
		case r.URL.Path == "/api/agents" && r.Method == "POST":
			body, _ = io.ReadAll(http.MaxBytesReader(w, r.Body, 1<<20))
			var b struct {
				Machine string `json:"machine"`
			}
			json.Unmarshal(body, &b)
			machine = b.Machine
			r.Body = io.NopCloser(bytes.NewReader(body))
		case r.URL.Path == "/api/state":
			machine = ""
		}
		if machine == "" {
			h(w, r)
			return
		}
		if r.URL.Path == "/api/update" && r.Method == http.MethodPost {
			s.updateRemote(w, r, machine)
			return
		}
		s.forward(w, r, machine, path, body)
	}
}

func (s *Server) forward(w http.ResponseWriter, r *http.Request, machine, path string, body []byte) {
	isUpdate := r.URL.Path == "/api/update"
	start := time.Now()
	if isUpdate {
		log.Printf("remote-update start (machine=%s)", machine)
	}
	client, err := s.herdr.Client(machine)
	if err != nil {
		if isUpdate {
			log.Printf("remote-update failed (machine=%s dur=%s): node offline", machine, time.Since(start).Round(time.Millisecond))
		}
		writeJSON(w, 503, map[string]any{"error": "这台电脑现在不在线（" + machine + "）"})
		return
	}
	if isUpdate && selfUpdateForwardTimeout > 0 {
		var cancel context.CancelFunc
		ctx, c := context.WithTimeout(r.Context(), selfUpdateForwardTimeout)
		cancel = c
		defer cancel()
		r = r.WithContext(ctx)
	}
	rp := &httputil.ReverseProxy{
		Transport:     client.Transport,
		FlushInterval: -1,
		Rewrite: func(pr *httputil.ProxyRequest) {
			pr.Out.URL.Scheme, pr.Out.URL.Host = "http", "node"
			pr.Out.URL.Path, pr.Out.URL.RawPath = path, ""
			q := pr.In.URL.Query()
			q.Del("machine")
			pr.Out.URL.RawQuery = q.Encode()
			pr.Out.Header.Del("Authorization")
			pr.Out.Header.Del("Cookie")
			if body != nil {
				pr.Out.Body = io.NopCloser(bytes.NewReader(body))
				pr.Out.ContentLength = int64(len(body))
			}
		},
		ModifyResponse: func(resp *http.Response) error {
			if r.URL.Path == "/api/file" && r.URL.Query().Get("download") != "1" && r.URL.Query().Get("as") == "html" && resp.StatusCode == http.StatusOK && isHTMLPath(r.URL.Query().Get("path")) {
				setHTMLSandboxHeaders(resp.Header)
				return nil
			}
			if !strings.HasPrefix(resp.Header.Get("Content-Type"), "application/json") {
				if isUpdate && resp.StatusCode != http.StatusOK {
					raw, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
					resp.Body.Close()
					msg := strings.TrimSpace(string(raw))
					if msg == "" {
						msg = resp.Status
					}
					log.Printf("remote-update failed (machine=%s status=%d dur=%s): %s", machine, resp.StatusCode, time.Since(start).Round(time.Millisecond), sanitizeUpdateErr(msg))
					jb, _ := json.Marshal(map[string]any{"error": "节点 " + machine + " 返回异常：" + sanitizeUpdateErr(msg)})
					resp.Body = io.NopCloser(bytes.NewReader(jb))
					resp.ContentLength = int64(len(jb))
					resp.Header.Set("Content-Type", "application/json; charset=utf-8")
					resp.Header.Set("Content-Length", strconv.Itoa(len(jb)))
				}
				return nil
			}
			b, err := io.ReadAll(resp.Body)
			resp.Body.Close()
			if err != nil {
				return err
			}
			if isUpdate {
				dur := time.Since(start).Round(time.Millisecond)
				var res struct {
					OK      bool   `json:"ok"`
					From    string `json:"from"`
					Version string `json:"version"`
					Latest  bool   `json:"latest"`
					Error   string `json:"error"`
				}
				_ = json.Unmarshal(b, &res)
				if resp.StatusCode == http.StatusOK {
					log.Printf("remote-update ok (machine=%s from=%s version=%s latest=%v dur=%s)", machine, res.From, res.Version, res.Latest, dur)
				} else {
					log.Printf("remote-update failed (machine=%s status=%d dur=%s): %s", machine, resp.StatusCode, dur, sanitizeUpdateErr(res.Error))
				}
			}
			b = suffixIDs(b, machine)
			resp.Body = io.NopCloser(bytes.NewReader(b))
			resp.ContentLength = int64(len(b))
			resp.Header.Set("Content-Length", strconv.Itoa(len(b)))
			return nil
		},
		ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
			if isUpdate {
				dur := time.Since(start).Round(time.Millisecond)
				log.Printf("remote-update failed (machine=%s dur=%s): %s", machine, dur, sanitizeUpdateErr(err.Error()))
				msg := "转发到 " + machine + " 失败：" + sanitizeUpdateErr(err.Error())
				switch {
				case isTimeoutErr(err):
					msg = "转发到 " + machine + " 更新超时：节点未在规定时间内返回，可能下载较慢或网络卡顿，请稍后刷新查看版本"
				case isConnResetOrEOF(err):
					msg = "转发到 " + machine + " 时连接中断：节点可能正在重启或暂时掉线，请稍候刷新查看版本；若未升级可重试"
				}
				writeJSON(w, 502, map[string]any{"error": msg})
				return
			}
			writeJSON(w, 502, map[string]any{"error": "转发到 " + machine + " 失败：" + err.Error()})
		},
	}
	rp.ServeHTTP(w, r)
}

// ---------------------------------------------------------------- devices and pairing

// A Device is something allowed in: a phone or browser (cookie) or a computer
// running `collie node` (bearer token, may also use the CLI).
type Device struct {
	ID        string    `json:"id"`
	Kind      string    `json:"kind"` // "browser" | "machine"
	Name      string    `json:"name"`
	OS        string    `json:"os,omitempty"`
	Hash      string    `json:"hash"`
	CreatedAt time.Time `json:"created_at"`
	LastSeen  time.Time `json:"last_seen"`
}

const (
	pairCodeTTL        = 10 * time.Minute
	pairCodeRetainTTL  = 24 * time.Hour
	pairCodeMaxHistory = 512
)

var pairFailDelay = 500 * time.Millisecond

type pairCode struct {
	kind    string
	expires time.Time
	usedAt  time.Time
}

type Auth struct {
	db     *sql.DB
	master string
	now    func() time.Time

	mu      sync.Mutex
	devices map[string]*Device // by hash
	codes   map[string]pairCode
	dirty   bool
}

func (a *Auth) nowTime() time.Time {
	if a.now != nil {
		return a.now()
	}
	return time.Now()
}

func newAuth(db *sql.DB, master string) *Auth {
	a := &Auth{db: db, master: master, devices: map[string]*Device{}, codes: map[string]pairCode{}}
	if db != nil {
		var raw string
		if db.QueryRow(`SELECT v FROM kv WHERE k='devices'`).Scan(&raw) == nil {
			var list []*Device
			json.Unmarshal([]byte(raw), &list)
			for _, d := range list {
				a.devices[d.Hash] = d
			}
		}
	}
	return a
}

func hashToken(t string) string { h := sha256.Sum256([]byte(t)); return hex.EncodeToString(h[:]) }

func randomToken() string {
	b := make([]byte, 24)
	rand.Read(b)
	return hex.EncodeToString(b)
}

// who identifies a token: the master token, a device, or nothing.
func (a *Auth) who(token string) (master bool, d *Device) {
	if token == "" {
		return false, nil
	}
	x, y := sha256.Sum256([]byte(token)), sha256.Sum256([]byte(a.master))
	if subtle.ConstantTimeCompare(x[:], y[:]) == 1 {
		return true, nil
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	d = a.devices[hashToken(token)]
	if d != nil && time.Since(d.LastSeen) > time.Minute {
		d.LastSeen = time.Now()
		a.dirty = true
	}
	return false, d
}

func (a *Auth) pruneCodesLocked(now time.Time) {
	for c, p := range a.codes {
		ref := p.expires
		if !p.usedAt.IsZero() && p.usedAt.After(ref) {
			ref = p.usedAt
		}
		if now.Sub(ref) > pairCodeRetainTTL {
			delete(a.codes, c)
		}
	}
	if len(a.codes) < pairCodeMaxHistory {
		return
	}
	type stale struct {
		code string
		ts   time.Time
	}
	var inactive []stale
	for c, p := range a.codes {
		if !p.usedAt.IsZero() || now.After(p.expires) {
			ts := p.expires
			if !p.usedAt.IsZero() {
				ts = p.usedAt
			}
			inactive = append(inactive, stale{c, ts})
		}
	}
	sort.Slice(inactive, func(i, j int) bool { return inactive[i].ts.Before(inactive[j].ts) })
	for _, it := range inactive {
		if len(a.codes) < pairCodeMaxHistory {
			break
		}
		delete(a.codes, it.code)
	}
}

func (a *Auth) newCode(kind string) (string, time.Time) {
	b := make([]byte, 5)
	rand.Read(b)
	code := base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(b) // 8 chars
	now := a.nowTime()
	exp := now.Add(pairCodeTTL)
	a.mu.Lock()
	defer a.mu.Unlock()
	a.pruneCodesLocked(now)
	a.codes[code] = pairCode{kind: kind, expires: exp}
	return code, exp
}

// redeem turns a one-time code into a device with its own token.
func (a *Auth) redeem(code, kind, name, osName string) (*Device, string, error) {
	code = strings.ToUpper(strings.TrimSpace(code))
	a.mu.Lock()
	defer a.mu.Unlock()
	if code == "" {
		return nil, "", fmt.Errorf("配对码无效，请核对后重试或重新生成")
	}
	p, ok := a.codes[code]
	if !ok {
		return nil, "", fmt.Errorf("配对码无效，请核对后重试或重新生成")
	}
	if !p.usedAt.IsZero() {
		return nil, "", fmt.Errorf("配对码已被使用（每个配对码只能用一次），请重新生成新的配对码")
	}
	now := a.nowTime()
	if now.After(p.expires) {
		return nil, "", fmt.Errorf("配对码已过期，请重新生成新的配对码")
	}
	if p.kind != kind {
		return nil, "", fmt.Errorf("这个配对码不是给%s用的", map[string]string{"browser": "手机或浏览器", "machine": "电脑"}[kind])
	}
	p.usedAt = now
	a.codes[code] = p
	token := randomToken()
	d := &Device{ID: a.uniqueID(kind, name), Kind: kind, Name: name, OS: osName, Hash: hashToken(token), CreatedAt: now, LastSeen: now}
	a.devices[d.Hash] = d
	a.dirty = true
	return d, token, nil
}

var slugRe = regexp.MustCompile(`[^a-z0-9-]+`)

func (a *Auth) uniqueID(kind, name string) string {
	base := strings.Trim(slugRe.ReplaceAllString(strings.ToLower(name), "-"), "-")
	if len(base) > 20 {
		base = base[:20]
	}
	if base == "" {
		base = kind
	}
	taken := map[string]bool{}
	for _, d := range a.devices {
		taken[d.ID] = true
	}
	id := base
	for i := 2; taken[id]; i++ {
		id = fmt.Sprintf("%s-%d", base, i)
	}
	return id
}

func (a *Auth) list() []Device {
	a.mu.Lock()
	defer a.mu.Unlock()
	out := []Device{}
	for _, d := range a.devices {
		out = append(out, *d)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.Before(out[j].CreatedAt) })
	return out
}

func (a *Auth) remove(id string) *Device {
	a.mu.Lock()
	defer a.mu.Unlock()
	for h, d := range a.devices {
		if d.ID == id {
			delete(a.devices, h)
			a.dirty = true
			return d
		}
	}
	return nil
}

func (a *Auth) save() {
	a.mu.Lock()
	if !a.dirty {
		a.mu.Unlock()
		return
	}
	list := make([]*Device, 0, len(a.devices))
	for _, d := range a.devices {
		list = append(list, d)
	}
	a.dirty = false
	a.mu.Unlock()
	if a.db == nil {
		return
	}
	b, _ := json.Marshal(list)
	if _, err := a.db.Exec(`INSERT INTO kv(k,v) VALUES('devices',?) ON CONFLICT(k) DO UPDATE SET v=excluded.v`, string(b)); err != nil {
		log.Printf("save devices: %v", err)
	}
}

// ---------------------------------------------------------------- addresses

type lanIface struct {
	Name  string
	Flags net.Flags
	IPs   []net.IP
}

func systemLanIfaces() []lanIface {
	ifs, _ := net.Interfaces()
	out := make([]lanIface, 0, len(ifs))
	for _, ifc := range ifs {
		addrs, _ := ifc.Addrs()
		ips := make([]net.IP, 0, len(addrs))
		for _, a := range addrs {
			switch v := a.(type) {
			case *net.IPNet:
				ips = append(ips, v.IP)
			case *net.IPAddr:
				ips = append(ips, v.IP)
			}
		}
		out = append(out, lanIface{Name: ifc.Name, Flags: ifc.Flags, IPs: ips})
	}
	return out
}

// lanAddresses lists the URLs other devices can try, best first: addresses
// given with -url, then IPv4 (10/8, 192.168/16, 172.16/12, 100.64/10,
// other global unicast), then IPv6 (global unicast, ULA), then loopback.
func lanAddresses(port int, extra []string) []string {
	return lanAddressesFrom(port, extra, systemLanIfaces())
}

func lanAddressesFrom(port int, extra []string, ifaces []lanIface) []string {
	var out []string
	seen := map[string]bool{}
	add := func(u string) {
		if !seen[u] {
			seen[u] = true
			out = append(out, u)
		}
	}
	for _, e := range extra {
		if e = strings.TrimRight(strings.TrimSpace(e), "/"); e != "" {
			add(e)
		}
	}
	type cand struct {
		ip   net.IP
		rank int
	}
	var cs []cand
	hasV4, hasV6 := false, false
	hasLoopbackV4, hasLoopbackV6 := false, false
	for _, ifc := range ifaces {
		if ifc.Flags&net.FlagUp == 0 || virtualIf(ifc.Name) {
			continue
		}
		if ifc.Flags&net.FlagLoopback != 0 {
			for _, raw := range ifc.IPs {
				if raw == nil || !raw.IsLoopback() {
					continue
				}
				if raw.To4() != nil {
					hasLoopbackV4 = true
				} else if raw.To16() != nil {
					hasLoopbackV6 = true
				}
			}
			continue
		}
		for _, raw := range ifc.IPs {
			if raw == nil || !raw.IsGlobalUnicast() {
				continue
			}
			if ip4 := raw.To4(); ip4 != nil {
				hasV4 = true
				// Company networks are usually 10/8; home Wi-Fi 192.168/16;
				// 172.16/12 is often a local or container network.
				rank := 4
				switch {
				case ip4[0] == 10:
					rank = 0
				case ip4[0] == 192 && ip4[1] == 168:
					rank = 1
				case ip4[0] == 172 && ip4[1]&0xf0 == 16:
					rank = 2
				case ip4[0] == 100 && ip4[1]&0xc0 == 64:
					rank = 3
				}
				cs = append(cs, cand{ip4, rank})
				continue
			}
			if ip16 := raw.To16(); ip16 != nil {
				hasV6 = true
				rank := 5
				if ip16.IsPrivate() { // ULA fc00::/7
					rank = 6
				}
				cs = append(cs, cand{ip16, rank})
			}
		}
	}
	sort.SliceStable(cs, func(i, j int) bool { return cs[i].rank < cs[j].rank })
	portStr := strconv.Itoa(port)
	for _, c := range cs {
		add("http://" + net.JoinHostPort(c.ip.String(), portStr))
	}
	if !hasV4 && !hasLoopbackV4 && (hasV6 || hasLoopbackV6) {
		add("http://" + net.JoinHostPort("::1", portStr))
	} else {
		add("http://" + net.JoinHostPort("127.0.0.1", portStr))
	}
	return out
}

func splitHost(hostport string) string {
	hostport = strings.TrimSpace(hostport)
	if host, _, err := net.SplitHostPort(hostport); err == nil {
		return host
	}
	return strings.TrimSuffix(strings.TrimPrefix(hostport, "["), "]")
}

func canonicalIP(host string) string {
	h := strings.TrimSpace(host)
	if i := strings.IndexByte(h, '%'); i >= 0 {
		h = h[:i]
	}
	if ip := net.ParseIP(h); ip != nil {
		return ip.String()
	}
	return host
}

func isLoopbackHost(hostport string) bool {
	h := splitHost(hostport)
	if strings.EqualFold(h, "localhost") {
		return true
	}
	if i := strings.IndexByte(h, '%'); i >= 0 {
		h = h[:i]
	}
	ip := net.ParseIP(h)
	return ip != nil && ip.IsLoopback()
}

func isLocalUIURL(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "http" || u.User != nil || u.Host == "" || u.Port() == "" {
		return false
	}
	if (u.Path != "" && u.Path != "/") || u.RawQuery != "" || u.Fragment != "" {
		return false
	}
	return isLoopbackHost(u.Host)
}

func normalizeBaseURL(raw string) string {
	a := strings.TrimRight(strings.TrimSpace(raw), "/")
	if a == "" || strings.Contains(a, "://") {
		return a
	}
	if ip := net.ParseIP(a); ip != nil && ip.To4() == nil {
		return "http://[" + a + "]"
	}
	return "http://" + a
}

func hasIPv4Loopback() bool {
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		return true
	}
	for _, a := range addrs {
		var ip net.IP
		switch v := a.(type) {
		case *net.IPNet:
			ip = v.IP
		case *net.IPAddr:
			ip = v.IP
		}
		if ip != nil && ip.To4() != nil && ip.IsLoopback() {
			return true
		}
	}
	return false
}

func formatTCPHostPort(a *net.TCPAddr) string {
	host := a.IP.String()
	if a.Zone != "" {
		host += "%" + a.Zone
	}
	return net.JoinHostPort(host, strconv.Itoa(a.Port))
}

func localDialAddr(bound *net.TCPAddr, ipv4Loopback func() bool) string {
	if bound == nil {
		return ""
	}
	portStr := strconv.Itoa(bound.Port)
	if bound.IP != nil && !bound.IP.IsUnspecified() {
		return formatTCPHostPort(bound)
	}
	if bound.IP != nil && bound.IP.To4() != nil {
		return net.JoinHostPort("127.0.0.1", portStr)
	}
	if ipv4Loopback == nil {
		ipv4Loopback = hasIPv4Loopback
	}
	if ipv4Loopback() {
		return net.JoinHostPort("127.0.0.1", portStr)
	}
	return net.JoinHostPort("::1", portStr)
}

// ---------------------------------------------------------------- hub endpoints

func (s *Server) hubRoutes(mux *http.ServeMux) {
	api := func(pattern string, h http.HandlerFunc) { mux.Handle(pattern, s.auth(h)) }
	mux.HandleFunc("POST /api/pair", s.pair)
	mux.HandleFunc("GET /api/node/connect", s.nodeConnect)
	api("POST /api/pair/new", s.pairNew)
	api("GET /api/local-app", s.localApp)
	api("GET /api/qr", s.qr)
	api("GET /api/devices", s.listDevices)
	api("GET /api/version", s.getVersion)
	api("GET /api/history", s.listHistory)
	api("POST /api/history/{id}/delete", s.deleteHistory)
	api("POST /api/devices/{id}/delete", s.deleteDevice)
	api("GET /api/hubs", s.getHubs)
	api("POST /api/hubs", s.setHubs)
}

func (s *Server) addresses(r *http.Request) []string {
	list := lanAddresses(s.port, s.publicURLs)
	// The address this browser already uses works for it; keep it visible.
	if r != nil && r.Host != "" {
		cur := "http://" + r.Host
		if r.TLS != nil {
			cur = "https://" + r.Host
		}
		found := false
		for _, a := range list {
			if a == cur {
				found = true
			}
		}
		if !found && !isLoopbackHost(r.Host) {
			list = append([]string{cur}, list...)
		}
	}
	return list
}

func (s *Server) pairNew(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Kind string `json:"kind"`
	}
	decode(w, r, &body)
	if body.Kind != "machine" {
		body.Kind = "browser"
	}
	code, exp := s.auths.newCode(body.Kind)
	writeJSON(w, 200, map[string]any{"code": code, "expires": exp, "kind": body.Kind, "addresses": s.addresses(r)})
}

func (s *Server) pair(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	var body struct {
		Code string `json:"code"`
		Kind string `json:"kind"`
		Name string `json:"name"`
		OS   string `json:"os"`
		// Another hub's page keeps the token itself: it can't use our cookie.
		WantToken bool `json:"want_token"`
	}
	if !decode(w, r, &body) {
		return
	}
	if body.Kind != "machine" {
		body.Kind = "browser"
	}
	name := strings.TrimSpace(body.Name)
	if name == "" {
		name = map[string]string{"browser": "浏览器", "machine": "电脑"}[body.Kind]
	}
	if len([]rune(name)) > 40 {
		name = string([]rune(name)[:40])
	}
	d, token, err := s.auths.redeem(body.Code, body.Kind, name, body.OS)
	if err != nil {
		if pairFailDelay > 0 {
			time.Sleep(pairFailDelay)
		}
		writeJSON(w, 401, map[string]any{"error": err.Error()})
		return
	}
	go s.auths.save()
	if d.Kind == "browser" && !body.WantToken {
		setTokenCookie(w, r, token)
	}
	writeJSON(w, 200, map[string]any{"ok": true, "id": d.ID, "token": token})
}

func setTokenCookie(w http.ResponseWriter, r *http.Request, token string) {
	const maxAge = 365 * 24 * 3600
	http.SetCookie(w, &http.Cookie{
		Name: cookieName, Value: token, Path: "/", HttpOnly: true,
		SameSite: http.SameSiteLaxMode, MaxAge: maxAge, Expires: time.Now().Add(maxAge * time.Second), Secure: r.TLS != nil,
	})
}

func (s *Server) qr(w http.ResponseWriter, r *http.Request) {
	text := r.URL.Query().Get("text")
	if text == "" || len(text) > 1024 {
		http.Error(w, "bad text", 400)
		return
	}
	level := qrcode.Medium
	if len(text) > 180 {
		level = qrcode.Low
	}
	png, err := qrcode.Encode(text, level, 512)
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	w.Header().Set("Content-Type", "image/png")
	w.Header().Set("Cache-Control", "private, max-age=600")
	w.Write(png)
}

func (s *Server) listDevices(w http.ResponseWriter, r *http.Request) {
	machines := map[string]Machine{}
	for _, m := range s.herdr.Machines() {
		machines[m.ID] = m
	}
	type view struct {
		Device
		Online  bool     `json:"online"`
		Current bool     `json:"current"`
		Home    string   `json:"home,omitempty"`
		Version string   `json:"version,omitempty"`
		Agents  []string `json:"agents"`
	}
	_, me := s.identity(r)
	out := []view{}
	for _, d := range s.auths.list() {
		v := view{Device: d}
		v.Hash = ""
		if m, ok := machines[d.ID]; ok && d.Kind == "machine" {
			v.Online, v.Home, v.Version, v.Agents = m.Online, m.Home, m.Version, m.Agents
		}
		v.Current = me != nil && me.ID == d.ID
		out = append(out, v)
	}
	local := s.herdr.Machines()[0]
	_, herdrErr := s.herdr.local.conn.call(r.Context(), "workspace.list", nil)
	writeJSON(w, 200, map[string]any{
		"devices":   out,
		"hub":       map[string]any{"name": local.Name, "home": local.Home, "herdr": herdrErr == nil, "version": version, "agents": localAgents(local.Home, true)},
		"addresses": s.addresses(r),
		"latest":    latestVersion(true),
	})
}

func (s *Server) deleteDevice(w http.ResponseWriter, r *http.Request) {
	d := s.auths.remove(r.PathValue("id"))
	if d == nil {
		writeJSON(w, 404, map[string]any{"error": "not found"})
		return
	}
	if d.Kind == "machine" {
		s.herdr.detach(d.ID, nil)
	}
	go s.auths.save()
	writeJSON(w, 200, map[string]any{"ok": true})
}

// nodeConnect upgrades a computer's connection into a multiplexed tunnel.
// The hub opens streams on it: "herdr" reaches that computer's herdr socket,
// "http" its node API (chat, screens, directories, launching).
func (s *Server) nodeConnect(w http.ResponseWriter, r *http.Request) {
	_, d := s.identity(r)
	if d == nil || d.Kind != "machine" {
		writeJSON(w, 401, map[string]any{"error": "unauthorized"})
		return
	}
	if !strings.EqualFold(r.Header.Get("Upgrade"), "collie-node") {
		writeJSON(w, 400, map[string]any{"error": "upgrade required"})
		return
	}
	hj, ok := w.(http.Hijacker)
	if !ok {
		writeJSON(w, 500, map[string]any{"error": "no hijack"})
		return
	}
	conn, buf, err := hj.Hijack()
	if err != nil {
		return
	}
	buf.WriteString("HTTP/1.1 101 Switching Protocols\r\nUpgrade: collie-node\r\nConnection: Upgrade\r\n\r\n")
	if err := buf.Flush(); err != nil {
		conn.Close()
		return
	}
	cfg := yamux.DefaultConfig()
	cfg.LogOutput = io.Discard
	cfg.KeepAliveInterval = 15 * time.Second
	sess, err := yamux.Client(&bufConn{Conn: conn, r: buf.Reader}, cfg)
	if err != nil {
		conn.Close()
		return
	}
	open := func() (net.Conn, error) { return sess.Open() }
	httpDial := streamDialer(open, "http")
	m := &Machine{
		ID: d.ID, Name: d.Name, OS: r.Header.Get("X-Collie-OS"), Home: r.Header.Get("X-Collie-Home"),
		Bin: r.Header.Get("X-Collie-Bin"), Version: r.Header.Get("X-Collie-Version"), Agents: parseAgentsHeader(r.Header.Get("X-Collie-Agents")), Online: true, Since: time.Now(),
		relayUpdate: r.Header.Get("X-Collie-Relay-Update") == "1",
		conn:        &herdrConn{dial: streamDialer(open, "herdr")},
		client: &http.Client{Transport: &http.Transport{
			DialContext:           func(ctx context.Context, _, _ string) (net.Conn, error) { return httpDial(ctx) },
			MaxIdleConnsPerHost:   8,
			IdleConnTimeout:       60 * time.Second,
			ResponseHeaderTimeout: 0,
			ExpectContinueTimeout: 1 * time.Second,
		}},
		close: func() { sess.Close() },
	}
	if n := strings.TrimSpace(r.Header.Get("X-Collie-Name")); n != "" {
		m.Name = n
	}
	m.ip = canonicalIP(splitHost(r.RemoteAddr))
	if u := r.Header.Get("X-Collie-UI"); isLocalUIURL(u) {
		m.ui = u
	}
	s.herdr.attach(m)
	log.Printf("node %s (%s) connected from %s", m.ID, m.Name, r.RemoteAddr)
	<-sess.CloseChan()
	s.herdr.detach(m.ID, m)
	log.Printf("node %s disconnected", m.ID)
}

// bufConn reads through the bufio.Reader that may already hold tunnel bytes.
type bufConn struct {
	net.Conn
	r *bufio.Reader
}

func (c *bufConn) Read(p []byte) (int, error) { return c.r.Read(p) }

// identity reports who made a request: the master token or a paired device.
func (s *Server) identity(r *http.Request) (master bool, d *Device) {
	var got string
	if c, err := r.Cookie(cookieName); err == nil {
		got = c.Value
	} else if h := r.Header.Get("Authorization"); strings.HasPrefix(h, "Bearer ") {
		got = strings.TrimPrefix(h, "Bearer ")
	} else if t := r.URL.Query().Get("access_token"); t != "" && r.Method == http.MethodGet &&
		strings.Contains(r.Header.Get("Accept"), "text/event-stream") {
		// An EventSource can't set headers; another hub's page passes the
		// token in the URL, for event streams only.
		got = t
	}
	if s.auths == nil {
		x, y := sha256.Sum256([]byte(got)), sha256.Sum256([]byte(s.token))
		return got != "" && subtle.ConstantTimeCompare(x[:], y[:]) == 1, nil
	}
	return s.auths.who(got)
}

// pairURL is what a phone opens after scanning.
func pairURL(base, code string) string {
	return strings.TrimRight(base, "/") + "/?pair=" + url.QueryEscape(code)
}

// printQR renders a QR code with half-block characters for a terminal.
func printQR(w io.Writer, text string) {
	q, err := qrcode.New(text, qrcode.Low)
	if err != nil {
		return
	}
	q.DisableBorder = false
	bm := q.Bitmap()
	for y := 0; y < len(bm); y += 2 {
		var b strings.Builder
		b.WriteString("  ")
		for x := 0; x < len(bm[y]); x++ {
			top := bm[y][x]
			bot := y+1 < len(bm) && bm[y+1][x]
			switch {
			case top && bot:
				b.WriteString(" ")
			case top:
				b.WriteString("▄")
			case bot:
				b.WriteString("▀")
			default:
				b.WriteString("█")
			}
		}
		fmt.Fprintln(w, b.String())
	}
}

// virtualIf reports container and VM bridges, whose addresses only make sense
// inside this machine.
func virtualIf(name string) bool {
	for _, p := range []string{"docker", "br-", "veth", "virbr", "cni", "flannel", "vmnet", "bridge", "lxc", "podman"} {
		if strings.HasPrefix(name, p) {
			return true
		}
	}
	return false
}
