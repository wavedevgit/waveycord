package api

import (
	"encoding/json"
	"regexp"
	"strings"

	"github.com/wavedevgit/waveycord/internal/store"
)

var roleMetadataKeyRe = regexp.MustCompile(`^[a-z0-9_]{1,50}$`)

// requireRoleConnectionScope enforces that the caller is an OAuth2 bearer token
// for this application carrying role_connections.write.
func (s *Server) requireRoleConnectionScope(c *Ctx, appID string) (store.OAuthToken, bool) {
	if !strings.HasPrefix(c.SessionID, "oauth:") {
		fail(c, ErrForbidden)
		return store.OAuthToken{}, false
	}
	token, _, err := s.Store.TokenByAccessHash(TokenHash(c.Token))
	if err != nil || token.AppID != appID {
		fail(c, ErrForbidden)
		return token, false
	}
	for _, scope := range token.Scopes {
		if scope == "role_connections.write" {
			return token, true
		}
	}
	fail(c, ErrForbidden)
	return token, false
}

func validateRoleMetadata(records []store.RoleMetadataRecord) string {
	if len(records) > 5 {
		return "at most 5 metadata records are allowed"
	}
	seen := map[string]bool{}
	for _, r := range records {
		if !roleMetadataKeyRe.MatchString(r.Key) {
			return "key must be 1-50 characters of lowercase letters, numbers or underscores"
		}
		if seen[r.Key] {
			return "metadata keys must be unique"
		}
		seen[r.Key] = true
		if len([]rune(r.Name)) < 1 || len([]rune(r.Name)) > 100 {
			return "name must be between 1 and 100 characters"
		}
		if len([]rune(r.Description)) < 1 || len([]rune(r.Description)) > 200 {
			return "description must be between 1 and 200 characters"
		}
		if r.Type < 1 || r.Type > 8 {
			return "type must be between 1 and 8"
		}
	}
	return ""
}

func (s *Server) handleGetRoleMetadata(c *Ctx) {
	app, ok := s.authorizeApplication(c, c.Param("applicationID"))
	if !ok {
		return
	}
	records, err := s.Store.RoleMetadata(app.ID)
	if err != nil {
		fail(c, err)
		return
	}
	c.JSON(records)
}

func (s *Server) handlePutRoleMetadata(c *Ctx) {
	app, ok := s.authorizeApplication(c, c.Param("applicationID"))
	if !ok {
		return
	}
	var records []store.RoleMetadataRecord
	if err := c.Bind(&records); err != nil {
		fail(c, ErrInvalidForm)
		return
	}
	if msg := validateRoleMetadata(records); msg != "" {
		c.FailForm("records", msg)
		return
	}
	if records == nil {
		records = []store.RoleMetadataRecord{}
	}
	saved, err := s.Store.SetRoleMetadata(app.ID, records)
	if err != nil {
		fail(c, err)
		return
	}
	c.JSON(saved)
}

func (s *Server) handleGetRoleConnection(c *Ctx) {
	if _, ok := s.requireRoleConnectionScope(c, c.Param("applicationID")); !ok {
		return
	}
	rc, err := s.Store.RoleConnection(c.User.ID, c.Param("applicationID"))
	if err != nil {
		fail(c, err)
		return
	}
	c.JSON(rc)
}

func (s *Server) handlePutRoleConnection(c *Ctx) {
	appID := c.Param("applicationID")
	if _, ok := s.requireRoleConnectionScope(c, appID); !ok {
		return
	}
	var req struct {
		PlatformName     *string           `json:"platform_name"`
		PlatformUsername *string           `json:"platform_username"`
		Metadata         map[string]string `json:"metadata"`
	}
	if err := c.Bind(&req); err != nil {
		fail(c, ErrInvalidForm)
		return
	}
	if req.PlatformName != nil && len([]rune(*req.PlatformName)) > 50 {
		c.FailForm("platform_name", "Must be 50 characters or fewer")
		return
	}
	if req.PlatformUsername != nil && len([]rune(*req.PlatformUsername)) > 100 {
		c.FailForm("platform_username", "Must be 100 characters or fewer")
		return
	}
	registry, err := s.Store.RoleMetadata(appID)
	if err != nil {
		fail(c, err)
		return
	}
	allowed := map[string]bool{}
	for _, r := range registry {
		allowed[r.Key] = true
	}
	for key, value := range req.Metadata {
		if !allowed[key] {
			c.FailForm("metadata", "Unknown metadata key: "+key)
			return
		}
		if len(value) > 200 {
			c.FailForm("metadata", "Metadata values must be 200 characters or fewer")
			return
		}
	}
	rc, err := s.Store.PutRoleConnection(c.User.ID, appID, req.PlatformName, req.PlatformUsername, req.Metadata)
	if err != nil {
		fail(c, err)
		return
	}
	c.JSON(rc)
}

var _ = json.Marshal
var _ = strings.TrimSpace
