package http

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/danielgtaylor/huma/v2/adapters/humago"

	"github.com/ThallesP/keel/internal/api"
	"github.com/ThallesP/keel/internal/app"
	"github.com/ThallesP/keel/internal/domain"
)

func registryGet(t *testing.T, reg app.Registry, actor domain.Actor) (int, []byte) {
	t.Helper()
	mux := http.NewServeMux()
	s := &Server{app: app.New(app.App{Config: app.Config{Registry: reg}})}
	s.registerRegistry(humago.New(mux, Config("test")))
	req := httptest.NewRequest(http.MethodGet, "/api/registry", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req.WithContext(context.WithValue(req.Context(), actorKey{}, actor)))
	return rec.Code, rec.Body.Bytes()
}

func TestRegistryLogin(t *testing.T) {
	reg := app.Registry{Host: "100.64.0.7:5000", Username: "keel", Password: "pw", CA: "-----BEGIN CERTIFICATE-----"}
	member := domain.Actor{UserID: "u", OrganizationID: "org"}

	status, body := registryGet(t, reg, member)
	if status != http.StatusOK {
		t.Fatalf("member: %d %s", status, body)
	}
	var got api.Registry
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatal(err)
	}
	if got != (api.Registry{Host: reg.Host, Username: reg.Username, Password: reg.Password, CA: reg.CA}) {
		t.Fatalf("got %+v", got)
	}

	for name, c := range map[string]struct {
		reg   app.Registry
		actor domain.Actor
		code  string
		http  int
	}{
		"signed out":         {reg, domain.Actor{}, domain.CodeNotAuthenticated, http.StatusUnauthorized},
		"no organization":    {reg, domain.Actor{UserID: "u"}, domain.CodeNoOrganization, http.StatusForbidden},
		"install without it": {app.Registry{}, member, domain.CodeUnavailable, http.StatusServiceUnavailable},
	} {
		t.Run(name, func(t *testing.T) {
			status, body := registryGet(t, c.reg, c.actor)
			var p api.Problem
			_ = json.Unmarshal(body, &p)
			if status != c.http || p.Code != c.code {
				t.Fatalf("got %d %s, want %d %s", status, body, c.http, c.code)
			}
		})
	}
}
