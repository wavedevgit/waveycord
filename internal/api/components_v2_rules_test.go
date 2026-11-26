package api

import (
	"strings"
	"testing"
)

// TestV2ValidationStrings pins which rule fires, so a passing integration test
// cannot hide a rejection that happened for the wrong reason.
func TestV2ValidationStrings(t *testing.T) {
	text := map[string]any{"type": 10, "content": "a"}
	section := func(accessory map[string]any, children ...any) map[string]any {
		return map[string]any{"type": 9, "components": children, "accessory": accessory}
	}
	thumb := map[string]any{"type": 11, "media": map[string]any{"url": "https://e/x.png"}}
	cases := []struct {
		name string
		body []any
		want string
	}{
		{"premium needs sku", []any{section(map[string]any{"type": 2, "style": 6}, text)}, "sku_id"},
		{"link needs url", []any{section(map[string]any{"type": 2, "style": 5, "label": "go"}, text)}, "valid url"},
		{"section accessory type", []any{section(map[string]any{"type": 14}, text)}, "button or thumbnail"},
		{"section child", []any{section(thumb, map[string]any{"type": 14})}, "text display children"},
		{"nested container", []any{map[string]any{"type": 17, "components": []any{
			map[string]any{"type": 17, "components": []any{text}},
		}}}, "containers allow"},
		{"container child type", []any{map[string]any{"type": 17, "components": []any{
			map[string]any{"type": 11, "media": map[string]any{"url": "https://e/x.png"}},
		}}}, "containers allow"},
		{"gallery empty", []any{map[string]any{"type": 12, "items": []any{}}}, "1-10 items"},
		{"gallery no url", []any{map[string]any{"type": 12, "items": []any{map[string]any{}}}}, "media.url"},
		{"file protocol", []any{map[string]any{"type": 13, "file": map[string]any{"url": "https://e/a.pdf"}}}, "attachment://"},
		{"file empty name", []any{map[string]any{"type": 13, "file": map[string]any{"url": "attachment://"}}}, "filename"},
		{"separator spacing", []any{map[string]any{"type": 14, "spacing": 0}}, "spacing must be 1 or 2"},
		{"accent range", []any{map[string]any{"type": 17, "accent_color": 0x1000000,
			"components": []any{text}}}, "accent_color"},
		{"duplicate ids", []any{
			map[string]any{"type": 10, "id": 7, "content": "a"},
			map[string]any{"type": 10, "id": 7, "content": "b"},
		}, "unique"},
		{"negative id", []any{map[string]any{"type": 10, "id": -1, "content": "a"}}, "negative"},
		{"text empty", []any{map[string]any{"type": 10, "content": ""}}, "cannot be empty"},
		{"text too long", []any{map[string]any{"type": 10, "content": strings.Repeat("a", 4001)}}, "4000"},
		{"empty list", []any{}, "at least one component"},
	}
	for _, tc := range cases {
		got := validateComponentsV2(tc.body)
		if !strings.Contains(got, tc.want) {
			t.Fatalf("%s: got %q, want substring %q", tc.name, got, tc.want)
		}
	}
	// A valid layout returns no error.
	ok := []any{section(map[string]any{
		"type": 11, "media": map[string]any{"url": "https://e/x.png"},
	}, text), map[string]any{"type": 14, "spacing": 1}}
	if got := validateComponentsV2(ok); got != "" {
		t.Fatalf("valid layout rejected: %s", got)
	}
}
