package api_test

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"testing"
	"time"
)

func TestCommandPermissionEnforcement(t *testing.T) {
	h := newHarness(t)
	alice := h.register("permdispatchowner")
	bob := h.register("permdispatchbob")
	charlie := h.register("permdispatchcharlie")
	dave := h.register("permdispatchdave")
	var guild struct{ ID string }
	h.decode("POST", "/guilds", alice.Token, map[string]any{"name": "Dispatch Perms"}, &guild)
	var channel struct{ ID string }
	h.decode("POST", "/guilds/"+guild.ID+"/channels", alice.Token, map[string]any{"name": "chat"}, &channel)
	var role struct{ ID string }
	h.decode("POST", "/guilds/"+guild.ID+"/roles", alice.Token, map[string]any{"name": "Mods", "permissions": "0"}, &role)
	var invite struct{ Code string }
	h.decode("POST", "/channels/"+channel.ID+"/invites", alice.Token, map[string]any{}, &invite)
	for _, acc := range []account{bob, charlie, dave} {
		h.request("POST", "/invites/"+invite.Code, acc.Token, nil)
	}
	if status, _ := h.request("PUT", "/guilds/"+guild.ID+"/members/"+charlie.ID+"/roles/"+role.ID, alice.Token, nil); status != 204 {
		t.Fatalf("assign role status %d", status)
	}

	var app struct {
		ID string `json:"id"`
	}
	h.decode("POST", "/applications", alice.Token, map[string]any{"name": "Dispatch Perm App"}, &app)
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	h.decode("PATCH", "/applications/"+app.ID, alice.Token, map[string]any{"verify_key": hex.EncodeToString(pub)}, nil)

	var commands []struct{ ID, Name string }
	h.decode("PUT", "/applications/"+app.ID+"/commands", alice.Token, []map[string]any{
		{"name": "moderate", "description": "Moderation", "type": 1},
		{"name": "adminonly", "description": "Admin only", "type": 1, "default_member_permissions": "8192"},
	}, &commands)
	var moderateID string
	for _, c := range commands {
		if c.Name == "moderate" {
			moderateID = c.ID
		}
	}
	if moderateID == "" {
		t.Fatal("moderate command missing")
	}

	// Role allow + user deny.
	if status := h.decode("PUT", "/applications/"+app.ID+"/guilds/"+guild.ID+"/commands/"+moderateID+"/permissions", alice.Token,
		map[string]any{"permissions": []map[string]any{
			{"id": role.ID, "type": 1, "permission": true},
			{"id": bob.ID, "type": 2, "permission": false},
		}}, nil); status != 200 {
		t.Fatalf("set permissions status %d", status)
	}

	now := time.Now().Unix()
	invoke := func(userID, command string) (int, map[string]any) {
		t.Helper()
		status, body := signedInteraction(t, h, priv, now, map[string]any{
			"id": "i-" + userID, "type": 2, "application_id": app.ID,
			"guild_id": guild.ID, "channel_id": channel.ID, "token": "t",
			"member": map[string]any{"user": map[string]any{"id": userID}},
			"data":   map[string]any{"name": command, "type": 1},
		})
		var reply map[string]any
		if err := json.Unmarshal(body, &reply); err != nil {
			t.Fatalf("decode reply: %v body %s", err, body)
		}
		return status, reply
	}
	allowed := func(reply map[string]any) bool {
		d, ok := reply["data"].(map[string]any)
		if !ok {
			return false
		}
		if flags, ok := d["flags"].(float64); ok && int(flags) == 64 {
			return false
		}
		content, _ := d["content"].(string)
		return content != "" && content != "You do not have permission to use this command."
	}

	if status, reply := invoke(alice.ID, "moderate"); status != 200 || !allowed(reply) {
		t.Fatalf("owner invocation denied: %d %+v", status, reply)
	}
	if _, reply := invoke(charlie.ID, "moderate"); !allowed(reply) {
		t.Fatalf("role-holder invocation denied: %+v", reply)
	}
	if _, reply := invoke(bob.ID, "moderate"); allowed(reply) {
		t.Fatalf("explicit user deny ignored: %+v", reply)
	}
	if _, reply := invoke(dave.ID, "moderate"); allowed(reply) {
		t.Fatalf("allowlist with no match allowed: %+v", reply)
	}

	// A channel deny blocks even a role allow.
	if status := h.decode("PUT", "/applications/"+app.ID+"/guilds/"+guild.ID+"/commands/"+moderateID+"/permissions", alice.Token,
		map[string]any{"permissions": []map[string]any{
			{"id": role.ID, "type": 1, "permission": true},
			{"id": channel.ID, "type": 3, "permission": false},
		}}, nil); status != 200 {
		t.Fatalf("channel deny status %d", status)
	}
	if _, reply := invoke(charlie.ID, "moderate"); allowed(reply) {
		t.Fatalf("channel deny ignored: %+v", reply)
	}
	if _, reply := invoke(alice.ID, "moderate"); !allowed(reply) {
		t.Fatalf("owner should bypass channel deny: %+v", reply)
	}

	// default_member_permissions applies when no overrides exist.
	if _, reply := invoke(charlie.ID, "adminonly"); allowed(reply) {
		t.Fatalf("default_member_permissions ignored: %+v", reply)
	}
	if _, reply := invoke(alice.ID, "adminonly"); !allowed(reply) {
		t.Fatalf("owner denied by default permissions: %+v", reply)
	}
}
