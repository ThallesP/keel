package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"time"

	"github.com/moby/moby/api/types/events"

	"github.com/ThallesP/keel/internal/domain"
)

type ControlPlane struct {
	URL   string
	Token string
	HTTP  *http.Client
	Log   *slog.Logger
	sleep func(context.Context, time.Duration) error
}

func NewControlPlane(url, token string, hc *http.Client, log *slog.Logger) *ControlPlane {
	return &ControlPlane{URL: url, Token: token, HTTP: hc, Log: log, sleep: sleepCtx}
}

type WorkerConfig struct {
	Sinks []SinkRoute `json:"sinks"`
}

type SinkRoute struct {
	ServiceIDs []string       `json:"serviceIds"`
	Sink       domain.LogSink `json:"sink"`
	Since      int64          `json:"since"`
}

func (c *ControlPlane) FetchConfig(ctx context.Context) (WorkerConfig, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
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
	if res.StatusCode != http.StatusOK {
		return WorkerConfig{}, fmt.Errorf("config %d", res.StatusCode)
	}
	var cfg WorkerConfig
	err = json.NewDecoder(res.Body).Decode(&cfg)
	return cfg, err
}

func (c *ControlPlane) PostEvents(ctx context.Context, evs []events.Message, resync bool) bool {
	body, _ := json.Marshal(evs)
	for n := 0; ; n = min(n+1, 6) {
		status, err := c.postEventsOnce(ctx, body, resync)
		wait := time.Duration(n*5+5) * time.Second
		switch {
		case status >= 200 && status <= 299:
			return true
		case status >= 400 && status <= 499:
			c.Log.Error("the control plane rejected docker events, skipping them", "status", status, "events", len(evs))
			return false
		case ctx.Err() != nil:
			return false
		}
		c.Log.Warn("posting docker events failed, retrying", "status", status, "err", err, "in", wait)
		resync = true
		if c.sleep(ctx, wait) != nil {
			return false
		}
	}
}

func (c *ControlPlane) postEventsOnce(ctx context.Context, body []byte, resync bool) (int, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.URL+"/worker/events", bytes.NewReader(body))
	if err != nil {
		return 0, err
	}
	req.Header.Set("Authorization", "Bearer "+c.Token)
	req.Header.Set("Content-Type", "application/json")
	if resync {
		req.Header.Set("X-Keel-Resync", "1")
	}
	res, err := c.HTTP.Do(req)
	if err != nil {
		return 0, err
	}
	_, _ = io.Copy(io.Discard, res.Body)
	res.Body.Close()
	return res.StatusCode, nil
}
