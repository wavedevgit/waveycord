package store

import (
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"time"

	"github.com/wavedevgit/waveycord/internal/model"
)

// OAuth2 authorization-code and token persistence. Codes are single-use and
// short-lived; tokens are stored as SHA-256 hashes, never plaintext.

// OAuthCode is a pending authorization code.
type OAuthCode struct {
	Code        string
	AppID       string
	UserID      string
	RedirectURI string
	Scopes      []string
	GuildID     *string
	Permissions *string
	ExpiresAt   int64
}

// VerifyClientSecret compares a presented secret with the stored hash.
func (s *Store) VerifyClientSecret(appID, secret string) (Application, error) {
	app, err := s.Application(appID)
	if err != nil {
		return app, err
	}
	var hash string
	if err := s.DB.QueryRow(`SELECT client_secret_hash FROM applications WHERE id = ?`, appID).Scan(&hash); err != nil {
		return app, mapErr(err)
	}
	if hash == "" || !constantTimeEqual(hash, TokenHashOf(secret)) {
		return app, ErrInvalidClient
	}
	return app, nil
}

// ErrInvalidClient marks a failed client authentication.
var ErrInvalidClient = errors.New("store: invalid client")

func constantTimeEqual(a, b string) bool {
	if len(a) != len(b) {
		return false
	}
	var diff byte
	for i := 0; i < len(a); i++ {
		diff |= a[i] ^ b[i]
	}
	return diff == 0
}

// CreateAuthorizationCode stores a single-use code valid for ten minutes.
func (s *Store) CreateAuthorizationCode(appID, userID, redirectURI string, scopes []string, guildID, permissions *string) (string, error) {
	buf := make([]byte, 24)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	code := hex.EncodeToString(buf)
	now := now()
	_, err := s.DB.Exec(`INSERT INTO oauth2_authorization_codes
		(code, application_id, user_id, redirect_uri, scopes, guild_id, permissions, created_at, expires_at)
		VALUES (?,?,?,?,?,?,?,?,?)`,
		code, appID, userID, redirectURI, jsonStrings(scopes), guildID, permissions, now, now+10*60*1000)
	return code, mapErr(err)
}

// ConsumeAuthorizationCode validates and burns a code.
func (s *Store) ConsumeAuthorizationCode(code, appID, redirectURI string) (OAuthCode, error) {
	var (
		oc     OAuthCode
		scopes string
		guild  sql.NullString
		perms  sql.NullString
	)
	err := s.DB.QueryRow(`SELECT code, application_id, user_id, redirect_uri, scopes, guild_id, permissions, expires_at
		FROM oauth2_authorization_codes WHERE code = ?`, code).
		Scan(&oc.Code, &oc.AppID, &oc.UserID, &oc.RedirectURI, &scopes, &guild, &perms, &oc.ExpiresAt)
	if err != nil {
		return oc, mapErr(err)
	}
	oc.Scopes = parseJSONStrings(scopes)
	oc.GuildID = ns(guild)
	oc.Permissions = ns(perms)
	if oc.AppID != appID || oc.RedirectURI != redirectURI {
		return oc, ErrNotFound
	}
	if oc.ExpiresAt < now() {
		return oc, ErrNotFound
	}
	res, err := s.DB.Exec(`UPDATE oauth2_authorization_codes SET consumed = 1
		WHERE code = ? AND consumed = 0`, code)
	if err != nil {
		return oc, mapErr(err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return oc, ErrNotFound
	}
	return oc, nil
}

// OAuthToken is an issued access/refresh pair.
type OAuthToken struct {
	ID        string
	AppID     string
	UserID    string
	Scopes    []string
	ExpiresAt int64
}

// CreateOAuthToken issues a new token pair. The caller receives plaintext once.
func (s *Store) CreateOAuthToken(appID, userID string, scopes []string) (OAuthToken, string, string, error) {
	access, err := NewOpaqueToken()
	if err != nil {
		return OAuthToken{}, "", "", err
	}
	refresh, err := NewOpaqueToken()
	if err != nil {
		return OAuthToken{}, "", "", err
	}
	t := OAuthToken{ID: s.NewID(), AppID: appID, UserID: userID, Scopes: scopes,
		ExpiresAt: now() + 7*24*60*60*1000}
	_, err = s.DB.Exec(`INSERT INTO oauth2_tokens
		(id, application_id, user_id, token_hash, refresh_hash, scopes, created_at, expires_at)
		VALUES (?,?,?,?,?,?,?,?)`,
		t.ID, appID, userID, TokenHashOf(access), TokenHashOf(refresh), jsonStrings(scopes),
		now(), t.ExpiresAt)
	if err != nil {
		return t, "", "", mapErr(err)
	}
	return t, access, refresh, nil
}

// TokenByAccessHash resolves a bearer access token.
func (s *Store) TokenByAccessHash(hash string) (OAuthToken, model.User, error) {
	var (
		t       OAuthToken
		scopes  string
		revoked bool
	)
	err := s.DB.QueryRow(`SELECT id, application_id, user_id, scopes, expires_at, revoked
		FROM oauth2_tokens WHERE token_hash = ?`, hash).
		Scan(&t.ID, &t.AppID, &t.UserID, &scopes, &t.ExpiresAt, &revoked)
	if err != nil {
		return t, model.User{}, mapErr(err)
	}
	if revoked || t.ExpiresAt < now() {
		return t, model.User{}, ErrNotFound
	}
	t.Scopes = parseJSONStrings(scopes)
	u, err := s.UserByID(t.UserID)
	return t, u, err
}

// RotateByRefreshHash invalidates a refresh token and issues a new pair.
func (s *Store) RotateByRefreshHash(refreshHash, appID string) (OAuthToken, string, string, error) {
	var (
		id      string
		userID  string
		scopes  string
		revoked bool
		expires int64
	)
	err := s.DB.QueryRow(`SELECT id, user_id, scopes, revoked, expires_at
		FROM oauth2_tokens WHERE refresh_hash = ?`, refreshHash).
		Scan(&id, &userID, &scopes, &revoked, &expires)
	if err != nil {
		return OAuthToken{}, "", "", mapErr(err)
	}
	var tokenApp string
	if err := s.DB.QueryRow(`SELECT application_id FROM oauth2_tokens WHERE id = ?`, id).Scan(&tokenApp); err != nil {
		return OAuthToken{}, "", "", mapErr(err)
	}
	if revoked || tokenApp != appID {
		return OAuthToken{}, "", "", ErrNotFound
	}
	if _, err := s.DB.Exec(`UPDATE oauth2_tokens SET revoked = 1 WHERE id = ?`, id); err != nil {
		return OAuthToken{}, "", "", mapErr(err)
	}
	return s.CreateOAuthToken(appID, userID, parseJSONStrings(scopes))
}

// RevokeOAuthToken revokes by access token hash.
func (s *Store) RevokeOAuthToken(hash string) error {
	_, err := s.DB.Exec(`UPDATE oauth2_tokens SET revoked = 1 WHERE token_hash = ?`, hash)
	return mapErr(err)
}

// PurgeExpiredOAuthCodes removes codes past their lifetime.
func (s *Store) PurgeExpiredOAuthCodes() {
	cutoff := time.Now().Add(-time.Hour).UnixMilli()
	_, _ = s.DB.Exec(`DELETE FROM oauth2_authorization_codes WHERE expires_at < ?`, cutoff)
}
