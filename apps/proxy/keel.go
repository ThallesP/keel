// Package proxy is Keel's Caddy plugin, compiled into the keel-proxy image next to caddy-l4
// (Dockerfile). Convex owns the configuration and pushes all of it through the admin API
// (packages/backend/convex/proxy.ts); this package adds what stock Caddy lacks for that:
//
//   - networks `host-tcp` and `host-udp`: listeners in the host's network namespace (hostns.go)
//   - GET /keel/host-addrs: the host addresses to bind, so Convex can write the listen lists
//   - GET /keel/certs?name=…: what the proxy knows about each hostname's certificate
//   - event handler `keel`: reports cert_obtained / cert_failed to Convex as they happen
//
// See docs/networking.md.
package proxy

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/caddyserver/caddy/v2"
	"github.com/caddyserver/caddy/v2/modules/caddytls"
	"go.uber.org/zap"
)

func init() {
	caddy.RegisterModule(Admin{})
	caddy.RegisterModule(Reporter{})
}

// Admin serves Keel's routes on the admin endpoint (a unix socket only Convex can reach).
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
		return caddy.APIError{HTTPStatus: http.StatusMethodNotAllowed, Err: fmt.Errorf("method not allowed")}
	}
	addrs, err := hostAddrs()
	if err != nil {
		return caddy.APIError{HTTPStatus: http.StatusInternalServerError, Err: err}
	}
	return writeJSON(w, addrs)
}

// Cert is one hostname's certificate as far as this process knows. `pending`: neither a cert
// nor a failure yet (ACME is working on it, or the name is not managed).
type Cert struct {
	State    string    `json:"state"` // ok | failed | pending
	Error    string    `json:"error,omitempty"`
	NotAfter time.Time `json:"notAfter,omitzero"`
}

func serveCerts(w http.ResponseWriter, r *http.Request) error {
	if r.Method != http.MethodGet {
		return caddy.APIError{HTTPStatus: http.StatusMethodNotAllowed, Err: fmt.Errorf("method not allowed")}
	}
	out := map[string]Cert{}
	for _, name := range r.URL.Query()["name"] {
		out[name] = certOf(name)
	}
	return writeJSON(w, out)
}

func certOf(name string) Cert {
	for _, c := range caddytls.AllMatchingCertificates(name) {
		if c.Leaf != nil && time.Now().Before(c.Leaf.NotAfter) {
			return Cert{State: "ok", NotAfter: c.Leaf.NotAfter}
		}
	}
	failuresMu.Lock()
	defer failuresMu.Unlock()
	if msg, ok := failures[name]; ok {
		return Cert{State: "failed", Error: msg}
	}
	return Cert{State: "pending"}
}

// Last obtain/renew failure per hostname, cleared when a cert arrives. Package state on purpose:
// it outlives config reloads, which recreate every module.
var (
	failuresMu sync.Mutex
	failures   = map[string]string{}
)

// Reporter is the `keel` event handler: Convex subscribes it to cert_obtained and cert_failed so
// a certificate's outcome reaches the canvas without polling (POST /proxy/events, worker token).
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
	// Dial from the host namespace: Convex listens on the host (tailnet address, or loopback in
	// development), which the container's own namespace may not route to.
	r.client = &http.Client{
		Timeout: 10 * time.Second,
		Transport: &http.Transport{
			DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
				var conn net.Conn
				err := inHost(func() error {
					var err error
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
	Event string `json:"event"` // cert_obtained | cert_failed
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
		report.Error = errorText(e.Data["error"])
		// A config reload cancelled this attempt; the new config starts another.
		if strings.Contains(report.Error, "context canceled") {
			return nil
		}
		// A failed renewal: the current certificate keeps serving while Caddy retries.
		if certOf(name).State == "ok" {
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

// post delivers one report, retrying briefly. A lost report only delays the canvas: the next
// sync reads /keel/certs.
func (r *Reporter) post(report certEvent) {
	body, _ := json.Marshal(report)
	for attempt := range 4 {
		if attempt > 0 {
			time.Sleep(time.Duration(attempt) * 2 * time.Second)
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

func errorText(v any) string {
	var s string
	switch e := v.(type) {
	case error:
		s = e.Error()
	case string:
		s = e
	default:
		s = fmt.Sprint(v)
	}
	return strings.Join(strings.Fields(s), " ")
}

func writeJSON(w http.ResponseWriter, v any) error {
	w.Header().Set("Content-Type", "application/json")
	return json.NewEncoder(w).Encode(v)
}

var (
	_ caddy.AdminRouter = Admin{}
	_ caddy.Provisioner = (*Reporter)(nil)
)
