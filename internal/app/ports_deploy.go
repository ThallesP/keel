package app

import (
	"context"

	"github.com/ThallesP/keel/internal/domain"
)

// DeployTx: deployments, steps, log, cluster.
// Owner: the deploy area (docs/go/spec/projects.md, docs/go/spec/swarm-worker.md).
type DeployTx interface {
	// HasRunningDeployment: a deployment of the environment has status running.
	HasRunningDeployment(environmentID string) (bool, error)
	// InsertDeployment writes the deployment and its steps. Its log starts empty (d.Log is ignored).
	InsertDeployment(d domain.Deployment) error
	// Deployment is one deployment with its steps and log (oldest line first). ErrNoRow when missing.
	Deployment(id string) (domain.Deployment, error)
	// LatestDeployment is the environment's newest deployment with steps and log. ErrNoRow when
	// there is none.
	LatestDeployment(environmentID string) (domain.Deployment, error)
	// RecentDeployments: the environment's newest `limit` deployments, newest first, with their
	// steps but without their log (Log is nil; DeploymentLog fills it).
	RecentDeployments(environmentID string, limit int) ([]domain.Deployment, error)
	// DeploymentLog is a deployment's log, oldest line first.
	DeploymentLog(id string) ([]domain.LogLine, error)
	// RunningDeployments: deployments with status running, oldest first, with steps and log.
	// environmentID "" means every environment.
	RunningDeployments(environmentID string) ([]domain.Deployment, error)
	// UpdateDeployment writes d's status, finishedAt and steps, appends `appended` to its log and
	// keeps the last 500 lines. ErrNoRow when the deployment is gone.
	UpdateDeployment(d domain.Deployment, appended []domain.LogLine) error

	// ClusterServers is the ready Swarm node count (0 before the first observation). The canvas
	// area's environment summary reads it.
	ClusterServers() (int, error)
	// SetClusterServers upserts the single cluster row.
	SetClusterServers(servers int, at int64) error
	// DeployOrganizationIDs: every organization; a cluster change invalidates all of them.
	DeployOrganizationIDs() ([]string, error)
}

// Swarm drives Docker Swarm through the manager's socket (adapters/swarm). Every method talks to
// Docker, so callers never hold a transaction across one.
type Swarm interface {
	// ImageCached: the manager's daemon has image locally.
	ImageCached(ctx context.Context, image string) (bool, error)
	// PullImage pulls image to the manager. An error reported inside the progress stream is a
	// failure too.
	PullImage(ctx context.Context, image string) error
	// ServiceVersion is svc-<nodeID>'s Swarm version index; found=false when the service does not
	// exist.
	ServiceVersion(ctx context.Context, nodeID string) (version uint64, found bool, err error)
	// CreateService creates svc-<spec.NodeID> from spec.
	CreateService(ctx context.Context, spec ServiceSpec) error
	// UpdateService replaces svc-<spec.NodeID>'s spec; version is the one ServiceVersion read.
	UpdateService(ctx context.Context, version uint64, spec ServiceSpec) error
	// RemoveService deletes svc-<nodeID>. A missing service is not an error.
	RemoveService(ctx context.Context, nodeID string) error
	// ObserveService reads svc-<nodeID> (nil when missing) and the tasks labelled
	// keel.service=<nodeID>, in Docker's order.
	ObserveService(ctx context.Context, nodeID string) (*SwarmService, []SwarmTask, error)
	// ObserveServices reads every service and every task labelled keel.service.
	ObserveServices(ctx context.Context) ([]SwarmService, []SwarmTask, error)
	// Servers counts Swarm nodes: those whose status is ready, and all of them.
	Servers(ctx context.Context) (ready, total int, err error)
	// EnsureAgent creates or updates the keel-agent global service (`keel agent` on every node).
	EnsureAgent(ctx context.Context, spec AgentSpec) error
}

// ServiceSpec is what apply asks Swarm to run for a node. The adapter turns it into the Swarm
// service spec of docs/go/spec/swarm-worker.md §5 (toSpec).
type ServiceSpec struct {
	NodeID   string
	Image    string
	Revision int
	Replicas int
	// Env is KEY=value, in order.
	Env []string
	// OneShot: the image exits 0 by design; a failed update continues instead of rolling back.
	OneShot bool
}

// SwarmService is the part of a Swarm service observation reads.
type SwarmService struct {
	Name   string
	Labels map[string]string
	// UpdateState is UpdateStatus.State ("" when Swarm reports none: a fresh create).
	UpdateState   string
	UpdateMessage string
}

// SwarmTask is the part of a Swarm task observation reads.
type SwarmTask struct {
	NodeID       string
	DesiredState string
	State        string
	Err          string
	// Timestamp is Status.Timestamp in unix ms, 0 when unknown.
	Timestamp int64
	// Labels are the task's container spec labels (keel.service, keel.revision).
	Labels map[string]string
}

// AgentSpec is the keel-agent global service: `keel agent` from Image on every Swarm node.
type AgentSpec struct {
	Image      string
	ControlURL string // KEEL_URL: how agents reach serve
	Token      string // KEEL_WORKER_TOKEN
}
