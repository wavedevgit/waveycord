package gateway

import (
	"encoding/json"
	"sync"

	"github.com/wavedevgit/waveycord/internal/model"
)

// deltaJournal keeps a bounded, ordered log of guild entity changes so passive
// sessions can receive only what changed since their last update. It is the
// in-process equivalent of the delta that the realtime post describes replacing
// full PASSIVE_UPDATE_V1 snapshots.
//
// The journal also keeps the latest raw entity per id: if a connection falls
// behind the bounded log (or an entity was removed), the next update is marked
// full and carries the current entity set instead of a delta.
type deltaJournal struct {
	mu     sync.Mutex
	guilds map[string]*guildDelta
	limit  int
}

type deltaEvent struct {
	seq  uint64
	kind string // channel, member, voice
	id   string
	data json.RawMessage
}

type guildDelta struct {
	seq      uint64
	oldest   uint64
	events   []deltaEvent
	channels map[string]json.RawMessage
	members  map[string]json.RawMessage
	voice    map[string]json.RawMessage
}

func newDeltaJournal(limit int) *deltaJournal {
	if limit <= 0 {
		limit = 2048
	}
	return &deltaJournal{guilds: map[string]*guildDelta{}, limit: limit}
}

func (j *deltaJournal) guild(guildID string) *guildDelta {
	g := j.guilds[guildID]
	if g == nil {
		g = &guildDelta{
			channels: map[string]json.RawMessage{},
			members:  map[string]json.RawMessage{},
			voice:    map[string]json.RawMessage{},
		}
		j.guilds[guildID] = g
	}
	return g
}

// head returns the current sequence for a guild, used as a cursor at identify.
func (j *deltaJournal) head(guildID string) uint64 {
	j.mu.Lock()
	defer j.mu.Unlock()
	return j.guild(guildID).seq
}

// record stores one changed entity. data is the dispatch fragment that carried
// it, so passive updates reuse the same object shape as live dispatches.
func (j *deltaJournal) record(guildID, kind, id string, data json.RawMessage) {
	if guildID == "" || id == "" || len(data) == 0 {
		return
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	g := j.guild(guildID)
	g.seq++
	g.events = append(g.events, deltaEvent{seq: g.seq, kind: kind, id: id, data: data})
	switch kind {
	case "channel":
		g.channels[id] = data
	case "member":
		g.members[id] = data
	case "voice":
		g.voice[id] = data
	}
	if len(g.events) > j.limit {
		drop := len(g.events) - j.limit
		g.oldest = g.events[drop-1].seq + 1
		g.events = append([]deltaEvent(nil), g.events[drop:]...)
	}
}

// remove forgets an entity and marks the guild as needing a full update, because
// the delta protocol has no explicit removal entry here.
func (j *deltaJournal) remove(guildID, kind, id string) {
	j.mu.Lock()
	defer j.mu.Unlock()
	g := j.guild(guildID)
	switch kind {
	case "channel":
		delete(g.channels, id)
	case "member":
		delete(g.members, id)
	case "voice":
		delete(g.voice, id)
	}
	// Force the next passive update to carry the current entity set.
	g.events = append(g.events, deltaEvent{seq: g.seq})
	g.seq++
	g.oldest = g.seq
}

// since returns the entities changed after cursor for one guild.
func (j *deltaJournal) since(guildID string, cursor uint64) (channels, members, voice []json.RawMessage, full bool, head uint64) {
	j.mu.Lock()
	defer j.mu.Unlock()
	g := j.guild(guildID)
	head = g.seq
	if cursor < g.oldest {
		full = true
	} else {
		seenC := map[string]bool{}
		seenM := map[string]bool{}
		seenV := map[string]bool{}
		for _, ev := range g.events {
			if ev.seq <= cursor || len(ev.data) == 0 {
				continue
			}
			switch ev.kind {
			case "channel":
				if !seenC[ev.id] {
					seenC[ev.id] = true
					channels = append(channels, ev.data)
				}
			case "member":
				if !seenM[ev.id] {
					seenM[ev.id] = true
					members = append(members, ev.data)
				}
			case "voice":
				if !seenV[ev.id] {
					seenV[ev.id] = true
					voice = append(voice, ev.data)
				}
			}
		}
	}
	if full {
		for _, raw := range g.channels {
			channels = append(channels, raw)
		}
		for _, raw := range g.members {
			members = append(members, raw)
		}
		for _, raw := range g.voice {
			voice = append(voice, raw)
		}
	}
	if channels == nil {
		channels = []json.RawMessage{}
	}
	if members == nil {
		members = []json.RawMessage{}
	}
	if voice == nil {
		voice = []json.RawMessage{}
	}
	return channels, members, voice, full, head
}

// passiveEvent turns a live dispatch into a journal entry. Returns false for
// events that are not entity deltas.
func passiveEvent(event string, data any) (kind, id string, ok bool) {
	raw, err := json.Marshal(data)
	if err != nil {
		return "", "", false
	}
	switch event {
	case "CHANNEL_CREATE", "CHANNEL_UPDATE":
		var c model.Channel
		if json.Unmarshal(raw, &c) == nil && c.ID != "" {
			return "channel", c.ID, true
		}
	case "GUILD_MEMBER_ADD", "GUILD_MEMBER_UPDATE":
		var m model.Member
		if json.Unmarshal(raw, &m) == nil && m.User != nil {
			return "member", m.User.ID, true
		}
	case "VOICE_STATE_UPDATE":
		var v struct {
			UserID string `json:"user_id"`
		}
		if json.Unmarshal(raw, &v) == nil && v.UserID != "" {
			return "voice", v.UserID, true
		}
	}
	return "", "", false
}

// passiveRemoval reports entity removals that force a full update.
func passiveRemoval(event string, data any) (kind, id string, ok bool) {
	raw, err := json.Marshal(data)
	if err != nil {
		return "", "", false
	}
	switch event {
	case "CHANNEL_DELETE":
		var c model.Channel
		if json.Unmarshal(raw, &c) == nil && c.ID != "" {
			return "channel", c.ID, true
		}
	case "GUILD_MEMBER_REMOVE":
		var v struct {
			User struct{ ID string } `json:"user"`
		}
		if json.Unmarshal(raw, &v) == nil && v.User.ID != "" {
			return "member", v.User.ID, true
		}
	case "VOICE_STATE_UPDATE":
		// A null voice state means the user left voice; handled by record with
		// the same id, so no forced full update.
	}
	return "", "", false
}
