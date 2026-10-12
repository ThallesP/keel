package app

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"strings"
	"time"

	"github.com/ThallesP/keel/internal/domain"
)

func settleStep(step domain.DeployStep, node *domain.Node, now int64) (domain.DeployStep, string) {
	if node == nil {
		step.Status, step.FinishedAt = domain.StepFailed, now
		return step, step.Label + ": node deleted"
	}
	o, d := node.Observed, node.Desired
	if step.AppliedAt == 0 || o == nil || d == nil || o.Revision != d.Revision {
		return step, ""
	}
	why := ""
	if o.Error != "" {
		why = " · " + o.Error
	}
	var text string
	switch {
	case o.State == domain.ObservedCrashloop:
		step.Status, text = domain.StepFailed, "crash loop"+why
	case o.State == domain.ObservedFailed:
		step.Status, text = domain.StepFailed, "rolled back by Swarm"+why
	case !domain.Converged(d, o):
		return step, ""
	case o.State == domain.ObservedCompleted:
		step.Status, text = domain.StepDone, "ran to completion"
	case d.Replicas == 0:
		step.Status, text = domain.StepDone, "stopped"
	default:
		step.Status, text = domain.StepDone, fmt.Sprintf("%d/%d replicas running", o.Running, d.Replicas)
	}
	step.FinishedAt = now
	return step, step.Label + ": " + text
}

func settleDeployment(d domain.Deployment, nodes map[string]*domain.Node, now int64) (next domain.Deployment, appended []domain.LogLine, changed bool) {
	next = d
	next.Steps = slices.Clone(d.Steps)
	for i, step := range d.Steps {
		node := nodes[step.NodeID]
		if step.NodeID == "" || step.Status.Finished() || step.Status == domain.StepPending && node != nil {
			continue
		}
		s, text := settleStep(step, node, now)
		next.Steps[i] = s
		if text != "" {
			appended = append(appended, domain.LogLine{At: now, NodeID: step.NodeID, Text: text})
		}
	}
	anyFailed, allDone, allApplied := false, true, true
	for _, s := range next.Steps {
		if s.NodeID == "" {
			continue
		}
		anyFailed = anyFailed || s.Status == domain.StepFailed
		allDone = allDone && s.Status == domain.StepDone
		allApplied = allApplied && s.AppliedAt != 0
	}
	health := &next.Steps[len(next.Steps)-1]
	switch {
	case anyFailed:
		health.Status, health.FinishedAt = domain.StepFailed, now
		next.Status, next.FinishedAt = domain.DeploymentFailed, now
	case allDone:
		health.Status, health.StartedAt, health.FinishedAt = domain.StepDone, cmp.Or(health.StartedAt, now), now
		next.Status, next.FinishedAt = domain.DeploymentSuccess, now
		text := "all replicas healthy"
		if strings.HasPrefix(d.Message, "stop ") {
			text = "stopped"
		}
		appended = append(appended, domain.LogLine{At: now, Text: text})
	case allApplied && health.Status == domain.StepPending:
		health.Status, health.StartedAt = domain.StepRunning, now
	}
	return next, appended, next.Status != d.Status || !reflect.DeepEqual(next.Steps, d.Steps)
}

func reconcile(tx Tx, ch *Changes, environmentID string, now int64) error {
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
			n, err := tx.Node(s.NodeID)
			if errors.Is(err, ErrNoRow) {
				continue
			}
			if err != nil {
				return err
			}
			nodes[s.NodeID] = &n
		}
		next, appended, changed := settleDeployment(d, nodes, now)
		if !changed {
			continue
		}
		if err := tx.UpdateDeployment(next, appended); err != nil {
			return err
		}
		org, err := tx.OrganizationOfEnvironment(d.EnvironmentID)
		if err != nil {
			return err
		}
		deploymentChanged(ch, org, next)
	}
	return nil
}

func (a *App) reconcileRunning(ctx context.Context, environmentID string) {
	err := a.write(ctx, func(tx Tx, ch *Changes) error {
		return reconcile(tx, ch, environmentID, a.Now())
	})
	if err != nil {
		a.Log.Error("reconcile", "err", err)
	}
}

func (a *App) timeoutDeployment(ctx context.Context, deploymentID string) {
	err := a.write(ctx, func(tx Tx, ch *Changes) error {
		d, err := tx.Deployment(deploymentID)
		if errors.Is(err, ErrNoRow) {
			return nil
		}
		if err != nil || d.Status != domain.DeploymentRunning {
			return err
		}
		org, err := tx.OrganizationOfEnvironment(d.EnvironmentID)
		if err != nil {
			return err
		}
		now := a.Now()
		var appended []domain.LogLine
		for i := range d.Steps {
			s := &d.Steps[i]
			if s.Status.Finished() {
				continue
			}
			s.Status, s.FinishedAt = domain.StepFailed, now
			if s.NodeID == "" {
				continue
			}
			n, err := tx.Node(s.NodeID)
			missing := errors.Is(err, ErrNoRow)
			if err != nil && !missing {
				return err
			}
			reason := "timed out waiting for replicas"
			if n.Observed != nil && n.Observed.Error != "" {
				reason += " · " + n.Observed.Error
			}
			appended = append(appended, domain.LogLine{At: now, NodeID: s.NodeID, Text: s.Label + ": " + reason})
			if missing {
				continue
			}
			n.ApplyError = reason
			if err := tx.UpdateNode(n); err != nil {
				return err
			}
			ch.Environment(org, n.EnvironmentID)
		}
		d.Status, d.FinishedAt = domain.DeploymentFailed, now
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

func (a *App) recoverDeploy(ctx context.Context) {
	a.Jobs.After("observe:all", 0, a.observeAll)

	var running []domain.Deployment
	var redos []applyRequest
	err := a.read(ctx, func(tx Tx) (err error) {
		if running, err = tx.RunningDeployments(""); err != nil {
			return err
		}
		for _, d := range running {
			for _, s := range d.Steps {
				if s.NodeID == "" || s.Status.Finished() || s.AppliedAt != 0 {
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
		a.scheduleDeploymentTimeout(d.ID, deployTimeout-time.Duration(now-d.StartedAt)*time.Millisecond)
	}
	for _, r := range redos {
		a.scheduleApply(r)
	}
	if a.Config.AgentImage != "" {
		a.Jobs.After("agent", 0, a.ensureAgent)
	}
}

func (a *App) ensureAgent(ctx context.Context) {
	url := cmp.Or(a.Config.AgentControlURL, a.Config.SiteURL)
	if url == "" || a.Config.WorkerToken == "" {
		a.Log.Warn("keel-agent not deployed: set KEEL_AGENT_CONTROL_URL (or KEEL_SITE_URL) and KEEL_WORKER_TOKEN")
		return
	}
	spec := AgentSpec{Image: a.Config.AgentImage, ControlURL: url, Token: a.Config.WorkerToken}
	if err := a.Swarm.EnsureAgent(ctx, spec); err != nil {
		a.Log.Error("keel-agent", "err", err)
	}
}
