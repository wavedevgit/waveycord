package search_test

import (
	"context"
	"net/http/httptest"
	"testing"

	"github.com/wavedevgit/waveycord/internal/search"
)

// TestBFGPolicy pins the drift thresholds.
func TestBFGPolicy(t *testing.T) {
	cases := []struct {
		name    string
		policy  search.BFGPolicy
		rows    int
		indexed int
		want    bool
	}{
		{"below threshold", search.BFGPolicy{Threshold: 100, Drift: 0.1}, 10, 0, false},
		{"above threshold exact", search.BFGPolicy{Threshold: 3, Drift: 0.1}, 10, 10, false},
		{"above threshold drift", search.BFGPolicy{Threshold: 3, Drift: 0.1}, 10, 5, true},
		{"negative drift", search.BFGPolicy{Threshold: 3, Drift: 0.1}, 10, 15, true},
		{"at tolerance", search.BFGPolicy{Threshold: 3, Drift: 0.2}, 10, 8, false},
		{"default drift", search.BFGPolicy{Threshold: 1}, 100, 50, true},
		{"no rows", search.BFGPolicy{Threshold: 1}, 0, 5, false},
	}
	for _, tc := range cases {
		if got := tc.policy.NeedsRebuild(tc.rows, tc.indexed); got != tc.want {
			t.Fatalf("%s: needs rebuild %v, want %v", tc.name, got, tc.want)
		}
	}
}

// TestBFGSizePolicy pins index-size automation thresholds.
func TestBFGSizePolicy(t *testing.T) {
	base := search.BFGPolicy{MaxDocs: 100, Growth: 0.25}
	cases := []struct {
		name   string
		policy search.BFGPolicy
		rows   int
		last   int
		want   bool
	}{
		{"disabled", search.BFGPolicy{}, 1000, 0, false},
		{"below max", base, 99, 0, false},
		{"first crossing", base, 100, 0, true},
		{"growth reached", base, 125, 100, true},
		{"below growth", base, 124, 100, false},
	}
	for _, tc := range cases {
		if got := tc.policy.NeedsSizeRebuild(tc.rows, tc.last); got != tc.want {
			t.Fatalf("%s: size rebuild %v, want %v", tc.name, got, tc.want)
		}
	}
}

// TestESGuildCountAndReset proves the per-guild index size read and the per-guild
// reset used by the BFG rebuild path.
func TestESGuildCountAndReset(t *testing.T) {
	fake := newFakeES()
	srv := httptest.NewServer(fake)
	defer srv.Close()
	es, err := search.NewES(search.ESConfig{URLs: []string{srv.URL}, Prefix: "bfg", Shards: 1, BatchSize: 2})
	if err != nil {
		t.Fatal(err)
	}
	defer es.Close()
	ctx := context.Background()

	if n, err := es.GuildDocCount(ctx, "missing"); err != nil || n != 0 {
		t.Fatalf("missing index count = %d err %v", n, err)
	}
	for _, doc := range []search.Doc{
		{MessageID: "a1", GuildID: "g1", ChannelID: "c", AuthorID: "u", Content: "alpha", CreatedAt: 1},
		{MessageID: "a2", GuildID: "g1", ChannelID: "c", AuthorID: "u", Content: "beta", CreatedAt: 2},
		{MessageID: "b1", GuildID: "g2", ChannelID: "c", AuthorID: "u", Content: "gamma", CreatedAt: 3},
	} {
		if err := es.Index(ctx, doc); err != nil {
			t.Fatalf("index %s: %v", doc.MessageID, err)
		}
	}
	if err := es.Flush(ctx); err != nil {
		t.Fatalf("flush: %v", err)
	}
	if n, err := es.GuildDocCount(ctx, "g1"); err != nil || n != 2 {
		t.Fatalf("g1 count = %d err %v", n, err)
	}
	if n, err := es.GuildDocCount(ctx, "g2"); err != nil || n != 1 {
		t.Fatalf("g2 count = %d err %v", n, err)
	}

	// Resetting one guild must not touch the other.
	if err := es.ResetGuild(ctx, "g1"); err != nil {
		t.Fatalf("reset g1: %v", err)
	}
	if n, _ := es.GuildDocCount(ctx, "g1"); n != 0 {
		t.Fatalf("g1 count after reset = %d", n)
	}
	if n, _ := es.GuildDocCount(ctx, "g2"); n != 1 {
		t.Fatalf("g2 count after g1 reset = %d", n)
	}
}
