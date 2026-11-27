package store

import (
	"database/sql"
	"encoding/json"

	"github.com/wavedevgit/waveycord/internal/model"
)

// --- guild templates ---

// Template is the template object.
type Template struct {
	Code                  string         `json:"code"`
	Name                  string         `json:"name"`
	Description           *string        `json:"description"`
	UsageCount            int            `json:"usage_count"`
	CreatorID             string         `json:"creator_id"`
	Creator               *model.User    `json:"creator,omitempty"`
	CreatedAt             string         `json:"created_at"`
	UpdatedAt             string         `json:"updated_at"`
	SourceGuildID         string         `json:"source_guild_id"`
	SerializedSourceGuild map[string]any `json:"serialized_source_guild"`
	IsDirty               bool           `json:"is_dirty"`
}

const templateColumns = `code, guild_id, creator_id, name, description, usage_count, serialized, created_at, updated_at`

func scanTemplate(row rowScanner) (Template, error) {
	var (
		t       Template
		desc    sql.NullString
		source  string
		ser     string
		created int64
		updated int64
	)
	if err := row.Scan(&t.Code, &source, &t.CreatorID, &t.Name, &desc, &t.UsageCount, &ser, &created, &updated); err != nil {
		return t, err
	}
	t.Description = ns(desc)
	t.SourceGuildID = source
	t.CreatedAt = iso(created)
	t.UpdatedAt = iso(updated)
	t.SerializedSourceGuild = map[string]any{}
	_ = json.Unmarshal([]byte(ser), &t.SerializedSourceGuild)
	return t, nil
}

// CreateTemplate snapshots a guild's shape.
func (s *Store) CreateTemplate(guildID, creatorID, name string, description *string) (Template, error) {
	g, err := s.Guild(guildID)
	if err != nil {
		return Template{}, err
	}
	channels, err := s.GuildChannels(guildID)
	if err != nil {
		return Template{}, err
	}
	type snapChannel struct {
		Name     *string `json:"name"`
		Type     int     `json:"type"`
		Topic    *string `json:"topic,omitempty"`
		Position int     `json:"position"`
	}
	snap := map[string]any{"name": g.Name, "description": g.Description, "channels": []snapChannel{}}
	list := make([]snapChannel, 0, len(channels))
	for _, c := range channels {
		list = append(list, snapChannel{Name: c.Name, Type: c.Type, Topic: c.Topic, Position: *c.Position})
	}
	snap["channels"] = list
	raw, _ := json.Marshal(snap)
	code := newInviteCode() + newInviteCode()
	_, err = s.DB.Exec(`INSERT INTO guild_templates
		(code, guild_id, creator_id, name, description, usage_count, serialized, created_at, updated_at)
		VALUES (?,?,?,?,?,0,?,?,?)`, code, guildID, creatorID, name, description, string(raw), now(), now())
	if err != nil {
		return Template{}, mapErr(err)
	}
	return s.Template(code)
}

// Template loads one template.
func (s *Store) Template(code string) (Template, error) {
	t, err := scanTemplate(s.DB.QueryRow(`SELECT `+templateColumns+` FROM guild_templates WHERE code = ?`, code))
	if err != nil {
		return t, mapErr(err)
	}
	if u, err := s.UserByID(t.CreatorID); err == nil {
		t.Creator = &u
	}
	return t, nil
}

// Templates lists a guild's templates.
func (s *Store) Templates(guildID string) ([]Template, error) {
	rows, err := s.DB.Query(`SELECT `+templateColumns+` FROM guild_templates WHERE guild_id = ? ORDER BY created_at DESC`, guildID)
	if err != nil {
		return nil, mapErr(err)
	}
	var codes []string
	for rows.Next() {
		var t Template
		var desc sql.NullString
		var source, ser string
		var created, updated int64
		if err := rows.Scan(&t.Code, &source, &t.CreatorID, &t.Name, &desc, &t.UsageCount, &ser, &created, &updated); err != nil {
			rows.Close()
			return nil, mapErr(err)
		}
		codes = append(codes, t.Code)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, mapErr(err)
	}
	out := make([]Template, 0, len(codes))
	for _, c := range codes {
		if t, err := s.Template(c); err == nil {
			out = append(out, t)
		}
	}
	return out, nil
}

// UpdateTemplate patches name/description.
func (s *Store) UpdateTemplate(guildID, code string, name, description *string) (Template, error) {
	set := []string{"updated_at = ?"}
	args := []any{now()}
	if name != nil {
		set = append(set, "name = ?")
		args = append(args, *name)
	}
	if description != nil {
		set = append(set, "description = ?")
		args = append(args, *description)
	}
	args = append(args, guildID, code)
	res, err := s.DB.Exec(`UPDATE guild_templates SET `+joinComma(set)+` WHERE guild_id = ? AND code = ?`, args...)
	if err != nil {
		return Template{}, mapErr(err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return Template{}, ErrNotFound
	}
	return s.Template(code)
}

// SyncTemplate re-snapshots the guild.
func (s *Store) SyncTemplate(guildID, code string) (Template, error) {
	t, err := s.Template(code)
	if err != nil || t.SourceGuildID != guildID {
		return Template{}, ErrNotFound
	}
	channels, err := s.GuildChannels(guildID)
	if err != nil {
		return Template{}, err
	}
	snap := map[string]any{}
	_ = json.Unmarshal([]byte(mustJSON(t.SerializedSourceGuild)), &snap)
	type snapChannel struct {
		Name     *string `json:"name"`
		Type     int     `json:"type"`
		Topic    *string `json:"topic,omitempty"`
		Position int     `json:"position"`
	}
	list := make([]snapChannel, 0, len(channels))
	for _, c := range channels {
		list = append(list, snapChannel{Name: c.Name, Type: c.Type, Topic: c.Topic, Position: *c.Position})
	}
	snap["channels"] = list
	raw, _ := json.Marshal(snap)
	_, err = s.DB.Exec(`UPDATE guild_templates SET serialized = ?, updated_at = ? WHERE guild_id = ? AND code = ?`,
		string(raw), now(), guildID, code)
	if err != nil {
		return Template{}, mapErr(err)
	}
	return s.Template(code)
}

// DeleteTemplate removes a template.
func (s *Store) DeleteTemplate(guildID, code string) error {
	res, err := s.DB.Exec(`DELETE FROM guild_templates WHERE guild_id = ? AND code = ?`, guildID, code)
	if err != nil {
		return mapErr(err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// UseTemplate creates a guild from a template snapshot.
func (s *Store) UseTemplate(code, ownerID string, name *string) (model.Guild, error) {
	t, err := s.Template(code)
	if err != nil {
		return model.Guild{}, err
	}
	guildName := t.Name
	if name != nil {
		guildName = *name
	}
	g, err := s.CreateGuild(ownerID, CreateGuildParams{Name: guildName, Description: t.Description})
	if err != nil {
		return g, err
	}
	if rawChannels, ok := t.SerializedSourceGuild["channels"].([]any); ok {
		for _, item := range rawChannels {
			c, ok := item.(map[string]any)
			if !ok {
				continue
			}
			typ := int(toFloat(c["type"]))
			switch typ {
			case ChannelText, ChannelVoice, ChannelCategory, ChannelAnnouncement, ChannelStage, ChannelForum, ChannelMedia:
			default:
				continue
			}
			cname, _ := c["name"].(string)
			if cname == "" {
				continue
			}
			topic, _ := c["topic"].(string)
			var topicPtr *string
			if topic != "" {
				topicPtr = &topic
			}
			_, _ = s.CreateChannel(g.ID, CreateChannelParams{Type: typ, Name: &cname, Topic: topicPtr})
		}
	}
	_, _ = s.DB.Exec(`UPDATE guild_templates SET usage_count = usage_count + 1 WHERE code = ?`, code)
	return g, nil
}

func mustJSON(v any) []byte { raw, _ := json.Marshal(v); return raw }

func toFloat(v any) float64 { f, _ := v.(float64); return f }

func joinComma(parts []string) string {
	out := ""
	for i, p := range parts {
		if i > 0 {
			out += ", "
		}
		out += p
	}
	return out
}

// --- scheduled events ---

// ScheduledEvent is the scheduled event object.
type ScheduledEvent struct {
	ID                 string          `json:"id"`
	GuildID            string          `json:"guild_id"`
	ChannelID          *string         `json:"channel_id"`
	CreatorID          *string         `json:"creator_id"`
	Name               string          `json:"name"`
	Description        *string         `json:"description"`
	ScheduledStartTime string          `json:"scheduled_start_time"`
	ScheduledEndTime   *string         `json:"scheduled_end_time"`
	PrivacyLevel       int             `json:"privacy_level"`
	Status             int             `json:"status"`
	EntityType         int             `json:"entity_type"`
	EntityID           *string         `json:"entity_id"`
	EntityMetadata     map[string]any  `json:"entity_metadata"`
	Creator            *model.User     `json:"creator,omitempty"`
	UserCount          int             `json:"user_count"`
	Image              *string         `json:"image"`
	RecurrenceRule     *RecurrenceRule `json:"recurrence_rule"`
}

// RecurrenceRule mirrors the documented scheduled-event recurrence subset.
type RecurrenceRule struct {
	Start      string     `json:"start"`
	End        *string    `json:"end,omitempty"`
	Frequency  int        `json:"frequency"`
	Interval   int        `json:"interval"`
	ByWeekday  []int      `json:"by_weekday,omitempty"`
	ByNWeekday []NWeekday `json:"by_n_weekday,omitempty"`
	ByMonth    []int      `json:"by_month,omitempty"`
	ByMonthDay []int      `json:"by_month_day,omitempty"`
	ByYearDay  []int      `json:"by_year_day,omitempty"`
	Count      *int       `json:"count,omitempty"`
}

// NWeekday is the nth weekday within a month.
type NWeekday struct {
	N   int `json:"n"`
	Day int `json:"day"`
}

const eventColumns = `id, guild_id, channel_id, creator_id, name, description, scheduled_start_time,
	scheduled_end_time, privacy_level, status, entity_type, entity_id, entity_metadata, image, recurrence_rule`

func scanEvent(row rowScanner) (ScheduledEvent, error) {
	var (
		e        ScheduledEvent
		channel  sql.NullString
		creator  sql.NullString
		desc     sql.NullString
		end      sql.NullString
		entityID sql.NullString
		meta     string
		image    sql.NullString
		rule     sql.NullString
	)
	if err := row.Scan(&e.ID, &e.GuildID, &channel, &creator, &e.Name, &desc, &e.ScheduledStartTime,
		&end, &e.PrivacyLevel, &e.Status, &e.EntityType, &entityID, &meta, &image, &rule); err != nil {
		return e, err
	}
	e.ChannelID = ns(channel)
	e.CreatorID = ns(creator)
	e.Description = ns(desc)
	e.ScheduledEndTime = ns(end)
	e.EntityID = ns(entityID)
	e.Image = ns(image)
	e.EntityMetadata = map[string]any{}
	_ = json.Unmarshal([]byte(meta), &e.EntityMetadata)
	if rule.Valid {
		var rr RecurrenceRule
		if json.Unmarshal([]byte(rule.String), &rr) == nil {
			e.RecurrenceRule = &rr
		}
	}
	return e, nil
}

// CreateEventParams is the create payload after validation.
type CreateEventParams struct {
	Name           string
	Description    *string
	ChannelID      *string
	Start          string
	End            *string
	PrivacyLevel   int
	EntityType     int
	EntityMetadata map[string]any
	Image          *string
	RecurrenceRule *RecurrenceRule
}

// CreateScheduledEvent inserts an event.
func (s *Store) CreateScheduledEvent(guildID, creatorID string, p CreateEventParams) (ScheduledEvent, error) {
	meta, _ := json.Marshal(p.EntityMetadata)
	rule, _ := json.Marshal(p.RecurrenceRule)
	var ruleVal any
	if p.RecurrenceRule != nil {
		ruleVal = string(rule)
	}
	id := s.NewID()
	tx, err := s.DB.Begin()
	if err != nil {
		return ScheduledEvent{}, mapErr(err)
	}
	defer tx.Rollback()
	// Lock the guild before counting so concurrent creators share the same cap.
	if _, err := tx.Exec(`UPDATE guilds SET name = name WHERE id = ?`, guildID); err != nil {
		return ScheduledEvent{}, mapErr(err)
	}
	var active int
	if err := tx.QueryRow(`SELECT COUNT(*) FROM scheduled_events WHERE guild_id = ? AND status IN (1,2)`, guildID).Scan(&active); err != nil {
		return ScheduledEvent{}, mapErr(err)
	}
	if active >= 100 {
		return ScheduledEvent{}, ErrConflict
	}
	_, err = tx.Exec(`INSERT INTO scheduled_events
		(id, guild_id, channel_id, creator_id, name, description, scheduled_start_time,
		 scheduled_end_time, privacy_level, status, entity_type, entity_id, entity_metadata, image, recurrence_rule, created_at)
		VALUES (?,?,?,?,?,?,?,?,?,1,?,?,?,?,?,?)`,
		id, guildID, p.ChannelID, creatorID, p.Name, p.Description, p.Start, p.End,
		p.PrivacyLevel, p.EntityType, p.ChannelID, string(meta), p.Image, ruleVal, now())
	if err != nil {
		return ScheduledEvent{}, mapErr(err)
	}
	if _, err := tx.Exec(`INSERT INTO scheduled_event_users (event_id, user_id) VALUES (?,?)`, id, creatorID); err != nil {
		return ScheduledEvent{}, mapErr(err)
	}
	if err := tx.Commit(); err != nil {
		return ScheduledEvent{}, mapErr(err)
	}
	return s.ScheduledEvent(id)
}

// ScheduledEvent loads one event.
func (s *Store) ScheduledEvent(id string) (ScheduledEvent, error) {
	e, err := scanEvent(s.DB.QueryRow(`SELECT `+eventColumns+` FROM scheduled_events WHERE id = ?`, id))
	if err != nil {
		return e, mapErr(err)
	}
	if e.CreatorID != nil {
		if u, err := s.UserByID(*e.CreatorID); err == nil {
			e.Creator = &u
		}
	}
	e.UserCount, err = s.EventUserCount(e.ID)
	if err != nil {
		return e, err
	}
	return e, nil
}

// ScheduledEvents lists a guild's events.
func (s *Store) ScheduledEvents(guildID string) ([]ScheduledEvent, error) {
	rows, err := s.DB.Query(`SELECT `+eventColumns+` FROM scheduled_events WHERE guild_id = ? ORDER BY id ASC`, guildID)
	if err != nil {
		return nil, mapErr(err)
	}
	var raw []ScheduledEvent
	for rows.Next() {
		e, err := scanEvent(rows)
		if err != nil {
			rows.Close()
			return nil, mapErr(err)
		}
		raw = append(raw, e)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, mapErr(err)
	}
	out := make([]ScheduledEvent, 0, len(raw))
	for i := range raw {
		count, err := s.EventUserCount(raw[i].ID)
		if err != nil {
			return nil, err
		}
		raw[i].UserCount = count
		if raw[i].CreatorID != nil {
			if u, err := s.UserByID(*raw[i].CreatorID); err == nil {
				raw[i].Creator = &u
			}
		}
		out = append(out, raw[i])
	}
	return out, nil
}

// UpdateEventParams holds nullable patches.
type UpdateEventParams struct {
	Name           *string
	Description    *string
	ChannelID      *string
	Start          *string
	End            *string
	Status         *int
	PrivacyLevel   *int
	EntityMetadata map[string]any
	RecurrenceRule *RecurrenceRule
}

// UpdateScheduledEvent applies a patch.
func (s *Store) UpdateScheduledEvent(guildID, id string, p UpdateEventParams) (ScheduledEvent, error) {
	set := []string{}
	args := []any{}
	if p.Name != nil {
		set = append(set, "name = ?")
		args = append(args, *p.Name)
	}
	if p.Description != nil {
		set = append(set, "description = ?")
		args = append(args, *p.Description)
	}
	if p.ChannelID != nil {
		set = append(set, "channel_id = ?")
		args = append(args, *p.ChannelID)
	}
	if p.Start != nil {
		set = append(set, "scheduled_start_time = ?")
		args = append(args, *p.Start)
	}
	if p.End != nil {
		set = append(set, "scheduled_end_time = ?")
		args = append(args, *p.End)
	}
	if p.Status != nil {
		set = append(set, "status = ?")
		args = append(args, *p.Status)
	}
	if p.PrivacyLevel != nil {
		set = append(set, "privacy_level = ?")
		args = append(args, *p.PrivacyLevel)
	}
	if p.EntityMetadata != nil {
		raw, _ := json.Marshal(p.EntityMetadata)
		set = append(set, "entity_metadata = ?")
		args = append(args, string(raw))
	}
	if p.RecurrenceRule != nil {
		raw, _ := json.Marshal(p.RecurrenceRule)
		set = append(set, "recurrence_rule = ?")
		args = append(args, string(raw))
	}
	if len(set) == 0 {
		return s.ScheduledEvent(id)
	}
	args = append(args, guildID, id)
	where := ` WHERE guild_id = ? AND id = ?`
	if p.Status != nil {
		// Validate at the write boundary too: concurrent requests cannot revive a
		// terminal event after the handler's earlier read.
		where += ` AND (status = ?`
		args = append(args, *p.Status)
		switch *p.Status {
		case 2, 4:
			where += ` OR status = 1`
		case 3:
			where += ` OR status = 2`
		}
		where += `)`
	}
	res, err := s.DB.Exec(`UPDATE scheduled_events SET `+joinComma(set)+where, args...)
	if err != nil {
		return ScheduledEvent{}, mapErr(err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ScheduledEvent{}, ErrNotFound
	}
	return s.ScheduledEvent(id)
}

// DeleteScheduledEvent removes an event.
func (s *Store) DeleteScheduledEvent(guildID, id string) error {
	res, err := s.DB.Exec(`DELETE FROM scheduled_events WHERE guild_id = ? AND id = ?`, guildID, id)
	if err != nil {
		return mapErr(err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// --- stage instances ---

// StageInstance is the stage instance object.
type StageInstance struct {
	ID                    string  `json:"id"`
	GuildID               string  `json:"guild_id"`
	ChannelID             string  `json:"channel_id"`
	Topic                 string  `json:"topic"`
	PrivacyLevel          int     `json:"privacy_level"`
	DiscoverableDisabled  bool    `json:"discoverable_disabled"`
	GuildScheduledEventID *string `json:"guild_scheduled_event_id"`
}

// CreateStageInstance inserts a stage instance.
func (s *Store) CreateStageInstance(guildID, channelID, topic string, privacy int, eventID *string) (StageInstance, error) {
	id := s.NewID()
	_, err := s.DB.Exec(`INSERT INTO stage_instances
		(id, guild_id, channel_id, topic, privacy_level, guild_scheduled_event_id, created_at)
		VALUES (?,?,?,?,?,?,?)`, id, guildID, channelID, topic, privacy, eventID, now())
	if err != nil {
		return StageInstance{}, mapErr(err)
	}
	return s.StageInstance(channelID)
}

// StageInstance loads by channel id.
func (s *Store) StageInstance(channelID string) (StageInstance, error) {
	var (
		si    StageInstance
		event sql.NullString
	)
	err := s.DB.QueryRow(`SELECT id, guild_id, channel_id, topic, privacy_level, discoverable_disabled, guild_scheduled_event_id
		FROM stage_instances WHERE channel_id = ?`, channelID).
		Scan(&si.ID, &si.GuildID, &si.ChannelID, &si.Topic, &si.PrivacyLevel, &si.DiscoverableDisabled, &event)
	si.GuildScheduledEventID = ns(event)
	return si, mapErr(err)
}

// UpdateStageInstance patches topic/privacy.
func (s *Store) UpdateStageInstance(channelID string, topic *string, privacy *int) (StageInstance, error) {
	set := []string{}
	args := []any{}
	if topic != nil {
		set = append(set, "topic = ?")
		args = append(args, *topic)
	}
	if privacy != nil {
		set = append(set, "privacy_level = ?")
		args = append(args, *privacy)
	}
	if len(set) == 0 {
		return s.StageInstance(channelID)
	}
	args = append(args, channelID)
	if _, err := s.DB.Exec(`UPDATE stage_instances SET `+joinComma(set)+` WHERE channel_id = ?`, args...); err != nil {
		return StageInstance{}, mapErr(err)
	}
	return s.StageInstance(channelID)
}

// DeleteStageInstance removes by channel.
func (s *Store) DeleteStageInstance(channelID string) error {
	res, err := s.DB.Exec(`DELETE FROM stage_instances WHERE channel_id = ?`, channelID)
	if err != nil {
		return mapErr(err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// --- integrations ---

// Integration type ids follow the public API's string type: third-party
// integrations are 1 (twitch) and 2 (youtube), bot applications are 3.
const (
	IntegrationTwitch  = 1
	IntegrationYouTube = 2
	IntegrationDiscord = 3
)

// Integration is the integration object. Type is serialized as the documented
// string value while the column stays an integer for legacy rows.
type Integration struct {
	ID                string         `json:"id"`
	Name              string         `json:"name"`
	Type              string         `json:"type"`
	Enabled           bool           `json:"enabled"`
	Syncing           *bool          `json:"syncing,omitempty"`
	RoleID            *string        `json:"role_id,omitempty"`
	EnableEmoticons   *bool          `json:"enable_emoticons,omitempty"`
	ExpireBehavior    *int           `json:"expire_behavior,omitempty"`
	ExpireGracePeriod *int           `json:"expire_grace_period,omitempty"`
	SyncedAt          *string        `json:"synced_at,omitempty"`
	SubscriberCount   *int           `json:"subscriber_count,omitempty"`
	Revoked           *bool          `json:"revoked,omitempty"`
	User              *model.User    `json:"user,omitempty"`
	Account           map[string]any `json:"account"`
	Application       map[string]any `json:"application,omitempty"`
	Scopes            []string       `json:"scopes,omitempty"`
	ChannelID         *string        `json:"-"`
	TypeID            int            `json:"-"`
}

func integrationTypeName(t int) string {
	switch t {
	case IntegrationTwitch:
		return "twitch"
	case IntegrationYouTube:
		return "youtube"
	case 4:
		return "guild_subscription"
	default:
		return "discord"
	}
}

// UpsertBotIntegration records a guild's bot integration with the scopes the
// installing authorization actually granted.
func (s *Store) UpsertBotIntegration(guildID, appID, name, botUserID string, scopes []string) error {
	raw, _ := json.Marshal(scopes)
	_, err := s.DB.Exec(`INSERT INTO integrations
		(guild_id, id, name, type, enabled, syncing, account_name, account_id, application_name, scopes, created_at)
		VALUES (?,?,?,3,1,0,?,?,?,?,?)
		ON CONFLICT(guild_id, id) DO UPDATE SET name = excluded.name, enabled = 1,
			scopes = excluded.scopes`,
		guildID, appID, name, name, botUserID, name, string(raw), now())
	return mapErr(err)
}

// CreateIntegration enables a Twitch/YouTube integration. The integration id
// is the provider account id, matching the documented object.
func (s *Store) CreateIntegration(guildID string, typ int, accountID, accountName, userID string) (Integration, error) {
	if typ != IntegrationTwitch && typ != IntegrationYouTube {
		return Integration{}, ErrNotFound
	}
	name := "Twitch"
	if typ == IntegrationYouTube {
		name = "YouTube"
	}
	if accountName == "" {
		accountName = accountID
	}
	ts := now()
	_, err := s.DB.Exec(`INSERT INTO integrations
		(guild_id, id, name, type, enabled, syncing, account_name, account_id,
		 application_name, user_id, synced_at, subscriber_count, created_at)
		VALUES (?,?,?,?,1,0,?,?,?,?,?,0,?)
		ON CONFLICT(guild_id, id) DO UPDATE SET type = excluded.type,
			account_name = excluded.account_name, account_id = excluded.account_id,
			user_id = excluded.user_id, revoked = 0`,
		guildID, accountID, name, typ, accountName, accountID, name, userID, ts, ts)
	if err != nil {
		return Integration{}, mapErr(err)
	}
	return s.Integration(guildID, accountID)
}

// CreateChannelIntegration attaches a third-party integration to a private
// channel instead of the whole guild.
func (s *Store) CreateChannelIntegration(guildID, channelID string, typ int, accountID, accountName, userID string) (Integration, error) {
	in, err := s.CreateIntegration(guildID, typ, accountID, accountName, userID)
	if err != nil {
		return Integration{}, err
	}
	if _, err := s.DB.Exec(`UPDATE integrations SET channel_id = ? WHERE guild_id = ? AND id = ?`,
		channelID, guildID, accountID); err != nil {
		return Integration{}, mapErr(err)
	}
	in.ChannelID = &channelID
	return in, nil
}

// Integrations lists a guild's integrations, capped at the documented 50.
func (s *Store) Integrations(guildID string) ([]Integration, error) {
	rows, err := s.DB.Query(`SELECT id, name, type, enabled, syncing, role_id, enable_emoticons,
		expire_behavior, expire_grace_period, account_name, account_id, application_name,
		subscriber_count, revoked, synced_at, scopes, user_id, channel_id, created_at
		FROM integrations WHERE guild_id = ? AND channel_id IS NULL ORDER BY created_at ASC, id ASC LIMIT 50`, guildID)
	if err != nil {
		return nil, mapErr(err)
	}
	return s.scanIntegrations(rows)
}

// ChannelIntegrations lists integrations attached to a private channel.
func (s *Store) ChannelIntegrations(channelID string) ([]Integration, error) {
	rows, err := s.DB.Query(`SELECT id, name, type, enabled, syncing, role_id, enable_emoticons,
		expire_behavior, expire_grace_period, account_name, account_id, application_name,
		subscriber_count, revoked, synced_at, scopes, user_id, channel_id, created_at
		FROM integrations WHERE channel_id = ? ORDER BY created_at ASC, id ASC LIMIT 50`, channelID)
	if err != nil {
		return nil, mapErr(err)
	}
	return s.scanIntegrations(rows)
}

func (s *Store) scanIntegrations(rows *sql.Rows) ([]Integration, error) {
	defer rows.Close()
	out := []Integration{}
	for rows.Next() {
		var (
			in       Integration
			typ      int
			syncing  bool
			role     sql.NullString
			emote    bool
			behavior int
			grace    int
			accName  sql.NullString
			accID    sql.NullString
			appName  sql.NullString
			subs     int
			revoked  bool
			syncedAt sql.NullInt64
			scopes   string
			userID   sql.NullString
			channel  sql.NullString
			created  int64
		)
		if err := rows.Scan(&in.ID, &in.Name, &typ, &in.Enabled, &syncing, &role, &emote,
			&behavior, &grace, &accName, &accID, &appName, &subs, &revoked, &syncedAt,
			&scopes, &userID, &channel, &created); err != nil {
			return nil, mapErr(err)
		}
		in.TypeID = typ
		in.Type = integrationTypeName(typ)
		in.RoleID = ns(role)
		in.Account = map[string]any{"name": stringOr(accName, ""), "id": stringOr(accID, "")}
		in.ChannelID = ns(channel)
		if typ == IntegrationDiscord || typ == 4 {
			in.Application = map[string]any{"id": in.ID, "name": stringOr(appName, in.Name)}
			var list []string
			_ = json.Unmarshal([]byte(scopes), &list)
			if list == nil {
				list = []string{}
			}
			in.Scopes = list
			if userID.Valid {
				if u, err := s.UserByID(userID.String); err == nil {
					in.User = &u
				}
			}
		} else {
			v := syncing
			in.Syncing = &v
			em := emote
			in.EnableEmoticons = &em
			be := behavior
			in.ExpireBehavior = &be
			gr := grace
			in.ExpireGracePeriod = &gr
			sc := subs
			in.SubscriberCount = &sc
			rv := revoked
			in.Revoked = &rv
			when := created
			if syncedAt.Valid {
				when = syncedAt.Int64
			}
			at := iso(when)
			in.SyncedAt = &at
		}
		out = append(out, in)
	}
	if err := rows.Err(); err != nil {
		return nil, mapErr(err)
	}
	return out, nil
}

// Integration loads one.
func (s *Store) Integration(guildID, id string) (Integration, error) {
	list, err := s.Integrations(guildID)
	if err != nil {
		return Integration{}, err
	}
	for _, in := range list {
		if in.ID == id {
			return in, nil
		}
	}
	if rows, err := s.ChannelIntegrationsByGuild(guildID, id); err == nil {
		return rows, nil
	}
	return Integration{}, ErrNotFound
}

// ChannelIntegrationsByGuild resolves an integration that is scoped to a
// channel of the guild.
func (s *Store) ChannelIntegrationsByGuild(guildID, id string) (Integration, error) {
	rows, err := s.DB.Query(`SELECT id, name, type, enabled, syncing, role_id, enable_emoticons,
		expire_behavior, expire_grace_period, account_name, account_id, application_name,
		subscriber_count, revoked, synced_at, scopes, user_id, channel_id, created_at
		FROM integrations WHERE guild_id = ? AND id = ?`, guildID, id)
	if err != nil {
		return Integration{}, mapErr(err)
	}
	list, err := s.scanIntegrations(rows)
	if err != nil || len(list) == 0 {
		return Integration{}, ErrNotFound
	}
	return list[0], nil
}

// SyncIntegration recomputes subscriber state from the guild's own membership
// and marks the integration synced.
func (s *Store) SyncIntegration(guildID, id string) (Integration, error) {
	in, err := s.Integration(guildID, id)
	if err != nil {
		return Integration{}, err
	}
	count := 0
	if in.RoleID != nil {
		rows, err := s.DB.Query(`SELECT role_ids FROM guild_members WHERE guild_id = ?`, guildID)
		if err != nil {
			return Integration{}, mapErr(err)
		}
		for rows.Next() {
			var raw string
			if err := rows.Scan(&raw); err != nil {
				rows.Close()
				return Integration{}, mapErr(err)
			}
			var roles []string
			if json.Unmarshal([]byte(raw), &roles) == nil {
				for _, r := range roles {
					if r == *in.RoleID {
						count++
						break
					}
				}
			}
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return Integration{}, mapErr(err)
		}
	}
	if _, err := s.DB.Exec(`UPDATE integrations SET syncing = 0, revoked = 0, synced_at = ?,
		subscriber_count = ? WHERE guild_id = ? AND id = ?`, now(), count, guildID, id); err != nil {
		return Integration{}, mapErr(err)
	}
	return s.Integration(guildID, id)
}

// UpdateIntegration patches emoticon/expiry settings.
func (s *Store) UpdateIntegration(guildID, id string, emoticons *bool, expireBehavior, grace *int) (Integration, error) {
	set := []string{}
	args := []any{}
	if emoticons != nil {
		set = append(set, "enable_emoticons = ?")
		args = append(args, *emoticons)
	}
	if expireBehavior != nil {
		set = append(set, "expire_behavior = ?")
		args = append(args, *expireBehavior)
	}
	if grace != nil {
		set = append(set, "expire_grace_period = ?")
		args = append(args, *grace)
	}
	if len(set) == 0 {
		return s.Integration(guildID, id)
	}
	args = append(args, guildID, id)
	if _, err := s.DB.Exec(`UPDATE integrations SET `+joinComma(set)+` WHERE guild_id = ? AND id = ?`, args...); err != nil {
		return Integration{}, mapErr(err)
	}
	return s.Integration(guildID, id)
}

// DeleteIntegration removes the row and reports the bot account id. Webhooks
// created by the application are removed with it.
func (s *Store) DeleteIntegration(guildID, id string) (string, error) {
	in, err := s.Integration(guildID, id)
	if err != nil {
		return "", err
	}
	botID := ""
	if in.TypeID == IntegrationDiscord {
		botID, _ = in.Account["id"].(string)
		_, _ = s.DB.Exec(`DELETE FROM webhooks WHERE guild_id = ? AND application_id = ?`, guildID, id)
	}
	if _, err := s.DB.Exec(`DELETE FROM integrations WHERE guild_id = ? AND id = ?`, guildID, id); err != nil {
		return "", mapErr(err)
	}
	return botID, nil
}

// GuildsWithIntegrationApplications maps guild ids to the application ids of
// Discord integrations that have commands registered.
func (s *Store) GuildsWithIntegrationApplications(userID string) (map[string][]string, error) {
	rows, err := s.DB.Query(`SELECT i.guild_id, i.id FROM integrations i
		WHERE i.type = 3 AND i.guild_id IN (SELECT guild_id FROM guild_members WHERE user_id = ?)
		AND EXISTS (SELECT 1 FROM application_commands c WHERE c.application_id = i.id AND c.guild_id = i.guild_id)`,
		userID)
	if err != nil {
		return nil, mapErr(err)
	}
	defer rows.Close()
	out := map[string][]string{}
	for rows.Next() {
		var guildID, appID string
		if err := rows.Scan(&guildID, &appID); err != nil {
			return nil, mapErr(err)
		}
		out[guildID] = append(out[guildID], appID)
	}
	return out, mapErr(rows.Err())
}

// ApplicationsWithCommands returns application ids that have commands
// registered globally or in the guild.
func (s *Store) ApplicationsWithCommands(guildID string) (map[string]bool, error) {
	rows, err := s.DB.Query(`SELECT DISTINCT application_id FROM application_commands
		WHERE guild_id IS NULL OR guild_id = ?`, guildID)
	if err != nil {
		return nil, mapErr(err)
	}
	defer rows.Close()
	out := map[string]bool{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, mapErr(err)
		}
		out[id] = true
	}
	return out, mapErr(rows.Err())
}

// DeleteChannelIntegration removes a channel-scoped integration.
func (s *Store) DeleteChannelIntegration(channelID, id string) error {
	res, err := s.DB.Exec(`DELETE FROM integrations WHERE channel_id = ? AND id = ?`, channelID, id)
	if err != nil {
		return mapErr(err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}
