package cli

import (
	"context"
	"fmt"
	"io"
	"slices"
	"time"

	"github.com/spf13/cobra"

	"github.com/ThallesP/keel/apps/cli/internal/keel"
	"github.com/ThallesP/keel/apps/cli/internal/output"
)

// Matches the Logs tab: there is no push, so following is polling the tail. A poll that hangs is
// dropped and retried rather than ending the stream.
const (
	followEvery = 2 * time.Second
	pollTimeout = 15 * time.Second
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
			tail, err := s.api.Tail(ctx, svc.ID, lines)
			if err != nil {
				return err
			}
			sortLines(tail.Lines)
			// Docker tails each task (old replicas too), so the server can return more than asked.
			fetched := tail.Lines
			tail.Lines = tail.Lines[max(0, len(tail.Lines)-lines):]
			if !follow {
				a.out.Result(struct {
					Service string `json:"service"`
					*keel.Tail
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

			seen := newLineSet()
			emit := func(ls []keel.LogLine) {
				for _, l := range ls {
					if seen.add(l) {
						a.out.Event(struct {
							Service string `json:"service"`
							keel.LogLine
						}{svc.Name, l}, logLine(l))
					}
				}
			}
			for _, l := range fetched[:len(fetched)-len(tail.Lines)] {
				seen.add(l) // older than what -n asked for: never print them later
			}
			emit(tail.Lines)
			tick := time.NewTicker(followEvery)
			defer tick.Stop()
			for {
				select {
				case <-ctx.Done():
					return nil // interrupting is how --follow ends
				case <-tick.C:
				}
				poll, cancel := context.WithTimeout(ctx, pollTimeout)
				tail, err := s.api.Tail(poll, svc.ID, 200)
				cancel()
				if ctx.Err() != nil {
					return nil
				}
				if oe, ok := err.(*output.Error); ok && (oe.Code == output.CodeTimeout || oe.Code == output.CodeNetwork) {
					a.out.Warn("%s; retrying", oe.Message)
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

func logLine(l keel.LogLine) string {
	return l.Time.Local().Format("2006-01-02 15:04:05") + "  " + l.Text
}

func sortLines(ls []keel.LogLine) {
	slices.SortStableFunc(ls, func(a, b keel.LogLine) int { return a.Time.Compare(b.Time.Time) })
}

// lineSet remembers what --follow printed. Each poll returns an overlapping tail; a line is new
// when it is later than the last printed one, or as late but not printed yet.
type lineSet struct {
	last time.Time
	at   map[string]bool // lines printed at `last`
}

func newLineSet() *lineSet { return &lineSet{at: map[string]bool{}} }

func (s *lineSet) add(l keel.LogLine) bool {
	key := l.Task + "\x00" + l.Stream + "\x00" + l.Text
	switch {
	case l.Time.Before(s.last):
		return false
	case l.Time.After(s.last):
		s.last, s.at = l.Time.Time, map[string]bool{}
	case s.at[key]:
		return false
	}
	s.at[key] = true
	return true
}
