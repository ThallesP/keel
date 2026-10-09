package api

import "github.com/ThallesP/keel/internal/domain"

// Canvas area wire types: projects, environments, nodes, variables (docs/go/spec/web-data.md §5.1,
// §5.2, §5.4, §5.6; docs/go/spec/projects.md §4.3, §9.1–9.4).

// ── Projects ──────────────────────────────────────────────────────────────────────────────

type ProjectEnvironment struct {
	ID           string `json:"id"`
	Name         string `json:"name" example:"production"`
	IsProduction bool   `json:"isProduction"`
}

// ProjectSummary is a project with its environments, production first.
type ProjectSummary struct {
	ID           string               `json:"id"`
	Name         string               `json:"name" example:"Acme API"`
	Slug         string               `json:"slug" example:"acme-api"`
	Environments []ProjectEnvironment `json:"environments"`
}

type ProjectList struct {
	Projects []ProjectSummary `json:"projects" doc:"In creation order"`
}

type CreateProjectRequest struct {
	Name string `json:"name" doc:"1–60 characters after trimming; the slug is derived from it" example:"Acme API"`
}

type DefaultProject struct {
	Slug string `json:"slug" example:"acme-support"`
}

// ProjectEnvironmentRef is the environment a project page opens on.
type ProjectEnvironmentRef struct {
	ID   string `json:"id"`
	Name string `json:"name" example:"production"`
}

// ProjectHome is a project with the environment its canvas opens on (production, else the first).
type ProjectHome struct {
	_           struct{}              `nullable:"true"`
	ID          string                `json:"id"`
	Name        string                `json:"name"`
	Slug        string                `json:"slug"`
	Environment ProjectEnvironmentRef `json:"environment"`
}

type ProjectBySlug struct {
	Project *ProjectHome `json:"project" doc:"null when missing or not in the caller's organization"`
}

// ── Environments ──────────────────────────────────────────────────────────────────────────

// EnvironmentSummary: the Ship button's and status bar's numbers.
type EnvironmentSummary struct {
	_              struct{}       `nullable:"true"`
	PendingChanges int            `json:"pendingChanges" doc:"Deployable nodes with a staged change"`
	Counts         map[string]int `json:"counts" doc:"Derived status per node (groups excluded, volumes count as pending); zero counts are absent"`
	Servers        int            `json:"servers" doc:"Ready Swarm nodes, install-wide"`
}

type EnvironmentSummaryResult struct {
	Summary *EnvironmentSummary `json:"summary" doc:"null when the environment is missing or not the caller's"`
}

// ── Nodes ─────────────────────────────────────────────────────────────────────────────────

type Position struct {
	X float64 `json:"x"`
	Y float64 `json:"y"`
}

type NodeConfig struct {
	SizeGb *float64 `json:"sizeGb,omitempty" doc:"Volumes"`
	Width  *float64 `json:"width,omitempty" doc:"Groups"`
	Height *float64 `json:"height,omitempty" doc:"Groups"`
}

// NodeDeploy is the progress of a deploying node.
type NodeDeploy struct {
	Step      string `json:"step" enum:"pulling image,rolling out,starting"`
	StartedAt int64  `json:"startedAt" doc:"When the current revision shipped (unix ms)"`
}

// NodeView is a canvas node as clients see it (docs/go/spec/projects.md §4.3). Status is derived,
// never stored.
type NodeView struct {
	ID               string         `json:"id"`
	Type             string         `json:"type" enum:"service,database,cache,volume,group"`
	Name             string         `json:"name"`
	ParentID         string         `json:"parentId,omitempty" doc:"The group it is in; position is then relative to it"`
	Position         Position       `json:"position"`
	Config           NodeConfig     `json:"config"`
	Dirty            bool           `json:"dirty" doc:"Has a staged change (the CLI's staged)"`
	Status           string         `json:"status" enum:"healthy,done,deploying,stopping,error,stopped,pending"`
	Image            string         `json:"image,omitempty"`
	Port             *int           `json:"port,omitempty"`
	Replicas         int            `json:"replicas"`
	Running          int            `json:"running"`
	Revision         int            `json:"revision" doc:"0 = never shipped"`
	DeployedRevision *int           `json:"deployedRevision,omitempty"`
	Public           bool           `json:"public" doc:"Has at least one endpoint"`
	PublicURL        string         `json:"publicUrl,omitempty" doc:"The first http endpoint's address"`
	Endpoints        []EndpointView `json:"endpoints"`
	Error            string         `json:"error,omitempty"`
	Deploy           *NodeDeploy    `json:"deploy,omitempty" doc:"Only while deploying"`
	StoppedAt        *int64         `json:"stoppedAt,omitempty" doc:"When the stop shipped (stopping, stopped)"`
	FinishedAt       *int64         `json:"finishedAt,omitempty" doc:"When a one-shot run finished (done)"`
}

// NodeViewOf builds the view; publicIP is KEEL_PUBLIC_IP ("" when unknown).
func NodeViewOf(n domain.Node, publicIP string) NodeView {
	status := domain.DeriveStatus(n)
	v := NodeView{
		ID:               n.ID,
		Type:             string(n.Type),
		Name:             n.Name,
		ParentID:         n.ParentID,
		Position:         Position{X: n.Position.X, Y: n.Position.Y},
		Config:           NodeConfig{SizeGb: n.Config.SizeGb, Width: n.Config.Width, Height: n.Config.Height},
		Dirty:            n.Dirty,
		Status:           string(status),
		DeployedRevision: n.DeployedRevision,
		Public:           len(n.Endpoints) > 0,
		Endpoints:        make([]EndpointView, 0, len(n.Endpoints)),
	}
	if d := n.Desired; d != nil {
		v.Image, v.Port, v.Replicas, v.Revision = d.Image, d.Port, d.Replicas, d.Revision
	}
	if o := n.Observed; o != nil {
		v.Running = o.Running
	}
	for _, e := range n.Endpoints {
		v.Endpoints = append(v.Endpoints, EndpointViewOf(e, publicIP))
		if e.Protocol == domain.ProtocolHTTP && v.PublicURL == "" {
			v.PublicURL = e.Address(publicIP)
		}
	}
	switch {
	case n.ApplyError != "":
		v.Error = n.ApplyError
	case status == domain.StatusError && n.Observed != nil:
		v.Error = n.Observed.Error
	}
	switch status {
	case domain.StatusDeploying:
		if n.ShippedAt != nil && *n.ShippedAt != 0 {
			step := "starting"
			revision := 0
			if n.Desired != nil {
				revision = n.Desired.Revision
			}
			if n.Observed == nil || n.Observed.Revision < revision {
				step = "pulling image"
			} else if n.Observed.State == domain.ObservedUpdating {
				step = "rolling out"
			}
			v.Deploy = &NodeDeploy{Step: step, StartedAt: *n.ShippedAt}
		}
	case domain.StatusStopped, domain.StatusStopping:
		v.StoppedAt = n.ShippedAt
	case domain.StatusDone:
		if n.Observed != nil {
			v.FinishedAt = n.Observed.FinishedAt
		}
	}
	return v
}

type NodeList struct {
	Nodes []NodeView `json:"nodes" doc:"In creation order"`
}

type CreateNodeRequest struct {
	Type     string    `json:"type" enum:"service,database,cache,volume,group"`
	Name     string    `json:"name,omitempty" doc:"Omitted or empty: named after the engine or image, made unique"`
	Position *Position `json:"position,omitempty" doc:"Omitted: right of the rightmost top-level node"`
	Image    *string   `json:"image,omitempty" doc:"Services only" example:"nginx:alpine"`
	Engine   string    `json:"engine,omitempty" enum:"postgres,mysql,mongo,redis" doc:"Databases and caches: picks image and port"`
	Port     *float64  `json:"port,omitempty" doc:"Container port, 1–65535"`
	Replicas *float64  `json:"replicas,omitempty" doc:"0–20, default 1"`
	Deploy   bool      `json:"deploy,omitempty" doc:"Ship it right away (skipped when a deployment is already running)"`
}

type CreatedNode struct {
	ID           string `json:"id"`
	DeploymentID string `json:"deploymentId,omitempty" doc:"Set when deploy started one"`
}

type UpdateNodeRequest struct {
	Name     *string     `json:"name,omitempty" doc:"Rename; references to the node follow"`
	Image    *string     `json:"image,omitempty" doc:"Runtime change (staged)"`
	Port     *float64    `json:"port,omitempty" doc:"Runtime change (staged)"`
	Replicas *float64    `json:"replicas,omitempty" doc:"Runtime change (staged)"`
	Config   *NodeConfig `json:"config,omitempty" doc:"Volume size, group box; omitted fields keep their value"`
	ParentID *string     `json:"parentId,omitempty" doc:"A group's id, or \"\" for the top level; the node keeps its place unless position is given"`
	Position *Position   `json:"position,omitempty" doc:"With parentId: the position inside the new parent"`
}

type DuplicatedNode struct {
	ID string `json:"id" doc:"The copy"`
}

type StartedNode struct {
	DeploymentID string `json:"deploymentId"`
}

type StoppedNode struct {
	DeploymentID *string `json:"deploymentId" doc:"null when it already was at 0 replicas"`
}

// ── Variables ─────────────────────────────────────────────────────────────────────────────

// VariableRef is a reference inside a value. node absent = the variable's own node; nodeId absent
// = the name resolves to nothing.
type VariableRef struct {
	Node    string `json:"node,omitempty"`
	NodeID  string `json:"nodeId,omitempty"`
	Key     string `json:"key"`
	Missing bool   `json:"missing"`
}

// VariablePart is literal text or a reference: exactly one of text / ref is present.
type VariablePart struct {
	Text *string      `json:"text,omitempty"`
	Ref  *VariableRef `json:"ref,omitempty"`
}

// VariablePartOf converts a domain part.
func VariablePartOf(p domain.RefPart) VariablePart {
	if p.Ref == nil {
		t := p.Text
		return VariablePart{Text: &t}
	}
	return VariablePart{Ref: &VariableRef{Node: p.Ref.Node, NodeID: p.Ref.NodeID, Key: p.Ref.Key, Missing: p.Ref.Missing}}
}

type VariableView struct {
	Key            string         `json:"key"`
	Value          string         `json:"value" doc:"As typed; may contain ${{ node.KEY }} references"`
	Resolved       string         `json:"resolved" doc:"References expanded: what the container gets"`
	Secret         bool           `json:"secret" doc:"The row's own flag: mask value"`
	ResolvedSecret bool           `json:"resolvedSecret" doc:"A referenced value is secret: mask resolved"`
	Parts          []VariablePart `json:"parts"`
}

type VariableList struct {
	Variables []VariableView `json:"variables" doc:"In row (creation) order: the container Env order"`
}

type ReferenceKey struct {
	Key      string `json:"key"`
	As       string `json:"as" doc:"The variable name to give a reference to it: DATABASE_URL stays, api.URL becomes API_URL"`
	Secret   bool   `json:"secret"`
	Provided bool   `json:"provided" doc:"Computed (DATABASE_URL, REDIS_URL, URL, HOST, PORT), not a row"`
}

type ReferenceSource struct {
	NodeID string         `json:"nodeId"`
	Name   string         `json:"name"`
	Type   string         `json:"type" enum:"service,database,cache"`
	Image  string         `json:"image,omitempty"`
	Keys   []ReferenceKey `json:"keys" doc:"Provided keys first (URL key, HOST, PORT), then the node's rows"`
}

type ReferenceSuggestion struct {
	NodeID string `json:"nodeId"`
	Node   string `json:"node"`
	Key    string `json:"key"`
	As     string `json:"as" doc:"The variable to create"`
	Value  string `json:"value" doc:"The reference, e.g. ${{ postgres.DATABASE_URL }}"`
}

type ReferenceSourceList struct {
	Sources     []ReferenceSource     `json:"sources" doc:"Every other deployable node of the environment, in creation order"`
	Suggestions []ReferenceSuggestion `json:"suggestions" doc:"Services only: each source's connection key the node neither references nor has a variable for yet"`
}

type SetVariableRequest struct {
	Key         string  `json:"key" doc:"UPPER_SNAKE_CASE" example:"DATABASE_URL"`
	Value       string  `json:"value" doc:"At most 4096 characters" example:"${{ postgres.DATABASE_URL }}"`
	Secret      bool    `json:"secret"`
	PreviousKey *string `json:"previousKey,omitempty" doc:"Rename that row to key instead of upserting; references follow"`
}

type DeleteVariableRequest struct {
	Key string `json:"key"`
}
