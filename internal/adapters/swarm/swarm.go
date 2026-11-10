package swarm

import (
	"encoding/base64"
	"encoding/json"

	"github.com/distribution/reference"
	"github.com/moby/moby/api/types/registry"
	"github.com/moby/moby/client"

	"github.com/ThallesP/keel/internal/app"
)

type Swarm struct {
	cli          *client.Client
	registryHost string
	registryAuth string
}

func New(reg app.Registry) (*Swarm, error) {
	cli, err := client.New(client.FromEnv)
	if err != nil {
		return nil, err
	}
	s := &Swarm{cli: cli}
	if reg.Host == "" {
		return s, nil
	}
	auth, err := json.Marshal(registry.AuthConfig{
		Username: reg.Username, Password: reg.Password, ServerAddress: reg.Host,
	})
	if err != nil {
		return nil, err
	}
	s.registryHost, s.registryAuth = reg.Host, base64.URLEncoding.EncodeToString(auth)
	return s, nil
}

func (s *Swarm) Close() error { return s.cli.Close() }

func (s *Swarm) authFor(image string) string {
	named, err := reference.ParseNormalizedNamed(image)
	if err != nil || s.registryHost == "" || reference.Domain(named) != s.registryHost {
		return ""
	}
	return s.registryAuth
}
