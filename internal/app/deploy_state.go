package app

import (
	"errors"
	"sync"
	"time"

	"github.com/ThallesP/keel/internal/domain"
)

// In-memory state of the deploy area: the per-node observe debounce (Convex kept it in
// nodes.observeScheduled + _scheduled_functions) and the per-node apply queues. It lives beside
// App rather than in it because App's fields belong to the foundation; one state per *App.

type deployRuntime struct {
	mu  sync.Mutex
	seq uint64
	// observe: the one pending scan per node (projects.md §8.6).
	observe map[string]pendingScan
	// applies: queued applies per node; a node's applies run one at a time.
	applies map[string]*applyQueue
}

// pendingScan is a scheduled observeNode. gen tells a superseded job (a later settle re-check
// replaced by a sooner event scan) that it no longer owns the node's slot.
type pendingScan struct {
	due    int64
	settle int
	gen    uint64
}

type applyQueue struct {
	queued []applyRequest
}

var deployRuntimes sync.Map // *App → *deployRuntime

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

// Seams owned by other areas, called through variables so this area's tests can stand in for
// them until those areas land (see deploy_export_test.go). Production code never reassigns them.
var (
	deployComputeEnv  = computeEnv
	deployWithTracing = (*App).withTracing
	deployFollowPort  = followPort
	deployProxySync   = (*App).ScheduleProxySync
)

// Timings (projects.md §12).
const (
	deployTimeout         = 5 * time.Minute // DEPLOY_TIMEOUT_MS
	observeDebounce       = 500 * time.Millisecond
	observeSettleDelay    = 2 * time.Second  // SETTLE_MS
	observeSettleMax      = 2                // SETTLE_MAX
	applyDeadline         = 15 * time.Minute // a pull can take minutes (postgres:16 took 224 s)
	recentDeploymentsScan = 50
	nodeDeploymentsMax    = 20
)

// deploymentChanged names what a write to deployment d invalidates: the deployment, its
// environment (latest, canvas) and each shipped node's deployment list.
func deploymentChanged(ch *Changes, org string, d domain.Deployment) {
	ch.Deployment(org, d.EnvironmentID, d.ID)
	for _, s := range d.Steps {
		if s.NodeID != "" {
			ch.Add(org, "/api/nodes/"+s.NodeID)
		}
	}
}

// deployOrgOf is the environment's organization ("" when it is gone).
func deployOrgOf(tx Tx, environmentID string) (string, error) {
	org, err := tx.OrganizationOfEnvironment(environmentID)
	if errors.Is(err, ErrNoRow) {
		return "", nil
	}
	return org, err
}
