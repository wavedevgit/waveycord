package api_test

import (
	"bytes"
	"image/png"
	"io"
	"net/http"
	"testing"
)

func TestWidgetImageStickerPacksAndMember(t *testing.T) {
	h := newHarness(t)
	alice := h.register("widgetimg")
	bob := h.register("widgetoutsider")
	var guild struct{ ID string }
	h.decode("POST", "/guilds", alice.Token, map[string]any{"name": "Image Guild"}, &guild)

	resp, err := h.server.Client().Get(h.server.URL + "/api/v10/guilds/" + guild.ID + "/widget.png")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 || resp.Header.Get("Content-Type") != "image/png" {
		t.Fatalf("widget.png status %d type %q", resp.StatusCode, resp.Header.Get("Content-Type"))
	}
	img, err := png.Decode(bytes.NewReader(raw))
	if err != nil {
		t.Fatalf("decode widget png: %v", err)
	}
	if img.Bounds().Dx() != 320 || img.Bounds().Dy() != 80 {
		t.Fatalf("widget dimensions %v", img.Bounds())
	}

	var packs []any
	if status := h.decode("GET", "/sticker-packs", "", nil, &packs); status != 200 || len(packs) != 0 {
		t.Fatalf("sticker packs %d %+v", status, packs)
	}

	var member struct {
		User struct{ ID string } `json:"user"`
	}
	if status := h.decode("GET", "/users/@me/guilds/"+guild.ID+"/member", alice.Token, nil, &member); status != 200 || member.User.ID != alice.ID {
		t.Fatalf("my member %d %+v", status, member)
	}
	var req, _ = http.NewRequest("GET", h.server.URL+"/api/v10/users/@me/guilds/"+guild.ID+"/member", nil)
	req.Header.Set("Authorization", bob.Token)
	resp2, err := h.server.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp2.Body.Close()
	if resp2.StatusCode != 404 {
		t.Fatalf("non-member status %d", resp2.StatusCode)
	}
}
