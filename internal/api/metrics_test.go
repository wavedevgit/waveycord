package api_test

import (
	"strings"
	"testing"
)

func TestHealthAndMetrics(t *testing.T) {
	h := newHarness(t)
	resp, err := h.server.Client().Get(h.server.URL + "/healthz")
	if err != nil {
		t.Fatal(err)
	}
	body := make([]byte, 512)
	n, _ := resp.Body.Read(body)
	resp.Body.Close()
	if resp.StatusCode != 200 || !strings.Contains(string(body[:n]), `"status":"ok"`) {
		t.Fatalf("health status %d body %s", resp.StatusCode, body[:n])
	}

	alice := h.register("meter")
	var guild struct{ ID string }
	h.decode("POST", "/guilds", alice.Token, map[string]any{"name": "Metrics"}, &guild)

	resp, err = h.server.Client().Get(h.server.URL + "/metrics")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	buf := make([]byte, 8192)
	n, _ = resp.Body.Read(buf)
	text := string(buf[:n])
	for _, want := range []string{
		"waveycord_http_requests_total{",
		"waveycord_gateway_connections 0",
		"waveycord_db_open_connections",
		"route=\"/guilds\"",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("metrics missing %q in:\n%s", want, text)
		}
	}
}
