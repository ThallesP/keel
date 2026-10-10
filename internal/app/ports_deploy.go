package app

import (
	"context"

	"github.com/ThallesP/keel/internal/domain"
)

type DeployTx interface {
	HasRunningDeployment(environmentID string) (bool, error)
	InsertDeployment(d domain.Deployment) error
	Deployment(id string) (domain.Deployment, error)
	LatestDeployment(environmentID string) (domain.Deployment, error)
	RecentDeployments(environmentID string, limit int) ([]domain.Deployment, error)
	DeploymentLog(id string) ([]domain.LogLine, error)
	RunningDeployments(environmentID string) ([]domain.Deployment, error)
	UpdateDeployment(d domain.Deployment, appended []domain.LogLine) error

	ClusterServers() (int, error)
	SetClusterServers(servers int, at int64) error
	DeployOrganizationIDs() ([]string, error)
}

type Swarm interface {
	ImageCached(ctx context.Context, image string) (bool, error)
	PullImage(ctx context.Context, image string) error
	ServiceVersion(ctx context.Context, nodeID string) (version uint64, found bool, err error)
	CreateService(ctx context.Context, spec ServiceSpec) error
	UpdateService(ctx context.Context, version uint64, spec ServiceSpec) error
	RemoveService(ctx context.Context, nodeID string) error
	ObserveService(ctx context.Context, nodeID string) (SwarmService, []SwarmTask, error)
	ObserveServices(ctx context.Context) ([]SwarmService, []SwarmTask, error)
	Servers(ctx context.Context) (ready int, err error)
	EnsureAgent(ctx context.Context, spec AgentSpec) error
}

type ServiceSpec struct {
	NodeID   string
	Image    string
	Revision int
	Replicas int
	Env      []string
	OneShot  bool
}

type SwarmService struct {
	Name          string
	Revision      int
	UpdateState   string
	UpdateMessage string
}

type SwarmTask struct {
	NodeID       string
	DesiredState string
	State        string
	Err          string
	Timestamp    int64
	Revision     int
}

type AgentSpec struct {
	Image      string
	ControlURL string
	Token      string
}
