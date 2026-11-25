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
| `PASSIVE_UPDATE_V2` deltas + OP 14 guild activation | implemented + tested |
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

### OAuth2

| Route | Status |
| --- | --- |
| `GET /oauth2/authorize` (consent view; JSON or HTML) | implemented + tested |
| `POST /oauth2/authorize` (consent, form or JSON) | implemented + tested |
| `POST /oauth2/token` `grant_type=authorization_code` | implemented + tested |
| `POST /oauth2/token` `grant_type=refresh_token` | implemented + tested |
| `GET /oauth2/@me` | implemented + tested |
| `PATCH /applications/{application.id}` (redirect URIs) | implemented + tested |
| Bot install via `scope=bot` + `guild_id` | implemented + tested (requires MANAGE_GUILD) |

Codes are single-use, expire after ten minutes and are bound to the registered
redirect URI and client. Secrets and tokens are stored as SHA-256 hashes; the
client secret is shown only in the create response. Unknown scopes, unregistered
redirect URIs, reused codes, revoked refresh tokens and unknown grant types are
rejected.

### Interactions

| Route | Status |
| --- | --- |
| `POST /interactions` (Ed25519 signature, timestamp replay window) | implemented + tested |
| Interaction type 1 (PING) → PONG | implemented + tested |
| Interaction type 2 (application command) dispatch | implemented + tested |
| Autocomplete (type 4) → empty choices | implemented |
| `GET/PUT/POST /applications/{id}/commands` | implemented + tested |
| `GET/PATCH/DELETE /applications/{id}/commands/{command.id}` | implemented + tested |
| Guild command variants at `/applications/{id}/guilds/{guild.id}/commands` | implemented + tested |
| `verify_key` registration through `PATCH /applications/{id}` | implemented + tested |

Signature verification covers `X-Signature-Timestamp + raw body` with the
application's 32-byte Ed25519 public key; timestamps outside five minutes are
rejected, as are missing, malformed and non-matching signatures. Guild commands
shadow global commands on invocation.

Deliberate adaptations: the interactions endpoint is global, so the payload must
carry `application_id` (Discord routes by per-application URL); dispatch is
in-process (echo handlers plus a default acknowledgement) because a self-hosted
deployment has no external bot endpoint to call; message components, modals and
role connections remain TBD.

Deliberate adaptation: Discord's consent screen is a browser UI. This server
returns the same fields as JSON when `Accept: application/json` is sent, and a
302 with `code`/`state` in the query for browser-style form posts. Interactions,
role connections and the developer portal UI remain TBD.

## Auto moderation

| Route | Status |
| --- | --- |
| `GET /guilds/{guild.id}/auto-moderation/rules` | implemented + tested |
| `POST /guilds/{guild.id}/auto-moderation/rules` | implemented + tested |
| `GET/PATCH/DELETE /guilds/{guild.id}/auto-moderation/rules/{rule.id}` | implemented + tested |
| MESSAGE_SEND keyword trigger (`keyword_filter`) | executed + tested |
| MESSAGE_SEND regex trigger (`regex_patterns`) | executed + tested |
| Keyword preset trigger (`presets`) | executed (built-in term lists) |
| Mention spam trigger (`mention_total_limit`) | executed + tested |
| Actions: block message, send alert, timeout | executed + tested |
| Exempt roles / exempt channels | executed + tested |
| Keyword allow lists | executed |
| Spam, member profile, mention raid triggers | stored, not evaluated |

Blocked messages return `400` with code `200000` and are not persisted. Alerts are
posted by a system account into the configured channel; timeouts set
`communication_disabled_until` on the member. Spam heuristics, profile scans and
mention-raid detection require cross-message state that this deployment does not
keep, and are exposed only as stored rules.

## Message search

Search is `ILIKE`-style SQL over the partitioned message table, filtered by the
caller's readable channels. It is **not** the published sharded-index
architecture: there is no Elasticsearch/Lucene cell, no PubSub-backed indexing
queue, no per-guild index sizing and no BFG cell. Consequences:

- Result quality is substring matching, not tokenised relevance.
- Latency grows with guild history rather than staying index-bound.
- Large guilds cannot exceed a single table scan budget.

Closing this gap means deploying an external index (Elasticsearch/OpenSearch) with
guild-sharded indices and an indexing worker fed from message writes. The code
boundary is `Store.SearchMessages`; a real implementation would replace that call
with an index client and keep the same permission filtering. This is recorded as a
known, deliberate gap rather than claimed parity.

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
