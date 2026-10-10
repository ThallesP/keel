package client

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ThallesP/keel/internal/cli/output"
)

type fakeAPI struct {
	t      *testing.T
	routes map[string]route
	sent   map[string]string
}

type route struct {
	status int
	body   string
}

func newFakeAPI(t *testing.T, routes map[string]route) (*Client, *fakeAPI) {
	t.Helper()
	f := &fakeAPI{t: t, routes: routes, sent: map[string]string{}}
	srv := httptest.NewServer(f)
	t.Cleanup(srv.Close)
	return New(srv.URL, "tok"), f
}

func (f *fakeAPI) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if got := r.Header.Get("Authorization"); got != "Bearer tok" {
		f.t.Errorf("%s %s: Authorization %q", r.Method, r.URL, got)
	}
	if ua := r.Header.Get("User-Agent"); !strings.HasPrefix(ua, "keel-cli") {
		f.t.Errorf("%s %s: User-Agent %q", r.Method, r.URL, ua)
	}
	key := r.Method + " " + r.URL.EscapedPath()
	if r.URL.RawQuery != "" {
		key += "?" + r.URL.RawQuery
	}
	body, _ := io.ReadAll(r.Body)
	if len(body) > 0 && r.Header.Get("Content-Type") != "application/json" {
		f.t.Errorf("%s: Content-Type %q", key, r.Header.Get("Content-Type"))
	}
	f.sent[key] = string(body)
	rt, ok := f.routes[key]
	if !ok {
		f.t.Errorf("unexpected request %s", key)
		w.WriteHeader(404)
		w.Write([]byte(`{"status":404,"detail":"No such API route","code":"NOT_FOUND"}`))
		return
	}
	if rt.status >= 400 {
		w.Header().Set("Content-Type", "application/problem+json")
	}
	w.WriteHeader(rt.status)
	w.Write([]byte(rt.body))
}

func jsonOf(t *testing.T, v any) string {
	t.Helper()
	var b strings.Builder
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		t.Fatal(err)
	}
	return strings.TrimSuffix(b.String(), "\n")
}

const nodes = `{"nodes":[
	{"id":"aaaaaaaaaaaaaaaaaaaa","type":"service","name":"api","position":{"x":0,"y":0},"config":{},"dirty":true,
	 "status":"pending","image":"nginx:alpine","port":8080,"replicas":2,"running":0,"revision":0,"public":false,"endpoints":[]},
	{"id":"gggggggggggggggggggg","type":"group","name":"backend","position":{"x":0,"y":0},"config":{},"dirty":false,
	 "status":"pending","replicas":0,"running":0,"revision":0,"public":false,"endpoints":[]},
	{"id":"vvvvvvvvvvvvvvvvvvvv","type":"volume","name":"data","position":{"x":0,"y":0},"config":{"sizeGb":5},"dirty":false,
	 "status":"healthy","replicas":0,"running":0,"revision":1,"public":true,"publicUrl":"https://x.sslip.io","endpoints":[],"error":"boom"}
]}`

func TestServices(t *testing.T) {
	c, _ := newFakeAPI(t, map[string]route{"GET /api/environments/env1/nodes": {200, nodes}})
	services, err := c.Services(context.Background(), "env1")
	if err != nil {
		t.Fatal(err)
	}
	got := jsonOf(t, services)
	want := `[{"id":"aaaaaaaaaaaaaaaaaaaa","name":"api","type":"service","status":"pending","image":"nginx:alpine","port":8080,"replicas":2,"running":0,"staged":true},` +
		`{"id":"vvvvvvvvvvvvvvvvvvvv","name":"data","type":"volume","status":"healthy","replicas":0,"running":0,"staged":false,"publicUrl":"https://x.sslip.io","error":"boom"}]`
	if got != want {
		t.Errorf("services:\n got %s\nwant %s", got, want)
	}
}

func TestCreateService(t *testing.T) {
	c, f := newFakeAPI(t, map[string]route{
		"POST /api/environments/env1/nodes": {201, `{"id":"aaaaaaaaaaaaaaaaaaaa"}`},
		"GET /api/environments/env1/nodes":  {200, nodes},
	})
	port := 8080
	svc, err := c.CreateService(context.Background(), "env1", "api", "nginx:alpine", &port, nil)
	if err != nil || svc.Name != "api" || !svc.Staged || svc.Port != 8080 {
		t.Fatalf("created %+v, %v", svc, err)
	}
	if body := f.sent["POST /api/environments/env1/nodes"]; body != `{"type":"service","name":"api","image":"nginx:alpine","port":8080}` {
		t.Errorf("sent %s", body)
	}

	c, _ = newFakeAPI(t, map[string]route{
		"POST /api/environments/env1/nodes": {201, `{"id":"gone"}`},
		"GET /api/environments/env1/nodes":  {200, nodes},
	})
	if _, err := c.CreateService(context.Background(), "env1", "api", "nginx:alpine", nil, nil); output.CodeOf(err) != output.CodeServiceNotFound {
		t.Errorf("vanished: %v", err)
	}
}

const deployment = `{"id":"dddddddddddddddddddd","environmentId":"env1","message":"ship api","status":"failed",
	"startedAt":1791460800123,"finishedAt":1791460812000,
	"steps":[{"nodeId":"aaaaaaaaaaaaaaaaaaaa","label":"api","status":"failed","startedAt":1791460800200},{"label":"health checks","status":"pending"}],
	"log":[{"at":1791460800300,"nodeId":"aaaaaaaaaaaaaaaaaaaa","text":"pulling nginx:alpine"},{"at":1791460812000,"text":"failed"}]}`

func TestDeployments(t *testing.T) {
	c, f := newFakeAPI(t, map[string]route{
		"POST /api/environments/env1/deployments":         {200, `{"id":"dddddddddddddddddddd"}`},
		"GET /api/deployments/dddddddddddddddddddd":       {200, `{"deployment":` + deployment + `}`},
		"GET /api/deployments/nope%2F..":                  {200, `{"deployment":null}`},
		"GET /api/environments/env1/deployments/latest":   {200, `{"deployment":null}`},
		"GET /api/nodes/aaaaaaaaaaaaaaaaaaaa/deployments": {200, `[` + deployment + `]`},
	})
	ctx := context.Background()
	id, err := c.StartDeployment(ctx, "env1", []string{"aaaaaaaaaaaaaaaaaaaa"}, true)
	if err != nil || id != "dddddddddddddddddddd" {
		t.Fatalf("start: %q, %v", id, err)
	}
	if body := f.sent["POST /api/environments/env1/deployments"]; body != `{"only":["aaaaaaaaaaaaaaaaaaaa"],"refresh":true}` {
		t.Errorf("start sent %s", body)
	}
	if _, err := c.StartDeployment(ctx, "env1", nil, false); err != nil {
		t.Fatal(err)
	}
	if body := f.sent["POST /api/environments/env1/deployments"]; body != `{}` {
		t.Errorf("ship sent %s, want every staged change", body)
	}

	d, err := c.Deployment(ctx, "dddddddddddddddddddd")
	if err != nil {
		t.Fatal(err)
	}
	want := `{"id":"dddddddddddddddddddd","status":"failed","message":"ship api","startedAt":"2026-10-08T12:00:00.123Z","finishedAt":"2026-10-08T12:00:12.000Z",` +
		`"steps":[{"serviceId":"aaaaaaaaaaaaaaaaaaaa","label":"api","status":"failed"},{"label":"health checks","status":"pending"}],` +
		`"log":[{"at":"2026-10-08T12:00:00.300Z","serviceId":"aaaaaaaaaaaaaaaaaaaa","text":"pulling nginx:alpine"},{"at":"2026-10-08T12:00:12.000Z","text":"failed"}]}`
	if got := jsonOf(t, d); got != want {
		t.Errorf("deployment:\n got %s\nwant %s", got, want)
	}
	if d, err := c.Deployment(ctx, "nope/.."); d != nil || err != nil {
		t.Errorf("unknown id: %+v, %v", d, err)
	}
	for _, id := range []string{"", ".", ".."} {
		if d, err := c.Deployment(ctx, id); d != nil || err != nil {
			t.Errorf("id %q: %+v, %v", id, d, err)
		}
	}
	if d, err := c.LatestDeployment(ctx, "env1"); d != nil || err != nil {
		t.Errorf("never deployed: %+v, %v", d, err)
	}
	ds, err := c.ServiceDeployments(ctx, "aaaaaaaaaaaaaaaaaaaa")
	if err != nil || len(ds) != 1 || ds[0].Steps[0].ServiceID != "aaaaaaaaaaaaaaaaaaaa" {
		t.Errorf("for node: %+v, %v", ds, err)
	}
}

func TestProjectsAndSummary(t *testing.T) {
	c, f := newFakeAPI(t, map[string]route{
		"GET /api/projects":                  {200, `{"projects":[]}`},
		"POST /api/projects":                 {201, `{"id":"p1","name":"CI App","slug":"ci-app","environments":[{"id":"e1","name":"production","isProduction":true}]}`},
		"GET /api/environments/e1/summary":   {200, `{"summary":{"pendingChanges":1,"counts":{"pending":1},"servers":2}}`},
		"GET /api/environments/gone/summary": {200, `{"summary":null}`},
	})
	ctx := context.Background()
	ps, err := c.Projects(ctx)
	if err != nil || ps == nil || len(ps) != 0 {
		t.Fatalf("projects %#v, %v", ps, err)
	}
	p, err := c.CreateProject(ctx, "CI App")
	if err != nil {
		t.Fatal(err)
	}
	if got := jsonOf(t, p); got != `{"id":"p1","name":"CI App","slug":"ci-app","environments":[{"id":"e1","name":"production","isProduction":true}]}` {
		t.Errorf("project %s", got)
	}
	if f.sent["POST /api/projects"] != `{"name":"CI App"}` {
		t.Errorf("sent %s", f.sent["POST /api/projects"])
	}
	s, err := c.Summary(ctx, "e1")
	if err != nil || s.PendingChanges != 1 || s.Servers != 2 {
		t.Errorf("summary %+v, %v", s, err)
	}
	if _, err := c.Summary(ctx, "gone"); output.CodeOf(err) != output.CodeProjectNotFound {
		t.Errorf("null summary: %v", err)
	}
}

func TestVariables(t *testing.T) {
	c, f := newFakeAPI(t, map[string]route{
		"GET /api/nodes/n1/variables":         {200, `{"variables":[{"key":"A","value":"${{ db.URL }}","resolved":"x","secret":false,"resolvedSecret":true,"parts":[]}]}`},
		"POST /api/nodes/n1/variables":        {204, ``},
		"POST /api/nodes/n1/variables/delete": {204, ``},
		"POST /api/nodes/n2/variables":        {422, `{"status":422,"detail":"Key: UPPER_SNAKE_CASE only","code":"INVALID_INPUT"}`},
	})
	ctx := context.Background()
	vs, err := c.Variables(ctx, "n1")
	if err != nil || len(vs) != 1 || vs[0].Resolved != "x" || !vs[0].ResolvedSecret {
		t.Fatalf("variables %+v, %v", vs, err)
	}
	if err := c.SetVariable(ctx, "n1", "GREETING", "hello", true); err != nil {
		t.Fatal(err)
	}
	if body := f.sent["POST /api/nodes/n1/variables"]; body != `{"key":"GREETING","value":"hello","secret":true}` {
		t.Errorf("set sent %s", body)
	}
	if err := c.RemoveVariable(ctx, "n1", "GREETING"); err != nil {
		t.Fatal(err)
	}
	if body := f.sent["POST /api/nodes/n1/variables/delete"]; body != `{"key":"GREETING"}` {
		t.Errorf("delete sent %s", body)
	}
	err = c.SetVariable(ctx, "n2", "bad", "x", false)
	if oe, ok := err.(*output.Error); !ok || oe.Code != output.CodeInvalidInput || oe.Message != "Key: UPPER_SNAKE_CASE only" {
		t.Errorf("bad key: %#v", err)
	}
}

func TestObservability(t *testing.T) {
	c, f := newFakeAPI(t, map[string]route{
		"GET /api/nodes/n1/logs?tail=100": {200, `{"source":"docker","lines":[{"time":1791460800123.5,"text":"hi <b>","stream":"stdout","task":"t1"},{"time":0,"text":"no stamp","stream":"stderr","task":""}],"replicas":[]}`},
		"GET /api/environments/e1/traces?nodeId=n1&range=1h&search=GET": {200, `{"source":"axiom","from":0,"to":0,"bucketMs":120000,
			"stats":{"requests":2,"errors":1,"p50":1.5,"p95":30,"p99":null},"buckets":[],
			"traces":[{"traceId":"t","name":"GET /","service":"api","kind":"server","start":1791460800123,"duration":12.5,"httpStatus":200,"spans":3,"errors":0,"error":false,"local":true}]}`},
		"GET /api/nodes/n1/tracing":                {200, `{"tracing":{"enabled":true,"traces":"on","env":[{"key":"OTEL_SERVICE_NAME","value":"api","secret":false,"overridden":false}]}}`},
		"GET /api/nodes/db/tracing":                {200, `{"tracing":null}`},
		"PUT /api/nodes/n1/tracing":                {204, ``},
		"POST /api/nodes/n1/tracing/local-env":     {200, `{"env":{"OTEL_SERVICE_NAME":"api"},"reason":null}`},
		"POST /api/nodes/n2/tracing/local-env":     {200, `{"env":null,"reason":"no Axiom sink"}`},
		"GET /api/tracing/prompt?environmentId=e1": {200, `{"prompt":"Add OpenTelemetry"}`},
		"PUT /api/nodes/n3/tracing":                {409, `{"status":409,"detail":"Connect Axiom to see traces","code":"TRACES_OFF"}`},
	})
	ctx := context.Background()
	tail, err := c.Tail(ctx, "n1", 100)
	if err != nil {
		t.Fatal(err)
	}
	if got := jsonOf(t, tail); got != `{"source":"docker","lines":[{"time":"2026-10-08T12:00:00.123Z","stream":"stdout","task":"t1","text":"hi <b>"},{"time":"1970-01-01T00:00:00.000Z","stream":"stderr","text":"no stamp"}]}` {
		t.Errorf("tail %s", got)
	}
	traces, err := c.Traces(ctx, "e1", "n1", "1h", "GET")
	if err != nil {
		t.Fatal(err)
	}
	if got := jsonOf(t, traces); got != `{"stats":{"requests":2,"errors":1,"p50Ms":1.5,"p95Ms":30,"p99Ms":null},"traces":[{"traceId":"t","name":"GET /","service":"api","start":"2026-10-08T12:00:00.123Z","durationMs":12.5,"httpStatus":200,"spans":3,"errors":0,"error":false,"local":true}]}` {
		t.Errorf("traces %s", got)
	}
	tr, err := c.Tracing(ctx, "n1")
	if err != nil || !tr.Enabled || tr.Store != "on" || len(tr.Env) != 1 {
		t.Errorf("tracing %+v, %v", tr, err)
	}
	if tr, err := c.Tracing(ctx, "db"); tr != nil || err != nil {
		t.Errorf("not a service: %+v, %v", tr, err)
	}
	if err := c.SetTracing(ctx, "n1", true); err != nil || f.sent["PUT /api/nodes/n1/tracing"] != `{"on":true}` {
		t.Errorf("set tracing: %v, sent %s", err, f.sent["PUT /api/nodes/n1/tracing"])
	}
	if err := c.SetTracing(ctx, "n3", true); output.CodeOf(err) != output.CodeTracesOff {
		t.Errorf("traces off: %v", err)
	}
	env, reason, err := c.LocalTracingEnv(ctx, "n1")
	if err != nil || env["OTEL_SERVICE_NAME"] != "api" || reason != "" {
		t.Errorf("local env %v %q %v", env, reason, err)
	}
	env, reason, err = c.LocalTracingEnv(ctx, "n2")
	if err != nil || env != nil || reason != "no Axiom sink" {
		t.Errorf("no sink: %v %q %v", env, reason, err)
	}
	prompt, err := c.TracingPrompt(ctx, "", "e1")
	if err != nil || prompt != "Add OpenTelemetry" {
		t.Errorf("prompt %q, %v", prompt, err)
	}
}

func TestDeleteService(t *testing.T) {
	c, _ := newFakeAPI(t, map[string]route{"DELETE /api/nodes/n1": {204, ``}})
	if err := c.DeleteService(context.Background(), "n1"); err != nil {
		t.Fatal(err)
	}
}

func TestTimeJSON(t *testing.T) {
	if b, _ := json.Marshal(Millis(1791460800123)); string(b) != `"2026-10-08T12:00:00.123Z"` {
		t.Errorf("marshal %s", b)
	}
}
