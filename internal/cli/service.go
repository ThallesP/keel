package cli

import (
	"fmt"
	"io"

	"github.com/spf13/cobra"

	"github.com/ThallesP/keel/internal/cli/client"
	"github.com/ThallesP/keel/internal/cli/output"
)

func (a *app) serviceCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:     "service",
		Aliases: []string{"services"},
		Short:   "Create, list and delete services",
	}
	cmd.AddCommand(a.serviceCreateCmd(), &cobra.Command{
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
				Services []client.Service `json:"services"`
			}{services}, func(w io.Writer) { servicesTable(w, services) })
			return nil
		},
	}, a.serviceDeleteCmd())
	return cmd
}

func (a *app) serviceCreateCmd() *cobra.Command {
	var image string
	var port, replicas int
	cmd := &cobra.Command{
		Use:   "create <name> --image <ref>",
		Short: "Stage a new service running an image (keel ship deploys it)",
		Long: `Add a service running an image to the project, as dropping one on the canvas does. It is
staged: set its variables with keel var set, then deploy it with keel ship <name>. Names are
a-z, 0-9 and -, up to 40, unique in the project; a taken one fails with NAME_TAKEN.`,
		Example: `  keel service create api --image ghcr.io/acme/api:1.4 --port 3000
  keel var set api DATABASE_URL='${{ postgres.DATABASE_URL }}'
  keel ship api`,
		Args: args(1, 1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if image == "" {
				return usage(cmd, "--image is required: the image to run, e.g. --image nginx:alpine")
			}
			var portArg, replicasArg *int
			if cmd.Flags().Changed("port") {
				portArg = &port
			}
			if cmd.Flags().Changed("replicas") {
				replicasArg = &replicas
			}
			ctx := cmd.Context()
			s, err := a.connect(ctx)
			if err != nil {
				return err
			}
			p, env, err := a.project(ctx, s)
			if err != nil {
				return err
			}
			svc, err := s.api.CreateService(ctx, env.ID, args[0], image, portArg, replicasArg)
			if err != nil {
				return err
			}
			a.out.Result(struct {
				Project projectRef     `json:"project"`
				Service client.Service `json:"service"`
			}{refOf(p), *svc}, func(w io.Writer) {
				fmt.Fprintf(w, "Staged %s (%s) in %s\n", svc.Name, svc.Image, p.Slug)
				fmt.Fprintf(w, "Next: keel var set %s KEY=VALUE, then keel ship %s\n", svc.Name, svc.Name)
			})
			return nil
		},
	}
	cmd.Flags().StringVar(&image, "image", "", "image to run, e.g. ghcr.io/acme/api:1.4 (required)")
	cmd.Flags().IntVar(&port, "port", 0, "port the container listens on (default 80)")
	cmd.Flags().IntVar(&replicas, "replicas", 0, "containers to run, 0–20 (default 1)")
	return cmd
}

func (a *app) serviceDeleteCmd() *cobra.Command {
	var yes bool
	cmd := &cobra.Command{
		Use:     "delete <service>",
		Aliases: []string{"rm"},
		Short:   "Delete a service right away (not staged)",
		Long: `Delete a service, database, cache or volume. Unlike every other change this is not staged:
its containers are removed now, along with its variables. Services whose variables reference it
get a staged change, since those references now resolve to nothing.

Asks first with a terminal; without one (or with --json) it needs --yes.`,
		Args: args(1, 1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if !yes && !a.interactive() {
				return usage(cmd, "Deleting %s can't be undone: pass --yes to confirm", args[0])
			}
			ctx := cmd.Context()
			s, svc, err := a.connectService(ctx, args[0])
			if err != nil {
				return err
			}
			if !yes && !a.confirm(fmt.Sprintf("Delete %s? It stops now and its variables are dropped.", svc.Name)) {
				return output.Errorf(output.CodeCancelled, "", "Nothing deleted")
			}
			if err := s.api.DeleteService(ctx, svc.ID); err != nil {
				return err
			}
			a.out.Result(struct {
				Service string `json:"service"`
				ID      string `json:"id"`
				Deleted bool   `json:"deleted"`
			}{svc.Name, svc.ID, true}, func(w io.Writer) {
				fmt.Fprintf(w, "Deleted %s\n", svc.Name)
			})
			return nil
		},
	}
	cmd.Flags().BoolVarP(&yes, "yes", "y", false, "delete without asking")
	return cmd
}
