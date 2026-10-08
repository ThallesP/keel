package agent

import (
	"context"
	"io"
)

// Docker is the slice of the Docker Engine API the agent uses, over the node's socket. Every call
// is a GET: the agent observes, it never mutates the daemon (mutations stay on the manager, in
// `keel serve`). The socket is bind-mounted read-only, but a read-only bind mount does not stop
// API writes on a unix socket, so read-only is a code invariant (MobyDocker enforces it per
// request). Tests use a fake.
type Docker interface {
	// Info is GET /info: the Swarm node id goes into every log event's `node`.
	Info(ctx context.Context) (NodeInfo, error)
	// ListSwarmContainers is GET /containers/json for Swarm task containers, running or exited:
	// filters {"label":["com.docker.swarm.service.name"],"status":["running","exited"]}, all=1.
	ListSwarmContainers(ctx context.Context) ([]Container, error)
	// Events is GET /events, filters {"type":["container","service","node"]}, since when known.
	// It returns once the daemon answered; the stream then runs until EOF, an error or ctx.
	Events(ctx context.Context, since string) (EventStream, error)
	// ContainerLogs is GET /containers/{id}/logs?follow=1&stdout=1&stderr=1&timestamps=1 plus
	// since=<since>, or tail=0 (new lines only) when since is "". The body is Docker's
	// multiplexed stream (raw text for TTY containers). Cancelling ctx closes it.
	ContainerLogs(ctx context.Context, id, since string) (io.ReadCloser, error)
}

// NodeInfo is what the agent reads from GET /info.
type NodeInfo struct {
	NodeID string // Swarm.NodeID
	Name   string
}

// Container is one entry of GET /containers/json.
type Container struct {
	ID     string
	Labels map[string]string
	State  string // "running", "exited", …
}

// Event is one Docker event, with the fields the agent and the control plane use.
type Event struct {
	Type       string
	Action     string
	ActorID    string            // Actor.ID: the container id for container events
	Attributes map[string]string // Actor.Attributes
	TimeNano   int64             // 0 when absent
	// Raw is the event as JSON (Type, Action, Actor, time, timeNano…): the body POSTed to
	// /worker/events.
	Raw []byte
}

// EventStream is a live Docker event stream.
type EventStream interface {
	// Next blocks for the next event. io.EOF when the daemon ended the stream.
	Next() (Event, error)
	Close() error
}

// Labels Swarm puts on task containers.
const (
	labelServiceName = "com.docker.swarm.service.name" // svc-<nodeId>
	labelTaskID      = "com.docker.swarm.task.id"
	labelTaskName    = "com.docker.swarm.task.name" // svc-<nodeId>.<slot>.<taskId>
)
