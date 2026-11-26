package store

import (
	"database/sql"
	"encoding/json"
)

// --- application command permissions ---

// CommandPermissionEntry is one role/user/channel override.
type CommandPermissionEntry struct {
	ID         string `json:"id"`
	Type       int    `json:"type"`
	Permission bool   `json:"permission"`
}

// CommandPermissions is the documented permission object for one command.
type CommandPermissions struct {
	ID            string                   `json:"id"`
	ApplicationID string                   `json:"application_id"`
	GuildID       string                   `json:"guild_id"`
	Permissions   []CommandPermissionEntry `json:"permissions"`
}

// ListCommandPermissions returns one entry per command that has overrides.
func (s *Store) ListCommandPermissions(appID, guildID string) ([]CommandPermissions, error) {
	rows, err := s.DB.Query(`SELECT command_id, target_id, type, permission
		FROM command_permissions WHERE application_id = ? AND guild_id = ?
		ORDER BY command_id, target_id`, appID, guildID)
	if err != nil {
		return nil, mapErr(err)
	}
	type rowData struct {
		command, target string
		typ             int
		allowed         bool
	}
	var raw []rowData
	for rows.Next() {
		var r rowData
		if err := rows.Scan(&r.command, &r.target, &r.typ, &r.allowed); err != nil {
			rows.Close()
			return nil, mapErr(err)
		}
		raw = append(raw, r)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, mapErr(err)
	}
	byCommand := map[string]*CommandPermissions{}
	order := []string{}
	for _, r := range raw {
		cp := byCommand[r.command]
		if cp == nil {
			cp = &CommandPermissions{ID: r.command, ApplicationID: appID, GuildID: guildID, Permissions: []CommandPermissionEntry{}}
			byCommand[r.command] = cp
			order = append(order, r.command)
		}
		cp.Permissions = append(cp.Permissions, CommandPermissionEntry{ID: r.target, Type: r.typ, Permission: r.allowed})
	}
	out := make([]CommandPermissions, 0, len(order))
	for _, id := range order {
		out = append(out, *byCommand[id])
	}
	return out, nil
}

// CommandPermissionsFor returns overrides for one command (empty when none).
func (s *Store) CommandPermissionsFor(appID, guildID, commandID string) (CommandPermissions, error) {
	cp := CommandPermissions{ID: commandID, ApplicationID: appID, GuildID: guildID, Permissions: []CommandPermissionEntry{}}
	rows, err := s.DB.Query(`SELECT target_id, type, permission FROM command_permissions
		WHERE application_id = ? AND guild_id = ? AND command_id = ? ORDER BY target_id`,
		appID, guildID, commandID)
	if err != nil {
		return cp, mapErr(err)
	}
	defer rows.Close()
	for rows.Next() {
		var e CommandPermissionEntry
		if err := rows.Scan(&e.ID, &e.Type, &e.Permission); err != nil {
			return cp, mapErr(err)
		}
		cp.Permissions = append(cp.Permissions, e)
	}
	return cp, mapErr(rows.Err())
}

// PutCommandPermissions replaces the override set for one command.
func (s *Store) PutCommandPermissions(appID, guildID, commandID string, entries []CommandPermissionEntry) (CommandPermissions, error) {
	tx, err := s.DB.Begin()
	if err != nil {
		return CommandPermissions{}, mapErr(err)
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`DELETE FROM command_permissions WHERE application_id = ? AND guild_id = ? AND command_id = ?`,
		appID, guildID, commandID); err != nil {
		return CommandPermissions{}, mapErr(err)
	}
	for _, e := range entries {
		if _, err := tx.Exec(`INSERT INTO command_permissions
			(application_id, guild_id, command_id, target_id, type, permission)
			VALUES (?,?,?,?,?,?)`, appID, guildID, commandID, e.ID, e.Type, e.Permission); err != nil {
			return CommandPermissions{}, mapErr(err)
		}
	}
	if err := tx.Commit(); err != nil {
		return CommandPermissions{}, mapErr(err)
	}
	return s.CommandPermissionsFor(appID, guildID, commandID)
}

// --- onboarding and welcome screen ---

// Onboarding is the onboarding object.
type Onboarding struct {
	GuildID           string          `json:"guild_id"`
	Prompts           json.RawMessage `json:"prompts"`
	DefaultChannelIDs []string        `json:"default_channel_ids"`
	Enabled           bool            `json:"enabled"`
	Mode              int             `json:"mode"`
}

// GetOnboarding returns stored onboarding or the documented default.
func (s *Store) GetOnboarding(guildID string) (Onboarding, error) {
	o := Onboarding{GuildID: guildID, Prompts: json.RawMessage("[]"), DefaultChannelIDs: []string{}}
	var prompts, defaults string
	err := s.DB.QueryRow(`SELECT prompts, default_channel_ids, enabled, mode FROM guild_onboarding WHERE guild_id = ?`, guildID).
		Scan(&prompts, &defaults, &o.Enabled, &o.Mode)
	if err != nil {
		if err == sql.ErrNoRows {
			return o, nil
		}
		return o, mapErr(err)
	}
	o.Prompts = json.RawMessage(prompts)
	o.DefaultChannelIDs = parseJSONStrings(defaults)
	return o, nil
}

// PutOnboarding upserts onboarding.
func (s *Store) PutOnboarding(guildID string, prompts json.RawMessage, defaults []string, enabled bool, mode int) (Onboarding, error) {
	if len(prompts) == 0 {
		prompts = json.RawMessage("[]")
	}
	_, err := s.DB.Exec(`INSERT INTO guild_onboarding (guild_id, prompts, default_channel_ids, enabled, mode)
		VALUES (?,?,?,?,?)
		ON CONFLICT(guild_id) DO UPDATE SET prompts = excluded.prompts,
			default_channel_ids = excluded.default_channel_ids,
			enabled = excluded.enabled, mode = excluded.mode`,
		guildID, string(prompts), jsonStrings(defaults), enabled, mode)
	if err != nil {
		return Onboarding{}, mapErr(err)
	}
	return s.GetOnboarding(guildID)
}

// WelcomeScreen is the welcome screen object.
type WelcomeScreen struct {
	Description     *string         `json:"description"`
	WelcomeChannels json.RawMessage `json:"welcome_channels"`
	Enabled         bool            `json:"enabled"`
}

// GetWelcomeScreen returns the stored screen or an empty default.
func (s *Store) GetWelcomeScreen(guildID string) (WelcomeScreen, error) {
	ws := WelcomeScreen{WelcomeChannels: json.RawMessage("[]")}
	var desc sql.NullString
	var channels string
	err := s.DB.QueryRow(`SELECT description, welcome_channels, enabled FROM welcome_screens WHERE guild_id = ?`, guildID).
		Scan(&desc, &channels, &ws.Enabled)
	if err != nil {
		if err == sql.ErrNoRows {
			return ws, nil
		}
		return ws, mapErr(err)
	}
	ws.Description = ns(desc)
	ws.WelcomeChannels = json.RawMessage(channels)
	return ws, nil
}

// PutWelcomeScreen upserts the welcome screen.
func (s *Store) PutWelcomeScreen(guildID string, description *string, channels json.RawMessage, enabled bool) (WelcomeScreen, error) {
	if len(channels) == 0 {
		channels = json.RawMessage("[]")
	}
	_, err := s.DB.Exec(`INSERT INTO welcome_screens (guild_id, description, welcome_channels, enabled)
		VALUES (?,?,?,?)
		ON CONFLICT(guild_id) DO UPDATE SET description = excluded.description,
			welcome_channels = excluded.welcome_channels, enabled = excluded.enabled`,
		guildID, description, string(channels), enabled)
	if err != nil {
		return WelcomeScreen{}, mapErr(err)
	}
	return s.GetWelcomeScreen(guildID)
}
