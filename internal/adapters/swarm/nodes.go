package swarm

import (
	"context"

	"github.com/moby/moby/api/types/swarm"
	"github.com/moby/moby/client"
)

func (s *Swarm) Servers(ctx context.Context) (ready, total int, err error) {
	res, err := s.cli.NodeList(ctx, client.NodeListOptions{})
	if err != nil {
		return 0, 0, err
	}
	for _, n := range res.Items {
		if n.Status.State == swarm.NodeStateReady {
			ready++
		}
	}
	return ready, len(res.Items), nil
}
