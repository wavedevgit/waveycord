package api_test

import (
	"context"
	"os"
	"strings"
	"testing"
)

// TestPostgresFullTextSearch proves the search path is backed by the tsvector/GIN
// index, that writes maintain it, and that permission filtering still applies.
func TestPostgresFullTextSearch(t *testing.T) {
	dsn := os.Getenv("WAVEYCORD_TEST_DSN")
	if !strings.HasPrefix(dsn, "postgres") {
		t.Skip("set WAVEYCORD_TEST_DSN to a postgres URL to run")
	}
	h := newHarness(t)
	alice := h.register("ftowner")
	bob := h.register("ftmember")
	var guild struct{ ID string }
	h.decode("POST", "/guilds", alice.Token, map[string]any{"name": "FullText"}, &guild)
	var open, closed struct{ ID string }
	h.decode("POST", "/guilds/"+guild.ID+"/channels", alice.Token, map[string]any{"name": "open"}, &open)
	h.decode("POST", "/guilds/"+guild.ID+"/channels", alice.Token, map[string]any{"name": "closed"}, &closed)

	post := func(channel, content string) string {
		t.Helper()
		var msg struct{ ID string }
		if status := h.decode("POST", "/channels/"+channel+"/messages", alice.Token,
			map[string]any{"content": content}, &msg); status != 200 {
			t.Fatalf("post %q status %d", content, status)
		}
		return msg.ID
	}
	tangerineID := post(open.ID, "the tangerine harvest is early")
	post(open.ID, "unrelated apricot note")
	post(closed.ID, "tangerine secret plans")

	// Structural evidence: tsvector column and GIN index exist.
	var bodyType string
	if err := h.store.DB.QueryRow(`SELECT format_type(atttypid, atttypmod) FROM pg_attribute
		WHERE attrelid = 'search_documents'::regclass AND attname = 'body'`).Scan(&bodyType); err != nil {
		t.Fatalf("tsvector column: %v", err)
	}
	if bodyType != "tsvector" {
		t.Fatalf("body type = %q, want tsvector", bodyType)
	}
	var ginCount int
	if err := h.store.DB.QueryRow(`SELECT COUNT(*) FROM pg_indexes
		WHERE tablename = 'search_documents' AND indexdef ILIKE '%USING gin%'`).Scan(&ginCount); err != nil {
		t.Fatalf("gin index: %v", err)
	}
	if ginCount == 0 {
		t.Fatal("search_documents has no GIN index")
	}

	// Search hits while only matching the indexed document.
	var first struct {
		Messages [][]struct {
			ID      string `json:"id"`
			Content string `json:"content"`
		} `json:"messages"`
		Total int `json:"total_results"`
	}
	if status := h.decode("GET", "/guilds/"+guild.ID+"/messages/search?content=tangerine", alice.Token, nil, &first); status != 200 {
		t.Fatalf("search status %d", status)
	}
	ids := map[string]bool{}
	for _, group := range first.Messages {
		for _, m := range group {
			ids[m.ID] = true
		}
	}
	if !ids[tangerineID] || len(ids) != 2 || first.Total != 2 {
		t.Fatalf("unexpected search result ids=%v total=%d", ids, first.Total)
	}

	// The GIN index is usable for the full-text predicate.
	ctx := context.Background()
	conn, err := h.store.DB.Conn(ctx)
	if err != nil {
		t.Fatalf("conn: %v", err)
	}
	defer conn.Close()
	if _, err := conn.ExecContext(ctx, `SET enable_seqscan = off`); err != nil {
		t.Fatalf("set seqscan: %v", err)
	}
	rows, err := conn.QueryContext(ctx, `EXPLAIN (COSTS OFF) SELECT message_id FROM search_documents
		WHERE body @@ plainto_tsquery('simple', 'tangerine')`)
	if err != nil {
		t.Fatalf("explain: %v", err)
	}
	var plan []string
	for rows.Next() {
		var line string
		if err := rows.Scan(&line); err != nil {
			rows.Close()
			t.Fatalf("scan plan: %v", err)
		}
		plan = append(plan, line)
	}
	rows.Close()
	joined := strings.Join(plan, "\n")
	if !strings.Contains(joined, "search_documents_body") {
		t.Fatalf("query plan does not use the GIN index:\n%s", joined)
	}

	// Permission filtering: Bob cannot read #closed.
	var invite struct{ Code string }
	h.decode("POST", "/channels/"+open.ID+"/invites", alice.Token, map[string]any{}, &invite)
	h.request("POST", "/invites/"+invite.Code, bob.Token, nil)
	if status, _ := h.request("PUT", "/channels/"+closed.ID+"/permissions/"+guild.ID, alice.Token, map[string]any{
		"type": 0, "allow": "0", "deny": "1024",
	}); status != 204 {
		t.Fatalf("deny view status %d", status)
	}
	var bobResult struct {
		Total int `json:"total_results"`
	}
	h.decode("GET", "/guilds/"+guild.ID+"/messages/search?content=tangerine", bob.Token, nil, &bobResult)
	if bobResult.Total != 1 {
		t.Fatalf("permission-filtered search total = %d, want 1", bobResult.Total)
	}

	// Author filter.
	var byAuthor struct {
		Total int `json:"total_results"`
	}
	h.decode("GET", "/guilds/"+guild.ID+"/messages/search?content=tangerine&author_id="+bob.ID, alice.Token, nil, &byAuthor)
	if byAuthor.Total != 0 {
		t.Fatalf("author filter total = %d, want 0", byAuthor.Total)
	}

	// Updates reindex: the old term disappears and the new one is found.
	if status := h.decode("PATCH", "/channels/"+open.ID+"/messages/"+tangerineID, alice.Token,
		map[string]any{"content": "the apricot harvest is early"}, nil); status != 200 {
		t.Fatalf("edit status %d", status)
	}
	var afterEdit struct {
		Total int `json:"total_results"`
	}
	h.decode("GET", "/guilds/"+guild.ID+"/messages/search?content=tangerine", alice.Token, nil, &afterEdit)
	if afterEdit.Total != 1 { // only the closed-channel message remains
		t.Fatalf("after edit total = %d, want 1", afterEdit.Total)
	}
	var apricot struct {
		Total int `json:"total_results"`
	}
	h.decode("GET", "/guilds/"+guild.ID+"/messages/search?content=apricot", alice.Token, nil, &apricot)
	if apricot.Total < 2 { // edited message + unrelated note
		t.Fatalf("apricot total = %d, want >= 2", apricot.Total)
	}

	// Deletes remove documents.
	if status, _ := h.request("DELETE", "/channels/"+closed.ID+"/messages/"+
		mustMessageID(t, h, closed.ID, "tangerine secret plans", alice.Token), alice.Token, nil); status != 204 {
		t.Fatalf("delete status %d", status)
	}
	var afterDelete struct {
		Total int `json:"total_results"`
	}
	h.decode("GET", "/guilds/"+guild.ID+"/messages/search?content=tangerine", alice.Token, nil, &afterDelete)
	if afterDelete.Total != 0 {
		t.Fatalf("after delete total = %d, want 0", afterDelete.Total)
	}
}

func mustMessageID(t *testing.T, h *harness, channelID, content, token string) string {
	t.Helper()
	var page []struct {
		ID      string `json:"id"`
		Content string `json:"content"`
	}
	h.decode("GET", "/channels/"+channelID+"/messages?limit=100", token, nil, &page)
	for _, m := range page {
		if m.Content == content {
			return m.ID
		}
	}
	t.Fatalf("message %q not found", content)
	return ""
}
