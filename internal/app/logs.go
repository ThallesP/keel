package app

// The log read side (convex/logs.ts): a service's tail from its organization's sink or from
// `docker service logs` on the manager, and the environment-wide stream (Axiom only: Docker has
// no cross-service query). Polled by the dashboard and the CLI; no table involved.

import (
	"context"
	"errors"
	"math"

	"github.com/ThallesP/keel/internal/domain"
)

// clampTail is min(max(1, floor(tail)), 1000).
func clampTail(tail float64) int {
	if math.IsNaN(tail) {
		return 1
	}
	return int(math.Min(math.Max(1, math.Floor(tail)), 1000))
}

// TailNodeLogs is the last tail lines (default 200 at the transport; clamped to 1–1000) of a
// node's service: the organization's Axiom sink when one is connected, else Docker.
// Provider failures are not wrapped (they reach the caller as server errors, as before).
func (a *App) TailNodeLogs(ctx context.Context, actor domain.Actor, nodeID string, tail float64) (domain.LogTail, error) {
	var sink *domain.LogSink
	err := a.read(ctx, func(tx Tx) error {
		scope, err := requireNode(tx, actor, nodeID)
		if err != nil {
			return err
		}
		rec, err := sinkOf(tx, scope.Org)
		if err != nil {
			return err
		}
		if rec != nil {
			s := rec.Sink
			sink = &s
		}
		return nil
	})
	if err != nil {
		return domain.LogTail{}, err
	}
	n := clampTail(tail)
	if sink != nil && sink.Kind == domain.SinkKindAxiom {
		return a.axiomTail(ctx, logsCfg(*sink), nodeID, n)
	}
	return a.dockerTail(ctx, nodeID, n)
}

// EnvironmentLogs is the newest tail lines (default 300; 1–1000) across every service of the
// environment, optionally only those containing search (cut to 200 characters) and only within
// rng ("" = the last 30 days). Oldest first.
func (a *App) EnvironmentLogs(ctx context.Context, actor domain.Actor, environmentID, search string, tail float64, rng domain.TimeRange) (domain.EnvironmentLogs, error) {
	if rng != "" && !rng.Valid() {
		return domain.EnvironmentLogs{}, domain.Invalid("Range must be one of 15m, 1h, 24h, 7d")
	}
	scope, err := a.envSinkScope(ctx, actor, environmentID)
	if err != nil {
		return domain.EnvironmentLogs{}, err
	}
	if scope.Sink == nil || scope.Sink.Kind != domain.SinkKindAxiom {
		return domain.EnvironmentLogs{}, domain.Invalid(msgNoLogStore)
	}
	q := linesQuery{N: clampTail(tail), Search: jsSlice(search, 200)}
	if rng != "" {
		// The same bucket-aligned start as the trace overview, so lines and requests cover one window.
		from, _, _ := domain.RangeWindow(rng, a.Now())
		q.From = f64(float64(from))
	}
	lines, err := a.axiomLines(ctx, logsCfg(*scope.Sink), scope.ServiceIDs, q)
	if err != nil {
		return domain.EnvironmentLogs{}, obsInvalid(err)
	}
	return domain.EnvironmentLogs{Source: domain.LogSourceAxiom, Lines: lines}, nil
}

const aroundMs = 30_000

// validMoment: `at` must be a finite epoch ms (a JSON number never is NaN; a query string can be).
func validMoment(at float64) error {
	if math.IsNaN(at) || math.IsInf(at, 0) {
		return domain.Invalid("at: not a time")
	}
	return nil
}

// LogsAround is every service's lines within 30 s either side of at, oldest first: the context
// of a log line that names no trace. Axiom only.
func (a *App) LogsAround(ctx context.Context, actor domain.Actor, environmentID string, at float64) ([]domain.EnvironmentLogLine, error) {
	if err := validMoment(at); err != nil {
		return nil, err
	}
	scope, err := a.envSinkScope(ctx, actor, environmentID)
	if err != nil {
		return nil, err
	}
	if scope.Sink == nil || scope.Sink.Kind != domain.SinkKindAxiom {
		return nil, domain.Invalid(msgNoLogStore)
	}
	lines, err := a.axiomLines(ctx, logsCfg(*scope.Sink), scope.ServiceIDs, linesQuery{
		N: 500, From: f64(at - aroundMs), To: f64(at + aroundMs), OldestFirst: true,
	})
	if err != nil {
		return nil, obsInvalid(err)
	}
	return lines, nil
}

// dockerTail is the last n lines of the node's Swarm service, every replica merged, each line
// tagged by task, read through the manager's Docker socket.
func (a *App) dockerTail(ctx context.Context, nodeID string, n int) (domain.LogTail, error) {
	empty := domain.LogTail{Source: domain.LogSourceDocker, Lines: []domain.ServiceLogLine{}, Replicas: []domain.LogReplica{}}
	if a.Logs == nil {
		return domain.LogTail{}, errors.New("docker logs: no log reader configured")
	}
	service := domain.ServicePrefix + nodeID
	type logsResult struct {
		body  []byte
		found bool
		err   error
	}
	logsc := make(chan logsResult, 1)
	go func() {
		body, found, err := a.Logs.ReadServiceLogs(ctx, service, n)
		logsc <- logsResult{body, found, err}
	}()
	tasks, err := a.Logs.ListLogReplicas(ctx, service)
	if err != nil {
		tasks = nil // any error → no replicas
	}
	res := <-logsc
	if res.err != nil {
		return domain.LogTail{}, res.err
	}
	if !res.found {
		return empty, nil
	}
	replicas := make([]domain.LogReplica, len(tasks))
	for i, t := range tasks {
		state := t.State
		if state == "" {
			state = "unknown"
		}
		replicas[i] = domain.LogReplica{Task: t.ID, Slot: t.Slot, State: state}
	}
	sortReplicas(replicas)
	return domain.LogTail{Source: domain.LogSourceDocker, Lines: demuxDockerLogs(res.body), Replicas: replicas}, nil
}
