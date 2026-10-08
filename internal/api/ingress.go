package api

// Public ingress (docs/go/spec/proxy-ingress.md §4). Ports are JSON numbers rather than integers
// so a non-integer gets the API's own sentence ("Port must be 1–65535"), as Convex answered.

// ExposeRequest is POST /api/nodes/{id}/expose. Every field is optional: none exposes a service
// on https://<name>-<hash>.<ip>.sslip.io and a database or cache on tcp <ip>:<its port>.
type ExposeRequest struct {
	Protocol   string   `json:"protocol,omitempty" enum:"http,tcp,udp" doc:"Default: http for a service, tcp for a database or cache"`
	Port       *float64 `json:"port,omitempty" doc:"Container port the proxy dials. Default: the node's port"`
	Domain     *string  `json:"domain,omitempty" doc:"http only: your own hostname, pointed at the control plane's public IP. Default: an sslip.io name"`
	PublicPort *float64 `json:"publicPort,omitempty" doc:"tcp/udp only: port on the control plane. Default: the container port when free, else the first free from 20000"`
}

// UnexposeRequest is POST /api/nodes/{id}/unexpose. No field closes every endpoint ("Make
// private"); otherwise name one: protocol and domain (http) or protocol and public port.
type UnexposeRequest struct {
	Protocol   string   `json:"protocol,omitempty" enum:"http,tcp,udp"`
	Domain     *string  `json:"domain,omitempty"`
	PublicPort *float64 `json:"publicPort,omitempty"`
}

// ControlPlane is GET /api/control-plane: what the dashboard tells users to point at or open.
type ControlPlane struct {
	PublicIP *string `json:"publicIp" doc:"KEEL_PUBLIC_IP, null when this install does not know it"`
}
