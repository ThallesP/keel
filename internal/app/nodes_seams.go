package app

import "github.com/ThallesP/keel/internal/domain"

var (
	canvasJoin       = joinOrFound
	canvasShip       = (*App).beginDeployment
	canvasSchedulers = func(a *App) CanvasSchedulers {
		return CanvasSchedulers{ProxySync: a.ScheduleProxySync, RemoveService: a.ScheduleRemoveService, Observe: a.ScheduleObserve}
	}
)

type CanvasSchedulers struct {
	ProxySync     func()
	RemoveService func(nodeID string)
	Observe       func(nodeID string)
}

func canvasAfterRemove(a *App, n domain.Node) {
	s := canvasSchedulers(a)
	if len(n.Endpoints) > 0 {
		s.ProxySync()
	}
	if n.Desired != nil {
		s.RemoveService(n.ID)
		s.Observe(n.ID)
	}
}

func canvasTouch(tx Tx, ch *Changes, org, environmentID string, extra ...string) error {
	nodes, err := tx.Nodes(environmentID)
	if err != nil {
		return err
	}
	ch.Environment(org, environmentID)
	for _, n := range nodes {
		ch.Add(org, "/api/nodes/"+n.ID)
	}
	for _, id := range extra {
		ch.Add(org, "/api/nodes/"+id)
	}
	return nil
}

func canvasMoved(ch *Changes, org, environmentID string) {
	ch.Add(org, "/api/environments/"+environmentID+"/nodes")
}

func canvasNames(nodes []domain.Node) map[string]bool {
	taken := make(map[string]bool, len(nodes))
	for _, n := range nodes {
		taken[n.Name] = true
	}
	return taken
}

func canvasCreatedAt(now int64, siblings []domain.Node) int64 {
	for _, n := range siblings {
		if n.CreatedAt >= now {
			now = n.CreatedAt + 1
		}
	}
	return now
}

func canvasNameTaken(name string) error {
	return domain.E(domain.CodeNameTaken, "\"%s\" is already taken", name)
}
