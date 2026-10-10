package app

import (
	"net"
	"strconv"
	"strings"

	"github.com/ThallesP/keel/internal/domain"
)

// caddyApps is the `apps` half of keel-proxy's Caddy config for routes (proxy-ingress.md §6.2).
// Listeners use the edge's `host-tcp` / `host-udp` networks (sockets in the host namespace) on
// each address from /keel/host-addrs: never a wildcard, `tailscale serve` holds 443 on the tailnet
// address. Maps marshal with sorted keys; arrays keep route order, so an unchanged set of
// endpoints produces byte-identical JSON and Caddy answers "config is unchanged".
func caddyApps(routes []ProxyRoute, addrs []string, rep proxyReporter, acme proxyACME) map[string]any {
	apps := map[string]any{}
	var web, raw []ProxyRoute
	for _, r := range routes {
		if r.Protocol == domain.ProtocolHTTP {
			web = append(web, r)
		} else {
			raw = append(raw, r)
		}
	}
	if len(web) > 0 {
		listen := make([]string, 0, len(addrs))
		for _, a := range addrs {
			listen = append(listen, "host-tcp/"+net.JoinHostPort(a, "443"))
		}
		handlers := make([]any, 0, len(web))
		managed := []string{}
		for _, r := range web {
			handlers = append(handlers, map[string]any{
				"match":    []any{map[string]any{"host": []string{r.Domain}}},
				"handle":   []any{map[string]any{"handler": "reverse_proxy", "upstreams": []any{map[string]any{"dial": caddyUpstream(r)}}}},
				"terminal": true,
			})
			if !caddyInternalName(r.Domain) {
				managed = append(managed, r.Domain)
			}
		}
		// Automatic HTTPS manages a certificate per host matcher and adds the :80 server (same
		// network and addresses) that redirects to HTTPS and answers ACME HTTP-01 challenges.
		apps["http"] = map[string]any{"servers": map[string]any{"public": map[string]any{
			"listen": listen,
			// HTTP/3 would need a UDP 443 listener in the host namespace too; not yet.
			"protocols": []string{"h1", "h2"},
			"routes":    handlers,
		}}}
		if (acme.CA != "" || acme.Email != "") && len(managed) > 0 {
			apps["tls"] = map[string]any{"automation": map[string]any{"policies": []any{
				map[string]any{"subjects": managed, "issuers": caddyIssuers(acme)},
			}}}
		}
		apps["events"] = map[string]any{"subscriptions": []any{map[string]any{
			"events":   []string{CertObtained, CertFailed},
			"handlers": []any{map[string]any{"handler": "keel", "url": rep.URL, "token": rep.Token}},
		}}}
	}
	if len(raw) > 0 {
		servers := map[string]any{}
		for _, r := range raw {
			listen := make([]string, 0, len(addrs))
			for _, a := range addrs {
				listen = append(listen, "host-"+string(r.Protocol)+"/"+net.JoinHostPort(a, strconv.Itoa(r.PublicPort)))
			}
			dial := caddyUpstream(r)
			if r.Protocol == domain.ProtocolUDP {
				dial = "udp/" + dial
			}
			servers[string(r.Protocol)+"-"+strconv.Itoa(r.PublicPort)] = map[string]any{
				"listen": listen,
				"routes": []any{map[string]any{"handle": []any{map[string]any{
					"handler":   "proxy",
					"upstreams": []any{map[string]any{"dial": []string{dial}}},
				}}}},
			}
		}
		apps["layer4"] = map[string]any{"servers": servers}
	}
	return apps
}

// caddyIssuers: with no `tls` app Caddy uses Let's Encrypt alone (its ZeroSSL fallback needs an
// email). KEEL_ACME_EMAIL gives the pair Caddy would build, Let's Encrypt then ZeroSSL;
// KEEL_ACME_CA (a staging CA) replaces both.
func caddyIssuers(acme proxyACME) []any {
	if acme.CA != "" {
		issuer := map[string]any{"module": "acme", "ca": acme.CA}
		if acme.Email != "" {
			issuer["email"] = acme.Email
		}
		return []any{issuer}
	}
	return []any{
		map[string]any{"module": "acme", "email": acme.Email},
		map[string]any{"module": "acme", "ca": domain.ZeroSSLCA, "email": acme.Email},
	}
}

// caddyUpstream is the node's Swarm service on the keel overlay, resolved by Docker DNS when a
// connection is made: redeploys and rescheduling never touch the proxy.
func caddyUpstream(r ProxyRoute) string {
	return domain.ServicePrefix + r.NodeID + ":" + strconv.Itoa(r.Port)
}

// caddyInternalName: localhost names get Caddy's internal CA through automatic HTTPS (dev), never an
// ACME policy.
func caddyInternalName(d string) bool { return d == "localhost" || strings.HasSuffix(d, ".localhost") }
