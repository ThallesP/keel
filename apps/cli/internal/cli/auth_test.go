package cli

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ThallesP/keel/apps/cli/internal/config"
	"github.com/ThallesP/keel/apps/cli/internal/output"
)

// pendingInstance saves an instance with a pending login in a fresh config dir and returns the
// loaded config and the number of polls so far. Poll n gets bodies[n], the last one after that;
// every other endpoint answers 500.
func pendingInstance(t *testing.T, expiresIn time.Duration, bodies ...string) (*config.Config, *atomic.Int32) {
	t.Helper()
	t.Setenv("KEEL_CONFIG_DIR", t.TempDir())
	var polls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/auth/device/token" {
			http.Error(w, "unavailable", http.StatusInternalServerError) // the rest is down
			return
		}
		body := bodies[min(int(polls.Add(1)), len(bodies))-1]
		if bytes.Contains([]byte(body), []byte(`"error"`)) {
			w.WriteHeader(400)
		}
		w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	cfg, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	cfg.SetInstance("keel.test", &config.Instance{
		URL:           "https://keel.test",
		ConvexURL:     srv.URL,
		ConvexSiteURL: srv.URL,
		Pending: &config.PendingLogin{
			DeviceCode: "dev", UserCode: "ABCDEFGH", URL: "https://keel.test/device?user_code=ABCDEFGH",
			ExpiresAt: time.Now().Add(expiresIn), Interval: 0, // no waiting in tests
		},
	})
	if err := cfg.Save(); err != nil {
		t.Fatal(err)
	}
	return cfg, &polls
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
	cfg, _ := pendingInstance(t, time.Minute, `{"access_token":"tok"}`)
	inst := cfg.Instances["keel.test"]
	if err := quietApp().finishLogin(context.Background(), cfg, "keel.test", inst); err != nil {
		t.Fatal(err)
	}
	saved, _ := config.Load()
	if s := saved.Instances["keel.test"]; s.Token != "tok" || s.Pending != nil {
		t.Errorf("saved %+v, want the token and no pending login", s)
	}
}

// Polled moments ago by another run: wait the interval and ask again rather than report pending.
func TestFinishLoginSlowDownAsksAgain(t *testing.T) {
	cfg, polls := pendingInstance(t, time.Minute, `{"error":"slow_down"}`, `{"access_token":"tok"}`)
	inst := cfg.Instances["keel.test"]
	if err := quietApp().finishLogin(context.Background(), cfg, "keel.test", inst); err != nil {
		t.Fatal(err)
	}
	if inst.Token != "tok" || polls.Load() != 2 {
		t.Errorf("token %q after %d polls, want tok after 2", inst.Token, polls.Load())
	}
}

func TestFinishLoginExpiredDoesNotPoll(t *testing.T) {
	cfg, polls := pendingInstance(t, -time.Second, `{"access_token":"tok"}`)
	err := quietApp().finishLogin(context.Background(), cfg, "keel.test", cfg.Instances["keel.test"])
	if output.CodeOf(err) != output.CodeNotAuthenticated || polls.Load() != 0 {
		t.Errorf("err = %v after %d polls, want NOT_AUTHENTICATED without polling", err, polls.Load())
	}
}

// Two runs right after the approval: the token goes to the first poll, the other one is told
// the code is used up and picks the token up from the config instead.
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

// The install has dropped a turned-down code: say so once, then forget it rather than poll a code
// that is gone ("used up") on every later run.
func TestFinishLoginDeniedForgetsTheLink(t *testing.T) {
	cfg, polls := pendingInstance(t, time.Minute, `{"error":"access_denied"}`)
	err := quietApp().finishLogin(context.Background(), cfg, "keel.test", cfg.Instances["keel.test"])
	if output.CodeOf(err) != output.CodeNotAuthenticated || !strings.Contains(err.Error(), "denied") {
		t.Fatalf("err = %v, want NOT_AUTHENTICATED saying denied", err)
	}
	again, _ := config.Load()
	if again.Instances["keel.test"].Pending != nil {
		t.Fatal("the denied link is still saved")
	}
	if err := quietApp().finishLogin(context.Background(), again, "keel.test", again.Instances["keel.test"]); err != nil || polls.Load() != 1 {
		t.Errorf("second run: err %v after %d polls, want nothing to finish after 1", err, polls.Load())
	}
}

// The token is handed out once, so keel login saves it before anything after the poll can fail.
func TestLoginKeepsTokenWhenConnectFails(t *testing.T) {
	_, _ = pendingInstance(t, time.Minute, `{"access_token":"tok"}`)
	cmd := quietApp().loginCmd()
	cmd.SetArgs([]string{})
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	if err := cmd.ExecuteContext(context.Background()); output.CodeOf(err) != output.CodeServer {
		t.Fatalf("err = %v, want the convex/token SERVER_ERROR", err)
	}
	saved, _ := config.Load()
	if s := saved.Instances["keel.test"]; s.Token != "tok" || s.Pending != nil {
		t.Errorf("saved %+v, want the token kept and the link gone", s)
	}
}

// keel login without a URL works on the saved install, never on a copy carrying KEEL_TOKEN.
func TestLoginTargetIgnoresKeelToken(t *testing.T) {
	cfg, _ := pendingInstance(t, time.Minute, `{"error":"authorization_pending"}`)
	t.Setenv("KEEL_TOKEN", "from-env")
	cmd := quietApp().loginCmd()
	cmd.SetContext(context.Background())
	name, inst, err := quietApp().loginTarget(cmd, cfg, nil, "", "", "")
	if err != nil {
		t.Fatal(err)
	}
	if inst != cfg.Instances[name] || inst.Token != "" || inst.Pending == nil {
		t.Errorf("got %+v, want the saved instance (no token, link pending)", inst)
	}
}
