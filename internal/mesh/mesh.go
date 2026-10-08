// Package mesh is how `keel agent` reaches the control plane. By default over the host's network:
// Swarm already needs the host's tailscaled, so KEEL_URL is reachable from every node. Opt-in:
// with KEEL_TS_AUTHKEY set (and a binary built with -tags tsnet), through an embedded, ephemeral
// Tailscale node named keel-agent-<hostname> whose state lives in a temporary directory.
package mesh

import (
	"context"
	"net/http"
	"os"
	"strings"
)

// Options configure Open.
type Options struct {
	AuthKey  string                          // KEEL_TS_AUTHKEY; "" = host network
	Hostname string                          // this machine's name; "" = os.Hostname()
	Logf     func(format string, args ...any) // one-line notices (may be nil)
}

// Mesh is an HTTP client to the control plane and what it holds open.
type Mesh struct {
	Client *http.Client
	// Tailnet is true when Client dials through the embedded Tailscale node.
	Tailnet bool
	close   func() error
}

// Close releases the embedded node, if any.
func (m *Mesh) Close() error {
	if m.close == nil {
		return nil
	}
	return m.close()
}

// OptionsFromEnv reads KEEL_TS_AUTHKEY.
func OptionsFromEnv(logf func(format string, args ...any)) Options {
	return Options{AuthKey: strings.TrimSpace(os.Getenv("KEEL_TS_AUTHKEY")), Logf: logf}
}

// Open returns the client: through tsnet when an auth key is set and tsnet is compiled in, else
// http.DefaultTransport.
func Open(ctx context.Context, opts Options) (*Mesh, error) {
	if opts.AuthKey == "" {
		return direct(), nil
	}
	return openTailnet(ctx, opts)
}

func direct() *Mesh { return &Mesh{Client: &http.Client{Transport: http.DefaultTransport}} }

// tailnetHostname is the embedded node's name: keel-agent-<hostname>, lowercased, with anything
// that is not a letter, digit or "-" turned into "-" (a DNS label).
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
