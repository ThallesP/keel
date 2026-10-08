package client

import (
	"encoding/json"
	"time"

	"github.com/ThallesP/keel/internal/api"
)

// The CLI's types are what it prints, so their JSON names are the CLI's contract (docs/cli.md),
// not the API's. Where the API's wire type already has the same JSON, it is used as is.
type (
	User         = api.User
	Organization = api.Organization
	Project      = api.ProjectSummary
	Environment  = api.ProjectEnvironment
	// Summary is an environment's staged changes, status counts and servers.
	Summary  = api.EnvironmentSummary
	Variable = api.VariableView
	// TracingVar is one OTEL_* variable a service's tracing sets.
	TracingVar = api.TracingEnvVar
)

// Service is any canvas node but a group: services, databases, caches, volumes.
type Service struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Type      string `json:"type"`
	Status    string `json:"status"`
	Image     string `json:"image,omitempty"`
	Port      int    `json:"port,omitempty"`
	Replicas  int    `json:"replicas"`
	Running   int    `json:"running"`
	Staged    bool   `json:"staged"`
	PublicURL string `json:"publicUrl,omitempty"`
	Error     string `json:"error,omitempty"`
}

func serviceOf(n api.NodeView) Service {
	s := Service{
		ID: n.ID, Name: n.Name, Type: n.Type, Status: n.Status, Image: n.Image,
		Replicas: n.Replicas, Running: n.Running, Staged: n.Dirty, PublicURL: n.PublicURL, Error: n.Error,
	}
	if n.Port != nil {
		s.Port = *n.Port
	}
	return s
}

type Tail struct {
	Source string    `json:"source"`
	Lines  []LogLine `json:"lines"`
}

type LogLine struct {
	Time   Time   `json:"time"`
	Stream string `json:"stream"`
	Task   string `json:"task,omitempty"`
	Text   string `json:"text"`
}

// TraceSummary is one request: a trace's root span, with counts over the whole trace.
type TraceSummary struct {
	TraceID    string  `json:"traceId"`
	Name       string  `json:"name"`
	Service    string  `json:"service"`
	Start      Time    `json:"start"`
	DurationMs float64 `json:"durationMs"`
	HTTPStatus *int    `json:"httpStatus"`
	Spans      int     `json:"spans"`
	Errors     int     `json:"errors"`
	Error      bool    `json:"error"`
	// From a `keel run` on someone's machine, not a deploy.
	Local bool `json:"local"`
}

// TraceStats counts requests (root spans) over a range; percentiles are null with none.
type TraceStats struct {
	Requests int      `json:"requests"`
	Errors   int      `json:"errors"`
	P50Ms    *float64 `json:"p50Ms"`
	P95Ms    *float64 `json:"p95Ms"`
	P99Ms    *float64 `json:"p99Ms"`
}

type Traces struct {
	Stats TraceStats `json:"stats"`
	// Newest first, at most 100.
	Traces []TraceSummary `json:"traces"`
}

// Tracing is a service's tracing switch and the OTEL_* variables it gives the service.
type Tracing struct {
	Enabled bool `json:"enabled"`
	// off: no Axiom sink; old: a sink from before traces; on: spans have somewhere to go.
	Store string       `json:"store"`
	Env   []TracingVar `json:"env"`
}

type Deployment struct {
	ID         string     `json:"id"`
	Status     string     `json:"status"` // running | success | failed
	Message    string     `json:"message"`
	StartedAt  Time       `json:"startedAt"`
	FinishedAt *Time      `json:"finishedAt,omitempty"`
	Steps      []Step     `json:"steps"`
	Log        []LogEntry `json:"log,omitempty"`
}

type Step struct {
	ServiceID string `json:"serviceId,omitempty"`
	Label     string `json:"label"`  // service name, or "health checks"
	Status    string `json:"status"` // pending | running | done | failed
}

type LogEntry struct {
	At        Time   `json:"at"`
	ServiceID string `json:"serviceId,omitempty"`
	Text      string `json:"text"`
}

// deploymentOf is the CLI's view of a deployment: nodes are services, times RFC 3339.
func deploymentOf(d *api.Deployment) *Deployment {
	if d == nil {
		return nil
	}
	out := &Deployment{
		ID: d.ID, Status: d.Status, Message: d.Message, StartedAt: Millis(float64(d.StartedAt)),
		Steps: make([]Step, len(d.Steps)), Log: make([]LogEntry, len(d.Log)),
	}
	if d.FinishedAt != nil {
		t := Millis(float64(*d.FinishedAt))
		out.FinishedAt = &t
	}
	for i, s := range d.Steps {
		out.Steps[i] = Step{ServiceID: s.NodeID, Label: s.Label, Status: s.Status}
	}
	for i, l := range d.Log {
		out.Log[i] = LogEntry{At: Millis(float64(l.At)), ServiceID: l.NodeID, Text: l.Text}
	}
	return out
}

// Time is an API timestamp (milliseconds since the epoch), printed as RFC 3339 in UTC with
// milliseconds.
type Time struct{ time.Time }

// Millis is the time of an epoch-milliseconds value, as the API sends them (possibly fractional).
func Millis(ms float64) Time { return Time{time.UnixMilli(int64(ms)).UTC()} }

// UnmarshalJSON takes epoch milliseconds or an RFC 3339 string.
func (t *Time) UnmarshalJSON(b []byte) error {
	var ms float64
	if err := json.Unmarshal(b, &ms); err == nil {
		*t = Millis(ms)
		return nil
	}
	var s string
	if err := json.Unmarshal(b, &s); err != nil {
		return err
	}
	parsed, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		return err
	}
	t.Time = parsed.UTC()
	return nil
}

func (t Time) MarshalJSON() ([]byte, error) {
	return json.Marshal(t.UTC().Format("2006-01-02T15:04:05.000Z07:00"))
}
