package keel

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/ThallesP/keel/apps/cli/internal/config"
	"github.com/ThallesP/keel/apps/cli/internal/output"
)

// fakeAuth serves one better-auth endpoint and returns an instance pointing at it.
func fakeAuth(t *testing.T, path string, status int, body string) (*config.Instance, *map[string]string) {
	t.Helper()
	got := map[string]string{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != path || r.Method != http.MethodPost {
			t.Errorf("request %s %s, want POST %s", r.Method, r.URL.Path, path)
		}
		if r.Header.Get("Origin") != "https://keel.test" {
			t.Errorf("Origin = %q", r.Header.Get("Origin"))
		}
		json.NewDecoder(r.Body).Decode(&got)
		w.WriteHeader(status)
		w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return &config.Instance{URL: "https://keel.test", ConvexSiteURL: srv.URL}, &got
}

func codeOf(err error) string {
	var oe *output.Error
	if errors.As(err, &oe) {
		return oe.Code
	}
	return ""
}

func TestStartLogin(t *testing.T) {
	inst, sent := fakeAuth(t, "/api/auth/device/code", 200, `{"device_code":"dev","user_code":"ABCDEFGH",
		"verification_uri":"https://keel.test/device","verification_uri_complete":"https://keel.test/device?user_code=ABCDEFGH",
		"expires_in":1800,"interval":5}`)
	p, err := StartLogin(context.Background(), inst)
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

func TestPollLogin(t *testing.T) {
	for _, tc := range []struct {
		name     string
		status   int
		body     string
		token    string
		slowDown bool
		code     string
	}{
		{"approved", 200, `{"access_token":"tok","token_type":"Bearer"}`, "tok", false, ""},
		{"pending", 400, `{"error":"authorization_pending","error_description":"Authorization pending"}`, "", false, ""},
		{"slow down", 400, `{"error":"slow_down","error_description":"Polling too frequently"}`, "", true, ""},
		{"expired", 400, `{"error":"expired_token"}`, "", false, output.CodeNotAuthenticated},
		{"denied", 400, `{"error":"access_denied"}`, "", false, output.CodeNotAuthenticated},
		{"used up", 400, `{"error":"invalid_grant"}`, "", false, output.CodeNotAuthenticated},
		{"server", 500, `oops`, "", false, output.CodeServer},
	} {
		t.Run(tc.name, func(t *testing.T) {
			inst, sent := fakeAuth(t, "/api/auth/device/token", tc.status, tc.body)
			inst.Pending = &config.PendingLogin{DeviceCode: "dev"}
			token, slowDown, err := PollLogin(context.Background(), inst)
			if token != tc.token || slowDown != tc.slowDown || codeOf(err) != tc.code {
				t.Errorf("got %q, %v, %v; want %q, %v, %s", token, slowDown, err, tc.token, tc.slowDown, tc.code)
			}
			if (*sent)["device_code"] != "dev" || (*sent)["client_id"] != "keel-cli" ||
				(*sent)["grant_type"] != "urn:ietf:params:oauth:grant-type:device_code" {
				t.Errorf("sent %v", *sent)
			}
		})
	}
}
