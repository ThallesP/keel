package serve

import (
	"errors"
	"fmt"
	"log/slog"

	"github.com/ThallesP/keel/internal/adapters/axiom"
	"github.com/ThallesP/keel/internal/adapters/caddy"
	"github.com/ThallesP/keel/internal/adapters/password"
	"github.com/ThallesP/keel/internal/adapters/swarm"
	"github.com/ThallesP/keel/internal/app"
)

// An adapter sets its ports on the app and returns how to release it at shutdown (nil when there
// is nothing to release). It runs after the store, jobs and realtime exist (a.Store, a.Jobs,
// a.Events are set) and before the HTTP server starts. It must not block on the external system:
// Docker, keel-proxy or Axiom being down at start is a runtime error of the calls that need them,
// not a reason to refuse to serve the dashboard.
type adapter struct {
	name  string
	build func(a *app.App, log *slog.Logger) (closer func() error, err error)
}

// adapters is every external adapter serve constructs, in order. Tests replace it.
var adapters = []adapter{
	{"passwords", wirePasswords},
	{"swarm", wireSwarm},
	{"proxy", wireProxy},
	{"axiom", wireAxiom},
}

// wireSwarm sets a.Swarm and a.Logs: the Docker client of the manager (adapters/swarm), which
// implements both app.Swarm (deploy area: apply, observe, nodes, the keel-agent service) and
// app.LogReader (observability area: `docker service logs`). DOCKER_HOST selects the socket.
func wireSwarm(a *app.App, log *slog.Logger) (func() error, error) {
	s, err := swarm.New()
	if err != nil {
		return nil, err
	}
	a.Swarm, a.Logs = s, s
	return s.Close, nil
}

// wireProxy sets a.Proxy: keel-proxy's admin API over its unix socket (adapters/caddy; the edge
// stays its own container, proxy-ingress.md §12.4 option 1). The socket path is
// KEEL_PROXY_SOCKET (default /run/keel-proxy/admin.sock); KEEL_PROXY_REPORT_URL is where the
// proxy posts certificate events (POST /proxy/events).
func wireProxy(a *app.App, log *slog.Logger) (func() error, error) {
	a.Proxy = caddy.New(Env("KEEL_PROXY_SOCKET", caddy.DefaultSocket), Env("KEEL_PROXY_REPORT_URL", ""))
	return nil, nil
}

// wireAxiom sets a.Axiom: Axiom's OAuth, dataset, query and ingest APIs (adapters/axiom), with
// a.Config.AxiomAuthURL / AxiomAPIURL as overrides (tests) and AllowLocalSinks.
func wireAxiom(a *app.App, log *slog.Logger) (func() error, error) {
	a.Axiom = axiom.New()
	return nil, nil
}

// wirePasswords sets a.Passwords: argon2id for new hashes, Better Auth's scrypt for imported ones.
func wirePasswords(a *app.App, _ *slog.Logger) (func() error, error) {
	a.Passwords = password.New()
	return nil, nil
}

// wireAdapters builds every adapter; on failure it releases the ones already built.
func wireAdapters(a *app.App, log *slog.Logger) ([]func() error, error) {
	var closers []func() error
	for _, ad := range adapters {
		closer, err := ad.build(a, log)
		if err != nil {
			closeAdapters(closers, log)
			return nil, fmt.Errorf("%s: %w", ad.name, err)
		}
		if closer != nil {
			closers = append(closers, closer)
		}
	}
	return closers, nil
}

// closeAdapters releases adapters in reverse construction order.
func closeAdapters(closers []func() error, log *slog.Logger) {
	var errs []error
	for i := len(closers) - 1; i >= 0; i-- {
		errs = append(errs, closers[i]())
	}
	if err := errors.Join(errs...); err != nil {
		log.Error("keel serve: release adapters", "err", err)
	}
}
