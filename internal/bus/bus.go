// Package bus carries gateway dispatches between API instances. Every instance
// fans out locally; Postgres LISTEN/NOTIFY makes the fanout cluster-wide so a
// client connected to any instance receives events for the guilds it joined.
package bus

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Event is one dispatch to fan out.
type Event struct {
	Origin  string          `json:"origin"`
	GuildID string          `json:"guild_id,omitempty"`
	UserIDs []string        `json:"user_ids,omitempty"`
	Name    string          `json:"name"`
	Data    json.RawMessage `json:"data"`
}

// Handler receives events published by any instance, including this one.
type Handler func(Event)

// Bus is the cluster transport.
type Bus interface {
	Publish(ctx context.Context, ev Event) error
	Close() error
}

// Local delivers events in-process only. It is the default for SQLite.
type Local struct {
	handler Handler
}

// NewLocal returns an in-process bus.
func NewLocal(h Handler) *Local { return &Local{handler: h} }

// Publish hands the event to the local handler.
func (l *Local) Publish(ctx context.Context, ev Event) error {
	if l.handler != nil {
		go l.handler(ev)
	}
	return nil
}

// Close is a no-op.
func (l *Local) Close() error { return nil }

// Postgres publishes over LISTEN/NOTIFY with the payload stored in a table, so
// a dispatch larger than the 8000-byte NOTIFY limit still crosses instances.
type Postgres struct {
	pool    *pgxpool.Pool
	origin  string
	handler Handler
	log     *slog.Logger

	mu       sync.Mutex
	conn     *pgx.Conn
	cancel   context.CancelFunc
	closed   atomic.Bool
	notifyMu sync.Mutex
	workers  sync.WaitGroup
}

// NewPostgres starts a listener on dsn. origin must be unique per instance.
func NewPostgres(ctx context.Context, dsn, origin string, h Handler, log *slog.Logger) (*Postgres, error) {
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		return nil, err
	}
	if log == nil {
		log = slog.Default()
	}
	p := &Postgres{pool: pool, origin: origin, handler: h, log: log}
	conn, err := pgx.Connect(ctx, dsn)
	if err != nil {
		pool.Close()
		return nil, err
	}
	if _, err := conn.Exec(ctx, `LISTEN waveycord_events`); err != nil {
		conn.Close(ctx)
		pool.Close()
		return nil, err
	}
	p.conn = conn
	listenCtx, cancel := context.WithCancel(context.Background())
	p.cancel = cancel
	p.workers.Add(2)
	go func() { defer p.workers.Done(); p.listen(listenCtx) }()
	go func() { defer p.workers.Done(); p.cleanup(listenCtx) }()
	return p, nil
}

// Publish stores the event and notifies the cluster.
func (p *Postgres) Publish(ctx context.Context, ev Event) error {
	if p.closed.Load() {
		return errors.New("bus: closed")
	}
	if ev.Origin == "" {
		ev.Origin = p.origin
	}
	raw, err := json.Marshal(ev)
	if err != nil {
		return err
	}
	var id int64
	err = p.pool.QueryRow(ctx, `INSERT INTO gateway_events (origin, payload, created_at)
		VALUES ($1,$2,$3) RETURNING id`, ev.Origin, string(raw), time.Now().UnixMilli()).Scan(&id)
	if err != nil {
		return err
	}
	p.notifyMu.Lock()
	defer p.notifyMu.Unlock()
	_, err = p.pool.Exec(ctx, `SELECT pg_notify('waveycord_events', $1)`, itoa(id))
	return err
}

func (p *Postgres) listen(ctx context.Context) {
	for {
		if ctx.Err() != nil {
			return
		}
		n, err := p.conn.WaitForNotification(ctx)
		if err != nil {
			if ctx.Err() != nil || p.closed.Load() {
				return
			}
			p.log.Warn("gateway bus notification error", "err", err)
			time.Sleep(250 * time.Millisecond)
			continue
		}
		p.deliver(ctx, n.Payload)
	}
}

func (p *Postgres) deliver(ctx context.Context, payload string) {
	var (
		origin string
		raw    string
	)
	err := p.pool.QueryRow(ctx, `SELECT origin, payload FROM gateway_events WHERE id = $1`, payload).
		Scan(&origin, &raw)
	if err != nil {
		if !errors.Is(err, pgx.ErrNoRows) && !p.closed.Load() && ctx.Err() == nil {
			p.log.Warn("gateway bus fetch", "err", err)
		}
		return
	}
	if origin == p.origin {
		return // already delivered locally when published
	}
	var ev Event
	if err := json.Unmarshal([]byte(raw), &ev); err != nil {
		return
	}
	if p.handler != nil {
		p.handler(ev)
	}
}

func (p *Postgres) cleanup(ctx context.Context) {
	ticker := time.NewTicker(10 * time.Minute)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			cutoff := time.Now().Add(-time.Hour).UnixMilli()
			_, _ = p.pool.Exec(ctx, `DELETE FROM gateway_events WHERE created_at < $1`, cutoff)
		}
	}
}

// Close stops the listener and closes the pool.
func (p *Postgres) Close() error {
	if !p.closed.CompareAndSwap(false, true) {
		return nil
	}
	p.cancel()
	p.workers.Wait()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if p.conn != nil {
		_ = p.conn.Close(ctx)
	}
	p.pool.Close()
	return nil
}

// HandlerFunc adapts a plain function.
func HandlerFunc(f func(Event)) Handler { return Handler(f) }

func itoa(n int64) string {
	if n == 0 {
		return "0"
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	return string(buf[i:])
}
