package app

import (
	"context"
	"testing"

	"github.com/ThallesP/keel/internal/domain"
)

func (a *App) TimeoutDeploymentForTest(ctx context.Context, id string) { a.timeoutDeployment(ctx, id) }

type DeploySeams struct {
	ComputeEnv func(tx Tx, n domain.Node) (map[string]string, error)
	FollowPort func(tx Tx, ch *Changes, scope NodeScope, port int, now int64) (bool, error)
	ProxySync  func()
}

func UseDeploySeams(t testing.TB, s DeploySeams) {
	env, follow, sync := deployComputeEnv, deployFollowPort, deployProxySync
	t.Cleanup(func() { deployComputeEnv, deployFollowPort, deployProxySync = env, follow, sync })
	deployComputeEnv = s.ComputeEnv
	deployFollowPort = s.FollowPort
	deployProxySync = func(*App) { s.ProxySync() }
}
