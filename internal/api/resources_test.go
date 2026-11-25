package api_test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"testing"
)

func (h *harness) path(rawURL string) string {
	h.t.Helper()
	u, err := url.Parse(rawURL)
	if err != nil {
		h.t.Fatalf("parse url %q: %v", rawURL, err)
	}
	return u.Path
}

func TestAttachmentUploadAndMessage(t *testing.T) {
	h := newHarness(t)
	alice := h.register("uploader")
	var guild struct{ ID string }
	h.decode("POST", "/guilds", alice.Token, map[string]any{"name": "Files"}, &guild)
	var channel struct{ ID string }
	h.decode("POST", "/guilds/"+guild.ID+"/channels", alice.Token, map[string]any{"name": "shots"}, &channel)

	var staged []struct {
		ID             string `json:"id"`
		Filename       string `json:"filename"`
		UploadFilename string `json:"upload_filename"`
		UploadURL      string `json:"upload_url"`
	}
	if status := h.decode("POST", "/channels/"+channel.ID+"/attachments", alice.Token, map[string]any{
		"files": []map[string]any{{"id": 0, "filename": "note.txt", "file_size": 5}},
	}, &staged); status != 200 || len(staged) != 1 {
		t.Fatalf("stage status %d %+v", status, staged)
	}

	raw := []byte("hello")
	req, err := http.NewRequest("PUT", h.server.URL+h.path(staged[0].UploadURL), bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "text/plain; charset=utf-8")
	resp, err := h.server.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 204 {
		t.Fatalf("upload status %d", resp.StatusCode)
	}

	var msg struct {
		Attachments []struct {
			ID          string `json:"id"`
			Filename    string `json:"filename"`
			Size        int64  `json:"size"`
			URL         string `json:"url"`
			ContentType string `json:"content_type"`
		} `json:"attachments"`
		Content string `json:"content"`
	}
	if status := h.decode("POST", "/channels/"+channel.ID+"/messages", alice.Token, map[string]any{
		"content":     "",
		"attachments": []map[string]any{{"id": staged[0].ID, "filename": "note.txt"}},
	}, &msg); status != 200 || len(msg.Attachments) != 1 {
		t.Fatalf("message status %d %+v", status, msg)
	}
	att := msg.Attachments[0]
	if att.Size != int64(len(raw)) || !strings.Contains(att.ContentType, "text/plain") || att.URL == "" {
		t.Fatalf("unexpected attachment %+v", att)
	}

	// The CDN route serves the same bytes.
	cdnURL := h.server.URL + h.path(att.URL)
	cdnResp, err := h.server.Client().Get(cdnURL)
	if err != nil {
		t.Fatal(err)
	}
	defer cdnResp.Body.Close()
	buf := new(bytes.Buffer)
	_, _ = buf.ReadFrom(cdnResp.Body)
	if cdnResp.StatusCode != 200 || buf.String() != "hello" {
		t.Fatalf("cdn status %d body %q", cdnResp.StatusCode, buf.String())
	}
}

func TestWebhooks(t *testing.T) {
	h := newHarness(t)
	alice := h.register("hooker")
	var guild struct{ ID string }
	h.decode("POST", "/guilds", alice.Token, map[string]any{"name": "Hooks"}, &guild)
	var channel struct{ ID string }
	h.decode("POST", "/guilds/"+guild.ID+"/channels", alice.Token, map[string]any{"name": "feed"}, &channel)

	var hook struct {
		ID      string `json:"id"`
		Name    string `json:"name"`
		Token   string `json:"token"`
		Channel string `json:"channel_id"`
		User    *struct{ ID string }
	}
	status, rawBody := h.request("POST", "/channels/"+channel.ID+"/webhooks", alice.Token, map[string]any{
		"name": "Build Feed",
	})
	if status != 201 {
		t.Fatalf("create webhook status %d body %s", status, rawBody)
	}
	if err := json.Unmarshal(rawBody, &hook); err != nil {
		t.Fatalf("decode webhook: %v", err)
	}
	// The webhook's user field is the creator; the posting bot has the webhook id.
	if hook.User == nil || hook.User.ID != alice.ID {
		t.Fatalf("webhook creator missing: %+v", hook.User)
	}

	var msg struct {
		Content string `json:"content"`
		Author  struct {
			ID       string `json:"id"`
			Username string `json:"username"`
			Bot      bool   `json:"bot"`
		} `json:"author"`
	}
	// Execution uses the token only: no Authorization header.
	if status := h.decode("POST", "/webhooks/"+hook.ID+"/"+hook.Token+"?wait=true", "", map[string]any{
		"content": "deployed 1.2.3",
	}, &msg); status != 200 {
		t.Fatalf("execute status %d", status)
	}
	if msg.Content != "deployed 1.2.3" || msg.Author.ID != hook.ID || !msg.Author.Bot {
		t.Fatalf("unexpected webhook message %+v", msg)
	}

	var listed []struct{ ID string }
	if status := h.decode("GET", "/channels/"+channel.ID+"/webhooks", alice.Token, nil, &listed); status != 200 || len(listed) != 1 {
		t.Fatalf("list webhooks %d %+v", status, listed)
	}

	if status, _ := h.request("POST", "/webhooks/"+hook.ID+"/"+hook.Token, "", map[string]any{"content": ""}); status != 400 {
		t.Fatalf("empty webhook message status %d", status)
	}
	if status, _ := h.request("DELETE", "/webhooks/"+hook.ID, alice.Token, nil); status != 204 {
		t.Fatalf("delete webhook status %d", status)
	}
	if status, _ := h.request("POST", "/webhooks/"+hook.ID+"/"+hook.Token+"?wait=true", "", map[string]any{"content": "gone"}); status != 404 {
		t.Fatalf("execute deleted webhook status %d", status)
	}
}

func TestEmojiAuditAndAck(t *testing.T) {
	h := newHarness(t)
	alice := h.register("auditor")
	var guild struct{ ID string }
	h.decode("POST", "/guilds", alice.Token, map[string]any{"name": "Audit"}, &guild)
	var channel struct{ ID string }
	h.decode("POST", "/guilds/"+guild.ID+"/channels", alice.Token, map[string]any{"name": "log"}, &channel)

	var emoji struct {
		ID            string `json:"id"`
		Name          string `json:"name"`
		RequireColons bool   `json:"require_colons"`
	}
	if status := h.decode("POST", "/guilds/"+guild.ID+"/emojis", alice.Token, map[string]any{
		"name": "party_blob", "image": "data:image/png;base64,AAAA",
	}, &emoji); status != 201 || emoji.Name != "party_blob" {
		t.Fatalf("create emoji %d %+v", status, emoji)
	}
	var emojis []struct{ ID string }
	if status := h.decode("GET", "/guilds/"+guild.ID+"/emojis", alice.Token, nil, &emojis); status != 200 || len(emojis) != 1 {
		t.Fatalf("list emoji %d %+v", status, emojis)
	}
	var renamed struct{ Name string }
	if status := h.decode("PATCH", "/guilds/"+guild.ID+"/emojis/"+emoji.ID, alice.Token, map[string]any{"name": "party_parrot"}, &renamed); status != 200 || renamed.Name != "party_parrot" {
		t.Fatalf("rename emoji %d %+v", status, renamed)
	}

	var msg struct{ ID string }
	h.decode("POST", "/channels/"+channel.ID+"/messages", alice.Token, map[string]any{"content": "unread"}, &msg)
	if status, _ := h.request("POST", "/channels/"+channel.ID+"/messages/"+msg.ID+"/ack", alice.Token, map[string]any{}); status != 204 {
		t.Fatalf("ack status %d", status)
	}

	var audit struct {
		Entries []struct {
			ActionType int    `json:"action_type"`
			TargetID   string `json:"target_id"`
			ActorID    string `json:"actor_id"`
		} `json:"audit_log_entries"`
		Users map[string]json.RawMessage `json:"users"`
	}
	if status := h.decode("GET", "/guilds/"+guild.ID+"/audit-logs", alice.Token, nil, &audit); status != 200 {
		t.Fatalf("audit status %d", status)
	}
	if len(audit.Entries) < 2 {
		t.Fatalf("expected audit entries, got %+v", audit.Entries)
	}
	seen := map[int]bool{}
	for _, e := range audit.Entries {
		seen[e.ActionType] = true
		if e.ActorID != alice.ID {
			t.Fatalf("actor mismatch %q", e.ActorID)
		}
	}
	if !seen[60] { // EMOJI_CREATE
		t.Fatalf("emoji create not in audit log: %+v", seen)
	}
	if !seen[10] { // CHANNEL_CREATE
		t.Fatalf("channel create not in audit log: %+v", seen)
	}

	if status, _ := h.request("DELETE", "/guilds/"+guild.ID+"/emojis/"+emoji.ID, alice.Token, nil); status != 204 {
		t.Fatalf("delete emoji %d", status)
	}
}
