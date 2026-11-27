package api_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
)

func TestReadyIncludesOnboardingAndWelcome(t *testing.T) {
	h := newHarness(t)
	alice := h.register("readyowner")
	var guild struct{ ID string }
	h.decode("POST", "/guilds", alice.Token, map[string]any{"name": "Ready Guild"}, &guild)
	var channel struct{ ID string }
	h.decode("POST", "/guilds/"+guild.ID+"/channels", alice.Token, map[string]any{"name": "start"}, &channel)

	if status := h.decode("PUT", "/guilds/"+guild.ID+"/onboarding", alice.Token, map[string]any{
		"enabled": true, "mode": 0, "default_channel_ids": []string{channel.ID},
		"prompts": []map[string]any{{
			"title": "Pick a path", "type": 0,
			"options": []map[string]any{{"title": "Start", "channel_id": channel.ID}},
		}},
	}, nil); status != 200 {
		t.Fatalf("onboarding status %d", status)
	}
	if status := h.decode("PATCH", "/guilds/"+guild.ID+"/welcome-screen", alice.Token, map[string]any{
		"enabled": true, "description": "Be nice",
		"welcome_channels": []map[string]any{{"channel_id": channel.ID, "description": "Start here"}},
	}, nil); status != 200 {
		t.Fatalf("welcome status %d", status)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	conn, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(h.server.URL, "http")+"/gateway", nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.CloseNow()
	deadline := time.Now().Add(10 * time.Second)
	readFrame(t, conn, deadline)
	writeFrame(t, conn, map[string]any{"op": 2, "d": map[string]any{"token": alice.Token, "intents": 0}})
	ready := readFrame(t, conn, deadline)
	if ready["t"] != "READY" {
		t.Fatalf("expected READY: %+v", ready)
	}
	guilds := ready["d"].(map[string]any)["guilds"].([]any)
	if len(guilds) != 1 {
		t.Fatalf("guilds = %d", len(guilds))
	}
	g := guilds[0].(map[string]any)
	onboarding, ok := g["onboarding"].(map[string]any)
	if !ok || onboarding["enabled"] != true || onboarding["mode"].(float64) != 0 {
		t.Fatalf("onboarding in READY = %+v", g["onboarding"])
	}
	if prompts, ok := onboarding["prompts"].([]any); !ok || len(prompts) != 1 {
		t.Fatalf("onboarding prompts = %+v", onboarding["prompts"])
	}
	welcome, ok := g["welcome_screen"].(map[string]any)
	if !ok || welcome["enabled"] != true || welcome["description"] != "Be nice" {
		t.Fatalf("welcome in READY = %+v", g["welcome_screen"])
	}
}

func TestWebhookThreadExecution(t *testing.T) {
	h := newHarness(t)
	alice := h.register("threadhook")
	var guild struct{ ID string }
	h.decode("POST", "/guilds", alice.Token, map[string]any{"name": "Hook Threads"}, &guild)
	var text, forum struct{ ID string }
	h.decode("POST", "/guilds/"+guild.ID+"/channels", alice.Token, map[string]any{"name": "chat"}, &text)
	h.decode("POST", "/guilds/"+guild.ID+"/channels", alice.Token, map[string]any{"name": "forum", "type": 15}, &forum)

	var hook struct {
		ID    string `json:"id"`
		Token string `json:"token"`
	}
	h.decode("POST", "/channels/"+text.ID+"/webhooks", alice.Token, map[string]any{"name": "Text Hook"}, &hook)

	var thread struct{ ID string }
	h.decode("POST", "/channels/"+text.ID+"/threads", alice.Token, map[string]any{"name": "side topic", "type": 11}, &thread)

	var msg struct {
		ID        string `json:"id"`
		ChannelID string `json:"channel_id"`
		Content   string `json:"content"`
	}
	if status := h.decode("POST", "/webhooks/"+hook.ID+"/"+hook.Token+"?thread_id="+thread.ID+"&wait=true", "",
		map[string]any{"content": "in thread"}, &msg); status != 200 || msg.ChannelID != thread.ID {
		t.Fatalf("thread_id execution %d %+v", status, msg)
	}

	// A non-thread channel id is rejected.
	if status, _ := h.request("POST", "/webhooks/"+hook.ID+"/"+hook.Token+"?thread_id="+text.ID, "",
		map[string]any{"content": "bad"}); status != 400 {
		t.Fatalf("non-thread thread_id status %d", status)
	}

	// Forum webhook with thread_name creates a thread and posts into it.
	var forumHook struct {
		ID    string `json:"id"`
		Token string `json:"token"`
	}
	if status := h.decode("POST", "/channels/"+forum.ID+"/webhooks", alice.Token, map[string]any{"name": "Forum Hook"}, &forumHook); status != 201 {
		t.Fatalf("forum webhook status %d", status)
	}
	var posted struct {
		ChannelID string `json:"channel_id"`
	}
	if status := h.decode("POST", "/webhooks/"+forumHook.ID+"/"+forumHook.Token+"?thread_name=Weekly+Update&wait=true", "",
		map[string]any{"content": "forum post"}, &posted); status != 200 {
		t.Fatalf("thread_name execution %d", status)
	}
	var active struct {
		Threads []struct {
			ID       string `json:"id"`
			ParentID string `json:"parent_id"`
		} `json:"threads"`
	}
	h.decode("GET", "/guilds/"+guild.ID+"/threads/active", alice.Token, nil, &active)
	found := false
	for _, th := range active.Threads {
		if th.ID == posted.ChannelID && th.ParentID == forum.ID {
			found = true
		}
	}
	if !found {
		t.Fatalf("created thread missing from active threads: %+v", active.Threads)
	}

	// thread_name is invalid on a plain text channel.
	if status, _ := h.request("POST", "/webhooks/"+hook.ID+"/"+hook.Token+"?thread_name=Nope", "",
		map[string]any{"content": "x"}); status != 400 {
		t.Fatalf("thread_name on text channel status %d", status)
	}
}

func TestTwitchYouTubeIntegrations(t *testing.T) {
	h := newHarness(t)
	alice := h.register("intowner")
	bob := h.register("intoutsider")
	var guild struct{ ID string }
	h.decode("POST", "/guilds", alice.Token, map[string]any{"name": "Integrations"}, &guild)
	var channel struct{ ID string }
	h.decode("POST", "/guilds/"+guild.ID+"/channels", alice.Token, map[string]any{"name": "streams"}, &channel)

	// Create returns 204 and the integration id is the provider account id.
	if status, _ := h.request("POST", "/guilds/"+guild.ID+"/integrations", alice.Token, map[string]any{
		"type": "twitch", "id": "streamer",
	}); status != 204 {
		t.Fatalf("create twitch integration %d", status)
	}
	if status, _ := h.request("POST", "/guilds/"+guild.ID+"/integrations", alice.Token, map[string]any{
		"type": "youtube", "id": "ytchannel",
	}); status != 204 {
		t.Fatalf("create youtube integration %d", status)
	}

	var list []struct {
		ID              string `json:"id"`
		Type            string `json:"type"`
		Account         map[string]any
		Syncing         *bool   `json:"syncing"`
		SubscriberCount *int    `json:"subscriber_count"`
		SyncedAt        *string `json:"synced_at"`
	}
	if status := h.decode("GET", "/guilds/"+guild.ID+"/integrations", alice.Token, nil, &list); status != 200 || len(list) != 2 {
		t.Fatalf("list integrations %d %+v", status, list)
	}
	if list[0].Type != "twitch" || list[0].Account["id"] != "streamer" || list[0].Syncing == nil {
		t.Fatalf("twitch object %+v", list[0])
	}
	if list[1].Type != "youtube" || list[1].SubscriberCount == nil || list[1].SyncedAt == nil {
		t.Fatalf("youtube object %+v", list[1])
	}

	// Validation: unknown type, missing id, bad grace period, foreign guild.
	if status, _ := h.request("POST", "/guilds/"+guild.ID+"/integrations", alice.Token, map[string]any{
		"type": "discord", "id": "x",
	}); status != 400 {
		t.Fatalf("invalid type status %d", status)
	}
	if status, _ := h.request("POST", "/guilds/"+guild.ID+"/integrations", alice.Token, map[string]any{
		"type": "twitch",
	}); status != 400 {
		t.Fatalf("missing id status %d", status)
	}
	if status, _ := h.request("PATCH", "/guilds/"+guild.ID+"/integrations/streamer", alice.Token, map[string]any{
		"expire_grace_period": 5,
	}); status != 400 {
		t.Fatalf("bad grace status %d", status)
	}
	if status, _ := h.request("PATCH", "/guilds/"+guild.ID+"/integrations/streamer", alice.Token, map[string]any{
		"expire_grace_period": 14, "expire_behavior": 1, "enable_emoticons": true,
	}); status != 204 {
		t.Fatalf("patch integration status %d", status)
	}
	if status, _ := h.request("POST", "/guilds/"+guild.ID+"/integrations", bob.Token, map[string]any{
		"type": "twitch", "id": "y",
	}); status != 403 {
		t.Fatalf("foreign create status %d", status)
	}
	if status, _ := h.request("POST", "/guilds/"+guild.ID+"/integrations/streamer/sync", alice.Token, nil); status != 204 {
		t.Fatalf("sync status %d", status)
	}
	if status, _ := h.request("POST", "/guilds/"+guild.ID+"/integrations/ytchannel/sync", bob.Token, nil); status != 403 {
		t.Fatalf("foreign sync status %d", status)
	}

	// Channel-scoped integrations.
	if status, _ := h.request("POST", "/guilds/"+guild.ID+"/integrations", alice.Token, map[string]any{
		"type": "twitch", "id": "chanstream", "channel_id": channel.ID,
	}); status != 204 {
		t.Fatalf("channel integration create %d", status)
	}
	var channelScoped []struct{ ID string }
	if status := h.decode("GET", "/channels/"+channel.ID+"/integrations", alice.Token, nil, &channelScoped); status != 200 || len(channelScoped) != 1 || channelScoped[0].ID != "chanstream" {
		t.Fatalf("channel integrations %d %+v", status, channelScoped)
	}
	var guildScoped []struct{ ID string }
	h.decode("GET", "/guilds/"+guild.ID+"/integrations", alice.Token, nil, &guildScoped)
	for _, in := range guildScoped {
		if in.ID == "chanstream" {
			t.Fatalf("channel integration leaked into guild list")
		}
	}
	if status, _ := h.request("DELETE", "/channels/"+channel.ID+"/integrations/chanstream", bob.Token, nil); status != 403 {
		t.Fatalf("foreign channel integration delete %d", status)
	}
	if status, _ := h.request("DELETE", "/channels/"+channel.ID+"/integrations/chanstream", alice.Token, nil); status != 204 {
		t.Fatalf("channel integration delete %d", status)
	}

	if status := h.decode("DELETE", "/guilds/"+guild.ID+"/integrations/streamer", alice.Token, nil, nil); status != 204 {
		t.Fatalf("delete integration %d", status)
	}
	var after []struct{ ID string }
	h.decode("GET", "/guilds/"+guild.ID+"/integrations", alice.Token, nil, &after)
	if len(after) != 1 {
		t.Fatalf("integrations after delete = %d", len(after))
	}

	// Bot integration drives the command-scope migration report.
	appID := h.botApplication(alice, guild.ID, "Integrations Bot")
	var migrated struct {
		IDs []string `json:"integration_ids_with_app_commands"`
	}
	if status := h.decode("POST", "/guilds/"+guild.ID+"/migrate-command-scope", alice.Token, nil, &migrated); status != 200 {
		t.Fatalf("migrate status %d", status)
	}
	found := false
	for _, id := range migrated.IDs {
		if id == appID {
			found = true
		}
	}
	if !found {
		t.Fatalf("migrate ids %+v missing %s", migrated.IDs, appID)
	}
	var mapping map[string][]string
	if status := h.decode("GET", "/users/@me/guilds/integration-application-ids", alice.Token, nil, &mapping); status != 200 || len(mapping[guild.ID]) != 1 {
		t.Fatalf("integration application ids %d %+v", status, mapping)
	}
	var onlyApps []struct {
		ID   string `json:"id"`
		Type string `json:"type"`
	}
	if status := h.decode("GET", "/guilds/"+guild.ID+"/integrations?has_commands=true", alice.Token, nil, &onlyApps); status != 200 || len(onlyApps) != 1 || onlyApps[0].ID != appID {
		t.Fatalf("has_commands filter %d %+v", status, onlyApps)
	}
}
