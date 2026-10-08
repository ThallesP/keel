package http_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ThallesP/keel/internal/adapters/password"
	"github.com/ThallesP/keel/internal/adapters/sqlite"
	"github.com/ThallesP/keel/internal/app"
	"github.com/ThallesP/keel/internal/domain"
	transport "github.com/ThallesP/keel/internal/transport/http"
)

type authHTTP struct {
	t   *testing.T
	srv *httptest.Server
	app *app.App
	now int64
}

func authServe(t *testing.T, siteURL string) *authHTTP {
	t.Helper()
	store, err := sqlite.Open(context.Background(), filepath.Join(t.TempDir(), "keel.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	h := &authHTTP{t: t, now: 1_800_000_000_000}
	h.app = app.New(app.App{
		Store: store, Config: app.Config{SiteURL: siteURL},
		Passwords: &password.Hasher{Params: password.Params{Memory: 64, Time: 1, Threads: 1, SaltLen: 16, KeyLen: 32}},
		Now:       func() int64 { return h.now },
	})
	h.srv = httptest.NewServer(transport.New(h.app, transport.Options{}))
	t.Cleanup(h.srv.Close)
	return h
}

type authReq struct {
	method, path string
	body         any
	cookie       string // keel_session value
	bearer       string
	origin       string
	referer      string
}

type authResp struct {
	status int
	header http.Header
	json   map[string]any
	raw    string
}

func (h *authHTTP) do(r authReq) authResp {
	h.t.Helper()
	var body io.Reader
	if r.body != nil {
		b, _ := json.Marshal(r.body)
		body = bytes.NewReader(b)
	}
	req, err := http.NewRequest(r.method, h.srv.URL+r.path, body)
	if err != nil {
		h.t.Fatal(err)
	}
	if r.body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if r.cookie != "" {
		req.AddCookie(&http.Cookie{Name: transport.SessionCookie, Value: r.cookie})
	}
	if r.bearer != "" {
		req.Header.Set("Authorization", "Bearer "+r.bearer)
	}
	if r.origin != "" {
		req.Header.Set("Origin", r.origin)
	}
	if r.referer != "" {
		req.Header.Set("Referer", r.referer)
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		h.t.Fatal(err)
	}
	defer res.Body.Close()
	raw, _ := io.ReadAll(res.Body)
	out := authResp{status: res.StatusCode, header: res.Header, raw: string(raw)}
	_ = json.Unmarshal(raw, &out.json)
	return out
}

func (r authResp) str(path ...string) string {
	var v any = r.json
	for _, p := range path {
		m, ok := v.(map[string]any)
		if !ok {
			return ""
		}
		v = m[p]
	}
	s, _ := v.(string)
	return s
}

func (r authResp) sessionCookie() *http.Cookie {
	for _, c := range (&http.Response{Header: r.header}).Cookies() {
		if c.Name == transport.SessionCookie {
			return c
		}
	}
	return nil
}

func authExpect(t *testing.T, r authResp, status int, code, detail string) {
	t.Helper()
	if r.status != status || r.str("code") != code || r.str("detail") != detail {
		t.Fatalf("got %d %s %q (%s), want %d %s %q", r.status, r.str("code"), r.str("detail"), r.raw, status, code, detail)
	}
}

func TestAuthHTTPAccounts(t *testing.T) {
	h := authServe(t, "http://keel.test")
	origin := h.srv.URL

	me := h.do(authReq{method: "GET", path: "/api/me"})
	if me.status != 200 || me.raw != "{\"user\":null,\"organization\":null}\n" {
		t.Fatalf("signed-out me: %d %s", me.status, me.raw)
	}
	open := h.do(authReq{method: "GET", path: "/api/auth/sign-up-open"})
	if open.status != 200 || open.json["open"] != true {
		t.Fatalf("sign-up-open: %d %s", open.status, open.raw)
	}

	up := h.do(authReq{method: "POST", path: "/api/auth/sign-up", origin: origin,
		body: map[string]string{"email": "ci@example.com", "password": "correct-horse-battery", "name": "CI"}})
	if up.status != 200 || up.str("token") == "" || up.str("user", "id") == "" || up.str("user", "email") != "ci@example.com" {
		t.Fatalf("sign-up: %d %s", up.status, up.raw)
	}
	c := up.sessionCookie()
	if c == nil || c.Value != up.str("token") || !c.HttpOnly || c.SameSite != http.SameSiteLaxMode || c.Path != "/" || c.Secure ||
		c.MaxAge != int(domain.SessionTTL/1000) {
		t.Fatalf("cookie: %+v", c)
	}
	token := c.Value

	second := h.do(authReq{method: "POST", path: "/api/auth/sign-up",
		body: map[string]string{"email": "second@example.com", "password": "correct-horse-battery", "name": "Second"}})
	authExpect(t, second, 403, "FORBIDDEN", "Sign-up is by invitation. Ask a member for an invite link.")
	short := h.do(authReq{method: "POST", path: "/api/auth/sign-up",
		body: map[string]string{"email": "x@example.com", "password": "short", "name": "X"}})
	authExpect(t, short, 422, "INVALID_INPUT", "Password too short")

	me = h.do(authReq{method: "GET", path: "/api/me", cookie: token})
	if me.str("user", "email") != "ci@example.com" || me.str("organization", "slug") != "default" || me.str("organization", "role") != "owner" {
		t.Fatalf("me: %s", me.raw)
	}

	in := h.do(authReq{method: "POST", path: "/api/auth/sign-in", body: map[string]string{"email": "CI@example.com", "password": "correct-horse-battery"}})
	if in.status != 200 || in.str("token") == "" || in.sessionCookie() == nil {
		t.Fatalf("sign-in: %d %s", in.status, in.raw)
	}
	bad := h.do(authReq{method: "POST", path: "/api/auth/sign-in", body: map[string]string{"email": "ci@example.com", "password": "nope-nope"}})
	authExpect(t, bad, 401, "NOT_AUTHENTICATED", "Invalid email or password")

	// Members and invitations over HTTP.
	inv := h.do(authReq{method: "POST", path: "/api/organization/invitations", bearer: token, body: map[string]string{"email": "Guest@example.com"}})
	if inv.status != 200 || inv.str("email") != "guest@example.com" || inv.str("role") != "member" || len(inv.str("id")) != 26 {
		t.Fatalf("invite: %d %s", inv.status, inv.raw)
	}
	look := h.do(authReq{method: "GET", path: "/api/invitations/" + inv.str("id")})
	if look.str("invitation", "email") != "guest@example.com" || look.str("invitation", "organization") != "Default" {
		t.Fatalf("lookup: %s", look.raw)
	}
	missing := h.do(authReq{method: "GET", path: "/api/invitations/nope"})
	if missing.status != 200 || missing.raw != "{\"invitation\":null}\n" {
		t.Fatalf("missing lookup: %d %s", missing.status, missing.raw)
	}
	list := h.do(authReq{method: "GET", path: "/api/organization/invitations", bearer: token})
	if invs, _ := list.json["invitations"].([]any); len(invs) != 1 {
		t.Fatalf("list: %s", list.raw)
	}
	members := h.do(authReq{method: "GET", path: "/api/organization/members", bearer: token})
	if ms, _ := members.json["members"].([]any); len(ms) != 1 {
		t.Fatalf("members: %s", members.raw)
	}
	del := h.do(authReq{method: "DELETE", path: "/api/organization/invitations/" + inv.str("id"), bearer: token})
	if del.status != 204 {
		t.Fatalf("cancel: %d %s", del.status, del.raw)
	}
	del = h.do(authReq{method: "DELETE", path: "/api/organization/invitations/nope", bearer: token})
	authExpect(t, del, 404, "NOT_FOUND", "Invitation not found")
	anon := h.do(authReq{method: "GET", path: "/api/organization/members"})
	authExpect(t, anon, 401, "NOT_AUTHENTICATED", "Not authenticated")

	// Sign-out deletes the session and clears the cookie.
	out := h.do(authReq{method: "POST", path: "/api/auth/sign-out", cookie: token, origin: origin})
	cleared := out.sessionCookie()
	if out.status != 200 || out.json["success"] != true || cleared == nil || cleared.Value != "" || cleared.MaxAge >= 0 {
		t.Fatalf("sign-out: %d %s %+v", out.status, out.raw, cleared)
	}
	if me := h.do(authReq{method: "GET", path: "/api/me", cookie: token}); me.str("user", "id") != "" {
		t.Fatalf("still signed in: %s", me.raw)
	}
}

func TestAuthHTTPSecureCookieOnHTTPS(t *testing.T) {
	h := authServe(t, "https://keel.example.com")
	up := h.do(authReq{method: "POST", path: "/api/auth/sign-up",
		body: map[string]string{"email": "ci@example.com", "password": "correct-horse-battery", "name": "CI"}})
	if c := up.sessionCookie(); c == nil || !c.Secure {
		t.Fatalf("cookie on https: %+v", c)
	}
}

func TestAuthHTTPCSRF(t *testing.T) {
	h := authServe(t, "http://keel.example.com:8080")
	up := h.do(authReq{method: "POST", path: "/api/auth/sign-up",
		body: map[string]string{"email": "ci@example.com", "password": "correct-horse-battery", "name": "CI"}})
	token := up.str("token")
	invite := func(r authReq) authResp {
		r.method, r.path, r.body = "POST", "/api/organization/invitations", map[string]string{"email": "g@example.com"}
		return h.do(r)
	}

	cases := []struct {
		name   string
		req    authReq
		status int
		detail string
	}{
		{"cookie, no origin", authReq{cookie: token}, 403, "Missing or null Origin"},
		{"cookie, null origin", authReq{cookie: token, origin: "null"}, 403, "Missing or null Origin"},
		{"cookie, foreign origin", authReq{cookie: token, origin: "http://evil.example"}, 403, "Invalid origin"},
		{"cookie, foreign referer", authReq{cookie: token, referer: "http://evil.example/page"}, 403, "Invalid origin"},
		{"cookie, look-alike port", authReq{cookie: token, origin: "http://keel.example.com:9090"}, 403, "Invalid origin"},
		{"cookie, request host", authReq{cookie: token, origin: h.srv.URL}, 200, ""},
		{"cookie, request host referer", authReq{cookie: token, referer: h.srv.URL + "/p/acme"}, 200, ""},
		{"cookie, site URL host", authReq{cookie: token, origin: "http://KEEL.example.com:8080"}, 200, ""},
		{"bearer, no origin", authReq{bearer: token}, 200, ""},
		{"bearer and cookie, foreign origin", authReq{bearer: token, cookie: token, origin: "http://evil.example"}, 200, ""},
	}
	for _, c := range cases {
		r := invite(c.req)
		if r.status != c.status {
			t.Errorf("%s: %d %s, want %d", c.name, r.status, r.raw, c.status)
			continue
		}
		if c.status == 403 && (r.str("code") != "FORBIDDEN" || r.str("detail") != c.detail ||
			r.header.Get("Content-Type") != "application/problem+json") {
			t.Errorf("%s: %s", c.name, r.raw)
		}
	}
	// Safe methods need no origin.
	if r := h.do(authReq{method: "GET", path: "/api/organization/invitations", cookie: token}); r.status != 200 {
		t.Fatalf("GET with cookie: %d %s", r.status, r.raw)
	}
}

func TestAuthHTTPSessionRenewalResendsCookie(t *testing.T) {
	h := authServe(t, "http://keel.test")
	token := h.do(authReq{method: "POST", path: "/api/auth/sign-up",
		body: map[string]string{"email": "ci@example.com", "password": "correct-horse-battery", "name": "CI"}}).str("token")
	if r := h.do(authReq{method: "GET", path: "/api/me", cookie: token}); r.sessionCookie() != nil {
		t.Fatal("cookie re-sent without a renewal")
	}
	h.now += 2 * domain.SessionUpdateAge
	r := h.do(authReq{method: "GET", path: "/api/me", cookie: token})
	c := r.sessionCookie()
	if c == nil || c.Value != token || c.MaxAge != int(domain.SessionTTL/1000) || r.str("user", "email") != "ci@example.com" {
		t.Fatalf("renewal cookie: %+v %s", c, r.raw)
	}
	// Bearer sessions renew too, without a cookie.
	h.now += 2 * domain.SessionUpdateAge
	if r := h.do(authReq{method: "GET", path: "/api/me", bearer: token}); r.sessionCookie() != nil || r.str("user", "id") == "" {
		t.Fatalf("bearer renewal: %+v %s", r.sessionCookie(), r.raw)
	}
}

func TestAuthHTTPSignInRateLimit(t *testing.T) {
	h := authServe(t, "http://keel.test")
	h.do(authReq{method: "POST", path: "/api/auth/sign-up",
		body: map[string]string{"email": "ci@example.com", "password": "correct-horse-battery", "name": "CI"}})
	for i := 0; i < app.SignInAttempts; i++ {
		r := h.do(authReq{method: "POST", path: "/api/auth/sign-in", body: map[string]string{"email": "ci@example.com", "password": "wrong-password"}})
		if r.status != 401 {
			t.Fatalf("try %d: %d", i, r.status)
		}
	}
	r := h.do(authReq{method: "POST", path: "/api/auth/sign-in", body: map[string]string{"email": "ci@example.com", "password": "correct-horse-battery"}})
	authExpect(t, r, 429, "RATE_LIMITED", "Too many requests. Please try again later.")
	if r.header.Get("Retry-After") != "300" || r.header.Get("X-Retry-After") != "300" {
		t.Fatalf("retry headers: %v", r.header)
	}
}

func TestAuthHTTPDeviceLogin(t *testing.T) {
	h := authServe(t, "http://keel.test")
	token := h.do(authReq{method: "POST", path: "/api/auth/sign-up",
		body: map[string]string{"email": "ci@example.com", "password": "correct-horse-battery", "name": "CI"}}).str("token")

	bad := h.do(authReq{method: "POST", path: "/api/auth/device/code", body: map[string]string{"client_id": "other"}})
	if bad.status != 400 || bad.raw != "{\"error\":\"invalid_client\",\"error_description\":\"Invalid client ID\"}\n" {
		t.Fatalf("bad client: %d %s", bad.status, bad.raw)
	}
	code := h.do(authReq{method: "POST", path: "/api/auth/device/code", body: map[string]string{"client_id": "keel-cli"}})
	if code.status != 200 || code.header.Get("Cache-Control") != "no-store" || len(code.str("device_code")) != 40 ||
		code.json["expires_in"] != float64(1800) || code.json["interval"] != float64(5) ||
		code.str("verification_uri") != "http://keel.test/device" ||
		code.str("verification_uri_complete") != "http://keel.test/device?user_code="+code.str("user_code") {
		t.Fatalf("device code: %d %v %s", code.status, code.header, code.raw)
	}
	poll := func() authResp {
		return h.do(authReq{method: "POST", path: "/api/auth/device/token", body: map[string]string{
			"grant_type": domain.DeviceGrantType, "device_code": code.str("device_code"), "client_id": "keel-cli"}})
	}
	p := poll()
	if p.status != 400 || p.str("error") != "authorization_pending" || p.str("error_description") != "Authorization pending" ||
		!strings.HasPrefix(p.header.Get("Content-Type"), "application/json") {
		t.Fatalf("pending: %d %v %s", p.status, p.header, p.raw)
	}
	if p := poll(); p.str("error") != "slow_down" {
		t.Fatalf("slow_down: %s", p.raw)
	}

	// Approve as CI does it: bearer, look the code up (binds it), approve.
	userCode := code.str("user_code")
	noSession := h.do(authReq{method: "POST", path: "/api/auth/device/approve", body: map[string]string{"userCode": userCode}})
	if noSession.status != 401 || noSession.str("error") != "unauthorized" || noSession.str("error_description") != "Authentication required" {
		t.Fatalf("approve signed out: %d %s", noSession.status, noSession.raw)
	}
	look := h.do(authReq{method: "GET", path: "/api/auth/device?user_code=" + userCode[:4] + "-" + userCode[4:], bearer: token})
	if look.status != 200 || look.str("status") != "pending" || look.str("user_code") != userCode[:4]+"-"+userCode[4:] {
		t.Fatalf("lookup: %d %s", look.status, look.raw)
	}
	unknown := h.do(authReq{method: "GET", path: "/api/auth/device?user_code=NOPE", bearer: token})
	if unknown.status != 400 || unknown.str("error") != "invalid_request" || unknown.str("error_description") != "Invalid user code" {
		t.Fatalf("unknown code: %d %s", unknown.status, unknown.raw)
	}
	ok := h.do(authReq{method: "POST", path: "/api/auth/device/approve", bearer: token, body: map[string]string{"userCode": userCode}})
	if ok.status != 200 || ok.json["success"] != true {
		t.Fatalf("approve: %d %s", ok.status, ok.raw)
	}

	h.now += 5_000
	got := poll()
	if got.status != 200 || got.str("access_token") == "" || got.str("token_type") != "Bearer" ||
		got.json["expires_in"] != float64(domain.SessionTTL/1000) || got.header.Get("Cache-Control") != "no-store" ||
		got.header.Get("Pragma") != "no-cache" {
		t.Fatalf("token: %d %v %s", got.status, got.header, got.raw)
	}
	me := h.do(authReq{method: "GET", path: "/api/me", bearer: got.str("access_token")})
	if me.str("user", "email") != "ci@example.com" {
		t.Fatalf("CLI me: %s", me.raw)
	}
	h.now += 5_000
	if again := poll(); again.status != 400 || again.str("error") != "invalid_grant" {
		t.Fatalf("second token: %d %s", again.status, again.raw)
	}
	// CLI sign-out with its bearer, no Origin, and the `{}` body keel logout sends.
	if out := h.do(authReq{method: "POST", path: "/api/auth/sign-out", bearer: got.str("access_token"), body: map[string]string{}}); out.status != 200 {
		t.Fatalf("CLI sign-out: %d %s", out.status, out.raw)
	}
	if me := h.do(authReq{method: "GET", path: "/api/me", bearer: got.str("access_token")}); me.str("user", "id") != "" {
		t.Fatalf("CLI session survived sign-out: %s", me.raw)
	}
}

// A request that carries a bearer is authenticated by that bearer, never by the browser's
// cookie: its CSRF exemption rests on the bearer, so the ambient cookie must not be what it acts
// with.
func TestAuthHTTPBearerWinsOverCookie(t *testing.T) {
	h := authServe(t, "http://keel.test")
	owner := h.do(authReq{method: "POST", path: "/api/auth/sign-up",
		body: map[string]string{"email": "owner@example.com", "password": "correct-horse-battery", "name": "Owner"}}).str("token")
	inv := h.do(authReq{method: "POST", path: "/api/organization/invitations", bearer: owner, body: map[string]string{"email": "member@example.com"}})
	member := h.do(authReq{method: "POST", path: "/api/auth/sign-up", body: map[string]string{
		"email": "member@example.com", "password": "correct-horse-battery", "name": "Member", "invitationId": inv.str("id")}}).str("token")
	if member == "" {
		t.Fatal("member sign-up failed")
	}

	if me := h.do(authReq{method: "GET", path: "/api/me", bearer: member, cookie: owner}); me.str("user", "email") != "member@example.com" {
		t.Fatalf("me with bearer and cookie: %s", me.raw)
	}
	invite := func(r authReq) authResp {
		r.method, r.path, r.body = "POST", "/api/organization/invitations", map[string]string{"email": "g@example.com"}
		return h.do(r)
	}
	// Cross-site with the owner's cookie and the member's bearer: acts as the member (who may not
	// invite), not as the owner.
	r := invite(authReq{bearer: member, cookie: owner, origin: "http://evil.example"})
	authExpect(t, r, 403, "FORBIDDEN", "You are not allowed to invite users to this organization")
	// A made-up bearer next to a valid cookie is signed out, not the cookie's account.
	r = invite(authReq{bearer: "made-up", cookie: owner, origin: "http://evil.example"})
	authExpect(t, r, 401, "NOT_AUTHENTICATED", "Not authenticated")
	if r.sessionCookie() != nil {
		t.Fatal("a bearer request got a session cookie")
	}
}

func TestAuthHTTPRateLimitedStatus(t *testing.T) {
	if got := transport.StatusOf(domain.CodeRateLimited); got != http.StatusTooManyRequests {
		t.Fatalf("RATE_LIMITED is HTTP %d", got)
	}
}
