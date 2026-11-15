// Command porch-os starts the Porch OS HTTP server.
//
// This is the R0 bootstrap: a zero-dependency health endpoint so that CI has a
// reproducible build and test baseline. Routing, configuration and database
// access are introduced from R1 onwards following docs/decisions/0002-go-backend-stack.md.
package main

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"os"
)

func main() {
	level := slog.LevelInfo
	if os.Getenv("LOG_LEVEL") == "debug" {
		level = slog.LevelDebug
	}
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: level})))

	mux := http.NewServeMux()
	mux.HandleFunc("GET /health", healthHandler)

	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}
	addr := ":" + port
	slog.Info("starting porch-os", "addr", addr)
	if err := http.ListenAndServe(addr, mux); err != nil {
		slog.Error("server stopped", "error", err)
		os.Exit(1)
	}
}

func healthHandler(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(map[string]string{"status": "ok"}); err != nil {
		slog.Error("write health response", "error", err)
	}
}
