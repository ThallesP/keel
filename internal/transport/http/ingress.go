package http

import (
	"context"
	"net/http"

	"github.com/danielgtaylor/huma/v2"

	"github.com/ThallesP/keel/internal/api"
	"github.com/ThallesP/keel/internal/app"
	"github.com/ThallesP/keel/internal/domain"
)

// Public ingress: expose / unexpose a node through keel-proxy, and the control plane's public IP.

type ingressExposeInput struct {
	ID   string             `path:"id" doc:"Node id"`
	Body *api.ExposeRequest `required:"false"`
}

type ingressExposeOutput struct{ Body api.EndpointView }

type ingressUnexposeInput struct {
	ID   string               `path:"id" doc:"Node id"`
	Body *api.UnexposeRequest `required:"false"`
}

type ingressControlPlaneOutput struct{ Body api.ControlPlane }

func (s *Server) registerIngress(h huma.API) {
	tags := []string{"ingress"}

	op(h, huma.Operation{
		OperationID: "exposeNode", Method: http.MethodPost, Path: "/api/nodes/{id}/expose", Tags: tags,
		Summary:     "Open a way in from the internet",
		Description: "Immediate, not gated by Ship. Exposing what is already exposed returns the existing endpoint; the same domain (http) or public port (tcp/udp) gets the new container port.",
	}, func(ctx context.Context, in *ingressExposeInput) (*ingressExposeOutput, error) {
		var opts app.ExposeInput
		if b := in.Body; b != nil {
			opts = app.ExposeInput{Protocol: domain.EndpointProtocol(b.Protocol), Port: b.Port, Domain: b.Domain, PublicPort: b.PublicPort}
		}
		ep, err := s.app.Expose(ctx, ActorFrom(ctx), in.ID, opts)
		if err != nil {
			return nil, err
		}
		return &ingressExposeOutput{Body: api.EndpointViewOf(ep, s.app.Config.PublicIP)}, nil
	})

	op(h, huma.Operation{
		OperationID: "unexposeNode", Method: http.MethodPost, Path: "/api/nodes/{id}/unexpose", Tags: tags,
		Summary:     "Close one endpoint, or every one",
		Description: "No selector closes every endpoint (Make private). An unknown endpoint is not an error.",
	}, func(ctx context.Context, in *ingressUnexposeInput) (*struct{}, error) {
		var opts app.UnexposeInput
		if b := in.Body; b != nil {
			opts = app.UnexposeInput{Protocol: domain.EndpointProtocol(b.Protocol), Domain: b.Domain, PublicPort: b.PublicPort}
		}
		return nil, s.app.Unexpose(ctx, ActorFrom(ctx), in.ID, opts)
	})

	op(h, huma.Operation{
		OperationID: "getControlPlane", Method: http.MethodGet, Path: "/api/control-plane", Tags: tags,
		Summary: "The control plane's public address",
	}, func(ctx context.Context, _ *struct{}) (*ingressControlPlaneOutput, error) {
		ip, err := s.app.ControlPlanePublicIP(ActorFrom(ctx))
		if err != nil {
			return nil, err
		}
		out := &ingressControlPlaneOutput{}
		if ip != "" {
			out.Body.PublicIP = &ip
		}
		return out, nil
	})
}
