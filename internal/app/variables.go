package app

import (
	"cmp"
	"context"
	"errors"
	"slices"
	"unicode/utf8"

	"github.com/ThallesP/keel/internal/domain"
)

type VariableView struct {
	Key            string
	Value          string
	Resolved       string
	Secret         bool
	ResolvedSecret bool
	Parts          []domain.RefPart
}

type ReferenceSource struct {
	NodeID string
	Name   string
	Type   domain.NodeType
	Image  string
	Keys   []ReferenceKey
}

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

type SetVariableInput struct {
	Key         string
	Value       string
	Secret      bool
	PreviousKey string
}

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

func (a *App) ListVariables(ctx context.Context, actor domain.Actor, nodeID string) ([]VariableView, error) {
	var out []VariableView
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
			out = append(out, VariableView{
				Key: row.Key, Value: row.Value, Resolved: e.Resolved,
				Secret: row.Secret, ResolvedSecret: e.Secret, Parts: e.Parts,
			})
		}
		return nil
	})
	return out, err
}

func (a *App) ReferenceableVariables(ctx context.Context, actor domain.Actor, nodeID string) (Referenceable, error) {
	var out Referenceable
	err := a.read(ctx, func(tx Tx) error {
		scope, ok, err := ownedNode(tx, actor, nodeID)
		if err != nil || !ok {
			return err
		}
		r, nodes, err := canvasResolver(tx, scope.Node.EnvironmentID)
		if err != nil {
			return err
		}
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
			if n.ID == nodeID || !n.Type.Deployable() || n.Desired == nil {
				continue
			}
			own := r.Own(n.ID)
			var keys []ReferenceKey
			for _, p := range domain.ProvidedKeys(n, func(_, fallback string) string { return fallback }) {
				if !slices.ContainsFunc(own, func(v domain.Variable) bool { return v.Key == p.Key }) {
					keys = append(keys, ReferenceKey{Key: p.Key, As: domain.SuggestedKey(n.Name, p.Key), Secret: p.Secret, Provided: true})
				}
			}
			for _, row := range own {
				keys = append(keys, ReferenceKey{Key: row.Key, As: row.Key, Secret: row.Secret})
			}
			out.Sources = append(out.Sources, ReferenceSource{NodeID: n.ID, Name: n.Name, Type: n.Type, Image: n.Desired.Image, Keys: keys})
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

func (a *App) SetVariable(ctx context.Context, actor domain.Actor, nodeID string, in SetVariableInput) error {
	return a.write(ctx, func(tx Tx, ch *Changes) error {
		scope, err := requireNode(tx, actor, nodeID)
		if err != nil {
			return err
		}
		if err := domain.ValidEnvKey(in.Key); err != nil {
			return err
		}
		if utf8.RuneCountInString(in.Value) > 4096 {
			return domain.Invalid("Value too long")
		}
		previous := cmp.Or(in.PreviousKey, in.Key)
		rows, err := tx.CanvasVariables(nodeID)
		if err != nil {
			return err
		}
		row := domain.Variable{ID: domain.NewID(), NodeID: nodeID, Key: in.Key, Value: in.Value, Secret: in.Secret}
		save := tx.CanvasInsertVariable
		i := slices.IndexFunc(rows, func(v domain.Variable) bool { return v.Key == previous })
		if i >= 0 {
			row.ID, save = rows[i].ID, tx.CanvasUpdateVariable
		}
		err = save(row)
		if errors.Is(err, ErrCanvasTaken) {
			return domain.E(domain.CodeNameTaken, "%s already exists", in.Key)
		}
		if err != nil {
			return err
		}
		node := scope.Node
		if i >= 0 && previous != in.Key {
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
		return canvasVariablesChanged(tx, ch, scope)
	})
}

func (a *App) RemoveVariable(ctx context.Context, actor domain.Actor, nodeID, key string) error {
	return a.write(ctx, func(tx Tx, ch *Changes) error {
		scope, err := requireNode(tx, actor, nodeID)
		if err != nil {
			return err
		}
		deleted, err := tx.CanvasDeleteVariable(nodeID, key)
		if err != nil || !deleted {
			return err
		}
		return canvasVariablesChanged(tx, ch, scope)
	})
}

func canvasVariablesChanged(tx Tx, ch *Changes, scope NodeScope) error {
	if err := tx.CanvasMarkDirty(scope.Node.ID); err != nil {
		return err
	}
	if err := markReferrersDirty(tx, scope.Node); err != nil {
		return err
	}
	return canvasTouch(tx, ch, scope.Project.OrganizationID, scope.Node.EnvironmentID)
}

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

func markReferrersDirty(tx Tx, node domain.Node) error {
	nodes, err := tx.Nodes(node.EnvironmentID)
	if err != nil {
		return err
	}
	vars, err := tx.CanvasEnvironmentVariables(node.EnvironmentID)
	if err != nil {
		return err
	}
	for _, id := range domain.Referrers(nodes, vars, node.ID) {
		if err := tx.CanvasMarkDirty(id); err != nil {
			return err
		}
	}
	return nil
}
