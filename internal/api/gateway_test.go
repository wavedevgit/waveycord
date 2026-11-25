package api_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/coder/websocket"
)

func dialGateway(url string, deadline time.Time) (*websocket.Conn, *websocket.Conn, error) {
	ctx, cancel := context.WithDeadline(context.Background(), deadline)
	defer cancel()
	conn, _, err := websocket.Dial(ctx, url, nil)
	return conn, nil, err
}

func readFrame(t *testing.T, conn *websocket.Conn, deadline time.Time) map[string]any {
	t.Helper()
	ctx, cancel := context.WithDeadline(context.Background(), deadline)
	defer cancel()
	_, data, err := conn.Read(ctx)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	var frame map[string]any
	if err := json.Unmarshal(data, &frame); err != nil {
		t.Fatalf("decode frame %q: %v", data, err)
	}
	return frame
}

func writeFrame(t *testing.T, conn *websocket.Conn, frame map[string]any) {
	t.Helper()
	raw, err := json.Marshal(frame)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := conn.Write(ctx, websocket.MessageText, raw); err != nil {
		t.Fatalf("write: %v", err)
	}
}
