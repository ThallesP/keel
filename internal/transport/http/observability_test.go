package http

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/danielgtaylor/huma/v2/adapters/humago"

	"github.com/ThallesP/keel/internal/adapters/sqlite"
	"github.com/ThallesP/keel/internal/app"
	"github.com/ThallesP/keel/internal/domain"
)

type obsFakeAxiom struct{ forward app.HTTPReply }

func (obsFakeAxiom) Query(context.Context, app.AxiomTarget, app.AxiomQuery) ([]app.AxiomRow, error) {
	return []app.AxiomRow{}, nil
}
func (obsFakeAxiom) CreateDataset(context.Context, app.AxiomTarget, string, string, string) error {
	return nil
}
func (obsFakeAxiom) Datasets(context.Context, app.AxiomTarget, string) ([]app.AxiomDataset, error) {
	return nil, nil
}
func (obsFakeAxiom) MintToken(context.Context, app.AxiomTarget, string, app.AxiomTokenRequest) (string, error) {
	return "", nil
}
func (obsFakeAxiom) Orgs(context.Context, app.AxiomTarget) ([]app.AxiomOrgInfo, error) {
	return nil, nil
}
func (obsFakeAxiom) RegisterClient(context.Context, string, string) (string, error) {
	return "client", nil
}
func (obsFakeAxiom) ExchangeCode(context.Context, string, app.AxiomCodeExchange) (string, error) {
	return "", nil
}
func (f obsFakeAxiom) ForwardTraces(context.Context, app.OTLPForward) (app.HTTPReply, error) {
	return f.forward, nil
}

type obsFakeLogs struct{ tail int }

func (l *obsFakeLogs) ReadServiceLogs(_ context.Context, _ string, tail int) ([]byte, bool, error) {
	l.tail = tail
	return nil, true, nil
}
func (l *obsFakeLogs) ListLogReplicas(context.Context, string) ([]app.LogReplica, error) {
	return nil, nil
}

type obsHarness struct {
	srv   *httptest.Server
	store *sqlite.Store
	app   *app.App
	logs  *obsFakeLogs
}

func newObsHarness(t *testing.T) *obsHarness {
	t.Helper()
	ctx := context.Background()
	store, err := sqlite.Open(ctx, filepath.Join(t.TempDir(), "keel.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	for _, q := range []string{
		`INSERT INTO organizations (id, name, slug, created_at) VALUES ('org', 'Acme', 'acme', 1), ('org2', 'Other', 'other', 1)`,
		`INSERT INTO projects (id, organization_id, name, slug, created_at) VALUES ('p', 'org', 'Shop', 'shop', 1), ('p2', 'org2', 'X', 'x', 2)`,
		`INSERT INTO environments (id, project_id, name, is_production, created_at) VALUES ('env', 'p', 'production', 1, 1), ('env2', 'p2', 'production', 1, 2)`,
		`INSERT INTO nodes (id, environment_id, type, name, desired_image, desired_revision, desired_replicas, created_at) VALUES ('api', 'env', 'service', 'api', 'nginx', 1, 1, 1)`,
	} {
		if _, err := store.DB().Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	logs := &obsFakeLogs{}
	a := app.New(app.App{
		Store: store, Axiom: obsFakeAxiom{forward: app.HTTPReply{Status: 200}}, Logs: logs,
		Log:    slog.New(slog.NewTextHandler(io.Discard, nil)),
		Config: app.Config{Version: "test", SiteURL: "https://keel.example.com", WorkerToken: "worker-secret"},
		Now:    func() int64 { return 1_791_460_812_345 },
	})
	s := &Server{app: a}
	mux := http.NewServeMux()
	s.Register(humago.New(mux, Config("test")))
	s.registerRaw(mux)
	actors := map[string]domain.Actor{
		"member":    {UserID: "u1", OrganizationID: "org", Role: domain.RoleMember},
		"foreigner": {UserID: "u2", OrganizationID: "org2", Role: domain.RoleOwner},
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx := context.WithValue(r.Context(), actorKey{}, actors[r.Header.Get("X-Test-Actor")])
		mux.ServeHTTP(w, r.WithContext(ctx))
	}))
	t.Cleanup(srv.Close)
	return &obsHarness{srv: srv, store: store, app: a, logs: logs}
}

type obsReply struct {
	status int
	ctype  string
	body   string
	header http.Header
}

func (h *obsHarness) do(t *testing.T, actor, method, path, body string, headers ...string) obsReply {
	t.Helper()
	req, _ := http.NewRequest(method, h.srv.URL+path, strings.NewReader(body))
	req.Header.Set("X-Test-Actor", actor)
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	for i := 0; i+1 < len(headers); i += 2 {
		req.Header.Set(headers[i], headers[i+1])
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(res.Body)
	return obsReply{res.StatusCode, res.Header.Get("Content-Type"), strings.TrimSpace(string(b)), res.Header}
}

func (h *obsHarness) setSink(t *testing.T, sink domain.LogSink) {
	t.Helper()
	err := h.store.Write(context.Background(), func(tx app.Tx) error { return tx.ReplaceLogSink("org", sink, 1_791_000_000_000) })
	if err != nil {
		t.Fatal(err)
	}
}

func obsWantProblem(t *testing.T, r obsReply, status int, code, detail string) {
	t.Helper()
	var p struct {
		Status int    `json:"status"`
		Code   string `json:"code"`
		Detail string `json:"detail"`
	}
	_ = json.Unmarshal([]byte(r.body), &p)
	if r.status != status || r.ctype != "application/problem+json" || p.Code != code || (detail != "" && p.Detail != detail) {
		t.Fatalf("got %d %s %s, want %d %s %q", r.status, r.ctype, r.body, status, code, detail)
	}
}

func TestObservabilityRoutes(t *testing.T) {
	h := newObsHarness(t)

	if r := h.do(t, "member", "GET", "/api/organization/log-sink", ""); r.status != 200 || r.body != `{"sink":null}` {
		t.Fatalf("no sink: %d %s", r.status, r.body)
	}
	h.setSink(t, domain.LogSink{Kind: "axiom", Domain: "api.axiom.co", Dataset: "keel-logs", Token: "xaat-0000wxyz"})
	r := h.do(t, "member", "GET", "/api/organization/log-sink", "")
	if r.body != `{"sink":{"kind":"axiom","domain":"api.axiom.co","dataset":"keel-logs","traces":null,"org":null,"tokenHint":"…wxyz"}}` {
		t.Fatalf("sink: %s", r.body)
	}
	if r := h.do(t, "", "GET", "/api/organization/log-sink", ""); r.body != `{"sink":null}` {
		t.Fatalf("signed out: %s", r.body)
	}
	if r := h.do(t, "member", "GET", "/api/organization/axiom/pending-orgs", ""); r.body != `{"orgs":[]}` {
		t.Fatalf("pending: %s", r.body)
	}

	if r := h.do(t, "foreigner", "GET", "/api/nodes/api/tracing", ""); r.status != 200 || r.body != `{"tracing":null}` {
		t.Fatalf("foreign tracing: %d %s", r.status, r.body)
	}
	r = h.do(t, "member", "GET", "/api/nodes/api/tracing", "")
	if !strings.HasPrefix(r.body, `{"tracing":{"enabled":false,"traces":"old","env":[{"key":"OTEL_EXPORTER_OTLP_ENDPOINT","value":"https://keel.example.com/otlp","secret":false,"overridden":false}`) {
		t.Fatalf("tracing view: %s", r.body)
	}
	obsWantProblem(t, h.do(t, "member", "PUT", "/api/nodes/api/tracing", `{"on":true}`), 409, domain.CodeTracesOff, "Sign in with Axiom again to turn on traces")
	obsWantProblem(t, h.do(t, "foreigner", "PUT", "/api/nodes/api/tracing", `{"on":false}`), 404, domain.CodeServiceNotFound, "Node not found")
	obsWantProblem(t, h.do(t, "", "PUT", "/api/nodes/api/tracing", `{"on":false}`), 401, domain.CodeNotAuthenticated, "Not authenticated")
	if r := h.do(t, "member", "POST", "/api/nodes/api/tracing/local-env", ""); r.status != 200 || r.body != `{"env":null,"reason":"Sign in with Axiom again to turn on traces"}` {
		t.Fatalf("local env: %d %s", r.status, r.body)
	}

	h.setSink(t, domain.LogSink{Kind: "axiom", Domain: "api.axiom.co", Dataset: "keel-logs", Traces: "keel-traces", Token: "xaat-0000wxyz", Org: "Acme"})
	if r := h.do(t, "member", "PUT", "/api/nodes/api/tracing", `{"on":true}`); r.status != 204 || r.body != "" {
		t.Fatalf("enable: %d %s", r.status, r.body)
	}
	r = h.do(t, "member", "POST", "/api/nodes/api/tracing/local-env", "")
	var local struct {
		Env    map[string]string `json:"env"`
		Reason *string           `json:"reason"`
	}
	if err := json.Unmarshal([]byte(r.body), &local); err != nil || local.Reason != nil || len(local.Env) != 7 {
		t.Fatalf("local env on: %s", r.body)
	}

	obsWantProblem(t, h.do(t, "member", "GET", "/api/environments/env/traces?range=2d", ""), 422, domain.CodeInvalidInput, "")
	obsWantProblem(t, h.do(t, "member", "GET", "/api/environments/env/traces", ""), 422, domain.CodeInvalidInput, "")
	obsWantProblem(t, h.do(t, "member", "GET", "/api/environments/env/traces/xyz", ""), 422, domain.CodeInvalidInput, "Not a trace id")
	obsWantProblem(t, h.do(t, "foreigner", "GET", "/api/environments/env/traces?range=1h", ""), 404, domain.CodeProjectNotFound, "Environment not found")
	r = h.do(t, "member", "GET", "/api/environments/env/traces?range=15m&search=GET", "")
	if r.status != 200 || !strings.Contains(r.body, `"bucketMs":30000`) || !strings.Contains(r.body, `"stats":{"requests":0,"errors":0,"p50":null,"p95":null,"p99":null}`) || !strings.Contains(r.body, `"traces":[]`) {
		t.Fatalf("overview: %d %s", r.status, r.body)
	}
	if r := h.do(t, "member", "GET", "/api/environments/env/traces/around?at=1791460800000", ""); r.status != 200 || r.body != "[]" {
		t.Fatalf("traces around: %d %s", r.status, r.body)
	}
	if r := h.do(t, "member", "GET", "/api/environments/env/traces/4bf92f3577b34da6a3ce929d0e0e4736", ""); r.status != 200 ||
		r.body != `{"source":"axiom","traceId":"4bf92f3577b34da6a3ce929d0e0e4736","spans":[],"logs":[]}` {
		t.Fatalf("trace: %d %s", r.status, r.body)
	}

	if r := h.do(t, "member", "GET", "/api/environments/env/logs?range=1h", ""); r.status != 200 || r.body != `{"source":"axiom","lines":[]}` {
		t.Fatalf("env logs: %d %s", r.status, r.body)
	}
	if r := h.do(t, "member", "GET", "/api/environments/env/logs/around?at=5", ""); r.status != 200 || r.body != `[]` {
		t.Fatalf("around: %d %s", r.status, r.body)
	}
	obsWantProblem(t, h.do(t, "member", "GET", "/api/environments/env/logs/around", ""), 422, domain.CodeInvalidInput, "")
	obsWantProblem(t, h.do(t, "foreigner", "GET", "/api/nodes/api/logs", ""), 404, domain.CodeServiceNotFound, "Node not found")

	r = h.do(t, "member", "GET", "/api/tracing/prompt?nodeId=api", "")
	var prompt struct{ Prompt string }
	_ = json.Unmarshal([]byte(r.body), &prompt)
	if !strings.Contains(prompt.Prompt, "as the service `api` in the project `shop`.") {
		t.Fatalf("prompt: %s", r.body[:200])
	}

	if r := h.do(t, "member", "DELETE", "/api/organization/log-sink", ""); r.status != 204 {
		t.Fatalf("disconnect: %d %s", r.status, r.body)
	}
	obsWantProblem(t, h.do(t, "", "DELETE", "/api/organization/log-sink", ""), 401, domain.CodeNotAuthenticated, "")
	if r := h.do(t, "member", "DELETE", "/api/organization/axiom/pending", ""); r.status != 204 {
		t.Fatalf("cancel: %d", r.status)
	}
	obsWantProblem(t, h.do(t, "member", "POST", "/api/organization/axiom/sign-in", `{"redirectUri":"https://x/nope"}`), 422, domain.CodeInvalidInput, "Bad redirect URI")
}

func TestNodeLogsDefaultTail(t *testing.T) {
	h := newObsHarness(t)
	if r := h.do(t, "member", "GET", "/api/nodes/api/logs", ""); r.status != 200 || r.body != `{"source":"docker","lines":[],"replicas":[]}` {
		t.Fatalf("logs: %d %s", r.status, r.body)
	}
	if h.logs.tail != 200 {
		t.Fatalf("default tail %d", h.logs.tail)
	}
	h.do(t, "member", "GET", "/api/nodes/api/logs?tail=300", "")
	if h.logs.tail != 300 {
		t.Fatalf("tail %d", h.logs.tail)
	}
}

func TestOTLPRoute(t *testing.T) {
	h := newObsHarness(t)
	r := h.do(t, "", "POST", "/otlp/v1/traces", "{}", "Authorization", "Bearer keel_otlp_nope", "Content-Type", "application/json")
	if r.status != 401 || r.body != "unauthorized" || !strings.HasPrefix(r.ctype, "text/plain") {
		t.Fatalf("unknown key: %d %s %s", r.status, r.ctype, r.body)
	}
	h.setSink(t, domain.LogSink{Kind: "axiom", Domain: "api.axiom.co", Dataset: "keel-logs", Traces: "keel-traces", Token: "t"})
	if r := h.do(t, "member", "PUT", "/api/nodes/api/tracing", `{"on":true}`); r.status != 204 {
		t.Fatalf("enable: %d %s", r.status, r.body)
	}
	var key string
	_ = h.store.DB().QueryRow(`SELECT key FROM otlp_keys`).Scan(&key)
	r = h.do(t, "", "POST", "/otlp/v1/traces", "{}", "Authorization", "Bearer "+key, "Content-Type", "text/plain")
	if r.status != 415 {
		t.Fatalf("type: %d %s", r.status, r.body)
	}
	r = h.do(t, "", "POST", "/otlp/v1/traces", "\x01", "Authorization", "Bearer "+key, "Content-Type", "application/x-protobuf")
	if r.status != 200 || r.ctype != "application/x-protobuf" {
		t.Fatalf("forward: %d %s %q", r.status, r.ctype, r.body)
	}
	if r := h.do(t, "", "GET", "/otlp/v1/traces", ""); r.status != 405 {
		t.Fatalf("GET: %d", r.status)
	}
}

func TestWorkerConfigRoute(t *testing.T) {
	h := newObsHarness(t)
	for _, auth := range []string{"", "Bearer wrong", "bearer worker-secret", "worker-secret"} {
		r := h.do(t, "", "GET", "/worker/config", "", "Authorization", auth)
		if r.status != 401 || r.body != "unauthorized" {
			t.Fatalf("auth %q: %d %s", auth, r.status, r.body)
		}
	}
	r := h.do(t, "", "GET", "/worker/config", "", "Authorization", "Bearer worker-secret ")
	if r.status != 200 || r.ctype != "application/json" || r.header.Get("Cache-Control") != "no-store" || r.body != `{"sinks":[]}` {
		t.Fatalf("empty: %d %s %v %s", r.status, r.ctype, r.header, r.body)
	}
	h.setSink(t, domain.LogSink{Kind: "axiom", Domain: "api.axiom.co", Dataset: "keel-logs", Token: "xaat-1"})
	want := `{"sinks":[{"projectId":"p","serviceIds":["api"],"sink":{"kind":"axiom","domain":"api.axiom.co","dataset":"keel-logs","token":"xaat-1"},"since":1791000000000}]}`
	if r := h.do(t, "", "GET", "/worker/config", "", "Authorization", "Bearer worker-secret"); r.body != want {
		t.Fatalf("configured: %s", r.body)
	}
	h.app.Config.WorkerToken = ""
	if r := h.do(t, "", "GET", "/worker/config", "", "Authorization", "Bearer "); r.status != 401 {
		t.Fatalf("unset token: %d", r.status)
	}
}
