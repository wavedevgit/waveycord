// Command waveycord serves the documented Discord API and gateway.
package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"github.com/wavedevgit/waveycord/internal/api"
	"github.com/wavedevgit/waveycord/internal/db"
	"github.com/wavedevgit/waveycord/internal/gateway"
	"github.com/wavedevgit/waveycord/internal/store"
)

func main() {
	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))

	addr := env("WAVEYCORD_ADDR", "127.0.0.1:8080")
	dbPath := env("WAVEYCORD_DB", "./waveycord.db")
	gatewayURL := env("WAVEYCORD_GATEWAY_URL", "ws://"+addr+"/gateway")

	handle, err := db.Open(dbPath)
	if err != nil {
		log.Error("open database", "err", err)
		os.Exit(1)
	}
	defer handle.Close()

	st := store.New(handle)
	cfg := api.DefaultConfig()
	cfg.GatewayURL = gatewayURL
	cfg.Logger = log
	if v := os.Getenv("WAVEYCORD_RATE_LIMIT"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			cfg.RateLimit = n
		}
	}

	hub := gateway.NewHub(
		func(token string) (string, bool) {
			sess, user, err := st.SessionByToken(api.TokenHash(token))
			if err != nil {
				return "", false
			}
			st.TouchSession(sess.ID)
			return user.ID, true
		},
		api.ReadyBuilder(st, gatewayURL),
		api.GuildIDsFor(st),
		log,
	)

	srv := api.New(st, hub, cfg)
	httpServer := &http.Server{
		Addr:              addr,
		Handler:           srv.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       120 * time.Second,
	}

	go func() {
		log.Info("listening", "addr", addr, "db", dbPath)
		if err := httpServer.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Error("serve", "err", err)
			os.Exit(1)
		}
	}()

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)
	<-stop

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := httpServer.Shutdown(ctx); err != nil {
		log.Error("shutdown", "err", err)
	}
}

func env(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
