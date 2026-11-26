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
	ID                 string         `json:"id"`
	GuildID            string         `json:"guild_id"`
	ChannelID          *string        `json:"channel_id"`
	CreatorID          *string        `json:"creator_id"`
	Name               string         `json:"name"`
	Description        *string        `json:"description"`
	ScheduledStartTime string         `json:"scheduled_start_time"`
	ScheduledEndTime   *string        `json:"scheduled_end_time"`
	PrivacyLevel       int            `json:"privacy_level"`
	Status             int            `json:"status"`
	EntityType         int            `json:"entity_type"`
	EntityID           *string        `json:"entity_id"`
	EntityMetadata     map[string]any `json:"entity_metadata"`
	Creator            *model.User    `json:"creator,omitempty"`
	UserCount          int            `json:"user_count"`
	Image              *string        `json:"image"`
}

const eventColumns = `id, guild_id, channel_id, creator_id, name, description, scheduled_start_time,
	scheduled_end_time, privacy_level, status, entity_type, entity_id, entity_metadata, image`

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
	)
	if err := row.Scan(&e.ID, &e.GuildID, &channel, &creator, &e.Name, &desc, &e.ScheduledStartTime,
		&end, &e.PrivacyLevel, &e.Status, &e.EntityType, &entityID, &meta, &image); err != nil {
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
}

// CreateScheduledEvent inserts an event.
func (s *Store) CreateScheduledEvent(guildID, creatorID string, p CreateEventParams) (ScheduledEvent, error) {
	meta, _ := json.Marshal(p.EntityMetadata)
	id := s.NewID()
	_, err := s.DB.Exec(`INSERT INTO scheduled_events
		(id, guild_id, channel_id, creator_id, name, description, scheduled_start_time,
		 scheduled_end_time, privacy_level, status, entity_type, entity_id, entity_metadata, image, created_at)
		VALUES (?,?,?,?,?,?,?,?,?,1,?,?,?,?,?)`,
		id, guildID, p.ChannelID, creatorID, p.Name, p.Description, p.Start, p.End,
		p.PrivacyLevel, p.EntityType, p.ChannelID, string(meta), p.Image, now())
	if err != nil {
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
	if len(set) == 0 {
		return s.ScheduledEvent(id)
	}
	args = append(args, guildID, id)
	res, err := s.DB.Exec(`UPDATE scheduled_events SET `+joinComma(set)+` WHERE guild_id = ? AND id = ?`, args...)
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

// Integration is the integration object.
type Integration struct {
	ID                string         `json:"id"`
	Name              string         `json:"name"`
	Type              int            `json:"type"`
	Enabled           bool           `json:"enabled"`
	Syncing           bool           `json:"syncing"`
	RoleID            *string        `json:"role_id"`
	EnableEmoticons   bool           `json:"enable_emoticons"`
	ExpireBehavior    int            `json:"expire_behavior"`
	ExpireGracePeriod int            `json:"expire_grace_period"`
	User              *model.User    `json:"user,omitempty"`
	Account           map[string]any `json:"account"`
	Application       map[string]any `json:"application,omitempty"`
	SyncedAt          string         `json:"synced_at"`
}

// UpsertBotIntegration records a guild's bot integration.
func (s *Store) UpsertBotIntegration(guildID, appID, name, botUserID string) error {
	_, err := s.DB.Exec(`INSERT INTO integrations
		(guild_id, id, name, type, enabled, syncing, account_name, account_id, application_name, created_at)
		VALUES (?,?,?,3,1,0,?,?,?,?)
		ON CONFLICT(guild_id, id) DO UPDATE SET name = excluded.name, enabled = 1`,
		guildID, appID, name, name, botUserID, name, now())
	return mapErr(err)
}

// CreateIntegration adds a Twitch/YouTube style integration entry.
func (s *Store) CreateIntegration(guildID string, typ int, name, accountName, accountID string) (Integration, error) {
	id := s.NewID()
	_, err := s.DB.Exec(`INSERT INTO integrations
		(guild_id, id, name, type, enabled, syncing, account_name, account_id, created_at)
		VALUES (?,?,?,?,1,0,?,?,?)`,
		guildID, id, name, typ, accountName, accountID, now())
	if err != nil {
		return Integration{}, mapErr(err)
	}
	return s.Integration(guildID, id)
}

// Integrations lists a guild's integrations.
func (s *Store) Integrations(guildID string) ([]Integration, error) {
	rows, err := s.DB.Query(`SELECT id, name, type, enabled, syncing, role_id, enable_emoticons,
		expire_behavior, expire_grace_period, account_name, account_id, application_name, created_at
		FROM integrations WHERE guild_id = ? ORDER BY id ASC`, guildID)
	if err != nil {
		return nil, mapErr(err)
	}
	var raw []Integration
	for rows.Next() {
		var (
			in      Integration
			role    sql.NullString
			accName sql.NullString
			accID   sql.NullString
			appName sql.NullString
			created int64
		)
		if err := rows.Scan(&in.ID, &in.Name, &in.Type, &in.Enabled, &in.Syncing, &role,
			&in.EnableEmoticons, &in.ExpireBehavior, &in.ExpireGracePeriod, &accName, &accID,
			&appName, &created); err != nil {
			rows.Close()
			return nil, mapErr(err)
		}
		in.RoleID = ns(role)
		in.Account = map[string]any{"name": stringOr(accName, ""), "id": stringOr(accID, "")}
		in.Application = map[string]any{"id": in.ID, "name": stringOr(appName, in.Name)}
		in.SyncedAt = iso(created)
		raw = append(raw, in)
	}
	rows.Close()
	return raw, mapErr(rows.Err())
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
	return Integration{}, ErrNotFound
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

// DeleteIntegration removes the row and reports the bot account id.
func (s *Store) DeleteIntegration(guildID, id string) (string, error) {
	in, err := s.Integration(guildID, id)
	if err != nil {
		return "", err
	}
	botID, _ := in.Account["id"].(string)
	if _, err := s.DB.Exec(`DELETE FROM integrations WHERE guild_id = ? AND id = ?`, guildID, id); err != nil {
		return "", mapErr(err)
	}
	return botID, nil
}
