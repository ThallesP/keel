// Package caddy is keel-proxy's admin API client (app.Proxy): HTTP/1.1 over the unix socket the
// edge container shares with `keel serve` only (docs/go/spec/proxy-ingress.md §5.1.2). The control
// plane never runs Caddy itself; it pushes the whole `apps` object and reads two plugin routes.
package caddy

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/ThallesP/keel/internal/app"
)

// DefaultSocket is KEEL_PROXY_SOCKET's default.
const DefaultSocket = "/run/keel-proxy/admin.sock"

// IdleTimeout: the admin socket may stay silent this long before a call gives up.
const IdleTimeout = 30 * time.Second

// Client implements app.Proxy.
type Client struct {
	socket    string
	reportURL string
	idle      time.Duration
	http      *http.Client
}

var _ app.Proxy = (*Client)(nil)

// New is a client for the admin socket at socket ("" = DefaultSocket). reportURL is
// KEEL_PROXY_REPORT_URL ("" lets the app use <KEEL_SITE_URL>/proxy/events). The edge dials it from
// the host's network namespace, where Docker's DNS does not answer: use an IP address (the
// control plane's tailnet address), not a name.
func New(socket, reportURL string) *Client {
	if socket == "" {
		socket = DefaultSocket
	}
	c := &Client{socket: socket, reportURL: reportURL, idle: IdleTimeout}
	c.http = &http.Client{Transport: &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			var d net.Dialer
			conn, err := d.DialContext(ctx, "unix", c.socket)
			if err != nil {
				return nil, err
			}
			return &idleConn{Conn: conn, idle: c.idle}, nil
		},
		DisableKeepAlives: true,
	}}
	return c
}

func (c *Client) ReportURL() string { return c.reportURL }

// HostAddrs: GET /keel/host-addrs. A JSON null (no address) is nil.
func (c *Client) HostAddrs(ctx context.Context) ([]string, error) {
	var addrs []string
	if err := c.getJSON(ctx, "/keel/host-addrs", &addrs); err != nil {
		return nil, err
	}
	return addrs, nil
}

// Certs: GET /keel/certs?name=<d1>&name=<d2>.
func (c *Client) Certs(ctx context.Context, names []string) (map[string]app.ProxyCert, error) {
	q := make([]string, 0, len(names))
	for _, n := range names {
		q = append(q, "name="+url.QueryEscape(n))
	}
	certs := map[string]app.ProxyCert{}
	if err := c.getJSON(ctx, "/keel/certs?"+strings.Join(q, "&"), &certs); err != nil {
		return nil, err
	}
	return certs, nil
}

// LoadApps: POST /config/apps. Caddy replaces the whole `apps` value atomically and keeps
// `admin`; on error it keeps serving the previous config and the error is a *app.ProxyRejected.
func (c *Client) LoadApps(ctx context.Context, apps []byte) error {
	status, text, err := c.do(ctx, http.MethodPost, "/config/apps", apps)
	if err != nil {
		return err
	}
	if status >= 300 {
		return &app.ProxyRejected{Message: CaddyError(text)}
	}
	return nil
}

func (c *Client) getJSON(ctx context.Context, path string, v any) error {
	status, text, err := c.do(ctx, http.MethodGet, path, nil)
	if err != nil {
		return err
	}
	if status >= 300 {
		return errors.New(CaddyError(text))
	}
	return json.Unmarshal([]byte(text), v)
}

// do answers any HTTP status; err only when the proxy could not be asked.
func (c *Client) do(ctx context.Context, method, path string, body []byte) (int, string, error) {
	var rd io.Reader
	if body != nil {
		rd = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, "http://keel-proxy"+path, rd)
	if err != nil {
		return 0, "", err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	res, err := c.http.Do(req)
	if err != nil {
		return 0, "", c.explain(err)
	}
	defer res.Body.Close()
	b, err := io.ReadAll(res.Body)
	if err != nil {
		return 0, "", c.explain(err)
	}
	return res.StatusCode, string(b), nil
}

func (c *Client) explain(err error) error {
	if errors.Is(err, syscall.ENOENT) || errors.Is(err, syscall.ECONNREFUSED) {
		return fmt.Errorf("keel-proxy is not running (no admin socket at %s)", c.socket)
	}
	var ne net.Error
	if errors.As(err, &ne) && ne.Timeout() {
		return fmt.Errorf("keel-proxy did not answer within %ss", strconv.FormatFloat(c.idle.Seconds(), 'f', -1, 64))
	}
	return err
}

var loadingPrefix = regexp.MustCompile(`^(loading (new )?config: )+`)

// CaddyError is the message of an admin error answer: Caddy writes {"error": "loading config:
// loading new config: …"}; the prefixes say nothing to a user.
func CaddyError(text string) string {
	message := text
	var body struct {
		Error *string `json:"error"`
	}
	if json.Unmarshal([]byte(text), &body) == nil && body.Error != nil {
		message = *body.Error
	}
	return strings.TrimSpace(loadingPrefix.ReplaceAllString(message, ""))
}

// idleConn fails a read or write that waits longer than idle (Node's socket timeout).
type idleConn struct {
	net.Conn
	idle time.Duration
}

func (c *idleConn) Read(b []byte) (int, error) {
	_ = c.Conn.SetReadDeadline(time.Now().Add(c.idle))
	return c.Conn.Read(b)
}

func (c *idleConn) Write(b []byte) (int, error) {
	_ = c.Conn.SetWriteDeadline(time.Now().Add(c.idle))
	return c.Conn.Write(b)
}
