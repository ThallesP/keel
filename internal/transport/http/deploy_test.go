package http

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"reflect"
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
	for _, k := range j.keys {
		if strings.HasPrefix(k, prefix) {
			return true
		}
	}
	return false
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
	five := 1.7e9
	cases := []struct {
		name string
		body string
		want []app.DockerEvent
		bad  bool
	}{
		{"empty", "", []app.DockerEvent{}, false},
		{"blank lines", "\n  \n", []app.DockerEvent{}, false},
		{"empty array", " [] ", []app.DockerEvent{}, false},
		{"array", `[{"Type":"container","Action":"start","Actor":{"ID":"c1","Attributes":{"name":"svc-n.1.x","com.docker.swarm.service.name":"svc-n"}},"time":1700000000},{"Type":"node","Action":"update"}]`,
			[]app.DockerEvent{{Type: "container", Action: "start", Name: "svc-n.1.x", ServiceName: "svc-n", Time: &five}, {Type: "node", Action: "update"}}, false},
		{"ndjson with blank lines and CRLF", "{\"Type\":\"service\",\"Action\":\"update\",\"Actor\":{\"Attributes\":{\"name\":\"svc-a\"}}}\r\n\r\n{\"Type\":\"node\",\"Action\":\"create\"}\n",
			[]app.DockerEvent{{Type: "service", Action: "update", Name: "svc-a"}, {Type: "node", Action: "create"}}, false},
		{"one object", `{"Type":"node","Action":"update","time":"soon"}`, []app.DockerEvent{{Type: "node", Action: "update"}}, false},
		{"String() of odd values", `{"Type":5,"Action":null,"Actor":{"Attributes":{"name":7}}}`, []app.DockerEvent{{Type: "5", Action: "null"}}, false},
		{"array in a document", `[[1]]`, nil, true},
		{"missing Action", `{"Type":"node"}`, nil, true},
		{"not an object", "null", nil, true},
		{"broken line", "{\"Type\":\"node\",\"Action\":\"x\"}\n{nope", nil, true},
		{"broken array", "[{", nil, true},
	}
	for _, c := range cases {
		got, err := parseWorkerEvents(c.body)
		if c.bad {
			if err == nil {
				t.Errorf("%s: accepted %+v", c.name, got)
			}
			continue
		}
		if err != nil {
			t.Errorf("%s: %v", c.name, err)
			continue
		}
		if c.name == "array" {
			if got[0].Time == nil || *got[0].Time != 1700000000 {
				t.Errorf("%s: time %v", c.name, got[0].Time)
			}
			got[0].Time = &five
		}
		if !reflect.DeepEqual(got, c.want) {
			t.Errorf("%s:\n got %+v\nwant %+v", c.name, got, c.want)
		}
	}
}

func post(h http.Handler, path, auth, body string, header ...string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	if auth != "" {
		r.Header.Set("Authorization", auth)
	}
	for i := 0; i+1 < len(header); i += 2 {
		r.Header.Set(header[i], header[i+1])
	}
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
		{"counted in UTF-16 units", "Bearer s3cret", strings.Repeat("😀", workerEventsMaxBody/2+1), 413, "too large"},
		{"multi-byte within the limit", "Bearer s3cret", `[{"Type":"x","Action":"` + strings.Repeat("é", 200_000) + `"}]`, 200, "ok"},
	}
	for _, c := range cases {
		w := post(h, "/worker/events", c.auth, c.body)
		if w.Code != c.status || w.Body.String() != c.text || !strings.HasPrefix(w.Header().Get("Content-Type"), "text/plain") {
			t.Errorf("%s: %d %q", c.name, w.Code, w.Body.String())
		}
	}

	jobs.keys = nil
	w := post(h, "/worker/events", "Bearer s3cret", `{"Type":"container","Action":"die","Actor":{"Attributes":{"com.docker.swarm.service.name":"svc-`+n.ID+`"}}}`, "X-Keel-Resync", "1")
	if w.Code != 200 || !jobs.has("observe:"+n.ID) || !jobs.has("observe:all") {
		t.Fatalf("%d jobs %v", w.Code, jobs.keys)
	}

	a2, _, _ := deployTestApp(t, "")
	if w := post(New(a2, Options{}), "/worker/events", "Bearer ", "[]"); w.Code != 401 {
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

func call(h http.Handler, method, path, body string) (int, map[string]any, string) {
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	if body != "" {
		r.Header.Set("Content-Type", "application/json")
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	var out map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &out)
	return w.Code, out, w.Body.String()
}

func TestDeploymentRoutes(t *testing.T) {
	a, jobs, n := deployTestApp(t, "")
	member := deployAPI(a, domain.Actor{UserID: "u1", OrganizationID: "org"})
	outsider := deployAPI(a, domain.Actor{UserID: "u2", OrganizationID: "org2"})
	signedOut := deployAPI(a, domain.Actor{})

	if code, body, _ := call(member, "POST", "/api/environments/env/deployments", `{"only":[]}`); code != 409 || body["code"] != "NOTHING_TO_SHIP" || body["detail"] != "Nothing to ship" {
		t.Fatalf("empty only: %d %v", code, body)
	}
	code, body, raw := call(member, "POST", "/api/environments/env/deployments", "")
	if code != 200 || body["id"] == nil {
		t.Fatalf("ship: %d %s", code, raw)
	}
	id := body["id"].(string)
	if !jobs.has("apply:"+n.ID) || !jobs.has("timeout:"+id) {
		t.Fatalf("jobs: %v", jobs.keys)
	}
	if code, body, _ := call(member, "POST", "/api/environments/env/deployments", `{}`); code != 409 || body["code"] != "DEPLOYMENT_RUNNING" || body["detail"] != "A deployment is already running" {
		t.Fatalf("second ship: %d %v", code, body)
	}
	if code, body, _ := call(outsider, "POST", "/api/environments/env/deployments", `{"only":["node1"],"refresh":true}`); code != 404 || body["code"] != "PROJECT_NOT_FOUND" || body["detail"] != "Environment not found" {
		t.Fatalf("outsider ship: %d %v", code, body)
	}
	if code, body, _ := call(signedOut, "POST", "/api/environments/env/deployments", `{}`); code != 401 || body["code"] != "NOT_AUTHENTICATED" {
		t.Fatalf("signed-out ship: %d %v", code, body)
	}

	_, body, raw = call(member, "GET", "/api/deployments/"+id, "")
	d, _ := body["deployment"].(map[string]any)
	if d == nil || d["id"] != id || d["message"] != "ship api" || d["status"] != "running" || d["_id"] != nil {
		t.Fatalf("get: %s", raw)
	}
	if !strings.Contains(raw, `"log":[]`) || !strings.Contains(raw, `{"label":"health checks","status":"pending"}`) || strings.Contains(raw, "finishedAt") {
		t.Fatalf("shape: %s", raw)
	}
	if _, body, raw := call(member, "GET", "/api/environments/env/deployments/latest", ""); body["deployment"] == nil {
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
