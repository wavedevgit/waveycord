// Command waveycord serves the documented Discord API and gateway.
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/wavedevgit/waveycord/internal/api"
	"github.com/wavedevgit/waveycord/internal/bus"
	"github.com/wavedevgit/waveycord/internal/db"
	"github.com/wavedevgit/waveycord/internal/gateway"
	"github.com/wavedevgit/waveycord/internal/search"
	"github.com/wavedevgit/waveycord/internal/sqlx"
	"github.com/wavedevgit/waveycord/internal/store"
)

func main() {
	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))

	addr := env("WAVEYCORD_ADDR", "127.0.0.1:8080")
	dsn := env("WAVEYCORD_DB", "./waveycord.db")
	gatewayURL := env("WAVEYCORD_GATEWAY_URL", "ws://"+addr+"/gateway")
	voiceEndpoint := env("WAVEYCORD_VOICE_ENDPOINT", "wss://voice.local")
	cdnBase := env("WAVEYCORD_CDN_BASE", "http://"+addr)

	handle, err := db.Open(dsn)
	if err != nil {
		log.Error("open database", "err", err)
		os.Exit(1)
	}
	defer handle.Close()

	st := store.New(handle)
	var searchIndex search.Index
	if esURLs := os.Getenv("WAVEYCORD_ES_URL"); esURLs != "" {
		es, err := search.NewES(search.ESConfig{
			URLs:     strings.Split(esURLs, ","),
			Username: os.Getenv("WAVEYCORD_ES_USERNAME"),
			Password: os.Getenv("WAVEYCORD_ES_PASSWORD"),
			Prefix:   env("WAVEYCORD_ES_PREFIX", "waveycord"),
			Shards:   envInt("WAVEYCORD_ES_SHARDS", 1),
			Replicas: envInt("WAVEYCORD_ES_REPLICAS", 0),
		})
		if err != nil {
			log.Error("open elasticsearch index", "err", err)
			os.Exit(1)
		}
		searchIndex = es
		defer es.Close()
		if err := es.Health(context.Background()); err != nil {
			log.Warn("elasticsearch health", "err", err)
		} else {
			log.Info("search backend", "backend", "elasticsearch", "prefix", env("WAVEYCORD_ES_PREFIX", "waveycord"))
		}
	} else {
		ix, err := search.Open(context.Background(), handle)
		if err != nil {
			log.Error("open search index", "err", err)
			os.Exit(1)
		}
		searchIndex = ix
		if bf, ok := ix.(search.Backfiller); ok {
			go func() {
				ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
				defer cancel()
				if n, err := bf.Backfill(ctx, 5000); err != nil {
					log.Warn("search backfill", "err", err)
				} else if n > 0 {
					log.Info("search backfill", "documents", n)
				}
			}()
		}
	}
	st.SetSearchIndex(searchIndex)
	if replicaDSN := os.Getenv("WAVEYCORD_PG_REPLICA_DSN"); replicaDSN != "" {
		replica, err := db.OpenReplica(replicaDSN)
		if err != nil {
			log.Error("open read replica", "err", err)
			os.Exit(1)
		}
		defer replica.Close()
		st.SetReadReplica(replica)
		log.Info("read replica enabled for message scans")
	}
	cfg := api.DefaultConfig()
	cfg.GatewayURL = gatewayURL
	cfg.CDNBase = cdnBase
	cfg.UploadDir = env("WAVEYCORD_UPLOAD_DIR", "./uploads")
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

	hub.SetStore(st, voiceEndpoint)

	// Postgres deployments share gateway dispatch across instances. SQLite is a
	// single-process store, so the in-process fanout is the whole bus.
	var transport bus.Bus
	if handle.Dialect.String() == "postgres" {
		origin := fmt.Sprintf("%s-%d-%d", hostname(), os.Getpid(), time.Now().UnixNano())
		pgBus, err := bus.NewPostgres(context.Background(), env("WAVEYCORD_PG_DSN", dsn), origin, hub.DeliverRemote, log)
		if err != nil {
			log.Error("start gateway bus", "err", err)
			os.Exit(1)
		}
		hub.SetBus(pgBus)
		transport = pgBus
		defer transport.Close()
	}

	// Monthly message partitions are created ahead of time; the check runs once a
	// day and on boot. SQLite is a no-op.
	go maintainPartitions(context.Background(), handle, log)

	srv := api.New(st, hub, cfg)
	httpServer := &http.Server{
		Addr:              addr,
		Handler:           srv.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       120 * time.Second,
	}

	go func() {
		log.Info("listening", "addr", addr, "db", handle.Dialect.String())
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

func maintainPartitions(ctx context.Context, handle *sqlx.DB, log *slog.Logger) {
	for {
		now := time.Now()
		if err := db.EnsureMessagePartitions(ctx, handle, now.AddDate(0, -1, 0), now.AddDate(0, 12, 0)); err != nil {
			log.Warn("ensure message partitions", "err", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(24 * time.Hour):
		}
	}
}

func envInt(key string, fallback int) int {
	if v := os.Getenv(key); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return fallback
}

func env(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func hostname() string {
	h, err := os.Hostname()
	if err != nil {
		return "host"
	}
	return h
}
