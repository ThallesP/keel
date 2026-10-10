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

	"github.com/ThallesP/keel/internal/domain"
)

type applyRequest struct {
	nodeID       string
	deploymentID string
	revision     int
	pull         bool
}

func (a *App) scheduleApply(req applyRequest) {
	rt := a.deploy
	rt.mu.Lock()
	q, draining := rt.applies[req.nodeID]
	if !draining {
		q = &applyQueue{}
		rt.applies[req.nodeID] = q
	}
	q.queued = append(q.queued, req)
	if q.running != nil && q.running.revision < req.revision {
		q.running.cancel(errApplySuperseded)
	}
	rt.mu.Unlock()
	if !draining {
		a.Jobs.After("apply:"+req.nodeID, 0, func(ctx context.Context) { a.drainApplies(ctx, req.nodeID) })
	}
}

func (a *App) drainApplies(ctx context.Context, nodeID string) {
	rt := a.deploy
	for {
		rt.mu.Lock()
		q := rt.applies[nodeID]
		if len(q.queued) == 0 || ctx.Err() != nil {
			delete(rt.applies, nodeID)
			rt.mu.Unlock()
			return
		}
		req := q.queued[0]
		q.queued = q.queued[1:]
		applyCtx, cancel := context.WithCancelCause(ctx)
		q.running = &runningApply{revision: req.revision, cancel: cancel}
		rt.mu.Unlock()
		a.safeApply(applyCtx, req)
		cancel(nil)
	}
}

func (a *App) safeApply(ctx context.Context, req applyRequest) {
	defer func() {
		if r := recover(); r != nil {
			a.Log.Error("apply panicked", "node", req.nodeID, "panic", r, "stack", string(debug.Stack()))
			record := context.WithoutCancel(ctx)
			a.setApplyError(record, req.nodeID, "internal error")
			a.writeStep(record, req.deploymentID, req.nodeID, stepFailed, "error: internal error")
		}
	}()
	a.apply(ctx, req)
}

type applyInput struct {
	desired domain.Desired
	env     []string
	oneShot bool
}

func (a *App) loadApplyInput(ctx context.Context, req applyRequest) (in applyInput, ok bool, err error) {
	err = a.read(ctx, func(tx Tx) error {
		n, err := tx.Node(req.nodeID)
		if errors.Is(err, ErrNoRow) {
			return nil
		}
		if err != nil || n.Desired == nil {
			return err
		}
		d, err := tx.Deployment(req.deploymentID)
		if err != nil && !errors.Is(err, ErrNoRow) {
			return err
		}
		if slices.ContainsFunc(d.Steps, func(s domain.DeployStep) bool { return s.NodeID == req.nodeID && s.AppliedAt != nil }) {
			return nil
		}
		env, err := deployComputeEnv(tx, n)
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

func (a *App) apply(parent context.Context, req applyRequest) {
	ctx, cancel := context.WithTimeout(parent, 15*time.Minute)
	defer cancel()
	record := context.WithoutCancel(parent)
	id := req.nodeID
	step := func(change stepChange, text string) { a.writeStep(record, req.deploymentID, id, change, text) }
	fail := func(err error) {
		if errors.Is(context.Cause(parent), errApplySuperseded) {
			rev, _, rerr := a.wantedRevision(record, id)
			if rerr != nil || rev <= req.revision {
				rev = req.revision + 1
			}
			a.applySuperseded(record, req, rev)
			return
		}
		if parent.Err() != nil {
			a.Log.Warn("apply interrupted by shutdown", "node", id, "deployment", req.deploymentID, "err", err)
			return
		}
		text := deployErrorText(err)
		a.setApplyError(record, id, text)
		step(stepFailed, "error: "+text)
	}

	in, ok, err := a.loadApplyInput(ctx, req)
	if err != nil {
		fail(err)
		return
	}
	if !ok {
		return
	}
	if in.desired.Revision > req.revision {
		a.applySuperseded(record, req, in.desired.Revision)
		return
	}
	image := in.desired.Image

	cached, err := a.Swarm.ImageCached(ctx, image)
	if err != nil {
		fail(err)
		return
	}
	if cached && !req.pull {
		step(stepRunning, "using cached "+image)
	} else {
		step(stepRunning, "pulling "+image)
		t0 := a.Now()
		err := a.Swarm.PullImage(ctx, image)
		switch {
		case err == nil:
			step(stepLog, fmt.Sprintf("pulled %s in %.1fs", image, float64(a.Now()-t0)/1000))
		case cached:
			step(stepLog, "pull failed ("+deployErrorText(err)+"), using cached image")
		default:
			fail(err)
			return
		}
	}

	rev, wanted, err := a.wantedRevision(ctx, id)
	if err != nil {
		fail(err)
		return
	}
	if !wanted {
		return
	}
	if rev > req.revision {
		a.applySuperseded(record, req, rev)
		return
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
		fail(err)
		return
	}
	text := "service updated · revision " + strconv.Itoa(in.desired.Revision)
	if created {
		_, wanted, err = a.wantedRevision(ctx, id)
		if err != nil {
			fail(err)
			return
		}
		if !wanted {
			if err := a.Swarm.RemoveService(ctx, id); err != nil {
				fail(err)
			}
			return
		}
		text = "service created · " + strconv.Itoa(in.desired.Replicas) + " replica(s)"
	}
	moved := false
	err = a.write(record, func(tx Tx, ch *Changes) error {
		if err := a.patchStep(tx, ch, req.deploymentID, id, stepApplied, text); err != nil {
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
		moved, err = deployFollowPort(tx, ch, scope, in.desired.Port, a.Now())
		return err
	})
	if err != nil {
		fail(err)
		return
	}
	if moved {
		a.ScheduleProxySync()
	}
	a.scheduleObserve(record, id, observeDebounce, 0)
}

func (a *App) applySuperseded(ctx context.Context, req applyRequest, revision int) {
	a.writeStep(ctx, req.deploymentID, req.nodeID, stepFailed, "superseded by revision "+strconv.Itoa(revision))
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

func (a *App) ScheduleRemoveService(nodeID string) {
	a.deploy.mu.Lock()
	delete(a.deploy.observe, nodeID)
	a.deploy.mu.Unlock()
	a.Jobs.After("remove:"+nodeID, 0, func(ctx context.Context) {
		dctx, cancel := context.WithTimeout(ctx, dockerCallDeadline)
		defer cancel()
		if err := a.Swarm.RemoveService(dctx, nodeID); err != nil {
			a.Log.Error("remove service", "node", nodeID, "err", err)
		}
		a.reconcileRunning(ctx, "")
	})
}

func deployErrorText(err error) string { return compactText(err.Error(), 300) }
