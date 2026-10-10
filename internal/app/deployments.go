package app

import (
	"context"
	"errors"
	"slices"
	"strings"
	"time"

	"github.com/ThallesP/keel/internal/domain"
)

const healthStepLabel = "health checks"

type ShipOptions struct {
	Only    []string
	Refresh bool
	Verb    string
}

func (a *App) beginDeployment(tx Tx, ch *Changes, scope EnvScope, opts ShipOptions) (string, error) {
	envID := scope.Environment.ID
	running, err := tx.HasRunningDeployment(envID)
	if err != nil {
		return "", err
	}
	if running {
		return "", domain.E(domain.CodeDeploymentRunning, "A deployment is already running")
	}
	nodes, err := tx.Nodes(envID)
	if err != nil {
		return "", err
	}
	var affected []domain.Node
	for _, n := range nodes {
		if n.Desired == nil || !n.Type.Deployable() {
			continue
		}
		if opts.Only == nil && n.Dirty || slices.Contains(opts.Only, n.ID) {
			affected = append(affected, n)
		}
	}
	if len(affected) == 0 {
		return "", domain.E(domain.CodeNothingToShip, "Nothing to ship")
	}

	now := a.Now()
	steps := make([]domain.DeployStep, 0, len(affected)+1)
	names := make([]string, 0, len(affected))
	for i := range affected {
		n := &affected[i]
		n.Desired.Revision++
		n.Dirty = false
		n.ShippedAt = new(now)
		n.ApplyError = ""
		if err := tx.UpdateNode(*n); err != nil {
			return "", err
		}
		steps = append(steps, domain.DeployStep{NodeID: n.ID, Label: n.Name, Status: domain.StepPending})
		names = append(names, n.Name)
	}
	steps = append(steps, domain.DeployStep{Label: healthStepLabel, Status: domain.StepPending})

	word := "ship"
	switch {
	case opts.Verb != "":
		word = opts.Verb
	case opts.Only != nil && opts.Refresh:
		word = "redeploy"
	case opts.Only != nil:
		word = "deploy"
	}
	d := domain.Deployment{
		ID:            domain.NewID(),
		EnvironmentID: envID,
		Message:       word + " " + strings.Join(names, ", "),
		Status:        domain.DeploymentRunning,
		StartedAt:     now,
		Steps:         steps,
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

func (a *App) ShipEnvironment(ctx context.Context, actor domain.Actor, environmentID string, opts ShipOptions) (string, error) {
	var id string
	err := a.write(ctx, func(tx Tx, ch *Changes) error {
		scope, err := requireEnvironment(tx, actor, environmentID)
		if err != nil {
			return err
		}
		id, err = a.beginDeployment(tx, ch, scope, opts)
		return err
	})
	return id, err
}

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

func (a *App) ListNodeDeployments(ctx context.Context, actor domain.Actor, nodeID string) ([]domain.Deployment, error) {
	var out []domain.Deployment
	err := a.read(ctx, func(tx Tx) error {
		scope, ok, err := ownedNode(tx, actor, nodeID)
		if err != nil || !ok {
			return err
		}
		recent, err := tx.RecentDeployments(scope.Environment.ID, 50)
		if err != nil {
			return err
		}
		for _, d := range recent {
			if len(out) == 20 {
				break
			}
			if !slices.ContainsFunc(d.Steps, func(s domain.DeployStep) bool { return s.NodeID == nodeID }) {
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

type stepChange int

const (
	stepRunning stepChange = iota
	stepLog
	stepApplied
	stepFailed
)

func (a *App) patchStep(tx Tx, ch *Changes, deploymentID, nodeID string, change stepChange, text string) error {
	d, err := tx.Deployment(deploymentID)
	if errors.Is(err, ErrNoRow) {
		return nil
	}
	if err != nil {
		return err
	}
	now := a.Now()
	for i := range d.Steps {
		s := &d.Steps[i]
		switch {
		case s.NodeID == nodeID && change == stepRunning:
			s.Status, s.StartedAt = domain.StepRunning, new(now)
		case s.NodeID == nodeID && change == stepApplied:
			s.AppliedAt = new(now)
		case change == stepFailed && (s.NodeID == nodeID || s.NodeID == ""):
			s.Status, s.FinishedAt = domain.StepFailed, new(now)
		}
	}
	var appended []domain.LogLine
	if text != "" {
		appended = []domain.LogLine{{At: now, NodeID: nodeID, Text: text}}
	}
	if change == stepFailed && d.Status == domain.DeploymentRunning {
		d.Status, d.FinishedAt = domain.DeploymentFailed, new(now)
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

func (a *App) scheduleDeploymentTimeout(deploymentID string, delay time.Duration) {
	a.Jobs.After("timeout:"+deploymentID, delay, func(ctx context.Context) {
		a.timeoutDeployment(ctx, deploymentID)
	})
}
