package store_test

import (
	"context"
	"errors"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/wavedevgit/waveycord/internal/db"
	"github.com/wavedevgit/waveycord/internal/search"
	"github.com/wavedevgit/waveycord/internal/store"
)

// countingIndex is a search index that tracks documents per guild and implements the
// optional BFG interfaces.
type countingIndex struct {
	mu   sync.Mutex
	docs map[string]map[string]search.Doc
	fail map[string]bool
}

func newCountingIndex() *countingIndex {
	return &countingIndex{docs: map[string]map[string]search.Doc{}, fail: map[string]bool{}}
}

func (c *countingIndex) Index(_ context.Context, doc search.Doc) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.fail[doc.MessageID] {
		return errInjected
	}
	if c.docs[doc.GuildID] == nil {
		c.docs[doc.GuildID] = map[string]search.Doc{}
	}
	c.docs[doc.GuildID][doc.MessageID] = doc
	return nil
}

func (c *countingIndex) Delete(_ context.Context, messageID string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, guild := range c.docs {
		delete(guild, messageID)
	}
	return nil
}

func (c *countingIndex) Search(context.Context, search.Query) (search.Result, error) {
	return search.Result{}, nil
}

func (c *countingIndex) GuildDocCount(_ context.Context, guildID string) (int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.docs[guildID]), nil
}

func (c *countingIndex) ResetGuild(_ context.Context, guildID string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.docs, guildID)
	return nil
}

func (c *countingIndex) dropOne(guildID string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for id := range c.docs[guildID] {
		delete(c.docs[guildID], id)
		return
	}
}

func (c *countingIndex) count(guildID string) int {
	n, _ := c.GuildDocCount(context.Background(), guildID)
	return n
}

// plainIndex deliberately omits the BFG interfaces.
type plainIndex struct{ n int }

func (p *plainIndex) Index(context.Context, search.Doc) error { p.n++; return nil }
func (p *plainIndex) Delete(context.Context, string) error    { return nil }
func (p *plainIndex) Search(context.Context, search.Query) (search.Result, error) {
	return search.Result{}, nil
}

func TestBFGMaintainGuildRebuildsDriftedIndex(t *testing.T) {
	handle, err := db.Open(filepath.Join(t.TempDir(), "bfg.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer handle.Close()
	st := store.New(handle)

	user, err := st.CreateUser(store.CreateUserParams{
		Username: "bfguser", Email: strPtr("bfg@example.test"), PasswordHash: "x",
	})
	if err != nil {
		t.Fatalf("create user: %v", err)
	}
	guild, err := st.CreateGuild(user.ID, store.CreateGuildParams{Name: "BFG Guild"})
	if err != nil {
		t.Fatalf("create guild: %v", err)
	}
	name := "chat"
	channel, err := st.CreateChannel(guild.ID, store.CreateChannelParams{Name: &name})
	if err != nil {
		t.Fatalf("create channel: %v", err)
	}

	idx := newCountingIndex()
	st.SetSearchIndex(idx)
	for i := 0; i < 4; i++ {
		if _, err := st.CreateMessage(store.CreateMessageParams{
			ChannelID: channel.ID, GuildID: guild.ID, AuthorID: user.ID,
			Content: "message " + string(rune('a'+i)),
		}); err != nil {
			t.Fatalf("create message %d: %v", i, err)
		}
	}
	if got := idx.count(guild.ID); got != 4 {
		t.Fatalf("indexed %d, want 4", got)
	}

	// Under the threshold the incremental path is trusted even when it drifted.
	idx.dropOne(guild.ID)
	rebuilt, n, err := st.BFGMaintainGuild(context.Background(), guild.ID, search.BFGPolicy{Threshold: 10}, 2)
	if err != nil || rebuilt || n != 3 {
		t.Fatalf("below threshold rebuilt=%v n=%d err=%v", rebuilt, n, err)
	}

	// Above the threshold the drift triggers a per-guild reset and rebuild.
	rebuilt, n, err = st.BFGMaintainGuild(context.Background(), guild.ID, search.BFGPolicy{Threshold: 3, Drift: 0.1}, 2)
	if err != nil || !rebuilt || n != 4 {
		t.Fatalf("rebuild rebuilt=%v n=%d err=%v", rebuilt, n, err)
	}
	if got := idx.count(guild.ID); got != 4 {
		t.Fatalf("after rebuild indexed %d, want 4", got)
	}

	// A second pass sees a consistent index and does nothing.
	rebuilt, n, err = st.BFGMaintainGuild(context.Background(), guild.ID, search.BFGPolicy{Threshold: 3, Drift: 0.1}, 2)
	if err != nil || rebuilt || n != 4 {
		t.Fatalf("second pass rebuilt=%v n=%d err=%v", rebuilt, n, err)
	}

	// Over-indexed drift is also corrected.
	if err := idx.Index(context.Background(), search.Doc{
		MessageID: "phantom", GuildID: guild.ID, ChannelID: channel.ID, AuthorID: user.ID,
		Content: "ghost", CreatedAt: 9,
	}); err != nil {
		t.Fatal(err)
	}
	rebuilt, _, err = st.BFGMaintainGuild(context.Background(), guild.ID, search.BFGPolicy{Threshold: 3, Drift: 0.1}, 2)
	if err != nil || !rebuilt {
		t.Fatalf("over-indexed rebuild rebuilt=%v err=%v", rebuilt, err)
	}
	if got := idx.count(guild.ID); got != 4 {
		t.Fatalf("over-indexed rebuild count %d", got)
	}

	// An index without the optional interfaces opts out.
	st.SetSearchIndex(&plainIndex{})
	if rebuilt, _, err := st.BFGMaintainGuild(context.Background(), guild.ID, search.BFGPolicy{Threshold: 1}, 2); err != nil || rebuilt {
		t.Fatalf("plain index rebuilt=%v err=%v", rebuilt, err)
	}
}

func strPtr(s string) *string { return &s }

var errInjected = errors.New("injected index failure")

// TestBFGWriteTrigger proves the opt-in write-triggered check rebuilds a drifted
// guild in the background.
func TestBFGWriteTrigger(t *testing.T) {
	handle, err := db.Open(filepath.Join(t.TempDir(), "bfgtrigger.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer handle.Close()
	st := store.New(handle)
	user, err := st.CreateUser(store.CreateUserParams{
		Username: "triggeruser", Email: strPtr("trigger@example.test"), PasswordHash: "x",
	})
	if err != nil {
		t.Fatal(err)
	}
	guild, err := st.CreateGuild(user.ID, store.CreateGuildParams{Name: "Trigger Guild"})
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
	st.SetBFGPolicy(search.BFGPolicy{Threshold: 1, Drift: 0.1}, 1)
	for i := 0; i < 3; i++ {
		if _, err := st.CreateMessage(store.CreateMessageParams{
			ChannelID: channel.ID, GuildID: guild.ID, AuthorID: user.ID, Content: "seed",
		}); err != nil {
			t.Fatal(err)
		}
	}
	idx.dropOne(guild.ID)
	if _, err := st.CreateMessage(store.CreateMessageParams{
		ChannelID: channel.ID, GuildID: guild.ID, AuthorID: user.ID, Content: "trigger",
	}); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if idx.count(guild.ID) == 4 {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("index never rebuilt: %d docs, want 4", idx.count(guild.ID))
}
