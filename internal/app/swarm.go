package app

import (
	"context"
	"errors"
	"fmt"
	"math/big"
	"runtime/debug"
	"sort"
	"strconv"
	"strings"
	"unicode/utf16"

	"github.com/ThallesP/keel/internal/domain"
)

// apply: desired → Swarm (convex/swarm.ts apply; docs/go/spec/swarm-worker.md §6). Runs as a job,
// outside any transaction: read, release, call Docker, write the outcome.
//
// Applies of one node run one at a time, in the order they were scheduled, and each re-reads
// desired.revision right before createOrUpdate: an apply whose revision has been superseded by a
// newer ship skips (the newer apply, queued behind it, does the work). That fixes the Convex race
// where two applies of one node could leave the older revision applied last (projects.md §7.2).

type applyRequest struct {
	nodeID       string
	deploymentID string // "" = no deployment to report to
	revision     int    // desired.revision this apply ships
	pull         bool   // refresh the image from the registry
}

// scheduleApply queues req behind the node's other applies and starts a drain job when none runs.
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
	key := fmt.Sprintf("apply:%s:%d", req.nodeID, rt.next())
	rt.mu.Unlock()
	if start {
		a.Jobs.After(key, 0, func(ctx context.Context) { a.drainApplies(ctx, req.nodeID) })
	}
}

// drainApplies runs the node's queued applies one after the other until none is left.
func (a *App) drainApplies(ctx context.Context, nodeID string) {
	rt := a.deployRuntime()
	for {
		rt.mu.Lock()
		q := rt.applies[nodeID]
		if q == nil || len(q.queued) == 0 {
			delete(rt.applies, nodeID)
			rt.mu.Unlock()
			return
		}
		req := q.queued[0]
		q.queued = q.queued[1:]
		rt.mu.Unlock()
		a.safeApply(ctx, req)
	}
}

func (a *App) safeApply(ctx context.Context, req applyRequest) {
	defer func() {
		if r := recover(); r != nil {
			a.Log.Error("apply panicked", "node", req.nodeID, "panic", r, "stack", string(debug.Stack()))
		}
	}()
	a.apply(ctx, req)
}

// applyInput is what apply ships (nodesInternal.applyInput), or nil when the node is gone or
// has no desired.
type deployApplyInput struct {
	name    string
	desired domain.Desired
	env     []string
	oneShot bool
}

func (a *App) loadApplyInput(ctx context.Context, nodeID string) (*deployApplyInput, error) {
	var in *deployApplyInput
	err := a.read(ctx, func(tx Tx) error {
		n, err := tx.Node(nodeID)
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
		in = &deployApplyInput{name: n.Name, desired: *n.Desired, env: deployEnvList(env), oneShot: n.OneShot}
		return nil
	})
	return in, err
}

// wantedRevision is the node's desired.revision, or ok=false when the node is gone or has no
// desired (apply's stillWanted).
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

// deployEnvList turns the container environment into Swarm's KEY=value list. The seams return a map,
// so the order is by key (the Convex worker kept variable creation order; Docker does not care).
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

func (a *App) apply(ctx context.Context, req applyRequest) {
	ctx, cancel := context.WithTimeout(ctx, applyDeadline)
	defer cancel()
	id := req.nodeID
	step := func(change stepChange, text string) { a.writeStep(ctx, req.deploymentID, id, change, text) }
	fail := func(err error) {
		text := deployErrorText(err)
		a.setApplyError(ctx, id, text)
		step(stepFailed, "error: "+text)
	}

	in, err := a.loadApplyInput(ctx, id)
	if err != nil {
		fail(err)
		return
	}
	if in == nil {
		return // deleted before we ran; the node delete's reconcile fails the step
	}
	if in.desired.Revision > req.revision {
		a.applySuperseded(ctx, req, in.desired.Revision)
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
			// Registry unreachable but the image is cached: fine. A missing image is not.
			if !cached {
				fail(err)
				return
			}
			step(stepLog, "pull failed ("+deployErrorText(err)+"), using cached image")
		} else {
			step(stepLog, "pulled "+image+" in "+deployToFixed1(float64(a.Now()-t0)/1000)+"s")
		}
	}

	// A pull can take minutes: the node may have been deleted (the delete removes the row before
	// it removes the service, so this read is authoritative) or shipped again meanwhile.
	rev, wanted, err := a.wantedRevision(ctx, id)
	if err != nil {
		fail(err)
		return
	}
	if !wanted {
		return
	}
	if rev > req.revision {
		a.applySuperseded(ctx, req, rev)
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
		// Deleted between the check and the create: the delete's remove already ran against
		// nothing, so take back the service we just made.
		if _, wanted, err := a.wantedRevision(ctx, id); err != nil {
			fail(err)
			return
		} else if !wanted {
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
	err = a.write(ctx, func(tx Tx, ch *Changes) error {
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
		// A shipped port change moves the endpoints that follow the node's port.
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
	// Docker events drive observation from here; this scan lands after stepApplied even if the
	// event burst of the create already went by. It coalesces with any scan they scheduled.
	a.scheduleObserveFor(ctx, id, observeDebounce, 0)
}

// applySuperseded: the node was shipped again since this apply was scheduled; the apply queued
// for the newer revision does the work. Only a finished deployment can be in this state (a new
// ship needs the environment's previous deployment to be over), so failing its step is cosmetic.
func (a *App) applySuperseded(ctx context.Context, req applyRequest, revision int) {
	a.writeStep(ctx, req.deploymentID, req.nodeID, stepFailed, "superseded by revision "+strconv.Itoa(revision))
}

// createOrUpdate creates svc-<id> if missing, else updates it; true when created. Two applies
// racing on one service make the loser's update fail ("update out of sequence"): re-read the
// version and try again, 3 attempts in all. Inspect errors are not retried.
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

// setApplyError sets (or with "" clears) the node's applyError (nodesInternal.setApplyError).
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

// ScheduleRemoveService removes the node's Swarm service (svc-<id>) soon; a missing service is
// fine. It also drops the node's pending scan and, once the service is gone, settles running
// deployments (a deleted node fails its steps with "node deleted" instead of waiting for the
// timeout). Called by: canvas (node delete).
func (a *App) ScheduleRemoveService(nodeID string) {
	rt := a.deployRuntime()
	rt.mu.Lock()
	delete(rt.observe, nodeID)
	rt.mu.Unlock()
	a.Jobs.After("remove:"+nodeID, 0, func(ctx context.Context) {
		if err := a.Swarm.RemoveService(ctx, nodeID); err != nil {
			a.Log.Error("remove service", "node", nodeID, "err", err)
		}
		a.reconcileRunning(ctx, "")
	})
}

// ── Text helpers ────────────────────────────────────────────────────────────────────────────

// deployErrorText is an error as the deploy log and applyError show it: whitespace runs
// collapsed to one space, trimmed, at most 300 UTF-16 units (swarm.ts errorText).
func deployErrorText(err error) string {
	fields := strings.FieldsFunc(err.Error(), deployIsJSSpace)
	s := strings.Join(fields, " ")
	return deployCutUTF16(s, 300)
}

// deployIsJSSpace is ECMAScript's \s: WhiteSpace and LineTerminator.
func deployIsJSSpace(r rune) bool {
	switch r {
	case '\t', '\n', '\v', '\f', '\r', ' ', 0xa0, 0x1680, 0x2028, 0x2029, 0x202f, 0x205f, 0x3000, 0xfeff:
		return true
	}
	return r >= 0x2000 && r <= 0x200a
}

// deployCutUTF16 keeps the first n UTF-16 code units of s (JS String.prototype.slice(0, n)), never
// splitting a surrogate pair.
func deployCutUTF16(s string, n int) string {
	units := 0
	for i, r := range s {
		w := utf16.RuneLen(r)
		if w < 0 {
			w = 1
		}
		if units+w > n {
			return s[:i]
		}
		units += w
	}
	return s
}

// deployToFixed1 is JavaScript's x.toFixed(1) for 0 ≤ x < 1e21: the nearest one-decimal value, the
// larger one on an exact tie (Go's strconv rounds exact ties to even).
func deployToFixed1(x float64) string {
	exact := new(big.Float).SetPrec(200).SetFloat64(x)
	exact.Mul(exact, big.NewFloat(10).SetPrec(200))
	floor, _ := exact.Int(nil) // truncates toward zero; x ≥ 0
	frac := new(big.Float).SetPrec(200).Sub(exact, new(big.Float).SetPrec(200).SetInt(floor))
	if frac.Cmp(big.NewFloat(0.5)) >= 0 {
		floor.Add(floor, big.NewInt(1))
	}
	s := floor.String()
	if len(s) < 2 {
		s = "0" + s
	}
	return s[:len(s)-1] + "." + s[len(s)-1:]
}
