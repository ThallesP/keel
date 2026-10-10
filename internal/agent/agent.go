package agent

import (
	"context"
	"errors"
	"fmt"
	"math"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
)

type Config struct {
	URL          string
	Token        string
	StatePath    string
	ConfigPoll   time.Duration
	DockerSocket string
}

const (
	defaultConfigPoll = 30 * time.Second
	shutdownBudget    = 5 * time.Second
)

func ConfigFromEnv() (Config, error) {
	return configFrom(os.Getenv, "/run/secrets/keel_worker_token")
}

func configFrom(getenv func(string) string, secretPath string) (Config, error) {
	cfg := Config{
		URL:          strings.TrimRight(getenv("KEEL_URL"), "/"),
		StatePath:    getenv("KEEL_STATE"),
		ConfigPoll:   defaultConfigPoll,
		DockerSocket: dockerSocket(getenv),
	}
	if cfg.URL == "" {
		return Config{}, errors.New("KEEL_URL is required (the control plane's URL, e.g. http://100.64.0.1:8080)")
	}
	if cfg.StatePath == "" {
		cfg.StatePath = "/var/lib/keel-agent/state.json"
	}
	if ms, err := strconv.ParseFloat(strings.TrimSpace(getenv("KEEL_CONFIG_POLL_MS")), 64); err == nil && ms > 0 && !math.IsInf(ms, 0) {
		cfg.ConfigPoll = time.Duration(ms * float64(time.Millisecond))
	}
	if t := getenv("KEEL_WORKER_TOKEN"); t != "" {
		cfg.Token = t
		return cfg, nil
	}
	raw, err := os.ReadFile(secretPath)
	if err != nil {
		return Config{}, errors.New("no KEEL_WORKER_TOKEN and no /run/secrets/keel_worker_token")
	}
	cfg.Token = strings.TrimSpace(string(raw))
	return cfg, nil
}

func orDefault(v, def string) string {
	if v == "" {
		return def
	}
	return v
}

type Agent struct {
	cfg       Config
	docker    Docker
	log       *Logger
	state     *State
	cp        *ControlPlane
	shipper   *Shipper
	forwarder *Forwarder
	wake      chan struct{}
}

func New(cfg Config, docker Docker, controlPlane, ingest *http.Client, log *Logger) *Agent {
	a := &Agent{cfg: cfg, docker: docker, log: log, wake: make(chan struct{}, 1)}
	a.state = LoadState(cfg.StatePath)
	a.cp = NewControlPlane(cfg.URL, cfg.Token, controlPlane, log)
	a.shipper = NewShipper(docker, a.state, log, NewSinkFactory(ingest, log), a.refreshConfig)
	a.forwarder = &Forwarder{
		Docker: docker, Poster: a.cp, State: a.state, Log: log,
		OnContainer: a.shipper.OnContainerEvent,
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
	writerCtx, stopWriter := context.WithCancel(context.Background())
	writerDone := make(chan struct{})
	go func() {
		defer close(writerDone)
		a.state.RunWriter(writerCtx, time.Second, a.log)
	}()
	defer func() {
		stopWriter()
		<-writerDone
		if err := a.state.Flush(); err != nil {
			a.log.Log("state", "write failed: "+err.Error())
		}
	}()

	me, err := a.docker.Info(ctx)
	if err != nil {
		a.shipper.Close(0)
		return fmt.Errorf("docker /info: %w", err)
	}
	a.shipper.SetNodeID(orDefault(me.NodeID, me.Name))
	a.log.Log("worker", "starting on node "+orDefault(me.NodeID, "?")+" ("+orDefault(me.Name, "?")+")")

	var wg sync.WaitGroup
	wg.Go(func() { a.pollConfig(ctx) })
	wg.Go(func() { a.forwarder.Run(ctx) })

	<-ctx.Done()
	a.log.Log("worker", signalName(ctx)+", flushing")
	a.shipper.StopFollowing()
	flushCtx, cancel := context.WithTimeout(context.Background(), shutdownBudget)
	a.shipper.Flush(flushCtx)
	cancel()
	a.shipper.Close(time.Second)
	waitAtMost(&wg, time.Second)
	return nil
}

func (a *Agent) pollConfig(ctx context.Context) {
	for {
		cfg, err := a.cp.FetchConfig(ctx)
		if err == nil {
			a.shipper.ApplyConfig(cfg.Sinks)
			err = a.shipper.ReconcileFollowers(ctx)
		}
		if err != nil && ctx.Err() == nil {
			a.log.Log("config", "poll failed: "+errorText(err))
		}
		t := time.NewTimer(a.cfg.ConfigPoll)
		select {
		case <-ctx.Done():
			t.Stop()
			return
		case <-t.C:
		case <-a.wake:
			t.Stop()
		}
	}
}

func signalName(ctx context.Context) string {
	if err := context.Cause(ctx); err != nil && strings.HasPrefix(err.Error(), "interrupt") {
		return "SIGINT"
	}
	return "SIGTERM"
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

func dockerSocket(getenv func(string) string) string {
	if s := getenv("DOCKER_SOCKET"); s != "" {
		return s
	}
	if getenv("DOCKER_HOST") != "" {
		return ""
	}
	return "/var/run/docker.sock"
}
