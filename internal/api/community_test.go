package api_test

import (
	"net/http"
	"strings"
	"testing"
)

func TestCommandPermissions(t *testing.T) {
	h := newHarness(t)
	alice := h.register("permowner")
	bob := h.register("permother")
	var guild struct{ ID string }
	h.decode("POST", "/guilds", alice.Token, map[string]any{"name": "Permissions Guild"}, &guild)
	var channel struct{ ID string }
	h.decode("POST", "/guilds/"+guild.ID+"/channels", alice.Token, map[string]any{"name": "chat"}, &channel)
	var role struct{ ID string }
	h.decode("POST", "/guilds/"+guild.ID+"/roles", alice.Token, map[string]any{"name": "Mods", "permissions": "0"}, &role)

	var app struct {
		ID    string `json:"id"`
		Token string `json:"token"`
	}
	h.decode("POST", "/applications", alice.Token, map[string]any{"name": "Perm App"}, &app)
	var commands []struct{ ID string }
	h.decode("PUT", "/applications/"+app.ID+"/commands", alice.Token, []map[string]any{
		{"name": "moderate", "description": "Moderation command", "type": 1},
	}, &commands)

	var bulk []any
	if status := h.decode("GET", "/applications/"+app.ID+"/guilds/"+guild.ID+"/commands/permissions", alice.Token, nil, &bulk); status != 200 || len(bulk) != 0 {
		t.Fatalf("initial bulk permissions %d %+v", status, bulk)
	}
	var empty struct {
		Permissions []any `json:"permissions"`
	}
	if status := h.decode("GET", "/applications/"+app.ID+"/guilds/"+guild.ID+"/commands/"+commands[0].ID+"/permissions", alice.Token, nil, &empty); status != 200 || len(empty.Permissions) != 0 {
		t.Fatalf("initial single permissions %d %+v", status, empty)
	}

	payload := map[string]any{"permissions": []map[string]any{
		{"id": role.ID, "type": 1, "permission": true},
		{"id": alice.ID, "type": 2, "permission": true},
		{"id": channel.ID, "type": 3, "permission": false},
	}}
	var stored struct {
		ID            string `json:"id"`
		ApplicationID string `json:"application_id"`
		GuildID       string `json:"guild_id"`
		Permissions   []struct {
			ID         string `json:"id"`
			Type       int    `json:"type"`
			Permission bool   `json:"permission"`
		} `json:"permissions"`
	}
	if status := h.decode("PUT", "/applications/"+app.ID+"/guilds/"+guild.ID+"/commands/"+commands[0].ID+"/permissions",
		alice.Token, payload, &stored); status != 200 || len(stored.Permissions) != 3 {
		t.Fatalf("put permissions %d %+v", status, stored)
	}
	if stored.ID != commands[0].ID || stored.GuildID != guild.ID {
		t.Fatalf("unexpected permissions object %+v", stored)
	}
	foundRole := false
	for _, p := range stored.Permissions {
		if p.ID == role.ID && p.Type == 1 && p.Permission {
			foundRole = true
		}
	}
	if !foundRole {
		t.Fatalf("role override missing from %+v", stored.Permissions)
	}
	var bulk2 []struct {
		ID          string `json:"id"`
		Permissions []any  `json:"permissions"`
	}
	if status := h.decode("GET", "/applications/"+app.ID+"/guilds/"+guild.ID+"/commands/permissions", alice.Token, nil, &bulk2); status != 200 || len(bulk2) != 1 || len(bulk2[0].Permissions) != 3 {
		t.Fatalf("bulk permissions %d %+v", status, bulk2)
	}

	// Bot auth can read the same object.
	if status := h.decode("GET", "/applications/"+app.ID+"/guilds/"+guild.ID+"/commands/"+commands[0].ID+"/permissions",
		"Bot "+app.Token, nil, nil); status != 200 {
		t.Fatalf("bot permission read %d", status)
	}

	// Validation.
	if status, _ := h.request("PUT", "/applications/"+app.ID+"/guilds/"+guild.ID+"/commands/"+commands[0].ID+"/permissions",
		alice.Token, map[string]any{"permissions": []map[string]any{{"id": "x", "type": 9, "permission": true}}}); status != 400 {
		t.Fatalf("invalid type status %d", status)
	}
	if status, _ := h.request("PUT", "/applications/"+app.ID+"/guilds/"+guild.ID+"/commands/"+commands[0].ID+"/permissions",
		alice.Token, map[string]any{"permissions": []map[string]any{{"id": role.ID, "type": 1, "permission": true}, {"id": role.ID, "type": 1, "permission": true}}}); status != 400 {
		t.Fatalf("duplicate id status %d", status)
	}
	if status, _ := h.request("PUT", "/applications/"+app.ID+"/guilds/"+guild.ID+"/commands/"+commands[0].ID+"/permissions",
		bob.Token, payload); status != 403 {
		t.Fatalf("foreign permissions write %d", status)
	}
}

func TestOnboardingAndWelcomeScreen(t *testing.T) {
	h := newHarness(t)
	alice := h.register("onboardowner")
	var guild struct{ ID string }
	h.decode("POST", "/guilds", alice.Token, map[string]any{"name": "Onboarding Guild"}, &guild)
	var one, two struct{ ID string }
	h.decode("POST", "/guilds/"+guild.ID+"/channels", alice.Token, map[string]any{"name": "one"}, &one)
	h.decode("POST", "/guilds/"+guild.ID+"/channels", alice.Token, map[string]any{"name": "two"}, &two)

	var defaults struct {
		Enabled           bool     `json:"enabled"`
		Mode              int      `json:"mode"`
		Prompts           []any    `json:"prompts"`
		DefaultChannelIDs []string `json:"default_channel_ids"`
	}
	if status := h.decode("GET", "/guilds/"+guild.ID+"/onboarding", alice.Token, nil, &defaults); status != 200 || defaults.Enabled || defaults.Mode != 0 || len(defaults.Prompts) != 0 {
		t.Fatalf("default onboarding %d %+v", status, defaults)
	}

	prompt := map[string]any{
		"title": "What do you want to do?",
		"type":  0,
		"options": []map[string]any{
			{"title": "Chat", "description": "Talk with people", "channel_id": one.ID},
			{"title": "Watch", "description": "Join events", "channel_id": two.ID},
		},
		"single_select": true,
		"required":      true,
	}
	var saved struct {
		Enabled           bool     `json:"enabled"`
		DefaultChannelIDs []string `json:"default_channel_ids"`
		Prompts           []any    `json:"prompts"`
	}
	if status := h.decode("PUT", "/guilds/"+guild.ID+"/onboarding", alice.Token, map[string]any{
		"prompts": []map[string]any{prompt}, "default_channel_ids": []string{one.ID},
		"enabled": true, "mode": 1,
	}, &saved); status != 200 || !saved.Enabled || len(saved.Prompts) != 1 || len(saved.DefaultChannelIDs) != 1 {
		t.Fatalf("put onboarding %d %+v", status, saved)
	}

	badPrompts := map[string]any{
		"too many prompts": map[string]any{"mode": 0, "prompts": []map[string]any{prompt, prompt, prompt, prompt, prompt}},
		"bad channel": map[string]any{"mode": 0, "prompts": []map[string]any{map[string]any{
			"title": "Pick", "options": []map[string]any{{"title": "X", "channel_id": "999"}},
		}}},
		"bad mode": map[string]any{"mode": 2, "prompts": []any{}},
	}
	for name, body := range badPrompts {
		if status, _ := h.request("PUT", "/guilds/"+guild.ID+"/onboarding", alice.Token, body); status != 400 {
			t.Fatalf("%s status %d", name, status)
		}
	}

	var welcome struct {
		Description     *string          `json:"description"`
		Enabled         bool             `json:"enabled"`
		WelcomeChannels []map[string]any `json:"welcome_channels"`
	}
	if status := h.decode("GET", "/guilds/"+guild.ID+"/welcome-screen", alice.Token, nil, &welcome); status != 200 || welcome.Enabled {
		t.Fatalf("default welcome %d %+v", status, welcome)
	}
	if status := h.decode("PATCH", "/guilds/"+guild.ID+"/welcome-screen", alice.Token, map[string]any{
		"enabled": true, "description": "Welcome to the server",
		"welcome_channels": []map[string]any{
			{"channel_id": one.ID, "description": "Start here"},
		},
	}, &welcome); status != 200 || !welcome.Enabled || welcome.Description == nil || len(welcome.WelcomeChannels) != 1 {
		t.Fatalf("patch welcome %d %+v", status, welcome)
	}
	// Enabling without a description is rejected when none exists yet.
	var otherGuild struct{ ID string }
	h.decode("POST", "/guilds", alice.Token, map[string]any{"name": "No Welcome"}, &otherGuild)
	if status, _ := h.request("PATCH", "/guilds/"+otherGuild.ID+"/welcome-screen", alice.Token,
		map[string]any{"enabled": true}); status != 400 {
		t.Fatalf("welcome without description %d", status)
	}
	// Unknown channel rejected.
	if status, _ := h.request("PATCH", "/guilds/"+guild.ID+"/welcome-screen", alice.Token, map[string]any{
		"welcome_channels": []map[string]any{{"channel_id": "999", "description": "x"}},
	}); status != 400 {
		t.Fatalf("unknown welcome channel %d", status)
	}
}

func TestWebhookVariants(t *testing.T) {
	h := newHarness(t)
	alice := h.register("hookvariant")
	var guild struct{ ID string }
	h.decode("POST", "/guilds", alice.Token, map[string]any{"name": "Hooks"}, &guild)
	var channel struct{ ID string }
	h.decode("POST", "/guilds/"+guild.ID+"/channels", alice.Token, map[string]any{"name": "feed"}, &channel)

	var hook struct {
		ID    string `json:"id"`
		Token string `json:"token"`
		Name  string `json:"name"`
	}
	h.decode("POST", "/channels/"+channel.ID+"/webhooks", alice.Token, map[string]any{"name": "Feed Hook"}, &hook)

	var fetched struct{ ID string }
	if status := h.decode("GET", "/webhooks/"+hook.ID+"/"+hook.Token, "", nil, &fetched); status != 200 || fetched.ID != hook.ID {
		t.Fatalf("get webhook with token %d %+v", status, fetched)
	}

	var slackMsg struct {
		Content string              `json:"content"`
		Author  struct{ ID string } `json:"author"`
	}
	if status := h.decode("POST", "/webhooks/"+hook.ID+"/"+hook.Token+"/slack?wait=true", "", map[string]any{
		"text": "hello from slack", "username": "slackbot",
	}, &slackMsg); status != 200 || slackMsg.Content != "hello from slack" || slackMsg.Author.ID != hook.ID {
		t.Fatalf("slack webhook %d %+v", status, slackMsg)
	}

	req, err := http.NewRequest("POST", h.server.URL+"/api/v10/webhooks/"+hook.ID+"/"+hook.Token+"/github?wait=true",
		strings.NewReader(`{"action":"opened","repository":{"full_name":"octo/repo"},"sender":{"login":"octocat"}}`))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-GitHub-Event", "pull_request")
	resp, err := h.server.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("github webhook status %d", resp.StatusCode)
	}
	body := make([]byte, 8192)
	n, _ := resp.Body.Read(body)
	text := string(body[:n])
	if !strings.Contains(text, "pull_request") || !strings.Contains(text, "octo/repo") || !strings.Contains(text, "octocat") {
		t.Fatalf("github webhook body %s", text)
	}

	var renamed struct{ Name string }
	if status := h.decode("PATCH", "/webhooks/"+hook.ID+"/"+hook.Token, "", map[string]any{"name": "Renamed"}, &renamed); status != 200 || renamed.Name != "Renamed" {
		t.Fatalf("patch webhook token %d %+v", status, renamed)
	}
	if status := h.decode("DELETE", "/webhooks/"+hook.ID+"/"+hook.Token, "", nil, nil); status != 204 {
		t.Fatalf("delete webhook token %d", status)
	}
	if status := h.decode("GET", "/webhooks/"+hook.ID+"/"+hook.Token, "", nil, nil); status != 404 {
		t.Fatalf("deleted webhook fetch %d", status)
	}
}
