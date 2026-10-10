//go:build linux && !keel_noproxy

package cli

import (
	"github.com/spf13/cobra"

	"github.com/ThallesP/keel/internal/adapters/caddy"
	"github.com/ThallesP/keel/internal/proxy"
	"github.com/ThallesP/keel/internal/serve"
)

func init() {
	extra = append(extra, proxyCommand)
}

func proxyCommand() *cobra.Command {
	var opts proxy.Options
	cmd := &cobra.Command{
		Use:   "proxy",
		Short: "Run keel-proxy, the control plane's public edge (no Docker socket)",
		Long: "Serves every exposed endpoint: HTTPS on 80/443 and raw TCP/UDP ports, with listeners in the " +
			"host's network namespace (/run/hostns/net, CAP_SYS_ADMIN). `keel serve` configures it through " +
			"the admin socket (KEEL_PROXY_SOCKET).",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return proxy.Run(cmd.Context(), opts)
		},
	}
	cmd.Flags().StringVar(&opts.Socket, "socket", serve.Env("KEEL_PROXY_SOCKET", caddy.DefaultSocket), "admin socket (KEEL_PROXY_SOCKET)")
	cmd.Flags().StringVar(&opts.ConfigFile, "config", "", "base config file instead of the built-in one")
	cmd.Flags().BoolVar(&opts.Resume, "resume", true, "serve the last pushed config (Caddy's autosave) when there is one")
	return cmd
}
