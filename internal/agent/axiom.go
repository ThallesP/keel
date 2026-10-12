package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/ThallesP/keel/internal/domain"
)

type AxiomSink struct {
	url   string
	token string
	log   *slog.Logger
	sleep func(context.Context, time.Duration) error
}

func NewAxiomSink(cfg SinkConfig, log *slog.Logger) *AxiomSink {
	return &AxiomSink{url: axiomIngestURL(cfg.Domain, cfg.Dataset), token: cfg.Token, log: log, sleep: sleepCtx}
}

func axiomIngestURL(host, dataset string) string {
	base := domain.AxiomBaseURL(host)
	if strings.HasSuffix(base, ".edge.axiom.co") {
		return base + "/v1/ingest/" + url.PathEscape(dataset)
	}
	return base + "/v1/datasets/" + url.PathEscape(dataset) + "/ingest"
}

func (s *AxiomSink) Send(ctx context.Context, events []LogEvent) bool {
	var body bytes.Buffer
	enc := json.NewEncoder(&body)
	for _, e := range events {
		_ = enc.Encode(e)
	}
	for attempt := range 5 {
		status, text, err := s.post(ctx, body.Bytes())
		switch {
		case status >= 200 && status <= 299:
			return true
		case status >= 400 && status <= 499 && status != 429:
			s.log.Error("axiom rejected log lines, dropping them", "status", status, "lines", len(events), "response", text)
			return true
		case ctx.Err() != nil:
			return false
		}
		s.log.Warn("axiom ingest failed, retrying", "status", status, "err", err, "response", text, "attempt", attempt+1)
		if s.sleep(ctx, time.Second<<attempt) != nil {
			return false
		}
	}
	s.log.Error("axiom unreachable, keeping log lines for a later attempt", "lines", len(events))
	return false
}

func (s *AxiomSink) post(ctx context.Context, body []byte) (int, string, error) {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.url, bytes.NewReader(body))
	if err != nil {
		return 0, "", err
	}
	req.Header.Set("Authorization", "Bearer "+s.token)
	req.Header.Set("Content-Type", "application/x-ndjson")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return 0, "", err
	}
	defer res.Body.Close()
	text, _ := io.ReadAll(io.LimitReader(res.Body, 200))
	return res.StatusCode, string(text), nil
}
