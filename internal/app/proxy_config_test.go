package app

import (
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/ThallesP/keel/internal/domain"
)

// The full example of proxy-ingress.md §6.3, base config included.
const golden63 = `{
  "admin": { "listen": "unix//run/keel-proxy/admin.sock|0600" },
  "apps": {
    "http": {
      "servers": {
        "public": {
          "listen": ["host-tcp/203.0.113.7:443", "host-tcp/[2001:db8::1]:443"],
          "protocols": ["h1", "h2"],
          "routes": [
            {
              "match": [{ "host": ["api-16w41g.203-0-113-7.sslip.io"] }],
              "handle": [{ "handler": "reverse_proxy", "upstreams": [{ "dial": "svc-j57a8x2kq3n4m5p6r7s8t9v0w1x2y3z4:8080" }] }],
              "terminal": true
            },
            {
              "match": [{ "host": ["app.example.com"] }],
              "handle": [{ "handler": "reverse_proxy", "upstreams": [{ "dial": "svc-j57a8x2kq3n4m5p6r7s8t9v0w1x2y3z4:8080" }] }],
              "terminal": true
            }
          ]
        }
      }
    },
    "tls": {
      "automation": {
        "policies": [
          {
            "subjects": ["api-16w41g.203-0-113-7.sslip.io", "app.example.com"],
            "issuers": [
              { "module": "acme", "email": "ops@example.com" },
              { "module": "acme", "ca": "https://acme.zerossl.com/v2/DV90", "email": "ops@example.com" }
            ]
          }
        ]
      }
    },
    "events": {
      "subscriptions": [
        {
          "events": ["cert_obtained", "cert_failed"],
          "handlers": [{ "handler": "keel", "url": "http://100.64.0.1:3211/proxy/events", "token": "<KEEL_WORKER_TOKEN>" }]
        }
      ]
    },
    "layer4": {
      "servers": {
        "tcp-5432": {
          "listen": ["host-tcp/203.0.113.7:5432", "host-tcp/[2001:db8::1]:5432"],
          "routes": [{ "handle": [{ "handler": "proxy", "upstreams": [{ "dial": ["svc-k17cs4z0mn2mr6yr7gx8tjqe1n7rw9qj:5432"] }] }] }]
        },
        "udp-27015": {
          "listen": ["host-udp/203.0.113.7:27015", "host-udp/[2001:db8::1]:27015"],
          "routes": [{ "handle": [{ "handler": "proxy", "upstreams": [{ "dial": ["udp/svc-jd7f9g6h5k4m3n2p1q0r9s8t7v6w5x4y:27015"] }] }] }]
        }
      }
    }
  }
}`

var (
	routes63 = []ProxyRoute{
		{NodeID: "j57a8x2kq3n4m5p6r7s8t9v0w1x2y3z4", Protocol: domain.ProtocolHTTP, Port: 8080, Domain: "api-16w41g.203-0-113-7.sslip.io"},
		{NodeID: "j57a8x2kq3n4m5p6r7s8t9v0w1x2y3z4", Protocol: domain.ProtocolHTTP, Port: 8080, Domain: "app.example.com"},
		{NodeID: "k17cs4z0mn2mr6yr7gx8tjqe1n7rw9qj", Protocol: domain.ProtocolTCP, Port: 5432, PublicPort: 5432},
		{NodeID: "jd7f9g6h5k4m3n2p1q0r9s8t7v6w5x4y", Protocol: domain.ProtocolUDP, Port: 27015, PublicPort: 27015},
	}
	addrs63    = []string{"203.0.113.7", "2001:db8::1"}
	reporter63 = proxyReporter{URL: "http://100.64.0.1:3211/proxy/events", Token: "<KEEL_WORKER_TOKEN>"}
)

// sameJSON compares two JSON documents structurally (key order does not matter, array order does).
func sameJSON(t *testing.T, got any, want string) {
	t.Helper()
	b, err := json.Marshal(got)
	if err != nil {
		t.Fatal(err)
	}
	var g, w any
	if err := json.Unmarshal(b, &g); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal([]byte(want), &w); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(g, w) {
		pretty, _ := json.MarshalIndent(g, "", "  ")
		t.Fatalf("config mismatch:\n got %s\nwant %s", pretty, want)
	}
}

func TestCaddyAppsGolden(t *testing.T) {
	apps := caddyApps(routes63, addrs63, reporter63, proxyACME{Email: "ops@example.com"})
	full := map[string]any{"admin": map[string]any{"listen": "unix//run/keel-proxy/admin.sock|0600"}, "apps": apps}
	sameJSON(t, full, golden63)
}

func TestCaddyAppsVariants(t *testing.T) {
	staging := "https://acme-staging-v02.api.letsencrypt.org/directory"
	web := routes63[:1]
	t.Run("staging CA without email", func(t *testing.T) {
		apps := caddyApps(web, addrs63[:1], reporter63, proxyACME{CA: staging})
		sameJSON(t, apps["tls"], `{"automation":{"policies":[{"subjects":["api-16w41g.203-0-113-7.sslip.io"],"issuers":[{"module":"acme","ca":"`+staging+`"}]}]}}`)
	})
	t.Run("staging CA with email", func(t *testing.T) {
		apps := caddyApps(web, addrs63[:1], reporter63, proxyACME{CA: staging, Email: "ops@example.com"})
		sameJSON(t, apps["tls"], `{"automation":{"policies":[{"subjects":["api-16w41g.203-0-113-7.sslip.io"],"issuers":[{"module":"acme","ca":"`+staging+`","email":"ops@example.com"}]}]}}`)
	})
	t.Run("no ACME settings: no tls app", func(t *testing.T) {
		apps := caddyApps(web, addrs63[:1], reporter63, proxyACME{})
		if _, ok := apps["tls"]; ok {
			t.Fatal("tls app without ACME settings")
		}
		if _, ok := apps["events"]; !ok {
			t.Fatal("events app missing")
		}
	})
	t.Run("localhost names are never in a policy", func(t *testing.T) {
		local := []ProxyRoute{
			{NodeID: "a", Protocol: domain.ProtocolHTTP, Port: 80, Domain: "app.localhost"},
			{NodeID: "b", Protocol: domain.ProtocolHTTP, Port: 80, Domain: "localhost"},
		}
		apps := caddyApps(local, addrs63[:1], reporter63, proxyACME{CA: staging})
		if _, ok := apps["tls"]; ok {
			t.Fatal("tls app for localhost names only")
		}
		apps = caddyApps(append(local, web...), addrs63[:1], reporter63, proxyACME{CA: staging})
		sameJSON(t, apps["tls"], `{"automation":{"policies":[{"subjects":["api-16w41g.203-0-113-7.sslip.io"],"issuers":[{"module":"acme","ca":"`+staging+`"}]}]}}`)
	})
	t.Run("tcp only: no http, tls or events", func(t *testing.T) {
		apps := caddyApps(routes63[2:3], addrs63[:1], reporter63, proxyACME{Email: "ops@example.com"})
		sameJSON(t, apps, `{"layer4":{"servers":{"tcp-5432":{"listen":["host-tcp/203.0.113.7:5432"],"routes":[{"handle":[{"handler":"proxy","upstreams":[{"dial":["svc-k17cs4z0mn2mr6yr7gx8tjqe1n7rw9qj:5432"]}]}]}]}}}}`)
	})
	t.Run("nothing exposed", func(t *testing.T) {
		sameJSON(t, caddyApps(nil, nil, reporter63, proxyACME{Email: "ops@example.com"}), `{}`)
	})
}

func TestCaddyAppsDeterministic(t *testing.T) {
	a, _ := json.Marshal(caddyApps(routes63, addrs63, reporter63, proxyACME{Email: "x@y.z"}))
	for range 20 {
		b, _ := json.Marshal(caddyApps(routes63, addrs63, reporter63, proxyACME{Email: "x@y.z"}))
		if string(a) != string(b) {
			t.Fatal("builder output is not byte-stable")
		}
	}
}

func TestProxyBlameListener(t *testing.T) {
	routes := []ProxyRoute{
		{NodeID: "a", Protocol: domain.ProtocolHTTP, Port: 8080, Domain: "a.example.com"},
		{NodeID: "b", Protocol: domain.ProtocolHTTP, Port: 8080, Domain: "b.example.com"},
		{NodeID: "c", Protocol: domain.ProtocolTCP, Port: 5432, PublicPort: 5432},
		{NodeID: "d", Protocol: domain.ProtocolUDP, Port: 53, PublicPort: 53},
		{NodeID: "e", Protocol: domain.ProtocolUDP, Port: 443, PublicPort: 443},
	}
	cases := []struct {
		name, message string
		want          map[string]string
	}{
		{
			"tcp port in use",
			"layer4 app module: start: listening on host-tcp/203.0.113.7:5432: listen tcp 203.0.113.7:5432: bind: address already in use",
			map[string]string{"tcp:5432": "Port 5432/tcp is already in use on the control plane"},
		},
		{
			"443 blames every http endpoint (IPv6 listener)",
			"http app module: start: listening on host-tcp/[2001:db8::1]:443: listen tcp [2001:db8::1]:443: bind: address already in use",
			map[string]string{
				"http:a.example.com": "Port 443/tcp is already in use on the control plane",
				"http:b.example.com": "Port 443/tcp is already in use on the control plane",
			},
		},
		{
			"80 too",
			"http app module: start: listening on host-tcp/203.0.113.7:80: listen tcp 203.0.113.7:80: bind: address already in use",
			map[string]string{
				"http:a.example.com": "Port 80/tcp is already in use on the control plane",
				"http:b.example.com": "Port 80/tcp is already in use on the control plane",
			},
		},
		{
			"udp other reason",
			"layer4 app module: start: listen udp 203.0.113.7:53: bind: permission denied",
			map[string]string{"udp:53": "Cannot listen on 53/udp: bind: permission denied"},
		},
		{
			"udp 443 is a normal udp port",
			"layer4 app module: start: listen udp 203.0.113.7:443: bind: address already in use",
			map[string]string{"udp:443": "Port 443/udp is already in use on the control plane"},
		},
		{
			"reason stops at a newline",
			"listen tcp 203.0.113.7:5432: bind: address already in use\nmore text",
			map[string]string{"tcp:5432": "Port 5432/tcp is already in use on the control plane"},
		},
		{"port of nobody live", "listen tcp 203.0.113.7:6379: bind: address already in use", map[string]string{}},
		{"no listener named", "json: cannot unmarshal string", nil},
	}
	for _, c := range cases {
		got := blameListener(c.message, routes)
		if len(got) != len(c.want) || (len(c.want) > 0 && !reflect.DeepEqual(got, c.want)) {
			t.Errorf("%s:\n got %v\nwant %v", c.name, got, c.want)
		}
	}
}

func TestProxyErrorText(t *testing.T) {
	if got := proxyErrorText(errors.New("  a\n  b\tc ")); got != "a b c" {
		t.Errorf("got %q", got)
	}
	if got := proxyErrorText(errors.New(strings.Repeat("x", 400))); len(got) != 300 {
		t.Errorf("len %d", len(got))
	}
}

func TestIngressEngineIsRedis(t *testing.T) {
	cases := map[string]bool{
		"redis:7": true, "redis": true, "docker.io/library/redis:7-alpine": true,
		"redis@sha256:abc": true, "ghcr.io/acme/redis:1@sha256:abc": true,
		"redis-stack:7": false, "postgres:16": false, "valkey/valkey:8": false, "redisx": false,
	}
	for image, want := range cases {
		if got := engineIsRedis(image); got != want {
			t.Errorf("engineIsRedis(%q) = %v", image, got)
		}
	}
}

func TestIngressUnexposeKey(t *testing.T) {
	f := func(v float64) *float64 { return &v }
	cases := []struct {
		protocol domain.EndpointProtocol
		name     string
		public   *float64
		key      string
		ok       bool
	}{
		{domain.ProtocolHTTP, "app.example.com", nil, "http:app.example.com", true},
		{domain.ProtocolHTTP, "", nil, "http:", true},
		{domain.ProtocolTCP, "", f(5432), "tcp:5432", true},
		{domain.ProtocolUDP, "", f(27015), "udp:27015", true},
		{domain.ProtocolTCP, "", f(5432.5), "", false},
		{domain.ProtocolTCP, "", nil, "", false},
	}
	for _, c := range cases {
		key, ok := unexposeKey(c.protocol, c.name, c.public)
		if key != c.key || ok != c.ok {
			t.Errorf("unexposeKey(%s, %q, %v) = %q %v", c.protocol, c.name, c.public, key, ok)
		}
	}
}
