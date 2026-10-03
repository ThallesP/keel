package keel

import (
	"context"
	"errors"
	"net"
	"net/url"
	"strings"

	"github.com/ThallesP/keel/apps/cli/internal/convex"
	"github.com/ThallesP/keel/apps/cli/internal/output"
)

// API is a signed-in connection. Its types are what the CLI prints, so their JSON names are the
// CLI's contract, not the Convex documents'.
type API struct {
	url string
	c   *convex.Client
}

type User struct {
	ID    string `json:"id"`
	Email string `json:"email"`
	Name  string `json:"name"`
}

type Organization struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Slug string `json:"slug"`
	Role string `json:"role"`
}

type Project struct {
	ID           string        `json:"id"`
	Name         string        `json:"name"`
	Slug         string        `json:"slug"`
	Environments []Environment `json:"environments"`
}

type Environment struct {
	ID           string `json:"id"`
	Name         string `json:"name"`
	IsProduction bool   `json:"isProduction"`
}

type Summary struct {
	// Services with staged changes that `keel ship` would deploy.
	PendingChanges Int            `json:"pendingChanges"`
	Counts         map[string]Int `json:"counts"`
	Servers        Int            `json:"servers"`
}

// Service is any canvas node but a group: services, databases, caches, volumes.
type Service struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Type      string `json:"type"`
	Status    string `json:"status"`
	Image     string `json:"image,omitempty"`
	Port      Int    `json:"port,omitempty"`
	Replicas  Int    `json:"replicas"`
	Running   Int    `json:"running"`
	Staged    bool   `json:"staged"`
	PublicURL string `json:"publicUrl,omitempty"`
	Error     string `json:"error,omitempty"`
}

type Variable struct {
	Key string
	// As written; may hold ${{ service.KEY }} references.
	Value string
	// After references are expanded.
	Resolved string
	Secret   bool
	// The resolved value contains a secret, through a reference or its own flag.
	ResolvedSecret bool
}

type Tail struct {
	Source string    `json:"source"`
	Lines  []LogLine `json:"lines"`
}

type LogLine struct {
	Time   Time   `json:"time"`
	Stream string `json:"stream"`
	Task   string `json:"task,omitempty"`
	Text   string `json:"text"`
}

type Deployment struct {
	ID         string     `json:"id"`
	Status     string     `json:"status"` // running | success | failed
	Message    string     `json:"message"`
	StartedAt  Time       `json:"startedAt"`
	FinishedAt *Time      `json:"finishedAt,omitempty"`
	Steps      []Step     `json:"steps"`
	Log        []LogEntry `json:"log,omitempty"`
}

type Step struct {
	ServiceID string `json:"serviceId,omitempty"`
	Label     string `json:"label"`  // service name, or "health checks"
	Status    string `json:"status"` // pending | running | done | failed
}

type LogEntry struct {
	At        Time   `json:"at"`
	ServiceID string `json:"serviceId,omitempty"`
	Text      string `json:"text"`
}

func (a *API) CurrentUser(ctx context.Context) (*User, error) {
	var u *struct {
		ID    string `json:"_id"`
		Email string `json:"email"`
		Name  string `json:"name"`
	}
	if err := a.query(ctx, "auth:getCurrentUser", nil, &u); err != nil {
		return nil, err
	}
	if u == nil {
		return nil, notAuthenticated(a.url, "Session expired or signed out")
	}
	return &User{ID: u.ID, Email: u.Email, Name: u.Name}, nil
}

// Organization is nil when the user isn't in one yet.
func (a *API) Organization(ctx context.Context) (*Organization, error) {
	var o *Organization
	err := a.query(ctx, "organizations:current", nil, &o) // before reading o: Go leaves operand order open
	return o, err
}

func (a *API) Projects(ctx context.Context) ([]Project, error) {
	var ps []Project
	err := a.query(ctx, "projects:list", nil, &ps) // before reading ps: Go leaves operand order open
	return ps, err
}

// CreateProject makes a project with its production environment; the slug comes from the name.
func (a *API) CreateProject(ctx context.Context, name string) (*Project, error) {
	var p *Project
	if err := a.mutation(ctx, "projects:create", args{"name": name}, &p); err != nil {
		return nil, err
	}
	return p, nil
}

func (a *API) Summary(ctx context.Context, environmentID string) (*Summary, error) {
	var s *Summary
	if err := a.query(ctx, "environments:summary", args{"environmentId": environmentID}, &s); err != nil {
		return nil, err
	}
	if s == nil {
		return nil, output.Errorf(output.CodeProjectNotFound, "keel project list", "Environment not found")
	}
	return s, nil
}

func (a *API) Services(ctx context.Context, environmentID string) ([]Service, error) {
	var nodes []struct {
		Service
		Dirty bool `json:"dirty"`
	}
	if err := a.query(ctx, "nodes:list", args{"environmentId": environmentID}, &nodes); err != nil {
		return nil, err
	}
	services := []Service{}
	for _, n := range nodes {
		if n.Type == "group" {
			continue
		}
		n.Service.Staged = n.Dirty
		services = append(services, n.Service)
	}
	return services, nil
}

func (a *API) Variables(ctx context.Context, serviceID string) ([]Variable, error) {
	var rows []struct {
		Key            string `json:"key"`
		Value          string `json:"value"`
		Resolved       string `json:"resolved"`
		Secret         bool   `json:"secret"`
		ResolvedSecret bool   `json:"resolvedSecret"`
	}
	if err := a.query(ctx, "variables:list", args{"nodeId": serviceID}, &rows); err != nil {
		return nil, err
	}
	vars := make([]Variable, len(rows))
	for i, r := range rows {
		vars[i] = Variable(r)
	}
	return vars, nil
}

// CreateService stages a service running image, as dropping one on the canvas does: nothing runs
// until `keel ship`. port and replicas are the server's defaults (80, 1) when nil.
func (a *API) CreateService(ctx context.Context, environmentID, name, image string, port, replicas *int) (*Service, error) {
	in := args{"environmentId": environmentID, "type": "service", "name": name, "image": image}
	if port != nil {
		in["port"] = *port
	}
	if replicas != nil {
		in["replicas"] = *replicas
	}
	var created struct {
		ID string `json:"id"`
	}
	if err := a.mutation(ctx, "nodes:create", in, &created); err != nil {
		return nil, err
	}
	services, err := a.Services(ctx, environmentID)
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
func (a *API) DeleteService(ctx context.Context, id string) error {
	return a.mutation(ctx, "nodes:remove", args{"id": id}, nil)
}

// SetVariable upserts one variable. Like the dashboard, it only stages: `keel ship` deploys.
func (a *API) SetVariable(ctx context.Context, serviceID, key, value string, secret bool) error {
	return a.mutation(ctx, "variables:set",
		args{"nodeId": serviceID, "key": key, "value": value, "secret": secret}, nil)
}

func (a *API) RemoveVariable(ctx context.Context, serviceID, key string) error {
	return a.mutation(ctx, "variables:remove", args{"nodeId": serviceID, "key": key}, nil)
}

func (a *API) Tail(ctx context.Context, serviceID string, lines int) (*Tail, error) {
	var t Tail
	if err := a.action(ctx, "logs:tail", args{"nodeId": serviceID, "tail": lines}, &t); err != nil {
		return nil, err
	}
	if t.Lines == nil {
		t.Lines = []LogLine{}
	}
	return &t, nil
}

// StartDeployment ships the staged changes of an environment, or only these services when given.
// refresh re-pulls images (Redeploy); otherwise the nodes' image cache is used.
func (a *API) StartDeployment(ctx context.Context, environmentID string, only []string, refresh bool) (string, error) {
	in := args{"environmentId": environmentID, "refresh": refresh}
	if len(only) > 0 {
		in["only"] = only
	}
	var id string
	err := a.mutation(ctx, "deployments:start", in, &id) // before reading id: Go leaves operand order open
	return id, err
}

// Deployment is nil when the id is unknown.
func (a *API) Deployment(ctx context.Context, id string) (*Deployment, error) {
	var d *deploymentDoc
	if err := a.query(ctx, "deployments:get", args{"id": id}, &d); err != nil {
		return nil, err
	}
	return d.view(), nil
}

// LatestDeployment is nil when the environment never deployed.
func (a *API) LatestDeployment(ctx context.Context, environmentID string) (*Deployment, error) {
	var d *deploymentDoc
	if err := a.query(ctx, "deployments:latest", args{"environmentId": environmentID}, &d); err != nil {
		return nil, err
	}
	return d.view(), nil
}

// ServiceDeployments is the last 20 deployments that touched a service, newest first.
func (a *API) ServiceDeployments(ctx context.Context, serviceID string) ([]Deployment, error) {
	var docs []*deploymentDoc
	if err := a.query(ctx, "deployments:listForNode", args{"nodeId": serviceID}, &docs); err != nil {
		return nil, err
	}
	out := make([]Deployment, len(docs))
	for i, d := range docs {
		out[i] = *d.view()
	}
	return out, nil
}

type deploymentDoc struct {
	ID         string `json:"_id"`
	Status     string `json:"status"`
	Message    string `json:"message"`
	StartedAt  Time   `json:"startedAt"`
	FinishedAt *Time  `json:"finishedAt"`
	Steps      []struct {
		NodeID string `json:"nodeId"`
		Label  string `json:"label"`
		Status string `json:"status"`
	} `json:"steps"`
	Log []struct {
		At     Time   `json:"at"`
		NodeID string `json:"nodeId"`
		Text   string `json:"text"`
	} `json:"log"`
}

func (d *deploymentDoc) view() *Deployment {
	if d == nil {
		return nil
	}
	out := &Deployment{
		ID: d.ID, Status: d.Status, Message: d.Message, StartedAt: d.StartedAt, FinishedAt: d.FinishedAt,
		Steps: make([]Step, len(d.Steps)), Log: make([]LogEntry, len(d.Log)),
	}
	for i, s := range d.Steps {
		out.Steps[i] = Step{ServiceID: s.NodeID, Label: s.Label, Status: s.Status}
	}
	for i, l := range d.Log {
		out.Log[i] = LogEntry{At: l.At, ServiceID: l.NodeID, Text: l.Text}
	}
	return out
}

type args map[string]any

func (a *API) query(ctx context.Context, path string, in args, out any) error {
	return translate(a.c.Query(ctx, path, in, out), a.url)
}

func (a *API) mutation(ctx context.Context, path string, in args, out any) error {
	return translate(a.c.Mutation(ctx, path, in, out), a.url)
}

func (a *API) action(ctx context.Context, path string, in args, out any) error {
	return translate(a.c.Action(ctx, path, in, out), a.url)
}

// translate turns transport and function errors into CLI errors. Keel's functions throw
// ConvexError with a plain message, so the known ones are matched by text here, in one place.
func translate(err error, webURL string) error {
	if err == nil {
		return nil
	}
	var oe *output.Error
	if errors.As(err, &oe) {
		return oe
	}
	var fe *convex.FunctionError
	if errors.As(err, &fe) {
		msg := fe.DataString()
		switch {
		case msg == "Not authenticated":
			return notAuthenticated(webURL, "Session expired or signed out")
		case strings.HasPrefix(msg, "You're not in an organization"):
			return output.Errorf(output.CodeNoOrganization,
				"Ask a member for an invite link (account menu → Invite people), then keel login "+webURL,
				"This account isn't in the install's organization")
		case msg == "A deployment is already running":
			return output.Errorf(output.CodeDeploymentRunning, "keel deployment get --wait",
				"A deployment is already running in this environment")
		case msg == "Nothing to ship":
			return output.Errorf(output.CodeNothingToShip,
				"Stage a change first (keel var set …), or redeploy: keel redeploy <service>",
				"Nothing to ship: no service has staged changes")
		case msg == "Node not found":
			return output.Errorf(output.CodeServiceNotFound, "keel service list", "Service not found")
		case msg == "Environment not found":
			return output.Errorf(output.CodeProjectNotFound, "keel project list", "Environment not found")
		case strings.HasPrefix(msg, `Project "`) && strings.HasSuffix(msg, `" already exists`):
			return output.Errorf(output.CodeNameTaken,
				"Pick another name, or use it: keel link "+strings.TrimSuffix(strings.TrimPrefix(msg, `Project "`), `" already exists`),
				"%s", msg)
		case strings.HasSuffix(msg, `" is already taken`):
			return output.Errorf(output.CodeNameTaken, "Pick another name; keel service list shows the taken ones", "%s", msg)
		case msg != "":
			return output.Errorf(output.CodeInvalidInput, "", "%s", msg)
		}
		return output.Errorf(output.CodeServer, "", "%s", fe.Message)
	}
	if errors.Is(err, convex.ErrUnauthenticated) {
		return notAuthenticated(webURL, "Session expired or signed out")
	}
	if errors.Is(err, context.Canceled) {
		return output.Errorf(output.CodeCancelled, "", "Cancelled")
	}
	var ne net.Error // *url.Error, from every failed request, is one
	if errors.As(err, &ne) {
		host := webURL
		if u, perr := url.Parse(webURL); perr == nil && u.Host != "" {
			host = u.Host
		}
		if ne.Timeout() {
			return output.Errorf(output.CodeTimeout, "Retry; check that "+host+" is up",
				"Timed out talking to %s", host)
		}
		return output.Errorf(output.CodeNetwork,
			"Check the URL, and that this machine is on the install's tailnet",
			"Can't reach %s: %v", host, rootCause(err))
	}
	return output.Errorf(output.CodeServer, "", "%v", err)
}

func rootCause(err error) error {
	for {
		next := errors.Unwrap(err)
		if next == nil {
			return err
		}
		err = next
	}
}
