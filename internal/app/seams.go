package app

// Cross-area seams. Each function here is called by one area and implemented by another; they are
// stubs in the foundation commit so areas can be built in parallel. The owning area REPLACES the
// stub by moving the function into its own file (and deleting it from here).

import (
	"context"

	"github.com/ThallesP/keel/internal/domain"
)

// ShipOptions are beginDeployment's options (docs/go/spec/projects.md §7.1).
type ShipOptions struct {
	Only    []string // nil = every dirty deployable node; non-nil (even empty) = exactly these
	Refresh bool     // pull the image again
	Verb    string   // message word override ("start", "stop"); "" derives ship/deploy/redeploy
}

// beginDeployment starts a deployment of the environment inside the caller's write transaction
// and registers the applies + timeout with ch.AfterCommit. Errors: DEPLOYMENT_RUNNING,
// NOTHING_TO_SHIP. Called by: canvas (nodes create{deploy}/start/stop), deploy (Ship).
// Owner: deploy.
func (a *App) beginDeployment(tx Tx, ch *Changes, scope EnvScope, opts ShipOptions) (string, error) {
	panic("beginDeployment: implemented by the deploy area")
}

// computeEnv is the node's container environment: its variables with ${{ node.KEY }} references
// expanded (docs/go/spec/projects.md §5.4). Called by: deploy (apply), observability (`keel run`).
// Owner: canvas.
func computeEnv(tx Tx, node domain.Node) (map[string]string, error) {
	panic("computeEnv: implemented by the canvas area")
}

// markReferrersDirty marks every node whose variables reference node (transitively) dirty
// (docs/go/spec/projects.md §5.6). Called by: canvas, observability (tracing switch), migrations.
// Owner: canvas.
func markReferrersDirty(tx Tx, ch *Changes, org string, node domain.Node) error {
	panic("markReferrersDirty: implemented by the canvas area")
}

// withTracing adds the OTEL_* variables to env when the node's tracing switch is on
// (docs/go/spec/observability.md). Called by: deploy (apply). Owner: observability.
func (a *App) withTracing(tx Tx, node domain.Node, env map[string]string) (map[string]string, error) {
	panic("withTracing: implemented by the observability area")
}

// ScheduleObserve observes one node's Swarm service soon, debounced per node (500 ms default,
// docs/go/spec/projects.md §8.6). Called by: canvas (node delete), deploy. Owner: deploy.
func (a *App) ScheduleObserve(nodeID string) {
	panic("ScheduleObserve: implemented by the deploy area")
}

// ScheduleRemoveService removes the node's Swarm service (svc-<id>) soon; a missing service is
// fine. Called by: canvas (node delete). Owner: deploy.
func (a *App) ScheduleRemoveService(nodeID string) {
	panic("ScheduleRemoveService: implemented by the deploy area")
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

func (a *App) recoverDeploy(ctx context.Context)        {} // owner: deploy (replace)
func (a *App) recoverObservability(ctx context.Context) {} // owner: observability (replace)
