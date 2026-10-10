package app

import (
	"cmp"
	"context"
	"math"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/ThallesP/keel/internal/domain"
)

const traceFailed = `ensure_field("error", typeof(bool)) == true or ensure_field("status.code", typeof(string)) contains "error"`

var traceIDRE = regexp.MustCompile(`(?i)^[0-9a-f]{16,32}$`)

func aplRoots(dataset string, serviceIDs []string, search string) string {
	match := ""
	if term := strings.TrimSpace(search); term != "" {
		match = ` | where name contains ` + aplLit(term) + ` or ensure_field("service.name", typeof(string)) contains ` + aplLit(term)
	}
	return aplDataset(dataset) + ` | where isempty(ensure_field("parent_span_id", typeof(string)))` +
		` | where tostring(ensure_field("resource.custom", typeof(dynamic))["keel.service_id"]) ` + aplIn(serviceIDs) + match
}

func (a *App) traceRequests(ctx context.Context, cfg axiomCfg, serviceIDs []string, search string, from, to float64) ([]domain.TraceSummary, error) {
	if len(serviceIDs) == 0 {
		return []domain.TraceSummary{}, nil
	}
	latest, err := a.axiomRows(ctx, cfg, aplRoots(cfg.Dataset, serviceIDs, search)+" | sort by _time desc | limit 100", from, to)
	if err != nil {
		return nil, err
	}
	out := readRows(a.Log, latest, axiomTraceSummaryOf)
	var ids []string
	for _, summary := range out {
		if traceIDRE.MatchString(summary.TraceID) && !slices.Contains(ids, summary.TraceID) {
			ids = append(ids, summary.TraceID)
		}
	}
	if len(ids) == 0 {
		return out, nil
	}
	apl := aplDataset(cfg.Dataset) + " | where trace_id " + aplIn(ids) + " | extend failed = " + traceFailed +
		" | summarize spans = count(), errors = countif(failed) by trace_id"
	counts, err := axiomRowsAs[axiomTraceCountRow](ctx, a, cfg, apl, from, a.axiomUntil())
	if err != nil {
		return nil, err
	}
	perTrace := map[string]axiomTraceCountRow{}
	for _, c := range counts {
		perTrace[c.TraceID] = c
	}
	for i := range out {
		if c, ok := perTrace[out[i].TraceID]; ok {
			out[i].Spans, out[i].Errors = c.Spans, c.Errors
		}
	}
	return out, nil
}

func (a *App) TraceOverview(ctx context.Context, actor domain.Actor, environmentID string, rng domain.TimeRange, search, nodeID string) (domain.TraceOverview, error) {
	spec, ok := rng.Spec()
	if !ok {
		return domain.TraceOverview{}, errBadRange
	}
	scope, err := a.envSinkScope(ctx, actor, environmentID, errNoSink)
	if err != nil {
		return domain.TraceOverview{}, err
	}
	if scope.Sink.Traces == "" {
		return domain.TraceOverview{}, errNoTraces
	}
	ids := scope.ServiceIDs
	if nodeID != "" {
		if !slices.Contains(ids, nodeID) {
			return domain.TraceOverview{}, domain.E(domain.CodeServiceNotFound, domain.MsgNodeNotFound)
		}
		ids = []string{nodeID}
	}
	cfg := axiomCfgOf(scope.Sink, scope.Sink.Traces)
	fromMs, toMs := spec.Window(a.Now())
	from, to := float64(fromMs), float64(toMs)
	search = truncateRunes(search, 200)
	statsAPL := aplRoots(cfg.Dataset, ids, search) + " | extend failed = " + traceFailed +
		" | summarize requests = count(), errors = countif(failed), p50 = percentile(duration, 50), p95 = percentile(duration, 95), p99 = percentile(duration, 99)"

	var totals, series []axiomStatsRow
	traces := []domain.TraceSummary{}
	if len(ids) > 0 {
		var wg sync.WaitGroup
		var errs [3]error
		wg.Go(func() { totals, errs[0] = axiomRowsAs[axiomStatsRow](ctx, a, cfg, statsAPL, from, to) })
		wg.Go(func() {
			series, errs[1] = axiomRowsAs[axiomStatsRow](ctx, a, cfg, statsAPL+" by bin(_time, "+spec.Bin+")", from, to)
		})
		wg.Go(func() { traces, errs[2] = a.traceRequests(ctx, cfg, ids, search, from, a.axiomUntil()) })
		wg.Wait()
		if err := cmp.Or(errs[:]...); err != nil {
			return domain.TraceOverview{}, obsInvalid(err)
		}
	}

	byBucket := map[int64]domain.TraceStats{}
	for _, r := range series {
		byBucket[int64(r.Time)/spec.BinMs*spec.BinMs] = r.stats()
	}
	buckets := make([]domain.TraceBucket, spec.Buckets)
	for i := range buckets {
		t := fromMs + int64(i)*spec.BinMs
		buckets[i] = domain.TraceBucket{Time: float64(t), TraceStats: byBucket[t]}
	}
	stats := domain.TraceStats{}
	if len(totals) > 0 {
		stats = totals[0].stats()
	}
	return domain.TraceOverview{
		Source: domain.LogSourceAxiom, From: from, To: to, BucketMs: float64(spec.BinMs),
		Stats: stats, Buckets: buckets, Traces: traces,
	}, nil
}

func (a *App) GetTrace(ctx context.Context, actor domain.Actor, environmentID, traceID string, at float64) (domain.Trace, error) {
	if !traceIDRE.MatchString(traceID) {
		return domain.Trace{}, domain.Invalid("Not a trace id")
	}
	id := strings.ToLower(traceID)
	if math.IsNaN(at) || math.IsInf(at, 0) {
		at = 0
	}
	scope, err := a.envSinkScope(ctx, actor, environmentID, errNoSink)
	if err != nil {
		return domain.Trace{}, err
	}
	from, to := float64(a.Now()-(7*24*time.Hour).Milliseconds()), a.axiomUntil()
	spans := []domain.Span{}
	if scope.Sink.Traces != "" {
		since := from
		if at != 0 {
			since = at - float64(time.Hour.Milliseconds())
		}
		cfg := axiomCfgOf(scope.Sink, scope.Sink.Traces)
		rows, err := a.axiomRows(ctx, cfg, aplDataset(cfg.Dataset)+" | where trace_id == "+aplLit(id)+" | sort by _time asc | limit 2000", since, to)
		if err != nil {
			return domain.Trace{}, obsInvalid(err)
		}
		spans = readRows(a.Log, rows, axiomSpanOf)
	}
	switch {
	case len(spans) > 0:
		lo, hi := math.Inf(1), math.Inf(-1)
		if at != 0 {
			lo, hi = at, at
		}
		for _, s := range spans {
			lo, hi = min(lo, s.Start), max(hi, s.Start+s.Duration)
		}
		from, to = lo-5_000, hi+5_000
	case at != 0:
		from, to = at-15*60_000, at+15*60_000
	}
	lines, err := a.axiomLines(ctx, axiomCfgOf(scope.Sink, scope.Sink.Dataset), scope.ServiceIDs, linesQuery{N: 500, Search: id, From: from, To: to, OldestFirst: true})
	if err != nil {
		return domain.Trace{}, obsInvalid(err)
	}
	return domain.Trace{Source: domain.LogSourceAxiom, TraceID: id, Spans: spans, Logs: lines}, nil
}

func (a *App) TracesAround(ctx context.Context, actor domain.Actor, environmentID string, at float64) ([]domain.TraceSummary, error) {
	if err := validLogMoment(at); err != nil {
		return nil, err
	}
	scope, err := a.envSinkScope(ctx, actor, environmentID, errNoSink)
	if err != nil {
		return nil, err
	}
	if scope.Sink.Traces == "" {
		return []domain.TraceSummary{}, nil
	}
	out, err := a.traceRequests(ctx, axiomCfgOf(scope.Sink, scope.Sink.Traces), scope.ServiceIDs, "", at-logsAroundMs, at+logsAroundMs)
	if err != nil {
		return nil, obsInvalid(err)
	}
	return out, nil
}
