package proxy

import (
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/caddyserver/caddy/v2"
)

// The full example config of proxy-ingress.md §6.3 (what the control plane's builder produces,
// see app/proxy_config_test.go) provisions with exactly the modules this edge compiles in: every
// app, handler, matcher, issuer and network name resolves. Validate provisions without starting,
// so nothing listens and no CA is contacted.
func TestSpecConfigProvisions(t *testing.T) {
	b, err := os.ReadFile("testdata/spec-6.3.json")
	if err != nil {
		t.Fatal(err)
	}
	var cfg caddy.Config
	if err := json.Unmarshal(b, &cfg); err != nil {
		t.Fatal(err)
	}
	cfg.Admin = &caddy.AdminConfig{Disabled: true}
	if err := caddy.Validate(&cfg); err != nil {
		t.Fatal(err)
	}

	// A module this edge does not carry is refused, so the check above means something.
	var bad caddy.Config
	if err := json.Unmarshal([]byte(`{"admin":{"disabled":true},"apps":{"layer4":{"servers":{"x":{"listen":["tcp/127.0.0.1:1"],"routes":[{"handle":[{"handler":"echo"}]}]}}}}}`), &bad); err != nil {
		t.Fatal(err)
	}
	if err := caddy.Validate(&bad); err == nil || !strings.Contains(err.Error(), "layer4.handlers.echo") {
		t.Fatalf("unknown module accepted: %v", err)
	}
}
