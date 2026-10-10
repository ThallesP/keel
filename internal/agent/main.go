package agent

import (
	"context"
	"fmt"
	"net/http"
	"os"

	"github.com/ThallesP/keel/internal/mesh"
)

func Main(ctx context.Context) error {
	cfg, err := ConfigFromEnv()
	if err != nil {
		return err
	}
	log := NewLogger(os.Stderr)
	docker, err := NewMobyDocker(cfg.DockerSocket)
	if err != nil {
		return err
	}
	defer docker.Close()
	m, err := mesh.Open(ctx, mesh.OptionsFromEnv(func(format string, args ...any) {
		log.Log("mesh", fmt.Sprintf(format, args...))
	}))
	if err != nil {
		return err
	}
	defer m.Close()
	return New(cfg, docker, m.Client, &http.Client{Transport: http.DefaultTransport}, log).Run(ctx)
}
