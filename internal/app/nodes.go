package app

import (
	"context"
	"errors"

	"github.com/ThallesP/keel/internal/domain"
)

// Canvas nodes (convex/nodes.ts, docs/go/spec/projects.md §3, §9.3, §10). Expose / unexpose are
// the ingress area's; Ship is the deploy area's.

// ListNodes is the canvas: every node of the environment in creation order (groups included),
// with its endpoints. Empty when the environment is missing or not the actor's.
func (a *App) ListNodes(ctx context.Context, actor domain.Actor, environmentID string) ([]domain.Node, error) {
	out := []domain.Node{}
	err := a.read(ctx, func(tx Tx) error {
		_, ok, err := ownedEnvironment(tx, actor, environmentID)
		if err != nil || !ok {
			return err
		}
		out, err = tx.Nodes(environmentID)
		return err
	})
	return out, err
}

// CreateNodeInput is nodes.create's arguments.
type CreateNodeInput struct {
	Type domain.NodeType
	// "" = named after what it runs (engine, image repo) or its type, made unique.
	Name string
	// nil = right of the rightmost top-level node (the CLI has no canvas to drop it on).
	Position *domain.Position
	// Services only: the image to run. Non-nil even when empty: "" is a bad image, not "none".
	Image *string
	// Databases and caches: picks image and port.
	Engine domain.Engine
	// Deployable types: instead of the engine's/type's port and 1 replica. JSON numbers, so 80.5
	// gets the port message rather than a decoding error.
	Port, Replicas *float64
	// Ship right away. Skipped silently when a deployment is already running (the node stays dirty).
	Deploy bool
}

// CreatedNode: DeploymentID is "" when no deployment started.
type CreatedNode struct {
	ID           string
	DeploymentID string
}

// CreateNode adds a node to the canvas. Checks run in the Convex order; the first failure wins.
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
		var picked *domain.EngineSpec
		if in.Engine != "" {
			spec, ok := domain.Engines[in.Engine]
			if !ok || spec.Type != in.Type {
				return domain.Invalid("%s is not a %s", in.Engine, in.Type)
			}
			picked = &spec
		}
		image := def.Image
		switch {
		case picked != nil:
			image = picked.Image
		case in.Image != nil:
			if err := domain.ValidImage(*in.Image); err != nil {
				return err
			}
			image = *in.Image
		}
		// Named after what runs (`nginx`, `api-server`), not the node kind (`service`).
		base := string(in.Engine)
		if base == "" {
			base = def.Name
			if image != "" {
				base = domain.NameFromImage(image, def.Name)
			}
		}
		taken := canvasNames(siblings)
		if in.Name != "" && taken[in.Name] {
			return canvasNameTaken(in.Name)
		}
		name := in.Name
		if name != "" {
			if err := domain.ValidName(name); err != nil {
				return err
			}
		} else {
			name = domain.UniqueName(base, taken)
		}
		var desired *domain.Desired
		if runtime {
			replicas, err := domain.ReplicasNumber(in.Replicas)
			if err != nil {
				return err
			}
			port, err := domain.PortNumber(in.Port)
			if err != nil {
				return err
			}
			desired = &domain.Desired{Image: image, Replicas: 1, Port: port}
			if replicas != nil {
				desired.Replicas = *replicas
			}
			if desired.Port == nil {
				p := def.Port
				if picked != nil {
					p = picked.Port
				}
				desired.Port = &p
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
		if err := tx.CanvasInsertNode(node); err != nil {
			if errors.Is(err, ErrCanvasTaken) {
				return canvasNameTaken(name)
			}
			return err
		}
		if in.Type == domain.NodeDatabase || in.Type == domain.NodeCache {
			for _, v := range domain.SeedVariables(domain.EngineOf(desired.Image)) {
				v.ID, v.NodeID = domain.NewID(), node.ID
				if err := tx.CanvasInsertVariable(v); err != nil {
					return err
				}
			}
		}
		if err := canvasTouch(tx, ch, scope.Org, environmentID); err != nil {
			return err
		}
		out.ID = node.ID
		if in.Deploy && in.Type.Deployable() {
			id, err := canvasShip(a, tx, ch, scope, ShipOptions{Only: []string{node.ID}})
			var refused *domain.Error
			switch {
			case errors.As(err, &refused):
				// A deployment is already running: the node stays dirty, no deploymentId.
			case err != nil:
				return err
			default:
				out.DeploymentID = id
			}
		}
		return nil
	})
	if err != nil {
		return CreatedNode{}, err
	}
	return out, nil
}

// NodeUpdate is PATCH /api/nodes/{id}: every field optional, applied in this order in one
// transaction (any failure changes nothing).
type NodeUpdate struct {
	// Rename (convex nodes.rename): references to the node follow; not a staged change.
	Name *string
	// Runtime (convex nodes.setDesired): always a staged change, also for the referrers.
	Image          *string
	Port, Replicas *float64
	// Size of a volume, box of a group. Fields left nil keep their value. Not staged.
	Config *domain.NodeConfig
	// Group membership: a group's id, or "" for the top level. The node keeps its place on the
	// canvas (its position is converted) unless Position is given too.
	ParentID *string
	// Position (relative to the parent) to set along with ParentID; a plain move is MoveNode.
	Position *domain.Position
}

func (u NodeUpdate) runtime() bool { return u.Image != nil || u.Port != nil || u.Replicas != nil }

// UpdateNode applies a NodeUpdate.
func (a *App) UpdateNode(ctx context.Context, actor domain.Actor, id string, u NodeUpdate) error {
	return a.write(ctx, func(tx Tx, ch *Changes) error {
		scope, err := requireNode(tx, actor, id)
		if err != nil {
			return err
		}
		node, changed := scope.Node, false
		if u.Name != nil {
			renamed, err := canvasRename(tx, node, *u.Name)
			if err != nil {
				return err
			}
			changed = changed || renamed.Name != node.Name
			node = renamed
		}
		if u.runtime() {
			if node, err = canvasSetDesired(tx, ch, scope.Org, node, u); err != nil {
				return err
			}
			changed = true
		}
		if u.Config != nil {
			if node, err = canvasSetConfig(tx, node, *u.Config); err != nil {
				return err
			}
			changed = true
		}
		if (u.ParentID != nil && *u.ParentID != node.ParentID) || u.Position != nil {
			if node, err = canvasSetParent(tx, node, u.ParentID, u.Position); err != nil {
				return err
			}
			changed = true
		}
		if !changed {
			return nil
		}
		return canvasTouch(tx, ch, scope.Org, node.EnvironmentID)
	})
}

// canvasRename: same name is a no-op (before validation); references to the node are rewritten
// (`${{ old.KEY }}` → `${{ new.KEY }}`). Endpoints keep their domain; OTEL_SERVICE_NAME follows
// on the node's next ship.
func canvasRename(tx Tx, node domain.Node, name string) (domain.Node, error) {
	if name == node.Name {
		return node, nil
	}
	if err := domain.ValidName(name); err != nil {
		return node, err
	}
	nodes, err := tx.Nodes(node.EnvironmentID)
	if err != nil {
		return node, err
	}
	if canvasNames(nodes)[name] {
		return node, canvasNameTaken(name)
	}
	if err := canvasRewriteReferences(tx, node, func(key string) (string, string) { return name, key }); err != nil {
		return node, err
	}
	node.Name = name
	if err := tx.CanvasUpdateNode(node); err != nil {
		if errors.Is(err, ErrCanvasTaken) {
			return node, canvasNameTaken(name)
		}
		return node, err
	}
	return node, nil
}

// canvasSetDesired is convex nodes.setDesired: image, port (cannot be unset), replicas; revision
// and tracing kept; dirty even when nothing changed (dirty is sticky, there is no diff).
func canvasSetDesired(tx Tx, ch *Changes, org string, node domain.Node, u NodeUpdate) (domain.Node, error) {
	if node.Desired == nil {
		return node, domain.Invalid("This node type has no runtime settings")
	}
	d := *node.Desired
	if u.Image != nil {
		if err := domain.ValidImage(*u.Image); err != nil {
			return node, err
		}
		d.Image = *u.Image
	}
	port, err := domain.PortNumber(u.Port)
	if err != nil {
		return node, err
	}
	if port != nil {
		d.Port = port
	}
	replicas, err := domain.ReplicasNumber(u.Replicas)
	if err != nil {
		return node, err
	}
	if replicas != nil {
		d.Replicas = *replicas
	}
	node.Desired, node.Dirty = &d, true
	if err := tx.CanvasUpdateNode(node); err != nil {
		return node, err
	}
	return node, markReferrersDirty(tx, ch, org, node)
}

// canvasSetConfig: volumes have a size, groups a box. Not a staged change: neither reaches Swarm.
func canvasSetConfig(tx Tx, node domain.Node, c domain.NodeConfig) (domain.Node, error) {
	positive := func(p *float64) bool { return p == nil || *p > 0 }
	if c.SizeGb != nil && node.Type != domain.NodeVolume {
		return node, domain.Invalid("Only volumes have a size")
	}
	if (c.Width != nil || c.Height != nil) && node.Type != domain.NodeGroup {
		return node, domain.Invalid("Only groups have a width and height")
	}
	if !positive(c.SizeGb) {
		return node, domain.Invalid("Size must be more than 0 GB")
	}
	if !positive(c.Width) || !positive(c.Height) {
		return node, domain.Invalid("Width and height must be more than 0")
	}
	cfg := node.Config
	if c.SizeGb != nil {
		cfg.SizeGb = deployPtr(*c.SizeGb)
	}
	if c.Width != nil {
		cfg.Width = deployPtr(*c.Width)
	}
	if c.Height != nil {
		cfg.Height = deployPtr(*c.Height)
	}
	node.Config = cfg
	return node, tx.CanvasUpdateNode(node)
}

// canvasSetParent moves a node into a group ("" = out of it). Groups do not nest (the dashboard
// renders parents before children by putting groups first). Without an explicit position the node
// keeps its place: positions are relative to the parent.
func canvasSetParent(tx Tx, node domain.Node, parentID *string, pos *domain.Position) (domain.Node, error) {
	if parentID != nil && *parentID != node.ParentID {
		abs := node.Position
		if node.ParentID != "" {
			if old, err := tx.Node(node.ParentID); err == nil {
				abs = domain.Position{X: old.Position.X + abs.X, Y: abs.Y + old.Position.Y}
			} else if !errors.Is(err, ErrNoRow) {
				return node, err
			}
		}
		if *parentID == "" {
			node.ParentID, node.Position = "", abs
		} else {
			if node.Type == domain.NodeGroup {
				return node, domain.Invalid("Groups cannot be nested")
			}
			group, err := tx.Node(*parentID)
			if errors.Is(err, ErrNoRow) || (err == nil && (group.EnvironmentID != node.EnvironmentID || group.Type != domain.NodeGroup)) {
				return node, domain.Invalid("Parent must be a group in the same environment")
			}
			if err != nil {
				return node, err
			}
			node.ParentID = group.ID
			node.Position = domain.Position{X: abs.X - group.Position.X, Y: abs.Y - group.Position.Y}
		}
	}
	if pos != nil {
		node.Position = *pos
	}
	return node, tx.CanvasUpdateNode(node)
}

// MoveNode writes a canvas position (relative to the node's group when it has one). Any numbers;
// not a staged change. The dashboard applies it optimistically on drag end.
func (a *App) MoveNode(ctx context.Context, actor domain.Actor, id string, pos domain.Position) error {
	return a.write(ctx, func(tx Tx, ch *Changes) error {
		scope, err := requireNode(tx, actor, id)
		if err != nil {
			return err
		}
		node := scope.Node
		node.Position = pos
		if err := tx.CanvasUpdateNode(node); err != nil {
			return err
		}
		canvasMoved(ch, scope.Org, node.EnvironmentID)
		return nil
	})
}

// DuplicateNode copies a node next to the original (+40, +40) as `<name>-copy`: same type,
// group, config, runtime (revision 0, so never shipped) and one-shot flag; variables verbatim in
// order (a duplicated database shares the generated passwords). Observation, endpoints and errors
// are not copied. Returns the copy's id.
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
		base := n.Name
		if len(base) > 32 {
			base = base[:32]
		}
		dup := domain.Node{
			ID:            domain.NewID(),
			EnvironmentID: n.EnvironmentID,
			Type:          n.Type,
			Name:          domain.UniqueName(base+"-copy", canvasNames(siblings)),
			ParentID:      n.ParentID,
			Position:      domain.Position{X: n.Position.X + 40, Y: n.Position.Y + 40},
			Config:        canvasCopyConfig(n.Config),
			Dirty:         n.Type.Deployable(),
			OneShot:       n.OneShot,
			CreatedAt:     canvasCreatedAt(a.Now(), siblings),
		}
		if n.Desired != nil {
			d := *n.Desired
			d.Revision = 0
			if d.Port != nil {
				d.Port = deployPtr(*d.Port)
			}
			dup.Desired = &d
		}
		if err := tx.CanvasInsertNode(dup); err != nil {
			if errors.Is(err, ErrCanvasTaken) {
				return canvasNameTaken(dup.Name)
			}
			return err
		}
		vars, err := tx.CanvasVariables(n.ID)
		if err != nil {
			return err
		}
		for _, v := range vars {
			if err := tx.CanvasInsertVariable(domain.Variable{ID: domain.NewID(), NodeID: dup.ID, Key: v.Key, Value: v.Value, Secret: v.Secret}); err != nil {
				return err
			}
		}
		copyID = dup.ID
		return canvasTouch(tx, ch, scope.Org, n.EnvironmentID)
	})
	return copyID, err
}

func canvasCopyConfig(c domain.NodeConfig) domain.NodeConfig {
	cp := func(p *float64) *float64 {
		if p == nil {
			return nil
		}
		return deployPtr(*p)
	}
	return domain.NodeConfig{SizeGb: cp(c.SizeGb), Width: cp(c.Width), Height: cp(c.Height)}
}

// StartNode scales a node back to 1 replica (when stopped) and ships it: "deploy <name>" for a
// never-shipped node, else "start <name>". Always ships, even when already running. A stopped
// 3-replica service comes back with 1. Returns the deployment id.
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
		d := *node.Desired
		if d.Replicas == 0 {
			d.Replicas = 1
		}
		node.Desired, node.Dirty = &d, true
		if err := tx.CanvasUpdateNode(node); err != nil {
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

// StopNode scales a node to 0 and ships that ("stop <name>"); the Swarm service stays defined.
// ok is false (and nothing is written) when it already is at 0 replicas.
func (a *App) StopNode(ctx context.Context, actor domain.Actor, id string) (deploymentID string, ok bool, err error) {
	err = a.write(ctx, func(tx Tx, ch *Changes) error {
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
		d := *node.Desired
		d.Replicas = 0
		node.Desired, node.Dirty = &d, true
		if err := tx.CanvasUpdateNode(node); err != nil {
			return err
		}
		if err := canvasTouch(tx, ch, scope.Org, node.EnvironmentID); err != nil {
			return err
		}
		deploymentID, err = canvasShip(a, tx, ch, scope.EnvScope, ShipOptions{Only: []string{id}, Verb: "stop"})
		ok = err == nil
		return err
	})
	if err != nil {
		return "", false, err
	}
	return deploymentID, ok, nil
}

// RemoveNode deletes a node with its variables (docs/go/spec/projects.md §10). A missing or
// foreign node is not an error: a multi-select delete races itself. Nodes referencing it become
// staged changes (their references now resolve to ""); its group's children move to the top
// level, keeping their place. After commit: proxy sync if it had endpoints, Swarm service removal
// and an observe (which settles a deployment waiting on it) if it had a runtime. Deployments and
// their steps are kept.
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
		// Before its own rows go: referrers are found through them and through its name.
		if err := markReferrersDirty(tx, ch, scope.Org, node); err != nil {
			return err
		}
		if err := tx.CanvasDeleteNodeVariables(id); err != nil {
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
			child.ParentID = ""
			child.Position = domain.Position{X: node.Position.X + child.Position.X, Y: node.Position.Y + child.Position.Y}
			if err := tx.CanvasUpdateNode(child); err != nil {
				return err
			}
		}
		// The row goes before the removal is scheduled: an in-flight apply that re-reads the node
		// after this commit sees nothing and backs out (removing a service it just created).
		if err := tx.DeleteNode(id); err != nil {
			return err
		}
		if err := canvasTouch(tx, ch, scope.Org, node.EnvironmentID, id); err != nil {
			return err
		}
		ch.AfterCommit(func() { canvasAfterRemove(a, node) })
		return nil
	})
}
