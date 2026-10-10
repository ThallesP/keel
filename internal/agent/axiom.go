package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

type AxiomSink struct {
	url   string
	token string
	http  *http.Client
	log   *Logger
	sleep func(context.Context, time.Duration) error
}

func NewAxiomSink(cfg SinkConfig, hc *http.Client, log *Logger) *AxiomSink {
	return &AxiomSink{url: axiomIngestURL(cfg.Domain, cfg.Dataset), token: cfg.Token, http: hc, log: log, sleep: sleepCtx}
}

func axiomIngestURL(domain, dataset string) string {
	base := domain
	if !strings.Contains(base, "://") {
		base = "https://" + base
	}
	base = strings.TrimRight(base, "/")
	if strings.HasSuffix(domain, ".edge.axiom.co") {
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
		case err == nil && status >= 200 && status <= 299:
			return true
		case err == nil && status >= 400 && status <= 499 && status != 429:
			s.log.Log("axiom", fmt.Sprintf("rejected %d, dropping %d events: %q", status, len(events), text))
			return true
		case ctx.Err() != nil:
			return false
		case err == nil:
			s.log.Log("axiom", fmt.Sprintf("ingest %d, retry %d: %q", status, attempt+1, text))
		default:
			s.log.Log("axiom", fmt.Sprintf("ingest failed (%s), retry %d", errorText(err), attempt+1))
		}
		if s.sleep(ctx, time.Second<<attempt) != nil {
			return false
		}
	}
	s.log.Log("axiom", fmt.Sprintf("unreachable, keeping %d events for a later attempt", len(events)))
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
	res, err := s.http.Do(req)
	if err != nil {
		return 0, "", err
	}
	defer res.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(res.Body, 64<<10))
	return res.StatusCode, truncate(string(raw), 200), nil
}
