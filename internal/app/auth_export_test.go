package app

import (
	"context"

	"github.com/ThallesP/keel/internal/domain"
)

// AuthJoinOrFoundForTest runs the joinOrFound seam in its own write transaction, for the
// use-case tests in package app_test.
func AuthJoinOrFoundForTest(a *App, ctx context.Context, actor domain.Actor) (domain.Actor, error) {
	var out domain.Actor
	err := a.write(ctx, func(tx Tx, _ *Changes) (err error) {
		out, err = joinOrFound(tx, actor, a.Now())
		return
	})
	return out, err
}
