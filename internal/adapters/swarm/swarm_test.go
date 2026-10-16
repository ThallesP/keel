package swarm

import (
	"encoding/base64"
	"encoding/json"
	"testing"

	"github.com/moby/moby/api/types/registry"

	"github.com/ThallesP/keel/internal/app"
)

func TestRegistryAuthOnlyForKeelsRegistry(t *testing.T) {
	s, err := New(app.Registry{Host: "100.64.0.7:5000", Username: "keel", Password: "pw"})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	for image, wantAuth := range map[string]bool{
		"100.64.0.7:5000/acme/api:3":             true,
		"100.64.0.7:5000/acme/api@sha256:" + sha: true,
		"100.64.0.7:5001/acme/api:3":             false,
		"nginx:alpine":                           false,
		"ghcr.io/thallesp/keel:latest":           false,
		"not a reference":                        false,
	} {
		if got := s.authFor(image) != ""; got != wantAuth {
			t.Errorf("authFor(%q) sent credentials = %v, want %v", image, got, wantAuth)
		}
	}

	raw, err := base64.URLEncoding.DecodeString(s.authFor("100.64.0.7:5000/acme/api:3"))
	if err != nil {
		t.Fatal(err)
	}
	var auth registry.AuthConfig
	if err := json.Unmarshal(raw, &auth); err != nil {
		t.Fatal(err)
	}
	if auth.Username != "keel" || auth.Password != "pw" || auth.ServerAddress != "100.64.0.7:5000" {
		t.Fatalf("X-Registry-Auth carries %+v", auth)
	}
}

func TestNoRegistryNoAuth(t *testing.T) {
	s, err := New(app.Registry{})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if s.authFor("100.64.0.7:5000/acme/api:3") != "" {
		t.Fatal("credentials sent without a registry")
	}
}

const sha = "3f2b6a4c3d5e6f708192a3b4c5d6e7f8091a2b3c4d5e6f708192a3b4c5d6e7f8"
