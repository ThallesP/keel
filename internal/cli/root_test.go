package cli

import (
	"context"
	"errors"
	"slices"
	"testing"

	"github.com/spf13/cobra"

	"github.com/ThallesP/keel/internal/cli/output"
)

// withExtra registers server commands for one test, as proxy_cmd.go and agent_cmd.go do.
func withExtra(t *testing.T, cmds ...*cobra.Command) {
	t.Helper()
	saved := slices.Clone(Extra)
	for _, c := range cmds {
		Extra = append(Extra, func() *cobra.Command { return c })
	}
	t.Cleanup(func() { Extra = saved })
}

// Server commands share the root with the CLI verbs but none of their pre-run (printer, config,
// discovery, login), and fail as plain `error:` lines with exit 1, not as the CLI's envelope.
func TestServerCommands(t *testing.T) {
	t.Setenv("KEEL_CONFIG_DIR", t.TempDir())
	ran := false
	withExtra(t,
		&cobra.Command{Use: "fake-daemon", RunE: func(*cobra.Command, []string) error { ran = true; return nil }},
		&cobra.Command{Use: "fake-broken", RunE: func(*cobra.Command, []string) error { return errors.New("boom") }},
	)

	a := &app{}
	if code := a.execute(context.Background(), []string{"fake-daemon", "--json"}); code != 0 || !ran {
		t.Fatalf("exit %d, ran %v", code, ran)
	}
	if a.out != nil {
		t.Error("a server command went through the CLI's pre-run")
	}
	if code := (&app{}).execute(context.Background(), []string{"fake-broken"}); code != output.ExitError {
		t.Errorf("failing server command: exit %d, want %d (not USAGE)", code, output.ExitError)
	}

	root := (&app{}).root()
	for _, name := range []string{"serve", "openapi", "fake-daemon"} {
		cmd, _, err := root.Find([]string{name})
		if err != nil || !isServer(cmd) {
			t.Errorf("%s: %v, server %v", name, err, isServer(cmd))
		}
	}
	for _, name := range []string{"login", "status", "service"} {
		if cmd, _, _ := root.Find([]string{name}); isServer(cmd) {
			t.Errorf("%s is marked a server command", name)
		}
	}
}

// CLI verbs keep the contract: a cobra error is USAGE (exit 2), and an unknown command too.
func TestCLIErrorsKeepTheContract(t *testing.T) {
	t.Setenv("KEEL_CONFIG_DIR", t.TempDir())
	if code := (&app{}).execute(context.Background(), []string{"project", "create", "--json"}); code != output.ExitUsage {
		t.Errorf("missing argument: exit %d", code)
	}
	if code := (&app{}).execute(context.Background(), []string{"nope", "--json"}); code != output.ExitUsage {
		t.Errorf("unknown command: exit %d", code)
	}
	t.Setenv("KEEL_URL", "")
	t.Setenv("KEEL_INSTANCE", "")
	t.Setenv("KEEL_TOKEN", "")
	if code := (&app{}).execute(context.Background(), []string{"whoami", "--json"}); code != output.ExitAuth {
		t.Errorf("not logged in: exit %d", code)
	}
}
