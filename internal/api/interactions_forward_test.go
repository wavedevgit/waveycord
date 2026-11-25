package api_test

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// TestInteractionForwardingToBotEndpoint registers an app interactions endpoint,
// verifies outbound signatures with the application's server public key, and
// checks that the endpoint's callback is relayed to the caller.
func TestInteractionForwardingToBotEndpoint(t *testing.T) {
	h := newHarness(t)
	alice := h.register("forwarddev")

	var app struct {
		ID               string `json:"id"`
		VerifyKey        string `json:"verify_key"`
		SigningPublicKey string `json:"signing_public_key"`
	}
	if status := h.decode("POST", "/applications", alice.Token, map[string]any{"name": "Forwarded Bot"}, &app); status != 201 {
		t.Fatalf("create app status %d", status)
	}
	if app.SigningPublicKey == "" {
		t.Fatal("application has no server signing public key")
	}
	outboundPub, err := hex.DecodeString(app.SigningPublicKey)
	if err != nil || len(outboundPub) != ed25519.PublicKeySize {
		t.Fatalf("bad signing public key: %v", err)
	}

	inboundPub, inboundPriv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}

	type captured struct {
		body      []byte
		signature string
		timestamp string
	}
	var (
		mu       sync.Mutex
		last     captured
		rejected bool
	)
	receiver := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(io.LimitReader(r.Body, 1<<20))
		mu.Lock()
		last = captured{body: body, signature: r.Header.Get("X-Signature-Ed25519"), timestamp: r.Header.Get("X-Signature-Timestamp")}
		mu.Unlock()

		var payload struct {
			Type int `json:"type"`
			Data struct {
				Name     string `json:"name"`
				CustomID string `json:"custom_id"`
			} `json:"data"`
		}
		_ = json.Unmarshal(body, &payload)
		w.Header().Set("Content-Type", "application/json")
		switch {
		case rejected:
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte(`{"error":"boom"}`))
		case payload.Type == 1:
			_, _ = w.Write([]byte(`{"type":1}`))
		case payload.Type == 3:
			_, _ = w.Write([]byte(`{"type":4,"data":{"content":"component:` + payload.Data.CustomID + `"}}`))
		case payload.Type == 5:
			_, _ = w.Write([]byte(`{"type":4,"data":{"content":"modal:` + payload.Data.CustomID + `"}}`))
		default:
			_, _ = w.Write([]byte(`{"type":4,"data":{"content":"forwarded ` + payload.Data.Name + `"}}`))
		}
	}))
	defer receiver.Close()

	if status := h.decode("PATCH", "/applications/"+app.ID, alice.Token, map[string]any{
		"verify_key":                hex.EncodeToString(inboundPub),
		"interactions_endpoint_url": receiver.URL,
	}, nil); status != 200 {
		t.Fatalf("configure endpoint status %d", status)
	}

	now := time.Now().Unix()
	status, body := signedInteraction(t, h, inboundPriv, now, map[string]any{
		"id": "1", "type": 1, "application_id": app.ID, "token": "ping",
	})
	if status != 200 || !strings.Contains(string(body), `"type":1`) {
		t.Fatalf("forwarded ping status %d body %s", status, body)
	}

	mu.Lock()
	got := last
	mu.Unlock()
	if got.signature == "" || got.timestamp == "" {
		t.Fatal("outbound request was not signed")
	}
	ts, err := strconv.ParseInt(got.timestamp, 10, 64)
	if err != nil || time.Since(time.Unix(ts, 0)) > time.Minute {
		t.Fatalf("bad outbound timestamp %q", got.timestamp)
	}
	sig, err := hex.DecodeString(got.signature)
	if err != nil || len(sig) != ed25519.SignatureSize {
		t.Fatalf("bad outbound signature: %v", err)
	}
	if !ed25519.Verify(ed25519.PublicKey(outboundPub), append([]byte(got.timestamp), got.body...), sig) {
		t.Fatal("outbound signature does not verify with the application signing key")
	}

	// Command callbacks are relayed verbatim.
	status, body = signedInteraction(t, h, inboundPriv, now, map[string]any{
		"id": "2", "type": 2, "application_id": app.ID,
		"data": map[string]any{"name": "deploy", "type": 1},
	})
	if status != 200 || !strings.Contains(string(body), "forwarded deploy") {
		t.Fatalf("forwarded command status %d body %s", status, body)
	}

	// Component and modal interactions forward too.
	status, body = signedInteraction(t, h, inboundPriv, now, map[string]any{
		"id": "2b", "type": 3, "application_id": app.ID,
		"data": map[string]any{"custom_id": "approve", "component_type": 2},
	})
	if status != 200 || !strings.Contains(string(body), "component:approve") {
		t.Fatalf("forwarded component status %d body %s", status, body)
	}
	status, body = signedInteraction(t, h, inboundPriv, now, map[string]any{
		"id": "2c", "type": 5, "application_id": app.ID,
		"data": map[string]any{"custom_id": "feedback", "components": []map[string]any{}},
	})
	if status != 200 || !strings.Contains(string(body), "modal:feedback") {
		t.Fatalf("forwarded modal status %d body %s", status, body)
	}

	// A failing bot endpoint becomes 502 for the caller.
	mu.Lock()
	rejected = true
	mu.Unlock()
	if status, _ := signedInteraction(t, h, inboundPriv, now, map[string]any{
		"id": "3", "type": 2, "application_id": app.ID,
		"data": map[string]any{"name": "fail", "type": 1},
	}); status != 502 {
		t.Fatalf("failing endpoint status %d", status)
	}

	// Endpoint URL validation: non-localhost must be https.
	if status, _ := h.request("PATCH", "/applications/"+app.ID, alice.Token, map[string]any{
		"interactions_endpoint_url": "http://example.test/hook",
	}); status != 400 {
		t.Fatalf("insecure endpoint status %d", status)
	}

	// Unreachable endpoint becomes 502.
	receiver.Close()
	if status, _ := signedInteraction(t, h, inboundPriv, now, map[string]any{
		"id": "4", "type": 1, "application_id": app.ID,
	}); status != 502 {
		t.Fatalf("unreachable endpoint status %d", status)
	}
}
