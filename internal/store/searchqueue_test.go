package store_test

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/wavedevgit/waveycord/internal/db"
	"github.com/wavedevgit/waveycord/internal/store"
)

// TestDurableSearchQueue proves indexing is at-least-once through a durable table:
// writes queue instead of indexing, the worker drains and acknowledges, a failure
// keeps the job for retry, and deletes take the same path.
func TestDurableSearchQueue(t *testing.T) {
	handle, err := db.Open(filepath.Join(t.TempDir(), "queue.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer handle.Close()
	st := store.New(handle)
	user, err := st.CreateUser(store.CreateUserParams{
		Username: "queueuser", Email: strPtr("queue@example.test"), PasswordHash: "x",
	})
	if err != nil {
		t.Fatal(err)
	}
	guild, err := st.CreateGuild(user.ID, store.CreateGuildParams{Name: "Queue Guild"})
	if err != nil {
		t.Fatal(err)
	}
	name := "chat"
	channel, err := st.CreateChannel(guild.ID, store.CreateChannelParams{Name: &name})
	if err != nil {
		t.Fatal(err)
	}
	idx := newCountingIndex()
	st.SetSearchIndex(idx)
	st.SetDurableSearchQueue(true)

	var ids []string
	for i := 0; i < 3; i++ {
		msg, err := st.CreateMessage(store.CreateMessageParams{
			ChannelID: channel.ID, GuildID: guild.ID, AuthorID: user.ID,
			Content: "queued message",
		})
		if err != nil {
			t.Fatalf("create message: %v", err)
		}
		ids = append(ids, msg.ID)
	}
	if got := idx.count(guild.ID); got != 0 {
		t.Fatalf("index wrote inline while the queue was enabled: %d", got)
	}
	if n, err := st.SearchQueueLen(); err != nil || n != 3 {
		t.Fatalf("queue len = %d err %v", n, err)
	}

	done, err := st.ProcessSearchQueue(context.Background(), 10)
	if err != nil || done != 3 {
		t.Fatalf("process done=%d err=%v", done, err)
	}
	if n, _ := st.SearchQueueLen(); n != 0 {
		t.Fatalf("queue not drained: %d", n)
	}
	if got := idx.count(guild.ID); got != 3 {
		t.Fatalf("indexed %d, want 3", got)
	}

	// Edits are queued too, and processing collapses to the latest content.
	updated := "edited content"
	if _, err := st.UpdateMessage(ids[0], &updated, nil, nil, nil); err != nil {
		t.Fatalf("update: %v", err)
	}
	if n, _ := st.SearchQueueLen(); n != 1 {
		t.Fatalf("edit queue len = %d", n)
	}
	if done, err := st.ProcessSearchQueue(context.Background(), 10); err != nil || done != 1 {
		t.Fatalf("process edit done=%d err=%v", done, err)
	}
	idx.mu.Lock()
	gotContent := idx.docs[guild.ID][ids[0]].Content
	idx.mu.Unlock()
	if gotContent != updated {
		t.Fatalf("indexed content %q, want %q", gotContent, updated)
	}

	// A failing index call keeps the job and increments attempts.
	bad, err := st.CreateMessage(store.CreateMessageParams{
		ChannelID: channel.ID, GuildID: guild.ID, AuthorID: user.ID, Content: "poison",
	})
	if err != nil {
		t.Fatal(err)
	}
	idx.mu.Lock()
	idx.fail[bad.ID] = true
	idx.mu.Unlock()
	if done, err := st.ProcessSearchQueue(context.Background(), 10); err != nil || done != 0 {
		t.Fatalf("failing batch done=%d err=%v", done, err)
	}
	var attempts int
	if err := st.DB.QueryRow(`SELECT attempts FROM search_queue WHERE message_id = ?`, bad.ID).Scan(&attempts); err != nil {
		t.Fatalf("attempts read: %v", err)
	}
	if attempts != 1 {
		t.Fatalf("attempts = %d, want 1", attempts)
	}
	if n, _ := st.SearchQueueLen(); n != 1 {
		t.Fatalf("failed job was dropped: %d", n)
	}
	idx.mu.Lock()
	delete(idx.fail, bad.ID)
	idx.mu.Unlock()
	if done, err := st.ProcessSearchQueue(context.Background(), 10); err != nil || done != 1 {
		t.Fatalf("retry done=%d err=%v", done, err)
	}
	if n, _ := st.SearchQueueLen(); n != 0 {
		t.Fatalf("retry left jobs: %d", n)
	}

	// Deletes go through the queue as well.
	if _, err := st.DeleteMessage(ids[1]); err != nil {
		t.Fatalf("delete: %v", err)
	}
	var op string
	if err := st.DB.QueryRow(`SELECT op FROM search_queue WHERE message_id = ?`, ids[1]).Scan(&op); err != nil {
		t.Fatalf("delete job missing: %v", err)
	}
	if op != "delete" {
		t.Fatalf("delete job op = %q", op)
	}
	if done, err := st.ProcessSearchQueue(context.Background(), 10); err != nil || done != 1 {
		t.Fatalf("delete process done=%d err=%v", done, err)
	}
	idx.mu.Lock()
	_, stillThere := idx.docs[guild.ID][ids[1]]
	idx.mu.Unlock()
	if stillThere {
		t.Fatal("deleted message still indexed")
	}

	// The background runner drains on its own interval.
	late, err := st.CreateMessage(store.CreateMessageParams{
		ChannelID: channel.ID, GuildID: guild.ID, AuthorID: user.ID, Content: "runner",
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go st.RunSearchQueue(ctx, 10*time.Millisecond, 10)
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		idx.mu.Lock()
		_, ok := idx.docs[guild.ID][late.ID]
		idx.mu.Unlock()
		if ok {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("background runner never indexed the message")
}

// TestDurableSearchQueuePostgres runs the same queue flow against an isolated
// Postgres schema so the BIGSERIAL schema and pg_notify call are exercised.
func TestDurableSearchQueuePostgres(t *testing.T) {
	base := os.Getenv("WAVEYCORD_TEST_DSN")
	if !strings.HasPrefix(base, "postgres") {
		t.Skip("set WAVEYCORD_TEST_DSN to a postgres URL to run")
	}
	suffix := make([]byte, 4)
	if _, err := rand.Read(suffix); err != nil {
		t.Fatal(err)
	}
	schema := "wc_q_" + hex.EncodeToString(suffix)
	admin, err := sql.Open("pgx", base)
	if err != nil {
		t.Fatalf("admin: %v", err)
	}
	defer admin.Close()
	if _, err := admin.Exec(`CREATE SCHEMA ` + schema); err != nil {
		t.Fatalf("create schema: %v", err)
	}
	defer admin.Exec(`DROP SCHEMA ` + schema + ` CASCADE`)
	sep := "?"
	if strings.Contains(base, "?") {
		sep = "&"
	}
	handle, err := db.Open(base + sep + "search_path=" + schema)
	if err != nil {
		t.Fatalf("open scoped db: %v", err)
	}
	defer handle.Close()

	st := store.New(handle)
	user, err := st.CreateUser(store.CreateUserParams{
		Username: "pgqueue", Email: strPtr("pgqueue@example.test"), PasswordHash: "x",
	})
	if err != nil {
		t.Fatal(err)
	}
	guild, err := st.CreateGuild(user.ID, store.CreateGuildParams{Name: "PG Queue"})
	if err != nil {
		t.Fatal(err)
	}
	name := "chat"
	channel, err := st.CreateChannel(guild.ID, store.CreateChannelParams{Name: &name})
	if err != nil {
		t.Fatal(err)
	}
	idx := newCountingIndex()
	st.SetSearchIndex(idx)
	st.SetDurableSearchQueue(true)
	msg, err := st.CreateMessage(store.CreateMessageParams{
		ChannelID: channel.ID, GuildID: guild.ID, AuthorID: user.ID, Content: "pg queued",
	})
	if err != nil {
		t.Fatal(err)
	}
	if n, err := st.SearchQueueLen(); err != nil || n != 1 {
		t.Fatalf("queue len = %d err %v", n, err)
	}
	if done, err := st.ProcessSearchQueue(context.Background(), 10); err != nil || done != 1 {
		t.Fatalf("process done=%d err=%v", done, err)
	}
	if got := idx.count(guild.ID); got != 1 {
		t.Fatalf("indexed %d, want 1 (message %s)", got, msg.ID)
	}
	if _, err := st.DeleteMessage(msg.ID); err != nil {
		t.Fatal(err)
	}
	if done, err := st.ProcessSearchQueue(context.Background(), 10); err != nil || done != 1 {
		t.Fatalf("delete done=%d err=%v", done, err)
	}
	if got := idx.count(guild.ID); got != 0 {
		t.Fatalf("delete left %d docs", got)
	}
}

// TestListenSearchQueueRejectsSQLite proves the wake-up listener is Postgres-only.
func TestListenSearchQueueRejectsSQLite(t *testing.T) {
	handle, err := db.Open(filepath.Join(t.TempDir(), "listen.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer handle.Close()
	st := store.New(handle)
	if _, err := st.ListenSearchQueue(context.Background()); !errors.Is(err, store.ErrListenUnsupported) {
		t.Fatalf("listen on sqlite err = %v", err)
	}
}

// TestSearchQueueListenWakes proves a Postgres notification drains the queue without
// waiting for the poll interval: the runner uses a one-hour tick and still indexes
// the new message promptly.
func TestSearchQueueListenWakes(t *testing.T) {
	base := os.Getenv("WAVEYCORD_TEST_DSN")
	if !strings.HasPrefix(base, "postgres") {
		t.Skip("set WAVEYCORD_TEST_DSN to a postgres URL to run")
	}
	suffix := make([]byte, 4)
	if _, err := rand.Read(suffix); err != nil {
		t.Fatal(err)
	}
	schema := "wc_l_" + hex.EncodeToString(suffix)
	admin, err := sql.Open("pgx", base)
	if err != nil {
		t.Fatalf("admin: %v", err)
	}
	defer admin.Close()
	if _, err := admin.Exec(`CREATE SCHEMA ` + schema); err != nil {
		t.Fatalf("create schema: %v", err)
	}
	defer admin.Exec(`DROP SCHEMA ` + schema + ` CASCADE`)
	sep := "?"
	if strings.Contains(base, "?") {
		sep = "&"
	}
	scoped := base + sep + "search_path=" + schema
	handle, err := db.Open(scoped)
	if err != nil {
		t.Fatalf("open scoped db: %v", err)
	}
	defer handle.Close()

	st := store.New(handle)
	st.SetPostgresDSN(scoped)
	idx := newCountingIndex()
	st.SetSearchIndex(idx)
	st.SetDurableSearchQueue(true)

	user, err := st.CreateUser(store.CreateUserParams{
		Username: "listenuser", Email: strPtr("listen@example.test"), PasswordHash: "x",
	})
	if err != nil {
		t.Fatal(err)
	}
	guild, err := st.CreateGuild(user.ID, store.CreateGuildParams{Name: "Listen Guild"})
	if err != nil {
		t.Fatal(err)
	}
	name := "chat"
	channel, err := st.CreateChannel(guild.ID, store.CreateChannelParams{Name: &name})
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	wake, err := st.ListenSearchQueue(ctx)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	go st.RunSearchQueueWithWake(ctx, wake, time.Hour, 50)

	// The only drain trigger is a notification (the poll interval is an hour), but a
	// notification can be coalesced while the worker is between iterations, so retry
	// the enqueue a few times before declaring failure.
	for attempt := 0; attempt < 3; attempt++ {
		msg, err := st.CreateMessage(store.CreateMessageParams{
			ChannelID: channel.ID, GuildID: guild.ID, AuthorID: user.ID, Content: "notify me",
		})
		if err != nil {
			t.Fatal(err)
		}
		deadline := time.Now().Add(6 * time.Second)
		for time.Now().Before(deadline) {
			idx.mu.Lock()
			_, ok := idx.docs[guild.ID][msg.ID]
			idx.mu.Unlock()
			if ok {
				if n, _ := st.SearchQueueLen(); n != 0 {
					t.Fatalf("queue not drained after wake: %d", n)
				}
				return
			}
			time.Sleep(20 * time.Millisecond)
		}
	}
	t.Fatal("notification never woke the queue worker")
}
