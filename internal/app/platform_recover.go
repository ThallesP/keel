package app

import (
	"context"
	"runtime/debug"
)

// recoverPart runs one area's part of the start-up recovery pass (Recover). A panic in it is
// logged and the pass goes on: one area's bug must not leave the others' timeouts, proxy sync
// and crons unarmed until the next restart.
func (a *App) recoverPart(ctx context.Context, part func(context.Context)) {
	defer func() {
		if r := recover(); r != nil {
			a.Log.Error("recovery pass: a part panicked, the others still run", "panic", r, "stack", string(debug.Stack()))
		}
	}()
	part(ctx)
}
