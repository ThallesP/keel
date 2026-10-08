package app

import (
	"github.com/ThallesP/keel/internal/domain"
)

// Seams the canvas area calls, held in variables so its tests can stand in for the areas that own
// them (auth: joinOrFound; deploy: beginDeployment, ScheduleRemoveService, ScheduleObserve;
// ingress: ScheduleProxySync). Production code never reassigns them. Tests set them through
// nodes_export_test.go.
var (
	canvasJoin = joinOrFound
	canvasShip = (*App).beginDeployment
	// canvasSchedulers are the jobs a node delete kicks after commit.
	canvasSchedulers = func(a *App) CanvasSchedulers {
		return CanvasSchedulers{ProxySync: a.ScheduleProxySync, RemoveService: a.ScheduleRemoveService, Observe: a.ScheduleObserve}
	}
)

// CanvasSchedulers: ScheduleProxySync (ingress), ScheduleRemoveService and ScheduleObserve (deploy).
type CanvasSchedulers struct {
	ProxySync     func()
	RemoveService func(nodeID string)
	Observe       func(nodeID string)
}

// canvasAfterRemove runs after a node delete commits (docs/go/spec/projects.md §10 step 7).
func canvasAfterRemove(a *App, n domain.Node) {
	s := canvasSchedulers(a)
	if len(n.Endpoints) > 0 {
		s.ProxySync()
	}
	if n.Desired != nil {
		s.RemoveService(n.ID)
		// Cancels the pending observe and settles a running deployment that waits on the node
		// ("<label>: node deleted") instead of letting it time out after 5 minutes.
		s.Observe(n.ID)
	}
}

// canvasTouch records that the environment changed in a way every node sub-resource can show:
// its canvas, summary and deployments (/api/environments/<env>) and each node's views
// (/api/nodes/<id>: variables resolve references across nodes). extra names nodes that are gone
// from the table already (a delete). docs/go/spec/web-data.md §10.2 Environment.
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

// canvasMoved: only node positions changed (web-data.md §10.2 Canvas): the canvas list, nothing
// else reads a position.
func canvasMoved(ch *Changes, org, environmentID string) {
	ch.Add(org, "/api/environments/"+environmentID+"/nodes")
}

// canvasNames is the set of node names of an environment.
func canvasNames(nodes []domain.Node) map[string]bool {
	taken := make(map[string]bool, len(nodes))
	for _, n := range nodes {
		taken[n.Name] = true
	}
	return taken
}

// canvasCreatedAt keeps creation order exact within an environment: the node list is ordered by
// created_at, so a node created in the same millisecond as another goes 1 ms after it.
func canvasCreatedAt(now int64, siblings []domain.Node) int64 {
	for _, n := range siblings {
		if n.CreatedAt >= now {
			now = n.CreatedAt + 1
		}
	}
	return now
}

// canvasNameTaken is the message for a node name already used in the environment.
func canvasNameTaken(name string) error {
	return domain.E(domain.CodeNameTaken, "\"%s\" is already taken", name)
}
