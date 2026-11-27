package store

import (
	"database/sql"
	"encoding/json"
)

// ApplicationIdentity is the documented identity object.
type ApplicationIdentity struct {
	UserID               string  `json:"user_id"`
	ProviderType         string  `json:"provider_type"`
	ProviderID           *string `json:"provider_id,omitempty"`
	ProviderIssuedUserID string  `json:"provider_issued_user_id"`
	Username             *string `json:"-"`
	Metadata             any     `json:"-"`
	Data                 any     `json:"-"`
}

// ApplicationIdentities lists the identities a user has for an application.
func (s *Store) ApplicationIdentities(appID, userID string) ([]ApplicationIdentity, error) {
	rows, err := s.DB.Query(`SELECT user_id, provider_type, provider_id, provider_issued_user_id
		FROM application_identities WHERE application_id = ? AND user_id = ? ORDER BY provider_issued_user_id`,
		appID, userID)
	if err != nil {
		return nil, mapErr(err)
	}
	defer rows.Close()
	out := []ApplicationIdentity{}
	for rows.Next() {
		var (
			identity ApplicationIdentity
			provider sql.NullString
		)
		if err := rows.Scan(&identity.UserID, &identity.ProviderType, &provider, &identity.ProviderIssuedUserID); err != nil {
			return nil, mapErr(err)
		}
		identity.ProviderID = ns(provider)
		out = append(out, identity)
	}
	return out, mapErr(rows.Err())
}

// ApplicationIdentitiesByExternalID resolves a user from an external key.
func (s *Store) ApplicationIdentitiesByExternalID(appID, providerType, providerIssuedUserID, providerID string) ([]ApplicationIdentity, error) {
	q := `SELECT user_id, provider_type, provider_id, provider_issued_user_id
		FROM application_identities WHERE application_id = ? AND provider_type = ? AND provider_issued_user_id = ?`
	args := []any{appID, providerType, providerIssuedUserID}
	if providerID != "" {
		q += ` AND provider_id = ?`
		args = append(args, providerID)
	}
	rows, err := s.DB.Query(q, args...)
	if err != nil {
		return nil, mapErr(err)
	}
	defer rows.Close()
	out := []ApplicationIdentity{}
	for rows.Next() {
		var (
			identity ApplicationIdentity
			provider sql.NullString
		)
		if err := rows.Scan(&identity.UserID, &identity.ProviderType, &provider, &identity.ProviderIssuedUserID); err != nil {
			return nil, mapErr(err)
		}
		identity.ProviderID = ns(provider)
		out = append(out, identity)
	}
	return out, mapErr(rows.Err())
}

// ApplicationIdentityProfile is the documented profile object.
type ApplicationIdentityProfile struct {
	Username *string `json:"username"`
	Metadata any     `json:"metadata"`
	Data     any     `json:"data"`
}

// ApplicationIdentityProfile loads the profile attached to an identity.
func (s *Store) ApplicationIdentityProfile(appID, userID, providerIssuedUserID string) (ApplicationIdentityProfile, error) {
	var (
		profile  ApplicationIdentityProfile
		username sql.NullString
		metadata sql.NullString
		data     sql.NullString
	)
	err := s.DB.QueryRow(`SELECT username, metadata, data FROM application_identities
		WHERE application_id = ? AND user_id = ? AND provider_issued_user_id = ?`,
		appID, userID, providerIssuedUserID).
		Scan(&username, &metadata, &data)
	if err != nil {
		return profile, mapErr(err)
	}
	profile.Username = ns(username)
	if metadata.Valid {
		_ = json.Unmarshal([]byte(metadata.String), &profile.Metadata)
	}
	if data.Valid {
		_ = json.Unmarshal([]byte(data.String), &profile.Data)
	}
	return profile, nil
}

// UpsertApplicationIdentityProfile writes a profile, creating a profile-only
// identity with provider type NONE when the user has none. It reports whether
// the identity was created.
func (s *Store) UpsertApplicationIdentityProfile(appID, userID, providerIssuedUserID string,
	username *string, metadata any, data any, replaceData bool, replaceMetadata bool) (ApplicationIdentityProfile, bool, error) {
	var (
		existingID   string
		existingProv string
	)
	err := s.DB.QueryRow(`SELECT id, provider_issued_user_id FROM application_identities
		WHERE application_id = ? AND user_id = ? LIMIT 1`, appID, userID).
		Scan(&existingID, &existingProv)
	if err == sql.ErrNoRows {
		meta, _ := json.Marshal(metadata)
		payload, _ := json.Marshal(data)
		idv := s.NewID()
		_, err := s.DB.Exec(`INSERT INTO application_identities
			(id, application_id, user_id, provider_type, provider_id, provider_issued_user_id,
			 username, metadata, data, created_at, updated_at)
			VALUES (?,?,?,?,?,?,?,?,?,?,?)`,
			idv, appID, userID, "NONE", "", providerIssuedUserID, username, nullableJSON(meta), nullableJSON(payload), now(), now())
		if err != nil {
			return ApplicationIdentityProfile{}, false, mapErr(err)
		}
		profile, err := s.ApplicationIdentityProfile(appID, userID, providerIssuedUserID)
		return profile, true, err
	}
	if err != nil {
		return ApplicationIdentityProfile{}, false, mapErr(err)
	}
	if existingProv != providerIssuedUserID {
		return ApplicationIdentityProfile{}, false, ErrConflict
	}
	set := []string{"updated_at = ?"}
	args := []any{now()}
	if username != nil {
		set = append(set, "username = ?")
		args = append(args, *username)
	}
	if replaceMetadata {
		meta, _ := json.Marshal(metadata)
		set = append(set, "metadata = ?")
		args = append(args, nullableJSON(meta))
	}
	if replaceData {
		payload, _ := json.Marshal(data)
		set = append(set, "data = ?")
		args = append(args, nullableJSON(payload))
	}
	args = append(args, existingID)
	if _, err := s.DB.Exec(`UPDATE application_identities SET `+joinComma(set)+` WHERE id = ?`, args...); err != nil {
		return ApplicationIdentityProfile{}, false, mapErr(err)
	}
	profile, err := s.ApplicationIdentityProfile(appID, userID, providerIssuedUserID)
	return profile, false, err
}

// DeleteApplicationIdentity removes one identity and its profile.
func (s *Store) DeleteApplicationIdentity(appID, userID, providerType, providerIssuedUserID string) error {
	res, err := s.DB.Exec(`DELETE FROM application_identities
		WHERE application_id = ? AND user_id = ? AND provider_type = ? AND provider_issued_user_id = ?`,
		appID, userID, providerType, providerIssuedUserID)
	if err != nil {
		return mapErr(err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// UserAuthorizedApplication reports whether a user granted an application the
// requested OAuth2 scope through any live token.
func (s *Store) UserAuthorizedApplication(appID, userID, scope string) (bool, error) {
	rows, err := s.DB.Query(`SELECT scopes FROM oauth2_tokens
		WHERE application_id = ? AND user_id = ? AND revoked = 0 AND expires_at > ?`, appID, userID, now())
	if err != nil {
		return false, mapErr(err)
	}
	defer rows.Close()
	for rows.Next() {
		var raw string
		if err := rows.Scan(&raw); err != nil {
			return false, mapErr(err)
		}
		for _, granted := range parseJSONStrings(raw) {
			if granted == scope {
				return true, nil
			}
		}
	}
	return false, mapErr(rows.Err())
}

func nullableJSON(raw []byte) any {
	if len(raw) == 0 || string(raw) == "null" {
		return nil
	}
	return string(raw)
}
