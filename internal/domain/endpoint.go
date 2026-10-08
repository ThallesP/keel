package domain

// EndpointProtocol: http = https://<domain> on the control plane's 80/443; tcp/udp =
// <public IP>:<publicPort>. Both → svc-<id>:<port>. See docs/networking.md.
type EndpointProtocol string

const (
	ProtocolHTTP EndpointProtocol = "http"
	ProtocolTCP  EndpointProtocol = "tcp"
	ProtocolUDP  EndpointProtocol = "udp"
)

type EndpointState string

const (
	EndpointStarting EndpointState = "starting"
	EndpointLive     EndpointState = "live"
	EndpointFailed   EndpointState = "failed"
)

type EndpointStatus struct {
	State EndpointState `json:"state"`
	Error string        `json:"error,omitempty"`
	At    int64         `json:"at"`
}

// Endpoint is one way in from the internet, served by keel-proxy.
type Endpoint struct {
	ID       string           `json:"id"`
	NodeID   string           `json:"nodeId"`
	Protocol EndpointProtocol `json:"protocol"`
	// The container port the proxy dials.
	Port int `json:"port"`
	// Expose was given a container port other than the node's; otherwise Port follows the
	// node's port each time a change to it ships.
	PinnedPort bool           `json:"pinnedPort,omitempty"`
	Domain     string         `json:"domain,omitempty"`     // http only; unique per install
	PublicPort *int           `json:"publicPort,omitempty"` // tcp/udp only; unique per protocol
	Status     EndpointStatus `json:"status"`
}
