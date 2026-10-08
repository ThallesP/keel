package api

import "github.com/ThallesP/keel/internal/domain"

// EndpointView is an endpoint as clients see it (docs/go/spec/proxy-ingress.md §3.7). pinnedPort
// and status.at are not exposed. The dashboard's canvas types mirror it.
type EndpointView struct {
	Protocol   string `json:"protocol" enum:"http,tcp,udp"`
	Port       int    `json:"port" doc:"Container port the proxy dials"`
	Domain     string `json:"domain,omitempty"`
	PublicPort *int   `json:"publicPort,omitempty"`
	Address    string `json:"address" doc:"https://<domain>, or <ip>:<publicPort>"`
	State      string `json:"state" enum:"starting,live,failed"`
	Error      string `json:"error,omitempty"`
}

// EndpointViewOf builds the view; publicIP is KEEL_PUBLIC_IP ("" when unknown).
func EndpointViewOf(e domain.Endpoint, publicIP string) EndpointView {
	return EndpointView{
		Protocol:   string(e.Protocol),
		Port:       e.Port,
		Domain:     e.Domain,
		PublicPort: e.PublicPort,
		Address:    e.Address(publicIP),
		State:      string(e.Status.State),
		Error:      e.Status.Error,
	}
}
