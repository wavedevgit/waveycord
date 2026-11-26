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
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		if idx.count(guild.ID) == 4 {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("index never rebuilt: %d docs, want 4", idx.count(guild.ID))
}

// TestBFGSizeAutomationRebuildsGrowingGuild proves index-size automation: once a
// guild reaches MaxDocs it is rebuilt, then rebuilt again only after it grows by the
// configured fraction past the size captured at the previous rebuild.
func TestBFGSizeAutomationRebuildsGrowingGuild(t *testing.T) {
	handle, err := db.Open(filepath.Join(t.TempDir(), "bfgsize.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer handle.Close()
	st := store.New(handle)
	user, err := st.CreateUser(store.CreateUserParams{
		Username: "sizeuser", Email: strPtr("size@example.test"), PasswordHash: "x",
	})
	if err != nil {
		t.Fatal(err)
	}
	guild, err := st.CreateGuild(user.ID, store.CreateGuildParams{Name: "Size Guild"})
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
	// Drift rebuilds disabled (huge threshold); only size automation can fire.
	policy := search.BFGPolicy{Threshold: 100000, Drift: 0.1, MaxDocs: 4, Growth: 1.0}

	add := func(n int) {
		t.Helper()
		for i := 0; i < n; i++ {
			if _, err := st.CreateMessage(store.CreateMessageParams{
				ChannelID: channel.ID, GuildID: guild.ID, AuthorID: user.ID, Content: "grow",
			}); err != nil {
				t.Fatalf("create message: %v", err)
			}
		}
	}

	add(4)
	rebuilt, n, err := st.BFGMaintainGuild(context.Background(), guild.ID, policy, 10)
	if err != nil || !rebuilt || n != 4 {
		t.Fatalf("first size rebuild rebuilt=%v n=%d err=%v", rebuilt, n, err)
	}

	// 6 rows is above MaxDocs but below 4 * 2, so no rebuild.
	add(2)
	rebuilt, _, err = st.BFGMaintainGuild(context.Background(), guild.ID, policy, 10)
	if err != nil || rebuilt {
		t.Fatalf("rebuild below growth rebuilt=%v err=%v", rebuilt, err)
	}

	// 8 rows crosses the growth target measured from the last rebuild.
	add(2)
	rebuilt, n, err = st.BFGMaintainGuild(context.Background(), guild.ID, policy, 10)
	if err != nil || !rebuilt || n != 8 {
		t.Fatalf("growth rebuild rebuilt=%v n=%d err=%v", rebuilt, n, err)
	}

	// Below MaxDocs the size path is inert even when drift rebuilds are enabled.
	small, err := st.CreateGuild(user.ID, store.CreateGuildParams{Name: "Small Guild"})
	if err != nil {
		t.Fatal(err)
	}
	smallCh, err := st.CreateChannel(small.ID, store.CreateChannelParams{Name: &name})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.CreateMessage(store.CreateMessageParams{
		ChannelID: smallCh.ID, GuildID: small.ID, AuthorID: user.ID, Content: "one",
	}); err != nil {
		t.Fatal(err)
	}
	rebuilt, _, err = st.BFGMaintainGuild(context.Background(), small.ID,
		search.BFGPolicy{Threshold: 100000, MaxDocs: 1000}, 10)
	if err != nil || rebuilt {
		t.Fatalf("small guild rebuilt=%v err=%v", rebuilt, err)
	}
}

// TestBFGSizeStatePersistsAcrossReopen proves the rebuild baseline is stored in the
// database, so a reopened store does not immediately rebuild again and a second
// instance sees the same state.
func TestBFGSizeStatePersistsAcrossReopen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "bfgpersist.db")
	handle, err := db.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	st := store.New(handle)
	user, err := st.CreateUser(store.CreateUserParams{
		Username: "persistuser", Email: strPtr("persist@example.test"), PasswordHash: "x",
	})
	if err != nil {
		t.Fatal(err)
	}
	guild, err := st.CreateGuild(user.ID, store.CreateGuildParams{Name: "Persist Guild"})
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
	policy := search.BFGPolicy{Threshold: 100000, MaxDocs: 4, Growth: 1.0}
	for i := 0; i < 4; i++ {
		if _, err := st.CreateMessage(store.CreateMessageParams{
			ChannelID: channel.ID, GuildID: guild.ID, AuthorID: user.ID, Content: "seed",
		}); err != nil {
			t.Fatal(err)
		}
	}
	if rebuilt, n, err := st.BFGMaintainGuild(context.Background(), guild.ID, policy, 10); err != nil || !rebuilt || n != 4 {
		t.Fatalf("initial rebuild rebuilt=%v n=%d err=%v", rebuilt, n, err)
	}
	handle.Close()

	// Reopen the same database; the persisted baseline must suppress an immediate
	// rebuild at the same size.
	handle2, err := db.Open(path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer handle2.Close()
	st2 := store.New(handle2)
	idx2 := newCountingIndex()
	st2.SetSearchIndex(idx2)
	rebuilt, _, err := st2.BFGMaintainGuild(context.Background(), guild.ID, policy, 10)
	if err != nil || rebuilt {
		t.Fatalf("reopened store rebuilt at unchanged size: rebuilt=%v err=%v", rebuilt, err)
	}
	// Growing past the persisted baseline triggers a rebuild again.
	for i := 0; i < 4; i++ {
		if _, err := st2.CreateMessage(store.CreateMessageParams{
			ChannelID: channel.ID, GuildID: guild.ID, AuthorID: user.ID, Content: "grow",
		}); err != nil {
			t.Fatal(err)
		}
	}
	rebuilt, n, err := st2.BFGMaintainGuild(context.Background(), guild.ID, policy, 10)
	if err != nil || !rebuilt || n != 8 {
		t.Fatalf("growth after reopen rebuilt=%v n=%d err=%v", rebuilt, n, err)
	}
}
