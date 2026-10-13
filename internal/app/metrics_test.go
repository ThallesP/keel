package app_test

import (
	"context"
	"slices"
	"testing"

	"github.com/ThallesP/keel/internal/app"
	"github.com/ThallesP/keel/internal/domain"
)

func TestMetricsOverview(t *testing.T) {
	ctx := context.Background()
	e := newObsEnv(t, 1_700_000_010_000)
	spec, _ := domain.Range15m.Spec()
	from, to := spec.Window(e.now)
	ingest := func(r app.MetricsReport) {
		t.Helper()
		if err := e.app.IngestMetrics(ctx, r); err != nil {
			t.Fatal(err)
		}
	}
	ingest(app.MetricsReport{At: from, ServerID: "s1", Tasks: []app.TaskSample{
		{NodeID: obsNodeAPI, Task: "t1", CPUCores: 0.5, MemoryBytes: 100, RxBytesPerSec: 10, TxBytesPerSec: 1},
		{NodeID: obsNodeAPI, Task: "t2", CPUCores: 0.25, MemoryBytes: 50, RxBytesPerSec: 10, TxBytesPerSec: 1},
		{NodeID: obsNodeForeign, Task: "t9", CPUCores: 4, MemoryBytes: 1 << 30},
		{NodeID: "gone", Task: "t8", CPUCores: 1},
	}})
	ingest(app.MetricsReport{At: from + 15_000, ServerID: "s1", Tasks: []app.TaskSample{
		{NodeID: obsNodeAPI, Task: "t1", CPUCores: 0.25, MemoryBytes: 120, RxBytesPerSec: 40, TxBytesPerSec: 4},
	}})
	ingest(app.MetricsReport{
		At: e.now - 10_000, ServerID: "s1",
		Server: &domain.ServerSample{ServerID: "s1", Name: "box", At: e.now - 10_000, CPUs: 4, CPUCores: 1.5, MemoryBytes: 3 << 30, MemoryTotalBytes: 8 << 30, DiskBytes: 40 << 30, DiskTotalBytes: 100 << 30},
		Tasks:  []app.TaskSample{{NodeID: obsNodeAPI, Task: "t1", CPUCores: 0.1, MemoryBytes: 90}},
	})
	ingest(app.MetricsReport{At: e.now - 10_000, ServerID: "s2", Tasks: []app.TaskSample{{NodeID: obsNodeWorker, Task: "t3", CPUCores: 0.1, MemoryBytes: 90}}})
	e.exec(t, `INSERT INTO deployments (id, environment_id, message, status, started_at) VALUES ('d1', 'env', 'Ship api', 'success', ?), ('d0', 'env', 'old', 'success', ?)`, from+60_000, from-1)

	got, err := e.app.MetricsOverview(ctx, e.member, "env", domain.Range15m, "")
	if err != nil {
		t.Fatal(err)
	}
	if got.From != from || got.To != to || got.BucketMs != 30_000 {
		t.Fatalf("window = %d..%d / %d", got.From, got.To, got.BucketMs)
	}
	ids := []string{}
	for _, s := range got.Services {
		ids = append(ids, s.NodeID)
	}
	if !slices.Equal(ids, []string{obsNodeAPI, obsNodeWorker}) {
		t.Fatalf("services = %v", ids)
	}
	api := got.Services[0].Buckets
	if len(api) != 30 || api[0].Time != from || *api[0].CPUCores != 0.5 || *api[0].MemoryBytes != 150 || *api[0].RxBytesPerSec != 30 || api[1].CPUCores != nil {
		t.Fatalf("api buckets[0..1] = %+v %+v", api[0], api[1])
	}
	if total := got.Total[0]; *total.CPUCores != 0.5 || *total.MemoryBytes != 150 || got.Total[1].CPUCores != nil {
		t.Fatalf("total buckets[0..1] = %+v %+v", total, got.Total[1])
	}
	if len(got.Servers) != 1 {
		t.Fatalf("servers = %+v", got.Servers)
	}
	if s := got.Servers[0]; s.ID != "s1" || s.Name != "box" || s.CPUs != 4 || s.MemoryTotalBytes != 8<<30 || !slices.Equal(s.NodeIDs, []string{obsNodeAPI}) {
		t.Fatalf("server s1 = %+v", s)
	}
	if len(got.Deployments) != 1 || got.Deployments[0].ID != "d1" || got.Deployments[0].Message != "Ship api" {
		t.Fatalf("deployments = %+v", got.Deployments)
	}

	only, err := e.app.MetricsOverview(ctx, e.member, "env", domain.Range15m, obsNodeWorker)
	if err != nil || len(only.Services) != 1 || only.Services[0].NodeID != obsNodeWorker || len(only.Servers) != 0 {
		t.Fatalf("worker only = %+v, %v", only, err)
	}
	_, err = e.app.MetricsOverview(ctx, e.member, "env", domain.Range15m, obsNodeForeign)
	obsWantCode(t, err, domain.CodeServiceNotFound, domain.MsgNodeNotFound)
	_, err = e.app.MetricsOverview(ctx, e.foreigner, "env", domain.Range15m, "")
	obsWantCode(t, err, domain.CodeProjectNotFound, domain.MsgEnvironmentNotFound)
	_, err = e.app.MetricsOverview(ctx, e.member, "env", "2h", "")
	obsWantCode(t, err, domain.CodeInvalidInput, "Range must be one of 15m, 1h, 24h, 7d")

	e.now += domain.RawRetention.Milliseconds() - 20_000
	e.app.PruneMetricsNow(ctx)
	if n := e.count(t, `SELECT COUNT(*) FROM service_metrics`); n != 2 {
		t.Fatalf("service samples after prune = %d, want the two newest", n)
	}
	if n := e.count(t, `SELECT COUNT(*) FROM server_metrics`); n != 1 {
		t.Fatalf("server samples after prune = %d", n)
	}
}

func TestMetricsHourlyRanges(t *testing.T) {
	ctx := context.Background()
	hour := int64(3_600_000)
	e := newObsEnv(t, 1_700_000_000_000/hour*hour+30*60_000)
	thisHour, lastHour := e.now/hour*hour, e.now/hour*hour-hour
	ingest := func(at int64, tasks ...app.TaskSample) {
		t.Helper()
		if err := e.app.IngestMetrics(ctx, app.MetricsReport{At: at, ServerID: "s1", Tasks: tasks}); err != nil {
			t.Fatal(err)
		}
	}
	ingest(lastHour+60_000, app.TaskSample{NodeID: obsNodeAPI, Task: "t1", CPUCores: 1, MemoryBytes: 100}, app.TaskSample{NodeID: obsNodeAPI, Task: "t2", CPUCores: 1, MemoryBytes: 100})
	ingest(lastHour+120_000, app.TaskSample{NodeID: obsNodeAPI, Task: "t1", CPUCores: 0, MemoryBytes: 50})
	ingest(thisHour+60_000, app.TaskSample{NodeID: obsNodeAPI, Task: "t1", CPUCores: 0.5, MemoryBytes: 300}, app.TaskSample{NodeID: obsNodeWorker, Task: "t3", CPUCores: 0.25, MemoryBytes: 10})
	e.app.RollupMetricsNow(ctx)

	got, err := e.app.MetricsOverview(ctx, e.member, "env", domain.Range24h, "")
	if err != nil {
		t.Fatal(err)
	}
	api := got.Services[0].Buckets
	last, now := api[len(api)-2], api[len(api)-1]
	if got.Services[0].NodeID != obsNodeAPI || last.Time != lastHour || *last.CPUCores != 1 || *last.MemoryBytes != 200 || *now.CPUCores != 0.5 || *now.MemoryBytes != 300 {
		t.Fatalf("api hourly buckets = %+v %+v", last, now)
	}
	if total := got.Total[len(got.Total)-1]; *total.CPUCores != 0.75 || *total.MemoryBytes != 310 {
		t.Fatalf("total of this hour = %+v", total)
	}

	ingest(thisHour+120_000, app.TaskSample{NodeID: obsNodeAPI, Task: "t1", CPUCores: 1.5, MemoryBytes: 100})
	e.app.RollupMetricsNow(ctx)
	got, _ = e.app.MetricsOverview(ctx, e.member, "env", domain.Range24h, obsNodeAPI)
	if now := got.Services[0].Buckets[23]; *now.CPUCores != 1 || *now.MemoryBytes != 300 || *got.Total[23].CPUCores != 1 {
		t.Fatalf("this hour after a second rollup = %+v, total %+v", now, got.Total[23])
	}
	got, _ = e.app.MetricsOverview(ctx, e.member, "env", domain.Range24h, "")
	if total := got.Total[23]; *total.CPUCores != 1.125 || *total.MemoryBytes != 310 {
		t.Fatalf("total of this hour with the worker in one tick of two = %+v, want 1.125 cores", total)
	}

	e.now += domain.MetricsRetention.Milliseconds()
	e.app.PruneMetricsNow(ctx)
	if n := e.count(t, `SELECT (SELECT COUNT(*) FROM service_metrics_hourly) + (SELECT COUNT(*) FROM environment_metrics_hourly)`); n != 0 {
		t.Fatalf("hourly rows after 7 days = %d", n)
	}
}

func TestDashboard(t *testing.T) {
	ctx := context.Background()
	e := newObsEnv(t, 1_700_000_000_000)

	panels, err := e.app.Dashboard(ctx, e.member, "env")
	if err != nil || !slices.Equal(panels, domain.DefaultPanels()) {
		t.Fatalf("default = %+v, %v", panels, err)
	}

	mine := []domain.Panel{
		{ID: "a", Kind: domain.PanelMemory, NodeID: obsNodeWorker, Wide: true},
		{ID: "b", Kind: domain.PanelLogs, Search: "timeout"},
	}
	if _, err := e.app.SaveDashboard(ctx, e.member, "env", mine); err != nil {
		t.Fatal(err)
	}
	if topics := e.pub.take("org"); !slices.Equal(topics, []string{"/api/environments/env/dashboard"}) {
		t.Fatalf("topics = %v", topics)
	}
	if panels, err = e.app.Dashboard(ctx, e.member, "env"); err != nil || !slices.Equal(panels, mine) {
		t.Fatalf("saved = %+v, %v", panels, err)
	}

	e.exec(t, `DELETE FROM nodes WHERE id = ?`, obsNodeWorker)
	panels, _ = e.app.Dashboard(ctx, e.member, "env")
	if panels[0].NodeID != "" {
		t.Fatalf("panel of a deleted service = %+v, want every service", panels[0])
	}

	tests := []struct {
		name   string
		panels []domain.Panel
		code   string
		msg    string
	}{
		{"unknown kind", []domain.Panel{{ID: "a", Kind: "disk"}}, domain.CodeInvalidInput, `Unknown panel kind "disk"`},
		{"duplicate id", []domain.Panel{{ID: "a", Kind: domain.PanelCPU}, {ID: "a", Kind: domain.PanelCPU}}, domain.CodeInvalidInput, "Every panel needs its own id"},
		{"another environment's service", []domain.Panel{{ID: "a", Kind: domain.PanelCPU, NodeID: obsNodeForeign}}, domain.CodeServiceNotFound, domain.MsgNodeNotFound},
		{"too many", slices.Repeat([]domain.Panel{{Kind: domain.PanelCPU}}, domain.MaxPanels+1), domain.CodeInvalidInput, "At most 24 panels"},
	}
	for _, tt := range tests {
		_, err := e.app.SaveDashboard(ctx, e.member, "env", tt.panels)
		obsWantCode(t, err, tt.code, tt.msg)
	}
	_, err = e.app.SaveDashboard(ctx, e.foreigner, "env", mine)
	obsWantCode(t, err, domain.CodeProjectNotFound, domain.MsgEnvironmentNotFound)
}
