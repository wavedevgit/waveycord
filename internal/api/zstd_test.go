package api_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/klauspost/compress/zstd"
)

// TestGatewayZstdStream connects with ?compress=zstd-stream, decodes the binary
// frames with a streaming zstd reader, and verifies HELLO, READY and a
// MESSAGE_CREATE payload arrive intact.
func TestGatewayZstdStream(t *testing.T) {
	h := newHarness(t)
	alice := h.register("zstduser")
	var guild struct{ ID string }
	h.decode("POST", "/guilds", alice.Token, map[string]any{"name": "Zstd Guild"}, &guild)
	var channel struct{ ID string }
	h.decode("POST", "/guilds/"+guild.ID+"/channels", alice.Token, map[string]any{"name": "live"}, &channel)

	url := "ws" + strings.TrimPrefix(h.server.URL, "http") + "/gateway?compress=zstd-stream&v=10&encoding=json"
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	conn, _, err := websocket.Dial(ctx, url, nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	pr, pw := io.Pipe()

	var binaryFrames, compressedBytes atomic.Int64
	var badType atomic.Value
	go func() {
		for {
			typ, data, err := conn.Read(ctx)
			if err != nil {
				_ = pw.CloseWithError(err)
				return
			}
			if typ != websocket.MessageBinary {
				badType.Store(typ)
				_ = pw.CloseWithError(errors.New("expected binary frame"))
				return
			}
			binaryFrames.Add(1)
			compressedBytes.Add(int64(len(data)))
			if _, err := pw.Write(data); err != nil {
				return
			}
		}
	}()

	dec, err := zstd.NewReader(pr)
	if err != nil {
		t.Fatalf("zstd reader: %v", err)
	}
	// Close order matters: the decoder waits on its reader goroutine, so the
	// websocket, pipe writer and decoder must be torn down in that order.
	defer func() {
		conn.CloseNow()
		pw.Close()
		dec.Close()
	}()
	frames := json.NewDecoder(dec)
	next := func() map[string]any {
		t.Helper()
		var f map[string]any
		if err := frames.Decode(&f); err != nil {
			t.Fatalf("decode compressed frame: %v (badType=%v)", err, badType.Load())
		}
		return f
	}

	hello := next()
	if hello["op"].(float64) != 10 {
		t.Fatalf("expected HELLO, got %+v", hello)
	}

	identify, _ := json.Marshal(map[string]any{"op": 2, "d": map[string]any{
		"token": alice.Token, "intents": 0, "properties": map[string]any{},
	}})
	if err := conn.Write(ctx, websocket.MessageText, identify); err != nil {
		t.Fatalf("identify: %v", err)
	}
	ready := next()
	if ready["t"] != "READY" {
		t.Fatalf("expected READY, got %+v", ready)
	}

	status, _ := h.request("POST", "/channels/"+channel.ID+"/messages", alice.Token, map[string]any{"content": "zstd hello"})
	if status != 200 {
		t.Fatalf("send status %d", status)
	}
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		f := next()
		if f["t"] != "MESSAGE_CREATE" {
			continue
		}
		d := f["d"].(map[string]any)
		if d["content"] != "zstd hello" || d["channel_id"] != channel.ID {
			t.Fatalf("unexpected dispatch %+v", d)
		}
		if binaryFrames.Load() < 3 {
			t.Fatalf("expected compressed binary frames, got %d", binaryFrames.Load())
		}
		// Compressed frames must actually be smaller than the equivalent JSON:
		// four frames of a few hundred bytes each stay far below 8 KiB.
		if compressedBytes.Load() > 8192 {
			t.Fatalf("compressed payload unexpectedly large: %d bytes", compressedBytes.Load())
		}
		t.Logf("zstd-stream: %d binary frames, %d compressed bytes", binaryFrames.Load(), compressedBytes.Load())
		return
	}
	t.Fatal("MESSAGE_CREATE not received over zstd-stream")
}

// TestGatewayUnknownCompressionFallsBackToJSON verifies an unsupported transport
// setting does not break the handshake: zlib-stream currently falls back to the
// documented uncompressed JSON frames.
func TestGatewayUnknownCompressionFallsBackToJSON(t *testing.T) {
	h := newHarness(t)
	alice := h.register("fallbackuser")
	url := "ws" + strings.TrimPrefix(h.server.URL, "http") + "/gateway?compress=zlib-stream&v=10"
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	conn, _, err := websocket.Dial(ctx, url, nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.CloseNow()
	typ, data, err := conn.Read(ctx)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if typ != websocket.MessageText {
		t.Fatalf("frame type = %v, want text", typ)
	}
	var hello map[string]any
	if err := json.Unmarshal(data, &hello); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if hello["op"].(float64) != 10 {
		t.Fatalf("expected HELLO, got %+v", hello)
	}
	_ = alice
}
