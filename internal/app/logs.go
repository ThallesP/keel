package app

import (
	"cmp"
	"context"
	"math"
	"sync"

	"github.com/ThallesP/keel/internal/domain"
)

func clampLogTail(tail float64) int {
	if math.IsNaN(tail) {
		return 1
	}
	return int(min(max(tail, 1), 1000))
}

func (a *App) TailNodeLogs(ctx context.Context, actor domain.Actor, nodeID string, tail float64) (domain.LogTail, error) {
	var sink *domain.LogSink
	err := a.read(ctx, func(tx Tx) error {
		scope, err := requireNode(tx, actor, nodeID)
		if err != nil {
			return err
		}
		sink, err = orgSinkOf(tx, scope.Org)
		return err
	})
	if err != nil {
		return domain.LogTail{}, err
	}
	if sink != nil {
		return a.axiomTail(ctx, axiomCfgOf(*sink, sink.Dataset), nodeID, clampLogTail(tail))
	}
	return a.dockerTail(ctx, nodeID, clampLogTail(tail))
}

func (a *App) EnvironmentLogs(ctx context.Context, actor domain.Actor, environmentID, search string, tail float64, rng domain.TimeRange) (domain.EnvironmentLogs, error) {
	from := a.Now() - axiomQueryWindowMs
	if rng != "" {
		spec, ok := rng.Spec()
		if !ok {
			return domain.EnvironmentLogs{}, errBadRange
		}
		from, _ = spec.Window(a.Now())
	}
	scope, err := a.envSinkScope(ctx, actor, environmentID, errNoLogStore)
	if err != nil {
		return domain.EnvironmentLogs{}, err
	}
	lines, err := a.axiomLines(ctx, axiomCfgOf(scope.Sink, scope.Sink.Dataset), scope.ServiceIDs, linesQuery{
		N: clampLogTail(tail), Search: truncateRunes(search, 200), From: float64(from), To: a.axiomUntil(),
	})
	if err != nil {
		return domain.EnvironmentLogs{}, obsInvalid(err)
	}
	return domain.EnvironmentLogs{Source: domain.LogSourceAxiom, Lines: lines}, nil
}

const logsAroundMs = 30_000

func validLogMoment(at float64) error {
	if math.IsNaN(at) || math.IsInf(at, 0) {
		return domain.Invalid("at: not a time")
	}
	return nil
}

func (a *App) LogsAround(ctx context.Context, actor domain.Actor, environmentID string, at float64) ([]domain.EnvironmentLogLine, error) {
	if err := validLogMoment(at); err != nil {
		return nil, err
	}
	scope, err := a.envSinkScope(ctx, actor, environmentID, errNoLogStore)
	if err != nil {
		return nil, err
	}
	lines, err := a.axiomLines(ctx, axiomCfgOf(scope.Sink, scope.Sink.Dataset), scope.ServiceIDs, linesQuery{
		N: 500, From: at - logsAroundMs, To: at + logsAroundMs, OldestFirst: true,
	})
	if err != nil {
		return nil, obsInvalid(err)
	}
	return lines, nil
}

func (a *App) dockerTail(ctx context.Context, nodeID string, n int) (domain.LogTail, error) {
	service := domain.ServicePrefix + nodeID
	var body []byte
	var found bool
	var readErr error
	var wg sync.WaitGroup
	wg.Go(func() { body, found, readErr = a.Logs.ReadServiceLogs(ctx, service, n) })
	tasks, err := a.Logs.ListLogReplicas(ctx, service)
	if err != nil {
		tasks = nil
	}
	wg.Wait()
	if readErr != nil {
		return domain.LogTail{}, readErr
	}
	if !found {
		return domain.LogTail{Source: domain.LogSourceDocker, Lines: []domain.ServiceLogLine{}, Replicas: []domain.LogReplica{}}, nil
	}
	replicas := make([]domain.LogReplica, len(tasks))
	for i, t := range tasks {
		replicas[i] = domain.LogReplica{Task: t.ID, Slot: t.Slot, State: cmp.Or(t.State, "unknown")}
	}
	sortLogReplicas(replicas)
	return domain.LogTail{Source: domain.LogSourceDocker, Lines: demuxDockerLogs(body), Replicas: replicas}, nil
}
