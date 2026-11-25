// Command waveycord-lb fronts several waveycord instances with consistent
// hashing and health checks.
package main

import (
	"context"
	"flag"
	"log/slog"
	"net/http"
	"os"
	"time"

	"github.com/wavedevgit/waveycord/internal/lb"
)

type multiFlag []string

func (m *multiFlag) String() string { return "" }
func (m *multiFlag) Set(v string) error {
	*m = append(*m, v)
	return nil
}

func main() {
	var (
		addr     = flag.String("addr", "127.0.0.1:8081", "listen address")
		replicas = flag.Int("replicas", 128, "virtual nodes per backend")
		interval = flag.Duration("health-interval", 3*time.Second, "health check interval")
		backends multiFlag
	)
	flag.Var(&backends, "backend", "backend base URL, repeatable")
	flag.Parse()
	if len(backends) == 0 {
		backends = multiFlag{"http://127.0.0.1:8080"}
	}
	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))

	balancer, err := lb.New(backends, *replicas, log)
	if err != nil {
		log.Error("create balancer", "err", err)
		os.Exit(2)
	}
	balancer.SetHealthInterval(*interval)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	balancer.Start(ctx)
	defer balancer.Close()

	srv := &http.Server{Addr: *addr, Handler: balancer.Handler(), ReadHeaderTimeout: 10 * time.Second}
	log.Info("load balancer listening", "addr", *addr, "backends", backends)
	if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Error("serve", "err", err)
		os.Exit(1)
	}
}
