package store

import (
	"database/sql"
	"strings"

	"github.com/wavedevgit/waveycord/internal/model"
)

const userColumns = `id, username, discriminator, global_name, avatar, banner, accent_color,
	email, phone, mfa_enabled, verified, bot, system, flags, public_flags, premium_type,
	locale, avatar_decoration`

type rowScanner interface{ Scan(...any) error }

func scanUser(row rowScanner) (model.User, error) {
	var (
		u        model.User
		global   sql.NullString
		avatar   sql.NullString
		banner   sql.NullString
		accent   sql.NullInt64
		email    sql.NullString
		phone    sql.NullString
		deco     sql.NullString
	)
	err := row.Scan(&u.ID, &u.Username, &u.Discriminator, &global, &avatar, &banner, &accent,
		&email, &phone, &u.MFAEnabled, &u.Verified, &u.Bot, &u.System, &u.Flags,
		&u.PublicFlags, &u.PremiumType, &u.Locale, &deco)
	if err != nil {
		return u, err
	}
	u.GlobalName = ns(global)
	u.Avatar = ns(avatar)
	u.Banner = ns(banner)
	if accent.Valid {
		v := int(accent.Int64)
		u.AccentColor = &v
	}
	u.Email = ns(email)
	if deco.Valid && deco.String != "" {
		u.AvatarDecorationData = &model.AvatarDecorationData{SKUID: deco.String}
	}
	return u, nil
}

// PrivateUser re-reads the columns only the account owner may see.
func (s *Store) PrivateUser(id string) (model.User, error) {
	u, err := scanUser(s.DB.QueryRow(`SELECT `+userColumns+` FROM users WHERE id = ?`, id))
	return u, mapErr(err)
}

// CreateUserParams is the full insert payload.
type CreateUserParams struct {
	Username    string
	GlobalName  *string
	Email       *string
	Phone       *string
	PasswordHash string
	Bot         bool
	System      bool
	Verified    bool
	Locale      string
	Discriminator string
}

// CreateUser inserts an account and returns the API object.
func (s *Store) CreateUser(p CreateUserParams) (model.User, error) {
	idv := s.NewID()
	if p.Discriminator == "" {
		p.Discriminator = "0"
	}
	if p.Locale == "" {
		p.Locale = "en-US"
	}
	var email, phone any
	if p.Email != nil {
		email = strings.ToLower(*p.Email)
	}
	if p.Phone != nil {
		phone = *p.Phone
	}
	_, err := s.DB.Exec(`INSERT INTO users
		(id, username, discriminator, global_name, email, phone, password_hash,
		 mfa_enabled, verified, bot, system, locale, created_at)
		VALUES (?,?,?,?,?,?,?,0,?,?,?,?,?)`,
		idv, p.Username, p.Discriminator, p.GlobalName, email, phone, p.PasswordHash,
		p.Verified, p.Bot, p.System, p.Locale, now())
	if err != nil {
		return model.User{}, mapErr(err)
	}
	return s.PrivateUser(idv)
}

// UserByID returns the public projection of an account.
func (s *Store) UserByID(idv string) (model.User, error) {
	u, err := scanUser(s.DB.QueryRow(`SELECT `+userColumns+` FROM users WHERE id = ?`, idv))
	return u, mapErr(err)
}

// UserByLogin resolves an email or phone to an account.
func (s *Store) UserByLogin(login string) (model.User, error) {
	login = strings.ToLower(strings.TrimSpace(login))
	u, err := scanUser(s.DB.QueryRow(`SELECT `+userColumns+` FROM users
		WHERE lower(email) = ? OR phone = ?`, login, login))
	return u, mapErr(err)
}

// PasswordHash returns the stored argon2id string.
func (s *Store) PasswordHash(idv string) (string, error) {
	var h string
	err := s.DB.QueryRow(`SELECT password_hash FROM users WHERE id = ?`, idv).Scan(&h)
	return h, mapErr(err)
}

// UpdateUserParams holds nullable patches.
type UpdateUserParams struct {
	Username    *string
	GlobalName  *string
	Avatar      *string
	Banner      *string
	AccentColor *int
	Bio         *string
	Locale      *string
	PasswordHash *string
}

// UpdateUser applies a patch and returns the updated account.
func (s *Store) UpdateUser(idv string, p UpdateUserParams) (model.User, error) {
	set := []string{}
	args := []any{}
	add := func(col string, v any) { set = append(set, col+" = ?"); args = append(args, v) }
	if p.Username != nil {
		add("username", *p.Username)
	}
	if p.GlobalName != nil {
		add("global_name", nullIfEmpty(*p.GlobalName))
	}
	if p.Avatar != nil {
		add("avatar", nullIfEmpty(*p.Avatar))
	}
	if p.Banner != nil {
		add("banner", nullIfEmpty(*p.Banner))
	}
	if p.AccentColor != nil {
		add("accent_color", *p.AccentColor)
	}
	if p.Bio != nil {
		add("bio", *p.Bio)
	}
	if p.Locale != nil {
		add("locale", *p.Locale)
	}
	if p.PasswordHash != nil {
		add("password_hash", *p.PasswordHash)
	}
	if len(set) == 0 {
		return s.PrivateUser(idv)
	}
	args = append(args, idv)
	if _, err := s.DB.Exec(`UPDATE users SET `+strings.Join(set, ", ")+` WHERE id = ?`, args...); err != nil {
		return model.User{}, mapErr(err)
	}
	return s.PrivateUser(idv)
}

func nullIfEmpty(v string) any {
	if v == "" {
		return nil
	}
	return v
}

// Session is a persisted auth session.
type Session struct {
	ID         string
	UserID     string
	TokenHash  string
	ClientInfo string
	CreatedAt  int64
	LastUsedAt int64
}

// CreateSession stores a hashed token.
func (s *Store) CreateSession(userID, tokenHash, clientInfo string) (Session, error) {
	sess := Session{ID: s.NewID(), UserID: userID, TokenHash: tokenHash, ClientInfo: clientInfo, CreatedAt: now(), LastUsedAt: now()}
	_, err := s.DB.Exec(`INSERT INTO sessions (id, user_id, token_hash, client_info, created_at, last_used_at)
		VALUES (?,?,?,?,?,?)`, sess.ID, sess.UserID, sess.TokenHash, sess.ClientInfo, sess.CreatedAt, sess.LastUsedAt)
	return sess, mapErr(err)
}

// SessionByToken resolves a session and its account.
func (s *Store) SessionByToken(hash string) (Session, model.User, error) {
	var sess Session
	var userID string
	err := s.DB.QueryRow(`SELECT id, user_id, token_hash, client_info, created_at, last_used_at
		FROM sessions WHERE token_hash = ?`, hash).Scan(&sess.ID, &userID, &sess.TokenHash, &sess.ClientInfo, &sess.CreatedAt, &sess.LastUsedAt)
	if err != nil {
		return sess, model.User{}, mapErr(err)
	}
	sess.UserID = userID
	u, err := s.PrivateUser(userID)
	return sess, u, err
}

// TouchSession records use without failing the request.
func (s *Store) TouchSession(idv string) {
	_, _ = s.DB.Exec(`UPDATE sessions SET last_used_at = ? WHERE id = ?`, now(), idv)
}

// DeleteSession removes one session by id for a user.
func (s *Store) DeleteSession(userID, sessionID string) error {
	res, err := s.DB.Exec(`DELETE FROM sessions WHERE id = ? AND user_id = ?`, sessionID, userID)
	if err != nil {
		return mapErr(err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

// SessionsForUser lists sessions newest first.
func (s *Store) SessionsForUser(userID string) ([]Session, error) {
	rows, err := s.DB.Query(`SELECT id, user_id, token_hash, client_info, created_at, last_used_at
		FROM sessions WHERE user_id = ? ORDER BY created_at DESC`, userID)
	if err != nil {
		return nil, mapErr(err)
	}
	defer rows.Close()
	var out []Session
	for rows.Next() {
		var sess Session
		if err := rows.Scan(&sess.ID, &sess.UserID, &sess.TokenHash, &sess.ClientInfo, &sess.CreatedAt, &sess.LastUsedAt); err != nil {
			return nil, mapErr(err)
		}
		out = append(out, sess)
	}
	return out, mapErr(rows.Err())
}
