package cli

import (
	"fmt"
	"io"
	"strings"

	"github.com/spf13/cobra"

	"github.com/ThallesP/keel/internal/cli/output"
)

const masked = "••••••••"

func (a *app) varCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:     "var",
		Aliases: []string{"vars", "variables"},
		Short:   "List, set and delete a service's variables",
		Long: `Variables belong to a service. Values may reference other services' variables with
${{ service.KEY }}. Changes are staged, as in the dashboard: keel ship deploys them.`,
	}
	cmd.AddCommand(a.varListCmd(), a.varSetCmd(), a.varDeleteCmd())
	return cmd
}

type varView struct {
	Key string `json:"key"`
	// As written, null when hidden.
	Value *string `json:"value"`
	// After ${{ }} references are expanded, null when hidden.
	Resolved *string `json:"resolved"`
	Secret   bool    `json:"secret"`
}

func (a *app) varListCmd() *cobra.Command {
	var show bool
	cmd := &cobra.Command{
		Use:     "list <service>",
		Aliases: []string{"ls"},
		Short:   "List a service's variables; secret values are hidden unless --show-secrets",
		Args:    args(1, 1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			s, err := a.connect(ctx)
			if err != nil {
				return err
			}
			_, env, err := a.project(ctx, s)
			if err != nil {
				return err
			}
			svc, _, err := s.service(ctx, env.ID, args[0])
			if err != nil {
				return err
			}
			vars, err := s.api.Variables(ctx, svc.ID)
			if err != nil {
				return err
			}
			views := make([]varView, len(vars))
			hidden := 0
			for i, v := range vars {
				views[i] = varView{Key: v.Key, Secret: v.Secret || v.ResolvedSecret}
				if show || !v.Secret {
					views[i].Value = &v.Value
				}
				if show || !(v.Secret || v.ResolvedSecret) {
					views[i].Resolved = &v.Resolved
				}
				if views[i].Value == nil || views[i].Resolved == nil {
					hidden++
				}
			}
			a.out.Result(struct {
				Service   string    `json:"service"`
				Variables []varView `json:"variables"`
			}{svc.Name, views}, func(w io.Writer) {
				if len(views) == 0 {
					fmt.Fprintf(w, "No variables on %s\n", svc.Name)
					return
				}
				t := table(w)
				fmt.Fprintln(t, "KEY\tVALUE")
				for _, v := range views {
					value := orMasked(v.Value)
					if r := orMasked(v.Resolved); r != value {
						value += "  → " + r
					}
					fmt.Fprintf(t, "%s\t%s\n", v.Key, value)
				}
				t.Flush()
				if hidden > 0 {
					a.out.Progress("%d secret %s hidden; --show-secrets reveals them", hidden, plural(hidden, "value", "values"))
				}
			})
			return nil
		},
	}
	cmd.Flags().BoolVar(&show, "show-secrets", false, "print secret values")
	return cmd
}

func orMasked(s *string) string {
	if s == nil {
		return masked
	}
	return *s
}

func (a *app) varSetCmd() *cobra.Command {
	var secret bool
	cmd := &cobra.Command{
		Use:   "set <service> KEY=VALUE...",
		Short: "Set variables (staged until keel ship)",
		Long: `Set one or more variables on a service. Existing keys keep their secret flag unless
--secret is given. Nothing restarts: the change is staged until keel ship.`,
		Example: `  keel var set api LOG_LEVEL=debug
  keel var set api DATABASE_URL='${{ postgres.DATABASE_URL }}'
  keel var set api STRIPE_KEY=sk_live_… --secret`,
		Args: args(2, -1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			type pair struct{ key, value string }
			pairs := make([]pair, 0, len(args)-1)
			for _, arg := range args[1:] {
				key, value, ok := strings.Cut(arg, "=")
				if !ok || key == "" {
					return usage(cmd, "%q is not KEY=VALUE", arg)
				}
				pairs = append(pairs, pair{key, value})
			}
			s, err := a.connect(ctx)
			if err != nil {
				return err
			}
			_, env, err := a.project(ctx, s)
			if err != nil {
				return err
			}
			svc, _, err := s.service(ctx, env.ID, args[0])
			if err != nil {
				return err
			}
			existing, err := s.api.Variables(ctx, svc.ID)
			if err != nil {
				return err
			}
			wasSecret := map[string]bool{}
			for _, v := range existing {
				wasSecret[v.Key] = v.Secret
			}
			keys := make([]string, len(pairs))
			for i, p := range pairs {
				isSecret := wasSecret[p.key]
				if cmd.Flags().Changed("secret") {
					isSecret = secret
				}
				if err := s.api.SetVariable(ctx, svc.ID, p.key, p.value, isSecret); err != nil {
					return withDone(err, keys[:i])
				}
				keys[i] = p.key
			}
			a.out.Result(struct {
				Service string   `json:"service"`
				Set     []string `json:"set"`
				Staged  bool     `json:"staged"`
			}{svc.Name, keys, true}, func(w io.Writer) {
				fmt.Fprintf(w, "Staged %s on %s. Deploy with: keel ship\n", strings.Join(keys, ", "), svc.Name)
			})
			return nil
		},
	}
	cmd.Flags().BoolVar(&secret, "secret", false, "mark the variables secret (hidden in lists)")
	return cmd
}

func (a *app) varDeleteCmd() *cobra.Command {
	return &cobra.Command{
		Use:     "delete <service> KEY...",
		Aliases: []string{"rm", "unset"},
		Short:   "Delete variables (staged until keel ship)",
		Args:    args(2, -1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			s, err := a.connect(ctx)
			if err != nil {
				return err
			}
			_, env, err := a.project(ctx, s)
			if err != nil {
				return err
			}
			svc, _, err := s.service(ctx, env.ID, args[0])
			if err != nil {
				return err
			}
			existing, err := s.api.Variables(ctx, svc.ID)
			if err != nil {
				return err
			}
			have := map[string]bool{}
			for _, v := range existing {
				have[v.Key] = true
			}
			keys := args[1:]
			for _, key := range keys {
				if !have[key] {
					return output.Errorf(output.CodeVariableNotFound, "keel var list "+svc.Name,
						"%s has no variable %s; nothing was deleted", svc.Name, key)
				}
			}
			for i, key := range keys {
				if err := s.api.RemoveVariable(ctx, svc.ID, key); err != nil {
					return withDone(err, keys[:i])
				}
			}
			a.out.Result(struct {
				Service string   `json:"service"`
				Deleted []string `json:"deleted"`
				Staged  bool     `json:"staged"`
			}{svc.Name, keys, true}, func(w io.Writer) {
				fmt.Fprintf(w, "Staged deleting %s on %s. Deploy with: keel ship\n", strings.Join(keys, ", "), svc.Name)
			})
			return nil
		},
	}
}

// withDone notes which keys a multi-key change got through before it failed.
func withDone(err error, done []string) error {
	oe, ok := err.(*output.Error)
	if !ok || len(done) == 0 {
		return err
	}
	oe.Message += fmt.Sprintf(" (after %s went through)", strings.Join(done, ", "))
	oe.Extra = map[string]any{"done": done}
	return oe
}
