package db_test

import (
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"os"
	"strings"
	"testing"

	_ "github.com/jackc/pgx/v5/stdlib"

	"github.com/wavedevgit/waveycord/internal/db"
)

// TestLegacyMessagesMigration builds a pre-partitioning deployment in an isolated
// schema, opens it through db.Open, and checks that the legacy rows were moved
// into monthly partitions with their bucket computed from the timestamp.
func TestLegacyMessagesMigration(t *testing.T) {
	base := os.Getenv("WAVEYCORD_TEST_DSN")
	if !strings.HasPrefix(base, "postgres") {
		t.Skip("set WAVEYCORD_TEST_DSN to a postgres URL to run")
	}
	suffix := make([]byte, 6)
	if _, err := rand.Read(suffix); err != nil {
		t.Fatal(err)
	}
	schema := "wc_mig_" + hex.EncodeToString(suffix)
	sep := "?"
	if strings.Contains(base, "?") {
		sep = "&"
	}
	scoped := base + sep + "search_path=" + schema

	admin, err := sql.Open("pgx", base)
	if err != nil {
		t.Fatalf("admin open: %v", err)
	}
	defer admin.Close()
	if _, err := admin.Exec(`CREATE SCHEMA ` + schema); err != nil {
		t.Fatalf("create schema: %v", err)
	}
	t.Cleanup(func() {
		_, _ = admin.Exec(`DROP SCHEMA ` + schema + ` CASCADE`)
	})

	// Minimal pre-partitioning tables: only the rows the migrator needs to satisfy
	// its foreign keys.
	setup := []string{
		`CREATE TABLE users (id TEXT PRIMARY KEY, username TEXT NOT NULL, discriminator TEXT NOT NULL,
			password_hash TEXT NOT NULL DEFAULT '', created_at BIGINT NOT NULL)`,
		`CREATE TABLE guilds (id TEXT PRIMARY KEY, name TEXT NOT NULL, owner_id TEXT NOT NULL,
			created_at BIGINT NOT NULL)`,
		`CREATE TABLE channels (id TEXT PRIMARY KEY, type INTEGER NOT NULL, guild_id TEXT,
			position INTEGER NOT NULL DEFAULT 0, created_at BIGINT NOT NULL)`,
		`CREATE TABLE messages (
			id TEXT PRIMARY KEY, channel_id TEXT NOT NULL, guild_id TEXT, author_id TEXT NOT NULL,
			content TEXT NOT NULL DEFAULT '', timestamp BIGINT NOT NULL, edited_timestamp BIGINT,
			tts SMALLINT NOT NULL DEFAULT 0, mention_everyone SMALLINT NOT NULL DEFAULT 0,
			mentions TEXT NOT NULL DEFAULT '[]', mention_roles TEXT NOT NULL DEFAULT '[]',
			mention_channels TEXT NOT NULL DEFAULT '[]', pinned SMALLINT NOT NULL DEFAULT 0,
			type INTEGER NOT NULL DEFAULT 0, flags INTEGER NOT NULL DEFAULT 0,
			referenced_message_id TEXT, webhook_id TEXT, application_id TEXT,
			components TEXT NOT NULL DEFAULT '[]', attachments TEXT NOT NULL DEFAULT '[]',
			embeds TEXT NOT NULL DEFAULT '[]', sticker_items TEXT NOT NULL DEFAULT '[]',
			nonce TEXT)`,
		`INSERT INTO users (id, username, discriminator, created_at) VALUES ('1','legacy','0',1)`,
		`INSERT INTO guilds (id, name, owner_id, created_at) VALUES ('2','legacy guild','1',1)`,
		`INSERT INTO channels (id, type, guild_id, created_at) VALUES ('3',0,'2',1)`,
		`INSERT INTO messages (id, channel_id, guild_id, author_id, content, timestamp)
			VALUES ('4','3','2','1','old row', 1700000000000)`,
	}
	for _, q := range setup {
		if _, err := admin.Exec(`SET search_path TO ` + schema); err != nil {
			t.Fatalf("search path: %v", err)
		}
		if _, err := admin.Exec(q); err != nil {
			t.Fatalf("setup %q: %v", q, err)
		}
	}

	handle, err := db.Open(scoped)
	if err != nil {
		t.Fatalf("open migrated database: %v", err)
	}
	defer handle.Close()

	var relkind string
	if err := handle.QueryRow(`SELECT relkind::text FROM pg_class
		WHERE relname='messages' AND relnamespace = current_schema()::regnamespace`).Scan(&relkind); err != nil {
		t.Fatalf("relkind: %v", err)
	}
	if relkind != "p" {
		t.Fatalf("messages relkind = %q after migration, want p", relkind)
	}
	var content, partition string
	var bucket int64
	if err := handle.QueryRow(`SELECT content, bucket, tableoid::regclass::text FROM messages WHERE id='4'`).
		Scan(&content, &bucket, &partition); err != nil {
		t.Fatalf("read migrated row: %v", err)
	}
	if content != "old row" {
		t.Fatalf("content = %q", content)
	}
	if bucket != 1700000000000/86400000 {
		t.Fatalf("bucket = %d", bucket)
	}
	if !strings.Contains(partition, "messages_h") || !strings.Contains(partition, "_y202311") {
		t.Fatalf("migrated row landed in %q", partition)
	}
	var indexed string
	if err := handle.QueryRow(`SELECT channel_id FROM message_index WHERE id='4'`).Scan(&indexed); err != nil {
		t.Fatalf("message_index row: %v", err)
	}
	if indexed != "3" {
		t.Fatalf("indexed channel = %q", indexed)
	}
}
