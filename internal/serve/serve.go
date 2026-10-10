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

type Options struct {
	Version string
	Web     fs.FS
}

var (
	ShutdownTimeout      = 8 * time.Second
	jobsCancelGrace      = time.Second
	realtimeCloseTimeout = time.Second
)

func Env(key, def string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	return def
}

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
	a.Conns = rt

	closers, err := wireAdapters(a, log)
	if err != nil {
		stopJobs(sched, log)
		shutdownRealtime(rt, log)
		return err
	}
	defer closeAdapters(closers, log)

	handler := transport.New(a, transport.Options{Web: opts.Web, WS: rt.Handler()})
	srv := &http.Server{Handler: handler, ReadHeaderTimeout: 10 * time.Second}
	log.Info("keel serve", "listen", ln.Addr().String(), "data", cfg.DataDir, "version", cfg.Version,
		"site", cfg.SiteURL, "dashboard", opts.Web != nil)
	errc := make(chan error, 1)
	go func() { errc <- srv.Serve(ln) }()

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
	deadline := time.Now().Add(ShutdownTimeout)
	shutdown, cancel := context.WithDeadline(context.Background(), deadline)
	defer cancel()
	cancelRecover()
	if err := srv.Shutdown(shutdown); err != nil {
		log.Error("keel serve: http shutdown", "err", err)
	}
	select {
	case <-recovered:
	case <-shutdown.Done():
		log.Error("keel serve: recovery pass still running at shutdown")
	}
	jobsCtx, cancelJobs := context.WithDeadline(shutdown, deadline.Add(-jobsCancelGrace))
	if err := sched.Stop(jobsCtx); err != nil {
		log.Error("keel serve: jobs still running at shutdown, cancelled", "err", err)
		if err := sched.Wait(shutdown); err != nil {
			log.Error("keel serve: cancelled jobs did not return in time", "err", err)
		}
	}
	cancelJobs()
	shutdownRealtime(rt, log)
	return serveErr
}

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
	ctx, cancel := context.WithTimeout(context.Background(), realtimeCloseTimeout)
	defer cancel()
	if err := rt.Shutdown(ctx); err != nil {
		log.Error("keel serve: realtime shutdown", "err", err)
	}
}

func recoverPanic(log *slog.Logger, what string) {
	if r := recover(); r != nil {
		log.Error("keel serve: "+what+" panicked", "panic", r, "stack", string(debug.Stack()))
	}
}
