package cli

import (
	"cmp"
	"fmt"
	"io"
	"slices"
	"strconv"
	"time"

	"github.com/spf13/cobra"

	"github.com/ThallesP/keel/internal/cli/client"
)

func (a *app) tracesCmd() *cobra.Command {
	var since, search string
	cmd := &cobra.Command{
		Use:   "traces [service]",
		Short: "List recent requests (OpenTelemetry traces) of the project or one service",
		Long: `List the latest requests (root spans) of the environment, or of one service, newest
first, with request counts and latency over the range. Requests from keel run are marked local.

Spans come from services with tracing on (keel tracing enable) and from keel run, through Keel's
OTLP relay to the organization's Axiom traces dataset. Without one, this fails with TRACES_OFF.`,
		Example: `  keel traces
  keel traces api --since 1h
  keel traces api --since 15m --json`,
		Args: args(0, 1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			if !slices.Contains([]string{"15m", "1h", "24h", "7d"}, since) {
				return usage(cmd, "--since must be one of 15m, 1h, 24h, 7d")
			}
			s, _, env, err := a.connectProject(ctx)
			if err != nil {
				return err
			}
			var name, id string
			if len(args) == 1 {
				svc, err := s.service(ctx, env.ID, args[0])
				if err != nil {
					return err
				}
				name, id = svc.Name, svc.ID
			}
			traces, err := s.api.Traces(ctx, env.ID, id, since, search)
			if err != nil {
				return err
			}
			a.out.Result(struct {
				Service string `json:"service,omitempty"`
				Since   string `json:"since"`
				*client.Traces
			}{name, since, traces}, func(w io.Writer) {
				if len(traces.Traces) == 0 {
					fmt.Fprintf(a.out.Err, "No requests from %s in the last %s\n", cmp.Or(name, "this project"), since)
					return
				}
				t := table(w)
				fmt.Fprintln(t, "TIME\tSERVICE\tREQUEST\tSTATUS\tDURATION\tSPANS\t")
				for _, r := range traces.Traces {
					local := ""
					if r.Local {
						local = "local"
					}
					fmt.Fprintf(t, "%s\t%s\t%s\t%s\t%s\t%d\t%s\n", r.Start.Local().Format(time.DateTime),
						cmp.Or(r.Service, "-"), r.Name, requestStatus(r), millis(&r.DurationMs), r.Spans, local)
				}
				t.Flush()
				st := traces.Stats
				a.out.Progress("%d %s in the last %s · %d failed · p50 %s · p95 %s · trace ids with --json",
					st.Requests, plural(st.Requests, "request", "requests"), since, st.Errors,
					millis(st.P50Ms), millis(st.P95Ms))
			})
			return nil
		},
	}
	cmd.Flags().StringVar(&since, "since", "15m", "how far back: 15m, 1h, 24h or 7d")
	cmd.Flags().StringVar(&search, "search", "", "only requests whose name or service contains this")
	return cmd
}

func requestStatus(r client.TraceSummary) string {
	switch {
	case r.HTTPStatus != nil:
		return strconv.Itoa(*r.HTTPStatus)
	case r.Error:
		return "error"
	}
	return "ok"
}

func millis(ms *float64) string {
	switch {
	case ms == nil:
		return "-"
	case *ms < 1:
		return fmt.Sprintf("%.2fms", *ms)
	case *ms < 1000:
		return fmt.Sprintf("%.1fms", *ms)
	}
	return time.Duration(*ms * float64(time.Millisecond)).Round(10 * time.Millisecond).String()
}
