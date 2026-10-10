package agent

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
)

type Config struct {
	URL        string
	Token      string
	StatePath  string
	ConfigPoll time.Duration
}

func configFrom(getenv func(string) string, secretPath string) (Config, error) {
	cfg := Config{
		URL:        strings.TrimRight(getenv("KEEL_URL"), "/"),
		Token:      getenv("KEEL_WORKER_TOKEN"),
		StatePath:  cmp.Or(getenv("KEEL_STATE"), "/var/lib/keel-agent/state.json"),
		ConfigPoll: 30 * time.Second,
	}
	if cfg.URL == "" {
		return Config{}, errors.New("KEEL_URL is required (the control plane's URL, e.g. http://100.64.0.1:8080)")
	}
	if ms, err := strconv.Atoi(getenv("KEEL_CONFIG_POLL_MS")); err == nil && ms > 0 {
		cfg.ConfigPoll = time.Duration(ms) * time.Millisecond
	}
	if cfg.Token != "" {
		return cfg, nil
	}
	raw, err := os.ReadFile(secretPath)
	if err != nil {
		return Config{}, errors.New("no KEEL_WORKER_TOKEN and no /run/secrets/keel_worker_token")
	}
	cfg.Token = strings.TrimSpace(string(raw))
	return cfg, nil
}

type Agent struct {
	poll      time.Duration
	docker    Docker
	log       *slog.Logger
	state     *State
	cp        *ControlPlane
	shipper   *Shipper
	forwarder *Forwarder
	wake      chan struct{}
}

func New(cfg Config, docker Docker, controlPlane *http.Client, log *slog.Logger) *Agent {
	a := &Agent{poll: cfg.ConfigPoll, docker: docker, log: log, wake: make(chan struct{}, 1)}
	a.state = LoadState(cfg.StatePath)
	a.cp = &ControlPlane{URL: cfg.URL, Token: cfg.Token, HTTP: controlPlane, Log: log, sleep: sleepCtx}
	a.shipper = NewShipper(docker, a.state, log, NewSinkFactory(log), a.refreshConfig)
	a.forwarder = &Forwarder{
		Docker: docker, Poster: a.cp, State: a.state, Log: log,
		OnContainer: a.shipper.OnContainerEvent, reconnect: 2 * time.Second,
	}
	return a
}

func (a *Agent) refreshConfig() {
	select {
	case a.wake <- struct{}{}:
	default:
	}
}

func (a *Agent) Run(ctx context.Context) error {
	me, err := a.docker.Info(ctx)
	if err != nil {
		return fmt.Errorf("docker /info: %w", err)
	}
	a.shipper.nodeID = cmp.Or(me.NodeID, me.Name)
	a.log.Info("keel agent", "node", me.NodeID, "name", me.Name)

	var wg sync.WaitGroup
	wg.Go(func() { a.state.RunWriter(ctx, time.Second, a.log) })
	wg.Go(func() { a.pollConfig(ctx) })
	wg.Go(func() { a.forwarder.Run(ctx) })

	<-ctx.Done()
	a.log.Info("keel agent: shutting down", "cause", context.Cause(ctx))
	a.shipper.stopFollowing()
	flushCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	a.shipper.Flush(flushCtx)
	cancel()
	a.shipper.Close()
	waitAtMost(&wg, time.Second)
	if err := a.state.Flush(); err != nil {
		a.log.Error("writing the agent state failed", "err", err)
	}
	return nil
}

func (a *Agent) pollConfig(ctx context.Context) {
	for {
		sinks, err := a.cp.FetchConfig(ctx)
		if err == nil {
			a.shipper.ApplyConfig(sinks)
			err = a.shipper.ReconcileFollowers(ctx)
		}
		if err != nil && ctx.Err() == nil {
			a.log.Error("polling the worker config failed", "err", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(a.poll):
		case <-a.wake:
		}
	}
}

func sleepCtx(ctx context.Context, d time.Duration) error {
	select {
	case <-time.After(d):
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func waitAtMost(wg *sync.WaitGroup, d time.Duration) {
	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(d):
	}
}
