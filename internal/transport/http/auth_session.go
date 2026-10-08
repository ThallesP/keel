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

// Session cookie, CSRF and request metadata for the auth area. withActor (server.go) calls
// authViaCookie, authSafeMethod, authCSRFRefusal and authSessionCookie.

// authViaCookie: the request is authenticated by the keel_session cookie (a browser), not by a
// bearer. Only such requests can be forged cross-site: a page on another origin cannot add an
// Authorization header without a CORS preflight, which this server never grants.
func authViaCookie(r *http.Request) bool {
	if strings.HasPrefix(r.Header.Get("Authorization"), "Bearer ") {
		return false
	}
	c, err := r.Cookie(SessionCookie)
	return err == nil && c.Value != ""
}

// authViaBearer: the request authenticates with Authorization: Bearer (CLI, CI, agents).
func authViaBearer(r *http.Request) bool {
	return strings.HasPrefix(r.Header.Get("Authorization"), "Bearer ")
}

// authSafeMethod: methods that never change anything.
func authSafeMethod(method string) bool {
	return method == http.MethodGet || method == http.MethodHead || method == http.MethodOptions
}

// authCSRFRefusal is the reason to refuse a cookie-authenticated unsafe request, or "" to let it
// through: its Origin (else Referer) must name this host, as the request addressed it or as
// KEEL_SITE_URL does (Better Auth's origin check, auth-orgs.md §4.6).
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
	if s.app != nil && s.app.Config.SiteURL != "" {
		if site, err := url.Parse(s.app.Config.SiteURL); err == nil && site.Host != "" && strings.EqualFold(u.Host, site.Host) {
			return ""
		}
	}
	return domain.MsgInvalidOrigin
}

// authForbidden writes the CSRF refusal.
func authForbidden(w http.ResponseWriter, msg string) {
	writeProblem(w, &api.Problem{Status: http.StatusForbidden, Title: http.StatusText(http.StatusForbidden), Detail: msg, Code: domain.CodeForbidden})
}

// authSecure: cookies are Secure when the dashboard is served over https.
func (s *Server) authSecure() bool {
	return s.app != nil && strings.HasPrefix(strings.ToLower(s.app.Config.SiteURL), "https://")
}

func (s *Server) authNow() int64 {
	if s.app != nil && s.app.Now != nil {
		return s.app.Now()
	}
	return time.Now().UnixMilli()
}

// authSessionCookie is keel_session carrying token until expiresAt (unix ms): HttpOnly,
// SameSite=Lax, Path=/, Secure on https.
func (s *Server) authSessionCookie(token string, expiresAt int64) http.Cookie {
	maxAge := int((expiresAt - s.authNow()) / 1000)
	if maxAge < 1 {
		maxAge = -1
	}
	return http.Cookie{
		Name: SessionCookie, Value: token, Path: "/", HttpOnly: true, SameSite: http.SameSiteLaxMode,
		Secure: s.authSecure(), MaxAge: maxAge, Expires: time.UnixMilli(expiresAt).UTC(),
	}
}

// authClearedCookie removes keel_session.
func (s *Server) authClearedCookie() http.Cookie {
	return http.Cookie{
		Name: SessionCookie, Value: "", Path: "/", HttpOnly: true, SameSite: http.SameSiteLaxMode,
		Secure: s.authSecure(), MaxAge: -1, Expires: time.Unix(0, 0).UTC(),
	}
}

// authRemoteIP is the client address without its port. X-Forwarded-For is not trusted: keel
// serve is reached directly (compose publishes it), so the header would be the client's word.
func authRemoteIP(remoteAddr string) string {
	if host, _, err := net.SplitHostPort(remoteAddr); err == nil {
		return host
	}
	return remoteAddr
}

type authClientKey struct{}

// authWithClient remembers where the request came from (withActor calls it).
func authWithClient(ctx context.Context, r *http.Request) context.Context {
	return context.WithValue(ctx, authClientKey{}, app.ClientInfo{IP: authRemoteIP(r.RemoteAddr), UserAgent: r.UserAgent()})
}

// authClientOf is the caller's address and user agent, recorded on the sessions it creates.
func authClientOf(ctx context.Context) app.ClientInfo {
	c, _ := ctx.Value(authClientKey{}).(app.ClientInfo)
	return c
}

// authDeviceError is an RFC 8628 error body ({error, error_description}) with its status.
type authDeviceError struct {
	status int
	body   api.DeviceError
}

func (e *authDeviceError) Error() string                { return e.body.ErrorDescription }
func (e *authDeviceError) GetStatus() int               { return e.status }
func (e *authDeviceError) MarshalJSON() ([]byte, error) { return json.Marshal(e.body) }

// authOp registers an operation of the auth area. Like op, but RFC 8628 refusals keep their
// shape, and rate limits become 429 with Retry-After.
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
			return nil, huma.ErrorWithHeaders(p, http.Header{"Retry-After": {after}, "X-Retry-After": {after}})
		}
		return nil, problemOf(err)
	})
}
