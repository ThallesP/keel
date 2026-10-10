package app

import (
	"net"
	"strconv"
	"strings"

	"github.com/ThallesP/keel/internal/domain"
)

type caddyConfig struct {
	HTTP   *caddyServers `json:"http,omitempty"`
	TLS    *caddyTLS     `json:"tls,omitempty"`
	Events *caddyEvents  `json:"events,omitempty"`
	Layer4 *caddyServers `json:"layer4,omitempty"`
}

type caddyServers struct {
	Servers map[string]caddyServer `json:"servers"`
}

type caddyServer struct {
	Listen    []string     `json:"listen"`
	Protocols []string     `json:"protocols,omitempty"`
	Routes    []caddyRoute `json:"routes"`
}

type caddyRoute struct {
	Match    []caddyMatch   `json:"match,omitempty"`
	Handle   []caddyHandler `json:"handle"`
	Terminal bool           `json:"terminal,omitempty"`
}

type caddyMatch struct {
	Host []string `json:"host"`
}

type caddyHandler struct {
	Handler   string          `json:"handler"`
	Upstreams []caddyUpstream `json:"upstreams,omitempty"`
	URL       string          `json:"url,omitempty"`
	Token     string          `json:"token,omitempty"`
}

type caddyUpstream struct {
	Dial any `json:"dial"`
}

type caddyTLS struct {
	Automation caddyAutomation `json:"automation"`
}

type caddyAutomation struct {
	Policies []caddyPolicy `json:"policies"`
}

type caddyPolicy struct {
	Subjects []string      `json:"subjects"`
	Issuers  []caddyIssuer `json:"issuers"`
}

type caddyIssuer struct {
	Module string `json:"module"`
	CA     string `json:"ca,omitempty"`
	Email  string `json:"email,omitempty"`
}

type caddyEvents struct {
	Subscriptions []caddySubscription `json:"subscriptions"`
}

type caddySubscription struct {
	Events   []string       `json:"events"`
	Handlers []caddyHandler `json:"handlers"`
}

func caddyApps(routes []ProxyRoute, addrs []string, reportURL string, c Config) caddyConfig {
	var web []caddyRoute
	var managed []string
	layer4 := map[string]caddyServer{}
	for _, r := range routes {
		dial := domain.ServicePrefix + r.NodeID + ":" + strconv.Itoa(r.Port)
		if r.Protocol == domain.ProtocolHTTP {
			web = append(web, caddyRoute{
				Match:    []caddyMatch{{Host: []string{r.Domain}}},
				Handle:   []caddyHandler{{Handler: "reverse_proxy", Upstreams: []caddyUpstream{{Dial: dial}}}},
				Terminal: true,
			})
			if r.Domain != "localhost" && !strings.HasSuffix(r.Domain, ".localhost") {
				managed = append(managed, r.Domain)
			}
			continue
		}
		if r.Protocol == domain.ProtocolUDP {
			dial = "udp/" + dial
		}
		layer4[string(r.Protocol)+"-"+strconv.Itoa(r.PublicPort)] = caddyServer{
			Listen: caddyListen(string(r.Protocol), addrs, r.PublicPort),
			Routes: []caddyRoute{{Handle: []caddyHandler{{Handler: "proxy", Upstreams: []caddyUpstream{{Dial: []string{dial}}}}}}},
		}
	}
	var apps caddyConfig
	if len(web) > 0 {
		apps.HTTP = &caddyServers{Servers: map[string]caddyServer{
			"public": {Listen: caddyListen("tcp", addrs, 443), Protocols: []string{"h1", "h2"}, Routes: web},
		}}
		apps.Events = &caddyEvents{Subscriptions: []caddySubscription{{
			Events:   []string{CertObtained, CertFailed},
			Handlers: []caddyHandler{{Handler: "keel", URL: reportURL, Token: c.WorkerToken}},
		}}}
	}
	if len(managed) > 0 && (c.ACMECA != "" || c.ACMEEmail != "") {
		issuers := []caddyIssuer{{Module: "acme", CA: c.ACMECA, Email: c.ACMEEmail}}
		if c.ACMECA == "" {
			issuers = append(issuers, caddyIssuer{Module: "acme", CA: "https://acme.zerossl.com/v2/DV90", Email: c.ACMEEmail})
		}
		apps.TLS = &caddyTLS{Automation: caddyAutomation{Policies: []caddyPolicy{{Subjects: managed, Issuers: issuers}}}}
	}
	if len(layer4) > 0 {
		apps.Layer4 = &caddyServers{Servers: layer4}
	}
	return apps
}

func caddyListen(network string, addrs []string, port int) []string {
	listen := make([]string, 0, len(addrs))
	for _, a := range addrs {
		listen = append(listen, "host-"+network+"/"+net.JoinHostPort(a, strconv.Itoa(port)))
	}
	return listen
}
