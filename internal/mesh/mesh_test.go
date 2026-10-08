package mesh

import (
	"context"
	"net/http"
	"strings"
	"testing"
)

func TestTailnetHostname(t *testing.T) {
	tests := map[string]string{
		"node-1":                "keel-agent-node-1",
		"Asgard.local":          "keel-agent-asgard-local",
		"  box_2 ":              "keel-agent-box-2",
		"":                      "keel-agent",
		"---":                   "keel-agent",
		strings.Repeat("a", 80): "keel-agent-" + strings.Repeat("a", 52),
	}
	for in, want := range tests {
		got := tailnetHostname(in)
		if got != want {
			t.Errorf("tailnetHostname(%q) = %q, want %q", in, got, want)
		}
		if len(got) > 63 {
			t.Errorf("tailnetHostname(%q) is %d chars, over a DNS label", in, len(got))
		}
	}
}

// Without an auth key the agent uses the host network: no tsnet node is started.
func TestOpenWithoutAuthKey(t *testing.T) {
	m, err := Open(context.Background(), Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()
	if m.Tailnet || m.Client == nil || m.Client.Transport != http.DefaultTransport {
		t.Fatalf("mesh = %+v, want the default transport", m)
	}
}

func TestOptionsFromEnv(t *testing.T) {
	t.Setenv("KEEL_TS_AUTHKEY", "  tskey-auth-x \n")
	if got := OptionsFromEnv(nil).AuthKey; got != "tskey-auth-x" {
		t.Fatalf("auth key = %q", got)
	}
}
