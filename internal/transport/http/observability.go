package http

import (
	"context"
	"net/http"

	"github.com/danielgtaylor/huma/v2"

	"github.com/ThallesP/keel/internal/api"
	"github.com/ThallesP/keel/internal/app"
	"github.com/ThallesP/keel/internal/domain"
)

var obsTags = []string{"observability"}

type (
	obsNodeID struct {
		ID string `path:"id" doc:"Node id"`
	}
	obsLogSinkOut       struct{ Body api.LogSinkEnvelope }
	obsConnectAxiomIn   struct{ Body api.ConnectAxiomRequest }
	obsConnectAxiomOut  struct{ Body api.ConnectAxiomResult }
	obsPendingOrgsOut   struct{ Body api.PendingAxiomOrgs }
	obsBeginSignInIn    struct{ Body api.BeginAxiomSignInRequest }
	obsBeginSignInOut   struct{ Body api.BeginAxiomSignInResult }
	obsCompleteSignInIn struct {
		Body api.CompleteAxiomSignInRequest
	}
	obsCompleteSignInOut  struct{ Body api.CompleteAxiomSignInResult }
	obsChooseOrgIn        struct{ Body api.ChooseAxiomOrgRequest }
	obsChooseOrgOut       struct{ Body api.AxiomSinkResult }
	obsLogTailOut         struct{ Body domain.LogTail }
	obsEnvironmentLogsOut struct{ Body domain.EnvironmentLogs }
	obsLogsAroundOut      struct{ Body []domain.EnvironmentLogLine }
	obsTraceOverviewOut   struct{ Body domain.TraceOverview }
	obsTraceOut           struct{ Body domain.Trace }
	obsTracesAroundOut    struct{ Body []domain.TraceSummary }
	obsTracingOut         struct{ Body api.TracingEnvelope }
	obsLocalTracingEnvOut struct{ Body api.LocalTracingEnv }
	obsTracingPromptOut   struct{ Body api.TracingPrompt }
	obsTailNodeLogsIn     struct {
		ID   string  `path:"id" doc:"Node id"`
		Tail float64 `query:"tail" default:"200" doc:"Lines, 1–1000 (clamped)"`
	}
	obsEnvironmentLogsIn struct {
		ID     string  `path:"id" doc:"Environment id"`
		Range  string  `query:"range" enum:"15m,1h,24h,7d" doc:"Only lines in this range (default: the last 30 days)"`
		Search string  `query:"search" doc:"Only lines containing this (case-insensitive; first 200 characters)"`
		Tail   float64 `query:"tail" default:"300" doc:"Newest lines, 1–1000 (clamped)"`
	}
	obsAroundIn struct {
		ID string  `path:"id" doc:"Environment id"`
		At float64 `query:"at" required:"true" doc:"Epoch ms"`
	}
	obsTraceOverviewIn struct {
		ID     string `path:"id" doc:"Environment id"`
		Range  string `query:"range" required:"true" enum:"15m,1h,24h,7d"`
		Search string `query:"search" doc:"Only requests whose root span name or service contains this"`
		NodeID string `query:"nodeId" doc:"Only this service's requests"`
	}
	obsGetTraceIn struct {
		ID      string  `path:"id" doc:"Environment id"`
		TraceID string  `path:"traceId"`
		At      float64 `query:"at" doc:"A moment inside the trace (epoch ms): its root's start or a line's time"`
	}
	obsSetTracingIn struct {
		ID   string `path:"id" doc:"Node id"`
		Body api.SetTracingRequest
	}
	obsTracingPromptIn struct {
		NodeID        string `query:"nodeId"`
		EnvironmentID string `query:"environmentId"`
	}
)

func (s *Server) registerObservability(h huma.API) {
	op(h, huma.Operation{
		OperationID: "getLogSink", Method: http.MethodGet, Path: "/api/organization/log-sink", Tags: obsTags,
		Summary: "The organization's log sink (never the token)",
	}, func(ctx context.Context, _ *struct{}) (*obsLogSinkOut, error) {
		v, err := s.app.LogSink(ctx, ActorFrom(ctx))
		if err != nil {
			return nil, err
		}
		return &obsLogSinkOut{Body: api.LogSinkEnvelope{Sink: v}}, nil
	})
	op(h, huma.Operation{
		OperationID: "disconnectLogSink", Method: http.MethodDelete, Path: "/api/organization/log-sink", Tags: obsTags,
		Summary: "Back to the Docker default for every project", DefaultStatus: http.StatusNoContent,
	}, func(ctx context.Context, _ *struct{}) (*struct{}, error) {
		return nil, s.app.DisconnectLogSink(ctx, ActorFrom(ctx))
	})
	op(h, huma.Operation{
		OperationID: "connectAxiom", Method: http.MethodPost, Path: "/api/organization/log-sink/axiom", Tags: obsTags,
		Summary: "Connect a pasted Axiom API token as the organization's sink",
	}, func(ctx context.Context, in *obsConnectAxiomIn) (*obsConnectAxiomOut, error) {
		b := in.Body
		err := s.app.ConnectAxiom(ctx, ActorFrom(ctx), app.ConnectAxiomInput{Domain: b.Domain, Dataset: b.Dataset, Traces: b.Traces, Token: b.Token})
		if err != nil {
			return nil, err
		}
		return &obsConnectAxiomOut{Body: api.ConnectAxiomResult{Dataset: b.Dataset, Traces: b.Traces}}, nil
	})
	op(h, huma.Operation{
		OperationID: "listPendingAxiomOrgs", Method: http.MethodGet, Path: "/api/organization/axiom/pending-orgs", Tags: obsTags,
		Summary: "Orgs to pick from after a sign-in that saw several",
	}, func(ctx context.Context, _ *struct{}) (*obsPendingOrgsOut, error) {
		orgs, err := s.app.PendingAxiomOrgs(ctx, ActorFrom(ctx))
		if err != nil {
			return nil, err
		}
		return &obsPendingOrgsOut{Body: api.PendingAxiomOrgs{Orgs: orgs}}, nil
	})
	op(h, huma.Operation{
		OperationID: "cancelAxiomSignIn", Method: http.MethodDelete, Path: "/api/organization/axiom/pending", Tags: obsTags,
		Summary: "Drop a pending org pick", DefaultStatus: http.StatusNoContent,
	}, func(ctx context.Context, _ *struct{}) (*struct{}, error) {
		return nil, s.app.CancelAxiomSignIn(ctx, ActorFrom(ctx))
	})
	op(h, huma.Operation{
		OperationID: "beginAxiomSignIn", Method: http.MethodPost, Path: "/api/organization/axiom/sign-in", Tags: obsTags,
		Summary: "Start Sign in with Axiom: the authorize URL to send the browser to",
	}, func(ctx context.Context, in *obsBeginSignInIn) (*obsBeginSignInOut, error) {
		u, err := s.app.BeginAxiomSignIn(ctx, ActorFrom(ctx), in.Body.RedirectURI)
		if err != nil {
			return nil, err
		}
		return &obsBeginSignInOut{Body: api.BeginAxiomSignInResult{URL: u}}, nil
	})
	op(h, huma.Operation{
		OperationID: "completeAxiomSignIn", Method: http.MethodPost, Path: "/api/organization/axiom/callback", Tags: obsTags,
		Summary: "Finish Sign in with Axiom with what /axiom/callback received (single use)",
	}, func(ctx context.Context, in *obsCompleteSignInIn) (*obsCompleteSignInOut, error) {
		r, err := s.app.CompleteAxiomSignIn(ctx, ActorFrom(ctx), in.Body.State, in.Body.Code)
		if err != nil {
			return nil, err
		}
		return &obsCompleteSignInOut{Body: api.CompleteAxiomSignInResult{Choose: r.Choose, Dataset: r.Dataset, Org: r.Org}}, nil
	})
	op(h, huma.Operation{
		OperationID: "chooseAxiomOrg", Method: http.MethodPost, Path: "/api/organization/axiom/choose", Tags: obsTags,
		Summary: "Pick the Axiom org of a pending sign-in",
	}, func(ctx context.Context, in *obsChooseOrgIn) (*obsChooseOrgOut, error) {
		r, err := s.app.ChooseAxiomOrg(ctx, ActorFrom(ctx), in.Body.OrgID)
		if err != nil {
			return nil, err
		}
		return &obsChooseOrgOut{Body: api.AxiomSinkResult{Dataset: r.Dataset, Org: r.Org}}, nil
	})

	op(h, huma.Operation{
		OperationID: "tailNodeLogs", Method: http.MethodGet, Path: "/api/nodes/{id}/logs", Tags: obsTags,
		Summary: "Last lines of a service (its organization's sink, else Docker)",
	}, func(ctx context.Context, in *obsTailNodeLogsIn) (*obsLogTailOut, error) {
		t, err := s.app.TailNodeLogs(ctx, ActorFrom(ctx), in.ID, in.Tail)
		if err != nil {
			return nil, err
		}
		return &obsLogTailOut{Body: t}, nil
	})
	op(h, huma.Operation{
		OperationID: "listEnvironmentLogs", Method: http.MethodGet, Path: "/api/environments/{id}/logs", Tags: obsTags,
		Summary: "Newest lines across the environment's services (Axiom only)",
	}, func(ctx context.Context, in *obsEnvironmentLogsIn) (*obsEnvironmentLogsOut, error) {
		l, err := s.app.EnvironmentLogs(ctx, ActorFrom(ctx), in.ID, in.Search, in.Tail, domain.TimeRange(in.Range))
		if err != nil {
			return nil, err
		}
		return &obsEnvironmentLogsOut{Body: l}, nil
	})
	op(h, huma.Operation{
		OperationID: "listLogsAround", Method: http.MethodGet, Path: "/api/environments/{id}/logs/around", Tags: obsTags,
		Summary: "Every service's lines within 30 s of a moment (Axiom only)",
	}, func(ctx context.Context, in *obsAroundIn) (*obsLogsAroundOut, error) {
		l, err := s.app.LogsAround(ctx, ActorFrom(ctx), in.ID, in.At)
		if err != nil {
			return nil, err
		}
		return &obsLogsAroundOut{Body: l}, nil
	})

	op(h, huma.Operation{
		OperationID: "getTraceOverview", Method: http.MethodGet, Path: "/api/environments/{id}/traces", Tags: obsTags,
		Summary: "Request rate, errors, latency and the latest requests over a range",
	}, func(ctx context.Context, in *obsTraceOverviewIn) (*obsTraceOverviewOut, error) {
		o, err := s.app.TraceOverview(ctx, ActorFrom(ctx), in.ID, domain.TimeRange(in.Range), in.Search, in.NodeID)
		if err != nil {
			return nil, err
		}
		return &obsTraceOverviewOut{Body: o}, nil
	})
	op(h, huma.Operation{
		OperationID: "listTracesAround", Method: http.MethodGet, Path: "/api/environments/{id}/traces/around", Tags: obsTags,
		Summary: "Requests that started within 30 s of a moment",
	}, func(ctx context.Context, in *obsAroundIn) (*obsTracesAroundOut, error) {
		t, err := s.app.TracesAround(ctx, ActorFrom(ctx), in.ID, in.At)
		if err != nil {
			return nil, err
		}
		return &obsTracesAroundOut{Body: t}, nil
	})
	op(h, huma.Operation{
		OperationID: "getTrace", Method: http.MethodGet, Path: "/api/environments/{id}/traces/{traceId}", Tags: obsTags,
		Summary: "Every span of one trace and the environment's lines that name it",
	}, func(ctx context.Context, in *obsGetTraceIn) (*obsTraceOut, error) {
		t, err := s.app.GetTrace(ctx, ActorFrom(ctx), in.ID, in.TraceID, in.At)
		if err != nil {
			return nil, err
		}
		return &obsTraceOut{Body: t}, nil
	})

	op(h, huma.Operation{
		OperationID: "getNodeTracing", Method: http.MethodGet, Path: "/api/nodes/{id}/tracing", Tags: obsTags,
		Summary: "A service's tracing switch and the OTEL_* variables it gets (key masked)",
	}, func(ctx context.Context, in *obsNodeID) (*obsTracingOut, error) {
		v, err := s.app.NodeTracing(ctx, ActorFrom(ctx), in.ID)
		if err != nil {
			return nil, err
		}
		return &obsTracingOut{Body: api.TracingEnvelope{Tracing: v}}, nil
	})
	op(h, huma.Operation{
		OperationID: "setNodeTracing", Method: http.MethodPut, Path: "/api/nodes/{id}/tracing", Tags: obsTags,
		Summary: "Turn tracing on or off for a service (staged until the next ship)", DefaultStatus: http.StatusNoContent,
	}, func(ctx context.Context, in *obsSetTracingIn) (*struct{}, error) {
		return nil, s.app.SetNodeTracing(ctx, ActorFrom(ctx), in.ID, in.Body.On)
	})
	op(h, huma.Operation{
		OperationID: "getLocalTracingEnv", Method: http.MethodPost, Path: "/api/nodes/{id}/tracing/local-env", Tags: obsTags,
		Summary: "Tracing variables for `keel run` (may create the environment's ingest key)",
	}, func(ctx context.Context, in *obsNodeID) (*obsLocalTracingEnvOut, error) {
		lt, err := s.app.LocalTracingEnv(ctx, ActorFrom(ctx), in.ID)
		if err != nil {
			return nil, err
		}
		out := api.LocalTracingEnv{Env: lt.Env}
		if lt.Reason != "" {
			out.Reason = &lt.Reason
		}
		return &obsLocalTracingEnvOut{Body: out}, nil
	})
	op(h, huma.Operation{
		OperationID: "getTracingPrompt", Method: http.MethodGet, Path: "/api/tracing/prompt", Tags: obsTags,
		Summary: "The agent prompt that sets up OpenTelemetry in a repo",
	}, func(ctx context.Context, in *obsTracingPromptIn) (*obsTracingPromptOut, error) {
		p, err := s.app.TracingPrompt(ctx, ActorFrom(ctx), in.NodeID, in.EnvironmentID)
		if err != nil {
			return nil, err
		}
		return &obsTracingPromptOut{Body: api.TracingPrompt{Prompt: p}}, nil
	})
}
