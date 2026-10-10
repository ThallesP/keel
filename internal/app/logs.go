package app

import (
	"context"
	"errors"
	"math"

	"github.com/ThallesP/keel/internal/domain"
)

func clampLogTail(tail float64) int {
	if math.IsNaN(tail) {
		return 1
	}
	return int(math.Min(math.Max(1, math.Floor(tail)), 1000))
}

func (a *App) TailNodeLogs(ctx context.Context, actor domain.Actor, nodeID string, tail float64) (domain.LogTail, error) {
	var sink *domain.LogSink
	err := a.read(ctx, func(tx Tx) error {
		scope, err := requireNode(tx, actor, nodeID)
		if err != nil {
			return err
		}
		rec, err := orgSinkOf(tx, scope.Org)
		if err != nil {
			return err
		}
		if rec != nil {
			sink = &rec.Sink
		}
		return nil
	})
	if err != nil {
		return domain.LogTail{}, err
	}
	n := clampLogTail(tail)
	if sink != nil && sink.Kind == domain.SinkKindAxiom {
		return a.axiomTail(ctx, axiomLogsCfg(*sink), nodeID, n)
	}
	return a.dockerTail(ctx, nodeID, n)
}

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
	q := linesQuery{N: clampLogTail(tail), Search: truncateRunes(search, 200), From: float64(a.Now() - axiomQueryWindowMs), To: a.axiomUntil()}
	if rng != "" {
		from, _, _ := domain.RangeWindow(rng, a.Now())
		q.From = float64(from)
	}
	lines, err := a.axiomLines(ctx, axiomLogsCfg(*scope.Sink), scope.ServiceIDs, q)
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
	scope, err := a.envSinkScope(ctx, actor, environmentID)
	if err != nil {
		return nil, err
	}
	if scope.Sink == nil || scope.Sink.Kind != domain.SinkKindAxiom {
		return nil, domain.Invalid(msgNoLogStore)
	}
	lines, err := a.axiomLines(ctx, axiomLogsCfg(*scope.Sink), scope.ServiceIDs, linesQuery{
		N: 500, From: at - logsAroundMs, To: at + logsAroundMs, OldestFirst: true,
	})
	if err != nil {
		return nil, obsInvalid(err)
	}
	return lines, nil
}

func (a *App) dockerTail(ctx context.Context, nodeID string, n int) (domain.LogTail, error) {
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
		tasks = nil
	}
	res := <-logsc
	if res.err != nil {
		return domain.LogTail{}, res.err
	}
	if !res.found {
		return domain.LogTail{Source: domain.LogSourceDocker, Lines: []domain.ServiceLogLine{}, Replicas: []domain.LogReplica{}}, nil
	}
	replicas := make([]domain.LogReplica, len(tasks))
	for i, t := range tasks {
		state := t.State
		if state == "" {
			state = "unknown"
		}
		replicas[i] = domain.LogReplica{Task: t.ID, Slot: t.Slot, State: state}
	}
	sortLogReplicas(replicas)
	return domain.LogTail{Source: domain.LogSourceDocker, Lines: demuxDockerLogs(res.body), Replicas: replicas}, nil
}
