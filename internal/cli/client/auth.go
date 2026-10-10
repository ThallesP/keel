package client

import (
	"context"
	"encoding/json"
	"net/http"
	"time"

	"github.com/ThallesP/keel/internal/api"
	"github.com/ThallesP/keel/internal/cli/config"
	"github.com/ThallesP/keel/internal/cli/output"
)

func Discover(ctx context.Context, webURL string) (*api.Meta, error) {
	c := New(webURL, "")
	r, err := c.send(ctx, http.MethodGet, "/api/meta", nil, nil)
	if err != nil {
		return nil, translate(err, webURL)
	}
	var m api.Meta
	if r.status == http.StatusOK && json.Unmarshal(r.body, &m) == nil && m.Name == "keel" {
		return &m, nil
	}
	return nil, output.Errorf(output.CodeDiscoveryFailed, "keel login <the URL you open the dashboard at>",
		"%s doesn't look like a Keel dashboard (no /api/meta)", webURL)
}

const ClientID = "keel-cli"

func (c *Client) StartLogin(ctx context.Context) (*config.PendingLogin, error) {
	var out api.DeviceCode
	if err := c.device(ctx, "/api/auth/device/code", api.DeviceCodeRequest{ClientID: ClientID}, &out); err != nil {
		return nil, err
	}
	return &config.PendingLogin{
		DeviceCode: out.DeviceCode,
		UserCode:   out.UserCode,
		URL:        out.VerificationURIComplete,
		ExpiresAt:  time.Now().Add(time.Duration(out.ExpiresIn) * time.Second).UTC().Truncate(time.Second),
		Interval:   max(out.Interval, 1),
	}, nil
}

type deviceReply struct {
	AccessToken string `json:"access_token"`
	Error       string `json:"error"`
	Description string `json:"error_description"`
	Code        string `json:"code"`
}

func (c *Client) PollLogin(ctx context.Context, deviceCode string) (token string, slowDown bool, err error) {
	const path = "/api/auth/device/token"
	r, err := c.send(ctx, http.MethodPost, path, nil, api.DeviceTokenRequest{
		GrantType:  "urn:ietf:params:oauth:grant-type:device_code",
		DeviceCode: deviceCode,
		ClientID:   ClientID,
	})
	if err != nil {
		return "", false, translate(err, c.URL)
	}
	var out deviceReply
	_ = json.Unmarshal(r.body, &out)
	switch {
	case r.status == http.StatusOK && out.AccessToken != "":
		return out.AccessToken, false, nil
	case out.Error == "authorization_pending":
		return "", false, nil
	case out.Error == "slow_down":
		return "", true, nil
	case out.Error == "expired_token":
		return "", false, notAuthenticated(c.URL, "The login link expired before anyone approved it")
	case out.Error == "access_denied":
		return "", false, notAuthenticated(c.URL, "The login was denied in the dashboard")
	case out.Error == "invalid_grant":
		return "", false, notAuthenticated(c.URL, "The login link is used up or unknown")
	}
	return "", false, c.deviceFailure(path, r, out)
}

func (c *Client) device(ctx context.Context, path string, in, out any) error {
	r, err := c.send(ctx, http.MethodPost, path, nil, in)
	if err != nil {
		return translate(err, c.URL)
	}
	if r.status == http.StatusOK {
		if err := json.Unmarshal(r.body, out); err != nil {
			return output.Errorf(output.CodeServer, "", "unexpected response from %s: %v", path, err)
		}
		return nil
	}
	var e deviceReply
	_ = json.Unmarshal(r.body, &e)
	return c.deviceFailure(path, r, e)
}

func (c *Client) deviceFailure(path string, r *reply, e deviceReply) *output.Error {
	switch {
	case e.Code != "":
		return c.failure("POST "+path, r)
	case r.status == http.StatusUnauthorized:
		return notAuthenticated(c.URL, "Session expired or signed out")
	case e.Description != "":
		return output.Errorf(output.CodeServer, "", "%s: %s", path, e.Description)
	}
	return output.Errorf(output.CodeServer, "", "%s: HTTP %d", path, r.status)
}

func (c *Client) SignOut(ctx context.Context) error {
	return c.call(ctx, http.MethodPost, "/api/auth/sign-out", nil, nil, nil)
}

func (c *Client) Me(ctx context.Context) (*User, *Organization, error) {
	if c.Token == "" {
		return nil, nil, notAuthenticated(c.URL, "Not logged in")
	}
	var me api.Me
	if err := c.call(ctx, http.MethodGet, "/api/me", nil, nil, &me); err != nil {
		if output.CodeOf(err) == output.CodeServer {
			if _, derr := Discover(ctx, c.URL); output.CodeOf(derr) == output.CodeDiscoveryFailed {
				return nil, nil, derr
			}
		}
		return nil, nil, err
	}
	if me.User == nil {
		return nil, nil, notAuthenticated(c.URL, "Session expired or signed out")
	}
	return me.User, me.Organization, nil
}
