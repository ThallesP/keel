package app

import (
	"context"
	"errors"
	"slices"
	"time"

	"github.com/ThallesP/keel/internal/domain"
)

func (a *App) IngestMetrics(ctx context.Context, r MetricsReport) error {
	return a.write(ctx, func(tx Tx, _ *Changes) error {
		if r.Server != nil {
			if err := tx.InsertServerSample(*r.Server); err != nil {
				return err
			}
		}
		known := map[string]bool{}
		for _, t := range r.Tasks {
			exists, seen := known[t.NodeID]
			if !seen {
				var err error
				if exists, err = tx.NodeExists(t.NodeID); err != nil {
					return err
				}
				known[t.NodeID] = exists
			}
			if !exists {
				continue
			}
			err := tx.InsertServiceSample(domain.ServiceSample{
				NodeID: t.NodeID, Task: t.Task, ServerID: r.ServerID, At: r.At,
				CPUCores: t.CPUCores, MemoryBytes: t.MemoryBytes,
				RxBytesPerSec: t.RxBytesPerSec, TxBytesPerSec: t.TxBytesPerSec,
			})
			if err != nil {
				return err
			}
		}
		return nil
	})
}

func (a *App) MetricsOverview(ctx context.Context, actor domain.Actor, environmentID string, rng domain.TimeRange, nodeID string) (domain.MetricsOverview, error) {
	spec, ok := rng.Spec()
	if !ok {
		return domain.MetricsOverview{}, errBadRange
	}
	now := a.Now()
	from, to := spec.Window(now)
	out := domain.MetricsOverview{From: from, To: to, BucketMs: spec.BinMs}
	err := a.read(ctx, func(tx Tx) error {
		if _, err := requireEnvironment(tx, actor, environmentID); err != nil {
			return err
		}
		ids, err := serviceIDs(tx, environmentID)
		if err != nil {
			return err
		}
		if nodeID != "" && !slices.Contains(ids, nodeID) {
			return domain.E(domain.CodeServiceNotFound, domain.MsgNodeNotFound)
		}
		q := BucketQuery{
			EnvironmentID: environmentID, NodeID: nodeID, From: from, To: to, BinMs: spec.BinMs,
			Hourly: spec.BinMs >= time.Hour.Milliseconds(),
		}
		rows, err := tx.ServiceBuckets(q)
		if err != nil {
			return err
		}
		out.Services = serviceSeries(rows, from, spec)
		totals, err := tx.TotalBuckets(q)
		if err != nil {
			return err
		}
		out.Total = emptyBuckets(from, spec)
		fillBuckets(out.Total, totals, from, spec)
		out.Servers, err = serverUsage(tx, environmentID, nodeID, now-domain.PlacementWindow.Milliseconds())
		if err != nil {
			return err
		}
		out.Deployments, err = tx.DeployMarks(environmentID, from, to)
		return err
	})
	return out, err
}

func serviceIDs(tx Tx, environmentID string) ([]string, error) {
	nodes, err := tx.Nodes(environmentID)
	if err != nil {
		return nil, err
	}
	ids := []string{}
	for _, n := range nodes {
		if n.Type != domain.NodeVolume && n.Type != domain.NodeGroup {
			ids = append(ids, n.ID)
		}
	}
	return ids, nil
}

func serviceSeries(rows []ServiceBucketRow, from int64, spec domain.RangeSpec) []domain.ServiceMetrics {
	out := []domain.ServiceMetrics{}
	for i, r := range rows {
		if i > 0 && rows[i-1].NodeID == r.NodeID {
			continue
		}
		end := i + 1
		for end < len(rows) && rows[end].NodeID == r.NodeID {
			end++
		}
		buckets := emptyBuckets(from, spec)
		fillBuckets(buckets, rows[i:end], from, spec)
		out = append(out, domain.ServiceMetrics{NodeID: r.NodeID, Buckets: buckets})
	}
	return out
}

func fillBuckets(buckets []domain.MetricsBucket, rows []ServiceBucketRow, from int64, spec domain.RangeSpec) {
	for _, r := range rows {
		slot := (r.Bucket - from) / spec.BinMs
		if slot < 0 || slot >= int64(len(buckets)) {
			continue
		}
		b := &buckets[slot]
		b.CPUCores, b.MemoryBytes = new(r.CPUCores), new(r.MemoryBytes)
		b.RxBytesPerSec, b.TxBytesPerSec = new(r.RxBytesPerSec), new(r.TxBytesPerSec)
	}
}

func emptyBuckets(from int64, spec domain.RangeSpec) []domain.MetricsBucket {
	buckets := make([]domain.MetricsBucket, spec.Buckets)
	for i := range buckets {
		buckets[i].Time = from + int64(i)*spec.BinMs
	}
	return buckets
}

func serverUsage(tx Tx, environmentID, nodeID string, since int64) ([]domain.ServerUsage, error) {
	placements, err := tx.RecentPlacements(environmentID, since)
	if err != nil {
		return nil, err
	}
	var order []string
	running := map[string][]string{}
	for _, p := range placements {
		if nodeID != "" && p.NodeID != nodeID {
			continue
		}
		if _, ok := running[p.ServerID]; !ok {
			order = append(order, p.ServerID)
		}
		running[p.ServerID] = append(running[p.ServerID], p.NodeID)
	}
	out := []domain.ServerUsage{}
	for _, id := range order {
		s, err := tx.LatestServerSample(id, since)
		if errors.Is(err, ErrNoRow) {
			continue
		}
		if err != nil {
			return nil, err
		}
		out = append(out, domain.ServerUsage{
			ID: s.ServerID, Name: s.Name, At: s.At, CPUs: s.CPUs, CPUCores: s.CPUCores,
			MemoryBytes: s.MemoryBytes, MemoryTotalBytes: s.MemoryTotalBytes,
			DiskBytes: s.DiskBytes, DiskTotalBytes: s.DiskTotalBytes, NodeIDs: running[id],
		})
	}
	return out, nil
}

func (a *App) recoverMetrics(ctx context.Context) {
	a.rollupMetrics(ctx)
	a.pruneMetrics(ctx)
	a.Jobs.Every("metrics.rollup", time.Minute, a.rollupMetrics)
	a.Jobs.Every("metrics.prune", time.Hour, a.pruneMetrics)
}

func (a *App) rollupMetrics(ctx context.Context) {
	hour := time.Hour.Milliseconds()
	since := (a.Now()/hour - 1) * hour
	err := a.write(ctx, func(tx Tx, _ *Changes) error { return tx.RollupMetrics(since) })
	if err != nil {
		a.Log.Error("metrics: roll up the last hours", "err", err)
	}
}

func (a *App) pruneMetrics(ctx context.Context) {
	now := a.Now()
	err := a.write(ctx, func(tx Tx, _ *Changes) error {
		return tx.PruneMetrics(now-domain.RawRetention.Milliseconds(), now-domain.MetricsRetention.Milliseconds())
	})
	if err != nil {
		a.Log.Error("metrics: prune old samples", "err", err)
	}
}

func (a *App) Dashboard(ctx context.Context, actor domain.Actor, environmentID string) ([]domain.Panel, error) {
	var panels []domain.Panel
	err := a.read(ctx, func(tx Tx) error {
		if _, err := requireEnvironment(tx, actor, environmentID); err != nil {
			return err
		}
		ids, err := serviceIDs(tx, environmentID)
		if err != nil {
			return err
		}
		saved, err := tx.DashboardOf(environmentID)
		if errors.Is(err, ErrNoRow) {
			saved, err = domain.DefaultPanels(), nil
		}
		if err != nil {
			return err
		}
		panels = domain.PanelsFor(saved, ids)
		return nil
	})
	return panels, err
}

func (a *App) SaveDashboard(ctx context.Context, actor domain.Actor, environmentID string, panels []domain.Panel) ([]domain.Panel, error) {
	err := a.write(ctx, func(tx Tx, ch *Changes) error {
		scope, err := requireEnvironment(tx, actor, environmentID)
		if err != nil {
			return err
		}
		ids, err := serviceIDs(tx, environmentID)
		if err != nil {
			return err
		}
		if err := domain.ValidatePanels(panels, ids); err != nil {
			return err
		}
		if err := tx.SaveDashboard(environmentID, panels, a.Now()); err != nil {
			return err
		}
		ch.Add(scope.Project.OrganizationID, "/api/environments/"+environmentID+"/dashboard")
		return nil
	})
	return panels, err
}
