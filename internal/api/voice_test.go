package api_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
)

func TestVoiceSignalingAndStatus(t *testing.T) {
	h := newHarness(t)
	alice := h.register("voiceowner")
	bob := h.register("voicemember")
	var guild struct{ ID string }
	h.decode("POST", "/guilds", alice.Token, map[string]any{"name": "Voice Guild"}, &guild)
	var text, voice struct{ ID string }
	h.decode("POST", "/guilds/"+guild.ID+"/channels", alice.Token, map[string]any{"name": "text"}, &text)
	h.decode("POST", "/guilds/"+guild.ID+"/channels", alice.Token, map[string]any{"name": "voice", "type": 2}, &voice)
	var invite struct{ Code string }
	h.decode("POST", "/channels/"+text.ID+"/invites", alice.Token, map[string]any{}, &invite)
	h.request("POST", "/invites/"+invite.Code, bob.Token, nil)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	conn, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(h.server.URL, "http")+"/gateway", nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.CloseNow()
	deadline := time.Now().Add(15 * time.Second)
	if hello := readFrame(t, conn, deadline); hello["op"].(float64) != 10 {
		t.Fatalf("expected HELLO: %+v", hello)
	}
	writeFrame(t, conn, map[string]any{"op": 2, "d": map[string]any{"token": bob.Token, "intents": 0}})
	if ready := readFrame(t, conn, deadline); ready["t"] != "READY" {
		t.Fatalf("expected READY: %+v", ready)
	}
	// Activate the guild so voice dispatches arrive live rather than through
	// PASSIVE_UPDATE_V2.
	writeFrame(t, conn, map[string]any{"op": 14, "d": map[string]any{"guild_id": guild.ID}})
	time.Sleep(50 * time.Millisecond)

	writeFrame(t, conn, map[string]any{"op": 4, "d": map[string]any{
		"guild_id": guild.ID, "channel_id": voice.ID, "self_mute": true, "self_deaf": true,
	}})
	var state, server map[string]any
	for i := 0; i < 10 && (state == nil || server == nil); i++ {
		f := readFrame(t, conn, deadline)
		switch f["t"] {
		case "VOICE_STATE_UPDATE":
			state = f["d"].(map[string]any)
		case "VOICE_SERVER_UPDATE":
			server = f["d"].(map[string]any)
		}
	}
	if state == nil || server == nil {
		t.Fatalf("missing voice dispatches: state=%+v server=%+v", state, server)
	}
	if state["channel_id"] != voice.ID || state["user_id"] != bob.ID || state["self_mute"] != true || state["guild_id"] != guild.ID {
		t.Fatalf("unexpected voice state %+v", state)
	}
	if sid, _ := state["session_id"].(string); sid == "" {
		t.Fatalf("voice state missing session_id: %+v", state)
	}
	if server["guild_id"] != guild.ID || server["channel_id"] != voice.ID ||
		server["endpoint"] != "wss://voice.test" || server["token"] == "" {
		t.Fatalf("unexpected voice server update %+v", server)
	}

	// Voice status is only available while connected.
	if status, _ := h.request("PATCH", "/channels/"+voice.ID+"/voice-status", bob.Token,
		map[string]any{"status": "pairing"}); status != 204 {
		t.Fatalf("patch voice status %d", status)
	}
	var vs struct {
		Status *string `json:"status"`
	}
	if status := h.decode("GET", "/channels/"+voice.ID+"/voice-status", bob.Token, nil, &vs); status != 200 || vs.Status == nil || *vs.Status != "pairing" {
		t.Fatalf("get voice status %d %+v", status, vs)
	}
	if status, _ := h.request("PATCH", "/channels/"+voice.ID+"/voice-status", bob.Token,
		map[string]any{"status": strings.Repeat("x", 141)}); status != 400 {
		t.Fatalf("long voice status %d", status)
	}

	// A fresh connection sees the persisted voice state in READY.
	aliceConn, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(h.server.URL, "http")+"/gateway", nil)
	if err != nil {
		t.Fatalf("dial alice: %v", err)
	}
	defer aliceConn.CloseNow()
	deadline2 := time.Now().Add(15 * time.Second)
	readFrame(t, aliceConn, deadline2)
	writeFrame(t, aliceConn, map[string]any{"op": 2, "d": map[string]any{"token": alice.Token, "intents": 0}})
	ready := readFrame(t, aliceConn, deadline2)
	if ready["t"] != "READY" {
		t.Fatalf("alice expected READY: %+v", ready)
	}
	guilds := ready["d"].(map[string]any)["guilds"].([]any)
	voiceStates := guilds[0].(map[string]any)["voice_states"].([]any)
	if len(voiceStates) != 1 || voiceStates[0].(map[string]any)["user_id"] != bob.ID {
		t.Fatalf("READY voice_states = %+v", voiceStates)
	}

	// Leaving clears the state and the status endpoint.
	writeFrame(t, conn, map[string]any{"op": 4, "d": map[string]any{
		"guild_id": guild.ID, "channel_id": nil,
	}})
	left := false
	for i := 0; i < 10 && !left; i++ {
		f := readFrame(t, conn, deadline)
		if f["t"] != "VOICE_STATE_UPDATE" {
			continue
		}
		d := f["d"].(map[string]any)
		if d["channel_id"] == nil && d["user_id"] == bob.ID {
			left = true
		}
	}
	if !left {
		t.Fatal("no VOICE_STATE_UPDATE with null channel after leaving")
	}
	if status, _ := h.request("GET", "/channels/"+voice.ID+"/voice-status", bob.Token, nil); status != 404 {
		t.Fatalf("voice status after leaving = %d", status)
	}

	// Joining a text channel is rejected with the voice close code.
	badConn, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(h.server.URL, "http")+"/gateway", nil)
	if err != nil {
		t.Fatalf("dial bad: %v", err)
	}
	defer badConn.CloseNow()
	readFrame(t, badConn, deadline2)
	writeFrame(t, badConn, map[string]any{"op": 2, "d": map[string]any{"token": bob.Token, "intents": 0}})
	readFrame(t, badConn, deadline2)
	writeFrame(t, badConn, map[string]any{"op": 4, "d": map[string]any{"guild_id": guild.ID, "channel_id": text.ID}})
	readCtx, rcancel := context.WithTimeout(ctx, 5*time.Second)
	defer rcancel()
	if _, _, err := badConn.Read(readCtx); err == nil {
		t.Fatal("expected the connection to close on a non-voice channel")
	}
}
