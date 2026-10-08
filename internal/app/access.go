package app

import (
	"errors"

	"github.com/ThallesP/keel/internal/domain"
)

// Access rules (convex/access.ts). Everything a member sees belongs to their organization; a
// foreign or missing row is the same "not found" so ids reveal nothing.

// EnvScope is an environment the actor may touch, with what it belongs to.
type EnvScope struct {
	Org         string
	Project     domain.Project
	Environment domain.Environment
}

// NodeScope is a node the actor may touch.
type NodeScope struct {
	EnvScope
	Node domain.Node
}

// ownedProject: the project when it is in the actor's organization, else ok=false.
func ownedProject(tx Tx, actor domain.Actor, id string) (domain.Project, bool, error) {
	if !actor.System && actor.OrganizationID == "" {
		return domain.Project{}, false, nil
	}
	p, err := tx.Project(id)
	if errors.Is(err, ErrNoRow) {
		return domain.Project{}, false, nil
	}
	if err != nil {
		return domain.Project{}, false, err
	}
	if !actor.System && p.OrganizationID != actor.OrganizationID {
		return domain.Project{}, false, nil
	}
	return p, true, nil
}

func ownedEnvironment(tx Tx, actor domain.Actor, id string) (EnvScope, bool, error) {
	env, err := tx.Environment(id)
	if errors.Is(err, ErrNoRow) {
		return EnvScope{}, false, nil
	}
	if err != nil {
		return EnvScope{}, false, err
	}
	p, ok, err := ownedProject(tx, actor, env.ProjectID)
	if err != nil || !ok {
		return EnvScope{}, false, err
	}
	return EnvScope{Org: p.OrganizationID, Project: p, Environment: env}, true, nil
}

func ownedNode(tx Tx, actor domain.Actor, id string) (NodeScope, bool, error) {
	n, err := tx.Node(id)
	if errors.Is(err, ErrNoRow) {
		return NodeScope{}, false, nil
	}
	if err != nil {
		return NodeScope{}, false, err
	}
	scope, ok, err := ownedEnvironment(tx, actor, n.EnvironmentID)
	if err != nil || !ok {
		return NodeScope{}, false, err
	}
	return NodeScope{EnvScope: scope, Node: n}, true, nil
}

// requireEnvironment: signed out → NOT_AUTHENTICATED; missing/foreign → "Environment not found".
func requireEnvironment(tx Tx, actor domain.Actor, id string) (EnvScope, error) {
	if err := actor.RequireUser(); err != nil {
		return EnvScope{}, err
	}
	scope, ok, err := ownedEnvironment(tx, actor, id)
	if err != nil {
		return EnvScope{}, err
	}
	if !ok {
		// PROJECT_NOT_FOUND: what the CLI branches on (cli-install.md C1).
		return EnvScope{}, domain.E(domain.CodeProjectNotFound, domain.MsgEnvironmentNotFound)
	}
	return scope, nil
}

// requireNode: signed out → NOT_AUTHENTICATED; missing/foreign → "Node not found" (code
// SERVICE_NOT_FOUND, which is what the CLI branches on).
func requireNode(tx Tx, actor domain.Actor, id string) (NodeScope, error) {
	if err := actor.RequireUser(); err != nil {
		return NodeScope{}, err
	}
	scope, ok, err := ownedNode(tx, actor, id)
	if err != nil {
		return NodeScope{}, err
	}
	if !ok {
		return NodeScope{}, domain.E(domain.CodeServiceNotFound, domain.MsgNodeNotFound)
	}
	return scope, nil
}
