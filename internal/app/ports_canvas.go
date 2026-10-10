package app

import (
	"errors"

	"github.com/ThallesP/keel/internal/domain"
)

type CanvasTx interface {
	CanvasOrganizationExists() (bool, error)

	CanvasProjects(organizationID string) ([]domain.Project, error)
	CanvasProjectBySlug(organizationID, slug string) (domain.Project, error)
	CanvasInsertProject(p domain.Project) error
	CanvasEnvironments(projectID string) ([]domain.Environment, error)
	CanvasInsertEnvironment(e domain.Environment) error

	CanvasClusterServers() (int, error)

	CanvasInsertNode(n domain.Node) error
	CanvasUpdateNode(n domain.Node) error
	CanvasMarkDirty(nodeID string) error

	CanvasVariables(nodeID string) ([]domain.Variable, error)
	CanvasEnvironmentVariables(environmentID string) ([]domain.Variable, error)
	CanvasInsertVariable(v domain.Variable) error
	CanvasUpdateVariable(v domain.Variable) error
	CanvasDeleteVariable(id string) error
	CanvasDeleteNodeVariables(nodeID string) error
}

var ErrCanvasTaken = errors.New("canvas: name taken")
