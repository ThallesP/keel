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

// axiomServer records ingest requests and answers with the given statuses in order (the last
// repeats).
type axiomServer struct {
	mu       sync.Mutex
	statuses []int
	reply    string
	bodies   []string
	paths    []string
	headers  []http.Header
}

func (s *axiomServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	s.mu.Lock()
	s.bodies = append(s.bodies, string(body))
	s.paths = append(s.paths, r.URL.EscapedPath())
	s.headers = append(s.headers, r.Header.Clone())
	status := 200
	if len(s.statuses) > 0 {
		status = s.statuses[0]
		if len(s.statuses) > 1 {
			s.statuses = s.statuses[1:]
		}
	}
	s.mu.Unlock()
	w.WriteHeader(status)
	io.WriteString(w, s.reply)
}

func (s *axiomServer) requests() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.bodies)
}

func testEvents() []LogEvent {
	return []LogEvent{
		{Time: "2024-01-01T00:00:00.000000001Z", Message: "hello", Stream: "stdout", ServiceID: "n1", Service: "svc-n1", Task: "t1", Replica: 1, Node: "node1", Container: "abcdef123456"},
		{Time: "2024-01-01T00:00:01Z", Message: `say "hi" <b>`, Stream: "stderr", ServiceID: "n1", Service: "svc-n1", Task: "t1", Replica: 1, Node: "node1", Container: "abcdef123456"},
	}
}

func newTestAxiom(t *testing.T, srv *httptest.Server) (*AxiomSink, *syncBuffer, *[]time.Duration) {
	t.Helper()
	buf := &syncBuffer{}
	s := NewAxiomSink(SinkConfig{Kind: "axiom", Domain: srv.URL, Dataset: "keel logs", Token: "xaat-secret"}, srv.Client(), NewLogger(buf))
	var sleeps []time.Duration
	s.sleep = func(ctx context.Context, d time.Duration) error {
		sleeps = append(sleeps, d)
		return ctx.Err()
	}
	return s, buf, &sleeps
}

func TestAxiomSendNDJSON(t *testing.T) {
	as := &axiomServer{}
	srv := httptest.NewServer(as)
	defer srv.Close()
	sink, _, sleeps := newTestAxiom(t, srv)
	if !sink.Send(context.Background(), testEvents()) {
		t.Fatal("not delivered")
	}
	want := `{"_time":"2024-01-01T00:00:00.000000001Z","message":"hello","stream":"stdout","service_id":"n1","service":"svc-n1","task":"t1","replica":1,"node":"node1","container":"abcdef123456"}` + "\n" +
		`{"_time":"2024-01-01T00:00:01Z","message":"say \"hi\" <b>","stream":"stderr","service_id":"n1","service":"svc-n1","task":"t1","replica":1,"node":"node1","container":"abcdef123456"}`
	if got := as.requests(); len(got) != 1 || got[0] != want {
		t.Fatalf("body =\n%v\nwant\n%s", got, want)
	}
	if as.paths[0] != "/v1/datasets/keel%20logs/ingest" {
		t.Errorf("path = %s", as.paths[0])
	}
	if h := as.headers[0]; h.Get("Authorization") != "Bearer xaat-secret" || h.Get("Content-Type") != "application/x-ndjson" {
		t.Errorf("headers = %v", h)
	}
	if len(*sleeps) != 0 {
		t.Errorf("slept %v", *sleeps)
	}
	if sink.Key() != "axiom:"+srv.URL+":keel logs:secret" {
		t.Errorf("key = %q", sink.Key())
	}
}

// A 4xx other than 429 is malformed: retrying cannot help, the batch is dropped (true).
func TestAxiomRejectDrops(t *testing.T) {
	as := &axiomServer{statuses: []int{400}, reply: `{"message":"bad"}`}
	srv := httptest.NewServer(as)
	defer srv.Close()
	sink, logs, sleeps := newTestAxiom(t, srv)
	if !sink.Send(context.Background(), testEvents()) {
		t.Fatal("a 400 must report the batch as handled")
	}
	if len(as.requests()) != 1 || len(*sleeps) != 0 {
		t.Fatalf("retried a 400")
	}
	if want := `[axiom] rejected 400, dropping 2 events {"text":"{\"message\":\"bad\"}"}`; !strings.Contains(logs.String(), want) {
		t.Fatalf("log = %s", logs.String())
	}
}

func TestAxiomRetriesThenGivesUp(t *testing.T) {
	as := &axiomServer{statuses: []int{429, 500}, reply: strings.Repeat("e", 300)}
	srv := httptest.NewServer(as)
	defer srv.Close()
	sink, logs, sleeps := newTestAxiom(t, srv)
	if sink.Send(context.Background(), testEvents()) {
		t.Fatal("delivered while Axiom fails")
	}
	if n := len(as.requests()); n != 5 {
		t.Fatalf("attempts = %d, want 5", n)
	}
	want := []time.Duration{time.Second, 2 * time.Second, 4 * time.Second, 8 * time.Second, 16 * time.Second}
	if !slices.Equal(*sleeps, want) {
		t.Fatalf("sleeps = %v, want %v", *sleeps, want)
	}
	out := logs.String()
	for _, w := range []string{
		`[axiom] ingest 429, retry 1 {"text":"` + strings.Repeat("e", 200) + `"}`,
		`[axiom] ingest 500, retry 5`,
		`[axiom] unreachable, keeping 2 events for a later attempt`,
	} {
		if !strings.Contains(out, w) {
			t.Errorf("log lacks %q", w)
		}
	}
}

func TestAxiomRecoversAfterRetry(t *testing.T) {
	as := &axiomServer{statuses: []int{503, 200}}
	srv := httptest.NewServer(as)
	defer srv.Close()
	sink, _, sleeps := newTestAxiom(t, srv)
	if !sink.Send(context.Background(), testEvents()) {
		t.Fatal("not delivered")
	}
	if !slices.Equal(*sleeps, []time.Duration{time.Second}) {
		t.Fatalf("sleeps = %v", *sleeps)
	}
}

func TestAxiomNetworkError(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	sink, logs, _ := newTestAxiom(t, srv)
	srv.Close()
	if sink.Send(context.Background(), testEvents()) {
		t.Fatal("delivered with Axiom down")
	}
	if !strings.Contains(logs.String(), "[axiom] ingest failed (") || !strings.Contains(logs.String(), "), retry 5") {
		t.Fatalf("log = %s", logs.String())
	}
}

func TestSinkFactory(t *testing.T) {
	f := NewSinkFactory(http.DefaultClient, NewLogger(io.Discard))
	if s, ok := f(SinkConfig{Kind: "axiom", Domain: "api.axiom.co", Dataset: "d", Token: "t"}); !ok || s.Key() != "axiom:api.axiom.co:d:t" {
		t.Fatalf("axiom sink = %v, %v", s, ok)
	}
	if _, ok := f(SinkConfig{Kind: "clickhouse"}); ok {
		t.Fatal("unknown kind built a sink")
	}
}
