package http

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
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

// canvasHTTP serves the canvas routes over a temp database. The actor is injected directly (the
// auth area resolves real sessions).
type canvasHTTP struct {
	t     *testing.T
	srv   *httptest.Server
	store *sqlite.Store
	actor domain.Actor
}

func canvasServe(t *testing.T) *canvasHTTP {
	t.Helper()
	store, err := sqlite.Open(context.Background(), filepath.Join(t.TempDir(), "keel.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	for _, q := range []string{
		`INSERT INTO organizations (id, name, slug, created_at) VALUES ('org-a', 'A', 'a', 1), ('org-b', 'B', 'b', 1)`,
		`INSERT INTO projects (id, organization_id, name, slug, created_at) VALUES ('p', 'org-a', 'Acme', 'acme', 1)`,
		`INSERT INTO environments (id, project_id, name, is_production, created_at) VALUES ('env', 'p', 'production', 1, 1)`,
	} {
		if _, err := store.DB().Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	a := app.New(app.App{Store: store, Config: app.Config{PublicIP: "203.0.113.7"}})
	c := &canvasHTTP{t: t, store: store, actor: domain.Actor{UserID: "u", OrganizationID: "org-a", Role: "member"}}
	s := &Server{app: a, log: a.Log}
	mux := http.NewServeMux()
	s.registerCanvas(humago.New(mux, Config("test")))
	c.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mux.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), actorKey{}, c.actor)))
	}))
	t.Cleanup(c.srv.Close)
	return c
}

// do sends body (JSON-encoded unless it is a string) and returns status, content type, raw body.
func (c *canvasHTTP) do(method, path string, body any) (int, string, string) {
	c.t.Helper()
	var r io.Reader
	switch b := body.(type) {
	case nil:
	case string:
		r = strings.NewReader(b)
	default:
		raw, _ := json.Marshal(b)
		r = bytes.NewReader(raw)
	}
	req, _ := http.NewRequest(method, c.srv.URL+path, r)
	if r != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		c.t.Fatal(err)
	}
	defer res.Body.Close()
	raw, _ := io.ReadAll(res.Body)
	return res.StatusCode, res.Header.Get("Content-Type"), strings.TrimSpace(string(raw))
}

func (c *canvasHTTP) json(method, path string, body any, wantStatus int, out any) {
	c.t.Helper()
	status, _, raw := c.do(method, path, body)
	if status != wantStatus {
		c.t.Fatalf("%s %s: %d %s, want %d", method, path, status, raw, wantStatus)
	}
	if out != nil {
		if err := json.Unmarshal([]byte(raw), out); err != nil {
			c.t.Fatalf("%s %s: %v in %s", method, path, err, raw)
		}
	}
}

// problem checks an error response: status, problem+json, code and the sentence.
func (c *canvasHTTP) problem(method, path string, body any, status int, code, detail string) {
	c.t.Helper()
	got, ct, raw := c.do(method, path, body)
	var p struct{ Code, Detail string }
	_ = json.Unmarshal([]byte(raw), &p)
	if got != status || ct != "application/problem+json" || p.Code != code || p.Detail != detail {
		c.t.Errorf("%s %s: %d %s %s, want %d %s %q", method, path, got, ct, raw, status, code, detail)
	}
}

func TestCanvasHTTPNodes(t *testing.T) {
	c := canvasServe(t)
	c.json("GET", "/api/environments/env/nodes", nil, 200, nil)
	if _, _, raw := c.do("GET", "/api/environments/env/nodes", nil); raw != `{"nodes":[]}` {
		t.Fatalf("empty canvas %s", raw)
	}

	var created struct {
		ID           string
		DeploymentID *string `json:"deploymentId"`
	}
	c.json("POST", "/api/environments/env/nodes", map[string]any{"type": "service", "name": "api", "port": 8080, "position": map[string]any{"x": 10, "y": 20}}, 201, &created)
	if created.ID == "" || created.DeploymentID != nil {
		t.Fatalf("created %+v", created)
	}
	var list struct{ Nodes []map[string]any }
	c.json("GET", "/api/environments/env/nodes", nil, 200, &list)
	want := map[string]any{
		"id": created.ID, "type": "service", "name": "api", "position": map[string]any{"x": 10.0, "y": 20.0}, "config": map[string]any{},
		"dirty": true, "status": "pending", "image": "nginx:alpine", "port": 8080.0, "replicas": 1.0, "running": 0.0, "revision": 0.0,
		"public": false, "endpoints": []any{},
	}
	if len(list.Nodes) != 1 || !canvasJSONEqual(list.Nodes[0], want) {
		t.Fatalf("view %v\nwant %v", list.Nodes, want)
	}

	c.problem("POST", "/api/environments/env/nodes", map[string]any{"type": "service", "name": "Bad"}, 422, "INVALID_INPUT", "Name: 1–40 chars, a-z 0-9 and - only")
	c.problem("POST", "/api/environments/env/nodes", map[string]any{"type": "service", "port": 80.5}, 422, "INVALID_INPUT", "Port must be 1–65535")
	c.problem("POST", "/api/environments/env/nodes", map[string]any{"type": "service", "name": "api"}, 409, "NAME_TAKEN", `"api" is already taken`)
	c.problem("POST", "/api/environments/nope/nodes", map[string]any{"type": "service"}, 404, "PROJECT_NOT_FOUND", "Environment not found")
	if status, _, raw := c.do("POST", "/api/environments/env/nodes", map[string]any{"type": "bogus"}); status != 422 || !strings.Contains(raw, `"code":"INVALID_INPUT"`) {
		t.Errorf("bad type: %d %s", status, raw)
	}

	if status, _, raw := c.do("PATCH", "/api/nodes/"+created.ID, map[string]any{"name": "web", "replicas": 2}); status != 204 {
		t.Fatalf("patch: %d %s", status, raw)
	}
	if status, _, _ := c.do("PUT", "/api/nodes/"+created.ID+"/position", map[string]any{"x": 1.25, "y": -2}); status != 204 {
		t.Fatalf("move: %d", status)
	}
	c.json("GET", "/api/environments/env/nodes", nil, 200, &list)
	if n := list.Nodes[0]; n["name"] != "web" || n["replicas"] != 2.0 || !canvasJSONEqual(n["position"], map[string]any{"x": 1.25, "y": -2.0}) {
		t.Errorf("after patch/move %v", n)
	}
	c.problem("PATCH", "/api/nodes/nope", map[string]any{"name": "x"}, 404, "SERVICE_NOT_FOUND", "Node not found")

	var dup struct{ ID string }
	c.json("POST", "/api/nodes/"+created.ID+"/duplicate", nil, 201, &dup)
	if dup.ID == "" || dup.ID == created.ID {
		t.Fatalf("duplicate %+v", dup)
	}

	// Summary: two dirty services.
	if _, _, raw := c.do("GET", "/api/environments/env/summary", nil); raw != `{"summary":{"pendingChanges":2,"counts":{"pending":2},"servers":0}}` {
		t.Errorf("summary %s", raw)
	}

	// Volumes have no runtime: delete needs no Swarm work. Missing ids are a 204 too.
	var vol struct{ ID string }
	c.json("POST", "/api/environments/env/nodes", map[string]any{"type": "volume"}, 201, &vol)
	c.problem("POST", "/api/nodes/"+vol.ID+"/stop", nil, 422, "INVALID_INPUT", "This node type cannot be stopped")
	// Already at 0 replicas: nothing ships, and the field is null rather than absent (web-data M9).
	var idle struct{ ID string }
	c.json("POST", "/api/environments/env/nodes", map[string]any{"type": "service", "name": "idle", "replicas": 0}, 201, &idle)
	if status, _, raw := c.do("POST", "/api/nodes/"+idle.ID+"/stop", nil); status != 200 || raw != `{"deploymentId":null}` {
		t.Errorf("stop at 0 replicas: %d %s", status, raw)
	}
	c.problem("POST", "/api/nodes/nope/stop", nil, 404, "SERVICE_NOT_FOUND", "Node not found")
	for _, id := range []string{vol.ID, vol.ID, "nope"} {
		if status, _, raw := c.do("DELETE", "/api/nodes/"+id, nil); status != 204 {
			t.Errorf("delete %s: %d %s", id, status, raw)
		}
	}
}

func TestCanvasHTTPVariables(t *testing.T) {
	c := canvasServe(t)
	var pg, api struct{ ID string }
	c.json("POST", "/api/environments/env/nodes", map[string]any{"type": "database"}, 201, &pg)
	c.json("POST", "/api/environments/env/nodes", map[string]any{"type": "service", "name": "api"}, 201, &api)
	base := "/api/nodes/" + api.ID + "/variables"

	if status, _, raw := c.do("POST", base, map[string]any{"key": "DB", "value": "x ${{ postgres.HOST }}", "secret": false}); status != 204 {
		t.Fatalf("set: %d %s", status, raw)
	}
	c.problem("POST", base, map[string]any{"key": "lower/case", "value": "x", "secret": false}, 422, "INVALID_INPUT", "Key: UPPER_SNAKE_CASE only")
	c.problem("POST", base, map[string]any{"key": "DB", "value": strings.Repeat("a", 4097), "secret": false}, 422, "INVALID_INPUT", "Value too long")
	c.problem("POST", "/api/nodes/nope/variables", map[string]any{"key": "A", "value": "", "secret": false}, 404, "SERVICE_NOT_FOUND", "Node not found")

	_, _, raw := c.do("GET", base, nil)
	want := `{"variables":[{"key":"DB","value":"x ${{ postgres.HOST }}","resolved":"x svc-` + pg.ID + `","secret":false,"resolvedSecret":false,` +
		`"parts":[{"text":"x "},{"ref":{"node":"postgres","nodeId":"` + pg.ID + `","key":"HOST","missing":false}}]}]}`
	if raw != want {
		t.Errorf("variables\n got %s\nwant %s", raw, want)
	}
	var sources struct {
		Sources []struct {
			NodeID string
			Keys   []struct {
				Key      string
				Provided bool
			}
		}
	}
	c.json("GET", base+"/referenceable", nil, 200, &sources)
	if len(sources.Sources) != 1 || sources.Sources[0].NodeID != pg.ID || sources.Sources[0].Keys[0].Key != "DATABASE_URL" || !sources.Sources[0].Keys[0].Provided {
		t.Errorf("sources %+v", sources)
	}

	for _, key := range []string{"DB", "DB", "NEVER"} { // no-op when missing
		if status, _, raw := c.do("POST", base+"/delete", map[string]any{"key": key}); status != 204 {
			t.Errorf("delete %s: %d %s", key, status, raw)
		}
	}
	if _, _, raw := c.do("GET", base, nil); raw != `{"variables":[]}` {
		t.Errorf("after delete %s", raw)
	}
}

func TestCanvasHTTPProjectsAndAccess(t *testing.T) {
	c := canvasServe(t)
	if _, _, raw := c.do("GET", "/api/projects", nil); raw != `{"projects":[{"id":"p","name":"Acme","slug":"acme","environments":[{"id":"env","name":"production","isProduction":true}]}]}` {
		t.Errorf("projects %s", raw)
	}
	if _, _, raw := c.do("GET", "/api/projects/by-slug/acme", nil); raw != `{"project":{"id":"p","name":"Acme","slug":"acme","environment":{"id":"env","name":"production"}}}` {
		t.Errorf("by slug %s", raw)
	}
	if _, _, raw := c.do("GET", "/api/projects/by-slug/nope", nil); raw != `{"project":null}` {
		t.Errorf("unknown slug %s", raw)
	}
	var node struct{ ID string }
	c.json("POST", "/api/environments/env/nodes", map[string]any{"type": "service"}, 201, &node)

	// Another organization's member: null / empty reads, not-found writes.
	c.actor = domain.Actor{UserID: "v", OrganizationID: "org-b", Role: "member"}
	for path, want := range map[string]string{
		"/api/environments/env/summary":                      `{"summary":null}`,
		"/api/environments/env/nodes":                        `{"nodes":[]}`,
		"/api/projects/by-slug/acme":                         `{"project":null}`,
		"/api/projects":                                      `{"projects":[]}`,
		"/api/nodes/" + node.ID + "/variables":               `{"variables":[]}`,
		"/api/nodes/" + node.ID + "/variables/referenceable": `{"sources":[]}`,
	} {
		if status, _, raw := c.do("GET", path, nil); status != 200 || raw != want {
			t.Errorf("foreign GET %s: %d %s, want %s", path, status, raw, want)
		}
	}
	c.problem("PATCH", "/api/nodes/"+node.ID, map[string]any{"name": "mine"}, 404, "SERVICE_NOT_FOUND", "Node not found")
	c.problem("POST", "/api/nodes/"+node.ID+"/start", nil, 404, "SERVICE_NOT_FOUND", "Node not found")
	c.problem("POST", "/api/environments/env/nodes", map[string]any{"type": "service"}, 404, "PROJECT_NOT_FOUND", "Environment not found")
	if status, _, _ := c.do("DELETE", "/api/nodes/"+node.ID, nil); status != 204 {
		t.Errorf("foreign delete status %d", status)
	}
	c.actor = domain.Actor{UserID: "u", OrganizationID: "org-a"}
	if _, _, raw := c.do("GET", "/api/environments/env/nodes", nil); !strings.Contains(raw, node.ID) {
		t.Errorf("foreign delete removed the node: %s", raw)
	}

	// Signed out.
	c.actor = domain.Actor{}
	c.problem("GET", "/api/projects", nil, 401, "NOT_AUTHENTICATED", "Not authenticated")
	c.problem("DELETE", "/api/nodes/"+node.ID, nil, 401, "NOT_AUTHENTICATED", "Not authenticated")
	if _, _, raw := c.do("GET", "/api/environments/env/summary", nil); raw != `{"summary":null}` {
		t.Errorf("signed-out summary %s", raw)
	}
}

func canvasJSONEqual(a, b any) bool {
	x, _ := json.Marshal(a)
	y, _ := json.Marshal(b)
	return bytes.Equal(x, y)
}
