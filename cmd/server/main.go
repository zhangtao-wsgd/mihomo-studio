package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
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
	listenAddr := envOr("LISTEN_ADDR", ":8080")
	secret := strings.TrimSpace(os.Getenv("MIHOMO_SECRET"))
	// Without a secret there is nothing to protect the controller with, and the panel has
	// no credential either. Only a loopback listener may continue in that state; a listener
	// reachable from the network must be configured. There is deliberately no built-in
	// fallback value: a hard-coded default would be public knowledge and would look like
	// protection while providing none.
	allowUnauthenticated := secret == ""
	if allowUnauthenticated {
		if !api.IsLoopbackListener(listenAddr) {
			logger.Error("MIHOMO_SECRET is required when the listener is reachable from the network",
				"listenAddr", listenAddr,
				"hint", "set MIHOMO_SECRET, or bind LISTEN_ADDR to 127.0.0.1")
			os.Exit(1)
		}
		logger.Warn("MIHOMO_SECRET is not set; running unauthenticated because the listener is loopback-only",
			"listenAddr", listenAddr)
	} else {
		logger.Info("panel and Mihomo controller share MIHOMO_SECRET; requests must send the admin token header")
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
	// The dashboard origin must match the address a browser actually uses, so a LAN-served
	// dashboard needs its origin listed explicitly. Leaving this unset keeps the loopback
	// defaults, which is correct for a local-only deployment.
	dashboardOrigins := splitCommaList(os.Getenv("DASHBOARD_ORIGINS"))
	builder := &config.Builder{
		DB:               db.DB,
		SnapshotDir:      snapshotDir,
		ControllerSecret: secret,
		ControllerURL:    controllerURL,
		MihomoBinary:     envOr("MIHOMO_BINARY", "/usr/local/bin/mihomo"),
		DashboardOrigins: dashboardOrigins,
	}
	var runtime workflow.Runtime
	if controller := controllerURL; controller != "" {
		runtime = &workflow.HTTPRuntime{BaseURL: controller, Secret: secret}
	}
	flow := &workflow.Service{DB: db.DB, Builder: builder, Runtime: runtime, ActivePath: activePath}
	server := &http.Server{Addr: listenAddr, Handler: (&api.Server{Store: db, Workflow: flow, Logger: logger, Auth: api.AuthConfig{Token: secret, AllowUnauthenticated: allowUnauthenticated}, ActiveConfigPath: activePath}).Handler(), ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 15 * time.Second, WriteTimeout: 30 * time.Second, IdleTimeout: 60 * time.Second}
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

// splitCommaList parses a comma-separated environment value, dropping blanks so a trailing
// comma or an empty variable does not produce an empty origin entry.
func splitCommaList(raw string) []string {
	if strings.TrimSpace(raw) == "" {
		return nil
	}
	parts := strings.Split(raw, ",")
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		if trimmed := strings.TrimSpace(part); trimmed != "" {
			out = append(out, trimmed)
		}
	}
	return out
}
