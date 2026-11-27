package collie

import (
	"bytes"
	"encoding/json"
	"image/png"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/lihuanyu0w0/collie/web"
)

func newTestAuthServer(t *testing.T, master string) (*Server, *Auth, http.Handler) {
	t.Helper()
	if authFailDelay != 0 || pairFailDelay != 0 {
		oldAuthDelay := authFailDelay
		oldPairDelay := pairFailDelay
		authFailDelay = 0
		pairFailDelay = 0
		t.Cleanup(func() {
			authFailDelay = oldAuthDelay
			pairFailDelay = oldPairDelay
		})
	}

	home := t.TempDir()
	auths := newAuth(nil, master)
	srv := &Server{
		herdr: newHerdr(filepath.Join(home, "missing.sock"), "hub", home),
		token: master,
		home:  home,
		web:   web.FS,
		auths: auths,
	}
	return srv, auths, srv.Routes()
}

func doJSONReq(t *testing.T, h http.Handler, method, target string, payload any, authHeader string, cookies ...*http.Cookie) *httptest.ResponseRecorder {
	t.Helper()
	var body *bytes.Reader
	if payload != nil {
		b, err := json.Marshal(payload)
		if err != nil {
			t.Fatal(err)
		}
		body = bytes.NewReader(b)
	} else {
		body = bytes.NewReader(nil)
	}
	req := httptest.NewRequest(method, target, body)
	if payload != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if authHeader != "" {
		req.Header.Set("Authorization", authHeader)
	}
	for _, c := range cookies {
		req.AddCookie(c)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func findCookie(rec *httptest.ResponseRecorder, name string) *http.Cookie {
	for _, c := range rec.Result().Cookies() {
		if c.Name == name {
			return c
		}
	}
	return nil
}

func TestLoginBrowserTokenRestoreAndRevocation(t *testing.T) {
	t.Parallel()
	const master = "master-secret-token"
	_, _, h := newTestAuthServer(t, master)

	// 1. Mint a browser pairing code and redeem it.
	rec := doJSONReq(t, h, http.MethodPost, "/api/pair/new", map[string]any{"kind": "browser"}, "Bearer "+master)
	if rec.Code != 200 {
		t.Fatalf("POST /api/pair/new = %d: %s", rec.Code, rec.Body.String())
	}
	var newCodeRes struct {
		Code string `json:"code"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &newCodeRes); err != nil || len(newCodeRes.Code) != 8 {
		t.Fatalf("bad pair/new body: %s", rec.Body.String())
	}

	pairRec := doJSONReq(t, h, http.MethodPost, "/api/pair", map[string]any{
		"code": newCodeRes.Code,
		"kind": "browser",
		"name": "iPhone · Safari",
	}, "")
	if pairRec.Code != 200 {
		t.Fatalf("POST /api/pair = %d: %s", pairRec.Code, pairRec.Body.String())
	}
	if cc := pairRec.Header().Get("Cache-Control"); !strings.Contains(cc, "no-store") {
		t.Fatalf("POST /api/pair Cache-Control = %q, want no-store", cc)
	}
	var pairRes struct {
		OK    bool   `json:"ok"`
		ID    string `json:"id"`
		Token string `json:"token"`
	}
	if err := json.Unmarshal(pairRec.Body.Bytes(), &pairRes); err != nil {
		t.Fatalf("decode pair response: %v", err)
	}
	if !pairRes.OK || pairRes.ID == "" || pairRes.Token == "" {
		t.Fatalf("expected ok, id, and one-time browser token in pair response, got %s", pairRec.Body.String())
	}
	pairCookie := findCookie(pairRec, cookieName)
	if pairCookie == nil || !pairCookie.HttpOnly || pairCookie.Value != pairRes.Token {
		t.Fatalf("expected HttpOnly %s cookie matching device token, got %+v", cookieName, pairCookie)
	}

	// 2. Session works with paired cookie, fails when cookie is evicted.
	if r := doJSONReq(t, h, http.MethodGet, "/api/session", nil, "", pairCookie); r.Code != 200 {
		t.Fatalf("GET /api/session with paired cookie = %d", r.Code)
	}
	if r := doJSONReq(t, h, http.MethodGet, "/api/session", nil, ""); r.Code != 401 {
		t.Fatalf("GET /api/session without cookie = %d, want 401", r.Code)
	}

	// 3. Silent Cookie restore via POST /api/login with browser device token.
	restoreRec := doJSONReq(t, h, http.MethodPost, "/api/login", map[string]any{"token": pairRes.Token}, "")
	if restoreRec.Code != 200 {
		t.Fatalf("POST /api/login with browser device token = %d: %s", restoreRec.Code, restoreRec.Body.String())
	}
	if cc := restoreRec.Header().Get("Cache-Control"); !strings.Contains(cc, "no-store") {
		t.Fatalf("POST /api/login Cache-Control = %q, want no-store", cc)
	}
	restoredCookie := findCookie(restoreRec, cookieName)
	if restoredCookie == nil || !restoredCookie.HttpOnly || restoredCookie.Value != pairRes.Token {
		t.Fatalf("restored cookie mismatch: %+v", restoredCookie)
	}
	if r := doJSONReq(t, h, http.MethodGet, "/api/session", nil, "", restoredCookie); r.Code != 200 {
		t.Fatalf("GET /api/session after silent restore = %d, want 200", r.Code)
	}

	// 4. Master token still works on POST /api/login.
	masterRec := doJSONReq(t, h, http.MethodPost, "/api/login", map[string]any{"token": master}, "")
	if masterRec.Code != 200 {
		t.Fatalf("POST /api/login with master token = %d", masterRec.Code)
	}
	if mc := findCookie(masterRec, cookieName); mc == nil || !mc.HttpOnly || mc.Value != master {
		t.Fatalf("master cookie mismatch: %+v", mc)
	}

	// 5. Security boundary: machine device token MUST NOT be allowed to restore a browser cookie.
	mCodeRec := doJSONReq(t, h, http.MethodPost, "/api/pair/new", map[string]any{"kind": "machine"}, "Bearer "+master)
	if mCodeRec.Code != 200 {
		t.Fatalf("POST /api/pair/new machine = %d", mCodeRec.Code)
	}
	var mCode struct {
		Code string `json:"code"`
	}
	_ = json.Unmarshal(mCodeRec.Body.Bytes(), &mCode)
	mPairRec := doJSONReq(t, h, http.MethodPost, "/api/pair", map[string]any{
		"code": mCode.Code,
		"kind": "machine",
		"name": "mac",
	}, "")
	if mPairRec.Code != 200 {
		t.Fatalf("POST /api/pair machine = %d: %s", mPairRec.Code, mPairRec.Body.String())
	}
	if findCookie(mPairRec, cookieName) != nil {
		t.Fatalf("machine pairing must not set browser cookie")
	}
	var mPairRes struct {
		Token string `json:"token"`
	}
	_ = json.Unmarshal(mPairRec.Body.Bytes(), &mPairRes)
	if mPairRes.Token == "" {
		t.Fatalf("machine pairing missing token")
	}
	mLoginRec := doJSONReq(t, h, http.MethodPost, "/api/login", map[string]any{"token": mPairRes.Token}, "")
	if mLoginRec.Code != 401 {
		t.Fatalf("POST /api/login with machine token = %d, want 401", mLoginRec.Code)
	}
	if findCookie(mLoginRec, cookieName) != nil {
		t.Fatalf("machine token login must not set cookie")
	}

	// 6. Security boundary: revoking the browser device immediately rejects Cookie restore & session.
	delRec := doJSONReq(t, h, http.MethodPost, "/api/devices/"+pairRes.ID+"/delete", nil, "Bearer "+master)
	if delRec.Code != 200 {
		t.Fatalf("POST /api/devices/%s/delete = %d: %s", pairRes.ID, delRec.Code, delRec.Body.String())
	}
	revokedRestore := doJSONReq(t, h, http.MethodPost, "/api/login", map[string]any{"token": pairRes.Token}, "")
	if revokedRestore.Code != 401 {
		t.Fatalf("POST /api/login with revoked browser token = %d, want 401", revokedRestore.Code)
	}
	if findCookie(revokedRestore, cookieName) != nil {
		t.Fatalf("revoked device login must not set cookie")
	}
	if r := doJSONReq(t, h, http.MethodGet, "/api/session", nil, "", restoredCookie); r.Code != 401 {
		t.Fatalf("GET /api/session with revoked cookie = %d, want 401", r.Code)
	}

	// 7. Empty and invalid tokens rejected.
	for _, bad := range []string{"", "   ", "not-a-valid-token"} {
		if r := doJSONReq(t, h, http.MethodPost, "/api/login", map[string]any{"token": bad}, ""); r.Code != 401 {
			t.Fatalf("POST /api/login(%q) = %d, want 401", bad, r.Code)
		}
	}
}

func TestPairCodeDistinctErrors(t *testing.T) {
	t.Parallel()
	const master = "master-secret-token"
	_, auths, h := newTestAuthServer(t, master)

	now := time.Date(2026, 11, 26, 0, 0, 0, 0, time.UTC)
	auths.now = func() time.Time { return now }

	redeemErr := func(code, kind string) (int, string) {
		rec := doJSONReq(t, h, http.MethodPost, "/api/pair", map[string]any{
			"code": code,
			"kind": kind,
			"name": "iPhone · Safari",
		}, "")
		var body struct {
			Error string `json:"error"`
		}
		_ = json.Unmarshal(rec.Body.Bytes(), &body)
		return rec.Code, body.Error
	}

	// 1. Unknown / invalid code.
	if status, msg := redeemErr("AAAAAAAA", "browser"); status != 401 || !strings.Contains(msg, "配对码无效") || strings.Contains(msg, "已过期") || strings.Contains(msg, "已被使用") {
		t.Fatalf("invalid code => (%d, %q), want distinct invalid message", status, msg)
	}

	// 2. Kind mismatch does not burn the code; first valid use succeeds; reuse reports used error even after TTL.
	c1, _ := auths.newCode("browser")
	if status, msg := redeemErr(c1, "machine"); status != 401 || !strings.Contains(msg, "不是给电脑用的") {
		t.Fatalf("wrong kind => (%d, %q)", status, msg)
	}
	if status, msg := redeemErr(c1, "browser"); status != 200 || msg != "" {
		t.Fatalf("first valid redeem => (%d, %q)", status, msg)
	}
	if status, msg := redeemErr(c1, "browser"); status != 401 || !strings.Contains(msg, "已被使用") || !strings.Contains(msg, "只能用一次") {
		t.Fatalf("immediate reuse => (%d, %q), want distinct reused message", status, msg)
	}

	// 3. Expired unused code reports expired (even after generating another code that runs pruning).
	c2, _ := auths.newCode("browser")
	now = now.Add(pairCodeTTL + time.Minute)
	c3, _ := auths.newCode("browser") // triggers pruneCodesLocked

	if status, msg := redeemErr(c2, "browser"); status != 401 || !strings.Contains(msg, "已过期") || strings.Contains(msg, "已被使用") {
		t.Fatalf("expired code => (%d, %q), want distinct expired message", status, msg)
	}
	// Used code c1 still reports "已被使用" rather than "已过期" within retention window.
	if status, msg := redeemErr(c1, "browser"); status != 401 || !strings.Contains(msg, "已被使用") {
		t.Fatalf("used code after TTL => (%d, %q), want reused message", status, msg)
	}
	// Fresh code c3 works normally.
	if status, msg := redeemErr(c3, "browser"); status != 200 || msg != "" {
		t.Fatalf("fresh code c3 => (%d, %q)", status, msg)
	}
}

func TestPWAAssetsUnauthenticated(t *testing.T) {
	t.Parallel()
	_, _, h := newTestAuthServer(t, "master-secret-token")

	// 1. GET /apple-touch-icon.png without any cookie or Authorization header.
	iconRec := doJSONReq(t, h, http.MethodGet, "/apple-touch-icon.png", nil, "")
	if iconRec.Code != 200 {
		t.Fatalf("GET /apple-touch-icon.png = %d, want 200", iconRec.Code)
	}
	if ct := iconRec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "image/png") {
		t.Fatalf("GET /apple-touch-icon.png Content-Type = %q, want image/png", ct)
	}
	cfg, err := png.DecodeConfig(bytes.NewReader(iconRec.Body.Bytes()))
	if err != nil {
		t.Fatalf("decode /apple-touch-icon.png: %v", err)
	}
	if cfg.Width != 180 || cfg.Height != 180 {
		t.Fatalf("/apple-touch-icon.png size = %dx%d, want 180x180", cfg.Width, cfg.Height)
	}

	// 2. GET /manifest.webmanifest without auth.
	manRec := doJSONReq(t, h, http.MethodGet, "/manifest.webmanifest", nil, "")
	if manRec.Code != 200 {
		t.Fatalf("GET /manifest.webmanifest = %d, want 200", manRec.Code)
	}
	if ct := manRec.Header().Get("Content-Type"); !strings.Contains(ct, "application/manifest+json") {
		t.Fatalf("GET /manifest.webmanifest Content-Type = %q, want application/manifest+json", ct)
	}
	var manifest struct {
		Name  string `json:"name"`
		Icons []struct {
			Src   string `json:"src"`
			Sizes string `json:"sizes"`
			Type  string `json:"type"`
		} `json:"icons"`
		LaunchHandler struct {
			ClientMode []string `json:"client_mode"`
		} `json:"launch_handler"`
		ProtocolHandlers []struct {
			Protocol string `json:"protocol"`
			URL      string `json:"url"`
		} `json:"protocol_handlers"`
	}
	if err := json.Unmarshal(manRec.Body.Bytes(), &manifest); err != nil {
		t.Fatalf("parse manifest.webmanifest: %v", err)
	}
	if manifest.Name != "Collie" || len(manifest.Icons) < 2 {
		t.Fatalf("unexpected manifest: %+v", manifest)
	}
	if len(manifest.LaunchHandler.ClientMode) < 2 || manifest.LaunchHandler.ClientMode[0] != "focus-existing" {
		t.Fatalf("manifest missing launch_handler focus-existing: %+v", manifest.LaunchHandler)
	}
	if len(manifest.ProtocolHandlers) == 0 || manifest.ProtocolHandlers[0].Protocol != "web+collie" || manifest.ProtocolHandlers[0].URL != "/?peer_invite=%s" {
		t.Fatalf("manifest missing web+collie protocol_handlers: %+v", manifest.ProtocolHandlers)
	}

	// 3. GET / (index.html) without auth exposes iOS PWA meta tags & apple-touch-icon link.
	idxRec := doJSONReq(t, h, http.MethodGet, "/", nil, "")
	if idxRec.Code != 200 {
		t.Fatalf("GET / = %d, want 200", idxRec.Code)
	}
	html := idxRec.Body.String()
	for _, want := range []string{
		`<link rel="apple-touch-icon" sizes="180x180" href="/apple-touch-icon.png">`,
		`<meta name="apple-mobile-web-app-capable" content="yes">`,
		`<meta name="apple-mobile-web-app-title" content="Collie">`,
		`<meta name="apple-mobile-web-app-status-bar-style"`,
		`id="login-tip"`,
	} {
		if !strings.Contains(html, want) {
			t.Fatalf("index.html missing %q", want)
		}
	}
}

func TestPWAFrontendRecoveryAndAvatarContract(t *testing.T) {
	t.Parallel()
	cssBytes, err := os.ReadFile(filepath.Join("..", "..", "web", "app.css"))
	if err != nil {
		t.Fatalf("read web/app.css: %v", err)
	}
	css := string(cssBytes)
	for _, want := range []string{
		"--human-bg: #111111;",
		"--human-line: #f7f7f5;",
		"--human-bg: #25252a;",
		"--human-line: #ededeb;",
		".td-av.user { background: var(--human-bg); border: 1px solid var(--human-bg); color: var(--human-line); }",
		".td-av.user svg { width: 21px; height: 21px; display: block; }",
		".login-tip {",
		"wait-halo 2.8s ease-in-out infinite",
		"-webkit-box-decoration-break: clone;",
		"box-decoration-break: clone;",
	} {
		if !strings.Contains(css, want) {
			t.Fatalf("web/app.css missing %q", want)
		}
	}
	if strings.Contains(css, "1.5px dashed var(--fg-3)") {
		t.Fatalf("web/app.css still contains legacy dashed .dot.waiting ring")
	}

	jsBytes, err := os.ReadFile(filepath.Join("..", "..", "web", "app.js"))
	if err != nil {
		t.Fatalf("read web/app.js: %v", err)
	}
	js := string(jsBytes)
	for _, want := range []string{
		`M18.2 24.4c9.2-3.2 22.2-4.2 35-2.4`,
		`<circle cx="39.4" cy="27.2" r="1.45" fill="currentColor" stroke="none"/>`,
		`const AUTH_TOKEN_KEY = "collie.authToken";`,
		`async function restoreSession()`,
		`window.navigator.standalone === true`,
		`display-mode: standalone`,
	} {
		if !strings.Contains(js, want) {
			t.Fatalf("web/app.js missing %q", want)
		}
	}

	if _, err := exec.LookPath("node"); err != nil {
		t.Skip("node not installed")
	}

	// Execute real app.js auth/recovery helpers in a lightweight Node harness.
	script := `
const fs = require("fs");
const vm = require("vm");
const assert = require("assert/strict");

const appSrc = fs.readFileSync(process.argv[1], "utf8");
const marker = "  boot();\n})();";
assert.ok(appSrc.includes(marker), "boot marker present");
const instrumented = appSrc.replace(
  marker,
  "  globalThis.__authTest = { authBackup, restoreSession, api, pair, login, doLogout, showLogin, isStandaloneMode, syncLoginTip, getLoginPairFailed: () => loginPairFailed };\n})();"
);

const storage = new Map();
const elements = new Map();
function makeEl(id) {
  const attrs = new Map();
  const listeners = new Map();
  const el = {
    id,
    tagName: "DIV",
    hidden: true,
    value: "",
    textContent: "",
    innerHTML: "",
    disabled: false,
    checked: false,
    className: "",
    dataset: {},
    style: { setProperty() {}, removeProperty() {} },
    classList: { add() {}, remove() {}, toggle() {}, contains() { return false; } },
    children: [],
    setAttribute(k, v) { attrs.set(k, String(v)); },
    getAttribute(k) { return attrs.has(k) ? attrs.get(k) : null; },
    removeAttribute(k) { attrs.delete(k); },
    hasAttribute(k) { return attrs.has(k); },
    addEventListener(type, fn) { (listeners.get(type) || listeners.set(type, []).get(type)).push(fn); },
    removeEventListener() {},
    querySelector(sel) {
      if (sel === "button[type='submit']") return getEl("#login-submit");
      return getEl(id + ":" + sel);
    },
    querySelectorAll() { return []; },
    append() {},
    after() {},
    remove() {},
    focus() {},
    blur() {},
    click() {},
    scrollIntoView() {},
    getBoundingClientRect() { return { top: 0, bottom: 40, left: 0, right: 300, width: 300, height: 40 }; },
    closest() { return null; },
    contains() { return false; },
    dispatch(type, ev = {}) { for (const fn of listeners.get(type) || []) fn(ev); },
  };
  return el;
}
function getEl(sel) {
  if (!elements.has(sel)) {
    const el = makeEl(sel);
    el.parentElement = makeEl(sel + ":parent");
    elements.set(sel, el);
  }
  return elements.get(sel);
}

let fetchImpl = async () => ({ ok: true, status: 200, headers: { get() { return null; } }, text: async () => "{}", json: async () => ({}) });
let reloadCount = 0;
let standaloneProp = false;
let standaloneMedia = false;

const sandbox = {
  console,
  setTimeout: (fn) => 0,
  clearTimeout() {},
  setInterval: () => 0,
  clearInterval: () => {},
  requestAnimationFrame: (fn) => { fn(); return 0; },
  cancelAnimationFrame() {},
  URL,
  URLSearchParams,
  AbortController,
  AbortSignal,
  DOMException,
  Event,
  Map,
  Set,
  Promise,
  JSON,
  Date,
  Math,
  RegExp,
  Object,
  Array,
  String,
  Number,
  Boolean,
  Error,
  encodeURIComponent,
  decodeURIComponent,
  MutationObserver: class { observe() {} disconnect() {} },
  CSS: { escape: (s) => String(s), supports: () => true },
  crypto: { randomUUID: () => "auth-test-uuid" },
  performance: { now: () => 0 },
  HTMLDialogElement: { prototype: {} },
  getComputedStyle: () => ({ getPropertyValue: () => "" }),
  innerWidth: 1440,
  innerHeight: 900,
  localStorage: {
    getItem: (k) => (storage.has(k) ? storage.get(k) : null),
    setItem: (k, v) => { storage.set(k, String(v)); },
    removeItem: (k) => { storage.delete(k); },
  },
  navigator: {
    userAgent: "Mozilla/5.0 (iPhone; CPU iPhone OS 18_0 like Mac OS X) AppleWebKit/605.1.15 Safari/604.1",
    get standalone() { return standaloneProp; },
  },
  location: { origin: "http://127.0.0.1:7788", host: "127.0.0.1:7788", hostname: "127.0.0.1", protocol: "http:", pathname: "/", search: "", hash: "", reload() { reloadCount++; } },
  history: { state: null, replaceState() {}, pushState() {} },
  document: {
    hidden: false,
    documentElement: makeEl("html"),
    body: makeEl("body"),
    querySelector: (s) => (s === "dialog[open]" ? null : getEl(s)),
    querySelectorAll: () => [],
    getElementById: (id) => getEl("#" + id),
    createElement: (tag) => makeEl(tag),
    addEventListener() {},
  },
  matchMedia: (q) => ({ matches: q === "(display-mode: standalone)" ? standaloneMedia : false, addEventListener() {} }),
  addEventListener() {},
  removeEventListener() {},
  dispatchEvent() {},
  confirm: () => true,
  fetch: (...args) => fetchImpl(...args),
  EventSource: function() { this.addEventListener = () => {}; this.close = () => {}; },
};
sandbox.window = sandbox;
sandbox.globalThis = sandbox;

vm.runInNewContext(instrumented, sandbox);
const T = sandbox.__authTest;

(async () => {
  const mkRes = (status, body) => ({
    ok: status >= 200 && status < 300,
    status,
    statusText: status === 200 ? "OK" : "ERR",
    headers: { get() { return null; } },
    text: async () => JSON.stringify(body),
    json: async () => body,
  });

  // 1. pair() saves returned browser token to localStorage.
  fetchImpl = async (url, init) => {
    assert.equal(url, "/api/pair");
    assert.equal(init.credentials, "same-origin");
    return mkRes(200, { ok: true, id: "iphone-safari", token: "dev-tok-123" });
  };
  await T.pair("ABCD2345");
  assert.equal(T.authBackup.get(), "dev-tok-123");

  // 2. api() on 401 silently calls /api/login once and retries original request without flashing #login.
  const calls = [];
  let cookieRestored = false;
  getEl("#login").hidden = true;
  getEl("#app").hidden = false;
  fetchImpl = async (url, init) => {
    calls.push(url);
    if (url === "/api/session") {
      return cookieRestored ? mkRes(200, { ok: true }) : mkRes(401, { error: "unauthorized" });
    }
    if (url === "/api/login") {
      assert.equal(JSON.parse(init.body).token, "dev-tok-123");
      cookieRestored = true;
      return mkRes(200, { ok: true });
    }
    throw new Error("unexpected url: " + url);
  };
  const sess = await T.api("/api/session");
  assert.deepEqual(sess, { ok: true });
  assert.deepEqual(calls, ["/api/session", "/api/login", "/api/session"]);
  assert.equal(getEl("#login").hidden, true, "login view must stay hidden on seamless restore");

  // 3. Revoked device (POST /api/login returns 401) clears localStorage, shows #login, no loop.
  calls.length = 0;
  fetchImpl = async (url) => {
    calls.push(url);
    return mkRes(401, { error: "unauthorized" });
  };
  await assert.rejects(() => T.api("/api/session"), /unauthorized/);
  assert.deepEqual(calls, ["/api/session", "/api/login"]);
  assert.equal(T.authBackup.get(), "", "revoked backup token must be cleared");
  assert.equal(getEl("#login").hidden, false, "login view must be shown");
  assert.equal(getEl("#login-tip").hidden, true, "tip hidden in regular browser without pair failure");

  // 4. Standalone mode reveals #login-tip on login screen.
  standaloneProp = true;
  T.showLogin();
  assert.equal(getEl("#login-tip").hidden, false, "tip shown in iOS standalone mode");
  standaloneProp = false;
  standaloneMedia = true;
  T.showLogin();
  assert.equal(getEl("#login-tip").hidden, false, "tip shown when display-mode: standalone matches");
  standaloneMedia = false;

  // 5. Pair failure also reveals #login-tip even outside standalone mode.
  T.showLogin({ pairFailed: true });
  assert.equal(getEl("#login-tip").hidden, false, "tip shown after pair failure");

  // 6. If cookies are disabled (login returns 200 but retry still gets 401), stop after 1 retry (no loop).
  T.authBackup.set("dev-tok-blocked-cookie");
  calls.length = 0;
  fetchImpl = async (url) => {
    calls.push(url);
    if (url === "/api/login") return mkRes(200, { ok: true });
    return mkRes(401, { error: "unauthorized" });
  };
  await assert.rejects(() => T.api("/api/session"), /unauthorized/);
  assert.deepEqual(calls, ["/api/session", "/api/login", "/api/session"]);

  // 7. doLogout clears backup token, calls /api/logout, and reloads.
  T.authBackup.set("dev-tok-logout");
  calls.length = 0;
  fetchImpl = async (url) => { calls.push(url); return mkRes(200, { ok: true }); };
  await T.doLogout();
  assert.equal(T.authBackup.get(), "");
  assert.deepEqual(calls, ["/api/logout"]);
  assert.equal(reloadCount, 1);
})().catch((err) => { console.error(err); process.exit(1); });
`
	cmd := exec.Command("node", "-e", script, filepath.Join("..", "..", "web", "app.js"))
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("node auth/PWA contract failed: %v\n%s", err, out)
	}
}
