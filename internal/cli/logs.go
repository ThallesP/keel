package cli

import (
	"context"
	"fmt"
	"io"
	"slices"
	"time"

	"github.com/spf13/cobra"

	"github.com/ThallesP/keel/internal/cli/client"
	"github.com/ThallesP/keel/internal/cli/output"
)

func (a *app) logsCmd() *cobra.Command {
	var lines int
	var follow bool
	cmd := &cobra.Command{
		Use:   "logs <service>",
		Short: "Print a service's recent log lines",
		Long: `Print the last lines of a service's logs and exit. Read from the project's log sink when
one is connected (Axiom), otherwise from Docker on the manager.

--follow keeps polling and prints new lines until interrupted; with --json it prints one
object per line: {"service","time","stream","task","text"}.`,
		Example: `  keel logs api
  keel logs api -n 500 --json
  keel logs api --follow`,
		Args: args(1, 1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			if lines < 1 || lines > 1000 {
				return usage(cmd, "--lines must be 1–1000")
			}
			s, svc, err := a.connectService(ctx, args[0])
			if err != nil {
				return err
			}
			tail, err := s.api.Tail(ctx, svc.ID, lines)
			if err != nil {
				return err
			}
			sortLines(tail.Lines)
			dropped := tail.Lines[:max(0, len(tail.Lines)-lines)]
			tail.Lines = tail.Lines[len(dropped):]
			if !follow {
				a.out.Result(struct {
					Service string `json:"service"`
					*client.Tail
				}{svc.Name, tail}, func(w io.Writer) {
					if len(tail.Lines) == 0 {
						fmt.Fprintf(a.out.Err, "No log lines for %s yet\n", svc.Name)
					}
					for _, l := range tail.Lines {
						fmt.Fprintln(w, logLine(l))
					}
				})
				return nil
			}

			var seen lineSet
			emit := func(ls []client.LogLine) {
				for _, l := range ls {
					if seen.add(l) {
						a.out.Event(struct {
							Service string `json:"service"`
							client.LogLine
						}{svc.Name, l}, logLine(l))
					}
				}
			}
			for _, l := range dropped {
				seen.add(l)
			}
			emit(tail.Lines)
			tick := time.NewTicker(2 * time.Second)
			defer tick.Stop()
			for {
				select {
				case <-ctx.Done():
					return nil
				case <-tick.C:
				}
				poll, cancel := context.WithTimeout(ctx, 15*time.Second)
				tail, err := s.api.Tail(poll, svc.ID, 200)
				cancel()
				if ctx.Err() != nil {
					return nil
				}
				if code := output.CodeOf(err); code == output.CodeTimeout || code == output.CodeNetwork {
					a.out.Warn("%v; retrying", err)
					continue
				}
				if err != nil {
					return err
				}
				sortLines(tail.Lines)
				emit(tail.Lines)
			}
		},
	}
	cmd.Flags().IntVarP(&lines, "lines", "n", 100, "number of lines, 1–1000")
	cmd.Flags().BoolVarP(&follow, "follow", "f", false, "keep printing new lines until interrupted")
	return cmd
}

func logLine(l client.LogLine) string {
	return l.Time.Local().Format(time.DateTime) + "  " + l.Text
}

func sortLines(ls []client.LogLine) {
	slices.SortStableFunc(ls, func(a, b client.LogLine) int { return a.Time.Compare(b.Time.Time) })
}

type lineSet struct {
	last time.Time
	at   map[[3]string]bool
}

func (s *lineSet) add(l client.LogLine) bool {
	key := [3]string{l.Task, l.Stream, l.Text}
	switch {
	case l.Time.Before(s.last):
		return false
	case l.Time.After(s.last):
		s.last, s.at = l.Time.Time, map[[3]string]bool{}
	case s.at[key]:
		return false
	}
	s.at[key] = true
	return true
}
