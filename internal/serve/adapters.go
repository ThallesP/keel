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

type adapter struct {
	name  string
	build func(a *app.App) (closer func() error, err error)
}

var adapters = []adapter{
	{"passwords", wirePasswords},
	{"swarm", wireSwarm},
	{"proxy", wireProxy},
	{"axiom", wireAxiom},
}

func wireSwarm(a *app.App) (func() error, error) {
	s, err := swarm.New()
	if err != nil {
		return nil, err
	}
	a.Swarm, a.Logs = s, s
	return s.Close, nil
}

func wireProxy(a *app.App) (func() error, error) {
	a.Proxy = caddy.New(Env("KEEL_PROXY_SOCKET", caddy.DefaultSocket), Env("KEEL_PROXY_REPORT_URL", ""))
	return nil, nil
}

func wireAxiom(a *app.App) (func() error, error) {
	a.Axiom = axiom.New()
	return nil, nil
}

func wirePasswords(a *app.App) (func() error, error) {
	a.Passwords = password.New()
	return nil, nil
}

func wireAdapters(a *app.App, log *slog.Logger) ([]func() error, error) {
	var closers []func() error
	for _, ad := range adapters {
		closer, err := ad.build(a)
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

func closeAdapters(closers []func() error, log *slog.Logger) {
	var errs []error
	for i := len(closers) - 1; i >= 0; i-- {
		errs = append(errs, closers[i]())
	}
	if err := errors.Join(errs...); err != nil {
		log.Error("keel serve: release adapters", "err", err)
	}
}
