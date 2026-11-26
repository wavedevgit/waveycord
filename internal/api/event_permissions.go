package api

import (
	"github.com/wavedevgit/waveycord/internal/perm"
	"github.com/wavedevgit/waveycord/internal/store"
)

func (s *Server) eventPermission(userID string, event store.ScheduledEvent, action string) error {
	have, err := s.guildPermissions(userID, event.GuildID)
	if err != nil {
		return err
	}
	if event.ChannelID != nil {
		ch, err := s.Store.Channel(*event.ChannelID)
		if err != nil {
			return err
		}
		have, err = s.channelPermissions(userID, ch)
		if err != nil {
			return err
		}
	}
	required := uint64(0)
	if event.EntityType != 3 {
		required = perm.ViewChannel
	}
	if action != "read" {
		required |= perm.CreateEvents
		if action != "create" {
			if event.CreatorID == nil || *event.CreatorID != userID || !perm.Has(have, perm.CreateEvents) {
				required = (required &^ perm.CreateEvents) | perm.ManageEvents
			}
		}
		if event.EntityType == 1 {
			required |= perm.ManageChannels | perm.MuteMembers | perm.MoveMembers
		}
		if event.EntityType == 2 {
			required |= perm.Connect
		}
	}
	if !perm.Has(have, required) {
		return ErrMissingPerms
	}
	return nil
}
