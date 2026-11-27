package api_test

import (
	"encoding/base64"
	"strings"
	"testing"
	"time"

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

func TestWebhookMessageRoutes(t *testing.T) {
	h := newHarness(t)
	alice := h.register("hookmsgowner")
	var guild struct{ ID string }
	h.decode("POST", "/guilds", alice.Token, map[string]any{"name": "Hook Msg"}, &guild)
	var channel struct{ ID string }
	h.decode("POST", "/guilds/"+guild.ID+"/channels", alice.Token, map[string]any{"name": "feed"}, &channel)
	var hook struct {
		ID    string `json:"id"`
		Token string `json:"token"`
	}
	h.decode("POST", "/channels/"+channel.ID+"/webhooks", alice.Token, map[string]any{"name": "Msg Hook"}, &hook)

	var msg struct {
		ID        string  `json:"id"`
		Content   string  `json:"content"`
		WebhookID *string `json:"webhook_id"`
	}
	if status := h.decode("POST", "/webhooks/"+hook.ID+"/"+hook.Token+"?wait=true", "", map[string]any{
		"content": "posted by hook",
	}, &msg); status != 200 || msg.ID == "" || msg.WebhookID == nil || *msg.WebhookID != hook.ID {
		t.Fatalf("execute webhook %d %+v", status, msg)
	}

	var fetched struct {
		ID      string `json:"id"`
		Content string `json:"content"`
	}
	if status := h.decode("GET", "/webhooks/"+hook.ID+"/"+hook.Token+"/messages/"+msg.ID, "", nil, &fetched); status != 200 || fetched.ID != msg.ID {
		t.Fatalf("get webhook message %d %+v", status, fetched)
	}

	var updated struct{ Content string }
	if status := h.decode("PATCH", "/webhooks/"+hook.ID+"/"+hook.Token+"/messages/"+msg.ID, "", map[string]any{
		"content": "edited by hook",
	}, &updated); status != 200 || updated.Content != "edited by hook" {
		t.Fatalf("patch webhook message %d %+v", status, updated)
	}

	// A different webhook's token cannot reach the message.
	var other struct {
		ID    string `json:"id"`
		Token string `json:"token"`
	}
	h.decode("POST", "/channels/"+channel.ID+"/webhooks", alice.Token, map[string]any{"name": "Other Hook"}, &other)
	if status, _ := h.request("GET", "/webhooks/"+other.ID+"/"+other.Token+"/messages/"+msg.ID, "", nil); status != 404 {
		t.Fatalf("foreign webhook message read %d", status)
	}

	if status := h.decode("DELETE", "/webhooks/"+hook.ID+"/"+hook.Token+"/messages/"+msg.ID, "", nil, nil); status != 204 {
		t.Fatalf("delete webhook message %d", status)
	}
	if status, _ := h.request("GET", "/webhooks/"+hook.ID+"/"+hook.Token+"/messages/"+msg.ID, "", nil); status != 404 {
		t.Fatalf("deleted webhook message %d", status)
	}
}

func TestApplicationEmojis(t *testing.T) {
	h := newHarness(t)
	alice := h.register("appemojiowner")
	other := h.register("appemojistranger")

	var app struct {
		ID    string `json:"id"`
		Token string `json:"token"`
	}
	h.decode("POST", "/applications", alice.Token, map[string]any{"name": "Emoji App"}, &app)
	botAuth := "Bot " + app.Token

	// 1x1 transparent PNG.
	const png = "data:image/png;base64,iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mNkYPhfDwAChwGA60e6kgAAAABJRU5ErkJggg=="

	var emoji struct {
		ID    *string              `json:"id"`
		Name  *string              `json:"name"`
		User  *struct{ ID string } `json:"user"`
		Roles []string             `json:"roles"`
	}
	if status := h.decode("POST", "/applications/"+app.ID+"/emojis", botAuth, map[string]any{
		"name": "party_wave", "image": png,
	}, &emoji); status != 200 || emoji.ID == nil || emoji.Name == nil || *emoji.Name != "party_wave" || emoji.User == nil {
		t.Fatalf("create app emoji %d %+v", status, emoji)
	}

	var list struct {
		Items []struct {
			ID   string `json:"id"`
			Name string `json:"name"`
		} `json:"items"`
	}
	if status := h.decode("GET", "/applications/"+app.ID+"/emojis", botAuth, nil, &list); status != 200 || len(list.Items) != 1 || list.Items[0].ID != *emoji.ID {
		t.Fatalf("list app emojis %d %+v", status, list)
	}

	var fetched struct{ ID string }
	if status := h.decode("GET", "/applications/"+app.ID+"/emojis/"+*emoji.ID, botAuth, nil, &fetched); status != 200 || fetched.ID != *emoji.ID {
		t.Fatalf("get app emoji %d %+v", status, fetched)
	}

	var renamed struct{ Name string }
	if status := h.decode("PATCH", "/applications/"+app.ID+"/emojis/"+*emoji.ID, botAuth, map[string]any{
		"name": "renamed_wave",
	}, &renamed); status != 200 || renamed.Name != "renamed_wave" {
		t.Fatalf("patch app emoji %d %+v", status, renamed)
	}

	// A user token and a foreign bot cannot manage the application's emojis.
	if status, _ := h.request("GET", "/applications/"+app.ID+"/emojis", alice.Token, nil); status != 404 {
		t.Fatalf("user token app emoji list %d", status)
	}
	var foreign struct {
		ID    string `json:"id"`
		Token string `json:"token"`
	}
	h.decode("POST", "/applications", other.Token, map[string]any{"name": "Other App"}, &foreign)
	if status, _ := h.request("GET", "/applications/"+app.ID+"/emojis", "Bot "+foreign.Token, nil); status != 404 {
		t.Fatalf("foreign bot app emoji list %d", status)
	}

	// Invalid uploads are rejected before storage.
	if status, _ := h.request("POST", "/applications/"+app.ID+"/emojis", botAuth, map[string]any{
		"name": "bad name", "image": png,
	}); status != 400 {
		t.Fatalf("invalid emoji name %d", status)
	}
	if status, _ := h.request("POST", "/applications/"+app.ID+"/emojis", botAuth, map[string]any{
		"name": "no_image", "image": "not base64!",
	}); status != 400 {
		t.Fatalf("invalid emoji image %d", status)
	}

	if status := h.decode("DELETE", "/applications/"+app.ID+"/emojis/"+*emoji.ID, botAuth, nil, nil); status != 204 {
		t.Fatalf("delete app emoji %d", status)
	}
	if status, _ := h.request("GET", "/applications/"+app.ID+"/emojis/"+*emoji.ID, botAuth, nil); status != 404 {
		t.Fatalf("deleted app emoji %d", status)
	}
}

func TestGetGuildEmoji(t *testing.T) {
	h := newHarness(t)
	alice := h.register("guildemojiowner")
	var guild struct{ ID string }
	h.decode("POST", "/guilds", alice.Token, map[string]any{"name": "Emoji Guild"}, &guild)
	var emoji struct {
		ID *string `json:"id"`
	}
	h.decode("POST", "/guilds/"+guild.ID+"/emojis", alice.Token, map[string]any{
		"name": "guild_wave", "image": "data:image/png;base64,iVBORw0KGgo=",
	}, &emoji)
	if emoji.ID == nil {
		t.Fatalf("guild emoji not created")
	}
	var fetched struct {
		ID   string  `json:"id"`
		Name *string `json:"name"`
	}
	if status := h.decode("GET", "/guilds/"+guild.ID+"/emojis/"+*emoji.ID, alice.Token, nil, &fetched); status != 200 || fetched.ID != *emoji.ID || fetched.Name == nil || *fetched.Name != "guild_wave" {
		t.Fatalf("get guild emoji %d %+v", status, fetched)
	}
	if status, _ := h.request("GET", "/guilds/"+guild.ID+"/emojis/999999999999999999", alice.Token, nil); status != 404 {
		t.Fatalf("unknown guild emoji %d", status)
	}
}

func TestSoundboardRoutes(t *testing.T) {
	h := newHarness(t)
	alice := h.register("soundowner")
	bob := h.register("soundmember")
	var guild struct{ ID string }
	h.decode("POST", "/guilds", alice.Token, map[string]any{"name": "Sounds"}, &guild)
	var text struct{ ID string }
	h.decode("POST", "/guilds/"+guild.ID+"/channels", alice.Token, map[string]any{"name": "text"}, &text)
	var invite struct{ Code string }
	h.decode("POST", "/channels/"+text.ID+"/invites", alice.Token, map[string]any{}, &invite)
	h.request("POST", "/invites/"+invite.Code, bob.Token, nil)

	var defaults []struct {
		SoundID string `json:"sound_id"`
	}
	if status := h.decode("GET", "/soundboard-default-sounds", alice.Token, nil, &defaults); status != 200 || len(defaults) == 0 {
		t.Fatalf("default sounds %d %+v", status, defaults)
	}

	sound := "data:audio/ogg;base64," + base64.StdEncoding.EncodeToString([]byte("OggS-test-sound"))
	var created struct {
		SoundID   string  `json:"sound_id"`
		Name      string  `json:"name"`
		Volume    float64 `json:"volume"`
		Available bool    `json:"available"`
	}
	if status := h.decode("POST", "/guilds/"+guild.ID+"/soundboard-sounds", alice.Token, map[string]any{
		"name": "airhorn", "sound": sound, "volume": 0.5,
	}, &created); status != 200 || created.SoundID == "" || created.Name != "airhorn" || created.Volume != 0.5 || !created.Available {
		t.Fatalf("create sound %d %+v", status, created)
	}

	var list struct {
		Items []struct {
			SoundID string `json:"sound_id"`
		} `json:"items"`
	}
	if status := h.decode("GET", "/guilds/"+guild.ID+"/soundboard-sounds", alice.Token, nil, &list); status != 200 || len(list.Items) != 1 || list.Items[0].SoundID != created.SoundID {
		t.Fatalf("list sounds %d %+v", status, list)
	}
	var fetched struct {
		SoundID string `json:"sound_id"`
	}
	if status := h.decode("GET", "/guilds/"+guild.ID+"/soundboard-sounds/"+created.SoundID, alice.Token, nil, &fetched); status != 200 || fetched.SoundID != created.SoundID {
		t.Fatalf("get sound %d %+v", status, fetched)
	}

	var renamed struct{ Name string }
	if status := h.decode("PATCH", "/guilds/"+guild.ID+"/soundboard-sounds/"+created.SoundID, alice.Token, map[string]any{
		"name": "airhorn2", "volume": 1.0,
	}, &renamed); status != 200 || renamed.Name != "airhorn2" {
		t.Fatalf("patch sound %d %+v", status, renamed)
	}

	if status, _ := h.request("POST", "/guilds/"+guild.ID+"/soundboard-sounds", alice.Token, map[string]any{
		"name": "x", "sound": sound,
	}); status != 400 {
		t.Fatalf("short sound name %d", status)
	}
	if status, _ := h.request("POST", "/guilds/"+guild.ID+"/soundboard-sounds", alice.Token, map[string]any{
		"name": "loud", "sound": sound, "volume": 2,
	}); status != 400 {
		t.Fatalf("invalid volume %d", status)
	}
	// A member without expression permissions cannot create or delete.
	if status, _ := h.request("POST", "/guilds/"+guild.ID+"/soundboard-sounds", bob.Token, map[string]any{
		"name": "member_sound", "sound": sound,
	}); status != 403 {
		t.Fatalf("member create sound %d", status)
	}
	if status, _ := h.request("DELETE", "/guilds/"+guild.ID+"/soundboard-sounds/"+created.SoundID, bob.Token, nil); status != 403 {
		t.Fatalf("member delete sound %d", status)
	}

	if status := h.decode("DELETE", "/guilds/"+guild.ID+"/soundboard-sounds/"+created.SoundID, alice.Token, nil, nil); status != 204 {
		t.Fatalf("delete sound %d", status)
	}
	if status, _ := h.request("GET", "/guilds/"+guild.ID+"/soundboard-sounds/"+created.SoundID, alice.Token, nil); status != 404 {
		t.Fatalf("deleted sound %d", status)
	}
}

func TestInviteTargetUsers(t *testing.T) {
	h := newHarness(t)
	alice := h.register("targetinviter")
	bob := h.register("targetbob")
	carol := h.register("targetcarol")
	var guild struct{ ID string }
	h.decode("POST", "/guilds", alice.Token, map[string]any{"name": "Targets"}, &guild)
	var channel struct{ ID string }
	h.decode("POST", "/guilds/"+guild.ID+"/channels", alice.Token, map[string]any{"name": "general"}, &channel)
	var invite struct{ Code string }
	h.decode("POST", "/channels/"+channel.ID+"/invites", alice.Token, map[string]any{}, &invite)

	if status := h.decode("PUT", "/invites/"+invite.Code+"/target-users", alice.Token, map[string]any{
		"user_ids": []string{bob.ID},
	}, nil); status != 204 {
		t.Fatalf("replace target users %d", status)
	}
	status, body := h.request("GET", "/invites/"+invite.Code+"/target-users", alice.Token, nil)
	if status != 200 || !strings.Contains(string(body), "user_id") || !strings.Contains(string(body), bob.ID) {
		t.Fatalf("get target users %d %q", status, body)
	}
	var job struct {
		Status    int `json:"status"`
		Total     int `json:"total_users"`
		Processed int `json:"processed_users"`
	}
	if status := h.decode("GET", "/invites/"+invite.Code+"/target-users/job-status", alice.Token, nil, &job); status != 200 || job.Status != 3 || job.Total != 1 {
		t.Fatalf("job status %d %+v", status, job)
	}

	// Single add and remove.
	if status := h.decode("PUT", "/invites/"+invite.Code+"/target-users/"+carol.ID, alice.Token, nil, nil); status != 204 {
		t.Fatalf("add target user %d", status)
	}
	if status := h.decode("DELETE", "/invites/"+invite.Code+"/target-users/"+carol.ID, alice.Token, nil, nil); status != 204 {
		t.Fatalf("remove target user %d", status)
	}
	// Bulk add then bulk delete.
	if status := h.decode("POST", "/invites/"+invite.Code+"/target-users/bulk-add", alice.Token, map[string]any{
		"user_ids": []string{bob.ID, carol.ID},
	}, nil); status != 204 {
		t.Fatalf("bulk add %d", status)
	}
	if status := h.decode("POST", "/invites/"+invite.Code+"/target-users/bulk-delete", alice.Token, map[string]any{
		"user_ids": []string{bob.ID},
	}, nil); status != 204 {
		t.Fatalf("bulk delete %d", status)
	}

	// Only listed users may accept.
	if status := h.decode("PUT", "/invites/"+invite.Code+"/target-users", alice.Token, map[string]any{
		"user_ids": []string{bob.ID},
	}, nil); status != 204 {
		t.Fatalf("restrict target users %d", status)
	}
	if status, _ := h.request("POST", "/invites/"+invite.Code, carol.Token, nil); status != 403 {
		t.Fatalf("unlisted accept %d", status)
	}
	if status, _ := h.request("POST", "/invites/"+invite.Code, bob.Token, nil); status != 200 {
		t.Fatalf("listed accept %d", status)
	}
}

func TestChannelFollowersAndConnections(t *testing.T) {
	h := newHarness(t)
	alice := h.register("followowner")
	var guild struct{ ID string }
	h.decode("POST", "/guilds", alice.Token, map[string]any{"name": "Follows"}, &guild)
	var news struct{ ID string }
	h.decode("POST", "/guilds/"+guild.ID+"/channels", alice.Token, map[string]any{"name": "news", "type": 5}, &news)
	var target struct{ ID string }
	h.decode("POST", "/guilds/"+guild.ID+"/channels", alice.Token, map[string]any{"name": "feed"}, &target)

	var follow struct {
		ChannelID string `json:"channel_id"`
		WebhookID string `json:"webhook_id"`
	}
	if status := h.decode("POST", "/channels/"+news.ID+"/followers", alice.Token, map[string]any{
		"webhook_channel_id": target.ID,
	}, &follow); status != 200 || follow.ChannelID != news.ID || follow.WebhookID == "" {
		t.Fatalf("follow channel %d %+v", status, follow)
	}
	var hooks []struct{ ID string }
	if status := h.decode("GET", "/channels/"+target.ID+"/webhooks", alice.Token, nil, &hooks); status != 200 || len(hooks) != 1 || hooks[0].ID != follow.WebhookID {
		t.Fatalf("follow webhook %d %+v", status, hooks)
	}
	if status, _ := h.request("POST", "/channels/"+target.ID+"/followers", alice.Token, map[string]any{
		"webhook_channel_id": target.ID,
	}); status != 400 {
		t.Fatalf("follow non-announcement %d", status)
	}

	var connections []map[string]any
	if status := h.decode("GET", "/users/@me/connections", alice.Token, nil, &connections); status != 200 || len(connections) != 0 {
		t.Fatalf("connections %d %+v", status, connections)
	}
}

func TestAddGuildMemberWithOAuthToken(t *testing.T) {
	h := newHarness(t)
	alice := h.register("joinbotowner")
	bob := h.register("jointarget")
	var guild struct{ ID string }
	h.decode("POST", "/guilds", alice.Token, map[string]any{"name": "Join"}, &guild)
	var app struct {
		ID    string               `json:"id"`
		Token string               `json:"token"`
		Bot   *struct{ ID string } `json:"bot"`
	}
	h.decode("POST", "/applications", alice.Token, map[string]any{"name": "Join Bot"}, &app)
	if _, err := h.store.AddMember(guild.ID, app.Bot.ID); err != nil {
		t.Fatalf("add bot member: %v", err)
	}

	seed := func(token string, scopes string) {
		now := time.Now().UnixMilli()
		if _, err := h.store.DB.Exec(`INSERT INTO oauth2_tokens
			(id, application_id, user_id, token_hash, refresh_hash, scopes, created_at, expires_at, revoked)
			VALUES (?,?,?,?,?,?,?,?,0)`,
			"tok-"+token, app.ID, bob.ID, store.TokenHashOf(token), "refresh-"+token, scopes, now, now+3600000); err != nil {
			t.Fatalf("seed oauth token: %v", err)
		}
	}
	seed("good-token", `["identify","guilds.join"]`)

	var member struct {
		User *struct{ ID string } `json:"user"`
		Nick *string              `json:"nick"`
	}
	if status := h.decode("PUT", "/guilds/"+guild.ID+"/members/"+bob.ID, "Bot "+app.Token, map[string]any{
		"access_token": "good-token", "nick": "Joined",
	}, &member); status != 201 || member.User == nil || member.User.ID != bob.ID || member.Nick == nil || *member.Nick != "Joined" {
		t.Fatalf("add guild member %d %+v", status, member)
	}

	seed("no-scope-token", `["identify"]`)
	if status, _ := h.request("PUT", "/guilds/"+guild.ID+"/members/"+bob.ID, "Bot "+app.Token, map[string]any{
		"access_token": "no-scope-token",
	}); status != 400 {
		t.Fatalf("missing guilds.join %d", status)
	}
	// A user token cannot call the route.
	if status, _ := h.request("PUT", "/guilds/"+guild.ID+"/members/"+bob.ID, alice.Token, map[string]any{
		"access_token": "good-token",
	}); status != 403 {
		t.Fatalf("user token member add %d", status)
	}
}

func TestMonetizationRoutes(t *testing.T) {
	h := newHarness(t)
	alice := h.register("monetizeowner")
	bob := h.register("monetizebuyer")
	var app struct {
		ID    string `json:"id"`
		Token string `json:"token"`
	}
	h.decode("POST", "/applications", alice.Token, map[string]any{"name": "Monetize"}, &app)
	botAuth := "Bot " + app.Token

	sku, err := h.store.CreateSKU(app.ID, 5, "Premium", "premium", 128)
	if err != nil {
		t.Fatalf("create sku: %v", err)
	}
	var skus []struct {
		ID    string `json:"id"`
		Type  int    `json:"type"`
		Flags int64  `json:"flags"`
	}
	if status := h.decode("GET", "/applications/"+app.ID+"/skus", botAuth, nil, &skus); status != 200 || len(skus) != 1 || skus[0].ID != sku.ID || skus[0].Type != 5 {
		t.Fatalf("list skus %d %+v", status, skus)
	}

	var ent struct {
		ID       string  `json:"id"`
		UserID   *string `json:"user_id"`
		GuildID  *string `json:"guild_id"`
		Consumed *bool   `json:"consumed"`
		Deleted  bool    `json:"deleted"`
	}
	if status := h.decode("POST", "/applications/"+app.ID+"/entitlements", botAuth, map[string]any{
		"sku_id": sku.ID, "owner_id": bob.ID, "owner_type": 2,
	}, &ent); status != 200 || ent.ID == "" || ent.UserID == nil || *ent.UserID != bob.ID {
		t.Fatalf("create entitlement %d %+v", status, ent)
	}
	var listed []struct{ ID string }
	if status := h.decode("GET", "/applications/"+app.ID+"/entitlements?user_id="+bob.ID, botAuth, nil, &listed); status != 200 || len(listed) != 1 || listed[0].ID != ent.ID {
		t.Fatalf("list entitlements %d %+v", status, listed)
	}
	if status := h.decode("POST", "/applications/"+app.ID+"/entitlements/"+ent.ID+"/consume", botAuth, nil, nil); status != 204 {
		t.Fatalf("consume entitlement %d", status)
	}
	var got struct {
		Consumed *bool `json:"consumed"`
	}
	if status := h.decode("GET", "/applications/"+app.ID+"/entitlements/"+ent.ID, botAuth, nil, &got); status != 200 || got.Consumed == nil || !*got.Consumed {
		t.Fatalf("consumed entitlement %d %+v", status, got)
	}
	if status := h.decode("DELETE", "/applications/"+app.ID+"/entitlements/"+ent.ID, botAuth, nil, nil); status != 204 {
		t.Fatalf("delete entitlement %d", status)
	}
	var filtered []struct{ ID string }
	h.decode("GET", "/applications/"+app.ID+"/entitlements", botAuth, nil, &filtered)
	if len(filtered) != 0 {
		t.Fatalf("deleted entitlement not excluded: %+v", filtered)
	}

	// Subscription listing is scoped to the SKU and user.
	now := time.Now().UnixMilli()
	if _, err := h.store.DB.Exec(`INSERT INTO subscriptions
		(id, user_id, sku_ids, entitlement_ids, renewal_sku_ids, current_period_start,
		 current_period_end, status, created_at)
		VALUES (?,?,?,?,?,?,?,?,?)`, "sub-1", bob.ID, `["`+sku.ID+`"]`, `[]`, nil, now, now+86400000, 0, now); err != nil {
		t.Fatalf("seed subscription: %v", err)
	}
	var subs []struct {
		ID     string   `json:"id"`
		SKUIDs []string `json:"sku_ids"`
	}
	if status := h.decode("GET", "/skus/"+sku.ID+"/subscriptions?user_id="+bob.ID, botAuth, nil, &subs); status != 200 || len(subs) != 1 || subs[0].ID != "sub-1" {
		t.Fatalf("list subscriptions %d %+v", status, subs)
	}
	var sub struct{ ID string }
	if status := h.decode("GET", "/skus/"+sku.ID+"/subscriptions/sub-1", botAuth, nil, &sub); status != 200 || sub.ID != "sub-1" {
		t.Fatalf("get subscription %d %+v", status, sub)
	}
	if status, _ := h.request("GET", "/skus/"+sku.ID+"/subscriptions?user_id="+bob.ID, alice.Token, nil); status != 404 {
		t.Fatalf("user token subscriptions %d", status)
	}
}

func TestApplicationIdentityProfiles(t *testing.T) {
	h := newHarness(t)
	alice := h.register("identityowner")
	bob := h.register("identityuser")
	var app struct {
		ID    string `json:"id"`
		Token string `json:"token"`
	}
	h.decode("POST", "/applications", alice.Token, map[string]any{"name": "Identity App"}, &app)
	botAuth := "Bot " + app.Token
	now := time.Now().UnixMilli()
	if _, err := h.store.DB.Exec(`INSERT INTO oauth2_tokens
		(id, application_id, user_id, token_hash, refresh_hash, scopes, created_at, expires_at, revoked)
		VALUES (?,?,?,?,?,?,?,?,0)`,
		"id-token", app.ID, bob.ID, store.TokenHashOf("identity-token"), "identity-refresh",
		`["application_identities.write"]`, now, now+3600000); err != nil {
		t.Fatalf("seed token: %v", err)
	}

	profile := map[string]any{
		"username": "bob_in_game",
		"data": map[string]any{
			"primary": map[string]any{"season": "Season 3", "rank_name": "Silver", "total_wins": 57},
			"dynamic": []map[string]any{{"type": 1, "name": "clan", "value": "wave"}},
		},
	}
	var created struct {
		Username *string `json:"username"`
		Data     *struct {
			Primary map[string]any `json:"primary"`
		} `json:"data"`
	}
	path := "/applications/" + app.ID + "/users/" + bob.ID + "/identities/ext-1/profile"
	if status := h.decode("PATCH", path, botAuth, profile, &created); status != 201 || created.Username == nil || *created.Username != "bob_in_game" || created.Data == nil {
		t.Fatalf("create profile %d %+v", status, created)
	}
	var fetched struct {
		Username *string `json:"username"`
		Data     *struct {
			Primary map[string]any   `json:"primary"`
			Dynamic []map[string]any `json:"dynamic"`
		} `json:"data"`
	}
	if status := h.decode("GET", path, botAuth, nil, &fetched); status != 200 || fetched.Username == nil || fetched.Data == nil || len(fetched.Data.Dynamic) != 1 {
		t.Fatalf("get profile %d %+v", status, fetched)
	}

	// Username-only update returns 204 and keeps the data payload.
	if status := h.decode("PATCH", path, botAuth, map[string]any{"username": "bob_v2"}, nil); status != 204 {
		t.Fatalf("update profile %d", status)
	}
	h.decode("GET", path, botAuth, nil, &fetched)
	if fetched.Username == nil || *fetched.Username != "bob_v2" || fetched.Data == nil {
		t.Fatalf("profile update lost data: %+v", fetched)
	}

	// A mismatched provider id is the documented conflict.
	if status, _ := h.request("PATCH", "/applications/"+app.ID+"/users/"+bob.ID+"/identities/ext-2/profile", botAuth, profile); status != 400 {
		t.Fatalf("provider mismatch %d", status)
	}

	var byUser struct {
		Identities []struct {
			UserID               string `json:"user_id"`
			ProviderType         string `json:"provider_type"`
			ProviderIssuedUserID string `json:"provider_issued_user_id"`
		} `json:"identities"`
	}
	if status := h.decode("GET", "/users/"+bob.ID+"/application-identities/"+app.ID, botAuth, nil, &byUser); status != 200 || len(byUser.Identities) != 1 || byUser.Identities[0].ProviderType != "NONE" {
		t.Fatalf("identities by user %d %+v", status, byUser)
	}
	var byExternal struct {
		Identities []struct {
			UserID string `json:"user_id"`
		} `json:"identities"`
	}
	if status := h.decode("GET", "/applications/"+app.ID+"/application-identities/NONE/ext-1", botAuth, nil, &byExternal); status != 200 || len(byExternal.Identities) != 1 || byExternal.Identities[0].UserID != bob.ID {
		t.Fatalf("identities by external %d %+v", status, byExternal)
	}

	// Missing OAuth authorization is rejected.
	carol := h.register("identityoutsider")
	if status, _ := h.request("GET", "/applications/"+app.ID+"/users/"+carol.ID+"/identities/ext-1/profile", botAuth, nil); status != 403 {
		t.Fatalf("unauthorized identity read %d", status)
	}

	if status := h.decode("POST", "/users/"+bob.ID+"/application-identities/"+app.ID+"/NONE/ext-1/delete", botAuth, nil, nil); status != 204 {
		t.Fatalf("delete identity %d", status)
	}
	if status, _ := h.request("GET", path, botAuth, nil); status != 404 {
		t.Fatalf("deleted profile %d", status)
	}
}
