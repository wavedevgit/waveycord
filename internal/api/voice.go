package api

import (
	"errors"

	"github.com/wavedevgit/waveycord/internal/perm"
	"github.com/wavedevgit/waveycord/internal/store"
)

// handleGetVoiceStatus returns the caller's custom voice status in the channel.
func (s *Server) handleGetVoiceStatus(c *Ctx) {
	ch, err := s.requireChannel(c.User.ID, c.Param("channelID"), perm.ViewChannel)
	if err != nil {
		fail(c, err)
		return
	}
	status, err := s.Store.VoiceStatus(ch.ID, c.User.ID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			c.JSONStatus(404, map[string]any{"code": 10000, "message": "Unknown Voice State"})
			return
		}
		fail(c, err)
		return
	}
	var value *string
	if status != "" {
		value = &status
	}
	c.JSON(map[string]any{"status": value})
}

// handleUpdateVoiceStatus sets or clears the caller's voice status. Only a user
// currently connected to the channel has a voice state to update.
func (s *Server) handleUpdateVoiceStatus(c *Ctx) {
	ch, err := s.requireChannel(c.User.ID, c.Param("channelID"), perm.ViewChannel)
	if err != nil {
		fail(c, err)
		return
	}
	var req struct {
		Status *string `json:"status"`
	}
	if err := c.Bind(&req); err != nil {
		fail(c, ErrInvalidForm)
		return
	}
	value := ""
	if req.Status != nil {
		value = *req.Status
		if len([]rune(value)) > 140 {
			c.FailForm("status", "Must be 140 characters or fewer")
			return
		}
	}
	if err := s.Store.SetVoiceStatus(ch.ID, c.User.ID, value); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			c.JSONStatus(404, map[string]any{"code": 10000, "message": "Unknown Voice State"})
			return
		}
		fail(c, err)
		return
	}
	c.NoContent()
}
