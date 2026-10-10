package app

import (
	"errors"

	"github.com/ThallesP/keel/internal/domain"
)

// CanvasTx: projects, environments, variables, and the canvas's own node writes (plain node reads
// are in CoreTx). Implemented by adapters/sqlite/canvas.go.
type CanvasTx interface {
	// CanvasOrganizationExists: any organization row at all (projects.list without a membership).
	CanvasOrganizationExists() (bool, error)

	// CanvasProjects: the organization's projects in creation order.
	CanvasProjects(organizationID string) ([]domain.Project, error)
	// CanvasProjectBySlug: ErrNoRow when the organization has no such project.
	CanvasProjectBySlug(organizationID, slug string) (domain.Project, error)
	// CanvasInsertProject: ErrCanvasTaken when the slug is taken in the organization.
	CanvasInsertProject(p domain.Project) error
	// CanvasEnvironments: the project's environments in creation order.
	CanvasEnvironments(projectID string) ([]domain.Environment, error)
	CanvasInsertEnvironment(e domain.Environment) error

	// CanvasClusterServers: ready Swarm nodes as last observed (the single cluster row), 0 when
	// never observed.
	CanvasClusterServers() (int, error)

	// CanvasInsertNode / CanvasUpdateNode are CoreTx's InsertNode / UpdateNode with a name clash
	// in the environment reported as ErrCanvasTaken.
	CanvasInsertNode(n domain.Node) error
	CanvasUpdateNode(n domain.Node) error
	// CanvasMarkDirty sets dirty on one node and nothing else (no stale whole-row write).
	CanvasMarkDirty(nodeID string) error

	// CanvasVariables: the node's variables in row order (creation order; a rename keeps the
	// row's place). That order is the container Env order.
	CanvasVariables(nodeID string) ([]domain.Variable, error)
	// CanvasEnvironmentVariables: every variable of every node of the environment, each node's in
	// row order.
	CanvasEnvironmentVariables(environmentID string) ([]domain.Variable, error)
	// CanvasInsertVariable / CanvasUpdateVariable: ErrCanvasTaken when the key exists on the node.
	CanvasInsertVariable(v domain.Variable) error
	CanvasUpdateVariable(v domain.Variable) error
	CanvasDeleteVariable(id string) error
	CanvasDeleteNodeVariables(nodeID string) error
}

// ErrCanvasTaken: a canvas write hit a uniqueness constraint (project slug per organization, node
// name per environment, variable key per node). The read-checks in the use cases normally catch
// it first; this maps the constraint to the same message.
var ErrCanvasTaken = errors.New("canvas: name taken")
