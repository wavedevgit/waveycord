package api_test

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/wavedevgit/waveycord/internal/api"
	"github.com/wavedevgit/waveycord/internal/bus"
	"github.com/wavedevgit/waveycord/internal/db"
	"github.com/wavedevgit/waveycord/internal/gateway"
	"github.com/wavedevgit/waveycord/internal/search"
	"github.com/wavedevgit/waveycord/internal/store"
)

// newPGCluster builds two API instances sharing one Postgres database and one
// LISTEN/NOTIFY bus, which is the deployment shape clustering is meant for.
func newPGCluster(t *testing.T) (*harness, *harness) {
	t.Helper()
	dsn := os.Getenv("WAVEYCORD_TEST_DSN")
	if !strings.HasPrefix(dsn, "postgres") {
		t.Skip("set WAVEYCORD_TEST_DSN to a postgres URL to run")
	}
	seed, err := db.Open(dsn)
	if err != nil {
		t.Fatalf("open seed db: %v", err)
	}
	truncateAll(t, seed)
	seed.Close()

	cfg := api.DefaultConfig()
	cfg.RateLimit = 10000
	cfg.UploadDir = t.TempDir()

	makeInstance := func(label string) *harness {
		handle, err := db.Open(dsn)
		if err != nil {
			t.Fatalf("open %s db: %v", label, err)
		}
		st := store.New(handle)
		ix, err := search.Open(context.Background(), handle)
		if err != nil {
			t.Fatalf("search %s: %v", label, err)
		}
		st.SetSearchIndex(ix)
		hub := gateway.NewHub(
			func(token string) (string, bool) {
				_, user, err := st.SessionByToken(api.TokenHash(token))
				return user.ID, err == nil
			},
			api.ReadyBuilder(st, cfg.GatewayURL),
			api.GuildIDsFor(st),
			nil,
		)
		hub.SetStore(st, "wss://voice.test")
		origin := label + "-" + randomSuffix(t)
		pgBus, err := bus.NewPostgres(context.Background(), dsn, origin, hub.DeliverRemote, nil)
		if err != nil {
			t.Fatalf("bus %s: %v", label, err)
		}
		hub.SetBus(pgBus)
		server := api.New(st, hub, cfg)
		voice, err := gateway.NewVoiceServer(st, gateway.VoiceOptions{IP: "127.0.0.1"})
		if err != nil {
			t.Fatalf("voice %s: %v", label, err)
		}
		if os.Getenv("WAVEYCORD_CLUSTER_MIX") == "1" {
			voice.SetClusterIdentity("mix-"+label+"-"+origin, true)
		}
		server.SetVoice(voice)
		httpSrv := httptest.NewServer(server.Handler())
		t.Cleanup(func() {
			hub.Close()
			voice.Close()
			pgBus.Close()
			httpSrv.Close()
			handle.Close()
		})
		return &harness{t: t, server: httpSrv, store: st, hub: hub}
	}
	a := makeInstance("a")
	b := makeInstance("b")
	time.Sleep(300 * time.Millisecond) // let both listeners subscribe
	return a, b
}

func randomSuffix(t *testing.T) string {
	t.Helper()
	buf := make([]byte, 4)
	if _, err := rand.Read(buf); err != nil {
		t.Fatal(err)
	}
	return hex.EncodeToString(buf)
}

// TestClusterAutoModCounters proves spam and mention-raid windows aggregate across
// instances: the burst is split so neither instance alone crosses the threshold.
func TestClusterAutoModCounters(t *testing.T) {
	a, b := newPGCluster(t)
	alice := a.register("clusterautomod")
	var guild struct{ ID string }
	a.decode("POST", "/guilds", alice.Token, map[string]any{"name": "Cluster AutoMod"}, &guild)
	var channel struct{ ID string }
	a.decode("POST", "/guilds/"+guild.ID+"/channels", alice.Token, map[string]any{"name": "chat"}, &channel)
	a.decode("POST", "/guilds/"+guild.ID+"/auto-moderation/rules", alice.Token, map[string]any{
		"name": "Spam", "event_type": 1, "trigger_type": 2,
		"actions": []map[string]any{{"type": 1}},
	}, nil)

	post := func(h *harness, content string) int {
		status, _ := h.request("POST", "/channels/"+channel.ID+"/messages", alice.Token, map[string]any{"content": content})
		return status
	}
	// Three on A, two on B. Each instance alone sees fewer than the limit; with
	// the bus both should hold five, so the sixth message trips the rule.
	for i := 0; i < 3; i++ {
		if status := post(a, "burst"); status != 200 {
			t.Fatalf("a message %d status %d", i+1, status)
		}
	}
	time.Sleep(400 * time.Millisecond)
	for i := 0; i < 2; i++ {
		if status := post(b, "burst"); status != 200 {
			t.Fatalf("b message %d status %d", i+1, status)
		}
	}
	time.Sleep(400 * time.Millisecond)
	if status := post(a, "burst"); status != 400 {
		t.Fatalf("cross-instance sixth message status %d, want 400", status)
	}

	// Mention raid across a different guild, split across instances.
	var raidGuild struct{ ID string }
	a.decode("POST", "/guilds", alice.Token, map[string]any{"name": "Cluster Raid"}, &raidGuild)
	var raidChannel struct{ ID string }
	a.decode("POST", "/guilds/"+raidGuild.ID+"/channels", alice.Token, map[string]any{"name": "chat"}, &raidChannel)
	a.decode("POST", "/guilds/"+raidGuild.ID+"/auto-moderation/rules", alice.Token, map[string]any{
		"name": "Raid", "event_type": 1, "trigger_type": 6,
		"actions": []map[string]any{{"type": 1}},
	}, nil)

	mention := "hey <@" + alice.ID + "> and <@" + alice.ID + ">"
	raidPost := func(h *harness, content string) int {
		status, _ := h.request("POST", "/channels/"+raidChannel.ID+"/messages", alice.Token, map[string]any{"content": content})
		return status
	}
	for i := 0; i < 3; i++ {
		if status := raidPost(a, mention); status != 200 {
			t.Fatalf("raid a message %d status %d", i+1, status)
		}
	}
	time.Sleep(400 * time.Millisecond)
	for i := 0; i < 2; i++ {
		if status := raidPost(b, mention); status != 200 {
			t.Fatalf("raid b message %d status %d", i+1, status)
		}
	}
	time.Sleep(400 * time.Millisecond)
	if status := raidPost(a, mention); status != 400 {
		t.Fatalf("cross-instance raid burst status %d, want 400", status)
	}
}

// TestClusterVoiceSpeaking proves an OP 5 spoken on one instance reaches a voice
// connection on another instance through the bus.
func TestClusterVoiceSpeaking(t *testing.T) {
	a, b := newPGCluster(t)
	alice := a.register("clustervoicea")
	bob := a.register("clustervoiceb")
	var guild struct{ ID string }
	a.decode("POST", "/guilds", alice.Token, map[string]any{"name": "Cluster Voice"}, &guild)
	var text, voice struct{ ID string }
	a.decode("POST", "/guilds/"+guild.ID+"/channels", alice.Token, map[string]any{"name": "text"}, &text)
	a.decode("POST", "/guilds/"+guild.ID+"/channels", alice.Token, map[string]any{"name": "voice", "type": 2}, &voice)
	var invite struct{ Code string }
	a.decode("POST", "/channels/"+text.ID+"/invites", alice.Token, map[string]any{}, &invite)
	a.request("POST", "/invites/"+invite.Code, bob.Token, nil)

	aliceToken := a.joinVoice(alice, guild.ID, voice.ID)
	bobToken := b.joinVoice(bob, guild.ID, voice.ID)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	dialVoice := func(h *harness, token string) *websocket.Conn {
		conn, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(h.server.URL, "http")+"/voice?token="+token, nil)
		if err != nil {
			t.Fatalf("dial voice: %v", err)
		}
		t.Cleanup(func() { conn.CloseNow() })
		deadline := time.Now().Add(10 * time.Second)
		readVoiceOp(t, conn, 8, deadline)
		writeVoice(t, conn, map[string]any{"op": 0, "d": map[string]any{"server_id": guild.ID, "session_id": "s"}})
		readVoiceOp(t, conn, 2, deadline)
		return conn
	}
	aliceVoice := dialVoice(a, aliceToken)
	bobVoice := dialVoice(b, bobToken)

	// Alice speaks on instance A; Bob is connected to instance B.
	ssrc := uint32(4242)
	writeVoice(t, aliceVoice, map[string]any{"op": 5, "d": map[string]any{"speaking": 1, "delay": 0, "ssrc": ssrc}})
	deadline := time.Now().Add(10 * time.Second)
	for {
		f := readVoice(t, bobVoice, deadline)
		if f["op"].(float64) != 5 {
			continue
		}
		d := f["d"].(map[string]any)
		if int(d["speaking"].(float64)) != 1 || uint32(d["ssrc"].(float64)) != ssrc {
			t.Fatalf("unexpected speaking payload %+v", d)
		}
		return
	}
}
