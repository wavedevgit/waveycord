package api

import (
	"crypto/ed25519"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/wavedevgit/waveycord/internal/store"
)

// interactionPayload is the subset of the interaction object this server acts on.
type interactionPayload struct {
	ID            string  `json:"id"`
	Type          int     `json:"type"`
	ApplicationID string  `json:"application_id"`
	GuildID       *string `json:"guild_id"`
	ChannelID     *string `json:"channel_id"`
	Data          struct {
		ID      string          `json:"id"`
		Name    string          `json:"name"`
		Type    int             `json:"type"`
		Options json.RawMessage `json:"options"`
	} `json:"data"`
}

// Interaction types from the docs.
const (
	interactionPing                           = 1
	interactionApplicationCommand             = 2
	interactionMessageComponent               = 3
	interactionApplicationCommandAutocomplete = 4
	interactionModalSubmit                    = 5
)

// maxInteractionSkew is the allowed clock difference between the signature
// timestamp and the server, as recommended for replay protection.
const maxInteractionSkew = 5 * time.Minute

// handleInteraction receives signed interactions. The signature covers
// X-Signature-Timestamp concatenated with the exact request body, which is why the
// body is read raw before any decoding.
func (s *Server) handleInteraction(c *Ctx) {
	body, err := io.ReadAll(http.MaxBytesReader(c.W, c.R.Body, 1<<20))
	if err != nil {
		oauthError(c, 413, "invalid_request", "Interaction payload too large")
		return
	}
	tsHeader := c.R.Header.Get("X-Signature-Timestamp")
	sigHeader := c.R.Header.Get("X-Signature-Ed25519")
	if tsHeader == "" || sigHeader == "" {
		c.JSONStatus(401, map[string]any{"code": 0, "message": "401: Unauthorized"})
		return
	}
	ts, err := strconv.ParseInt(tsHeader, 10, 64)
	if err != nil || absDuration(time.Since(time.Unix(ts, 0))) > maxInteractionSkew {
		c.JSONStatus(401, map[string]any{"code": 0, "message": "401: Unauthorized"})
		return
	}
	var payload interactionPayload
	if err := json.Unmarshal(body, &payload); err != nil || payload.ApplicationID == "" {
		c.JSONStatus(400, map[string]any{"code": 50035, "message": "Invalid Form Body"})
		return
	}
	app, err := s.Store.Application(payload.ApplicationID)
	if err != nil {
		c.JSONStatus(401, map[string]any{"code": 0, "message": "401: Unauthorized"})
		return
	}
	pub, err := hex.DecodeString(strings.TrimSpace(app.VerifyKey))
	if err != nil || len(pub) != ed25519.PublicKeySize {
		c.JSONStatus(401, map[string]any{"code": 0, "message": "401: Unauthorized"})
		return
	}
	sig, err := hex.DecodeString(strings.TrimSpace(sigHeader))
	if err != nil || len(sig) != ed25519.SignatureSize {
		c.JSONStatus(401, map[string]any{"code": 0, "message": "401: Unauthorized"})
		return
	}
	if !ed25519.Verify(ed25519.PublicKey(pub), []byte(tsHeader+string(body)), sig) {
		c.JSONStatus(401, map[string]any{"code": 0, "message": "401: Unauthorized"})
		return
	}

	switch payload.Type {
	case interactionPing:
		c.JSON(map[string]any{"type": 1})
	case interactionApplicationCommand:
		s.dispatchInteraction(c, app, payload)
	case interactionApplicationCommandAutocomplete:
		c.JSON(map[string]any{"type": 8, "data": map[string]any{"choices": []any{}}})
	default:
		c.JSONStatus(400, map[string]any{"code": 50035, "message": "Unsupported interaction type"})
	}
}

func (s *Server) dispatchInteraction(c *Ctx, app store.Application, payload interactionPayload) {
	guildID := ""
	if payload.GuildID != nil {
		guildID = *payload.GuildID
	}
	if payload.Data.Name == "" {
		// An explicit data.id invocation resolves the command directly.
		if payload.Data.ID != "" {
			if cmd, err := s.Store.Command(payload.Data.ID); err == nil && cmd.ApplicationID == app.ID {
				options := cmd.Options
				resp := dispatchCommand(cmd, options)
				c.JSON(resp)
				return
			}
		}
		c.JSON(interactionMessage("Unknown command."))
		return
	}
	cmd, err := s.Store.CommandByName(app.ID, guildID, payload.Data.Name)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			flags := interactionMessage("Unknown command `" + payload.Data.Name + "`.")
			flags.Data["flags"] = 64
			c.JSON(flags)
			return
		}
		c.JSONStatus(400, map[string]any{"code": 50035, "message": "Invalid Form Body"})
		return
	}
	options := payload.Data.Options
	if len(options) == 0 {
		options = cmd.Options
	}
	c.JSON(dispatchCommand(cmd, options))
}

func absDuration(d time.Duration) time.Duration {
	if d < 0 {
		return -d
	}
	return d
}
