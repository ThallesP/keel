package app

import (
	"context"

	"github.com/ThallesP/keel/internal/domain"
)

// Test hooks for package app_test (the ingress use-case tests run against the real SQLite
// adapter, which package app itself cannot import).

// FollowPortForTest runs the followPort seam in a write transaction, as swarm apply does.
func (a *App) FollowPortForTest(ctx context.Context, nodeID string, port int) (bool, error) {
	var moved bool
	err := a.write(ctx, func(tx Tx, ch *Changes) error {
		scope, ok, err := ownedNode(tx, domain.SystemActor, nodeID)
		if err != nil || !ok {
			return err
		}
		moved, err = followPort(tx, ch, scope, port, a.Now())
		return err
	})
	return moved, err
}

// RecoverIngressForTest runs only the ingress part of Recover.
func (a *App) RecoverIngressForTest(ctx context.Context) { a.recoverIngress(ctx) }
