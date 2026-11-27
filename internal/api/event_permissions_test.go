package api_test

import (
	"strconv"
	"testing"
	"time"

	"github.com/wavedevgit/waveycord/internal/perm"
)

func TestScheduledEventPermissions(t *testing.T) {
	h := newHarness(t)
	owner, member := h.register("eventpermowner"), h.register("eventpermmember")
	var guild, text struct{ ID string }
	h.decode("POST", "/guilds", owner.Token, map[string]any{"name": "Event Permissions"}, &guild)
	h.decode("POST", "/guilds/"+guild.ID+"/channels", owner.Token, map[string]any{"name": "chat"}, &text)
	var invite struct{ Code string }
	h.decode("POST", "/channels/"+text.ID+"/invites", owner.Token, map[string]any{}, &invite)
	h.request("POST", "/invites/"+invite.Code, member.Token, nil)
	start, end := time.Now().Add(time.Hour).UTC().Format(time.RFC3339), time.Now().Add(2*time.Hour).UTC().Format(time.RFC3339)
	body := map[string]any{"name": "Member Event", "entity_type": 3, "scheduled_start_time": start, "scheduled_end_time": end, "entity_metadata": map[string]any{"location": "Town Hall"}}
	path := "/guilds/" + guild.ID + "/scheduled-events"
	if status, _ := h.request("POST", path, member.Token, body); status != 403 {
		t.Fatalf("unprivileged create %d", status)
	}
	var role struct{ ID string }
	h.decode("POST", "/guilds/"+guild.ID+"/roles", owner.Token, map[string]any{"name": "Event creators", "permissions": strconv.FormatUint(perm.CreateEvents, 10)}, &role)
	h.request("PUT", "/guilds/"+guild.ID+"/members/"+member.ID+"/roles/"+role.ID, owner.Token, nil)
	var event struct{ ID string }
	if status := h.decode("POST", path, member.Token, body, &event); status != 201 {
		t.Fatalf("creator permission rejected %d", status)
	}
	if status, _ := h.request("PATCH", path+"/"+event.ID, member.Token, map[string]any{"name": "Updated by creator"}); status != 200 {
		t.Fatalf("creator update %d", status)
	}
	var owned struct{ ID string }
	h.decode("POST", path, owner.Token, body, &owned)
	if status, _ := h.request("PATCH", path+"/"+owned.ID, member.Token, map[string]any{"name": "Not mine"}); status != 403 {
		t.Fatalf("modified another creator's event %d", status)
	}
	if status, _ := h.request("DELETE", path+"/"+event.ID, member.Token, nil); status != 204 {
		t.Fatalf("creator delete %d", status)
	}
}
