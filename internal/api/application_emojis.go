package api

import (
	"encoding/base64"
	"errors"
	"strings"

	"github.com/wavedevgit/waveycord/internal/store"
)

// maxEmojiImageBytes is the documented 256 KiB ceiling for emoji uploads.
const maxEmojiImageBytes = 256 * 1024

// applicationBotAccess resolves the application named in the path and requires
// the request to be authenticated as that application's bot. Application-scoped
// management routes use the bot token as their credential.
func (s *Server) applicationBotAccess(c *Ctx) (string, bool) {
	appID, ok := s.applicationBot(c)
	if !ok {
		return "", false
	}
	if named := c.Param("applicationID"); named != "" && named != appID {
		fail(c, NewError(404, 10002, "Unknown Application"))
		return "", false
	}
	return appID, true
}

// applicationBot resolves the calling bot's application without a path check.
func (s *Server) applicationBot(c *Ctx) (string, bool) {
	app, err := s.Store.ApplicationForBot(c.User.ID)
	if err != nil {
		fail(c, NewError(404, 10002, "Unknown Application"))
		return "", false
	}
	return app.ID, true
}

// validateEmojiName applies the documented 32-character nickname rule.
func validateEmojiName(c *Ctx, raw string) (string, bool) {
	name := strings.TrimSpace(raw)
	if name == "" || len([]rune(name)) > 32 {
		c.FailForm("name", "Must be between 1 and 32 characters")
		return "", false
	}
	if strings.ContainsAny(name, " ~`!@#$%^&*()+=[]{}|;:'\",.<>/?\\") {
		c.FailForm("name", "Must contain only alphanumerics and underscores")
		return "", false
	}
	return name, true
}

// handleListApplicationEmojis returns the application's emoji list under the
// documented items key.
func (s *Server) handleListApplicationEmojis(c *Ctx) {
	appID, ok := s.applicationBotAccess(c)
	if !ok {
		return
	}
	emojis, err := s.Store.ApplicationEmojis(appID)
	if err != nil {
		fail(c, err)
		return
	}
	c.JSON(map[string]any{"items": emojis})
}

// handleGetApplicationEmoji returns one application-owned emoji.
func (s *Server) handleGetApplicationEmoji(c *Ctx) {
	appID, ok := s.applicationBotAccess(c)
	if !ok {
		return
	}
	emoji, err := s.Store.ApplicationEmoji(appID, c.Param("emojiID"))
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			fail(c, ErrUnknownEmoji)
			return
		}
		fail(c, err)
		return
	}
	c.JSON(emoji)
}

// handleCreateApplicationEmoji creates an application-owned emoji. The image
// must be image data and fit the documented size limit.
func (s *Server) handleCreateApplicationEmoji(c *Ctx) {
	appID, ok := s.applicationBotAccess(c)
	if !ok {
		return
	}
	var req struct {
		Name     string `json:"name"`
		Image    string `json:"image"`
		Animated *bool  `json:"animated"`
	}
	if err := c.Bind(&req); err != nil {
		fail(c, ErrInvalidForm)
		return
	}
	name, ok := validateEmojiName(c, req.Name)
	if !ok {
		return
	}
	if _, ok := decodeEmojiImage(c, req.Image); !ok {
		return
	}
	animated := dataURIMediaType(req.Image) == "data:image/gif"
	if req.Animated != nil {
		animated = *req.Animated
	}
	emoji, err := s.Store.CreateApplicationEmoji(appID, c.User.ID, name, animated)
	if err != nil {
		fail(c, err)
		return
	}
	c.JSON(emoji)
}

// handleUpdateApplicationEmoji renames an application-owned emoji.
func (s *Server) handleUpdateApplicationEmoji(c *Ctx) {
	appID, ok := s.applicationBotAccess(c)
	if !ok {
		return
	}
	var req struct {
		Name *string `json:"name"`
	}
	if err := c.Bind(&req); err != nil {
		fail(c, ErrInvalidForm)
		return
	}
	if req.Name == nil {
		c.FailForm("name", "This field is required")
		return
	}
	name, ok := validateEmojiName(c, *req.Name)
	if !ok {
		return
	}
	emoji, err := s.Store.UpdateApplicationEmoji(appID, c.Param("emojiID"), &name)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			fail(c, ErrUnknownEmoji)
			return
		}
		fail(c, err)
		return
	}
	c.JSON(emoji)
}

// handleDeleteApplicationEmoji removes an application-owned emoji.
func (s *Server) handleDeleteApplicationEmoji(c *Ctx) {
	appID, ok := s.applicationBotAccess(c)
	if !ok {
		return
	}
	if err := s.Store.DeleteApplicationEmoji(appID, c.Param("emojiID")); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			fail(c, ErrUnknownEmoji)
			return
		}
		fail(c, err)
		return
	}
	c.NoContent()
}

// decodeEmojiImage validates image data and enforces the documented 256 KiB
// ceiling on the decoded bytes.
func decodeEmojiImage(c *Ctx, image string) ([]byte, bool) {
	image = strings.TrimSpace(image)
	if image == "" {
		c.FailForm("image", "This field is required")
		return nil, false
	}
	payload := image
	if strings.HasPrefix(image, "data:") {
		comma := strings.IndexByte(image, ',')
		header := image
		if comma >= 0 {
			header, payload = image[:comma], image[comma+1:]
		}
		if !strings.Contains(header, ";base64") {
			c.FailForm("image", "Must be base64 image data")
			return nil, false
		}
	}
	raw, err := base64.StdEncoding.DecodeString(payload)
	if err != nil {
		raw, err = base64.RawStdEncoding.DecodeString(payload)
	}
	if err != nil {
		c.FailForm("image", "Must be base64 image data")
		return nil, false
	}
	if len(raw) == 0 {
		c.FailForm("image", "Must contain image data")
		return nil, false
	}
	if len(raw) > maxEmojiImageBytes {
		c.FailForm("image", "Must be 256 KiB or smaller")
		return nil, false
	}
	return raw, true
}

// dataURIMediaType returns the media type of a data URI when present.
func dataURIMediaType(image string) string {
	if !strings.HasPrefix(image, "data:") {
		return ""
	}
	if semi := strings.IndexByte(image, ';'); semi > 5 {
		return image[:semi]
	}
	return ""
}
