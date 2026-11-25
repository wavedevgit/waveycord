package api

import (
	"errors"

	"github.com/wavedevgit/waveycord/internal/model"
	"github.com/wavedevgit/waveycord/internal/perm"
	"github.com/wavedevgit/waveycord/internal/store"
)

// guildPermissions computes a member's guild-level permissions. The guild owner
// holds every bit.
func (s *Server) guildPermissions(userID, guildID string) (uint64, error) {
	g, err := s.Store.Guild(guildID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return 0, ErrUnknownGuild
		}
		return 0, err
	}
	if g.OwnerID == userID {
		return perm.AllPermissions, nil
	}
	member, err := s.Store.IsMember(guildID, userID)
	if err != nil {
		return 0, err
	}
	if !member {
		return 0, ErrForbidden
	}
	return s.Store.BasePermissions(guildID, userID)
}

// channelPermissions computes the effective permissions in one channel,
// including overwrites and the thread parent inheritance rule.
func (s *Server) channelPermissions(userID string, ch model.Channel) (uint64, error) {
	if ch.GuildID == nil {
		// DM and group DM: every participant has the DM permission set.
		recipients, err := s.Store.Recipients(ch.ID)
		if err != nil {
			return 0, err
		}
		for _, r := range recipients {
			if r.ID == userID {
				return perm.ViewChannel | perm.SendMessages | perm.ReadMessageHistory |
					perm.AddReactions | perm.AttachFiles | perm.EmbedLinks | perm.ManageMessages |
					perm.ManageChannels | perm.CreateInstantInvite | perm.SendTTSMessages |
					perm.MentionEveryone | perm.UseExternalEmojis, nil
			}
		}
		return 0, ErrForbidden
	}
	base, err := s.guildPermissions(userID, *ch.GuildID)
	if err != nil {
		return 0, err
	}
	if base&perm.Administrator != 0 {
		return perm.AllPermissions, nil
	}
	target := ch
	if (ch.Type == store.ChannelPublicThread || ch.Type == store.ChannelPrivateThread ||
		ch.Type == store.ChannelAnnouncementThread) && ch.ParentID != nil {
		if parent, err := s.Store.Channel(*ch.ParentID); err == nil {
			target = parent
		}
	}
	overwrites, err := s.Store.Overwrites(target.ID)
	if err != nil {
		return 0, err
	}
	resolved := make([]perm.Overwrite, 0, len(overwrites))
	for _, ow := range overwrites {
		allow, _ := perm.Parse(ow.Allow)
		deny, _ := perm.Parse(ow.Deny)
		resolved = append(resolved, perm.Overwrite{ID: ow.ID, Type: ow.Type, Allow: allow, Deny: deny})
	}
	m, err := s.Store.Member(*ch.GuildID, userID)
	if err != nil {
		return 0, err
	}
	return perm.Resolve(base, *ch.GuildID, m.Roles, userID, resolved), nil
}

// requireGuild loads a guild and asserts guild-level permissions.
func (s *Server) requireGuild(userID, guildID string, bits uint64) (model.Guild, error) {
	g, err := s.Store.Guild(guildID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return g, ErrUnknownGuild
		}
		return g, err
	}
	if bits == 0 {
		if ok, _ := s.Store.IsMember(guildID, userID); !ok && g.OwnerID != userID {
			return g, ErrForbidden
		}
		return g, nil
	}
	have, err := s.guildPermissions(userID, guildID)
	if err != nil {
		return g, err
	}
	if !perm.Has(have, bits) {
		return g, ErrMissingPerms
	}
	return g, nil
}

// requireChannel loads a channel and asserts channel permissions.
func (s *Server) requireChannel(userID, channelID string, bits uint64) (model.Channel, error) {
	ch, err := s.Store.Channel(channelID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return ch, ErrUnknownChannel
		}
		return ch, err
	}
	have, err := s.channelPermissions(userID, ch)
	if err != nil {
		return ch, err
	}
	if !perm.Has(have, bits) {
		return ch, ErrMissingPerms
	}
	return ch, nil
}

// assertGuildChannel rejects DM channels on guild-only routes.
func assertGuildChannel(ch model.Channel) error {
	if ch.GuildID == nil {
		return ErrGuildOnly
	}
	return nil
}

// assertDMChannel rejects guild channels on DM-only routes.
func assertDMChannel(ch model.Channel) error {
	if ch.GuildID != nil {
		return ErrDMOnly
	}
	return nil
}
