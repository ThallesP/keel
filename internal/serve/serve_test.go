package serve

import (
	"context"
	"encoding/json"
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

func TestConfigJS(t *testing.T) {
	for _, tc := range []struct {
		cfg  app.Config
		want string
	}{
		{app.Config{SiteURL: "http://100.64.0.1", Version: "1.2.3"},
			`window.__KEEL__ = {"apiUrl":"http://100.64.0.1","version":"1.2.3"};` + "\n"},
		{app.Config{Version: "dev"}, `window.__KEEL__ = {"version":"dev"};` + "\n"},
		{app.Config{SiteURL: `http://x/</script>"`, Version: "v"},
			`window.__KEEL__ = {"apiUrl":"http://x/\u003c/script\u003e\"","version":"v"};` + "\n"},
	} {
		got := ConfigJS(tc.cfg)
		if got != tc.want {
			t.Fatalf("ConfigJS(%+v) =\n%s\nwant\n%s", tc.cfg, got, tc.want)
		}
		// What the CLI's discovery does: the JSON between the first "{" and the last "}".
		var v map[string]string
		if err := json.Unmarshal([]byte(got[strings.Index(got, "{"):strings.LastIndex(got, "}")+1]), &v); err != nil {
			t.Fatalf("CLI cannot parse %q: %v", got, err)
		}
		if v["apiUrl"] != tc.cfg.SiteURL || v["version"] != tc.cfg.Version {
			t.Fatalf("parsed %v", v)
		}
	}
}

// fakeAdapter records what serve handed it and when it was released.
type fakeAdapter struct {
	mu     sync.Mutex
	app    *app.App
	closed bool
}

func (f *fakeAdapter) build(a *app.App, _ *slog.Logger) (func() error, error) {
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
		done <- serveOn(ctx, ln, cfg, Options{Version: cfg.Version, Web: web}, slog.New(slog.NewTextHandler(io.Discard, nil)))
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

	resp, body := get("/config.js")
	if resp.StatusCode != 200 || body != ConfigJS(cfg) || resp.Header.Get("Cache-Control") != "no-store" {
		t.Fatalf("/config.js: %d %q %v", resp.StatusCode, body, resp.Header)
	}
	if resp, body = get("/api/meta"); resp.StatusCode != 200 || !strings.Contains(body, `"version":"1.2.3"`) {
		t.Fatalf("/api/meta: %d %s", resp.StatusCode, body)
	}
	for _, path := range []string{"/", "/p/acme"} { // SPA fallback
		if resp, body = get(path); resp.StatusCode != 200 || !strings.Contains(body, "<title>Keel</title>") {
			t.Fatalf("%s: %d %s", path, resp.StatusCode, body)
		}
	}
	if resp, _ = get("/assets/app.js"); !strings.Contains(resp.Header.Get("Cache-Control"), "immutable") {
		t.Fatalf("/assets: %v", resp.Header)
	}

	// The adapter ran with the jobs scheduler and the realtime publisher already in place.
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

	// /api/ws is mounted: a session that does not resolve is told it is signed out.
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

	// Graceful shutdown: returns cleanly, releases the adapter, leaves a reopenable database.
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

func TestWireAdaptersReleasesOnFailure(t *testing.T) {
	first := &fakeAdapter{}
	saved := adapters
	adapters = []adapter{
		{"first", first.build},
		{"broken", func(*app.App, *slog.Logger) (func() error, error) { return nil, io.ErrUnexpectedEOF }},
	}
	t.Cleanup(func() { adapters = saved })
	_, err := wireAdapters(app.New(app.App{}), slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err == nil || !strings.Contains(err.Error(), "broken: unexpected EOF") {
		t.Fatalf("err = %v", err)
	}
	if !first.closed {
		t.Fatal("adapter built before the failure was not released")
	}
}
