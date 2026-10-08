// Command keel is Keel: control plane (serve), edge proxy (proxy), node agent (agent) and the CLI,
// in one binary. See docs/go/ARCHITECTURE.md.
package main

import (
	"context"
	"os"
	"os/signal"
	"syscall"

	web "github.com/ThallesP/keel/apps/web"
	"github.com/ThallesP/keel/internal/cli"
)

func main() {
	// The dashboard: embedded with -tags embedweb, nil otherwise (dev: Vite serves it).
	cli.WebFS = web.Dist()
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	code := cli.Execute(ctx)
	stop()
	os.Exit(code)
}
