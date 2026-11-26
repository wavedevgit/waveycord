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
| Guild templates, scheduled events, stage instances, integrations | implemented + tested (see sections below) |

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
| `GET/PATCH /channels/{channel.id}/voice-status` | implemented + tested |
| Gateway OP 4 voice state update, persisted voice states | implemented + tested |
| `VOICE_STATE_UPDATE` + `VOICE_SERVER_UPDATE` dispatches | implemented + tested |
| Voice gateway WebSocket (`/voice`): HELLO, IDENTIFY, READY, SELECT_PROTOCOL, SESSION_DESCRIPTION, heartbeats | implemented + tested |
| UDP IP discovery (74-byte request/response) | implemented + tested |
| RTP packet reception + counters (no decode) | implemented + tested |
| SFU-style RTP fanout to same-channel connections (local + cross-instance via bus) | implemented + tested |
| Media crypto boundary: X25519 key exchange, AES-256-GCM envelope, replay protection, key rotation | implemented + tested (server-terminated for the WCDF path) |
| Member group key agreement (op 34/35) with server-opaque `WCE2` media, local + cross-instance | implemented + tested |
| Epoch rekey signal on voice join/leave (op 36, local + cross-instance) | implemented + tested |
| Epoch rotation excluding departed members; stale-epoch rejection | implemented + tested |
| Joint epoch secret from per-member contributions (TreeKEM-style) | implemented + tested |
| Signed roster commit (op 38) + tree-derived epoch secret | implemented + tested |
| `internal/mls`: HPKE envelopes, Add/Remove proposals, binary tree key schedule | implemented + tested (TreeKEM-style, not RFC 9420) |
| Speaking events broadcast within a channel | implemented + tested |
| Opus encode/decode, SFU fanout, DAVE E2EE, jitter buffer | TBD |

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
| Index reset + full reindex workflow (`WAVEYCORD_SEARCH_REINDEX=reset`) | implemented + tested (tsvector and live OpenSearch) |
| Polls, components v2 validation | TBD |

## Invites and webhooks

| Route | Status |
| --- | --- |
| `GET /invites/{code}` | implemented (public) |
| `POST /invites/{code}` (accept) | implemented |
| `DELETE /invites/{code}` | implemented |
| Webhook create/list/get/update/delete | implemented |
| `POST /webhooks/{id}/{token}` (execute, `?wait=true`) | implemented + tested |
| `GET/PATCH/DELETE /webhooks/{id}/{token}` (token management) | implemented + tested |
| `POST /webhooks/{id}/{token}/slack` | implemented + tested |
| `POST /webhooks/{id}/{token}/github` | implemented + tested |
| `?thread_id` / `?thread_name` webhook execution (all variants) | implemented + tested |

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
| `GET/PUT /applications/{id}/role-connections/metadata` | implemented + tested |
| `GET/PUT /users/@me/applications/{id}/role-connection` | implemented + tested (scope-enforced) |
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
| `GET /applications/{app.id}/guilds/{guild.id}/commands/permissions` | implemented + tested |
| `GET/PUT .../commands/{command.id}/permissions` | implemented + tested |
| Command permission enforcement at interaction dispatch | implemented + tested |
| `verify_key` registration through `PATCH /applications/{id}` | implemented + tested |
| Interaction type 3 (message component) callback + payload validation | implemented + tested |
| Interaction type 5 (modal submit) callback + submitted-field parsing | implemented + tested |
| Outgoing message component validation (rows, buttons, selects) | implemented + tested |
| External interactions endpoint forwarding + outbound Ed25519 signature | implemented + tested |
| Endpoint callback relay, 502 on failure, URL validation | implemented + tested |

Signature verification covers `X-Signature-Timestamp + raw body` with the
application's 32-byte Ed25519 public key; timestamps outside five minutes are
rejected, as are missing, malformed and non-matching signatures. Guild commands
shadow global commands on invocation.

External endpoints: when `interactions_endpoint_url` is set the verified request is
forwarded unchanged, signed over `timestamp + body` with a per-application Ed25519
server key whose public half is returned as `signing_public_key`. A 2xx JSON
callback is relayed to the caller; transport errors, non-2xx responses and invalid
JSON become `502`. Without an endpoint, dispatch stays in-process. URLs must be
`https` (plain `http` is accepted for localhost).

Components: outgoing message components are validated on create, edit and webhook
execute — at most five action rows, 1-5 children per row, one select per row,
button style 1-5 with `custom_id` (or `url` for link buttons), string selects with
1-25 options, and no text inputs outside modals. Type-3 interactions require a
valid `custom_id` and interactive `component_type`; select values are bounded and
echoed in the in-process callback. Type-5 interactions require a modal `custom_id`
and action-row structure with text inputs or selects; submitted fields are parsed
and summarised. Both types forward unchanged when an endpoint is configured.

Role connections: an application registers up to five metadata records
(types 1-8, unique keys, bounded names/descriptions). A user's connection is stored
per `(user, application)` with platform name, username and metadata. Both GET and
PUT require an OAuth2 bearer token for that application carrying
`role_connections.write`; user sessions, bot tokens, missing scope, and tokens
issued for another application are rejected with `403`. Metadata keys are validated
against the registered registry.

Command permissions are stored per guild and command with role/user/channel
overrides (types 1/2/3), validated against guild membership, and returned in the
documented shape. They are enforced at dispatch: a user-specific overwrite wins
outright; a matching channel or role deny blocks; otherwise a matching allow grants
access; an allowlist with no match denies. With no overwrites,
`default_member_permissions` is checked against guild permissions. Guild owners and
administrators bypass overrides. Denials return an ephemeral type-4 callback
(`flags: 64`). Tested across owner, role-holder, explicit user deny, no-match,
channel deny and default-permission cases.

Execution accepts `?thread_id` (must be a thread whose parent is the webhook channel)
and `?thread_name` (forum/media channels only; a public thread is created and the
message posted there). Slack delivery maps `text` to message content; GitHub delivery summarises
`X-GitHub-Event` with action, repository and sender when present. Both support
`?wait=true`. Webhook tokens can read, rename and delete the webhook without a
session.

Deliberate adaptations: the interactions endpoint is global, so the payload must
carry `application_id` (Discord routes by per-application URL).

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
| Spam trigger (stateful per-user/channel window) | executed + tested |
| Member profile trigger (join and member update) | executed + tested |
| Actions: block message, send alert, timeout | executed + tested |
| Exempt roles / exempt channels | executed + tested |
| Keyword allow lists | executed |
| Mention raid trigger (guild-wide mention window) | executed + tested |

Blocked messages return `400` with code `200000` and are not persisted. Alerts are
posted by a system account into the configured channel; timeouts set
`communication_disabled_until` on the member.

SPAM is stateful: a sliding five-second window counts a user's messages per
channel, and the sixth message trips the rule. MEMBER_PROFILE rules scan the
username, display name and nickname both at join time (invite accept, invite on
registration) and on member updates, applying the configured timeout and alert
actions. MENTION_RAID aggregates mentions per guild across every channel and user in a
ten-second window; more than ten mentions trips the rule and runs the configured
block/alert/timeout actions. `@everyone` and `@here` count as one mention each.
Normal volume and other guilds' windows do not trip it. Counters are published
over the gateway bus, so a burst split across API instances still aggregates and
trips the rule; the same applies to the SPAM window. Discord's production raid
thresholds are unpublished, so the window and limit here are fixed and documented
rather than tuned.

## Onboarding and welcome screen

| Route | Status |
| --- | --- |
| `GET/PUT /guilds/{guild.id}/onboarding` | implemented + tested |
| `GET/PATCH /guilds/{guild.id}/welcome-screen` | implemented + tested |

Onboarding validates prompt titles/option bounds, guild-owned channels and roles,
default channel limits and mode (0/1); welcome screens bound the description to
140 characters, limit to five unique guild channels and require a description when
enabled. Both are stored and returned, and are now included in READY guild payloads
as `onboarding` and `welcome_screen` (tested).

## Guild templates

| Route | Status |
| --- | --- |
| `GET /guilds/templates/{code}` (public) | implemented + tested |
| `POST /guilds/templates/{code}` (create guild from template) | implemented + tested |
| `GET/POST /guilds/{guild.id}/templates` | implemented + tested |
| `PUT/PATCH/DELETE /guilds/{guild.id}/templates/{code}` | implemented + tested |

Templates snapshot the guild name, description and channels at creation time;
`PUT` re-snapshots, usage increments when a guild is created from a template, and
creating from a template copies supported channel types.

## Scheduled events

| Route | Status |
| --- | --- |
| `GET/POST /guilds/{guild.id}/scheduled-events` | implemented + tested |
| `GET/PATCH/DELETE /guilds/{guild.id}/scheduled-events/{event.id}` | implemented + tested |
| `GET .../scheduled-events/{event.id}/users` | implemented (always empty) |

Validated: ISO8601 start/end, entity types 1 (stage), 2 (voice) and 3 (external),
channel type must match the entity, external events require a location and end
time, privacy level only `GUILD_ONLY`. Status transitions 1-4 are accepted.
Event interest registration is not implemented, so the users list is empty.

## Stage instances

| Route | Status |
| --- | --- |
| `POST /stage-instances` | implemented + tested |
| `GET/PATCH/DELETE /stage-instances/{channel.id}` | implemented + tested |

Requires a stage channel and `MANAGE_CHANNELS`; topic is 1-120 characters; one
instance per channel; create/update/delete dispatch the stage events.

## Integrations

| Route | Status |
| --- | --- |
| `GET /guilds/{guild.id}/integrations` | implemented + tested |
| `GET/PATCH/DELETE /guilds/{guild.id}/integrations/{integration.id}` | implemented + tested |

A bot installed through the OAuth2 `bot` scope creates an integration row
(`type: 3`, account = bot user). PATCH accepts emoticon/expiry settings; DELETE
removes the row and kicks the bot from the guild. Twitch/YouTube integrations and
integration sync are not implemented.

## Message search

Search is a service boundary (`internal/search.Index`) with three backends:

- `pgIndex` — Postgres tsvector + GIN, write-through on message create/update/delete.
- `sqliteIndex` — substring matching for the embedded engine.
- `esIndex` — Elasticsearch/OpenSearch adapter. Messages are written to one index
  per guild (`<prefix>-guild-<guild_id>`), created with the configured shard and
  replica count. Writes go through a bounded bulk-indexing worker (batch/interval
  flush, `Flush` barrier); deletes resolve through an in-memory id→index map and
  fall back to a wildcard `_delete_by_query`. Queries are guild-scoped: the
  index name carries the guild, and channel/author filters are applied inside the
  request, after the caller's readable channels are resolved. `GET
  /guilds/{id}/messages/search` is unchanged.

Tested: guild-sharded indexing, ranked results (`_score` ordering), shard
isolation between guilds, channel and author filtering, and delete propagation.
Two tests exist: a protocol-double test against a fake Elasticsearch REST server,
and `TestESRealCluster`, which runs the same assertions against a live
Elasticsearch/OpenSearch node when `WAVEYCORD_TEST_ES_URL` is set. The real-cluster
run was executed against OpenSearch 2.19.0 and passed.

Remaining differences from Discord's published design:

- One cluster configured by URL list, not a multi-cluster "cell" architecture with
  per-cell routing and BFG (big-freaking-guild) reindexing.
- Inline bounded bulk worker, not a PubSub-backed guaranteed-delivery indexing
  queue with destination-grouped batching.
- No index-size automation or reindex-on-threshold workflow.
- Reindex is now built in: `Store.ReindexSearch` scans message rows in id order,
  optionally resets the index (`search.Resetter`), and re-upserts documents.
  `WAVEYCORD_SEARCH_REINDEX=reset` runs it in the background at startup. Tested by
  wiping the Postgres tsvector index and by resetting live OpenSearch indices and
  rebuilding them; searches return the original results afterwards.
- Still absent: multi-cluster cells, PubSub-backed guaranteed indexing, BFG
  reindex-on-threshold, and index-size automation. The reindex is a single bounded
  scan, not a distributed migration.

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
