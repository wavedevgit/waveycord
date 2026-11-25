package api_test

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/wavedevgit/waveycord/internal/search"
)

// TestSearchReindexRebuildsIndex proves the reindex workflow restores search after
// the index is wiped, using the Postgres tsvector backend.
func TestSearchReindexRebuildsIndex(t *testing.T) {
	dsn := os.Getenv("WAVEYCORD_TEST_DSN")
	if !strings.HasPrefix(dsn, "postgres") {
		t.Skip("set WAVEYCORD_TEST_DSN to a postgres URL to run")
	}
	h := newHarness(t)
	alice := h.register("reindexowner")
	var guild struct{ ID string }
	h.decode("POST", "/guilds", alice.Token, map[string]any{"name": "Reindex"}, &guild)
	var channel struct{ ID string }
	h.decode("POST", "/guilds/"+guild.ID+"/channels", alice.Token, map[string]any{"name": "chat"}, &channel)
	for i := 0; i < 3; i++ {
		h.request("POST", "/channels/"+channel.ID+"/messages", alice.Token,
			map[string]any{"content": "reindexable content"})
	}
	total := func() int {
		var res struct {
			Total int `json:"total_results"`
		}
		h.decode("GET", "/guilds/"+guild.ID+"/messages/search?content=reindexable", alice.Token, nil, &res)
		return res.Total
	}
	if total() != 3 {
		t.Fatalf("initial search total = %d", total())
	}
	if _, err := h.store.DB.Exec(`DELETE FROM search_documents`); err != nil {
		t.Fatalf("wipe index: %v", err)
	}
	if got := total(); got != 0 {
		t.Fatalf("search after wipe = %d, want 0", got)
	}
	n, err := h.store.ReindexSearch(context.Background(), false, 100, time.Minute)
	if err != nil {
		t.Fatalf("reindex: %v", err)
	}
	if n < 3 {
		t.Fatalf("reindexed %d documents, want >= 3", n)
	}
	if got := total(); got != 3 {
		t.Fatalf("search after reindex = %d, want 3", got)
	}
}

// TestESReindexWorkflow proves reset + reindex against a live Elasticsearch/
// OpenSearch node rebuilds the guild-sharded indices from message rows.
func TestESReindexWorkflow(t *testing.T) {
	dsn := os.Getenv("WAVEYCORD_TEST_DSN")
	esURL := os.Getenv("WAVEYCORD_TEST_ES_URL")
	if !strings.HasPrefix(dsn, "postgres") || esURL == "" {
		t.Skip("set WAVEYCORD_TEST_DSN and WAVEYCORD_TEST_ES_URL to run")
	}
	h := newHarness(t)
	alice := h.register("esreindex")
	var guild struct{ ID string }
	h.decode("POST", "/guilds", alice.Token, map[string]any{"name": "ES Reindex"}, &guild)
	var channel struct{ ID string }
	h.decode("POST", "/guilds/"+guild.ID+"/channels", alice.Token, map[string]any{"name": "chat"}, &channel)

	prefix := "wc-reindex-" + randomSuffix(t)
	es, err := search.NewES(search.ESConfig{URLs: []string{esURL}, Prefix: prefix, BatchSize: 10, FlushInterval: 50 * time.Millisecond})
	if err != nil {
		t.Fatalf("es: %v", err)
	}
	defer es.Close()
	h.store.SetSearchIndex(es)
	t.Cleanup(func() { _ = es.Reset(context.Background()) })

	for i := 0; i < 3; i++ {
		h.request("POST", "/channels/"+channel.ID+"/messages", alice.Token,
			map[string]any{"content": "opensearchable note"})
	}
	if err := es.Flush(context.Background()); err != nil {
		t.Fatalf("flush: %v", err)
	}
	waitTotal := func(want int) {
		t.Helper()
		deadline := time.Now().Add(15 * time.Second)
		for time.Now().Before(deadline) {
			var res struct {
				Total int `json:"total_results"`
			}
			h.decode("GET", "/guilds/"+guild.ID+"/messages/search?content=opensearchable", alice.Token, nil, &res)
			if res.Total == want {
				return
			}
			time.Sleep(150 * time.Millisecond)
		}
		t.Fatalf("search total never reached %d", want)
	}
	waitTotal(3)

	if err := es.Reset(context.Background()); err != nil {
		t.Fatalf("reset: %v", err)
	}
	waitTotal(0)

	n, err := h.store.ReindexSearch(context.Background(), true, 100, 2*time.Minute)
	if err != nil {
		t.Fatalf("reindex: %v", err)
	}
	if n < 3 {
		t.Fatalf("reindexed %d documents, want >= 3", n)
	}
	if err := es.Flush(context.Background()); err != nil {
		t.Fatalf("flush after reindex: %v", err)
	}
	waitTotal(3)
}
