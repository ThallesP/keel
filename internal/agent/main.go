package agent

import (
	"context"
	"log/slog"
	"net/http"
	"os"
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
	return New(cfg, docker, http.DefaultClient, log).Run(ctx)
}
