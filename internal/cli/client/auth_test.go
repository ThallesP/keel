package client

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/ThallesP/keel/internal/cli/output"
)

func fakeDevice(t *testing.T, path string, status int, body string) (*Client, *map[string]string) {
	t.Helper()
	got := map[string]string{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != path || r.Method != http.MethodPost {
			t.Errorf("request %s %s, want POST %s", r.Method, r.URL.Path, path)
		}
		if r.Header.Get("Authorization") != "" {
			t.Errorf("device request carries %q; it must not", r.Header.Get("Authorization"))
		}
		json.NewDecoder(r.Body).Decode(&got)
		w.WriteHeader(status)
		w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return New(srv.URL, ""), &got
}

func TestStartLogin(t *testing.T) {
	c, sent := fakeDevice(t, "/api/auth/device/code", 200, `{"device_code":"dev","user_code":"ABCDEFGH",
		"verification_uri":"https://keel.test/device","verification_uri_complete":"https://keel.test/device?user_code=ABCDEFGH",
		"expires_in":1800,"interval":5}`)
	p, err := c.StartLogin(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if (*sent)["client_id"] != "keel-cli" {
		t.Errorf("client_id = %q", (*sent)["client_id"])
	}
	if p.DeviceCode != "dev" || p.UserCode != "ABCDEFGH" || p.Interval != 5 ||
		p.URL != "https://keel.test/device?user_code=ABCDEFGH" {
		t.Errorf("pending = %+v", p)
	}
	if d := time.Until(p.ExpiresAt); d < 29*time.Minute || d > 30*time.Minute {
		t.Errorf("expires in %v, want ~30m", d)
	}
}

func TestStartLoginRefused(t *testing.T) {
	c, _ := fakeDevice(t, "/api/auth/device/code", 400, `{"error":"invalid_client","error_description":"Invalid client ID"}`)
	_, err := c.StartLogin(context.Background())
	if output.CodeOf(err) != output.CodeServer || err.Error() != "/api/auth/device/code: Invalid client ID" {
		t.Errorf("err = %v", err)
	}
}

func TestPollLogin(t *testing.T) {
	for _, tc := range []struct {
		name     string
		status   int
		body     string
		token    string
		slowDown bool
		code     string
		fix      string
	}{
		{"approved", 200, `{"access_token":"tok","token_type":"Bearer","expires_in":604800}`, "tok", false, "", ""},
		{"pending", 400, `{"error":"authorization_pending","error_description":"Authorization pending"}`, "", false, "", ""},
		{"slow down", 400, `{"error":"slow_down","error_description":"Polling too frequently"}`, "", true, "", ""},
		{"expired", 400, `{"error":"expired_token","error_description":"Device code has expired"}`, "", false, output.CodeNotAuthenticated, ""},
		{"denied", 400, `{"error":"access_denied","error_description":"Access denied"}`, "", false, output.CodeNotAuthenticated, ""},
		{"used up", 400, `{"error":"invalid_grant","error_description":"Invalid device code"}`, "", false, output.CodeNotAuthenticated, ""},
		{"user gone", 500, `{"error":"server_error","error_description":"User not found"}`, "", false, output.CodeServer, ""},
		{"rate limited", 429, `{"status":429,"title":"Too Many Requests","detail":"Too many requests. Please try again later.","code":"RATE_LIMITED"}`,
			"", false, output.CodeRateLimited, "Retry in 30s"},
		{"down", 502, `<html>bad gateway</html>`, "", false, output.CodeServer, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c, sent := fakeDevice(t, "/api/auth/device/token", tc.status, tc.body)
			if tc.status == 429 {
				c.HTTP = &http.Client{Transport: retryAfter{"30"}}
			}
			token, slowDown, err := c.PollLogin(context.Background(), "dev")
			if token != tc.token || slowDown != tc.slowDown || output.CodeOf(err) != tc.code {
				t.Errorf("got %q, %v, %v; want %q, %v, %s", token, slowDown, err, tc.token, tc.slowDown, tc.code)
			}
			if tc.fix != "" && err.(*output.Error).Fix != tc.fix {
				t.Errorf("fix = %q, want %q", err.(*output.Error).Fix, tc.fix)
			}
			if (*sent)["device_code"] != "dev" || (*sent)["client_id"] != "keel-cli" ||
				(*sent)["grant_type"] != "urn:ietf:params:oauth:grant-type:device_code" {
				t.Errorf("sent %v", *sent)
			}
		})
	}
}

type retryAfter struct{ seconds string }

func (r retryAfter) RoundTrip(req *http.Request) (*http.Response, error) {
	resp, err := http.DefaultTransport.RoundTrip(req)
	if err == nil {
		resp.Header.Set("Retry-After", r.seconds)
	}
	return resp, err
}

func TestMe(t *testing.T) {
	var auth string
	body := `{"user":{"id":"u1","email":"me@example.com","name":"Me"},"organization":{"id":"o1","name":"Default","slug":"default","role":"owner"}}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth = r.Header.Get("Authorization")
		w.Write([]byte(body))
	}))
	defer srv.Close()

	user, org, err := New(srv.URL, "tok").Me(context.Background())
	if err != nil || user.Email != "me@example.com" || org.Role != "owner" || auth != "Bearer tok" {
		t.Fatalf("me = %+v, %+v, %v (auth %q)", user, org, err, auth)
	}
	body = `{"user":null,"organization":null}`
	_, _, err = New(srv.URL, "dead").Me(context.Background())
	if oe, ok := err.(*output.Error); !ok || oe.Code != output.CodeNotAuthenticated ||
		oe.Message != "Session expired or signed out" || oe.Fix != "keel login "+srv.URL {
		t.Errorf("signed out: %#v", err)
	}
	if _, _, err := New(srv.URL, "").Me(context.Background()); output.CodeOf(err) != output.CodeNotAuthenticated {
		t.Errorf("no token: %v", err)
	}
}

func TestMeNotKeel(t *testing.T) {
	for _, tc := range []struct {
		name string
		mux  map[string]string
		code string
		msg  string
	}{
		{"older keel", map[string]string{
			"/api/me":    `<!doctype html><html></html>`,
			"/api/meta":  `<!doctype html><html></html>`,
			"/config.js": `window.__KEEL__ = {"convexUrl":"http://100.64.0.1:3210","convexSiteUrl":"http://100.64.0.1:3211"};`,
		}, output.CodeDiscoveryFailed, "runs an older Keel"},
		{"something else", map[string]string{}, output.CodeDiscoveryFailed, "doesn't look like a Keel dashboard"},
		{"keel failing", map[string]string{
			"/api/me":   `{"user":`,
			"/api/meta": `{"name":"keel","version":"1.2.3","siteUrl":"https://keel.test"}`,
		}, output.CodeServer, "unexpected response from GET /api/me"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				body, ok := tc.mux[r.URL.Path]
				if !ok {
					http.NotFound(w, r)
					return
				}
				w.Write([]byte(body))
			}))
			defer srv.Close()
			_, _, err := New(srv.URL, "tok").Me(context.Background())
			if output.CodeOf(err) != tc.code || err == nil || !strings.Contains(err.Error(), tc.msg) {
				t.Errorf("err = %v, want %s %q", err, tc.code, tc.msg)
			}
		})
	}
}

func TestDiscover(t *testing.T) {
	for _, tc := range []struct {
		name string
		mux  map[string]string
		code string
		msg  string
	}{
		{"keel", map[string]string{"/api/meta": `{"name":"keel","version":"1.2.3","siteUrl":"https://keel.test"}`}, "", ""},
		{"older keel", map[string]string{
			"/api/meta":  `<!doctype html><html></html>`,
			"/config.js": `window.__KEEL__ = {"convexUrl":"http://100.64.0.1:3210","convexSiteUrl":"http://100.64.0.1:3211"};`,
		}, output.CodeDiscoveryFailed, "runs an older Keel"},
		{"not keel", map[string]string{"/api/meta": `{"hello":"world"}`}, output.CodeDiscoveryFailed, "doesn't look like a Keel dashboard"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				body, ok := tc.mux[r.URL.Path]
				if !ok {
					http.NotFound(w, r)
					return
				}
				w.Write([]byte(body))
			}))
			defer srv.Close()
			m, err := Discover(context.Background(), srv.URL)
			if output.CodeOf(err) != tc.code || (err != nil && !strings.Contains(err.Error(), tc.msg)) {
				t.Fatalf("err = %v, want %s %q", err, tc.code, tc.msg)
			}
			if err == nil && (m.Version != "1.2.3" || m.SiteURL != "https://keel.test") {
				t.Errorf("meta = %+v", m)
			}
		})
	}
	srv := httptest.NewServer(http.NotFoundHandler())
	url := srv.URL
	srv.Close()
	if _, err := Discover(context.Background(), url); output.CodeOf(err) != output.CodeNetwork {
		t.Errorf("closed port: %v", err)
	}
}
