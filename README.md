# waveycord

A from-scratch implementation of the documented Discord API server in Go.

Scope: the documented HTTP API (`/api/v10`) and the documented Gateway protocol,
backed by a real SQL database (SQLite in WAL mode by default), with the object
shapes, error envelopes, permission bitfields and rate-limit headers the
published documentation specifies.

## Status

See `docs/COMPATIBILITY.md` for the endpoint-by-endpoint matrix. Nothing in this
tree is a mock: every handler reads and writes rows, and every listed endpoint is
exercised by an integration test that runs against the real server binary.

## Build

```bash
go build ./...
go test ./...
```

## Quickstart (SQLite)

```bash
go run ./cmd/waveycord
curl -s localhost:8080/api/v10/gateway
```

## Run with Postgres and the load balancer

```bash
WAVEYCORD_ADDR=127.0.0.1:8080 \
WAVEYCORD_DB=./waveycord.db \
go run ./cmd/waveycord
```

```bash
export WAVEYCORD_DB='postgres://waveycord:waveycord@127.0.0.1:5432/waveycord?sslmode=disable'
go run ./cmd/waveycord &            # 8080
WAVEYCORD_ADDR=127.0.0.1:8082 go run ./cmd/waveycord &   # second instance
go run ./cmd/waveycord-lb -addr 127.0.0.1:8081 \
  -backend http://127.0.0.1:8080 -backend http://127.0.0.1:8082
```

`docker compose up --build` runs Postgres, two instances and the balancer.

### Elasticsearch/OpenSearch search backend

```bash
WAVEYCORD_ES_URL=http://127.0.0.1:9200 \
WAVEYCORD_ES_PREFIX=waveycord \
WAVEYCORD_ES_SHARDS=1 \
go run ./cmd/waveycord
```

One index per guild (`<prefix>-guild-<guild_id>`), written through a bulk worker.
Without `WAVEYCORD_ES_URL` the server uses Postgres tsvector (or SQLite substring
matching).

`WAVEYCORD_SEARCH_REINDEX=reset` resets the selected backend and rebuilds it from
message rows at startup (bounded background scan); `incremental` re-upserts without
resetting.

The server serves `GET /api/v10/gateway` against the same listener, so a client
points its REST base at `http://127.0.0.1:8080/api/v10` (or the balancer at
`http://127.0.0.1:8081/api/v10`).

## Layout

| Path | Contents |
| --- | --- |
| `cmd/waveycord` | binary entrypoint, config, graceful shutdown |
| `internal/id` | snowflake generation and parsing |
| `internal/db` | connection, schema, migrations |
| `internal/model` | API object structs (docs field-for-field) |
| `internal/store` | SQL queries per aggregate |
| `internal/perm` | permission bitfields and overwrite resolution |
| `internal/api` | router, middleware, handlers |
| `internal/gateway` | gateway sessions and dispatch hub |

## License

MIT.
