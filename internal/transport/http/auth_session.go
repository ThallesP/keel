package http

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/danielgtaylor/huma/v2"

	"github.com/ThallesP/keel/internal/api"
	"github.com/ThallesP/keel/internal/app"
	"github.com/ThallesP/keel/internal/domain"
)

func authViaCookie(r *http.Request) bool {
	if authViaBearer(r) {
		return false
	}
	c, err := r.Cookie(SessionCookie)
	return err == nil && c.Value != ""
}

func authViaBearer(r *http.Request) bool {
	return strings.HasPrefix(r.Header.Get("Authorization"), "Bearer ")
}

func authSafeMethod(method string) bool {
	return method == http.MethodGet || method == http.MethodHead || method == http.MethodOptions
}

func (s *Server) authCSRFRefusal(r *http.Request) string {
	src := r.Header.Get("Origin")
	if src == "" {
		src = r.Header.Get("Referer")
	}
	if src == "" || src == "null" {
		return domain.MsgMissingOrigin
	}
	u, err := url.Parse(src)
	if err != nil || u.Host == "" {
		return domain.MsgInvalidOrigin
	}
	if strings.EqualFold(u.Host, r.Host) {
		return ""
	}
	if site, err := url.Parse(s.app.Config.SiteURL); err == nil && site.Host != "" && strings.EqualFold(u.Host, site.Host) {
		return ""
	}
	return domain.MsgInvalidOrigin
}

func (s *Server) authSecure() bool {
	return strings.HasPrefix(strings.ToLower(s.app.Config.SiteURL), "https://")
}

func (s *Server) authSessionCookie(token string, expiresAt int64) http.Cookie {
	maxAge := int((expiresAt - s.app.Now()) / 1000)
	if maxAge < 1 {
		maxAge = -1
	}
	return http.Cookie{
		Name: SessionCookie, Value: token, Path: "/", HttpOnly: true, SameSite: http.SameSiteLaxMode,
		Secure: s.authSecure(), MaxAge: maxAge, Expires: time.UnixMilli(expiresAt).UTC(),
	}
}

func (s *Server) authClearedCookie() http.Cookie {
	return http.Cookie{
		Name: SessionCookie, Value: "", Path: "/", HttpOnly: true, SameSite: http.SameSiteLaxMode,
		Secure: s.authSecure(), MaxAge: -1, Expires: time.Unix(0, 0).UTC(),
	}
}

func authRemoteIP(remoteAddr string) string {
	if host, _, err := net.SplitHostPort(remoteAddr); err == nil {
		return host
	}
	return remoteAddr
}

type authClientKey struct{}

func authWithClient(ctx context.Context, r *http.Request) context.Context {
	return context.WithValue(ctx, authClientKey{}, app.ClientInfo{IP: authRemoteIP(r.RemoteAddr), UserAgent: r.UserAgent()})
}

func authClientOf(ctx context.Context) app.ClientInfo {
	c, _ := ctx.Value(authClientKey{}).(app.ClientInfo)
	return c
}

type authDeviceError struct {
	status int
	body   api.DeviceError
}

func (e *authDeviceError) Error() string                { return e.body.ErrorDescription }
func (e *authDeviceError) GetStatus() int               { return e.status }
func (e *authDeviceError) MarshalJSON() ([]byte, error) { return json.Marshal(e.body) }

func authOp[I, O any](h huma.API, o huma.Operation, handler func(ctx context.Context, in *I) (*O, error)) {
	huma.Register(h, o, func(ctx context.Context, in *I) (*O, error) {
		out, err := handler(ctx, in)
		if err == nil {
			return out, nil
		}
		var refusal *domain.DeviceRefusal
		if errors.As(err, &refusal) {
			return nil, &authDeviceError{status: refusal.Status, body: api.DeviceError{Error: refusal.Code, ErrorDescription: refusal.Description}}
		}
		var limited *domain.RateLimitError
		if errors.As(err, &limited) {
			p := &api.Problem{Status: http.StatusTooManyRequests, Title: http.StatusText(http.StatusTooManyRequests),
				Detail: domain.MsgTooManyRequests, Code: domain.CodeRateLimited}
			after := strconv.FormatInt(limited.RetryAfterSeconds, 10)
			return nil, huma.ErrorWithHeaders(p, http.Header{"Retry-After": {after}})
		}
		return nil, problemOf(err)
	})
}
