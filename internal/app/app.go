// Package app holds Keel's use cases. It depends on domain and on the ports declared in this
// package (ports*.go); adapters implement them and serve wires them. See docs/go/ARCHITECTURE.md.
package app

import (
	"context"
	"log/slog"
	"time"
)

// App is every use case. Fields are ports, set once by serve (or by a test).
type App struct {
	Store  Store
	Events Publisher
	Jobs   Jobs
	Config Config
	Log    *slog.Logger
	// Now is the clock (unix ms). Tests replace it.
	Now func() int64

	// Area ports (declared in ports_<area>.go).
	Swarm Swarm
	Logs  LogReader
	Proxy Proxy
	Axiom Axiom
}

// Config is the environment serve was started with (docs/go/ARCHITECTURE.md, "Env").
type Config struct {
	Version         string
	SiteURL         string // dashboard URL as users open it, no trailing slash
	WorkerToken     string // bearer for /worker/* and /proxy/events
	PublicIP        string // KEEL_PUBLIC_IP, "" when unknown
	ACMECA          string // KEEL_ACME_CA
	ACMEEmail       string // KEEL_ACME_EMAIL
	OTLPURL         string // KEEL_OTLP_URL: OTLP relay base injected into traced services
	AxiomAuthURL    string // KEEL_AXIOM_AUTH_URL override (tests)
	AxiomAPIURL     string // KEEL_AXIOM_API_URL override (tests)
	AllowLocalSinks bool   // KEEL_ALLOW_LOCAL_SINKS
}

// New fills defaults: a no-op publisher, the wall clock, slog.Default.
func New(a App) *App {
	if a.Events == nil {
		a.Events = noopPublisher{}
	}
	if a.Now == nil {
		a.Now = func() int64 { return time.Now().UnixMilli() }
	}
	if a.Log == nil {
		a.Log = slog.Default()
	}
	return &a
}

// read runs fn in a read-only transaction.
func (a *App) read(ctx context.Context, fn func(tx Tx) error) error {
	return a.Store.Read(ctx, fn)
}

// write runs fn in a write transaction and, once it commits, publishes what it changed.
func (a *App) write(ctx context.Context, fn func(tx Tx, ch *Changes) error) error {
	ch := &Changes{}
	if err := a.Store.Write(ctx, func(tx Tx) error { return fn(tx, ch) }); err != nil {
		return err
	}
	ch.publish(a.Events)
	return nil
}

type noopPublisher struct{}

func (noopPublisher) Publish(string, []string) {}
