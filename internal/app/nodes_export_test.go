package app

import (
	"context"
	"testing"

	"github.com/ThallesP/keel/internal/domain"
)

// Test-only access for the canvas tests (package app_test), compiled only with them.

// CanvasSeams stands in for the seams other areas own. Nil fields keep the real ones.
type CanvasSeams struct {
	Join       func(tx Tx, actor domain.Actor, now int64) (domain.Actor, error)
	Ship       func(a *App, tx Tx, ch *Changes, scope EnvScope, opts ShipOptions) (string, error)
	Schedulers *CanvasSchedulers
}

// StubCanvasSeams replaces the seams for the rest of t. Tests using it must not run in parallel.
func StubCanvasSeams(t testing.TB, s CanvasSeams) {
	join, ship, sched := canvasJoin, canvasShip, canvasSchedulers
	t.Cleanup(func() { canvasJoin, canvasShip, canvasSchedulers = join, ship, sched })
	if s.Join != nil {
		canvasJoin = s.Join
	}
	if s.Ship != nil {
		canvasShip = s.Ship
	}
	if s.Schedulers != nil {
		fixed := *s.Schedulers
		canvasSchedulers = func(*App) CanvasSchedulers { return fixed }
	}
}

// CanvasComputeEnv runs the computeEnv seam on a node.
func (a *App) CanvasComputeEnv(ctx context.Context, nodeID string) (env map[string]string, err error) {
	err = a.read(ctx, func(tx Tx) error {
		n, err := tx.Node(nodeID)
		if err != nil {
			return err
		}
		env, err = computeEnv(tx, n)
		return err
	})
	return env, err
}

// RecoverCanvas runs the canvas part of the start-up pass alone (Recover also runs the other
// areas' parts, which need their ports).
func (a *App) RecoverCanvas(ctx context.Context) { a.recoverCanvas(ctx) }

// CanvasMarkReferrersDirty runs the markReferrersDirty seam on a node and returns the topics.
func (a *App) CanvasMarkReferrersDirty(ctx context.Context, nodeID string) error {
	return a.write(ctx, func(tx Tx, ch *Changes) error {
		n, err := tx.Node(nodeID)
		if err != nil {
			return err
		}
		org, err := tx.OrganizationOfEnvironment(n.EnvironmentID)
		if err != nil {
			return err
		}
		return markReferrersDirty(tx, ch, org, n)
	})
}
