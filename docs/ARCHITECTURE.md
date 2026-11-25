# Architecture

The layout follows the object model in the official documentation
(`developers/resources/*.mdx`) and the user documentation
(`discord-userdoccers`). The scaling design follows the published engineering
material on message storage.

## Process layout

```
client ──► waveycord-lb ──► waveycord instance A ──► PostgreSQL
                        └─► waveycord instance B ──► PostgreSQL
                                     │
                                     └── LISTEN/NOTIFY ── gateway fanout
```

- `waveycord-lb` is a real load balancer: consistent hashing on
  `channel:<id>`, `guild:<id>` and `invite:<code>` for HTTP reads,
  least-outstanding for WebSocket upgrades, health checks on `/healthz`, and a
  `/-/ring` view of live members.
- Instances are stateless with respect to HTTP. Sessions live in the database;
  gateway connections are the only per-instance state.
- `internal/bus` carries gateway dispatches across instances. The event body is
  stored in `gateway_events` and the `NOTIFY` payload is only the row id, so a
  dispatch is not limited by the 8000-byte `NOTIFY` ceiling. The publishing
  instance skips its own notification after fanning out locally.

## Message storage

The schema keeps the published access pattern:

- `messages` is keyed `(channel_id, id DESC)` — partition by channel, sort by
  snowflake — so a channel page is one ordered range scan and `before/after/
  around` are index scans.
- Snowflakes are generated from Discord's epoch (2015-01-01), 42-bit ms
  timestamp, 5-bit worker, 12-bit sequence, so ids sort chronologically.
- Reads are coalesced with `singleflight` keyed by channel and page parameters
  (`internal/api/messages.go`). The load balancer keeps a channel's reads on one
  instance, which is what makes coalescing effective — the same two-part design
  the published message-storage post describes for data services.
- `after` pages are fetched ascending then reversed so every page response is
  newest-first.

Postgres is the multi-instance backend; SQLite (WAL) is the single-process
default. One statement set serves both: `internal/sqlx` rewrites `?` to `$n` and
maps Go bools onto the `SMALLINT` flag columns.

## Permissions

`internal/perm` implements the documented resolution order: base role bits
(union of `@everyone` and member roles), `ADMINISTRATOR` short-circuit,
`@everyone` overwrite, accumulated role overwrites, member overwrite. Thread
channels resolve against their parent channel. Guild owners hold every bit.
Channel lists filter out channels without `VIEW_CHANNEL`.

## Gateway

`internal/gateway` implements the documented handshake: `HELLO` with
`heartbeat_interval`, `IDENTIFY` → `READY`, `HEARTBEAT` → `HEARTBEAT_ACK`,
`RESUME` with a 200-event replay buffer, `INVALID_SESSION`, and dispatch frames
carrying `s`. Close codes use the documented `4000`–`4014` range. RESUME from a
sequence older than the buffer answers `INVALID_SESSION: true`, which is the
documented recovery path.

## Reliability choices

- SQLite runs one writer connection to avoid `SQLITE_BUSY`; Postgres defaults to
  16 connections (`WAVEYCORD_PG_MAX_CONNS`).
- Postgres schema application takes `pg_advisory_lock` so simultaneous instance
  starts cannot deadlock in the catalog.
- Nested queries are never issued while a `*sql.Rows` cursor is open; rows are
  drained and closed before hydration. The single SQLite connection makes that
  mandatory rather than stylistic.
- Every mutating handler writes an audit entry when a guild is involved.
