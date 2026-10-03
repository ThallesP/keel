// Package keel is a Keel install as the CLI sees it: find its Convex URLs, sign in through
// better-auth, and call the same Convex functions the dashboard calls. Every error it returns
// is an *output.Error.
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

// SignIn exchanges email and password for a better-auth session token.
func SignIn(ctx context.Context, inst *config.Instance, email, password string) (string, error) {
	var out struct {
		Token string `json:"token"`
	}
	err := authCall(ctx, inst, http.MethodPost, "/api/auth/sign-in/email", "",
		map[string]string{"email": email, "password": password}, &out)
	if err != nil {
		return "", err
	}
	return out.Token, nil
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

// authCall is one better-auth endpoint on the Convex site URL. Origin is the dashboard URL, the
// only origin the install trusts.
func authCall(ctx context.Context, inst *config.Instance, method, path, token string, in, out any) error {
	var body io.Reader
	if in != nil {
		b, err := json.Marshal(in)
		if err != nil {
			return err
		}
		body = bytes.NewReader(b)
	}
	headers := map[string]string{"Origin": inst.URL}
	if token != "" {
		headers["Authorization"] = "Bearer " + token
	}
	resp, err := request(ctx, method, inst.ConvexSiteURL+path, body, headers)
	if err != nil {
		return translate(err, inst.URL)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode == http.StatusOK {
		if out == nil {
			return nil
		}
		if err := json.Unmarshal(raw, out); err != nil {
			return output.Errorf(output.CodeServer, "", "unexpected response from %s: %v", path, err)
		}
		return nil
	}
	var e struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	}
	json.Unmarshal(raw, &e)
	switch {
	case e.Code == "INVALID_EMAIL_OR_PASSWORD":
		return notAuthenticated(inst.URL, "Wrong email or password")
	case resp.StatusCode == http.StatusUnauthorized:
		return notAuthenticated(inst.URL, "Session expired or signed out")
	case e.Message != "":
		return output.Errorf(output.CodeServer, "", "%s: %s", path, e.Message)
	}
	return output.Errorf(output.CodeServer, "", "%s: HTTP %d", path, resp.StatusCode)
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
