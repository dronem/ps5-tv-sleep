// Command ps5-tv-sleep puts an Android TV to sleep over ADB when a PS5 enters
// rest mode or switches off.
package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/dronem/ps5-tv-sleep/internal/app"
	"github.com/dronem/ps5-tv-sleep/internal/config"
	"github.com/dronem/ps5-tv-sleep/internal/tv"
)

func main() {
	if err := run(); err != nil {
		slog.Error("fatal", "err", err)
		os.Exit(1)
	}
}

func run() error {
	cfg, err := config.Load(os.Getenv)
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	return app.New(cfg, tv.ExecADB).Run(ctx)
}
