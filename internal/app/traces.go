package app

// OpenTelemetry traces of an environment (convex/traces.ts, traceProviders/axiom.ts), read from
// the traces dataset of its organization's sink and joined with the log lines that name them.
// Spans get there through the OTLP relay (otlp.go) tagged with the Keel service id (tracing.go),
// which scopes requests to the environment. A trace opened by id is not scoped: the id is the
// key. Traces need a store, so there is no Docker fallback. A request is a root span.

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
	traceList       = 100                // requests per overview (the dashboard's REQUESTS)
	traceMaxSpans   = 2000               // spans per trace
	traceWindowMs   = 7 * 24 * 3_600_000 // a trace opened without `at` is looked for this far back
	traceHourMs     = 3_600_000          // with `at`, spans are looked for from the hour before it
	traceLogSlackMs = 5_000              // slack around a trace's spans when looking for its lines
	traceLogWindow  = 15 * 60_000        // without spans, how far either side of `at`
	traceLogLines   = 500                // lines per trace
	traceSearchMax  = 200                // search is cut to this many characters
	traceRoot       = `where isempty(ensure_field("parent_span_id", typeof(string)))`
	traceFailed     = `ensure_field("error", typeof(bool)) == true or ensure_field("status.code", typeof(string)) contains "error"`
	traceServiceID  = `tostring(ensure_field("resource.custom", typeof(dynamic))["keel.service_id"])`
	traceStats      = `requests = count(), errors = countif(failed), p50 = percentile(duration, 50), p95 = percentile(duration, 95), p99 = percentile(duration, 99)`
)

var traceIDRE = regexp.MustCompile(`(?i)^[0-9a-f]{16,32}$`)

// traceScope is axiomScope: the environment's sink (logs side), its traces dataset (nil on a
// sink from before traces) and the services its requests are scoped to.
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

// aplRoots is the root spans of these services (at least one id), optionally only those whose
// name or service.name contains search.
func aplRoots(dataset string, serviceIDs []string, search string) string {
	match := ""
	if term := domain.TrimJS(search); term != "" {
		match = ` | where name contains ` + aplLit(term) + ` or ensure_field("service.name", typeof(string)) contains ` + aplLit(term)
	}
	ids := make([]string, len(serviceIDs))
	for i, id := range serviceIDs {
		ids[i] = aplLit(id)
	}
	return aplDataset(dataset) + " | " + traceRoot + " | where " + traceServiceID + " in (" + strings.Join(ids, ", ") + ")" + match
}

// traceRequests is the latest limit requests in [from, to ?? now+60s], newest first, each with
// the span and error counts of its whole trace.
func (a *App) traceRequests(ctx context.Context, cfg axiomCfg, serviceIDs []string, search string, from float64, to *float64, limit int) ([]domain.TraceSummary, error) {
	if len(serviceIDs) == 0 {
		return []domain.TraceSummary{}, nil
	}
	latest, err := a.axiomRows(ctx, cfg, aplRoots(cfg.Dataset, serviceIDs, search)+" | sort by _time desc | limit "+strconv.Itoa(limit), &from, to)
	if err != nil {
		return nil, err
	}
	var ids []string
	seen := map[string]bool{}
	for _, r := range latest {
		v, _ := r.Get("trace_id")
		id := jsString(v)
		if traceIDRE.MatchString(id) && !seen[id] {
			seen[id] = true
			ids = append(ids, aplLit(id))
		}
	}
	type counts struct{ spans, errors float64 }
	perTrace := map[string]counts{}
	if len(ids) > 0 {
		// A trace's other spans start after its root, so `from` holds them; `to` might not.
		apl := aplDataset(cfg.Dataset) + " | where trace_id in (" + strings.Join(ids, ", ") + ") | extend failed = " + traceFailed +
			" | summarize spans = count(), errors = countif(failed) by trace_id"
		rows, err := a.axiomRows(ctx, cfg, apl, &from, nil)
		if err != nil {
			return nil, err
		}
		for _, r := range rows {
			id, _ := r.Get("trace_id")
			s, _ := r.Get("spans")
			e, _ := r.Get("errors")
			perTrace[jsString(id)] = counts{jsNum(s), jsNum(e)}
		}
	}
	out := make([]domain.TraceSummary, len(latest))
	for i, r := range latest {
		get := func(k string) any { v, _ := r.Get(k); return v }
		traceID := jsString(get("trace_id"))
		isErr := spanStatusOf(r) == "error"
		s := domain.TraceSummary{
			TraceID:    traceID,
			Name:       jsString(get("name")),
			Service:    jsString(rowPickValue(r, "service.name")),
			Kind:       spanKindOf(get("kind")),
			Start:      axiomTimeOf(get("_time")),
			Duration:   durationOf(get("duration")),
			HTTPStatus: spanHTTPStatus(r),
			Spans:      1,
			Error:      isErr,
			Local:      rowPickValue(r, "resource.deployment.environment.name") == "local",
		}
		if isErr {
			s.Errors = 1
		}
		if c, ok := perTrace[traceID]; ok {
			s.Spans, s.Errors = c.spans, c.errors
		}
		out[i] = s
	}
	return out, nil
}

// TraceOverview is request rate, errors and latency over rng, and the latest requests, of the
// environment's services or of one of them (nodeID, for `keel traces <service>`).
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
	search = jsSlice(search, traceSearchMax)
	matching := aplRoots(cfg.Dataset, ids, search)

	totals, series := []*JSONObject{}, []*JSONObject{}
	traces := []domain.TraceSummary{}
	// An environment with no services has no requests; `in ()` is not valid APL.
	if len(ids) > 0 {
		var wg sync.WaitGroup
		var errs [3]error
		wg.Add(3)
		go func() {
			defer wg.Done()
			totals, errs[0] = a.axiomRows(ctx, cfg, matching+" | extend failed = "+traceFailed+" | summarize "+traceStats, &from, &to)
		}()
		go func() {
			defer wg.Done()
			series, errs[1] = a.axiomRows(ctx, cfg, matching+" | extend failed = "+traceFailed+" | summarize "+traceStats+" by bin(_time, "+spec.Bin+")", &from, &to)
		}()
		go func() {
			defer wg.Done()
			traces, errs[2] = a.traceRequests(ctx, cfg, ids, search, from, nil, traceList)
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
		t, ok := r.Get("_time")
		if !ok || t == nil {
			if keys := r.Keys(); len(keys) > 0 {
				t, _ = r.Get(keys[0])
			}
		}
		bucket := math.Floor(axiomTimeOf(t)/binMs) * binMs
		byBucket[bucket] = traceStatsOf(r)
	}
	buckets := make([]domain.TraceBucket, count)
	for i := range buckets {
		t := from + float64(i)*binMs
		buckets[i] = domain.TraceBucket{Time: t, TraceStats: byBucket[t]}
	}
	stats := domain.TraceStats{}
	if len(totals) > 0 {
		stats = traceStatsOf(totals[0])
	}
	return domain.TraceOverview{
		Source: domain.LogSourceAxiom, From: from, To: to, BucketMs: binMs,
		Stats: stats, Buckets: buckets, Traces: traces,
	}, nil
}

// GetTrace is every span of one trace and the environment's log lines that name its id. at: a
// moment inside the trace if known (its root's start, or the time of the line it was opened
// from); 0 = unknown.
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
	now := float64(a.Now())
	spans := []domain.Span{}
	if scope.traces != nil {
		since := now - traceWindowMs
		if at != 0 {
			since = at - traceHourMs
		}
		rows, err := a.axiomRows(ctx, *scope.traces, aplDataset(scope.traces.Dataset)+" | where trace_id == "+aplLit(id)+
			" | sort by _time asc | limit "+strconv.Itoa(traceMaxSpans), &since, nil)
		if err != nil {
			return domain.Trace{}, obsInvalid(err)
		}
		for _, r := range rows {
			spans = append(spans, axiomSpanOf(r))
		}
	}
	// The lines can only have been written while the trace ran: look there when its spans say
	// when that was (stretched to at, so the line it was opened from is always found), else
	// around at, else over the last week like the spans.
	var from float64
	var to *float64
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
		from, to = lo-traceLogSlackMs, obsF64(hi+traceLogSlackMs)
	case at != 0:
		from, to = at-traceLogWindow, obsF64(at+traceLogWindow)
	default:
		from = now - traceWindowMs
	}
	lines, err := a.axiomLines(ctx, scope.logs, scope.serviceIDs, linesQuery{N: traceLogLines, Search: id, From: &from, To: to, OldestFirst: true})
	if err != nil {
		return domain.Trace{}, obsInvalid(err)
	}
	return domain.Trace{Source: domain.LogSourceAxiom, TraceID: id, Spans: spans, Logs: lines}, nil
}

// TracesAround is the requests that started within 30 s either side of at, newest first: the
// traces near a log line. Empty without a traces dataset.
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
	out, err := a.traceRequests(ctx, *scope.traces, scope.serviceIDs, "", at-logsAroundMs, obsF64(at+logsAroundMs), traceList)
	if err != nil {
		return nil, obsInvalid(err)
	}
	return out, nil
}
