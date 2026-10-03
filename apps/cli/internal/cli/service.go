package cli

import (
	"io"

	"github.com/spf13/cobra"

	"github.com/ThallesP/keel/apps/cli/internal/keel"
)

func (a *app) serviceCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:     "service",
		Aliases: []string{"services"},
		Short:   "List services",
	}
	cmd.AddCommand(&cobra.Command{
		Use:     "list",
		Aliases: []string{"ls"},
		Short:   "List the project's services, databases, caches and volumes",
		Args:    args(0, 0),
		RunE: func(cmd *cobra.Command, args []string) error {
			s, err := a.connect(cmd.Context())
			if err != nil {
				return err
			}
			_, env, err := a.project(cmd.Context(), s)
			if err != nil {
				return err
			}
			services, err := s.api.Services(cmd.Context(), env.ID)
			if err != nil {
				return err
			}
			a.out.Result(struct {
				Services []keel.Service `json:"services"`
			}{services}, func(w io.Writer) { servicesTable(w, services) })
			return nil
		},
	})
	return cmd
}
