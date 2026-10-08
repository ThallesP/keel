package swarm

// app.LogReader: the Docker default log provider (docs/go/spec/observability.md §5.1, §12).
// `docker service logs` through the manager's socket: every call fans out to every node running a
// task of the service, and it only holds what the nodes' json-file driver kept. Owner: the
// observability area.

import (
	"context"
	"io"
	"strconv"

	cerrdefs "github.com/containerd/errdefs"
	"github.com/moby/moby/client"

	"github.com/ThallesP/keel/internal/app"
)

var _ app.LogReader = (*Swarm)(nil)

// ReadServiceLogs is the raw, non-follow body of the service's logs (stdout and stderr, the last
// tail lines, timestamps and details on). found=false when the service does not exist.
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

// ListLogReplicas is every task of the service, exited ones included (Swarm keeps history).
func (s *Swarm) ListLogReplicas(ctx context.Context, service string) ([]app.LogReplica, error) {
	res, err := s.cli.TaskList(ctx, client.TaskListOptions{Filters: make(client.Filters).Add("service", service)})
	if err != nil {
		return nil, err
	}
	out := make([]app.LogReplica, len(res.Items))
	for i, t := range res.Items {
		out[i] = app.LogReplica{ID: t.ID, Slot: t.Slot, State: string(t.Status.State)}
	}
	return out, nil
}
