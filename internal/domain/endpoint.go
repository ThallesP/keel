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

// Key is the endpoint's identity across the install: http:<domain>, tcp:<publicPort>, udp:<…>.
func (e Endpoint) Key() string {
	if e.Protocol == ProtocolHTTP {
		return "http:" + e.Domain
	}
	port := 0
	if e.PublicPort != nil {
		port = *e.PublicPort
	}
	return string(e.Protocol) + ":" + itoa(port)
}

// Address is how the endpoint is reached: https://<domain>, or <ip>:<publicPort> (the literal
// text "<public IP>" when KEEL_PUBLIC_IP is unknown). docs/go/spec/proxy-ingress.md §3.6.
func (e Endpoint) Address(publicIP string) string {
	if e.Protocol == ProtocolHTTP {
		return "https://" + e.Domain
	}
	if publicIP == "" {
		publicIP = "<public IP>"
	}
	port := 0
	if e.PublicPort != nil {
		port = *e.PublicPort
	}
	return publicIP + ":" + itoa(port)
}

func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	neg := i < 0
	if neg {
		i = -i
	}
	var b [20]byte
	n := len(b)
	for i > 0 {
		n--
		b[n] = byte('0' + i%10)
		i /= 10
	}
	if neg {
		n--
		b[n] = '-'
	}
	return string(b[n:])
}
