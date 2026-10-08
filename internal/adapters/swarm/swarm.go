// Package swarm drives Docker Swarm through the manager's Docker socket (moby client). It
// implements app.Swarm (deploy area: apply, observe, nodes) and app.LogReader (observability area:
// service logs). Files: swarm.go (client), apply.go/observe.go (deploy), logs.go (observability).
package swarm

import (
	"github.com/moby/moby/client"
)

// Swarm is the Docker client of the control plane.
type Swarm struct {
	cli *client.Client
}

// New connects to DOCKER_HOST (default unix:///var/run/docker.sock).
func New() (*Swarm, error) {
	cli, err := client.New(client.FromEnv)
	if err != nil {
		return nil, err
	}
	return &Swarm{cli: cli}, nil
}

// Client is the underlying moby client.
func (s *Swarm) Client() *client.Client { return s.cli }

func (s *Swarm) Close() error { return s.cli.Close() }
