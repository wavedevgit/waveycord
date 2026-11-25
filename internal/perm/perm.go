// Package perm implements Discord permission bitfields and the documented
// overwrite resolution order.
package perm

import (
	"fmt"
	"strconv"
)

// Permission bit values, as published in topics/permissions.
const (
	CreateInstantInvite uint64 = 1 << 0
	KickMembers         uint64 = 1 << 1
	BanMembers          uint64 = 1 << 2
	Administrator       uint64 = 1 << 3
	ManageChannels      uint64 = 1 << 4
	ManageGuild         uint64 = 1 << 5
	AddReactions        uint64 = 1 << 6
	ViewAuditLog        uint64 = 1 << 7
	PrioritySpeaker     uint64 = 1 << 8
	Stream              uint64 = 1 << 9
	ViewChannel         uint64 = 1 << 10
	SendMessages        uint64 = 1 << 11
	SendTTSMessages     uint64 = 1 << 12
	ManageMessages      uint64 = 1 << 13
	EmbedLinks          uint64 = 1 << 14
	AttachFiles         uint64 = 1 << 15
	ReadMessageHistory  uint64 = 1 << 16
	MentionEveryone     uint64 = 1 << 17
	UseExternalEmojis   uint64 = 1 << 18
	ViewGuildInsights   uint64 = 1 << 19
	Connect             uint64 = 1 << 20
	Speak               uint64 = 1 << 21
	MuteMembers         uint64 = 1 << 22
	DeafenMembers       uint64 = 1 << 23
	MoveMembers         uint64 = 1 << 24
	UseVAD              uint64 = 1 << 25
	ChangeNickname      uint64 = 1 << 26
	ManageNicknames     uint64 = 1 << 27
	ManageRoles         uint64 = 1 << 28
	ManageWebhooks      uint64 = 1 << 29
	ManageGuildExpressions uint64 = 1 << 30
	UseApplicationCommands uint64 = 1 << 31
	RequestToSpeak      uint64 = 1 << 32
	ManageEvents        uint64 = 1 << 33
	ManageThreads       uint64 = 1 << 34
	CreatePublicThreads uint64 = 1 << 35
	CreatePrivateThreads uint64 = 1 << 36
	UseExternalStickers uint64 = 1 << 37
	SendMessagesInThreads uint64 = 1 << 38
	UseEmbeddedActivities uint64 = 1 << 39
	ModerateMembers     uint64 = 1 << 40
	ViewCreatorMonetizationAnalytics uint64 = 1 << 41
	UseSoundboard       uint64 = 1 << 42
	CreateGuildExpressions uint64 = 1 << 43
	CreateEvents        uint64 = 1 << 44
	UseExternalSounds   uint64 = 1 << 45
	SendVoiceMessages   uint64 = 1 << 46
	SendPolls           uint64 = 1 << 49
	UseExternalApps     uint64 = 1 << 50

	AllPermissions uint64 = (1 << 51) - 1
	// AllGuildPermissions excludes the DM-only bits; Discord documents the same
	// mask for default role creation.
	AllGuildPermissions uint64 = AllPermissions &^ (ViewChannel | SendMessages | SendTTSMessages |
		ManageMessages | EmbedLinks | AttachFiles | ReadMessageHistory | MentionEveryone |
		UseExternalEmojis | AddReactions | UseApplicationCommands | CreatePublicThreads |
		CreatePrivateThreads | SendMessagesInThreads | UseExternalStickers)
)

// Parse reads a permission bitfield string. Discord sends them as decimal
// strings; an empty string is zero.
func Parse(s string) (uint64, error) {
	if s == "" {
		return 0, nil
	}
	v, err := strconv.ParseUint(s, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("perm: invalid bitfield %q", s)
	}
	return v, nil
}

// Format renders a bitfield the way the API does.
func Format(v uint64) string { return strconv.FormatUint(v, 10) }

// Has reports whether all bits in want are set in have.
func Has(have, want uint64) bool { return have&want == want }

// Overwrite is the pair of bitfields on a channel overwrite.
type Overwrite struct {
	ID    string
	Type  int // 0 role, 1 member
	Allow uint64
	Deny  uint64
}

// Resolve computes a member's channel permissions using the documented order:
// base role permissions, administrator short-circuit, @everyone overwrite, role
// overwrites (accumulated), member overwrite.
func Resolve(base uint64, everyoneRoleID string, roleIDs []string, memberID string, overwrites []Overwrite) uint64 {
	if base&Administrator != 0 {
		return AllPermissions
	}
	// @everyone overwrite.
	if ow, ok := find(overwrites, TypeRole, everyoneRoleID); ok {
		base &^= ow.Deny
		base |= ow.Allow
	}
	var allow, deny uint64
	for _, rid := range roleIDs {
		if ow, ok := find(overwrites, TypeRole, rid); ok {
			allow |= ow.Allow
			deny |= ow.Deny
		}
	}
	base &^= deny
	base |= allow
	// Member overwrite wins outright.
	if ow, ok := find(overwrites, TypeMember, memberID); ok {
		base &^= ow.Deny
		base |= ow.Allow
	}
	return base
}

const (
	TypeRole   = 0
	TypeMember = 1
)

func find(ows []Overwrite, typ int, id string) (Overwrite, bool) {
	for _, ow := range ows {
		if ow.Type == typ && ow.ID == id {
			return ow, true
		}
	}
	return Overwrite{}, false
}
