package http

import (
	"context"
	"net/http"

	"github.com/danielgtaylor/huma/v2"

	"github.com/ThallesP/keel/internal/api"
)

type metaOutput struct{ Body api.Meta }

func (s *Server) registerMeta(h huma.API) {
	op(h, huma.Operation{
		OperationID: "getMeta", Method: http.MethodGet, Path: "/api/meta", Tags: []string{"meta"},
		Summary: "What this install is", Security: []map[string][]string{},
	}, func(ctx context.Context, _ *struct{}) (*metaOutput, error) {
		return &metaOutput{Body: api.Meta{Name: "keel", Version: s.app.Config.Version, SiteURL: s.app.Config.SiteURL}}, nil
	})
}
