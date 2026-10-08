//go:build !tsnet

package mesh

import "context"

// Built without tsnet: an auth key cannot be honoured, so say so once and use the host network
// (which reaches KEEL_URL through the host's tailscaled, as Swarm itself does).
func openTailnet(_ context.Context, opts Options) (*Mesh, error) {
	if opts.Logf != nil {
		opts.Logf("KEEL_TS_AUTHKEY is set but this keel was built without tsnet (-tags tsnet); using the host network")
	}
	return direct(), nil
}
