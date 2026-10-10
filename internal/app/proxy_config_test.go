package app

import (
	"encoding/json"
	"maps"
	"os"
	"reflect"
	"testing"

	"github.com/ThallesP/keel/internal/domain"
)

var (
	routes63 = []ProxyRoute{
		{NodeID: "j57a8x2kq3n4m5p6r7s8t9v0w1x2y3z4", Protocol: domain.ProtocolHTTP, Port: 8080, Domain: "api-16w41g.203-0-113-7.sslip.io"},
		{NodeID: "j57a8x2kq3n4m5p6r7s8t9v0w1x2y3z4", Protocol: domain.ProtocolHTTP, Port: 8080, Domain: "app.example.com"},
		{NodeID: "k17cs4z0mn2mr6yr7gx8tjqe1n7rw9qj", Protocol: domain.ProtocolTCP, Port: 5432, PublicPort: 5432},
		{NodeID: "jd7f9g6h5k4m3n2p1q0r9s8t7v6w5x4y", Protocol: domain.ProtocolUDP, Port: 27015, PublicPort: 27015},
	}
	addrs63  = []string{"203.0.113.7", "2001:db8::1"}
	report63 = "http://100.64.0.1:3211/proxy/events"
	token63  = "<KEEL_WORKER_TOKEN>"
)

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
	b, err := os.ReadFile("../proxy/testdata/spec-6.3.json")
	if err != nil {
		t.Fatal(err)
	}
	var golden struct{ Apps json.RawMessage }
	if err := json.Unmarshal(b, &golden); err != nil {
		t.Fatal(err)
	}
	sameJSON(t, caddyApps(routes63, addrs63, report63, Config{WorkerToken: token63, ACMEEmail: "ops@example.com"}), string(golden.Apps))
}

func TestCaddyAppsVariants(t *testing.T) {
	staging := "https://acme-staging-v02.api.letsencrypt.org/directory"
	web := routes63[:1]
	t.Run("staging CA without email", func(t *testing.T) {
		apps := caddyApps(web, addrs63[:1], report63, Config{ACMECA: staging})
		sameJSON(t, apps.TLS, `{"automation":{"policies":[{"subjects":["api-16w41g.203-0-113-7.sslip.io"],"issuers":[{"module":"acme","ca":"`+staging+`"}]}]}}`)
	})
	t.Run("staging CA with email", func(t *testing.T) {
		apps := caddyApps(web, addrs63[:1], report63, Config{ACMECA: staging, ACMEEmail: "ops@example.com"})
		sameJSON(t, apps.TLS, `{"automation":{"policies":[{"subjects":["api-16w41g.203-0-113-7.sslip.io"],"issuers":[{"module":"acme","ca":"`+staging+`","email":"ops@example.com"}]}]}}`)
	})
	t.Run("no ACME settings: no tls app", func(t *testing.T) {
		apps := caddyApps(web, addrs63[:1], report63, Config{})
		if apps.TLS != nil || apps.Events == nil {
			t.Fatalf("tls %v, events %v", apps.TLS, apps.Events)
		}
	})
	t.Run("localhost names are never in a policy", func(t *testing.T) {
		local := []ProxyRoute{
			{NodeID: "a", Protocol: domain.ProtocolHTTP, Port: 80, Domain: "app.localhost"},
			{NodeID: "b", Protocol: domain.ProtocolHTTP, Port: 80, Domain: "localhost"},
		}
		if apps := caddyApps(local, addrs63[:1], report63, Config{ACMECA: staging}); apps.TLS != nil {
			t.Fatal("tls app for localhost names only")
		}
		apps := caddyApps(append(local, web...), addrs63[:1], report63, Config{ACMECA: staging})
		sameJSON(t, apps.TLS, `{"automation":{"policies":[{"subjects":["api-16w41g.203-0-113-7.sslip.io"],"issuers":[{"module":"acme","ca":"`+staging+`"}]}]}}`)
	})
	t.Run("tcp only: no http, tls or events", func(t *testing.T) {
		apps := caddyApps(routes63[2:3], addrs63[:1], report63, Config{ACMEEmail: "ops@example.com"})
		sameJSON(t, apps, `{"layer4":{"servers":{"tcp-5432":{"listen":["host-tcp/203.0.113.7:5432"],"routes":[{"handle":[{"handler":"proxy","upstreams":[{"dial":["svc-k17cs4z0mn2mr6yr7gx8tjqe1n7rw9qj:5432"]}]}]}]}}}}`)
	})
	t.Run("nothing exposed", func(t *testing.T) {
		sameJSON(t, caddyApps(nil, nil, report63, Config{ACMEEmail: "ops@example.com"}), `{}`)
	})
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
		if !maps.Equal(got, c.want) {
			t.Errorf("%s:\n got %v\nwant %v", c.name, got, c.want)
		}
	}
}
