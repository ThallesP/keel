package app

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"runtime/debug"
	"slices"
	"strconv"
	"time"

	"github.com/ThallesP/durable"

	"github.com/ThallesP/keel/internal/domain"
)

type applyRequest struct {
	NodeID       string
	DeploymentID string
	Revision     int
	Pull         bool
}

func (a *App) startApply(ctx context.Context, req applyRequest) error {
	a.supersedeApply(req)
	_, err := a.flows.apply.Start(ctx, req, durable.WithID("apply:"+req.DeploymentID+":"+req.NodeID))
	return err
}

func (a *App) supersedeApply(req applyRequest) {
	rt := a.deploy
	rt.mu.Lock()
	defer rt.mu.Unlock()
	if running, ok := rt.applying[req.NodeID]; ok && running.revision < req.Revision {
		running.cancel(errApplySuperseded)
	}
}

func (a *App) runApply(ctx durable.Context, req applyRequest) (struct{}, error) {
	return struct{}{}, durable.Do(ctx, "apply", func(ctx context.Context) error {
		return a.safeApply(ctx, req)
	})
}

func (a *App) safeApply(parent context.Context, req applyRequest) (err error) {
	ctx, cancel := context.WithCancelCause(parent)
	defer cancel(nil)
	release := a.holdApply(req, cancel)
	defer release()
	defer func() {
		if r := recover(); r != nil {
			a.Log.Error("apply panicked", "node", req.NodeID, "panic", r, "stack", string(debug.Stack()))
			record := context.WithoutCancel(ctx)
			a.setApplyError(record, req.NodeID, "internal error")
			a.writeStep(record, req.DeploymentID, req.NodeID, stepFailed, "error: internal error")
		}
	}()
	return a.apply(ctx, req)
}

func (a *App) holdApply(req applyRequest, cancel context.CancelCauseFunc) (release func()) {
	rt := a.deploy
	rt.mu.Lock()
	defer rt.mu.Unlock()
	rt.applying[req.NodeID] = runningApply{revision: req.Revision, cancel: cancel}
	return func() {
		rt.mu.Lock()
		defer rt.mu.Unlock()
		delete(rt.applying, req.NodeID)
	}
}

type applyInput struct {
	desired domain.Desired
	env     []string
	oneShot bool
}

func (a *App) loadApplyInput(ctx context.Context, req applyRequest) (in applyInput, ok bool, err error) {
	err = a.read(ctx, func(tx Tx) error {
		n, err := tx.Node(req.NodeID)
		if errors.Is(err, ErrNoRow) {
			return nil
		}
		if err != nil || n.Desired == nil {
			return err
		}
		d, err := tx.Deployment(req.DeploymentID)
		if err != nil && !errors.Is(err, ErrNoRow) {
			return err
		}
		if slices.ContainsFunc(d.Steps, func(s domain.DeployStep) bool { return s.NodeID == req.NodeID && s.AppliedAt != 0 }) {
			return nil
		}
		env, err := computeEnv(tx, n)
		if err != nil {
			return err
		}
		if env, err = a.withTracing(tx, n, env); err != nil {
			return err
		}
		in, ok = applyInput{desired: *n.Desired, env: deployEnvList(env), oneShot: n.OneShot}, true
		return nil
	})
	return in, ok, err
}

func (a *App) wantedRevision(ctx context.Context, nodeID string) (revision int, ok bool, err error) {
	err = a.read(ctx, func(tx Tx) error {
		n, err := tx.Node(nodeID)
		if errors.Is(err, ErrNoRow) {
			return nil
		}
		if err != nil {
			return err
		}
		if n.Desired != nil {
			revision, ok = n.Desired.Revision, true
		}
		return nil
	})
	return revision, ok, err
}

func deployEnvList(env map[string]string) []string {
	out := make([]string, 0, len(env))
	for _, k := range slices.Sorted(maps.Keys(env)) {
		out = append(out, k+"="+env[k])
	}
	return out
}

func (a *App) apply(parent context.Context, req applyRequest) error {
	ctx, cancel := context.WithTimeout(parent, 15*time.Minute)
	defer cancel()
	record := context.WithoutCancel(parent)
	id := req.NodeID
	step := func(change stepChange, text string) { a.writeStep(record, req.DeploymentID, id, change, text) }
	superseded := func(revision int) { step(stepFailed, "superseded by revision "+strconv.Itoa(revision)) }
	fail := func(err error) error {
		if errors.Is(context.Cause(parent), errApplySuperseded) {
			rev, _, rerr := a.wantedRevision(record, id)
			if rerr != nil || rev <= req.Revision {
				rev = req.Revision + 1
			}
			superseded(rev)
			return nil
		}
		if parent.Err() != nil {
			a.Log.Warn("apply interrupted by shutdown", "node", id, "deployment", req.DeploymentID, "err", err)
			return context.Cause(parent)
		}
		text := CompactText(err.Error(), 300)
		a.setApplyError(record, id, text)
		step(stepFailed, "error: "+text)
		return nil
	}

	in, ok, err := a.loadApplyInput(ctx, req)
	if err != nil {
		return fail(err)
	}
	if !ok {
		return nil
	}
	if in.desired.Revision > req.Revision {
		superseded(in.desired.Revision)
		return nil
	}
	image := in.desired.Image

	cached, err := a.Swarm.ImageCached(ctx, image)
	if err != nil {
		return fail(err)
	}
	if cached && !req.Pull {
		step(stepRunning, "using cached "+image)
	} else {
		step(stepRunning, "pulling "+image)
		t0 := a.Now()
		err := a.Swarm.PullImage(ctx, image)
		switch {
		case err == nil:
			step(stepLog, fmt.Sprintf("pulled %s in %.1fs", image, float64(a.Now()-t0)/1000))
		case cached:
			step(stepLog, "pull failed ("+CompactText(err.Error(), 300)+"), using cached image")
		default:
			return fail(err)
		}
	}

	rev, wanted, err := a.wantedRevision(ctx, id)
	if err != nil {
		return fail(err)
	}
	if !wanted {
		return nil
	}
	if rev > req.Revision {
		superseded(rev)
		return nil
	}

	spec := ServiceSpec{
		NodeID:   id,
		Image:    image,
		Revision: in.desired.Revision,
		Replicas: in.desired.Replicas,
		Env:      in.env,
		OneShot:  in.oneShot,
	}
	created, err := a.createOrUpdate(ctx, spec)
	if err != nil {
		return fail(err)
	}
	text := "service updated · revision " + strconv.Itoa(in.desired.Revision)
	if created {
		_, wanted, err = a.wantedRevision(ctx, id)
		if err != nil {
			return fail(err)
		}
		if !wanted {
			if err := a.Swarm.RemoveService(ctx, id); err != nil {
				return fail(err)
			}
			return nil
		}
		text = "service created · " + strconv.Itoa(in.desired.Replicas) + " replica(s)"
	}
	moved := false
	err = a.write(record, func(tx Tx, ch *Changes) error {
		if err := a.patchStep(tx, ch, req.DeploymentID, id, stepApplied, text); err != nil {
			return err
		}
		scope, ok, err := ownedNode(tx, domain.SystemActor, id)
		if err != nil || !ok {
			return err
		}
		if n := scope.Node; n.ApplyError != "" {
			n.ApplyError = ""
			if err := tx.UpdateNode(n); err != nil {
				return err
			}
			ch.Environment(scope.Project.OrganizationID, n.EnvironmentID)
		}
		if in.desired.Port == 0 {
			return nil
		}
		moved, err = followPort(tx, ch, scope, in.desired.Port, a.Now())
		return err
	})
	if err != nil {
		return fail(err)
	}
	if moved {
		a.ScheduleProxySync()
	}
	a.scheduleObserve(record, id, observeDebounce, 0)
	return nil
}

func (a *App) createOrUpdate(ctx context.Context, spec ServiceSpec) (bool, error) {
	for attempt := 0; ; attempt++ {
		version, found, err := a.Swarm.ServiceVersion(ctx, spec.NodeID)
		if err != nil {
			return false, err
		}
		if !found {
			err = a.Swarm.CreateService(ctx, spec)
		} else {
			err = a.Swarm.UpdateService(ctx, version, spec)
		}
		if err == nil {
			return !found, nil
		}
		if attempt >= 2 || ctx.Err() != nil {
			return false, err
		}
	}
}

func (a *App) setApplyError(ctx context.Context, nodeID, text string) {
	err := a.write(ctx, func(tx Tx, ch *Changes) error {
		n, err := tx.Node(nodeID)
		if errors.Is(err, ErrNoRow) {
			return nil
		}
		if err != nil || n.ApplyError == text {
			return err
		}
		n.ApplyError = text
		if err := tx.UpdateNode(n); err != nil {
			return err
		}
		return environmentChanged(tx, ch, n.EnvironmentID)
	})
	if err != nil {
		a.Log.Error("set apply error", "node", nodeID, "err", err)
	}
}

type removeRun struct {
	NodeID string
}

func (a *App) runRemove(ctx durable.Context, run removeRun) (struct{}, error) {
	return struct{}{}, durable.Do(ctx, "remove service", func(ctx context.Context) error {
		return a.removeService(ctx, run.NodeID)
	}, durable.Retry(8, 2*time.Second))
}

func (a *App) removeService(ctx context.Context, nodeID string) error {
	dctx, cancel := context.WithTimeout(ctx, dockerCallDeadline)
	defer cancel()
	err := a.Swarm.RemoveService(dctx, nodeID)
	if err != nil {
		a.Log.Error("remove service", "node", nodeID, "err", err)
	}
	return err
}
