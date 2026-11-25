package api_test

import (
	"testing"
)

// TestAutoModMentionRaid proves the guild-scoped mention window: mentions spread
// across channels and messages aggregate, a burst above the threshold blocks, and
// normal volume does not.
func TestAutoModMentionRaid(t *testing.T) {
	h := newHarness(t)
	alice := h.register("raidowner")
	bob := h.register("raidmember")
	var guild struct{ ID string }
	h.decode("POST", "/guilds", alice.Token, map[string]any{"name": "Raid Guild"}, &guild)
	var c1, c2 struct{ ID string }
	h.decode("POST", "/guilds/"+guild.ID+"/channels", alice.Token, map[string]any{"name": "one"}, &c1)
	h.decode("POST", "/guilds/"+guild.ID+"/channels", alice.Token, map[string]any{"name": "two"}, &c2)

	var rule struct{ ID string }
	if status := h.decode("POST", "/guilds/"+guild.ID+"/auto-moderation/rules", alice.Token, map[string]any{
		"name": "Mention raid", "event_type": 1, "trigger_type": 6,
		"trigger_metadata": map[string]any{"mention_raid_protection_enabled": true},
		"actions":          []map[string]any{{"type": 1}},
	}, &rule); status != 201 {
		t.Fatalf("create raid rule status %d", status)
	}

	twoMentions := "hey <@" + bob.ID + "> and <@" + alice.ID + ">"
	oneMention := "hey <@" + bob.ID + ">"

	// Normal volume: six messages with two mentions each is below the ten-mention
	// threshold only until the count crosses; send four first.
	for i := 0; i < 4; i++ {
		channel := c1.ID
		if i%2 == 1 {
			channel = c2.ID
		}
		if status, body := h.request("POST", "/channels/"+channel+"/messages", alice.Token,
			map[string]any{"content": twoMentions}); status != 200 {
			t.Fatalf("mention message %d status %d body %s", i+1, status, body)
		}
	}
	// 8 mentions so far, spread across both channels. Two more messages bring the
	// guild total to 12, so the second is blocked (11 > 10).
	if status, _ := h.request("POST", "/channels/"+c2.ID+"/messages", alice.Token,
		map[string]any{"content": twoMentions}); status != 200 {
		t.Fatalf("ninth/tenth mentions should still pass: %d", status)
	}
	status, body := h.request("POST", "/channels/"+c1.ID+"/messages", alice.Token,
		map[string]any{"content": oneMention})
	if status != 400 {
		t.Fatalf("raid burst status %d body %s", status, body)
	}

	var stored []struct{ ID string }
	h.decode("GET", "/channels/"+c1.ID+"/messages?limit=100", alice.Token, nil, &stored)
	var stored2 []struct{ ID string }
	h.decode("GET", "/channels/"+c2.ID+"/messages?limit=100", alice.Token, nil, &stored2)
	if len(stored)+len(stored2) != 5 {
		t.Fatalf("stored messages = %d + %d, want 5", len(stored), len(stored2))
	}

	// A second guild has an independent window: the same burst does not trip its
	// rule until its own threshold is crossed.
	var other struct{ ID string }
	h.decode("POST", "/guilds", alice.Token, map[string]any{"name": "Quiet Guild"}, &other)
	var quiet struct{ ID string }
	h.decode("POST", "/guilds/"+other.ID+"/channels", alice.Token, map[string]any{"name": "quiet"}, &quiet)
	h.decode("POST", "/guilds/"+other.ID+"/auto-moderation/rules", alice.Token, map[string]any{
		"name": "Mention raid", "event_type": 1, "trigger_type": 6,
		"actions": []map[string]any{{"type": 1}},
	}, nil)
	for i := 0; i < 3; i++ {
		if status, _ := h.request("POST", "/channels/"+quiet.ID+"/messages", alice.Token,
			map[string]any{"content": twoMentions}); status != 200 {
			t.Fatalf("other guild message %d status %d", i+1, status)
		}
	}

	// Mention count includes @everyone/@here; a burst of those also trips.
	ev := "ping <@everyone> <@here> <@" + bob.ID + "> <@" + alice.ID + ">"
	blocked := false
	for i := 0; i < 5; i++ {
		status, _ := h.request("POST", "/channels/"+c1.ID+"/messages", alice.Token,
			map[string]any{"content": ev})
		if status == 400 {
			blocked = true
			break
		}
	}
	if !blocked {
		t.Fatal("everyone/here mention burst was not blocked")
	}
}
