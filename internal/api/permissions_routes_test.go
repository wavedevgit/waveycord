package api_test

import (
	"strconv"
	"testing"

	"github.com/wavedevgit/waveycord/internal/perm"
)

func TestChannelPermissionRoutesAndRoleFetch(t *testing.T) {
	h := newHarness(t)
	alice := h.register("permrouteowner")
	bob := h.register("permroutebob")
	dave := h.register("permroutedave")
	var guild struct{ ID string }
	h.decode("POST", "/guilds", alice.Token, map[string]any{"name": "Perm Routes"}, &guild)
	var channel struct{ ID string }
	h.decode("POST", "/guilds/"+guild.ID+"/channels", alice.Token, map[string]any{"name": "chat"}, &channel)
	var role struct {
		ID          string `json:"id"`
		Permissions string `json:"permissions"`
	}
	h.decode("POST", "/guilds/"+guild.ID+"/roles", alice.Token, map[string]any{
		"name":        "Mods",
		"permissions": strconv.FormatUint(perm.ManageMessages|perm.SendMessages, 10),
	}, &role)
	var invite struct{ Code string }
	h.decode("POST", "/channels/"+channel.ID+"/invites", alice.Token, map[string]any{}, &invite)
	h.request("POST", "/invites/"+invite.Code, bob.Token, nil)
	if status, _ := h.request("PUT", "/guilds/"+guild.ID+"/members/"+bob.ID+"/roles/"+role.ID, alice.Token, nil); status != 204 {
		t.Fatalf("role assign %d", status)
	}
	// Deny SendMessages for the role on the channel.
	if status, _ := h.request("PUT", "/channels/"+channel.ID+"/permissions/"+role.ID, alice.Token, map[string]any{
		"type": 0, "allow": "0", "deny": strconv.FormatUint(perm.SendMessages, 10),
	}); status != 204 {
		t.Fatalf("overwrite %d", status)
	}

	parsePerms := func(raw string) uint64 {
		v, err := perm.Parse(raw)
		if err != nil {
			t.Fatalf("parse perms %q: %v", raw, err)
		}
		return v
	}

	var member struct {
		Permissions string `json:"permissions"`
	}
	if status := h.decode("GET", "/channels/"+channel.ID+"/permissions/member/"+bob.ID, alice.Token, nil, &member); status != 200 {
		t.Fatalf("member permissions %d", status)
	}
	got := parsePerms(member.Permissions)
	if !perm.Has(got, perm.ManageMessages) || perm.Has(got, perm.SendMessages) {
		t.Fatalf("member permissions = %d", got)
	}

	var rolePerms struct {
		Permissions string `json:"permissions"`
	}
	if status := h.decode("GET", "/channels/"+channel.ID+"/permissions/role/"+role.ID, alice.Token, nil, &rolePerms); status != 200 {
		t.Fatalf("role permissions %d", status)
	}
	rp := parsePerms(rolePerms.Permissions)
	if !perm.Has(rp, perm.ManageMessages) || perm.Has(rp, perm.SendMessages) {
		t.Fatalf("role permissions = %d", rp)
	}

	var fetched struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	}
	if status := h.decode("GET", "/guilds/"+guild.ID+"/roles/"+role.ID, alice.Token, nil, &fetched); status != 200 || fetched.Name != "Mods" {
		t.Fatalf("get role %d %+v", status, fetched)
	}

	// Administrator role resolves to all permissions.
	var adminRole struct{ ID string }
	h.decode("POST", "/guilds/"+guild.ID+"/roles", alice.Token, map[string]any{
		"name": "Admin", "permissions": strconv.FormatUint(perm.Administrator, 10),
	}, &adminRole)
	var adminPerms struct {
		Permissions string `json:"permissions"`
	}
	h.decode("GET", "/channels/"+channel.ID+"/permissions/role/"+adminRole.ID, alice.Token, nil, &adminPerms)
	if parsePerms(adminPerms.Permissions) != perm.AllPermissions {
		t.Fatalf("admin role perms = %s", adminPerms.Permissions)
	}

	// Authorization and not-found paths.
	if status, _ := h.request("GET", "/channels/"+channel.ID+"/permissions/member/"+alice.ID, bob.Token, nil); status != 403 {
		t.Fatalf("non-manager member route %d", status)
	}
	if status, _ := h.request("GET", "/channels/"+channel.ID+"/permissions/member/"+dave.ID, alice.Token, nil); status != 404 {
		t.Fatalf("non-member target %d", status)
	}
	if status, _ := h.request("GET", "/channels/"+channel.ID+"/permissions/role/999", alice.Token, nil); status != 404 {
		t.Fatalf("unknown role %d", status)
	}
	if status, _ := h.request("GET", "/guilds/"+guild.ID+"/roles/999", alice.Token, nil); status != 404 {
		t.Fatalf("get unknown role %d", status)
	}
}
