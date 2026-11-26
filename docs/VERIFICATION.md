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
| Member/role channel-permission inspection and single-role fetch | `TestChannelPermissionRoutesAndRoleFetch` |
| Gateway HELLO/IDENTIFY/READY/HEARTBEAT/RESUME, zstd-stream, passive deltas | `TestGatewayZstdStream`, `TestPassiveUpdateV2Delta` |
| Cross-instance bus dispatch, cluster auto-mod counters, speaking, E2EE rekey | `TestCluster*` |
| Postgres dialect, partitioning, migration, reindex | `TestPostgresMessagePartitioning`, `TestLegacyMessagesMigration`, `TestSearchReindexRebuildsIndex` |
| Load balancer consistent hashing + health checks | `internal/lb` tests |
| OAuth2 code flow, bot install, role connections | `TestOAuth2*`, `TestRoleConnections*` |
| Interactions, command permissions, components/modals, forwarding | `TestInteractions*`, `TestCommandPermission*` |
| Auto moderation keyword/regex/spam/profile/mention-raid | `TestAutoModeration*`, `TestAutoModSpamTrigger`, `TestAutoModMemberProfileTrigger` |
| Search: tsvector/GIN and live OpenSearch guild-sharded indices | `TestPostgresFullTextSearch`, `TestESAdapter*`, `TestESRealCluster` |
| Multi-cell search routing and cell isolation | `TestMultiCellRouting` (two fake clusters) |
| BFG drift policy, per-guild ES count/reset, background rebuild trigger | `TestBFGPolicy`, `TestESGuildCountAndReset`, `TestBFGMaintainGuildRebuildsDriftedIndex`, `TestBFGWriteTrigger` |
| Index-size automation: MaxDocs crossing, growth-triggered rebuild, small guild inert | `TestBFGSizePolicy`, `TestBFGSizeAutomationRebuildsGrowingGuild` |
| Resumable search migration checkpoint and idempotent rerun | `TestSearchMigrationCheckpoint` |
| BFG baseline persisted across store reopen and shared via the database | `TestBFGSizeStatePersistsAcrossReopen` |
| Durable search queue: enqueue-only writes, drain/ack, retry, deletes, runner (SQLite + Postgres) | `TestDurableSearchQueue`, `TestDurableSearchQueuePostgres` |
| LISTEN wake-up (dedicated pgx connection), immediate drain, SQLite rejection; per-job acknowledgement check | `TestSearchQueueListenWakes`, `TestListenSearchQueueRejectsSQLite` |
| Voice gateway, UDP discovery, RTP fanout, jitter/PLC, Opus mixing, AEAD, MLS-style epochs | `TestVoice*` |
| Host-only voice endpoint advertised; root upgrade path connects | `TestVoiceHostOnlyEndpoint` |
| Opaque group-media reorder, dedupe and gap release (local) | `TestVoiceOpaqueJitter` |
| Opaque group-media loss handling across two instances, gap metrics, no mixing | `TestClusterOpaqueGapShedding` |
| MLS path secrets + Welcome (removal and add) | `TestPathUpdateAndWelcome`, `TestVoiceMLSSignedRosterCommit` |
| MLS ciphersuite negotiation + proposal references | `TestCiphersuiteAndProposalRefs`, `TestCiphersuiteNegotiation`, `TestProposalSuiteNegotiation` |
| RFC-style path KDF, application key schedule, nonce ratchet | `TestExpandWithLabelAndApplicationKeys`, `TestVoiceMLSSignedRosterCommit` |
| Signed proposal wire messages in commits, verify rejection paths | `TestCiphersuiteAndProposalRefs`, `TestVoiceMLSSignedRosterCommit` |
| Per-proposal reference derivation and commit reference binding, forged/duplicate/reordered rejection | `TestProposalReferenceBinding` |
| RFC-shaped commit authentication: transcript chain over commit signature and authenticated content | `TestCommitContextChaining`, `TestVoiceMLSSignedRosterCommit` |
| LeafNode/KeyPackage signatures, TLS round trip, credential-driven membership, package-less Add rejection, op 38 KeyPackage add | `TestKeyPackageLeaf`, `TestVoiceMLSSignedRosterCommit` |
| External proposal references resolved from relayed proposals, missing/mismatched rejection | `TestExternalProposalReference`, `TestVoiceMLSCommitServerValidation` |
| Proposal persistence across store reopen, channel scoping, idempotent save, referenced commit verifies after reload | `TestMLSProposalSurvivesReopen` |
| Independent proposal authors: member-authored acceptance, forged signature and outsider rejection | `TestIndependentProposalAuthors`, `TestVoiceMLSSignedRosterCommit` |
| Server rejects invalid op 38 commits before relaying, closes sender; valid commit relays | `TestVoiceMLSCommitServerValidation`, `TestVoiceMLSSignedRosterCommit` |
| Credential directory persistence/upsert and validation against it | `TestMLSCredentialDirectory` |
| First commit roster must be in voice and registered (no TOFU) | `TestVoiceMLSCommitRejectsUnknownCredential` |
| Two-suite negotiation through op 38 (AES-128 media), server rejection of unsupported and silent suite changes | `TestVoiceMLSSignedRosterCommit`, `TestVoiceMLSCommitRejectsUnsupportedSuite`, `TestVoiceMLSCommitRejectsSilentSuiteChange` |
| TreeKEM resolution with blanks/padding, persisted tree keys across epochs, add/remove with carried tree | `TestResolutionWithBlanks`, `TestTreePublicsPersistAcrossEpochs`, `TestVoiceMLSSignedRosterCommit` |
| Node-addressed envelope decryption via persisted node keys, non-member rejection, newcomer without node keys | `TestNodeAddressedEnvelope` |
| Stable leaf slots across removal, node-addressed envelope on the op 38 wire, removed-slot exclusion | `TestStableLeafSlotsAcrossRemoval`, `TestVoiceMLSSignedRosterCommit` |
| Removed-member branch rekey: fresh node key delivered to retained members, stale key useless, empty branch dropped | `TestRemovedMemberBranchExcluded`, `TestVoiceMLSSignedRosterCommit` |
| Welcome-delivered node keys, newcomer opens a node-addressed envelope, rejection without keys | `TestWelcomeDeliversNodeKeys`, `TestVoiceMLSSignedRosterCommit` |
| Ancestor-chain root derivation for newcomers (ancestor below root, no root secret) | `TestNewcomerAncestorChainDerivation` |
| Encrypted group-info Welcome framing, no plaintext root, tamper/replay rejection | `TestWelcomeGroupInfoFraming`, `TestVoiceMLSSignedRosterCommit` |
| RFC-shaped GroupInfo: round trip, tree-hash and confirmation-tag verification, strict decode rejection | `TestGroupInfoTLSRoundTrip`, `TestVoiceMLSSignedRosterCommit` |
| MLSMessage framing round trip, version/format/truncation rejection, two-instance relay | `TestMLSMessageRoundTrip`, `TestVoiceMLSSignedRosterCommit` |
| Cluster-wide Opus mixing across two instances | `TestClusterWideMixing` (`-tags opus`, cluster mix enabled) |
| REST extras: templates, events, stage, integrations, stickers, widget, webhooks | `TestGuildTemplates`, `TestScheduledEvents`, `TestGuildStickers`, `TestGuildWidget`, ... |
| Widget PNG, sticker packs, current-member route | `TestWidgetImageStickerPacksAndMember` |
| Developer portal: cookie login, app/command/permission pages, settings (redirect URIs, endpoint), client secret, token reset, ownership, logout, CSRF rejection | `TestDeveloperPortal` |
| Portal cookie attributes: HttpOnly/SameSite, Secure on TLS and when forced | `TestPortalCookieSecurity` |
| Polls: create, vote, retract, multiselect, expire, voters, validation, viewer `me_voted`, expiry sweep + per-viewer `MESSAGE_UPDATE`, persisted final tally | `TestPollsCreateVoteExpireAndVoters` |
| Components v2 validation (flag, nesting, counts, text, media, edit rules) | `TestComponentsV2Validation`, `TestV2ValidationStrings` |
| MFA TOTP enrollment, login gate, backup codes, disable | `TestTOTPEnrollmentLoginAndBackupCodes` |
| WebAuthn ES256 registration, assertion login, rename/delete, single-use challenge | `TestWebAuthnRegistrationAndLogin` (software authenticator) |
| Conditional and passwordless WebAuthn start/finish, cross-user rejection | `TestConditionalAndPasswordlessWebAuthn` |
| Remote auth start/approve/cancel/redeem, ownership, expiry, single use | `TestRemoteAuthHandoff` |
| SMS MFA send/enable/login/resend/disable, wrong/expired/reused codes, last-factor flag | `TestSMSMFAEnrollmentLoginAndDisable` |

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
- Group-E2EE frames bypass PLC and mixing (server cannot decode them); they do get
  per-sender reorder/dedupe from the envelope counter, but frames relayed from other
  instances are delivered in bus order.
- Search is one ES cluster with bounded reindex, not multi-cell/PubSub/BFG.
- Some documented REST surfaces remain unregistered.

These are recorded in `docs/COMPATIBILITY.md` as gaps rather than described as
parity.
