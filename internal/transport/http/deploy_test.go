package http

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/danielgtaylor/huma/v2/adapters/humago"

	"github.com/ThallesP/keel/internal/adapters/sqlite"
	"github.com/ThallesP/keel/internal/app"
	"github.com/ThallesP/keel/internal/domain"
)

type recordedJobs struct{ keys []string }

func (j *recordedJobs) After(key string, _ time.Duration, _ func(context.Context)) {
	j.keys = append(j.keys, key)
}
func (j *recordedJobs) Every(string, time.Duration, func(context.Context)) {}

func (j *recordedJobs) has(prefix string) bool {
	return slices.ContainsFunc(j.keys, func(k string) bool { return strings.HasPrefix(k, prefix) })
}

func deployTestApp(t *testing.T, token string) (*app.App, *recordedJobs, domain.Node) {
	t.Helper()
	store, err := sqlite.Open(context.Background(), filepath.Join(t.TempDir(), "keel.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	for _, q := range []string{
		`INSERT INTO organizations (id, name, slug, created_at) VALUES ('org', 'Acme', 'acme', 1), ('org2', 'Other', 'other', 2)`,
		`INSERT INTO projects (id, organization_id, name, slug, created_at) VALUES ('p', 'org', 'Acme', 'acme', 1)`,
		`INSERT INTO environments (id, project_id, name, is_production, created_at) VALUES ('env', 'p', 'production', 1, 1)`,
	} {
		if _, err := store.DB().Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	n := domain.Node{ID: "node1", EnvironmentID: "env", Type: domain.NodeService, Name: "api",
		Desired: &domain.Desired{Image: "nginx:alpine", Replicas: 1}, Dirty: true, CreatedAt: 1}
	if err := store.Write(context.Background(), func(tx app.Tx) error { return tx.InsertNode(n) }); err != nil {
		t.Fatal(err)
	}
	jobs := &recordedJobs{}
	a := app.New(app.App{Store: store, Jobs: jobs, Config: app.Config{WorkerToken: token},
		Log: slog.New(slog.NewTextHandler(io.Discard, nil))})
	return a, jobs, n
}

func TestParseWorkerEvents(t *testing.T) {
	cases := []struct {
		name, body string
		want       []app.DockerEvent
	}{
		{"empty", "", nil},
		{"blank lines", "\n  \n", nil},
		{"empty array", " [] ", nil},
		{"array", `[{"Type":"container","Action":"start","Actor":{"ID":"c1","Attributes":{"name":"svc-n.1.x","com.docker.swarm.service.name":"svc-n"}},"time":1700000000},{"Type":"node","Action":"update"}]`,
			[]app.DockerEvent{{Type: "container", Action: "start", Name: "svc-n.1.x", ServiceName: "svc-n"}, {Type: "node", Action: "update"}}},
		{"ndjson with blank lines and CRLF", "{\"Type\":\"service\",\"Action\":\"update\",\"Actor\":{\"Attributes\":{\"name\":\"svc-a\"}}}\r\n\r\n{\"Type\":\"node\",\"Action\":\"create\"}\n",
			[]app.DockerEvent{{Type: "service", Action: "update", Name: "svc-a"}, {Type: "node", Action: "create"}}},
		{"one object", `{"Type":"node","Action":"update"}`, []app.DockerEvent{{Type: "node", Action: "update"}}},
	}
	for _, c := range cases {
		got, err := parseWorkerEvents([]byte(c.body))
		if err != nil || !slices.Equal(got, c.want) {
			t.Errorf("%s: %v\n got %+v\nwant %+v", c.name, err, got, c.want)
		}
	}
	for name, body := range map[string]string{
		"array in a document": `[[1]]`,
		"missing Action":      `{"Type":"node"}`,
		"not an object":       "null",
		"not a string":        `{"Type":5,"Action":"update"}`,
		"broken line":         "{\"Type\":\"node\",\"Action\":\"x\"}\n{nope",
		"broken array":        "[{",
	} {
		if got, err := parseWorkerEvents([]byte(body)); err == nil {
			t.Errorf("%s: accepted %+v", name, got)
		}
	}
}

func postEvents(h http.Handler, auth, body, resync string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(http.MethodPost, "/worker/events", strings.NewReader(body))
	r.Header.Set("Authorization", auth)
	r.Header.Set("X-Keel-Resync", resync)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

func TestWorkerEventsRoute(t *testing.T) {
	a, jobs, n := deployTestApp(t, "s3cret")
	h := New(a, Options{})
	cases := []struct {
		name, auth, body string
		status           int
		text             string
	}{
		{"no header", "", "[]", 401, "unauthorized"},
		{"wrong token", "Bearer nope", "[]", 401, "unauthorized"},
		{"scheme is case-sensitive", "bearer s3cret", "[]", 401, "unauthorized"},
		{"token is trimmed", "Bearer  s3cret ", "[]", 200, "ok"},
		{"bad json", "Bearer s3cret", "{", 400, "bad json"},
		{"not events", "Bearer s3cret", `[{"Type":"x"}]`, 400, "bad json"},
		{"limit is inclusive", "Bearer s3cret", "[" + strings.Repeat(" ", workerEventsMaxBody-2) + "]", 200, "ok"},
		{"too large", "Bearer s3cret", "[" + strings.Repeat(" ", workerEventsMaxBody-1) + "]", 413, "too large"},
		{"counted in bytes", "Bearer s3cret", `[{"Type":"x","Action":"` + strings.Repeat("é", workerEventsMaxBody/2) + `"}]`, 413, "too large"},
	}
	for _, c := range cases {
		w := postEvents(h, c.auth, c.body, "")
		if w.Code != c.status || w.Body.String() != c.text || !strings.HasPrefix(w.Header().Get("Content-Type"), "text/plain") {
			t.Errorf("%s: %d %q", c.name, w.Code, w.Body.String())
		}
	}

	jobs.keys = nil
	w := postEvents(h, "Bearer s3cret", `{"Type":"container","Action":"die","Actor":{"Attributes":{"com.docker.swarm.service.name":"svc-`+n.ID+`"}}}`, "1")
	if w.Code != 200 || !jobs.has("observe:"+n.ID) || !jobs.has("observe:all") {
		t.Fatalf("%d jobs %v", w.Code, jobs.keys)
	}

	a.Config.WorkerToken = ""
	if w := postEvents(h, "Bearer ", "[]", ""); w.Code != 401 {
		t.Fatalf("unset token: %d", w.Code)
	}
}

func deployAPI(a *app.App, actor domain.Actor) http.Handler {
	mux := http.NewServeMux()
	s := &Server{app: a}
	s.registerDeploy(humago.New(mux, Config("test")))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mux.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), actorKey{}, actor)))
	})
}

type deployResponse struct {
	Code, Detail, ID string
	Deployment       *struct{ ID, Message, Status string }
}

func call(h http.Handler, method, path, body string) (int, deployResponse, string) {
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	if body != "" {
		r.Header.Set("Content-Type", "application/json")
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	var out deployResponse
	_ = json.Unmarshal(w.Body.Bytes(), &out)
	return w.Code, out, w.Body.String()
}

func TestDeploymentRoutes(t *testing.T) {
	a, jobs, n := deployTestApp(t, "")
	member := deployAPI(a, domain.Actor{UserID: "u1", OrganizationID: "org"})
	outsider := deployAPI(a, domain.Actor{UserID: "u2", OrganizationID: "org2"})
	signedOut := deployAPI(a, domain.Actor{})

	if code, body, _ := call(member, "POST", "/api/environments/env/deployments", `{"only":[]}`); code != 409 || body.Code != "NOTHING_TO_SHIP" || body.Detail != "Nothing to ship" {
		t.Fatalf("empty only: %d %v", code, body)
	}
	code, body, raw := call(member, "POST", "/api/environments/env/deployments", "")
	id := body.ID
	if code != 200 || id == "" {
		t.Fatalf("ship: %d %s", code, raw)
	}
	if !jobs.has("apply:"+n.ID) || !jobs.has("timeout:"+id) {
		t.Fatalf("jobs: %v", jobs.keys)
	}
	if code, body, _ := call(member, "POST", "/api/environments/env/deployments", `{}`); code != 409 || body.Code != "DEPLOYMENT_RUNNING" || body.Detail != "A deployment is already running" {
		t.Fatalf("second ship: %d %v", code, body)
	}
	if code, body, _ := call(outsider, "POST", "/api/environments/env/deployments", `{"only":["node1"],"refresh":true}`); code != 404 || body.Code != "PROJECT_NOT_FOUND" || body.Detail != "Environment not found" {
		t.Fatalf("outsider ship: %d %v", code, body)
	}
	if code, body, _ := call(signedOut, "POST", "/api/environments/env/deployments", `{}`); code != 401 || body.Code != "NOT_AUTHENTICATED" {
		t.Fatalf("signed-out ship: %d %v", code, body)
	}

	_, body, raw = call(member, "GET", "/api/deployments/"+id, "")
	if d := body.Deployment; d == nil || d.ID != id || d.Message != "ship api" || d.Status != "running" {
		t.Fatalf("get: %s", raw)
	}
	if !strings.Contains(raw, `"log":[]`) || !strings.Contains(raw, `{"label":"health checks","status":"pending"}`) || strings.Contains(raw, "finishedAt") {
		t.Fatalf("shape: %s", raw)
	}
	if _, body, raw := call(member, "GET", "/api/environments/env/deployments/latest", ""); body.Deployment == nil {
		t.Fatalf("latest: %s", raw)
	}
	if code, _, raw := call(member, "GET", "/api/nodes/node1/deployments", ""); code != 200 || !strings.HasPrefix(raw, `[{"id":"`+id) {
		t.Fatalf("node list: %s", raw)
	}
	for name, h := range map[string]http.Handler{"outsider": outsider, "signed out": signedOut} {
		for _, path := range []string{"/api/deployments/" + id, "/api/environments/env/deployments/latest", "/api/deployments/%25%25malformed"} {
			if code, _, raw := call(h, "GET", path, ""); code != 200 || strings.TrimSpace(raw) != `{"deployment":null}` {
				t.Errorf("%s %s: %d %s", name, path, code, raw)
			}
		}
		if code, _, raw := call(h, "GET", "/api/nodes/node1/deployments", ""); code != 200 || strings.TrimSpace(raw) != `[]` {
			t.Errorf("%s node list: %d %s", name, code, raw)
		}
	}
}
