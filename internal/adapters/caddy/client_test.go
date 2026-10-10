package caddy

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/ThallesP/keel/internal/app"
)

func serveAdmin(t *testing.T, handler http.HandlerFunc) string {
	t.Helper()
	sock := filepath.Join(t.TempDir(), "admin.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	srv := &http.Server{Handler: handler}
	go srv.Serve(ln)
	t.Cleanup(func() { srv.Close() })
	return sock
}

func TestClientCalls(t *testing.T) {
	var gotBody, gotType, gotQuery string
	sock := serveAdmin(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == "GET" && r.URL.Path == "/keel/host-addrs":
			io.WriteString(w, `["203.0.113.7","2001:db8::1"]`)
		case r.Method == "GET" && r.URL.Path == "/keel/certs":
			gotQuery = r.URL.RawQuery
			io.WriteString(w, `{"a.example.com":{"state":"ok","notAfter":"2027-01-06T10:00:00Z"},"b.example.com":{"state":"failed","error":"boom"}}`)
		case r.Method == "POST" && r.URL.Path == "/config/apps":
			b, _ := io.ReadAll(r.Body)
			gotBody, gotType = string(b), r.Header.Get("Content-Type")
			if gotBody == `{"bad":true}` {
				w.WriteHeader(http.StatusBadRequest)
				io.WriteString(w, `{"error":"loading config: loading new config: layer4 app module: start: listen tcp 203.0.113.7:5432: bind: address already in use"}`)
			}
		default:
			http.Error(w, `{"error":"unknown route"}`, http.StatusNotFound)
		}
	})
	c := New(sock, "http://100.64.0.1:8080/proxy/events")
	ctx := context.Background()

	addrs, err := c.HostAddrs(ctx)
	if err != nil || !reflect.DeepEqual(addrs, []string{"203.0.113.7", "2001:db8::1"}) {
		t.Fatalf("addrs %v %v", addrs, err)
	}
	certs, err := c.Certs(ctx, []string{"a.example.com", "b.example.com", "x y&z"})
	want := map[string]app.ProxyCert{"a.example.com": {State: "ok"}, "b.example.com": {State: "failed", Error: "boom"}}
	if err != nil || !reflect.DeepEqual(certs, want) {
		t.Fatalf("certs %v %v", certs, err)
	}
	if gotQuery != "name=a.example.com&name=b.example.com&name=x+y%26z" {
		t.Fatalf("query %q", gotQuery)
	}
	if err := c.LoadApps(ctx, []byte(`{}`)); err != nil || gotBody != `{}` || gotType != "application/json" {
		t.Fatalf("load: %v %q %q", err, gotBody, gotType)
	}
	err = c.LoadApps(ctx, []byte(`{"bad":true}`))
	var rejected *app.ProxyRejected
	if !errors.As(err, &rejected) || rejected.Message != "layer4 app module: start: listen tcp 203.0.113.7:5432: bind: address already in use" {
		t.Fatalf("rejected: %#v", err)
	}
	if c.ReportURL() != "http://100.64.0.1:8080/proxy/events" {
		t.Fatal(c.ReportURL())
	}
}

func TestClientNullAddrs(t *testing.T) {
	sock := serveAdmin(t, func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, "null\n") })
	addrs, err := New(sock, "").HostAddrs(context.Background())
	if err != nil || addrs != nil {
		t.Fatalf("%v %v", addrs, err)
	}
}

func TestClientErrors(t *testing.T) {
	ctx := context.Background()
	missing := filepath.Join(t.TempDir(), "admin.sock")
	_, err := New(missing, "").HostAddrs(ctx)
	if err == nil || err.Error() != "keel-proxy is not running (no admin socket at "+missing+")" {
		t.Fatalf("missing socket: %v", err)
	}

	stale := filepath.Join(t.TempDir(), "admin.sock")
	ln, err := net.ListenUnix("unix", &net.UnixAddr{Name: stale, Net: "unix"})
	if err != nil {
		t.Fatal(err)
	}
	ln.SetUnlinkOnClose(false)
	ln.Close()
	if err := New(stale, "").LoadApps(ctx, []byte(`{}`)); err == nil || err.Error() != "keel-proxy is not running (no admin socket at "+stale+")" {
		t.Fatalf("refused: %v", err)
	}

	slow := serveAdmin(t, func(w http.ResponseWriter, r *http.Request) { time.Sleep(300 * time.Millisecond) })
	c := New(slow, "")
	c.idle = 50 * time.Millisecond
	if _, err := c.HostAddrs(ctx); err == nil || err.Error() != "keel-proxy did not answer within 0.05s" {
		t.Fatalf("timeout: %v", err)
	}

	failing := serveAdmin(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		io.WriteString(w, `{"error":"host network namespace: open /run/hostns/net: no such file or directory"}`)
	})
	if _, err := New(failing, "").HostAddrs(ctx); err == nil || err.Error() != "host network namespace: open /run/hostns/net: no such file or directory" {
		t.Fatalf("admin error: %v", err)
	}
}

func TestCaddyError(t *testing.T) {
	cases := map[string]string{
		`{"error":"loading config: loading new config: http app module: start: boom"}`: "http app module: start: boom",
		`{"error":"loading new config: loading config: x"}`:                            "x",
		`{"error":"  spaced  "}`: "spaced",
		`not json at all`:        "not json at all",
		`{"error":42}`:           `{"error":42}`,
		`{"other":"x"}`:          `{"other":"x"}`,
		`loading config: raw`:    "raw",
	}
	for in, want := range cases {
		if got := CaddyError(in); got != want {
			t.Errorf("CaddyError(%q) = %q, want %q", in, got, want)
		}
	}
}
