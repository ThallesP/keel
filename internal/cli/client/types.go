package client

import (
	"encoding/json"
	"time"

	"github.com/ThallesP/keel/internal/api"
)

type (
	User         = api.User
	Organization = api.Organization
)

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
	Local      bool    `json:"local"`
}

type TraceStats struct {
	Requests int      `json:"requests"`
	Errors   int      `json:"errors"`
	P50Ms    *float64 `json:"p50Ms"`
	P95Ms    *float64 `json:"p95Ms"`
	P99Ms    *float64 `json:"p99Ms"`
}

type Traces struct {
	Stats  TraceStats     `json:"stats"`
	Traces []TraceSummary `json:"traces"`
}

type Tracing struct {
	Enabled bool                `json:"enabled"`
	Store   string              `json:"store"`
	Env     []api.TracingEnvVar `json:"env"`
}

type Deployment struct {
	ID         string     `json:"id"`
	Status     string     `json:"status"`
	Message    string     `json:"message"`
	StartedAt  Time       `json:"startedAt"`
	FinishedAt *Time      `json:"finishedAt,omitempty"`
	Steps      []Step     `json:"steps"`
	Log        []LogEntry `json:"log,omitempty"`
}

type Step struct {
	ServiceID string `json:"serviceId,omitempty"`
	Label     string `json:"label"`
	Status    string `json:"status"`
}

type LogEntry struct {
	At        Time   `json:"at"`
	ServiceID string `json:"serviceId,omitempty"`
	Text      string `json:"text"`
}

func deploymentOf(d *api.Deployment) *Deployment {
	if d == nil {
		return nil
	}
	out := &Deployment{
		ID: d.ID, Status: d.Status, Message: d.Message, StartedAt: Millis(d.StartedAt),
		Steps: make([]Step, len(d.Steps)), Log: make([]LogEntry, len(d.Log)),
	}
	if d.FinishedAt != nil {
		t := Millis(*d.FinishedAt)
		out.FinishedAt = &t
	}
	for i, s := range d.Steps {
		out.Steps[i] = Step{ServiceID: s.NodeID, Label: s.Label, Status: s.Status}
	}
	for i, l := range d.Log {
		out.Log[i] = LogEntry{At: Millis(l.At), ServiceID: l.NodeID, Text: l.Text}
	}
	return out
}

type Time struct{ time.Time }

func Millis(ms int64) Time { return Time{time.UnixMilli(ms).UTC()} }

func (t *Time) UnmarshalJSON(b []byte) error {
	var ms float64
	if err := json.Unmarshal(b, &ms); err == nil {
		*t = Millis(int64(ms))
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
