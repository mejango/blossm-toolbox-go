package main

import (
	"context"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/mejango/blossm-toolbox-go/diaruntime"
)

func main() {
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()
	if err := diaruntime.Run(ctx); err != nil {
		diaruntime.LogStopped(slog.Default(), err)
		os.Exit(1)
	}
}
