package api

type ExposeRequest struct {
	Protocol   string   `json:"protocol,omitempty" enum:"http,tcp,udp" doc:"Default: http for a service, tcp for a database or cache"`
	Port       *float64 `json:"port,omitempty" doc:"Container port the proxy dials. Default: the node's port"`
	Domain     *string  `json:"domain,omitempty" doc:"http only: your own hostname, pointed at the control plane's public IP. Default: an sslip.io name"`
	PublicPort *float64 `json:"publicPort,omitempty" doc:"tcp/udp only: port on the control plane. Default: the container port when free, else the first free from 20000"`
}

type UnexposeRequest struct {
	Protocol   string   `json:"protocol,omitempty" enum:"http,tcp,udp"`
	Domain     *string  `json:"domain,omitempty"`
	PublicPort *float64 `json:"publicPort,omitempty"`
}

type ControlPlane struct {
	PublicIP *string `json:"publicIp" doc:"KEEL_PUBLIC_IP, null when this install does not know it"`
}
