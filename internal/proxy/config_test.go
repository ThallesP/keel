//go:build linux

package proxy

import (
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/caddyserver/caddy/v2"
)

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

	var bad caddy.Config
	if err := json.Unmarshal([]byte(`{"admin":{"disabled":true},"apps":{"layer4":{"servers":{"x":{"listen":["tcp/127.0.0.1:1"],"routes":[{"handle":[{"handler":"echo"}]}]}}}}}`), &bad); err != nil {
		t.Fatal(err)
	}
	if err := caddy.Validate(&bad); err == nil || !strings.Contains(err.Error(), "layer4.handlers.echo") {
		t.Fatalf("unknown module accepted: %v", err)
	}
}
