package store_test

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/wavedevgit/waveycord/internal/db"
	"github.com/wavedevgit/waveycord/internal/store"
)

// TestSearchMigrationCheckpoint proves the migration is resumable and records
// per-guild completion, so a restart does not redo finished guilds.
func TestSearchMigrationCheckpoint(t *testing.T) {
	path := filepath.Join(t.TempDir(), "migration.db")
	handle, err := db.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	st := store.New(handle)
	user, err := st.CreateUser(store.CreateUserParams{
		Username: "miguser", Email: strPtr("mig@example.test"), PasswordHash: "x",
	})
	if err != nil {
		t.Fatal(err)
	}
	name := "chat"
	var guilds []string
	for _, gname := range []string{"Mig A", "Mig B"} {
		g, err := st.CreateGuild(user.ID, store.CreateGuildParams{Name: gname})
		if err != nil {
			t.Fatal(err)
		}
		ch, err := st.CreateChannel(g.ID, store.CreateChannelParams{Name: &name})
		if err != nil {
			t.Fatal(err)
		}
		for i := 0; i < 2; i++ {
			if _, err := st.CreateMessage(store.CreateMessageParams{
				ChannelID: ch.ID, GuildID: g.ID, AuthorID: user.ID, Content: "seed",
			}); err != nil {
				t.Fatal(err)
			}
		}
		guilds = append(guilds, g.ID)
	}
	idx := newCountingIndex()
	st.SetSearchIndex(idx)
	n, err := st.MigrateSearchIndex(context.Background(), 10, 5*time.Second)
	if err != nil || n != 4 {
		t.Fatalf("migration n=%d err=%v", n, err)
	}
	for _, g := range guilds {
		if got := idx.count(g); got != 2 {
			t.Fatalf("guild %s indexed %d", g, got)
		}
	}
	var done int
	if err := st.DB.QueryRow(`SELECT COUNT(*) FROM search_migration_state WHERE migrated = 1`).Scan(&done); err != nil {
		t.Fatal(err)
	}
	if done != 2 {
		t.Fatalf("checkpoints = %d", done)
	}
	// A second run does no work.
	if n, err := st.MigrateSearchIndex(context.Background(), 10, time.Second); err != nil || n != 0 {
		t.Fatalf("second migration n=%d err=%v", n, err)
	}
	// The checkpoint survives a reopen.
	handle.Close()
	handle2, err := db.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer handle2.Close()
	st2 := store.New(handle2)
	st2.SetSearchIndex(newCountingIndex())
	if n, err := st2.MigrateSearchIndex(context.Background(), 10, time.Second); err != nil || n != 0 {
		t.Fatalf("post-reopen migration n=%d err=%v", n, err)
	}
}
