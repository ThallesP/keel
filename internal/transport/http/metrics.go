package http

import (
	"context"
	"net/http"

	"github.com/danielgtaylor/huma/v2"

	"github.com/ThallesP/keel/internal/api"
	"github.com/ThallesP/keel/internal/domain"
)

var metricsTags = []string{"metrics"}

type (
	metricsOverviewIn struct {
		ID     string `path:"id" doc:"Environment id"`
		Range  string `query:"range" required:"true" enum:"15m,1h,24h,7d"`
		NodeID string `query:"nodeId" doc:"Only this service"`
	}
	metricsOverviewOut struct{ Body domain.MetricsOverview }
	metricsEnvIn       struct {
		ID string `path:"id" doc:"Environment id"`
	}
	metricsDashboardOut  struct{ Body api.Dashboard }
	metricsSaveDashboard struct {
		ID   string `path:"id" doc:"Environment id"`
		Body api.Dashboard
	}
)

func (s *Server) registerMetrics(h huma.API) {
	op(h, huma.Operation{
		OperationID: "getMetrics", Method: http.MethodGet, Path: "/api/environments/{id}/metrics", Tags: metricsTags,
		Summary: "CPU, memory and network of the environment's services, its servers and its Ships over a range",
	}, func(ctx context.Context, in *metricsOverviewIn) (*metricsOverviewOut, error) {
		o, err := s.app.MetricsOverview(ctx, ActorFrom(ctx), in.ID, domain.TimeRange(in.Range), in.NodeID)
		if err != nil {
			return nil, err
		}
		return &metricsOverviewOut{Body: o}, nil
	})
	op(h, huma.Operation{
		OperationID: "getDashboard", Method: http.MethodGet, Path: "/api/environments/{id}/dashboard", Tags: metricsTags,
		Summary: "The Observability page's panels (the default layout until someone changes it)",
	}, func(ctx context.Context, in *metricsEnvIn) (*metricsDashboardOut, error) {
		panels, err := s.app.Dashboard(ctx, ActorFrom(ctx), in.ID)
		if err != nil {
			return nil, err
		}
		return &metricsDashboardOut{Body: api.Dashboard{Panels: panels}}, nil
	})
	op(h, huma.Operation{
		OperationID: "saveDashboard", Method: http.MethodPut, Path: "/api/environments/{id}/dashboard", Tags: metricsTags,
		Summary: "Replace the Observability page's panels",
	}, func(ctx context.Context, in *metricsSaveDashboard) (*metricsDashboardOut, error) {
		panels, err := s.app.SaveDashboard(ctx, ActorFrom(ctx), in.ID, in.Body.Panels)
		if err != nil {
			return nil, err
		}
		return &metricsDashboardOut{Body: api.Dashboard{Panels: panels}}, nil
	})
}
