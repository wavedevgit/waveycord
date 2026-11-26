package api

import (
	"github.com/wavedevgit/waveycord/internal/perm"
)

// handleChannelMemberPermissions returns one member's effective channel
// permissions. Callers need MANAGE_ROLES, matching the administrative surface.
func (s *Server) handleChannelMemberPermissions(c *Ctx) {
	ch, err := s.requireChannel(c.User.ID, c.Param("channelID"), perm.ManageRoles)
	if err != nil {
		fail(c, err)
		return
	}
	if ch.GuildID == nil {
		fail(c, ErrGuildOnly)
		return
	}
	target := c.Param("userID")
	if _, err := s.Store.Member(*ch.GuildID, target); err != nil {
		fail(c, ErrUnknownMember)
		return
	}
	perms, err := s.channelPermissions(target, ch)
	if err != nil {
		fail(c, err)
		return
	}
	c.JSON(map[string]any{"permissions": perm.Format(perms)})
}

// handleChannelRolePermissions resolves a role's channel permissions, ignoring
// member-specific overwrites.
func (s *Server) handleChannelRolePermissions(c *Ctx) {
	ch, err := s.requireChannel(c.User.ID, c.Param("channelID"), perm.ManageRoles)
	if err != nil {
		fail(c, err)
		return
	}
	if ch.GuildID == nil {
		fail(c, ErrGuildOnly)
		return
	}
	role, err := s.Store.Role(*ch.GuildID, c.Param("roleID"))
	if err != nil {
		fail(c, ErrUnknownRole)
		return
	}
	base, _ := perm.Parse(role.Permissions)
	if base&perm.Administrator != 0 {
		c.JSON(map[string]any{"permissions": perm.Format(perm.AllPermissions)})
		return
	}
	// @everyone base applies to every role holder only if this is @everyone; for
	// other roles the effective set is the role bits modified by that role's
	// overwrite and @everyone's overwrite.
	resolved := perm.Resolve(base, *ch.GuildID, []string{role.ID}, "", roleOverwrites(s, ch.ID, *ch.GuildID, role.ID))
	c.JSON(map[string]any{"permissions": perm.Format(resolved)})
}

func roleOverwrites(s *Server, channelID, everyoneID, roleID string) []perm.Overwrite {
	rows, err := s.Store.Overwrites(channelID)
	if err != nil {
		return nil
	}
	out := make([]perm.Overwrite, 0, len(rows))
	for _, ow := range rows {
		if ow.Type != perm.TypeRole || (ow.ID != everyoneID && ow.ID != roleID) {
			continue
		}
		allow, _ := perm.Parse(ow.Allow)
		deny, _ := perm.Parse(ow.Deny)
		out = append(out, perm.Overwrite{ID: ow.ID, Type: ow.Type, Allow: allow, Deny: deny})
	}
	return out
}

// handleGetRole returns one guild role.
func (s *Server) handleGetRole(c *Ctx) {
	g, err := s.requireGuild(c.User.ID, c.Param("guildID"), 0)
	if err != nil {
		fail(c, err)
		return
	}
	role, err := s.Store.Role(g.ID, c.Param("roleID"))
	if err != nil {
		fail(c, ErrUnknownRole)
		return
	}
	c.JSON(role)
}
