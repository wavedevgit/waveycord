package api_test

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/hex"
	"strings"
	"testing"
	"time"
)

func TestMessageComponentValidation(t *testing.T) {
	h := newHarness(t)
	alice := h.register("componentdev")
	var guild struct{ ID string }
	h.decode("POST", "/guilds", alice.Token, map[string]any{"name": "Components"}, &guild)
	var channel struct{ ID string }
	h.decode("POST", "/guilds/"+guild.ID+"/channels", alice.Token, map[string]any{"name": "ui"}, &channel)

	button := func(customID string) map[string]any {
		return map[string]any{"type": 1, "components": []map[string]any{
			{"type": 2, "style": 1, "label": "Approve", "custom_id": customID},
		}}
	}
	var msg struct {
		ID         string `json:"id"`
		Components []any  `json:"components"`
	}
	if status := h.decode("POST", "/channels/"+channel.ID+"/messages", alice.Token, map[string]any{
		"content": "vote below", "components": []map[string]any{button("approve")},
	}, &msg); status != 200 || len(msg.Components) != 1 {
		t.Fatalf("valid components status %d %+v", status, msg)
	}

	cases := []struct {
		name       string
		components any
	}{
		{"too many rows", func() []map[string]any {
			rows := make([]map[string]any, 0, 6)
			for i := 0; i < 6; i++ {
				rows = append(rows, button("b"+string(rune('a'+i))))
			}
			return rows
		}()},
		{"button missing custom_id", []map[string]any{{"type": 1, "components": []map[string]any{
			{"type": 2, "style": 2, "label": "No id"},
		}}}},
		{"link without url", []map[string]any{{"type": 1, "components": []map[string]any{
			{"type": 2, "style": 5, "label": "Broken"},
		}}}},
		{"two selects in one row", []map[string]any{{"type": 1, "components": []map[string]any{
			{"type": 5, "custom_id": "users"},
			{"type": 8, "custom_id": "channels"},
		}}}},
		{"text input in message", []map[string]any{{"type": 1, "components": []map[string]any{
			{"type": 4, "custom_id": "field"},
		}}}},
		{"string select without options", []map[string]any{{"type": 1, "components": []map[string]any{
			{"type": 3, "custom_id": "pick"},
		}}}},
		{"unknown component", []map[string]any{{"type": 1, "components": []map[string]any{
			{"type": 99, "custom_id": "x"},
		}}}},
	}
	for _, tc := range cases {
		if status, _ := h.request("POST", "/channels/"+channel.ID+"/messages", alice.Token, map[string]any{
			"content": "bad", "components": tc.components,
		}); status != 400 {
			t.Fatalf("%s: status %d", tc.name, status)
		}
	}

	// Valid select row is accepted.
	if status := h.decode("POST", "/channels/"+channel.ID+"/messages", alice.Token, map[string]any{
		"content": "pick", "components": []map[string]any{{"type": 1, "components": []map[string]any{
			{"type": 3, "custom_id": "pick", "options": []map[string]any{
				{"label": "One", "value": "1"}, {"label": "Two", "value": "2"},
			}},
		}}},
	}, nil); status != 200 {
		t.Fatalf("valid select status %d", status)
	}

	// The same validation applies on edit.
	if status := h.decode("PATCH", "/channels/"+channel.ID+"/messages/"+msg.ID, alice.Token, map[string]any{
		"components": cases[1].components,
	}, nil); status != 400 {
		t.Fatalf("edit invalid components status %d", status)
	}
}

func TestComponentAndModalInteractionCallbacks(t *testing.T) {
	h := newHarness(t)
	alice := h.register("modaldeve")
	var guild struct{ ID string }
	h.decode("POST", "/guilds", alice.Token, map[string]any{"name": "Modal Guild"}, &guild)

	var app struct {
		ID string `json:"id"`
	}
	h.decode("POST", "/applications", alice.Token, map[string]any{"name": "Component App"}, &app)
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	if status := h.decode("PATCH", "/applications/"+app.ID, alice.Token, map[string]any{
		"verify_key": hex.EncodeToString(pub),
	}, nil); status != 200 {
		t.Fatalf("register key status %d", status)
	}
	now := time.Now().Unix()

	// Button click.
	status, body := signedInteraction(t, h, priv, now, map[string]any{
		"id": "c1", "type": 3, "application_id": app.ID, "guild_id": guild.ID,
		"data": map[string]any{"custom_id": "approve", "component_type": 2},
	})
	if status != 200 || !strings.Contains(string(body), "Component `approve` received.") {
		t.Fatalf("button callback status %d body %s", status, body)
	}

	// String select values are echoed.
	status, body = signedInteraction(t, h, priv, now, map[string]any{
		"id": "c2", "type": 3, "application_id": app.ID, "guild_id": guild.ID,
		"data": map[string]any{"custom_id": "pick", "component_type": 3, "values": []string{"a", "b"}},
	})
	if status != 200 || !strings.Contains(string(body), "values: a, b") {
		t.Fatalf("select callback status %d body %s", status, body)
	}

	// Invalid component payloads.
	status, _ = signedInteraction(t, h, priv, now, map[string]any{
		"id": "c3", "type": 3, "application_id": app.ID,
		"data": map[string]any{"component_type": 2},
	})
	if status != 400 {
		t.Fatalf("missing custom_id status %d", status)
	}
	status, _ = signedInteraction(t, h, priv, now, map[string]any{
		"id": "c4", "type": 3, "application_id": app.ID,
		"data": map[string]any{"custom_id": "x", "component_type": 999},
	})
	if status != 400 {
		t.Fatalf("bad component_type status %d", status)
	}
	status, _ = signedInteraction(t, h, priv, now, map[string]any{
		"id": "c5", "type": 3, "application_id": app.ID,
		"data": map[string]any{"custom_id": "x", "component_type": 3, "values": make([]string, 26)},
	})
	if status != 400 {
		t.Fatalf("too many values status %d", status)
	}

	// Modal submit with two text fields.
	status, body = signedInteraction(t, h, priv, now, map[string]any{
		"id": "m1", "type": 5, "application_id": app.ID, "guild_id": guild.ID,
		"data": map[string]any{
			"custom_id": "feedback",
			"components": []map[string]any{{"type": 1, "components": []map[string]any{
				{"type": 4, "custom_id": "name", "value": "Ada"},
				{"type": 4, "custom_id": "note", "value": "works"},
			}}},
		},
	})
	if status != 200 || !strings.Contains(string(body), "Modal `feedback` submitted: name=Ada; note=works.") {
		t.Fatalf("modal callback status %d body %s", status, body)
	}

	// Invalid modal payloads.
	for name, data := range map[string]any{
		"missing custom_id": map[string]any{"components": []any{}},
		"no components":     map[string]any{"custom_id": "m"},
		"non-row top level": map[string]any{"custom_id": "m", "components": []map[string]any{
			{"type": 4, "custom_id": "x", "value": "y"},
		}},
		"button inside modal": map[string]any{"custom_id": "m", "components": []map[string]any{
			{"type": 1, "components": []map[string]any{{"type": 2, "custom_id": "x"}}},
		}},
		"field missing custom_id": map[string]any{"custom_id": "m", "components": []map[string]any{
			{"type": 1, "components": []map[string]any{{"type": 4, "value": "z"}}},
		}},
	} {
		status, _ := signedInteraction(t, h, priv, now, map[string]any{
			"id": "bad", "type": 5, "application_id": app.ID, "data": data,
		})
		if status != 400 {
			t.Fatalf("%s: status %d", name, status)
		}
	}
}
