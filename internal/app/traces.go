package app

import (
	"context"
	"math"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"

	"github.com/ThallesP/keel/internal/domain"
)

const (
	traceList       = 100
	traceMaxSpans   = 2000
	traceWindowMs   = 7 * 24 * 3_600_000
	traceHourMs     = 3_600_000
	traceLogSlackMs = 5_000
	traceLogWindow  = 15 * 60_000
	traceLogLines   = 500
	traceSearchMax  = 200
	traceRoot       = `where isempty(ensure_field("parent_span_id", typeof(string)))`
	traceFailed     = `ensure_field("error", typeof(bool)) == true or ensure_field("status.code", typeof(string)) contains "error"`
	traceServiceID  = `tostring(ensure_field("resource.custom", typeof(dynamic))["keel.service_id"])`
	traceStats      = `requests = count(), errors = countif(failed), p50 = percentile(duration, 50), p95 = percentile(duration, 95), p99 = percentile(duration, 99)`
)

var traceIDRE = regexp.MustCompile(`(?i)^[0-9a-f]{16,32}$`)

type traceScope struct {
	logs       axiomCfg
	traces     *axiomCfg
	serviceIDs []string
}

func (a *App) traceScope(ctx context.Context, actor domain.Actor, environmentID string) (traceScope, error) {
	s, err := a.envSinkScope(ctx, actor, environmentID)
	if err != nil {
		return traceScope{}, err
	}
	if s.Sink == nil || s.Sink.Kind != domain.SinkKindAxiom {
		return traceScope{}, errTracesOff(msgNoSink)
	}
	out := traceScope{logs: axiomLogsCfg(*s.Sink), serviceIDs: s.ServiceIDs}
	if s.Sink.Traces != "" {
		t := out.logs
		t.Dataset = s.Sink.Traces
		out.traces = &t
	}
	return out, nil
}

func aplRoots(dataset string, serviceIDs []string, search string) string {
	match := ""
	if term := strings.TrimSpace(search); term != "" {
		match = ` | where name contains ` + aplLit(term) + ` or ensure_field("service.name", typeof(string)) contains ` + aplLit(term)
	}
	ids := make([]string, len(serviceIDs))
	for i, id := range serviceIDs {
		ids[i] = aplLit(id)
	}
	return aplDataset(dataset) + " | " + traceRoot + " | where " + traceServiceID + " in (" + strings.Join(ids, ", ") + ")" + match
}

func (a *App) traceRequests(ctx context.Context, cfg axiomCfg, serviceIDs []string, search string, from, to float64, limit int) ([]domain.TraceSummary, error) {
	if len(serviceIDs) == 0 {
		return []domain.TraceSummary{}, nil
	}
	latest, err := a.axiomRows(ctx, cfg, aplRoots(cfg.Dataset, serviceIDs, search)+" | sort by _time desc | limit "+strconv.Itoa(limit), from, to)
	if err != nil {
		return nil, err
	}
	out := readRows(a.Log, latest, axiomTraceSummaryOf)
	var ids []string
	seen := map[string]bool{}
	for _, summary := range out {
		if traceIDRE.MatchString(summary.TraceID) && !seen[summary.TraceID] {
			seen[summary.TraceID] = true
			ids = append(ids, aplLit(summary.TraceID))
		}
	}
	if len(ids) == 0 {
		return out, nil
	}
	apl := aplDataset(cfg.Dataset) + " | where trace_id in (" + strings.Join(ids, ", ") + ") | extend failed = " + traceFailed +
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
		return domain.TraceOverview{}, domain.Invalid("Range must be one of 15m, 1h, 24h, 7d")
	}
	scope, err := a.traceScope(ctx, actor, environmentID)
	if err != nil {
		return domain.TraceOverview{}, err
	}
	if scope.traces == nil {
		return domain.TraceOverview{}, errTracesOff(msgNoTraces)
	}
	ids := scope.serviceIDs
	if nodeID != "" {
		if !slices.Contains(scope.serviceIDs, nodeID) {
			return domain.TraceOverview{}, domain.E(domain.CodeServiceNotFound, domain.MsgNodeNotFound)
		}
		ids = []string{nodeID}
	}
	cfg := *scope.traces
	fromMs, toMs, count := domain.RangeWindow(rng, a.Now())
	from, to := float64(fromMs), float64(toMs)
	search = truncateRunes(search, traceSearchMax)
	matching := aplRoots(cfg.Dataset, ids, search)

	var totals, series []axiomStatsRow
	traces := []domain.TraceSummary{}
	if len(ids) > 0 {
		var wg sync.WaitGroup
		var errs [3]error
		wg.Add(3)
		go func() {
			defer wg.Done()
			totals, errs[0] = axiomRowsAs[axiomStatsRow](ctx, a, cfg, matching+" | extend failed = "+traceFailed+" | summarize "+traceStats, from, to)
		}()
		go func() {
			defer wg.Done()
			series, errs[1] = axiomRowsAs[axiomStatsRow](ctx, a, cfg, matching+" | extend failed = "+traceFailed+" | summarize "+traceStats+" by bin(_time, "+spec.Bin+")", from, to)
		}()
		go func() {
			defer wg.Done()
			traces, errs[2] = a.traceRequests(ctx, cfg, ids, search, from, a.axiomUntil(), traceList)
		}()
		wg.Wait()
		for _, err := range errs {
			if err != nil {
				return domain.TraceOverview{}, obsInvalid(err)
			}
		}
	}

	binMs := float64(spec.BinMs)
	byBucket := map[float64]domain.TraceStats{}
	for _, r := range series {
		bucket := math.Floor(float64(r.Time)/binMs) * binMs
		byBucket[bucket] = r.stats()
	}
	buckets := make([]domain.TraceBucket, count)
	for i := range buckets {
		t := from + float64(i)*binMs
		buckets[i] = domain.TraceBucket{Time: t, TraceStats: byBucket[t]}
	}
	stats := domain.TraceStats{}
	if len(totals) > 0 {
		stats = totals[0].stats()
	}
	return domain.TraceOverview{
		Source: domain.LogSourceAxiom, From: from, To: to, BucketMs: binMs,
		Stats: stats, Buckets: buckets, Traces: traces,
	}, nil
}

func (a *App) GetTrace(ctx context.Context, actor domain.Actor, environmentID, traceID string, at float64) (domain.Trace, error) {
	if !traceIDRE.MatchString(traceID) {
		return domain.Trace{}, domain.Invalid(msgNotATraceID)
	}
	id := strings.ToLower(traceID)
	if math.IsNaN(at) || math.IsInf(at, 0) {
		at = 0
	}
	scope, err := a.traceScope(ctx, actor, environmentID)
	if err != nil {
		return domain.Trace{}, err
	}
	from, to := float64(a.Now())-traceWindowMs, a.axiomUntil()
	spans := []domain.Span{}
	if scope.traces != nil {
		since := from
		if at != 0 {
			since = at - traceHourMs
		}
		rows, err := a.axiomRows(ctx, *scope.traces, aplDataset(scope.traces.Dataset)+" | where trace_id == "+aplLit(id)+
			" | sort by _time asc | limit "+strconv.Itoa(traceMaxSpans), since, to)
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
			lo = math.Min(lo, s.Start)
			hi = math.Max(hi, s.Start+s.Duration)
		}
		from, to = lo-traceLogSlackMs, hi+traceLogSlackMs
	case at != 0:
		from, to = at-traceLogWindow, at+traceLogWindow
	}
	lines, err := a.axiomLines(ctx, scope.logs, scope.serviceIDs, linesQuery{N: traceLogLines, Search: id, From: from, To: to, OldestFirst: true})
	if err != nil {
		return domain.Trace{}, obsInvalid(err)
	}
	return domain.Trace{Source: domain.LogSourceAxiom, TraceID: id, Spans: spans, Logs: lines}, nil
}

func (a *App) TracesAround(ctx context.Context, actor domain.Actor, environmentID string, at float64) ([]domain.TraceSummary, error) {
	if err := validLogMoment(at); err != nil {
		return nil, err
	}
	scope, err := a.traceScope(ctx, actor, environmentID)
	if err != nil {
		return nil, err
	}
	if scope.traces == nil {
		return []domain.TraceSummary{}, nil
	}
	out, err := a.traceRequests(ctx, *scope.traces, scope.serviceIDs, "", at-logsAroundMs, at+logsAroundMs, traceList)
	if err != nil {
		return nil, obsInvalid(err)
	}
	return out, nil
}
