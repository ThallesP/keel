package swarm

import (
	"github.com/moby/moby/client"
)

type Swarm struct {
	cli *client.Client
}

func New() (*Swarm, error) {
	cli, err := client.New(client.FromEnv)
	if err != nil {
		return nil, err
	}
	return &Swarm{cli: cli}, nil
}

func (s *Swarm) Close() error { return s.cli.Close() }
