package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/ThallesP/keel/internal/cli/client"
	"github.com/ThallesP/keel/internal/cli/output"
)

// Deployments settle within the server's 5-minute timeout (deployments.ts); this only guards a
// control plane that stopped answering.
const defaultWait = 10 * time.Minute

type waitFlags struct {
	detach  bool
	timeout time.Duration
}

func (f *waitFlags) register(cmd *cobra.Command) {
	cmd.Flags().BoolVarP(&f.detach, "detach", "d", false, "return once the deployment starts instead of waiting for it")
	cmd.Flags().DurationVar(&f.timeout, "timeout", defaultWait, "how long to wait before giving up (the deployment keeps going)")
}

func (a *app) shipCmd() *cobra.Command {
	var wait waitFlags
	cmd := &cobra.Command{
		Use:   "ship [service...]",
		Short: "Deploy staged changes, like the Ship button",
		Long: `Deploy every service with staged changes, or only the ones named. Waits until the
deployment succeeds or fails and exits non-zero on failure (code DEPLOYMENT_FAILED); --detach
returns as soon as it starts. One deployment runs per environment at a time.`,
		Example: `  keel ship
  keel ship api worker --json
  keel ship --detach && keel deployment get --wait`,
		Args: args(0, -1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return a.deploy(cmd, args, false, wait)
		},
	}
	wait.register(cmd)
	return cmd
}

func (a *app) redeployCmd() *cobra.Command {
	var wait waitFlags
	cmd := &cobra.Command{
		Use:   "redeploy <service...>",
		Short: "Redeploy services, pulling their images again",
		Long: `Roll services out again with a fresh pull of their image, whether or not anything is
staged. Staged changes on these services go out with it. Waits like keel ship.`,
		Args: args(1, -1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return a.deploy(cmd, args, true, wait)
		},
	}
	wait.register(cmd)
	return cmd
}

func (a *app) deploy(cmd *cobra.Command, names []string, refresh bool, wait waitFlags) error {
	ctx := cmd.Context()
	s, err := a.connect(ctx)
	if err != nil {
		return err
	}
	_, env, err := a.project(ctx, s)
	if err != nil {
		return err
	}
	services, err := s.api.Services(ctx, env.ID)
	if err != nil {
		return err
	}
	ids := make([]string, len(names))
	for i, name := range names {
		svc, err := findService(services, name)
		if err != nil {
			return err
		}
		ids[i] = svc.ID
	}
	id, err := s.api.StartDeployment(ctx, env.ID, ids, refresh)
	if err != nil {
		return err
	}
	if wait.detach {
		d, err := s.api.Deployment(ctx, id)
		if err != nil {
			return err
		}
		a.printDeployment(d, func(w io.Writer) {
			fmt.Fprintf(w, "Started: %s\nWatch it: keel deployment get %s --wait\n", d.Message, d.ID)
		})
		return nil
	}
	return a.await(ctx, s, id, services, wait.timeout)
}

// await polls a deployment until it settles, streaming its log to stderr. Success prints the
// deployment; failure is a DEPLOYMENT_FAILED error that carries it.
func (a *app) await(ctx context.Context, s *session, id string, services []client.Service, timeout time.Duration) error {
	names := map[string]string{}
	for _, svc := range services {
		names[svc.ID] = svc.Name
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	printed := 0
	for first := true; ; first = false {
		if !first {
			select {
			case <-ctx.Done():
			case <-time.After(time.Second):
			}
		}
		d, err := s.api.Deployment(ctx, id)
		if ctx.Err() != nil {
			return stoppedWaiting(ctx, id)
		}
		if err != nil {
			return err
		}
		if d == nil {
			return output.Errorf(output.CodeDeploymentNotFound, "keel deployment list <service>", "Deployment %s not found", id)
		}
		if first {
			a.out.Progress("%s (%s)", capitalize(d.Message), d.ID)
		}
		if printed > len(d.Log) { // the server keeps the last 500 entries
			printed = 0
		}
		for _, l := range d.Log[printed:] {
			if name := names[l.ServiceID]; name != "" && !strings.HasPrefix(l.Text, name+":") {
				a.out.Progress("  %s: %s", name, l.Text)
			} else {
				a.out.Progress("  %s", l.Text)
			}
		}
		printed = len(d.Log)
		switch d.Status {
		case "running":
			continue
		case "success":
			a.printDeployment(d, func(w io.Writer) {
				fmt.Fprintf(w, "Deployed: %s in %s\n", d.Message, duration(d))
			})
			return nil
		}
		var failed []string
		for _, step := range d.Steps {
			if step.Status == "failed" && step.ServiceID != "" {
				failed = append(failed, step.Label)
			}
		}
		fix := "keel deployment get " + d.ID
		if len(failed) > 0 {
			fix = "keel logs " + failed[0]
		}
		what := d.Message
		if len(failed) > 0 {
			what = strings.Join(failed, ", ")
		}
		return &output.Error{
			Code:    output.CodeDeploymentFailed,
			Message: "Deployment failed: " + what,
			Fix:     fix,
			Extra:   map[string]any{"deployment": d},
		}
	}
}

func stoppedWaiting(ctx context.Context, id string) error {
	fix := "keel deployment get " + id + " --wait"
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return output.Errorf(output.CodeTimeout, fix, "Still deploying after the --timeout; it keeps going on the server")
	}
	return output.Errorf(output.CodeCancelled, fix, "Stopped waiting; the deployment keeps going on the server")
}

func (a *app) printDeployment(d *client.Deployment, human func(io.Writer)) {
	a.out.Result(struct {
		Deployment *client.Deployment `json:"deployment"`
	}{d}, human)
}

func (a *app) deploymentCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:     "deployment",
		Aliases: []string{"deployments"},
		Short:   "Inspect deployments",
	}
	cmd.AddCommand(a.deploymentListCmd(), a.deploymentGetCmd())
	return cmd
}

func (a *app) deploymentListCmd() *cobra.Command {
	return &cobra.Command{
		Use:     "list <service>",
		Aliases: []string{"ls"},
		Short:   "Last 20 deployments that touched a service, newest first",
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
			ds, err := s.api.ServiceDeployments(ctx, svc.ID)
			if err != nil {
				return err
			}
			for i := range ds {
				ds[i].Log = nil
			}
			a.out.Result(struct {
				Service     string              `json:"service"`
				Deployments []client.Deployment `json:"deployments"`
			}{svc.Name, ds}, func(w io.Writer) {
				if len(ds) == 0 {
					fmt.Fprintf(w, "%s was never deployed\n", svc.Name)
					return
				}
				t := table(w)
				fmt.Fprintln(t, "ID\tSTATUS\tMESSAGE\tSTARTED\tTOOK")
				for _, d := range ds {
					fmt.Fprintf(t, "%s\t%s\t%s\t%s\t%s\n", d.ID, d.Status, d.Message, ago(d.StartedAt.Time), duration(&d))
				}
				t.Flush()
			})
			return nil
		},
	}
}

func (a *app) deploymentGetCmd() *cobra.Command {
	var wait bool
	var timeout time.Duration
	cmd := &cobra.Command{
		Use:   "get [id]",
		Short: "Show a deployment with its steps and log; the latest without an id",
		Args:  args(0, 1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			s, err := a.connect(ctx)
			if err != nil {
				return err
			}
			var d *client.Deployment
			var services []client.Service
			if len(args) == 1 {
				d, err = s.api.Deployment(ctx, args[0])
			} else {
				_, env, perr := a.project(ctx, s)
				if perr != nil {
					return perr
				}
				d, err = s.api.LatestDeployment(ctx, env.ID)
			}
			if err != nil {
				return err
			}
			if d == nil {
				what := "This project was never deployed"
				if len(args) == 1 {
					what = fmt.Sprintf("Deployment %s not found", args[0])
				}
				return output.Errorf(output.CodeDeploymentNotFound, "keel deployment list <service>", "%s", what)
			}
			if wait && d.Status != "success" { // await also turns a failed one into a non-zero exit
				if _, env, err := a.project(ctx, s); err == nil {
					services, _ = s.api.Services(ctx, env.ID)
				}
				return a.await(ctx, s, d.ID, services, timeout)
			}
			a.printDeployment(d, func(w io.Writer) {
				t := table(w)
				fmt.Fprintf(t, "Deployment\t%s\n", d.ID)
				fmt.Fprintf(t, "Status\t%s\n", d.Status)
				fmt.Fprintf(t, "Message\t%s\n", d.Message)
				fmt.Fprintf(t, "Started\t%s (%s)\n", d.StartedAt.Local().Format(time.DateTime), ago(d.StartedAt.Time))
				fmt.Fprintf(t, "Took\t%s\n", duration(d))
				t.Flush()
				fmt.Fprintln(w)
				for _, step := range d.Steps {
					fmt.Fprintf(w, "  %-8s %s\n", step.Status, step.Label)
				}
				if len(d.Log) > 0 {
					fmt.Fprintln(w)
					for _, l := range d.Log {
						fmt.Fprintf(w, "%s  %s\n", l.At.Local().Format(time.TimeOnly), l.Text)
					}
				}
			})
			return nil
		},
	}
	cmd.Flags().BoolVar(&wait, "wait", false, "wait until it settles; exits non-zero if it failed")
	cmd.Flags().DurationVar(&timeout, "timeout", defaultWait, "with --wait, how long to wait")
	return cmd
}

func capitalize(s string) string {
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}
