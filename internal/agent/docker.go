package agent

import (
	"context"
	"io"
	"time"

	"github.com/moby/moby/api/types/events"
)

type Docker interface {
	Info(ctx context.Context) (NodeInfo, error)
	ListSwarmContainers(ctx context.Context) ([]Container, error)
	Events(ctx context.Context, since string) EventStream
	ContainerLogs(ctx context.Context, id, since string) (io.ReadCloser, error)
	ContainerStats(ctx context.Context, id string) (ContainerStats, error)
}

type ContainerStats struct {
	Read        time.Time
	CPUNanos    uint64
	MemoryBytes uint64
	RxBytes     uint64
	TxBytes     uint64
}

type NodeInfo struct {
	NodeID string
	Name   string
}

type Container struct {
	ID     string
	Labels map[string]string
	State  string
}

type EventStream interface {
	Next() (events.Message, error)
}

const (
	labelServiceName = "com.docker.swarm.service.name"
	labelTaskID      = "com.docker.swarm.task.id"
	labelTaskName    = "com.docker.swarm.task.name"
)
