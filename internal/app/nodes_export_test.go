package app

import (
	"context"
	"testing"

	"github.com/ThallesP/keel/internal/domain"
)

type CanvasSeams struct {
	Join       func(tx Tx, actor domain.Actor, now int64) (domain.Actor, error)
	Ship       func(a *App, tx Tx, ch *Changes, scope EnvScope, opts ShipOptions) (string, error)
	Schedulers *CanvasSchedulers
}

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

func (a *App) RecoverCanvas(ctx context.Context) { a.recoverCanvas(ctx) }

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
