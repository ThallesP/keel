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

type ControlPlane struct {
	URL   string
	Token string
	HTTP  *http.Client
	Log   *Logger
	sleep func(context.Context, time.Duration) error
}

func NewControlPlane(url, token string, hc *http.Client, log *Logger) *ControlPlane {
	return &ControlPlane{URL: url, Token: token, HTTP: hc, Log: log, sleep: sleepCtx}
}

type WorkerConfig struct {
	Sinks []SinkRoute `json:"sinks"`
}

type SinkRoute struct {
	ServiceIDs []string   `json:"serviceIds"`
	Sink       SinkConfig `json:"sink"`
	Since      int64      `json:"since"`
}

type SinkConfig struct {
	Kind    string `json:"kind"`
	Domain  string `json:"domain"`
	Dataset string `json:"dataset"`
	Token   string `json:"token"`
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
	if res.StatusCode < 200 || res.StatusCode > 299 {
		_, _ = io.Copy(io.Discard, res.Body)
		return WorkerConfig{}, fmt.Errorf("config %d", res.StatusCode)
	}
	var cfg WorkerConfig
	err = json.NewDecoder(res.Body).Decode(&cfg)
	return cfg, err
}

func (c *ControlPlane) PostEvents(ctx context.Context, body []byte, resync bool) bool {
	for n := 0; ; n = min(n+1, 6) {
		status, err := c.postEventsOnce(ctx, body, resync)
		wait := time.Duration(n*5+5) * time.Second
		switch {
		case status >= 200 && status <= 299:
			return true
		case status >= 400 && status <= 499:
			c.Log.Logf("events", "rejected %d, skipping %q", status, truncate(string(body), 120))
			return false
		case ctx.Err() != nil:
			return false
		case err == nil:
			c.Log.Logf("events", "post failed %d, retry in %s", status, wait)
		default:
			c.Log.Logf("events", "post failed (%s), retry in %s", errorText(err), wait)
		}
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
