// Package cli is the keel command: the CLI verbs (docs/cli.md) and the server subcommands
// (serve, proxy, agent, openapi).
package cli

import (
	"context"
	"encoding/json"
	"os"

	"github.com/spf13/cobra"

	"github.com/ThallesP/keel/internal/serve"
	transport "github.com/ThallesP/keel/internal/transport/http"
)

// Version is set at build time (-ldflags "-X github.com/ThallesP/keel/internal/cli.Version=…").
var Version = "dev"

// Server subcommands. The CLI verbs register themselves on the same root (see the cli area).
func serverCommands() []*cobra.Command {
	serveCmd := &cobra.Command{
		Use:   "serve",
		Short: "Run the control plane (API, dashboard, Swarm driver)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return serve.Run(cmd.Context(), serve.Options{Version: Version, Web: WebFS})
		},
	}
	openapiCmd := &cobra.Command{
		Use:   "openapi",
		Short: "Print the OpenAPI document",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			enc := json.NewEncoder(os.Stdout)
			enc.SetIndent("", "  ")
			return enc.Encode(transport.OpenAPI(Version))
		},
	}
	return []*cobra.Command{serveCmd, openapiCmd}
}

// Extra registers more top-level commands (keel proxy, keel agent, the CLI verbs). Areas add to it
// from init() in their own file, so root.go never needs editing.
var Extra []func() *cobra.Command

// Root is the keel command.
func Root() *cobra.Command {
	root := &cobra.Command{
		Use:           "keel",
		Short:         "Keel: deploy containers on your own servers",
		SilenceUsage:  true,
		SilenceErrors: true,
	}
	root.AddCommand(serverCommands()...)
	for _, mk := range Extra {
		root.AddCommand(mk())
	}
	return root
}

// Execute runs the command line.
func Execute(ctx context.Context) int {
	if err := Root().ExecuteContext(ctx); err != nil {
		os.Stderr.WriteString("error: " + err.Error() + "\n")
		return 1
	}
	return 0
}
