package api_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/klauspost/compress/zstd"
)

// TestPassiveUpdateV2Delta verifies that a passively subscribed guild receives
// only the entity that changed, in a PASSIVE_UPDATE_V2 dispatch, and that OP 14
// promotes the guild to live dispatch.
func TestPassiveUpdateV2Delta(t *testing.T) {
	h := newHarness(t)
	h.hub.SetPassiveInterval(40 * time.Millisecond)

	alice := h.register("ownerpassive")
	bob := h.register("bobpassive")
	var guild struct{ ID string }
	h.decode("POST", "/guilds", alice.Token, map[string]any{"name": "Passive Guild"}, &guild)
	var channel struct{ ID string }
	h.decode("POST", "/guilds/"+guild.ID+"/channels", alice.Token, map[string]any{"name": "general"}, &channel)
	var invite struct{ Code string }
	h.decode("POST", "/channels/"+channel.ID+"/invites", alice.Token, map[string]any{}, &invite)
	if status := h.decode("POST", "/invites/"+invite.Code, bob.Token, nil, nil); status != 200 {
		t.Fatalf("join status %d", status)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	url := "ws" + strings.TrimPrefix(h.server.URL, "http") + "/gateway?compress=zstd-stream"
	conn, _, err := websocket.Dial(ctx, url, nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.CloseNow()

	// Decode the zstd stream the same way a real client would.
	dec, pw := newWSZstdReader(ctx, t, conn)
	defer dec.Close()
	defer pw.Close()
	defer conn.CloseNow()
	frames := json.NewDecoder(dec)
	next := func() map[string]any {
		t.Helper()
		var f map[string]any
		if err := frames.Decode(&f); err != nil {
			t.Fatalf("decode frame: %v", err)
		}
		return f
	}

	if f := next(); f["op"].(float64) != 10 {
		t.Fatalf("expected HELLO, got %+v", f)
	}
	identify, _ := json.Marshal(map[string]any{"op": 2, "d": map[string]any{
		"token": bob.Token, "intents": 0, "properties": map[string]any{},
	}})
	if err := conn.Write(ctx, websocket.MessageText, identify); err != nil {
		t.Fatalf("identify: %v", err)
	}
	if f := next(); f["t"] != "READY" {
		t.Fatalf("expected READY, got %+v", f)
	}

	// A nickname change must reach the passive session as a delta containing one
	// member and no channel or voice entries.
	if status, _ := h.request("PATCH", "/guilds/"+guild.ID+"/members/"+bob.ID, alice.Token,
		map[string]any{"nick": "Bobby"}); status != 200 {
		t.Fatalf("nick status %d", status)
	}
	var delta map[string]any
	sawLiveMemberUpdate := false
	for i := 0; i < 20; i++ {
		f := next()
		if f["t"] == "GUILD_MEMBER_UPDATE" {
			sawLiveMemberUpdate = true
		}
		if f["t"] == "PASSIVE_UPDATE_V2" {
			delta = f["d"].(map[string]any)
			break
		}
	}
	if delta == nil {
		t.Fatal("no PASSIVE_UPDATE_V2 received")
	}
	if sawLiveMemberUpdate {
		t.Fatal("passive guild received the live GUILD_MEMBER_UPDATE firehose")
	}
	if delta["guild_id"] != guild.ID {
		t.Fatalf("guild_id = %v", delta["guild_id"])
	}
	members := delta["members"].([]any)
	channels := delta["channels"].([]any)
	voice := delta["voice_states"].([]any)
	if len(members) != 1 || len(channels) != 0 || len(voice) != 0 {
		t.Fatalf("delta not minimal: members=%d channels=%d voice=%d", len(members), len(channels), len(voice))
	}
	m := members[0].(map[string]any)
	if m["nick"] != "Bobby" {
		t.Fatalf("member nick = %v", m["nick"])
	}
	user := m["user"].(map[string]any)
	if user["id"] != bob.ID {
		t.Fatalf("member user id = %v", user["id"])
	}
	if _, hasFull := delta["full"]; hasFull {
		t.Fatalf("unexpected full update: %+v", delta)
	}

	// OP 14 promotes the guild: the next nickname change arrives live.
	sub, _ := json.Marshal(map[string]any{"op": 14, "d": map[string]any{
		"guild_id": guild.ID, "channels": map[string][][2]int{},
	}})
	if err := conn.Write(ctx, websocket.MessageText, sub); err != nil {
		t.Fatalf("guild subscribe: %v", err)
	}
	time.Sleep(80 * time.Millisecond)
	if status, _ := h.request("PATCH", "/guilds/"+guild.ID+"/members/"+bob.ID, alice.Token,
		map[string]any{"nick": "Robert"}); status != 200 {
		t.Fatalf("second nick status %d", status)
	}
	for i := 0; i < 20; i++ {
		f := next()
		switch f["t"] {
		case "GUILD_MEMBER_UPDATE":
			m := f["d"].(map[string]any)
			if m["nick"] != "Robert" {
				t.Fatalf("live member nick = %v", m["nick"])
			}
			return
		case "PASSIVE_UPDATE_V2":
			t.Fatalf("still passive after OP 14: %+v", f["d"])
		}
	}
	t.Fatal("live GUILD_MEMBER_UPDATE not received after activation")
}

// newWSZstdReader feeds binary websocket frames into a streaming zstd decoder.
func newWSZstdReader(ctx context.Context, t *testing.T, conn *websocket.Conn) (*zstd.Decoder, *io.PipeWriter) {
	t.Helper()
	pr, pw := io.Pipe()
	go func() {
		for {
			typ, data, err := conn.Read(ctx)
			if err != nil {
				_ = pw.CloseWithError(err)
				return
			}
			if typ != websocket.MessageBinary {
				_ = pw.CloseWithError(errors.New("expected binary frame"))
				return
			}
			if _, err := pw.Write(data); err != nil {
				return
			}
		}
	}()
	dec, err := zstd.NewReader(pr)
	if err != nil {
		t.Fatalf("zstd reader: %v", err)
	}
	return dec, pw
}
