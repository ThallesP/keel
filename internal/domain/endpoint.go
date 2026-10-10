package domain

import (
	"cmp"
	"strconv"
)

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
	State EndpointState
	Error string
	At    int64
}

type Endpoint struct {
	ID         string
	NodeID     string
	Protocol   EndpointProtocol
	Port       int
	PinnedPort bool
	Domain     string
	PublicPort *int
	Status     EndpointStatus
}

func (e Endpoint) Key() string {
	if e.Protocol == ProtocolHTTP {
		return "http:" + e.Domain
	}
	return string(e.Protocol) + ":" + e.publicPort()
}

func (e Endpoint) Address(publicIP string) string {
	if e.Protocol == ProtocolHTTP {
		return "https://" + e.Domain
	}
	return cmp.Or(publicIP, "<public IP>") + ":" + e.publicPort()
}

func (e Endpoint) publicPort() string {
	if e.PublicPort == nil {
		return "0"
	}
	return strconv.Itoa(*e.PublicPort)
}
