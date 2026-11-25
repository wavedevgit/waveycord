package api

import (
	"errors"

	"github.com/wavedevgit/waveycord/internal/perm"
	"github.com/wavedevgit/waveycord/internal/store"
)

func (s *Server) handleGetInvite(c *Ctx) {
	inv, err := s.Store.Invite(c.Param("code"))
	if err != nil {
		fail(c, ErrUnknownInvite)
		return
	}
	if expired, _ := s.Store.InviteExpired(inv.Code); expired {
		fail(c, ErrUnknownInvite)
		return
	}
	if c.R.URL.Query().Get("with_counts") == "true" && inv.Guild != nil {
		n, _ := s.Store.MemberCount(inv.Guild.ID)
		inv.ApproximateMemberCount = &n
	}
	c.JSON(inv)
}

func (s *Server) handleDeleteInvite(c *Ctx) {
	code := c.Param("code")
	inv, err := s.Store.Invite(code)
	if err != nil {
		fail(c, ErrUnknownInvite)
		return
	}
	if inv.Guild != nil {
		if _, err := s.requireChannel(c.User.ID, inv.Channel.ID, perm.ManageChannels); err != nil {
			fail(c, err)
			return
		}
	} else if inv.Inviter == nil || inv.Inviter.ID != c.User.ID {
		fail(c, ErrForbidden)
		return
	}
	if err := s.Store.DeleteInvite(code); err != nil {
		fail(c, err)
		return
	}
	if s.Hub != nil && inv.Guild != nil {
		s.Hub.DispatchGuild(inv.Guild.ID, "INVITE_DELETE", map[string]any{
			"channel_id": inv.Channel.ID, "guild_id": inv.Guild.ID, "code": code,
		})
	}
	c.NoContent()
}

func (s *Server) handleAcceptInvite(c *Ctx) {
	code := c.Param("code")
	inv, err := s.Store.Invite(code)
	if err != nil {
		fail(c, ErrUnknownInvite)
		return
	}
	if expired, _ := s.Store.InviteExpired(code); expired {
		fail(c, ErrUnknownInvite)
		return
	}
	if inv.Guild == nil {
		fail(c, NewError(400, 50020, "This invite does not belong to a guild"))
		return
	}
	if banned, err := s.Store.Bans(inv.Guild.ID); err == nil {
		for _, b := range banned {
			if b.User.ID == c.User.ID {
				fail(c, ErrForbidden)
				return
			}
		}
	}
	if _, err := s.Store.AddMember(inv.Guild.ID, c.User.ID); err != nil && !errors.Is(err, store.ErrConflict) {
		fail(c, err)
		return
	}
	_ = s.Store.ConsumeInvite(code)
	g, err := s.Store.Guild(inv.Guild.ID)
	if err != nil {
		fail(c, err)
		return
	}
	member, _ := s.Store.Member(inv.Guild.ID, c.User.ID)
	if s.Hub != nil {
		s.Hub.DispatchUser(c.User.ID, "GUILD_CREATE", g)
		s.Hub.DispatchGuild(inv.Guild.ID, "GUILD_MEMBER_ADD", member)
	}
	c.JSON(inv)
}

func (s *Server) handleCreateInvite(c *Ctx) {
	ch, err := s.requireChannel(c.User.ID, c.Param("channelID"), perm.CreateInstantInvite)
	if err != nil {
		fail(c, err)
		return
	}
	if ch.GuildID == nil {
		fail(c, ErrGuildOnly)
		return
	}
	var req struct {
		MaxAge     *int  `json:"max_age"`
		MaxUses    *int  `json:"max_uses"`
		Temporary  *bool `json:"temporary"`
		Unique     *bool `json:"unique"`
		TargetType *int  `json:"target_type"`
	}
	_ = c.Bind(&req)
	p := store.CreateInviteParams{ChannelID: ch.ID, GuildID: *ch.GuildID, InviterID: c.User.ID, MaxAge: 86400}
	if req.MaxAge != nil {
		p.MaxAge = *req.MaxAge
	}
	if p.MaxAge < 0 || p.MaxAge > 604800 {
		c.FailForm("max_age", "Must be between 0 and 604800")
		return
	}
	if req.MaxUses != nil {
		p.MaxUses = *req.MaxUses
	}
	if p.MaxUses < 0 || p.MaxUses > 100 {
		c.FailForm("max_uses", "Must be between 0 and 100")
		return
	}
	if req.Temporary != nil {
		p.Temporary = *req.Temporary
	}
	if req.Unique != nil {
		p.Unique = *req.Unique
	}
	inv, err := s.Store.CreateInvite(p)
	if err != nil {
		fail(c, err)
		return
	}
	if s.Hub != nil {
		s.Hub.DispatchGuild(*ch.GuildID, "INVITE_CREATE", inv)
	}
	c.JSONStatus(201, inv)
}
