# Engineering blog notes

Posts read from https://discord.com/category/engineering and what this server
takes from each. A post is listed as mirrored only where code and tests exist.

## How Discord Stores Trillions of Messages (2023-03-06)

Source design: messages in ScyllaDB, partition key = channel + static time bucket;
read path protected by Rust data services that coalesce identical requests and are
routed by consistent hash on channel id.

Mirrored:

- Partition layout by channel then time — `messages` is `HASH(channel_id)` →
  `RANGE(bucket)` with monthly children (`internal/db/schema_postgres.sql`,
  `internal/db/partitions.go`).
- Channel-ordered reads — index `(channel_id, id DESC)`, snowflake ordering.
- Request coalescing — `singleflight` keyed by channel and page parameters
  (`internal/api/messages.go`).
- Consistent-hash routing to keep a channel's reads on one instance
  (`internal/lb`).
- Live migration and validation — `TestLegacyMessagesMigration`.

Not applicable: ScyllaDB internals (SSTables, compaction, shard-per-core, token
ring), 72-node topology, Spark migrator.

## How Discord Indexes Trillions of Messages (2025-04-24)

Source design: a separate search index sharded by guild (and by user for DMs),
with bulk indexing and a dedicated cell for very large guilds.

Mirrored:

- A dedicated `message_index` table: message id → channel/bucket, used to route
  id-only reads. This is the same separation of concerns (storage vs lookup) at a
  smaller scale.
- Guild-scoped message search with permission filtering
  (`GET /guilds/{id}/messages/search`).

Not implemented: Elasticsearch, PubSub-backed indexing workers, BFG reindexing.
Search is SQL `LIKE`, which is adequate for a test deployment and not for
trillions of rows.

## Why Discord is switching from Go to Rust (2020-02-04) / read states

Source design: Read States is an LRU cache in front of Cassandra, one read state
per user per channel, counters updated atomically, writes batched.

Mirrored:

- `read_states` table plus `POST /channels/{id}/messages/{id}/ack`.
- Hot read path kept small: ack writes one row, unread counts are a column.

Not implemented: a distributed LRU cache tier; the database is the cache at this
scale. This is a capacity decision, not an API difference.

## How Discord Reduced Websocket Traffic by 40% (2024-09-20)

Source design: streaming zstd replaces streaming zlib; per-payload dictionaries
were tested and rejected; passive sessions send deltas instead of full snapshots.

Status: not implemented. The gateway currently sends uncompressed JSON frames.
Adding `compress=zstd-stream` requires a streaming compressor per connection and a
length-prefixed frame format; the blog's numbers (MESSAGE_CREATE 270 → 166 bytes)
would apply here too. This is the largest known gap between this server and the
published realtime design, and it is tracked in `COMPATIBILITY.md`.

## How We Moved Discord Voice to the Edge / DAVE / WebRTC posts

Voice is a media-plane problem (WebRTC, SFU, E2EE DAVE) with no REST equivalent.
Not implemented; the voice region listing endpoint returns a static set. No claim
of parity is made.
