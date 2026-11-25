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

The published message-storage design partitions messages by channel and by a
static time window. Postgres cannot be ScyllaDB, but declarative partitioning
expresses the same logical layout:

```
messages                                  PARTITION BY HASH (channel_id)      -- 8 children
  messages_h0 ... messages_h7             PARTITION BY RANGE (bucket)
    messages_hN_yYYYYMM                   one child per calendar month
    messages_hN_default                   fallback for out-of-window rows
```

- `bucket` is the UTC day index (`timestamp / 86_400_000`), written on insert.
- A channel read names one channel, so hash partitioning prunes to one of eight
  children; the snowflake-derived bucket bounds prune to one month child.
  `EXPLAIN` on a page query shows a single child index scan.
- `message_index (id → channel_id, bucket)` is the analogue of Discord's separate
  message index: an id-only request (reaction, edit, delete) resolves its channel
  once and reads one partition instead of scanning all of them.
- Indexes `(channel_id, id DESC)` and `(guild_id, id DESC)` are declared on the
  parent and Postgres creates them on every new child automatically.
- Monthly children are created for `now-1 month .. now+12 months` at boot and
  once a day (`db.EnsureMessagePartitions`). Out-of-range writes land in the
  per-hash DEFAULT child rather than failing.
- Existing deployments are migrated in place: the old table is renamed, the
  partitioned parent is created, rows are copied with their bucket, the
  `message_index` is filled, and the legacy table is dropped. The whole migration
  runs under the schema advisory lock. `TestLegacyMessagesMigration` builds a
  pre-partitioning schema and asserts the migrated row lands in the correct
  monthly child.
- Reactions and pins reference `channel_id` for cascade (a message id is no longer
  globally unique across partitions); message deletion clears both explicitly.

SQLite keeps a single `messages` table with the same columns and index table; the
partitioning is a Postgres deployment concern, documented rather than emulated.

Engine difference, stated plainly: Discord runs ScyllaDB (wide-column, shard per
core, no GC). This implementation uses Postgres declarative partitioning. The
partition key, the per-channel index, the separate message index, and the
coalesced read path match the published design; compaction, SSTables, quorum
reads and token-ring ownership do not exist here and are not claimed.

## OAuth2

Authorization is database-backed: `oauth2_authorization_codes` holds single-use
codes bound to application, user, redirect URI and scopes; `oauth2_tokens` holds
access/refresh pairs as SHA-256 hashes. Consent with `scope=bot` installs the
application's bot user into the requested guild after a `MANAGE_GUILD` check, the
same point the real flow installs the bot. Access tokens authenticate as the
authorizing user for `GET /users/@me` and `/oauth2/@me`.

## Interactions

`POST /interactions` verifies Ed25519 over the timestamp string concatenated with
the exact raw body, so the body is read before decoding and any re-serialisation
would invalidate the signature. Application public keys live on the application
row (`verify_key`), registered by the owner. Commands are rows scoped by
`(application_id, guild_id)`, with guild rows shadowing global ones at dispatch.
Dispatch has two paths. With `interactions_endpoint_url` configured, the raw
verified body is forwarded to the bot with an app-specific Ed25519 signature
(`signing_public_key` is exposed for verification) and the endpoint's JSON
callback is relayed; failures surface as `502`. Without an endpoint, the
documented envelope (`type: 4`, `data.content`) comes from an in-process handler
registry. Per-application signing seeds are stored in the application row; a
production deployment would hold them in a KMS instead.

Component surfaces share one validator (`internal/api/components.go`): message
create/edit/webhook reject malformed action rows before persistence, type-3
interactions validate `custom_id`/`component_type`/`values`, and type-5
interactions validate the modal row tree and extract submitted fields. Local
callbacks are acknowledgements (`type: 4`) describing what was received; a
configured bot endpoint receives the raw payload instead.

## Auto moderation

Rules are rows scoped by guild with JSON trigger metadata and actions. Message
creation calls `enforceAutoMod` before persistence: enabled `MESSAGE_SEND` rules
are matched by keyword/regex/preset or mention count, exempt roles and channels
are skipped, then block, alert and timeout actions run. Blocking is a response
decision (`400`, code `200000`) and the row is never written. Alerts create a
message authored by a system account; timeouts update the member row. Spam is evaluated with a per-`(guild, channel, user)` sliding window held in the
API process; member-profile rules run the keyword/regex/preset matcher over
username, display name and nickname at join and on member update. Mention-raid
detection is still stored-only: it needs cross-channel aggregation that this
process does not keep.

## Message search

`internal/search` is the boundary; `pgIndex` is the default on Postgres and
`sqliteIndex` keeps substring behavior for the embedded engine. The Postgres index
is a `search_documents` table (`message_id` primary key, `body tsvector`, GIN) kept
in sync on message create/update/delete. `Store.SearchMessages` hydrates ranked ids
back into API objects; the handler filters channels by permission before the query,
so both backends share one authorization path.

The index is populated for existing rows by `Backfiller` (bounded batch at
startup). Writes are inline, not queued; the message row stays authoritative and
an index failure does not fail the write.

## Role connections

`role_connections` stores one row per `(user_id, application_id)` with platform
name, username and a metadata map; `applications.role_connection_metadata` holds
the registry the values are validated against. Both endpoints authenticate with an
OAuth2 bearer token, verify the token's application matches the path, and require
the `role_connections.write` scope — the session and bot authentication paths are
rejected.

## Voice signaling

`voice_states` stores one row per `(guild_id, user_id)` with channel, session,
self/server mute and deaf flags, stream/video flags, stage suppression,
request-to-speak timestamp, a per-join voice token and the channel status. OP 4
validates membership and channel type, writes the row, dispatches
`VOICE_STATE_UPDATE` through the guild fanout (so passive sessions see it as a
journal delta) and sends `VOICE_SERVER_UPDATE` to the user. READY includes the
persisted states. Media transport is out of scope: the advertised endpoint has no
voice gateway behind it.

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
carrying `s`. Close codes use the documented `4000`–`4014` range.

Passive sessions follow the same post: guilds begin passive, a bounded journal
records channel/member/voice changes with sequence numbers, and a ticker emits one
`PASSIVE_UPDATE_V2` per changed guild containing only entities past that
connection's cursor. OP 14 promotes a guild to the live firehose. A cursor older
than the journal (or an entity removal) produces a `full` update with the current
entity set, because the delta shape has no deletion entry.

Transport compression follows the realtime post: a client that connects with
`?compress=zstd-stream` gets one streaming zstd encoder for the life of the
connection, so the compression window spans payloads instead of restarting on each
frame. Flushed chunks are sent as binary frames; inbound client payloads remain
uncompressed JSON. Payloads larger than one flush boundary are still one logical
JSON object to a streaming decoder. RESUME from a
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

## Operations

| Variable | Purpose |
| --- | --- |
| `WAVEYCORD_ADDR` | listen address (default `127.0.0.1:8080`) |
| `WAVEYCORD_DB` | SQLite path or `postgres://` URL |
| `WAVEYCORD_PG_DSN` | bus/listener DSN if it differs from `WAVEYCORD_DB` |
| `WAVEYCORD_PG_REPLICA_DSN` | read replica for message scans |
| `WAVEYCORD_PG_MAX_CONNS` | Postgres pool size (default 16) |
| `WAVEYCORD_GATEWAY_URL` | value advertised by `GET /gateway` |
| `WAVEYCORD_CDN_BASE` | absolute base for attachment URLs |
| `WAVEYCORD_UPLOAD_DIR` | attachment storage directory |
| `WAVEYCORD_RATE_LIMIT` | requests per route bucket per window |
| `WAVEYCORD_WORKER_ID` | snowflake worker bits (0–31) |

Endpoints outside the API:

- `GET /healthz` — pings the database; `503` with `"status":"degraded"` on failure.
- `GET /metrics` — Prometheus text: request counts by method/route/status, DB pool
  gauges, gateway connections and dispatch totals.
- `GET /-/ring` (balancer) — current live backends.

Read/write splitting: only the message-list and message-search scans use the
replica (`Store.read`). Hydration (users, members, reactions) stays on the
primary so a just-created account or channel is never a replica miss.

Graceful shutdown drains HTTP for 10 s and closes the bus listener, which is the
only connection with a session-scoped Postgres advisory lock.
