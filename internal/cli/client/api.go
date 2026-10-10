package client

import (
	"context"
	"net/http"
	"net/url"
	"slices"
	"strconv"

	"github.com/ThallesP/keel/internal/api"
	"github.com/ThallesP/keel/internal/cli/output"
	"github.com/ThallesP/keel/internal/domain"
)

func (c *Client) Projects(ctx context.Context) ([]api.ProjectSummary, error) {
	var out api.ProjectList
	if err := c.call(ctx, http.MethodGet, "/api/projects", nil, nil, &out); err != nil {
		return nil, err
	}
	return out.Projects, nil
}

func (c *Client) CreateProject(ctx context.Context, name string) (*api.ProjectSummary, error) {
	var p api.ProjectSummary
	if err := c.call(ctx, http.MethodPost, "/api/projects", nil, api.CreateProjectRequest{Name: name}, &p); err != nil {
		return nil, err
	}
	return &p, nil
}

func (c *Client) Summary(ctx context.Context, environmentID string) (*api.EnvironmentSummary, error) {
	var out api.EnvironmentSummaryResult
	if err := c.call(ctx, http.MethodGet, apiPath("/api/environments/%s/summary", environmentID), nil, nil, &out); err != nil {
		return nil, err
	}
	if out.Summary == nil {
		return nil, output.Errorf(output.CodeProjectNotFound, "keel project list", "Environment not found")
	}
	return out.Summary, nil
}

func (c *Client) Services(ctx context.Context, environmentID string) ([]Service, error) {
	var out api.NodeList
	if err := c.call(ctx, http.MethodGet, apiPath("/api/environments/%s/nodes", environmentID), nil, nil, &out); err != nil {
		return nil, err
	}
	services := []Service{}
	for _, n := range out.Nodes {
		if n.Type == "group" {
			continue
		}
		services = append(services, serviceOf(n))
	}
	return services, nil
}

func (c *Client) Variables(ctx context.Context, serviceID string) ([]api.VariableView, error) {
	var out api.VariableList
	if err := c.call(ctx, http.MethodGet, apiPath("/api/nodes/%s/variables", serviceID), nil, nil, &out); err != nil {
		return nil, err
	}
	return out.Variables, nil
}

func (c *Client) CreateService(ctx context.Context, environmentID, name, image string, port, replicas *int) (*Service, error) {
	in := api.CreateNodeRequest{Type: "service", Name: name, Image: &image, Port: port, Replicas: replicas}
	var created api.CreatedNode
	if err := c.call(ctx, http.MethodPost, apiPath("/api/environments/%s/nodes", environmentID), nil, in, &created); err != nil {
		return nil, err
	}
	services, err := c.Services(ctx, environmentID)
	if err != nil {
		return nil, err
	}
	i := slices.IndexFunc(services, func(s Service) bool { return s.ID == created.ID })
	if i < 0 {
		return nil, output.Errorf(output.CodeServiceNotFound, "keel service list", "Service %s was deleted right after it was created", name)
	}
	return &services[i], nil
}

func (c *Client) DeleteService(ctx context.Context, id string) error {
	return c.call(ctx, http.MethodDelete, apiPath("/api/nodes/%s", id), nil, nil, nil)
}

func (c *Client) SetVariable(ctx context.Context, serviceID, key, value string, secret bool) error {
	return c.call(ctx, http.MethodPost, apiPath("/api/nodes/%s/variables", serviceID), nil,
		api.SetVariableRequest{Key: key, Value: value, Secret: secret}, nil)
}

func (c *Client) RemoveVariable(ctx context.Context, serviceID, key string) error {
	return c.call(ctx, http.MethodPost, apiPath("/api/nodes/%s/variables/delete", serviceID), nil,
		api.DeleteVariableRequest{Key: key}, nil)
}

func (c *Client) Tail(ctx context.Context, serviceID string, lines int) (*Tail, error) {
	var t domain.LogTail
	q := url.Values{"tail": {strconv.Itoa(lines)}}
	if err := c.call(ctx, http.MethodGet, apiPath("/api/nodes/%s/logs", serviceID), q, nil, &t); err != nil {
		return nil, err
	}
	out := &Tail{Source: t.Source, Lines: make([]LogLine, len(t.Lines))}
	for i, l := range t.Lines {
		out.Lines[i] = LogLine{Time: millis(int64(l.Time)), Stream: l.Stream, Task: l.Task, Text: l.Text}
	}
	return out, nil
}

func (c *Client) Traces(ctx context.Context, environmentID, serviceID, since, search string) (*Traces, error) {
	q := url.Values{"range": {since}}
	if serviceID != "" {
		q.Set("nodeId", serviceID)
	}
	if search != "" {
		q.Set("search", search)
	}
	var o domain.TraceOverview
	if err := c.call(ctx, http.MethodGet, apiPath("/api/environments/%s/traces", environmentID), q, nil, &o); err != nil {
		return nil, err
	}
	out := &Traces{
		Stats: TraceStats{
			Requests: o.Stats.Requests, Errors: o.Stats.Errors,
			P50Ms: o.Stats.P50, P95Ms: o.Stats.P95, P99Ms: o.Stats.P99,
		},
		Traces: make([]TraceSummary, len(o.Traces)),
	}
	for i, t := range o.Traces {
		out.Traces[i] = TraceSummary{
			TraceID: t.TraceID, Name: t.Name, Service: t.Service, Start: millis(int64(t.Start)), DurationMs: t.Duration,
			HTTPStatus: t.HTTPStatus, Spans: t.Spans, Errors: t.Errors, Error: t.Error, Local: t.Local,
		}
	}
	return out, nil
}

func (c *Client) Tracing(ctx context.Context, serviceID string) (*Tracing, error) {
	var out api.TracingEnvelope
	if err := c.call(ctx, http.MethodGet, apiPath("/api/nodes/%s/tracing", serviceID), nil, nil, &out); err != nil {
		return nil, err
	}
	t := out.Tracing
	if t == nil {
		return nil, nil
	}
	return &Tracing{Enabled: t.Enabled, Store: t.Traces, Env: t.Env}, nil
}

func (c *Client) SetTracing(ctx context.Context, serviceID string, on bool) error {
	return c.call(ctx, http.MethodPut, apiPath("/api/nodes/%s/tracing", serviceID), nil, api.SetTracingRequest{On: on}, nil)
}

func (c *Client) LocalTracingEnv(ctx context.Context, serviceID string) (map[string]string, string, error) {
	var r api.LocalTracingEnv
	if err := c.call(ctx, http.MethodPost, apiPath("/api/nodes/%s/tracing/local-env", serviceID), nil, nil, &r); err != nil {
		return nil, "", err
	}
	if r.Reason != nil {
		return nil, *r.Reason, nil
	}
	return r.Env, "", nil
}

func (c *Client) TracingPrompt(ctx context.Context, serviceID, environmentID string) (string, error) {
	q := url.Values{}
	if serviceID != "" {
		q.Set("nodeId", serviceID)
	}
	if environmentID != "" {
		q.Set("environmentId", environmentID)
	}
	var out api.TracingPrompt
	if err := c.call(ctx, http.MethodGet, "/api/tracing/prompt", q, nil, &out); err != nil {
		return "", err
	}
	return out.Prompt, nil
}

func (c *Client) StartDeployment(ctx context.Context, environmentID string, only []string, refresh bool) (string, error) {
	in := api.ShipRequest{Only: only, Refresh: refresh}
	var out api.ShipResponse
	if err := c.call(ctx, http.MethodPost, apiPath("/api/environments/%s/deployments", environmentID), nil, in, &out); err != nil {
		return "", err
	}
	return out.ID, nil
}

func (c *Client) Deployment(ctx context.Context, id string) (*Deployment, error) {
	if id == "" || id == "." || id == ".." {
		return nil, nil
	}
	var out api.DeploymentEnvelope
	if err := c.call(ctx, http.MethodGet, apiPath("/api/deployments/%s", id), nil, nil, &out); err != nil {
		return nil, err
	}
	return deploymentOf(out.Deployment), nil
}

func (c *Client) LatestDeployment(ctx context.Context, environmentID string) (*Deployment, error) {
	var out api.DeploymentEnvelope
	if err := c.call(ctx, http.MethodGet, apiPath("/api/environments/%s/deployments/latest", environmentID), nil, nil, &out); err != nil {
		return nil, err
	}
	return deploymentOf(out.Deployment), nil
}

func (c *Client) ServiceDeployments(ctx context.Context, serviceID string) ([]Deployment, error) {
	var docs []api.Deployment
	if err := c.call(ctx, http.MethodGet, apiPath("/api/nodes/%s/deployments", serviceID), nil, nil, &docs); err != nil {
		return nil, err
	}
	out := make([]Deployment, len(docs))
	for i := range docs {
		out[i] = *deploymentOf(&docs[i])
	}
	return out, nil
}
