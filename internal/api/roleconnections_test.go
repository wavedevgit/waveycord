package api_test

import (
	"encoding/json"
	"net/url"
	"testing"
)

func (h *harness) oauthToken(acc account, appID, secret, redirect, scope string) string {
	h.t.Helper()
	var approved struct{ Code string }
	status, body := h.form("/oauth2/authorize", url.Values{
		"client_id": {appID}, "response_type": {"code"}, "redirect_uri": {redirect},
		"scope": {scope}, "authorize": {"true"},
	}, acc.Token, "application/json")
	if status != 200 {
		h.t.Fatalf("consent status %d body %s", status, body)
	}
	if err := json.Unmarshal(body, &approved); err != nil {
		h.t.Fatalf("consent decode: %v", err)
	}
	status, body = h.form("/oauth2/token", url.Values{
		"grant_type": {"authorization_code"}, "code": {approved.Code},
		"redirect_uri": {redirect}, "client_id": {appID}, "client_secret": {secret},
	}, "", "application/json")
	if status != 200 {
		h.t.Fatalf("token status %d body %s", status, body)
	}
	var token struct {
		AccessToken string `json:"access_token"`
	}
	if err := json.Unmarshal(body, &token); err != nil || token.AccessToken == "" {
		h.t.Fatalf("token decode: %v body %s", err, body)
	}
	return token.AccessToken
}

func TestRoleConnectionsMetadataAndScope(t *testing.T) {
	h := newHarness(t)
	alice := h.register("rcowner")

	redirect := "https://example.test/rc"
	var app, other struct {
		ID           string `json:"id"`
		ClientSecret string `json:"client_secret"`
	}
	h.decode("POST", "/applications", alice.Token, map[string]any{"name": "Role App"}, &app)
	h.decode("POST", "/applications", alice.Token, map[string]any{"name": "Other App"}, &other)
	h.decode("PATCH", "/applications/"+app.ID, alice.Token, map[string]any{"redirect_uris": []string{redirect}}, nil)
	h.decode("PATCH", "/applications/"+other.ID, alice.Token, map[string]any{"redirect_uris": []string{redirect}}, nil)

	registry := []map[string]any{
		{"type": 1, "key": "rank", "name": "Rank", "description": "Leaderboard rank"},
		{"type": 7, "key": "verified", "name": "Verified", "description": "Account verified"},
	}
	var stored []struct {
		Key  string `json:"key"`
		Type int    `json:"type"`
	}
	if status := h.decode("PUT", "/applications/"+app.ID+"/role-connections/metadata", alice.Token, registry, &stored); status != 200 || len(stored) != 2 {
		t.Fatalf("put metadata status %d %+v", status, stored)
	}
	var fetched []struct{ Key string }
	if status := h.decode("GET", "/applications/"+app.ID+"/role-connections/metadata", alice.Token, nil, &fetched); status != 200 || len(fetched) != 2 {
		t.Fatalf("get metadata status %d %+v", status, fetched)
	}

	// Metadata registry validation.
	badRegistries := map[string][]map[string]any{
		"too many": {
			{"type": 1, "key": "a", "name": "A", "description": "d"},
			{"type": 1, "key": "b", "name": "B", "description": "d"},
			{"type": 1, "key": "c", "name": "C", "description": "d"},
			{"type": 1, "key": "d", "name": "D", "description": "d"},
			{"type": 1, "key": "e", "name": "E", "description": "d"},
			{"type": 1, "key": "f", "name": "F", "description": "d"},
		},
		"duplicate key": {
			{"type": 1, "key": "x", "name": "X", "description": "d"},
			{"type": 2, "key": "x", "name": "X2", "description": "d"},
		},
		"bad key":        {{"type": 1, "key": "Bad Key", "name": "X", "description": "d"}},
		"bad type":       {{"type": 9, "key": "k", "name": "K", "description": "d"}},
		"no name":        {{"type": 1, "key": "k", "description": "d"}},
		"no description": {{"type": 1, "key": "k", "name": "K"}},
	}
	for name, reg := range badRegistries {
		if status, _ := h.request("PUT", "/applications/"+app.ID+"/role-connections/metadata", alice.Token, reg); status != 400 {
			t.Fatalf("%s status %d", name, status)
		}
	}

	// Role connection requires the write scope and the matching application.
	writeToken := h.oauthToken(alice, app.ID, app.ClientSecret, redirect, "identify role_connections.write")
	readOnly := h.oauthToken(alice, app.ID, app.ClientSecret, redirect, "identify")
	otherToken := h.oauthToken(alice, other.ID, other.ClientSecret, redirect, "identify role_connections.write")

	var connection struct {
		PlatformName     *string           `json:"platform_name"`
		PlatformUsername *string           `json:"platform_username"`
		Metadata         map[string]string `json:"metadata"`
	}
	status, body := h.requestWithToken("PUT", "/users/@me/applications/"+app.ID+"/role-connection",
		"Bearer "+writeToken, map[string]any{
			"platform_name": "Steam", "platform_username": "ada",
			"metadata": map[string]any{"rank": "42", "verified": "true"},
		})
	if status != 200 {
		t.Fatalf("put role connection status %d body %s", status, body)
	}
	if err := json.Unmarshal(body, &connection); err != nil {
		t.Fatalf("decode connection: %v", err)
	}
	if connection.PlatformName == nil || *connection.PlatformName != "Steam" ||
		connection.Metadata["rank"] != "42" || connection.Metadata["verified"] != "true" {
		t.Fatalf("unexpected connection %+v", connection)
	}

	var reread struct {
		Metadata map[string]string `json:"metadata"`
	}
	if status := h.decode("GET", "/users/@me/applications/"+app.ID+"/role-connection", "Bearer "+writeToken, nil, &reread); status != 200 || reread.Metadata["rank"] != "42" {
		t.Fatalf("reread status %d %+v", status, reread)
	}

	// Unknown metadata keys are rejected.
	if status, _ := h.requestWithToken("PUT", "/users/@me/applications/"+app.ID+"/role-connection",
		"Bearer "+writeToken, map[string]any{"metadata": map[string]any{"hacked": "1"}}); status != 400 {
		t.Fatalf("unknown metadata key status %d", status)
	}
	// Non-string metadata values fail to bind.
	if status, _ := h.requestWithToken("PUT", "/users/@me/applications/"+app.ID+"/role-connection",
		"Bearer "+writeToken, map[string]any{"metadata": map[string]any{"rank": 42}}); status != 400 {
		t.Fatalf("non-string metadata status %d", status)
	}

	// Scope and application enforcement.
	if status, _ := h.requestWithToken("GET", "/users/@me/applications/"+app.ID+"/role-connection", "Bearer "+readOnly, nil); status != 403 {
		t.Fatalf("read token GET status %d", status)
	}
	if status, _ := h.requestWithToken("PUT", "/users/@me/applications/"+app.ID+"/role-connection", "Bearer "+readOnly,
		map[string]any{"metadata": map[string]any{}}); status != 403 {
		t.Fatalf("read token PUT status %d", status)
	}
	if status, _ := h.requestWithToken("GET", "/users/@me/applications/"+app.ID+"/role-connection", "Bearer "+otherToken, nil); status != 403 {
		t.Fatalf("wrong application status %d", status)
	}
	if status, _ := h.request("GET", "/users/@me/applications/"+app.ID+"/role-connection", alice.Token, nil); status != 403 {
		t.Fatalf("session token status %d", status)
	}

	// Registry management is owner/bot only.
	bob := h.register("rcother")
	if status, _ := h.request("PUT", "/applications/"+app.ID+"/role-connections/metadata", bob.Token, registry); status != 403 {
		t.Fatalf("foreign metadata status %d", status)
	}
}
