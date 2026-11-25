package api_test

import (
	"strings"
	"testing"
)

func TestApplicationsAndBotAuth(t *testing.T) {
	h := newHarness(t)
	alice := h.register("developer")

	var app struct {
		ID    string `json:"id"`
		Name  string `json:"name"`
		Token string `json:"token"`
		Bot   *struct {
			ID       string `json:"id"`
			Username string `json:"username"`
			Bot      bool   `json:"bot"`
		} `json:"bot"`
		VerifyKey string `json:"verify_key"`
	}
	if status := h.decode("POST", "/applications", alice.Token, map[string]any{
		"name": "Wavey Bot", "description": "integration test",
	}, &app); status != 201 {
		t.Fatalf("create application status %d", status)
	}
	if app.Token == "" || app.Bot == nil || !app.Bot.Bot || app.VerifyKey == "" {
		t.Fatalf("incomplete application %+v", app)
	}

	// The bot token authenticates as the bot account.
	botAuth := "Bot " + app.Token
	var me struct {
		ID       string `json:"id"`
		Username string `json:"username"`
		Bot      bool   `json:"bot"`
	}
	if status := h.decode("GET", "/users/@me", botAuth, nil, &me); status != 200 || me.ID != app.Bot.ID || !me.Bot {
		t.Fatalf("bot self status %d %+v", status, me)
	}
	var fetched struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	}
	if status := h.decode("GET", "/applications/@me", botAuth, nil, &fetched); status != 200 || fetched.ID != app.ID || fetched.Name != "Wavey Bot" {
		t.Fatalf("applications/@me status %d %+v", status, fetched)
	}

	// Reset rotates the token and invalidates the previous one.
	var rotated struct{ Token string }
	if status := h.decode("POST", "/applications/"+app.ID+"/bot/reset", alice.Token, nil, &rotated); status != 200 || rotated.Token == "" || rotated.Token == app.Token {
		t.Fatalf("reset status %d", status)
	}
	if status, _ := h.request("GET", "/users/@me", botAuth, nil); status != 401 {
		t.Fatalf("old bot token still valid: %d", status)
	}
	if status, _ := h.request("GET", "/users/@me", "Bot "+rotated.Token, nil); status != 200 {
		t.Fatalf("rotated token rejected: %d", status)
	}

	// A different account cannot rotate the token.
	bob := h.register("intruderdev")
	if status, _ := h.request("POST", "/applications/"+app.ID+"/bot/reset", bob.Token, nil); status != 403 {
		t.Fatalf("foreign reset status %d", status)
	}
}

func TestGuildMessageSearch(t *testing.T) {
	h := newHarness(t)
	alice := h.register("searcher")
	var guild struct{ ID string }
	h.decode("POST", "/guilds", alice.Token, map[string]any{"name": "Search"}, &guild)
	var general, private struct{ ID string }
	h.decode("POST", "/guilds/"+guild.ID+"/channels", alice.Token, map[string]any{"name": "general"}, &general)
	h.decode("POST", "/guilds/"+guild.ID+"/channels", alice.Token, map[string]any{"name": "private"}, &private)
	h.request("POST", "/channels/"+general.ID+"/messages", alice.Token, map[string]any{"content": "the quick brown fox"})
	h.request("POST", "/channels/"+general.ID+"/messages", alice.Token, map[string]any{"content": "quick response"})
	h.request("POST", "/channels/"+private.ID+"/messages", alice.Token, map[string]any{"content": "quick secret"})

	var result struct {
		Messages [][]struct {
			ID        string `json:"id"`
			Content   string `json:"content"`
			ChannelID string `json:"channel_id"`
		} `json:"messages"`
		Total int `json:"total_results"`
	}
	if status := h.decode("GET", "/guilds/"+guild.ID+"/messages/search?content=quick", alice.Token, nil, &result); status != 200 {
		t.Fatalf("search status %d", status)
	}
	if result.Total != 3 || len(result.Messages) != 3 {
		t.Fatalf("search total %d groups %d", result.Total, len(result.Messages))
	}

	// Restrict by channel query parameter is not documented as a filter, so the
	// second account only sees what it can read. Deny VIEW_CHANNEL on #private.
	bob := h.register("outsider")
	var invite struct{ Code string }
	h.decode("POST", "/channels/"+general.ID+"/invites", alice.Token, map[string]any{}, &invite)
	h.request("POST", "/invites/"+invite.Code, bob.Token, nil)
	if status, _ := h.request("PUT", "/channels/"+private.ID+"/permissions/"+guild.ID, alice.Token, map[string]any{
		"type": 0, "allow": "0", "deny": "1024",
	}); status != 204 {
		t.Fatalf("deny view status %d", status)
	}
	var bobResult struct {
		Total int `json:"total_results"`
	}
	h.decode("GET", "/guilds/"+guild.ID+"/messages/search?content=quick", bob.Token, nil, &bobResult)
	if bobResult.Total != 2 {
		t.Fatalf("permission-filtered search total = %d, want 2", bobResult.Total)
	}
	if strings.Contains(strings.ToLower(strings.TrimSpace("")), "x") {
		t.Fatal("unreachable")
	}
}
