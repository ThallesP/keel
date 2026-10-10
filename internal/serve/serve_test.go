package serve

import (
	"context"
	"io"
	"log/slog"
	"net"
	"net/http"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"testing/fstest"
	"time"

	"github.com/coder/websocket"

	"github.com/ThallesP/keel/internal/adapters/jobs"
	"github.com/ThallesP/keel/internal/adapters/realtime"
	"github.com/ThallesP/keel/internal/adapters/sqlite"
	"github.com/ThallesP/keel/internal/app"
)

type fakeAdapter struct {
	mu     sync.Mutex
	app    *app.App
	closed bool
}

func (f *fakeAdapter) build(a *app.App) (func() error, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.app = a
	return func() error {
		f.mu.Lock()
		defer f.mu.Unlock()
		f.closed = true
		return nil
	}, nil
}

func TestServeOn(t *testing.T) {
	fake := &fakeAdapter{}
	saved := adapters
	adapters = []adapter{{"fake", fake.build}}
	t.Cleanup(func() { adapters = saved })

	dir := t.TempDir()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	base := "http://" + ln.Addr().String()
	web := fstest.MapFS{
		"index.html":    {Data: []byte("<!doctype html><title>Keel</title>")},
		"assets/app.js": {Data: []byte("console.log(1)")},
	}
	cfg := app.Config{Version: "1.2.3", SiteURL: "http://keel.test", DataDir: filepath.Join(dir, "data")}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		done <- serveOn(ctx, ln, cfg, Options{Version: cfg.Version, Web: web}, slog.New(slog.DiscardHandler))
	}()

	get := func(path string) (*http.Response, string) {
		t.Helper()
		resp, err := http.Get(base + path)
		if err != nil {
			t.Fatalf("GET %s: %v", path, err)
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		return resp, string(b)
	}

	resp, body := get("/api/meta")
	if resp.StatusCode != 200 || !strings.Contains(body, `"version":"1.2.3"`) {
		t.Fatalf("/api/meta: %d %s", resp.StatusCode, body)
	}
	for _, path := range []string{"/", "/index.html", "/p/acme", "/assets", "/assets/"} {
		resp, body = get(path)
		if resp.StatusCode != 200 || !strings.Contains(body, "<title>Keel</title>") || resp.Header.Get("Cache-Control") != "no-cache" {
			t.Fatalf("%s: %d %v %s", path, resp.StatusCode, resp.Header, body)
		}
	}
	if resp, body = get("/assets/app.js"); resp.Header.Get("Cache-Control") != "public, max-age=31536000, immutable" || body != "console.log(1)" {
		t.Fatalf("/assets: %v %s", resp.Header, body)
	}

	fake.mu.Lock()
	a := fake.app
	fake.mu.Unlock()
	if a == nil {
		t.Fatal("adapter not built")
	}
	if _, ok := a.Events.(*realtime.Server); !ok {
		t.Fatalf("app.Events = %T, want the realtime server", a.Events)
	}
	if _, ok := a.Jobs.(*jobs.Scheduler); !ok {
		t.Fatalf("app.Jobs = %T, want the jobs scheduler", a.Jobs)
	}
	ran := make(chan struct{})
	a.Jobs.After("test", 0, func(context.Context) { close(ran) })
	select {
	case <-ran:
	case <-time.After(2 * time.Second):
		t.Fatal("job did not run")
	}

	wsctx, wscancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer wscancel()
	ws, _, err := websocket.Dial(wsctx, "ws://"+ln.Addr().String()+"/api/ws", &websocket.DialOptions{
		HTTPHeader: http.Header{"Cookie": {"keel_session=nope"}, "Origin": {base}},
	})
	if err != nil {
		t.Fatalf("dial /api/ws: %v", err)
	}
	if err := ws.Write(wsctx, websocket.MessageText, []byte(`{"id":1,"connect":{}}`)); err != nil {
		t.Fatal(err)
	}
	for {
		if _, _, err = ws.Read(wsctx); err != nil {
			break
		}
	}
	if got := websocket.CloseStatus(err); got != 4501 {
		t.Fatalf("ws close %d (%v), want 4501", got, err)
	}

	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("serveOn: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("serveOn did not return after cancel")
	}
	fake.mu.Lock()
	closed := fake.closed
	fake.mu.Unlock()
	if !closed {
		t.Fatal("adapter not released at shutdown")
	}
	if _, err := http.Get(base + "/api/meta"); err == nil {
		t.Fatal("still accepting connections after shutdown")
	}
	store, err := sqlite.Open(context.Background(), filepath.Join(cfg.DataDir, "keel.db"))
	if err != nil {
		t.Fatalf("reopen database: %v", err)
	}
	store.Close()
}

func TestShutdownCancelsJobsBeforeClosingDatabase(t *testing.T) {
	if ShutdownTimeout+realtimeCloseTimeout >= 10*time.Second {
		t.Fatalf("shutdown budget %v + %v does not fit docker stop's default 10 s", ShutdownTimeout, realtimeCloseTimeout)
	}
	savedTimeout, savedGrace := ShutdownTimeout, jobsCancelGrace
	ShutdownTimeout, jobsCancelGrace = 400*time.Millisecond, 200*time.Millisecond
	fake := &fakeAdapter{}
	saved := adapters
	adapters = []adapter{{"fake", fake.build}}
	t.Cleanup(func() { adapters, ShutdownTimeout, jobsCancelGrace = saved, savedTimeout, savedGrace })

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	cfg := app.Config{Version: "dev", DataDir: t.TempDir()}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		done <- serveOn(ctx, ln, cfg, Options{}, slog.New(slog.DiscardHandler))
	}()
	waitUntil(t, "adapter built", func() bool {
		fake.mu.Lock()
		defer fake.mu.Unlock()
		return fake.app != nil
	})
	fake.mu.Lock()
	a := fake.app
	fake.mu.Unlock()

	started, wrote := make(chan struct{}), make(chan error, 1)
	a.Jobs.After("apply", 0, func(ctx context.Context) {
		close(started)
		<-ctx.Done()
		time.Sleep(50 * time.Millisecond)
		wrote <- a.Store.Write(context.WithoutCancel(ctx), func(app.Tx) error { return nil })
	})
	<-started
	begin := time.Now()
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("serveOn: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("serveOn did not return")
	}
	if took := time.Since(begin); took > ShutdownTimeout+realtimeCloseTimeout {
		t.Fatalf("shutdown took %v", took)
	}
	select {
	case err := <-wrote:
		if err != nil {
			t.Fatalf("the cancelled job could not record its outcome: %v", err)
		}
	default:
		t.Fatal("serveOn returned before the cancelled job finished")
	}
}

func waitUntil(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(2 * time.Millisecond)
	}
}

func TestWireAdaptersReleasesOnFailure(t *testing.T) {
	first := &fakeAdapter{}
	saved := adapters
	adapters = []adapter{
		{"first", first.build},
		{"broken", func(*app.App) (func() error, error) { return nil, io.ErrUnexpectedEOF }},
	}
	t.Cleanup(func() { adapters = saved })
	_, err := wireAdapters(app.New(app.App{}), slog.New(slog.DiscardHandler))
	if err == nil || !strings.Contains(err.Error(), "broken: unexpected EOF") {
		t.Fatalf("err = %v", err)
	}
	if !first.closed {
		t.Fatal("adapter built before the failure was not released")
	}
}
