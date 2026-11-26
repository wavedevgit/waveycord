package api_test

import (
	"testing"
)

func TestGuildStickers(t *testing.T) {
	h := newHarness(t)
	alice := h.register("stickerowner")
	var guild struct{ ID string }
	h.decode("POST", "/guilds", alice.Token, map[string]any{"name": "Stickers"}, &guild)

	var sticker struct {
		ID          string  `json:"id"`
		Name        string  `json:"name"`
		Description *string `json:"description"`
		Tags        string  `json:"tags"`
		Type        int     `json:"type"`
		FormatType  int     `json:"format_type"`
		Available   bool    `json:"available"`
	}
	if status := h.decode("POST", "/guilds/"+guild.ID+"/stickers", alice.Token, map[string]any{
		"name": "wave", "description": "a wave", "tags": "wave hello",
	}, &sticker); status != 201 || sticker.Name != "wave" || sticker.Type != 2 || !sticker.Available {
		t.Fatalf("create sticker %d %+v", status, sticker)
	}
	var list []struct{ ID string }
	if status := h.decode("GET", "/guilds/"+guild.ID+"/stickers", alice.Token, nil, &list); status != 200 || len(list) != 1 {
		t.Fatalf("list stickers %d %+v", status, list)
	}
	if status := h.decode("GET", "/stickers/"+sticker.ID, alice.Token, nil, nil); status != 200 {
		t.Fatalf("get sticker %d", status)
	}
	var patched struct{ Name string }
	if status := h.decode("PATCH", "/guilds/"+guild.ID+"/stickers/"+sticker.ID, alice.Token,
		map[string]any{"name": "wave2", "tags": "wave2 hello"}, &patched); status != 200 || patched.Name != "wave2" {
		t.Fatalf("patch sticker %d %+v", status, patched)
	}

	// Validation.
	if status, _ := h.request("POST", "/guilds/"+guild.ID+"/stickers", alice.Token, map[string]any{
		"name": "x", "tags": "t",
	}); status != 400 {
		t.Fatalf("short name status %d", status)
	}
	if status, _ := h.request("POST", "/guilds/"+guild.ID+"/stickers", alice.Token, map[string]any{
		"name": "valid", "tags": "",
	}); status != 400 {
		t.Fatalf("missing tags status %d", status)
	}

	if status := h.decode("DELETE", "/guilds/"+guild.ID+"/stickers/"+sticker.ID, alice.Token, nil, nil); status != 204 {
		t.Fatalf("delete sticker %d", status)
	}
	if status := h.decode("GET", "/stickers/"+sticker.ID, alice.Token, nil, nil); status != 404 {
		t.Fatalf("deleted sticker %d", status)
	}
}

func TestGuildWidget(t *testing.T) {
	h := newHarness(t)
	alice := h.register("widgetowner")
	var guild struct{ ID string }
	h.decode("POST", "/guilds", alice.Token, map[string]any{"name": "Widget Guild"}, &guild)
	var channel struct{ ID string }
	h.decode("POST", "/guilds/"+guild.ID+"/channels", alice.Token, map[string]any{"name": "lobby"}, &channel)

	var settings struct {
		Enabled   bool    `json:"enabled"`
		ChannelID *string `json:"channel_id"`
	}
	if status := h.decode("GET", "/guilds/"+guild.ID+"/widget", alice.Token, nil, &settings); status != 200 || settings.Enabled {
		t.Fatalf("default widget %d %+v", status, settings)
	}
	if status := h.decode("PATCH", "/guilds/"+guild.ID+"/widget", alice.Token, map[string]any{
		"enabled": true, "channel_id": channel.ID,
	}, &settings); status != 200 || !settings.Enabled || settings.ChannelID == nil || *settings.ChannelID != channel.ID {
		t.Fatalf("patch widget %d %+v", status, settings)
	}
	var public struct {
		ID       string                `json:"id"`
		Name     string                `json:"name"`
		Channels []struct{ ID string } `json:"channels"`
	}
	if status := h.decode("GET", "/guilds/"+guild.ID+"/widget.json", "", nil, &public); status != 200 || public.ID != guild.ID || len(public.Channels) != 1 {
		t.Fatalf("widget json %d %+v", status, public)
	}
	if status, _ := h.request("PATCH", "/guilds/"+guild.ID+"/widget", alice.Token, map[string]any{
		"channel_id": "999",
	}); status != 400 {
		t.Fatalf("unknown widget channel %d", status)
	}
	if status := h.decode("PATCH", "/guilds/"+guild.ID+"/widget", alice.Token, map[string]any{"enabled": false}, nil); status != 200 {
		t.Fatalf("disable widget %d", status)
	}
	if status := h.decode("GET", "/guilds/"+guild.ID+"/widget.json", "", nil, nil); status != 403 {
		t.Fatalf("disabled widget json %d", status)
	}
}

func TestBulkCommandPermissions(t *testing.T) {
	h := newHarness(t)
	alice := h.register("bulkperm")
	var guild struct{ ID string }
	h.decode("POST", "/guilds", alice.Token, map[string]any{"name": "Bulk Perms"}, &guild)
	var role struct{ ID string }
	h.decode("POST", "/guilds/"+guild.ID+"/roles", alice.Token, map[string]any{"name": "Mods", "permissions": "0"}, &role)

	var app struct{ ID string }
	h.decode("POST", "/applications", alice.Token, map[string]any{"name": "Bulk App"}, &app)
	var commands []struct{ ID, Name string }
	h.decode("PUT", "/applications/"+app.ID+"/commands", alice.Token, []map[string]any{
		{"name": "one", "description": "One", "type": 1},
		{"name": "two", "description": "Two", "type": 1},
	}, &commands)

	var bulk []struct {
		ID          string                `json:"id"`
		Permissions []struct{ ID string } `json:"permissions"`
	}
	if status := h.decode("PUT", "/applications/"+app.ID+"/guilds/"+guild.ID+"/commands/permissions", alice.Token,
		[]map[string]any{
			{"id": commands[0].ID, "permissions": []map[string]any{{"id": role.ID, "type": 1, "permission": true}}},
			{"id": commands[1].ID, "permissions": []any{}},
		}, &bulk); status != 200 || len(bulk) != 2 {
		t.Fatalf("bulk put %d %+v", status, bulk)
	}
	var listed []struct {
		ID          string `json:"id"`
		Permissions []any  `json:"permissions"`
	}
	if status := h.decode("GET", "/applications/"+app.ID+"/guilds/"+guild.ID+"/commands/permissions", alice.Token, nil, &listed); status != 200 || len(listed) != 1 || listed[0].ID != commands[0].ID {
		t.Fatalf("bulk list %d %+v", status, listed)
	}

	// Replacing the set removes the previous command's rows.
	if status := h.decode("PUT", "/applications/"+app.ID+"/guilds/"+guild.ID+"/commands/permissions", alice.Token,
		[]map[string]any{
			{"id": commands[1].ID, "permissions": []map[string]any{{"id": role.ID, "type": 1, "permission": false}}},
		}, nil); status != 200 {
		t.Fatalf("bulk replace %d", status)
	}
	var first struct {
		Permissions []any `json:"permissions"`
	}
	h.decode("GET", "/applications/"+app.ID+"/guilds/"+guild.ID+"/commands/"+commands[0].ID+"/permissions", alice.Token, nil, &first)
	if len(first.Permissions) != 0 {
		t.Fatalf("old permissions survived: %+v", first)
	}

	// Validation.
	if status, _ := h.request("PUT", "/applications/"+app.ID+"/guilds/"+guild.ID+"/commands/permissions", alice.Token,
		[]map[string]any{{"id": "missing", "permissions": []any{}}}); status != 404 {
		t.Fatalf("unknown command %d", status)
	}
	if status, _ := h.request("PUT", "/applications/"+app.ID+"/guilds/"+guild.ID+"/commands/permissions", alice.Token,
		[]map[string]any{{"id": commands[0].ID, "permissions": []map[string]any{{"id": role.ID, "type": 9}}}}); status != 400 {
		t.Fatalf("invalid permission type %d", status)
	}
}
