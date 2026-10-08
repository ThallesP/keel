package cli

import (
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/ThallesP/keel/internal/proxy"
)

// `keel proxy`: the public edge (embedded Caddy + caddy-l4 + Keel's modules), in its own
// container. Owner: the ingress area (docs/go/spec/proxy-ingress.md §7, §12.4 option 1).
func init() {
	Extra = append(Extra, proxyCommand)
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
			if opts.Socket == "" {
				opts.Socket = strings.TrimSpace(os.Getenv("KEEL_PROXY_SOCKET"))
			}
			return proxy.Run(cmd.Context(), opts)
		},
	}
	cmd.Flags().StringVar(&opts.Socket, "socket", "", "admin socket (default $KEEL_PROXY_SOCKET or "+proxy.DefaultSocket+")")
	cmd.Flags().StringVar(&opts.ConfigFile, "config", "", "base config file instead of the built-in one")
	cmd.Flags().BoolVar(&opts.Resume, "resume", true, "serve the last pushed config (Caddy's autosave) when there is one")
	return cmd
}
