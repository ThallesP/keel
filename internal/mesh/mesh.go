package mesh

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"

	"tailscale.com/tsnet"
)

type Options struct {
	AuthKey string
	Logf    func(format string, args ...any)
}

type Mesh struct {
	Client *http.Client
	close  func() error
}

func (m *Mesh) Close() error {
	if m.close == nil {
		return nil
	}
	return m.close()
}

func OptionsFromEnv(logf func(format string, args ...any)) Options {
	return Options{AuthKey: strings.TrimSpace(os.Getenv("KEEL_TS_AUTHKEY")), Logf: logf}
}

func Open(ctx context.Context, opts Options) (*Mesh, error) {
	if opts.AuthKey == "" {
		return &Mesh{Client: &http.Client{Transport: http.DefaultTransport}}, nil
	}
	host, _ := os.Hostname()
	dir, err := os.MkdirTemp("", "keel-agent-tsnet-")
	if err != nil {
		return nil, err
	}
	srv := &tsnet.Server{
		Hostname:  tailnetHostname(host),
		AuthKey:   opts.AuthKey,
		Ephemeral: true,
		Dir:       dir,
		Logf:      func(string, ...any) {},
		UserLogf:  opts.Logf,
	}
	upCtx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	if _, err := srv.Up(upCtx); err != nil {
		srv.Close()
		os.RemoveAll(dir)
		return nil, fmt.Errorf("tailnet: %w", err)
	}
	opts.Logf("joined the tailnet as %s", srv.Hostname)
	return &Mesh{
		Client: srv.HTTPClient(),
		close: func() error {
			err := srv.Close()
			os.RemoveAll(dir)
			return err
		},
	}, nil
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
