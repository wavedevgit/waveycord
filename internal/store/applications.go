package store

import (
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"strings"

	"github.com/wavedevgit/waveycord/internal/model"
)

// Application is the application object as documented.
type Application struct {
	ID                  string      `json:"id"`
	Name                string      `json:"name"`
	Description         string      `json:"description"`
	Icon                *string     `json:"icon"`
	OwnerID             string      `json:"owner_id,omitempty"`
	BotPublic           bool        `json:"bot_public"`
	BotRequireCodeGrant bool        `json:"bot_require_code_grant"`
	VerifyKey           string      `json:"verify_key,omitempty"`
	Bot                 *model.User `json:"bot,omitempty"`
	Token               string      `json:"token,omitempty"`
	ClientSecret        string      `json:"client_secret,omitempty"`
	RedirectURIs        []string    `json:"redirect_uris"`
	CreatedAt           string      `json:"-"`
}

const applicationColumns = `id, name, description, icon, owner_id, bot_user_id, bot_public,
	bot_require_code_grant, verify_key, redirect_uris`

func (s *Store) scanApplication(row rowScanner) (Application, string, error) {
	var (
		a         Application
		icon      sql.NullString
		botUID    string
		redirects string
	)
	err := row.Scan(&a.ID, &a.Name, &a.Description, &icon, &a.OwnerID, &botUID,
		&a.BotPublic, &a.BotRequireCodeGrant, &a.VerifyKey, &redirects)
	a.Icon = ns(icon)
	a.RedirectURIs = parseJSONStrings(redirects)
	if a.RedirectURIs == nil {
		a.RedirectURIs = []string{}
	}
	return a, botUID, err
}

// CreateApplication creates an application and its bot account. The returned
// token is the bot token and is the only time it is visible.
func (s *Store) CreateApplication(ownerID, name, description string) (Application, error) {
	appID := s.NewID()
	botID := s.NewID()
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		return Application{}, err
	}
	token, err := NewOpaqueToken()
	if err != nil {
		return Application{}, err
	}
	secret, err := NewOpaqueToken()
	if err != nil {
		return Application{}, err
	}
	tx, err := s.DB.Begin()
	if err != nil {
		return Application{}, mapErr(err)
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`INSERT INTO users (id, username, discriminator, password_hash, bot, verified, locale, created_at)
		VALUES (?,?, '0000', '', 1, 1, 'en-US', ?)`, botID, name, now()); err != nil {
		return Application{}, mapErr(err)
	}
	if _, err := tx.Exec(`INSERT INTO applications
		(id, name, description, owner_id, bot_user_id, bot_public, bot_require_code_grant, verify_key,
		 client_secret_hash, redirect_uris, created_at)
		VALUES (?,?,?,?,?,1,0,?,?,'[]',?)`,
		appID, name, description, ownerID, botID, hex.EncodeToString(key), TokenHashOf(secret), now()); err != nil {
		return Application{}, mapErr(err)
	}
	if _, err := tx.Exec(`INSERT INTO bot_tokens (token_hash, application_id, user_id, created_at)
		VALUES (?,?,?,?)`, TokenHashOf(token), appID, botID, now()); err != nil {
		return Application{}, mapErr(err)
	}
	if err := tx.Commit(); err != nil {
		return Application{}, mapErr(err)
	}
	app, err := s.Application(appID)
	if err != nil {
		return app, err
	}
	app.Token = token
	app.ClientSecret = secret
	return app, nil
}

// UpdateApplication patches the fields an owner controls.
func (s *Store) UpdateApplication(appID, ownerID string, name, description *string, redirectURIs *[]string, verifyKey *string) (Application, error) {
	app, err := s.Application(appID)
	if err != nil {
		return app, err
	}
	if app.OwnerID != ownerID {
		return app, ErrNotFound
	}
	set := []string{}
	args := []any{}
	if name != nil {
		set = append(set, "name = ?")
		args = append(args, *name)
	}
	if description != nil {
		set = append(set, "description = ?")
		args = append(args, *description)
	}
	if redirectURIs != nil {
		set = append(set, "redirect_uris = ?")
		args = append(args, jsonStrings(*redirectURIs))
	}
	if verifyKey != nil {
		set = append(set, "verify_key = ?")
		args = append(args, *verifyKey)
	}
	if len(set) == 0 {
		return app, nil
	}
	args = append(args, appID)
	if _, err := s.DB.Exec(`UPDATE applications SET `+strings.Join(set, ", ")+` WHERE id = ?`, args...); err != nil {
		return Application{}, mapErr(err)
	}
	return s.Application(appID)
}

// Application loads an application by id.
func (s *Store) Application(idv string) (Application, error) {
	a, botID, err := s.scanApplication(s.DB.QueryRow(`SELECT `+applicationColumns+` FROM applications WHERE id = ?`, idv))
	if err != nil {
		return a, mapErr(err)
	}
	if u, err := s.UserByID(botID); err == nil {
		a.Bot = &u
	}
	return a, nil
}

// ApplicationForBot resolves the application owning a bot user.
func (s *Store) ApplicationForBot(botUserID string) (Application, error) {
	a, botID, err := s.scanApplication(s.DB.QueryRow(`SELECT `+applicationColumns+` FROM applications WHERE bot_user_id = ?`, botUserID))
	if err != nil {
		return a, mapErr(err)
	}
	if u, err := s.UserByID(botID); err == nil {
		a.Bot = &u
	}
	return a, nil
}

// ApplicationsForOwner lists applications created by an account.
func (s *Store) ApplicationsForOwner(ownerID string) ([]Application, error) {
	rows, err := s.DB.Query(`SELECT `+applicationColumns+` FROM applications WHERE owner_id = ? ORDER BY id ASC`, ownerID)
	if err != nil {
		return nil, mapErr(err)
	}
	defer rows.Close()
	out := []Application{}
	for rows.Next() {
		a, _, err := s.scanApplication(rows)
		if err != nil {
			return nil, mapErr(err)
		}
		out = append(out, a)
	}
	return out, mapErr(rows.Err())
}

// ResetBotToken issues a new bot token and invalidates the old one.
func (s *Store) ResetBotToken(appID, ownerID string) (string, error) {
	app, err := s.Application(appID)
	if err != nil {
		return "", err
	}
	if app.OwnerID != ownerID {
		return "", ErrNotFound
	}
	token, err := NewOpaqueToken()
	if err != nil {
		return "", err
	}
	tx, err := s.DB.Begin()
	if err != nil {
		return "", mapErr(err)
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`DELETE FROM bot_tokens WHERE application_id = ?`, appID); err != nil {
		return "", mapErr(err)
	}
	if _, err := tx.Exec(`INSERT INTO bot_tokens (token_hash, application_id, user_id, created_at)
		VALUES (?,?,?,?)`, TokenHashOf(token), appID, derefString(app.Bot), now()); err != nil {
		return "", mapErr(err)
	}
	return token, mapErr(tx.Commit())
}

func derefString(u *model.User) string {
	if u == nil {
		return ""
	}
	return u.ID
}

// BotSession resolves a bot token to its user and application.
func (s *Store) BotSession(tokenHash string) (model.User, Application, error) {
	var (
		appID string
		user  string
	)
	err := s.DB.QueryRow(`SELECT application_id, user_id FROM bot_tokens WHERE token_hash = ?`, tokenHash).
		Scan(&appID, &user)
	if err != nil {
		return model.User{}, Application{}, mapErr(err)
	}
	u, err := s.UserByID(user)
	if err != nil {
		return model.User{}, Application{}, err
	}
	app, err := s.Application(appID)
	return u, app, err
}

// NewOpaqueToken returns a 40-byte URL-safe bearer token.
func NewOpaqueToken() (string, error) {
	buf := make([]byte, 40)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(buf), nil
}

// TokenHashOf is how every token is stored at rest.
func TokenHashOf(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

// SearchMessages implements guild message search over the messages table. The
// channel list is already permission-filtered by the caller.
func (s *Store) SearchMessages(guildID, content, authorID string, channels []string, limit int) ([]model.Message, int, error) {
	if len(channels) == 0 {
		return []model.Message{}, 0, nil
	}
	if limit <= 0 || limit > 25 {
		limit = 25
	}
	placeholders := make([]string, len(channels))
	args := []any{guildID, "%" + content + "%"}
	for i, c := range channels {
		placeholders[i] = "?"
		args = append(args, c)
	}
	where := `guild_id = ? AND content LIKE ? AND channel_id IN (` + strings.Join(placeholders, ",") + `)`
	if authorID != "" {
		where += ` AND author_id = ?`
		args = append(args, authorID)
	}
	var total int
	if err := s.read().QueryRow(`SELECT COUNT(*) FROM messages WHERE `+where, args...).Scan(&total); err != nil {
		return nil, 0, mapErr(err)
	}
	rows, err := s.read().Query(`SELECT `+messageColumns+` FROM messages WHERE `+where+` ORDER BY id DESC LIMIT ?`,
		append(args, limit)...)
	if err != nil {
		return nil, 0, mapErr(err)
	}
	var raws []rawMessage
	for rows.Next() {
		raw, err := scanRawMessage(rows)
		if err != nil {
			rows.Close()
			return nil, 0, mapErr(err)
		}
		raws = append(raws, raw)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, 0, mapErr(err)
	}
	out := make([]model.Message, 0, len(raws))
	for _, raw := range raws {
		out = append(out, s.hydrate(raw, 0))
	}
	return out, total, nil
}
