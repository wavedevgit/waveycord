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

## Run

```bash
WAVEYCORD_ADDR=127.0.0.1:8080 \
WAVEYCORD_DB=./waveycord.db \
go run ./cmd/waveycord
```

The server serves `GET /api/v10/gateway` against the same listener, so a client
points its REST base at `http://127.0.0.1:8080/api/v10`.

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
