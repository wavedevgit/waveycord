package gateway

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/wavedevgit/waveycord/internal/bus"
	"github.com/wavedevgit/waveycord/internal/db"
)

// TestPostgresBusCrossInstanceDispatch proves that a dispatch published by one
// instance reaches a gateway connection held by another. It needs a Postgres DSN
// in WAVEYCORD_TEST_DSN and is skipped otherwise.
func TestPostgresBusCrossInstanceDispatch(t *testing.T) {
	dsn := os.Getenv("WAVEYCORD_TEST_DSN")
	if !strings.HasPrefix(dsn, "postgres") {
		t.Skip("set WAVEYCORD_TEST_DSN to a postgres URL to run")
	}
	handle, err := db.Open(dsn)
	if err != nil {
		t.Fatalf("schema: %v", err)
	}
	handle.Close()

	auth := func(token string) (string, bool) { return "user-1", token == "valid" }
	ready := func(userID, sessionID string) (any, error) {
		return map[string]any{"session_id": sessionID, "user": map[string]any{"id": userID}}, nil
	}
	guilds := func(userID string) []string { return []string{"guild-1"} }

	hubA := NewHub(auth, ready, guilds, nil)
	hubB := NewHub(auth, ready, guilds, nil)

	ctx := context.Background()
	busA, err := bus.NewPostgres(ctx, dsn, "instance-a", hubA.DeliverRemote, nil)
	if err != nil {
		t.Fatalf("bus a: %v", err)
	}
	defer busA.Close()
	busB, err := bus.NewPostgres(ctx, dsn, "instance-b", hubB.DeliverRemote, nil)
	if err != nil {
		t.Fatalf("bus b: %v", err)
	}
	defer busB.Close()
	hubA.SetBus(busA)
	hubB.SetBus(busB)

	srv := httptest.NewServer(hubA.Handler())
	defer srv.Close()

	dialCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	url := "ws" + strings.TrimPrefix(srv.URL, "http") + "/gateway"
	conn, _, err := websocket.Dial(dialCtx, url, nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.CloseNow()

	read := func() map[string]any {
		rctx, rcancel := context.WithTimeout(ctx, 10*time.Second)
		defer rcancel()
		_, data, err := conn.Read(rctx)
		if err != nil {
			t.Fatalf("read: %v", err)
		}
		var frame map[string]any
		if err := json.Unmarshal(data, &frame); err != nil {
			t.Fatalf("decode: %v", err)
		}
		return frame
	}
	if f := read(); f["op"].(float64) != 10 {
		t.Fatalf("expected HELLO: %+v", f)
	}
	wctx, wcancel := context.WithTimeout(ctx, 5*time.Second)
	defer wcancel()
	identify, _ := json.Marshal(map[string]any{"op": 2, "d": map[string]any{"token": "valid"}})
	if err := conn.Write(wctx, websocket.MessageText, identify); err != nil {
		t.Fatalf("identify write: %v", err)
	}
	if f := read(); f["t"] != "READY" {
		t.Fatalf("expected READY: %+v", f)
	}

	// Give the listener a moment to be subscribed and then publish from B.
	time.Sleep(200 * time.Millisecond)
	hubB.DispatchGuild("guild-1", "MESSAGE_CREATE", map[string]any{"id": "m1", "channel_id": "c1", "content": "cross-instance"})

	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		frame := read()
		if frame["t"] != "MESSAGE_CREATE" {
			continue
		}
		d := frame["d"].(map[string]any)
		if d["content"] != "cross-instance" {
			t.Fatalf("unexpected payload: %+v", d)
		}
		return
	}
	t.Fatal("cross-instance MESSAGE_CREATE not received")
}
