package cli

import (
	"fmt"
	"io"

	"github.com/spf13/cobra"

	"github.com/ThallesP/keel/internal/cli/client"
	"github.com/ThallesP/keel/internal/cli/output"
)

func (a *app) tracingCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "tracing",
		Short: "Set up OpenTelemetry tracing for a service",
		Long: `Tracing is a per-service switch. On, Keel gives the service the standard OTEL_* variables
on the next ship, so any OpenTelemetry SDK in it sends spans to Keel; keel traces and the
Observability page show them. The code side is an agent's job: keel tracing prompt prints
instructions for a coding agent, including how to check the result locally with keel run.`,
	}
	cmd.AddCommand(a.tracingPromptCmd(), a.tracingStatusCmd(),
		a.tracingSwitchCmd(true), a.tracingSwitchCmd(false))
	return cmd
}

func (a *app) tracingPromptCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "prompt [service]",
		Short: "Print the prompt that has a coding agent instrument a repo and test it locally",
		Long: `Print instructions for a coding agent (Claude Code, Cursor, Codex…) working in a service's
repo: add OpenTelemetry with the official SDK, configured only from the OTEL_* variables Keel
sets, run it with keel run, and confirm with keel traces that requests arrive. Without a
service, the agent finds it with keel service list. The same text as Copy prompt in the
dashboard.`,
		Example: `  keel tracing prompt api | pbcopy
  keel tracing prompt api > TRACING.md`,
		Args: args(0, 1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			s, err := a.connect(ctx)
			if err != nil {
				return err
			}
			_, env, err := a.project(ctx, s)
			if err != nil && len(args) == 1 {
				return err
			}
			var envID, svcID string
			if env != nil {
				envID = env.ID
			}
			if len(args) == 1 {
				svc, err := s.service(ctx, env.ID, args[0])
				if err != nil {
					return err
				}
				svcID = svc.ID
			}
			prompt, err := s.api.TracingPrompt(ctx, svcID, envID)
			if err != nil {
				return err
			}
			a.out.Result(struct {
				Prompt string `json:"prompt"`
			}{prompt}, func(w io.Writer) { fmt.Fprint(w, prompt) })
			return nil
		},
	}
}

func (a *app) tracingStatusCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "status <service>",
		Short: "Whether tracing is on for a service, and the variables it gets",
		Args:  args(1, 1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			s, svc, err := a.connectService(ctx, args[0])
			if err != nil {
				return err
			}
			tracing, err := s.api.Tracing(ctx, svc.ID)
			if err != nil {
				return err
			}
			if tracing == nil {
				return output.Errorf(output.CodeInvalidInput, "Pick a service: keel service list",
					"%s is a %s; only services can be traced", svc.Name, svc.Type)
			}
			a.out.Result(struct {
				Service string `json:"service"`
				Staged  bool   `json:"staged"`
				*client.Tracing
			}{svc.Name, svc.Staged, tracing}, func(w io.Writer) {
				state := "off"
				if tracing.Enabled {
					state = "on"
				}
				if svc.Staged {
					state += " (staged changes; keel ship " + svc.Name + " deploys them)"
				}
				fmt.Fprintf(w, "Tracing for %s: %s\n", svc.Name, state)
				switch tracing.Store {
				case "off":
					fmt.Fprintln(w, "Nowhere to send spans yet: open Observability in the dashboard and Sign in with Axiom")
				case "old":
					fmt.Fprintln(w, "This Axiom connection predates traces: Sign in with Axiom again on Observability")
				}
				if !tracing.Enabled {
					return
				}
				fmt.Fprintln(w)
				t := table(w)
				for _, v := range tracing.Env {
					note := ""
					if v.Overridden {
						note = "(replaced by the service's own variables)"
					}
					fmt.Fprintf(t, "%s\t%s\t%s\n", v.Key, v.Value, note)
				}
				t.Flush()
			})
			return nil
		},
	}
}

func (a *app) tracingSwitchCmd(on bool) *cobra.Command {
	use, short := "enable <service>", "Turn tracing on for a service (staged until keel ship)"
	if !on {
		use, short = "disable <service>", "Turn tracing off for a service (staged until keel ship)"
	}
	return &cobra.Command{
		Use:   use,
		Short: short,
		Long: short + `. On, the service gets the OTEL_* variables, pointing at Keel, on its next deploy: keel
ship <service>, or keel redeploy <service> to pull a new image too. Needs an Axiom connection
with traces (TRACES_OFF otherwise).`,
		Args: args(1, 1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			s, svc, err := a.connectService(ctx, args[0])
			if err != nil {
				return err
			}
			if svc.Type != "service" {
				return output.Errorf(output.CodeInvalidInput, "Pick a service: keel service list",
					"%s is a %s; only services can be traced", svc.Name, svc.Type)
			}
			if err := s.api.SetTracing(ctx, svc.ID, on); err != nil {
				return err
			}
			a.out.Result(struct {
				Service string `json:"service"`
				Tracing bool   `json:"tracing"`
				Staged  bool   `json:"staged"`
			}{svc.Name, on, true}, func(w io.Writer) {
				state := "on"
				if !on {
					state = "off"
				}
				fmt.Fprintf(w, "Tracing %s for %s, staged. Next: keel ship %s (or keel redeploy %s to pull a new image too)\n",
					state, svc.Name, svc.Name, svc.Name)
			})
			return nil
		},
	}
}
