package http

import (
	"context"
	"net/http"

	"github.com/danielgtaylor/huma/v2"

	"github.com/ThallesP/keel/internal/api"
	"github.com/ThallesP/keel/internal/app"
	"github.com/ThallesP/keel/internal/domain"
)

var deployTags = []string{"deploy"}

type shipEnvironmentInput struct {
	ID   string           `path:"id" doc:"Environment id"`
	Body *api.ShipRequest `required:"false"`
}

type shipEnvironmentOutput struct{ Body api.ShipResponse }

type environmentDeploymentInput struct {
	ID string `path:"id" doc:"Environment id"`
}

type deploymentInput struct {
	ID string `path:"id" doc:"Deployment id (any string: a malformed one is null, not an error)"`
}

type nodeDeploymentsInput struct {
	ID string `path:"id" doc:"Node id"`
}

type deploymentEnvelope api.DeploymentEnvelope

func (deploymentEnvelope) TransformSchema(r huma.Registry, s *huma.Schema) *huma.Schema {
	s.Properties["deployment"] = &huma.Schema{
		Description: "null when there is none or it is not yours",
		OneOf:       []*huma.Schema{s.Properties["deployment"], {Type: "null"}},
	}
	return s
}

type deploymentEnvelopeOutput struct{ Body deploymentEnvelope }

type nodeDeploymentsOutput struct{ Body []api.Deployment }

func deploymentEnvelopeOf(d *domain.Deployment) deploymentEnvelope {
	if d == nil {
		return deploymentEnvelope{}
	}
	wire := api.DeploymentOf(*d)
	return deploymentEnvelope{Deployment: &wire}
}

func (s *Server) registerDeploy(h huma.API) {
	op(h, huma.Operation{
		OperationID: "shipEnvironment", Method: http.MethodPost, Path: "/api/environments/{id}/deployments",
		Tags: deployTags, Summary: "Ship, redeploy or retry",
		Description: "Without `only`, ships every node with staged changes. With `only`, deploys exactly " +
			"those nodes (`refresh` pulls their images again). One deployment runs per environment at a time.",
	}, func(ctx context.Context, in *shipEnvironmentInput) (*shipEnvironmentOutput, error) {
		var opts app.ShipOptions
		if in.Body != nil {
			opts = app.ShipOptions{Only: in.Body.Only, Refresh: in.Body.Refresh}
		}
		id, err := s.app.ShipEnvironment(ctx, ActorFrom(ctx), in.ID, opts)
		if err != nil {
			return nil, err
		}
		return &shipEnvironmentOutput{Body: api.ShipResponse{ID: id}}, nil
	})

	op(h, huma.Operation{
		OperationID: "getLatestDeployment", Method: http.MethodGet, Path: "/api/environments/{id}/deployments/latest",
		Tags: deployTags, Summary: "The environment's newest deployment",
	}, func(ctx context.Context, in *environmentDeploymentInput) (*deploymentEnvelopeOutput, error) {
		d, err := s.app.LatestDeployment(ctx, ActorFrom(ctx), in.ID)
		if err != nil {
			return nil, err
		}
		return &deploymentEnvelopeOutput{Body: deploymentEnvelopeOf(d)}, nil
	})

	op(h, huma.Operation{
		OperationID: "getDeployment", Method: http.MethodGet, Path: "/api/deployments/{id}",
		Tags: deployTags, Summary: "One deployment",
	}, func(ctx context.Context, in *deploymentInput) (*deploymentEnvelopeOutput, error) {
		d, err := s.app.GetDeployment(ctx, ActorFrom(ctx), in.ID)
		if err != nil {
			return nil, err
		}
		return &deploymentEnvelopeOutput{Body: deploymentEnvelopeOf(d)}, nil
	})

	op(h, huma.Operation{
		OperationID: "listNodeDeployments", Method: http.MethodGet, Path: "/api/nodes/{id}/deployments",
		Tags: deployTags, Summary: "The node's recent deployments",
		Description: "Of the environment's 50 newest deployments, those that shipped this node, at most 20, newest first.",
	}, func(ctx context.Context, in *nodeDeploymentsInput) (*nodeDeploymentsOutput, error) {
		ds, err := s.app.ListNodeDeployments(ctx, ActorFrom(ctx), in.ID)
		if err != nil {
			return nil, err
		}
		out := make([]api.Deployment, 0, len(ds))
		for _, d := range ds {
			out = append(out, api.DeploymentOf(d))
		}
		return &nodeDeploymentsOutput{Body: out}, nil
	})
}
