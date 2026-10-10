//go:build linux

package proxy

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/caddyserver/caddy/v2"

	keelcaddy "github.com/ThallesP/keel/internal/adapters/caddy"
	"github.com/ThallesP/keel/internal/app"
)

func TestRunServesAdminAndResumes(t *testing.T) {
	useAutosave(t)
	sock := filepath.Join(t.TempDir(), "admin.sock")

	stop := startEdge(t, Options{Socket: sock, Resume: true})
	client := keelcaddy.New(sock, "")
	ctx := context.Background()

	certs, err := client.Certs(ctx, []string{"a.example.com"})
	if err != nil || certs["a.example.com"].State != "pending" {
		t.Fatalf("certs before any TLS app: %v %v", certs, err)
	}
	if _, err := client.HostAddrs(ctx); err == nil || !strings.Contains(err.Error(), "host network namespace") {
		t.Fatalf("host addrs outside a container: %v", err)
	}

	port := freePort(t)
	if err := client.LoadApps(ctx, l4Apps(port, echoServer(t))); err != nil {
		t.Fatal(err)
	}
	echoThrough(t, port)

	err = client.LoadApps(ctx, []byte(`{"layer4":{"servers":{"tcp-1":{"listen":["host-tcp/127.0.0.1:1"],"routes":[]}}}}`))
	var rejected *app.ProxyRejected
	if !errors.As(err, &rejected) || !strings.Contains(rejected.Message, "host network namespace") || strings.HasPrefix(rejected.Message, "loading") {
		t.Fatalf("host-tcp load: %#v", err)
	}
	echoThrough(t, port)

	stop()
	if b, err := os.ReadFile(caddy.ConfigAutosavePath); err != nil || !strings.Contains(string(b), "layer4") {
		t.Fatalf("autosave: %s %v", b, err)
	}

	stop = startEdge(t, Options{Socket: sock, Resume: true})
	echoThrough(t, port)
	if err := client.LoadApps(ctx, []byte(`{}`)); err != nil {
		t.Fatal(err)
	}
	stop()
}

func TestRunResumeFallsBackWhenTheLastConfigNoLongerLoads(t *testing.T) {
	useAutosave(t)
	sock := filepath.Join(t.TempDir(), "admin.sock")
	stale := fmt.Sprintf(`{"admin":{"listen":"unix/%s|0600"},"apps":{"layer4":{"servers":{"tcp-5432":{"listen":["tcp/192.0.2.1:5432"],"routes":[]}}}}}`, sock)
	if err := os.WriteFile(caddy.ConfigAutosavePath, []byte(stale), 0o600); err != nil {
		t.Fatal(err)
	}

	stop := startEdge(t, Options{Socket: sock, Resume: true})
	client := keelcaddy.New(sock, "")
	port := freePort(t)
	apps := l4Apps(port, echoServer(t))
	for deadline := time.Now().Add(5 * time.Second); ; time.Sleep(20 * time.Millisecond) {
		err := client.LoadApps(context.Background(), apps)
		if err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal(err)
		}
	}
	echoThrough(t, port)
	stop()
}

func useAutosave(t *testing.T) {
	old := caddy.ConfigAutosavePath
	caddy.ConfigAutosavePath = filepath.Join(t.TempDir(), "autosave.json")
	t.Cleanup(func() { caddy.ConfigAutosavePath = old })
}

func l4Apps(port int, upstream string) []byte {
	return fmt.Appendf(nil, `{"layer4":{"servers":{"tcp-%d":{"listen":["tcp/127.0.0.1:%d"],"routes":[{"handle":[{"handler":"proxy","upstreams":[{"dial":["%s"]}]}]}]}}}}`, port, port, upstream)
}

func startEdge(t *testing.T, opts Options) (stop func()) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- Run(ctx, opts) }()
	deadline := time.Now().Add(10 * time.Second)
	for {
		if conn, err := net.Dial("unix", opts.Socket); err == nil {
			conn.Close()
			break
		}
		select {
		case err := <-done:
			t.Fatalf("edge exited: %v", err)
		default:
		}
		if time.Now().After(deadline) {
			t.Fatal("admin socket never came up")
		}
		time.Sleep(20 * time.Millisecond)
	}
	stop = sync.OnceFunc(func() {
		cancel()
		if err := <-done; err != nil {
			t.Errorf("stop: %v", err)
		}
	})
	t.Cleanup(stop)
	return stop
}

func echoServer(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go func() { defer conn.Close(); io.Copy(conn, conn) }()
		}
	}()
	return ln.Addr().String()
}

func freePort(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	return ln.Addr().(*net.TCPAddr).Port
}

func echoThrough(t *testing.T, port int) {
	t.Helper()
	var conn net.Conn
	var err error
	for deadline := time.Now().Add(5 * time.Second); ; time.Sleep(20 * time.Millisecond) {
		if conn, err = net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", port), 2*time.Second); err == nil || time.Now().After(deadline) {
			break
		}
	}
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
	if _, err := conn.Write([]byte("ping")); err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 4)
	if _, err := io.ReadFull(conn, buf); err != nil || string(buf) != "ping" {
		t.Fatalf("echo through the proxy: %q %v", buf, err)
	}
}
