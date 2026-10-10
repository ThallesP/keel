package app

import (
	"context"
	"errors"
	"fmt"
	"runtime/debug"
	"sort"
	"strconv"

	"github.com/ThallesP/keel/internal/domain"
)

type applyRequest struct {
	nodeID       string
	deploymentID string
	revision     int
	pull         bool
}

func (a *App) scheduleApply(req applyRequest) {
	rt := a.deployRuntime()
	rt.mu.Lock()
	q := rt.applies[req.nodeID]
	start := q == nil
	if start {
		q = &applyQueue{}
		rt.applies[req.nodeID] = q
	}
	q.queued = append(q.queued, req)
	if q.running != nil && q.running.revision < req.revision {
		q.running.cancel(errApplySuperseded)
	}
	key := fmt.Sprintf("apply:%s:%d", req.nodeID, rt.next())
	rt.mu.Unlock()
	if start {
		a.Jobs.After(key, 0, func(ctx context.Context) { a.drainApplies(ctx, req.nodeID) })
	}
}

func (a *App) drainApplies(ctx context.Context, nodeID string) {
	rt := a.deployRuntime()
	for {
		rt.mu.Lock()
		q := rt.applies[nodeID]
		if q == nil || len(q.queued) == 0 || ctx.Err() != nil {
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
		rt.mu.Lock()
		if q.running != nil && q.running.revision == req.revision {
			q.running = nil
		}
		rt.mu.Unlock()
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
	applied bool
}

func (a *App) loadApplyInput(ctx context.Context, req applyRequest) (*applyInput, error) {
	var in *applyInput
	err := a.read(ctx, func(tx Tx) error {
		n, err := tx.Node(req.nodeID)
		if errors.Is(err, ErrNoRow) {
			return nil
		}
		if err != nil {
			return err
		}
		if n.Desired == nil {
			return nil
		}
		env, err := deployComputeEnv(tx, n)
		if err != nil {
			return err
		}
		if env, err = deployWithTracing(a, tx, n, env); err != nil {
			return err
		}
		in = &applyInput{desired: *n.Desired, env: deployEnvList(env), oneShot: n.OneShot}
		if req.deploymentID == "" {
			return nil
		}
		d, err := tx.Deployment(req.deploymentID)
		if errors.Is(err, ErrNoRow) {
			return nil
		}
		if err != nil {
			return err
		}
		for _, s := range d.Steps {
			if s.NodeID == req.nodeID && s.AppliedAt != nil {
				in.applied = true
			}
		}
		return nil
	})
	return in, err
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
	keys := make([]string, 0, len(env))
	for k := range env {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	out := make([]string, 0, len(keys))
	for _, k := range keys {
		out = append(out, k+"="+env[k])
	}
	return out
}

func (a *App) apply(parent context.Context, req applyRequest) {
	ctx, cancel := context.WithTimeout(parent, applyDeadline)
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
	if a.noSwarm("apply") {
		fail(errors.New("no Swarm driver configured"))
		return
	}

	in, err := a.loadApplyInput(ctx, req)
	if err != nil {
		fail(err)
		return
	}
	if in == nil {
		return
	}
	if in.applied {
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
		if err := a.Swarm.PullImage(ctx, image); err != nil {
			if !cached {
				fail(err)
				return
			}
			step(stepLog, "pull failed ("+deployErrorText(err)+"), using cached image")
		} else {
			step(stepLog, "pulled "+image+" in "+strconv.FormatFloat(float64(a.Now()-t0)/1000, 'f', 1, 64)+"s")
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
	}
	text := "service updated · revision " + strconv.Itoa(in.desired.Revision)
	if created {
		text = "service created · " + strconv.Itoa(in.desired.Replicas) + " replica(s)"
	}
	moved := false
	err = a.write(record, func(tx Tx, ch *Changes) error {
		if req.deploymentID != "" {
			if err := a.patchStep(tx, ch, req.deploymentID, id, stepApplied, text); err != nil {
				return err
			}
		}
		n, err := tx.Node(id)
		if errors.Is(err, ErrNoRow) {
			return nil
		}
		if err != nil {
			return err
		}
		org, err := deployOrgOf(tx, n.EnvironmentID)
		if err != nil {
			return err
		}
		if n.ApplyError != "" {
			n.ApplyError = ""
			if err := tx.UpdateNode(n); err != nil {
				return err
			}
			ch.Environment(org, n.EnvironmentID)
		}
		if in.desired.Port != nil {
			scope, ok, err := ownedNode(tx, domain.SystemActor, id)
			if err != nil || !ok {
				return err
			}
			if moved, err = deployFollowPort(tx, ch, scope, *in.desired.Port, a.Now()); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		fail(err)
		return
	}
	if moved {
		deployProxySync(a)
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
		org, err := deployOrgOf(tx, n.EnvironmentID)
		if err != nil {
			return err
		}
		ch.Environment(org, n.EnvironmentID)
		return nil
	})
	if err != nil {
		a.Log.Error("set apply error", "node", nodeID, "err", err)
	}
}

func (a *App) ScheduleRemoveService(nodeID string) {
	rt := a.deployRuntime()
	rt.mu.Lock()
	delete(rt.observe, nodeID)
	rt.mu.Unlock()
	a.Jobs.After("remove:"+nodeID, 0, func(ctx context.Context) {
		if !a.noSwarm("remove service") {
			dctx, cancel := context.WithTimeout(ctx, dockerCallDeadline)
			if err := a.Swarm.RemoveService(dctx, nodeID); err != nil {
				a.Log.Error("remove service", "node", nodeID, "err", err)
			}
			cancel()
		}
		a.reconcileRunning(ctx, "")
	})
}

func deployErrorText(err error) string { return compactText(err.Error(), 300) }
