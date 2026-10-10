//go:build linux

package proxy

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/caddyserver/caddy/v2"
	"github.com/caddyserver/caddy/v2/modules/caddytls"
	"github.com/caddyserver/certmagic"
	"go.uber.org/zap"
)

func init() {
	caddy.RegisterModule(Admin{})
	caddy.RegisterModule(Reporter{})
}

type Admin struct{}

func (Admin) CaddyModule() caddy.ModuleInfo {
	return caddy.ModuleInfo{ID: "admin.api.keel", New: func() caddy.Module { return new(Admin) }}
}

func (Admin) Routes() []caddy.AdminRoute {
	return []caddy.AdminRoute{
		{Pattern: "/keel/host-addrs", Handler: caddy.AdminHandlerFunc(serveHostAddrs)},
		{Pattern: "/keel/certs", Handler: caddy.AdminHandlerFunc(serveCerts)},
	}
}

func serveHostAddrs(w http.ResponseWriter, r *http.Request) error {
	if r.Method != http.MethodGet {
		return caddy.APIError{HTTPStatus: http.StatusMethodNotAllowed, Err: errors.New("method not allowed")}
	}
	addrs, err := hostAddrs()
	if err != nil {
		return caddy.APIError{HTTPStatus: http.StatusInternalServerError, Err: err}
	}
	return writeJSON(w, addrs)
}

type cert struct {
	State string `json:"state"`
	Error string `json:"error,omitempty"`
}

func serveCerts(w http.ResponseWriter, r *http.Request) error {
	if r.Method != http.MethodGet {
		return caddy.APIError{HTTPStatus: http.StatusMethodNotAllowed, Err: errors.New("method not allowed")}
	}
	out := map[string]cert{}
	for _, name := range r.URL.Query()["name"] {
		out[name] = certOf(name)
	}
	return writeJSON(w, out)
}

func certOf(name string) cert {
	for _, c := range matchingCerts(name) {
		if c.Leaf != nil && time.Now().Before(c.Leaf.NotAfter) {
			return cert{State: "ok"}
		}
	}
	failuresMu.Lock()
	defer failuresMu.Unlock()
	if msg, ok := failures[name]; ok {
		return cert{State: "failed", Error: msg}
	}
	return cert{State: "pending"}
}

func matchingCerts(name string) (certs []certmagic.Certificate) {
	defer func() {
		if recover() != nil {
			certs = nil
		}
	}()
	return caddytls.AllMatchingCertificates(name)
}

var (
	failuresMu sync.Mutex
	failures   = map[string]string{}
)

type Reporter struct {
	URL   string `json:"url,omitempty"`
	Token string `json:"token,omitempty"`

	client *http.Client
	logger *zap.Logger
}

func (Reporter) CaddyModule() caddy.ModuleInfo {
	return caddy.ModuleInfo{ID: "events.handlers.keel", New: func() caddy.Module { return new(Reporter) }}
}

func (r *Reporter) Provision(ctx caddy.Context) error {
	r.logger = ctx.Logger()
	r.client = &http.Client{
		Timeout: 10 * time.Second,
		Transport: &http.Transport{
			DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
				var conn net.Conn
				err := inHost(func() (err error) {
					conn, err = (&net.Dialer{Timeout: 5 * time.Second}).DialContext(ctx, network, addr)
					return err
				})
				return conn, err
			},
		},
	}
	return nil
}

type certEvent struct {
	Event string `json:"event"`
	Name  string `json:"name"`
	Error string `json:"error,omitempty"`
}

func (r *Reporter) Handle(_ context.Context, e caddy.Event) error {
	name, _ := e.Data["identifier"].(string)
	if name == "" {
		return nil
	}
	report := certEvent{Event: e.Name(), Name: name}
	switch e.Name() {
	case "cert_obtained":
		failuresMu.Lock()
		delete(failures, name)
		failuresMu.Unlock()
	case "cert_failed":
		report.Error = strings.Join(strings.Fields(fmt.Sprint(e.Data["error"])), " ")
		if strings.Contains(report.Error, "context canceled") || certOf(name).State == "ok" {
			return nil
		}
		failuresMu.Lock()
		failures[name] = report.Error
		failuresMu.Unlock()
	default:
		return nil
	}
	if r.URL != "" {
		go r.post(report)
	}
	return nil
}

var reportBackoff = func(attempt int) time.Duration { return time.Duration(attempt) * 2 * time.Second }

func (r *Reporter) post(report certEvent) {
	body, _ := json.Marshal(report)
	for attempt := range 4 {
		if attempt > 0 {
			time.Sleep(reportBackoff(attempt))
		}
		req, err := http.NewRequest(http.MethodPost, r.URL, bytes.NewReader(body))
		if err != nil {
			r.logger.Error("keel report", zap.Error(err))
			return
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+r.Token)
		res, err := r.client.Do(req)
		if err == nil {
			res.Body.Close()
			if res.StatusCode < 300 {
				return
			}
			err = fmt.Errorf("status %d", res.StatusCode)
		}
		r.logger.Warn("keel report", zap.String("name", report.Name), zap.Error(err))
	}
}

func writeJSON(w http.ResponseWriter, v any) error {
	w.Header().Set("Content-Type", "application/json")
	return json.NewEncoder(w).Encode(v)
}

var (
	_ caddy.AdminRouter = Admin{}
	_ caddy.Provisioner = (*Reporter)(nil)
)
