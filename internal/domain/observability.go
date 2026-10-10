package domain

import (
	"math"
	"regexp"
)

const SinkKindAxiom = "axiom"

type LogSink struct {
	Kind    string `json:"kind"`
	Domain  string `json:"domain"`
	Dataset string `json:"dataset"`
	Traces  string `json:"traces,omitempty"`
	Token   string `json:"token"`
	Org     string `json:"org,omitempty"`
}

type AxiomOrg struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Domain      string `json:"domain"`
	MaxDatasets int    `json:"maxDatasets,omitempty"`
}

var AxiomDomains = []string{"api.axiom.co", "api.eu.axiom.co"}

const (
	DatasetLogs   = "keel-logs"
	DatasetTraces = "keel-traces"
)

var axiomDatasetRE = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9._-]{0,127}$`)

func ValidDataset(name string) bool { return axiomDatasetRE.MatchString(name) }

func TokenHint(token string) string { return "…" + obsLastChars(token, 4) }

const OTLPKeyPrefix = "keel_otlp_"

func MaskOTLPKey(key string) string {
	return OTLPKeyPrefix + "…" + obsLastChars(key, 4)
}

func obsLastChars(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[len(r)-n:])
}

type TimeRange string

const (
	Range15m TimeRange = "15m"
	Range1h  TimeRange = "1h"
	Range24h TimeRange = "24h"
	Range7d  TimeRange = "7d"
)

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

func (r TimeRange) Spec() (RangeSpec, bool) {
	s, ok := timeRanges[r]
	return s, ok
}

func (r TimeRange) Valid() bool { _, ok := timeRanges[r]; return ok }

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

const (
	LogSourceDocker = "docker"
	LogSourceAxiom  = "axiom"
)

type ServiceLogLine struct {
	Time   float64 `json:"time" doc:"Epoch ms, fractional; 0 when the line had no stamp"`
	Text   string  `json:"text"`
	Stream string  `json:"stream" enum:"stdout,stderr"`
	Task   string  `json:"task" doc:"Swarm task id; empty when unknown"`
}

type LogReplica struct {
	Task  string `json:"task"`
	Slot  int    `json:"slot"`
	State string `json:"state"`
}

type LogTail struct {
	Source   string           `json:"source" enum:"docker,axiom"`
	Lines    []ServiceLogLine `json:"lines"`
	Replicas []LogReplica     `json:"replicas"`
}

type EnvironmentLogLine struct {
	ServiceLogLine
	ServiceID string `json:"serviceId" doc:"Node id"`
}

type EnvironmentLogs struct {
	Source string               `json:"source" enum:"docker,axiom"`
	Lines  []EnvironmentLogLine `json:"lines"`
}

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

type TraceStats struct {
	Requests float64  `json:"requests"`
	Errors   float64  `json:"errors"`
	P50      *float64 `json:"p50" nullable:"true"`
	P95      *float64 `json:"p95" nullable:"true"`
	P99      *float64 `json:"p99" nullable:"true"`
}

type TraceBucket struct {
	Time float64 `json:"time"`
	TraceStats
}

type TraceOverview struct {
	Source   string         `json:"source" enum:"axiom"`
	From     float64        `json:"from"`
	To       float64        `json:"to"`
	BucketMs float64        `json:"bucketMs"`
	Stats    TraceStats     `json:"stats"`
	Buckets  []TraceBucket  `json:"buckets" doc:"Every bucket, oldest first, empty ones included"`
	Traces   []TraceSummary `json:"traces" doc:"Newest first, at most 100"`
}

type Trace struct {
	Source  string               `json:"source" enum:"axiom"`
	TraceID string               `json:"traceId"`
	Spans   []Span               `json:"spans"`
	Logs    []EnvironmentLogLine `json:"logs"`
}

const (
	TracesOff = "off"
	TracesOld = "old"
	TracesOn  = "on"
)

type TracingEnvVar struct {
	Key        string `json:"key"`
	Value      string `json:"value"`
	Secret     bool   `json:"secret"`
	Overridden bool   `json:"overridden" doc:"The service's own variables replace this one"`
}

type TracingView struct {
	_       struct{}        `nullable:"true"`
	Enabled bool            `json:"enabled"`
	Traces  string          `json:"traces" enum:"off,old,on"`
	Env     []TracingEnvVar `json:"env"`
}

type LogSinkView struct {
	_         struct{} `nullable:"true"`
	Kind      string   `json:"kind" enum:"axiom"`
	Domain    string   `json:"domain"`
	Dataset   string   `json:"dataset"`
	Traces    *string  `json:"traces" nullable:"true"`
	Org       *string  `json:"org" nullable:"true"`
	TokenHint string   `json:"tokenHint" doc:"… and the token's last 4 characters"`
}

type AxiomOrgChoice struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}
