package domain

import "strconv"

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

type Endpoint struct {
	ID         string           `json:"id"`
	NodeID     string           `json:"nodeId"`
	Protocol   EndpointProtocol `json:"protocol"`
	Port       int              `json:"port"`
	PinnedPort bool             `json:"pinnedPort,omitempty"`
	Domain     string           `json:"domain,omitempty"`
	PublicPort *int             `json:"publicPort,omitempty"`
	Status     EndpointStatus   `json:"status"`
}

func (e Endpoint) Key() string {
	if e.Protocol == ProtocolHTTP {
		return "http:" + e.Domain
	}
	port := 0
	if e.PublicPort != nil {
		port = *e.PublicPort
	}
	return string(e.Protocol) + ":" + strconv.Itoa(port)
}

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
	return publicIP + ":" + strconv.Itoa(port)
}
