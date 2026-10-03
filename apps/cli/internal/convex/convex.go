// Package convex calls Convex functions over Convex's HTTP API: POST /api/{query,mutation,action}
// with {"path":"module:function","args":{...},"format":"json"} and the user's JWT as a bearer
// token. No subscriptions; callers that watch something poll.
package convex

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"
)

type Client struct {
	URL   string
	Token string
	HTTP  *http.Client
}

// FunctionError is a function that threw. Data is set for a ConvexError: the payload the
// function chose to expose, which for Keel is the user-facing message.
type FunctionError struct {
	Message string
	Data    json.RawMessage
}

func (e *FunctionError) Error() string { return e.Message }

// ErrUnauthenticated is a request Convex refused (HTTP 401) for its token, before any function ran.
var ErrUnauthenticated = errors.New("unauthenticated")

// DataString is Data when it is a JSON string (every ConvexError Keel throws), else "".
func (e *FunctionError) DataString() string {
	var s string
	if json.Unmarshal(e.Data, &s) != nil {
		return ""
	}
	return s
}

func (c *Client) Query(ctx context.Context, path string, args, out any) error {
	return c.call(ctx, "query", path, args, out)
}

func (c *Client) Mutation(ctx context.Context, path string, args, out any) error {
	return c.call(ctx, "mutation", path, args, out)
}

func (c *Client) Action(ctx context.Context, path string, args, out any) error {
	return c.call(ctx, "action", path, args, out)
}

func (c *Client) call(ctx context.Context, kind, path string, args, out any) error {
	encoded, err := json.Marshal(args)
	if err != nil {
		return err
	}
	if string(encoded) == "null" { // nil, or a nil map: Convex wants an object
		encoded = []byte("{}")
	}
	body, err := json.Marshal(map[string]any{"path": path, "args": json.RawMessage(encoded), "format": "json"})
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.URL+"/api/"+kind, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	if c.Token != "" {
		req.Header.Set("Authorization", "Bearer "+c.Token)
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}
	var r struct {
		Status       string          `json:"status"`
		Value        json.RawMessage `json:"value"`
		ErrorMessage string          `json:"errorMessage"`
		ErrorData    json.RawMessage `json:"errorData"`
	}
	if err := json.Unmarshal(raw, &r); err != nil || r.Status == "" {
		if resp.StatusCode == http.StatusUnauthorized {
			return fmt.Errorf("%s %s: %w: %s", kind, path, ErrUnauthenticated, snippet(raw))
		}
		return fmt.Errorf("%s %s: HTTP %d: %s", kind, path, resp.StatusCode, snippet(raw))
	}
	if r.Status != "success" {
		return &FunctionError{Message: clean(r.ErrorMessage), Data: r.ErrorData}
	}
	if out == nil {
		return nil
	}
	return json.Unmarshal(r.Value, out)
}

var requestID = regexp.MustCompile(`^\[Request ID: [^\]]*\] (Server Error\n)?`)

// clean drops the request-id prefix and the stack trace Convex appends.
func clean(msg string) string {
	msg = requestID.ReplaceAllString(msg, "")
	msg = strings.TrimPrefix(msg, "Uncaught ")
	if i := strings.Index(msg, "\n    at "); i >= 0 {
		msg = msg[:i]
	}
	return strings.TrimSpace(msg)
}

func snippet(b []byte) string {
	s := strings.TrimSpace(string(b))
	if len(s) > 200 {
		s = s[:200] + "…"
	}
	return s
}
