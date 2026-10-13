package sqlite

import (
	"encoding/json"

	"github.com/ThallesP/keel/internal/app"
	"github.com/ThallesP/keel/internal/domain"
	"github.com/ThallesP/keel/internal/gen/sqlc"
)

func (t *tx) NodeExists(id string) (bool, error) {
	n, err := t.q.MetricsNodeExists(t.ctx, id)
	return n > 0, err
}

func (t *tx) InsertServiceSample(s domain.ServiceSample) error {
	return t.q.MetricsInsertService(t.ctx, sqlc.MetricsInsertServiceParams{
		NodeID: s.NodeID, Task: s.Task, ServerID: s.ServerID, At: s.At,
		CpuCores: s.CPUCores, MemoryBytes: s.MemoryBytes,
		RxBytesPerSec: s.RxBytesPerSec, TxBytesPerSec: s.TxBytesPerSec,
	})
}

func (t *tx) InsertServerSample(s domain.ServerSample) error {
	return t.q.MetricsInsertServer(t.ctx, sqlc.MetricsInsertServerParams{
		ServerID: s.ServerID, Name: s.Name, At: s.At, Cpus: int64(s.CPUs), CpuCores: s.CPUCores,
		MemoryBytes: s.MemoryBytes, MemoryTotalBytes: s.MemoryTotalBytes,
		DiskBytes: s.DiskBytes, DiskTotalBytes: s.DiskTotalBytes,
	})
}

func (t *tx) ServiceBuckets(q app.BucketQuery) ([]app.ServiceBucketRow, error) {
	if q.Hourly {
		rows, err := t.q.MetricsServiceBucketsHourly(t.ctx, sqlc.MetricsServiceBucketsHourlyParams{
			BinMs: q.BinMs, EnvironmentID: q.EnvironmentID, NodeID: q.NodeID, FromMs: q.From, ToMs: q.To,
		})
		return bucketRows(rows, func(r sqlc.MetricsServiceBucketsHourlyRow) app.ServiceBucketRow {
			return app.ServiceBucketRow{NodeID: r.NodeID, Bucket: r.Bucket, CPUCores: r.CpuCores, MemoryBytes: r.MemoryBytes, RxBytesPerSec: r.RxBytesPerSec, TxBytesPerSec: r.TxBytesPerSec}
		}), err
	}
	rows, err := t.q.MetricsServiceBuckets(t.ctx, sqlc.MetricsServiceBucketsParams{
		BinMs: q.BinMs, EnvironmentID: q.EnvironmentID, NodeID: q.NodeID, FromMs: q.From, ToMs: q.To,
	})
	return bucketRows(rows, func(r sqlc.MetricsServiceBucketsRow) app.ServiceBucketRow {
		return app.ServiceBucketRow{NodeID: r.NodeID, Bucket: r.Bucket, CPUCores: r.CpuCores, MemoryBytes: r.MemoryBytes, RxBytesPerSec: r.RxBytesPerSec, TxBytesPerSec: r.TxBytesPerSec}
	}), err
}

func (t *tx) TotalBuckets(q app.BucketQuery) ([]app.ServiceBucketRow, error) {
	if q.Hourly {
		rows, err := t.q.MetricsTotalBucketsHourly(t.ctx, sqlc.MetricsTotalBucketsHourlyParams{
			BinMs: q.BinMs, EnvironmentID: q.EnvironmentID, NodeID: q.NodeID, FromMs: q.From, ToMs: q.To,
		})
		return bucketRows(rows, func(r sqlc.MetricsTotalBucketsHourlyRow) app.ServiceBucketRow {
			return app.ServiceBucketRow{Bucket: r.Bucket, CPUCores: r.CpuCores, MemoryBytes: r.MemoryBytes, RxBytesPerSec: r.RxBytesPerSec, TxBytesPerSec: r.TxBytesPerSec}
		}), err
	}
	rows, err := t.q.MetricsTotalBuckets(t.ctx, sqlc.MetricsTotalBucketsParams{
		BinMs: q.BinMs, EnvironmentID: q.EnvironmentID, NodeID: q.NodeID, FromMs: q.From, ToMs: q.To,
	})
	return bucketRows(rows, func(r sqlc.MetricsTotalBucketsRow) app.ServiceBucketRow {
		return app.ServiceBucketRow{Bucket: r.Bucket, CPUCores: r.CpuCores, MemoryBytes: r.MemoryBytes, RxBytesPerSec: r.RxBytesPerSec, TxBytesPerSec: r.TxBytesPerSec}
	}), err
}

func bucketRows[R any](rows []R, row func(R) app.ServiceBucketRow) []app.ServiceBucketRow {
	out := make([]app.ServiceBucketRow, len(rows))
	for i, r := range rows {
		out[i] = row(r)
	}
	return out
}

func (t *tx) RecentPlacements(environmentID string, since int64) ([]app.Placement, error) {
	rows, err := t.q.MetricsRecentPlacements(t.ctx, sqlc.MetricsRecentPlacementsParams{EnvironmentID: environmentID, At: since})
	if err != nil {
		return nil, err
	}
	out := make([]app.Placement, len(rows))
	for i, r := range rows {
		out[i] = app.Placement{ServerID: r.ServerID, NodeID: r.NodeID}
	}
	return out, nil
}

func (t *tx) LatestServerSample(serverID string, since int64) (domain.ServerSample, error) {
	r, err := t.q.MetricsLatestServer(t.ctx, sqlc.MetricsLatestServerParams{ServerID: serverID, At: since})
	if err != nil {
		return domain.ServerSample{}, noRow(err)
	}
	return domain.ServerSample{
		ServerID: r.ServerID, Name: r.Name, At: r.At, CPUs: int(r.Cpus), CPUCores: r.CpuCores,
		MemoryBytes: r.MemoryBytes, MemoryTotalBytes: r.MemoryTotalBytes,
		DiskBytes: r.DiskBytes, DiskTotalBytes: r.DiskTotalBytes,
	}, nil
}

func (t *tx) DeployMarks(environmentID string, from, to int64) ([]domain.DeployMark, error) {
	rows, err := t.q.MetricsListDeployments(t.ctx, sqlc.MetricsListDeploymentsParams{EnvironmentID: environmentID, FromMs: from, ToMs: to})
	if err != nil {
		return nil, err
	}
	out := make([]domain.DeployMark, len(rows))
	for i, r := range rows {
		out[i] = domain.DeployMark{ID: r.ID, At: r.StartedAt, Message: r.Message, Status: domain.DeploymentStatus(r.Status)}
	}
	return out, nil
}

func (t *tx) RollupMetrics(since int64) error {
	return t.q.MetricsRollupHours(t.ctx, since)
}

func (t *tx) PruneMetrics(rawBefore, hourlyBefore int64) error {
	if err := t.q.MetricsPruneServices(t.ctx, rawBefore); err != nil {
		return err
	}
	if err := t.q.MetricsPruneServers(t.ctx, rawBefore); err != nil {
		return err
	}
	return t.q.MetricsPruneHourly(t.ctx, hourlyBefore)
}

func (t *tx) DashboardOf(environmentID string) ([]domain.Panel, error) {
	raw, err := t.q.MetricsGetDashboard(t.ctx, environmentID)
	if err != nil {
		return nil, noRow(err)
	}
	panels := []domain.Panel{}
	return panels, json.Unmarshal([]byte(raw), &panels)
}

func (t *tx) SaveDashboard(environmentID string, panels []domain.Panel, now int64) error {
	b, err := json.Marshal(panels)
	if err != nil {
		return err
	}
	return t.q.MetricsUpsertDashboard(t.ctx, sqlc.MetricsUpsertDashboardParams{EnvironmentID: environmentID, Panels: string(b), UpdatedAt: now})
}
