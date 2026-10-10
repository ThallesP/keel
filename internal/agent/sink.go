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
	Key() string
	Send(ctx context.Context, events []LogEvent) bool
}

func sinkKey(c SinkConfig) string {
	token := c.Token
	if len(token) > 6 {
		token = token[len(token)-6:]
	}
	return c.Kind + ":" + c.Domain + ":" + c.Dataset + ":" + token
}

type SinkFactory func(cfg SinkConfig) (Sink, bool)

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
