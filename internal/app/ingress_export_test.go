package app

import (
	"context"

	"github.com/ThallesP/keel/internal/domain"
)

func (a *App) FollowPortForTest(ctx context.Context, nodeID string, port int) (moved bool, err error) {
	err = a.write(ctx, func(tx Tx, ch *Changes) error {
		scope, ok, err := ownedNode(tx, domain.SystemActor, nodeID)
		if err != nil || !ok {
			return err
		}
		moved, err = followPort(tx, ch, scope, port, a.Now())
		return err
	})
	return moved, err
}

func (a *App) RecoverIngressForTest(ctx context.Context) { a.recoverIngress(ctx) }
