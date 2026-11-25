package api_test

import (
	"strings"
	"testing"
)

func TestAutoModerationRulesAndEnforcement(t *testing.T) {
	h := newHarness(t)
	alice := h.register("automodowner")
	bob := h.register("automodoutsider")
	var guild struct{ ID string }
	h.decode("POST", "/guilds", alice.Token, map[string]any{"name": "AutoMod Guild"}, &guild)
	var general, alerts struct{ ID string }
	h.decode("POST", "/guilds/"+guild.ID+"/channels", alice.Token, map[string]any{"name": "general"}, &general)
	h.decode("POST", "/guilds/"+guild.ID+"/channels", alice.Token, map[string]any{"name": "alerts"}, &alerts)

	// Only managers may read or create rules.
	if status, _ := h.request("GET", "/guilds/"+guild.ID+"/auto-moderation/rules", bob.Token, nil); status != 403 {
		t.Fatalf("non-manager list status %d", status)
	}

	var keywordRule struct {
		ID              string           `json:"id"`
		Name            string           `json:"name"`
		Enabled         bool             `json:"enabled"`
		TriggerType     int              `json:"trigger_type"`
		TriggerMetadata map[string]any   `json:"trigger_metadata"`
		Actions         []map[string]any `json:"actions"`
	}
	if status := h.decode("POST", "/guilds/"+guild.ID+"/auto-moderation/rules", alice.Token, map[string]any{
		"name":         "No bad words",
		"event_type":   1,
		"trigger_type": 1,
		"trigger_metadata": map[string]any{
			"keyword_filter": []string{"badword", "worseword"},
			"regex_patterns": []string{`\d{3}-\d{2}-\d{4}`},
		},
		"actions": []map[string]any{{"type": 1}},
		"enabled": true,
	}, &keywordRule); status != 201 {
		t.Fatalf("create rule status %d", status)
	}
	if keywordRule.TriggerType != 1 || len(keywordRule.Actions) != 1 || !keywordRule.Enabled {
		t.Fatalf("unexpected rule %+v", keywordRule)
	}

	var listed []struct{ ID string }
	if status := h.decode("GET", "/guilds/"+guild.ID+"/auto-moderation/rules", alice.Token, nil, &listed); status != 200 || len(listed) != 1 {
		t.Fatalf("list rules status %d %+v", status, listed)
	}

	// Keyword block.
	status, body := h.request("POST", "/channels/"+general.ID+"/messages", alice.Token,
		map[string]any{"content": "this has a badword inside"})
	if status != 400 || !strings.Contains(string(body), "200000") {
		t.Fatalf("keyword block status %d body %s", status, body)
	}
	var msgs []struct{ ID string }
	h.decode("GET", "/channels/"+general.ID+"/messages", alice.Token, nil, &msgs)
	if len(msgs) != 0 {
		t.Fatalf("blocked message was persisted: %+v", msgs)
	}
	// Regex block.
	if status, _ := h.request("POST", "/channels/"+general.ID+"/messages", alice.Token,
		map[string]any{"content": "id 123-45-6789"}); status != 400 {
		t.Fatalf("regex block status %d", status)
	}
	// Disabled rules do not block.
	if status := h.decode("PATCH", "/guilds/"+guild.ID+"/auto-moderation/rules/"+keywordRule.ID, alice.Token,
		map[string]any{"enabled": false}, nil); status != 200 {
		t.Fatalf("disable rule status %d", status)
	}
	if status, _ := h.request("POST", "/channels/"+general.ID+"/messages", alice.Token,
		map[string]any{"content": "badword allowed now"}); status != 200 {
		t.Fatalf("disabled rule status %d", status)
	}
	h.decode("PATCH", "/guilds/"+guild.ID+"/auto-moderation/rules/"+keywordRule.ID, alice.Token,
		map[string]any{"enabled": true}, nil)

	// Exempt channel bypasses the keyword rule.
	var exemptRule struct{ ID string }
	if status := h.decode("POST", "/guilds/"+guild.ID+"/auto-moderation/rules", alice.Token, map[string]any{
		"name":             "Exempted channel",
		"event_type":       1,
		"trigger_type":     1,
		"trigger_metadata": map[string]any{"keyword_filter": []string{"badword"}},
		"actions":          []map[string]any{{"type": 1}},
		"exempt_channels":  []string{general.ID},
	}, &exemptRule); status != 201 {
		t.Fatalf("exempt rule status %d", status)
	}
	// Exempting the channel on the blocking rule lets the same message through.
	if status := h.decode("PATCH", "/guilds/"+guild.ID+"/auto-moderation/rules/"+keywordRule.ID, alice.Token,
		map[string]any{"exempt_channels": []string{general.ID}}, nil); status != 200 {
		t.Fatalf("exempt channel patch status %d", status)
	}
	if status, _ := h.request("POST", "/channels/"+general.ID+"/messages", alice.Token,
		map[string]any{"content": "badword is exempt here"}); status != 200 {
		t.Fatalf("exempt channel should allow the message: %d", status)
	}
	if status := h.decode("PATCH", "/guilds/"+guild.ID+"/auto-moderation/rules/"+keywordRule.ID, alice.Token,
		map[string]any{"exempt_channels": []string{}}, nil); status != 200 {
		t.Fatalf("clear exemption status %d", status)
	}
	if status, _ := h.request("POST", "/channels/"+general.ID+"/messages", alice.Token,
		map[string]any{"content": "badword"}); status != 400 {
		t.Fatalf("rule should block again after clearing exempt_channels: %d", status)
	}
	if status := h.decode("DELETE", "/guilds/"+guild.ID+"/auto-moderation/rules/"+exemptRule.ID, alice.Token, nil, nil); status != 204 {
		t.Fatalf("delete exempt rule status %d", status)
	}

	// Mention limit: 3 mentions with limit 2 is blocked, 2 is allowed.
	mentions := []string{"<@" + bob.ID + ">", "<@" + alice.ID + ">"}
	var mentionRule struct{ ID string }
	if status := h.decode("POST", "/guilds/"+guild.ID+"/auto-moderation/rules", alice.Token, map[string]any{
		"name":             "Mention limit",
		"event_type":       1,
		"trigger_type":     4,
		"trigger_metadata": map[string]any{"mention_total_limit": 2},
		"actions":          []map[string]any{{"type": 1}},
	}, &mentionRule); status != 201 {
		t.Fatalf("mention rule status %d", status)
	}
	tooMany := "hey " + strings.Join(mentions, " ") + " and <@" + bob.ID + ">"
	if status, _ := h.request("POST", "/channels/"+general.ID+"/messages", alice.Token,
		map[string]any{"content": tooMany}); status != 400 {
		t.Fatalf("mention spam status %d", status)
	}
	if status, _ := h.request("POST", "/channels/"+general.ID+"/messages", alice.Token,
		map[string]any{"content": "hey " + strings.Join(mentions, " ")}); status != 200 {
		t.Fatalf("allowed mentions status %d", status)
	}
	h.decode("DELETE", "/guilds/"+guild.ID+"/auto-moderation/rules/"+mentionRule.ID, alice.Token, nil, nil)

	// Alert + timeout actions: message sends, timeout applies, alert channel gets a
	// message authored by the system account.
	var actionRule struct{ ID string }
	if status := h.decode("POST", "/guilds/"+guild.ID+"/auto-moderation/rules", alice.Token, map[string]any{
		"name":             "Timeout and alert",
		"event_type":       1,
		"trigger_type":     1,
		"trigger_metadata": map[string]any{"keyword_filter": []string{"spoiler"}},
		"actions": []map[string]any{
			{"type": 2, "metadata": map[string]any{"channel_id": alerts.ID}},
			{"type": 3, "metadata": map[string]any{"duration_seconds": 60}},
		},
	}, &actionRule); status != 201 {
		t.Fatalf("action rule status %d", status)
	}
	if status, _ := h.request("POST", "/channels/"+general.ID+"/messages", alice.Token,
		map[string]any{"content": "no spoiler here"}); status != 200 {
		t.Fatalf("timeout rule should allow the message: %d", status)
	}
	var alertMsgs []struct {
		Content string                    `json:"content"`
		Author  struct{ Username string } `json:"author"`
	}
	if status := h.decode("GET", "/channels/"+alerts.ID+"/messages", alice.Token, nil, &alertMsgs); status != 200 || len(alertMsgs) != 1 {
		t.Fatalf("alert messages status %d %+v", status, alertMsgs)
	}
	if !strings.Contains(alertMsgs[0].Content, "Timeout and alert") || alertMsgs[0].Author.Username != "AutoMod" {
		t.Fatalf("unexpected alert %+v", alertMsgs[0])
	}
	var member struct {
		CommunicationDisabledUntil *string `json:"communication_disabled_until"`
	}
	h.decode("GET", "/guilds/"+guild.ID+"/members/"+alice.ID, alice.Token, nil, &member)
	if member.CommunicationDisabledUntil == nil {
		t.Fatal("timeout action did not set communication_disabled_until")
	}

	// Validation and cleanup.
	if status, _ := h.request("POST", "/guilds/"+guild.ID+"/auto-moderation/rules", alice.Token, map[string]any{
		"name": "bad alert", "event_type": 1, "trigger_type": 1,
		"trigger_metadata": map[string]any{"keyword_filter": []string{"x"}},
		"actions":          []map[string]any{{"type": 2}},
	}); status != 400 {
		t.Fatalf("alert without channel status %d", status)
	}
	if status, _ := h.request("DELETE", "/guilds/"+guild.ID+"/auto-moderation/rules/"+keywordRule.ID, alice.Token, nil); status != 204 {
		t.Fatalf("delete rule status %d", status)
	}
}
