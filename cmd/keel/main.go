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
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	code := cli.Execute(ctx, web.Dist())
	stop()
	os.Exit(code)
}
