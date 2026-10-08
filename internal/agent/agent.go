// Package agent is `keel agent`, the per-node process (was apps/worker): it runs on every Swarm
// node as a global service and does two things, both outbound-only and read-only on Docker
// (docs/go/spec/swarm-worker.md §13, docs/go/spec/observability.md §11, docs/logs.md):
//
//  1. events.go forwards `docker events` to the control plane (POST /worker/events) so Swarm
//     observation is event-driven instead of polled.
//  2. logs.go streams container stdout/stderr to the organization's log sink when one is
//     configured. The routing comes from GET /worker/config, polled.
//
// Nothing listens. Every Docker call is a GET. Restarts resume from the state file.
package agent

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Config is the agent's environment. Names are the Bun worker's, so today's `keel-worker` Swarm
// service definition keeps working with the image swapped.
type Config struct {
	URL          string        // KEEL_URL (required), trailing "/" stripped
	Token        string        // KEEL_WORKER_TOKEN, else /run/secrets/keel_worker_token (trimmed)
	StatePath    string        // KEEL_STATE, default /var/lib/keel-worker/state.json
	ConfigPoll   time.Duration // KEEL_CONFIG_POLL_MS, default 30 s
	DockerSocket string        // DOCKER_SOCKET, default /var/run/docker.sock
}

const (
	defaultStatePath  = "/var/lib/keel-worker/state.json"
	defaultSecretPath = "/run/secrets/keel_worker_token"
	defaultSocket     = "/var/run/docker.sock"
	defaultConfigPoll = 30 * time.Second
	shutdownBudget    = 5 * time.Second // Swarm's stop grace period is 10 s
)

// ConfigFromEnv reads the agent's environment.
func ConfigFromEnv() (Config, error) { return configFrom(os.Getenv, defaultSecretPath) }

func configFrom(getenv func(string) string, secretPath string) (Config, error) {
	cfg := Config{
		URL:          strings.TrimRight(getenv("KEEL_URL"), "/"),
		StatePath:    orDefault(getenv("KEEL_STATE"), defaultStatePath),
		ConfigPoll:   defaultConfigPoll,
		DockerSocket: orDefault(getenv("DOCKER_SOCKET"), defaultSocket),
	}
	if cfg.URL == "" {
		return Config{}, errors.New("KEEL_URL is required (Convex site URL, e.g. https://x.convex.site)")
	}
	if ms, err := strconv.Atoi(strings.TrimSpace(getenv("KEEL_CONFIG_POLL_MS"))); err == nil && ms > 0 {
		cfg.ConfigPoll = time.Duration(ms) * time.Millisecond
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

// Agent is one running `keel agent`.
type Agent struct {
	cfg       Config
	docker    Docker
	log       *Logger
	state     *State
	cp        *ControlPlane
	shipper   *Shipper
	forwarder *Forwarder
	wake      chan struct{} // early config poll
}

// New wires an agent. controlPlane carries the /worker/* requests (the mesh client), ingest the
// sink requests (the public internet).
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

// refreshConfig wakes the config loop now rather than at the next poll. A wake while a poll is in
// flight is kept, so that poll is followed by another one at once.
func (a *Agent) refreshConfig() {
	select {
	case a.wake <- struct{}{}:
	default:
	}
}

// Run runs until ctx is done, then flushes the log queues (at most 5 s) and returns nil: a
// signal is a clean exit. Only a failing GET /info at start is an error.
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
	nodeID := me.NodeID
	if nodeID == "" {
		nodeID = me.Name
	}
	a.shipper.SetNodeID(nodeID)
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

// pollConfig fetches /worker/config, applies it and reconciles the followers, every ConfigPoll
// or sooner when woken.
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

// signalName is the worker's "SIGTERM"/"SIGINT" from signal.NotifyContext's cause.
func signalName(ctx context.Context) string {
	cause := ""
	if err := context.Cause(ctx); err != nil {
		cause = err.Error()
	}
	switch {
	case strings.HasPrefix(cause, "interrupt"):
		return "SIGINT"
	case strings.HasPrefix(cause, "terminated"):
		return "SIGTERM"
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
