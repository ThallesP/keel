package agent

import (
	"context"
	"net/http"
)

// LogEvent is one shipped log line. Field names (and order) are the contract with every read
// provider (docs/logs.md, docs/go/spec/observability.md §13): change one side, change both.
type LogEvent struct {
	Time      string `json:"_time"`      // RFC3339Nano from Docker's timestamps=1, else now (ISO, ms)
	Message   string `json:"message"`    // the line without the stamp (ANSI escapes kept)
	Stream    string `json:"stream"`     // "stdout" | "stderr"
	ServiceID string `json:"service_id"` // node id: the Swarm service svc-<id> minus the prefix
	Service   string `json:"service"`    // Swarm service name svc-<id>
	Task      string `json:"task"`       // Swarm task id (one per replica run)
	Replica   int    `json:"replica"`    // Swarm slot, from the task name svc-<id>.<slot>.<task>
	Node      string `json:"node"`       // Swarm node id this agent runs on
	Container string `json:"container"`  // 12-char container id
}

// Sink receives batches of log events for one organization's sink. Send never fails loudly: true
// once the sink has the events, or when it rejected them as malformed (4xx: retrying cannot help,
// so they are dropped with a log); false when it stayed unreachable after the sink's own retries,
// and the caller keeps the batch and its resume points for a later attempt.
type Sink interface {
	Key() string
	Send(ctx context.Context, events []LogEvent) bool
}

// sinkKey is a config's stable identity, so a changed token or dataset builds a new sink and
// equal sinks (several projects of one organization) share one queue.
func sinkKey(c SinkConfig) string {
	token := c.Token
	if len(token) > 6 {
		token = token[len(token)-6:]
	}
	return c.Kind + ":" + c.Domain + ":" + c.Dataset + ":" + token
}

// SinkFactory builds the sink of a config; ok is false for a kind this agent does not know.
type SinkFactory func(cfg SinkConfig) (Sink, bool)

// NewSinkFactory is the one place that knows every sink kind. hc carries the ingest requests
// (the public internet, never the mesh).
func NewSinkFactory(hc *http.Client, log *Logger) SinkFactory {
	return func(cfg SinkConfig) (Sink, bool) {
		switch cfg.Kind {
		case "axiom":
			return NewAxiomSink(cfg, hc, log), true
		default:
			return nil, false
		}
	}
}
