// Package model contains the API objects exactly as the documentation defines
// them: field names, optionality and types match the published tables.
package model

// AvatarDecorationData is the avatar_decoration_data object.
type AvatarDecorationData struct {
	SKUID string `json:"sku_id"`
	Asset string `json:"asset"`
}

// User is the user object.
type User struct {
	ID                   string                `json:"id"`
	Username             string                `json:"username"`
	Discriminator        string                `json:"discriminator"`
	GlobalName           *string               `json:"global_name"`
	Avatar               *string               `json:"avatar"`
	Bot                  bool                  `json:"bot,omitempty"`
	System               bool                  `json:"system,omitempty"`
	MFAEnabled           bool                  `json:"mfa_enabled,omitempty"`
	Banner               *string               `json:"banner,omitempty"`
	AccentColor          *int                  `json:"accent_color,omitempty"`
	Locale               string                `json:"locale,omitempty"`
	Verified             bool                  `json:"verified,omitempty"`
	Email                *string               `json:"email,omitempty"`
	Flags                int64                 `json:"flags,omitempty"`
	PremiumType          int                   `json:"premium_type,omitempty"`
	PublicFlags          int64                 `json:"public_flags,omitempty"`
	AvatarDecorationData *AvatarDecorationData `json:"avatar_decoration_data,omitempty"`
}

// Role is the role object.
type Role struct {
	ID           string  `json:"id"`
	Name         string  `json:"name"`
	Color        int     `json:"color"`
	Hoist        bool    `json:"hoist"`
	Icon         *string `json:"icon"`
	UnicodeEmoji *string `json:"unicode_emoji"`
	Position     int     `json:"position"`
	Permissions  string  `json:"permissions"`
	Managed      bool    `json:"managed"`
	Mentionable  bool    `json:"mentionable"`
	Flags        int64   `json:"flags"`
}

// Emoji is the emoji object used inside messages and guilds.
type Emoji struct {
	ID            *string  `json:"id"`
	Name          *string  `json:"name"`
	Roles         []string `json:"roles,omitempty"`
	User          *User    `json:"user,omitempty"`
	RequireColons bool     `json:"require_colons,omitempty"`
	Managed       bool     `json:"managed,omitempty"`
	Animated      bool     `json:"animated,omitempty"`
	Available     bool     `json:"available,omitempty"`
}

// Member is the guild member object.
type Member struct {
	User                       *User    `json:"user,omitempty"`
	Nick                       *string  `json:"nick"`
	Avatar                     *string  `json:"avatar,omitempty"`
	Banner                     *string  `json:"banner,omitempty"`
	Roles                      []string `json:"roles"`
	JoinedAt                   string   `json:"joined_at"`
	PremiumSince               *string  `json:"premium_since"`
	Deaf                       bool     `json:"deaf"`
	Mute                       bool     `json:"mute"`
	Flags                      int      `json:"flags"`
	Pending                    bool     `json:"pending,omitempty"`
	CommunicationDisabledUntil *string  `json:"communication_disabled_until,omitempty"`
}

// Overwrite is a channel permission overwrite.
type Overwrite struct {
	ID    string `json:"id"`
	Type  int    `json:"type"`
	Allow string `json:"allow"`
	Deny  string `json:"deny"`
}

// ThreadMetadata is the thread metadata object.
type ThreadMetadata struct {
	Archived            bool    `json:"archived"`
	AutoArchiveDuration int     `json:"auto_archive_duration"`
	ArchiveTimestamp    string  `json:"archive_timestamp"`
	Locked              bool    `json:"locked"`
	Invitable           *bool   `json:"invitable,omitempty"`
	CreateTimestamp     *string `json:"create_timestamp,omitempty"`
}

// Tag is a forum channel tag.
type Tag struct {
	ID        string  `json:"id"`
	Name      string  `json:"name"`
	Moderated bool    `json:"moderated"`
	EmojiID   *string `json:"emoji_id"`
	EmojiName *string `json:"emoji_name"`
}

// DefaultReaction is a forum channel default reaction.
type DefaultReaction struct {
	EmojiID   *string `json:"emoji_id"`
	EmojiName *string `json:"emoji_name"`
}

// Channel is the channel object.
type Channel struct {
	ID                            string           `json:"id"`
	Type                          int              `json:"type"`
	GuildID                       *string          `json:"guild_id,omitempty"`
	Position                      *int             `json:"position,omitempty"`
	PermissionOverwrites          []Overwrite      `json:"permission_overwrites,omitempty"`
	Name                          *string          `json:"name"`
	Topic                         *string          `json:"topic,omitempty"`
	NSFW                          *bool            `json:"nsfw,omitempty"`
	LastMessageID                 *string          `json:"last_message_id,omitempty"`
	Bitrate                       *int             `json:"bitrate,omitempty"`
	UserLimit                     *int             `json:"user_limit,omitempty"`
	RateLimitPerUser              *int             `json:"rate_limit_per_user,omitempty"`
	Recipients                    []*User          `json:"recipients,omitempty"`
	OwnerID                       *string          `json:"owner_id,omitempty"`
	LastPinTimestamp              *string          `json:"last_pin_timestamp,omitempty"`
	RTCRegion                     *string          `json:"rtc_region,omitempty"`
	VideoQualityMode              *int             `json:"video_quality_mode,omitempty"`
	MessageCount                  *int             `json:"message_count,omitempty"`
	MemberCount                   *int             `json:"member_count,omitempty"`
	ThreadMetadata                *ThreadMetadata  `json:"thread_metadata,omitempty"`
	Member                        *ThreadMember    `json:"member,omitempty"`
	DefaultAutoArchiveDuration    *int             `json:"default_auto_archive_duration,omitempty"`
	Permissions                   *string          `json:"permissions,omitempty"`
	Flags                         *int             `json:"flags,omitempty"`
	TotalMessageSent              *int             `json:"total_message_sent,omitempty"`
	AvailableTags                 []Tag            `json:"available_tags,omitempty"`
	AppliedTags                   []string         `json:"applied_tags,omitempty"`
	DefaultReactionEmoji          *DefaultReaction `json:"default_reaction_emoji,omitempty"`
	DefaultThreadRateLimitPerUser *int             `json:"default_thread_rate_limit_per_user,omitempty"`
	DefaultSortOrder              *int             `json:"default_sort_order,omitempty"`
	DefaultForumLayout            *int             `json:"default_forum_layout,omitempty"`
	Icon                          *string          `json:"icon,omitempty"`
	ParentID                      *string          `json:"parent_id,omitempty"`
}

// ThreadMember is the thread member object.
type ThreadMember struct {
	ID            *string `json:"id,omitempty"`
	UserID        *string `json:"user_id,omitempty"`
	JoinTimestamp string  `json:"join_timestamp"`
	Flags         int     `json:"flags"`
	Member        *Member `json:"member,omitempty"`
}

// Guild is the guild object.
type Guild struct {
	ID                          string   `json:"id"`
	Name                        string   `json:"name"`
	Icon                        *string  `json:"icon"`
	Splash                      *string  `json:"splash"`
	DiscoverySplash             *string  `json:"discovery_splash"`
	Owner                       *bool    `json:"owner,omitempty"`
	OwnerID                     string   `json:"owner_id"`
	Permissions                 *string  `json:"permissions,omitempty"`
	AfkChannelID                *string  `json:"afk_channel_id"`
	AfkTimeout                  int      `json:"afk_timeout"`
	WidgetEnabled               *bool    `json:"widget_enabled,omitempty"`
	WidgetChannelID             *string  `json:"widget_channel_id,omitempty"`
	VerificationLevel           int      `json:"verification_level"`
	DefaultMessageNotifications int      `json:"default_message_notifications"`
	ExplicitContentFilter       int      `json:"explicit_content_filter"`
	Roles                       []Role   `json:"roles"`
	Emojis                      []Emoji  `json:"emojis"`
	Features                    []string `json:"features"`
	MfaLevel                    int      `json:"mfa_level"`
	ApplicationID               *string  `json:"application_id"`
	SystemChannelID             *string  `json:"system_channel_id"`
	SystemChannelFlags          int      `json:"system_channel_flags"`
	RulesChannelID              *string  `json:"rules_channel_id"`
	MaxPresences                *int     `json:"max_presences,omitempty"`
	MaxMembers                  int      `json:"max_members,omitempty"`
	VanityURLCode               *string  `json:"vanity_url_code"`
	Description                 *string  `json:"description"`
	Banner                      *string  `json:"banner"`
	PremiumTier                 int      `json:"premium_tier"`
	PremiumSubscriptionCount    int      `json:"premium_subscription_count,omitempty"`
	PreferredLocale             string   `json:"preferred_locale"`
	PublicUpdatesChannelID      *string  `json:"public_updates_channel_id"`
	MaxVideoChannelUsers        *int     `json:"max_video_channel_users,omitempty"`
	MaxStageVideoChannelUsers   *int     `json:"max_stage_video_channel_users,omitempty"`
	ApproximateMemberCount      *int     `json:"approximate_member_count,omitempty"`
	ApproximatePresenceCount    *int     `json:"approximate_presence_count,omitempty"`
	NSFWLevel                   int      `json:"nsfw_level"`
	Stickers                    []any    `json:"stickers,omitempty"`
	PremiumProgressBarEnabled   bool     `json:"premium_progress_bar_enabled"`
	SafetyAlertsChannelID       *string  `json:"safety_alerts_channel_id"`
}

// Attachment is the attachment object.
type Attachment struct {
	ID          string  `json:"id"`
	Filename    string  `json:"filename"`
	Description *string `json:"description,omitempty"`
	ContentType *string `json:"content_type,omitempty"`
	Size        int64   `json:"size"`
	URL         string  `json:"url"`
	ProxyURL    string  `json:"proxy_url"`
	Height      *int    `json:"height,omitempty"`
	Width       *int    `json:"width,omitempty"`
	Ephemeral   *bool   `json:"ephemeral,omitempty"`
	Flags       *int    `json:"flags,omitempty"`
}

// EmbedFooter / EmbedImage / EmbedThumbnail / EmbedVideo / EmbedProvider /
// EmbedAuthor / EmbedField make up the embed object.
type EmbedFooter struct {
	Text         string  `json:"text"`
	IconURL      *string `json:"icon_url,omitempty"`
	ProxyIconURL *string `json:"proxy_icon_url,omitempty"`
}

type EmbedImage struct {
	URL      string  `json:"url"`
	ProxyURL *string `json:"proxy_url,omitempty"`
	Height   *int    `json:"height,omitempty"`
	Width    *int    `json:"width,omitempty"`
}

type EmbedThumbnail struct {
	URL      string  `json:"url"`
	ProxyURL *string `json:"proxy_url,omitempty"`
	Height   *int    `json:"height,omitempty"`
	Width    *int    `json:"width,omitempty"`
}

type EmbedVideo struct {
	URL      *string `json:"url,omitempty"`
	ProxyURL *string `json:"proxy_url,omitempty"`
	Height   *int    `json:"height,omitempty"`
	Width    *int    `json:"width,omitempty"`
}

type EmbedProvider struct {
	Name *string `json:"name,omitempty"`
	URL  *string `json:"url,omitempty"`
}

type EmbedAuthor struct {
	Name         string  `json:"name"`
	URL          *string `json:"url,omitempty"`
	IconURL      *string `json:"icon_url,omitempty"`
	ProxyIconURL *string `json:"proxy_icon_url,omitempty"`
}

type EmbedField struct {
	Name   string `json:"name"`
	Value  string `json:"value"`
	Inline *bool  `json:"inline,omitempty"`
}

// Embed is the embed object.
type Embed struct {
	Title       *string         `json:"title,omitempty"`
	Type        *string         `json:"type,omitempty"`
	Description *string         `json:"description,omitempty"`
	URL         *string         `json:"url,omitempty"`
	Timestamp   *string         `json:"timestamp,omitempty"`
	Color       *int            `json:"color,omitempty"`
	Footer      *EmbedFooter    `json:"footer,omitempty"`
	Image       *EmbedImage     `json:"image,omitempty"`
	Thumbnail   *EmbedThumbnail `json:"thumbnail,omitempty"`
	Video       *EmbedVideo     `json:"video,omitempty"`
	Provider    *EmbedProvider  `json:"provider,omitempty"`
	Author      *EmbedAuthor    `json:"author,omitempty"`
	Fields      []EmbedField    `json:"fields,omitempty"`
}

// Reaction is the reaction object.
type Reaction struct {
	Count        int `json:"count"`
	CountDetails struct {
		Burst  int `json:"burst"`
		Normal int `json:"normal"`
	} `json:"count_details"`
	Me          bool     `json:"me"`
	MeBurst     bool     `json:"me_burst"`
	Emoji       Emoji    `json:"emoji"`
	BurstColors []string `json:"burst_colors"`
}

// StickerItem is the sticker item object.
type StickerItem struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	FormatType int    `json:"format_type"`
}

// Message is the message object.
type Message struct {
	ID                string        `json:"id"`
	ChannelID         string        `json:"channel_id"`
	Author            User          `json:"author"`
	Content           string        `json:"content"`
	Timestamp         string        `json:"timestamp"`
	EditedTimestamp   *string       `json:"edited_timestamp"`
	TTS               bool          `json:"tts"`
	MentionEveryone   bool          `json:"mention_everyone"`
	Mentions          []User        `json:"mentions"`
	MentionRoles      []string      `json:"mention_roles"`
	MentionChannels   []any         `json:"mention_channels,omitempty"`
	Attachments       []Attachment  `json:"attachments"`
	Embeds            []Embed       `json:"embeds"`
	Reactions         []Reaction    `json:"reactions,omitempty"`
	Nonce             *string       `json:"nonce,omitempty"`
	Pinned            bool          `json:"pinned"`
	Flags             int           `json:"flags"`
	Components        []any         `json:"components"`
	ReferencedMessage *Message      `json:"referenced_message,omitempty"`
	Thread            *Channel      `json:"thread,omitempty"`
	StickerItems      []StickerItem `json:"sticker_items,omitempty"`
	Type              int           `json:"type"`
	WebhookID         *string       `json:"webhook_id,omitempty"`
	ApplicationID     *string       `json:"application_id,omitempty"`
	GuildID           *string       `json:"guild_id,omitempty"`
	Member            *Member       `json:"member,omitempty"`
}

// Invite is the invite object.
type Invite struct {
	Type                     int      `json:"type"`
	Code                     string   `json:"code"`
	Guild                    *Guild   `json:"guild,omitempty"`
	Channel                  *Channel `json:"channel"`
	Inviter                  *User    `json:"inviter,omitempty"`
	TargetType               *int     `json:"target_type,omitempty"`
	TargetUser               *User    `json:"target_user,omitempty"`
	TargetApplication        *any     `json:"target_application,omitempty"`
	ApproximatePresenceCount *int     `json:"approximate_presence_count,omitempty"`
	ApproximateMemberCount   *int     `json:"approximate_member_count,omitempty"`
	ExpiresAt                *string  `json:"expires_at"`
	Uses                     int      `json:"uses,omitempty"`
	MaxUses                  int      `json:"max_uses,omitempty"`
	MaxAge                   int      `json:"max_age,omitempty"`
	Temporary                bool     `json:"temporary,omitempty"`
	CreatedAt                string   `json:"created_at,omitempty"`
}

// ReadState is the read state object.
type ReadState struct {
	ID               string  `json:"id"`
	MentionCount     int     `json:"mention_count"`
	LastMessageID    *string `json:"last_message_id"`
	LastPinTimestamp *string `json:"last_pin_timestamp,omitempty"`
}

// Relationship is the relationship object.
type Relationship struct {
	ID       string  `json:"id"`
	Type     int     `json:"type"`
	UserID   string  `json:"user_id"`
	Nickname *string `json:"nickname,omitempty"`
	Since    *string `json:"since,omitempty"`
}
