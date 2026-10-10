package app

import (
	"cmp"
	"context"
	"errors"

	"github.com/ThallesP/keel/internal/domain"
)

func (a *App) ListNodes(ctx context.Context, actor domain.Actor, environmentID string) ([]domain.Node, error) {
	var nodes []domain.Node
	err := a.read(ctx, func(tx Tx) error {
		_, ok, err := ownedEnvironment(tx, actor, environmentID)
		if err != nil || !ok {
			return err
		}
		nodes, err = tx.Nodes(environmentID)
		return err
	})
	return nodes, err
}

type CreateNodeInput struct {
	Type           domain.NodeType
	Name           string
	Position       *domain.Position
	Image          *string
	Engine         domain.Engine
	Port, Replicas *float64
	Deploy         bool
}

type CreatedNode struct {
	ID           string
	DeploymentID string
}

func (a *App) CreateNode(ctx context.Context, actor domain.Actor, environmentID string, in CreateNodeInput) (CreatedNode, error) {
	var out CreatedNode
	err := a.write(ctx, func(tx Tx, ch *Changes) error {
		scope, err := requireEnvironment(tx, actor, environmentID)
		if err != nil {
			return err
		}
		siblings, err := tx.Nodes(environmentID)
		if err != nil {
			return err
		}
		def, ok := domain.NodeDefaults[in.Type]
		if !ok {
			return domain.Invalid("Unknown node type")
		}
		runtime := def.Image != ""
		if in.Image != nil && in.Type != domain.NodeService {
			return domain.Invalid("Only services take a custom image")
		}
		if (in.Port != nil || in.Replicas != nil) && !runtime {
			return domain.Invalid("This node type has no runtime settings")
		}
		image, port := def.Image, def.Port
		if in.Engine != "" {
			spec, ok := domain.Engines[in.Engine]
			if !ok || spec.Type != in.Type {
				return domain.Invalid("%s is not a %s", in.Engine, in.Type)
			}
			image, port = spec.Image, spec.Port
		}
		if in.Image != nil {
			if err := domain.ValidImage(*in.Image); err != nil {
				return err
			}
			image = *in.Image
		}
		taken := canvasNames(siblings)
		if taken[in.Name] {
			return canvasNameTaken(in.Name)
		}
		name := in.Name
		if name == "" {
			name = domain.UniqueName(domain.NameFromImage(image, def.Name), taken)
		}
		if err := domain.ValidName(name); err != nil {
			return err
		}
		replicas, err := domain.ReplicasNumber(in.Replicas)
		if err != nil {
			return err
		}
		customPort, err := domain.PortNumber(in.Port)
		if err != nil {
			return err
		}
		var desired *domain.Desired
		if runtime {
			desired = &domain.Desired{Image: image, Replicas: 1, Port: cmp.Or(customPort, &port)}
			if replicas != nil {
				desired.Replicas = *replicas
			}
		}
		pos := domain.NextPosition(siblings)
		if in.Position != nil {
			pos = *in.Position
		}
		node := domain.Node{
			ID:            domain.NewID(),
			EnvironmentID: environmentID,
			Type:          in.Type,
			Name:          name,
			Position:      pos,
			Config:        domain.DefaultConfig(in.Type),
			Desired:       desired,
			Dirty:         in.Type.Deployable(),
			CreatedAt:     canvasCreatedAt(a.Now(), siblings),
		}
		if err := tx.InsertNode(node); err != nil {
			return err
		}
		if in.Type == domain.NodeDatabase || in.Type == domain.NodeCache {
			if err := canvasInsertVariables(tx, node.ID, domain.SeedVariables(domain.EngineOf(image))); err != nil {
				return err
			}
		}
		if err := canvasTouch(tx, ch, scope.Org, environmentID); err != nil {
			return err
		}
		out.ID = node.ID
		if !in.Deploy || !in.Type.Deployable() {
			return nil
		}
		id, err := canvasShip(a, tx, ch, scope, ShipOptions{Only: []string{node.ID}})
		if _, refused := errors.AsType[*domain.Error](err); refused {
			return nil
		}
		if err != nil {
			return err
		}
		out.DeploymentID = id
		return nil
	})
	if err != nil {
		return CreatedNode{}, err
	}
	return out, nil
}

type NodeUpdate struct {
	Name           *string
	Image          *string
	Port, Replicas *float64
	Config         *domain.NodeConfig
	ParentID       *string
	Position       *domain.Position
}

func (a *App) UpdateNode(ctx context.Context, actor domain.Actor, id string, u NodeUpdate) error {
	return a.write(ctx, func(tx Tx, ch *Changes) error {
		scope, err := requireNode(tx, actor, id)
		if err != nil {
			return err
		}
		node := scope.Node
		renamed := u.Name != nil && *u.Name != node.Name
		runtime := u.Image != nil || u.Port != nil || u.Replicas != nil
		reparented := u.ParentID != nil && *u.ParentID != node.ParentID
		if !renamed && !runtime && u.Config == nil && !reparented && u.Position == nil {
			return nil
		}
		if renamed {
			if err := canvasRename(tx, &node, *u.Name); err != nil {
				return err
			}
		}
		if runtime {
			if err := canvasSetDesired(&node, u); err != nil {
				return err
			}
		}
		if u.Config != nil {
			if err := canvasSetConfig(&node, *u.Config); err != nil {
				return err
			}
		}
		if reparented {
			if err := canvasSetParent(tx, &node, *u.ParentID); err != nil {
				return err
			}
		}
		if u.Position != nil {
			node.Position = *u.Position
		}
		err = tx.UpdateNode(node)
		if errors.Is(err, ErrCanvasTaken) {
			return canvasNameTaken(node.Name)
		}
		if err != nil {
			return err
		}
		if runtime {
			if err := markReferrersDirty(tx, ch, scope.Org, node); err != nil {
				return err
			}
		}
		return canvasTouch(tx, ch, scope.Org, node.EnvironmentID)
	})
}

func canvasRename(tx Tx, node *domain.Node, name string) error {
	if err := domain.ValidName(name); err != nil {
		return err
	}
	if err := canvasRewriteReferences(tx, *node, func(key string) (string, string) { return name, key }); err != nil {
		return err
	}
	node.Name = name
	return nil
}

func canvasSetDesired(node *domain.Node, u NodeUpdate) error {
	if node.Desired == nil {
		return domain.Invalid("This node type has no runtime settings")
	}
	if u.Image != nil {
		if err := domain.ValidImage(*u.Image); err != nil {
			return err
		}
		node.Desired.Image = *u.Image
	}
	port, err := domain.PortNumber(u.Port)
	if err != nil {
		return err
	}
	replicas, err := domain.ReplicasNumber(u.Replicas)
	if err != nil {
		return err
	}
	node.Desired.Port = cmp.Or(port, node.Desired.Port)
	if replicas != nil {
		node.Desired.Replicas = *replicas
	}
	node.Dirty = true
	return nil
}

func canvasSetConfig(node *domain.Node, c domain.NodeConfig) error {
	positive := func(p *float64) bool { return p == nil || *p > 0 }
	if c.SizeGb != nil && node.Type != domain.NodeVolume {
		return domain.Invalid("Only volumes have a size")
	}
	if (c.Width != nil || c.Height != nil) && node.Type != domain.NodeGroup {
		return domain.Invalid("Only groups have a width and height")
	}
	if !positive(c.SizeGb) {
		return domain.Invalid("Size must be more than 0 GB")
	}
	if !positive(c.Width) || !positive(c.Height) {
		return domain.Invalid("Width and height must be more than 0")
	}
	node.Config.SizeGb = cmp.Or(c.SizeGb, node.Config.SizeGb)
	node.Config.Width = cmp.Or(c.Width, node.Config.Width)
	node.Config.Height = cmp.Or(c.Height, node.Config.Height)
	return nil
}

func canvasSetParent(tx Tx, node *domain.Node, parentID string) error {
	if node.ParentID != "" {
		old, err := tx.Node(node.ParentID)
		if err != nil {
			return err
		}
		node.Position = old.Position.Add(node.Position)
	}
	node.ParentID = parentID
	if parentID == "" {
		return nil
	}
	if node.Type == domain.NodeGroup {
		return domain.Invalid("Groups cannot be nested")
	}
	group, err := tx.Node(parentID)
	if err != nil && !errors.Is(err, ErrNoRow) {
		return err
	}
	if group.EnvironmentID != node.EnvironmentID || group.Type != domain.NodeGroup {
		return domain.Invalid("Parent must be a group in the same environment")
	}
	node.Position = node.Position.Sub(group.Position)
	return nil
}

func (a *App) MoveNode(ctx context.Context, actor domain.Actor, id string, pos domain.Position) error {
	return a.write(ctx, func(tx Tx, ch *Changes) error {
		scope, err := requireNode(tx, actor, id)
		if err != nil {
			return err
		}
		scope.Node.Position = pos
		if err := tx.UpdateNode(scope.Node); err != nil {
			return err
		}
		ch.Add(scope.Org, "/api/environments/"+scope.Environment.ID+"/nodes")
		return nil
	})
}

func (a *App) DuplicateNode(ctx context.Context, actor domain.Actor, id string) (string, error) {
	var copyID string
	err := a.write(ctx, func(tx Tx, ch *Changes) error {
		scope, err := requireNode(tx, actor, id)
		if err != nil {
			return err
		}
		n := scope.Node
		if n.Type == domain.NodeGroup {
			return domain.Invalid("Groups cannot be duplicated")
		}
		siblings, err := tx.Nodes(n.EnvironmentID)
		if err != nil {
			return err
		}
		base := n.Name[:min(len(n.Name), 32)]
		dup := domain.Node{
			ID:            domain.NewID(),
			EnvironmentID: n.EnvironmentID,
			Type:          n.Type,
			Name:          domain.UniqueName(base+"-copy", canvasNames(siblings)),
			ParentID:      n.ParentID,
			Position:      domain.Position{X: n.Position.X + 40, Y: n.Position.Y + 40},
			Config:        n.Config,
			Dirty:         n.Type.Deployable(),
			OneShot:       n.OneShot,
			CreatedAt:     canvasCreatedAt(a.Now(), siblings),
		}
		if n.Desired != nil {
			d := *n.Desired
			d.Revision = 0
			dup.Desired = &d
		}
		if err := tx.InsertNode(dup); err != nil {
			return err
		}
		vars, err := tx.CanvasVariables(n.ID)
		if err != nil {
			return err
		}
		if err := canvasInsertVariables(tx, dup.ID, vars); err != nil {
			return err
		}
		copyID = dup.ID
		return canvasTouch(tx, ch, scope.Org, n.EnvironmentID)
	})
	return copyID, err
}

func (a *App) StartNode(ctx context.Context, actor domain.Actor, id string) (string, error) {
	var deploymentID string
	err := a.write(ctx, func(tx Tx, ch *Changes) error {
		scope, err := requireNode(tx, actor, id)
		if err != nil {
			return err
		}
		node := scope.Node
		if node.Desired == nil {
			return domain.Invalid("This node type cannot be started")
		}
		verb := "start"
		if node.Desired.Revision == 0 {
			verb = "deploy"
		}
		node.Desired.Replicas, node.Dirty = max(node.Desired.Replicas, 1), true
		if err := tx.UpdateNode(node); err != nil {
			return err
		}
		if err := canvasTouch(tx, ch, scope.Org, node.EnvironmentID); err != nil {
			return err
		}
		deploymentID, err = canvasShip(a, tx, ch, scope.EnvScope, ShipOptions{Only: []string{id}, Verb: verb})
		return err
	})
	return deploymentID, err
}

func (a *App) StopNode(ctx context.Context, actor domain.Actor, id string) (string, bool, error) {
	var deploymentID string
	err := a.write(ctx, func(tx Tx, ch *Changes) error {
		scope, err := requireNode(tx, actor, id)
		if err != nil {
			return err
		}
		node := scope.Node
		if node.Desired == nil {
			return domain.Invalid("This node type cannot be stopped")
		}
		if node.Desired.Replicas == 0 {
			return nil
		}
		node.Desired.Replicas, node.Dirty = 0, true
		if err := tx.UpdateNode(node); err != nil {
			return err
		}
		if err := canvasTouch(tx, ch, scope.Org, node.EnvironmentID); err != nil {
			return err
		}
		deploymentID, err = canvasShip(a, tx, ch, scope.EnvScope, ShipOptions{Only: []string{id}, Verb: "stop"})
		return err
	})
	if err != nil {
		return "", false, err
	}
	return deploymentID, deploymentID != "", nil
}

func (a *App) RemoveNode(ctx context.Context, actor domain.Actor, id string) error {
	if err := actor.RequireUser(); err != nil {
		return err
	}
	return a.write(ctx, func(tx Tx, ch *Changes) error {
		scope, ok, err := ownedNode(tx, actor, id)
		if err != nil || !ok {
			return err
		}
		node := scope.Node
		if err := markReferrersDirty(tx, ch, scope.Org, node); err != nil {
			return err
		}
		siblings, err := tx.Nodes(node.EnvironmentID)
		if err != nil {
			return err
		}
		for _, child := range siblings {
			if child.ParentID != id {
				continue
			}
			child.ParentID, child.Position = "", node.Position.Add(child.Position)
			if err := tx.UpdateNode(child); err != nil {
				return err
			}
		}
		if err := tx.DeleteNode(id); err != nil {
			return err
		}
		if err := canvasTouch(tx, ch, scope.Org, node.EnvironmentID); err != nil {
			return err
		}
		ch.Node(scope.Org, node.EnvironmentID, id)
		ch.AfterCommit(func() {
			s := canvasSchedulers(a)
			if len(node.Endpoints) > 0 {
				s.ProxySync()
			}
			if node.Desired != nil {
				s.RemoveService(id)
				s.Observe(id)
			}
		})
		return nil
	})
}
