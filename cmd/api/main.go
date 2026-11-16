// Command api is the stateless HTTP process. It never runs migrations.
package main

import (
	"context"
	"log/slog"
	"os"

	"github.com/WeijianHuang/porch-os/internal/app"
	"github.com/WeijianHuang/porch-os/internal/platform/config"
)

func main() {
	cfg, err := config.Load()
	if err != nil {
		slog.Error("invalid configuration", "error", err)
		os.Exit(1)
	}
	if err := app.Run(context.Background(), cfg); err != nil {
		slog.Error("api stopped", "error", err)
		os.Exit(1)
	}
}
