package app

import "context"

// Recover is the start-up pass that replaces durable scheduling: observe everything, re-arm
// deployment timeouts, proxy sync, data migrations. serve calls it once. Each area adds its part
// in its own file as a function named recover<Area>(ctx) and calls it from here.
// Owner: foundation (the list); areas (their parts).
func (a *App) Recover(ctx context.Context) {
	for _, fn := range []func(context.Context){a.recoverCanvas, a.recoverDeploy, a.recoverIngress, a.recoverObservability} {
		a.recoverPart(ctx, fn)
	}
}
