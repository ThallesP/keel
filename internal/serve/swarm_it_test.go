package serve

// End-to-end against a real Docker Swarm: keel serve and keel agent in this process, a real
// Swarm behind KEEL_IT_DOCKER_HOST. Skipped unless that variable is set; NEVER point it at a Swarm
// that runs anything you care about (it creates and deletes svc-* services). CI runs it on the
// runner's own fresh Swarm (ci.yml, job swarm); locally, use a Docker-in-Docker daemon:
//
//	docker run -d --privileged --name keel-dind -v /tmp/keel-dind:/var/run/dind docker:28-dind \
//	  dockerd --host unix:///var/run/dind/docker.sock
//	DOCKER_HOST=unix:///tmp/keel-dind/docker.sock docker swarm init --advertise-addr 127.0.0.1
//	DOCKER_HOST=unix:///tmp/keel-dind/docker.sock docker network create -d overlay --attachable keel
//	KEEL_IT_DOCKER_HOST=unix:///tmp/keel-dind/docker.sock go test ./internal/serve -run SwarmIT -v

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
	"github.com/ThallesP/keel/internal/app"
)

type itClient struct {
	t     *testing.T
	base  string
	token string
}

func (c *itClient) do(method, path string, body any) (int, map[string]any) {
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
	out := map[string]any{}
	_ = json.Unmarshal(raw, &out)
	return resp.StatusCode, out
}

func (c *itClient) ok(method, path string, body any) map[string]any {
	c.t.Helper()
	code, out := c.do(method, path, body)
	if code >= 300 {
		c.t.Fatalf("%s %s: %d %v", method, path, code, out)
	}
	return out
}

// settle waits for the environment's latest deployment to leave running and returns it.
func (c *itClient) settle(env string) map[string]any {
	c.t.Helper()
	deadline := time.Now().Add(3 * time.Minute)
	for time.Now().Before(deadline) {
		d, _ := c.ok("GET", "/api/environments/"+env+"/deployments/latest", nil)["deployment"].(map[string]any)
		if d != nil && d["status"] != "running" {
			return d
		}
		time.Sleep(time.Second)
	}
	c.t.Fatal("deployment did not settle in 3 minutes")
	return nil
}

func (c *itClient) node(env, id string) map[string]any {
	c.t.Helper()
	for _, n := range c.ok("GET", "/api/environments/"+env+"/nodes", nil)["nodes"].([]any) {
		if m := n.(map[string]any); m["id"] == id {
			return m
		}
	}
	return nil
}

func TestSwarmIT(t *testing.T) {
	host := os.Getenv("KEEL_IT_DOCKER_HOST")
	if host == "" {
		t.Skip("set KEEL_IT_DOCKER_HOST to a disposable Swarm manager to run")
	}
	t.Setenv("DOCKER_HOST", host)
	t.Setenv("DOCKER_SOCKET", "")
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
	go func() { served <- serveOn(ctx, ln, cfg, Options{Version: "it"}, log) }()

	// keel agent on the same daemon: Docker events drive observation.
	t.Setenv("KEEL_URL", base)
	t.Setenv("KEEL_WORKER_TOKEN", cfg.WorkerToken)
	t.Setenv("KEEL_STATE", filepath.Join(t.TempDir(), "agent-state.json"))
	agentDone := make(chan error, 1)
	go func() { agentDone <- agent.Main(ctx) }()

	docker, err := client.New(client.WithHost(host))
	if err != nil {
		t.Fatal(err)
	}
	var created []string
	t.Cleanup(func() {
		cancel()
		<-served
		<-agentDone
		for _, id := range created { // leave the shared daemon clean even on failure
			_, _ = docker.ServiceRemove(context.Background(), "svc-"+id, client.ServiceRemoveOptions{})
		}
		docker.Close()
		if t.Failed() {
			t.Logf("serve log:\n%s", logs.String())
		}
	})

	c := &itClient{t: t, base: base}
	for i := 0; ; i++ { // the listener is up before serveOn returns from setup
		if code, _ := c.do("GET", "/api/meta", nil); code == 200 {
			break
		}
		if i > 50 {
			t.Fatal("serve did not come up")
		}
		time.Sleep(100 * time.Millisecond)
	}
	signUp := c.ok("POST", "/api/auth/sign-up", map[string]any{"email": "it@example.com", "password": "correct horse battery", "name": "IT"})
	c.token = signUp["token"].(string)
	slug := c.ok("POST", "/api/projects/default", nil)["slug"].(string)
	project := c.ok("GET", "/api/projects/by-slug/"+slug, nil)["project"].(map[string]any)
	env := project["environment"].(map[string]any)["id"].(string)

	// Deploy: pull, create, observe through agent events, settle.
	n := c.ok("POST", "/api/environments/"+env+"/nodes", map[string]any{
		"type": "service", "name": fmt.Sprintf("web-%d", time.Now().UnixNano()%100000), "image": "nginx:alpine", "port": 80, "deploy": true,
	})
	id := n["id"].(string)
	created = append(created, id)
	if d := c.settle(env); d["status"] != "success" {
		t.Fatalf("first deploy: %v", d)
	}
	if v := c.node(env, id); v["status"] != "healthy" || v["running"] != float64(1) {
		t.Fatalf("after deploy: %v", v)
	}

	// A staged variable ships as an update and lands in the service's env.
	c.ok("POST", "/api/nodes/"+id+"/variables", map[string]any{"key": "GREETING", "value": "port ${{ " + c.node(env, id)["name"].(string) + ".PORT }}", "secret": false})
	if s := c.ok("GET", "/api/environments/"+env+"/summary", nil)["summary"].(map[string]any); s["pendingChanges"] != float64(1) {
		t.Fatalf("summary after a variable: %v", s)
	}
	c.ok("POST", "/api/environments/"+env+"/deployments", map[string]any{})
	if d := c.settle(env); d["status"] != "success" {
		t.Fatalf("ship: %v", d)
	}
	svc, err := docker.ServiceInspect(context.Background(), "svc-"+id, client.ServiceInspectOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if env := svc.Service.Spec.TaskTemplate.ContainerSpec.Env; strings.Join(env, ",") != "GREETING=port 80" {
		t.Fatalf("service env: %v", env)
	}

	// Stop, start, redeploy.
	c.ok("POST", "/api/nodes/"+id+"/stop", nil)
	if d := c.settle(env); d["status"] != "success" || c.node(env, id)["status"] != "stopped" {
		t.Fatalf("stop: %v / %v", d, c.node(env, id))
	}
	c.ok("POST", "/api/nodes/"+id+"/start", nil)
	if d := c.settle(env); d["status"] != "success" || c.node(env, id)["status"] != "healthy" {
		t.Fatalf("start: %v / %v", d, c.node(env, id))
	}
	c.ok("POST", "/api/environments/"+env+"/deployments", map[string]any{"only": []string{id}, "refresh": true})
	if d := c.settle(env); d["status"] != "success" || !strings.HasPrefix(d["message"].(string), "redeploy ") {
		t.Fatalf("redeploy: %v", d)
	}

	// Delete removes the Swarm service.
	if code, out := c.do("DELETE", "/api/nodes/"+id, nil); code != 204 {
		t.Fatalf("delete: %d %v", code, out)
	}
	for i := 0; ; i++ {
		_, err := docker.ServiceInspect(context.Background(), "svc-"+id, client.ServiceInspectOptions{})
		if err != nil {
			break
		}
		if i > 30 {
			t.Fatal("svc still there 30 s after delete")
		}
		time.Sleep(time.Second)
	}
}
