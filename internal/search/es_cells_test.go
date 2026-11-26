package search_test

import (
	"net/http/httptest"
	"testing"

	"github.com/wavedevgit/waveycord/internal/search"
)

// TestMultiCellRouting proves guilds are assigned to independent Elasticsearch
// cells: indices live only in the owning cell, and ranked queries stay guild-scoped.
func TestMultiCellRouting(t *testing.T) {
	cellA := newFakeES()
	cellB := newFakeES()
	srvA := httptest.NewServer(cellA)
	defer srvA.Close()
	srvB := httptest.NewServer(cellB)
	defer srvB.Close()

	es, err := search.NewES(search.ESConfig{
		Cells:  [][]string{{srvA.URL}, {srvB.URL}},
		Prefix: "cells", Shards: 2, BatchSize: 4,
	})
	if err != nil {
		t.Fatalf("new es: %v", err)
	}
	defer es.Close()
	if es.CellCount() != 2 {
		t.Fatalf("cells = %d", es.CellCount())
	}

	// Find guild ids that hash to each cell.
	var guildA, guildB string
	for i := 0; i < 100 && (guildA == "" || guildB == ""); i++ {
		id := "g" + itoaTestSearch(i)
		switch es.CellIndex(id) {
		case 0:
			if guildA == "" {
				guildA = id
			}
		case 1:
			if guildB == "" {
				guildB = id
			}
		}
	}
	if guildA == "" || guildB == "" {
		t.Fatal("could not find guilds for both cells")
	}

	ctx := t.Context()
	docs := []search.Doc{
		{MessageID: "a1", GuildID: guildA, ChannelID: "c1", AuthorID: "u1", Content: "tangerine harvest", CreatedAt: 2},
		{MessageID: "a2", GuildID: guildA, ChannelID: "c1", AuthorID: "u1", Content: "tangerine tangerine", CreatedAt: 3},
		{MessageID: "b1", GuildID: guildB, ChannelID: "c2", AuthorID: "u2", Content: "tangerine secret", CreatedAt: 1},
	}
	for _, d := range docs {
		if err := es.Index(ctx, d); err != nil {
			t.Fatalf("index %s: %v", d.MessageID, err)
		}
	}
	if err := es.Flush(ctx); err != nil {
		t.Fatalf("flush: %v", err)
	}

	cellA.mu.Lock()
	_, aInA := cellA.indices["cells-guild-"+guildA]
	_, bInA := cellA.indices["cells-guild-"+guildB]
	countA := len(cellA.indices)
	cellA.mu.Unlock()
	cellB.mu.Lock()
	_, aInB := cellB.indices["cells-guild-"+guildA]
	_, bInB := cellB.indices["cells-guild-"+guildB]
	countB := len(cellB.indices)
	cellB.mu.Unlock()
	if !aInA || bInA || aInB || !bInB {
		t.Fatalf("cell isolation failed: A(a=%v,b=%v) B(a=%v,b=%v)", aInA, bInA, aInB, bInB)
	}
	if countA != 1 || countB != 1 {
		t.Fatalf("index counts A=%d B=%d", countA, countB)
	}

	// Guild-scoped ranked query only touches the owning cell.
	res, err := es.Search(ctx, search.Query{GuildID: guildA, Content: "tangerine", Channels: []string{"c1", "c2"}})
	if err != nil {
		t.Fatalf("search A: %v", err)
	}
	if res.Total != 2 || len(res.MessageIDs) != 2 || res.MessageIDs[0] != "a2" {
		t.Fatalf("guild A results = %+v", res)
	}
	res, err = es.Search(ctx, search.Query{GuildID: guildB, Content: "tangerine", Channels: []string{"c1", "c2"}})
	if err != nil || res.Total != 1 || res.MessageIDs[0] != "b1" {
		t.Fatalf("guild B results = %+v err=%v", res, err)
	}

	// A guild with no index in its cell is an empty result, not an error.
	res, err = es.Search(ctx, search.Query{GuildID: "unindexed-guild", Content: "tangerine", Channels: []string{"c1"}})
	if err != nil || res.Total != 0 {
		t.Fatalf("empty guild search = %+v err=%v", res, err)
	}

	// Deletion hits only the owning cell.
	if err := es.Delete(ctx, "a2"); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if err := es.Flush(ctx); err != nil {
		t.Fatalf("flush delete: %v", err)
	}
	res, _ = es.Search(ctx, search.Query{GuildID: guildA, Content: "tangerine", Channels: []string{"c1"}})
	if res.Total != 1 {
		t.Fatalf("after delete total = %d", res.Total)
	}

	// Reset clears every cell.
	if err := es.Reset(ctx); err != nil {
		t.Fatalf("reset: %v", err)
	}
	cellA.mu.Lock()
	leftA := len(cellA.indices)
	cellA.mu.Unlock()
	cellB.mu.Lock()
	leftB := len(cellB.indices)
	cellB.mu.Unlock()
	if leftA != 0 || leftB != 0 {
		t.Fatalf("reset left indices A=%d B=%d", leftA, leftB)
	}
	if err := es.Health(ctx); err != nil {
		t.Fatalf("health: %v", err)
	}
}

func itoaTestSearch(n int) string {
	if n == 0 {
		return "0"
	}
	var buf [12]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	return string(buf[i:])
}
