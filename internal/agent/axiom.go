package agent

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// AxiomSink POSTs NDJSON to the dataset's ingest endpoint. Field names are kept as they are;
// `_time` is the timestamp Axiom indexes on. Basic (ingest-only) API tokens are enough for this
// side (docs/go/spec/observability.md §11.8).
type AxiomSink struct {
	key   string
	url   string
	token string
	http  *http.Client
	log   *Logger

	attempts int           // 5
	timeout  time.Duration // 15 s per attempt
	backoff  time.Duration // 1 s, doubled per attempt (1+2+4+8+16 = 31 s)
	sleep    func(context.Context, time.Duration) error
}

// NewAxiomSink builds the sink of an Axiom config.
func NewAxiomSink(cfg SinkConfig, hc *http.Client, log *Logger) *AxiomSink {
	return &AxiomSink{
		key: sinkKey(cfg), url: axiomIngestURL(cfg.Domain, cfg.Dataset), token: cfg.Token,
		http: hc, log: log,
		attempts: 5, timeout: 15 * time.Second, backoff: time.Second, sleep: sleepCtx,
	}
}

// axiomIngestURL depends on the host, as in axiom-go: api.axiom.co / api.eu.axiom.co take
// /v1/datasets/<dataset>/ingest (/v1/ingest/<dataset> is 404 there); regional edge hosts
// (*.edge.axiom.co) take /v1/ingest/<dataset>.
func axiomIngestURL(domain, dataset string) string {
	base := domain
	if !strings.Contains(base, "://") {
		base = "https://" + base
	}
	base = strings.TrimRight(base, "/")
	if strings.HasSuffix(domain, ".edge.axiom.co") {
		return base + "/v1/ingest/" + encodeURIComponent(dataset)
	}
	return base + "/v1/datasets/" + encodeURIComponent(dataset) + "/ingest"
}

func (s *AxiomSink) Key() string { return s.key }

// Send delivers a batch: up to 5 attempts, 15 s each, sleeping 1, 2, 4, 8, 16 s after each failed
// one (also after the last). 2xx → true; 4xx other than 429 → dropped as malformed, true; still
// failing after 5 attempts → false (the caller keeps the batch). Also false when ctx ends.
func (s *AxiomSink) Send(ctx context.Context, events []LogEvent) bool {
	lines := make([][]byte, len(events))
	for i, e := range events {
		lines[i] = marshal(e)
	}
	body := bytes.Join(lines, []byte("\n"))
	for attempt := 0; attempt < s.attempts; attempt++ {
		status, text, err := s.post(ctx, body)
		switch {
		case err == nil && status >= 200 && status <= 299:
			return true
		case err == nil && status >= 400 && status <= 499 && status != 429:
			s.log.Log("axiom", fmt.Sprintf("rejected %d, dropping %d events", status, len(events)), "text", text)
			return true
		case ctx.Err() != nil:
			return false
		case err == nil:
			s.log.Log("axiom", fmt.Sprintf("ingest %d, retry %d", status, attempt+1), "text", text)
		default:
			s.log.Log("axiom", fmt.Sprintf("ingest failed (%s), retry %d", errorText(err), attempt+1))
		}
		if s.sleep(ctx, s.backoff<<attempt) != nil {
			return false
		}
	}
	s.log.Log("axiom", fmt.Sprintf("unreachable, keeping %d events for a later attempt", len(events)))
	return false
}

// post is one attempt; text is the first 200 characters of the response.
func (s *AxiomSink) post(ctx context.Context, body []byte) (int, string, error) {
	ctx, cancel := context.WithTimeout(ctx, s.timeout)
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
	if res.StatusCode >= 200 && res.StatusCode <= 299 {
		_, _ = io.Copy(io.Discard, res.Body)
		return res.StatusCode, "", nil
	}
	raw, _ := io.ReadAll(io.LimitReader(res.Body, 64<<10))
	return res.StatusCode, truncate(string(raw), 200), nil
}

// encodeURIComponent is JavaScript's: everything but A-Z a-z 0-9 - _ . ! ~ * ' ( ) is
// percent-encoded as UTF-8.
func encodeURIComponent(s string) string {
	const hex = "0123456789ABCDEF"
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		if 'a' <= c && c <= 'z' || 'A' <= c && c <= 'Z' || '0' <= c && c <= '9' || strings.IndexByte("-_.!~*'()", c) >= 0 {
			b.WriteByte(c)
			continue
		}
		b.WriteByte('%')
		b.WriteByte(hex[c>>4])
		b.WriteByte(hex[c&15])
	}
	return b.String()
}
