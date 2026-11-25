# Compatibility

Status is evidence-based: an endpoint is listed as implemented only when a test
in `internal/api/*_test.go` exercises it against a real database. `TBD` means the
route is not registered yet.

## Gateway

| Route | Status |
| --- | --- |
| `GET /gateway` | implemented |
| `GET /gateway/bot` | implemented |
| WebSocket `/gateway` (HELLO, IDENTIFY, READY, HEARTBEAT/ACK, RESUME, INVALID_SESSION) | implemented + tested |
| Transport compression `?compress=zstd-stream` | implemented + tested |
| Cross-instance dispatch (Postgres LISTEN/NOTIFY) | implemented + tested |

## Auth

| Route | Status |
| --- | --- |
| `POST /auth/register` | implemented |
| `POST /auth/login` | implemented |
| `POST /auth/logout` | implemented |
| `GET /auth/sessions` | implemented |
| `POST /auth/sessions/logout` | implemented |
| MFA / WebAuthn / remote auth | TBD |

## Users

| Route | Status |
| --- | --- |
| `GET /users/@me` | implemented |
| `PATCH /users/@me` | implemented |
| `GET /users/@me/guilds` | implemented |
| `GET /users/@me/channels` | implemented |
| `POST /users/@me/channels` | implemented |
| `GET /users/@me/relationships` | implemented |
| `PUT/DELETE /users/@me/relationships/{user.id}` | implemented |
| `DELETE /users/@me/guilds/{guild.id}` (leave) | implemented |
| `GET /users/{user.id}` | implemented |
| `GET /users/{user.id}/profile` | implemented (basic) |

## Guilds

| Route | Status |
| --- | --- |
| `POST /guilds` | implemented |
| `GET/PATCH/DELETE /guilds/{guild.id}` | implemented |
| `GET /guilds/{guild.id}/preview` | implemented |
| `GET /guilds/{guild.id}/channels` | implemented |
| `POST /guilds/{guild.id}/channels` | implemented |
| `PATCH /guilds/{guild.id}/channels` (positions) | implemented |
| `GET /guilds/{guild.id}/threads/active` | implemented |
| `GET /guilds/{guild.id}/members` | implemented |
| `GET/PATCH/DELETE /guilds/{guild.id}/members/{user.id}` | implemented |
| `PUT/DELETE /guilds/{guild.id}/members/{user.id}/roles/{role.id}` | implemented |
| `GET/POST /guilds/{guild.id}/roles` | implemented |
| `PATCH/DELETE /guilds/{guild.id}/roles/{role.id}` | implemented |
| `GET /guilds/{guild.id}/bans` and `GET/PUT/DELETE .../bans/{user.id}` | implemented |
| `GET /guilds/{guild.id}/invites` | implemented |
| `GET /guilds/{guild.id}/emojis` and emoji CRUD | implemented |
| `GET /guilds/{guild.id}/audit-logs` | implemented |
| `GET /guilds/{guild.id}/vanity-url` | implemented (read) |
| `GET /guilds/{guild.id}/webhooks` | implemented |
| `GET /regions`, `GET /guilds/{guild.id}/regions` | implemented (static) |
| Guild templates, scheduled events, auto-moderation, integrations, stage instances | TBD |

## Channels

| Route | Status |
| --- | --- |
| `GET/PATCH/DELETE /channels/{channel.id}` | implemented |
| `PUT/DELETE /channels/{channel.id}/permissions/{overwrite.id}` | implemented |
| `GET/POST /channels/{channel.id}/invites` | implemented |
| `POST /channels/{channel.id}/typing` | implemented |
| `PUT/DELETE /channels/{channel.id}/recipients/{user.id}` | implemented |
| `POST /channels/{channel.id}/attachments` | implemented |
| `PUT /attachments/{id}/{filename}` | implemented |
| `GET /cdn/attachments/{channel}/{id}/{filename}` | implemented |
| `POST /channels/{channel.id}/threads` | implemented |
| `POST /channels/{channel.id}/messages/{message.id}/threads` | implemented |
| Thread member routes | implemented |
| `GET /channels/{channel.id}/threads/archived/public` and `/private` | implemented |
| Voice state routes, stage instances | TBD |

## Messages

| Route | Status |
| --- | --- |
| `GET /channels/{channel.id}/messages` (before/after/around/limit) | implemented |
| `POST /channels/{channel.id}/messages` | implemented |
| `GET/PATCH/DELETE /channels/{channel.id}/messages/{message.id}` | implemented |
| `POST /channels/{channel.id}/messages/bulk-delete` | implemented |
| `POST /channels/{channel.id}/messages/{message.id}/crosspost` | implemented (validation only) |
| Reaction routes (add, remove own, remove user, list users, clear emoji, clear all) | implemented |
| `GET /channels/{channel.id}/pins`, `PUT/DELETE .../pins/{message.id}` | implemented |
| `POST /channels/{channel.id}/messages/{message.id}/ack` | implemented |
| `GET /guilds/{guild.id}/messages/search` | implemented |
| Polls, components v2 validation | TBD |

## Invites and webhooks

| Route | Status |
| --- | --- |
| `GET /invites/{code}` | implemented (public) |
| `POST /invites/{code}` (accept) | implemented |
| `DELETE /invites/{code}` | implemented |
| Webhook create/list/get/update/delete | implemented |
| `POST /webhooks/{id}/{token}` (execute, `?wait=true`) | implemented |

## Applications and bots

| Route | Status |
| --- | --- |
| `POST /applications` | implemented |
| `GET /applications` | implemented |
| `GET /applications/@me` (bot token) | implemented |
| `POST /applications/{application.id}/bot/reset` | implemented |
| `Authorization: Bot <token>` | implemented + tested |

OAuth2 authorization code flow and interaction endpoints are TBD.

## Deliberately different from Discord

- CDN is served by the same process (`/cdn/...`); Discord uses a separate host.
- Attachments are stored on local disk (`WAVEYCORD_UPLOAD_DIR`); object storage
  is a deployment choice, not an API difference.
- Voice gateway and media transport are not implemented.
- Emoji images are accepted but not decoded; the object shape is returned.

## Storage layout (Postgres)

| Concern | Implementation |
| --- | --- |
| Partition key | `HASH(channel_id)` then `RANGE(bucket)` (UTC day, monthly children) |
| Id-only reads | `message_index` resolves channel, then one partition |
| Migration | legacy table renamed, copied with bucket, dropped; tested |
| Future buckets | monthly children created at boot + daily; DEFAULT child per hash |
| SQLite | single table, same columns and index table |

## Scale notes

- Postgres is required for more than one instance.
- The load balancer hashes channel ids so per-channel read coalescing stays on
  one instance, and health-checks members every 3 s by default.
- Gateway sessions are sticky to the instance that accepted the WebSocket; a
  reconnect may land elsewhere and the session resumes from the database-backed
  account, not from instance memory.
