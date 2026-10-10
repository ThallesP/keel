package sqlite

import (
	"cmp"

	"github.com/ThallesP/keel/internal/app"
	"github.com/ThallesP/keel/internal/domain"
	"github.com/ThallesP/keel/internal/gen/sqlc"
)

func projectOf(p sqlc.Project) domain.Project {
	return domain.Project{ID: p.ID, OrganizationID: p.OrganizationID, Name: p.Name, Slug: p.Slug, CreatedAt: p.CreatedAt}
}

func environmentOf(e sqlc.Environment) domain.Environment {
	return domain.Environment{ID: e.ID, ProjectID: e.ProjectID, Name: e.Name, IsProduction: e.IsProduction != 0, CreatedAt: e.CreatedAt}
}

func (t *tx) Project(id string) (domain.Project, error) {
	p, err := t.q.CoreGetProject(t.ctx, id)
	if err != nil {
		return domain.Project{}, noRow(err)
	}
	return projectOf(p), nil
}

func (t *tx) Environment(id string) (domain.Environment, error) {
	e, err := t.q.CoreGetEnvironment(t.ctx, id)
	if err != nil {
		return domain.Environment{}, noRow(err)
	}
	return environmentOf(e), nil
}

func (t *tx) OrganizationOfEnvironment(environmentID string) (string, error) {
	org, err := t.q.CoreOrganizationOfEnvironment(t.ctx, environmentID)
	return org, noRow(err)
}

func nodeOf(r sqlc.Node) domain.Node {
	n := domain.Node{
		ID:               r.ID,
		EnvironmentID:    r.EnvironmentID,
		Type:             domain.NodeType(r.Type),
		Name:             r.Name,
		ParentID:         str(r.ParentID),
		Position:         domain.Position{X: r.PositionX, Y: r.PositionY},
		Config:           domain.NodeConfig{SizeGb: r.ConfigSizeGb, Width: r.ConfigWidth, Height: r.ConfigHeight},
		DeployedRevision: int(r.DeployedRevision),
		Dirty:            r.Dirty != 0,
		ShippedAt:        r.ShippedAt,
		ApplyError:       r.ApplyError,
		OneShot:          r.OneShot != 0,
		CreatedAt:        r.CreatedAt,
	}
	if r.DesiredImage != nil {
		n.Desired = &domain.Desired{
			Image:    *r.DesiredImage,
			Revision: int(r.DesiredRevision),
			Replicas: int(r.DesiredReplicas),
			Port:     int(r.DesiredPort),
			Tracing:  r.DesiredTracing != 0,
		}
	}
	if r.ObservedAt != nil {
		n.Observed = &domain.Observed{
			Revision:   int(r.ObservedRevision),
			Running:    int(r.ObservedRunning),
			Completed:  int(r.ObservedCompleted),
			FinishedAt: r.ObservedFinishedAt,
			State:      domain.ObservedState(r.ObservedState),
			Error:      r.ObservedError,
			At:         *r.ObservedAt,
		}
	}
	return n
}

func endpointOf(r sqlc.Endpoint) domain.Endpoint {
	return domain.Endpoint{
		ID:         r.ID,
		NodeID:     r.NodeID,
		Protocol:   domain.EndpointProtocol(r.Protocol),
		Port:       int(r.Port),
		PinnedPort: r.PinnedPort != 0,
		Domain:     r.Domain,
		PublicPort: int(r.PublicPort),
		Status:     domain.EndpointStatus{State: domain.EndpointState(r.StatusState), Error: r.StatusError, At: r.StatusAt},
	}
}

func (t *tx) Node(id string) (domain.Node, error) {
	r, err := t.q.CoreGetNode(t.ctx, id)
	if err != nil {
		return domain.Node{}, noRow(err)
	}
	n := nodeOf(r)
	eps, err := t.q.CoreListEndpointsByNode(t.ctx, id)
	if err != nil {
		return domain.Node{}, err
	}
	for _, e := range eps {
		n.Endpoints = append(n.Endpoints, endpointOf(e))
	}
	return n, nil
}

func attach(rows []sqlc.Node, eps []sqlc.Endpoint) []domain.Node {
	byNode := map[string][]domain.Endpoint{}
	for _, e := range eps {
		byNode[e.NodeID] = append(byNode[e.NodeID], endpointOf(e))
	}
	out := make([]domain.Node, 0, len(rows))
	for _, r := range rows {
		n := nodeOf(r)
		n.Endpoints = byNode[r.ID]
		out = append(out, n)
	}
	return out
}

func (t *tx) Nodes(environmentID string) ([]domain.Node, error) {
	rows, err := t.q.CoreListNodesByEnvironment(t.ctx, environmentID)
	if err != nil {
		return nil, err
	}
	eps, err := t.q.CoreListEndpointsByEnvironment(t.ctx, environmentID)
	if err != nil {
		return nil, err
	}
	return attach(rows, eps), nil
}

func (t *tx) AllNodes() ([]domain.Node, error) {
	rows, err := t.q.CoreListAllNodes(t.ctx)
	if err != nil {
		return nil, err
	}
	eps, err := t.q.CoreListAllEndpoints(t.ctx)
	if err != nil {
		return nil, err
	}
	return attach(rows, eps), nil
}

func nodeParams(n domain.Node) sqlc.CoreInsertNodeParams {
	p := sqlc.CoreInsertNodeParams{
		ID:               n.ID,
		EnvironmentID:    n.EnvironmentID,
		Type:             string(n.Type),
		Name:             n.Name,
		ParentID:         nullStr(n.ParentID),
		PositionX:        n.Position.X,
		PositionY:        n.Position.Y,
		ConfigSizeGb:     n.Config.SizeGb,
		ConfigWidth:      n.Config.Width,
		ConfigHeight:     n.Config.Height,
		DeployedRevision: int64(n.DeployedRevision),
		Dirty:            b2i(n.Dirty),
		ShippedAt:        n.ShippedAt,
		ApplyError:       n.ApplyError,
		OneShot:          b2i(n.OneShot),
		CreatedAt:        n.CreatedAt,
	}
	if d := n.Desired; d != nil {
		p.DesiredImage, p.DesiredRevision, p.DesiredReplicas = new(d.Image), int64(d.Revision), int64(d.Replicas)
		p.DesiredPort, p.DesiredTracing = int64(d.Port), b2i(d.Tracing)
	}
	if o := n.Observed; o != nil {
		p.ObservedRevision, p.ObservedRunning, p.ObservedAt, p.ObservedState = int64(o.Revision), int64(o.Running), new(o.At), string(o.State)
		p.ObservedCompleted, p.ObservedFinishedAt, p.ObservedError = int64(o.Completed), o.FinishedAt, o.Error
	}
	return p
}

func (t *tx) InsertNode(n domain.Node) error {
	return canvasTaken(t.q.CoreInsertNode(t.ctx, nodeParams(n)))
}

func (t *tx) UpdateNode(n domain.Node) error {
	res, err := t.q.CoreUpdateNode(t.ctx, sqlc.CoreUpdateNodeParams(nodeParams(n)))
	if err != nil {
		return canvasTaken(err)
	}
	if res == 0 {
		return app.ErrNoRow
	}
	return nil
}

func (t *tx) DeleteNode(id string) error {
	return t.q.CoreDeleteNode(t.ctx, id)
}

func (t *tx) ReplaceEndpoints(nodeID string, eps []domain.Endpoint) error {
	if err := t.q.CoreDeleteEndpointsOfNode(t.ctx, nodeID); err != nil {
		return err
	}
	for i, e := range eps {
		err := t.q.CoreInsertEndpoint(t.ctx, sqlc.CoreInsertEndpointParams{
			ID:          cmp.Or(e.ID, domain.NewID()),
			NodeID:      nodeID,
			Ord:         int64(i),
			Protocol:    string(e.Protocol),
			Port:        int64(e.Port),
			PinnedPort:  b2i(e.PinnedPort),
			Domain:      e.Domain,
			PublicPort:  int64(e.PublicPort),
			StatusState: string(e.Status.State),
			StatusError: e.Status.Error,
			StatusAt:    e.Status.At,
		})
		if err != nil {
			return err
		}
	}
	return nil
}
