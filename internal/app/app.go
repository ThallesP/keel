package app

import (
	"context"
	"log/slog"
	"time"
)

type App struct {
	Store  Store
	Events Publisher
	Conns  Connections
	Jobs   Jobs
	Config Config
	Log    *slog.Logger
	Now    func() int64

	Swarm Swarm
	Logs  LogReader
	Proxy Proxy
	Axiom Axiom

	Passwords  Passwords
	authLimits *authLimiters
}

type Config struct {
	Version         string
	SiteURL         string
	WorkerToken     string
	PublicIP        string
	ACMECA          string
	ACMEEmail       string
	OTLPURL         string
	AxiomAuthURL    string
	AxiomAPIURL     string
	AllowLocalSinks bool
	DataDir         string
	AgentImage      string
	AgentControlURL string
}

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

func (a *App) read(ctx context.Context, fn func(tx Tx) error) error {
	return a.Store.Read(ctx, fn)
}

func (a *App) write(ctx context.Context, fn func(tx Tx, ch *Changes) error) error {
	ch := &Changes{}
	if err := a.Store.Write(ctx, func(tx Tx) error { return fn(tx, ch) }); err != nil {
		return err
	}
	recordInvalidations(ctx, ch)
	ch.publish(a.Events)
	return nil
}

type noopPublisher struct{}

func (noopPublisher) Publish(string, []string) {}
