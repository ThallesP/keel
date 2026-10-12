package mesh

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"time"

	"tailscale.com/tsnet"
)

func Open(ctx context.Context, log *slog.Logger) (*http.Client, func(), error) {
	authKey := os.Getenv("KEEL_TS_AUTHKEY")
	if authKey == "" {
		return http.DefaultClient, func() {}, nil
	}
	host, _ := os.Hostname()
	dir, err := os.MkdirTemp("", "keel-agent-tsnet-")
	if err != nil {
		return nil, nil, err
	}
	srv := &tsnet.Server{
		Hostname: tailnetHostname(host), AuthKey: authKey, Ephemeral: true, Dir: dir,
		UserLogf: func(format string, args ...any) { log.Info(fmt.Sprintf(format, args...)) },
	}
	closeTailnet := func() {
		srv.Close()
		os.RemoveAll(dir)
	}
	upCtx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	if _, err := srv.Up(upCtx); err != nil {
		closeTailnet()
		return nil, nil, fmt.Errorf("tailnet: %w", err)
	}
	log.Info("joined the tailnet", "hostname", srv.Hostname)
	return srv.HTTPClient(), closeTailnet, nil
}

func tailnetHostname(host string) string {
	name := strings.Trim(strings.Map(func(r rune) rune {
		if r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '-' {
			return r
		}
		return '-'
	}, strings.ToLower(host)), "-")
	if name == "" {
		return "keel-agent"
	}
	name = "keel-agent-" + name
	if len(name) > 63 {
		name = strings.TrimRight(name[:63], "-")
	}
	return name
}
