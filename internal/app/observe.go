package app

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/ThallesP/keel/internal/domain"
)

var transientTaskStates = map[string]bool{
	"new": true, "allocated": true, "assigned": true, "accepted": true,
	"preparing": true, "ready": true, "starting": true,
}

func taskRevision(labels map[string]string) (int, bool) {
	r, err := strconv.Atoi(labels["keel.revision"])
	if err != nil {
		return 0, false
	}
	return r, true
}

func summarizeTasks(tasks []SwarmTask, svc SwarmService, now int64) domain.Observed {
	rolledBack := svc.UpdateState == "paused" || strings.HasPrefix(svc.UpdateState, "rollback")
	revision, _ := taskRevision(svc.Labels)
	for _, t := range tasks {
		if r, ok := taskRevision(t.Labels); ok {
			revision = max(revision, r)
		}
	}

	o := domain.Observed{Revision: revision, At: now}
	var live, failed, completed int
	var pending bool
	var finishedAt int64
	for _, t := range tasks {
		if r, ok := taskRevision(t.Labels); !ok || r != revision {
			continue
		}
		if t.DesiredState == "running" {
			live++
			pending = pending || t.State == "pending"
		}
		if t.DesiredState == "running" && t.State == "running" {
			o.Running++
			if t.NodeID != "" && !slices.Contains(o.NodeIDs, t.NodeID) {
				o.NodeIDs = append(o.NodeIDs, t.NodeID)
			}
		}
		switch t.State {
		case "failed", "rejected":
			failed++
			o.Error = t.Err
		case "complete":
			completed++
			finishedAt = max(finishedAt, t.Timestamp)
		}
	}
	if rolledBack && o.Error == "" {
		o.Error = svc.UpdateMessage
	}
	oneShot := completed > 0 && live == 0 && failed == 0
	if oneShot {
		o.Completed, o.FinishedAt = &completed, &finishedAt
	}
	switch {
	case rolledBack:
		o.State = domain.ObservedFailed
	case failed >= 5:
		o.State = domain.ObservedCrashloop
	case pending:
		o.State = domain.ObservedPending
	case svc.UpdateState == "updating" || o.Running < live:
		o.State = domain.ObservedUpdating
	case oneShot:
		o.State = domain.ObservedCompleted
	default:
		o.State = domain.ObservedOK
	}
	return o
}

func settlingTasks(tasks []SwarmTask, revision int) bool {
	return slices.ContainsFunc(tasks, func(t SwarmTask) bool {
		r, ok := taskRevision(t.Labels)
		return ok && r == revision && t.DesiredState != "shutdown" && transientTaskStates[t.State]
	})
}

func (a *App) ScheduleObserve(nodeID string) {
	if _, exists := a.scheduleObserve(context.Background(), nodeID, observeDebounce, 0); !exists {
		a.Jobs.After("reconcile", 0, func(ctx context.Context) { a.reconcileRunning(ctx, "") })
	}
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
	a.Jobs.After(fmt.Sprintf("observe:%s:%d", id, gen), delay, func(ctx context.Context) {
		a.runScheduledObserve(ctx, id, gen)
	})
	return true, true
}

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

func (a *App) observeNode(ctx context.Context, id string, settle int) {
	if a.noSwarm("observeNode") {
		return
	}
	dctx, cancel := context.WithTimeout(ctx, dockerCallDeadline)
	inspected, tasks, err := a.Swarm.ObserveService(dctx, id)
	cancel()
	if err != nil {
		a.Log.Error("observeNode", "node", id, "err", err)
		return
	}
	var svc SwarmService
	if inspected != nil {
		svc = *inspected
	}
	now := a.Now()
	observed := summarizeTasks(tasks, svc, now)
	a.Log.Info("observeNode", "node", id, "tasks", len(tasks), "update", svc.UpdateState, "revision", observed.Revision,
		"state", observed.State, "running", observed.Running, "settle", settle)
	err = a.write(ctx, func(tx Tx, ch *Changes) error {
		envID, err := a.setObserved(tx, ch, id, observed)
		if err != nil {
			return err
		}
		return a.reconcile(tx, ch, envID, now)
	})
	if err != nil {
		a.Log.Error("observeNode", "node", id, "err", err)
		return
	}
	settling := settle < observeSettleMax && settlingTasks(tasks, observed.Revision)
	updating := settle < observeUpdatingMax && (svc.UpdateState == "updating" || svc.UpdateState == "rollback_started")
	if settling || updating {
		a.scheduleObserve(ctx, id, observeSettleDelay, settle+1)
	}
}

func (a *App) observeAll(ctx context.Context) {
	if a.noSwarm("observe (full sweep)") {
		return
	}
	var nodes []domain.Node
	err := a.read(ctx, func(tx Tx) (err error) {
		nodes, err = tx.AllNodes()
		return err
	})
	if err != nil {
		a.Log.Error("observe (full sweep)", "err", err)
		return
	}
	nodes = slices.DeleteFunc(nodes, func(n domain.Node) bool { return n.Desired == nil || n.Desired.Revision == 0 })
	a.Log.Info("observe (full sweep)", "nodes", len(nodes))
	if len(nodes) == 0 {
		a.observeServers(ctx)
		return
	}
	dctx, cancel := context.WithTimeout(ctx, dockerCallDeadline)
	services, tasks, err := a.Swarm.ObserveServices(dctx)
	cancel()
	if err != nil {
		a.Log.Error("observe (full sweep)", "err", err)
		return
	}
	serviceByName := make(map[string]SwarmService, len(services))
	for _, svc := range services {
		serviceByName[svc.Name] = svc
	}
	tasksByNode := map[string][]SwarmTask{}
	for _, t := range tasks {
		id := t.Labels["keel.service"]
		tasksByNode[id] = append(tasksByNode[id], t)
	}
	now := a.Now()
	err = a.write(ctx, func(tx Tx, ch *Changes) error {
		for _, n := range nodes {
			if _, err := a.setObserved(tx, ch, n.ID, summarizeTasks(tasksByNode[n.ID], serviceByName[n.ServiceName()], now)); err != nil {
				return err
			}
		}
		return a.reconcile(tx, ch, "", now)
	})
	if err != nil {
		a.Log.Error("observe (full sweep)", "err", err)
		return
	}
	a.observeServers(ctx)
}

func (a *App) observeServers(ctx context.Context) {
	if a.noSwarm("observeServers") {
		return
	}
	dctx, cancel := context.WithTimeout(ctx, dockerCallDeadline)
	ready, total, err := a.Swarm.Servers(dctx)
	cancel()
	if err != nil {
		a.Log.Error("observeServers", "err", err)
		return
	}
	a.Log.Info("observeServers", "ready", ready, "total", total)
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
		if err != nil {
			return err
		}
		for _, org := range orgs {
			ch.Add(org, "/api/environments")
		}
		return nil
	})
	if err != nil {
		a.Log.Error("observeServers", "err", err)
	}
}

func (a *App) setObserved(tx Tx, ch *Changes, id string, o domain.Observed) (string, error) {
	n, err := tx.Node(id)
	if errors.Is(err, ErrNoRow) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	before := observedFace(n)
	n.Observed = &o
	if domain.Converged(n.Desired, &o) && o.Revision > 0 {
		n.DeployedRevision = new(o.Revision)
	}
	switch {
	case o.State == domain.ObservedCompleted:
		n.OneShot = true
	case o.Running > 0:
		n.OneShot = false
	}
	if err := tx.UpdateNode(n); err != nil {
		return "", err
	}
	if observedFace(n) != before {
		org, err := deployOrgOf(tx, n.EnvironmentID)
		if err != nil {
			return "", err
		}
		ch.Environment(org, n.EnvironmentID)
	}
	return n.EnvironmentID, nil
}

type nodeFace struct {
	status           domain.NodeStatus
	running          int
	deployedRevision int
	err              string
	step             string
	finishedAt       int64
}

func observedFace(n domain.Node) nodeFace {
	var o domain.Observed
	if n.Observed != nil {
		o = *n.Observed
	}
	f := nodeFace{status: domain.DeriveStatus(n), running: o.Running, err: n.ApplyError}
	if n.DeployedRevision != nil {
		f.deployedRevision = *n.DeployedRevision
	}
	if f.err == "" && f.status == domain.StatusError {
		f.err = o.Error
	}
	if f.status == domain.StatusDeploying {
		f.step = domain.DeployingStep(n)
	}
	if f.status == domain.StatusDone && o.FinishedAt != nil {
		f.finishedAt = *o.FinishedAt
	}
	return f
}
