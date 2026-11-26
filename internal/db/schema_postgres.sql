-- waveycord Postgres schema. Object model mirrors the official API docs.
-- Boolean-ish flags are SMALLINT 0/1 so the same statements serve SQLite and
-- Postgres without per-dialect argument conversion in handlers.

CREATE TABLE IF NOT EXISTS users (
    id                 TEXT PRIMARY KEY,
    username           TEXT NOT NULL,
    discriminator      TEXT NOT NULL,
    global_name        TEXT,
    avatar             TEXT,
    banner             TEXT,
    accent_color       INTEGER,
    email              TEXT UNIQUE,
    phone              TEXT UNIQUE,
    password_hash      TEXT NOT NULL,
    mfa_enabled        SMALLINT NOT NULL DEFAULT 0,
    verified           SMALLINT NOT NULL DEFAULT 0,
    bot                SMALLINT NOT NULL DEFAULT 0,
    system             SMALLINT NOT NULL DEFAULT 0,
    flags              BIGINT NOT NULL DEFAULT 0,
    public_flags       BIGINT NOT NULL DEFAULT 0,
    premium_type       INTEGER NOT NULL DEFAULT 0,
    locale             TEXT NOT NULL DEFAULT 'en-US',
    bio                TEXT NOT NULL DEFAULT '',
    avatar_decoration  TEXT,
    mfa_secret         TEXT,
    created_at         BIGINT NOT NULL
);

CREATE TABLE IF NOT EXISTS mfa_backup_codes (
    user_id TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    code    TEXT NOT NULL,
    used    SMALLINT NOT NULL DEFAULT 0,
    PRIMARY KEY (user_id, code)
);

CREATE TABLE IF NOT EXISTS webauthn_credentials (
    id            TEXT PRIMARY KEY,
    user_id       TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    credential_id TEXT NOT NULL UNIQUE,
    public_key    BYTEA NOT NULL,
    sign_count    BIGINT NOT NULL DEFAULT 0,
    name          TEXT NOT NULL,
    created_at    BIGINT NOT NULL,
    last_used     BIGINT
);

CREATE TABLE IF NOT EXISTS webauthn_challenges (
    ticket    TEXT PRIMARY KEY,
    user_id   TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    challenge TEXT NOT NULL,
    kind      TEXT NOT NULL,
    expires   BIGINT NOT NULL
);

CREATE TABLE IF NOT EXISTS webauthn_login_challenges (
    ticket    TEXT PRIMARY KEY,
    challenge TEXT NOT NULL,
    kind      TEXT NOT NULL,
    expires   BIGINT NOT NULL
);

CREATE TABLE IF NOT EXISTS sms_mfa (
    user_id    TEXT PRIMARY KEY REFERENCES users(id) ON DELETE CASCADE,
    phone      TEXT NOT NULL,
    enabled    SMALLINT NOT NULL DEFAULT 0,
    created_at BIGINT NOT NULL
);

CREATE TABLE IF NOT EXISTS sms_mfa_codes (
    user_id TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    purpose TEXT NOT NULL,
    code    TEXT NOT NULL,
    expires BIGINT NOT NULL,
    PRIMARY KEY (user_id, purpose)
);

CREATE TABLE IF NOT EXISTS remote_auth_sessions (
    ticket      TEXT PRIMARY KEY,
    user_id     TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    fingerprint TEXT NOT NULL,
    status      TEXT NOT NULL,
    created_at  BIGINT NOT NULL,
    expires     BIGINT NOT NULL
);
CREATE INDEX IF NOT EXISTS remote_auth_status ON remote_auth_sessions (status, expires);

CREATE TABLE IF NOT EXISTS mfa_tickets (
    id      TEXT PRIMARY KEY,
    user_id TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    expires BIGINT NOT NULL
);

CREATE INDEX IF NOT EXISTS users_username_discriminator
    ON users (lower(username), discriminator);

CREATE TABLE IF NOT EXISTS sessions (
    id           TEXT PRIMARY KEY,
    user_id      TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    token_hash   TEXT NOT NULL UNIQUE,
    client_info  TEXT NOT NULL DEFAULT '',
    created_at   BIGINT NOT NULL,
    last_used_at BIGINT NOT NULL
);
CREATE INDEX IF NOT EXISTS sessions_user ON sessions (user_id);

CREATE TABLE IF NOT EXISTS guilds (
    id                            TEXT PRIMARY KEY,
    name                          TEXT NOT NULL,
    icon                          TEXT,
    splash                        TEXT,
    discovery_splash              TEXT,
    banner                        TEXT,
    owner_id                      TEXT NOT NULL REFERENCES users(id),
    region                        TEXT NOT NULL DEFAULT 'deprecated',
    afk_channel_id                TEXT,
    afk_timeout                   INTEGER NOT NULL DEFAULT 300,
    widget_enabled                SMALLINT NOT NULL DEFAULT 0,
    verification_level            INTEGER NOT NULL DEFAULT 0,
    default_message_notifications INTEGER NOT NULL DEFAULT 0,
    explicit_content_filter       INTEGER NOT NULL DEFAULT 0,
    mfa_level                     INTEGER NOT NULL DEFAULT 0,
    system_channel_id             TEXT,
    system_channel_flags          INTEGER NOT NULL DEFAULT 0,
    rules_channel_id              TEXT,
    features                      TEXT NOT NULL DEFAULT '[]',
    preferred_locale              TEXT NOT NULL DEFAULT 'en-US',
    description                   TEXT,
    vanity_url_code               TEXT,
    premium_tier                  INTEGER NOT NULL DEFAULT 0,
    premium_subscription_count    INTEGER NOT NULL DEFAULT 0,
    nsfw_level                    INTEGER NOT NULL DEFAULT 0,
    created_at                    BIGINT NOT NULL
);

CREATE TABLE IF NOT EXISTS roles (
    id            TEXT PRIMARY KEY,
    guild_id      TEXT NOT NULL REFERENCES guilds(id) ON DELETE CASCADE,
    name          TEXT NOT NULL,
    color         INTEGER NOT NULL DEFAULT 0,
    hoist         SMALLINT NOT NULL DEFAULT 0,
    position      INTEGER NOT NULL DEFAULT 0,
    permissions   TEXT NOT NULL DEFAULT '0',
    managed       SMALLINT NOT NULL DEFAULT 0,
    mentionable   SMALLINT NOT NULL DEFAULT 0,
    icon          TEXT,
    unicode_emoji TEXT,
    flags         BIGINT NOT NULL DEFAULT 0
);
CREATE INDEX IF NOT EXISTS roles_guild ON roles (guild_id, position);

CREATE TABLE IF NOT EXISTS guild_members (
    guild_id                     TEXT NOT NULL REFERENCES guilds(id) ON DELETE CASCADE,
    user_id                      TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    nick                         TEXT,
    joined_at                    BIGINT NOT NULL,
    premium_since                BIGINT,
    deaf                         SMALLINT NOT NULL DEFAULT 0,
    mute                         SMALLINT NOT NULL DEFAULT 0,
    pending                      SMALLINT NOT NULL DEFAULT 0,
    communication_disabled_until BIGINT,
    avatar                       TEXT,
    banner                       TEXT,
    role_ids                     TEXT NOT NULL DEFAULT '[]',
    PRIMARY KEY (guild_id, user_id)
);
CREATE INDEX IF NOT EXISTS members_user ON guild_members (user_id);

CREATE TABLE IF NOT EXISTS channels (
    id                     TEXT PRIMARY KEY,
    type                   INTEGER NOT NULL,
    guild_id               TEXT REFERENCES guilds(id) ON DELETE CASCADE,
    position               INTEGER NOT NULL DEFAULT 0,
    name                   TEXT,
    topic                  TEXT,
    nsfw                   SMALLINT NOT NULL DEFAULT 0,
    last_message_id        TEXT,
    bitrate                INTEGER,
    user_limit             INTEGER,
    rate_limit_per_user    INTEGER NOT NULL DEFAULT 0,
    parent_id              TEXT,
    owner_id               TEXT,
    last_pin_timestamp     BIGINT,
    rtc_region             TEXT,
    video_quality_mode     INTEGER NOT NULL DEFAULT 1,
    message_count          INTEGER NOT NULL DEFAULT 0,
    member_count           INTEGER NOT NULL DEFAULT 0,
    thread_metadata        TEXT,
    archived               SMALLINT NOT NULL DEFAULT 0,
    message_retention      TEXT,
    icon                   TEXT,
    flags                  INTEGER NOT NULL DEFAULT 0,
    available_tags         TEXT,
    default_reaction_emoji TEXT,
    default_sort_order     TEXT,
    default_forum_layout   INTEGER NOT NULL DEFAULT 0,
    template               TEXT,
    created_at             BIGINT NOT NULL
);
CREATE INDEX IF NOT EXISTS channels_guild ON channels (guild_id, position);

CREATE TABLE IF NOT EXISTS permission_overwrites (
    channel_id TEXT NOT NULL REFERENCES channels(id) ON DELETE CASCADE,
    target_id  TEXT NOT NULL,
    type       INTEGER NOT NULL,
    allow      TEXT NOT NULL DEFAULT '0',
    deny       TEXT NOT NULL DEFAULT '0',
    PRIMARY KEY (channel_id, target_id)
);

-- Messages follow the published storage design: partition by channel, then by a
-- time bucket. HASH(channel_id) gives one partition per channel group; each hash
-- partition is RANGE(bucket) with one child per month, so a channel page reads a
-- single partition pair and old months can be dropped without a rewrite.
CREATE TABLE IF NOT EXISTS messages (
    id                    TEXT NOT NULL,
    channel_id            TEXT NOT NULL REFERENCES channels(id) ON DELETE CASCADE,
    guild_id              TEXT,
    author_id             TEXT NOT NULL REFERENCES users(id),
    content               TEXT NOT NULL DEFAULT '',
    timestamp             BIGINT NOT NULL,
    edited_timestamp      BIGINT,
    tts                   SMALLINT NOT NULL DEFAULT 0,
    mention_everyone      SMALLINT NOT NULL DEFAULT 0,
    mentions              TEXT NOT NULL DEFAULT '[]',
    mention_roles         TEXT NOT NULL DEFAULT '[]',
    mention_channels      TEXT NOT NULL DEFAULT '[]',
    pinned                SMALLINT NOT NULL DEFAULT 0,
    type                  INTEGER NOT NULL DEFAULT 0,
    flags                 INTEGER NOT NULL DEFAULT 0,
    referenced_message_id TEXT,
    webhook_id            TEXT,
    application_id        TEXT,
    components            TEXT NOT NULL DEFAULT '[]',
    attachments           TEXT NOT NULL DEFAULT '[]',
    embeds                TEXT NOT NULL DEFAULT '[]',
    sticker_items         TEXT NOT NULL DEFAULT '[]',
    nonce                 TEXT,
    bucket                BIGINT NOT NULL,
    PRIMARY KEY (channel_id, bucket, id)
) PARTITION BY HASH (channel_id);

-- Declarative hash children. Month children are created by db.EnsureMessagePartitions.
CREATE TABLE IF NOT EXISTS messages_h0 PARTITION OF messages FOR VALUES WITH (MODULUS 8, REMAINDER 0) PARTITION BY RANGE (bucket);
CREATE TABLE IF NOT EXISTS messages_h1 PARTITION OF messages FOR VALUES WITH (MODULUS 8, REMAINDER 1) PARTITION BY RANGE (bucket);
CREATE TABLE IF NOT EXISTS messages_h2 PARTITION OF messages FOR VALUES WITH (MODULUS 8, REMAINDER 2) PARTITION BY RANGE (bucket);
CREATE TABLE IF NOT EXISTS messages_h3 PARTITION OF messages FOR VALUES WITH (MODULUS 8, REMAINDER 3) PARTITION BY RANGE (bucket);
CREATE TABLE IF NOT EXISTS messages_h4 PARTITION OF messages FOR VALUES WITH (MODULUS 8, REMAINDER 4) PARTITION BY RANGE (bucket);
CREATE TABLE IF NOT EXISTS messages_h5 PARTITION OF messages FOR VALUES WITH (MODULUS 8, REMAINDER 5) PARTITION BY RANGE (bucket);
CREATE TABLE IF NOT EXISTS messages_h6 PARTITION OF messages FOR VALUES WITH (MODULUS 8, REMAINDER 6) PARTITION BY RANGE (bucket);
CREATE TABLE IF NOT EXISTS messages_h7 PARTITION OF messages FOR VALUES WITH (MODULUS 8, REMAINDER 7) PARTITION BY RANGE (bucket);

CREATE TABLE IF NOT EXISTS messages_h0_default PARTITION OF messages_h0 DEFAULT;
CREATE TABLE IF NOT EXISTS messages_h1_default PARTITION OF messages_h1 DEFAULT;
CREATE TABLE IF NOT EXISTS messages_h2_default PARTITION OF messages_h2 DEFAULT;
CREATE TABLE IF NOT EXISTS messages_h3_default PARTITION OF messages_h3 DEFAULT;
CREATE TABLE IF NOT EXISTS messages_h4_default PARTITION OF messages_h4 DEFAULT;
CREATE TABLE IF NOT EXISTS messages_h5_default PARTITION OF messages_h5 DEFAULT;
CREATE TABLE IF NOT EXISTS messages_h6_default PARTITION OF messages_h6 DEFAULT;
CREATE TABLE IF NOT EXISTS messages_h7_default PARTITION OF messages_h7 DEFAULT;

CREATE INDEX IF NOT EXISTS messages_channel ON messages (channel_id, id DESC);
CREATE INDEX IF NOT EXISTS messages_guild ON messages (guild_id, id DESC);

-- Direct id -> channel lookup, the analogue of Discord's separate message index.
CREATE TABLE IF NOT EXISTS message_index (
    id         TEXT PRIMARY KEY,
    channel_id TEXT NOT NULL REFERENCES channels(id) ON DELETE CASCADE,
    bucket     BIGINT NOT NULL,
    created_at BIGINT NOT NULL
);
CREATE INDEX IF NOT EXISTS message_index_channel ON message_index (channel_id);

CREATE TABLE IF NOT EXISTS polls (
    -- No FK to messages: the partitioned table's primary key is (channel_id, bucket, id).
    -- Store.DeleteMessage deletes the poll row explicitly.
    message_id        TEXT PRIMARY KEY,
    channel_id        TEXT NOT NULL,
    question          TEXT NOT NULL,
    answers           TEXT NOT NULL DEFAULT '[]',
    expiry            BIGINT NOT NULL,
    allow_multiselect SMALLINT NOT NULL DEFAULT 0,
    layout_type       INTEGER NOT NULL DEFAULT 1,
    finalized         SMALLINT NOT NULL DEFAULT 0,
    created_at        BIGINT NOT NULL
);

CREATE TABLE IF NOT EXISTS poll_votes (
    message_id TEXT NOT NULL REFERENCES polls(message_id) ON DELETE CASCADE,
    answer_id  INTEGER NOT NULL,
    user_id    TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    created_at BIGINT NOT NULL,
    PRIMARY KEY (message_id, answer_id, user_id)
);
CREATE INDEX IF NOT EXISTS poll_votes_answer ON poll_votes (message_id, answer_id, user_id);

CREATE TABLE IF NOT EXISTS message_reactions (
    message_id TEXT NOT NULL,
    channel_id TEXT NOT NULL REFERENCES channels(id) ON DELETE CASCADE,
    user_id    TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    emoji_key  TEXT NOT NULL,
    emoji_name TEXT NOT NULL,
    emoji_id   TEXT,
    animated   SMALLINT NOT NULL DEFAULT 0,
    created_at BIGINT NOT NULL,
    PRIMARY KEY (message_id, emoji_key, user_id)
);

CREATE TABLE IF NOT EXISTS invites (
    code                  TEXT PRIMARY KEY,
    guild_id              TEXT REFERENCES guilds(id) ON DELETE CASCADE,
    channel_id            TEXT NOT NULL REFERENCES channels(id) ON DELETE CASCADE,
    inviter_id            TEXT NOT NULL REFERENCES users(id),
    uses                  INTEGER NOT NULL DEFAULT 0,
    max_uses              INTEGER NOT NULL DEFAULT 0,
    max_age               INTEGER NOT NULL DEFAULT 86400,
    temporary             SMALLINT NOT NULL DEFAULT 0,
    unique_flag           SMALLINT NOT NULL DEFAULT 0,
    target_type           INTEGER,
    target_user_id        TEXT,
    target_application_id TEXT,
    created_at            BIGINT NOT NULL
);

CREATE TABLE IF NOT EXISTS bans (
    guild_id  TEXT NOT NULL REFERENCES guilds(id) ON DELETE CASCADE,
    user_id   TEXT NOT NULL,
    reason    TEXT,
    banned_at BIGINT NOT NULL,
    PRIMARY KEY (guild_id, user_id)
);

CREATE TABLE IF NOT EXISTS relationships (
    user_id   TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    target_id TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    type      INTEGER NOT NULL,
    nickname  TEXT,
    since     BIGINT,
    PRIMARY KEY (user_id, target_id)
);

CREATE TABLE IF NOT EXISTS read_states (
    user_id            TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    channel_id         TEXT NOT NULL,
    last_message_id    TEXT,
    mention_count      INTEGER NOT NULL DEFAULT 0,
    last_pin_timestamp BIGINT,
    PRIMARY KEY (user_id, channel_id)
);

CREATE TABLE IF NOT EXISTS emojis (
    id             TEXT PRIMARY KEY,
    guild_id       TEXT REFERENCES guilds(id) ON DELETE CASCADE,
    name           TEXT,
    creator_id     TEXT,
    roles          TEXT NOT NULL DEFAULT '[]',
    require_colons SMALLINT NOT NULL DEFAULT 1,
    managed        SMALLINT NOT NULL DEFAULT 0,
    animated       SMALLINT NOT NULL DEFAULT 0,
    available      SMALLINT NOT NULL DEFAULT 1
);

CREATE TABLE IF NOT EXISTS channel_recipients (
    channel_id TEXT NOT NULL REFERENCES channels(id) ON DELETE CASCADE,
    user_id    TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    PRIMARY KEY (channel_id, user_id)
);

CREATE TABLE IF NOT EXISTS thread_members (
    channel_id     TEXT NOT NULL REFERENCES channels(id) ON DELETE CASCADE,
    user_id        TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    join_timestamp BIGINT NOT NULL,
    flags          INTEGER NOT NULL DEFAULT 0,
    PRIMARY KEY (channel_id, user_id)
);

CREATE TABLE IF NOT EXISTS pins (
    channel_id TEXT NOT NULL REFERENCES channels(id) ON DELETE CASCADE,
    message_id TEXT NOT NULL,
    pinned_at  BIGINT NOT NULL,
    PRIMARY KEY (channel_id, message_id)
);

CREATE TABLE IF NOT EXISTS schema_meta (
    key   TEXT PRIMARY KEY,
    value TEXT NOT NULL
);

-- Cluster-wide gateway fanout. The row holds the payload; NOTIFY carries the id.
CREATE TABLE IF NOT EXISTS gateway_events (
    id         BIGSERIAL PRIMARY KEY,
    origin     TEXT NOT NULL,
    payload    TEXT NOT NULL,
    created_at BIGINT NOT NULL
);
CREATE INDEX IF NOT EXISTS gateway_events_created ON gateway_events (created_at);

CREATE TABLE IF NOT EXISTS uploads (
    id           TEXT PRIMARY KEY,
    channel_id   TEXT NOT NULL REFERENCES channels(id) ON DELETE CASCADE,
    user_id      TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    filename     TEXT NOT NULL,
    content_type TEXT,
    size         BIGINT NOT NULL DEFAULT 0,
    path         TEXT,
    uploaded     SMALLINT NOT NULL DEFAULT 0,
    created_at   BIGINT NOT NULL
);

CREATE TABLE IF NOT EXISTS webhooks (
    id             TEXT PRIMARY KEY,
    type           INTEGER NOT NULL DEFAULT 1,
    guild_id       TEXT REFERENCES guilds(id) ON DELETE CASCADE,
    channel_id     TEXT NOT NULL REFERENCES channels(id) ON DELETE CASCADE,
    creator_id     TEXT REFERENCES users(id) ON DELETE SET NULL,
    name           TEXT NOT NULL,
    avatar         TEXT,
    token          TEXT NOT NULL,
    application_id TEXT,
    created_at     BIGINT NOT NULL
);
CREATE INDEX IF NOT EXISTS webhooks_channel ON webhooks (channel_id);

CREATE TABLE IF NOT EXISTS audit_logs (
    id          TEXT PRIMARY KEY,
    guild_id    TEXT NOT NULL REFERENCES guilds(id) ON DELETE CASCADE,
    actor_id    TEXT,
    action_type INTEGER NOT NULL,
    target_id   TEXT,
    changes     TEXT NOT NULL DEFAULT '[]',
    reason      TEXT,
    created_at  BIGINT NOT NULL
);
CREATE INDEX IF NOT EXISTS audit_guild ON audit_logs (guild_id, id DESC);

CREATE TABLE IF NOT EXISTS applications (
    id                     TEXT PRIMARY KEY,
    name                   TEXT NOT NULL,
    description            TEXT NOT NULL DEFAULT '',
    icon                   TEXT,
    owner_id               TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    bot_user_id            TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    bot_public             SMALLINT NOT NULL DEFAULT 1,
    bot_require_code_grant SMALLINT NOT NULL DEFAULT 0,
    verify_key             TEXT NOT NULL DEFAULT '',
    created_at             BIGINT NOT NULL
);

CREATE TABLE IF NOT EXISTS bot_tokens (
    token_hash     TEXT PRIMARY KEY,
    application_id TEXT NOT NULL REFERENCES applications(id) ON DELETE CASCADE,
    user_id        TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    created_at     BIGINT NOT NULL
);

-- Additive migrations for deployments created by earlier revisions. CREATE TABLE
-- IF NOT EXISTS does not add columns to an existing table.
ALTER TABLE guilds ADD COLUMN IF NOT EXISTS widget_channel_id TEXT;
ALTER TABLE channels ADD COLUMN IF NOT EXISTS archived SMALLINT NOT NULL DEFAULT 0;
ALTER TABLE channels ADD COLUMN IF NOT EXISTS template TEXT;
ALTER TABLE channels ADD COLUMN IF NOT EXISTS message_retention TEXT;
-- Only meaningful while a legacy monolithic messages table is still present; the
-- partitioned table already has this column.
DO $$
BEGIN
    IF EXISTS (
        SELECT 1 FROM pg_class c JOIN pg_namespace n ON n.oid = c.relnamespace
        WHERE c.relname = 'messages' AND n.nspname = current_schema() AND c.relkind = 'r'
    ) THEN
        ALTER TABLE messages ADD COLUMN IF NOT EXISTS bucket BIGINT NOT NULL DEFAULT 0;
    END IF;
END $$;

CREATE TABLE IF NOT EXISTS oauth2_authorization_codes (
    code           TEXT PRIMARY KEY,
    application_id TEXT NOT NULL REFERENCES applications(id) ON DELETE CASCADE,
    user_id        TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    redirect_uri   TEXT NOT NULL,
    scopes         TEXT NOT NULL DEFAULT '[]',
    guild_id       TEXT,
    permissions    TEXT,
    created_at     BIGINT NOT NULL,
    expires_at     BIGINT NOT NULL,
    consumed       SMALLINT NOT NULL DEFAULT 0
);

CREATE TABLE IF NOT EXISTS oauth2_tokens (
    id             TEXT PRIMARY KEY,
    application_id TEXT NOT NULL REFERENCES applications(id) ON DELETE CASCADE,
    user_id        TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    token_hash     TEXT NOT NULL UNIQUE,
    refresh_hash   TEXT NOT NULL UNIQUE,
    scopes         TEXT NOT NULL DEFAULT '[]',
    created_at     BIGINT NOT NULL,
    expires_at     BIGINT NOT NULL,
    revoked        SMALLINT NOT NULL DEFAULT 0
);
CREATE INDEX IF NOT EXISTS oauth2_tokens_user ON oauth2_tokens (user_id);

ALTER TABLE applications ADD COLUMN IF NOT EXISTS client_secret_hash TEXT NOT NULL DEFAULT '';
ALTER TABLE applications ADD COLUMN IF NOT EXISTS redirect_uris TEXT NOT NULL DEFAULT '[]';
ALTER TABLE applications ADD COLUMN IF NOT EXISTS signing_key TEXT NOT NULL DEFAULT '';
ALTER TABLE applications ADD COLUMN IF NOT EXISTS signing_public_key TEXT NOT NULL DEFAULT '';
ALTER TABLE applications ADD COLUMN IF NOT EXISTS interactions_endpoint_url TEXT;
ALTER TABLE applications ADD COLUMN IF NOT EXISTS role_connection_metadata TEXT NOT NULL DEFAULT '[]';

CREATE TABLE IF NOT EXISTS application_commands (
    id                        TEXT PRIMARY KEY,
    application_id            TEXT NOT NULL REFERENCES applications(id) ON DELETE CASCADE,
    guild_id                  TEXT,
    name                      TEXT NOT NULL,
    description               TEXT NOT NULL DEFAULT '',
    type                      INTEGER NOT NULL DEFAULT 1,
    options                   TEXT NOT NULL DEFAULT '[]',
    default_member_permissions TEXT,
    dm_permission             SMALLINT NOT NULL DEFAULT 1,
    nsfw                      SMALLINT NOT NULL DEFAULT 0,
    version                   TEXT NOT NULL,
    created_at                BIGINT NOT NULL
);
CREATE INDEX IF NOT EXISTS application_commands_app ON application_commands (application_id, guild_id);

CREATE TABLE IF NOT EXISTS auto_moderation_rules (
    id               TEXT PRIMARY KEY,
    guild_id         TEXT NOT NULL REFERENCES guilds(id) ON DELETE CASCADE,
    creator_id       TEXT,
    name             TEXT NOT NULL,
    event_type       INTEGER NOT NULL DEFAULT 1,
    trigger_type     INTEGER NOT NULL,
    trigger_metadata TEXT NOT NULL DEFAULT '{}',
    actions          TEXT NOT NULL DEFAULT '[]',
    enabled          SMALLINT NOT NULL DEFAULT 1,
    exempt_roles     TEXT NOT NULL DEFAULT '[]',
    exempt_channels  TEXT NOT NULL DEFAULT '[]',
    created_at       BIGINT NOT NULL
);
CREATE INDEX IF NOT EXISTS automod_guild ON auto_moderation_rules (guild_id);

CREATE TABLE IF NOT EXISTS role_connections (
    user_id           TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    application_id    TEXT NOT NULL REFERENCES applications(id) ON DELETE CASCADE,
    platform_name     TEXT,
    platform_username TEXT,
    metadata          TEXT NOT NULL DEFAULT '{}',
    updated_at        BIGINT NOT NULL,
    PRIMARY KEY (user_id, application_id)
);

CREATE TABLE IF NOT EXISTS voice_states (
    guild_id                   TEXT NOT NULL REFERENCES guilds(id) ON DELETE CASCADE,
    channel_id                 TEXT NOT NULL REFERENCES channels(id) ON DELETE CASCADE,
    user_id                    TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    session_id                 TEXT NOT NULL,
    deaf                       SMALLINT NOT NULL DEFAULT 0,
    mute                       SMALLINT NOT NULL DEFAULT 0,
    self_deaf                  SMALLINT NOT NULL DEFAULT 0,
    self_mute                  SMALLINT NOT NULL DEFAULT 0,
    self_stream                SMALLINT NOT NULL DEFAULT 0,
    self_video                 SMALLINT NOT NULL DEFAULT 0,
    suppress                   SMALLINT NOT NULL DEFAULT 0,
    request_to_speak_timestamp TEXT,
    voice_token                TEXT NOT NULL DEFAULT '',
    status                     TEXT NOT NULL DEFAULT '',
    updated_at                 BIGINT NOT NULL,
    PRIMARY KEY (guild_id, user_id)
);
CREATE INDEX IF NOT EXISTS voice_states_channel ON voice_states (channel_id);

CREATE TABLE IF NOT EXISTS guild_templates (
    code         TEXT PRIMARY KEY,
    guild_id     TEXT NOT NULL REFERENCES guilds(id) ON DELETE CASCADE,
    creator_id   TEXT NOT NULL REFERENCES users(id),
    name         TEXT NOT NULL,
    description  TEXT,
    usage_count  INTEGER NOT NULL DEFAULT 0,
    serialized   TEXT NOT NULL DEFAULT '{}',
    created_at   BIGINT NOT NULL,
    updated_at   BIGINT NOT NULL
);

CREATE TABLE IF NOT EXISTS scheduled_events (
    id                   TEXT PRIMARY KEY,
    guild_id             TEXT NOT NULL REFERENCES guilds(id) ON DELETE CASCADE,
    channel_id           TEXT,
    creator_id           TEXT,
    name                 TEXT NOT NULL,
    description          TEXT,
    scheduled_start_time TEXT NOT NULL,
    scheduled_end_time   TEXT,
    privacy_level        INTEGER NOT NULL DEFAULT 2,
    status               INTEGER NOT NULL DEFAULT 1,
    entity_type          INTEGER NOT NULL,
    entity_id            TEXT,
    entity_metadata      TEXT NOT NULL DEFAULT '{}',
    image                TEXT,
    created_at           BIGINT NOT NULL
);
CREATE INDEX IF NOT EXISTS scheduled_events_guild ON scheduled_events (guild_id);

CREATE TABLE IF NOT EXISTS stage_instances (
    id                       TEXT PRIMARY KEY,
    guild_id                 TEXT NOT NULL REFERENCES guilds(id) ON DELETE CASCADE,
    channel_id               TEXT NOT NULL UNIQUE REFERENCES channels(id) ON DELETE CASCADE,
    topic                    TEXT NOT NULL,
    privacy_level            INTEGER NOT NULL DEFAULT 2,
    discoverable_disabled    SMALLINT NOT NULL DEFAULT 0,
    guild_scheduled_event_id TEXT,
    created_at               BIGINT NOT NULL
);

CREATE TABLE IF NOT EXISTS integrations (
    guild_id             TEXT NOT NULL REFERENCES guilds(id) ON DELETE CASCADE,
    id                   TEXT NOT NULL,
    name                 TEXT NOT NULL,
    type                 INTEGER NOT NULL DEFAULT 3,
    enabled              SMALLINT NOT NULL DEFAULT 1,
    syncing              SMALLINT NOT NULL DEFAULT 0,
    role_id              TEXT,
    enable_emoticons     SMALLINT NOT NULL DEFAULT 0,
    expire_behavior      INTEGER NOT NULL DEFAULT 0,
    expire_grace_period  INTEGER NOT NULL DEFAULT 0,
    account_name         TEXT,
    account_id           TEXT,
    application_name     TEXT,
    created_at           BIGINT NOT NULL,
    PRIMARY KEY (guild_id, id)
);

CREATE TABLE IF NOT EXISTS command_permissions (
    application_id TEXT NOT NULL REFERENCES applications(id) ON DELETE CASCADE,
    guild_id       TEXT NOT NULL REFERENCES guilds(id) ON DELETE CASCADE,
    command_id     TEXT NOT NULL,
    target_id      TEXT NOT NULL,
    type           INTEGER NOT NULL,
    permission     SMALLINT NOT NULL DEFAULT 0,
    PRIMARY KEY (application_id, guild_id, command_id, target_id)
);

CREATE TABLE IF NOT EXISTS guild_onboarding (
    guild_id            TEXT PRIMARY KEY REFERENCES guilds(id) ON DELETE CASCADE,
    prompts             TEXT NOT NULL DEFAULT '[]',
    default_channel_ids TEXT NOT NULL DEFAULT '[]',
    enabled             SMALLINT NOT NULL DEFAULT 0,
    mode                INTEGER NOT NULL DEFAULT 0
);

CREATE TABLE IF NOT EXISTS welcome_screens (
    guild_id         TEXT PRIMARY KEY REFERENCES guilds(id) ON DELETE CASCADE,
    description      TEXT,
    welcome_channels TEXT NOT NULL DEFAULT '[]',
    enabled          SMALLINT NOT NULL DEFAULT 0
);

CREATE TABLE IF NOT EXISTS stickers (
    id          TEXT PRIMARY KEY,
    guild_id    TEXT REFERENCES guilds(id) ON DELETE CASCADE,
    name        TEXT NOT NULL,
    description TEXT,
    tags        TEXT NOT NULL DEFAULT '',
    format_type INTEGER NOT NULL DEFAULT 1,
    available   SMALLINT NOT NULL DEFAULT 1,
    user_id     TEXT,
    sort_value  INTEGER NOT NULL DEFAULT 0,
    created_at  BIGINT NOT NULL
);
CREATE INDEX IF NOT EXISTS stickers_guild ON stickers (guild_id);

CREATE TABLE IF NOT EXISTS cluster_instances (
    id        TEXT PRIMARY KEY,
    last_seen BIGINT NOT NULL
);
