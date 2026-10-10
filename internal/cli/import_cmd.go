package cli

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/ThallesP/keel/internal/adapters/convexexport"
	"github.com/ThallesP/keel/internal/adapters/sqlite"
	"github.com/ThallesP/keel/internal/domain"
	"github.com/ThallesP/keel/internal/serve"
)

func init() {
	Extra = append(Extra, func() *cobra.Command {
		var dataDir string
		cmd := &cobra.Command{
			Use:   "import-convex <snapshot.zip|dir>",
			Short: "Import a Convex export of a Keel install into this data dir (run before the first keel serve)",
			Long: `Import a Convex snapshot (npx convex export, or a dashboard backup) of a Keel install.

Accounts keep their passwords, CLI logins keep working (dashboards sign in again), and every id is
kept, so Swarm services, default domains and traces still line up. The data dir must be fresh:
no organization yet. Stop keel serve first.`,
			Args: cobra.ExactArgs(1),
			RunE: func(cmd *cobra.Command, args []string) error {
				if err := os.MkdirAll(dataDir, 0o700); err != nil {
					return err
				}
				store, err := sqlite.Open(cmd.Context(), filepath.Join(dataDir, "keel.db"))
				if err != nil {
					return err
				}
				defer store.Close()
				convexexport.HashToken = domain.HashSessionToken
				rep, err := convexexport.Import(cmd.Context(), store.DB(), args[0])
				if err != nil {
					return err
				}
				enc := json.NewEncoder(os.Stdout)
				enc.SetIndent("", "  ")
				if err := enc.Encode(rep); err != nil {
					return err
				}
				fmt.Fprintf(os.Stderr, "imported into %s\n", filepath.Join(dataDir, "keel.db"))
				return nil
			},
		}
		cmd.Flags().StringVar(&dataDir, "data-dir", serve.Env("KEEL_DATA_DIR", "/data"), "keel serve's data dir")
		return cmd
	})
}
