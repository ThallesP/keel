package app

// Cross-area seams. Each function here is called by one area and implemented by another; they are
// stubs in the foundation commit so areas can be built in parallel. The owning area REPLACES the
// stub by moving the function into its own file (and deleting it from here).

import (
	"context"

	"github.com/ThallesP/keel/internal/domain"
)

// withTracing adds the OTEL_* variables to env when the node's tracing switch is on
// (docs/go/spec/observability.md). Called by: deploy (apply). Owner: observability.
func (a *App) withTracing(tx Tx, node domain.Node, env map[string]string) (map[string]string, error) {
	panic("withTracing: implemented by the observability area")
}

// Recover is the start-up pass that replaces durable scheduling: observe everything, re-arm
// deployment timeouts, proxy sync, data migrations. serve calls it once. Each area adds its part
// in its own file as a function named recover<Area>(ctx) and calls it from here.
// Owner: foundation (the list); areas (their parts).
func (a *App) Recover(ctx context.Context) {
	for _, fn := range []func(context.Context){a.recoverDeploy, a.recoverIngress, a.recoverObservability} {
		a.recoverPart(ctx, fn) // a panicking part does not skip the next (platform_recover.go)
	}
}

func (a *App) recoverObservability(ctx context.Context) {} // owner: observability (replace)
