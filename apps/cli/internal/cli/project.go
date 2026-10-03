package cli

import (
	"fmt"
	"io"
	"strings"

	"github.com/spf13/cobra"

	"github.com/ThallesP/keel/apps/cli/internal/config"
	"github.com/ThallesP/keel/apps/cli/internal/keel"
)

type projectRef struct {
	ID   string `json:"id"`
	Slug string `json:"slug"`
	Name string `json:"name"`
}

func refOf(p *keel.Project) projectRef { return projectRef{p.ID, p.Slug, p.Name} }

func (a *app) projectCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:     "project",
		Aliases: []string{"projects"},
		Short:   "List projects",
	}
	cmd.AddCommand(&cobra.Command{
		Use:     "list",
		Aliases: []string{"ls"},
		Short:   "List the organization's projects; current marks the one commands use here",
		Args:    args(0, 0),
		RunE: func(cmd *cobra.Command, args []string) error {
			s, err := a.connect(cmd.Context())
			if err != nil {
				return err
			}
			projects, err := s.api.Projects(cmd.Context())
			if err != nil {
				return err
			}
			slug := a.projectSlug(s)
			if slug == "" && len(projects) == 1 {
				slug = projects[0].Slug
			}
			type row struct {
				keel.Project
				Current bool `json:"current"`
			}
			rows := make([]row, len(projects))
			for i, p := range projects {
				rows[i] = row{p, p.Slug == slug || p.ID == slug}
			}
			a.out.Result(struct {
				Projects []row `json:"projects"`
			}{rows}, func(w io.Writer) {
				t := table(w)
				fmt.Fprintln(t, "\tSLUG\tNAME\tENVIRONMENTS")
				for _, r := range rows {
					envs := make([]string, len(r.Environments))
					for i, e := range r.Environments {
						envs[i] = e.Name
					}
					fmt.Fprintf(t, "%s\t%s\t%s\t%s\n", mark(r.Current), r.Slug, r.Name, strings.Join(envs, ", "))
				}
				t.Flush()
			})
			return nil
		},
	})
	return cmd
}

func (a *app) linkCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "link [project]",
		Short: "Link this directory to a project",
		Long: `Link the working directory (and everything below it) to a project of the current
install, so commands run here need no --project. Without an argument, links the only project
when there is exactly one. The link lives in the config file, not in the directory.`,
		Args: args(0, 1),
		RunE: func(cmd *cobra.Command, args []string) error {
			s, err := a.connect(cmd.Context())
			if err != nil {
				return err
			}
			projects, err := s.api.Projects(cmd.Context())
			if err != nil {
				return err
			}
			slug := a.projectFlag
			if len(args) == 1 {
				slug = args[0]
			}
			p, err := pickProject(projects, slug, s.inst.URL)
			if err != nil {
				return err
			}
			dir := cwd()
			s.cfg.SetLink(dir, &config.Link{Instance: s.name, Project: p.Slug})
			if err := saveConfig(s.cfg); err != nil {
				return err
			}
			a.out.Result(struct {
				Dir      string     `json:"dir"`
				Instance string     `json:"instance"`
				Project  projectRef `json:"project"`
			}{dir, s.name, refOf(p)}, func(w io.Writer) {
				fmt.Fprintf(w, "Linked %s to %s on %s\n", dir, p.Slug, s.name)
			})
			return nil
		},
	}
}

func (a *app) unlinkCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "unlink",
		Short: "Remove the link of this directory (or the closest linked parent)",
		Args:  args(0, 0),
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := a.loadConfig()
			if err != nil {
				return err
			}
			dir, link := cfg.LinkFor(cwd())
			if link != nil {
				delete(cfg.Links, dir)
				if err := saveConfig(cfg); err != nil {
					return err
				}
			}
			a.out.Result(struct {
				Unlinked bool   `json:"unlinked"`
				Dir      string `json:"dir,omitempty"`
			}{link != nil, dir}, func(w io.Writer) {
				if link == nil {
					fmt.Fprintln(w, "Nothing linked here")
				} else {
					fmt.Fprintf(w, "Unlinked %s\n", dir)
				}
			})
			return nil
		},
	}
}

func (a *app) statusCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: "Project overview: services, staged changes, last deployment",
		Args:  args(0, 0),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			s, err := a.connect(ctx)
			if err != nil {
				return err
			}
			p, env, err := a.project(ctx, s)
			if err != nil {
				return err
			}
			summary, err := s.api.Summary(ctx, env.ID)
			if err != nil {
				return err
			}
			services, err := s.api.Services(ctx, env.ID)
			if err != nil {
				return err
			}
			latest, err := s.api.LatestDeployment(ctx, env.ID)
			if err != nil {
				return err
			}
			if latest != nil {
				latest.Log = nil
			}
			a.out.Result(struct {
				Instance         string           `json:"instance"`
				URL              string           `json:"url"`
				Project          projectRef       `json:"project"`
				Environment      keel.Environment `json:"environment"`
				PendingChanges   keel.Int         `json:"pendingChanges"`
				Servers          keel.Int         `json:"servers"`
				Services         []keel.Service   `json:"services"`
				LatestDeployment *keel.Deployment `json:"latestDeployment"`
			}{s.name, s.inst.URL, refOf(p), *env, summary.PendingChanges, summary.Servers, services, latest},
				func(w io.Writer) {
					t := table(w)
					fmt.Fprintf(t, "Project\t%s · %s\n", p.Slug, env.Name)
					fmt.Fprintf(t, "Instance\t%s  %s\n", s.name, s.inst.URL)
					fmt.Fprintf(t, "Servers\t%d\n", summary.Servers)
					if n := summary.PendingChanges; n > 0 {
						fmt.Fprintf(t, "Staged\t%d %s → keel ship\n", n, plural(int(n), "service", "services"))
					} else {
						fmt.Fprintf(t, "Staged\tnothing\n")
					}
					if latest != nil {
						fmt.Fprintf(t, "Last deploy\t%s · %s · %s\n", latest.Status, latest.Message, ago(latest.StartedAt.Time))
					}
					t.Flush()
					fmt.Fprintln(w)
					servicesTable(w, services)
				})
			return nil
		},
	}
}
