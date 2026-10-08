package app

import (
	"context"
	"errors"
	"reflect"
	"strconv"
	"strings"
	"time"

	"github.com/ThallesP/keel/internal/domain"
)

// Settling deployments against what Swarm reports, the per-deployment timeout, and the deploy
// part of the start-up recovery (convex/reconcile.ts; docs/go/spec/swarm-worker.md §10, §18).

// settleStep is reconcile.ts settle: a running (or orphaned pending) node step against its node
// (nil when deleted). Returns the next step and the log text ("" for none).
func settleStep(step domain.DeployStep, node *domain.Node, now int64) (domain.DeployStep, string) {
	name := step.Label
	if node == nil {
		step.Status, step.FinishedAt = domain.StepFailed, deployPtr(now)
		return step, name + ": node deleted"
	}
	if step.AppliedAt == nil || node.Observed == nil || node.Desired == nil {
		return step, ""
	}
	o, d := node.Observed, node.Desired
	if o.Revision != d.Revision {
		return step, ""
	}
	if o.State == domain.ObservedCrashloop || o.State == domain.ObservedFailed {
		why := ""
		if o.Error != "" {
			why = " · " + o.Error
		}
		what := "crash loop"
		if o.State == domain.ObservedFailed {
			what = "rolled back by Swarm"
		}
		step.Status, step.FinishedAt = domain.StepFailed, deployPtr(now)
		return step, name + ": " + what + why
	}
	if domain.Converged(d, o) {
		var text string
		switch {
		case o.State == domain.ObservedCompleted:
			text = name + ": ran to completion"
		case d.Replicas == 0:
			text = name + ": stopped"
		default:
			text = name + ": " + strconv.Itoa(o.Running) + "/" + strconv.Itoa(d.Replicas) + " replicas running"
		}
		step.Status, step.FinishedAt = domain.StepDone, deployPtr(now)
		return step, text
	}
	return step, ""
}

// settleDeployment is one iteration of reconcile.ts run over deployment d, given its nodes
// (nil = deleted). It returns d's next state and the lines to append; changed=false when it is
// exactly as it was.
func settleDeployment(d domain.Deployment, nodes map[string]*domain.Node, now int64) (next domain.Deployment, appended []domain.LogLine, changed bool) {
	next = d
	next.Steps = append([]domain.DeployStep(nil), d.Steps...)
	for i, step := range d.Steps {
		if step.NodeID == "" || step.Status == domain.StepDone || step.Status == domain.StepFailed {
			continue
		}
		node := nodes[step.NodeID]
		// A pending step waits for apply, which backs out silently when the node is gone; only a
		// deleted node settles it here. Running steps settle against observed state.
		if step.Status == domain.StepPending && node != nil {
			continue
		}
		s, text := settleStep(step, node, now)
		next.Steps[i] = s
		if text != "" {
			appended = append(appended, domain.LogLine{At: now, NodeID: step.NodeID, Text: text})
		}
	}
	anyFailed, allDone, allApplied := false, true, true
	health := -1
	for i, s := range next.Steps {
		if s.NodeID == "" {
			if health < 0 {
				health = i
			}
			continue
		}
		if s.Status == domain.StepFailed {
			anyFailed = true
		}
		if s.Status != domain.StepDone {
			allDone = false
		}
		if s.Status == domain.StepPending || s.AppliedAt == nil {
			allApplied = false
		}
	}
	if health >= 0 {
		h := &next.Steps[health]
		switch {
		case anyFailed:
			h.Status, h.FinishedAt = domain.StepFailed, deployPtr(now)
		case allDone:
			h.Status, h.FinishedAt = domain.StepDone, deployPtr(now)
			if h.StartedAt == nil {
				h.StartedAt = deployPtr(now)
			}
			text := "all replicas healthy"
			if strings.HasPrefix(d.Message, "stop ") {
				text = "stopped"
			}
			appended = append(appended, domain.LogLine{At: now, Text: text})
		case allApplied && h.Status == domain.StepPending:
			h.Status, h.StartedAt = domain.StepRunning, deployPtr(now)
		}
	}
	switch {
	case anyFailed:
		next.Status = domain.DeploymentFailed
	case allDone:
		next.Status = domain.DeploymentSuccess
	default:
		next.Status = domain.DeploymentRunning
	}
	if next.Status == domain.DeploymentRunning {
		next.FinishedAt = nil
	} else {
		next.FinishedAt = deployPtr(now)
	}
	changed = len(appended) > 0 || next.Status != d.Status || !reflect.DeepEqual(next.Steps, d.Steps) ||
		!reflect.DeepEqual(next.FinishedAt, d.FinishedAt)
	return next, appended, changed
}

// reconcile settles the running deployments of environmentID ("" = every environment) inside tx
// (reconcile.run). Only deployments that changed are written and published.
func (a *App) reconcile(tx Tx, ch *Changes, environmentID string, now int64) error {
	running, err := tx.RunningDeployments(environmentID)
	if err != nil {
		return err
	}
	for _, d := range running {
		nodes := map[string]*domain.Node{}
		for _, s := range d.Steps {
			if s.NodeID == "" {
				continue
			}
			if _, seen := nodes[s.NodeID]; seen {
				continue
			}
			n, err := tx.Node(s.NodeID)
			switch {
			case errors.Is(err, ErrNoRow):
				nodes[s.NodeID] = nil
			case err != nil:
				return err
			default:
				nodes[s.NodeID] = &n
			}
		}
		next, appended, changed := settleDeployment(d, nodes, now)
		if !changed {
			continue
		}
		if err := tx.UpdateDeployment(next, appended); err != nil {
			return err
		}
		org, err := deployOrgOf(tx, d.EnvironmentID)
		if err != nil {
			return err
		}
		deploymentChanged(ch, org, next)
	}
	return nil
}

// reconcileRunning is reconcile in its own transaction.
func (a *App) reconcileRunning(ctx context.Context, environmentID string) {
	err := a.write(ctx, func(tx Tx, ch *Changes) error {
		return a.reconcile(tx, ch, environmentID, a.Now())
	})
	if err != nil {
		a.Log.Error("reconcile", "err", err)
	}
}

// timeoutDeployment fails a deployment still running 5 minutes after it started: every
// unfinished step fails, and each node still there turns red with the last observed error until
// its next ship (reconcile.timeoutDeployment).
func (a *App) timeoutDeployment(ctx context.Context, deploymentID string) {
	err := a.write(ctx, func(tx Tx, ch *Changes) error {
		d, err := tx.Deployment(deploymentID)
		if errors.Is(err, ErrNoRow) {
			return nil
		}
		if err != nil || d.Status != domain.DeploymentRunning {
			return err
		}
		org, err := deployOrgOf(tx, d.EnvironmentID)
		if err != nil {
			return err
		}
		now := a.Now()
		var appended []domain.LogLine
		for i := range d.Steps {
			s := &d.Steps[i]
			if s.Status == domain.StepDone || s.Status == domain.StepFailed {
				continue
			}
			s.Status, s.FinishedAt = domain.StepFailed, deployPtr(now)
			if s.NodeID == "" {
				continue
			}
			n, err := tx.Node(s.NodeID)
			missing := errors.Is(err, ErrNoRow)
			if err != nil && !missing {
				return err
			}
			why := ""
			if !missing && n.Observed != nil && n.Observed.Error != "" {
				why = " · " + n.Observed.Error
			}
			appended = append(appended, domain.LogLine{At: now, NodeID: s.NodeID, Text: s.Label + ": timed out waiting for replicas" + why})
			if !missing {
				n.ApplyError = "timed out waiting for replicas" + why
				if err := tx.UpdateNode(n); err != nil {
					return err
				}
				ch.Environment(org, n.EnvironmentID)
			}
		}
		d.Status, d.FinishedAt = domain.DeploymentFailed, deployPtr(now)
		if err := tx.UpdateDeployment(d, appended); err != nil {
			return err
		}
		deploymentChanged(ch, org, d)
		return nil
	})
	if err != nil {
		a.Log.Error("deployment timeout", "deployment", deploymentID, "err", err)
	}
}

// recoverDeploy is the deploy part of the start-up pass that replaces Convex's durable
// scheduler (swarm-worker.md §18): a full sweep; every running deployment's timeout re-armed at
// max(now, startedAt + 5 min); applies that never reached Swarm re-queued (the in-memory jobs
// died with the previous process); the Quick Tunnel era's cloudflared services removed
// (migrations.run step 4); and, when KEEL_AGENT_IMAGE is set, the keel-agent service.
func (a *App) recoverDeploy(ctx context.Context) {
	a.Jobs.After("observe:all", 0, a.observeAll)
	a.Jobs.After("swarm:legacy-tunnels", 0, a.removeLegacyTunnels)

	var running []domain.Deployment
	var redos []applyRequest
	err := a.read(ctx, func(tx Tx) error {
		var err error
		if running, err = tx.RunningDeployments(""); err != nil {
			return err
		}
		for _, d := range running {
			for _, s := range d.Steps {
				if s.NodeID == "" || !(s.Status == domain.StepPending || s.Status == domain.StepRunning && s.AppliedAt == nil) {
					continue
				}
				n, err := tx.Node(s.NodeID)
				if errors.Is(err, ErrNoRow) {
					continue
				}
				if err != nil {
					return err
				}
				if n.Desired == nil {
					continue
				}
				redos = append(redos, applyRequest{
					nodeID:       n.ID,
					deploymentID: d.ID,
					revision:     n.Desired.Revision,
					pull:         strings.HasPrefix(d.Message, "redeploy "),
				})
			}
		}
		return nil
	})
	if err != nil {
		a.Log.Error("recover deployments", "err", err)
	}
	now := a.Now()
	for _, d := range running {
		a.scheduleDeploymentTimeout(d.ID, time.Duration(d.StartedAt+deployTimeout.Milliseconds()-now)*time.Millisecond)
	}
	for _, r := range redos {
		a.scheduleApply(r)
	}
	if a.Config.AgentImage != "" {
		a.Jobs.After("agent", 0, a.ensureAgent)
	}
}

func (a *App) removeLegacyTunnels(ctx context.Context) {
	if a.noSwarm("remove legacy tunnels") {
		return
	}
	n, err := a.Swarm.RemoveLegacyTunnels(ctx)
	if err != nil {
		a.Log.Error("remove legacy tunnels", "err", err)
	} else if n > 0 {
		a.Log.Info("removed legacy tunnel services", "count", n)
	}
}

// noSwarm reports (and logs) a serve started without a Swarm driver: jobs that need Docker
// skip instead of crashing the process.
func (a *App) noSwarm(what string) bool {
	if a.Swarm != nil {
		return false
	}
	a.Log.Error(what + ": no Swarm driver configured")
	return true
}

// ensureAgent creates or updates the keel-agent global service (replaces
// scripts/deploy-worker.sh). Agents reach serve at KEEL_AGENT_CONTROL_URL, else KEEL_SITE_URL.
func (a *App) ensureAgent(ctx context.Context) {
	if a.noSwarm("keel-agent") {
		return
	}
	url := a.Config.AgentControlURL
	if url == "" {
		url = a.Config.SiteURL
	}
	if url == "" || a.Config.WorkerToken == "" {
		a.Log.Warn("keel-agent not deployed: set KEEL_AGENT_CONTROL_URL (or KEEL_SITE_URL) and KEEL_WORKER_TOKEN")
		return
	}
	spec := AgentSpec{Image: a.Config.AgentImage, ControlURL: url, Token: a.Config.WorkerToken}
	if err := a.Swarm.EnsureAgent(ctx, spec); err != nil {
		a.Log.Error("keel-agent", "err", err)
	}
}
