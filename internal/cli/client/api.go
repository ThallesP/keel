package client

import (
	"context"
	"net/http"
	"net/url"
	"strconv"

	"github.com/ThallesP/keel/internal/api"
	"github.com/ThallesP/keel/internal/cli/output"
)

// The API calls the CLI makes, one per operation of openapi.json. Each returns the CLI's types.

// Projects is the organization's projects, environments production first. An account without an
// organization gets none before the organization exists, NO_ORGANIZATION after.
func (c *Client) Projects(ctx context.Context) ([]Project, error) {
	var out api.ProjectList
	if err := c.call(ctx, http.MethodGet, "/api/projects", nil, nil, &out); err != nil {
		return nil, err
	}
	if out.Projects == nil {
		out.Projects = []Project{}
	}
	return out.Projects, nil
}

// CreateProject makes a project with its production environment; the slug comes from the name.
// On a fresh install the first one founds the organization.
func (c *Client) CreateProject(ctx context.Context, name string) (*Project, error) {
	var p Project
	if err := c.call(ctx, http.MethodPost, "/api/projects", nil, api.CreateProjectRequest{Name: name}, &p); err != nil {
		return nil, err
	}
	return &p, nil
}

func (c *Client) Summary(ctx context.Context, environmentID string) (*Summary, error) {
	var out api.EnvironmentSummaryResult
	if err := c.call(ctx, http.MethodGet, apiPath("/api/environments/%s/summary", environmentID), nil, nil, &out); err != nil {
		return nil, err
	}
	if out.Summary == nil {
		return nil, output.Errorf(output.CodeProjectNotFound, "keel project list", "Environment not found")
	}
	return out.Summary, nil
}

// Services is the environment's nodes but groups, in canvas (creation) order.
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

func (c *Client) Variables(ctx context.Context, serviceID string) ([]Variable, error) {
	var out api.VariableList
	if err := c.call(ctx, http.MethodGet, apiPath("/api/nodes/%s/variables", serviceID), nil, nil, &out); err != nil {
		return nil, err
	}
	return out.Variables, nil
}

// CreateService stages a service running image, as dropping one on the canvas does: nothing runs
// until `keel ship`. port and replicas are the server's defaults (80, 1) when nil.
func (c *Client) CreateService(ctx context.Context, environmentID, name, image string, port, replicas *int) (*Service, error) {
	in := api.CreateNodeRequest{Type: "service", Name: name, Image: &image}
	if port != nil {
		p := float64(*port)
		in.Port = &p
	}
	if replicas != nil {
		r := float64(*replicas)
		in.Replicas = &r
	}
	var created api.CreatedNode
	if err := c.call(ctx, http.MethodPost, apiPath("/api/environments/%s/nodes", environmentID), nil, in, &created); err != nil {
		return nil, err
	}
	services, err := c.Services(ctx, environmentID)
	if err != nil {
		return nil, err
	}
	for i := range services {
		if services[i].ID == created.ID {
			return &services[i], nil
		}
	}
	return nil, output.Errorf(output.CodeServiceNotFound, "keel service list", "Service %s was deleted right after it was created", name)
}

// DeleteService removes a service now, not at the next ship: its Swarm service, variables and
// canvas node go. Services that reference its variables get staged changes.
func (c *Client) DeleteService(ctx context.Context, id string) error {
	return c.call(ctx, http.MethodDelete, apiPath("/api/nodes/%s", id), nil, nil, nil)
}

// SetVariable upserts one variable. Like the dashboard, it only stages: `keel ship` deploys.
func (c *Client) SetVariable(ctx context.Context, serviceID, key, value string, secret bool) error {
	return c.call(ctx, http.MethodPost, apiPath("/api/nodes/%s/variables", serviceID), nil,
		api.SetVariableRequest{Key: key, Value: value, Secret: secret}, nil)
}

func (c *Client) RemoveVariable(ctx context.Context, serviceID, key string) error {
	return c.call(ctx, http.MethodPost, apiPath("/api/nodes/%s/variables/delete", serviceID), nil,
		api.DeleteVariableRequest{Key: key}, nil)
}

func (c *Client) Tail(ctx context.Context, serviceID string, lines int) (*Tail, error) {
	var t api.LogTail
	q := url.Values{"tail": {strconv.Itoa(lines)}}
	if err := c.call(ctx, http.MethodGet, apiPath("/api/nodes/%s/logs", serviceID), q, nil, &t); err != nil {
		return nil, err
	}
	out := &Tail{Source: t.Source, Lines: make([]LogLine, len(t.Lines))}
	for i, l := range t.Lines {
		out.Lines[i] = LogLine{Time: Millis(l.Time), Stream: l.Stream, Task: l.Task, Text: l.Text}
	}
	return out, nil
}

// Traces is the requests of the environment, or of one service, over the last `since`
// (15m, 1h, 24h or 7d), optionally only those whose name or service contains search.
func (c *Client) Traces(ctx context.Context, environmentID, serviceID, since, search string) (*Traces, error) {
	q := url.Values{"range": {since}}
	if serviceID != "" {
		q.Set("nodeId", serviceID)
	}
	if search != "" {
		q.Set("search", search)
	}
	var o api.TraceOverview
	if err := c.call(ctx, http.MethodGet, apiPath("/api/environments/%s/traces", environmentID), q, nil, &o); err != nil {
		return nil, err
	}
	out := &Traces{
		Stats: TraceStats{
			Requests: int(o.Stats.Requests), Errors: int(o.Stats.Errors),
			P50Ms: o.Stats.P50, P95Ms: o.Stats.P95, P99Ms: o.Stats.P99,
		},
		Traces: make([]TraceSummary, len(o.Traces)),
	}
	for i, t := range o.Traces {
		out.Traces[i] = TraceSummary{
			TraceID: t.TraceID, Name: t.Name, Service: t.Service, Start: Millis(t.Start), DurationMs: t.Duration,
			Spans: int(t.Spans), Errors: int(t.Errors), Error: t.Error, Local: t.Local,
		}
		if t.HTTPStatus != nil {
			s := int(*t.HTTPStatus)
			out.Traces[i].HTTPStatus = &s
		}
	}
	return out, nil
}

// Tracing is nil for anything but a service.
func (c *Client) Tracing(ctx context.Context, serviceID string) (*Tracing, error) {
	var out api.TracingEnvelope
	if err := c.call(ctx, http.MethodGet, apiPath("/api/nodes/%s/tracing", serviceID), nil, nil, &out); err != nil {
		return nil, err
	}
	t := out.Tracing
	if t == nil {
		return nil, nil
	}
	env := t.Env
	if env == nil {
		env = []TracingVar{}
	}
	return &Tracing{Enabled: t.Enabled, Store: t.Traces, Env: env}, nil
}

// SetTracing turns a service's tracing on or off. Staged, like a variable: keel ship applies it.
func (c *Client) SetTracing(ctx context.Context, serviceID string, on bool) error {
	return c.call(ctx, http.MethodPut, apiPath("/api/nodes/%s/tracing", serviceID), nil, api.SetTracingRequest{On: on}, nil)
}

// LocalTracingEnv is the tracing variables of a local run of a service, without the endpoint.
// Nil with the reason when the organization has nowhere to store traces.
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

// TracingPrompt is the agent prompt, naming the service or the environment's project when given.
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

// StartDeployment ships the staged changes of an environment, or only these services when given.
// refresh re-pulls images (Redeploy); otherwise the nodes' image cache is used.
func (c *Client) StartDeployment(ctx context.Context, environmentID string, only []string, refresh bool) (string, error) {
	in := api.ShipRequest{Refresh: refresh}
	if len(only) > 0 {
		in.Only = only
	}
	var out api.ShipResponse
	if err := c.call(ctx, http.MethodPost, apiPath("/api/environments/%s/deployments", environmentID), nil, in, &out); err != nil {
		return "", err
	}
	return out.ID, nil
}

// Deployment is nil when the id is unknown (or not the caller's). The id is whatever the user
// typed: one that can't be a path segment ("", ".", "..") would reach another route, and no
// deployment has it.
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

// LatestDeployment is nil when the environment never deployed.
func (c *Client) LatestDeployment(ctx context.Context, environmentID string) (*Deployment, error) {
	var out api.DeploymentEnvelope
	if err := c.call(ctx, http.MethodGet, apiPath("/api/environments/%s/deployments/latest", environmentID), nil, nil, &out); err != nil {
		return nil, err
	}
	return deploymentOf(out.Deployment), nil
}

// ServiceDeployments is the last 20 deployments that touched a service, newest first.
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
