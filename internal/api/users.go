package api

import (
	"errors"
	"strings"

	"github.com/wavedevgit/waveycord/internal/model"
	"github.com/wavedevgit/waveycord/internal/perm"
	"github.com/wavedevgit/waveycord/internal/store"
)

func (s *Server) handleGetSelf(c *Ctx) {
	c.JSON(s.selfView(c.User))
}

type updateSelfRequest struct {
	Username    *string `json:"username"`
	GlobalName  *string `json:"global_name"`
	Avatar      *string `json:"avatar"`
	Banner      *string `json:"banner"`
	AccentColor *int    `json:"accent_color"`
	Bio         *string `json:"bio"`
	Locale      *string `json:"locale"`
	Password    *string `json:"password"`
	NewPassword *string `json:"new_password"`
}

func (s *Server) handleUpdateSelf(c *Ctx) {
	var req updateSelfRequest
	if err := c.Bind(&req); err != nil {
		fail(c, ErrInvalidForm)
		return
	}
	p := store.UpdateUserParams{
		Username:    req.Username,
		GlobalName:  req.GlobalName,
		Avatar:      req.Avatar,
		Banner:      req.Banner,
		AccentColor: req.AccentColor,
		Bio:         req.Bio,
		Locale:      req.Locale,
	}
	if req.Username != nil && !validUsername(*req.Username) {
		c.FailForm("username", "Must be between 2 and 32 characters and contain no forbidden substrings")
		return
	}
	if req.NewPassword != nil {
		if req.Password == nil {
			c.FailForm("password", "Current password is required")
			return
		}
		hash, err := s.Store.PasswordHash(c.User.ID)
		if err != nil || !VerifyPassword(*req.Password, hash) {
			c.FailForm("password", "Password does not match")
			return
		}
		if len(*req.NewPassword) < 8 || len(*req.NewPassword) > 72 {
			c.FailForm("new_password", "Must be between 8 and 72 characters")
			return
		}
		newHash, err := HashPassword(*req.NewPassword)
		if err != nil {
			fail(c, err)
			return
		}
		p.PasswordHash = &newHash
	}
	user, err := s.Store.UpdateUser(c.User.ID, p)
	if err != nil {
		if errors.Is(err, store.ErrConflict) {
			c.FailForm("username", "Username is already taken")
			return
		}
		fail(c, err)
		return
	}
	c.User = user
	if s.Hub != nil {
		s.Hub.DispatchUser(c.User.ID, "USER_UPDATE", s.selfView(user))
	}
	c.JSON(s.selfView(user))
}

// partialGuild is the GET /users/@me/guilds projection.
type partialGuild struct {
	ID                     string   `json:"id"`
	Name                   string   `json:"name"`
	Icon                   *string  `json:"icon"`
	Banner                 *string  `json:"banner"`
	Owner                  bool     `json:"owner"`
	OwnerID                string   `json:"owner_id"`
	Permissions            string   `json:"permissions"`
	Features               []string `json:"features"`
	ApproximateMemberCount *int     `json:"approximate_member_count,omitempty"`
}

func (s *Server) handleMyGuilds(c *Ctx) {
	guilds, err := s.Store.GuildsForUser(c.User.ID)
	if err != nil {
		fail(c, err)
		return
	}
	out := make([]partialGuild, 0, len(guilds))
	for _, g := range guilds {
		perms, _ := s.guildPermissions(c.User.ID, g.ID)
		out = append(out, partialGuild{
			ID: g.ID, Name: g.Name, Icon: g.Icon, Banner: g.Banner,
			Owner: g.OwnerID == c.User.ID, OwnerID: g.OwnerID,
			Permissions: perm.Format(perms), Features: g.Features,
		})
	}
	c.JSON(out)
}

func (s *Server) handleGetUser(c *Ctx) {
	user, err := s.Store.UserByID(c.Param("userID"))
	if err != nil {
		fail(c, ErrUnknownUser)
		return
	}
	c.JSON(user)
}

func (s *Server) handleGetUserProfile(c *Ctx) {
	user, err := s.Store.UserByID(c.Param("userID"))
	if err != nil {
		fail(c, ErrUnknownUser)
		return
	}
	payload := map[string]any{
		"user":                user,
		"connected_accounts":  []any{},
		"premium_since":       nil,
		"premium_guild_since": nil,
		"mutual_guilds":       []any{},
	}
	if gid := c.R.URL.Query().Get("guild_id"); gid != "" {
		if member, err := s.Store.Member(gid, user.ID); err == nil {
			payload["guild_member"] = member
		}
	}
	c.JSON(payload)
}

func (s *Server) handleMyChannels(c *Ctx) {
	channels, err := s.Store.DMChannels(c.User.ID)
	if err != nil {
		fail(c, err)
		return
	}
	c.JSON(channels)
}

type createDMRequest struct {
	RecipientID  string            `json:"recipient_id"`
	AccessTokens []string          `json:"access_tokens"`
	Nicks        map[string]string `json:"nicks"`
}

func (s *Server) handleCreateDM(c *Ctx) {
	var req createDMRequest
	if err := c.Bind(&req); err != nil {
		fail(c, ErrInvalidForm)
		return
	}
	if req.RecipientID == "" {
		c.FailForm("recipient_id", "This field is required")
		return
	}
	if _, err := s.Store.UserByID(req.RecipientID); err != nil {
		fail(c, ErrUnknownUser)
		return
	}
	ch, err := s.Store.OpenDM(c.User.ID, req.RecipientID)
	if err != nil {
		fail(c, err)
		return
	}
	if s.Hub != nil {
		s.Hub.DispatchUser(req.RecipientID, "CHANNEL_CREATE", ch)
	}
	c.JSON(ch)
}

func (s *Server) handleListRelationships(c *Ctx) {
	rels, users, err := s.Store.Relationships(c.User.ID)
	if err != nil {
		fail(c, err)
		return
	}
	_ = users
	c.JSON(rels)
}

func (s *Server) handlePutRelationship(c *Ctx) {
	target := c.Param("userID")
	if target == c.User.ID {
		fail(c, NewError(400, 80007, "You cannot add yourself as a friend"))
		return
	}
	if _, err := s.Store.UserByID(target); err != nil {
		fail(c, ErrUnknownUser)
		return
	}
	var req struct {
		Type int `json:"type"`
	}
	_ = c.Bind(&req)
	if req.Type == 0 {
		req.Type = 1
	}
	if err := s.Store.PutRelationship(c.User.ID, target, req.Type); err != nil {
		fail(c, err)
		return
	}
	c.JSONStatus(204, nil)
}

func (s *Server) handleDeleteRelationship(c *Ctx) {
	if err := s.Store.DeleteRelationship(c.User.ID, c.Param("userID")); err != nil {
		fail(c, err)
		return
	}
	c.NoContent()
}

// sanitizeName trims and collapses whitespace for guild/channel names.
func sanitizeName(v string) string {
	return strings.Join(strings.Fields(v), " ")
}

var _ = model.Channel{}
