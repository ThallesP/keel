// keel is the Keel CLI. See internal/cli for the commands and internal/output for the contract
// agents rely on.
package main

import (
	"context"
	"os"
	"os/signal"
	"syscall"

	"github.com/ThallesP/keel/apps/cli/internal/cli"
	"github.com/ThallesP/keel/apps/cli/internal/keel"
)

// Set at build time: -ldflags "-X main.version=1.2.3".
var version = "dev"

func main() {
	keel.UserAgent = "keel-cli/" + version
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	code := cli.Execute(ctx, version)
	stop()
	os.Exit(code)
}
