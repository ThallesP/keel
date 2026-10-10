package http

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/danielgtaylor/huma/v2/adapters/humago"

	"github.com/ThallesP/keel/internal/adapters/sqlite"
	"github.com/ThallesP/keel/internal/app"
	"github.com/ThallesP/keel/internal/domain"
)

type ingressHarness struct {
	t     *testing.T
	app   *app.App
	store *sqlite.Store
	actor domain.Actor
	auth  string
	h     http.Handler
}

func newIngressHarness(t *testing.T) *ingressHarness {
	t.Helper()
	store := testStore(t,
		`INSERT INTO organizations (id, name, slug, created_at) VALUES ('org', 'Acme', 'acme', 1), ('org-b', 'Other', 'other', 1)`,
		`INSERT INTO projects (id, organization_id, name, slug, created_at) VALUES ('p', 'org', 'Shop', 'shop', 1)`,
		`INSERT INTO environments (id, project_id, name, is_production, created_at) VALUES ('env', 'p', 'production', 1, 1)`,
	)
	for _, n := range []domain.Node{
		{ID: "j57a8x2kq3n4m5p6r7s8t9v0w1x2y3z4", EnvironmentID: "env", Type: domain.NodeService, Name: "api", CreatedAt: 1,
			Desired: &domain.Desired{Image: "api:1", Revision: 1, Replicas: 1, Port: 8080}, DeployedRevision: 1},
		{ID: "pg", EnvironmentID: "env", Type: domain.NodeDatabase, Name: "postgres", CreatedAt: 2,
			Desired: &domain.Desired{Image: "postgres:16", Revision: 1, Replicas: 1, Port: 8080}, DeployedRevision: 1},
	} {
		if err := store.Write(context.Background(), func(tx app.Tx) error { return tx.InsertNode(n) }); err != nil {
			t.Fatal(err)
		}
	}
	h := &ingressHarness{t: t, store: store}
	h.app = app.New(app.App{Store: store, Jobs: &recordedJobs{}, Config: app.Config{PublicIP: "203.0.113.7", WorkerToken: "s3cret"}})
	mux := http.NewServeMux()
	humaAPI := humago.New(mux, Config("test"))
	s := &Server{app: h.app}
	s.registerIngress(humaAPI)
	s.registerIngressRaw(mux)
	h.h = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mux.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), actorKey{}, h.actor)))
	})
	return h
}

func (h *ingressHarness) do(method, path, body string) (int, string) {
	h.t.Helper()
	var rd io.Reader
	if body != "" {
		rd = strings.NewReader(body)
	}
	req := httptest.NewRequest(method, path, rd)
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	if h.auth != "" {
		req.Header.Set("Authorization", h.auth)
	}
	rec := httptest.NewRecorder()
	h.h.ServeHTTP(rec, req)
	return rec.Code, rec.Body.String()
}

func (h *ingressHarness) problem(status int, body string, wantStatus int, code, detail string) {
	h.t.Helper()
	var p struct{ Code, Detail string }
	_ = json.Unmarshal([]byte(body), &p)
	if status != wantStatus || p.Code != code || p.Detail != detail {
		h.t.Fatalf("got %d %s, want %d %s %q", status, body, wantStatus, code, detail)
	}
}

var (
	ingressMember  = domain.Actor{UserID: "u1", OrganizationID: "org", Role: domain.RoleOwner}
	ingressForeign = domain.Actor{UserID: "u2", OrganizationID: "org-b", Role: domain.RoleOwner}
)

func TestIngressHTTPExpose(t *testing.T) {
	h := newIngressHarness(t)
	h.actor = ingressMember
	status, body := h.do("POST", "/api/nodes/j57a8x2kq3n4m5p6r7s8t9v0w1x2y3z4/expose", "")
	want := `{"protocol":"http","port":8080,"domain":"api-16w41g.203-0-113-7.sslip.io","address":"https://api-16w41g.203-0-113-7.sslip.io","state":"starting"}`
	if status != 200 || strings.TrimSpace(body) != want {
		t.Fatalf("got %d %s", status, body)
	}
	status, body = h.do("POST", "/api/nodes/pg/expose", `{"publicPort": 15432}`)
	want = `{"protocol":"tcp","port":8080,"publicPort":15432,"address":"203.0.113.7:15432","state":"starting"}`
	if status != 200 || strings.TrimSpace(body) != want {
		t.Fatalf("got %d %s", status, body)
	}

	status, body = h.do("POST", "/api/nodes/pg/expose", `{"port": 70000}`)
	h.problem(status, body, 422, domain.CodeInvalidInput, "Port must be 1–65535")
	status, body = h.do("POST", "/api/nodes/j57a8x2kq3n4m5p6r7s8t9v0w1x2y3z4/expose", `{"protocol":"tcp","publicPort":15432}`)
	h.problem(status, body, 409, domain.CodeConflict, "Port 15432/tcp is already used by postgres")
	status, body = h.do("POST", "/api/nodes/j57a8x2kq3n4m5p6r7s8t9v0w1x2y3z4/expose", `{"protocol":"ftp"}`)
	if status != 422 || !strings.Contains(body, `"code":"INVALID_INPUT"`) {
		t.Fatalf("bad protocol: %d %s", status, body)
	}

	h.actor = ingressForeign
	status, body = h.do("POST", "/api/nodes/pg/expose", `{}`)
	h.problem(status, body, 404, domain.CodeServiceNotFound, "Node not found")
	status, body = h.do("POST", "/api/nodes/pg/unexpose", "")
	h.problem(status, body, 404, domain.CodeServiceNotFound, "Node not found")

	h.actor = domain.Actor{}
	status, body = h.do("POST", "/api/nodes/pg/expose", `{}`)
	h.problem(status, body, 401, domain.CodeNotAuthenticated, "Not authenticated")
}

func TestIngressHTTPUnexpose(t *testing.T) {
	h := newIngressHarness(t)
	h.actor = ingressMember
	if status, body := h.do("POST", "/api/nodes/pg/expose", `{}`); status != 200 {
		t.Fatalf("expose: %d %s", status, body)
	}
	status, body := h.do("POST", "/api/nodes/pg/unexpose", `{"protocol":"tcp"}`)
	h.problem(status, body, 422, domain.CodeInvalidInput, "Name the endpoint: protocol and domain (http) or public port")
	if status, body := h.do("POST", "/api/nodes/pg/unexpose", `{"protocol":"tcp","publicPort":8080}`); status != 204 || body != "" {
		t.Fatalf("unexpose: %d %q", status, body)
	}
	if status, _ := h.do("POST", "/api/nodes/pg/unexpose", ""); status != 204 {
		t.Fatalf("make private with no body: %d", status)
	}
}

func TestIngressHTTPControlPlane(t *testing.T) {
	h := newIngressHarness(t)
	status, body := h.do("GET", "/api/control-plane", "")
	h.problem(status, body, 401, domain.CodeNotAuthenticated, "Not authenticated")

	h.actor = domain.Actor{UserID: "u3"}
	status, body = h.do("GET", "/api/control-plane", "")
	if status != 200 || strings.TrimSpace(body) != `{"publicIp":"203.0.113.7"}` {
		t.Fatalf("got %d %s", status, body)
	}
	h.app.Config.PublicIP = ""
	status, body = h.do("GET", "/api/control-plane", "")
	if status != 200 || strings.TrimSpace(body) != `{"publicIp":null}` {
		t.Fatalf("got %d %s", status, body)
	}
}

func TestIngressHTTPProxyEvents(t *testing.T) {
	h := newIngressHarness(t)
	h.actor = ingressMember
	if status, body := h.do("POST", "/api/nodes/j57a8x2kq3n4m5p6r7s8t9v0w1x2y3z4/expose", ""); status != 200 {
		t.Fatalf("expose: %d %s", status, body)
	}
	h.actor = domain.Actor{}
	const domainName = "api-16w41g.203-0-113-7.sslip.io"
	good := `{"event":"cert_failed","name":"` + domainName + `","error":"HTTP 429 urn:ietf:params:acme:error:rateLimited - too many"}`

	cases := []struct {
		name   string
		body   string
		auth   string
		status int
		answer string
	}{
		{"no token", good, "", 401, "unauthorized"},
		{"wrong token", good, "Bearer s3cre", 401, "unauthorized"},
		{"longer token", good, "Bearer s3cret2", 401, "unauthorized"},
		{"not a bearer", good, "Basic s3cret", 401, "unauthorized"},
		{"too large", `{"event":"cert_failed","name":"x","error":"` + strings.Repeat("a", 256<<10) + `"}`, "Bearer s3cret", 413, "too large"},
		{"bad json", `{"event":`, "Bearer s3cret", 400, "bad report"},
		{"empty body", ``, "Bearer s3cret", 400, "bad report"},
		{"bad event", `{"event":"cert_renewed","name":"x"}`, "Bearer s3cret", 400, "bad report"},
		{"name not a string", `{"event":"cert_obtained","name":1}`, "Bearer s3cret", 400, "bad report"},
		{"error not a string", `{"event":"cert_failed","name":"x","error":1}`, "Bearer s3cret", 400, "bad report"},
		{"empty name", `{"event":"cert_obtained","name":""}`, "Bearer s3cret", 400, "bad report"},
		{"not an object", `[1,2]`, "Bearer s3cret", 400, "bad report"},
		{"null", `null`, "Bearer s3cret", 400, "bad report"},
		{"unknown name is fine", `{"event":"cert_obtained","name":"nobody.example.com"}`, "Bearer s3cret", 200, "ok"},
		{"padded token", good, "Bearer  s3cret ", 200, "ok"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			h.auth = c.auth
			if status, body := h.do("POST", "/proxy/events", c.body); status != c.status || body != c.answer {
				t.Fatalf("got %d %q, want %d %q", status, body, c.status, c.answer)
			}
		})
	}
	var n domain.Node
	if err := h.store.Read(context.Background(), func(tx app.Tx) (err error) {
		n, err = tx.Node("j57a8x2kq3n4m5p6r7s8t9v0w1x2y3z4")
		return
	}); err != nil {
		t.Fatal(err)
	}
	if s := n.Endpoints[0].Status; s.State != domain.EndpointFailed || s.Error != "Could not get a certificate: too many" {
		t.Fatalf("status %+v", s)
	}

	h.app.Config.WorkerToken = ""
	h.auth = "Bearer "
	if status, body := h.do("POST", "/proxy/events", good); status != 401 || body != "unauthorized" {
		t.Fatalf("unset token: %d %q", status, body)
	}
}
