package serve

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/moby/moby/client"

	"github.com/ThallesP/keel/internal/agent"
	"github.com/ThallesP/keel/internal/api"
	"github.com/ThallesP/keel/internal/app"
)

type itClient struct {
	t     *testing.T
	base  string
	token string
}

func (c *itClient) do(method, path string, body any) (int, []byte) {
	c.t.Helper()
	var r io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		r = bytes.NewReader(b)
	}
	req, _ := http.NewRequest(method, c.base+path, r)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		c.t.Fatalf("%s %s: %v", method, path, err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, raw
}

func call[T any](c *itClient, method, path string, body any) T {
	c.t.Helper()
	code, raw := c.do(method, path, body)
	var out T
	if code >= 300 || len(raw) > 0 && json.Unmarshal(raw, &out) != nil {
		c.t.Fatalf("%s %s: %d %s", method, path, code, raw)
	}
	return out
}

func (c *itClient) settle(env string) api.Deployment {
	c.t.Helper()
	for range 180 {
		d := call[api.DeploymentEnvelope](c, "GET", "/api/environments/"+env+"/deployments/latest", nil).Deployment
		if d != nil && d.Status != "running" {
			return *d
		}
		time.Sleep(time.Second)
	}
	c.t.Fatal("deployment did not settle in 3 minutes")
	return api.Deployment{}
}

func (c *itClient) node(env, id string) api.NodeView {
	c.t.Helper()
	for _, n := range call[api.NodeList](c, "GET", "/api/environments/"+env+"/nodes", nil).Nodes {
		if n.ID == id {
			return n
		}
	}
	return api.NodeView{}
}

func TestSwarmIT(t *testing.T) {
	host := os.Getenv("KEEL_IT_DOCKER_HOST")
	if host == "" {
		t.Skip("set KEEL_IT_DOCKER_HOST to a disposable Swarm manager to run")
	}
	t.Setenv("DOCKER_HOST", host)
	saved := adapters
	adapters = []adapter{{"passwords", wirePasswords}, {"swarm", wireSwarm}}
	t.Cleanup(func() { adapters = saved })

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	base := "http://" + ln.Addr().String()
	cfg := app.Config{Version: "it", SiteURL: base, WorkerToken: "it-token", DataDir: filepath.Join(t.TempDir(), "data")}
	logs := &strings.Builder{}
	log := slog.New(slog.NewTextHandler(logs, nil))
	ctx, cancel := context.WithCancel(context.Background())
	served := make(chan error, 1)
	go func() { served <- serveOn(ctx, ln, cfg, nil, log) }()

	t.Setenv("KEEL_URL", base)
	t.Setenv("KEEL_WORKER_TOKEN", cfg.WorkerToken)
	t.Setenv("KEEL_STATE", filepath.Join(t.TempDir(), "agent-state.json"))
	agentDone := make(chan error, 1)
	go func() { agentDone <- agent.Main(ctx) }()

	docker, err := client.New(client.WithHost(host))
	if err != nil {
		t.Fatal(err)
	}
	var id string
	t.Cleanup(func() {
		cancel()
		<-served
		<-agentDone
		if id != "" {
			_, _ = docker.ServiceRemove(context.Background(), "svc-"+id, client.ServiceRemoveOptions{})
		}
		docker.Close()
		if t.Failed() {
			t.Logf("serve log:\n%s", logs.String())
		}
	})

	c := &itClient{t: t, base: base}
	c.token = call[api.SignedIn](c, "POST", "/api/auth/sign-up", api.SignUpRequest{Email: "it@example.com", Password: "correct horse battery", Name: "IT"}).Token
	slug := call[api.DefaultProject](c, "POST", "/api/projects/default", nil).Slug
	env := call[api.ProjectBySlug](c, "GET", "/api/projects/by-slug/"+slug, nil).Project.Environment.ID

	id = call[api.CreatedNode](c, "POST", "/api/environments/"+env+"/nodes", api.CreateNodeRequest{
		Type: "service", Name: fmt.Sprintf("web-%d", time.Now().UnixNano()%100000), Image: new("nginx:alpine"), Port: new(80), Deploy: true,
	}).ID
	if d := c.settle(env); d.Status != "success" {
		t.Fatalf("first deploy: %+v", d)
	}
	if v := c.node(env, id); v.Status != "healthy" || v.Running != 1 {
		t.Fatalf("after deploy: %+v", v)
	}

	call[struct{}](c, "POST", "/api/nodes/"+id+"/variables", api.SetVariableRequest{Key: "GREETING", Value: "port ${{ " + c.node(env, id).Name + ".PORT }}"})
	if s := call[api.EnvironmentSummaryResult](c, "GET", "/api/environments/"+env+"/summary", nil).Summary; s.PendingChanges != 1 {
		t.Fatalf("summary after a variable: %+v", s)
	}
	call[api.ShipResponse](c, "POST", "/api/environments/"+env+"/deployments", api.ShipRequest{})
	if d := c.settle(env); d.Status != "success" {
		t.Fatalf("ship: %+v", d)
	}
	svc, err := docker.ServiceInspect(context.Background(), "svc-"+id, client.ServiceInspectOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if env := svc.Service.Spec.TaskTemplate.ContainerSpec.Env; strings.Join(env, ",") != "GREETING=port 80" {
		t.Fatalf("service env: %v", env)
	}

	call[api.StoppedNode](c, "POST", "/api/nodes/"+id+"/stop", nil)
	if d := c.settle(env); d.Status != "success" || c.node(env, id).Status != "stopped" {
		t.Fatalf("stop: %+v / %+v", d, c.node(env, id))
	}
	call[api.StartedNode](c, "POST", "/api/nodes/"+id+"/start", nil)
	if d := c.settle(env); d.Status != "success" || c.node(env, id).Status != "healthy" {
		t.Fatalf("start: %+v / %+v", d, c.node(env, id))
	}
	call[api.ShipResponse](c, "POST", "/api/environments/"+env+"/deployments", api.ShipRequest{Only: []string{id}, Refresh: true})
	if d := c.settle(env); d.Status != "success" || !strings.HasPrefix(d.Message, "redeploy ") {
		t.Fatalf("redeploy: %+v", d)
	}

	if code, raw := c.do("DELETE", "/api/nodes/"+id, nil); code != 204 {
		t.Fatalf("delete: %d %s", code, raw)
	}
	for range 30 {
		if _, err := docker.ServiceInspect(context.Background(), "svc-"+id, client.ServiceInspectOptions{}); err != nil {
			return
		}
		time.Sleep(time.Second)
	}
	t.Fatal("svc still there 30 s after delete")
}
