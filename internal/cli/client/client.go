package client

import (
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/ThallesP/keel/internal/api"
	"github.com/ThallesP/keel/internal/cli/output"
)

var UserAgent = "keel-cli"

type Client struct {
	URL   string
	Token string
	HTTP  *http.Client
}

func New(url, token string) *Client {
	return &Client{URL: url, Token: token, HTTP: &http.Client{Timeout: time.Minute}}
}

type reply struct {
	status int
	header http.Header
	body   []byte
}

func (c *Client) send(ctx context.Context, method, path string, query url.Values, in any) (*reply, error) {
	var body io.Reader
	if in != nil {
		b, err := json.Marshal(in)
		if err != nil {
			return nil, err
		}
		body = bytes.NewReader(b)
	}
	target := c.URL + path
	if len(query) > 0 {
		target += "?" + query.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, method, target, body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", UserAgent)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Origin", c.URL)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if c.Token != "" {
		req.Header.Set("Authorization", "Bearer "+c.Token)
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	return &reply{status: resp.StatusCode, header: resp.Header, body: raw}, nil
}

func (c *Client) call(ctx context.Context, method, path string, query url.Values, in, out any) error {
	r, err := c.send(ctx, method, path, query, in)
	if err != nil {
		return translate(err, c.URL)
	}
	if r.status < 200 || r.status > 299 {
		return c.failure(method+" "+path, r)
	}
	if out == nil || r.status == http.StatusNoContent {
		return nil
	}
	if err := json.Unmarshal(r.body, out); err != nil {
		return output.Errorf(output.CodeServer, "", "unexpected response from %s %s: %v", method, path, err)
	}
	return nil
}

func (c *Client) failure(what string, r *reply) *output.Error {
	var p api.Problem
	if json.Unmarshal(r.body, &p) != nil || p.Code == "" {
		return output.Errorf(output.CodeServer, "", "%s: HTTP %d: %s", what, r.status, snippet(r.body))
	}
	msg := p.Detail
	if len(p.Errors) > 0 {
		details := make([]string, len(p.Errors))
		for i, e := range p.Errors {
			details[i] = strings.TrimSpace(e.Location + " " + e.Message)
		}
		msg += ": " + strings.Join(details, "; ")
	}
	return withFix(p.Code, cmp.Or(msg, p.Title), c.URL, r.header)
}

func withFix(code, msg, webURL string, h http.Header) *output.Error {
	fix := ""
	switch code {
	case output.CodeNotAuthenticated:
		return notAuthenticated(webURL, "Session expired or signed out")
	case output.CodeNoOrganization:
		fix = "Ask a member for an invite link (account menu → Invite people), then keel login " + webURL
	case output.CodeDeploymentRunning:
		fix = "keel deployment get --wait"
	case output.CodeNothingToShip:
		fix = "Stage a change first (keel var set …), or redeploy: keel redeploy <service>"
	case output.CodeServiceNotFound:
		fix = "keel service list"
	case output.CodeTracesOff:
		fix = "Open Observability in the dashboard (" + webURL + ") and Sign in with Axiom"
	case output.CodeProjectNotFound:
		fix = "keel project list"
	case output.CodeNameTaken:
		fix = "Pick another name; keel service list shows the taken ones"
		if slug := takenProject(msg); slug != "" {
			fix = "Pick another name, or use it: keel link " + slug
		}
	case output.CodeRateLimited:
		fix = "Wait a moment, then retry"
		if s := h.Get("Retry-After"); s != "" {
			fix = "Retry in " + s + "s"
		}
	}
	return &output.Error{Code: code, Message: msg, Fix: fix}
}

func takenProject(msg string) string {
	s, isProject := strings.CutPrefix(msg, `Project "`)
	slug, isTaken := strings.CutSuffix(s, `" already exists`)
	if !isProject || !isTaken {
		return ""
	}
	return slug
}

func translate(err error, webURL string) error {
	if errors.Is(err, context.Canceled) {
		return output.Errorf(output.CodeCancelled, "", "Cancelled")
	}
	var ne net.Error
	if errors.As(err, &ne) {
		host := webURL
		if u, err := url.Parse(webURL); err == nil && u.Host != "" {
			host = u.Host
		}
		if ne.Timeout() {
			return output.Errorf(output.CodeTimeout, "Retry; check that "+host+" is up",
				"Timed out talking to %s", host)
		}
		return output.Errorf(output.CodeNetwork,
			"Check the URL, and that this machine is on the install's tailnet",
			"Can't reach %s: %v", host, rootCause(err))
	}
	return output.Errorf(output.CodeServer, "", "%v", err)
}

func rootCause(err error) error {
	for {
		next := errors.Unwrap(err)
		if next == nil {
			return err
		}
		err = next
	}
}

func notAuthenticated(webURL, msg string) *output.Error {
	return &output.Error{Code: output.CodeNotAuthenticated, Message: msg, Fix: "keel login " + webURL}
}

func snippet(b []byte) string {
	s := strings.TrimSpace(string(b))
	if len(s) > 200 {
		s = s[:200] + "…"
	}
	return s
}

func apiPath(format string, ids ...string) string {
	args := make([]any, len(ids))
	for i, id := range ids {
		args[i] = url.PathEscape(id)
	}
	return fmt.Sprintf(format, args...)
}
