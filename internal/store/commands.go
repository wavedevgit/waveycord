package store

import (
	"database/sql"
	"encoding/json"
	"strings"

	"github.com/wavedevgit/waveycord/internal/sqlx"
)

// ApplicationCommand is the documented command object.
type ApplicationCommand struct {
	ID                       string          `json:"id"`
	Type                     int             `json:"type"`
	ApplicationID            string          `json:"application_id"`
	GuildID                  *string         `json:"guild_id,omitempty"`
	Name                     string          `json:"name"`
	Description              string          `json:"description"`
	Options                  json.RawMessage `json:"options,omitempty"`
	DefaultMemberPermissions *string         `json:"default_member_permissions"`
	DMPermission             *bool           `json:"dm_permission,omitempty"`
	NSFW                     bool            `json:"nsfw,omitempty"`
	Version                  string          `json:"version"`
	Contexts                 []int           `json:"contexts,omitempty"`
	IntegrationTypes         []int           `json:"integration_types,omitempty"`
}

// CommandInput is one command in a bulk overwrite.
type CommandInput struct {
	Name                     string          `json:"name"`
	Description              string          `json:"description"`
	Type                     int             `json:"type"`
	Options                  json.RawMessage `json:"options"`
	DefaultMemberPermissions *string         `json:"default_member_permissions"`
	DMPermission             *bool           `json:"dm_permission"`
	NSFW                     bool            `json:"nsfw"`
}

const commandColumns = `id, application_id, guild_id, name, description, type, options,
	default_member_permissions, dm_permission, nsfw, version`

func scanCommand(row rowScanner) (ApplicationCommand, error) {
	var (
		c       ApplicationCommand
		guild   sql.NullString
		options string
		perms   sql.NullString
		dm      bool
	)
	err := row.Scan(&c.ID, &c.ApplicationID, &guild, &c.Name, &c.Description, &c.Type,
		&options, &perms, &dm, &c.NSFW, &c.Version)
	c.GuildID = ns(guild)
	c.DefaultMemberPermissions = ns(perms)
	c.DMPermission = &dm
	c.Options = json.RawMessage(options)
	if len(c.Options) == 0 {
		c.Options = json.RawMessage("[]")
	}
	return c, err
}

// ListCommands returns global (guildID empty) or guild commands.
func (s *Store) ListCommands(appID, guildID string) ([]ApplicationCommand, error) {
	var rows *sql.Rows
	var err error
	if guildID == "" {
		rows, err = s.DB.Query(`SELECT `+commandColumns+` FROM application_commands
			WHERE application_id = ? AND guild_id IS NULL ORDER BY id ASC`, appID)
	} else {
		rows, err = s.DB.Query(`SELECT `+commandColumns+` FROM application_commands
			WHERE application_id = ? AND guild_id = ? ORDER BY id ASC`, appID, guildID)
	}
	if err != nil {
		return nil, mapErr(err)
	}
	defer rows.Close()
	out := []ApplicationCommand{}
	for rows.Next() {
		c, err := scanCommand(rows)
		if err != nil {
			return nil, mapErr(err)
		}
		out = append(out, c)
	}
	return out, mapErr(rows.Err())
}

// CommandByName resolves an invocation target.
func (s *Store) CommandByName(appID, guildID, name string) (ApplicationCommand, error) {
	var (
		row *sql.Row
	)
	if guildID != "" {
		row = s.DB.QueryRow(`SELECT `+commandColumns+` FROM application_commands
			WHERE application_id = ? AND guild_id = ? AND name = ?`, appID, guildID, name)
		c, err := scanCommand(row)
		if err == nil {
			return c, nil
		}
	}
	row = s.DB.QueryRow(`SELECT `+commandColumns+` FROM application_commands
		WHERE application_id = ? AND guild_id IS NULL AND name = ?`, appID, name)
	c, err := scanCommand(row)
	return c, mapErr(err)
}

// ReplaceCommands performs the documented bulk overwrite for one scope.
func (s *Store) ReplaceCommands(appID, guildID string, in []CommandInput) ([]ApplicationCommand, error) {
	tx, err := s.DB.Begin()
	if err != nil {
		return nil, mapErr(err)
	}
	defer tx.Rollback()
	if guildID == "" {
		if _, err := tx.Exec(`DELETE FROM application_commands WHERE application_id = ? AND guild_id IS NULL`, appID); err != nil {
			return nil, mapErr(err)
		}
	} else {
		if _, err := tx.Exec(`DELETE FROM application_commands WHERE application_id = ? AND guild_id = ?`, appID, guildID); err != nil {
			return nil, mapErr(err)
		}
	}
	for _, c := range in {
		if _, err := insertCommand(tx, s, appID, guildID, c); err != nil {
			return nil, err
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, mapErr(err)
	}
	return s.ListCommands(appID, guildID)
}

// CreateCommand inserts one command.
func (s *Store) CreateCommand(appID, guildID string, in CommandInput) (ApplicationCommand, error) {
	existing, err := s.ListCommands(appID, guildID)
	if err != nil {
		return ApplicationCommand{}, err
	}
	for _, c := range existing {
		if c.Name == in.Name {
			return ApplicationCommand{}, ErrConflict
		}
	}
	tx, err := s.DB.Begin()
	if err != nil {
		return ApplicationCommand{}, mapErr(err)
	}
	defer tx.Rollback()
	id, err := insertCommand(tx, s, appID, guildID, in)
	if err != nil {
		return ApplicationCommand{}, err
	}
	if err := tx.Commit(); err != nil {
		return ApplicationCommand{}, mapErr(err)
	}
	return s.Command(id)
}

func insertCommand(tx *sqlx.Tx, s *Store, appID, guildID string, in CommandInput) (string, error) {
	if in.Type == 0 {
		in.Type = 1
	}
	if in.Description == "" {
		in.Description = "No description"
	}
	options := in.Options
	if len(options) == 0 {
		options = json.RawMessage("[]")
	}
	dm := true
	if in.DMPermission != nil {
		dm = *in.DMPermission
	}
	id := s.NewID()
	_, err := tx.Exec(`INSERT INTO application_commands
		(id, application_id, guild_id, name, description, type, options,
		 default_member_permissions, dm_permission, nsfw, version, created_at)
		VALUES (?,?,?,?,?,?,?,?,?,?,?,?)`,
		id, appID, nullIfEmpty(guildID), in.Name, in.Description, in.Type, string(options),
		in.DefaultMemberPermissions, dm, in.NSFW, s.NewID(), now())
	if err != nil {
		return "", mapErr(err)
	}
	return id, nil
}

// Command loads one command by id.
func (s *Store) Command(id string) (ApplicationCommand, error) {
	c, err := scanCommand(s.DB.QueryRow(`SELECT `+commandColumns+` FROM application_commands WHERE id = ?`, id))
	return c, mapErr(err)
}

// UpdateCommand patches the editable fields.
func (s *Store) UpdateCommand(id string, in CommandInput) (ApplicationCommand, error) {
	set := []string{}
	args := []any{}
	if in.Name != "" {
		set = append(set, "name = ?")
		args = append(args, in.Name)
	}
	if in.Description != "" {
		set = append(set, "description = ?")
		args = append(args, in.Description)
	}
	if len(in.Options) > 0 {
		set = append(set, "options = ?")
		args = append(args, string(in.Options))
	}
	if in.DefaultMemberPermissions != nil {
		set = append(set, "default_member_permissions = ?")
		args = append(args, *in.DefaultMemberPermissions)
	}
	if in.DMPermission != nil {
		set = append(set, "dm_permission = ?")
		args = append(args, *in.DMPermission)
	}
	if len(set) == 0 {
		return s.Command(id)
	}
	set = append(set, "version = ?")
	args = append(args, s.NewID())
	args = append(args, id)
	if _, err := s.DB.Exec(`UPDATE application_commands SET `+strings.Join(set, ", ")+` WHERE id = ?`, args...); err != nil {
		return ApplicationCommand{}, mapErr(err)
	}
	return s.Command(id)
}

// DeleteCommand removes one command.
func (s *Store) DeleteCommand(id string) error {
	res, err := s.DB.Exec(`DELETE FROM application_commands WHERE id = ?`, id)
	if err != nil {
		return mapErr(err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}
