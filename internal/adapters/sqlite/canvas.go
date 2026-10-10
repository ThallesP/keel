package sqlite

import (
	"fmt"
	"strings"

	"github.com/ThallesP/keel/internal/adapters/sqlite/db"
	"github.com/ThallesP/keel/internal/app"
	"github.com/ThallesP/keel/internal/domain"
)

// Implements app.CanvasTx.

// canvasTaken maps a UNIQUE violation to app.ErrCanvasTaken (keeping the driver text).
func canvasTaken(err error) error {
	if err != nil && strings.Contains(err.Error(), "UNIQUE constraint failed") {
		return fmt.Errorf("%w: %v", app.ErrCanvasTaken, err)
	}
	return err
}

func variablesOf(rows []db.Variable) []domain.Variable {
	out := make([]domain.Variable, 0, len(rows))
	for _, r := range rows {
		out = append(out, domain.Variable{ID: r.ID, NodeID: r.NodeID, Key: r.Key, Value: r.Value, Secret: r.Secret != 0})
	}
	return out
}

func (t *tx) CanvasOrganizationExists() (bool, error) {
	n, err := t.q.CanvasCountOrganizations(t.ctx)
	return n > 0, err
}

func (t *tx) CanvasProjects(organizationID string) ([]domain.Project, error) {
	rows, err := t.q.CanvasListProjects(t.ctx, organizationID)
	if err != nil {
		return nil, err
	}
	out := make([]domain.Project, 0, len(rows))
	for _, p := range rows {
		out = append(out, projectOf(p))
	}
	return out, nil
}

func (t *tx) CanvasProjectBySlug(organizationID, slug string) (domain.Project, error) {
	p, err := t.q.CanvasGetProjectBySlug(t.ctx, db.CanvasGetProjectBySlugParams{OrganizationID: organizationID, Slug: slug})
	if err != nil {
		return domain.Project{}, noRow(err)
	}
	return projectOf(p), nil
}

func (t *tx) CanvasInsertProject(p domain.Project) error {
	return canvasTaken(t.q.CanvasInsertProject(t.ctx, db.CanvasInsertProjectParams{
		ID: p.ID, OrganizationID: p.OrganizationID, Name: p.Name, Slug: p.Slug, CreatedAt: p.CreatedAt,
	}))
}

func (t *tx) CanvasEnvironments(projectID string) ([]domain.Environment, error) {
	rows, err := t.q.CanvasListEnvironments(t.ctx, projectID)
	if err != nil {
		return nil, err
	}
	out := make([]domain.Environment, 0, len(rows))
	for _, e := range rows {
		out = append(out, environmentOf(e))
	}
	return out, nil
}

func (t *tx) CanvasInsertEnvironment(e domain.Environment) error {
	return t.q.CanvasInsertEnvironment(t.ctx, db.CanvasInsertEnvironmentParams{
		ID: e.ID, ProjectID: e.ProjectID, Name: e.Name, IsProduction: b2i(e.IsProduction), CreatedAt: e.CreatedAt,
	})
}

func (t *tx) CanvasClusterServers() (int, error) {
	rows, err := t.q.CanvasGetClusterServers(t.ctx)
	if err != nil || len(rows) == 0 {
		return 0, err
	}
	return int(rows[0]), nil
}

func (t *tx) CanvasInsertNode(n domain.Node) error { return canvasTaken(t.InsertNode(n)) }

func (t *tx) CanvasUpdateNode(n domain.Node) error { return canvasTaken(t.UpdateNode(n)) }

func (t *tx) CanvasMarkDirty(nodeID string) error { return t.q.CanvasMarkDirty(t.ctx, nodeID) }

func (t *tx) CanvasVariables(nodeID string) ([]domain.Variable, error) {
	rows, err := t.q.CanvasListVariables(t.ctx, nodeID)
	if err != nil {
		return nil, err
	}
	return variablesOf(rows), nil
}

func (t *tx) CanvasEnvironmentVariables(environmentID string) ([]domain.Variable, error) {
	rows, err := t.q.CanvasListEnvironmentVariables(t.ctx, environmentID)
	if err != nil {
		return nil, err
	}
	return variablesOf(rows), nil
}

func (t *tx) CanvasInsertVariable(v domain.Variable) error {
	return canvasTaken(t.q.CanvasInsertVariable(t.ctx, db.CanvasInsertVariableParams{
		ID: v.ID, NodeID: v.NodeID, Key: v.Key, Value: v.Value, Secret: b2i(v.Secret),
	}))
}

func (t *tx) CanvasUpdateVariable(v domain.Variable) error {
	n, err := t.q.CanvasUpdateVariable(t.ctx, db.CanvasUpdateVariableParams{
		ID: v.ID, Key: v.Key, Value: v.Value, Secret: b2i(v.Secret),
	})
	if err != nil {
		return canvasTaken(err)
	}
	if n == 0 {
		return app.ErrNoRow
	}
	return nil
}

func (t *tx) CanvasDeleteVariable(id string) error { return t.q.CanvasDeleteVariable(t.ctx, id) }

func (t *tx) CanvasDeleteNodeVariables(nodeID string) error {
	return t.q.CanvasDeleteNodeVariables(t.ctx, nodeID)
}
