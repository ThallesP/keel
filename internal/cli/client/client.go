// Package client is a Keel install as the CLI sees it: find it from its dashboard URL
// (GET /api/meta), sign in through the device login (RFC 8628), and call Keel's HTTP API
// (openapi.json) with the session token as a bearer. Dashboard and API share one origin, so the
// dashboard URL is the API's base. Every error it returns is an *output.Error.
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

	"github.com/ThallesP/keel/internal/cli/output"
)

// UserAgent is set by the CLI to keel-cli/<version>.
var UserAgent = "keel-cli"

var sharedHTTP = &http.Client{Timeout: 60 * time.Second}

// Client is one install's API, as one account when Token is set.
type Client struct {
	// Dashboard URL (scheme://host[:port]), which is also the API's origin.
	URL string
	// Session token, sent as `Authorization: Bearer`.
	Token string
	// nil: a shared client with a 60-second timeout.
	HTTP *http.Client
}

// New is a client for the install at url, signed in with token ("" for none).
func New(url, token string) *Client { return &Client{URL: url, Token: token} }

// reply is one HTTP response, read whole.
type reply struct {
	status int
	header http.Header
	body   []byte
}

// send makes one request to the install. A nil in sends no body.
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
	// The dashboard's origin, as a browser would send it. Bearer requests need no CSRF check, but
	// an install behind a proxy that wants one gets it.
	req.Header.Set("Origin", c.URL)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if c.Token != "" {
		req.Header.Set("Authorization", "Bearer "+c.Token)
	}
	resp, err := cmp.Or(c.HTTP, sharedHTTP).Do(req)
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

// call is one API operation: in as the JSON body (nil: none), a 2xx JSON answer decoded into out
// (nil: ignored). Anything else is the error the server described, as the CLI reports it.
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

// problem is an error body: RFC 9457 problem details with Keel's `code` (api.Problem), plus the
// members a problem may add (`slug` on a taken project name).
type problem struct {
	Status int    `json:"status"`
	Title  string `json:"title"`
	Detail string `json:"detail"`
	Code   string `json:"code"`
	Slug   string `json:"slug"`
	Errors []struct {
		Message  string `json:"message"`
		Location string `json:"location"`
	} `json:"errors"`
}

// failure turns a non-2xx response into the CLI's error. The code is the server's, as is (the
// API's codes are the CLI's vocabulary); the CLI adds the fix. A body without a code (a proxy in
// front of the install, an older server) falls back on the status and the message text.
func (c *Client) failure(what string, r *reply) *output.Error {
	var p problem
	_ = json.Unmarshal(r.body, &p)
	msg := p.Detail
	if len(p.Errors) > 0 {
		details := make([]string, len(p.Errors))
		for i, e := range p.Errors {
			details[i] = strings.TrimSpace(e.Location + " " + e.Message)
		}
		msg = strings.TrimSpace(msg + ": " + strings.Join(details, "; "))
	}
	code := p.Code
	if code == "" {
		code = codeOfMessage(msg)
	}
	if code == "" {
		switch r.status {
		case http.StatusUnauthorized:
			code = output.CodeNotAuthenticated
		case http.StatusTooManyRequests:
			code = output.CodeRateLimited
		}
	}
	if code == "" {
		return output.Errorf(output.CodeServer, "", "%s: HTTP %d: %s", what, r.status, snippet(r.body))
	}
	msg = cmp.Or(msg, p.Title, http.StatusText(r.status))
	return withFix(code, msg, p.Slug, c.URL, r.header)
}

// withFix is a server error as the CLI reports it: the server's code and message, and the next
// command to run (docs/cli.md, "Contract").
func withFix(code, msg, slug, webURL string, h http.Header) *output.Error {
	fix := ""
	switch code {
	case output.CodeNotAuthenticated:
		// Every signed-in call carries a token: refused, it is dead.
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
		if slug == "" {
			slug = takenProject(msg)
		}
		if slug != "" {
			fix = "Pick another name, or use it: keel link " + slug
		} else {
			fix = "Pick another name; keel service list shows the taken ones"
		}
	case output.CodeRateLimited:
		fix = "Wait a moment, then retry"
		if s := h.Get("Retry-After"); s != "" {
			fix = "Retry in " + s + "s"
		}
	}
	return &output.Error{Code: code, Message: msg, Fix: fix}
}

// takenProject is the slug in `Project "<slug>" already exists`, "" for other messages.
func takenProject(msg string) string {
	if s, ok := strings.CutPrefix(msg, `Project "`); ok {
		if s, ok := strings.CutSuffix(s, `" already exists`); ok {
			return s
		}
	}
	return ""
}

// codeOfMessage is the code of a Convex-era message, for an error body without a code.
func codeOfMessage(msg string) string {
	switch {
	case msg == "Not authenticated":
		return output.CodeNotAuthenticated
	case strings.HasPrefix(msg, "You're not in an organization"):
		return output.CodeNoOrganization
	case msg == "A deployment is already running":
		return output.CodeDeploymentRunning
	case msg == "Nothing to ship":
		return output.CodeNothingToShip
	case msg == "Node not found":
		return output.CodeServiceNotFound
	case msg == "Connect Axiom to see traces" || msg == "Sign in with Axiom again to turn on traces":
		return output.CodeTracesOff
	case msg == "Environment not found":
		return output.CodeProjectNotFound
	case takenProject(msg) != "", strings.HasSuffix(msg, `" is already taken`):
		return output.CodeNameTaken
	}
	return ""
}

// translate turns a transport error into the CLI's: CANCELLED, TIMEOUT, NETWORK_ERROR, or
// SERVER_ERROR for anything else.
func translate(err error, webURL string) error {
	var oe *output.Error
	if errors.As(err, &oe) {
		return oe
	}
	if errors.Is(err, context.Canceled) {
		return output.Errorf(output.CodeCancelled, "", "Cancelled")
	}
	var ne net.Error // *url.Error, from every failed request, is one
	if errors.As(err, &ne) {
		host := webURL
		if u, perr := url.Parse(webURL); perr == nil && u.Host != "" {
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

func notAuthenticated(url, msg string) *output.Error {
	fix := "keel login <dashboard-url>, or set KEEL_URL and KEEL_TOKEN"
	if url != "" {
		fix = "keel login " + url
	}
	return &output.Error{Code: output.CodeNotAuthenticated, Message: msg, Fix: fix}
}

func snippet(b []byte) string {
	s := strings.TrimSpace(string(b))
	if len(s) > 200 {
		s = s[:200] + "…"
	}
	return s
}

// apiPath fills the %s of an API path with ids, escaped: they may come from the user.
func apiPath(format string, ids ...string) string {
	args := make([]any, len(ids))
	for i, id := range ids {
		args[i] = url.PathEscape(id)
	}
	return fmt.Sprintf(format, args...)
}
