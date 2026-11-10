package http

import (
	"context"
	"net/http"

	"github.com/danielgtaylor/huma/v2"

	"github.com/ThallesP/keel/internal/api"
)

type registryOutput struct{ Body api.Registry }

func (s *Server) registerRegistry(h huma.API) {
	op(h, huma.Operation{
		OperationID: "getRegistryLogin", Method: http.MethodGet, Path: "/api/registry", Tags: []string{"registry"},
		Summary:     "Credentials and CA for the install's image registry",
		Description: "What `keel registry login` hands to Docker. Any member of the organization may read it.",
	}, func(ctx context.Context, _ *struct{}) (*registryOutput, error) {
		r, err := s.app.RegistryLogin(ActorFrom(ctx))
		if err != nil {
			return nil, err
		}
		return &registryOutput{Body: api.Registry{Host: r.Host, Username: r.Username, Password: r.Password, CA: r.CA}}, nil
	})
}
