-- waveycord schema. Mirrors the object model in the official Discord API docs
-- (developers/resources/*.mdx) so REST payloads map 1:1 onto rows.

PRAGMA journal_mode = WAL;
PRAGMA foreign_keys = ON;

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
    mfa_enabled        INTEGER NOT NULL DEFAULT 0,
    verified           INTEGER NOT NULL DEFAULT 0,
    bot                INTEGER NOT NULL DEFAULT 0,
    system             INTEGER NOT NULL DEFAULT 0,
    flags              INTEGER NOT NULL DEFAULT 0,
    public_flags       INTEGER NOT NULL DEFAULT 0,
    premium_type       INTEGER NOT NULL DEFAULT 0,
    locale             TEXT NOT NULL DEFAULT 'en-US',
    bio                TEXT NOT NULL DEFAULT '',
    avatar_decoration  TEXT,          -- JSON avatar_decoration_data
    created_at         INTEGER NOT NULL
);

CREATE INDEX IF NOT EXISTS users_username_discriminator
    ON users (lower(username), discriminator);

CREATE TABLE IF NOT EXISTS sessions (
    id           TEXT PRIMARY KEY,
    user_id      TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    token_hash   TEXT NOT NULL UNIQUE,
    client_info  TEXT NOT NULL DEFAULT '',
    created_at   INTEGER NOT NULL,
    last_used_at INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS sessions_user ON sessions (user_id);

CREATE TABLE IF NOT EXISTS guilds (
    id                             TEXT PRIMARY KEY,
    name                           TEXT NOT NULL,
    icon                           TEXT,
    splash                         TEXT,
    discovery_splash               TEXT,
    banner                         TEXT,
    owner_id                       TEXT NOT NULL REFERENCES users(id),
    region                         TEXT NOT NULL DEFAULT 'deprecated',
    afk_channel_id                 TEXT,
    afk_timeout                    INTEGER NOT NULL DEFAULT 300,
    widget_enabled                 INTEGER NOT NULL DEFAULT 0,
    verification_level             INTEGER NOT NULL DEFAULT 0,
    default_message_notifications  INTEGER NOT NULL DEFAULT 0,
    explicit_content_filter        INTEGER NOT NULL DEFAULT 0,
    mfa_level                      INTEGER NOT NULL DEFAULT 0,
    system_channel_id              TEXT,
    system_channel_flags           INTEGER NOT NULL DEFAULT 0,
    rules_channel_id               TEXT,
    features                       TEXT NOT NULL DEFAULT '[]', -- JSON array
    preferred_locale               TEXT NOT NULL DEFAULT 'en-US',
    description                    TEXT,
    vanity_url_code                TEXT,
    premium_tier                   INTEGER NOT NULL DEFAULT 0,
    premium_subscription_count     INTEGER NOT NULL DEFAULT 0,
    nsfw_level                     INTEGER NOT NULL DEFAULT 0,
    created_at                     INTEGER NOT NULL
);

CREATE TABLE IF NOT EXISTS roles (
    id            TEXT PRIMARY KEY,
    guild_id      TEXT NOT NULL REFERENCES guilds(id) ON DELETE CASCADE,
    name          TEXT NOT NULL,
    color         INTEGER NOT NULL DEFAULT 0,
    hoist         INTEGER NOT NULL DEFAULT 0,
    position      INTEGER NOT NULL DEFAULT 0,
    permissions   TEXT NOT NULL DEFAULT '0',
    managed       INTEGER NOT NULL DEFAULT 0,
    mentionable   INTEGER NOT NULL DEFAULT 0,
    icon          TEXT,
    unicode_emoji TEXT,
    flags         INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX IF NOT EXISTS roles_guild ON roles (guild_id, position);

CREATE TABLE IF NOT EXISTS guild_members (
    guild_id                   TEXT NOT NULL REFERENCES guilds(id) ON DELETE CASCADE,
    user_id                    TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    nick                       TEXT,
    joined_at                  INTEGER NOT NULL,
    premium_since              INTEGER,
    deaf                       INTEGER NOT NULL DEFAULT 0,
    mute                       INTEGER NOT NULL DEFAULT 0,
    pending                    INTEGER NOT NULL DEFAULT 0,
    communication_disabled_until INTEGER,
    avatar                     TEXT,
    banner                     TEXT,
    role_ids                   TEXT NOT NULL DEFAULT '[]', -- JSON array of ids
    PRIMARY KEY (guild_id, user_id)
);
CREATE INDEX IF NOT EXISTS members_user ON guild_members (user_id);

CREATE TABLE IF NOT EXISTS channels (
    id                      TEXT PRIMARY KEY,
    type                    INTEGER NOT NULL,
    guild_id                TEXT REFERENCES guilds(id) ON DELETE CASCADE,
    position                INTEGER NOT NULL DEFAULT 0,
    name                    TEXT,
    topic                   TEXT,
    nsfw                    INTEGER NOT NULL DEFAULT 0,
    last_message_id         TEXT,
    bitrate                 INTEGER,
    user_limit              INTEGER,
    rate_limit_per_user     INTEGER NOT NULL DEFAULT 0,
    parent_id               TEXT,
    owner_id                TEXT,
    last_pin_timestamp      INTEGER,
    rtc_region              TEXT,
    video_quality_mode      INTEGER NOT NULL DEFAULT 1,
    message_count           INTEGER NOT NULL DEFAULT 0,
    member_count            INTEGER NOT NULL DEFAULT 0,
    thread_metadata         TEXT,   -- JSON
    archived                INTEGER NOT NULL DEFAULT 0,
    message_retention       TEXT,
    icon                    TEXT,
    flags                   INTEGER NOT NULL DEFAULT 0,
    available_tags          TEXT,
    default_reaction_emoji  TEXT,
    default_sort_order      TEXT,
    default_forum_layout    INTEGER NOT NULL DEFAULT 0,
    template                TEXT,
    created_at              INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS channels_guild ON channels (guild_id, position);

CREATE TABLE IF NOT EXISTS permission_overwrites (
    channel_id TEXT NOT NULL REFERENCES channels(id) ON DELETE CASCADE,
    target_id  TEXT NOT NULL,
    type       INTEGER NOT NULL, -- 0 role, 1 member
    allow      TEXT NOT NULL DEFAULT '0',
    deny       TEXT NOT NULL DEFAULT '0',
    PRIMARY KEY (channel_id, target_id)
);

CREATE TABLE IF NOT EXISTS messages (
    id                    TEXT PRIMARY KEY,
    channel_id            TEXT NOT NULL REFERENCES channels(id) ON DELETE CASCADE,
    guild_id              TEXT,
    author_id             TEXT NOT NULL REFERENCES users(id),
    content               TEXT NOT NULL DEFAULT '',
    timestamp             INTEGER NOT NULL,
    edited_timestamp      INTEGER,
    tts                   INTEGER NOT NULL DEFAULT 0,
    mention_everyone      INTEGER NOT NULL DEFAULT 0,
    mentions              TEXT NOT NULL DEFAULT '[]',
    mention_roles         TEXT NOT NULL DEFAULT '[]',
    mention_channels      TEXT NOT NULL DEFAULT '[]',
    pinned                INTEGER NOT NULL DEFAULT 0,
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
    emoji_key  TEXT NOT NULL,   -- name:id or unicode
    emoji_name TEXT NOT NULL,
    emoji_id   TEXT,
    animated   INTEGER NOT NULL DEFAULT 0,
    created_at INTEGER NOT NULL,
    PRIMARY KEY (message_id, emoji_key, user_id)
);

CREATE TABLE IF NOT EXISTS invites (
    code           TEXT PRIMARY KEY,
    guild_id       TEXT REFERENCES guilds(id) ON DELETE CASCADE,
    channel_id     TEXT NOT NULL REFERENCES channels(id) ON DELETE CASCADE,
    inviter_id     TEXT NOT NULL REFERENCES users(id),
    uses           INTEGER NOT NULL DEFAULT 0,
    max_uses       INTEGER NOT NULL DEFAULT 0,
    max_age        INTEGER NOT NULL DEFAULT 86400,
    temporary      INTEGER NOT NULL DEFAULT 0,
    unique_flag    INTEGER NOT NULL DEFAULT 0,
    target_type    INTEGER,
    target_user_id TEXT,
    target_application_id TEXT,
    created_at     INTEGER NOT NULL
);

CREATE TABLE IF NOT EXISTS bans (
    guild_id   TEXT NOT NULL REFERENCES guilds(id) ON DELETE CASCADE,
    user_id    TEXT NOT NULL,
    reason     TEXT,
    banned_at  INTEGER NOT NULL,
    PRIMARY KEY (guild_id, user_id)
);

CREATE TABLE IF NOT EXISTS relationships (
    user_id    TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    target_id  TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    type       INTEGER NOT NULL,
    nickname   TEXT,
    since      INTEGER,
    PRIMARY KEY (user_id, target_id)
);

CREATE TABLE IF NOT EXISTS read_states (
    user_id           TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    channel_id        TEXT NOT NULL,
    last_message_id   TEXT,
    mention_count     INTEGER NOT NULL DEFAULT 0,
    last_pin_timestamp INTEGER,
    PRIMARY KEY (user_id, channel_id)
);

CREATE TABLE IF NOT EXISTS emojis (
    id             TEXT PRIMARY KEY,
    guild_id       TEXT REFERENCES guilds(id) ON DELETE CASCADE,
    name           TEXT,
    creator_id     TEXT,
    roles          TEXT NOT NULL DEFAULT '[]',
    require_colons INTEGER NOT NULL DEFAULT 1,
    managed        INTEGER NOT NULL DEFAULT 0,
    animated       INTEGER NOT NULL DEFAULT 0,
    available      INTEGER NOT NULL DEFAULT 1
);

CREATE TABLE IF NOT EXISTS channel_recipients (
    channel_id TEXT NOT NULL REFERENCES channels(id) ON DELETE CASCADE,
    user_id    TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    PRIMARY KEY (channel_id, user_id)
);

CREATE TABLE IF NOT EXISTS thread_members (
    channel_id     TEXT NOT NULL REFERENCES channels(id) ON DELETE CASCADE,
    user_id        TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    join_timestamp INTEGER NOT NULL,
    flags          INTEGER NOT NULL DEFAULT 0,
    PRIMARY KEY (channel_id, user_id)
);

CREATE TABLE IF NOT EXISTS pins (
    channel_id TEXT NOT NULL REFERENCES channels(id) ON DELETE CASCADE,
    message_id TEXT NOT NULL REFERENCES messages(id) ON DELETE CASCADE,
    pinned_at  INTEGER NOT NULL,
    PRIMARY KEY (channel_id, message_id)
);

CREATE TABLE IF NOT EXISTS schema_meta (
    key   TEXT PRIMARY KEY,
    value TEXT NOT NULL
);

-- Cluster-wide gateway fanout. The row holds the payload; NOTIFY carries the id.
CREATE TABLE IF NOT EXISTS gateway_events (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    origin     TEXT NOT NULL,
    payload    TEXT NOT NULL,
    created_at INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS gateway_events_created ON gateway_events (created_at);

CREATE TABLE IF NOT EXISTS uploads (
    id           TEXT PRIMARY KEY,
    channel_id   TEXT NOT NULL REFERENCES channels(id) ON DELETE CASCADE,
    user_id      TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    filename     TEXT NOT NULL,
    content_type TEXT,
    size         INTEGER NOT NULL DEFAULT 0,
    path         TEXT,
    uploaded     INTEGER NOT NULL DEFAULT 0,
    created_at   INTEGER NOT NULL
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
    created_at     INTEGER NOT NULL
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
    created_at  INTEGER NOT NULL
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
    created_at             INTEGER NOT NULL
);

CREATE TABLE IF NOT EXISTS bot_tokens (
    token_hash     TEXT PRIMARY KEY,
    application_id TEXT NOT NULL REFERENCES applications(id) ON DELETE CASCADE,
    user_id        TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    created_at     INTEGER NOT NULL
);
