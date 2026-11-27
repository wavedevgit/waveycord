package api

import (
	"crypto/ed25519"
	"encoding/hex"
	"errors"
	"mime"
	"net/http"
	"net/url"
	"strings"

	"github.com/wavedevgit/waveycord/internal/perm"
	"github.com/wavedevgit/waveycord/internal/store"
)

// Supported OAuth2 scopes. The set follows the scopes table in the docs; scopes
// that only apply to bot tokens are accepted and carried through.
var oauthScopes = map[string]bool{
	"identify": true, "email": true, "connections": true, "guilds": true,
	"guilds.join": true, "guilds.members.read": true, "bot": true,
	"applications.commands": true, "voice": true, "messages.read": true,
	"dm_channels.read": true, "role_connections.write": true,
}

func validScopes(raw string) ([]string, bool) {
	fields := strings.Fields(raw)
	if len(fields) == 0 {
		return nil, false
	}
	out := make([]string, 0, len(fields))
	for _, f := range fields {
		if !oauthScopes[f] {
			return nil, false
		}
		out = append(out, f)
	}
	return out, true
}

type authorizeRequest struct {
	ClientID           string  `json:"client_id"`
	ResponseType       string  `json:"response_type"`
	RedirectURI        string  `json:"redirect_uri"`
	Scope              string  `json:"scope"`
	State              *string `json:"state"`
	GuildID            *string `json:"guild_id"`
	Permissions        *string `json:"permissions"`
	Authorize          bool    `json:"authorize"`
	DisableGuildSelect bool    `json:"disable_guild_select"`
}

func oauthError(c *Ctx, status int, code, description string) {
	c.JSONStatus(status, map[string]any{"error": code, "error_description": description})
}

// parseAuthorize reads JSON, form-encoded or query parameters.
func parseAuthorize(c *Ctx) (authorizeRequest, error) {
	var req authorizeRequest
	ct, _, _ := mime.ParseMediaType(c.R.Header.Get("Content-Type"))
	if ct == "application/x-www-form-urlencoded" || ct == "multipart/form-data" {
		if err := c.R.ParseForm(); err != nil {
			return req, err
		}
		req.ClientID = c.R.Form.Get("client_id")
		req.ResponseType = c.R.Form.Get("response_type")
		req.RedirectURI = c.R.Form.Get("redirect_uri")
		req.Scope = c.R.Form.Get("scope")
		if v := c.R.Form.Get("state"); v != "" {
			req.State = &v
		}
		if v := c.R.Form.Get("guild_id"); v != "" {
			req.GuildID = &v
		}
		if v := c.R.Form.Get("permissions"); v != "" {
			req.Permissions = &v
		}
		req.Authorize = c.R.Form.Get("authorize") == "true" || c.R.Form.Get("authorize") == "1"
		return req, nil
	}
	if c.R.ContentLength > 0 {
		if err := c.Bind(&req); err != nil {
			return req, err
		}
	}
	return req, nil
}

// handleAuthorizeGet renders the authorization request. The real endpoint returns
// an HTML consent page; with Accept: application/json this returns the same
// decision data so a client can drive consent programmatically.
func (s *Server) handleAuthorizeGet(c *Ctx) {
	var req authorizeRequest
	q := c.R.URL.Query()
	req.ClientID = q.Get("client_id")
	req.ResponseType = q.Get("response_type")
	req.RedirectURI = q.Get("redirect_uri")
	req.Scope = q.Get("scope")
	if v := q.Get("state"); v != "" {
		req.State = &v
	}
	if v := q.Get("guild_id"); v != "" {
		req.GuildID = &v
	}
	if v := q.Get("permissions"); v != "" {
		req.Permissions = &v
	}

	app, scopes, ok := s.validateAuthorize(c, req)
	if !ok {
		return
	}
	if strings.Contains(c.R.Header.Get("Accept"), "text/html") {
		c.W.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = c.W.Write([]byte(authorizePage(app, scopes, req)))
		return
	}
	c.JSON(map[string]any{
		"application":  app,
		"scopes":       scopes,
		"redirect_uri": req.RedirectURI,
		"state":        req.State,
	})
}

func authorizePage(app store.Application, scopes []string, req authorizeRequest) string {
	state := ""
	if req.State != nil {
		state = *req.State
	}
	escapedName := strings.NewReplacer("<", "&lt;", ">", "&gt;", "&", "&amp;", `"`, "&quot;").Replace(app.Name)
	_ = state
	return `<!doctype html><html><head><meta charset="utf-8"><title>Authorize ` + escapedName + `</title></head>
<body><h1>` + escapedName + ` wants access</h1><ul><li>` + strings.Join(scopes, "</li><li>") + `</li></ul>
<p>Submit this form as the logged-in user to approve.</p></body></html>`
}

// validateAuthorize checks the client, registered redirect and scopes.
func (s *Server) validateAuthorize(c *Ctx, req authorizeRequest) (store.Application, []string, bool) {
	app, err := s.Store.Application(req.ClientID)
	if err != nil {
		oauthError(c, 400, "invalid_request", "Unknown application")
		return app, nil, false
	}
	if req.ResponseType != "" && req.ResponseType != "code" {
		oauthError(c, 400, "unsupported_response_type", "Only response_type=code is supported")
		return app, nil, false
	}
	scopes, ok := validScopes(req.Scope)
	if !ok {
		oauthError(c, 400, "invalid_scope", "Unknown or empty scope")
		return app, nil, false
	}
	if !s.redirectAllowed(app, req.RedirectURI) {
		oauthError(c, 400, "invalid_request", "redirect_uri is not registered for this application")
		return app, nil, false
	}
	if req.Permissions != nil {
		if _, err := perm.Parse(*req.Permissions); err != nil {
			oauthError(c, 400, "invalid_request", "permissions must be a bitfield")
			return app, nil, false
		}
	}
	return app, scopes, true
}

func (s *Server) redirectAllowed(app store.Application, redirect string) bool {
	if redirect == "" {
		return false
	}
	u, err := url.Parse(redirect)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return false
	}
	if u.Scheme != "https" && u.Hostname() != "localhost" && u.Hostname() != "127.0.0.1" {
		return false
	}
	for _, allowed := range app.RedirectURIs {
		if allowed == redirect {
			return true
		}
	}
	return false
}

// handleAuthorizePost is the consent decision: it mints a code and, when the bot
// scope is present, installs the bot into the requested guild.
func (s *Server) handleAuthorizePost(c *Ctx) {
	req, err := parseAuthorize(c)
	if err != nil {
		oauthError(c, 400, "invalid_request", "Malformed request")
		return
	}
	app, scopes, ok := s.validateAuthorize(c, req)
	if !ok {
		return
	}
	if !req.Authorize {
		oauthError(c, 400, "access_denied", "Consent was not granted")
		return
	}
	wantsBot := false
	for _, sc := range scopes {
		if sc == "bot" {
			wantsBot = true
		}
	}
	if wantsBot {
		if req.GuildID == nil {
			oauthError(c, 400, "invalid_request", "guild_id is required when installing a bot")
			return
		}
		if _, err := s.requireGuild(c.User.ID, *req.GuildID, perm.ManageGuild); err != nil {
			oauthError(c, 403, "access_denied", "You cannot install this bot in that guild")
			return
		}
		if app.Bot == nil {
			oauthError(c, 400, "invalid_request", "Application has no bot user")
			return
		}
		if _, err := s.Store.AddMember(*req.GuildID, app.Bot.ID); err != nil {
			fail(c, err)
			return
		}
		member, _ := s.Store.Member(*req.GuildID, app.Bot.ID)
		_ = s.Store.UpsertBotIntegration(*req.GuildID, app.ID, app.Name, app.Bot.ID, scopes)
		if s.Hub != nil {
			s.Hub.DispatchGuild(*req.GuildID, "GUILD_MEMBER_ADD", member)
		}
	}
	code, err := s.Store.CreateAuthorizationCode(app.ID, c.User.ID, req.RedirectURI, scopes, req.GuildID, req.Permissions)
	if err != nil {
		fail(c, err)
		return
	}
	if strings.Contains(c.R.Header.Get("Accept"), "application/json") {
		c.JSON(map[string]any{"code": code, "state": req.State, "redirect_uri": req.RedirectURI})
		return
	}
	target, err := url.Parse(req.RedirectURI)
	if err != nil {
		fail(c, err)
		return
	}
	q := target.Query()
	q.Set("code", code)
	if req.State != nil {
		q.Set("state", *req.State)
	}
	target.RawQuery = q.Encode()
	http.Redirect(c.W, c.R, target.String(), http.StatusFound)
}

func (s *Server) handleOAuth2Token(c *Ctx) {
	ct, _, _ := mime.ParseMediaType(c.R.Header.Get("Content-Type"))
	form := map[string]string{}
	if ct == "application/x-www-form-urlencoded" || ct == "multipart/form-data" {
		if err := c.R.ParseForm(); err != nil {
			oauthError(c, 400, "invalid_request", "Malformed form")
			return
		}
		for k := range c.R.PostForm {
			form[k] = c.R.PostForm.Get(k)
		}
	} else {
		var body map[string]any
		if err := c.Bind(&body); err != nil {
			oauthError(c, 400, "invalid_request", "Malformed body")
			return
		}
		for k, v := range body {
			if s, ok := v.(string); ok {
				form[k] = s
			}
		}
	}
	app, err := s.Store.VerifyClientSecret(form["client_id"], form["client_secret"])
	if err != nil {
		if errors.Is(err, store.ErrInvalidClient) {
			oauthError(c, 401, "invalid_client", "Invalid client credentials")
			return
		}
		oauthError(c, 401, "invalid_client", "Invalid client credentials")
		return
	}

	switch form["grant_type"] {
	case "authorization_code":
		code, err := s.Store.ConsumeAuthorizationCode(form["code"], app.ID, form["redirect_uri"])
		if err != nil {
			oauthError(c, 400, "invalid_grant", "Code is invalid, expired or already used")
			return
		}
		token, access, refresh, err := s.Store.CreateOAuthToken(app.ID, code.UserID, code.Scopes)
		if err != nil {
			fail(c, err)
			return
		}
		c.JSON(map[string]any{
			"access_token": access, "token_type": "Bearer",
			"expires_in":    int((token.ExpiresAt - nowMillisFn()) / 1000),
			"refresh_token": refresh, "scope": strings.Join(code.Scopes, " "),
		})
	case "refresh_token":
		token, access, refresh, err := s.Store.RotateByRefreshHash(TokenHash(form["refresh_token"]), app.ID)
		if err != nil {
			oauthError(c, 400, "invalid_grant", "Refresh token is invalid or revoked")
			return
		}
		c.JSON(map[string]any{
			"access_token": access, "token_type": "Bearer",
			"expires_in":    int((token.ExpiresAt - nowMillisFn()) / 1000),
			"refresh_token": refresh, "scope": strings.Join(token.Scopes, " "),
		})
	default:
		oauthError(c, 400, "unsupported_grant_type", "Supported grants: authorization_code, refresh_token")
	}
}

// handleOAuth2Me reports the token's application, scopes and user.
func (s *Server) handleOAuth2Me(c *Ctx) {
	token, user, err := s.Store.TokenByAccessHash(TokenHash(c.Token))
	if err != nil {
		c.JSONStatus(401, map[string]any{"code": 0, "message": "401: Unauthorized"})
		return
	}
	app, err := s.Store.Application(token.AppID)
	if err != nil {
		fail(c, err)
		return
	}
	c.JSON(map[string]any{
		"application": app,
		"scopes":      token.Scopes,
		"expires":     isoTime(token.ExpiresAt),
		"user":        user,
	})
}

func (s *Server) handleUpdateApplication(c *Ctx) {
	var req struct {
		Name                    *string   `json:"name"`
		Description             *string   `json:"description"`
		RedirectURIs            *[]string `json:"redirect_uris"`
		VerifyKey               *string   `json:"verify_key"`
		InteractionsEndpointURL *string   `json:"interactions_endpoint_url"`
	}
	if err := c.Bind(&req); err != nil {
		fail(c, ErrInvalidForm)
		return
	}
	if req.RedirectURIs != nil {
		if len(*req.RedirectURIs) > 10 {
			c.FailForm("redirect_uris", "Must be 10 or fewer in length")
			return
		}
		for _, u := range *req.RedirectURIs {
			parsed, err := url.Parse(u)
			if err != nil || parsed.Scheme == "" || parsed.Host == "" {
				c.FailForm("redirect_uris", "Invalid URI")
				return
			}
		}
	}
	if req.VerifyKey != nil {
		key, err := hex.DecodeString(strings.TrimSpace(*req.VerifyKey))
		if err != nil || len(key) != ed25519.PublicKeySize {
			c.FailForm("verify_key", "Must be a 32-byte Ed25519 public key in hex")
			return
		}
		trimmed := strings.ToLower(strings.TrimSpace(*req.VerifyKey))
		req.VerifyKey = &trimmed
	}
	if req.InteractionsEndpointURL != nil && *req.InteractionsEndpointURL != "" {
		parsed, err := url.Parse(*req.InteractionsEndpointURL)
		if err != nil || (parsed.Scheme != "https" && !(parsed.Scheme == "http" && (parsed.Hostname() == "localhost" || parsed.Hostname() == "127.0.0.1"))) || parsed.Host == "" {
			c.FailForm("interactions_endpoint_url", "Must be an https URL (http is allowed for localhost)")
			return
		}
	}
	appID := c.Param("applicationID")
	ownerID := c.User.ID
	if appID == "@me" {
		// PATCH /applications/@me targets the bot's own application; the generic
		// route matches the literal "@me" first. The bot is not the owner, so the
		// ownership check runs against the application's owner.
		own, err := s.Store.ApplicationForBot(c.User.ID)
		if err != nil {
			c.JSONStatus(404, map[string]any{"code": 10002, "message": "Unknown Application"})
			return
		}
		appID = own.ID
		ownerID = own.OwnerID
	}
	app, err := s.Store.UpdateApplication(appID, ownerID, req.Name, req.Description, req.RedirectURIs, req.VerifyKey, req.InteractionsEndpointURL)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			c.JSONStatus(404, map[string]any{"code": 10002, "message": "Unknown Application"})
			return
		}
		fail(c, err)
		return
	}
	c.JSON(app)
}
