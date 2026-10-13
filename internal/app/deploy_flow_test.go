package app_test

import (
	"context"
	"errors"
	"reflect"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/ThallesP/durable"

	"github.com/ThallesP/keel/internal/app"
	"github.com/ThallesP/keel/internal/domain"
)

var ctx = context.Background()

func TestShipRulesAndMessages(t *testing.T) {
	w := newWorld(t)
	if _, err := w.app.ShipEnvironment(ctx, w.member, "env", app.ShipOptions{}); codeAndMessage(err) != "NOTHING_TO_SHIP: Nothing to ship" {
		t.Fatalf("empty environment: %v", codeAndMessage(err))
	}
	a := w.addNode("api", applyErr("old failure"))
	w.addNode("data", kind(domain.NodeVolume))
	c := w.addNode("cache", clean, shipped(4))
	b := w.addNode("worker", image("ghcr.io/acme/worker:1"))
	w.addNode("elsewhere", inEnv("env2"))

	id := w.ship(app.ShipOptions{})
	d := w.deployment(id)
	if d.Message != "ship api, worker" || d.Status != domain.DeploymentRunning || d.StartedAt != w.clock.Load() || d.FinishedAt != 0 {
		t.Fatalf("deployment: %+v", d)
	}
	if got := stepStatuses(d); got != "api=pending worker=pending health checks=pending" {
		t.Fatalf("steps: %s", got)
	}
	if d.Steps[0].NodeID != a.ID || d.Steps[1].NodeID != b.ID || d.Steps[2].NodeID != "" || len(d.Log) != 0 {
		t.Fatalf("steps: %+v log %v", d.Steps, d.Log)
	}
	for _, n := range []domain.Node{w.node(a.ID), w.node(b.ID)} {
		if n.Desired.Revision != 1 || n.Dirty || n.ShippedAt != w.clock.Load() || n.ApplyError != "" {
			t.Fatalf("shipped node: %+v", n)
		}
	}
	if n := w.node(c.ID); n.Desired.Revision != 4 || n.ShippedAt != 0 {
		t.Fatalf("clean node shipped: %+v", n)
	}
	if !w.pub.has("org", "/api/environments/env") || !w.pub.has("org", "/api/deployments/"+id) || !w.pub.has("org", "/api/nodes/"+a.ID) {
		t.Fatalf("topics: %v", w.pub.topics)
	}
	if r := w.run("deploy:" + id); r.Workflow != "deploy" || r.Status != durable.StatusPending {
		t.Fatalf("deploy run: %+v", r)
	}

	w.addNode("late")
	if _, err := w.app.ShipEnvironment(ctx, w.member, "env", app.ShipOptions{}); codeAndMessage(err) != "DEPLOYMENT_RUNNING: A deployment is already running" {
		t.Fatalf("second ship: %v", codeAndMessage(err))
	}
	if n := w.node(a.ID); n.Desired.Revision != 1 {
		t.Fatalf("refused ship bumped a revision: %+v", n.Desired)
	}
	if runs, err := w.app.Workflows.List(ctx, durable.RunFilter{}); err != nil || len(runs) != 1 {
		t.Fatalf("a refused ship left a run: %+v %v", runs, err)
	}
}

func TestShipOnly(t *testing.T) {
	cases := []struct {
		name    string
		opts    func(a, vol domain.Node) app.ShipOptions
		message string
		err     string
	}{
		{"only, no refresh", func(a, _ domain.Node) app.ShipOptions { return app.ShipOptions{Only: []string{a.ID}} }, "deploy api", ""},
		{"only, refresh", func(a, _ domain.Node) app.ShipOptions { return app.ShipOptions{Only: []string{a.ID}, Refresh: true} }, "redeploy api", ""},
		{"verb", func(a, _ domain.Node) app.ShipOptions { return app.ShipOptions{Only: []string{a.ID}, Verb: "stop"} }, "stop api", ""},
		{"unknown and non-deployable ids are ignored", func(a, vol domain.Node) app.ShipOptions {
			return app.ShipOptions{Only: []string{"nope", vol.ID, a.ID}}
		}, "deploy api", ""},
		{"an empty list is still a list", func(domain.Node, domain.Node) app.ShipOptions { return app.ShipOptions{Only: []string{}} }, "", "NOTHING_TO_SHIP: Nothing to ship"},
		{"only foreign ids", func(domain.Node, domain.Node) app.ShipOptions { return app.ShipOptions{Only: []string{"x"}} }, "", "NOTHING_TO_SHIP: Nothing to ship"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			w := newWorld(t)
			a := w.addNode("api", clean, shipped(2))
			w.addNode("other")
			vol := w.addNode("data", kind(domain.NodeVolume))
			id, err := w.app.ShipEnvironment(ctx, w.member, "env", c.opts(a, vol))
			if c.err != "" {
				if codeAndMessage(err) != c.err {
					t.Fatalf("got %v", codeAndMessage(err))
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			d := w.deployment(id)
			if d.Message != c.message || len(d.Steps) != 2 || w.node(a.ID).Desired.Revision != 3 {
				t.Fatalf("got %q %+v", d.Message, d.Steps)
			}
		})
	}
}

func TestOrganizationIsolation(t *testing.T) {
	w := newWorld(t)
	a := w.addNode("api")
	id := w.ship(app.ShipOptions{})

	for _, actor := range []domain.Actor{w.outsider, {}} {
		if d, err := w.app.GetDeployment(ctx, actor, id); err != nil || d != nil {
			t.Errorf("%+v GetDeployment: %v %v", actor, d, err)
		}
		if d, err := w.app.LatestDeployment(ctx, actor, "env"); err != nil || d != nil {
			t.Errorf("%+v LatestDeployment: %v %v", actor, d, err)
		}
		if ds, err := w.app.ListNodeDeployments(ctx, actor, a.ID); err != nil || len(ds) != 0 {
			t.Errorf("%+v ListNodeDeployments: %v %v", actor, ds, err)
		}
	}
	if _, err := w.app.ShipEnvironment(ctx, w.outsider, "env", app.ShipOptions{Only: []string{a.ID}}); codeAndMessage(err) != "PROJECT_NOT_FOUND: Environment not found" {
		t.Errorf("outsider ship: %v", codeAndMessage(err))
	}
	if _, err := w.app.ShipEnvironment(ctx, w.member, "nope", app.ShipOptions{}); codeAndMessage(err) != "PROJECT_NOT_FOUND: Environment not found" {
		t.Errorf("missing environment: %v", codeAndMessage(err))
	}
	if _, err := w.app.ShipEnvironment(ctx, domain.Actor{}, "env", app.ShipOptions{}); codeAndMessage(err) != "NOT_AUTHENTICATED: Not authenticated" {
		t.Errorf("signed-out ship: %v", codeAndMessage(err))
	}
	if d, _ := w.app.LatestDeployment(ctx, w.outsider, "env2"); d != nil {
		t.Errorf("env2 latest: %+v", d)
	}
	if n := w.node(a.ID); n.Desired.Revision != 1 {
		t.Errorf("revision moved: %d", n.Desired.Revision)
	}
}

func TestDeploymentReads(t *testing.T) {
	w := newWorld(t)
	a := w.addNode("api")
	b := w.addNode("worker")
	if d, err := w.app.LatestDeployment(ctx, w.member, "env"); err != nil || d != nil {
		t.Fatalf("no deployment yet: %v %v", d, err)
	}
	if d, err := w.app.GetDeployment(ctx, w.member, "%%not-an-id"); err != nil || d != nil {
		t.Fatalf("malformed id: %v %v", d, err)
	}

	var apiIDs, workerIDs []string
	for i := range 55 {
		target, ids := a, &apiIDs
		if i >= 25 {
			target, ids = b, &workerIDs
		}
		id := w.ship(app.ShipOptions{Only: []string{target.ID}})
		*ids = append(*ids, id)
		if err := w.app.TimeoutDeploymentForTest(ctx, id); err != nil {
			t.Fatal(err)
		}
		w.clock.Add(1)
	}
	latest, err := w.app.LatestDeployment(ctx, w.member, "env")
	if err != nil || latest == nil || latest.ID != workerIDs[29] {
		t.Fatalf("latest: %+v %v", latest, err)
	}
	if len(latest.Log) == 0 || latest.Log[0].Text != "worker: timed out waiting for replicas" {
		t.Fatalf("latest log: %+v", latest.Log)
	}
	ds, _ := w.app.ListNodeDeployments(ctx, w.member, b.ID)
	if len(ds) != 20 || ds[0].ID != workerIDs[29] || ds[19].ID != workerIDs[10] || len(ds[0].Log) == 0 {
		t.Fatalf("worker list: %d", len(ds))
	}
	ds, _ = w.app.ListNodeDeployments(ctx, w.member, a.ID)
	if len(ds) != 20 || ds[0].ID != apiIDs[24] || ds[19].ID != apiIDs[5] {
		t.Fatalf("api list: %d", len(ds))
	}
	got, _ := w.app.GetDeployment(ctx, w.member, apiIDs[0])
	if got == nil || got.Message != "deploy api" {
		t.Fatalf("get: %+v", got)
	}
}

func TestApplyHappyPath(t *testing.T) {
	w := newWorld(t)
	a := w.addNode("api", port(8080))
	err := w.store.Write(context.Background(), func(tx app.Tx) error {
		for _, v := range []domain.Variable{{Key: "PORT", Value: "8080"}, {Key: "DATABASE_URL", Value: "postgres://x"}} {
			v.ID, v.NodeID = domain.NewID(), a.ID
			if err := tx.CanvasInsertVariable(v); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	w.clock.Add(1)
	w.start()
	id := w.ship(app.ShipOptions{})
	w.flush()
	if len(w.swarm.creates) != 1 {
		t.Fatalf("creates: %+v", w.swarm.creates)
	}
	want := app.ServiceSpec{NodeID: a.ID, Image: "nginx:alpine", Revision: 1, Replicas: 1, Env: []string{"DATABASE_URL=postgres://x", "PORT=8080"}}
	if !reflect.DeepEqual(w.swarm.creates[0], want) {
		t.Fatalf("spec:\n got %+v\nwant %+v", w.swarm.creates[0], want)
	}
	d := w.deployment(id)
	if got := logTexts(d); !reflect.DeepEqual(got, []string{"pulling nginx:alpine", "pulled nginx:alpine in 0.0s", "service created · 1 replica(s)"}) {
		t.Fatalf("log: %q", got)
	}
	if s := d.Steps[0]; s.Status != domain.StepRunning || s.StartedAt == 0 || s.AppliedAt == 0 {
		t.Fatalf("step: %+v", s)
	}
	w.jobs.advance(499 * time.Millisecond)
	if len(w.swarm.observed) != 0 {
		t.Fatalf("observed too early")
	}
	w.jobs.advance(time.Millisecond)
	d = w.deployment(id)
	if d.Status != domain.DeploymentSuccess || d.FinishedAt == 0 || stepStatuses(d) != "api=done health checks=done" {
		t.Fatalf("after observe: %s %s", d.Status, stepStatuses(d))
	}
	if got := logTexts(d)[3:]; !reflect.DeepEqual(got, []string{"api: 1/1 replicas running", "all replicas healthy"}) {
		t.Fatalf("log: %q", got)
	}
	n := w.node(a.ID)
	if n.DeployedRevision != 1 || n.Observed == nil || domain.DeriveStatus(n) != domain.StatusHealthy {
		t.Fatalf("node: %+v", n)
	}
	w.flush()
	if r := w.run("deploy:" + id); r.Status != durable.StatusSucceeded {
		t.Fatalf("the deploy run did not end when the deployment settled: %+v", r)
	}

	w.pub.reset()
	id = w.ship(app.ShipOptions{Only: []string{a.ID}, Refresh: true})
	w.flush()
	w.jobs.advance(time.Second)
	d = w.deployment(id)
	if got := logTexts(d); len(got) < 3 || got[0] != "pulling nginx:alpine" || got[2] != "service updated · revision 2" || d.Status != domain.DeploymentSuccess {
		t.Fatalf("redeploy log: %q %s", got, d.Status)
	}
	id = w.ship(app.ShipOptions{Only: []string{a.ID}})
	w.flush()
	w.jobs.advance(time.Second)
	if got := logTexts(w.deployment(id)); got[0] != "using cached nginx:alpine" || got[1] != "service updated · revision 3" {
		t.Fatalf("restart log: %q", got)
	}
	if len(w.swarm.pulls) != 2 {
		t.Fatalf("pulls: %v", w.swarm.pulls)
	}
}

func TestApplyFailures(t *testing.T) {
	t.Run("missing image", func(t *testing.T) {
		w := newWorld(t)
		a := w.addNode("api", image("nope:1"))
		b := w.addNode("worker")
		w.swarm.pullErr["nope:1"] = errors.New("Error response from daemon:\n  pull access denied for nope")
		w.start()
		id := w.ship(app.ShipOptions{})
		w.flush()
		d := w.deployment(id)
		if d.Status != domain.DeploymentFailed || stepStatuses(d) != "api=failed worker=running health checks=failed" {
			t.Fatalf("deployment: %s %s", d.Status, stepStatuses(d))
		}
		if got := nodeLog(d, a.ID); got[1] != "error: Error response from daemon: pull access denied for nope" {
			t.Fatalf("log: %q", got)
		}
		if n := w.node(a.ID); n.ApplyError != "Error response from daemon: pull access denied for nope" || domain.DeriveStatus(n) != domain.StatusError {
			t.Fatalf("node: %+v", n)
		}
		if d.Steps[1].AppliedAt == 0 {
			t.Fatalf("sibling: %+v", d.Steps[1])
		}
		if len(w.swarm.creates) != 1 || w.swarm.creates[0].NodeID != b.ID {
			t.Fatalf("creates: %+v", w.swarm.creates)
		}
		if r := w.run("deploy:" + id); r.Status != durable.StatusSucceeded {
			t.Fatalf("the deploy run did not end when the apply failed: %+v", r)
		}
	})
	t.Run("registry down, image cached", func(t *testing.T) {
		w := newWorld(t)
		a := w.addNode("api")
		w.swarm.cached["nginx:alpine"] = true
		w.swarm.pullErr["nginx:alpine"] = errors.New("registry unreachable")
		w.start()
		id := w.ship(app.ShipOptions{Only: []string{a.ID}, Refresh: true})
		w.flush()
		got := logTexts(w.deployment(id))
		if got[1] != "pull failed (registry unreachable), using cached image" || got[2] != "service created · 1 replica(s)" {
			t.Fatalf("log: %q", got)
		}
	})
	t.Run("update out of sequence is retried, three attempts in all", func(t *testing.T) {
		w := newWorld(t)
		a := w.addNode("api")
		w.start()
		w.ship(app.ShipOptions{})
		w.flush()
		w.jobs.advance(time.Second)
		w.swarm.updateFailures = 2
		id := w.ship(app.ShipOptions{Only: []string{a.ID}})
		w.flush()
		if got := logTexts(w.deployment(id)); got[1] != "service updated · revision 2" {
			t.Fatalf("log: %q", got)
		}
		w.jobs.advance(time.Second)
		w.swarm.updateFailures = 3
		id = w.ship(app.ShipOptions{Only: []string{a.ID}})
		w.flush()
		if got := logTexts(w.deployment(id)); got[1] != "error: Error response from daemon: rpc error: update out of sequence" {
			t.Fatalf("log: %q", got)
		}
	})
	t.Run("port moves endpoints and syncs the proxy", func(t *testing.T) {
		w := newWorld(t)
		a := w.addNode("api", port(9999))
		err := w.store.Write(context.Background(), func(tx app.Tx) error {
			return tx.ReplaceEndpoints(a.ID, []domain.Endpoint{{Protocol: domain.ProtocolHTTP, Port: 80, Domain: "api.example.com",
				Status: domain.EndpointStatus{State: domain.EndpointLive}}})
		})
		if err != nil {
			t.Fatal(err)
		}
		w.start()
		w.ship(app.ShipOptions{})
		w.flush()
		if w.jobs.count("proxy:sync") != 1 || w.node(a.ID).Endpoints[0].Port != 9999 {
			t.Fatalf("jobs: %v endpoints %+v", w.jobs.keys(), w.node(a.ID).Endpoints)
		}
	})
}

func TestApplySerializedAndSkipsStaleRevision(t *testing.T) {
	t.Run("queued behind: skipped before pulling", func(t *testing.T) {
		w := newWorld(t)
		x := w.addNode("x")
		d1 := w.ship(app.ShipOptions{})
		w.failRunning(d1)
		d2 := w.ship(app.ShipOptions{Only: []string{x.ID}})
		w.start()
		w.flush()
		if len(w.swarm.creates) != 1 || w.swarm.creates[0].Revision != 2 || !slices.Equal(w.swarm.pulls, []string{"nginx:alpine"}) {
			t.Fatalf("creates %+v pulls %v", w.swarm.creates, w.swarm.pulls)
		}
		if got := logTexts(w.deployment(d1)); !slices.Equal(got, []string{"superseded by revision 2"}) {
			t.Fatalf("d1 log: %q", got)
		}
		if s := w.deployment(d2).Steps[0]; s.AppliedAt == 0 {
			t.Fatalf("d2 step: %+v", s)
		}
	})
	t.Run("shipped again during the pull: skipped before createOrUpdate", func(t *testing.T) {
		w := newWorld(t)
		x := w.addNode("x", image("slow:1"))
		d1 := w.ship(app.ShipOptions{})
		shipped := make(chan string, 1)
		var once sync.Once
		w.swarm.onPull = func(string) {
			once.Do(func() {
				w.failRunning(d1)
				shipped <- w.ship(app.ShipOptions{Only: []string{x.ID}})
			})
		}
		w.start()
		w.flush()
		d2 := <-shipped
		if len(w.swarm.creates) != 1 || w.swarm.creates[0].Revision != 2 || len(w.swarm.updates) != 0 {
			t.Fatalf("creates %+v updates %+v", w.swarm.creates, w.swarm.updates)
		}
		got := logTexts(w.deployment(d1))
		if got[len(got)-1] != "superseded by revision 2" {
			t.Fatalf("d1 log: %q", got)
		}
		if got := logTexts(w.deployment(d2)); got[0] != "using cached slow:1" || got[1] != "service created · 1 replica(s)" {
			t.Fatalf("d2 log: %q", got)
		}
	})
}

func TestApplyNodeDeleted(t *testing.T) {
	t.Run("during the pull", func(t *testing.T) {
		w := newWorld(t)
		a := w.addNode("api")
		w.swarm.onPull = func(string) { w.deleteNode(a.ID) }
		w.start()
		w.ship(app.ShipOptions{})
		w.flush()
		if len(w.swarm.creates) != 0 {
			t.Fatalf("created a service for a deleted node")
		}
	})
	t.Run("between the check and the create", func(t *testing.T) {
		w := newWorld(t)
		a := w.addNode("api")
		w.swarm.onCreate = func(app.ServiceSpec) { w.deleteNode(a.ID) }
		w.start()
		id := w.ship(app.ShipOptions{})
		w.flush()
		if !reflect.DeepEqual(w.swarm.removed, []string{a.ID}) {
			t.Fatalf("orphan not taken back: %v", w.swarm.removed)
		}
		if d := w.deployment(id); d.Steps[0].AppliedAt != 0 {
			t.Fatalf("step applied: %+v", d.Steps[0])
		}
	})
	t.Run("delete settles the step and removes the service", func(t *testing.T) {
		w := newWorld(t)
		a := w.addNode("api")
		id := w.ship(app.ShipOptions{})
		if err := w.app.RemoveNode(ctx, w.member, a.ID); err != nil {
			t.Fatal(err)
		}
		w.jobs.run()
		d := w.deployment(id)
		if d.Status != domain.DeploymentFailed || !reflect.DeepEqual(logTexts(d), []string{"api: node deleted"}) {
			t.Fatalf("deployment: %s %q", d.Status, logTexts(d))
		}
		w.start()
		w.flush()
		if !reflect.DeepEqual(w.swarm.removed, []string{a.ID}) || len(w.swarm.creates) != 0 {
			t.Fatalf("removed %v creates %+v", w.swarm.removed, w.swarm.creates)
		}
		if r := w.run("deploy:" + id); r.Status != durable.StatusSucceeded {
			t.Fatalf("the deploy run missed the signal sent before it started: %+v", r)
		}
		w.app.ScheduleObserve(a.ID)
		if w.jobs.count("reconcile") != 1 || w.jobs.count("observe:") != 0 {
			t.Fatalf("jobs: %v", w.jobs.keys())
		}
	})
}

func TestDeploymentTimeout(t *testing.T) {
	w := newWorld(t)
	w.app.SetDeployTimeoutForTest(time.Second)
	a := w.addNode("api")
	b := w.addNode("worker")
	w.swarm.tasks[a.ID] = []app.SwarmTask{
		{DesiredState: "shutdown", State: "rejected", Err: "no suitable node", Revision: 1},
		{DesiredState: "running", State: "pending", Revision: 1},
	}
	w.start()
	id := w.ship(app.ShipOptions{})
	w.flush()
	w.jobs.advance(4*time.Minute + 59*time.Second)
	d := w.deployment(id)
	if d.Status != domain.DeploymentRunning || stepStatuses(d) != "api=running worker=done health checks=running" {
		t.Fatalf("before the timeout: %s %s", d.Status, stepStatuses(d))
	}
	w.deleteNode(b.ID)
	w.pub.reset()
	w.waitFor("the timeout", func() bool { return w.deployment(id).Status != domain.DeploymentRunning })
	w.flush()
	d = w.deployment(id)
	if d.Status != domain.DeploymentFailed || stepStatuses(d) != "api=failed worker=done health checks=failed" {
		t.Fatalf("after: %s %s", d.Status, stepStatuses(d))
	}
	if got := logTexts(d); got[len(got)-1] != "api: timed out waiting for replicas · no suitable node" {
		t.Fatalf("log: %q", got)
	}
	n := w.node(a.ID)
	if n.ApplyError != "timed out waiting for replicas · no suitable node" || domain.DeriveStatus(n) != domain.StatusError {
		t.Fatalf("node: %+v", n)
	}
	if !w.pub.has("org", "/api/deployments/"+id) || !w.pub.has("org", "/api/environments/env") {
		t.Fatalf("topics: %v", w.pub.topics)
	}
	if r := w.run("deploy:" + id); r.Status != durable.StatusSucceeded {
		t.Fatalf("deploy run: %+v", r)
	}
	if err := w.app.TimeoutDeploymentForTest(ctx, id); err != nil {
		t.Fatal(err)
	}
	if d2 := w.deployment(id); !reflect.DeepEqual(d2, d) {
		t.Fatalf("second timeout changed it")
	}
}

func TestDeploymentsResumeAfterRestart(t *testing.T) {
	w := newWorld(t)
	w.app.SetDeployTimeoutForTest(200 * time.Millisecond)
	w.addNode("api")
	b := w.addNode("worker", inEnv("env2"))
	w.start()
	overdue := w.ship(app.ShipOptions{})
	w.flush()
	w.stop()
	fresh, err := w.app.ShipEnvironment(ctx, w.outsider, "env2", app.ShipOptions{})
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(300 * time.Millisecond)

	w.restart(app.Config{AgentImage: "ghcr.io/thallesp/keel:1.2.3", SiteURL: "http://100.64.0.1:8080", WorkerToken: "secret"})
	w.app.SetDeployTimeoutForTest(time.Second)
	w.swarm.tasks[b.ID] = []app.SwarmTask{{DesiredState: "running", State: "pending", Revision: 1}}
	w.start()
	w.app.Recover(ctx)
	w.flush()
	w.jobs.run()
	if d := w.deployment(overdue); d.Status != domain.DeploymentFailed {
		t.Fatalf("overdue deployment: %s %s", d.Status, stepStatuses(d))
	}
	if !reflect.DeepEqual(w.swarm.agents, []app.AgentSpec{{Image: "ghcr.io/thallesp/keel:1.2.3", ControlURL: "http://100.64.0.1:8080", Token: "secret"}}) {
		t.Fatalf("agent: %+v", w.swarm.agents)
	}
	if w.swarm.observed[0] != "*" {
		t.Fatalf("no sweep: %v", w.swarm.observed)
	}
	if len(w.swarm.creates) != 2 || w.swarm.creates[1].NodeID != b.ID {
		t.Fatalf("the apply shipped while the engine was down did not run: %+v", w.swarm.creates)
	}
	if d := w.deployment(fresh); d.Status != domain.DeploymentRunning {
		t.Fatalf("fresh timed out early: %s %s", d.Status, stepStatuses(d))
	}
	w.waitFor("the fresh deployment to time out", func() bool { return w.deployment(fresh).Status == domain.DeploymentFailed })
}

func TestIngestEventsDebounce(t *testing.T) {
	w := newWorld(t)
	a := w.addNode("api")
	b := w.addNode("worker")
	w.addNode("draft", noDesired)
	w.start()
	w.ship(app.ShipOptions{})
	w.flush()
	w.jobs.advance(time.Second)
	w.flush()
	w.swarm.observed = nil

	svcEvent := func(typ, name string) app.DockerEvent {
		if typ == "container" {
			return app.DockerEvent{Type: typ, Name: name + ".1.abc", ServiceName: name}
		}
		return app.DockerEvent{Type: typ, Name: name}
	}
	w.app.IngestWorkerEvents(ctx, []app.DockerEvent{
		svcEvent("container", "svc-"+a.ID),
		svcEvent("service", "svc-"+a.ID),
		svcEvent("container", "svc-"+b.ID),
		svcEvent("container", "svc-unknown"),
		svcEvent("container", "postgres"),
		{Type: "network"},
		{Type: "node"},
		{Type: "node"},
	}, false)
	if w.jobs.count("observe:servers") != 1 || w.jobs.count("observe:"+a.ID) != 1 || w.jobs.count("observe:"+b.ID) != 1 || w.jobs.count("observe:all") != 0 {
		t.Fatalf("jobs: %v", w.jobs.keys())
	}
	w.jobs.advance(200 * time.Millisecond)
	w.app.IngestWorkerEvents(ctx, []app.DockerEvent{svcEvent("container", "svc-"+a.ID)}, false)
	if w.jobs.count("observe:"+a.ID) != 1 {
		t.Fatalf("not coalesced: %v", w.jobs.keys())
	}
	w.jobs.advance(300 * time.Millisecond)
	slices.Sort(w.swarm.observed)
	if !slices.Equal(w.swarm.observed, []string{min(a.ID, b.ID), max(a.ID, b.ID)}) {
		t.Fatalf("observed: %v", w.swarm.observed)
	}

	w.swarm.observed = nil
	w.swarm.tasks[a.ID] = []app.SwarmTask{{DesiredState: "running", State: "starting", Revision: 1}}
	w.app.IngestWorkerEvents(ctx, []app.DockerEvent{svcEvent("container", "svc-"+a.ID)}, false)
	w.jobs.advance(500 * time.Millisecond)
	if w.jobs.count("observe:"+a.ID) != 1 {
		t.Fatalf("no settle re-check: %v", w.jobs.keys())
	}
	w.jobs.advance(time.Second)
	w.app.IngestWorkerEvents(ctx, []app.DockerEvent{svcEvent("container", "svc-"+a.ID)}, false)
	w.jobs.advance(500 * time.Millisecond)
	if len(w.swarm.observed) != 2 {
		t.Fatalf("sooner scan did not run: %v", w.swarm.observed)
	}
	w.jobs.advance(10 * time.Second)
	if len(w.swarm.observed) != 4 {
		t.Fatalf("scans: %d %v", len(w.swarm.observed), w.jobs.keys())
	}

	w.app.IngestWorkerEvents(ctx, nil, true)
	w.app.IngestWorkerEvents(ctx, nil, true)
	if w.jobs.count("observe:all") != 1 {
		t.Fatalf("resync: %v", w.jobs.keys())
	}
}

func TestObservePublishesOnlyChanges(t *testing.T) {
	w := newWorld(t)
	a := w.addNode("api")
	w.start()
	w.ship(app.ShipOptions{})
	w.flush()
	w.jobs.advance(time.Second)
	w.flush()
	w.pub.reset()
	w.app.ScheduleObserve(a.ID)
	w.jobs.advance(time.Second)
	if len(w.pub.topics) != 0 {
		t.Fatalf("an unchanged scan published %v", w.pub.topics)
	}
	w.swarm.tasks[a.ID] = []app.SwarmTask{{DesiredState: "running", State: "failed", Err: "oom", Revision: 1}}
	w.app.ScheduleObserve(a.ID)
	w.jobs.advance(time.Second)
	if !w.pub.has("org", "/api/environments/env") {
		t.Fatalf("a changed scan did not publish: %v", w.pub.topics)
	}

	w.pub.reset()
	w.swarm.ready = 2
	w.app.IngestWorkerEvents(ctx, []app.DockerEvent{{Type: "node"}}, false)
	w.jobs.run()
	if !w.pub.has("org", "/api/environments") || !w.pub.has("org2", "/api/environments") {
		t.Fatalf("servers: %v", w.pub.topics)
	}
	w.pub.reset()
	w.app.IngestWorkerEvents(ctx, []app.DockerEvent{{Type: "node"}}, false)
	w.jobs.run()
	if len(w.pub.topics) != 0 {
		t.Fatalf("unchanged servers published %v", w.pub.topics)
	}
	var servers int
	_ = w.store.Read(ctx, func(tx app.Tx) (err error) { servers, err = tx.ClusterServers(); return })
	if servers != 2 {
		t.Fatalf("servers = %d", servers)
	}
}

func TestReconcileOnlyTheAffectedEnvironment(t *testing.T) {
	w := newWorld(t)
	a := w.addNode("api")
	w.addNode("other", inEnv("env2"))
	w.start()
	d1 := w.ship(app.ShipOptions{})
	d2, err := w.app.ShipEnvironment(ctx, w.outsider, "env2", app.ShipOptions{})
	if err != nil {
		t.Fatal(err)
	}
	w.flush()
	w.pub.reset()
	w.clock.Add(500)
	w.jobs.runOne(t, "observe:"+a.ID)
	w.flush()
	if w.deployment(d1).Status != domain.DeploymentSuccess || w.deployment(d2).Status != domain.DeploymentRunning {
		t.Fatalf("d1 %s d2 %s", w.deployment(d1).Status, w.deployment(d2).Status)
	}
	if len(w.pub.topics["org2"]) != 0 {
		t.Fatalf("org2 was told: %v", w.pub.topics)
	}
	if r := w.run("deploy:" + d2); r.Status != durable.StatusWaiting {
		t.Fatalf("the other environment's deploy run was woken: %+v", r)
	}
}

func TestUpdateSettlesWithoutEvents(t *testing.T) {
	w := newWorld(t)
	a := w.addNode("api")
	w.start()
	w.ship(app.ShipOptions{})
	w.flush()
	w.jobs.advance(time.Second)
	w.flush()
	w.swarm.updateState[a.ID] = "updating"
	id := w.ship(app.ShipOptions{Only: []string{a.ID}})
	w.flush()
	w.jobs.advance(10 * time.Second)
	if d := w.deployment(id); d.Status != domain.DeploymentRunning {
		t.Fatalf("settled while Swarm still updates: %s", d.Status)
	}
	w.swarm.updateState[a.ID] = "completed"
	w.jobs.advance(3 * time.Second)
	if d := w.deployment(id); d.Status != domain.DeploymentSuccess {
		t.Fatalf("after Swarm completed: %s %s", d.Status, stepStatuses(d))
	}
	n := len(w.swarm.observed)
	w.jobs.advance(30 * time.Second)
	if len(w.swarm.observed) != n {
		t.Fatalf("kept polling after completion: %d → %d", n, len(w.swarm.observed))
	}
}

func TestStalledPullYieldsToNewerRevision(t *testing.T) {
	w := newWorld(t)
	a := w.addNode("api", image("slow:1"))
	d1 := w.ship(app.ShipOptions{})
	shipped := make(chan string, 1)
	var once sync.Once
	w.swarm.onPull = func(string) {
		once.Do(func() {
			w.failRunning(d1)
			shipped <- w.ship(app.ShipOptions{Only: []string{a.ID}, Refresh: true})
		})
	}
	w.swarm.blockedPulls = 1
	w.start()
	w.flush()
	d2 := <-shipped
	if got := logTexts(w.deployment(d1)); got[len(got)-1] != "superseded by revision 2" {
		t.Fatalf("d1 log: %q", got)
	}
	if n := w.node(a.ID); n.ApplyError != "" {
		t.Fatalf("superseded apply marked the node: %q", n.ApplyError)
	}
	if len(w.swarm.creates) != 1 || w.swarm.creates[0].Revision != 2 {
		t.Fatalf("creates: %+v", w.swarm.creates)
	}
	if s := w.deployment(d2).Steps[0]; s.AppliedAt == 0 {
		t.Fatalf("d2 step: %+v", s)
	}
}
