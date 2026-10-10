package app

import (
	"net"
	"strconv"
	"strings"

	"github.com/ThallesP/keel/internal/domain"
)

type caddyIssuer struct {
	Module string `json:"module"`
	CA     string `json:"ca,omitempty"`
	Email  string `json:"email,omitempty"`
}

func caddyApps(routes []ProxyRoute, addrs []string, rep proxyReporter, acme proxyACME) map[string]any {
	var handlers []any
	var managed []string
	servers := map[string]any{}
	for _, r := range routes {
		dial := domain.ServicePrefix + r.NodeID + ":" + strconv.Itoa(r.Port)
		if r.Protocol == domain.ProtocolHTTP {
			handlers = append(handlers, map[string]any{
				"match":    []any{map[string]any{"host": []string{r.Domain}}},
				"handle":   []any{map[string]any{"handler": "reverse_proxy", "upstreams": []any{map[string]any{"dial": dial}}}},
				"terminal": true,
			})
			if r.Domain != "localhost" && !strings.HasSuffix(r.Domain, ".localhost") {
				managed = append(managed, r.Domain)
			}
			continue
		}
		if r.Protocol == domain.ProtocolUDP {
			dial = "udp/" + dial
		}
		servers[string(r.Protocol)+"-"+strconv.Itoa(r.PublicPort)] = map[string]any{
			"listen": caddyListen(string(r.Protocol), addrs, r.PublicPort),
			"routes": []any{map[string]any{"handle": []any{map[string]any{
				"handler":   "proxy",
				"upstreams": []any{map[string]any{"dial": []string{dial}}},
			}}}},
		}
	}
	apps := map[string]any{}
	if len(handlers) > 0 {
		apps["http"] = map[string]any{"servers": map[string]any{"public": map[string]any{
			"listen":    caddyListen("tcp", addrs, 443),
			"protocols": []string{"h1", "h2"},
			"routes":    handlers,
		}}}
		apps["events"] = map[string]any{"subscriptions": []any{map[string]any{
			"events":   []string{CertObtained, CertFailed},
			"handlers": []any{map[string]any{"handler": "keel", "url": rep.URL, "token": rep.Token}},
		}}}
	}
	if len(managed) > 0 && (acme.CA != "" || acme.Email != "") {
		issuers := []caddyIssuer{{Module: "acme", CA: acme.CA, Email: acme.Email}}
		if acme.CA == "" {
			issuers = append(issuers, caddyIssuer{Module: "acme", CA: "https://acme.zerossl.com/v2/DV90", Email: acme.Email})
		}
		apps["tls"] = map[string]any{"automation": map[string]any{"policies": []any{
			map[string]any{"subjects": managed, "issuers": issuers},
		}}}
	}
	if len(servers) > 0 {
		apps["layer4"] = map[string]any{"servers": servers}
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
