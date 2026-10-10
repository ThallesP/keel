package app

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/ThallesP/keel/internal/domain"
)

type deployRuntime struct {
	mu      sync.Mutex
	seq     uint64
	observe map[string]pendingScan
	applies map[string]*applyQueue
}

type pendingScan struct {
	due    int64
	settle int
	gen    uint64
}

type applyQueue struct {
	queued  []applyRequest
	running *runningApply
}

type runningApply struct {
	revision int
	cancel   context.CancelCauseFunc
}

var errApplySuperseded = errors.New("superseded by a newer revision")

var deployRuntimes sync.Map

func (a *App) deployRuntime() *deployRuntime {
	if v, ok := deployRuntimes.Load(a); ok {
		return v.(*deployRuntime)
	}
	v, _ := deployRuntimes.LoadOrStore(a, &deployRuntime{
		observe: map[string]pendingScan{},
		applies: map[string]*applyQueue{},
	})
	return v.(*deployRuntime)
}

func (r *deployRuntime) next() uint64 {
	r.seq++
	return r.seq
}

var (
	deployComputeEnv  = computeEnv
	deployWithTracing = (*App).withTracing
	deployFollowPort  = followPort
	deployProxySync   = (*App).ScheduleProxySync
)

const (
	deployTimeout         = 5 * time.Minute
	observeDebounce       = 500 * time.Millisecond
	observeSettleDelay    = 2 * time.Second
	observeSettleMax      = 2
	observeUpdatingMax    = int(deployTimeout / observeSettleDelay)
	applyDeadline         = 15 * time.Minute
	dockerCallDeadline    = time.Minute
	recentDeploymentsScan = 50
	nodeDeploymentsMax    = 20
)

func deploymentChanged(ch *Changes, org string, d domain.Deployment) {
	ch.Deployment(org, d.EnvironmentID, d.ID)
	for _, s := range d.Steps {
		if s.NodeID != "" {
			ch.Add(org, "/api/nodes/"+s.NodeID)
		}
	}
}

func deployOrgOf(tx Tx, environmentID string) (string, error) {
	org, err := tx.OrganizationOfEnvironment(environmentID)
	if errors.Is(err, ErrNoRow) {
		return "", nil
	}
	return org, err
}
