package app

import (
	"context"

	"github.com/ThallesP/keel/internal/domain"
)

// IngressTx: endpoint lookups beyond CoreTx (endpoints are written with ReplaceEndpoints).
// Owner: the ingress area (docs/go/spec/proxy-ingress.md). Method names carry the area so the
// composite Tx never sees two areas declare the same one.
type IngressTx interface {
	// IngressHasVariable: the node has a variable named key (the Redis guard).
	IngressHasVariable(nodeID, key string) (bool, error)
	// IngressOtherEndpoints: every endpoint of every node except nodeID, install-wide (all
	// organizations), with the owner's name, in node creation order then endpoint order.
	IngressOtherEndpoints(nodeID string) ([]OwnedEndpoint, error)
	// IngressRoutes: every endpoint of the install as the proxy serves it, ordered by node
	// creation then endpoint order (deterministic: it orders routes and ACME subjects).
	IngressRoutes() ([]ProxyRoute, error)
	// IngressAnyEndpoint: at least one endpoint exists.
	IngressAnyEndpoint() (bool, error)
	// IngressNodesWithDomain: ids of the nodes with an http endpoint on domain.
	IngressNodesWithDomain(domain string) ([]string, error)
}

// OwnedEndpoint is another node's endpoint, as expose's collision checks need it.
type OwnedEndpoint struct {
	Protocol   domain.EndpointProtocol
	Domain     string
	PublicPort int // 0 for http
	Owner      string
}

// ProxyRoute is one endpoint as keel-proxy serves it (proxy-ingress.md §5.2).
type ProxyRoute struct {
	NodeID     string
	Protocol   domain.EndpointProtocol
	Port       int
	Domain     string // http only
	PublicPort int    // tcp/udp only
}

// Key is the route's endpointKey (http:<domain>, tcp:<publicPort>, udp:<publicPort>).
func (r ProxyRoute) Key() string {
	return ingressKey(r.Protocol, r.Domain, r.PublicPort)
}

// Proxy is keel-proxy's admin API over its unix socket (adapters/caddy). Errors are already the
// sentence to show: Caddy's own message with its "loading config: " prefixes stripped, or
// "keel-proxy is not running (no admin socket at …)", or "keel-proxy did not answer within 30s".
type Proxy interface {
	// HostAddrs: GET /keel/host-addrs, the host addresses the proxy binds (nil when none).
	HostAddrs(ctx context.Context) ([]string, error)
	// LoadApps: POST /config/apps with the whole `apps` object. A load Caddy refused (status ≥ 300)
	// is a *ProxyRejected; anything else means the proxy could not be asked.
	LoadApps(ctx context.Context, apps []byte) error
	// Certs: GET /keel/certs?name=…, what the proxy knows about each name's certificate.
	Certs(ctx context.Context, names []string) (map[string]ProxyCert, error)
	// ReportURL is where the proxy POSTs certificate reports (KEEL_PROXY_REPORT_URL). "" means
	// <KEEL_SITE_URL>/proxy/events.
	ReportURL() string
}

// ProxyRejected: Caddy answered a load with an error and keeps serving its previous config.
// Message is Caddy's error without its "loading (new) config: " prefixes.
type ProxyRejected struct{ Message string }

func (e *ProxyRejected) Error() string { return e.Message }

// ProxyCert is one name's certificate as keel-proxy knows it: state ok | failed | pending.
type ProxyCert struct {
	State string `json:"state"`
	Error string `json:"error,omitempty"`
}
