package app

import (
	"errors"

	"github.com/ThallesP/keel/internal/domain"
)

type EnvScope struct {
	Org         string
	Project     domain.Project
	Environment domain.Environment
}

type NodeScope struct {
	EnvScope
	Node domain.Node
}

func ownedEnvironment(tx Tx, actor domain.Actor, id string) (EnvScope, bool, error) {
	if !actor.System && actor.OrganizationID == "" {
		return EnvScope{}, false, nil
	}
	env, err := tx.Environment(id)
	if errors.Is(err, ErrNoRow) {
		return EnvScope{}, false, nil
	}
	if err != nil {
		return EnvScope{}, false, err
	}
	p, err := tx.Project(env.ProjectID)
	if err != nil {
		return EnvScope{}, false, err
	}
	if !actor.System && p.OrganizationID != actor.OrganizationID {
		return EnvScope{}, false, nil
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

func requireEnvironment(tx Tx, actor domain.Actor, id string) (EnvScope, error) {
	if err := actor.RequireUser(); err != nil {
		return EnvScope{}, err
	}
	scope, ok, err := ownedEnvironment(tx, actor, id)
	if err != nil {
		return EnvScope{}, err
	}
	if !ok {
		return EnvScope{}, domain.E(domain.CodeProjectNotFound, domain.MsgEnvironmentNotFound)
	}
	return scope, nil
}

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
