package api_test

import (
	"testing"

	"github.com/wavedevgit/waveycord/internal/store"
)

func TestGuildMemberAdministrationRoutes(t *testing.T) {
	h := newHarness(t)
	alice := h.register("adminowner")
	bob := h.register("adminmember")
	carol := h.register("adminoutsider")
	var guild struct{ ID string }
	h.decode("POST", "/guilds", alice.Token, map[string]any{"name": "Admin"}, &guild)
	var voice struct{ ID string }
	h.decode("POST", "/guilds/"+guild.ID+"/channels", alice.Token, map[string]any{"name": "voice", "type": 2}, &voice)
	var text struct{ ID string }
	h.decode("POST", "/guilds/"+guild.ID+"/channels", alice.Token, map[string]any{"name": "text"}, &text)
	var invite struct{ Code string }
	h.decode("POST", "/channels/"+text.ID+"/invites", alice.Token, map[string]any{}, &invite)
	if status, body := h.request("POST", "/invites/"+invite.Code, bob.Token, nil); status != 200 {
		t.Fatalf("join guild %d %s", status, body)
	}

	// Member search filters by username.
	var found []struct {
		User struct{ Username string }
	}
	if status := h.decode("GET", "/guilds/"+guild.ID+"/members/search?query=adminmember", alice.Token, nil, &found); status != 200 || len(found) != 1 {
		t.Fatalf("member search %d %+v", status, found)
	}
	if status := h.decode("GET", "/guilds/"+guild.ID+"/members/search?query=adminoutsider", alice.Token, nil, &found); status != 200 || len(found) != 0 {
		t.Fatalf("member search outsider %d %+v", status, found)
	}

	// Role member counts include the @everyone role for both members.
	var counts []struct {
		ID    string `json:"id"`
		Count int    `json:"count"`
	}
	if status := h.decode("GET", "/guilds/"+guild.ID+"/roles/member-counts", alice.Token, nil, &counts); status != 200 || len(counts) != 1 || counts[0].ID != guild.ID || counts[0].Count != 2 {
		t.Fatalf("role counts %d %+v", status, counts)
	}

	// Bulk role positions.
	var role struct{ ID string }
	h.decode("POST", "/guilds/"+guild.ID+"/roles", alice.Token, map[string]any{"name": "Moderator"}, &role)
	var roles []struct {
		ID       string `json:"id"`
		Position int    `json:"position"`
	}
	if status := h.decode("PATCH", "/guilds/"+guild.ID+"/roles", alice.Token, []map[string]any{
		{"id": role.ID, "position": 5},
	}, &roles); status != 200 {
		t.Fatalf("role positions %d", status)
	}
	for _, r := range roles {
		if r.ID == role.ID && r.Position != 5 {
			t.Fatalf("position not applied: %+v", r)
		}
	}

	// Permission resolution for a member and a role.
	var perms struct{ Permissions string }
	if status := h.decode("GET", "/guilds/"+guild.ID+"/permissions/member/"+bob.ID+"?channel_id="+text.ID, alice.Token, nil, &perms); status != 200 || perms.Permissions == "" {
		t.Fatalf("member permissions %d %+v", status, perms)
	}
	if status := h.decode("GET", "/guilds/"+guild.ID+"/permissions/role/"+role.ID+"?channel_id="+text.ID, alice.Token, nil, &perms); status != 200 || perms.Permissions == "" {
		t.Fatalf("role permissions %d %+v", status, perms)
	}
	if status, _ := h.request("GET", "/guilds/"+guild.ID+"/permissions/member/"+bob.ID+"?channel_id="+text.ID, bob.Token, nil); status != 403 {
		t.Fatalf("member permissions without MANAGE_ROLES %d", status)
	}

	// Self nickname patch.
	var self struct {
		Nick *string `json:"nick"`
	}
	if status := h.decode("PATCH", "/guilds/"+guild.ID+"/members/@me/nick", bob.Token, map[string]any{"nick": "Bobby"}, &self); status != 200 || self.Nick == nil || *self.Nick != "Bobby" {
		t.Fatalf("self nick %d %+v", status, self)
	}
	if status := h.decode("PATCH", "/guilds/"+guild.ID+"/members/@me", bob.Token, map[string]any{"nick": "Robert"}, &self); status != 200 || self.Nick == nil || *self.Nick != "Robert" {
		t.Fatalf("self patch %d %+v", status, self)
	}

	// Voice state read requires an existing session; patch moves the caller.
	if status, _ := h.request("GET", "/guilds/"+guild.ID+"/voice-states/@me", bob.Token, nil); status != 404 {
		t.Fatalf("voice state before join %d", status)
	}
	if _, err := h.store.PutVoiceState(store.VoiceState{GuildID: guild.ID, ChannelID: &voice.ID, UserID: bob.ID, SessionID: "sess-bob"}, "tok-bob"); err != nil {
		t.Fatalf("seed voice state: %v", err)
	}
	var vs struct {
		ChannelID *string `json:"channel_id"`
		UserID    string  `json:"user_id"`
	}
	if status := h.decode("GET", "/guilds/"+guild.ID+"/voice-states/@me", bob.Token, nil, &vs); status != 200 || vs.UserID != bob.ID {
		t.Fatalf("own voice state %d %+v", status, vs)
	}
	if status := h.decode("PATCH", "/guilds/"+guild.ID+"/voice-states/@me", bob.Token, map[string]any{"channel_id": voice.ID}, &vs); status != 200 {
		t.Fatalf("patch own voice state %d", status)
	}
	if status, _ := h.request("PATCH", "/guilds/"+guild.ID+"/voice-states/"+bob.ID, bob.Token, map[string]any{"channel_id": voice.ID}); status != 403 {
		t.Fatalf("move other without permission %d", status)
	}

	// Prune endpoints.
	var pruneCount struct {
		PruneCount int `json:"prune_count"`
	}
	if status := h.decode("GET", "/guilds/"+guild.ID+"/prune?days=7", alice.Token, nil, &pruneCount); status != 200 {
		t.Fatalf("prune count %d", status)
	}
	var pruned struct {
		Pruned int `json:"pruned"`
	}
	if status := h.decode("POST", "/guilds/"+guild.ID+"/prune", alice.Token, map[string]any{"days": 7}, &pruned); status != 200 {
		t.Fatalf("prune %d", status)
	}
	if status, _ := h.request("POST", "/guilds/"+guild.ID+"/prune", bob.Token, map[string]any{"days": 7}); status != 403 {
		t.Fatalf("prune without KICK_MEMBERS %d", status)
	}

	// Bulk ban reports failures for unknown users and bans real ones.
	var bulk struct {
		Banned []string `json:"banned_users"`
		Failed []string `json:"failed_users"`
	}
	if status := h.decode("POST", "/guilds/"+guild.ID+"/bulk-ban", alice.Token, map[string]any{
		"user_ids": []string{bob.ID, "999999999999999999"},
	}, &bulk); status != 200 || len(bulk.Banned) != 1 || len(bulk.Failed) != 1 {
		t.Fatalf("bulk ban %d %+v", status, bulk)
	}
	if status, _ := h.request("POST", "/guilds/"+guild.ID+"/bulk-ban", carol.Token, map[string]any{
		"user_ids": []string{bob.ID},
	}); status != 403 {
		t.Fatalf("foreign bulk ban %d", status)
	}

	// Incident actions are reflected on the guild object.
	if status := h.decode("PUT", "/guilds/"+guild.ID+"/incident-actions", alice.Token, map[string]any{
		"dms_disabled": true, "invites_disabled_until": "2030-01-01T00:00:00Z",
	}, nil); status != 204 {
		t.Fatalf("incident actions %d", status)
	}
	var guildObj struct {
		DMSDisabled          bool    `json:"dms_disabled"`
		InvitesDisabledUntil *string `json:"invites_disabled_until"`
	}
	if status := h.decode("GET", "/guilds/"+guild.ID, alice.Token, nil, &guildObj); status != 200 || !guildObj.DMSDisabled || guildObj.InvitesDisabledUntil == nil {
		t.Fatalf("guild incident state %d %+v", status, guildObj)
	}
}

func TestMiscDocumentedRoutes(t *testing.T) {
	h := newHarness(t)
	alice := h.register("miscowner")
	var guild struct{ ID string }
	h.decode("POST", "/guilds", alice.Token, map[string]any{"name": "Misc"}, &guild)
	var text struct{ ID string }
	h.decode("POST", "/guilds/"+guild.ID+"/channels", alice.Token, map[string]any{"name": "text"}, &text)
	var msg struct{ ID string }
	h.decode("POST", "/channels/"+text.ID+"/messages", alice.Token, map[string]any{"content": "pin me"}, &msg)

	// Documented pin paths alias the existing handlers.
	if status := h.decode("PUT", "/channels/"+text.ID+"/messages/pins/"+msg.ID, alice.Token, nil, nil); status != 204 {
		t.Fatalf("pin %d", status)
	}
	var pins []struct{ ID string }
	if status := h.decode("GET", "/channels/"+text.ID+"/messages/pins", alice.Token, nil, &pins); status != 200 || len(pins) != 1 {
		t.Fatalf("pins %d %+v", status, pins)
	}
	if status := h.decode("DELETE", "/channels/"+text.ID+"/messages/pins/"+msg.ID, alice.Token, nil, nil); status != 204 {
		t.Fatalf("unpin %d", status)
	}

	// Voice status uses PUT in the public docs.
	if status, _ := h.request("PUT", "/channels/"+text.ID+"/voice-status", alice.Token, map[string]any{"status": "hi"}); status != 404 {
		t.Fatalf("voice status without voice state %d", status)
	}

	// Sticker pack catalogue is empty.
	if status, _ := h.request("GET", "/sticker-packs/1", alice.Token, nil); status != 404 {
		t.Fatalf("sticker pack %d", status)
	}

	// Role connection delete is idempotent.
	if status, _ := h.request("DELETE", "/users/@me/applications/1/role-connection", alice.Token, nil); status != 204 {
		t.Fatalf("role connection delete %d", status)
	}

	// /applications/@me patch targets the bot's own application.
	var app struct {
		ID    string `json:"id"`
		Token string `json:"token"`
	}
	h.decode("POST", "/applications", alice.Token, map[string]any{"name": "Misc Bot"}, &app)
	var patched struct{ Name string }
	if status := h.decode("PATCH", "/applications/@me", "Bot "+app.Token, map[string]any{"name": "Misc Bot Renamed"}, &patched); status != 200 || patched.Name != "Misc Bot Renamed" {
		t.Fatalf("patch my application %d %+v", status, patched)
	}

	// Soundboard playback requires an active voice state.
	if status, _ := h.request("POST", "/channels/"+text.ID+"/send-soundboard-sound", alice.Token, map[string]any{"sound_id": "1"}); status != 400 {
		t.Fatalf("soundboard without voice %d", status)
	}
}
