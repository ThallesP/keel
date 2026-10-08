// Package serve wires `keel serve`: config from the environment, adapters, the app, the HTTP
// server, background jobs. Nothing else constructs adapters.
//
// Files: serve.go (config, Run, start-up and shutdown order), adapters.go (one function per
// external adapter: Swarm, proxy, Axiom), configjs.go (GET /config.js).
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
	"runtime/debug"
	"strings"
	"time"

	"github.com/ThallesP/keel/internal/adapters/jobs"
	"github.com/ThallesP/keel/internal/adapters/realtime"
	"github.com/ThallesP/keel/internal/adapters/sqlite"
	"github.com/ThallesP/keel/internal/app"
	"github.com/ThallesP/keel/internal/domain"
	transport "github.com/ThallesP/keel/internal/transport/http"
)

// Options are what main passes in; everything else comes from the environment.
type Options struct {
	Version string
	Web     fs.FS // the embedded dashboard, nil in dev builds
}

// ShutdownTimeout bounds the whole graceful shutdown (compose gives the container 30 s).
const ShutdownTimeout = 20 * time.Second

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
		SiteURL:         strings.TrimRight(Env("KEEL_SITE_URL", Env("SITE_URL", "")), "/"),
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

// Run serves until ctx is cancelled (SIGINT/SIGTERM), then shuts down gracefully.
func Run(ctx context.Context, opts Options) error {
	log := slog.New(slog.NewTextHandler(os.Stderr, nil))
	slog.SetDefault(log)
	cfg := ConfigFromEnv(opts.Version)
	ln, err := net.Listen("tcp", Env("KEEL_LISTEN", ":8080"))
	if err != nil {
		return err
	}
	return serveOn(ctx, ln, cfg, opts, log)
}

// serveOn runs the control plane on ln until ctx is done. Start-up order: database, jobs,
// realtime, external adapters, HTTP, then the recovery pass in the background. Shutdown order:
// stop accepting HTTP (in-flight requests finish), wait for the recovery pass, stop jobs (running
// ones finish), close WebSockets, release adapters, close the database.
func serveOn(ctx context.Context, ln net.Listener, cfg app.Config, opts Options, log *slog.Logger) (err error) {
	defer ln.Close()
	if err := os.MkdirAll(cfg.DataDir, 0o700); err != nil {
		return err
	}
	store, err := sqlite.Open(ctx, filepath.Join(cfg.DataDir, "keel.db"))
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, store.Close()) }()

	sched := jobs.New(log)
	a := app.New(app.App{Store: store, Config: cfg, Log: log, Jobs: sched})

	rt, err := realtime.New(realtime.Config{Authenticate: authenticator(a), SiteURL: cfg.SiteURL, Log: log})
	if err != nil {
		stopJobs(sched, log)
		return fmt.Errorf("realtime: %w", err)
	}
	a.Events = rt
	// TODO(integration, auth): sign-out must close the session's sockets and a membership change
	// must move the user's sockets to the new organization channel. rt.DisconnectSession(id) and
	// rt.DisconnectUser(id) do that; set them here on the port the auth area declares for it.

	closers, err := wireAdapters(a, log)
	if err != nil {
		stopJobs(sched, log)
		shutdownRealtime(rt, log)
		return err
	}
	defer closeAdapters(closers, log)

	handler := transport.New(a, transport.Options{Web: opts.Web, WS: rt.Handler(), ConfigJS: ConfigJS(cfg)})
	srv := &http.Server{Handler: handler, ReadHeaderTimeout: 10 * time.Second}
	log.Info("keel serve", "listen", ln.Addr().String(), "data", cfg.DataDir, "version", cfg.Version,
		"site", cfg.SiteURL, "dashboard", opts.Web != nil)
	errc := make(chan error, 1)
	go func() { errc <- srv.Serve(ln) }()

	// The recovery pass replaces durable scheduling (observe everything, re-arm timeouts, proxy
	// sync, data migrations). It runs once the listener is up so the API answers meanwhile.
	recoverCtx, cancelRecover := context.WithCancel(context.Background())
	recovered := make(chan struct{})
	go func() {
		defer close(recovered)
		defer recoverPanic(log, "recovery pass")
		a.Recover(recoverCtx)
	}()

	var serveErr error
	select {
	case <-ctx.Done():
	case serveErr = <-errc:
		if errors.Is(serveErr, http.ErrServerClosed) {
			serveErr = nil
		} else {
			serveErr = fmt.Errorf("serve: %w", serveErr)
		}
	}

	log.Info("keel serve: shutting down")
	shutdown, cancel := context.WithTimeout(context.Background(), ShutdownTimeout)
	defer cancel()
	cancelRecover() // winds down while HTTP drains
	// 1. Stop accepting; in-flight requests finish (WebSockets are hijacked: realtime closes them).
	if err := srv.Shutdown(shutdown); err != nil {
		log.Error("keel serve: http shutdown", "err", err)
	}
	// 2. The recovery pass writes to the database and schedules jobs: let it end first.
	select {
	case <-recovered:
	case <-shutdown.Done():
		log.Error("keel serve: recovery pass still running at shutdown")
	}
	// 3. Jobs: pending ones are dropped (the next start's recovery pass redoes what matters),
	// running ones get the rest of the shutdown budget.
	if err := sched.Stop(shutdown); err != nil {
		log.Error("keel serve: jobs still running at shutdown", "err", err)
	}
	// 4. WebSockets: clients reconnect to the next process and refetch everything.
	shutdownRealtime(rt, log)
	// 5. Adapters, then 6. the database (deferred above, in that order).
	return serveErr
}

// authenticator resolves the WebSocket upgrade request's session (cookie, else bearer) the same
// way the HTTP middleware does.
func authenticator(a *app.App) func(r *http.Request) (domain.Actor, error) {
	return func(r *http.Request) (domain.Actor, error) {
		token := transport.SessionToken(r)
		if token == "" {
			return domain.Actor{}, nil
		}
		return a.ResolveSession(r.Context(), token)
	}
}

func stopJobs(s *jobs.Scheduler, log *slog.Logger) {
	ctx, cancel := context.WithTimeout(context.Background(), ShutdownTimeout)
	defer cancel()
	if err := s.Stop(ctx); err != nil {
		log.Error("keel serve: stop jobs", "err", err)
	}
}

func shutdownRealtime(rt *realtime.Server, log *slog.Logger) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := rt.Shutdown(ctx); err != nil {
		log.Error("keel serve: realtime shutdown", "err", err)
	}
}

// recoverPanic logs a panic in a background goroutine instead of crashing the control plane.
func recoverPanic(log *slog.Logger, what string) {
	if r := recover(); r != nil {
		log.Error("keel serve: "+what+" panicked", "panic", r, "stack", string(debug.Stack()))
	}
}
