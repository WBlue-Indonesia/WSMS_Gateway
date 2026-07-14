// Command wsmsd is the WSMS-Gateway server: REST API + device WebSocket hub +
// operator-aware dispatcher.
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

	"github.com/nizwar/wsms-gateway/server/internal/api"
	"github.com/nizwar/wsms-gateway/server/internal/config"
	"github.com/nizwar/wsms-gateway/server/internal/dispatch"
	"github.com/nizwar/wsms-gateway/server/internal/fcm"
	"github.com/nizwar/wsms-gateway/server/internal/maintenance"
	"github.com/nizwar/wsms-gateway/server/internal/router"
	"github.com/nizwar/wsms-gateway/server/internal/store"
	"github.com/nizwar/wsms-gateway/server/internal/webhook"
	"github.com/nizwar/wsms-gateway/server/internal/ws"
)

func main() {
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo})))
	cfg := config.Load()

	db, err := store.Open(cfg.DatabaseURL, os.Getenv("WSMS_DEBUG") == "1")
	if err != nil {
		slog.Error("db open failed", "err", err)
		os.Exit(1)
	}

	// One-time bootstrap credentials for first run (logged once).
	if tok, created, err := store.BootstrapClient(db); err == nil && created {
		slog.Warn("BOOTSTRAP client API token (store it now, not shown again)", "token", tok)
	}
	if tok, created, err := store.BootstrapEnrollmentToken(db); err == nil && created {
		slog.Warn("BOOTSTRAP device enrollment token (valid 24h)", "token", tok)
	}

	prefixes, err := store.LoadPrefixes(db)
	if err != nil {
		slog.Error("load prefixes failed", "err", err)
		os.Exit(1)
	}
	if cfg.MasterKeyDev {
		slog.Warn("WSMS_SECRET_KEY not set — using an insecure dev master key; set it in production (64 hex chars)")
	}

	engine := router.New(db, prefixes)
	hub := ws.NewHub(db)

	// FCM waker (optional): revives offline devices when work is waiting for them.
	waker, err := fcm.New(cfg)
	if err != nil {
		slog.Error("fcm init failed", "err", err)
	}
	if waker != nil {
		slog.Info("FCM wake enabled", "project", cfg.FCMProjectID)
	}

	disp := dispatch.New(db, engine, hub, cfg)
	if waker != nil {
		disp.SetWaker(waker)
	}

	ctx, cancel := context.WithCancel(context.Background())
	go disp.Run(ctx)
	go webhook.New(db, cfg).Run(ctx)
	go maintenance.New(db, cfg).Run(ctx)

	srv := &http.Server{
		Addr:              cfg.HTTPAddr,
		Handler:           api.New(db, hub, engine, cfg, disp).Handler(),
		ReadHeaderTimeout: 10 * time.Second,
	}

	go func() {
		slog.Info("listening", "addr", cfg.HTTPAddr, "workers", cfg.DispatchWorkers)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			slog.Error("http server failed", "err", err)
			os.Exit(1)
		}
	}()

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
	<-sig
	slog.Info("shutting down")

	cancel()
	shutCtx, shutCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer shutCancel()
	_ = srv.Shutdown(shutCtx)
	hub.Shutdown(shutCtx)
}
