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
}

func UseDeploySeams(t testing.TB, s DeploySeams) {
	env, follow := deployComputeEnv, deployFollowPort
	t.Cleanup(func() { deployComputeEnv, deployFollowPort = env, follow })
	deployComputeEnv = s.ComputeEnv
	deployFollowPort = s.FollowPort
}
