package agent

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
)

type recordedPost struct {
	body, resync, auth, contentType string
}

type eventsServer struct {
	mu       sync.Mutex
	statuses []int
	posts    []recordedPost
}

func (s *eventsServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	s.mu.Lock()
	s.posts = append(s.posts, recordedPost{string(body), r.Header.Get("X-Keel-Resync"), r.Header.Get("Authorization"), r.Header.Get("Content-Type")})
	status := next(&s.statuses, http.StatusOK)
	s.mu.Unlock()
	if r.Method != http.MethodPost || r.URL.Path != "/worker/events" {
		status = http.StatusNotFound
	}
	w.WriteHeader(status)
}

func (s *eventsServer) all() []recordedPost {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.posts)
}

func newTestControlPlane(t *testing.T, url string) (*ControlPlane, *syncBuffer, *[]time.Duration) {
	t.Helper()
	buf := &syncBuffer{}
	cp := NewControlPlane(url, "tok", &http.Client{}, NewLogger(buf))
	var sleeps []time.Duration
	cp.sleep = func(ctx context.Context, d time.Duration) error {
		sleeps = append(sleeps, d)
		return ctx.Err()
	}
	return cp, buf, &sleeps
}

func TestFetchConfig(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/worker/config" || r.Header.Get("Authorization") != "Bearer tok" {
			w.WriteHeader(http.StatusUnauthorized)
			io.WriteString(w, "unauthorized")
			return
		}
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"sinks":[{"projectId":"p1","serviceIds":["n1","n2"],"sink":{"kind":"axiom","domain":"api.axiom.co","dataset":"keel","traces":"keel-traces","token":"xaat-1","org":"acme"},"since":1759912345678},{"projectId":"p2","serviceIds":[],"sink":{"kind":"axiom","domain":"d","dataset":"x","token":"t"}}]}`)
	}))
	defer srv.Close()

	cp, _, _ := newTestControlPlane(t, srv.URL)
	cfg, err := cp.FetchConfig(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Sinks) != 2 {
		t.Fatalf("sinks = %+v", cfg.Sinks)
	}
	s := cfg.Sinks[0]
	if !slices.Equal(s.ServiceIDs, []string{"n1", "n2"}) || s.Since != 1759912345678 {
		t.Errorf("route = %+v", s)
	}
	if s.Sink != (SinkConfig{Kind: "axiom", Domain: "api.axiom.co", Dataset: "keel", Token: "xaat-1"}) {
		t.Errorf("sink = %+v", s.Sink)
	}
	if cfg.Sinks[1].Since != 0 {
		t.Errorf("absent since = %d, want 0", cfg.Sinks[1].Since)
	}

	cp.Token = "wrong"
	if _, err := cp.FetchConfig(context.Background()); err == nil || err.Error() != "config 401" {
		t.Fatalf("err = %v, want config 401", err)
	}
}

func TestPostEventsAccepted(t *testing.T) {
	es := &eventsServer{statuses: []int{200}}
	srv := httptest.NewServer(es)
	defer srv.Close()
	cp, _, sleeps := newTestControlPlane(t, srv.URL)
	if !cp.PostEvents(context.Background(), []byte("[]"), true) {
		t.Fatal("not accepted")
	}
	if !cp.PostEvents(context.Background(), []byte(`{"Type":"node"}`), false) {
		t.Fatal("not accepted")
	}
	want := []recordedPost{
		{"[]", "1", "Bearer tok", "application/json"},
		{`{"Type":"node"}`, "", "Bearer tok", "application/json"},
	}
	if got := es.all(); !slices.Equal(got, want) {
		t.Fatalf("posts = %+v, want %+v", got, want)
	}
	if len(*sleeps) != 0 {
		t.Fatalf("slept %v", *sleeps)
	}
}

func TestPostEventsRejected(t *testing.T) {
	es := &eventsServer{statuses: []int{400}}
	srv := httptest.NewServer(es)
	defer srv.Close()
	cp, logs, sleeps := newTestControlPlane(t, srv.URL)
	body := strings.Repeat("x", 200)
	if cp.PostEvents(context.Background(), []byte(body), false) {
		t.Fatal("a 4xx was accepted")
	}
	if len(es.all()) != 1 || len(*sleeps) != 0 {
		t.Fatalf("a 4xx was retried: %d posts, sleeps %v", len(es.all()), *sleeps)
	}
	if want := `[events] rejected 400, skipping "` + strings.Repeat("x", 120) + `"`; !strings.Contains(logs.String(), want) {
		t.Fatalf("log = %s", logs.String())
	}
}

func TestPostEventsRetries(t *testing.T) {
	es := &eventsServer{statuses: []int{500, 503, 502, 500, 500, 500, 500, 500, 500, 200}}
	srv := httptest.NewServer(es)
	defer srv.Close()
	cp, logs, sleeps := newTestControlPlane(t, srv.URL)
	if !cp.PostEvents(context.Background(), []byte("{}"), false) {
		t.Fatal("not accepted")
	}
	wantSleeps := []time.Duration{5, 10, 15, 20, 25, 30, 35, 35, 35}
	for i := range wantSleeps {
		wantSleeps[i] *= time.Second
	}
	if !slices.Equal(*sleeps, wantSleeps) {
		t.Fatalf("sleeps = %v, want %v", *sleeps, wantSleeps)
	}
	posts := es.all()
	if posts[0].resync != "" {
		t.Errorf("first attempt resync = %q", posts[0].resync)
	}
	for _, p := range posts[1:] {
		if p.resync != "1" {
			t.Fatalf("a retry did not ask for a resync: %+v", posts)
		}
	}
	for _, want := range []string{"[events] post failed 500, retry in 5s", "[events] post failed 503, retry in 10s", "[events] post failed 500, retry in 35s"} {
		if !strings.Contains(logs.String(), want) {
			t.Errorf("log lacks %q:\n%s", want, logs.String())
		}
	}
}

func TestPostEventsNetworkErrorAndShutdown(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	url := srv.URL
	srv.Close()
	cp, logs, _ := newTestControlPlane(t, url)
	ctx, cancel := context.WithCancel(context.Background())
	attempts := 0
	cp.sleep = func(ctx context.Context, d time.Duration) error {
		attempts++
		if attempts == 2 {
			cancel()
		}
		return ctx.Err()
	}
	if cp.PostEvents(ctx, []byte("[]"), true) {
		t.Fatal("accepted with the control plane down")
	}
	if !strings.Contains(logs.String(), "[events] post failed (") || !strings.Contains(logs.String(), "), retry in 10s") {
		t.Fatalf("log = %s", logs.String())
	}
}
