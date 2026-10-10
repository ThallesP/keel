package agent

import (
	"context"
	"net/http"
)

type LogEvent struct {
	Time      string `json:"_time"`
	Message   string `json:"message"`
	Stream    string `json:"stream"`
	ServiceID string `json:"service_id"`
	Service   string `json:"service"`
	Task      string `json:"task"`
	Replica   int    `json:"replica"`
	Node      string `json:"node"`
	Container string `json:"container"`
}

type Sink interface {
	Send(ctx context.Context, events []LogEvent) bool
}

type SinkFactory func(cfg SinkConfig) (Sink, bool)

func NewSinkFactory(hc *http.Client, log *Logger) SinkFactory {
	return func(cfg SinkConfig) (Sink, bool) {
		if cfg.Kind != "axiom" {
			return nil, false
		}
		return NewAxiomSink(cfg, hc, log), true
	}
}
