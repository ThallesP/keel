package client

import (
	"context"
	"encoding/json"
	"net/http"
	"time"

	"github.com/ThallesP/keel/internal/api"
	"github.com/ThallesP/keel/internal/cli/config"
	"github.com/ThallesP/keel/internal/cli/output"
	"github.com/ThallesP/keel/internal/domain"
)

func Discover(ctx context.Context, webURL string) error {
	r, err := New(webURL, "").send(ctx, http.MethodGet, "/api/meta", nil, nil)
	if err != nil {
		return translate(err, webURL)
	}
	var m api.Meta
	if r.status == http.StatusOK && json.Unmarshal(r.body, &m) == nil && m.Name == "keel" {
		return nil
	}
	return output.Errorf(output.CodeDiscoveryFailed, "keel login <the URL you open the dashboard at>",
		"%s doesn't look like a Keel dashboard (no /api/meta)", webURL)
}

func (c *Client) StartLogin(ctx context.Context) (*config.PendingLogin, error) {
	const path = "/api/auth/device/code"
	r, err := c.send(ctx, http.MethodPost, path, nil, api.DeviceCodeRequest{ClientID: domain.DeviceClientID})
	if err != nil {
		return nil, translate(err, c.URL)
	}
	var out api.DeviceCode
	if r.status != http.StatusOK || json.Unmarshal(r.body, &out) != nil {
		return nil, c.deviceFailure(path, r)
	}
	return &config.PendingLogin{
		DeviceCode: out.DeviceCode,
		UserCode:   out.UserCode,
		URL:        out.VerificationURIComplete,
		ExpiresAt:  time.Now().Add(time.Duration(out.ExpiresIn) * time.Second).UTC().Truncate(time.Second),
		Interval:   max(out.Interval, 1),
	}, nil
}

func (c *Client) PollLogin(ctx context.Context, deviceCode string) (token string, slowDown bool, err error) {
	const path = "/api/auth/device/token"
	r, err := c.send(ctx, http.MethodPost, path, nil, api.DeviceTokenRequest{
		GrantType:  domain.DeviceGrantType,
		DeviceCode: deviceCode,
		ClientID:   domain.DeviceClientID,
	})
	if err != nil {
		return "", false, translate(err, c.URL)
	}
	var out struct {
		api.DeviceToken
		api.DeviceError
	}
	_ = json.Unmarshal(r.body, &out)
	if r.status == http.StatusOK && out.AccessToken != "" {
		return out.AccessToken, false, nil
	}
	switch out.Error {
	case "authorization_pending":
		return "", false, nil
	case "slow_down":
		return "", true, nil
	case "expired_token":
		return "", false, notAuthenticated(c.URL, "The login link expired before anyone approved it")
	case "access_denied":
		return "", false, notAuthenticated(c.URL, "The login was denied in the dashboard")
	case "invalid_grant":
		return "", false, notAuthenticated(c.URL, "The login link is used up or unknown")
	}
	return "", false, c.deviceFailure(path, r)
}

func (c *Client) deviceFailure(path string, r *reply) *output.Error {
	var e api.DeviceError
	if json.Unmarshal(r.body, &e) != nil || e.ErrorDescription == "" {
		return c.failure("POST "+path, r)
	}
	return output.Errorf(output.CodeServer, "", "%s: %s", path, e.ErrorDescription)
}

func (c *Client) SignOut(ctx context.Context) error {
	return c.call(ctx, http.MethodPost, "/api/auth/sign-out", nil, nil, nil)
}

func (c *Client) Me(ctx context.Context) (*api.User, *api.Organization, error) {
	if c.Token == "" {
		return nil, nil, notAuthenticated(c.URL, "Not logged in")
	}
	var me api.Me
	err := c.call(ctx, http.MethodGet, "/api/me", nil, nil, &me)
	if output.CodeOf(err) == output.CodeServer {
		if derr := Discover(ctx, c.URL); output.CodeOf(derr) == output.CodeDiscoveryFailed {
			return nil, nil, derr
		}
	}
	if err != nil {
		return nil, nil, err
	}
	if me.User == nil {
		return nil, nil, notAuthenticated(c.URL, "Session expired or signed out")
	}
	return me.User, me.Organization, nil
}
