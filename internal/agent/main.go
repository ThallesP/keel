package agent

import (
	"context"
	"log/slog"
	"os"

	"github.com/ThallesP/keel/internal/mesh"
)

func Main(ctx context.Context) error {
	cfg, err := configFrom(os.Getenv, "/run/secrets/keel_worker_token")
	if err != nil {
		return err
	}
	log := slog.New(slog.NewTextHandler(os.Stderr, nil))
	docker, err := NewMobyDocker()
	if err != nil {
		return err
	}
	defer docker.Close()
	controlPlane, closeMesh, err := mesh.Open(ctx, log)
	if err != nil {
		return err
	}
	defer closeMesh()
	return New(cfg, docker, controlPlane, log).Run(ctx)
}
