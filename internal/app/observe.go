package app

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/ThallesP/keel/internal/domain"
)

// Observation: Swarm → nodes.observed (convex/swarm.ts observeNode/observe/observeSwarmNodes,
// nodesInternal.ts setObserved/scheduleObserve; docs/go/spec/swarm-worker.md §8).

const (
	deployLabelService  = "keel.service"
	deployLabelRevision = "keel.revision"
)

// transientTaskStates: between "scheduled" and "running"; a container event follows within
// seconds. `pending` (nothing can schedule it) is deliberately absent: that is the timeout's job.
var transientTaskStates = map[string]bool{
	"new": true, "allocated": true, "assigned": true, "accepted": true,
	"preparing": true, "ready": true, "starting": true,
}

// taskRevision is JavaScript's Number(labels["keel.revision"]): ok=false when the label is
// missing or not a number (NaN never equals a revision). An empty label is 0, as in JS.
func taskRevision(labels map[string]string) (int, bool) {
	v, present := labels[deployLabelRevision]
	if !present {
		return 0, false
	}
	v = strings.TrimFunc(v, deployIsJSSpace)
	if v == "" {
		return 0, true
	}
	n, err := strconv.ParseFloat(v, 64)
	if err != nil || n != float64(int(n)) {
		return 0, false
	}
	return int(n), true
}

// summarizeTasks is swarm.ts summarize, exactly: the observed state of a node from its tasks (in
// Docker's order) and its service (nil when there is none).
func summarizeTasks(tasks []SwarmTask, svc *SwarmService, now int64) domain.Observed {
	update := ""
	if svc != nil {
		update = svc.UpdateState
	}
	rolledBack := update == "paused" || strings.HasPrefix(update, "rollback")

	// Old revisions linger in task history; only the newest says anything about health. After a
	// rollback the spec reverts but the failed revision's tasks are still newest, so `revision`
	// names what failed and state "failed" says it is not running.
	revision := 0
	if svc != nil {
		if r, ok := taskRevision(svc.Labels); ok {
			revision = r
		}
	}
	for _, t := range tasks {
		if r, ok := taskRevision(t.Labels); ok && r > revision {
			revision = r
		}
	}

	var live, running, failed, completed []SwarmTask
	for _, t := range tasks {
		if r, ok := taskRevision(t.Labels); !ok || r != revision {
			continue
		}
		if t.DesiredState == "running" {
			live = append(live, t)
			if t.State == "running" {
				running = append(running, t)
			}
		}
		if t.State == "failed" || t.State == "rejected" {
			failed = append(failed, t)
		}
		if t.State == "complete" {
			completed = append(completed, t)
		}
	}
	oneShot := len(completed) > 0 && len(live) == 0 && len(failed) == 0

	o := domain.Observed{Revision: revision, Running: len(running), NodeIDs: []string{}, At: now}
	if oneShot {
		n := len(completed)
		var finished int64
		for i, t := range completed {
			if i == 0 || t.Timestamp > finished {
				finished = t.Timestamp
			}
		}
		o.Completed, o.FinishedAt = &n, &finished
	}
	seen := map[string]bool{}
	for _, t := range running {
		if t.NodeID != "" && !seen[t.NodeID] {
			seen[t.NodeID] = true
			o.NodeIDs = append(o.NodeIDs, t.NodeID)
		}
	}
	pending := false
	for _, t := range live {
		if t.State == "pending" {
			pending = true
			break
		}
	}
	switch {
	case rolledBack:
		o.State = domain.ObservedFailed
	case len(failed) >= 5:
		o.State = domain.ObservedCrashloop
	case pending:
		o.State = domain.ObservedPending
	// `updating` until Swarm reports `completed`: a task that dies inside the Monitor window must
	// not count as converged, Swarm is about to roll it back.
	case update == "updating" || len(running) < len(live):
		o.State = domain.ObservedUpdating
	case oneShot:
		o.State = domain.ObservedCompleted
	default:
		o.State = domain.ObservedOK
	}
	if len(failed) > 0 && failed[len(failed)-1].Err != "" {
		o.Error = failed[len(failed)-1].Err
	} else if rolledBack && svc != nil {
		o.Error = svc.UpdateMessage
	}
	return o
}

// settlingTasks: some task of revision is mid-transition (swarm.ts settling).
func settlingTasks(tasks []SwarmTask, revision int) bool {
	for _, t := range tasks {
		if r, ok := taskRevision(t.Labels); ok && r == revision && t.DesiredState != "shutdown" && transientTaskStates[t.State] {
			return true
		}
	}
	return false
}

// ── Debounce (nodesInternal.scheduleObserveFor) ─────────────────────────────────────────────

// ScheduleObserve observes one node's Swarm service soon, debounced per node (500 ms). For a
// node that no longer exists it settles running deployments instead, so a deleted node's steps
// fail with "node deleted". Called by: canvas (node delete), deploy.
func (a *App) ScheduleObserve(nodeID string) {
	ctx := context.Background()
	if _, exists := a.scheduleObserve(ctx, nodeID, observeDebounce, 0); !exists {
		a.Jobs.After("reconcile", 0, func(ctx context.Context) { a.reconcileRunning(ctx, "") })
	}
}

// scheduleObserveFor schedules observeNode(id, settle) after delay unless a scan of the node is
// already due no later; a pending scan due later (a settle re-check) is replaced. Ids that are
// not a node with desired (orphan services, a user's own containers) are ignored. True when a
// scan was scheduled.
func (a *App) scheduleObserveFor(ctx context.Context, id string, delay time.Duration, settle int) bool {
	scheduled, _ := a.scheduleObserve(ctx, id, delay, settle)
	return scheduled
}

func (a *App) scheduleObserve(ctx context.Context, id string, delay time.Duration, settle int) (scheduled, exists bool) {
	err := a.read(ctx, func(tx Tx) error {
		n, err := tx.Node(id)
		if errors.Is(err, ErrNoRow) {
			return nil
		}
		if err != nil {
			return err
		}
		exists = n.Desired != nil
		return nil
	})
	if err != nil {
		a.Log.Error("schedule observe", "node", id, "err", err)
		return false, true
	}
	if !exists {
		return false, false
	}
	rt := a.deployRuntime()
	due := a.Now() + delay.Milliseconds()
	rt.mu.Lock()
	if p, ok := rt.observe[id]; ok && p.due <= due {
		rt.mu.Unlock()
		return false, true
	}
	gen := rt.next()
	rt.observe[id] = pendingScan{due: due, settle: settle, gen: gen}
	rt.mu.Unlock()
	// The generation makes every scan its own job, so a sooner scan is never dropped behind a
	// pending later one; the later one finds the slot taken and does nothing.
	a.Jobs.After(fmt.Sprintf("observe:%s:%d", id, gen), delay, func(ctx context.Context) {
		a.runScheduledObserve(ctx, id, gen)
	})
	return true, true
}

// runScheduledObserve frees the node's slot first (events from now on get their own scan, since
// this scan's Docker read may predate them), then scans.
func (a *App) runScheduledObserve(ctx context.Context, id string, gen uint64) {
	rt := a.deployRuntime()
	rt.mu.Lock()
	p, ok := rt.observe[id]
	if !ok || p.gen != gen {
		rt.mu.Unlock()
		return
	}
	delete(rt.observe, id)
	rt.mu.Unlock()
	a.observeNode(ctx, id, p.settle)
}

// ── Scans ───────────────────────────────────────────────────────────────────────────────────

// observeNode scans one node, records what Swarm reports, settles its environment's running
// deployment, and re-checks (at most twice, 2 s apart) while a task is mid-transition.
func (a *App) observeNode(ctx context.Context, id string, settle int) {
	svc, tasks, err := a.Swarm.ObserveService(ctx, id)
	if err != nil {
		a.Log.Error("observeNode", "node", id, "err", err)
		return
	}
	now := a.Now()
	observed := summarizeTasks(tasks, svc, now)
	update := "-"
	if svc != nil && svc.UpdateState != "" {
		update = svc.UpdateState
	}
	a.Log.Info(fmt.Sprintf("observeNode %s tasks=%d update=%s revision=%d state=%s running=%d settle=%d",
		id, len(tasks), update, observed.Revision, observed.State, observed.Running, settle))
	err = a.write(ctx, func(tx Tx, ch *Changes) error {
		envID, err := a.setObserved(tx, ch, id, observed)
		if err != nil {
			return err
		}
		return a.reconcile(tx, ch, envID, now) // "" (node gone): every running deployment
	})
	if err != nil {
		a.Log.Error("observeNode", "node", id, "err", err)
		return
	}
	if settle < observeSettleMax && settlingTasks(tasks, observed.Revision) {
		a.scheduleObserveFor(ctx, id, observeSettleDelay, settle+1)
	}
}

// observeAll is the full sweep (swarm.ts observe): every shipped node from one listing of
// services and tasks, one reconcile, then the server count. Run at start and on agent resync.
func (a *App) observeAll(ctx context.Context) {
	var nodes []domain.Node
	err := a.read(ctx, func(tx Tx) error {
		all, err := tx.AllNodes()
		for _, n := range all {
			if n.Desired != nil && n.Desired.Revision > 0 {
				nodes = append(nodes, n)
			}
		}
		return err
	})
	if err != nil {
		a.Log.Error("observe (full sweep)", "err", err)
		return
	}
	if len(nodes) > 0 {
		services, tasks, err := a.Swarm.ObserveServices(ctx)
		if err != nil {
			a.Log.Error("observe (full sweep)", "err", err)
			return
		}
		byName := make(map[string]*SwarmService, len(services))
		for i := range services {
			byName[services[i].Name] = &services[i]
		}
		now := a.Now()
		err = a.write(ctx, func(tx Tx, ch *Changes) error {
			for _, n := range nodes {
				var own []SwarmTask
				for _, t := range tasks {
					if t.Labels[deployLabelService] == n.ID {
						own = append(own, t)
					}
				}
				if _, err := a.setObserved(tx, ch, n.ID, summarizeTasks(own, byName[domain.ServicePrefix+n.ID], now)); err != nil {
					return err
				}
			}
			return a.reconcile(tx, ch, "", now)
		})
		if err != nil {
			a.Log.Error("observe (full sweep)", "err", err)
			return
		}
	}
	a.Log.Info(fmt.Sprintf("observe (full sweep) nodes=%d", len(nodes)))
	a.observeServers(ctx)
}

// observeServers counts ready Swarm nodes into the cluster row (environments.setServers). Every
// organization's summaries refetch only when the count changed.
func (a *App) observeServers(ctx context.Context) {
	ready, total, err := a.Swarm.Servers(ctx)
	if err != nil {
		a.Log.Error("observeServers", "err", err)
		return
	}
	a.Log.Info(fmt.Sprintf("observeServers ready=%d/%d", ready, total))
	err = a.write(ctx, func(tx Tx, ch *Changes) error {
		before, err := tx.ClusterServers()
		if err != nil {
			return err
		}
		if err := tx.SetClusterServers(ready, a.Now()); err != nil {
			return err
		}
		if before == ready {
			return nil
		}
		orgs, err := tx.DeployOrganizationIDs()
		for _, org := range orgs {
			ch.Add(org, "/api/environments")
		}
		return err
	})
	if err != nil {
		a.Log.Error("observeServers", "err", err)
	}
}

// setObserved records a scan (nodesInternal.setObserved): observed, deployedRevision when
// converged, oneShot learned from the run. Returns the node's environment, "" when the node is
// gone (a no-op). The canvas refetches only when something it shows changed: observed.at moves
// on every scan.
func (a *App) setObserved(tx Tx, ch *Changes, id string, observed domain.Observed) (string, error) {
	n, err := tx.Node(id)
	if errors.Is(err, ErrNoRow) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	before := observedFace(n)
	next := n
	o := observed
	next.Observed = &o
	if domain.Converged(next.Desired, &o) && o.Revision > 0 {
		r := o.Revision
		next.DeployedRevision = &r
	}
	switch {
	case o.State == domain.ObservedCompleted:
		next.OneShot = true
	case o.Running > 0:
		next.OneShot = false
	}
	if err := tx.UpdateNode(next); err != nil {
		return "", err
	}
	if observedFace(next) != before {
		org, err := deployOrgOf(tx, n.EnvironmentID)
		if err != nil {
			return "", err
		}
		ch.Environment(org, n.EnvironmentID)
	}
	return n.EnvironmentID, nil
}

// nodeFace is what the node view (nodes.list) derives from observed, deployedRevision and
// applyError: comparing two of them tells whether a scan changed anything a client shows.
type nodeFace struct {
	status           domain.NodeStatus
	running          int
	deployedRevision int
	err              string
	step             string
	finishedAt       int64
}

func observedFace(n domain.Node) nodeFace {
	f := nodeFace{status: domain.DeriveStatus(n), deployedRevision: -1}
	if n.DeployedRevision != nil {
		f.deployedRevision = *n.DeployedRevision
	}
	o := n.Observed
	if o != nil {
		f.running = o.Running
	}
	f.err = n.ApplyError
	if f.err == "" && f.status == domain.StatusError && o != nil {
		f.err = o.Error
	}
	if f.status == domain.StatusDeploying && n.Desired != nil {
		switch {
		case o == nil || o.Revision < n.Desired.Revision:
			f.step = "pulling image"
		case o.State == domain.ObservedUpdating:
			f.step = "rolling out"
		default:
			f.step = "starting"
		}
	}
	if f.status == domain.StatusDone && o != nil && o.FinishedAt != nil {
		f.finishedAt = *o.FinishedAt
	}
	return f
}
