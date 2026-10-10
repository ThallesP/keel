package app

import (
	"context"

	"github.com/ThallesP/keel/internal/domain"
)

type EnvironmentSummary struct {
	PendingChanges int
	Counts         map[domain.NodeStatus]int
	Servers        int
}

func (a *App) EnvironmentSummaryOf(ctx context.Context, actor domain.Actor, environmentID string) (*EnvironmentSummary, error) {
	var out *EnvironmentSummary
	err := a.read(ctx, func(tx Tx) error {
		_, ok, err := ownedEnvironment(tx, actor, environmentID)
		if err != nil || !ok {
			return err
		}
		nodes, err := tx.Nodes(environmentID)
		if err != nil {
			return err
		}
		out = &EnvironmentSummary{Counts: map[domain.NodeStatus]int{}}
		for _, n := range nodes {
			if n.Type == domain.NodeGroup {
				continue
			}
			if n.Dirty && n.Type.Deployable() {
				out.PendingChanges++
			}
			out.Counts[domain.DeriveStatus(n)]++
		}
		out.Servers, err = tx.ClusterServers()
		return err
	})
	return out, err
}
