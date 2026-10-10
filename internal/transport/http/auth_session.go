package http

import (
	"cmp"
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/ThallesP/keel/internal/api"
	"github.com/ThallesP/keel/internal/app"
	"github.com/ThallesP/keel/internal/domain"
)

func authSafeMethod(method string) bool {
	return method == http.MethodGet || method == http.MethodHead || method == http.MethodOptions
}

func (s *Server) authCSRFRefusal(r *http.Request) *domain.Error {
	src := cmp.Or(r.Header.Get("Origin"), r.Header.Get("Referer"))
	if src == "" || src == "null" {
		return domain.E(domain.CodeForbidden, "Missing or null Origin")
	}
	u, err := url.Parse(src)
	if err != nil || u.Host == "" {
		return domain.E(domain.CodeForbidden, "Invalid origin")
	}
	if strings.EqualFold(u.Host, r.Host) {
		return nil
	}
	if site, err := url.Parse(s.app.Config.SiteURL); err == nil && strings.EqualFold(u.Host, site.Host) {
		return nil
	}
	return domain.E(domain.CodeForbidden, "Invalid origin")
}

func (s *Server) authSessionCookie(token string, expiresAt int64) http.Cookie {
	maxAge := int((expiresAt - s.app.Now()) / 1000)
	if maxAge < 1 {
		maxAge = -1
	}
	return http.Cookie{
		Name: SessionCookie, Value: token, Path: "/", HttpOnly: true, SameSite: http.SameSiteLaxMode,
		Secure: strings.HasPrefix(strings.ToLower(s.app.Config.SiteURL), "https://"),
		MaxAge: maxAge, Expires: time.UnixMilli(expiresAt),
	}
}

type authClientKey struct{}

func authClientOf(ctx context.Context) app.ClientInfo {
	c, _ := ctx.Value(authClientKey{}).(app.ClientInfo)
	return c
}

type authDeviceError struct{ *domain.DeviceRefusal }

func (e authDeviceError) GetStatus() int { return e.Status }

func (e authDeviceError) MarshalJSON() ([]byte, error) {
	return json.Marshal(api.DeviceError{Error: e.Code, ErrorDescription: e.Description})
}
