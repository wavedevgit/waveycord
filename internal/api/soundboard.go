package api

import (
	"encoding/base64"
	"errors"
	"strings"

	"github.com/wavedevgit/waveycord/internal/perm"
	"github.com/wavedevgit/waveycord/internal/store"
)

// maxSoundboardBytes is the documented 512 KiB ceiling for sound uploads.
const maxSoundboardBytes = 512 * 1024

// defaultSoundboardSounds is the documented default set. Discord does not
// publish the full catalogue, so only the example sound is served.
func defaultSoundboardSounds() []store.SoundboardSound {
	quack := "🦆"
	return []store.SoundboardSound{{
		Name: "quack", SoundID: "1", Volume: 1, EmojiName: &quack, Available: true,
	}}
}

// handleDefaultSoundboardSounds lists the sounds every user can play.
func (s *Server) handleDefaultSoundboardSounds(c *Ctx) {
	c.JSON(defaultSoundboardSounds())
}

// handleGuildSoundboardSounds lists a guild's sounds.
func (s *Server) handleGuildSoundboardSounds(c *Ctx) {
	g, err := s.requireGuild(c.User.ID, c.Param("guildID"), 0)
	if err != nil {
		fail(c, err)
		return
	}
	sounds, err := s.Store.GuildSoundboardSounds(g.ID)
	if err != nil {
		fail(c, err)
		return
	}
	c.JSON(map[string]any{"items": sounds})
}

// handleGetGuildSoundboardSound returns one guild sound.
func (s *Server) handleGetGuildSoundboardSound(c *Ctx) {
	g, err := s.requireGuild(c.User.ID, c.Param("guildID"), 0)
	if err != nil {
		fail(c, err)
		return
	}
	sound, err := s.Store.GuildSoundboardSound(g.ID, c.Param("soundID"))
	if err != nil {
		failSoundboard(c, err)
		return
	}
	c.JSON(sound)
}

// handleCreateGuildSoundboardSound creates a guild sound.
func (s *Server) handleCreateGuildSoundboardSound(c *Ctx) {
	g, err := s.requireGuild(c.User.ID, c.Param("guildID"), perm.CreateGuildExpressions)
	if err != nil {
		fail(c, err)
		return
	}
	var req struct {
		Name      string   `json:"name"`
		Sound     string   `json:"sound"`
		Volume    *float64 `json:"volume"`
		EmojiID   *string  `json:"emoji_id"`
		EmojiName *string  `json:"emoji_name"`
	}
	if err := c.Bind(&req); err != nil {
		fail(c, ErrInvalidForm)
		return
	}
	if msg := validateSoundboardName(req.Name); msg != "" {
		c.FailForm("name", msg)
		return
	}
	if msg := validateSoundboardVolume(req.Volume); msg != "" {
		c.FailForm("volume", msg)
		return
	}
	if msg := validateSoundboardEmoji(req.EmojiID, req.EmojiName); msg != "" {
		c.FailForm("emoji_id", msg)
		return
	}
	if !validSoundboardData(c, req.Sound) {
		return
	}
	sound, err := s.Store.CreateGuildSoundboardSound(g.ID, c.User.ID, store.CreateSoundboardParams{
		Name: req.Name, Volume: req.Volume, EmojiID: req.EmojiID, EmojiName: req.EmojiName,
	})
	if err != nil {
		fail(c, err)
		return
	}
	s.audit(c, g.ID, store.AuditSoundboardSoundCreate, sound.SoundID, nil)
	if s.Hub != nil {
		s.Hub.DispatchGuild(g.ID, "GUILD_SOUNDBOARD_SOUND_CREATE", sound)
	}
	c.JSON(sound)
}

// handleUpdateGuildSoundboardSound modifies a guild sound.
func (s *Server) handleUpdateGuildSoundboardSound(c *Ctx) {
	g, err := s.requireGuild(c.User.ID, c.Param("guildID"), 0)
	if err != nil {
		fail(c, err)
		return
	}
	existing, err := s.Store.GuildSoundboardSound(g.ID, c.Param("soundID"))
	if err != nil {
		failSoundboard(c, err)
		return
	}
	if !s.canManageSoundboard(c.User.ID, g.ID, existing) {
		fail(c, NewError(403, 50013, "Missing Permissions"))
		return
	}
	var req struct {
		Name      *string  `json:"name"`
		Volume    *float64 `json:"volume"`
		EmojiID   *string  `json:"emoji_id"`
		EmojiName *string  `json:"emoji_name"`
	}
	if err := c.Bind(&req); err != nil {
		fail(c, ErrInvalidForm)
		return
	}
	if req.Name != nil {
		if msg := validateSoundboardName(*req.Name); msg != "" {
			c.FailForm("name", msg)
			return
		}
	}
	if msg := validateSoundboardVolume(req.Volume); msg != "" {
		c.FailForm("volume", msg)
		return
	}
	if req.EmojiID != nil || req.EmojiName != nil {
		if msg := validateSoundboardEmoji(req.EmojiID, req.EmojiName); msg != "" {
			c.FailForm("emoji_id", msg)
			return
		}
	}
	sound, err := s.Store.UpdateGuildSoundboardSound(g.ID, existing.SoundID, store.UpdateSoundboardParams{
		Name: req.Name, Volume: req.Volume, EmojiID: req.EmojiID, EmojiName: req.EmojiName,
	})
	if err != nil {
		failSoundboard(c, err)
		return
	}
	s.audit(c, g.ID, store.AuditSoundboardSoundUpdate, sound.SoundID, nil)
	if s.Hub != nil {
		s.Hub.DispatchGuild(g.ID, "GUILD_SOUNDBOARD_SOUND_UPDATE", sound)
	}
	c.JSON(sound)
}

// handleDeleteGuildSoundboardSound removes a guild sound.
func (s *Server) handleDeleteGuildSoundboardSound(c *Ctx) {
	g, err := s.requireGuild(c.User.ID, c.Param("guildID"), 0)
	if err != nil {
		fail(c, err)
		return
	}
	existing, err := s.Store.GuildSoundboardSound(g.ID, c.Param("soundID"))
	if err != nil {
		failSoundboard(c, err)
		return
	}
	if !s.canManageSoundboard(c.User.ID, g.ID, existing) {
		fail(c, NewError(403, 50013, "Missing Permissions"))
		return
	}
	if err := s.Store.DeleteGuildSoundboardSound(g.ID, existing.SoundID); err != nil {
		failSoundboard(c, err)
		return
	}
	s.audit(c, g.ID, store.AuditSoundboardSoundDelete, existing.SoundID, nil)
	if s.Hub != nil {
		s.Hub.DispatchGuild(g.ID, "GUILD_SOUNDBOARD_SOUND_DELETE", map[string]any{
			"guild_id": g.ID, "sound_id": existing.SoundID,
		})
	}
	c.NoContent()
}

// canManageSoundboard grants creators CREATE_GUILD_EXPRESSIONS and everyone
// else MANAGE_GUILD_EXPRESSIONS, matching the documented split.
func (s *Server) canManageSoundboard(userID, guildID string, sound store.SoundboardSound) bool {
	perms, err := s.guildPermissions(userID, guildID)
	if err != nil {
		return false
	}
	if perms&perm.ManageGuildExpressions != 0 || perms&perm.Administrator != 0 {
		return true
	}
	creator := ""
	if sound.User != nil {
		creator = sound.User.ID
	}
	return creator != "" && creator == userID && perms&perm.CreateGuildExpressions != 0
}

func failSoundboard(c *Ctx, err error) {
	if errors.Is(err, store.ErrNotFound) {
		fail(c, NewError(404, 10097, "Unknown sound"))
		return
	}
	fail(c, err)
}

func validateSoundboardName(name string) string {
	n := len([]rune(name))
	if n < 2 || n > 32 {
		return "Must be between 2 and 32 characters"
	}
	return ""
}

func validateSoundboardVolume(volume *float64) string {
	if volume != nil && (*volume < 0 || *volume > 1) {
		return "Must be between 0 and 1"
	}
	return ""
}

func validateSoundboardEmoji(id, name *string) string {
	if id != nil && name != nil && *id != "" && *name != "" {
		return "Only one of emoji_id or emoji_name may be set"
	}
	return ""
}

// validSoundboardData accepts a base64 mp3/ogg data URI within the bound.
func validSoundboardData(c *Ctx, data string) bool {
	data = strings.TrimSpace(data)
	if data == "" {
		c.FailForm("sound", "This field is required")
		return false
	}
	payload := data
	if strings.HasPrefix(data, "data:") {
		comma := strings.IndexByte(data, ',')
		if comma < 0 {
			c.FailForm("sound", "Must be base64 sound data")
			return false
		}
		header, rest := data[:comma], data[comma+1:]
		payload = rest
		if !strings.Contains(header, "audio/") || !strings.Contains(header, ";base64") {
			c.FailForm("sound", "Must be an audio data URI")
			return false
		}
	}
	raw, err := base64.StdEncoding.DecodeString(payload)
	if err != nil {
		raw, err = base64.RawStdEncoding.DecodeString(payload)
	}
	if err != nil || len(raw) == 0 {
		c.FailForm("sound", "Must be base64 sound data")
		return false
	}
	if len(raw) > maxSoundboardBytes {
		c.FailForm("sound", "Must be 512 KiB or smaller")
		return false
	}
	return true
}
