package app

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/ThallesP/keel/internal/domain"
)

// Deployments: Ship, the reads, and the step writers apply reports progress with
// (convex/deployments.ts; docs/go/spec/projects.md §7.1, §9.5; swarm-worker.md §6.3, §10.1).

// Messages (byte-identical to the Convex ConvexError strings; the CLI maps them).
const (
	MsgDeploymentRunning = "A deployment is already running"
	MsgNothingToShip     = "Nothing to ship"
)

// healthStepLabel is the final step of every deployment (it has no node).
const healthStepLabel = "health checks"

// ShipOptions are beginDeployment's options (docs/go/spec/projects.md §7.1).
type ShipOptions struct {
	Only    []string // nil = every dirty deployable node; non-nil (even empty) = exactly these
	Refresh bool     // pull the image again
	Verb    string   // message word override ("start", "stop"); "" derives ship/deploy/redeploy
}

// beginDeployment bumps desired.revision on the affected nodes, records one step per node plus
// the health step, and (after commit) schedules an apply per node and the deployment's timeout.
// It runs inside the caller's write transaction. Errors: DEPLOYMENT_RUNNING, NOTHING_TO_SHIP.
func (a *App) beginDeployment(tx Tx, ch *Changes, scope EnvScope, opts ShipOptions) (string, error) {
	envID := scope.Environment.ID
	running, err := tx.HasRunningDeployment(envID)
	if err != nil {
		return "", err
	}
	if running {
		return "", domain.E(domain.CodeDeploymentRunning, MsgDeploymentRunning)
	}
	nodes, err := tx.Nodes(envID)
	if err != nil {
		return "", err
	}
	var wanted map[string]bool
	if opts.Only != nil { // an empty list is still a set: nothing matches
		wanted = make(map[string]bool, len(opts.Only))
		for _, id := range opts.Only {
			wanted[id] = true
		}
	}
	var affected []domain.Node
	for _, n := range nodes {
		if n.Desired == nil || !n.Type.Deployable() {
			continue
		}
		if wanted != nil && !wanted[n.ID] || wanted == nil && !n.Dirty {
			continue
		}
		affected = append(affected, n)
	}
	if len(affected) == 0 {
		return "", domain.E(domain.CodeNothingToShip, MsgNothingToShip)
	}

	now := a.Now()
	steps := make([]domain.DeployStep, 0, len(affected)+1)
	names := make([]string, 0, len(affected))
	for i := range affected {
		n := &affected[i]
		d := *n.Desired
		d.Revision++
		n.Desired = &d
		n.Dirty = false
		n.ShippedAt = deployPtr(now)
		n.ApplyError = ""
		if err := tx.UpdateNode(*n); err != nil {
			return "", err
		}
		steps = append(steps, domain.DeployStep{NodeID: n.ID, Label: n.Name, Status: domain.StepPending})
		names = append(names, n.Name)
	}
	steps = append(steps, domain.DeployStep{Label: healthStepLabel, Status: domain.StepPending})

	word := opts.Verb
	if word == "" {
		switch {
		case wanted != nil && opts.Refresh:
			word = "redeploy"
		case wanted != nil:
			word = "deploy"
		default:
			word = "ship"
		}
	}
	d := domain.Deployment{
		ID:            domain.NewID(),
		EnvironmentID: envID,
		Message:       word + " " + strings.Join(names, ", "),
		Status:        domain.DeploymentRunning,
		StartedAt:     now,
		Steps:         steps,
		Log:           []domain.LogLine{},
	}
	if err := tx.InsertDeployment(d); err != nil {
		return "", err
	}
	ch.Environment(scope.Org, envID)
	deploymentChanged(ch, scope.Org, d)
	ch.AfterCommit(func() {
		for _, n := range affected {
			a.scheduleApply(applyRequest{nodeID: n.ID, deploymentID: d.ID, revision: n.Desired.Revision, pull: opts.Refresh})
		}
		a.scheduleDeploymentTimeout(d.ID, deployTimeout)
	})
	return d.ID, nil
}

// ShipEnvironment is deployments.start: Ship (opts.Only nil: every dirty deployable node), or
// redeploy/retry the listed nodes. Returns the deployment id. opts.Verb is ignored.
func (a *App) ShipEnvironment(ctx context.Context, actor domain.Actor, environmentID string, opts ShipOptions) (string, error) {
	var id string
	err := a.write(ctx, func(tx Tx, ch *Changes) error {
		scope, err := requireEnvironment(tx, actor, environmentID)
		if err != nil {
			return err
		}
		id, err = a.beginDeployment(tx, ch, scope, ShipOptions{Only: opts.Only, Refresh: opts.Refresh})
		return err
	})
	if err != nil {
		return "", err
	}
	return id, nil
}

// LatestDeployment is the environment's newest deployment, or nil when there is none or the
// environment is not the actor's (deployments.latest).
func (a *App) LatestDeployment(ctx context.Context, actor domain.Actor, environmentID string) (*domain.Deployment, error) {
	var out *domain.Deployment
	err := a.read(ctx, func(tx Tx) error {
		_, ok, err := ownedEnvironment(tx, actor, environmentID)
		if err != nil || !ok {
			return err
		}
		d, err := tx.LatestDeployment(environmentID)
		if errors.Is(err, ErrNoRow) {
			return nil
		}
		if err != nil {
			return err
		}
		out = &d
		return nil
	})
	return out, err
}

// GetDeployment is one deployment, or nil when the id is malformed, missing or not the actor's
// (deployments.get: the id comes from a URL).
func (a *App) GetDeployment(ctx context.Context, actor domain.Actor, id string) (*domain.Deployment, error) {
	var out *domain.Deployment
	err := a.read(ctx, func(tx Tx) error {
		d, err := tx.Deployment(id)
		if errors.Is(err, ErrNoRow) {
			return nil
		}
		if err != nil {
			return err
		}
		_, ok, err := ownedEnvironment(tx, actor, d.EnvironmentID)
		if err != nil || !ok {
			return err
		}
		out = &d
		return nil
	})
	return out, err
}

// ListNodeDeployments: of the node's environment's 50 newest deployments, those with a step for
// the node, at most 20, newest first. Empty when the node is not the actor's
// (deployments.listForNode).
func (a *App) ListNodeDeployments(ctx context.Context, actor domain.Actor, nodeID string) ([]domain.Deployment, error) {
	out := []domain.Deployment{}
	err := a.read(ctx, func(tx Tx) error {
		scope, ok, err := ownedNode(tx, actor, nodeID)
		if err != nil || !ok {
			return err
		}
		recent, err := tx.RecentDeployments(scope.Environment.ID, recentDeploymentsScan)
		if err != nil {
			return err
		}
		for _, d := range recent {
			if len(out) == nodeDeploymentsMax {
				break
			}
			if !deploymentHasNode(d, nodeID) {
				continue
			}
			if d.Log, err = tx.DeploymentLog(d.ID); err != nil {
				return err
			}
			out = append(out, d)
		}
		return nil
	})
	return out, err
}

func deploymentHasNode(d domain.Deployment, nodeID string) bool {
	for _, s := range d.Steps {
		if s.NodeID == nodeID {
			return true
		}
	}
	return false
}

// ── Step writers (deployments.stepRunning/stepLog/stepApplied/stepFailed) ───────────────────

type stepChange int

const (
	stepRunning stepChange = iota // {status: running, startedAt: now}
	stepLog                       // log line only
	stepApplied                   // {appliedAt: now}; status stays running
	stepFailed                    // {status: failed, finishedAt: now}; fails the deployment
)

// patchStep applies change to the node's step of the deployment inside tx and appends text to
// its log. A failed step also fails the health step and, if still running, the deployment. A
// missing deployment is a no-op. Finished deployments are patched too, never revived.
func (a *App) patchStep(tx Tx, ch *Changes, deploymentID, nodeID string, change stepChange, text string) error {
	d, err := tx.Deployment(deploymentID)
	if errors.Is(err, ErrNoRow) {
		return nil
	}
	if err != nil {
		return err
	}
	now := a.Now()
	failed := change == stepFailed
	for i := range d.Steps {
		s := &d.Steps[i]
		switch {
		case s.NodeID == nodeID:
			switch change {
			case stepRunning:
				s.Status, s.StartedAt = domain.StepRunning, deployPtr(now)
			case stepApplied:
				s.AppliedAt = deployPtr(now)
			case stepFailed:
				s.Status, s.FinishedAt = domain.StepFailed, deployPtr(now)
			}
		case failed && s.NodeID == "":
			s.Status, s.FinishedAt = domain.StepFailed, deployPtr(now)
		}
	}
	var appended []domain.LogLine
	if text != "" {
		appended = []domain.LogLine{{At: now, NodeID: nodeID, Text: text}}
	}
	if failed && d.Status == domain.DeploymentRunning {
		d.Status, d.FinishedAt = domain.DeploymentFailed, deployPtr(now)
	}
	if err := tx.UpdateDeployment(d, appended); err != nil {
		return err
	}
	org, err := deployOrgOf(tx, d.EnvironmentID)
	if err != nil {
		return err
	}
	deploymentChanged(ch, org, d)
	return nil
}

// writeStep is patchStep in its own transaction (apply's progress lines). No-op without a
// deployment.
func (a *App) writeStep(ctx context.Context, deploymentID, nodeID string, change stepChange, text string) {
	if deploymentID == "" {
		return
	}
	err := a.write(ctx, func(tx Tx, ch *Changes) error {
		return a.patchStep(tx, ch, deploymentID, nodeID, change, text)
	})
	if err != nil {
		a.Log.Error("deployment step", "deployment", deploymentID, "node", nodeID, "err", err)
	}
}

func deployPtr[T any](v T) *T { return &v }

// scheduleDeploymentTimeout runs timeoutDeployment after delay (one job per deployment).
func (a *App) scheduleDeploymentTimeout(deploymentID string, delay time.Duration) {
	if delay < 0 {
		delay = 0
	}
	a.Jobs.After("timeout:"+deploymentID, delay, func(ctx context.Context) {
		a.timeoutDeployment(ctx, deploymentID)
	})
}
