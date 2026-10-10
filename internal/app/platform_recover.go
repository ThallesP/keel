package app

import (
	"context"
	"runtime/debug"
)

func (a *App) Recover(ctx context.Context) {
	for _, fn := range []func(context.Context){a.recoverCanvas, a.recoverDeploy, a.recoverIngress, a.recoverObservability} {
		a.recoverPart(ctx, fn)
	}
}

func (a *App) recoverPart(ctx context.Context, part func(context.Context)) {
	defer func() {
		if r := recover(); r != nil {
			a.Log.Error("recovery pass: a part panicked, the others still run", "panic", r, "stack", string(debug.Stack()))
		}
	}()
	part(ctx)
}
