package search_test

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"regexp"
	"sort"
	"strings"
	"sync"
	"testing"

	"github.com/wavedevgit/waveycord/internal/search"
)

// fakeES implements the subset of the Elasticsearch REST API the adapter uses.
// It is a protocol double for unit tests, not Elasticsearch.
type fakeES struct {
	mu      sync.Mutex
	indices map[string]map[string]map[string]any
	created map[string]map[string]any
}

func newFakeES() *fakeES {
	return &fakeES{indices: map[string]map[string]map[string]any{}, created: map[string]map[string]any{}}
}

var esPath = regexp.MustCompile(`^/([^/?]+)(/.*)?$`)

func (f *fakeES) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	switch {
	case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/_cluster/health"):
		_, _ = w.Write([]byte(`{"status":"green"}`))
	case r.Method == http.MethodPut:
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		name := strings.TrimPrefix(r.URL.Path, "/")
		if _, exists := f.indices[name]; exists {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"error":{"type":"resource_already_exists_exception"}}`))
			return
		}
		f.indices[name] = map[string]map[string]any{}
		f.created[name] = body
		_, _ = w.Write([]byte(`{"acknowledged":true}`))
	case r.Method == http.MethodPost && r.URL.Path == "/_bulk":
		raw, _ := io.ReadAll(r.Body)
		lines := bytes.Split(bytes.TrimSpace(raw), []byte("\n"))
		errors := false
		for i := 0; i+1 < len(lines); i += 2 {
			var action map[string]map[string]any
			var source map[string]any
			if json.Unmarshal(lines[i], &action) != nil || json.Unmarshal(lines[i+1], &source) != nil {
				errors = true
				continue
			}
			meta := action["index"]
			if meta == nil {
				errors = true
				continue
			}
			index, _ := meta["_index"].(string)
			id, _ := meta["_id"].(string)
			if f.indices[index] == nil {
				f.indices[index] = map[string]map[string]any{}
			}
			f.indices[index][id] = source
		}
		_, _ = w.Write([]byte(`{"errors":false,"items":[]}`))
		_ = errors
	case r.Method == http.MethodDelete && !strings.HasSuffix(r.URL.Path, "/_delete_by_query"):
		name := strings.TrimPrefix(r.URL.Path, "/")
		prefixMatch := strings.HasSuffix(name, "*")
		base := strings.TrimSuffix(name, "*")
		deleted := 0
		for index := range f.indices {
			if prefixMatch && strings.HasPrefix(index, base) {
				delete(f.indices, index)
				deleted++
			} else if !prefixMatch && index == name {
				delete(f.indices, index)
				deleted++
			}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"acknowledged": true, "deleted": deleted})
	case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/_delete_by_query"):
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		var target any
		if q := body["query"].(map[string]any); q != nil {
			if term, ok := q["term"].(map[string]any); ok {
				target = term["message_id"]
			}
		}
		name := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/"), "/_delete_by_query")
		deleted := 0
		for index, docs := range f.indices {
			if name != "" && index != name && !strings.HasSuffix(name, "*") {
				continue
			}
			for id, doc := range docs {
				if target != nil && doc["message_id"] == target {
					delete(docs, id)
					deleted++
				}
			}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"deleted": deleted})
	case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/_search"):
		name := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/"), "/_search")
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		query := body["query"].(map[string]any)["bool"].(map[string]any)
		must := query["must"].([]any)[0].(map[string]any)["match"].(map[string]any)
		text := strings.ToLower(must["content"].(string))
		tokens := strings.Fields(text)
		var channelFilter map[string]bool
		var authorFilter string
		if filters, ok := query["filter"].([]any); ok {
			for _, raw := range filters {
				f := raw.(map[string]any)
				if terms, ok := f["terms"].(map[string]any); ok {
					channelFilter = map[string]bool{}
					for _, v := range terms["channel_id"].([]any) {
						channelFilter[v.(string)] = true
					}
				}
				if term, ok := f["term"].(map[string]any); ok {
					if v, ok := term["author_id"].(string); ok {
						authorFilter = v
					}
				}
			}
		}
		type hit struct {
			id    string
			score float64
			doc   map[string]any
		}
		var hits []hit
		for _, doc := range f.indices[name] {
			content := strings.ToLower(doc["content"].(string))
			score := 0.0
			for _, tok := range tokens {
				score += float64(strings.Count(content, tok))
			}
			if score == 0 {
				continue
			}
			if channelFilter != nil && !channelFilter[doc["channel_id"].(string)] {
				continue
			}
			if authorFilter != "" && doc["author_id"] != authorFilter {
				continue
			}
			hits = append(hits, hit{id: doc["message_id"].(string), score: score, doc: doc})
		}
		sort.Slice(hits, func(i, j int) bool {
			if hits[i].score != hits[j].score {
				return hits[i].score > hits[j].score
			}
			return hits[i].doc["created_at"].(float64) > hits[j].doc["created_at"].(float64)
		})
		size := len(hits)
		if v, ok := body["size"].(float64); ok && int(v) < size {
			size = int(v)
		}
		out := map[string]any{}
		hitList := make([]any, 0, size)
		for _, h := range hits[:size] {
			hitList = append(hitList, map[string]any{"_id": h.id, "_source": h.doc, "_score": h.score})
		}
		out["hits"] = map[string]any{
			"total": map[string]any{"value": len(hits), "relation": "eq"},
			"hits":  hitList,
		}
		_ = json.NewEncoder(w).Encode(out)
	default:
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"error":"not found"}`))
	}
}

func TestESAdapterGuildShardingAndRanking(t *testing.T) {
	fake := newFakeES()
	srv := httptest.NewServer(fake)
	defer srv.Close()

	es, err := search.NewES(search.ESConfig{
		URLs: []string{srv.URL}, Prefix: "test", Shards: 3, BatchSize: 10,
	})
	if err != nil {
		t.Fatalf("new es: %v", err)
	}
	defer es.Close()

	ctx := t.Context()
	docs := []search.Doc{
		{MessageID: "m1", GuildID: "a", ChannelID: "c1", AuthorID: "u1", Content: "tangerine harvest early", CreatedAt: 3},
		{MessageID: "m2", GuildID: "a", ChannelID: "c1", AuthorID: "u1", Content: "tangerine tangerine apricot", CreatedAt: 2},
		{MessageID: "m3", GuildID: "a", ChannelID: "c2", AuthorID: "u2", Content: "apricot only", CreatedAt: 1},
		{MessageID: "m4", GuildID: "b", ChannelID: "cb", AuthorID: "u9", Content: "tangerine secret", CreatedAt: 1},
	}
	for _, d := range docs {
		if err := es.Index(ctx, d); err != nil {
			t.Fatalf("index %s: %v", d.MessageID, err)
		}
	}
	if err := es.Flush(ctx); err != nil {
		t.Fatalf("flush: %v", err)
	}

	fake.mu.Lock()
	_, hasA := fake.indices["test-guild-a"]
	_, hasB := fake.indices["test-guild-b"]
	shards := fake.created["test-guild-a"]["settings"].(map[string]any)["number_of_shards"]
	fake.mu.Unlock()
	if !hasA || !hasB {
		t.Fatalf("expected per-guild indices, got %v", keys(fake.created))
	}
	if shards != float64(3) {
		t.Fatalf("number_of_shards = %v, want 3", shards)
	}

	// Ranked, guild-scoped result: m2 (two hits) before m1.
	res, err := es.Search(ctx, search.Query{GuildID: "a", Content: "tangerine", Channels: []string{"c1", "c2"}, Limit: 25})
	if err != nil {
		t.Fatalf("search A: %v", err)
	}
	if res.Total != 2 || len(res.MessageIDs) != 2 || res.MessageIDs[0] != "m2" || res.MessageIDs[1] != "m1" {
		t.Fatalf("ranked result = %+v, total=%d", res.MessageIDs, res.Total)
	}

	// Channel filter.
	res, err = es.Search(ctx, search.Query{GuildID: "a", Content: "tangerine", Channels: []string{"c2"}})
	if err != nil || res.Total != 0 {
		t.Fatalf("channel filter result=%+v err=%v", res, err)
	}

	// Shard isolation: guild B only sees its own document.
	res, err = es.Search(ctx, search.Query{GuildID: "b", Content: "tangerine", Channels: []string{"cb"}})
	if err != nil || res.Total != 1 || res.MessageIDs[0] != "m4" {
		t.Fatalf("guild B result=%+v err=%v", res, err)
	}

	// Author filter.
	res, err = es.Search(ctx, search.Query{GuildID: "a", Content: "tangerine", Channels: []string{"c1", "c2"}, AuthorID: "u2"})
	if err != nil || res.Total != 0 {
		t.Fatalf("author filter result=%+v err=%v", res, err)
	}

	// Deletion removes the document from the guild index.
	if err := es.Delete(ctx, "m2"); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if err := es.Flush(ctx); err != nil {
		t.Fatalf("flush after delete: %v", err)
	}
	res, err = es.Search(ctx, search.Query{GuildID: "a", Content: "tangerine", Channels: []string{"c1", "c2"}})
	if err != nil || res.Total != 1 || res.MessageIDs[0] != "m1" {
		t.Fatalf("after delete result=%+v err=%v", res, err)
	}

	if err := es.Health(ctx); err != nil {
		t.Fatalf("health: %v", err)
	}
}

func keys(m map[string]map[string]any) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
