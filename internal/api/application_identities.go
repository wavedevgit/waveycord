package api

import (
	"encoding/json"
	"errors"
	"strings"

	"github.com/wavedevgit/waveycord/internal/store"
)

// identityScope is the OAuth2 scope every application-identity route requires.
const identityScope = "application_identities.write"

// identityAccess resolves the bot application and checks that the target user
// granted it the identity scope. The application in the path must be the bot's.
func (s *Server) identityAccess(c *Ctx, appParam, userParam string) (string, bool) {
	appID, ok := s.applicationBot(c)
	if !ok {
		return "", false
	}
	if appID != c.Param(appParam) {
		fail(c, NewError(404, 10002, "Unknown Application"))
		return "", false
	}
	userID := c.Param(userParam)
	if _, err := s.Store.UserByID(userID); err != nil {
		fail(c, ErrUnknownUser)
		return "", false
	}
	granted, err := s.Store.UserAuthorizedApplication(appID, userID, identityScope)
	if err != nil {
		fail(c, err)
		return "", false
	}
	if !granted {
		fail(c, NewError(403, 50013, "Missing Permissions"))
		return "", false
	}
	return appID, true
}

// handleGetUserApplicationIdentities lists a user's identities for an app.
func (s *Server) handleGetUserApplicationIdentities(c *Ctx) {
	appID, ok := s.identityAccess(c, "applicationID", "userID")
	if !ok {
		return
	}
	identities, err := s.Store.ApplicationIdentities(appID, c.Param("userID"))
	if err != nil {
		fail(c, err)
		return
	}
	c.JSON(map[string]any{"identities": identities})
}

// handleGetApplicationIdentities resolves identities from an external key.
func (s *Server) handleGetApplicationIdentities(c *Ctx) {
	appID, ok := s.applicationBot(c)
	if !ok {
		return
	}
	if appID != c.Param("applicationID") {
		fail(c, NewError(404, 10002, "Unknown Application"))
		return
	}
	identities, err := s.Store.ApplicationIdentitiesByExternalID(appID,
		c.Param("providerType"), c.Param("providerIssuedUserID"), c.R.URL.Query().Get("provider_id"))
	if err != nil {
		fail(c, err)
		return
	}
	c.JSON(map[string]any{"identities": identities})
}

// handleGetApplicationIdentityProfile returns a stored profile.
func (s *Server) handleGetApplicationIdentityProfile(c *Ctx) {
	appID, ok := s.identityAccess(c, "applicationID", "userID")
	if !ok {
		return
	}
	profile, err := s.Store.ApplicationIdentityProfile(appID, c.Param("userID"), c.Param("providerIssuedUserID"))
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			fail(c, NewError(404, 0, "Unknown Application Identity"))
			return
		}
		fail(c, err)
		return
	}
	c.JSON(profile)
}

// handleUpdateApplicationIdentityProfile writes a profile. Data is replaced
// whole when present; omitting it leaves the previous payload untouched.
func (s *Server) handleUpdateApplicationIdentityProfile(c *Ctx) {
	appID, ok := s.identityAccess(c, "applicationID", "userID")
	if !ok {
		return
	}
	var req struct {
		Username *string         `json:"username"`
		Metadata json.RawMessage `json:"metadata"`
		Data     json.RawMessage `json:"data"`
	}
	if err := c.Bind(&req); err != nil {
		fail(c, ErrInvalidForm)
		return
	}
	if req.Username != nil && len([]rune(*req.Username)) > 1024 {
		c.FailForm("username", "Must be 1024 characters or fewer")
		return
	}
	replaceMetadata := req.Metadata != nil
	replaceData := req.Data != nil
	if replaceData {
		if msg := validateProfileData(req.Data); msg != "" {
			c.FailForm("data", msg)
			return
		}
	}
	var metadata any
	if replaceMetadata {
		if err := json.Unmarshal(req.Metadata, &metadata); err != nil {
			c.FailForm("metadata", "Must be valid JSON")
			return
		}
	}
	var data any
	if replaceData {
		if err := json.Unmarshal(req.Data, &data); err != nil {
			c.FailForm("data", "Must be valid JSON")
			return
		}
	}
	profile, created, err := s.Store.UpsertApplicationIdentityProfile(appID, c.Param("userID"),
		c.Param("providerIssuedUserID"), req.Username, metadata, data, replaceData, replaceMetadata)
	if err != nil {
		if errors.Is(err, store.ErrConflict) {
			c.FailForm("provider_issued_user_id", "Provider user ID does not match existing identity record")
			return
		}
		fail(c, err)
		return
	}
	if created {
		c.JSONStatus(201, profile)
		return
	}
	c.NoContent()
}

// handleDeleteApplicationIdentity removes one identity and its profile.
func (s *Server) handleDeleteApplicationIdentity(c *Ctx) {
	appID, ok := s.identityAccess(c, "applicationID", "userID")
	if !ok {
		return
	}
	err := s.Store.DeleteApplicationIdentity(appID, c.Param("userID"),
		c.Param("providerType"), c.Param("providerIssuedUserID"))
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			fail(c, NewError(404, 0, "Unknown Application Identity"))
			return
		}
		fail(c, err)
		return
	}
	c.NoContent()
}

// validateProfileData enforces the documented serialized and field limits.
func validateProfileData(raw json.RawMessage) string {
	if len(raw) > 10*1024 {
		return "Profile data is too large, must be less than 10KB"
	}
	var payload struct {
		Primary map[string]json.RawMessage `json:"primary"`
		Dynamic []struct {
			Type  int             `json:"type"`
			Name  string          `json:"name"`
			Value json.RawMessage `json:"value"`
		} `json:"dynamic"`
	}
	if err := json.Unmarshal(raw, &payload); err != nil {
		return "Must be an object"
	}
	if len(payload.Dynamic) > 30 {
		return "Must contain 30 dynamic fields or fewer"
	}
	for _, field := range payload.Dynamic {
		if field.Type < 1 || field.Type > 3 {
			return "Dynamic field type must be 1, 2 or 3"
		}
		if len([]rune(field.Name)) == 0 || len([]rune(field.Name)) > 100 {
			return "Dynamic field name must be between 1 and 100 characters"
		}
		if field.Type == 1 {
			var value string
			if err := json.Unmarshal(field.Value, &value); err != nil {
				return "String dynamic fields need a string value"
			}
			if len([]rune(value)) > 256 {
				return "Dynamic string values must be 256 characters or fewer"
			}
		}
	}
	for key, value := range payload.Primary {
		if !strings.HasSuffix(key, "_image") {
			var text string
			if err := json.Unmarshal(value, &text); err == nil && len([]rune(text)) > 100 {
				return "Primary string values must be 100 characters or fewer"
			}
		}
	}
	return ""
}
