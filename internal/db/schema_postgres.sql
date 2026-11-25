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
    created_at         BIGINT NOT NULL
);

CREATE UNIQUE INDEX IF NOT EXISTS users_username_discriminator
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

-- Messages are indexed by (channel_id, id) which is the partition key plus
-- sort key from the published message storage design: a channel read is one
-- ordered range scan, and the snowflake id orders chronologically.
CREATE TABLE IF NOT EXISTS messages (
    id                    TEXT PRIMARY KEY,
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
    nonce                 TEXT
);
CREATE INDEX IF NOT EXISTS messages_channel ON messages (channel_id, id DESC);
CREATE INDEX IF NOT EXISTS messages_guild ON messages (guild_id, id DESC);

CREATE TABLE IF NOT EXISTS message_reactions (
    message_id TEXT NOT NULL REFERENCES messages(id) ON DELETE CASCADE,
    channel_id TEXT NOT NULL,
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
    message_id TEXT NOT NULL REFERENCES messages(id) ON DELETE CASCADE,
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
