package app_test

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

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
	w.addNode("data", kind(domain.NodeVolume))               // dirty never matters: not deployable
	c := w.addNode("cache", clean, shipped(4))               // not dirty: Ship leaves it
	b := w.addNode("worker", image("ghcr.io/acme/worker:1")) // created after api
	w.addNode("elsewhere", inEnv("env2"))

	id := w.ship(app.ShipOptions{})
	d := w.deployment(id)
	if d.Message != "ship api, worker" || d.Status != domain.DeploymentRunning || d.StartedAt != *w.clock || d.FinishedAt != nil {
		t.Fatalf("deployment: %+v", d)
	}
	if got := stepStatuses(d); got != "api=pending worker=pending health checks=pending" {
		t.Fatalf("steps: %s", got)
	}
	if d.Steps[0].NodeID != a.ID || d.Steps[1].NodeID != b.ID || d.Steps[2].NodeID != "" || len(d.Log) != 0 {
		t.Fatalf("steps: %+v log %v", d.Steps, d.Log)
	}
	for _, n := range []domain.Node{w.node(a.ID), w.node(b.ID)} {
		if n.Desired.Revision != 1 || n.Dirty || n.ShippedAt == nil || *n.ShippedAt != *w.clock || n.ApplyError != "" {
			t.Fatalf("shipped node: %+v", n)
		}
	}
	if n := w.node(c.ID); n.Desired.Revision != 4 || n.ShippedAt != nil {
		t.Fatalf("clean node shipped: %+v", n)
	}
	if !w.pub.has("org", "/api/environments/env") || !w.pub.has("org", "/api/deployments/"+id) || !w.pub.has("org", "/api/nodes/"+a.ID) {
		t.Fatalf("topics: %v", w.pub.topics)
	}
	if w.jobs.count("apply:") != 2 || w.jobs.count("timeout:"+id) != 1 {
		t.Fatalf("jobs: %v", w.jobs.keys())
	}

	// One running deployment per environment; the refused ship changes nothing.
	w.addNode("late")
	if _, err := w.app.ShipEnvironment(ctx, w.member, "env", app.ShipOptions{}); codeAndMessage(err) != "DEPLOYMENT_RUNNING: A deployment is already running" {
		t.Fatalf("second ship: %v", codeAndMessage(err))
	}
	if n := w.node(a.ID); n.Desired.Revision != 1 {
		t.Fatalf("refused ship bumped a revision: %+v", n.Desired)
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
		{"unknown and non-deployable ids are ignored", func(a, vol domain.Node) app.ShipOptions {
			return app.ShipOptions{Only: []string{"nope", vol.ID, a.ID}}
		}, "deploy api", ""},
		{"an empty list is still a list", func(domain.Node, domain.Node) app.ShipOptions { return app.ShipOptions{Only: []string{}} }, "", "NOTHING_TO_SHIP: Nothing to ship"},
		{"only foreign ids", func(domain.Node, domain.Node) app.ShipOptions { return app.ShipOptions{Only: []string{"x"}} }, "", "NOTHING_TO_SHIP: Nothing to ship"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			w := newWorld(t)
			a := w.addNode("api", clean, shipped(2)) // clean: only ships it regardless
			w.addNode("other")                       // dirty, but not listed
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

func TestBeginDeploymentVerb(t *testing.T) {
	w := newWorld(t)
	a := w.addNode("api", shipped(1), replicas(0))
	id, err := w.app.BeginDeploymentForTest(ctx, "env", app.ShipOptions{Only: []string{a.ID}, Verb: "stop"})
	if err != nil {
		t.Fatal(err)
	}
	if d := w.deployment(id); d.Message != "stop api" {
		t.Fatalf("message %q", d.Message)
	}
}

// TestOrganizationIsolation: another organization's member sees nothing and changes nothing;
// signed out is the same as foreign for reads.
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
		if ds, err := w.app.ListNodeDeployments(ctx, actor, a.ID); err != nil || ds == nil || len(ds) != 0 {
			t.Errorf("%+v ListNodeDeployments: %v %v", actor, ds, err)
		}
	}
	if _, err := w.app.ShipEnvironment(ctx, w.outsider, "env", app.ShipOptions{Only: []string{a.ID}}); codeAndMessage(err) != "PROJECT_NOT_FOUND: Environment not found" {
		t.Errorf("outsider ship: %v", codeAndMessage(err))
	}
	if _, err := w.app.ShipEnvironment(ctx, w.member, "nope", app.ShipOptions{}); codeAndMessage(err) != "PROJECT_NOT_FOUND: Environment not found" {
		t.Errorf("missing environment: %v", codeAndMessage(err))
	}
	if _, err := w.app.ShipEnvironment(ctx, domain.Actor{}, "env", app.ShipOptions{}); domain.CodeOf(err) != domain.CodeNotAuthenticated {
		t.Errorf("signed-out ship: %v", codeAndMessage(err))
	}
	// The outsider's own environment does not leak ours either.
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

	// 25 deployments of api and 30 of worker, alternating blocks; each fails right away.
	var apiIDs, workerIDs []string
	for i := 0; i < 55; i++ {
		target, ids := a, &apiIDs
		if i >= 25 {
			target, ids = b, &workerIDs
		}
		id := w.ship(app.ShipOptions{Only: []string{target.ID}})
		*ids = append(*ids, id)
		w.app.TimeoutDeploymentForTest(ctx, id)
		*w.clock++
	}
	latest, err := w.app.LatestDeployment(ctx, w.member, "env")
	if err != nil || latest == nil || latest.ID != workerIDs[29] {
		t.Fatalf("latest: %+v %v", latest, err)
	}
	if len(latest.Log) == 0 || latest.Log[0].Text != "worker: timed out waiting for replicas" {
		t.Fatalf("latest log: %+v", latest.Log)
	}
	// worker: the 20 newest of its 30.
	ds, _ := w.app.ListNodeDeployments(ctx, w.member, b.ID)
	if len(ds) != 20 || ds[0].ID != workerIDs[29] || ds[19].ID != workerIDs[10] || len(ds[0].Log) == 0 {
		t.Fatalf("worker list: %d", len(ds))
	}
	// api: its deployments are older than the 50 newest except 20 of them.
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
	w.envVars[a.ID] = map[string]string{"PORT": "8080", "DATABASE_URL": "postgres://x"}
	*w.clock += 1
	id := w.ship(app.ShipOptions{})
	w.jobs.run() // apply
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
	if s := d.Steps[0]; s.Status != domain.StepRunning || s.StartedAt == nil || s.AppliedAt == nil {
		t.Fatalf("step: %+v", s)
	}
	if !reflect.DeepEqual(w.follows, []int{8080}) {
		t.Fatalf("followPort: %v", w.follows)
	}
	// The scan apply scheduled lands 500 ms later and settles the deployment.
	w.jobs.advance(499 * time.Millisecond)
	if len(w.swarm.observed) != 0 {
		t.Fatalf("observed too early")
	}
	w.jobs.advance(time.Millisecond)
	d = w.deployment(id)
	if d.Status != domain.DeploymentSuccess || d.FinishedAt == nil || stepStatuses(d) != "api=done health checks=done" {
		t.Fatalf("after observe: %s %s", d.Status, stepStatuses(d))
	}
	if got := logTexts(d)[3:]; !reflect.DeepEqual(got, []string{"api: 1/1 replicas running", "all replicas healthy"}) {
		t.Fatalf("log: %q", got)
	}
	n := w.node(a.ID)
	if n.DeployedRevision == nil || *n.DeployedRevision != 1 || n.Observed == nil || domain.DeriveStatus(n) != domain.StatusHealthy {
		t.Fatalf("node: %+v", n)
	}

	// Redeploy: cached, but refresh pulls again; the service is updated.
	w.pub.reset()
	id = w.ship(app.ShipOptions{Only: []string{a.ID}, Refresh: true})
	w.jobs.advance(time.Second)
	d = w.deployment(id)
	if got := logTexts(d); len(got) < 3 || got[0] != "pulling nginx:alpine" || got[2] != "service updated · revision 2" || d.Status != domain.DeploymentSuccess {
		t.Fatalf("redeploy log: %q %s", got, d.Status)
	}
	// Restart: cached and no refresh.
	id = w.ship(app.ShipOptions{Only: []string{a.ID}})
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
		id := w.ship(app.ShipOptions{})
		w.jobs.runOne(t, "apply:"+a.ID)
		d := w.deployment(id)
		if d.Status != domain.DeploymentFailed || stepStatuses(d) != "api=failed worker=pending health checks=failed" {
			t.Fatalf("deployment: %s %s", d.Status, stepStatuses(d))
		}
		if got := logTexts(d); got[1] != "error: Error response from daemon: pull access denied for nope" {
			t.Fatalf("log: %q", got)
		}
		if n := w.node(a.ID); n.ApplyError != "Error response from daemon: pull access denied for nope" || domain.DeriveStatus(n) != domain.StatusError {
			t.Fatalf("node: %+v", n)
		}
		// The sibling still applies into the failed deployment (the Convex behaviour).
		w.jobs.run()
		if d := w.deployment(id); d.Steps[1].AppliedAt == nil || d.Status != domain.DeploymentFailed {
			t.Fatalf("sibling: %+v", d.Steps[1])
		}
		if len(w.swarm.creates) != 1 || w.swarm.creates[0].NodeID != b.ID {
			t.Fatalf("creates: %+v", w.swarm.creates)
		}
	})
	t.Run("registry down, image cached", func(t *testing.T) {
		w := newWorld(t)
		a := w.addNode("api")
		w.swarm.cached["nginx:alpine"] = true
		w.swarm.pullErr["nginx:alpine"] = errors.New("registry unreachable")
		id := w.ship(app.ShipOptions{Only: []string{a.ID}, Refresh: true})
		w.jobs.run()
		got := logTexts(w.deployment(id))
		if got[1] != "pull failed (registry unreachable), using cached image" || got[2] != "service created · 1 replica(s)" {
			t.Fatalf("log: %q", got)
		}
	})
	t.Run("update out of sequence is retried, three attempts in all", func(t *testing.T) {
		w := newWorld(t)
		a := w.addNode("api")
		w.ship(app.ShipOptions{})
		w.jobs.advance(time.Second)
		w.swarm.updateFailures = 2
		id := w.ship(app.ShipOptions{Only: []string{a.ID}})
		w.jobs.run()
		if got := logTexts(w.deployment(id)); got[1] != "service updated · revision 2" {
			t.Fatalf("log: %q", got)
		}
		w.jobs.advance(time.Second)
		w.swarm.updateFailures = 3
		id = w.ship(app.ShipOptions{Only: []string{a.ID}})
		w.jobs.run()
		if got := logTexts(w.deployment(id)); got[1] != "error: Error response from daemon: rpc error: update out of sequence" {
			t.Fatalf("log: %q", got)
		}
	})
	t.Run("port moves endpoints and syncs the proxy", func(t *testing.T) {
		w := newWorld(t)
		w.addNode("api", port(9999))
		w.ship(app.ShipOptions{})
		w.jobs.run()
		if w.syncs != 1 {
			t.Fatalf("proxy syncs: %d", w.syncs)
		}
	})
}

// TestApplySerializedAndSkipsStaleRevision: a node's applies run one after the other; an apply
// overtaken by a newer ship of its node never reaches createOrUpdate.
func TestApplySerializedAndSkipsStaleRevision(t *testing.T) {
	t.Run("queued behind: skipped before pulling", func(t *testing.T) {
		w := newWorld(t)
		a := w.addNode("api", image("broken:1"))
		x := w.addNode("x")
		w.swarm.pullErr["broken:1"] = errors.New("not found")
		d1 := w.ship(app.ShipOptions{})
		w.jobs.runOne(t, "apply:"+a.ID) // fails d1: a new ship is allowed while x's apply waits
		d2 := w.ship(app.ShipOptions{Only: []string{x.ID}})
		if n := w.jobs.count("apply:" + x.ID); n != 1 {
			t.Fatalf("x has %d apply jobs, want the one queue: %v", n, w.jobs.keys())
		}
		w.jobs.run()
		if len(w.swarm.creates) != 1 || w.swarm.creates[0].Revision != 2 || len(w.swarm.pulls) != 2 {
			t.Fatalf("creates %+v pulls %v", w.swarm.creates, w.swarm.pulls)
		}
		if got := logTexts(w.deployment(d1)); got[len(got)-1] != "superseded by revision 2" {
			t.Fatalf("d1 log: %q", got)
		}
		if s := w.deployment(d2).Steps[0]; s.AppliedAt == nil {
			t.Fatalf("d2 step: %+v", s)
		}
	})
	t.Run("shipped again during the pull: skipped before createOrUpdate", func(t *testing.T) {
		w := newWorld(t)
		a := w.addNode("api", image("broken:1"))
		x := w.addNode("x", image("slow:1"))
		w.swarm.pullErr["broken:1"] = errors.New("not found")
		d1 := w.ship(app.ShipOptions{})
		w.jobs.runOne(t, "apply:"+a.ID)
		var d2 string
		w.swarm.onPull = func(img string) {
			if img == "slow:1" && d2 == "" {
				d2 = w.ship(app.ShipOptions{Only: []string{x.ID}})
				if n := w.jobs.count("apply:" + x.ID); n != 0 {
					t.Errorf("a second drain was scheduled while one runs: %v", w.jobs.keys())
				}
			}
		}
		w.jobs.run()
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
		w.ship(app.ShipOptions{})
		w.jobs.run()
		if len(w.swarm.creates) != 0 {
			t.Fatalf("created a service for a deleted node")
		}
	})
	t.Run("between the check and the create", func(t *testing.T) {
		w := newWorld(t)
		a := w.addNode("api")
		w.swarm.onCreate = func(app.ServiceSpec) { w.deleteNode(a.ID) }
		id := w.ship(app.ShipOptions{})
		w.jobs.run()
		if !reflect.DeepEqual(w.swarm.removed, []string{a.ID}) {
			t.Fatalf("orphan not taken back: %v", w.swarm.removed)
		}
		if d := w.deployment(id); d.Steps[0].AppliedAt != nil {
			t.Fatalf("step applied: %+v", d.Steps[0])
		}
	})
	t.Run("delete settles the step", func(t *testing.T) {
		w := newWorld(t)
		a := w.addNode("api")
		id := w.ship(app.ShipOptions{})
		w.deleteNode(a.ID)
		w.app.ScheduleRemoveService(a.ID)
		w.jobs.runOne(t, "remove:"+a.ID)
		d := w.deployment(id)
		if d.Status != domain.DeploymentFailed || !reflect.DeepEqual(logTexts(d), []string{"api: node deleted"}) {
			t.Fatalf("deployment: %s %q", d.Status, logTexts(d))
		}
		if !reflect.DeepEqual(w.swarm.removed, []string{a.ID}) {
			t.Fatalf("removed: %v", w.swarm.removed)
		}
		// ScheduleObserve of a gone node settles too (and observes nothing).
		w.app.ScheduleObserve(a.ID)
		if w.jobs.count("reconcile") != 1 || w.jobs.count("observe:") != 0 {
			t.Fatalf("jobs: %v", w.jobs.keys())
		}
	})
}

func TestDeploymentTimeout(t *testing.T) {
	w := newWorld(t)
	a := w.addNode("api")
	b := w.addNode("worker")
	w.swarm.tasks[a.ID] = []app.SwarmTask{
		{DesiredState: "shutdown", State: "rejected", Err: "no suitable node", Labels: map[string]string{"keel.revision": "1"}},
		{DesiredState: "running", State: "pending", Labels: map[string]string{"keel.revision": "1"}},
	}
	id := w.ship(app.ShipOptions{})
	w.jobs.advance(4*time.Minute + 59*time.Second)
	d := w.deployment(id)
	if d.Status != domain.DeploymentRunning || stepStatuses(d) != "api=running worker=done health checks=running" {
		t.Fatalf("before the timeout: %s %s", d.Status, stepStatuses(d))
	}
	w.deleteNode(b.ID) // a done step stays done; deletion only matters for unfinished steps
	w.pub.reset()
	w.jobs.advance(time.Second)
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
	// Late writes never revive it.
	w.app.TimeoutDeploymentForTest(ctx, id)
	if d2 := w.deployment(id); !reflect.DeepEqual(d2, d) {
		t.Fatalf("second timeout changed it")
	}
}

func TestRecover(t *testing.T) {
	w := newWorld(t)
	a := w.addNode("api")
	b := w.addNode("worker", inEnv("env2"))
	old := w.ship(app.ShipOptions{})
	*w.clock += 4 * 60_000 // 4 minutes later
	fresh, err := w.app.ShipEnvironment(ctx, w.outsider, "env2", app.ShipOptions{})
	if err != nil {
		t.Fatal(err)
	}
	// A restart loses every in-memory job.
	*w.clock += 2 * 60_000 // the old deployment is a minute overdue
	w.restart(app.Config{AgentImage: "ghcr.io/thallesp/keel:1.2.3", SiteURL: "http://100.64.0.1:8080", WorkerToken: "secret"})
	w.app.Recover(ctx)

	w.jobs.run()
	if d := w.deployment(old); d.Status != domain.DeploymentFailed {
		t.Fatalf("overdue deployment: %s %s", d.Status, stepStatuses(d))
	}
	if !reflect.DeepEqual(w.swarm.agents, []app.AgentSpec{{Image: "ghcr.io/thallesp/keel:1.2.3", ControlURL: "http://100.64.0.1:8080", Token: "secret"}}) {
		t.Fatalf("agent: %+v", w.swarm.agents)
	}
	if w.swarm.observed[0] != "*" || w.swarm.tunnelSweeps != 1 {
		t.Fatalf("no sweep: %v, tunnels %d", w.swarm.observed, w.swarm.tunnelSweeps)
	}
	// The pending applies of both deployments were re-queued.
	if len(w.swarm.creates) != 2 {
		t.Fatalf("creates: %+v", w.swarm.creates)
	}
	_ = a
	// The fresh one healed through the scan; give it a pending task so only the timer ends it.
	w.swarm.tasks[b.ID] = []app.SwarmTask{{DesiredState: "running", State: "pending", Labels: map[string]string{"keel.revision": "1"}}}
	w.app.ScheduleObserve(b.ID)
	w.jobs.advance(time.Second)
	if d := w.deployment(fresh); d.Status != domain.DeploymentRunning {
		t.Fatalf("fresh: %s %s", d.Status, stepStatuses(d))
	}
	w.jobs.advance(2*time.Minute + 57*time.Second) // started 2 min before the restart: 3 min left
	if d := w.deployment(fresh); d.Status != domain.DeploymentRunning {
		t.Fatalf("fresh timed out early")
	}
	w.jobs.advance(2 * time.Second)
	if d := w.deployment(fresh); d.Status != domain.DeploymentFailed {
		t.Fatalf("fresh not timed out: %s", d.Status)
	}
}

func TestIngestEventsDebounce(t *testing.T) {
	w := newWorld(t)
	a := w.addNode("api")
	b := w.addNode("worker")
	w.addNode("draft", noDesired)
	w.ship(app.ShipOptions{})
	w.jobs.run()
	w.jobs.advance(time.Second)
	w.swarm.observed = nil

	svcEvent := func(typ, name string) app.DockerEvent {
		if typ == "container" {
			return app.DockerEvent{Type: typ, Action: "start", Name: name + ".1.abc", ServiceName: name}
		}
		return app.DockerEvent{Type: typ, Action: "update", Name: name}
	}
	w.app.IngestWorkerEvents(ctx, []app.DockerEvent{
		svcEvent("container", "svc-"+a.ID),
		svcEvent("service", "svc-"+a.ID), // same node, same batch: once
		svcEvent("container", "svc-"+b.ID),
		svcEvent("container", "svc-unknown"),
		svcEvent("container", "postgres"), // a user's own container
		{Type: "network", Action: "connect"},
		{Type: "node", Action: "update"},
		{Type: "node", Action: "update"},
	}, false)
	if w.jobs.count("observe:servers") != 1 || w.jobs.count("observe:"+a.ID) != 1 || w.jobs.count("observe:"+b.ID) != 1 || w.jobs.count("observe:all") != 0 {
		t.Fatalf("jobs: %v", w.jobs.keys())
	}
	// Another burst inside the debounce window coalesces.
	w.jobs.advance(200 * time.Millisecond)
	w.app.IngestWorkerEvents(ctx, []app.DockerEvent{svcEvent("container", "svc-"+a.ID)}, false)
	if w.jobs.count("observe:"+a.ID) != 1 {
		t.Fatalf("not coalesced: %v", w.jobs.keys())
	}
	w.jobs.advance(300 * time.Millisecond)
	if got := sortedCopy(w.swarm.observed); !reflect.DeepEqual(got, sortedCopy([]string{a.ID, b.ID})) {
		t.Fatalf("observed: %v", w.swarm.observed)
	}

	// A task mid-transition: re-checked 2 s later, at most twice.
	w.swarm.observed = nil
	w.swarm.tasks[a.ID] = []app.SwarmTask{{DesiredState: "running", State: "starting", Labels: map[string]string{"keel.revision": "1"}}}
	w.app.IngestWorkerEvents(ctx, []app.DockerEvent{svcEvent("container", "svc-"+a.ID)}, false)
	w.jobs.advance(500 * time.Millisecond)
	if w.jobs.count("observe:"+a.ID) != 1 {
		t.Fatalf("no settle re-check: %v", w.jobs.keys())
	}
	// An event replaces the later re-check with a sooner scan (and the chain starts over).
	w.jobs.advance(time.Second)
	w.app.IngestWorkerEvents(ctx, []app.DockerEvent{svcEvent("container", "svc-"+a.ID)}, false)
	w.jobs.advance(500 * time.Millisecond)
	if len(w.swarm.observed) != 2 {
		t.Fatalf("sooner scan did not run: %v", w.swarm.observed)
	}
	w.jobs.advance(10 * time.Second)
	if len(w.swarm.observed) != 4 { // event scan + 2 re-checks; the replaced one never ran
		t.Fatalf("scans: %d %v", len(w.swarm.observed), w.jobs.keys())
	}

	// Resync: one full sweep (coalesced).
	w.app.IngestWorkerEvents(ctx, nil, true)
	w.app.IngestWorkerEvents(ctx, nil, true)
	if w.jobs.count("observe:all") != 1 {
		t.Fatalf("resync: %v", w.jobs.keys())
	}
}

func TestObservePublishesOnlyChanges(t *testing.T) {
	w := newWorld(t)
	a := w.addNode("api")
	w.ship(app.ShipOptions{})
	w.jobs.advance(time.Second)
	w.pub.reset()
	w.app.ScheduleObserve(a.ID)
	w.jobs.advance(time.Second)
	if len(w.pub.topics) != 0 {
		t.Fatalf("an unchanged scan published %v", w.pub.topics)
	}
	w.swarm.tasks[a.ID] = []app.SwarmTask{{DesiredState: "running", State: "failed", Err: "oom", Labels: map[string]string{"keel.revision": "1"}}}
	w.app.ScheduleObserve(a.ID)
	w.jobs.advance(time.Second)
	if !w.pub.has("org", "/api/environments/env") {
		t.Fatalf("a changed scan did not publish: %v", w.pub.topics)
	}

	// Servers: the cluster row reaches every organization, only when the count changes.
	w.pub.reset()
	w.swarm.ready, w.swarm.total = 2, 3
	w.app.IngestWorkerEvents(ctx, []app.DockerEvent{{Type: "node", Action: "update"}}, false)
	w.jobs.run()
	if !w.pub.has("org", "/api/environments") || !w.pub.has("org2", "/api/environments") {
		t.Fatalf("servers: %v", w.pub.topics)
	}
	w.pub.reset()
	w.app.IngestWorkerEvents(ctx, []app.DockerEvent{{Type: "node", Action: "update"}}, false)
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
	b := w.addNode("other", inEnv("env2"))
	d1 := w.ship(app.ShipOptions{})
	d2, err := w.app.ShipEnvironment(ctx, w.outsider, "env2", app.ShipOptions{})
	if err != nil {
		t.Fatal(err)
	}
	w.jobs.run() // both applied; observes pending
	w.pub.reset()
	*w.clock += 500
	w.jobs.runOne(t, "observe:"+a.ID)
	if w.deployment(d1).Status != domain.DeploymentSuccess || w.deployment(d2).Status != domain.DeploymentRunning {
		t.Fatalf("d1 %s d2 %s", w.deployment(d1).Status, w.deployment(d2).Status)
	}
	if len(w.pub.topics["org2"]) != 0 {
		t.Fatalf("org2 was told: %v", w.pub.topics)
	}
	_ = b
}

// Without keel agent there are no Docker events: a rolling update that outlasts the settle
// re-checks is polled until Swarm reports it done, so the deployment still settles.
func TestUpdateSettlesWithoutEvents(t *testing.T) {
	w := newWorld(t)
	a := w.addNode("api")
	w.ship(app.ShipOptions{})
	w.jobs.advance(time.Second)
	w.swarm.updateState[a.ID] = "updating"
	id := w.ship(app.ShipOptions{Only: []string{a.ID}})
	w.jobs.advance(10 * time.Second) // well past the two settle re-checks
	if d := w.deployment(id); d.Status != domain.DeploymentRunning {
		t.Fatalf("settled while Swarm still updates: %s", d.Status)
	}
	w.swarm.updateState[a.ID] = "completed"
	w.jobs.advance(3 * time.Second)
	if d := w.deployment(id); d.Status != domain.DeploymentSuccess {
		t.Fatalf("after Swarm completed: %s %s", d.Status, stepStatuses(d))
	}
	// And the polling stops once done.
	n := len(w.swarm.observed)
	w.jobs.advance(30 * time.Second)
	if len(w.swarm.observed) != n {
		t.Fatalf("kept polling after completion: %d → %d", n, len(w.swarm.observed))
	}
}

// A pull that stalls must not hold the node's retry back: queueing a newer revision cancels the
// running apply, which backs off as superseded without marking the node errored.
func TestStalledPullYieldsToNewerRevision(t *testing.T) {
	w := newWorld(t)
	a := w.addNode("api", image("slow:1"))
	d1 := w.ship(app.ShipOptions{})
	var d2 string
	w.swarm.onPull = func(img string) {
		if d2 == "" {
			// The deployment timed out meanwhile; the user retries while the pull still hangs.
			w.failRunning(d1)
			d2 = w.ship(app.ShipOptions{Only: []string{a.ID}, Refresh: true})
		}
	}
	w.swarm.pullBlocksUntilCancelled = true
	w.jobs.run()
	if got := logTexts(w.deployment(d1)); got[len(got)-1] != "superseded by revision 2" {
		t.Fatalf("d1 log: %q", got)
	}
	if n := w.node(a.ID); n.ApplyError != "" {
		t.Fatalf("superseded apply marked the node: %q", n.ApplyError)
	}
	if len(w.swarm.creates) != 1 || w.swarm.creates[0].Revision != 2 {
		t.Fatalf("creates: %+v", w.swarm.creates)
	}
	if s := w.deployment(d2).Steps[0]; s.AppliedAt == nil {
		t.Fatalf("d2 step: %+v", s)
	}
}
