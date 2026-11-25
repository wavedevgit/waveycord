package api_test

import (
	"os"
	"strings"
	"testing"
)

// TestPostgresMessagePartitioning asserts the storage layout matches the
// published design: messages is a partitioned table, a row lands in a hash child
// for its channel and a monthly bucket child, and the id index maps back to the
// owning channel.
func TestPostgresMessagePartitioning(t *testing.T) {
	dsn := os.Getenv("WAVEYCORD_TEST_DSN")
	if !strings.HasPrefix(dsn, "postgres") {
		t.Skip("set WAVEYCORD_TEST_DSN to a postgres URL to run")
	}
	h := newHarness(t)
	alice := h.register("partitioner")
	var guild struct{ ID string }
	h.decode("POST", "/guilds", alice.Token, map[string]any{"name": "Partitions"}, &guild)
	var channel struct{ ID string }
	h.decode("POST", "/guilds/"+guild.ID+"/channels", alice.Token, map[string]any{"name": "chunks"}, &channel)
	var msg struct {
		ID        string `json:"id"`
		Timestamp string `json:"timestamp"`
	}
	h.decode("POST", "/channels/"+channel.ID+"/messages", alice.Token, map[string]any{"content": "partitioned"}, &msg)

	db := h.store.DB
	var relkind string
	if err := db.QueryRow(`SELECT c.relkind::text FROM pg_class c
		JOIN pg_namespace n ON n.oid = c.relnamespace
		WHERE c.relname = 'messages' AND n.nspname = current_schema()`).Scan(&relkind); err != nil {
		t.Fatalf("relkind: %v", err)
	}
	if relkind != "p" {
		t.Fatalf("messages relkind = %q, want p (partitioned)", relkind)
	}

	var partition string
	if err := db.QueryRow(`SELECT tableoid::regclass::text FROM messages WHERE id = ?`, msg.ID).Scan(&partition); err != nil {
		t.Fatalf("tableoid: %v", err)
	}
	if !strings.HasPrefix(partition, "messages_h") || !strings.Contains(partition, "_y") {
		t.Fatalf("message landed in %q, want a messages_h*_y* child", partition)
	}

	var bucket int64
	var indexedChannel string
	if err := db.QueryRow(`SELECT bucket, channel_id FROM message_index WHERE id = ?`, msg.ID).
		Scan(&bucket, &indexedChannel); err != nil {
		t.Fatalf("message_index: %v", err)
	}
	if indexedChannel != channel.ID {
		t.Fatalf("message_index channel = %q, want %q", indexedChannel, channel.ID)
	}

	// The bucket matches the day window of the snowflake timestamp.
	var ts int64
	if err := db.QueryRow(`SELECT timestamp FROM messages WHERE id = ?`, msg.ID).Scan(&ts); err != nil {
		t.Fatalf("timestamp: %v", err)
	}
	if bucket != ts/86400000 {
		t.Fatalf("bucket = %d, want %d", bucket, ts/86400000)
	}

	// Every hash partition has a default child, so an out-of-window insert cannot
	// fail on a missing partition.
	var defaults int
	if err := db.QueryRow(`SELECT COUNT(*) FROM pg_class c JOIN pg_namespace n ON n.oid = c.relnamespace
		WHERE n.nspname = current_schema() AND c.relname LIKE 'messages_h%_default'`).Scan(&defaults); err != nil {
		t.Fatalf("default partitions: %v", err)
	}
	if defaults != 8 {
		t.Fatalf("default partition count = %d, want 8", defaults)
	}
}
