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

const DefaultSocket = "/run/keel-proxy/admin.sock"

const IdleTimeout = 30 * time.Second

type Client struct {
	socket    string
	reportURL string
	idle      time.Duration
	http      *http.Client
}

var _ app.Proxy = (*Client)(nil)

func New(socket, reportURL string) *Client {
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

func (c *Client) HostAddrs(ctx context.Context) ([]string, error) {
	var addrs []string
	if err := c.getJSON(ctx, "/keel/host-addrs", &addrs); err != nil {
		return nil, err
	}
	return addrs, nil
}

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
