package domain

import (
	"math"
	"regexp"
)

// Observability: log sinks, the log and trace read models, the tracing switch
// (docs/go/spec/observability.md). The read-model types below are value objects with their JSON
// shape: api/observability.go re-exports them, so the dashboard, the CLI and the server agree on
// one definition. Times are epoch milliseconds and durations milliseconds, both fractional
// (float64), as the TypeScript providers returned them.

// SinkKindAxiom is the only sink kind today. No sink = the Docker default (nothing shipped).
const SinkKindAxiom = "axiom"

// LogSink is where an organization's container logs and traces go (§1.6). Token is a secret: it
// leaves the server only to the agent (bearer-protected /worker/config).
type LogSink struct {
	Kind    string `json:"kind"`
	Domain  string `json:"domain"`           // api.axiom.co | api.eu.axiom.co | a full origin (local mock)
	Dataset string `json:"dataset"`          // logs dataset
	Traces  string `json:"traces,omitempty"` // traces dataset; "" on sinks connected before traces
	Token   string `json:"token"`
	Org     string `json:"org,omitempty"` // Axiom org name, display only; set by Sign in with Axiom
}

// AxiomOrg is an Axiom organization a personal token can see (§1.6).
type AxiomOrg struct {
	ID          string   `json:"id"`
	Name        string   `json:"name"`
	Domain      string   `json:"domain"` // API host its data lives on
	MaxDatasets *float64 `json:"maxDatasets,omitempty"`
}

// AxiomDomains are the regions a pasted sink may name.
var AxiomDomains = []string{"api.axiom.co", "api.eu.axiom.co"}

// Datasets Sign in with Axiom provisions (shared by every project of the organization).
const (
	DatasetLogs   = "keel-logs"
	DatasetTraces = "keel-traces"
)

var axiomDatasetRE = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9._-]{0,127}$`)

// ValidDataset: Axiom dataset names (letters, digits, - _ .; not starting with . or -).
func ValidDataset(name string) bool { return axiomDatasetRE.MatchString(name) }

// TokenHint is "…" + the token's last 4 characters: all the UI ever sees of a sink token.
func TokenHint(token string) string { return "…" + obsLastChars(token, 4) }

// OTLPKeyPrefix starts every OTLP relay ingest key.
const OTLPKeyPrefix = "keel_otlp_"

// MaskOTLPKey is how tracing views show an ingest key: keel_otlp_…<last 4>, or keel_otlp_… when
// the environment has none yet.
func MaskOTLPKey(key string) string {
	if key == "" {
		return OTLPKeyPrefix + "…"
	}
	return OTLPKeyPrefix + "…" + obsLastChars(key, 4)
}

// obsLastChars is JS s.slice(-n) for the ASCII strings tokens are.
func obsLastChars(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[len(r)-n:])
}

// ── Time ranges (convex/timeRange.ts) ────────────────────────────────────────────────────────

// TimeRange is the Observability page's range.
type TimeRange string

const (
	Range15m TimeRange = "15m"
	Range1h  TimeRange = "1h"
	Range24h TimeRange = "24h"
	Range7d  TimeRange = "7d"
)

// RangeSpec: the range's length and the bucket the charts use (APL bin literal and its ms).
type RangeSpec struct {
	Ms    int64
	Bin   string
	BinMs int64
}

const rangeMinuteMs, rangeHourMs = 60_000, 3_600_000

var timeRanges = map[TimeRange]RangeSpec{
	Range15m: {Ms: 15 * rangeMinuteMs, Bin: "30s", BinMs: 30_000},
	Range1h:  {Ms: rangeHourMs, Bin: "2m", BinMs: 2 * rangeMinuteMs},
	Range24h: {Ms: 24 * rangeHourMs, Bin: "1h", BinMs: rangeHourMs},
	Range7d:  {Ms: 7 * 24 * rangeHourMs, Bin: "6h", BinMs: 6 * rangeHourMs},
}

// Spec is the range's spec; ok=false for an unknown range.
func (r TimeRange) Spec() (RangeSpec, bool) {
	s, ok := timeRanges[r]
	return s, ok
}

func (r TimeRange) Valid() bool { _, ok := timeRanges[r]; return ok }

// RangeWindow is the window a range covers, aligned like APL's bin(): count buckets, the last
// one holding now (timeRange.ts rangeWindow).
func RangeWindow(r TimeRange, now int64) (from, to int64, count int) {
	s := timeRanges[r]
	count = int(math.Round(float64(s.Ms) / float64(s.BinMs)))
	from = rangeFloorDiv(now, s.BinMs)*s.BinMs - int64(count-1)*s.BinMs
	return from, from + int64(count)*s.BinMs, count
}

func rangeFloorDiv(a, b int64) int64 {
	q := a / b
	if (a%b != 0) && ((a < 0) != (b < 0)) {
		q--
	}
	return q
}

// ── Log read model (logProviders/types.ts) ───────────────────────────────────────────────────

// Log sources.
const (
	LogSourceDocker = "docker"
	LogSourceAxiom  = "axiom"
)

// ServiceLogLine is one container line of a service (TS LogLine). Task is "" when unknown.
type ServiceLogLine struct {
	Time   float64 `json:"time" doc:"Epoch ms, fractional; 0 when the line had no stamp"`
	Text   string  `json:"text"`
	Stream string  `json:"stream" enum:"stdout,stderr"`
	Task   string  `json:"task" doc:"Swarm task id; empty when unknown"`
}

// LogReplica is one Swarm task of the service (TS Replica). State is "" from Axiom.
type LogReplica struct {
	Task  string `json:"task"`
	Slot  int    `json:"slot"`
	State string `json:"state"`
}

// LogTail is the last lines of one service (TS Tail; logs.tail).
type LogTail struct {
	Source   string           `json:"source" enum:"docker,axiom"`
	Lines    []ServiceLogLine `json:"lines"`
	Replicas []LogReplica     `json:"replicas"`
}

// EnvironmentLogLine is a line of any service of an environment (TS ProjectLine).
type EnvironmentLogLine struct {
	ServiceLogLine
	ServiceID string `json:"serviceId" doc:"Node id"`
}

// EnvironmentLogs is the Observability stream's lines (TS ProjectTail; logs.recent).
type EnvironmentLogs struct {
	Source string               `json:"source" enum:"docker,axiom"`
	Lines  []EnvironmentLogLine `json:"lines"`
}

// ── Trace read model (traceProviders/types.ts) ───────────────────────────────────────────────

type Attribute struct {
	Key   string `json:"key"`
	Value string `json:"value"`
}

type SpanEvent struct {
	Time       float64     `json:"time"`
	Name       string      `json:"name"`
	Attributes []Attribute `json:"attributes"`
}

type Span struct {
	SpanID        string      `json:"spanId"`
	ParentID      string      `json:"parentId" doc:"Empty for a root span"`
	Name          string      `json:"name"`
	Service       string      `json:"service" doc:"service.name"`
	Kind          string      `json:"kind" doc:"server, client, internal, producer, consumer or empty"`
	Start         float64     `json:"start"`
	Duration      float64     `json:"duration"`
	Status        string      `json:"status" enum:"ok,error,unset"`
	StatusMessage string      `json:"statusMessage"`
	Scope         string      `json:"scope" doc:"scope.name"`
	Attributes    []Attribute `json:"attributes"`
	Resource      []Attribute `json:"resource"`
	Events        []SpanEvent `json:"events"`
}

// TraceSummary is one request (a root span) with its trace's span and error counts.
type TraceSummary struct {
	TraceID    string   `json:"traceId"`
	Name       string   `json:"name"`
	Service    string   `json:"service"`
	Kind       string   `json:"kind"`
	Start      float64  `json:"start"`
	Duration   float64  `json:"duration"`
	HTTPStatus *float64 `json:"httpStatus" nullable:"true"`
	Spans      float64  `json:"spans"`
	Errors     float64  `json:"errors"`
	Error      bool     `json:"error"`
	Local      bool     `json:"local" doc:"Sent by keel run (deployment.environment.name=local)"`
}

// TraceStats: requests, errors and root-span latency percentiles (ms; null with no requests).
type TraceStats struct {
	Requests float64  `json:"requests"`
	Errors   float64  `json:"errors"`
	P50      *float64 `json:"p50" nullable:"true"`
	P95      *float64 `json:"p95" nullable:"true"`
	P99      *float64 `json:"p99" nullable:"true"`
}

// TraceBucket is TraceStats for one bucket starting at Time.
type TraceBucket struct {
	Time float64 `json:"time"`
	TraceStats
}

// TraceOverview backs the Observability KPIs, charts and request list (traces.overview).
type TraceOverview struct {
	Source   string         `json:"source" enum:"axiom"`
	From     float64        `json:"from"`
	To       float64        `json:"to"`
	BucketMs float64        `json:"bucketMs"`
	Stats    TraceStats     `json:"stats"`
	Buckets  []TraceBucket  `json:"buckets" doc:"Every bucket, oldest first, empty ones included"`
	Traces   []TraceSummary `json:"traces" doc:"Newest first, at most 100"`
}

// Trace is one trace's spans and the environment's lines that name it (traces.get).
type Trace struct {
	Source  string               `json:"source" enum:"axiom"`
	TraceID string               `json:"traceId"`
	Spans   []Span               `json:"spans"`
	Logs    []EnvironmentLogLine `json:"logs"`
}

// ── Tracing switch ───────────────────────────────────────────────────────────────────────────

// Traces store states of an organization: no sink, a sink from before traces, or yes.
const (
	TracesOff = "off"
	TracesOld = "old"
	TracesOn  = "on"
)

// TracingEnvVar is one OTEL_* variable as a service's tracing view shows it.
type TracingEnvVar struct {
	Key        string `json:"key"`
	Value      string `json:"value"`
	Secret     bool   `json:"secret"`
	Overridden bool   `json:"overridden" doc:"The service's own variables replace this one"`
}

// TracingView is a service's tracing (tracing.forNode). The ingest key is masked.
type TracingView struct {
	_       struct{}        `nullable:"true"` // reads return it or null
	Enabled bool            `json:"enabled"`
	Traces  string          `json:"traces" enum:"off,old,on"`
	Env     []TracingEnvVar `json:"env"`
}

// LogSinkView is the organization's sink as the UI may see it: never the token.
type LogSinkView struct {
	_         struct{} `nullable:"true"` // reads return it or null
	Kind      string   `json:"kind" enum:"axiom"`
	Domain    string   `json:"domain"`
	Dataset   string   `json:"dataset"`
	Traces    *string  `json:"traces" nullable:"true"`
	Org       *string  `json:"org" nullable:"true"`
	TokenHint string   `json:"tokenHint" doc:"… and the token's last 4 characters"`
}

// AxiomOrgChoice is an org the user can pick after Sign in with Axiom (names only).
type AxiomOrgChoice struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}
