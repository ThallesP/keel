package app

import "context"

func (a *App) StubShip(ship func(tx Tx, ch *Changes, scope EnvScope, opts ShipOptions) (string, error)) {
	a.ship = ship
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

func (a *App) CanvasMarkReferrersDirty(ctx context.Context, nodeID string) error {
	return a.write(ctx, func(tx Tx, _ *Changes) error {
		n, err := tx.Node(nodeID)
		if err != nil {
			return err
		}
		return markReferrersDirty(tx, n)
	})
}
