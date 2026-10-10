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
	cli.WebFS = web.Dist()
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	code := cli.Execute(ctx)
	stop()
	os.Exit(code)
}
