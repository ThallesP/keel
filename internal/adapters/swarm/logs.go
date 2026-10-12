package swarm

import (
	"cmp"
	"context"
	"io"
	"strconv"

	cerrdefs "github.com/containerd/errdefs"
	"github.com/moby/moby/client"

	"github.com/ThallesP/keel/internal/domain"
)

func (s *Swarm) ReadServiceLogs(ctx context.Context, service string, tail int) ([]byte, bool, error) {
	rc, err := s.cli.ServiceLogs(ctx, service, client.ServiceLogsOptions{
		ShowStdout: true,
		ShowStderr: true,
		Tail:       strconv.Itoa(tail),
		Timestamps: true,
		Details:    true,
	})
	if cerrdefs.IsNotFound(err) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	defer rc.Close()
	body, err := io.ReadAll(rc)
	if err != nil {
		return nil, false, err
	}
	return body, true, nil
}

func (s *Swarm) ListLogReplicas(ctx context.Context, service string) ([]domain.LogReplica, error) {
	res, err := s.cli.TaskList(ctx, client.TaskListOptions{Filters: make(client.Filters).Add("service", service)})
	if err != nil {
		return nil, err
	}
	out := make([]domain.LogReplica, len(res.Items))
	for i, t := range res.Items {
		out[i] = domain.LogReplica{Task: t.ID, Slot: t.Slot, State: cmp.Or(string(t.Status.State), "unknown")}
	}
	return out, nil
}
