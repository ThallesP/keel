package app

// Cross-area seams. Each function here is called by one area and implemented by another; they are
// stubs in the foundation commit so areas can be built in parallel. The owning area REPLACES the
// stub by moving the function into its own file (and deleting it from here).

import (
	"context"

	"github.com/ThallesP/keel/internal/domain"
)

// joinOrFound is the actor's membership, founding the install's organization when there is none
// yet (convex/projects.ts joinOrFound). Returns the actor with OrganizationID/Role set.
// Errors: NOT_AUTHENTICATED; NO_ORGANIZATION when an organization exists and the actor is not in
// it. Called by: canvas (EnsureDefaultProject, CreateProject). Owner: auth.
func joinOrFound(tx Tx, actor domain.Actor, now int64) (domain.Actor, error) {
	panic("joinOrFound: implemented by the auth area")
}

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

// followPort moves every unpinned endpoint of the node to port (docs/go/spec/proxy-ingress.md).
// Returns true when one moved (the caller then calls ScheduleProxySync after commit).
// Called by: deploy (apply). Owner: ingress.
func followPort(tx Tx, ch *Changes, scope NodeScope, port int, now int64) (bool, error) {
	panic("followPort: implemented by the ingress area")
}

// ScheduleProxySync rebuilds and loads keel-proxy's config soon (coalesced). Safe to call often.
// Called by: canvas (node delete), deploy (apply), ingress. Owner: ingress.
func (a *App) ScheduleProxySync() {
	panic("ScheduleProxySync: implemented by the ingress area")
}

// ScheduleObserve observes one node's Swarm service soon, debounced per node (500 ms default,
// docs/go/spec/projects.md §8.6). Called by: canvas (node delete), deploy. Owner: deploy.
func (a *App) ScheduleObserve(nodeID string) {
	panic("ScheduleObserve: implemented by the deploy area")
}

// Recover is the start-up pass that replaces durable scheduling: observe everything, re-arm
// deployment timeouts, proxy sync, data migrations. serve calls it once. Each area adds its part
// in its own file as a function named recover<Area>(ctx) and calls it from here.
// Owner: foundation (the list); areas (their parts).
func (a *App) Recover(ctx context.Context) {
	for _, fn := range []func(context.Context){a.recoverDeploy, a.recoverIngress, a.recoverObservability} {
		fn(ctx)
	}
}

func (a *App) recoverDeploy(ctx context.Context)        {} // owner: deploy (replace)
func (a *App) recoverIngress(ctx context.Context)       {} // owner: ingress (replace)
func (a *App) recoverObservability(ctx context.Context) {} // owner: observability (replace)
