package api_test

import (
	"strings"
	"testing"
)

// TestAutoModSpamTrigger proves the stateful rate window: the first five messages
// are accepted, the sixth inside the window is blocked by a SPAM rule.
func TestAutoModSpamTrigger(t *testing.T) {
	h := newHarness(t)
	alice := h.register("spamowner")
	var guild struct{ ID string }
	h.decode("POST", "/guilds", alice.Token, map[string]any{"name": "Spam Guild"}, &guild)
	var channel struct{ ID string }
	h.decode("POST", "/guilds/"+guild.ID+"/channels", alice.Token, map[string]any{"name": "chat"}, &channel)

	var rule struct{ ID string }
	if status := h.decode("POST", "/guilds/"+guild.ID+"/auto-moderation/rules", alice.Token, map[string]any{
		"name": "Spam", "event_type": 1, "trigger_type": 2,
		"actions": []map[string]any{{"type": 1}},
	}, &rule); status != 201 {
		t.Fatalf("create spam rule status %d", status)
	}

	for i := 0; i < 5; i++ {
		if status, body := h.request("POST", "/channels/"+channel.ID+"/messages", alice.Token,
			map[string]any{"content": "burst message"}); status != 200 {
			t.Fatalf("message %d status %d body %s", i+1, status, body)
		}
	}
	status, body := h.request("POST", "/channels/"+channel.ID+"/messages", alice.Token,
		map[string]any{"content": "burst message"})
	if status != 400 || !strings.Contains(string(body), "200000") {
		t.Fatalf("sixth message status %d body %s", status, body)
	}
	var msgs []struct{ ID string }
	h.decode("GET", "/channels/"+channel.ID+"/messages", alice.Token, nil, &msgs)
	if len(msgs) != 5 {
		t.Fatalf("stored messages = %d, want 5", len(msgs))
	}

	// A different channel has its own window.
	var other struct{ ID string }
	h.decode("POST", "/guilds/"+guild.ID+"/channels", alice.Token, map[string]any{"name": "other"}, &other)
	if status, _ := h.request("POST", "/channels/"+other.ID+"/messages", alice.Token,
		map[string]any{"content": "fresh channel"}); status != 200 {
		t.Fatalf("other channel status %d", status)
	}
}

// TestAutoModMemberProfileTrigger proves profile scans run on join and on member
// update, applying the configured timeout and alert actions.
func TestAutoModMemberProfileTrigger(t *testing.T) {
	h := newHarness(t)
	alice := h.register("profileowner")
	var guild struct{ ID string }
	h.decode("POST", "/guilds", alice.Token, map[string]any{"name": "Profile Guild"}, &guild)
	var channel, alerts struct{ ID string }
	h.decode("POST", "/guilds/"+guild.ID+"/channels", alice.Token, map[string]any{"name": "chat"}, &channel)
	h.decode("POST", "/guilds/"+guild.ID+"/channels", alice.Token, map[string]any{"name": "mod-log"}, &alerts)
	var invite struct{ Code string }
	h.decode("POST", "/channels/"+channel.ID+"/invites", alice.Token, map[string]any{}, &invite)

	var rule struct{ ID string }
	if status := h.decode("POST", "/guilds/"+guild.ID+"/auto-moderation/rules", alice.Token, map[string]any{
		"name": "Profile filter", "event_type": 1, "trigger_type": 5,
		"trigger_metadata": map[string]any{"keyword_filter": []string{"wicked"}},
		"actions": []map[string]any{
			{"type": 3, "metadata": map[string]any{"duration_seconds": 120}},
			{"type": 2, "metadata": map[string]any{"channel_id": alerts.ID}},
		},
	}, &rule); status != 201 {
		t.Fatalf("create profile rule status %d", status)
	}

	// Username is scanned at join time.
	var forbidden struct{ Token string }
	if status := h.decode("POST", "/auth/register", "", map[string]any{
		"username": "wickedname", "password": "correct horse battery",
		"email": "wicked@example.test", "invite": invite.Code,
	}, &forbidden); status != 201 {
		t.Fatalf("register via invite status %d", status)
	}
	var joined struct {
		ID string `json:"id"`
	}
	if status := h.decode("GET", "/users/@me", forbidden.Token, nil, &joined); status != 200 {
		t.Fatalf("self status %d", status)
	}
	var joinedMember struct {
		CommunicationDisabledUntil *string `json:"communication_disabled_until"`
	}
	if status := h.decode("GET", "/guilds/"+guild.ID+"/members/"+joined.ID, alice.Token, nil, &joinedMember); status != 200 {
		t.Fatalf("join member status %d", status)
	}
	if joinedMember.CommunicationDisabledUntil == nil {
		t.Fatal("join-time profile scan did not apply the timeout")
	}

	// Join without a matching name, then trigger the scan through a nickname
	// update.
	bob := h.register("cleanprofile")
	if status := h.decode("POST", "/invites/"+invite.Code, bob.Token, nil, nil); status != 200 {
		t.Fatalf("bob join status %d", status)
	}
	var clean struct {
		CommunicationDisabledUntil *string `json:"communication_disabled_until"`
	}
	h.decode("GET", "/guilds/"+guild.ID+"/members/"+bob.ID, alice.Token, nil, &clean)
	if clean.CommunicationDisabledUntil != nil {
		t.Fatal("clean profile was timed out on join")
	}
	if status := h.decode("PATCH", "/guilds/"+guild.ID+"/members/"+bob.ID, alice.Token,
		map[string]any{"nick": "wicked nick"}, nil); status != 200 {
		t.Fatalf("nick patch status %d", status)
	}
	var after struct {
		Nick                       *string `json:"nick"`
		CommunicationDisabledUntil *string `json:"communication_disabled_until"`
	}
	h.decode("GET", "/guilds/"+guild.ID+"/members/"+bob.ID, alice.Token, nil, &after)
	if after.Nick == nil || *after.Nick != "wicked nick" {
		t.Fatalf("nick = %v", after.Nick)
	}
	if after.CommunicationDisabledUntil == nil {
		t.Fatal("nickname profile scan did not apply the timeout")
	}

	// Both alerts were posted by the system account.
	var alertMsgs []struct {
		Content string                    `json:"content"`
		Author  struct{ Username string } `json:"author"`
	}
	h.decode("GET", "/channels/"+alerts.ID+"/messages", alice.Token, nil, &alertMsgs)
	if len(alertMsgs) != 2 {
		t.Fatalf("alert messages = %d, want 2", len(alertMsgs))
	}
	for _, m := range alertMsgs {
		if !strings.Contains(m.Content, "Profile filter") || m.Author.Username != "AutoMod" {
			t.Fatalf("unexpected alert %+v", m)
		}
	}

	// A profile rule without timeout/alert still validates and stores.
	if status := h.decode("POST", "/guilds/"+guild.ID+"/auto-moderation/rules", alice.Token, map[string]any{
		"name": "Profile block only", "event_type": 1, "trigger_type": 5,
		"trigger_metadata": map[string]any{"keyword_filter": []string{"zzz"}},
		"actions":          []map[string]any{{"type": 1}},
	}, nil); status != 201 {
		t.Fatalf("block-only profile rule status %d", status)
	}
}
