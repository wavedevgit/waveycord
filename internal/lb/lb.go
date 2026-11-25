// Package lb implements the HTTP/WebSocket load balancer that fronts waveycord
// instances: consistent hashing on the resource id for reads, least-outstanding
// for WebSocket upgrades, and health-checked membership.
package lb

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httputil"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/wavedevgit/waveycord/internal/hashring"
)

// Backend is one upstream instance.
type Backend struct {
	URL    *url.URL
	proxy  *httputil.ReverseProxy
	alive  atomic.Bool
	active atomic.Int64
}

// Alive reports health.
func (b *Backend) Alive() bool { return b.alive.Load() }

// Balancer routes requests across backends.
type Balancer struct {
	ring     *hashring.Ring
	backends []*Backend
	lookup   map[string]*Backend
	log      *slog.Logger
	interval time.Duration
	counter  atomic.Uint64
	cancel   context.CancelFunc
}

// New builds a balancer. Backend URLs must include scheme and host.
func New(raw []string, replicas int, log *slog.Logger) (*Balancer, error) {
	if log == nil {
		log = slog.Default()
	}
	b := &Balancer{ring: hashring.New(replicas), lookup: map[string]*Backend{}, log: log, interval: 3 * time.Second}
	for _, item := range raw {
		u, err := url.Parse(item)
		if err != nil || u.Scheme == "" || u.Host == "" {
			return nil, &url.Error{Op: "parse", URL: item, Err: errInvalidBackend}
		}
		be := &Backend{URL: u}
		be.alive.Store(true)
		be.proxy = &httputil.ReverseProxy{
			Director: func(r *http.Request) {
				r.URL.Scheme = u.Scheme
				r.URL.Host = u.Host
				r.Host = u.Host
			},
			ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
				log.Warn("proxy error", "backend", u.Host, "err", err)
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusBadGateway)
				_, _ = w.Write([]byte(`{"code":0,"message":"502 Bad Gateway"}`))
			},
			FlushInterval: 100 * time.Millisecond,
			ModifyResponse: func(resp *http.Response) error {
				resp.Header.Set("X-Waveycord-Backend", u.Host)
				return nil
			},
		}
		b.ring.Add(u.Host)
		b.backends = append(b.backends, be)
		b.lookup[u.Host] = be
	}
	return b, nil
}

// Start runs the health loop until Close.
func (b *Balancer) Start(ctx context.Context) {
	ctx, cancel := context.WithCancel(ctx)
	b.cancel = cancel
	go b.healthLoop(ctx)
}

// Close stops the health loop.
func (b *Balancer) Close() {
	if b.cancel != nil {
		b.cancel()
	}
}

// Handler returns the routing handler.
func (b *Balancer) Handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/healthz":
			ok := 0
			for _, be := range b.backends {
				if be.alive.Load() {
					ok++
				}
			}
			status := http.StatusOK
			if ok == 0 {
				status = http.StatusServiceUnavailable
			}
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(status)
			_ = json.NewEncoder(w).Encode(map[string]any{"healthy": ok, "total": len(b.backends)})
			return
		case "/-/ring":
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{"members": b.ring.Members()})
			return
		}
		target := b.pick(r)
		if target == nil {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = w.Write([]byte(`{"code":0,"message":"503 Service Unavailable"}`))
			return
		}
		target.active.Add(1)
		defer target.active.Add(-1)
		target.proxy.ServeHTTP(w, r)
	})
}

func (b *Balancer) pick(r *http.Request) *Backend {
	if isUpgrade(r) {
		return b.leastOutstanding()
	}
	if key := RoutingKey(r.URL.Path); key != "" {
		if host, ok := b.ring.Get(key); ok {
			if be := b.lookup[host]; be != nil && be.alive.Load() {
				return be
			}
		}
	}
	// Round robin over live backends.
	live := make([]*Backend, 0, len(b.backends))
	for _, be := range b.backends {
		if be.alive.Load() {
			live = append(live, be)
		}
	}
	if len(live) == 0 {
		return nil
	}
	return live[int(b.counter.Add(1))%len(live)]
}

func (b *Balancer) leastOutstanding() *Backend {
	var best *Backend
	for _, be := range b.backends {
		if !be.alive.Load() {
			continue
		}
		if best == nil || be.active.Load() < best.active.Load() {
			best = be
		}
	}
	return best
}

var idPath = regexp.MustCompile(`^/api/v10/(channels|guilds|invites)/([^/]+)`)

// RoutingKey extracts the consistent-hash key for a path. Channel and guild ids
// keep all traffic for one resource on one instance, which is what makes
// request coalescing effective.
func RoutingKey(path string) string {
	m := idPath.FindStringSubmatch(path)
	if m == nil {
		return ""
	}
	return m[1] + ":" + m[2]
}

func isUpgrade(r *http.Request) bool {
	return strings.EqualFold(r.Header.Get("Upgrade"), "websocket") ||
		strings.Contains(strings.ToLower(r.Header.Get("Connection")), "upgrade")
}

func (b *Balancer) healthLoop(ctx context.Context) {
	client := &http.Client{Timeout: 2 * time.Second}
	check := func() {
		var wg sync.WaitGroup
		for _, be := range b.backends {
			wg.Add(1)
			go func(be *Backend) {
				defer wg.Done()
				resp, err := client.Get(be.URL.Scheme + "://" + be.URL.Host + "/healthz")
				healthy := err == nil && resp.StatusCode == http.StatusOK
				if resp != nil {
					resp.Body.Close()
				}
				was := be.alive.Swap(healthy)
				if healthy && !was {
					b.ring.Add(be.URL.Host)
					b.log.Info("backend up", "host", be.URL.Host)
				}
				if !healthy && was {
					b.ring.Remove(be.URL.Host)
					b.log.Warn("backend down", "host", be.URL.Host)
				}
			}(be)
		}
		wg.Wait()
	}
	check()
	ticker := time.NewTicker(b.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			check()
		}
	}
}

var errInvalidBackend = &backendError{"invalid backend URL"}

type backendError struct{ msg string }

func (e *backendError) Error() string { return e.msg }

// SetHealthInterval overrides the health check period before Start.
func (b *Balancer) SetHealthInterval(d time.Duration) {
	if d > 0 {
		b.interval = d
	}
}

// RingMembers exposes the current set for observability.
func (b *Balancer) RingMembers() []string { return b.ring.Members() }
