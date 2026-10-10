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

var (
	deployComputeEnv = computeEnv
	deployFollowPort = followPort
	deployProxySync  = (*App).ScheduleProxySync
)

const (
	deployTimeout      = 5 * time.Minute
	observeDebounce    = 500 * time.Millisecond
	observeSettleDelay = 2 * time.Second
	dockerCallDeadline = time.Minute
)

func deploymentChanged(ch *Changes, org string, d domain.Deployment) {
	ch.Deployment(org, d.EnvironmentID, d.ID)
	for _, s := range d.Steps {
		if s.NodeID != "" {
			ch.Add(org, "/api/nodes/"+s.NodeID)
		}
	}
}
