package cli

import (
	"context"
	"errors"
	"slices"
	"testing"

	"github.com/spf13/cobra"

	"github.com/ThallesP/keel/internal/cli/output"
)

func TestServerCommands(t *testing.T) {
	t.Setenv("KEEL_CONFIG_DIR", t.TempDir())
	ran := false
	saved := slices.Clone(Extra)
	t.Cleanup(func() { Extra = saved })
	for _, c := range []*cobra.Command{
		{Use: "fake-daemon", RunE: func(*cobra.Command, []string) error { ran = true; return nil }},
		{Use: "fake-broken", RunE: func(*cobra.Command, []string) error { return errors.New("boom") }},
	} {
		Extra = append(Extra, func() *cobra.Command { return c })
	}

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
	for _, name := range []string{"serve", "openapi", "agent", "fake-daemon"} {
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
