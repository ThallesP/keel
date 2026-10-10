package app

import (
	"context"

	"github.com/ThallesP/keel/internal/domain"
)

func (a *App) TimeoutDeploymentForTest(ctx context.Context, id string) { a.timeoutDeployment(ctx, id) }

type DeploySeams struct {
	ComputeEnv func(tx Tx, n domain.Node) (map[string]string, error)
	FollowPort func(tx Tx, ch *Changes, scope NodeScope, port int, now int64) (bool, error)
	ProxySync  func()
}

func UseDeploySeams(s DeploySeams) (restore func()) {
	env, follow, sync := deployComputeEnv, deployFollowPort, deployProxySync
	deployComputeEnv = s.ComputeEnv
	deployFollowPort = s.FollowPort
	deployProxySync = func(*App) { s.ProxySync() }
	return func() {
		deployComputeEnv, deployFollowPort, deployProxySync = env, follow, sync
	}
}
