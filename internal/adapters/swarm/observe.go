package swarm

import (
	"context"

	cerrdefs "github.com/containerd/errdefs"
	"github.com/moby/moby/api/types/swarm"
	"github.com/moby/moby/client"

	"github.com/ThallesP/keel/internal/app"
)

func (s *Swarm) ObserveService(ctx context.Context, nodeID string) (*app.SwarmService, []app.SwarmTask, error) {
	type inspected struct {
		svc *app.SwarmService
		err error
	}
	done := make(chan inspected, 1)
	go func() {
		res, err := s.cli.ServiceInspect(ctx, serviceName(nodeID), client.ServiceInspectOptions{})
		switch {
		case cerrdefs.IsNotFound(err):
			done <- inspected{}
		case err != nil:
			done <- inspected{err: err}
		default:
			svc := serviceOf(res.Service)
			done <- inspected{svc: &svc}
		}
	}()
	list, err := s.cli.TaskList(ctx, client.TaskListOptions{
		Filters: make(client.Filters).Add("label", labelService+"="+nodeID),
	})
	in := <-done
	if err != nil {
		return nil, nil, err
	}
	if in.err != nil {
		return nil, nil, in.err
	}
	return in.svc, tasksOf(list.Items), nil
}

func (s *Swarm) ObserveServices(ctx context.Context) ([]app.SwarmService, []app.SwarmTask, error) {
	filter := func() client.Filters { return make(client.Filters).Add("label", labelService) }
	type listed struct {
		items []swarm.Service
		err   error
	}
	done := make(chan listed, 1)
	go func() {
		res, err := s.cli.ServiceList(ctx, client.ServiceListOptions{Filters: filter()})
		done <- listed{res.Items, err}
	}()
	tasks, err := s.cli.TaskList(ctx, client.TaskListOptions{Filters: filter()})
	services := <-done
	if err != nil {
		return nil, nil, err
	}
	if services.err != nil {
		return nil, nil, services.err
	}
	out := make([]app.SwarmService, 0, len(services.items))
	for _, svc := range services.items {
		out = append(out, serviceOf(svc))
	}
	return out, tasksOf(tasks.Items), nil
}

func serviceOf(svc swarm.Service) app.SwarmService {
	out := app.SwarmService{Name: svc.Spec.Name, Labels: svc.Spec.Labels}
	if u := svc.UpdateStatus; u != nil {
		out.UpdateState, out.UpdateMessage = string(u.State), u.Message
	}
	return out
}

func tasksOf(items []swarm.Task) []app.SwarmTask {
	out := make([]app.SwarmTask, 0, len(items))
	for _, t := range items {
		task := app.SwarmTask{
			NodeID:       t.NodeID,
			DesiredState: string(t.DesiredState),
			State:        string(t.Status.State),
			Err:          t.Status.Err,
		}
		if !t.Status.Timestamp.IsZero() {
			task.Timestamp = t.Status.Timestamp.UnixMilli()
		}
		if cs := t.Spec.ContainerSpec; cs != nil {
			task.Labels = cs.Labels
		}
		out = append(out, task)
	}
	return out
}
