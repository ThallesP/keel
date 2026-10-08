// Command keel is Keel: control plane (serve), edge proxy (proxy), node agent (agent) and the CLI,
// in one binary. See docs/go/ARCHITECTURE.md.
package main

import (
	"context"
	"os"
	"os/signal"
	"syscall"

	"github.com/ThallesP/keel/internal/cli"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	code := cli.Execute(ctx)
	stop()
	os.Exit(code)
}
