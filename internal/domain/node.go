package domain

type NodeType string

const (
	NodeService  NodeType = "service"
	NodeDatabase NodeType = "database"
	NodeCache    NodeType = "cache"
	NodeVolume   NodeType = "volume"
	NodeGroup    NodeType = "group"
)

func (t NodeType) Deployable() bool {
	return t == NodeService || t == NodeDatabase || t == NodeCache
}

type Position struct {
	X float64
	Y float64
}

func (p Position) Add(q Position) Position { return Position{X: p.X + q.X, Y: p.Y + q.Y} }

func (p Position) Sub(q Position) Position { return Position{X: p.X - q.X, Y: p.Y - q.Y} }

type NodeConfig struct {
	SizeGb *float64
	Width  *float64
	Height *float64
}

type Desired struct {
	Image    string
	Revision int
	Replicas int
	Port     *int
	Tracing  bool
}

type ObservedState string

const (
	ObservedOK        ObservedState = "ok"
	ObservedUpdating  ObservedState = "updating"
	ObservedCrashloop ObservedState = "crashloop"
	ObservedPending   ObservedState = "pending"
	ObservedFailed    ObservedState = "failed"
	ObservedCompleted ObservedState = "completed"
)

type Observed struct {
	Revision   int
	Running    int
	Completed  *int
	FinishedAt *int64
	State      ObservedState
	Error      string
	At         int64
}

type Node struct {
	ID               string
	EnvironmentID    string
	Type             NodeType
	Name             string
	ParentID         string
	Position         Position
	Config           NodeConfig
	Desired          *Desired
	Observed         *Observed
	Endpoints        []Endpoint
	DeployedRevision *int
	Dirty            bool
	ShippedAt        *int64
	ApplyError       string
	OneShot          bool
	CreatedAt        int64
}

func (n Node) ServiceName() string { return ServicePrefix + n.ID }

const ServicePrefix = "svc-"
