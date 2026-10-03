// Package keel is a Keel install as the CLI sees it: find its Convex URLs, sign in through
// better-auth's device authorization, and call the same Convex functions the dashboard calls.
// Every error it returns is an *output.Error.
package keel

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/ThallesP/keel/apps/cli/internal/config"
	"github.com/ThallesP/keel/apps/cli/internal/convex"
	"github.com/ThallesP/keel/apps/cli/internal/output"
)

// UserAgent is set by main to include the version.
var UserAgent = "keel-cli"

var httpClient = &http.Client{Timeout: 60 * time.Second}

// Discover reads an install's Convex URLs from the dashboard's /config.js, which the web image
// writes at start (apps/web/docker-entrypoint.sh): `window.__KEEL__ = {"convexUrl":…};`.
func Discover(ctx context.Context, webURL string) (convexURL, siteURL string, err error) {
	fix := fmt.Sprintf("keel login %s --convex-url <url> --convex-site-url <url>", webURL)
	resp, err := request(ctx, http.MethodGet, webURL+"/config.js", nil, nil)
	if err != nil {
		return "", "", translate(err, webURL)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	start, end := bytes.IndexByte(body, '{'), bytes.LastIndexByte(body, '}')
	var cfg struct {
		ConvexURL     string `json:"convexUrl"`
		ConvexSiteURL string `json:"convexSiteUrl"`
	}
	if resp.StatusCode != http.StatusOK || start < 0 || end < start ||
		json.Unmarshal(body[start:end+1], &cfg) != nil {
		return "", "", output.Errorf(output.CodeDiscoveryFailed, fix,
			"%s doesn't look like a Keel dashboard (no /config.js)", webURL)
	}
	if cfg.ConvexURL == "" || cfg.ConvexSiteURL == "" {
		return "", "", output.Errorf(output.CodeDiscoveryFailed, fix,
			"%s/config.js has no Convex URLs (a dev server?)", webURL)
	}
	return strings.TrimRight(cfg.ConvexURL, "/"), strings.TrimRight(cfg.ConvexSiteURL, "/"), nil
}

// clientID is the CLI's client id for device authorization; convex/auth.ts accepts only it.
const clientID = "keel-cli"

// StartLogin starts a device authorization (RFC 8628): a link to the dashboard that someone
// signed in approves, after which PollLogin gets a session for their account.
func StartLogin(ctx context.Context, inst *config.Instance) (*config.PendingLogin, error) {
	var out struct {
		DeviceCode string `json:"device_code"`
		UserCode   string `json:"user_code"`
		URL        string `json:"verification_uri_complete"`
		ExpiresIn  int    `json:"expires_in"`
		Interval   int    `json:"interval"`
	}
	err := authCall(ctx, inst, http.MethodPost, "/api/auth/device/code", "",
		map[string]string{"client_id": clientID}, &out)
	if err != nil {
		return nil, err
	}
	return &config.PendingLogin{
		DeviceCode: out.DeviceCode,
		UserCode:   out.UserCode,
		URL:        out.URL,
		ExpiresAt:  time.Now().Add(time.Duration(out.ExpiresIn) * time.Second).UTC().Truncate(time.Second),
		Interval:   max(out.Interval, 1),
	}, nil
}

// PollLogin asks once whether inst.Pending was approved and returns the session token if so. No
// token and no error means not yet; slowDown means the install wants polls further apart. The
// token is handed out once, to the first poll after the approval.
func PollLogin(ctx context.Context, inst *config.Instance) (token string, slowDown bool, err error) {
	status, raw, err := authDo(ctx, inst, http.MethodPost, "/api/auth/device/token", "", map[string]string{
		"grant_type":  "urn:ietf:params:oauth:grant-type:device_code",
		"device_code": inst.Pending.DeviceCode,
		"client_id":   clientID,
	})
	if err != nil {
		return "", false, err
	}
	var out struct {
		AccessToken string `json:"access_token"`
		Error       string `json:"error"`
		Description string `json:"error_description"`
	}
	json.Unmarshal(raw, &out)
	switch {
	case status == http.StatusOK && out.AccessToken != "":
		return out.AccessToken, false, nil
	case out.Error == "authorization_pending":
		return "", false, nil
	case out.Error == "slow_down":
		return "", true, nil
	case out.Error == "expired_token":
		return "", false, notAuthenticated(inst.URL, "The login link expired before anyone approved it")
	case out.Error == "access_denied":
		return "", false, notAuthenticated(inst.URL, "The login was denied in the dashboard")
	case out.Error == "invalid_grant":
		return "", false, notAuthenticated(inst.URL, "The login link is used up or unknown")
	case out.Description != "":
		return "", false, output.Errorf(output.CodeServer, "", "/api/auth/device/token: %s", out.Description)
	}
	return "", false, output.Errorf(output.CodeServer, "", "/api/auth/device/token: HTTP %d", status)
}

// SignOut revokes the session server-side.
func SignOut(ctx context.Context, inst *config.Instance) error {
	return authCall(ctx, inst, http.MethodPost, "/api/auth/sign-out", inst.Token, struct{}{}, nil)
}

// Connect swaps the session token for a Convex JWT. JWTs live 15 minutes, so every run gets a
// fresh one rather than caching it.
func Connect(ctx context.Context, inst *config.Instance) (*API, error) {
	if inst.Token == "" {
		return nil, notAuthenticated(inst.URL, "Not logged in")
	}
	var out struct {
		Token string `json:"token"`
	}
	if err := authCall(ctx, inst, http.MethodGet, "/api/auth/convex/token", inst.Token, nil, &out); err != nil {
		return nil, err
	}
	return &API{
		url: inst.URL,
		c:   &convex.Client{URL: inst.ConvexURL, Token: out.Token, HTTP: httpClient},
	}, nil
}

// authCall is one better-auth endpoint on the Convex site URL, decoding its JSON into out.
func authCall(ctx context.Context, inst *config.Instance, method, path, token string, in, out any) error {
	status, raw, err := authDo(ctx, inst, method, path, token, in)
	if err != nil {
		return err
	}
	if status == http.StatusOK {
		if out == nil {
			return nil
		}
		if err := json.Unmarshal(raw, out); err != nil {
			return output.Errorf(output.CodeServer, "", "unexpected response from %s: %v", path, err)
		}
		return nil
	}
	var e struct {
		Message     string `json:"message"`
		Description string `json:"error_description"`
	}
	json.Unmarshal(raw, &e)
	switch {
	case status == http.StatusUnauthorized:
		return notAuthenticated(inst.URL, "Session expired or signed out")
	case e.Message != "" || e.Description != "":
		return output.Errorf(output.CodeServer, "", "%s: %s", path, or(e.Message, e.Description))
	}
	return output.Errorf(output.CodeServer, "", "%s: HTTP %d", path, status)
}

// authDo sends one request to a better-auth endpoint and returns the status and body. Origin is
// the dashboard URL, the only origin the install trusts.
func authDo(ctx context.Context, inst *config.Instance, method, path, token string, in any) (int, []byte, error) {
	var body io.Reader
	if in != nil {
		b, err := json.Marshal(in)
		if err != nil {
			return 0, nil, err
		}
		body = bytes.NewReader(b)
	}
	headers := map[string]string{"Origin": inst.URL}
	if token != "" {
		headers["Authorization"] = "Bearer " + token
	}
	resp, err := request(ctx, method, inst.ConvexSiteURL+path, body, headers)
	if err != nil {
		return 0, nil, translate(err, inst.URL)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, raw, nil
}

func or(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

func request(ctx context.Context, method, url string, body io.Reader, headers map[string]string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, method, url, body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", UserAgent)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	return httpClient.Do(req)
}

func notAuthenticated(url, msg string) *output.Error {
	fix := "keel login <dashboard-url>, or set KEEL_URL and KEEL_TOKEN"
	if url != "" {
		fix = "keel login " + url
	}
	return &output.Error{Code: output.CodeNotAuthenticated, Message: msg, Fix: fix}
}
