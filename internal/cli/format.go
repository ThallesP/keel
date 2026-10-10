package cli

import (
	"fmt"
	"io"
	"text/tabwriter"
	"time"

	"github.com/ThallesP/keel/internal/cli/client"
)

func table(w io.Writer) *tabwriter.Writer {
	return tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
}

func servicesTable(w io.Writer, services []client.Service) {
	if len(services) == 0 {
		fmt.Fprintln(w, "No services yet")
		return
	}
	t := table(w)
	fmt.Fprintln(t, "NAME\tTYPE\tSTATUS\tIMAGE\tREPLICAS\tURL")
	for _, s := range services {
		status := s.Status
		if s.Staged {
			status += " (staged)"
		}
		fmt.Fprintf(t, "%s\t%s\t%s\t%s\t%d/%d\t%s\n",
			s.Name, s.Type, status, dash(s.Image), s.Running, s.Replicas, dash(s.PublicURL))
	}
	t.Flush()
}

func ago(t time.Time) string {
	d := time.Since(t)
	switch {
	case d < 10*time.Second:
		return "just now"
	case d < time.Minute:
		return fmt.Sprintf("%ds ago", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	case d < 48*time.Hour:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	}
	return fmt.Sprintf("%dd ago", int(d.Hours()/24))
}

func duration(d *client.Deployment) string {
	if d.FinishedAt == nil {
		return "-"
	}
	return d.FinishedAt.Sub(d.StartedAt.Time).Round(time.Second).String()
}

func dash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}
