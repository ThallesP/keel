package app

import (
	"context"

	"github.com/ThallesP/keel/internal/domain"
)

type IngressTx interface {
	IngressHasVariable(nodeID, key string) (bool, error)
	IngressOtherEndpoints(nodeID string) ([]OwnedEndpoint, error)
	IngressRoutes() ([]ProxyRoute, error)
	IngressAnyEndpoint() (bool, error)
	IngressNodesWithDomain(domain string) ([]string, error)
}

type OwnedEndpoint struct {
	Protocol   domain.EndpointProtocol
	Domain     string
	PublicPort int
	Owner      string
}

type ProxyRoute struct {
	NodeID     string
	Protocol   domain.EndpointProtocol
	Port       int
	Domain     string
	PublicPort int
}

func (r ProxyRoute) Key() string {
	return ingressKey(r.Protocol, r.Domain, r.PublicPort)
}

type Proxy interface {
	HostAddrs(ctx context.Context) ([]string, error)
	LoadApps(ctx context.Context, apps []byte) error
	Certs(ctx context.Context, names []string) (map[string]ProxyCert, error)
	ReportURL() string
}

type ProxyRejected struct{ Message string }

func (e *ProxyRejected) Error() string { return e.Message }

type ProxyCert struct {
	State string `json:"state"`
	Error string `json:"error,omitempty"`
}
