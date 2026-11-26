package api_test

import (
	"context"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
)

// TestVoiceHostOnlyEndpoint proves VOICE_SERVER_UPDATE advertises a host-only
// endpoint (as Discord does) and that a client can still connect by appending
// "/?token=..." to it.
func TestVoiceHostOnlyEndpoint(t *testing.T) {
	h := newHarness(t)
	alice := h.register("hostvoice")
	var guild struct{ ID string }
	h.decode("POST", "/guilds", alice.Token, map[string]any{"name": "Host Voice"}, &guild)
	var text, voice struct{ ID string }
	h.decode("POST", "/guilds/"+guild.ID+"/channels", alice.Token, map[string]any{"name": "text"}, &text)
	h.decode("POST", "/guilds/"+guild.ID+"/channels", alice.Token, map[string]any{"name": "voice", "type": 2}, &voice)

	base, err := url.Parse(h.server.URL)
	if err != nil {
		t.Fatal(err)
	}
	endpoint := "ws://" + base.Host
	h.hub.SetVoiceEndpoint(endpoint)

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	conn, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(h.server.URL, "http")+"/gateway", nil)
	if err != nil {
		t.Fatalf("dial gateway: %v", err)
	}
	defer conn.CloseNow()
	deadline := time.Now().Add(10 * time.Second)
	readFrame(t, conn, deadline)
	writeFrame(t, conn, map[string]any{"op": 2, "d": map[string]any{"token": alice.Token, "intents": 0}})
	readFrame(t, conn, deadline)
	writeFrame(t, conn, map[string]any{"op": 14, "d": map[string]any{"guild_id": guild.ID}})
	time.Sleep(30 * time.Millisecond)
	writeFrame(t, conn, map[string]any{"op": 4, "d": map[string]any{
		"guild_id": guild.ID, "channel_id": voice.ID,
	}})

	var advertised, token string
	for i := 0; i < 10 && token == ""; i++ {
		f := readFrame(t, conn, deadline)
		if f["t"] != "VOICE_SERVER_UPDATE" {
			continue
		}
		d := f["d"].(map[string]any)
		advertised, _ = d["endpoint"].(string)
		token, _ = d["token"].(string)
	}
	if token == "" {
		t.Fatal("no voice token")
	}
	parsed, err := url.Parse(advertised)
	if err != nil || parsed.Host == "" {
		t.Fatalf("bad endpoint %q", advertised)
	}
	if parsed.Path != "" && parsed.Path != "/" {
		t.Fatalf("endpoint is not host-only: %q", advertised)
	}

	// Connect using endpoint + "/?token=", exactly as a client would.
	voiceConn, _, err := websocket.Dial(ctx, strings.TrimRight(advertised, "/")+"/?token="+token, nil)
	if err != nil {
		t.Fatalf("dial root voice: %v", err)
	}
	defer voiceConn.CloseNow()
	readVoiceOp(t, voiceConn, 8, deadline)
	writeVoice(t, voiceConn, map[string]any{"op": 0, "d": map[string]any{
		"user_id": alice.ID, "token": token,
	}})
	ready := readVoiceOp(t, voiceConn, 2, deadline)
	d := ready["d"].(map[string]any)
	if uint32(d["ssrc"].(float64)) == 0 || d["ip"] == "" {
		t.Fatalf("voice ready incomplete: %+v", d)
	}
	if status, _ := h.request("GET", "/does-not-exist", "", nil); status != 404 {
		t.Fatalf("non-upgrade root request status %d", status)
	}
}
