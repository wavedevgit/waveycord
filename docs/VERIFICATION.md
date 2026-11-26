# Verification status

What is verified, and what cannot be verified. "1:1 with Discord's backend" is not
a checkable claim: Discord's internal services, storage engines, RPC boundaries and
deployment topology are not public. This file records that distinction per area.

## Verified by tests in this repository

Each row names the test(s) that produce the evidence. Run with `go test ./...`;
the PostgreSQL/OpenSearch rows additionally need `WAVEYCORD_TEST_DSN` and
`WAVEYCORD_TEST_ES_URL`.

| Area | Evidence |
| --- | --- |
| REST auth, users, guilds, channels, messages, reactions, pins, threads | `internal/api/*_test.go` |
| Permissions, role overwrites, owner/admin bypass | `TestInviteJoinAndPermissions`, `TestRolesMembersAndBans` |
| Gateway HELLO/IDENTIFY/READY/HEARTBEAT/RESUME, zstd-stream, passive deltas | `TestGatewayZstdStream`, `TestPassiveUpdateV2Delta` |
| Cross-instance bus dispatch, cluster auto-mod counters, speaking, E2EE rekey | `TestCluster*` |
| Postgres dialect, partitioning, migration, reindex | `TestPostgresMessagePartitioning`, `TestLegacyMessagesMigration`, `TestSearchReindexRebuildsIndex` |
| Load balancer consistent hashing + health checks | `internal/lb` tests |
| OAuth2 code flow, bot install, role connections | `TestOAuth2*`, `TestRoleConnections*` |
| Interactions, command permissions, components/modals, forwarding | `TestInteractions*`, `TestCommandPermission*` |
| Auto moderation keyword/regex/spam/profile/mention-raid | `TestAutoModeration*`, `TestAutoModSpamTrigger`, `TestAutoModMemberProfileTrigger` |
| Search: tsvector/GIN and live OpenSearch guild-sharded indices | `TestPostgresFullTextSearch`, `TestESAdapter*`, `TestESRealCluster` |
| Voice gateway, UDP discovery, RTP fanout, jitter/PLC, Opus mixing, AEAD, MLS-style epochs | `TestVoice*` |
| MLS path secrets + Welcome (removal and add) | `TestPathUpdateAndWelcome`, `TestVoiceMLSSignedRosterCommit` |
| MLS ciphersuite negotiation + proposal references | `TestCiphersuiteAndProposalRefs` |
| Cluster-wide Opus mixing across two instances | `TestClusterWideMixing` (`-tags opus`, cluster mix enabled) |
| REST extras: templates, events, stage, integrations, stickers, widget, webhooks | `TestGuildTemplates`, `TestScheduledEvents`, `TestGuildStickers`, `TestGuildWidget`, ... |
| Widget PNG, sticker packs, current-member route | `TestWidgetImageStickerPacksAndMember` |

## Not verifiable, therefore not claimed

| Claim | Why it cannot be checked |
| --- | --- |
| Byte/struct parity with Discord's internal databases | Schemas are private. This project uses its own Postgres schema and partitioning. |
| ScyllaDB/cluster topology parity | Discord's multi-cluster cells, token ring and repair behaviour are not public. |
| Exact gateway frame ordering/preferences | Public docs describe shapes, not production sequencing under load. |
| Exact rate-limit buckets | Discord's per-route limits are dynamic and undocumented; this server uses a fixed bucket. |
| Exact search relevance | Discord's analyzers, boosts and cell routing are private. |
| Exact DAVE/MLS implementation | The voice E2EE work here is TreeKEM-*style*, not RFC 9420, and Discord's DAVE details are only partially public. |
| Exact media pipeline | No Opus mixing/forwarding timing, jitter policy or SFU scheduling from Discord is published. |

## Test stability note

Cluster voice tests failed intermittently when a rekey/roster frame arrived while a
helper expected the next frame to be READY or SESSION_DESCRIPTION; the observed
failure at ~5 s matched the old fixed deadline on that wait. Every handshake helper
now waits for its specific opcode (2 for READY, 4 for SESSION_DESCRIPTION, 38 for a
commit) with a 20 s deadline. Two consecutive full PostgreSQL suites pass after the
change. Treat a single cluster-test failure as suspect until reproduced alone.

## Known functional gaps (not just unverifiable)

- No RFC 9420 MLS path secrets/Welcome/ciphersuite negotiation; rotation needs a
  member commit.
- Group-E2EE frames bypass jitter/PLC and mixing (server cannot decode them).
- Search is one ES cluster with bounded reindex, not multi-cell/PubSub/BFG.
- Voice endpoint advertises `/voice`, not a host.
- Some documented REST surfaces remain unregistered.

These are recorded in `docs/COMPATIBILITY.md` as gaps rather than described as
parity.
