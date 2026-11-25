package api_test

import (
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"
)

func (h *harness) form(path string, values url.Values, token string, accept string) (int, []byte) {
	h.t.Helper()
	req, err := http.NewRequest("POST", h.server.URL+"/api/v10"+path, strings.NewReader(values.Encode()))
	if err != nil {
		h.t.Fatalf("request: %v", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if accept != "" {
		req.Header.Set("Accept", accept)
	}
	if token != "" {
		req.Header.Set("Authorization", token)
	}
	resp, err := h.server.Client().Do(req)
	if err != nil {
		h.t.Fatalf("do: %v", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, body
}

func TestOAuth2AuthorizationCodeFlow(t *testing.T) {
	h := newHarness(t)
	alice := h.register("oauthowner")
	var guild struct{ ID string }
	h.decode("POST", "/guilds", alice.Token, map[string]any{"name": "OAuth Guild"}, &guild)

	var app struct {
		ID           string `json:"id"`
		Name         string `json:"name"`
		ClientSecret string `json:"client_secret"`
		Bot          *struct {
			ID  string `json:"id"`
			Bot bool   `json:"bot"`
		} `json:"bot"`
	}
	if status := h.decode("POST", "/applications", alice.Token, map[string]any{
		"name": "OAuth App",
	}, &app); status != 201 {
		t.Fatalf("create app status %d", status)
	}
	if app.ClientSecret == "" || app.Bot == nil {
		t.Fatalf("application missing secret or bot: %+v", app)
	}

	redirect := "https://example.test/cb"
	var patched struct {
		RedirectURIs []string `json:"redirect_uris"`
	}
	if status := h.decode("PATCH", "/applications/"+app.ID, alice.Token, map[string]any{
		"redirect_uris": []string{redirect},
	}, &patched); status != 200 || len(patched.RedirectURIs) != 1 {
		t.Fatalf("patch app status %d %+v", status, patched)
	}

	// The consent screen can be inspected without approving anything.
	var consent struct {
		Application struct{ ID string } `json:"application"`
		Scopes      []string            `json:"scopes"`
	}
	status := h.decode("GET", "/oauth2/authorize?client_id="+app.ID+
		"&response_type=code&scope=identify+bot&redirect_uri="+url.QueryEscape(redirect)+
		"&guild_id="+guild.ID, alice.Token, nil, &consent)
	if status != 200 || consent.Application.ID != app.ID || len(consent.Scopes) != 2 {
		t.Fatalf("authorize view status %d %+v", status, consent)
	}

	// Approve: consent mints a code and installs the bot into the guild.
	var approved struct {
		Code  string  `json:"code"`
		State *string `json:"state"`
	}
	status, body := h.form("/oauth2/authorize", url.Values{
		"client_id": {app.ID}, "response_type": {"code"}, "redirect_uri": {redirect},
		"scope": {"identify bot"}, "guild_id": {guild.ID}, "state": {"xyz"}, "authorize": {"true"},
	}, alice.Token, "application/json")
	if status != 200 {
		t.Fatalf("approve status %d body %s", status, body)
	}
	if err := json.Unmarshal(body, &approved); err != nil {
		t.Fatalf("approve decode: %v", err)
	}
	if approved.Code == "" || approved.State == nil || *approved.State != "xyz" {
		t.Fatalf("approve payload %+v", approved)
	}

	// Browser-style consent redirects with code and state in the query.
	values := url.Values{
		"client_id": {app.ID}, "response_type": {"code"}, "redirect_uri": {redirect},
		"scope": {"identify"}, "state": {"redir"}, "authorize": {"true"},
	}
	req, err := http.NewRequest("POST", h.server.URL+"/api/v10/oauth2/authorize", strings.NewReader(values.Encode()))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Authorization", alice.Token)
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("redirect consent: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusFound {
		t.Fatalf("redirect consent status %d", resp.StatusCode)
	}
	location, err := url.Parse(resp.Header.Get("Location"))
	if err != nil {
		t.Fatalf("location: %v", err)
	}
	if location.Host != "example.test" || location.Query().Get("code") == "" || location.Query().Get("state") != "redir" {
		t.Fatalf("unexpected redirect %q", resp.Header.Get("Location"))
	}

	var botMember struct {
		User struct {
			ID  string `json:"id"`
			Bot bool   `json:"bot"`
		} `json:"user"`
	}
	if status := h.decode("GET", "/guilds/"+guild.ID+"/members/"+app.Bot.ID, alice.Token, nil, &botMember); status != 200 || !botMember.User.Bot {
		t.Fatalf("bot not installed: status %d %+v", status, botMember)
	}

	// A bad client secret is rejected before the code is consumed.
	if status, _ := h.form("/oauth2/token", url.Values{
		"grant_type": {"authorization_code"}, "code": {approved.Code},
		"redirect_uri": {redirect}, "client_id": {app.ID}, "client_secret": {"wrong"},
	}, "", "application/json"); status != 401 {
		t.Fatalf("wrong secret status %d", status)
	}

	var token struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
		TokenType    string `json:"token_type"`
		ExpiresIn    int    `json:"expires_in"`
		Scope        string `json:"scope"`
	}
	status, body = h.form("/oauth2/token", url.Values{
		"grant_type": {"authorization_code"}, "code": {approved.Code},
		"redirect_uri": {redirect}, "client_id": {app.ID}, "client_secret": {app.ClientSecret},
	}, "", "application/json")
	if status != 200 {
		t.Fatalf("token status %d body %s", status, body)
	}
	if err := json.Unmarshal(body, &token); err != nil {
		t.Fatalf("token decode: %v", err)
	}
	if token.AccessToken == "" || token.RefreshToken == "" || token.TokenType != "Bearer" || token.Scope != "identify bot" {
		t.Fatalf("unexpected token response %+v", token)
	}

	var me struct{ ID string }
	if status := h.decode("GET", "/users/@me", "Bearer "+token.AccessToken, nil, &me); status != 200 || me.ID != alice.ID {
		t.Fatalf("oauth bearer self status %d %+v", status, me)
	}

	var info struct {
		Application struct{ ID string } `json:"application"`
		Scopes      []string            `json:"scopes"`
		User        struct{ ID string } `json:"user"`
		Expires     string              `json:"expires"`
	}
	if status := h.decode("GET", "/oauth2/@me", "Bearer "+token.AccessToken, nil, &info); status != 200 ||
		info.Application.ID != app.ID || len(info.Scopes) != 2 || info.User.ID != alice.ID || info.Expires == "" {
		t.Fatalf("oauth2 me status %d %+v", status, info)
	}

	// The code is single use.
	if status, _ := h.form("/oauth2/token", url.Values{
		"grant_type": {"authorization_code"}, "code": {approved.Code},
		"redirect_uri": {redirect}, "client_id": {app.ID}, "client_secret": {app.ClientSecret},
	}, "", "application/json"); status != 400 {
		t.Fatalf("reused code status %d", status)
	}

	// Refresh rotates both tokens.
	var refreshed struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
	}
	status, body = h.form("/oauth2/token", url.Values{
		"grant_type": {"refresh_token"}, "refresh_token": {token.RefreshToken},
		"client_id": {app.ID}, "client_secret": {app.ClientSecret},
	}, "", "application/json")
	if status != 200 {
		t.Fatalf("refresh status %d body %s", status, body)
	}
	if err := json.Unmarshal(body, &refreshed); err != nil {
		t.Fatalf("refresh decode: %v", err)
	}
	if refreshed.AccessToken == "" || refreshed.AccessToken == token.AccessToken || refreshed.RefreshToken == token.RefreshToken {
		t.Fatalf("refresh did not rotate tokens: %+v", refreshed)
	}
	if status, _ := h.request("GET", "/users/@me", "Bearer "+refreshed.AccessToken, nil); status != 200 {
		t.Fatalf("new access token rejected: %d", status)
	}
	if status, _ := h.form("/oauth2/token", url.Values{
		"grant_type": {"refresh_token"}, "refresh_token": {token.RefreshToken},
		"client_id": {app.ID}, "client_secret": {app.ClientSecret},
	}, "", "application/json"); status != 400 {
		t.Fatalf("old refresh token still valid: %d", status)
	}
}

func TestOAuth2RejectsInvalidRequests(t *testing.T) {
	h := newHarness(t)
	alice := h.register("oauthstrict")
	bob := h.register("oauthother")
	var guild struct{ ID string }
	h.decode("POST", "/guilds", alice.Token, map[string]any{"name": "Strict Guild"}, &guild)

	var app struct {
		ID           string `json:"id"`
		ClientSecret string `json:"client_secret"`
	}
	h.decode("POST", "/applications", alice.Token, map[string]any{"name": "Strict App"}, &app)
	h.decode("PATCH", "/applications/"+app.ID, alice.Token, map[string]any{
		"redirect_uris": []string{"https://example.test/cb"},
	}, nil)

	base := map[string]any{
		"client_id": app.ID, "response_type": "code",
		"redirect_uri": "https://example.test/cb", "scope": "identify", "authorize": true,
	}
	// Unregistered redirect.
	bad := cloneMap(base)
	bad["redirect_uri"] = "https://evil.test/cb"
	if status, _ := h.request("POST", "/oauth2/authorize", alice.Token, bad); status != 400 {
		t.Fatalf("unregistered redirect status %d", status)
	}
	// Unknown scope.
	bad = cloneMap(base)
	bad["scope"] = "identify telepathy"
	if status, _ := h.request("POST", "/oauth2/authorize", alice.Token, bad); status != 400 {
		t.Fatalf("unknown scope status %d", status)
	}
	// Bot scope without a guild.
	bad = cloneMap(base)
	bad["scope"] = "bot"
	if status, _ := h.request("POST", "/oauth2/authorize", alice.Token, bad); status != 400 {
		t.Fatalf("bot without guild status %d", status)
	}
	// Bot install into a guild the user does not manage.
	bobGuild := cloneMap(base)
	bobGuild["scope"] = "bot"
	bobGuild["guild_id"] = guild.ID
	if status, _ := h.request("POST", "/oauth2/authorize", bob.Token, bobGuild); status != 403 {
		t.Fatalf("foreign guild install status %d", status)
	}
	// Only the owner may change redirect URIs.
	if status, _ := h.request("PATCH", "/applications/"+app.ID, bob.Token, map[string]any{
		"redirect_uris": []string{"https://bob.test/cb"},
	}); status != 404 {
		t.Fatalf("foreign app patch status %d", status)
	}
	// Unknown grant type.
	if status, _ := h.form("/oauth2/token", url.Values{
		"grant_type": {"password"}, "client_id": {app.ID}, "client_secret": {app.ClientSecret},
	}, "", "application/json"); status != 400 {
		t.Fatalf("unknown grant status %d", status)
	}
}

func cloneMap(in map[string]any) map[string]any {
	out := make(map[string]any, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}
