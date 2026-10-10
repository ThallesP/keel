package agent

import (
	"context"
	"fmt"
	"io"
	"net/http"

	"github.com/moby/moby/api/types/events"
	"github.com/moby/moby/client"
)

type MobyDocker struct {
	cli *client.Client
}

func NewMobyDocker() (*MobyDocker, error) {
	cli, err := client.New(client.FromEnv, client.WithHTTPRequestHook(readOnly))
	if err != nil {
		return nil, err
	}
	return &MobyDocker{cli: cli}, nil
}

func readOnly(r *http.Request) error {
	if r.Method == http.MethodGet || r.Method == http.MethodHead {
		return nil
	}
	return fmt.Errorf("keel agent is read-only on Docker: refusing %s %s", r.Method, r.URL.Path)
}

func (d *MobyDocker) Close() error { return d.cli.Close() }

func (d *MobyDocker) Info(ctx context.Context) (NodeInfo, error) {
	res, err := d.cli.Info(ctx, client.InfoOptions{})
	if err != nil {
		return NodeInfo{}, err
	}
	return NodeInfo{NodeID: res.Info.Swarm.NodeID, Name: res.Info.Name}, nil
}

func (d *MobyDocker) ListSwarmContainers(ctx context.Context) ([]Container, error) {
	res, err := d.cli.ContainerList(ctx, client.ContainerListOptions{
		All: true,
		Filters: make(client.Filters).
			Add("label", labelServiceName).
			Add("status", "running", "exited"),
	})
	if err != nil {
		return nil, err
	}
	out := make([]Container, 0, len(res.Items))
	for _, c := range res.Items {
		out = append(out, Container{ID: c.ID, Labels: c.Labels, State: string(c.State)})
	}
	return out, nil
}

func (d *MobyDocker) Events(ctx context.Context, since string) EventStream {
	return mobyEvents(d.cli.Events(ctx, client.EventsListOptions{
		Since:   since,
		Filters: make(client.Filters).Add("type", "container", "service", "node"),
	}))
}

type mobyEvents client.EventsResult

func (s mobyEvents) Next() (events.Message, error) {
	select {
	case m := <-s.Messages:
		return m, nil
	case err := <-s.Err:
		return events.Message{}, err
	}
}

func (d *MobyDocker) ContainerLogs(ctx context.Context, id, since string) (io.ReadCloser, error) {
	opts := client.ContainerLogsOptions{
		ShowStdout: true,
		ShowStderr: true,
		Timestamps: true,
		Follow:     true,
		Since:      since,
	}
	if since == "" {
		opts.Tail = "0"
	}
	return d.cli.ContainerLogs(ctx, id, opts)
}
