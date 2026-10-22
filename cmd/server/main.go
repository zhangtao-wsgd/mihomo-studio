package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"mihomo-studio/internal/api"
	"mihomo-studio/internal/config"
	"mihomo-studio/internal/store"
	"mihomo-studio/internal/workflow"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	db, err := store.Open(os.Getenv("DATABASE_PATH"))
	if err != nil {
		logger.Error("open database", "error", err)
		os.Exit(1)
	}
	defer db.DB.Close()
	secret := os.Getenv("MIHOMO_SECRET")
	if secret == "" {
		secret = "local-development-only-change-me"
		logger.Warn("using development Mihomo secret; set MIHOMO_SECRET before sharing this host")
	}
	snapshotDir := os.Getenv("SNAPSHOT_DIR")
	if snapshotDir == "" {
		snapshotDir = "./data/snapshots"
	}
	activePath := os.Getenv("ACTIVE_CONFIG_PATH")
	if activePath == "" {
		activePath = "./data/runtime/active/config.yaml"
	}
	controllerURL := os.Getenv("MIHOMO_CONTROLLER_URL")
	builder := &config.Builder{DB: db.DB, SnapshotDir: snapshotDir, ControllerSecret: secret, ControllerURL: controllerURL, MihomoBinary: envOr("MIHOMO_BINARY", "/usr/local/bin/mihomo")}
	var runtime workflow.Runtime
	if controller := controllerURL; controller != "" {
		runtime = &workflow.HTTPRuntime{BaseURL: controller, Secret: secret}
	}
	flow := &workflow.Service{DB: db.DB, Builder: builder, Runtime: runtime, ActivePath: activePath}
	server := &http.Server{Addr: envOr("LISTEN_ADDR", ":8080"), Handler: (&api.Server{Store: db, Workflow: flow, Logger: logger}).Handler(), ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 15 * time.Second, WriteTimeout: 30 * time.Second, IdleTimeout: 60 * time.Second}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	go func() {
		logger.Info("HTTP server starting", "address", server.Addr)
		if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			logger.Error("HTTP server failed", "error", err)
			stop()
		}
	}()
	<-ctx.Done()
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := server.Shutdown(shutdownCtx); err != nil {
		logger.Error("HTTP server shutdown failed", "error", err)
	}
}
func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
