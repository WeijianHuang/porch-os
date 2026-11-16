// Package app wires infrastructure and owns process lifecycle. No business
// rules live here (design/04 §2).
package app

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os/signal"
	"syscall"
	"time"

	httpx "github.com/WeijianHuang/porch-os/internal/adapter/http"
	"github.com/WeijianHuang/porch-os/internal/adapter/postgres"
	"github.com/WeijianHuang/porch-os/internal/platform/config"
)

// Run starts cmd/api: no migrations run here. Only cmd/job migrate is allowed
// to change the schema (ADR-0008).
func Run(ctx context.Context, cfg config.Config) error {
	pool, err := postgres.NewPool(ctx, cfg)
	if err != nil {
		return err
	}
	defer pool.Close()
	store := postgres.NewStore(pool)

	srv := &http.Server{
		Addr:              ":" + cfg.Port,
		Handler:           httpx.NewRouter(store, cfg),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       60 * time.Second,
	}
	errc := make(chan error, 1)
	go func() {
		slog.Info("api listening", "addr", srv.Addr, "schema_version", cfg.ExpectedSchemaVersion)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errc <- err
		}
	}()

	sigCtx, stop := signal.NotifyContext(ctx, syscall.SIGTERM, syscall.SIGINT)
	defer stop()
	select {
	case err := <-errc:
		return err
	case <-sigCtx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		slog.Info("graceful shutdown started")
		return srv.Shutdown(shutdownCtx)
	}
}
