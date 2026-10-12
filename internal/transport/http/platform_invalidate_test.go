package http

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/ThallesP/keel/internal/app"
	"github.com/ThallesP/keel/internal/domain"
)

func TestKeelInvalidateHeader(t *testing.T) {
	var sawRecorder bool
	handler := withInvalidations(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rec := app.InvalidationsFrom(r.Context())
		sawRecorder = rec != nil
		if sawRecorder {
			rec.Add("org-a", "/api/projects", "/api/environments/e1")
			rec.Add("org-b", "/api/environments/other-org")
		}
		if r.URL.Query().Get("empty") != "" {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("{}"))
	}))
	do := func(method, target string, actor domain.Actor) *httptest.ResponseRecorder {
		t.Helper()
		r := httptest.NewRequest(method, target, nil)
		r = r.WithContext(context.WithValue(r.Context(), actorKey{}, actor))
		w := httptest.NewRecorder()
		sawRecorder = false
		handler.ServeHTTP(w, r)
		return w
	}
	member := domain.Actor{UserID: "u1", OrganizationID: "org-a"}

	w := do(http.MethodPost, "/api/nodes/n1/variables", member)
	if got := w.Header().Get(InvalidateHeader); got != "/api/environments/e1,/api/projects" {
		t.Fatalf("POST: %s = %q", InvalidateHeader, got)
	}
	for _, m := range []string{http.MethodPut, http.MethodPatch, http.MethodDelete} {
		if got := do(m, "/api/nodes/n1", member).Header().Get(InvalidateHeader); got != "/api/environments/e1,/api/projects" {
			t.Fatalf("%s: %q", m, got)
		}
	}
	if got := do(http.MethodPost, "/api/nodes/n1/variables?empty=1", member).Header().Get(InvalidateHeader); got == "" {
		t.Fatal("204 without the header")
	}
	newbie := domain.Actor{UserID: "u2"}
	if got := do(http.MethodPost, "/api/projects/default", newbie).Header().Get(InvalidateHeader); got != "/api/environments/e1,/api/environments/other-org,/api/projects" {
		t.Fatalf("no-org caller: %q", got)
	}
	for _, tc := range []struct{ method, target string }{
		{http.MethodGet, "/api/projects"},
		{http.MethodHead, "/api/projects"},
		{http.MethodPost, "/worker/events"},
		{http.MethodPost, "/proxy/events"},
		{http.MethodPost, "/otlp/v1/traces"},
	} {
		w := do(tc.method, tc.target, member)
		if sawRecorder || w.Header().Get(InvalidateHeader) != "" {
			t.Fatalf("%s %s: recorder %v, header %q", tc.method, tc.target, sawRecorder, w.Header().Get(InvalidateHeader))
		}
	}
	quiet := withInvalidations(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	rw := httptest.NewRecorder()
	quiet.ServeHTTP(rw, httptest.NewRequest(http.MethodPost, "/api/auth/sign-out", nil))
	if _, ok := rw.Header()[InvalidateHeader]; ok {
		t.Fatal("header without topics")
	}
}
