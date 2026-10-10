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
	X float64 `json:"x"`
	Y float64 `json:"y"`
}

type NodeConfig struct {
	SizeGb *float64 `json:"sizeGb,omitempty"`
	Width  *float64 `json:"width,omitempty"`
	Height *float64 `json:"height,omitempty"`
}

type Desired struct {
	Image    string `json:"image"`
	Revision int    `json:"revision"`
	Replicas int    `json:"replicas"`
	Port     *int   `json:"port,omitempty"`
	Tracing  bool   `json:"tracing,omitempty"`
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
	Revision   int           `json:"revision"`
	Running    int           `json:"running"`
	Completed  *int          `json:"completed,omitempty"`
	FinishedAt *int64        `json:"finishedAt,omitempty"`
	State      ObservedState `json:"state"`
	NodeIDs    []string      `json:"nodeIds"`
	Error      string        `json:"error,omitempty"`
	At         int64         `json:"at"`
}

type Node struct {
	ID               string     `json:"id"`
	EnvironmentID    string     `json:"environmentId"`
	Type             NodeType   `json:"type"`
	Name             string     `json:"name"`
	ParentID         string     `json:"parentId,omitempty"`
	Position         Position   `json:"position"`
	Config           NodeConfig `json:"config"`
	Desired          *Desired   `json:"desired,omitempty"`
	Observed         *Observed  `json:"observed,omitempty"`
	Endpoints        []Endpoint `json:"endpoints,omitempty"`
	DeployedRevision *int       `json:"deployedRevision,omitempty"`
	Dirty            bool       `json:"dirty,omitempty"`
	ShippedAt        *int64     `json:"shippedAt,omitempty"`
	ApplyError       string     `json:"applyError,omitempty"`
	OneShot          bool       `json:"oneShot,omitempty"`
	CreatedAt        int64      `json:"createdAt"`
}

func (n Node) ServiceName() string { return ServicePrefix + n.ID }

const ServicePrefix = "svc-"
