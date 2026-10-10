package swarm

import (
	"context"
	"errors"
	"sync"

	cerrdefs "github.com/containerd/errdefs"
	"github.com/moby/moby/api/types/swarm"
	"github.com/moby/moby/client"

	"github.com/ThallesP/keel/internal/app"
)

func (s *Swarm) ObserveService(ctx context.Context, nodeID string) (app.SwarmService, []app.SwarmTask, error) {
	var svc app.SwarmService
	var inspectErr error
	var wg sync.WaitGroup
	wg.Go(func() {
		res, err := s.cli.ServiceInspect(ctx, serviceName(nodeID), client.ServiceInspectOptions{})
		if cerrdefs.IsNotFound(err) {
			return
		}
		svc, inspectErr = serviceOf(res.Service), err
	})
	list, err := s.cli.TaskList(ctx, client.TaskListOptions{
		Filters: make(client.Filters).Add("label", labelService+"="+nodeID),
	})
	wg.Wait()
	if err := errors.Join(err, inspectErr); err != nil {
		return app.SwarmService{}, nil, err
	}
	return svc, tasksOf(list.Items), nil
}

func (s *Swarm) ObserveServices(ctx context.Context) ([]app.SwarmService, []app.SwarmTask, error) {
	filter := make(client.Filters).Add("label", labelService)
	var services client.ServiceListResult
	var listErr error
	var wg sync.WaitGroup
	wg.Go(func() { services, listErr = s.cli.ServiceList(ctx, client.ServiceListOptions{Filters: filter}) })
	tasks, err := s.cli.TaskList(ctx, client.TaskListOptions{Filters: filter})
	wg.Wait()
	if err := errors.Join(err, listErr); err != nil {
		return nil, nil, err
	}
	out := make([]app.SwarmService, 0, len(services.Items))
	for _, svc := range services.Items {
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
