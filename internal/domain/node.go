package domain

// NodeType is what a canvas node is. volume and group never reach Swarm in v1.
type NodeType string

const (
	NodeService  NodeType = "service"
	NodeDatabase NodeType = "database"
	NodeCache    NodeType = "cache"
	NodeVolume   NodeType = "volume"
	NodeGroup    NodeType = "group"
)

// Deployable: types that become a Swarm service.
func (t NodeType) Deployable() bool {
	return t == NodeService || t == NodeDatabase || t == NodeCache
}

type Position struct {
	X float64 `json:"x"`
	Y float64 `json:"y"`
}

// NodeConfig: type-specific, non-deploy settings. Volumes: sizeGb. Groups: width/height.
type NodeConfig struct {
	SizeGb *float64 `json:"sizeGb,omitempty"`
	Width  *float64 `json:"width,omitempty"`
	Height *float64 `json:"height,omitempty"`
}

// Desired is what the user asked for. Revision 0 means never shipped. Env is computed at apply
// time from variables, so it is not stored here.
type Desired struct {
	Image    string `json:"image"`
	Revision int    `json:"revision"`
	Replicas int    `json:"replicas"`
	Port     *int   `json:"port,omitempty"`
	Tracing  bool   `json:"tracing,omitempty"`
}

// ObservedState is what Swarm reports about the current revision.
type ObservedState string

const (
	ObservedOK        ObservedState = "ok"
	ObservedUpdating  ObservedState = "updating"
	ObservedCrashloop ObservedState = "crashloop"
	ObservedPending   ObservedState = "pending"
	ObservedFailed    ObservedState = "failed"
	ObservedCompleted ObservedState = "completed"
)

// Observed is written only by observation (swarm scans). See convex/schema.ts for the meaning of
// `failed` (update rolled back/paused) and `completed` (every task of the revision exited 0).
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

// Node is a canvas node: service, database, cache, volume or group.
type Node struct {
	ID            string     `json:"id"`
	EnvironmentID string     `json:"environmentId"`
	Type          NodeType   `json:"type"`
	Name          string     `json:"name"`
	ParentID      string     `json:"parentId,omitempty"`
	Position      Position   `json:"position"`
	Config        NodeConfig `json:"config"`
	Desired       *Desired   `json:"desired,omitempty"`
	Observed      *Observed  `json:"observed,omitempty"`
	Endpoints     []Endpoint `json:"endpoints,omitempty"`
	// Last revision observation saw fully converged.
	DeployedRevision *int `json:"deployedRevision,omitempty"`
	// Config or variables (or a variable it references) changed since the last ship.
	Dirty bool `json:"dirty,omitempty"`
	// When the current desired.revision was shipped (unix ms).
	ShippedAt *int64 `json:"shippedAt,omitempty"`
	// Last apply failure (pull error, bad spec) or a deployment timeout. Cleared on the next ship.
	ApplyError string `json:"applyError,omitempty"`
	// The last run exited 0 and nothing kept running (one-shot image).
	OneShot   bool  `json:"oneShot,omitempty"`
	CreatedAt int64 `json:"createdAt"`
}

// ServiceName is the Swarm service of a node.
func (n Node) ServiceName() string { return ServicePrefix + n.ID }

// ServicePrefix names every Keel-managed Swarm service: svc-<nodeId>.
const ServicePrefix = "svc-"
