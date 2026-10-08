package app

import (
	"context"

	"github.com/ThallesP/keel/internal/domain"
)

// Test-only access for package app_test (compiled only with this package's tests).

// BeginDeploymentForTest runs beginDeployment (with a verb, as the canvas area's node actions do)
// in its own transaction.
func (a *App) BeginDeploymentForTest(ctx context.Context, environmentID string, opts ShipOptions) (string, error) {
	var id string
	err := a.write(ctx, func(tx Tx, ch *Changes) error {
		scope, ok, err := ownedEnvironment(tx, domain.SystemActor, environmentID)
		if err != nil {
			return err
		}
		if !ok {
			return ErrNoRow
		}
		id, err = a.beginDeployment(tx, ch, scope, opts)
		return err
	})
	return id, err
}

// TimeoutDeploymentForTest runs the 5-minute timeout of a deployment now.
func (a *App) TimeoutDeploymentForTest(ctx context.Context, id string) { a.timeoutDeployment(ctx, id) }

// DeploySeams stand in for other areas' seams that apply calls.
type DeploySeams struct {
	ComputeEnv func(tx Tx, n domain.Node) (map[string]string, error)
	FollowPort func(tx Tx, ch *Changes, scope NodeScope, port int, now int64) (bool, error)
	ProxySync  func()
}

// UseDeploySeams installs s (tracing adds nothing) and returns the restore function.
func UseDeploySeams(s DeploySeams) (restore func()) {
	env, tracing, follow, sync := deployComputeEnv, deployWithTracing, deployFollowPort, deployProxySync
	deployComputeEnv = s.ComputeEnv
	deployWithTracing = func(_ *App, _ Tx, _ domain.Node, env map[string]string) (map[string]string, error) { return env, nil }
	deployFollowPort = s.FollowPort
	deployProxySync = func(*App) { s.ProxySync() }
	return func() {
		deployComputeEnv, deployWithTracing, deployFollowPort, deployProxySync = env, tracing, follow, sync
	}
}
