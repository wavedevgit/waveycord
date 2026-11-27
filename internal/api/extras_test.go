package api_test

import (
	"net/url"
	"testing"
	"time"
)

func TestGuildTemplates(t *testing.T) {
	h := newHarness(t)
	alice := h.register("templateowner")
	bob := h.register("templateuser")
	var guild struct{ ID string }
	h.decode("POST", "/guilds", alice.Token, map[string]any{"name": "Template Source"}, &guild)
	h.decode("POST", "/guilds/"+guild.ID+"/channels", alice.Token, map[string]any{"name": "general"}, nil)
	h.decode("POST", "/guilds/"+guild.ID+"/channels", alice.Token, map[string]any{"name": "voice", "type": 2}, nil)

	var created struct {
		Code        string  `json:"code"`
		Name        string  `json:"name"`
		Description *string `json:"description"`
		UsageCount  int     `json:"usage_count"`
		CreatorID   string  `json:"creator_id"`
	}
	if status := h.decode("POST", "/guilds/"+guild.ID+"/templates", alice.Token, map[string]any{
		"name": "Base Template", "description": "starter",
	}, &created); status != 201 || created.Code == "" {
		t.Fatalf("create template status %d %+v", status, created)
	}
	if created.CreatorID != alice.ID || created.Name != "Base Template" {
		t.Fatalf("unexpected template %+v", created)
	}

	var listed []struct{ Code string }
	if status := h.decode("GET", "/guilds/"+guild.ID+"/templates", alice.Token, nil, &listed); status != 200 || len(listed) != 1 {
		t.Fatalf("list templates status %d %+v", status, listed)
	}

	// Public template view.
	var public struct {
		Code          string         `json:"code"`
		SourceGuildID string         `json:"source_guild_id"`
		Serialized    map[string]any `json:"serialized_source_guild"`
	}
	if status := h.decode("GET", "/guilds/templates/"+created.Code, "", nil, &public); status != 200 ||
		public.SourceGuildID != guild.ID {
		t.Fatalf("public template status %d %+v", status, public)
	}
	channels, _ := public.Serialized["channels"].([]any)
	if len(channels) != 2 {
		t.Fatalf("template channels = %d", len(channels))
	}

	// Sync and rename.
	if status := h.decode("PUT", "/guilds/"+guild.ID+"/templates/"+created.Code, alice.Token, nil, nil); status != 200 {
		t.Fatalf("sync status %d", status)
	}
	var renamed struct{ Name string }
	if status := h.decode("PATCH", "/guilds/"+guild.ID+"/templates/"+created.Code, alice.Token,
		map[string]any{"name": "Renamed Template"}, &renamed); status != 200 || renamed.Name != "Renamed Template" {
		t.Fatalf("rename status %d %+v", status, renamed)
	}

	// Another user creates a guild from the template.
	var newGuild struct{ ID string }
	if status := h.decode("POST", "/guilds/templates/"+created.Code, bob.Token,
		map[string]any{"name": "From Template"}, &newGuild); status != 201 || newGuild.ID == "" {
		t.Fatalf("use template status %d", status)
	}
	var newChannels []struct {
		Name string `json:"name"`
		Type int    `json:"type"`
	}
	h.decode("GET", "/guilds/"+newGuild.ID+"/channels", bob.Token, nil, &newChannels)
	if len(newChannels) != 2 || newChannels[0].Name != "general" || newChannels[1].Type != 2 {
		t.Fatalf("template channels not copied: %+v", newChannels)
	}

	if status := h.decode("DELETE", "/guilds/"+guild.ID+"/templates/"+created.Code, alice.Token, nil, nil); status != 204 {
		t.Fatalf("delete template status %d", status)
	}
	if status := h.decode("GET", "/guilds/templates/"+created.Code, "", nil, nil); status != 404 {
		t.Fatalf("deleted template status %d", status)
	}
}

func TestScheduledEvents(t *testing.T) {
	h := newHarness(t)
	alice := h.register("eventowner")
	var guild struct{ ID string }
	h.decode("POST", "/guilds", alice.Token, map[string]any{"name": "Events"}, &guild)
	var text, voice, stage struct{ ID string }
	h.decode("POST", "/guilds/"+guild.ID+"/channels", alice.Token, map[string]any{"name": "text"}, &text)
	h.decode("POST", "/guilds/"+guild.ID+"/channels", alice.Token, map[string]any{"name": "voice", "type": 2}, &voice)
	h.decode("POST", "/guilds/"+guild.ID+"/channels", alice.Token, map[string]any{"name": "stage", "type": 13}, &stage)

	start := time.Now().Add(time.Hour).UTC().Format(time.RFC3339)
	end := time.Now().Add(2 * time.Hour).UTC().Format(time.RFC3339)

	var external struct {
		ID             string         `json:"id"`
		Status         int            `json:"status"`
		EntityType     int            `json:"entity_type"`
		EntityMetadata map[string]any `json:"entity_metadata"`
	}
	if status := h.decode("POST", "/guilds/"+guild.ID+"/scheduled-events", alice.Token, map[string]any{
		"name": "Community meetup", "entity_type": 3,
		"scheduled_start_time": start, "scheduled_end_time": end,
		"entity_metadata": map[string]any{"location": "Town Hall"},
	}, &external); status != 201 || external.Status != 1 || external.EntityType != 3 {
		t.Fatalf("create external event %d %+v", status, external)
	}
	if external.EntityMetadata["location"] != "Town Hall" {
		t.Fatalf("metadata = %+v", external.EntityMetadata)
	}
	for _, body := range []map[string]any{
		{"privacy_level": 1},
		{"channel_id": text.ID},
		{"entity_metadata": map[string]any{"location": ""}},
		{"scheduled_end_time": time.Now().Add(-time.Hour).UTC().Format(time.RFC3339)},
	} {
		if status, _ := h.request("PATCH", "/guilds/"+guild.ID+"/scheduled-events/"+external.ID, alice.Token, body); status != 400 {
			t.Fatalf("invalid event update %d %+v", status, body)
		}
	}

	// Validation failures.
	bad := []map[string]any{
		{"name": "no end", "entity_type": 3, "scheduled_start_time": start,
			"entity_metadata": map[string]any{"location": "x"}},
		{"name": "no location", "entity_type": 3, "scheduled_start_time": start,
			"scheduled_end_time": end, "entity_metadata": map[string]any{}},
		{"name": "bad channel", "entity_type": 2, "scheduled_start_time": start, "channel_id": text.ID},
		{"name": "stage no channel", "entity_type": 1, "scheduled_start_time": start},
		{"name": "bad time", "entity_type": 3, "scheduled_start_time": "tomorrow",
			"scheduled_end_time": end, "entity_metadata": map[string]any{"location": "x"}},
	}
	for _, body := range bad {
		if status, _ := h.request("POST", "/guilds/"+guild.ID+"/scheduled-events", alice.Token, body); status != 400 {
			t.Fatalf("invalid event %v status %d", body["name"], status)
		}
	}

	var voiceEvent struct {
		ID       string  `json:"id"`
		EntityID *string `json:"entity_id"`
	}
	if status := h.decode("POST", "/guilds/"+guild.ID+"/scheduled-events", alice.Token, map[string]any{
		"name": "Voice hangout", "entity_type": 2, "channel_id": voice.ID,
		"scheduled_start_time": start,
	}, &voiceEvent); status != 201 || voiceEvent.EntityID == nil || *voiceEvent.EntityID != voice.ID {
		t.Fatalf("voice event %d %+v", status, voiceEvent)
	}

	var listed []struct{ ID string }
	if status := h.decode("GET", "/guilds/"+guild.ID+"/scheduled-events", alice.Token, nil, &listed); status != 200 || len(listed) != 2 {
		t.Fatalf("list events %d %+v", status, listed)
	}
	var fetched struct{ Name string }
	if status := h.decode("GET", "/guilds/"+guild.ID+"/scheduled-events/"+external.ID, alice.Token, nil, &fetched); status != 200 || fetched.Name != "Community meetup" {
		t.Fatalf("get event %d %+v", status, fetched)
	}
	var updated struct {
		Name   string `json:"name"`
		Status int    `json:"status"`
	}
	if status, _ := h.request("PATCH", "/guilds/"+guild.ID+"/scheduled-events/"+external.ID, alice.Token, map[string]any{"status": 3}); status != 400 {
		t.Fatalf("invalid transition %d", status)
	}
	if status, _ := h.request("PATCH", "/guilds/"+guild.ID+"/scheduled-events/"+external.ID, alice.Token, map[string]any{"status": 2}); status != 200 {
		t.Fatalf("start event %d", status)
	}
	if status := h.decode("PATCH", "/guilds/"+guild.ID+"/scheduled-events/"+external.ID, alice.Token, map[string]any{
		"name": "Meetup (completed)", "status": 3,
	}, &updated); status != 200 || updated.Status != 3 {
		t.Fatalf("patch event %d %+v", status, updated)
	}
	if status, _ := h.request("PATCH", "/guilds/"+guild.ID+"/scheduled-events/"+external.ID, alice.Token, map[string]any{"status": 2}); status != 400 {
		t.Fatalf("terminal event restarted %d", status)
	}
	var users []any
	if status := h.decode("GET", "/guilds/"+guild.ID+"/scheduled-events/"+external.ID+"/users", alice.Token, nil, &users); status != 200 || len(users) != 1 {
		t.Fatalf("event users %d %+v", status, users)
	}
	interestPath := "/guilds/" + guild.ID + "/scheduled-events/" + external.ID + "/users"
	for i := 0; i < 2; i++ {
		if status, _ := h.request("PUT", interestPath+"/@me", alice.Token, nil); status != 200 {
			t.Fatalf("subscribe: %d", status)
		}
	}
	var subscribers []struct {
		User   struct{ ID string } `json:"user"`
		Member map[string]any      `json:"member"`
	}
	if status := h.decode("GET", interestPath+"?with_member=true&limit=1", alice.Token, nil, &subscribers); status != 200 || len(subscribers) != 1 || subscribers[0].User.ID != alice.ID || subscribers[0].Member == nil {
		t.Fatalf("subscribers %d %+v", status, subscribers)
	}
	var counted struct {
		Count int `json:"user_count"`
	}
	if status := h.decode("GET", "/guilds/"+guild.ID+"/scheduled-events/"+external.ID+"?with_user_count=true", alice.Token, nil, &counted); status != 200 || counted.Count != 1 {
		t.Fatalf("event count %d %+v", status, counted)
	}
	if status := h.decode("GET", interestPath+"?after="+alice.ID, alice.Token, nil, &users); status != 200 || len(users) != 0 {
		t.Fatalf("after %d %+v", status, users)
	}
	if status, _ := h.request("GET", interestPath+"?limit=101", alice.Token, nil); status != 400 {
		t.Fatalf("limit %d", status)
	}
	for i := 0; i < 2; i++ {
		if status, _ := h.request("DELETE", interestPath+"/@me", alice.Token, nil); status != 204 {
			t.Fatalf("unsubscribe %d", status)
		}
	}
	if status := h.decode("GET", interestPath, alice.Token, nil, &users); status != 200 || len(users) != 0 {
		t.Fatalf("unsubscribed %d %+v", status, users)
	}
	if status := h.decode("DELETE", "/guilds/"+guild.ID+"/scheduled-events/"+external.ID, alice.Token, nil, nil); status != 204 {
		t.Fatalf("delete event %d", status)
	}
	if status := h.decode("GET", "/guilds/"+guild.ID+"/scheduled-events/"+external.ID, alice.Token, nil, nil); status != 404 {
		t.Fatalf("deleted event %d", status)
	}
}

func TestStageInstancesAndIntegrations(t *testing.T) {
	h := newHarness(t)
	alice := h.register("stageowner")
	var guild struct{ ID string }
	h.decode("POST", "/guilds", alice.Token, map[string]any{"name": "Stage Guild"}, &guild)
	var text, stage struct{ ID string }
	h.decode("POST", "/guilds/"+guild.ID+"/channels", alice.Token, map[string]any{"name": "text"}, &text)
	h.decode("POST", "/guilds/"+guild.ID+"/channels", alice.Token, map[string]any{"name": "stage", "type": 13}, &stage)

	var si struct {
		ID      string `json:"id"`
		Channel string `json:"channel_id"`
		Topic   string `json:"topic"`
		Privacy int    `json:"privacy_level"`
	}
	if status := h.decode("POST", "/stage-instances", alice.Token, map[string]any{
		"channel_id": stage.ID, "topic": "Opening keynote",
	}, &si); status != 201 || si.Channel != stage.ID || si.Topic != "Opening keynote" {
		t.Fatalf("create stage %d %+v", status, si)
	}
	if status := h.decode("POST", "/stage-instances", alice.Token, map[string]any{
		"channel_id": stage.ID, "topic": "Second",
	}, nil); status != 400 {
		t.Fatalf("duplicate stage status %d", status)
	}
	if status := h.decode("POST", "/stage-instances", alice.Token, map[string]any{
		"channel_id": text.ID, "topic": "Wrong channel",
	}, nil); status != 400 {
		t.Fatalf("non-stage channel status %d", status)
	}
	var got struct{ Topic string }
	if status := h.decode("GET", "/stage-instances/"+stage.ID, alice.Token, nil, &got); status != 200 || got.Topic != "Opening keynote" {
		t.Fatalf("get stage %d %+v", status, got)
	}
	var patched struct{ Topic string }
	if status := h.decode("PATCH", "/stage-instances/"+stage.ID, alice.Token,
		map[string]any{"topic": "Updated topic"}, &patched); status != 200 || patched.Topic != "Updated topic" {
		t.Fatalf("patch stage %d %+v", status, patched)
	}
	if status := h.decode("DELETE", "/stage-instances/"+stage.ID, alice.Token, nil, nil); status != 204 {
		t.Fatalf("delete stage %d", status)
	}
	if status := h.decode("GET", "/stage-instances/"+stage.ID, alice.Token, nil, nil); status != 404 {
		t.Fatalf("deleted stage %d", status)
	}

	// Bot install creates an integration row.
	var app struct {
		ID           string               `json:"id"`
		Name         string               `json:"name"`
		ClientSecret string               `json:"client_secret"`
		Bot          *struct{ ID string } `json:"bot"`
	}
	h.decode("POST", "/applications", alice.Token, map[string]any{"name": "Integration Bot"}, &app)
	redirect := "https://example.test/int"
	h.decode("PATCH", "/applications/"+app.ID, alice.Token, map[string]any{"redirect_uris": []string{redirect}}, nil)
	status, body := h.form("/oauth2/authorize", url.Values{
		"client_id": {app.ID}, "response_type": {"code"}, "redirect_uri": {redirect},
		"scope": {"bot"}, "guild_id": {guild.ID}, "authorize": {"true"},
	}, alice.Token, "application/json")
	if status != 200 {
		t.Fatalf("bot install status %d body %s", status, body)
	}

	var integrations []struct {
		ID      string         `json:"id"`
		Name    string         `json:"name"`
		Type    string         `json:"type"`
		Account map[string]any `json:"account"`
		Scopes  []string       `json:"scopes"`
	}
	if status := h.decode("GET", "/guilds/"+guild.ID+"/integrations", alice.Token, nil, &integrations); status != 200 || len(integrations) != 1 {
		t.Fatalf("list integrations %d %+v", status, integrations)
	}
	if integrations[0].ID != app.ID || integrations[0].Type != "discord" || integrations[0].Account["id"] != app.Bot.ID {
		t.Fatalf("unexpected integration %+v", integrations[0])
	}
	if integrations[0].Scopes == nil {
		t.Fatalf("bot integration scopes missing: %+v", integrations[0])
	}
	var one struct{ ID string }
	if status := h.decode("GET", "/guilds/"+guild.ID+"/integrations/"+app.ID, alice.Token, nil, &one); status != 200 || one.ID != app.ID {
		t.Fatalf("get integration %d %+v", status, one)
	}
	// Subscriber settings belong to Twitch/YouTube integrations only.
	if status, _ := h.request("PATCH", "/guilds/"+guild.ID+"/integrations/"+app.ID, alice.Token,
		map[string]any{"enable_emoticons": true}); status != 400 {
		t.Fatalf("patch bot integration %d", status)
	}
	if status := h.decode("DELETE", "/guilds/"+guild.ID+"/integrations/"+app.ID, alice.Token, nil, nil); status != 204 {
		t.Fatalf("delete integration %d", status)
	}
	if status := h.decode("GET", "/guilds/"+guild.ID+"/members/"+app.Bot.ID, alice.Token, nil, nil); status != 404 {
		t.Fatalf("bot still a member after integration removal: %d", status)
	}
}

func TestScheduledEventRecurrenceRule(t *testing.T) {
	h := newHarness(t)
	alice := h.register("recurringowner")
	var guild struct{ ID string }
	h.decode("POST", "/guilds", alice.Token, map[string]any{"name": "Recurring"}, &guild)
	start := time.Now().Add(time.Hour).UTC().Format(time.RFC3339)
	ruleStart := time.Now().UTC().Format(time.RFC3339)

	var created struct {
		ID             string `json:"id"`
		RecurrenceRule *struct {
			Frequency int   `json:"frequency"`
			Interval  int   `json:"interval"`
			ByWeekday []int `json:"by_weekday"`
		} `json:"recurrence_rule"`
	}
	if status := h.decode("POST", "/guilds/"+guild.ID+"/scheduled-events", alice.Token, map[string]any{
		"name": "Weekly standup", "entity_type": 3,
		"scheduled_start_time": start,
		"scheduled_end_time":   time.Now().Add(2 * time.Hour).UTC().Format(time.RFC3339),
		"entity_metadata":      map[string]any{"location": "Voice lounge"},
		"recurrence_rule": map[string]any{
			"start": ruleStart, "frequency": 2, "interval": 1, "by_weekday": []int{2},
		},
	}, &created); status != 201 || created.RecurrenceRule == nil || created.RecurrenceRule.Frequency != 2 {
		t.Fatalf("create recurring event %d %+v", status, created)
	}
	if len(created.RecurrenceRule.ByWeekday) != 1 || created.RecurrenceRule.ByWeekday[0] != 2 {
		t.Fatalf("recurrence rule not persisted: %+v", created.RecurrenceRule)
	}

	invalid := []map[string]any{
		{"start": ruleStart, "frequency": 2, "interval": 2, "by_weekday": []int{1, 2}}, // weekly needs one day
		{"start": ruleStart, "frequency": 2, "interval": 2, "by_weekday": []int{2}, "end": ruleStart},
		{"start": ruleStart, "frequency": 3, "interval": 1, "by_weekday": []int{0, 2}}, // not an allowed set
		{"start": ruleStart, "frequency": 1, "interval": 1, "by_weekday": []int{2}},    // monthly needs by_n_weekday
		{"start": ruleStart, "frequency": 0, "interval": 1, "by_month": []int{2}, "by_month_day": []int{30}},
		{"start": ruleStart, "frequency": 2, "interval": 1, "by_weekday": []int{2}, "by_n_weekday": []map[string]any{{"n": 1, "day": 2}}},
		{"start": ruleStart, "frequency": 2, "interval": 1, "by_weekday": []int{2}, "count": 3},
		{"start": ruleStart, "frequency": 2, "interval": 3, "by_weekday": []int{2}},
	}
	for i, body := range invalid {
		payload := map[string]any{
			"name": "Bad rule", "entity_type": 3,
			"scheduled_start_time": start,
			"scheduled_end_time":   time.Now().Add(2 * time.Hour).UTC().Format(time.RFC3339),
			"entity_metadata":      map[string]any{"location": "Here"},
			"recurrence_rule":      body,
		}
		if status, _ := h.request("POST", "/guilds/"+guild.ID+"/scheduled-events", alice.Token, payload); status != 400 {
			t.Fatalf("invalid rule %d status %d", i, status)
		}
	}

	// Monthly fourth Wednesday is accepted and replaces the stored rule.
	if status := h.decode("PATCH", "/guilds/"+guild.ID+"/scheduled-events/"+created.ID, alice.Token, map[string]any{
		"recurrence_rule": map[string]any{
			"start": ruleStart, "frequency": 1, "interval": 1,
			"by_n_weekday": []map[string]any{{"n": 4, "day": 2}},
		},
	}, nil); status != 200 {
		t.Fatalf("patch recurrence %d", status)
	}
}
