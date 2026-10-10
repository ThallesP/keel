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

	timeout time.Duration
	sleep   func(context.Context, time.Duration) error
}

func NewControlPlane(url, token string, hc *http.Client, log *Logger) *ControlPlane {
	return &ControlPlane{URL: url, Token: token, HTTP: hc, Log: log, timeout: 10 * time.Second, sleep: sleepCtx}
}

type WorkerConfig struct {
	Sinks []SinkRoute `json:"sinks"`
}

type SinkRoute struct {
	ProjectID  string     `json:"projectId"`
	ServiceIDs []string   `json:"serviceIds"`
	Sink       SinkConfig `json:"sink"`
	Since      *float64   `json:"since,omitempty"`
}

type SinkConfig struct {
	Kind    string `json:"kind"`
	Domain  string `json:"domain"`
	Dataset string `json:"dataset"`
	Token   string `json:"token"`
}

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
		if c.sleep(ctx, time.Duration(n*5+5)*time.Second) != nil {
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
