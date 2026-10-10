package app

import (
	"context"

	"github.com/ThallesP/keel/internal/domain"
)

func AuthJoinOrFoundForTest(a *App, ctx context.Context, actor domain.Actor) (domain.Actor, error) {
	var out domain.Actor
	err := a.write(ctx, func(tx Tx, _ *Changes) (err error) {
		out, err = joinOrFound(tx, actor, a.Now())
		return
	})
	return out, err
}
