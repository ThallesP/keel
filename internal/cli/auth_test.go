package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ThallesP/keel/internal/cli/config"
	"github.com/ThallesP/keel/internal/cli/output"
)

type fakeInstall struct {
	*httptest.Server
	polls     []string
	me        string
	pollCount atomic.Int32
	codes     atomic.Int32
}

func newFakeInstall(t *testing.T, polls ...string) *fakeInstall {
	t.Helper()
	f := &fakeInstall{polls: polls}
	f.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/meta":
			w.Write([]byte(`{"name":"keel","version":"test","siteUrl":"` + f.URL + `"}`))
		case "/api/auth/device/token":
			var in map[string]string
			json.NewDecoder(r.Body).Decode(&in)
			if in["device_code"] != "dev" || in["client_id"] != "keel-cli" {
				t.Errorf("poll sent %v", in)
			}
			body := f.polls[min(int(f.pollCount.Add(1)), len(f.polls))-1]
			if strings.Contains(body, `"error"`) {
				w.WriteHeader(400)
			}
			w.Write([]byte(body))
		case "/api/auth/device/code":
			f.codes.Add(1)
			w.Write([]byte(`{"device_code":"dev2","user_code":"WXYZWXYZ","verification_uri":"` + f.URL + `/device",` +
				`"verification_uri_complete":"` + f.URL + `/device?user_code=WXYZWXYZ","expires_in":1800,"interval":5}`))
		case "/api/me":
			if f.me == "" {
				http.Error(w, "unavailable", http.StatusInternalServerError)
				return
			}
			w.Write([]byte(f.me))
		default:
			http.Error(w, "unavailable", http.StatusInternalServerError)
		}
	}))
	t.Cleanup(f.Close)
	return f
}

func pendingInstance(t *testing.T, expiresIn time.Duration, polls ...string) (*config.Config, *fakeInstall) {
	t.Helper()
	t.Setenv("KEEL_CONFIG_DIR", t.TempDir())
	t.Setenv("KEEL_URL", "")
	t.Setenv("KEEL_TOKEN", "")
	t.Setenv("KEEL_INSTANCE", "")
	f := newFakeInstall(t, polls...)
	cfg, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	cfg.Instances["keel.test"] = &config.Instance{
		URL: f.URL,
		Pending: &config.PendingLogin{
			DeviceCode: "dev", UserCode: "ABCDEFGH", URL: f.URL + "/device?user_code=ABCDEFGH",
			ExpiresAt: time.Now().Add(expiresIn),
		},
	}
	if err := cfg.Save(); err != nil {
		t.Fatal(err)
	}
	return cfg, f
}

func quietApp() *app {
	return &app{out: &output.Printer{Out: &bytes.Buffer{}, Err: &bytes.Buffer{}}}
}

func TestFinishLoginPending(t *testing.T) {
	cfg, _ := pendingInstance(t, time.Minute, `{"error":"authorization_pending"}`)
	inst := cfg.Instances["keel.test"]
	err := quietApp().finishLogin(context.Background(), cfg, "keel.test", inst)
	if output.CodeOf(err) != output.CodeAuthorizationPending || err.(*output.Error).ExitCode() != output.ExitAuth {
		t.Fatalf("err = %v, want AUTHORIZATION_PENDING", err)
	}
	if inst.Pending == nil || inst.Token != "" {
		t.Errorf("instance changed: %+v", inst)
	}
}

func TestFinishLoginApprovedSavesToken(t *testing.T) {
	cfg, _ := pendingInstance(t, time.Minute, `{"access_token":"tok","token_type":"Bearer","expires_in":604800}`)
	inst := cfg.Instances["keel.test"]
	if err := quietApp().finishLogin(context.Background(), cfg, "keel.test", inst); err != nil {
		t.Fatal(err)
	}
	saved, _ := config.Load()
	if s := saved.Instances["keel.test"]; s.Token != "tok" || s.Pending != nil {
		t.Errorf("saved %+v, want the token and no pending login", s)
	}
}

func TestFinishLoginSlowDownAsksAgain(t *testing.T) {
	cfg, f := pendingInstance(t, time.Minute, `{"error":"slow_down"}`, `{"access_token":"tok"}`)
	inst := cfg.Instances["keel.test"]
	if err := quietApp().finishLogin(context.Background(), cfg, "keel.test", inst); err != nil {
		t.Fatal(err)
	}
	if inst.Token != "tok" || f.pollCount.Load() != 2 {
		t.Errorf("token %q after %d polls, want tok after 2", inst.Token, f.pollCount.Load())
	}
}

func TestFinishLoginExpiredDoesNotPoll(t *testing.T) {
	cfg, f := pendingInstance(t, -time.Second, `{"access_token":"tok"}`)
	err := quietApp().finishLogin(context.Background(), cfg, "keel.test", cfg.Instances["keel.test"])
	if output.CodeOf(err) != output.CodeNotAuthenticated || f.pollCount.Load() != 0 {
		t.Errorf("err = %v after %d polls, want NOT_AUTHENTICATED without polling", err, f.pollCount.Load())
	}
}

func TestFinishLoginTokenTakenByAnotherRun(t *testing.T) {
	cfg, _ := pendingInstance(t, time.Minute, `{"error":"invalid_grant"}`)
	other, _ := config.Load()
	other.Instances["keel.test"].Token, other.Instances["keel.test"].Pending = "tok", nil
	if err := other.Save(); err != nil {
		t.Fatal(err)
	}
	inst := cfg.Instances["keel.test"]
	if err := quietApp().finishLogin(context.Background(), cfg, "keel.test", inst); err != nil {
		t.Fatal(err)
	}
	if inst.Token != "tok" || inst.Pending != nil {
		t.Errorf("instance %+v, want the other run's token", inst)
	}
}

func TestFinishLoginNothingPending(t *testing.T) {
	inst := &config.Instance{URL: "https://keel.test", Token: "tok"}
	if err := quietApp().finishLogin(context.Background(), nil, "keel.test", inst); err != nil {
		t.Fatal(err)
	}
}

func TestFinishLoginDeniedForgetsTheLink(t *testing.T) {
	cfg, f := pendingInstance(t, time.Minute, `{"error":"access_denied"}`)
	err := quietApp().finishLogin(context.Background(), cfg, "keel.test", cfg.Instances["keel.test"])
	if output.CodeOf(err) != output.CodeNotAuthenticated || !strings.Contains(err.Error(), "denied") {
		t.Fatalf("err = %v, want NOT_AUTHENTICATED saying denied", err)
	}
	again, _ := config.Load()
	if again.Instances["keel.test"].Pending != nil {
		t.Fatal("the denied link is still saved")
	}
	if err := quietApp().finishLogin(context.Background(), again, "keel.test", again.Instances["keel.test"]); err != nil || f.pollCount.Load() != 1 {
		t.Errorf("second run: err %v after %d polls, want nothing to finish after 1", err, f.pollCount.Load())
	}
}

func login(t *testing.T, args ...string) (map[string]any, error) {
	t.Helper()
	out := &bytes.Buffer{}
	a := &app{out: &output.Printer{JSON: true, Out: out, Err: &bytes.Buffer{}}}
	cmd := a.loginCmd()
	cmd.SetArgs(args)
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	err := cmd.ExecuteContext(context.Background())
	var got map[string]any
	if out.Len() > 0 {
		if jerr := json.Unmarshal(out.Bytes(), &got); jerr != nil {
			t.Fatalf("output %q: %v", out, jerr)
		}
	}
	return got, err
}

func TestLoginKeepsTokenWhenConnectFails(t *testing.T) {
	pendingInstance(t, time.Minute, `{"access_token":"tok"}`)
	if _, err := login(t); output.CodeOf(err) != output.CodeServer {
		t.Fatalf("err = %v, want the /api/me SERVER_ERROR", err)
	}
	saved, _ := config.Load()
	if s := saved.Instances["keel.test"]; s.Token != "tok" || s.Pending != nil {
		t.Errorf("saved %+v, want the token kept and the link gone", s)
	}
}

func TestLoginDeviceFlow(t *testing.T) {
	t.Setenv("KEEL_CONFIG_DIR", t.TempDir())
	t.Setenv("KEEL_URL", "")
	t.Setenv("KEEL_TOKEN", "")
	f := newFakeInstall(t, `{"access_token":"tok"}`)

	got, err := login(t, f.URL+"/p/acme")
	if err != nil {
		t.Fatal(err)
	}
	host := strings.TrimPrefix(f.URL, "http://")
	if got["status"] != "pending" || got["code"] != "WXYZ-WXYZ" || got["instance"] != host || got["url"] != f.URL ||
		got["approvalUrl"] != f.URL+"/device?user_code=WXYZWXYZ" || f.codes.Load() != 1 {
		t.Fatalf("pending result %v", got)
	}
	saved, _ := config.Load()
	if p := saved.Instances[host].Pending; p == nil || p.DeviceCode != "dev2" || saved.Current != host {
		t.Fatalf("saved %+v", saved)
	}

	saved.Instances[host].Pending.DeviceCode = "dev"
	saved.Save()
	f.me = `{"user":{"id":"u1","email":"ci@example.com","name":"CI"},"organization":null}`
	got, err = login(t)
	if err != nil {
		t.Fatal(err)
	}
	if got["status"] != "loggedIn" || got["organization"] != nil || got["user"].(map[string]any)["email"] != "ci@example.com" {
		t.Fatalf("logged in result %v", got)
	}
	saved, _ = config.Load()
	if s := saved.Instances[host]; s.Token != "tok" || s.Pending != nil || s.Email != "ci@example.com" {
		t.Errorf("saved %+v", s)
	}

	got, err = login(t)
	if err != nil || got["status"] != "loggedIn" || f.codes.Load() != 1 {
		t.Errorf("again: %v, %v (%d codes)", got, err, f.codes.Load())
	}
	f.me = `{"user":null,"organization":null}`
	got, err = login(t)
	if err != nil || got["status"] != "pending" || f.codes.Load() != 2 {
		t.Errorf("dead session: %v, %v (%d codes)", got, err, f.codes.Load())
	}
}

func TestLoginTargetIgnoresKeelToken(t *testing.T) {
	cfg, _ := pendingInstance(t, time.Minute, `{"error":"authorization_pending"}`)
	t.Setenv("KEEL_TOKEN", "from-env")
	name, inst, err := quietApp().loginTarget(quietApp().loginCmd(), cfg, nil, "")
	if err != nil {
		t.Fatal(err)
	}
	if inst != cfg.Instances[name] || inst.Token != "" || inst.Pending == nil {
		t.Errorf("got %+v, want the saved instance (no token, link pending)", inst)
	}
}

func TestConnectChecksTheSession(t *testing.T) {
	t.Setenv("KEEL_CONFIG_DIR", t.TempDir())
	f := newFakeInstall(t)
	f.me = `{"user":null,"organization":null}`
	t.Setenv("KEEL_URL", f.URL)
	t.Setenv("KEEL_TOKEN", "dead")
	_, err := quietApp().connect(context.Background())
	if oe, ok := err.(*output.Error); !ok || oe.Code != output.CodeNotAuthenticated ||
		oe.Message != "Session expired or signed out" || oe.Fix != "keel login "+f.URL {
		t.Errorf("err = %#v", err)
	}
}
