package agent

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestConfigFromEnv(t *testing.T) {
	dir := t.TempDir()
	secret := filepath.Join(dir, "keel_worker_token")
	if err := os.WriteFile(secret, []byte("  from-secret\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	missing := filepath.Join(dir, "nope")
	tests := []struct {
		name    string
		env     map[string]string
		secret  string
		want    Config
		wantErr string
	}{
		{
			name: "defaults", secret: missing,
			env:  map[string]string{"KEEL_URL": "http://100.64.0.1:3211//", "KEEL_WORKER_TOKEN": "tok"},
			want: Config{URL: "http://100.64.0.1:3211", Token: "tok", StatePath: "/var/lib/keel-agent/state.json", ConfigPoll: 30 * time.Second, DockerSocket: "/var/run/docker.sock"},
		},
		{
			name: "overrides", secret: missing,
			env: map[string]string{"KEEL_URL": "https://keel.example", "KEEL_WORKER_TOKEN": "tok", "KEEL_STATE": "/s/state.json",
				"KEEL_CONFIG_POLL_MS": "1500", "DOCKER_SOCKET": "/run/docker.sock"},
			want: Config{URL: "https://keel.example", Token: "tok", StatePath: "/s/state.json", ConfigPoll: 1500 * time.Millisecond, DockerSocket: "/run/docker.sock"},
		},
		{
			name: "fractional poll", secret: missing,
			env:  map[string]string{"KEEL_URL": "http://x", "KEEL_WORKER_TOKEN": "tok", "KEEL_CONFIG_POLL_MS": " 250.5 "},
			want: Config{URL: "http://x", Token: "tok", StatePath: "/var/lib/keel-agent/state.json", ConfigPoll: 250500 * time.Microsecond, DockerSocket: "/var/run/docker.sock"},
		},
		{
			name: "token from the Swarm secret", secret: secret,
			env:  map[string]string{"KEEL_URL": "http://x", "KEEL_CONFIG_POLL_MS": "abc"},
			want: Config{URL: "http://x", Token: "from-secret", StatePath: "/var/lib/keel-agent/state.json", ConfigPoll: 30 * time.Second, DockerSocket: "/var/run/docker.sock"},
		},
		{
			name: "env token wins over the secret", secret: secret,
			env:  map[string]string{"KEEL_URL": "http://x", "KEEL_WORKER_TOKEN": "env", "KEEL_CONFIG_POLL_MS": "0"},
			want: Config{URL: "http://x", Token: "env", StatePath: "/var/lib/keel-agent/state.json", ConfigPoll: 30 * time.Second, DockerSocket: "/var/run/docker.sock"},
		},
		{
			name: "no url", secret: secret, env: map[string]string{"KEEL_WORKER_TOKEN": "tok"},
			wantErr: "KEEL_URL is required (the control plane's URL, e.g. http://100.64.0.1:8080)",
		},
		{
			name: "no token", secret: missing, env: map[string]string{"KEEL_URL": "http://x"},
			wantErr: "no KEEL_WORKER_TOKEN and no /run/secrets/keel_worker_token",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := configFrom(func(k string) string { return tt.env[k] }, tt.secret)
			if tt.wantErr != "" {
				if err == nil || err.Error() != tt.wantErr {
					t.Fatalf("err = %v, want %q", err, tt.wantErr)
				}
				return
			}
			if err != nil || got != tt.want {
				t.Fatalf("config = %+v, %v\nwant %+v", got, err, tt.want)
			}
		})
	}
}

type controlPlaneStub struct {
	mu      sync.Mutex
	configs int
	later   string
	events  []post
}

func (c *controlPlaneStub) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get("Authorization") != "Bearer tok" {
		w.WriteHeader(http.StatusUnauthorized)
		io.WriteString(w, "unauthorized")
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	switch {
	case r.Method == http.MethodGet && r.URL.Path == "/worker/config":
		c.configs++
		w.Header().Set("Content-Type", "application/json")
		if c.configs == 1 {
			io.WriteString(w, `{"sinks":[]}`)
			return
		}
		io.WriteString(w, c.later)
	case r.Method == http.MethodPost && r.URL.Path == "/worker/events":
		body, _ := io.ReadAll(r.Body)
		c.events = append(c.events, post{string(body), r.Header.Get("X-Keel-Resync") == "1"})
		io.WriteString(w, "ok")
	default:
		w.WriteHeader(http.StatusNotFound)
	}
}

func (c *controlPlaneStub) snapshot() (int, []post) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.configs, slices.Clone(c.events)
}

func TestAgentRun(t *testing.T) {
	as := &axiomServer{}
	axiom := httptest.NewServer(as)
	defer axiom.Close()
	cps := &controlPlaneStub{later: `{"sinks":[{"projectId":"p1","serviceIds":["n1"],"sink":{"kind":"axiom","domain":"` +
		axiom.URL + `","dataset":"keel","token":"xaat-test"},"since":1704067200000}]}`}
	cp := httptest.NewServer(cps)
	defer cp.Close()

	d := newFakeDocker()
	c := task('a', "n1", "1", "running")
	d.setContainers(c)
	d.script(c.ID, logScript{data: stamped("2024-01-01T00:00:01.5Z listening on :8080")})
	start := ev("container", "start", c.ID, c.Labels, 1704067200500000000)
	d.streams = []eventScript{{events: []Event{start}}}

	statePath := filepath.Join(t.TempDir(), "state.json")
	cfg := Config{URL: cp.URL, Token: "tok", StatePath: statePath, ConfigPoll: time.Hour}
	logs := &syncBuffer{}
	a := New(cfg, d, cp.Client(), axiom.Client(), NewLogger(logs))
	ctx, cancel := context.WithCancelCause(context.Background())
	done := make(chan error, 1)
	go func() { done <- a.Run(ctx) }()

	waitFor(t, func() bool { return len(as.requests()) == 1 })
	configs, events := cps.snapshot()
	if configs < 2 {
		t.Fatalf("config polls = %d, want an early second poll", configs)
	}
	if len(events) < 2 || events[0] != (post{"[]", true}) || events[1].resync || !strings.Contains(events[1].body, `"Action":"start"`) {
		t.Fatalf("events = %+v", events)
	}
	if !strings.Contains(as.requests()[0], `"message":"listening on :8080","stream":"stdout","service_id":"n1","service":"svc-n1","task":"task-a","replica":1,"node":"node-1","container":"aaaaaaaaaaaa"`) {
		t.Fatalf("ingest body = %s", as.requests()[0])
	}

	cancel(errors.New("terminated signal received"))
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Run = %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Run did not return after the signal")
	}
	raw, err := os.ReadFile(statePath)
	if err != nil {
		t.Fatal(err)
	}
	want := `{"eventsSince":"1704067200.500000001","logsSince":{"` + c.ID + `":"1704067201.500000001"}}`
	if string(raw) != want {
		t.Fatalf("state = %s\nwant    %s", raw, want)
	}
	out := logs.String()
	for _, w := range []string{"[worker] starting on node node-1 (box)", "[worker] SIGTERM, flushing", "[events] streaming docker events"} {
		if !strings.Contains(out, w) {
			t.Errorf("log lacks %q:\n%s", w, out)
		}
	}
}

func TestAgentRunNeedsDocker(t *testing.T) {
	d := newFakeDocker()
	d.infoErr = errors.New("dial unix /var/run/docker.sock: connect: no such file or directory")
	a := New(Config{URL: "http://127.0.0.1:1", Token: "tok", StatePath: filepath.Join(t.TempDir(), "s.json"), ConfigPoll: time.Hour},
		d, http.DefaultClient, http.DefaultClient, NewLogger(io.Discard))
	err := a.Run(context.Background())
	if err == nil || !strings.Contains(err.Error(), "docker /info: dial unix") {
		t.Fatalf("Run = %v", err)
	}
}

func TestSignalName(t *testing.T) {
	for cause, want := range map[string]string{"interrupt signal received": "SIGINT", "terminated signal received": "SIGTERM", "": "SIGTERM"} {
		ctx, cancel := context.WithCancelCause(context.Background())
		cancel(errors.New(cause))
		if got := signalName(ctx); got != want {
			t.Errorf("signalName(%q) = %s, want %s", cause, got, want)
		}
	}
}
