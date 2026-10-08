package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

// ControlPlane is the agent's client of `keel serve`'s worker routes: outbound only, one bearer
// token for every route, 10 s per request (docs/go/spec/swarm-worker.md §13.2, §13.4).
type ControlPlane struct {
	URL   string // KEEL_URL without trailing "/"
	Token string
	HTTP  *http.Client
	Log   *Logger

	timeout time.Duration
	sleep   func(context.Context, time.Duration) error
}

// NewControlPlane builds the client. hc is the mesh client (tsnet or the default transport).
func NewControlPlane(url, token string, hc *http.Client, log *Logger) *ControlPlane {
	return &ControlPlane{URL: url, Token: token, HTTP: hc, Log: log, timeout: 10 * time.Second, sleep: sleepCtx}
}

// WorkerConfig is GET /worker/config: the sinks this node feeds, each with the services it
// covers. Mirrors the control plane's worker.config (observability.md §11.1); only ever gains
// fields.
type WorkerConfig struct {
	Sinks []SinkRoute `json:"sinks"`
}

// SinkRoute is one project's sink.
type SinkRoute struct {
	ProjectID  string     `json:"projectId"`
	ServiceIDs []string   `json:"serviceIds"`
	Sink       SinkConfig `json:"sink"`
	// Since is when the organization connected the sink, epoch ms (may be fractional): where a
	// container without a resume point starts.
	Since *float64 `json:"since,omitempty"`
}

// SinkConfig is the stored sink. The agent ignores the fields only the read side uses (traces,
// org).
type SinkConfig struct {
	Kind    string `json:"kind"`
	Domain  string `json:"domain"`
	Dataset string `json:"dataset"`
	Token   string `json:"token"`
}

// FetchConfig is GET <KEEL_URL>/worker/config. Non-2xx → error "config <status>".
func (c *ControlPlane) FetchConfig(ctx context.Context) (WorkerConfig, error) {
	ctx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.URL+"/worker/config", nil)
	if err != nil {
		return WorkerConfig{}, err
	}
	req.Header.Set("Authorization", "Bearer "+c.Token)
	res, err := c.HTTP.Do(req)
	if err != nil {
		return WorkerConfig{}, err
	}
	defer res.Body.Close()
	if res.StatusCode < 200 || res.StatusCode > 299 {
		_, _ = io.Copy(io.Discard, res.Body)
		return WorkerConfig{}, fmt.Errorf("config %d", res.StatusCode)
	}
	var cfg WorkerConfig
	if err := json.NewDecoder(res.Body).Decode(&cfg); err != nil {
		return WorkerConfig{}, err
	}
	return cfg, nil
}

// PostEvents POSTs Docker events to <KEEL_URL>/worker/events. It retries forever on 5xx and
// network failures (an event is never dropped silently) with a capped backoff of n*5+5 seconds,
// n = 0…6; a 4xx will not succeed on retry, so it is logged and skipped. Every retry asks for a
// resync. Returns whether the control plane accepted the body; false as well when ctx ends.
func (c *ControlPlane) PostEvents(ctx context.Context, body []byte, resync bool) bool {
	for n := 0; ; n = min(n+1, 6) {
		status, err := c.postEventsOnce(ctx, body, resync)
		switch {
		case err == nil && status >= 200 && status <= 299:
			return true
		case err == nil && status >= 400 && status <= 499:
			c.Log.Log("events", fmt.Sprintf("rejected %d, skipping", status), "body", truncate(string(body), 120))
			return false
		case ctx.Err() != nil:
			return false
		case err == nil:
			c.Log.Log("events", fmt.Sprintf("post failed %d, retry in %ds", status, n*5+5))
		default:
			c.Log.Log("events", fmt.Sprintf("post failed (%s), retry in %ds", errorText(err), n*5+5))
		}
		resync = true
		if c.sleep(ctx, time.Duration(n*5000+5000)*time.Millisecond) != nil {
			return false
		}
	}
}

func (c *ControlPlane) postEventsOnce(ctx context.Context, body []byte, resync bool) (int, error) {
	ctx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.URL+"/worker/events", bytes.NewReader(body))
	if err != nil {
		return 0, err
	}
	req.Header.Set("Authorization", "Bearer "+c.Token)
	req.Header.Set("Content-Type", "application/json")
	flag := "0"
	if resync {
		flag = "1"
	}
	req.Header.Set("X-Keel-Resync", flag)
	res, err := c.HTTP.Do(req)
	if err != nil {
		return 0, err
	}
	_, _ = io.Copy(io.Discard, res.Body)
	res.Body.Close()
	return res.StatusCode, nil
}
