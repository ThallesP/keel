package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"

	"github.com/moby/moby/api/types/events"
	"github.com/moby/moby/client"
)

type MobyDocker struct {
	cli *client.Client
}

func NewMobyDocker(socket string) (*MobyDocker, error) {
	opts := []client.Opt{client.FromEnv, client.WithHTTPRequestHook(readOnly)}
	if socket != "" {
		opts = append(opts, client.WithHost("unix://"+socket))
	}
	return newMobyDocker(opts...)
}

func newMobyDocker(opts ...client.Opt) (*MobyDocker, error) {
	cli, err := client.New(opts...)
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

func (d *MobyDocker) Events(ctx context.Context, since string) (EventStream, error) {
	ctx, cancel := context.WithCancel(ctx)
	res := d.cli.Events(ctx, client.EventsListOptions{
		Since:   since,
		Filters: make(client.Filters).Add("type", "container", "service", "node"),
	})
	select {
	case err := <-res.Err:
		cancel()
		if err == nil {
			err = io.EOF
		}
		return nil, err
	default:
	}
	return &mobyEvents{res: res, cancel: cancel}, nil
}

type mobyEvents struct {
	res    client.EventsResult
	cancel context.CancelFunc
}

func (s *mobyEvents) Next() (Event, error) {
	select {
	case m := <-s.res.Messages:
		return eventOf(m), nil
	case err, ok := <-s.res.Err:
		if !ok || err == nil {
			return Event{}, io.EOF
		}
		return Event{}, err
	}
}

func (s *mobyEvents) Close() error {
	s.cancel()
	return nil
}

func eventOf(m events.Message) Event {
	raw, _ := json.Marshal(m)
	return Event{
		Type:       string(m.Type),
		Action:     string(m.Action),
		ActorID:    m.Actor.ID,
		Attributes: m.Actor.Attributes,
		TimeNano:   m.TimeNano,
		Raw:        raw,
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
