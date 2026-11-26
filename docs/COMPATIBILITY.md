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
| MFA: TOTP enroll/disable, backup codes, login gate (`/auth/mfa/totp`) | implemented + tested |
| WebAuthn (ES256) credential register/list/rename/delete and assertion login | implemented + tested |
| Conditional (discoverable-credential) and passwordless WebAuthn sign-in | implemented + tested |
| Remote auth (QR) handshake: start/approve/cancel and single-use redemption | implemented + tested |
| SMS MFA: enrollment, login gate and redemption, disable | implemented + tested (code delivery is out of scope) |

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
| `GET /guilds/{guild.id}/roles/{role.id}` | implemented + tested |
| `GET /channels/{channel.id}/permissions/member/{user.id}` | implemented + tested (MANAGE_ROLES) |
| `GET /channels/{channel.id}/permissions/role/{role.id}` | implemented + tested (MANAGE_ROLES) |
| `PATCH/DELETE /guilds/{guild.id}/roles/{role.id}` | implemented |
| `GET /guilds/{guild.id}/bans` and `GET/PUT/DELETE .../bans/{user.id}` | implemented |
| `GET /guilds/{guild.id}/invites` | implemented |
| `GET /guilds/{guild.id}/emojis` and emoji CRUD | implemented |
| `GET/POST /guilds/{guild.id}/stickers`, `GET/PATCH/DELETE .../{sticker.id}` | implemented + tested |
| `GET /stickers/{sticker.id}` | implemented + tested |
| `GET/PATCH /guilds/{guild.id}/widget`, `GET /guilds/{guild.id}/widget.json` | implemented + tested |
| `GET /guilds/{guild.id}/widget.png` | implemented + tested (placeholder card) |
| `GET /sticker-packs` | implemented + tested (empty catalogue) |
| `GET /users/@me/guilds/{guild.id}/member` | implemented + tested |
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
| `GET /channels/{channel.id}/permissions/member/{user.id}`, `.../role/{role.id}` | implemented + tested |
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
| Reorder/dedupe/gap shedding for server-opaque group media by sender counter, local and cross-instance (no PLC possible) | implemented + tested |
| Epoch rekey signal on voice join/leave (op 36, local + cross-instance) | implemented + tested |
| Epoch rotation excluding departed members; stale-epoch rejection | implemented + tested |
| Joint epoch secret from per-member contributions (TreeKEM-style) | implemented + tested |
| Signed roster commit (op 38) + tree-derived epoch secret | implemented + tested |
| `internal/mls`: HPKE envelopes, Add/Remove proposals, binary tree key schedule | implemented + tested (TreeKEM-style, not RFC 9420) |
| Path secrets (per-divergence envelopes) + Welcome for added members | implemented + tested |
| Ciphersuite negotiation (X25519/HKDF-SHA256/AES-256-GCM) + proposal references | implemented + tested |
| RFC-style `ExpandWithLabel` path KDF, resolution-driven envelopes, per-application key schedule | implemented + tested |
| Signed Add/Remove proposal wire messages carried in the commit, signature and body validated on verify | implemented + tested |
| TreeKEM resolution with blank/padding subtrees; per-node public keys persisted across epochs and covered by the commit signature | implemented + tested |
| Node-addressed path envelopes: member node private keys persisted in epoch state, `DerivePathRootWithKeys` | implemented + tested (unit); leaf layout limitation below |
| Speaking events broadcast within a channel | implemented + tested |
| Opus encode/decode, SFU fanout, DAVE E2EE, jitter buffer | implemented + tested (`-tags opus` for codec paths) |

Group-E2EE media (`WCE2`) is never decrypted. Each sender's frames pass through a
reorder buffer keyed by the envelope counter: the first frame is held briefly,
out-of-order frames are emitted in counter order, a duplicate of an already emitted
counter is dropped, and a lost counter is released after the gap window instead of
stalling. Cross-instance frames get the same treatment on the receiving instance,
keyed by the ssrc in the envelope, so a hole does not stall a relayed stream. A shed
gap increments `waveycord_voice_e2ee_gaps_total` and a dropped duplicate increments
`waveycord_voice_e2ee_duplicates_total`. PLC is impossible on this path because the
payload is ciphertext the server cannot decode, so there is no concealment frame to
synthesise and none is fabricated.

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
| Multi-cell routing: guild hashed to one of N ES clusters, per-guild index inside it (`WAVEYCORD_ES_CELLS`) | implemented + tested (two fake clusters) |
| BFG reindex-on-threshold: drifted large guild reset and rebuilt alone (`WAVEYCORD_SEARCH_BFG_THRESHOLD`, `_DRIFT`, `_EVERY`) | implemented + tested |
| Durable indexing queue: message writes enqueued, worker drains with retry (`WAVEYCORD_SEARCH_QUEUE=1`) | implemented + tested |
| Postgres `LISTEN` wake-up on a dedicated connection; poll interval as fallback | implemented + tested |
| Polls: create on message send, vote, retract, expire, answer voters | implemented + tested |
| Poll viewer-relative `me_voted`; expiry sweep finalizes, freezes the tally and pushes per-viewer `MESSAGE_UPDATE` | implemented + tested |
| Components v2: container/section/text/media/file/separator validation under flag `1<<15` | implemented + tested |

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
| `PUT .../commands/permissions` (deprecated bulk replace) | implemented + tested |
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

Cell architecture: `WAVEYCORD_ES_CELLS` takes semicolon-separated cells, each a
comma-separated URL list (`cellA1,cellA2;cellB1`). A guild is hashed to one cell,
its index is created only there, and queries/deletes use that cell; bulk batches are
grouped per cell. Tested with two independent fake clusters: each guild's index exists
only in its cell, ranked guild-scoped queries read one cell, an unindexed guild
returns empty, deletes hit only the owning cell, and reset clears every cell.

Remaining differences from Discord's published design:

- Cells are configured statically; there is no per-cell autoscaling, routing table
  or BFG (big-freaking-guild) reindexing.
- Inline bounded bulk worker, not a PubSub-backed guaranteed-delivery indexing
  queue with destination-grouped batching.
- No index-size automation or reindex-on-threshold workflow.
- Reindex is now built in: `Store.ReindexSearch` scans message rows in id order,
  optionally resets the index (`search.Resetter`), and re-upserts documents.
  `WAVEYCORD_SEARCH_REINDEX=reset` runs it in the background at startup. Tested by
  wiping the Postgres tsvector index and by resetting live OpenSearch indices and
  rebuilding them; searches return the original results afterwards.
- BFG reindex-on-threshold is implemented: `search.BFGPolicy` compares a guild's
  authoritative row count with its index count and rebuilds that guild alone
  (per-guild index deletion, then a bounded re-index) when the drift exceeds the
  ratio and the guild is at or above the size threshold. It is opt-in through
  `WAVEYCORD_SEARCH_BFG_THRESHOLD` (plus `WAVEYCORD_SEARCH_BFG_DRIFT` and
  `WAVEYCORD_SEARCH_BFG_EVERY`), triggered in the background from message writes at
  most once per guild at a time, and can also be called directly through
  `Store.BFGMaintainGuild`. Tested for under-threshold skip, under- and over-indexed
  drift, idempotent second pass, per-guild ES count/reset isolation and the async
  write trigger.
- Durable indexing queue: with `WAVEYCORD_SEARCH_QUEUE=1`, message create/edit/delete
  append a row to `search_queue` instead of calling the index inline. Jobs collapse
  to one per message, a worker drains them in id order and deletes a row only after
  the index accepted it, and a failed call leaves the row with an incremented attempt
  count so indexing is at-least-once. Postgres also emits `pg_notify` on enqueue as a
  wake-up hint, but the durable table is the guarantee and the worker polls; the
  delete path uses the same queue. Tested on SQLite and on an isolated Postgres schema
  for inline-bypass, drain/ack, edit collapse, injected failure and retry, delete jobs
  and the background runner. On Postgres a dedicated `LISTEN waveycord_search`
  connection (its own `pgx` connection, never a pooled writer) wakes the worker
  immediately; the poll interval is the fallback and the durable row remains the
  guarantee, so a missed notification costs one poll.
- Still absent: index-size automation. The LISTEN connection is per instance and
  carries no payload, so a wake-up is coalesced and only tells the worker to poll the
  durable table; the BFG rebuild remains a bounded per-guild scan, not a distributed
  migration.

## Components v2

Sending flag `1<<15` switches message components to the v2 layout rules. With the
flag, `content`, `embeds`, `poll` and `sticker_ids` are rejected, and a component-only
message is allowed (the empty-message check treats v2 components as content).

Validated: at most 40 components counted recursively (children plus section
accessory); unique non-negative component `id`s; top level allows action row, section,
text display, media gallery, file, separator and container; containers reject nesting,
need at least one child, restrict children to those seven types and require
`accent_color` in `0x000000`-`0xFFFFFF`; sections need 1-3 text-display children and
a button or thumbnail accessory; text displays need non-empty content and the total
across the message is capped at 4000 characters; media galleries need 1-10 items with
`media.url`; file components need an `attachment://name` url; separator `spacing` is 1
or 2; thumbnails need `media.url` and descriptions cap at 1024 characters; buttons in
a section follow style rules (link needs a url, premium needs `sku_id`, others need
`custom_id` and a label).

Editing a v2 message rejects `content` and non-empty `embeds`, and accepts replacement
components validated under the same flag.

Not 1:1: component `id`s are accepted as sent and never auto-generated; `custom_id`
uniqueness across a message is not enforced; attachments referenced by file/media
components are not resolved or exposed, because the message attachment pipeline is
separate; legacy action-row rules still reject premium button style 6 outside a
section accessory.

## MFA (TOTP)

`POST /users/@me/mfa/totp/enable` takes `{secret, code, password}`, checks the
password, requires a valid base32 secret and a code within one 30-second step,
stores the secret, marks `mfa_enabled`, generates ten recovery codes and returns
`{token, recovery_codes}` with a fresh session. `POST /users/@me/mfa/totp/disable`
accepts a current TOTP code or an unused recovery code, clears the secret and
deletes the codes, returning the user object.

`GET /users/@me/mfa/codes` and `POST /users/@me/mfa/codes` require the password;
the GET lists `{code, used}` entries and the POST replaces the set with ten new
codes. `POST /auth/login` now returns `{mfa: true, totp: true, sms: false, ticket}`
without a token when MFA is enabled; `POST /auth/mfa/totp` takes
`{code, ticket, login, password}`, validates the unexpired ticket (5 minutes), a TOTP
or unused backup code, consumes the ticket and returns `{token}`. A wrong code does
not consume the ticket.

Not 1:1: WebAuthn, SMS and remote-auth (QR) factors are not implemented; recovery
codes are stored verbatim so the GET can list them, where Discord does not return
them again; there is no rate limiting on code attempts, and no email/phone
verification step. `mfa_enabled` is cleared on public user projections and exposed
only to the account holder.

## WebAuthn (security keys)

Registration is a two-step flow on `POST /users/@me/mfa/webauthn/credentials`: an
empty body returns `{ticket, challenge}`, and a second request with
`{name, ticket, credential}` verifies the attested authenticator data and stores the
credential. Verification checks the client-data type (`webauthn.create`), challenge,
configured origin, the RP id hash, the attested-credential flag, that the raw id
matches the attested credential id, and the ES256 COSE key (kty 2, alg -7, crv 1, on
curve). It returns the credential and ten `backup_codes`, and marks MFA enabled.
`GET` lists `{id, name, created_at, last_used}`, `PATCH` renames, `DELETE` removes;
deleting the last credential clears MFA when no TOTP secret exists.

Login advertises the factors: `POST /auth/login` returns `{mfa, totp, webauthn, sms,
ticket}`. `POST /auth/mfa/webauthn/start` takes that ticket and returns a fresh
`{ticket, challenge}`; `POST /auth/mfa/webauthn` takes `{ticket, credential}`,
verifies the assertion (client-data type `webauthn.get`, challenge, origin, RP id
hash, user-present flag, ES256 signature over authenticator data plus the
client-data hash), consumes the challenge, records the counter and returns `{token}`.
A challenge is single-use; a failed verification does not consume it.

Discoverable-credential and passwordless sign-in reuse the same assertion verifier:

- `POST /auth/conditional/start` (unauthenticated) returns `{ticket, challenge,
  timeout}` with no user binding; `POST /auth/conditional/finish` takes
  `{ticket, credential}`, resolves the raw credential id to its owner, verifies the
  assertion and returns `{token}`. Unknown credentials and bad signatures are
  rejected without consuming the ticket.
- `POST /auth/passwordless/start` requires a session and a registered credential
  (`403` otherwise); it issues a user-bound challenge. `POST
  /auth/passwordless/finish` accepts only a challenge of the passwordless kind for
  the same user, verifying the assertion against that user's credential.

Not 1:1: only the ES256 algorithm is supported; the attestation statement is not
cryptographically verified (`fmt` is read but `attStmt` is ignored), only two
formats are accepted, and the assertion counter is stored without enforcing a
strictly increasing value. Remote-auth/QR and SMS are not implemented. The login
challenge route is an extension: the client route table exposes only
`/auth/mfa/:param`. The request bodies for the conditional/passwordless finish routes
were not confirmed against a client build; only the start paths appear in the client
route table.

## SMS MFA

`POST /users/@me/mfa/sms/send {phone}` issues a six-digit enrollment code (5 minute
TTL, phone must be 6-20 digits with an optional leading `+`), and
`POST /users/@me/mfa/sms/enable {phone, code}` consumes it, stores the phone in
`sms_mfa`, enables the factor, and returns ten `recovery_codes`.
`POST /users/@me/mfa/sms/disable` requires the account password and clears the
factor, deleting outstanding codes; the account's `mfa_enabled` flag is recomputed,
so disabling the last factor stops the login gate.

Login returns `{mfa, totp, webauthn, sms, ticket}`. When SMS is the only factor the
login code is issued with the ticket; `POST /auth/mfa/sms/send {ticket}` resends it.
`POST /auth/mfa/sms {ticket, code}` verifies the code, consumes the login ticket and
returns `{token}`. Codes are single use — a superseded or reused code is rejected,
and expired codes are rejected and pruned on read.

Not 1:1: no SMS is actually delivered; codes are stored verbatim in
`sms_mfa_codes` so tests and operators can read them, where a production service
would store a hash and hand the plaintext only to the carrier. There is no phone
number verification against the user record, no per-attempt rate limit, and the
disable request body (password) was chosen here rather than confirmed against a
client build.

## Remote auth (QR)

The signed-in client registers the fingerprint shown by the QR device with
`POST /users/@me/remote-auth {fingerprint}` (32 hex-encoded bytes) and receives
`{ticket, expires_at}`; handshakes live two minutes in `remote_auth_sessions`.
`POST /users/@me/remote-auth/finish {ticket}` approves it (owner only, pending only),
`POST /users/@me/remote-auth/cancel {ticket}` cancels it (owner only), and the
unauthenticated `POST /users/@me/remote-auth/login {ticket, fingerprint}` redeems an
approved handshake exactly once, returning `{token}` for the approving account. A
wrong fingerprint cannot redeem, a redeemed ticket is deleted, and expired
handshakes are refused on read and pruned by the periodic sweep alongside polls.

Not 1:1: there is no QR websocket gateway and no key exchange, so the fingerprint is
a claimed identifier rather than a cryptographic binding and the redeemed token is
returned in plaintext to the redeemer instead of encrypted to the QR device's public
key; the client route table also exposes `/users/@me/remote-auth/login`, which is
implemented here, but the real transport delivers the token over the websocket
instead of this HTTP response. SMS MFA is not implemented.

## Polls

A message created with a `poll` object becomes type 32. The request is validated
against the documented limits: question 1-300 characters, 1-10 answers of 1-55
characters each with an optional emoji id or name, `duration` 1-768 hours
(default 24, stored as an ISO8601 `expiry`), `layout_type` 1 only, and
`allow_multiselect` defaulting to false. Poll rows live in `polls`, per-voter
selections in `poll_votes`.

Reading a message attaches `poll` with `results.answer_counts` (one entry per answer
that has a vote) and `is_finalized`. Expiry is enforced lazily on read and on vote, so
an expired poll reports finalized and rejects new votes without a scheduler. Votes are
set with `PUT /channels/{id}/polls/{message_id}/answers/@me` and `{answer_ids}` — the
route the client uses, undocumented in the reference — an empty list retracts, a
single-select poll rejects more than one answer, and unknown answer ids are rejected.
`GET .../answers/{answer_id}` lists voters with `after`/`limit`, and
`POST .../expire` returns the updated message and is restricted to the poll author.
Each vote and expiry dispatches `MESSAGE_UPDATE`.

`me_voted` is viewer-relative: message reads (`GET /channels/{id}/messages`,
`GET .../messages/{id}`, pinned messages, and poll responses) hydrate poll results
against the requesting user. A poll that passes its expiry is finalized lazily on
read, and a background sweep (`StartPollSweeper`, every 30 s in `main`) finalizes
expired polls in a batch and dispatches `MESSAGE_UPDATE` for each, so clients see the
finalized state without another request. The sweep is idempotent: a poll already
finalized by a read is not returned again.

Poll updates are pushed per recipient rather than broadcast: guild fanout resolves the
member list and serializes the message for each user id, and DM fanout does the same
for recipients and the author, so `me_voted` is correct in the pushed
`MESSAGE_UPDATE` as well as in HTTP reads. Each dispatch is a `DispatchUser`, which
also crosses the cluster bus. Guild fanout is therefore O(members) per poll update,
which is accepted because poll updates are low-frequency; when the member lookup
fails the path falls back to a single guild broadcast with viewer-free results.

Final tally: when a poll is finalized — by the sweep, by the author's expire call, or
lazily on the first read after expiry — the exact per-answer counts are computed from
`poll_votes` once and stored in `polls.final_counts`. A finalized poll serves that
snapshot rather than recounting, so the tally cannot move after the fact; the tests
delete the vote rows after finalization to prove the snapshot is authoritative.
Votes are rejected once finalized, so the snapshot cannot disagree with accepted
votes.

Not 1:1: counts are computed synchronously at finalization instead of by a separate
asynchronous tally worker, so there is no window where a poll is ended but not yet
finalized; a finalized tally also remains visible through reads indefinitely rather
than Discord's approximate-live/accurate-finalized split during the poll.

## Permission inspection routes

`GET /channels/{id}/permissions/member/{user.id}` returns the target member's
effective channel permissions (role bits, @everyone overwrite, role overwrites,
member overwrite, administrator short-circuit) and requires `MANAGE_ROLES`.
`GET /channels/{id}/permissions/role/{role.id}` resolves a role's bits through the
@everyone and role overwrites, ignoring member-specific entries. `GET
/guilds/{id}/roles/{role.id}` returns a single role. All three are tested, including
403 for non-managers, 404 for unknown roles/non-members and the administrator path.

## Stickers and widget

Guild stickers support create/list/get/patch/delete with name 2-30, tags 1-200,
description ≤100 and a 20-sticker cap; global `GET /stickers/{id}` is registered.
Guild widget settings validate the channel belongs to the guild; the public
`widget.json` returns guild name, channels and an empty member list, and is refused
with `403` when the widget is disabled. Sticker image data is accepted but not decoded;
`widget.png` returns a real PNG (320x80, dark card with accent strip and decorative
presence dots) but renders no text, because the server ships no font. The sticker
pack catalogue is an empty list; `GET /users/@me/guilds/{id}/member` returns the
caller's member object.

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
