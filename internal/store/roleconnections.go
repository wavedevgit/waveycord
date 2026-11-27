package store

import (
	"database/sql"
	"encoding/json"

	"github.com/wavedevgit/waveycord/internal/model"
)

// RoleMetadataRecord is one application role connection metadata record.
type RoleMetadataRecord struct {
	Type                     int               `json:"type"`
	Key                      string            `json:"key"`
	Name                     string            `json:"name"`
	Description              string            `json:"description"`
	NameLocalizations        map[string]string `json:"name_localizations,omitempty"`
	DescriptionLocalizations map[string]string `json:"description_localizations,omitempty"`
}

// RoleConnection is the user's linked role connection for one application.
type RoleConnection struct {
	PlatformName     *string           `json:"platform_name"`
	PlatformUsername *string           `json:"platform_username"`
	Metadata         map[string]string `json:"metadata"`
	UpdatedAt        string            `json:"-"`
}

// SetRoleMetadata stores the application's metadata registry.
func (s *Store) SetRoleMetadata(appID string, records []RoleMetadataRecord) ([]RoleMetadataRecord, error) {
	raw, err := json.Marshal(records)
	if err != nil {
		return nil, err
	}
	res, err := s.DB.Exec(`UPDATE applications SET role_connection_metadata = ? WHERE id = ?`, string(raw), appID)
	if err != nil {
		return nil, mapErr(err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return nil, ErrNotFound
	}
	return records, nil
}

// RoleMetadata returns the application's metadata registry.
func (s *Store) RoleMetadata(appID string) ([]RoleMetadataRecord, error) {
	var raw string
	if err := s.DB.QueryRow(`SELECT role_connection_metadata FROM applications WHERE id = ?`, appID).Scan(&raw); err != nil {
		return nil, mapErr(err)
	}
	out := []RoleMetadataRecord{}
	if raw == "" {
		return out, nil
	}
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		return []RoleMetadataRecord{}, nil
	}
	return out, nil
}

// PutRoleConnection upserts the user's role connection.
func (s *Store) PutRoleConnection(userID, appID string, platformName, platformUsername *string, metadata map[string]string) (RoleConnection, error) {
	if metadata == nil {
		metadata = map[string]string{}
	}
	raw, err := json.Marshal(metadata)
	if err != nil {
		return RoleConnection{}, err
	}
	_, err = s.DB.Exec(`INSERT INTO role_connections
		(user_id, application_id, platform_name, platform_username, metadata, updated_at)
		VALUES (?,?,?,?,?,?)
		ON CONFLICT(user_id, application_id) DO UPDATE SET
			platform_name = excluded.platform_name,
			platform_username = excluded.platform_username,
			metadata = excluded.metadata,
			updated_at = excluded.updated_at`,
		userID, appID, platformName, platformUsername, string(raw), now())
	if err != nil {
		return RoleConnection{}, mapErr(err)
	}
	return s.RoleConnection(userID, appID)
}

// RoleConnection returns a stored connection or an empty one.
func (s *Store) RoleConnection(userID, appID string) (RoleConnection, error) {
	var (
		rc       RoleConnection
		name     sql.NullString
		username sql.NullString
		meta     string
		updated  int64
	)
	err := s.DB.QueryRow(`SELECT platform_name, platform_username, metadata, updated_at
		FROM role_connections WHERE user_id = ? AND application_id = ?`, userID, appID).
		Scan(&name, &username, &meta, &updated)
	if err != nil {
		if err == sql.ErrNoRows {
			return RoleConnection{Metadata: map[string]string{}}, nil
		}
		return rc, mapErr(err)
	}
	rc.PlatformName = ns(name)
	rc.PlatformUsername = ns(username)
	rc.Metadata = map[string]string{}
	_ = json.Unmarshal([]byte(meta), &rc.Metadata)
	if rc.Metadata == nil {
		rc.Metadata = map[string]string{}
	}
	rc.UpdatedAt = iso(updated)
	return rc, nil
}

var _ = model.User{}

// DeleteRoleConnection removes the user's role connection for an application.
func (s *Store) DeleteRoleConnection(userID, appID string) error {
	res, err := s.DB.Exec(`DELETE FROM role_connections WHERE user_id = ? AND application_id = ?`, userID, appID)
	if err != nil {
		return mapErr(err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}
