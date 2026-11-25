package api_test

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"strconv"
	"testing"
	"time"
)

func signedInteraction(t *testing.T, h *harness, key ed25519.PrivateKey, ts int64, payload any) (int, []byte) {
	t.Helper()
	body, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	sig := hex.EncodeToString(ed25519.Sign(key, append([]byte(strconv.FormatInt(ts, 10)), body...)))
	req, err := http.NewRequest("POST", h.server.URL+"/api/v10/interactions", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Signature-Timestamp", strconv.FormatInt(ts, 10))
	req.Header.Set("X-Signature-Ed25519", sig)
	resp, err := h.server.Client().Do(req)
	if err != nil {
		t.Fatalf("do: %v", err)
	}
	defer resp.Body.Close()
	out := new(bytes.Buffer)
	_, _ = out.ReadFrom(resp.Body)
	return resp.StatusCode, out.Bytes()
}

func TestInteractionsSignatureCommandsAndDispatch(t *testing.T) {
	h := newHarness(t)
	alice := h.register("interactiondev")
	bob := h.register("interactionother")
	var guild struct{ ID string }
	h.decode("POST", "/guilds", alice.Token, map[string]any{"name": "Interactions"}, &guild)

	var app struct {
		ID        string `json:"id"`
		VerifyKey string `json:"verify_key"`
	}
	if status := h.decode("POST", "/applications", alice.Token, map[string]any{"name": "Interaction App"}, &app); status != 201 {
		t.Fatalf("create app status %d", status)
	}

	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	if status := h.decode("PATCH", "/applications/"+app.ID, alice.Token, map[string]any{
		"verify_key": hex.EncodeToString(pub),
	}, nil); status != 200 {
		t.Fatalf("register verify key status %d", status)
	}

	var commands []struct {
		ID   string `json:"id"`
		Name string `json:"name"`
		Type int    `json:"type"`
	}
	if status := h.decode("PUT", "/applications/"+app.ID+"/commands", alice.Token, []map[string]any{
		{"name": "hello", "description": "Say hello", "type": 1},
		{"name": "echo", "description": "Repeat text", "type": 1, "options": []map[string]any{
			{"name": "text", "description": "Text to repeat", "type": 3, "required": true},
		}},
	}, &commands); status != 200 || len(commands) != 2 {
		t.Fatalf("bulk commands status %d %+v", status, commands)
	}
	var listed []struct{ ID string }
	if status := h.decode("GET", "/applications/"+app.ID+"/commands", alice.Token, nil, &listed); status != 200 || len(listed) != 2 {
		t.Fatalf("list commands status %d %+v", status, listed)
	}
	var one struct{ ID, Name string }
	if status := h.decode("GET", "/applications/"+app.ID+"/commands/"+commands[0].ID, alice.Token, nil, &one); status != 200 || one.Name != commands[0].Name {
		t.Fatalf("get command status %d %+v", status, one)
	}

	now := time.Now().Unix()
	// PING must be answered with PONG.
	status, body := signedInteraction(t, h, priv, now, map[string]any{
		"id": "1", "type": 1, "application_id": app.ID, "token": "ping-token",
	})
	if status != 200 {
		t.Fatalf("ping status %d body %s", status, body)
	}
	var pong struct{ Type int }
	if err := json.Unmarshal(body, &pong); err != nil || pong.Type != 1 {
		t.Fatalf("pong payload %s", body)
	}

	// Invalid signature, missing headers and stale timestamps are rejected.
	badReq, _ := http.NewRequest("POST", h.server.URL+"/api/v10/interactions",
		bytes.NewReader([]byte(`{"type":1,"application_id":"`+app.ID+`"}`)))
	badReq.Header.Set("Content-Type", "application/json")
	badReq.Header.Set("X-Signature-Timestamp", strconv.FormatInt(now, 10))
	badReq.Header.Set("X-Signature-Ed25519", hex.EncodeToString(make([]byte, ed25519.SignatureSize)))
	resp, err := h.server.Client().Do(badReq)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 401 {
		t.Fatalf("bad signature status %d", resp.StatusCode)
	}
	if status, _ := h.request("POST", "/interactions", "", map[string]any{"type": 1, "application_id": app.ID}); status != 401 {
		t.Fatalf("unsigned status %d", status)
	}
	if status, _ := signedInteraction(t, h, priv, now-3600, map[string]any{
		"id": "1", "type": 1, "application_id": app.ID,
	}); status != 401 {
		t.Fatalf("stale timestamp status %d", status)
	}

	// Command invocation dispatches to the registered handler.
	status, body = signedInteraction(t, h, priv, now, map[string]any{
		"id": "2", "type": 2, "application_id": app.ID, "guild_id": guild.ID,
		"channel_id": "999", "token": "cmd-token",
		"data": map[string]any{"name": "hello", "type": 1},
	})
	if status != 200 {
		t.Fatalf("invoke hello status %d body %s", status, body)
	}
	var reply struct {
		Type int `json:"type"`
		Data struct {
			Content string `json:"content"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &reply); err != nil {
		t.Fatalf("decode reply: %v", err)
	}
	if reply.Type != 4 || reply.Data.Content != "Hello from hello!" {
		t.Fatalf("unexpected invocation reply %s", body)
	}

	status, body = signedInteraction(t, h, priv, now, map[string]any{
		"id": "3", "type": 2, "application_id": app.ID, "guild_id": guild.ID,
		"data": map[string]any{"name": "echo", "type": 1, "options": []map[string]any{
			{"name": "text", "type": 3, "value": "echoed text"},
		}},
	})
	if status != 200 {
		t.Fatalf("invoke echo status %d body %s", status, body)
	}
	if err := json.Unmarshal(body, &reply); err != nil || reply.Data.Content != "echoed text" {
		t.Fatalf("unexpected echo reply %s", body)
	}

	status, body = signedInteraction(t, h, priv, now, map[string]any{
		"id": "4", "type": 2, "application_id": app.ID, "guild_id": guild.ID,
		"data": map[string]any{"name": "missing", "type": 1},
	})
	if status != 200 {
		t.Fatalf("unknown command status %d", status)
	}
	var unknown struct {
		Type int `json:"type"`
		Data struct {
			Flags int `json:"flags"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &unknown); err != nil || unknown.Data.Flags != 64 {
		t.Fatalf("unknown command reply %s", body)
	}

	// Guild-scoped commands shadow and coexist with global ones.
	var guildCommands []struct{ ID, Name string }
	if status := h.decode("PUT", "/applications/"+app.ID+"/guilds/"+guild.ID+"/commands", alice.Token,
		[]map[string]any{{"name": "local", "description": "Guild only", "type": 1}}, &guildCommands); status != 200 || len(guildCommands) != 1 {
		t.Fatalf("guild commands status %d %+v", status, guildCommands)
	}
	status, body = signedInteraction(t, h, priv, now, map[string]any{
		"id": "5", "type": 2, "application_id": app.ID, "guild_id": guild.ID,
		"data": map[string]any{"name": "local", "type": 1},
	})
	if status != 200 || !bytes.Contains(body, []byte("Command `local` executed.")) {
		t.Fatalf("guild command reply status %d %s", status, body)
	}

	// Only the owner or bot may manage commands.
	if status, _ := h.request("PUT", "/applications/"+app.ID+"/commands", bob.Token,
		[]map[string]any{{"name": "stolen", "description": "x", "type": 1}}); status != 403 {
		t.Fatalf("foreign command registration status %d", status)
	}
	if status, _ := h.request("PUT", "/applications/"+app.ID+"/commands", alice.Token,
		[]map[string]any{{"name": "Bad Name!", "description": "x", "type": 1}}); status != 400 {
		t.Fatalf("invalid command name status %d", status)
	}
}
