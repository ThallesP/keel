package app

import "github.com/ThallesP/durable"

type workflows struct {
	deploy *durable.Workflow[deployRun, struct{}]
	apply  *durable.Workflow[applyRequest, struct{}]
	remove *durable.Workflow[removeRun, struct{}]
}

func (a *App) registerWorkflows() workflows {
	return workflows{
		deploy: durable.Register(a.Workflows, "deploy", a.runDeploy),
		apply: durable.Register(a.Workflows, "apply", a.runApply,
			durable.Concurrency(1), durable.PartitionBy(func(req applyRequest) string { return req.NodeID })),
		remove: durable.Register(a.Workflows, "remove", a.runRemove),
	}
}

func startRun[I, O any](a *App, tx Tx, ch *Changes, wf *durable.Workflow[I, O], id string, in I) error {
	run, err := wf.NewRun(in, durable.WithID(id))
	if err != nil {
		return err
	}
	if err := tx.StartRun(run); err != nil {
		return err
	}
	ch.AfterCommit(a.Workflows.Wake)
	return nil
}
