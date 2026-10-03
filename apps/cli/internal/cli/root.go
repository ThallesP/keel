// Package cli is the keel command tree. Commands are thin: resolve the target (instance, project,
// service), call one or two Convex functions through package keel, print through package output.
package cli

import (
	"context"
	"errors"
	"os"
	"strconv"
	"strings"

	"github.com/spf13/cobra"

	"github.com/ThallesP/keel/apps/cli/internal/output"
)

type app struct {
	out          *output.Printer
	json         bool
	instanceFlag string
	projectFlag  string
}

const rootLong = `Keel from the terminal, built to be driven by agents as much as by people.

Output: results go to stdout, progress and warnings to stderr. With --json (or KEEL_JSON=1)
stdout is exactly one JSON object: {"ok":true,...} on success, or
{"ok":false,"code":"SERVICE_NOT_FOUND","error":"...","fix":"..."} on failure. logs --follow
prints one JSON object per line instead.

Exit codes: 0 ok, 1 error, 2 bad usage, 4 not logged in, 130 interrupted.

Nothing prompts unless stdin is a terminal; missing input fails with a USAGE error naming
the flag to pass.

Environment:
  KEEL_JSON=1        same as --json
  KEEL_URL           dashboard URL; with KEEL_TOKEN, use an install without keel login
  KEEL_TOKEN         session token (keel token prints yours)
  KEEL_INSTANCE      same as --instance
  KEEL_PROJECT       same as --project
  KEEL_CONFIG_DIR    where config.json lives (default ~/.config/keel)`

// Execute runs the CLI and returns the process exit code.
func Execute(ctx context.Context, version string) int {
	a := &app{}
	root := &cobra.Command{
		Use:           "keel",
		Short:         "Deploy and operate Keel projects",
		Long:          rootLong,
		Version:       version,
		SilenceUsage:  true,
		SilenceErrors: true,
		PersistentPreRun: func(cmd *cobra.Command, args []string) {
			a.out = output.New(a.jsonMode())
		},
	}
	root.CompletionOptions.HiddenDefaultCmd = true
	root.SetFlagErrorFunc(func(cmd *cobra.Command, err error) error {
		return usage(cmd, "%v", err)
	})
	flags := root.PersistentFlags()
	flags.BoolVar(&a.json, "json", false, "print one JSON object on stdout (KEEL_JSON=1)")
	flags.StringVar(&a.instanceFlag, "instance", "", "Keel install to use, by name (KEEL_INSTANCE)")
	flags.StringVarP(&a.projectFlag, "project", "p", "", "project slug (KEEL_PROJECT); default: linked or only project")

	root.AddCommand(
		a.loginCmd(), a.logoutCmd(), a.whoamiCmd(), a.tokenCmd(),
		a.statusCmd(), a.projectCmd(), a.linkCmd(), a.unlinkCmd(),
		a.serviceCmd(), a.logsCmd(), a.varCmd(),
		a.shipCmd(), a.redeployCmd(), a.deploymentCmd(),
	)

	cmd, err := root.ExecuteContextC(ctx)
	if err == nil {
		return 0
	}
	if a.out == nil { // failed before PersistentPreRun: bad flag or unknown command
		a.out = output.New(a.jsonMode() || jsonArg(os.Args[1:]))
	}
	var oe *output.Error
	switch {
	case errors.As(err, &oe):
	case ctx.Err() != nil:
		oe = output.Errorf(output.CodeCancelled, "", "Cancelled")
	default: // cobra's own errors: unknown command, wrong number of arguments
		oe = usage(cmd, "%v", err)
	}
	return a.out.Fail(oe)
}

func (a *app) jsonMode() bool {
	v := os.Getenv("KEEL_JSON")
	return a.json || v == "1" || v == "true"
}

// jsonArg finds --json in arguments cobra could not parse, in every form pflag accepts
// (--json, --json=true, --json=1, ...); the last one wins, as in pflag.
func jsonArg(args []string) bool {
	on := false
	for _, s := range args {
		if s == "--" {
			break
		}
		if s == "--json" {
			on = true
		} else if v, ok := strings.CutPrefix(s, "--json="); ok {
			on, _ = strconv.ParseBool(v)
		}
	}
	return on
}

func usage(cmd *cobra.Command, format string, args ...any) *output.Error {
	return output.Errorf(output.CodeUsage, cmd.CommandPath()+" --help", format, args...)
}

// args checks the positional argument count with a usage error that shows the expected form.
func args(min, max int) cobra.PositionalArgs {
	return func(cmd *cobra.Command, args []string) error {
		if len(args) < min || max >= 0 && len(args) > max {
			return usage(cmd, "usage: %s", cmd.UseLine())
		}
		return nil
	}
}
