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
	"github.com/ThallesP/keel/internal/api"
	"github.com/ThallesP/keel/internal/app"
	"github.com/ThallesP/keel/internal/domain"
	transport "github.com/ThallesP/keel/internal/transport/http"
)

type authHTTP struct {
	t   *testing.T
	srv *httptest.Server
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
	a := app.New(app.App{
		Store: store, Config: app.Config{SiteURL: siteURL},
		Passwords: &password.Hasher{Params: password.Params{Memory: 64, Time: 1, Threads: 1, SaltLen: 16, KeyLen: 32}},
		Now:       func() int64 { return h.now },
	})
	h.srv = httptest.NewServer(transport.New(a, transport.Options{}))
	t.Cleanup(h.srv.Close)
	return h
}

type authReq struct {
	method, path string
	body         map[string]string
	cookie       string
	bearer       string
	origin       string
	referer      string
}

type authResp struct {
	status int
	header http.Header
	raw    string
}

type authMe struct {
	User         api.User         `json:"user"`
	Organization api.Organization `json:"organization"`
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
	return authResp{status: res.StatusCode, header: res.Header, raw: string(raw)}
}

func (h *authHTTP) signUp(email, invitationID string) authResp {
	return h.do(authReq{method: "POST", path: "/api/auth/sign-up", body: map[string]string{
		"email": email, "password": "correct-horse-battery", "name": "CI", "invitationId": invitationID}})
}

func (h *authHTTP) invite(r authReq) authResp {
	r.method, r.path, r.body = "POST", "/api/organization/invitations", map[string]string{"email": "g@example.com"}
	return h.do(r)
}

func authBody[T any](t *testing.T, r authResp) T {
	t.Helper()
	var v T
	if err := json.Unmarshal([]byte(r.raw), &v); err != nil {
		t.Fatalf("%d %s: %v", r.status, r.raw, err)
	}
	return v
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
	if p := authBody[api.Problem](t, r); r.status != status || p.Code != code || p.Detail != detail {
		t.Fatalf("got %d %s, want %d %s %q", r.status, r.raw, status, code, detail)
	}
}

func authExpectDevice(t *testing.T, r authResp, status int, code, description string) {
	t.Helper()
	if e := authBody[api.DeviceError](t, r); r.status != status || e != (api.DeviceError{Error: code, ErrorDescription: description}) {
		t.Fatalf("got %d %s, want %d %s %q", r.status, r.raw, status, code, description)
	}
}

func TestAuthHTTPAccounts(t *testing.T) {
	h := authServe(t, "http://keel.test")

	me := h.do(authReq{method: "GET", path: "/api/me"})
	if me.status != 200 || me.raw != "{\"user\":null,\"organization\":null}\n" {
		t.Fatalf("signed-out me: %d %s", me.status, me.raw)
	}
	open := h.do(authReq{method: "GET", path: "/api/auth/sign-up-open"})
	if open.status != 200 || !authBody[api.SignUpOpen](t, open).Open {
		t.Fatalf("sign-up-open: %d %s", open.status, open.raw)
	}

	up := h.signUp("ci@example.com", "")
	signedIn := authBody[api.SignedIn](t, up)
	if up.status != 200 || signedIn.Token == "" || signedIn.User.ID == "" || signedIn.User.Email != "ci@example.com" {
		t.Fatalf("sign-up: %d %s", up.status, up.raw)
	}
	c := up.sessionCookie()
	if c == nil || c.Value != signedIn.Token || !c.HttpOnly || c.SameSite != http.SameSiteLaxMode || c.Path != "/" || c.Secure ||
		c.MaxAge != int(domain.SessionTTL/1000) {
		t.Fatalf("cookie: %+v", c)
	}
	token := c.Value

	authExpect(t, h.signUp("second@example.com", ""), 403, "FORBIDDEN", "Sign-up is by invitation. Ask a member for an invite link.")
	short := h.do(authReq{method: "POST", path: "/api/auth/sign-up",
		body: map[string]string{"email": "x@example.com", "password": "short", "name": "X"}})
	authExpect(t, short, 422, "INVALID_INPUT", "Password too short")

	me = h.do(authReq{method: "GET", path: "/api/me", cookie: token})
	if m := authBody[authMe](t, me); m.User.Email != "ci@example.com" || m.Organization.Slug != "default" || m.Organization.Role != "owner" {
		t.Fatalf("me: %s", me.raw)
	}

	in := h.do(authReq{method: "POST", path: "/api/auth/sign-in", body: map[string]string{"email": "CI@example.com", "password": "correct-horse-battery"}})
	if in.status != 200 || authBody[api.SignedIn](t, in).Token == "" || in.sessionCookie() == nil {
		t.Fatalf("sign-in: %d %s", in.status, in.raw)
	}
	bad := h.do(authReq{method: "POST", path: "/api/auth/sign-in", body: map[string]string{"email": "ci@example.com", "password": "nope-nope"}})
	authExpect(t, bad, 401, "NOT_AUTHENTICATED", "Invalid email or password")

	created := h.do(authReq{method: "POST", path: "/api/organization/invitations", bearer: token, body: map[string]string{"email": "Guest@example.com"}})
	inv := authBody[api.CreatedInvitation](t, created)
	if created.status != 200 || inv.Email != "guest@example.com" || inv.Role != "member" || len(inv.ID) != 26 {
		t.Fatalf("invite: %d %s", created.status, created.raw)
	}
	look := h.do(authReq{method: "GET", path: "/api/invitations/" + inv.ID})
	if l := authBody[api.InvitationLookup](t, look).Invitation; l == nil || *l != (api.PublicInvitation{Email: "guest@example.com", Organization: "Default"}) {
		t.Fatalf("lookup: %s", look.raw)
	}
	missing := h.do(authReq{method: "GET", path: "/api/invitations/nope"})
	if missing.status != 200 || missing.raw != "{\"invitation\":null}\n" {
		t.Fatalf("missing lookup: %d %s", missing.status, missing.raw)
	}
	list := h.do(authReq{method: "GET", path: "/api/organization/invitations", bearer: token})
	if len(authBody[api.Invitations](t, list).Invitations) != 1 {
		t.Fatalf("list: %s", list.raw)
	}
	members := h.do(authReq{method: "GET", path: "/api/organization/members", bearer: token})
	if len(authBody[api.Members](t, members).Members) != 1 {
		t.Fatalf("members: %s", members.raw)
	}
	del := h.do(authReq{method: "DELETE", path: "/api/organization/invitations/" + inv.ID, bearer: token})
	if del.status != 204 {
		t.Fatalf("cancel: %d %s", del.status, del.raw)
	}
	del = h.do(authReq{method: "DELETE", path: "/api/organization/invitations/nope", bearer: token})
	authExpect(t, del, 404, "NOT_FOUND", "Invitation not found")
	anon := h.do(authReq{method: "GET", path: "/api/organization/members"})
	authExpect(t, anon, 401, "NOT_AUTHENTICATED", "Not authenticated")

	out := h.do(authReq{method: "POST", path: "/api/auth/sign-out", cookie: token, origin: h.srv.URL})
	cleared := out.sessionCookie()
	if out.status != 200 || !authBody[api.AuthSuccess](t, out).Success || cleared == nil || cleared.Value != "" || cleared.MaxAge >= 0 {
		t.Fatalf("sign-out: %d %s %+v", out.status, out.raw, cleared)
	}
	if me := h.do(authReq{method: "GET", path: "/api/me", cookie: token}); authBody[authMe](t, me).User.ID != "" {
		t.Fatalf("still signed in: %s", me.raw)
	}
}

func TestAuthHTTPSecureCookieOnHTTPS(t *testing.T) {
	h := authServe(t, "https://keel.example.com")
	if c := h.signUp("ci@example.com", "").sessionCookie(); c == nil || !c.Secure {
		t.Fatalf("cookie on https: %+v", c)
	}
}

func TestAuthHTTPCSRF(t *testing.T) {
	h := authServe(t, "http://keel.example.com:8080")
	token := authBody[api.SignedIn](t, h.signUp("ci@example.com", "")).Token

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
		r := h.invite(c.req)
		p := authBody[api.Problem](t, r)
		if r.status != c.status || p.Detail != c.detail {
			t.Errorf("%s: %d %s, want %d %q", c.name, r.status, r.raw, c.status, c.detail)
			continue
		}
		if c.status == 403 && (p.Code != "FORBIDDEN" || r.header.Get("Content-Type") != "application/problem+json") {
			t.Errorf("%s: %v %s", c.name, r.header, r.raw)
		}
	}
	if r := h.do(authReq{method: "GET", path: "/api/organization/invitations", cookie: token}); r.status != 200 {
		t.Fatalf("GET with cookie: %d %s", r.status, r.raw)
	}
}

func TestAuthHTTPSessionRenewalResendsCookie(t *testing.T) {
	h := authServe(t, "http://keel.test")
	token := authBody[api.SignedIn](t, h.signUp("ci@example.com", "")).Token
	if r := h.do(authReq{method: "GET", path: "/api/me", cookie: token}); r.sessionCookie() != nil {
		t.Fatal("cookie re-sent without a renewal")
	}
	h.now += 2 * domain.SessionUpdateAge
	r := h.do(authReq{method: "GET", path: "/api/me", cookie: token})
	c := r.sessionCookie()
	if c == nil || c.Value != token || c.MaxAge != int(domain.SessionTTL/1000) || authBody[authMe](t, r).User.Email != "ci@example.com" {
		t.Fatalf("renewal cookie: %+v %s", c, r.raw)
	}
	h.now += 2 * domain.SessionUpdateAge
	if r := h.do(authReq{method: "GET", path: "/api/me", bearer: token}); r.sessionCookie() != nil || authBody[authMe](t, r).User.ID == "" {
		t.Fatalf("bearer renewal: %+v %s", r.sessionCookie(), r.raw)
	}
}

func TestAuthHTTPSignInRateLimit(t *testing.T) {
	h := authServe(t, "http://keel.test")
	h.signUp("ci@example.com", "")
	for i := range app.SignInAttempts {
		r := h.do(authReq{method: "POST", path: "/api/auth/sign-in", body: map[string]string{"email": "ci@example.com", "password": "wrong-password"}})
		if r.status != 401 {
			t.Fatalf("try %d: %d", i, r.status)
		}
	}
	r := h.do(authReq{method: "POST", path: "/api/auth/sign-in", body: map[string]string{"email": "ci@example.com", "password": "correct-horse-battery"}})
	authExpect(t, r, 429, "RATE_LIMITED", "Too many requests. Please try again later.")
	if r.header.Get("Retry-After") != "300" {
		t.Fatalf("retry headers: %v", r.header)
	}
}

func TestAuthHTTPDeviceLogin(t *testing.T) {
	h := authServe(t, "http://keel.test")
	token := authBody[api.SignedIn](t, h.signUp("ci@example.com", "")).Token

	bad := h.do(authReq{method: "POST", path: "/api/auth/device/code", body: map[string]string{"client_id": "other"}})
	if bad.status != 400 || bad.raw != "{\"error\":\"invalid_client\",\"error_description\":\"Invalid client ID\"}\n" {
		t.Fatalf("bad client: %d %s", bad.status, bad.raw)
	}
	started := h.do(authReq{method: "POST", path: "/api/auth/device/code", body: map[string]string{"client_id": "keel-cli"}})
	code := authBody[api.DeviceCode](t, started)
	if started.status != 200 || started.header.Get("Cache-Control") != "no-store" || len(code.DeviceCode) != 40 ||
		code.ExpiresIn != 1800 || code.Interval != 5 || code.VerificationURI != "http://keel.test/device" ||
		code.VerificationURIComplete != "http://keel.test/device?user_code="+code.UserCode {
		t.Fatalf("device code: %d %v %s", started.status, started.header, started.raw)
	}
	poll := func() authResp {
		return h.do(authReq{method: "POST", path: "/api/auth/device/token", body: map[string]string{
			"grant_type": domain.DeviceGrantType, "device_code": code.DeviceCode, "client_id": "keel-cli"}})
	}
	p := poll()
	authExpectDevice(t, p, 400, "authorization_pending", "Authorization pending")
	if !strings.HasPrefix(p.header.Get("Content-Type"), "application/json") {
		t.Fatalf("pending content type: %v", p.header)
	}
	authExpectDevice(t, poll(), 400, "slow_down", "Polling too frequently")

	approve := authReq{method: "POST", path: "/api/auth/device/approve", body: map[string]string{"userCode": code.UserCode}}
	authExpectDevice(t, h.do(approve), 401, "unauthorized", "Authentication required")
	pretty := code.UserCode[:4] + "-" + code.UserCode[4:]
	look := h.do(authReq{method: "GET", path: "/api/auth/device?user_code=" + pretty, bearer: token})
	if s := authBody[api.DeviceStatus](t, look); look.status != 200 || s != (api.DeviceStatus{UserCode: pretty, Status: "pending"}) {
		t.Fatalf("lookup: %d %s", look.status, look.raw)
	}
	unknown := h.do(authReq{method: "GET", path: "/api/auth/device?user_code=NOPE", bearer: token})
	authExpectDevice(t, unknown, 400, "invalid_request", "Invalid user code")
	approve.bearer = token
	if ok := h.do(approve); ok.status != 200 || !authBody[api.AuthSuccess](t, ok).Success {
		t.Fatalf("approve: %d %s", ok.status, ok.raw)
	}

	h.now += 5_000
	got := poll()
	cli := authBody[api.DeviceToken](t, got)
	if got.status != 200 || cli.AccessToken == "" || cli.TokenType != "Bearer" || cli.ExpiresIn != domain.SessionTTL/1000 ||
		got.header.Get("Cache-Control") != "no-store" || got.header.Get("Pragma") != "no-cache" {
		t.Fatalf("token: %d %v %s", got.status, got.header, got.raw)
	}
	if me := h.do(authReq{method: "GET", path: "/api/me", bearer: cli.AccessToken}); authBody[authMe](t, me).User.Email != "ci@example.com" {
		t.Fatalf("CLI me: %s", me.raw)
	}
	h.now += 5_000
	authExpectDevice(t, poll(), 400, "invalid_grant", "Invalid device code")
	if out := h.do(authReq{method: "POST", path: "/api/auth/sign-out", bearer: cli.AccessToken, body: map[string]string{}}); out.status != 200 {
		t.Fatalf("CLI sign-out: %d %s", out.status, out.raw)
	}
	if me := h.do(authReq{method: "GET", path: "/api/me", bearer: cli.AccessToken}); authBody[authMe](t, me).User.ID != "" {
		t.Fatalf("CLI session survived sign-out: %s", me.raw)
	}
}

func TestAuthHTTPBearerWinsOverCookie(t *testing.T) {
	h := authServe(t, "http://keel.test")
	owner := authBody[api.SignedIn](t, h.signUp("owner@example.com", "")).Token
	inv := h.do(authReq{method: "POST", path: "/api/organization/invitations", bearer: owner, body: map[string]string{"email": "member@example.com"}})
	member := authBody[api.SignedIn](t, h.signUp("member@example.com", authBody[api.CreatedInvitation](t, inv).ID)).Token
	if member == "" {
		t.Fatal("member sign-up failed")
	}

	if me := h.do(authReq{method: "GET", path: "/api/me", bearer: member, cookie: owner}); authBody[authMe](t, me).User.Email != "member@example.com" {
		t.Fatalf("me with bearer and cookie: %s", me.raw)
	}
	r := h.invite(authReq{bearer: member, cookie: owner, origin: "http://evil.example"})
	authExpect(t, r, 403, "FORBIDDEN", "You are not allowed to invite users to this organization")
	r = h.invite(authReq{bearer: "made-up", cookie: owner, origin: "http://evil.example"})
	authExpect(t, r, 401, "NOT_AUTHENTICATED", "Not authenticated")
	if r.sessionCookie() != nil {
		t.Fatal("a bearer request got a session cookie")
	}
}
