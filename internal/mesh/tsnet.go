package mesh

import (
	"context"
	"fmt"
	"os"
	"time"

	"tailscale.com/tsnet"
)

const upTimeout = 2 * time.Minute

func openTailnet(ctx context.Context, opts Options) (*Mesh, error) {
	host := opts.Hostname
	if host == "" {
		host, _ = os.Hostname()
	}
	dir, err := os.MkdirTemp("", "keel-agent-tsnet-")
	if err != nil {
		return nil, err
	}
	userLogf := opts.Logf
	if userLogf == nil {
		userLogf = func(string, ...any) {}
	}
	srv := &tsnet.Server{
		Hostname:  tailnetHostname(host),
		AuthKey:   opts.AuthKey,
		Ephemeral: true,
		Dir:       dir,
		Logf:      func(string, ...any) {},
		UserLogf:  userLogf,
	}
	upCtx, cancel := context.WithTimeout(ctx, upTimeout)
	defer cancel()
	if _, err := srv.Up(upCtx); err != nil {
		srv.Close()
		os.RemoveAll(dir)
		return nil, fmt.Errorf("tailnet: %w", err)
	}
	userLogf("joined the tailnet as %s", srv.Hostname)
	return &Mesh{
		Client:  srv.HTTPClient(),
		Tailnet: true,
		close: func() error {
			err := srv.Close()
			os.RemoveAll(dir)
			return err
		},
	}, nil
}
