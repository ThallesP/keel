package agent

import (
	"context"
	"io"
)

type Docker interface {
	Info(ctx context.Context) (NodeInfo, error)
	ListSwarmContainers(ctx context.Context) ([]Container, error)
	Events(ctx context.Context, since string) EventStream
	ContainerLogs(ctx context.Context, id, since string) (io.ReadCloser, error)
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

type Event struct {
	Type       string
	Action     string
	ActorID    string
	Attributes map[string]string
	TimeNano   int64
	Raw        []byte
}

type EventStream interface {
	Next() (Event, error)
	Close() error
}

const (
	labelServiceName = "com.docker.swarm.service.name"
	labelTaskID      = "com.docker.swarm.task.id"
	labelTaskName    = "com.docker.swarm.task.name"
)
