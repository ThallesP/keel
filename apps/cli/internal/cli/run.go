package cli

import (
	"errors"
	"os"
	"os/exec"
	"os/signal"
	"regexp"
	"slices"
	"strings"
	"syscall"

	"github.com/spf13/cobra"

	"github.com/ThallesP/keel/apps/cli/internal/keel"
	"github.com/ThallesP/keel/apps/cli/internal/output"
)

// A variable resolved to a service's overlay hostname (`svc-<id>`, variables.serviceHost) only
// resolves inside the cluster.
var overlayHost = regexp.MustCompile(`\bsvc-[0-9a-z]{32}\b`)

// childExit carries a `keel run` command's own exit code out through Execute, unprinted.
type childExit struct{ code int }

func (e *childExit) Error() string { return "exit status" }

func (a *app) runCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "run <service> -- <command> [args...]",
		Short: "Run a local command with a service's variables and tracing",
		Long: `Run a command on this machine with the service's variables, and with tracing variables
that send its OpenTelemetry spans to Keel tagged deployment.environment.name=local. keel traces
<service> shows them, as does the Observability page. The way to try instrumentation before it
ships (keel tracing prompt).

Variables already set in this shell win. Variables that point at a service inside the cluster
(svc-… hosts) are left out: they do not resolve from here. Tracing variables are left out, with
a warning, when the organization has nowhere to store traces yet.

stdin, stdout and stderr are the command's; keel exits with its exit code.`,
		Example: `  keel run api -- bun dev
  keel run api -- python -m app
  keel run worker -- go run ./cmd/worker`,
		Args: func(cmd *cobra.Command, args []string) error {
			if at := cmd.ArgsLenAtDash(); at != 1 || len(args) < 2 {
				return usage(cmd, "usage: %s (the command goes after --)", cmd.UseLine())
			}
			return nil
		},
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
			tracing, reason, err := s.api.LocalTracingEnv(ctx, svc.ID)
			if err != nil {
				return err
			}
			endpoint := strings.TrimRight(s.inst.ConvexSiteURL, "/") + "/otlp"
			environ, skipped := runEnv(os.Environ(), vars, tracing, endpoint)

			if len(skipped) > 0 {
				a.out.Warn("left out %s: %s inside the cluster; set %s in this shell to run against something reachable",
					strings.Join(skipped, ", "), plural(len(skipped), "it points", "they point"), plural(len(skipped), "it", "them"))
			}
			if tracing == nil {
				a.out.Warn("no tracing variables: %s (open Observability in %s)", reason, s.inst.URL)
			} else {
				a.out.Progress("%s: tracing to %s as deployment.environment.name=local; keel traces %s shows the requests", svc.Name, endpoint, svc.Name)
			}
			return run(args[1:], environ)
		},
	}
}

// runEnv layers the environment of a local run: this shell, then the service's variables, then
// its tracing variables, each only where the layer before left the key unset (a service's own
// variables win over the tracing ones when it is deployed, too). The ingest key only goes to
// Keel's endpoint: with an endpoint of its own set, the run gets no Keel headers (as tracing.ts
// does when deployed). Variables that only resolve in the cluster are skipped and returned by key.
func runEnv(shell []string, vars []keel.Variable, tracing map[string]string, endpoint string) ([]string, []string) {
	out := slices.Clone(shell)
	set := map[string]bool{}
	for _, kv := range shell {
		key, _, _ := strings.Cut(kv, "=")
		set[key] = true
	}
	add := func(key, value string) {
		if !set[key] {
			set[key] = true
			out = append(out, key+"="+value)
		}
	}
	var skipped []string
	for _, v := range vars {
		if overlayHost.MatchString(v.Resolved) {
			if !set[v.Key] {
				skipped = append(skipped, v.Key)
			}
			continue
		}
		add(v.Key, v.Resolved)
	}
	if tracing != nil {
		ownEndpoint := set["OTEL_EXPORTER_OTLP_ENDPOINT"] || set["OTEL_EXPORTER_OTLP_TRACES_ENDPOINT"]
		add("OTEL_EXPORTER_OTLP_ENDPOINT", endpoint)
		keys := make([]string, 0, len(tracing))
		for k := range tracing {
			keys = append(keys, k)
		}
		slices.Sort(keys)
		for _, k := range keys {
			if k == "OTEL_EXPORTER_OTLP_HEADERS" && ownEndpoint {
				continue
			}
			add(k, tracing[k])
		}
	}
	return out, skipped
}

// run starts the command and waits for it. At a terminal the command shares keel's foreground
// process group, so Ctrl-C already reaches everything it started and is not sent again; other
// signals keel gets are passed on to it. Without a terminal (an agent, a script) the command
// gets a process group of its own and every signal keel gets goes to the whole group, so stopping
// keel stops `npm run dev` and the node under it alike.
func run(argv []string, environ []string) error {
	path, err := exec.LookPath(argv[0])
	if err != nil {
		return output.Errorf(output.CodeUsage, "Check the command after --", "Can't run %s: %v", argv[0], err)
	}
	c := exec.Command(path, argv[1:]...)
	c.Env = environ
	c.Stdin, c.Stdout, c.Stderr = os.Stdin, os.Stdout, os.Stderr

	fromTerminal := output.IsTerminal(os.Stdin)
	if !fromTerminal {
		ownGroup(c)
	}

	signals := make(chan os.Signal, 4)
	signal.Notify(signals, os.Interrupt, syscall.SIGTERM, syscall.SIGHUP, syscall.SIGQUIT)
	defer signal.Stop(signals)
	if err := c.Start(); err != nil {
		return output.Errorf(output.CodeUsage, "Check the command after --", "Can't run %s: %v", argv[0], err)
	}
	done := make(chan struct{})
	go func() {
		for {
			select {
			case sig := <-signals:
				switch {
				case !fromTerminal:
					_ = signalGroup(c.Process, sig)
				case sig != os.Interrupt:
					_ = c.Process.Signal(sig)
				}
			case <-done:
				return
			}
		}
	}()
	err = c.Wait()
	close(done)

	var ee *exec.ExitError
	if errors.As(err, &ee) {
		if ws, ok := ee.Sys().(syscall.WaitStatus); ok && ws.Signaled() {
			return &childExit{128 + int(ws.Signal())}
		}
		return &childExit{ee.ExitCode()}
	}
	return err
}
