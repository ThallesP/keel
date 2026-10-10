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
		if got := tailnetHostname(in); got != want {
			t.Errorf("tailnetHostname(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestOpenWithoutAuthKey(t *testing.T) {
	t.Setenv("KEEL_TS_AUTHKEY", "")
	client, closeMesh, err := Open(context.Background(), nil)
	if err != nil || client != http.DefaultClient {
		t.Fatalf("Open = %v, %v", client, err)
	}
	closeMesh()
}
