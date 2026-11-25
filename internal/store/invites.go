package store

import (
	"crypto/rand"
	"database/sql"
	"errors"
	"math/big"

	"github.com/wavedevgit/waveycord/internal/model"
)

const inviteCodeAlphabet = "ABCDEFGHJKLMNPQRSTUVWXYZabcdefghijkmnopqrstuvwxyz23456789"

func newInviteCode() string {
	const n = 8
	buf := make([]byte, n)
	for i := range buf {
		v, err := rand.Int(rand.Reader, big.NewInt(int64(len(inviteCodeAlphabet))))
		if err != nil {
			panic("store: crypto/rand failed: " + err.Error())
		}
		buf[i] = inviteCodeAlphabet[v.Int64()]
	}
	return string(buf)
}

// CreateInviteParams is the documented invite create payload.
type CreateInviteParams struct {
	ChannelID string
	GuildID   string
	InviterID string
	MaxAge    int
	MaxUses   int
	Temporary bool
	Unique    bool
}

// CreateInvite inserts an invite.
func (s *Store) CreateInvite(p CreateInviteParams) (model.Invite, error) {
	code := newInviteCode()
	if p.Unique {
		for i := 0; i < 5; i++ {
			if _, err := s.Invite(code); errors.Is(err, ErrNotFound) {
				break
			}
			code = newInviteCode()
		}
	}
	_, err := s.DB.Exec(`INSERT INTO invites
		(code, guild_id, channel_id, inviter_id, max_uses, max_age, temporary, unique_flag, created_at)
		VALUES (?,?,?,?,?,?,?,?,?)`,
		code, nullIfEmpty(p.GuildID), p.ChannelID, p.InviterID, p.MaxUses, p.MaxAge, p.Temporary, p.Unique, now())
	if err != nil {
		return model.Invite{}, mapErr(err)
	}
	return s.Invite(code)
}

// Invite loads an invite with channel, guild and inviter expanded.
func (s *Store) Invite(code string) (model.Invite, error) {
	var (
		inv     model.Invite
		guildID sql.NullString
		inviter sql.NullString
		created int64
		maxUses int
		maxAge  int
		uses    int
		temp    bool
		channel string
	)
	err := s.DB.QueryRow(`SELECT code, guild_id, channel_id, inviter_id, uses, max_uses, max_age,
		temporary, created_at FROM invites WHERE code = ?`, code).
		Scan(&inv.Code, &guildID, &channel, &inviter, &uses, &maxUses, &maxAge, &temp, &created)
	if err != nil {
		return inv, mapErr(err)
	}
	ch, err := s.Channel(channel)
	if err != nil {
		return inv, err
	}
	inv.Channel = &ch
	inv.Uses = uses
	inv.MaxUses = maxUses
	inv.MaxAge = maxAge
	inv.Temporary = temp
	inv.CreatedAt = iso(created)
	if maxAge > 0 {
		exp := iso(created + int64(maxAge)*1000)
		inv.ExpiresAt = &exp
	}
	if guildID.Valid {
		g, err := s.Guild(guildID.String)
		if err == nil {
			inv.Guild = &g
			inv.Type = 0
			members, _ := s.MemberCount(guildID.String)
			inv.ApproximateMemberCount = &members
		}
	} else {
		inv.Type = 1
	}
	if inviter.Valid {
		if u, err := s.UserByID(inviter.String); err == nil {
			inv.Inviter = &u
		}
	}
	return inv, nil
}

// InviteExpired reports whether the invite can no longer be used.
func (s *Store) InviteExpired(code string) (bool, error) {
	var created int64
	var maxAge, maxUses, uses int
	err := s.DB.QueryRow(`SELECT created_at, max_age, max_uses, uses FROM invites WHERE code = ?`, code).
		Scan(&created, &maxAge, &maxUses, &uses)
	if err != nil {
		return false, mapErr(err)
	}
	if maxUses > 0 && uses >= maxUses {
		return true, nil
	}
	if maxAge > 0 && now() > created+int64(maxAge)*1000 {
		return true, nil
	}
	return false, nil
}

// ConsumeInvite increments the use counter.
func (s *Store) ConsumeInvite(code string) error {
	_, err := s.DB.Exec(`UPDATE invites SET uses = uses + 1 WHERE code = ?`, code)
	return mapErr(err)
}

// GuildInvites lists a guild's invites.
func (s *Store) GuildInvites(guildID string) ([]model.Invite, error) {
	rows, err := s.DB.Query(`SELECT code FROM invites WHERE guild_id = ? ORDER BY created_at DESC`, guildID)
	if err != nil {
		return nil, mapErr(err)
	}
	var codes []string
	for rows.Next() {
		var c string
		if err := rows.Scan(&c); err != nil {
			rows.Close()
			return nil, mapErr(err)
		}
		codes = append(codes, c)
	}
	rows.Close()
	out := []model.Invite{}
	for _, c := range codes {
		if inv, err := s.Invite(c); err == nil {
			out = append(out, inv)
		}
	}
	return out, nil
}

// ChannelInvites lists invites created on a channel.
func (s *Store) ChannelInvites(channelID string) ([]model.Invite, error) {
	rows, err := s.DB.Query(`SELECT code FROM invites WHERE channel_id = ? ORDER BY created_at DESC`, channelID)
	if err != nil {
		return nil, mapErr(err)
	}
	var codes []string
	for rows.Next() {
		var c string
		if err := rows.Scan(&c); err != nil {
			rows.Close()
			return nil, mapErr(err)
		}
		codes = append(codes, c)
	}
	rows.Close()
	out := []model.Invite{}
	for _, c := range codes {
		if inv, err := s.Invite(c); err == nil {
			out = append(out, inv)
		}
	}
	return out, nil
}

// DeleteInvite removes an invite.
func (s *Store) DeleteInvite(code string) error {
	res, err := s.DB.Exec(`DELETE FROM invites WHERE code = ?`, code)
	if err != nil {
		return mapErr(err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// MemberCount counts guild members.
func (s *Store) MemberCount(guildID string) (int, error) {
	var n int
	err := s.DB.QueryRow(`SELECT COUNT(*) FROM guild_members WHERE guild_id = ?`, guildID).Scan(&n)
	return n, mapErr(err)
}

// --- relationships ---

// Relationships lists an account's relationships with users expanded.
func (s *Store) Relationships(userID string) ([]model.Relationship, []model.User, error) {
	rows, err := s.DB.Query(`SELECT target_id, type, nickname, since FROM relationships WHERE user_id = ? ORDER BY type, target_id`, userID)
	if err != nil {
		return nil, nil, mapErr(err)
	}
	defer rows.Close()
	rels := []model.Relationship{}
	users := []model.User{}
	for rows.Next() {
		var (
			r      model.Relationship
			nick   sql.NullString
			since  sql.NullInt64
			target string
		)
		if err := rows.Scan(&target, &r.Type, &nick, &since); err != nil {
			return nil, nil, mapErr(err)
		}
		r.ID = target
		r.UserID = target
		r.Nickname = ns(nick)
		if since.Valid {
			v := iso(since.Int64)
			r.Since = &v
		}
		u, err := s.UserByID(target)
		if err != nil {
			continue
		}
		rels = append(rels, r)
		users = append(users, u)
	}
	return rels, users, mapErr(rows.Err())
}

// PutRelationship upserts a relationship of the given type.
func (s *Store) PutRelationship(userID, targetID string, typ int) error {
	var since any
	if typ == 1 { // friend
		since = now()
	}
	_, err := s.DB.Exec(`INSERT INTO relationships (user_id, target_id, type, since)
		VALUES (?,?,?,?) ON CONFLICT(user_id, target_id) DO UPDATE SET type = excluded.type`,
		userID, targetID, typ, since)
	return mapErr(err)
}

// DeleteRelationship removes a relationship.
func (s *Store) DeleteRelationship(userID, targetID string) error {
	_, err := s.DB.Exec(`DELETE FROM relationships WHERE user_id = ? AND target_id = ?`, userID, targetID)
	return mapErr(err)
}
