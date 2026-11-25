package search_test

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/wavedevgit/waveycord/internal/search"
)

// TestESRealCluster runs the same sharding/ranking assertions against a live
// Elasticsearch or OpenSearch node when WAVEYCORD_TEST_ES_URL is set.
func TestESRealCluster(t *testing.T) {
	base := os.Getenv("WAVEYCORD_TEST_ES_URL")
	if base == "" {
		t.Skip("set WAVEYCORD_TEST_ES_URL to run against a real cluster")
	}
	suffix := make([]byte, 4)
	_, _ = rand.Read(suffix)
	prefix := "wc-it-" + hex.EncodeToString(suffix)
	es, err := search.NewES(search.ESConfig{
		URLs: []string{base}, Prefix: prefix, Shards: 2, BatchSize: 10,
		FlushInterval: 50 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("new es: %v", err)
	}
	defer es.Close()
	t.Cleanup(func() {
		req, _ := http.NewRequest(http.MethodDelete, strings.TrimRight(base, "/")+"/"+prefix+"-guild-*", nil)
		resp, err := http.DefaultClient.Do(req)
		if err == nil {
			resp.Body.Close()
		}
	})

	if err := es.Health(t.Context()); err != nil {
		t.Fatalf("health: %v", err)
	}
	docs := []search.Doc{
		{MessageID: "r1", GuildID: "a1", ChannelID: "c1", AuthorID: "u1", Content: "tangerine harvest early", CreatedAt: 3},
		{MessageID: "r2", GuildID: "a1", ChannelID: "c1", AuthorID: "u1", Content: "tangerine tangerine apricot", CreatedAt: 2},
		{MessageID: "r3", GuildID: "b1", ChannelID: "cb", AuthorID: "u9", Content: "tangerine secret", CreatedAt: 1},
	}
	for _, d := range docs {
		if err := es.Index(t.Context(), d); err != nil {
			t.Fatalf("index: %v", err)
		}
	}
	if err := es.Flush(t.Context()); err != nil {
		t.Fatalf("flush: %v", err)
	}

	waitTotal := func(guild string, want int) search.Result {
		deadline := time.Now().Add(15 * time.Second)
		var last search.Result
		for time.Now().Before(deadline) {
			res, err := es.Search(context.Background(), search.Query{
				GuildID: guild, Content: "tangerine", Channels: []string{"c1", "cb"},
			})
			if err != nil {
				t.Fatalf("search: %v", err)
			}
			last = res
			if res.Total == want {
				return res
			}
			time.Sleep(150 * time.Millisecond)
		}
		t.Fatalf("guild %s total = %d, want %d (last=%+v)", guild, last.Total, want, last)
		return last
	}

	res := waitTotal("a1", 2)
	if len(res.MessageIDs) != 2 || res.MessageIDs[0] != "r2" {
		t.Fatalf("ranked ids = %+v, want r2 first", res.MessageIDs)
	}
	res = waitTotal("b1", 1)
	if len(res.MessageIDs) != 1 || res.MessageIDs[0] != "r3" {
		t.Fatalf("shard isolation ids = %+v", res.MessageIDs)
	}

	if err := es.Delete(t.Context(), "r2"); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if err := es.Flush(t.Context()); err != nil {
		t.Fatalf("flush delete: %v", err)
	}
	waitTotal("a1", 1)
	fmt.Println("real cluster search verified")
}
