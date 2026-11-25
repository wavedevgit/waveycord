package api

import (
	"bytes"
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

	if s.forwardInteraction(c, app, body) {
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

// forwardInteraction relays a verified interaction to the application's endpoint,
// signed with the application's server key, and writes the endpoint response back.
// It returns false when no endpoint is configured (local dispatch applies).
func (s *Server) forwardInteraction(c *Ctx, app store.Application, raw []byte) bool {
	if app.InteractionsEndpointURL == nil || *app.InteractionsEndpointURL == "" {
		return false
	}
	seed, err := hex.DecodeString(app.SigningKey)
	if err != nil || len(seed) != ed25519.SeedSize {
		c.JSONStatus(500, map[string]any{"code": 0, "message": "Application signing key unavailable"})
		return true
	}
	key := ed25519.NewKeyFromSeed(seed)
	ts := strconv.FormatInt(time.Now().Unix(), 10)
	sig := hex.EncodeToString(ed25519.Sign(key, []byte(ts+string(raw))))
	req, err := http.NewRequestWithContext(c.R.Context(), http.MethodPost,
		*app.InteractionsEndpointURL, bytes.NewReader(raw))
	if err != nil {
		c.JSONStatus(502, map[string]any{"code": 0, "message": "Bad Gateway"})
		return true
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "DiscordBot (waveycord, 1.0)")
	req.Header.Set("X-Signature-Timestamp", ts)
	req.Header.Set("X-Signature-Ed25519", sig)
	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		s.cfg.Logger.Warn("interaction forward", "app", app.ID, "err", err)
		c.JSONStatus(502, map[string]any{"code": 0, "message": "Bad Gateway"})
		return true
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 || !json.Valid(body) {
		s.cfg.Logger.Warn("interaction endpoint rejected", "app", app.ID, "status", resp.StatusCode)
		c.JSONStatus(502, map[string]any{"code": 0, "message": "Bad Gateway"})
		return true
	}
	c.W.Header().Set("Content-Type", "application/json")
	c.W.WriteHeader(resp.StatusCode)
	_, _ = c.W.Write(body)
	return true
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
