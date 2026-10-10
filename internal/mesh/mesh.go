package mesh

import (
	"context"
	"net/http"
	"os"
	"strings"
)

type Options struct {
	AuthKey  string
	Hostname string
	Logf     func(format string, args ...any)
}

type Mesh struct {
	Client  *http.Client
	Tailnet bool
	close   func() error
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
		return direct(), nil
	}
	return openTailnet(ctx, opts)
}

func direct() *Mesh { return &Mesh{Client: &http.Client{Transport: http.DefaultTransport}} }

func tailnetHostname(host string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(host) {
		if r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '-' {
			b.WriteRune(r)
		} else {
			b.WriteByte('-')
		}
	}
	name := strings.Trim(b.String(), "-")
	if name == "" {
		return "keel-agent"
	}
	name = "keel-agent-" + name
	if len(name) > 63 {
		name = strings.TrimRight(name[:63], "-")
	}
	return name
}
