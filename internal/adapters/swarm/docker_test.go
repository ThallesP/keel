package swarm

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"sync"
	"testing"

	"github.com/moby/moby/api/types/swarm"
	"github.com/moby/moby/client"

	"github.com/ThallesP/keel/internal/app"
)

type dockerCall struct {
	method, path, query, body string
}

type fakeDocker struct {
	mu       sync.Mutex
	calls    []dockerCall
	handlers map[string]func(w http.ResponseWriter, r *http.Request)
}

var versionPrefix = regexp.MustCompile(`^/v[0-9.]+`)

func (f *fakeDocker) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	path := versionPrefix.ReplaceAllString(r.URL.Path, "")
	body, _ := io.ReadAll(r.Body)
	r.Body = io.NopCloser(bytes.NewReader(body))
	f.mu.Lock()
	f.calls = append(f.calls, dockerCall{r.Method, path, r.URL.RawQuery, string(body)})
	h := f.handlers[r.Method+" "+path]
	f.mu.Unlock()
	if h == nil {
		w.WriteHeader(http.StatusNotFound)
		_, _ = io.WriteString(w, `{"message":"no such object"}`)
		return
	}
	h(w, r)
}

func jsonReply(v string) func(http.ResponseWriter, *http.Request) {
	return func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, v)
	}
}

func newDockerSwarm(t *testing.T, handlers map[string]func(http.ResponseWriter, *http.Request)) (*Swarm, *fakeDocker) {
	t.Helper()
	f := &fakeDocker{handlers: handlers}
	return fakeEngine(t, f.ServeHTTP), f
}

func fakeEngine(t *testing.T, handler http.HandlerFunc) *Swarm {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	cli, err := client.New(client.WithHost("tcp://"+strings.TrimPrefix(srv.URL, "http://")), client.WithAPIVersion("1.47"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cli.Close() })
	return &Swarm{cli: cli}
}

func TestDockerApplyCalls(t *testing.T) {
	ctx := context.Background()
	s, f := newDockerSwarm(t, map[string]func(http.ResponseWriter, *http.Request){
		"GET /images/nginx:alpine/json": jsonReply(`{"Id":"sha256:1"}`),
		"GET /services/svc-old":         jsonReply(`{"ID":"x","Version":{"Index":42},"Spec":{"Name":"svc-old"}}`),
		"POST /services/create":         jsonReply(`{"ID":"new"}`),
		"POST /services/svc-old/update": jsonReply(`{}`),
		"DELETE /services/svc-old":      func(w http.ResponseWriter, _ *http.Request) {},
	})
	if ok, err := s.ImageCached(ctx, "nginx:alpine"); !ok || err != nil {
		t.Fatalf("cached: %v %v", ok, err)
	}
	if ok, err := s.ImageCached(ctx, "nope:1"); ok || err != nil {
		t.Fatalf("not cached: %v %v", ok, err)
	}
	if _, found, err := s.ServiceVersion(ctx, "new"); found || err != nil {
		t.Fatalf("missing service: %v %v", found, err)
	}
	if v, found, err := s.ServiceVersion(ctx, "old"); !found || v != 42 || err != nil {
		t.Fatalf("existing service: %v %v %v", v, found, err)
	}
	spec := app.ServiceSpec{NodeID: "new", Image: "nginx:alpine", Revision: 1, Replicas: 1, Env: []string{"A=1"}}
	if err := s.CreateService(ctx, spec); err != nil {
		t.Fatal(err)
	}
	spec.NodeID = "old"
	if err := s.UpdateService(ctx, 42, spec); err != nil {
		t.Fatal(err)
	}
	if err := s.RemoveService(ctx, "old"); err != nil {
		t.Fatal(err)
	}
	if err := s.RemoveService(ctx, "gone"); err != nil {
		t.Fatalf("a missing service is removed already: %v", err)
	}
	var create, update *dockerCall
	for i, c := range f.calls {
		switch c.method + " " + c.path {
		case "POST /services/create":
			create = &f.calls[i]
		case "POST /services/svc-old/update":
			update = &f.calls[i]
		}
	}
	if create == nil || update == nil || !strings.Contains(update.query, "version=42") {
		t.Fatalf("calls: %+v", f.calls)
	}
	var sent swarm.ServiceSpec
	if err := json.Unmarshal([]byte(create.body), &sent); err != nil || sent.Name != "svc-new" || sent.EndpointSpec != nil {
		t.Fatalf("create body: %s", create.body)
	}
}

func TestDockerPull(t *testing.T) {
	ctx := context.Background()
	s, _ := newDockerSwarm(t, map[string]func(http.ResponseWriter, *http.Request){
		"POST /images/create": func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Query().Get("fromImage") == "docker.io/library/bad" {
				_, _ = io.WriteString(w, `{"status":"Pulling"}`+"\n"+`{"errorDetail":{"message":"manifest unknown"},"error":"manifest unknown"}`+"\n")
				return
			}
			_, _ = io.WriteString(w, `{"status":"Pulling"}`+"\n"+`{"status":"Downloaded newer image"}`+"\n")
		},
	})
	if err := s.PullImage(ctx, "nginx:alpine"); err != nil {
		t.Fatal(err)
	}
	if err := s.PullImage(ctx, "bad:1"); err == nil || !strings.Contains(err.Error(), "manifest unknown") {
		t.Fatalf("an error inside the stream fails the pull: %v", err)
	}
}

func TestDockerObserve(t *testing.T) {
	ctx := context.Background()
	s, f := newDockerSwarm(t, map[string]func(http.ResponseWriter, *http.Request){
		"GET /services/svc-n1": jsonReply(`{"ID":"x","Spec":{"Name":"svc-n1","Labels":{"keel.revision":"2","keel.service":"n1"}},"UpdateStatus":{"State":"rollback_completed","Message":"rolled back"}}`),
		"GET /tasks": jsonReply(`[{"ID":"t1","NodeID":"sw1","DesiredState":"running","Status":{"State":"running","Timestamp":"2026-10-08T12:00:00.123456789Z"},"Spec":{"ContainerSpec":{"Labels":{"keel.revision":"2","keel.service":"n1"}}}},
			{"ID":"t2","DesiredState":"shutdown","Status":{"State":"failed","Err":"exit 1"},"Spec":{"ContainerSpec":{}}}]`),
		"GET /services": jsonReply(`[{"ID":"x","Spec":{"Name":"svc-n1","Labels":{"keel.revision":"2"}}}]`),
		"GET /nodes":    jsonReply(`[{"ID":"a","Status":{"State":"ready"}},{"ID":"b","Status":{"State":"down"}},{"ID":"c","Status":{"State":"ready"}}]`),
	})
	svc, tasks, err := s.ObserveService(ctx, "n1")
	if err != nil {
		t.Fatal(err)
	}
	if svc.Name != "svc-n1" || svc.UpdateState != "rollback_completed" || svc.UpdateMessage != "rolled back" || svc.Labels["keel.revision"] != "2" {
		t.Fatalf("service: %+v", svc)
	}
	if len(tasks) != 2 || tasks[0].State != "running" || tasks[0].Timestamp != 1791460800123 ||
		tasks[0].Labels["keel.revision"] != "2" || tasks[1].Err != "exit 1" || tasks[1].Timestamp != 0 || tasks[1].Labels != nil {
		t.Fatalf("tasks: %+v", tasks)
	}
	var taskQuery string
	for _, c := range f.calls {
		if c.path == "/tasks" {
			taskQuery = c.query
		}
	}
	if !strings.Contains(taskQuery, "keel.service%3Dn1") {
		t.Fatalf("task filter: %s", taskQuery)
	}
	if svc, _, err := s.ObserveService(ctx, "gone"); svc.Name != "" || err != nil {
		t.Fatalf("missing service: %+v %v", svc, err)
	}
	services, all, err := s.ObserveServices(ctx)
	if err != nil || len(services) != 1 || len(all) != 2 {
		t.Fatalf("sweep: %+v %+v %v", services, all, err)
	}
	if ready, total, err := s.Servers(ctx); ready != 2 || total != 3 || err != nil {
		t.Fatalf("servers: %d/%d %v", ready, total, err)
	}
}

func TestDockerEnsureAgent(t *testing.T) {
	ctx := context.Background()
	var mu sync.Mutex
	current := ""
	s, f := newDockerSwarm(t, map[string]func(http.ResponseWriter, *http.Request){
		"GET /images/ghcr.io/thallesp/keel:1.0/json": jsonReply(`{"Id":"sha256:1","RepoDigests":["ghcr.io/thallesp/keel@sha256:feed"]}`),
		"GET /services/keel-agent": func(w http.ResponseWriter, _ *http.Request) {
			mu.Lock()
			defer mu.Unlock()
			if current == "" {
				w.WriteHeader(http.StatusNotFound)
				_, _ = io.WriteString(w, `{"message":"service keel-agent not found"}`)
				return
			}
			_, _ = io.WriteString(w, `{"ID":"agent","Version":{"Index":7},"Spec":{"Name":"keel-agent","Labels":{"keel.agent.spec":"`+current+`"}}}`)
		},
		"POST /services/create": func(w http.ResponseWriter, r *http.Request) {
			var spec struct{ Labels map[string]string }
			_ = json.NewDecoder(r.Body).Decode(&spec)
			mu.Lock()
			current = spec.Labels[agentSpecLabel]
			mu.Unlock()
			_, _ = io.WriteString(w, `{"ID":"agent"}`)
		},
		"POST /services/keel-agent/update": jsonReply(`{}`),
		"GET /secrets":                     jsonReply(`[{"ID":"s0","Spec":{"Name":"keel-agent-token-old"}}]`),
		"POST /secrets/create":             jsonReply(`{"ID":"snew"}`),
		"DELETE /secrets/s0":               func(w http.ResponseWriter, _ *http.Request) {},
	})
	spec := app.AgentSpec{Image: "ghcr.io/thallesp/keel:1.0", ControlURL: "http://100.64.0.1:8080", Token: "tok"}
	if err := s.EnsureAgent(ctx, spec); err != nil {
		t.Fatal(err)
	}
	if err := s.EnsureAgent(ctx, spec); err != nil {
		t.Fatal(err)
	}
	spec.Token = "rotated"
	if err := s.EnsureAgent(ctx, spec); err != nil {
		t.Fatal(err)
	}
	counts := map[string]int{}
	var created string
	for _, c := range f.calls {
		counts[c.method+" "+c.path]++
		if c.path == "/services/create" {
			created = c.body
		}
	}
	if counts["POST /services/create"] != 1 || counts["POST /services/keel-agent/update"] != 1 || counts["DELETE /secrets/s0"] != 3 {
		t.Fatalf("calls: %v", counts)
	}
	if !strings.Contains(created, `"Image":"ghcr.io/thallesp/keel:1.0@sha256:feed"`) || !strings.Contains(created, `"Global":{}`) {
		t.Fatalf("created: %s", created)
	}
}
