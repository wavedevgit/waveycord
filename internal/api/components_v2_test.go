package api_test

import (
	"testing"
)

func v2msg(extra map[string]any) map[string]any {
	body := map[string]any{"flags": 32768}
	for k, v := range extra {
		body[k] = v
	}
	return body
}

func TestComponentsV2Validation(t *testing.T) {
	h := newHarness(t)
	alice := h.register("v2alice")
	var guild struct{ ID string }
	h.decode("POST", "/guilds", alice.Token, map[string]any{"name": "V2 Guild"}, &guild)
	var channel struct{ ID string }
	h.decode("POST", "/guilds/"+guild.ID+"/channels", alice.Token, map[string]any{"name": "chat"}, &channel)

	valid := []any{
		map[string]any{
			"type": 17, "accent_color": 703487,
			"components": []any{
				map[string]any{"type": 10, "content": "# Hello v2"},
				map[string]any{"type": 14, "divider": true, "spacing": 2},
				map[string]any{
					"type": 9,
					"components": []any{
						map[string]any{"type": 10, "content": "section body"},
					},
					"accessory": map[string]any{
						"type": 11, "media": map[string]any{"url": "https://example.com/a.png"},
						"description": "alt text",
					},
				},
				map[string]any{
					"type": 1,
					"components": []any{
						map[string]any{"type": 2, "style": 1, "custom_id": "go", "label": "Go"},
					},
				},
				map[string]any{
					"type": 12,
					"items": []any{
						map[string]any{"media": map[string]any{"url": "https://example.com/b.png"}},
						map[string]any{"media": map[string]any{"url": "attachment://c.png"}, "spoiler": true},
					},
				},
				map[string]any{"type": 13, "file": map[string]any{"url": "attachment://manual.pdf"}},
			},
		},
	}
	var msg struct {
		ID         string `json:"id"`
		Type       int    `json:"type"`
		Flags      int    `json:"flags"`
		Components []struct {
			Type       int `json:"type"`
			Components []struct {
				Type int `json:"type"`
			} `json:"components"`
		} `json:"components"`
	}
	if status := h.decode("POST", "/channels/"+channel.ID+"/messages", alice.Token,
		v2msg(map[string]any{"components": valid}), &msg); status != 200 {
		t.Fatalf("valid v2 status %d", status)
	}
	if msg.Flags != 32768 || len(msg.Components) != 1 || msg.Components[0].Type != 17 {
		t.Fatalf("stored v2 shape: flags=%d components=%+v", msg.Flags, msg.Components)
	}
	if len(msg.Components[0].Components) != 6 {
		t.Fatalf("container children = %d", len(msg.Components[0].Components))
	}

	post := func(body map[string]any) int {
		status, _ := h.request("POST", "/channels/"+channel.ID+"/messages", alice.Token, body)
		return status
	}
	text := func(content string) map[string]any {
		return map[string]any{"type": 10, "content": content}
	}
	cases := []struct {
		name string
		body map[string]any
	}{
		{"v2 type without flag", map[string]any{"content": "x", "components": []any{text("hi")}}},
		{"section without accessory", v2msg(map[string]any{"components": []any{
			map[string]any{"type": 9, "components": []any{text("a")}},
		}})},
		{"section child not text", v2msg(map[string]any{"components": []any{
			map[string]any{"type": 9, "components": []any{map[string]any{"type": 14}},
				"accessory": map[string]any{"type": 11, "media": map[string]any{"url": "https://e/x.png"}}},
		}})},
		{"nested container", v2msg(map[string]any{"components": []any{
			map[string]any{"type": 17, "components": []any{map[string]any{"type": 17, "components": []any{text("a")}}}},
		}})},
		{"empty container", v2msg(map[string]any{"components": []any{map[string]any{"type": 17, "components": []any{}}}})},
		{"too many components", v2msg(map[string]any{"components": func() []any {
			out := []any{}
			for i := 0; i < 41; i++ {
				out = append(out, text("x"))
			}
			return out
		}()})},
		{"text over 4000 total", v2msg(map[string]any{"components": []any{
			text(repeatRune('a', 3000)), text(repeatRune('b', 1500)),
		}})},
		{"empty media gallery", v2msg(map[string]any{"components": []any{
			map[string]any{"type": 12, "items": []any{}},
		}})},
		{"eleven gallery items", v2msg(map[string]any{"components": []any{
			map[string]any{"type": 12, "items": func() []any {
				out := []any{}
				for i := 0; i < 11; i++ {
					out = append(out, map[string]any{"media": map[string]any{"url": "https://e/x.png"}})
				}
				return out
			}()},
		}})},
		{"file url not attachment", v2msg(map[string]any{"components": []any{
			map[string]any{"type": 13, "file": map[string]any{"url": "https://example.com/a.pdf"}},
		}})},
		{"separator spacing", v2msg(map[string]any{"components": []any{
			map[string]any{"type": 14, "spacing": 3},
		}})},
		{"accent out of range", v2msg(map[string]any{"components": []any{
			map[string]any{"type": 17, "accent_color": 0x1000000, "components": []any{text("a")}},
		}})},
		{"duplicate component ids", v2msg(map[string]any{"components": []any{
			map[string]any{"type": 10, "id": 5, "content": "a"},
			map[string]any{"type": 10, "id": 5, "content": "b"},
		}})},
		{"content alongside v2", v2msg(map[string]any{"content": "nope", "components": []any{text("a")}})},
		{"embeds alongside v2", v2msg(map[string]any{"embeds": []any{map[string]any{"title": "x"}}, "components": []any{text("a")}})},
		{"poll alongside v2", v2msg(map[string]any{"poll": map[string]any{
			"question": map[string]any{"text": "q"},
			"answers":  []any{map[string]any{"poll_media": map[string]any{"text": "a"}}},
		}, "components": []any{text("a")}})},
		{"premium accessory button without sku", v2msg(map[string]any{"components": []any{
			map[string]any{"type": 9, "components": []any{text("a")},
				"accessory": map[string]any{"type": 2, "style": 6}},
		}})},
		{"link accessory button without url", v2msg(map[string]any{"components": []any{
			map[string]any{"type": 9, "components": []any{text("a")},
				"accessory": map[string]any{"type": 2, "style": 5, "label": "go"}},
		}})},
		{"empty component list", v2msg(map[string]any{})},
	}
	for _, tc := range cases {
		if status := post(tc.body); status != 400 {
			t.Fatalf("%s: status %d, want 400", tc.name, status)
		}
	}

	// Editing: content is rejected, components can be replaced under the same flag.
	if status, _ := h.request("PATCH", "/channels/"+channel.ID+"/messages/"+msg.ID, alice.Token,
		map[string]any{"content": "hello"}); status != 400 {
		t.Fatalf("edit content on v2 message status %d", status)
	}
	var edited struct {
		Components []struct {
			Type int `json:"type"`
		} `json:"components"`
	}
	if status := h.decode("PATCH", "/channels/"+channel.ID+"/messages/"+msg.ID, alice.Token,
		map[string]any{"components": []any{text("replaced")}}, &edited); status != 200 {
		t.Fatalf("edit v2 components status %d", status)
	}
	if len(edited.Components) != 1 || edited.Components[0].Type != 10 {
		t.Fatalf("edited components: %+v", edited.Components)
	}
}

// repeatRune builds a string of n copies of r.
func repeatRune(r rune, n int) string {
	out := make([]rune, n)
	for i := range out {
		out[i] = r
	}
	return string(out)
}
