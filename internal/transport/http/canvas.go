package http

import (
	"context"
	"net/http"

	"github.com/danielgtaylor/huma/v2"

	"github.com/ThallesP/keel/internal/api"
	"github.com/ThallesP/keel/internal/app"
	"github.com/ThallesP/keel/internal/domain"
)

// Canvas area: projects, environments, nodes, variables (docs/go/ARCHITECTURE.md "Resolved API
// decisions", docs/go/spec/web-data.md §4). Owner: the canvas area.

type canvasIDInput struct {
	ID string `path:"id"`
}

type canvasSlugInput struct {
	Slug string `path:"slug"`
}

type (
	canvasProjectListOut   struct{ Body api.ProjectList }
	canvasProjectOut       struct{ Body api.ProjectSummary }
	canvasDefaultOut       struct{ Body api.DefaultProject }
	canvasProjectBySlugOut struct{ Body api.ProjectBySlug }
	canvasSummaryOut       struct{ Body api.EnvironmentSummaryResult }
	canvasNodeListOut      struct{ Body api.NodeList }
	canvasCreatedNodeOut   struct{ Body api.CreatedNode }
	canvasDuplicatedOut    struct{ Body api.DuplicatedNode }
	canvasStartedOut       struct{ Body api.StartedNode }
	canvasStoppedOut       struct{ Body api.StoppedNode }
	canvasVariableListOut  struct{ Body api.VariableList }
	canvasSourceListOut    struct{ Body api.ReferenceSourceList }
	canvasNoContent        struct{}
)

type canvasCreateProjectInput struct {
	Body api.CreateProjectRequest
}

type canvasCreateNodeInput struct {
	ID   string `path:"id" doc:"Environment id"`
	Body api.CreateNodeRequest
}

type canvasUpdateNodeInput struct {
	ID   string `path:"id"`
	Body api.UpdateNodeRequest
}

type canvasMoveNodeInput struct {
	ID   string `path:"id"`
	Body api.Position
}

type canvasSetVariableInput struct {
	ID   string `path:"id" doc:"Node id"`
	Body api.SetVariableRequest
}

type canvasDeleteVariableInput struct {
	ID   string `path:"id" doc:"Node id"`
	Body api.DeleteVariableRequest
}

func canvasProjectOf(p app.ProjectSummary) api.ProjectSummary {
	out := api.ProjectSummary{ID: p.Project.ID, Name: p.Project.Name, Slug: p.Project.Slug, Environments: []api.ProjectEnvironment{}}
	for _, e := range p.Environments {
		out.Environments = append(out.Environments, api.ProjectEnvironment{ID: e.ID, Name: e.Name, IsProduction: e.IsProduction})
	}
	return out
}

func canvasPosition(p *api.Position) *domain.Position {
	if p == nil {
		return nil
	}
	return &domain.Position{X: p.X, Y: p.Y}
}

func (s *Server) registerCanvas(h huma.API) {
	tags := []string{"canvas"}
	operation := func(id, method, path, summary string) huma.Operation {
		return huma.Operation{OperationID: id, Method: method, Path: path, Summary: summary, Tags: tags}
	}

	// ── Projects ──

	op(h, operation("listProjects", http.MethodGet, "/api/projects",
		"Projects of the caller's organization"),
		func(ctx context.Context, _ *struct{}) (*canvasProjectListOut, error) {
			list, err := s.app.ListProjects(ctx, ActorFrom(ctx))
			if err != nil {
				return nil, err
			}
			out := &canvasProjectListOut{Body: api.ProjectList{Projects: make([]api.ProjectSummary, 0, len(list))}}
			for _, p := range list {
				out.Body.Projects = append(out.Body.Projects, canvasProjectOf(p))
			}
			return out, nil
		})

	create := operation("createProject", http.MethodPost, "/api/projects",
		"Create a project with its production environment (founds the organization on a fresh install)")
	create.DefaultStatus = http.StatusCreated
	op(h, create, func(ctx context.Context, in *canvasCreateProjectInput) (*canvasProjectOut, error) {
		p, err := s.app.CreateProject(ctx, ActorFrom(ctx), in.Body.Name)
		if err != nil {
			return nil, err
		}
		return &canvasProjectOut{Body: canvasProjectOf(p)}, nil
	})

	op(h, operation("ensureDefaultProject", http.MethodPost, "/api/projects/default",
		"The project the dashboard opens first (created on first use)"),
		func(ctx context.Context, _ *struct{}) (*canvasDefaultOut, error) {
			slug, err := s.app.EnsureDefaultProject(ctx, ActorFrom(ctx))
			if err != nil {
				return nil, err
			}
			return &canvasDefaultOut{Body: api.DefaultProject{Slug: slug}}, nil
		})

	op(h, operation("getProjectBySlug", http.MethodGet, "/api/projects/by-slug/{slug}",
		"A project and the environment its canvas opens on"),
		func(ctx context.Context, in *canvasSlugInput) (*canvasProjectBySlugOut, error) {
			p, err := s.app.ProjectBySlug(ctx, ActorFrom(ctx), in.Slug)
			if err != nil {
				return nil, err
			}
			out := &canvasProjectBySlugOut{}
			if p != nil {
				out.Body.Project = &api.ProjectHome{
					ID: p.Project.ID, Name: p.Project.Name, Slug: p.Project.Slug,
					Environment: api.ProjectEnvironmentRef{ID: p.Environment.ID, Name: p.Environment.Name},
				}
			}
			return out, nil
		})

	// ── Environments ──

	op(h, operation("getEnvironmentSummary", http.MethodGet, "/api/environments/{id}/summary",
		"Staged changes, status counts and servers"),
		func(ctx context.Context, in *canvasIDInput) (*canvasSummaryOut, error) {
			sum, err := s.app.EnvironmentSummaryOf(ctx, ActorFrom(ctx), in.ID)
			if err != nil {
				return nil, err
			}
			out := &canvasSummaryOut{}
			if sum != nil {
				counts := make(map[string]int, len(sum.Counts))
				for status, n := range sum.Counts {
					counts[string(status)] = n
				}
				out.Body.Summary = &api.EnvironmentSummary{PendingChanges: sum.PendingChanges, Counts: counts, Servers: sum.Servers}
			}
			return out, nil
		})

	// ── Nodes ──

	op(h, operation("listNodes", http.MethodGet, "/api/environments/{id}/nodes",
		"The canvas: every node of the environment"),
		func(ctx context.Context, in *canvasIDInput) (*canvasNodeListOut, error) {
			nodes, err := s.app.ListNodes(ctx, ActorFrom(ctx), in.ID)
			if err != nil {
				return nil, err
			}
			out := &canvasNodeListOut{Body: api.NodeList{Nodes: make([]api.NodeView, 0, len(nodes))}}
			for _, n := range nodes {
				out.Body.Nodes = append(out.Body.Nodes, api.NodeViewOf(n, s.app.Config.PublicIP))
			}
			return out, nil
		})

	createNode := operation("createNode", http.MethodPost, "/api/environments/{id}/nodes",
		"Add a service, database, cache, volume or group")
	createNode.DefaultStatus = http.StatusCreated
	op(h, createNode, func(ctx context.Context, in *canvasCreateNodeInput) (*canvasCreatedNodeOut, error) {
		b := in.Body
		created, err := s.app.CreateNode(ctx, ActorFrom(ctx), in.ID, app.CreateNodeInput{
			Type: domain.NodeType(b.Type), Name: b.Name, Position: canvasPosition(b.Position),
			Image: b.Image, Engine: domain.Engine(b.Engine), Port: b.Port, Replicas: b.Replicas, Deploy: b.Deploy,
		})
		if err != nil {
			return nil, err
		}
		return &canvasCreatedNodeOut{Body: api.CreatedNode{ID: created.ID, DeploymentID: created.DeploymentID}}, nil
	})

	op(h, operation("updateNode", http.MethodPatch, "/api/nodes/{id}",
		"Rename, change the runtime (image, port, replicas), config, or group"),
		func(ctx context.Context, in *canvasUpdateNodeInput) (*canvasNoContent, error) {
			b := in.Body
			u := app.NodeUpdate{
				Name: b.Name, Image: b.Image, Port: b.Port, Replicas: b.Replicas,
				ParentID: b.ParentID, Position: canvasPosition(b.Position),
			}
			if b.Config != nil {
				u.Config = &domain.NodeConfig{SizeGb: b.Config.SizeGb, Width: b.Config.Width, Height: b.Config.Height}
			}
			return &canvasNoContent{}, s.app.UpdateNode(ctx, ActorFrom(ctx), in.ID, u)
		})

	op(h, operation("moveNode", http.MethodPut, "/api/nodes/{id}/position",
		"Canvas position (relative to the node's group)"),
		func(ctx context.Context, in *canvasMoveNodeInput) (*canvasNoContent, error) {
			return &canvasNoContent{}, s.app.MoveNode(ctx, ActorFrom(ctx), in.ID, domain.Position{X: in.Body.X, Y: in.Body.Y})
		})

	duplicate := operation("duplicateNode", http.MethodPost, "/api/nodes/{id}/duplicate",
		"Copy a node with its variables")
	duplicate.DefaultStatus = http.StatusCreated
	op(h, duplicate, func(ctx context.Context, in *canvasIDInput) (*canvasDuplicatedOut, error) {
		id, err := s.app.DuplicateNode(ctx, ActorFrom(ctx), in.ID)
		if err != nil {
			return nil, err
		}
		return &canvasDuplicatedOut{Body: api.DuplicatedNode{ID: id}}, nil
	})

	op(h, operation("startNode", http.MethodPost, "/api/nodes/{id}/start",
		"Deploy a never-shipped node, or start a stopped one"),
		func(ctx context.Context, in *canvasIDInput) (*canvasStartedOut, error) {
			id, err := s.app.StartNode(ctx, ActorFrom(ctx), in.ID)
			if err != nil {
				return nil, err
			}
			return &canvasStartedOut{Body: api.StartedNode{DeploymentID: id}}, nil
		})

	op(h, operation("stopNode", http.MethodPost, "/api/nodes/{id}/stop",
		"Scale to 0 and ship that"),
		func(ctx context.Context, in *canvasIDInput) (*canvasStoppedOut, error) {
			id, ok, err := s.app.StopNode(ctx, ActorFrom(ctx), in.ID)
			if err != nil {
				return nil, err
			}
			out := &canvasStoppedOut{}
			if ok {
				out.Body.DeploymentID = &id
			}
			return out, nil
		})

	op(h, operation("deleteNode", http.MethodDelete, "/api/nodes/{id}",
		"Delete a node and its variables (204 also when already gone)"),
		func(ctx context.Context, in *canvasIDInput) (*canvasNoContent, error) {
			return &canvasNoContent{}, s.app.RemoveNode(ctx, ActorFrom(ctx), in.ID)
		})

	// ── Variables ──

	op(h, operation("listVariables", http.MethodGet, "/api/nodes/{id}/variables",
		"A node's variables with references expanded"),
		func(ctx context.Context, in *canvasIDInput) (*canvasVariableListOut, error) {
			vars, err := s.app.ListVariables(ctx, ActorFrom(ctx), in.ID)
			if err != nil {
				return nil, err
			}
			out := &canvasVariableListOut{Body: api.VariableList{Variables: make([]api.VariableView, 0, len(vars))}}
			for _, v := range vars {
				parts := make([]api.VariablePart, 0, len(v.Parts))
				for _, p := range v.Parts {
					parts = append(parts, api.VariablePartOf(p))
				}
				out.Body.Variables = append(out.Body.Variables, api.VariableView{
					Key: v.Key, Value: v.Value, Resolved: v.Resolved, Secret: v.Secret, ResolvedSecret: v.ResolvedSecret, Parts: parts,
				})
			}
			return out, nil
		})

	op(h, operation("listReferenceableVariables", http.MethodGet, "/api/nodes/{id}/variables/referenceable",
		"What the node's variables can reference"),
		func(ctx context.Context, in *canvasIDInput) (*canvasSourceListOut, error) {
			ref, err := s.app.ReferenceableVariables(ctx, ActorFrom(ctx), in.ID)
			if err != nil {
				return nil, err
			}
			out := &canvasSourceListOut{Body: api.ReferenceSourceList{
				Sources:     make([]api.ReferenceSource, 0, len(ref.Sources)),
				Suggestions: make([]api.ReferenceSuggestion, 0, len(ref.Suggestions)),
			}}
			for _, sg := range ref.Suggestions {
				out.Body.Suggestions = append(out.Body.Suggestions, api.ReferenceSuggestion{NodeID: sg.NodeID, Node: sg.Node, Key: sg.Key, As: sg.As, Value: sg.Value})
			}
			for _, src := range ref.Sources {
				keys := make([]api.ReferenceKey, 0, len(src.Keys))
				for _, k := range src.Keys {
					keys = append(keys, api.ReferenceKey{Key: k.Key, As: k.As, Secret: k.Secret, Provided: k.Provided})
				}
				out.Body.Sources = append(out.Body.Sources, api.ReferenceSource{
					NodeID: src.NodeID, Name: src.Name, Type: string(src.Type), Image: src.Image, Keys: keys,
				})
			}
			return out, nil
		})

	op(h, operation("setVariable", http.MethodPost, "/api/nodes/{id}/variables",
		"Set (upsert) a variable, or rename one with previousKey"),
		func(ctx context.Context, in *canvasSetVariableInput) (*canvasNoContent, error) {
			b := in.Body
			return &canvasNoContent{}, s.app.SetVariable(ctx, ActorFrom(ctx), in.ID, app.SetVariableInput{
				Key: b.Key, Value: b.Value, Secret: b.Secret, PreviousKey: b.PreviousKey,
			})
		})

	op(h, operation("deleteVariable", http.MethodPost, "/api/nodes/{id}/variables/delete",
		"Delete a variable (no-op when missing)"),
		func(ctx context.Context, in *canvasDeleteVariableInput) (*canvasNoContent, error) {
			return &canvasNoContent{}, s.app.RemoveVariable(ctx, ActorFrom(ctx), in.ID, in.Body.Key)
		})
}
