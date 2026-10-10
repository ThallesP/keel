package app

import (
	"context"
	"errors"

	"github.com/ThallesP/keel/internal/domain"
)

// Variables and references (convex/variables.ts, docs/go/spec/projects.md §5, §9.4). Any node type
// may have variables. References resolve at apply time (computeEnv), so every ship sees current
// values; changing what a node provides makes every node referencing it a staged change.

// VariableView is a variable as the Variables tab shows it.
type VariableView struct {
	Key   string
	Value string // as typed
	// Every reference expanded: what the container gets.
	Resolved string
	// The row's own flag (mask Value).
	Secret bool
	// A referenced value is secret (mask Resolved).
	ResolvedSecret bool
	Parts          []domain.RefPart
}

// ReferenceSource is a node the reference picker offers, with the keys it answers to.
type ReferenceSource struct {
	NodeID string
	Name   string
	Type   domain.NodeType
	Image  string // "" when none
	Keys   []ReferenceKey
}

// ReferenceKey: Provided keys are computed (DATABASE_URL, HOST, …), the rest are the node's rows.
type ReferenceKey struct {
	Key      string
	As       string
	Secret   bool
	Provided bool
}

type ReferenceSuggestion struct {
	NodeID string
	Node   string
	Key    string
	As     string
	Value  string
}

type Referenceable struct {
	Sources     []ReferenceSource
	Suggestions []ReferenceSuggestion
}

// SetVariableInput is variables.set's arguments. PreviousKey renames that row instead (nil = Key).
type SetVariableInput struct {
	Key         string
	Value       string
	Secret      bool
	PreviousKey *string
}

// MaxVariableValue is the longest value, in UTF-16 code units (JavaScript length).
const MaxVariableValue = 4096

// canvasResolver loads what resolving references in the environment needs, in one go.
func canvasResolver(tx Tx, environmentID string) (*domain.Resolver, []domain.Node, error) {
	nodes, err := tx.Nodes(environmentID)
	if err != nil {
		return nil, nil, err
	}
	vars, err := tx.CanvasEnvironmentVariables(environmentID)
	if err != nil {
		return nil, nil, err
	}
	return domain.NewResolver(nodes, vars), nodes, nil
}

// ListVariables: the node's variables in row order, references expanded. Empty when the node is
// missing or not the actor's.
func (a *App) ListVariables(ctx context.Context, actor domain.Actor, nodeID string) ([]VariableView, error) {
	out := []VariableView{}
	err := a.read(ctx, func(tx Tx) error {
		scope, ok, err := ownedNode(tx, actor, nodeID)
		if err != nil || !ok {
			return err
		}
		r, _, err := canvasResolver(tx, scope.Node.EnvironmentID)
		if err != nil {
			return err
		}
		for _, row := range r.Own(nodeID) {
			e := r.Expand(scope.Node, row.Value)
			parts := e.Parts
			if parts == nil {
				parts = []domain.RefPart{}
			}
			out = append(out, VariableView{
				Key: row.Key, Value: row.Value, Resolved: e.Resolved,
				Secret: row.Secret, ResolvedSecret: e.Secret, Parts: parts,
			})
		}
		return nil
	})
	return out, err
}

// ReferenceableVariables is what the reference picker offers: every other deployable node of the
// environment in creation order, its provided keys not shadowed by its own rows, then its rows.
// Credentials are never read here (every one at its fallback), so REDIS_URL reads as not secret.
func (a *App) ReferenceableVariables(ctx context.Context, actor domain.Actor, nodeID string) (Referenceable, error) {
	out := Referenceable{Sources: []ReferenceSource{}, Suggestions: []ReferenceSuggestion{}}
	err := a.read(ctx, func(tx Tx) error {
		scope, ok, err := ownedNode(tx, actor, nodeID)
		if err != nil || !ok {
			return err
		}
		r, nodes, err := canvasResolver(tx, scope.Node.EnvironmentID)
		if err != nil {
			return err
		}
		fallback := func(_, fb string) string { return fb }
		suggest := scope.Node.Type == domain.NodeService
		referenced, taken := map[string]bool{}, map[string]bool{}
		for _, row := range r.Own(nodeID) {
			taken[row.Key] = true
			for _, part := range r.Expand(scope.Node, row.Value).Parts {
				if part.Ref != nil && part.Ref.NodeID != "" {
					referenced[part.Ref.NodeID] = true
				}
			}
		}
		for _, n := range nodes {
			if n.ID == nodeID || !n.Type.Deployable() {
				continue
			}
			own := r.Own(n.ID)
			shadowed := map[string]bool{}
			for _, row := range own {
				shadowed[row.Key] = true
			}
			keys := []ReferenceKey{}
			for _, p := range domain.ProvidedKeys(n, fallback) {
				if !shadowed[p.Key] {
					keys = append(keys, ReferenceKey{Key: p.Key, As: domain.SuggestedKey(n.Name, p.Key), Secret: p.Secret, Provided: true})
				}
			}
			for _, row := range own {
				keys = append(keys, ReferenceKey{Key: row.Key, As: row.Key, Secret: row.Secret})
			}
			src := ReferenceSource{NodeID: n.ID, Name: n.Name, Type: n.Type, Keys: keys}
			if n.Desired != nil {
				src.Image = n.Desired.Image
			}
			out.Sources = append(out.Sources, src)
			if suggest && !referenced[n.ID] {
				if s, ok := connectionSuggestion(n, keys); ok && !taken[s.As] {
					out.Suggestions = append(out.Suggestions, s)
				}
			}
		}
		return nil
	})
	return out, err
}

func connectionSuggestion(n domain.Node, keys []ReferenceKey) (ReferenceSuggestion, bool) {
	for _, k := range keys {
		if k.Provided && k.Key != "HOST" && k.Key != "PORT" {
			return ReferenceSuggestion{NodeID: n.ID, Node: n.Name, Key: k.Key, As: k.As, Value: domain.RefText(n.Name, k.Key)}, true
		}
	}
	return ReferenceSuggestion{}, false
}

// SetVariable upserts a variable by key; PreviousKey renames that row (references to it follow,
// `${{ node.KEY }}` elsewhere and `${{ KEY }}` on the node itself). An unknown PreviousKey inserts.
// The node and everything referencing it become staged changes.
func (a *App) SetVariable(ctx context.Context, actor domain.Actor, nodeID string, in SetVariableInput) error {
	return a.write(ctx, func(tx Tx, ch *Changes) error {
		scope, err := requireNode(tx, actor, nodeID)
		if err != nil {
			return err
		}
		if err := domain.ValidEnvKey(in.Key); err != nil {
			return err
		}
		if domain.UTF16Len(in.Value) > MaxVariableValue {
			return domain.Invalid("Value too long")
		}
		previous := in.Key
		if in.PreviousKey != nil {
			previous = *in.PreviousKey
		}
		rows, err := tx.CanvasVariables(nodeID)
		if err != nil {
			return err
		}
		var existing *domain.Variable
		for i := range rows {
			if previous != in.Key && rows[i].Key == in.Key {
				return canvasKeyExists(in.Key)
			}
		}
		for i := range rows {
			if rows[i].Key == previous {
				existing = &rows[i]
				break
			}
		}
		if existing != nil {
			err = tx.CanvasUpdateVariable(domain.Variable{ID: existing.ID, NodeID: nodeID, Key: in.Key, Value: in.Value, Secret: in.Secret})
		} else {
			err = tx.CanvasInsertVariable(domain.Variable{ID: domain.NewID(), NodeID: nodeID, Key: in.Key, Value: in.Value, Secret: in.Secret})
		}
		if errors.Is(err, ErrCanvasTaken) {
			return canvasKeyExists(in.Key)
		}
		if err != nil {
			return err
		}
		node := scope.Node
		if existing != nil && previous != in.Key {
			err := canvasRewriteReferences(tx, node, func(k string) (string, string) {
				if k == previous {
					return node.Name, in.Key
				}
				return node.Name, k
			})
			if err != nil {
				return err
			}
		}
		if err := tx.CanvasMarkDirty(nodeID); err != nil {
			return err
		}
		if err := markReferrersDirty(tx, ch, scope.Org, node); err != nil {
			return err
		}
		return canvasTouch(tx, ch, scope.Org, node.EnvironmentID)
	})
}

func canvasKeyExists(key string) error {
	return domain.E(domain.CodeNameTaken, "%s already exists", key)
}

// RemoveVariable deletes a variable; a missing key is a no-op (nothing staged). References to it
// now resolve to "" and show as missing.
func (a *App) RemoveVariable(ctx context.Context, actor domain.Actor, nodeID, key string) error {
	return a.write(ctx, func(tx Tx, ch *Changes) error {
		scope, err := requireNode(tx, actor, nodeID)
		if err != nil {
			return err
		}
		rows, err := tx.CanvasVariables(nodeID)
		if err != nil {
			return err
		}
		for _, row := range rows {
			if row.Key != key {
				continue
			}
			if err := tx.CanvasDeleteVariable(row.ID); err != nil {
				return err
			}
			if err := tx.CanvasMarkDirty(nodeID); err != nil {
				return err
			}
			if err := markReferrersDirty(tx, ch, scope.Org, scope.Node); err != nil {
				return err
			}
			return canvasTouch(tx, ch, scope.Org, scope.Node.EnvironmentID)
		}
		return nil
	})
}

// canvasRewriteReferences rewrites every reference to node in its environment (`to` gives the new
// name and key for a key); rows whose text does not change are not written.
func canvasRewriteReferences(tx Tx, node domain.Node, to func(oldKey string) (name, key string)) error {
	vars, err := tx.CanvasEnvironmentVariables(node.EnvironmentID)
	if err != nil {
		return err
	}
	for _, v := range vars {
		value := domain.RewriteRefs(v.Value, v.NodeID, node, to)
		if value == v.Value {
			continue
		}
		v.Value = value
		if err := tx.CanvasUpdateVariable(v); err != nil {
			return err
		}
	}
	return nil
}

// computeEnv is the node's container environment: its own variables with every reference
// expanded (docs/go/spec/projects.md §5.4). Provided keys (DATABASE_URL, HOST, …) are not added.
func computeEnv(tx Tx, node domain.Node) (map[string]string, error) {
	r, _, err := canvasResolver(tx, node.EnvironmentID)
	if err != nil {
		return nil, err
	}
	rows := r.Own(node.ID)
	env := make(map[string]string, len(rows))
	for _, row := range rows {
		env[row.Key] = r.Expand(node, row.Value).Resolved
	}
	return env, nil
}

// markReferrersDirty marks every node whose variables reference node, transitively, dirty
// (docs/go/spec/projects.md §5.6). node itself is the caller's to mark. Reads the environment's
// rows as they are now, so call it after writing the change (or, for a delete, before).
func markReferrersDirty(tx Tx, ch *Changes, org string, node domain.Node) error {
	nodes, err := tx.Nodes(node.EnvironmentID)
	if err != nil {
		return err
	}
	vars, err := tx.CanvasEnvironmentVariables(node.EnvironmentID)
	if err != nil {
		return err
	}
	marked := domain.Referrers(nodes, vars, node.ID)
	for _, id := range marked {
		if err := tx.CanvasMarkDirty(id); err != nil {
			return err
		}
	}
	if len(marked) > 0 {
		return canvasTouch(tx, ch, org, node.EnvironmentID)
	}
	return nil
}
