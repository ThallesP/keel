// Package serve wires `keel serve`: config from the environment, adapters, the app, the HTTP
// server, background jobs. Nothing else constructs adapters.
package serve

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/ThallesP/keel/internal/adapters/sqlite"
	"github.com/ThallesP/keel/internal/app"
	transport "github.com/ThallesP/keel/internal/transport/http"
)

// Options are what main passes in; everything else comes from the environment.
type Options struct {
	Version string
	Web     fs.FS // the embedded dashboard, nil in dev builds
}

// Env reads a variable with a default.
func Env(key, def string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	return def
}

// ConfigFromEnv is app.Config from the environment (docs/go/ARCHITECTURE.md, "Env").
func ConfigFromEnv(version string) app.Config {
	return app.Config{
		Version:         version,
		SiteURL:         strings.TrimRight(Env("KEEL_SITE_URL", ""), "/"),
		WorkerToken:     Env("KEEL_WORKER_TOKEN", ""),
		PublicIP:        Env("KEEL_PUBLIC_IP", ""),
		ACMECA:          Env("KEEL_ACME_CA", ""),
		ACMEEmail:       Env("KEEL_ACME_EMAIL", ""),
		OTLPURL:         strings.TrimRight(Env("KEEL_OTLP_URL", ""), "/"),
		AxiomAuthURL:    Env("KEEL_AXIOM_AUTH_URL", ""),
		AxiomAPIURL:     Env("KEEL_AXIOM_API_URL", ""),
		AllowLocalSinks: Env("KEEL_ALLOW_LOCAL_SINKS", "") == "1",
		DataDir:         Env("KEEL_DATA_DIR", "/data"),
		AgentImage:      Env("KEEL_AGENT_IMAGE", ""),
		AgentControlURL: strings.TrimRight(Env("KEEL_AGENT_CONTROL_URL", ""), "/"),
	}
}

// Run serves until ctx is cancelled.
func Run(ctx context.Context, opts Options) error {
	log := slog.New(slog.NewTextHandler(os.Stderr, nil))
	slog.SetDefault(log)
	cfg := ConfigFromEnv(opts.Version)

	dataDir := cfg.DataDir
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		return err
	}
	store, err := sqlite.Open(ctx, filepath.Join(dataDir, "keel.db"))
	if err != nil {
		return err
	}
	defer store.Close()

	a := app.New(app.App{Store: store, Config: cfg, Log: log})
	handler := transport.New(a, transport.Options{Web: opts.Web, ConfigJS: "window.__KEEL_CONFIG__ = {};\n"})

	listen := Env("KEEL_LISTEN", ":8080")
	ln, err := net.Listen("tcp", listen)
	if err != nil {
		return err
	}
	srv := &http.Server{Handler: handler, ReadHeaderTimeout: 10 * time.Second}
	log.Info("keel serve", "listen", ln.Addr().String(), "data", dataDir, "version", opts.Version)
	errc := make(chan error, 1)
	go func() { errc <- srv.Serve(ln) }()
	select {
	case <-ctx.Done():
		shutdown, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		return srv.Shutdown(shutdown)
	case err := <-errc:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return fmt.Errorf("serve: %w", err)
	}
}
