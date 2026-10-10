package agent

import (
	"context"
	"log/slog"
	"net/http"
	"time"

	"github.com/ThallesP/keel/internal/domain"
)

type LogEvent struct {
	Time      time.Time `json:"_time"`
	Message   string    `json:"message"`
	Stream    string    `json:"stream"`
	ServiceID string    `json:"service_id"`
	Service   string    `json:"service"`
	Task      string    `json:"task"`
	Replica   int       `json:"replica"`
	Node      string    `json:"node"`
	Container string    `json:"container"`
}

type Sink interface {
	Send(ctx context.Context, events []LogEvent) bool
}

type SinkFactory func(cfg domain.LogSink) (Sink, bool)

func NewSinkFactory(hc *http.Client, log *slog.Logger) SinkFactory {
	return func(cfg domain.LogSink) (Sink, bool) {
		if cfg.Kind != "axiom" {
			return nil, false
		}
		return NewAxiomSink(cfg, hc, log), true
	}
}
