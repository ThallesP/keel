package app

import (
	"context"

	"github.com/ThallesP/keel/internal/domain"
)

// EnvironmentSummary is the Ship button's and status bar's numbers (convex/environments.ts summary).
type EnvironmentSummary struct {
	// Deployable nodes with a staged change: "Ship · N changes".
	PendingChanges int
	// Derived status per node, groups excluded (volumes count as pending). Zero counts are absent.
	Counts map[domain.NodeStatus]int
	// Ready Swarm nodes, install-wide.
	Servers int
}

// EnvironmentSummaryOf is nil when the environment is missing or not the actor's.
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
		s := &EnvironmentSummary{Counts: map[domain.NodeStatus]int{}}
		for _, n := range nodes {
			if n.Type == domain.NodeGroup {
				continue
			}
			if n.Dirty && n.Type.Deployable() {
				s.PendingChanges++
			}
			s.Counts[domain.DeriveStatus(n)]++
		}
		if s.Servers, err = tx.CanvasClusterServers(); err != nil {
			return err
		}
		out = s
		return nil
	})
	return out, err
}
