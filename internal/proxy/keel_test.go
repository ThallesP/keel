//go:build linux

package proxy

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/caddyserver/caddy/v2"
	"go.uber.org/zap"
)

func newEvent(t *testing.T, name string, data map[string]any) caddy.Event {
	t.Helper()
	e, err := caddy.NewEvent(caddy.Context{Context: context.Background()}, name, data)
	if err != nil {
		t.Fatal(err)
	}
	return e
}

type reports struct {
	mu   sync.Mutex
	got  []certEvent
	auth []string
	ch   chan struct{}
}

func reportServer(t *testing.T, status int) (*reports, *Reporter) {
	t.Helper()
	rs := &reports{ch: make(chan struct{}, 16)}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var e certEvent
		_ = json.NewDecoder(r.Body).Decode(&e)
		rs.mu.Lock()
		rs.got = append(rs.got, e)
		rs.auth = append(rs.auth, r.Header.Get("Authorization")+" "+r.Header.Get("Content-Type"))
		rs.mu.Unlock()
		w.WriteHeader(status)
		rs.ch <- struct{}{}
	}))
	t.Cleanup(srv.Close)
	return rs, &Reporter{URL: srv.URL + "/proxy/events", Token: "tok", client: srv.Client(), logger: zap.NewNop()}
}

func (rs *reports) wait(t *testing.T, n int) []certEvent {
	t.Helper()
	for range n {
		select {
		case <-rs.ch:
		case <-time.After(5 * time.Second):
			t.Fatal("no report")
		}
	}
	rs.mu.Lock()
	defer rs.mu.Unlock()
	return append([]certEvent(nil), rs.got...)
}

func TestReporterHandle(t *testing.T) {
	rs, r := reportServer(t, http.StatusOK)
	const name = "api-16w41g.203-0-113-7.sslip.io"

	for _, e := range []caddy.Event{
		newEvent(t, "cert_failed", map[string]any{"error": "x"}),
		newEvent(t, "cert_obtaining", map[string]any{"identifier": name}),
		newEvent(t, "cert_failed", map[string]any{"identifier": name, "error": errors.New("obtain: context canceled")}),
	} {
		if err := r.Handle(context.Background(), e); err != nil {
			t.Fatal(err)
		}
	}
	if got := certOf(name); got.State != "pending" {
		t.Fatalf("after ignored events: %+v", got)
	}

	failure := errors.New("HTTP 400 urn:ietf:params:acme:error:dns -\n  DNS problem:   NXDOMAIN")
	if err := r.Handle(context.Background(), newEvent(t, "cert_failed", map[string]any{"identifier": name, "error": failure})); err != nil {
		t.Fatal(err)
	}
	got := rs.wait(t, 1)
	want := certEvent{Event: "cert_failed", Name: name, Error: "HTTP 400 urn:ietf:params:acme:error:dns - DNS problem: NXDOMAIN"}
	if got[0] != want {
		t.Fatalf("report %+v", got[0])
	}
	if rs.auth[0] != "Bearer tok application/json" {
		t.Fatalf("headers %q", rs.auth[0])
	}
	if c := certOf(name); c.State != "failed" || c.Error != want.Error {
		t.Fatalf("certOf after failure: %+v", c)
	}

	if err := r.Handle(context.Background(), newEvent(t, "cert_obtained", map[string]any{"identifier": name})); err != nil {
		t.Fatal(err)
	}
	got = rs.wait(t, 1)
	if got[1] != (certEvent{Event: "cert_obtained", Name: name}) {
		t.Fatalf("report %+v", got[1])
	}
	if c := certOf(name); c.State != "pending" {
		t.Fatalf("failure not cleared: %+v", c)
	}

	quiet := &Reporter{logger: zap.NewNop()}
	if err := quiet.Handle(context.Background(), newEvent(t, "cert_failed", map[string]any{"identifier": "q.example.com", "error": "boom"})); err != nil {
		t.Fatal(err)
	}
	if c := certOf("q.example.com"); c.State != "failed" || c.Error != "boom" {
		t.Fatalf("quiet: %+v", c)
	}
}

func TestReporterRetries(t *testing.T) {
	old := reportBackoff
	reportBackoff = func(int) time.Duration { return time.Millisecond }
	t.Cleanup(func() { reportBackoff = old })
	rs, r := reportServer(t, http.StatusUnauthorized)
	r.post(certEvent{Event: "cert_obtained", Name: "x.example.com"})
	if got := rs.wait(t, 4); len(got) != 4 {
		t.Fatalf("attempts: %d", len(got))
	}
}

func TestAdminRoutes(t *testing.T) {
	failuresMu.Lock()
	failures["bad.example.com"] = "boom"
	failuresMu.Unlock()
	t.Cleanup(func() {
		failuresMu.Lock()
		delete(failures, "bad.example.com")
		failuresMu.Unlock()
	})
	rec := httptest.NewRecorder()
	if err := serveCerts(rec, httptest.NewRequest("GET", "/keel/certs?name=bad.example.com&name=new.example.com", nil)); err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(rec.Body)
	if strings.TrimSpace(string(body)) != `{"bad.example.com":{"state":"failed","error":"boom"},"new.example.com":{"state":"pending"}}` {
		t.Fatalf("certs: %s", body)
	}
	for _, h := range []caddy.AdminHandlerFunc{serveCerts, serveHostAddrs} {
		err := h(httptest.NewRecorder(), httptest.NewRequest("POST", "/keel/x", nil))
		var api caddy.APIError
		if !errors.As(err, &api) || api.HTTPStatus != http.StatusMethodNotAllowed {
			t.Fatalf("POST: %v", err)
		}
	}
}

func TestErrorText(t *testing.T) {
	cases := []struct {
		in   any
		want string
	}{
		{errors.New(" a\n b  c "), "a b c"},
		{"x\ty", "x y"},
		{42, "42"},
		{nil, "<nil>"},
	}
	for _, c := range cases {
		if got := errorText(c.in); got != c.want {
			t.Errorf("errorText(%v) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestBaseWithSocket(t *testing.T) {
	b, err := baseWithSocket("")
	if err != nil || strings.TrimSpace(string(b)) != `{
  "admin": { "listen": "unix//run/keel-proxy/admin.sock|0600" }
}` {
		t.Fatalf("default: %s %v", b, err)
	}
	b, err = baseWithSocket("/tmp/k/admin.sock")
	if err != nil || string(b) != `{"admin":{"listen":"unix//tmp/k/admin.sock|0600"}}` {
		t.Fatalf("custom: %s %v", b, err)
	}
}
