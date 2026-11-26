package search_test

import (
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/wavedevgit/waveycord/internal/search"
)

func TestESDurableAcknowledgement(t *testing.T) {
	var fail atomic.Bool
	var writes atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Method == http.MethodPut && r.URL.Path == "/durable-guild-g" {
			w.Write([]byte(`{"acknowledged":true}`))
			return
		}
		writes.Add(1)
		if fail.Load() {
			w.WriteHeader(503)
			w.Write([]byte(`{"error":"unavailable"}`))
			return
		}
		w.Write([]byte(`{"result":"created","failures":[]}`))
	}))
	defer srv.Close()
	es, err := search.NewES(search.ESConfig{URLs: []string{srv.URL}, Prefix: "durable"})
	if err != nil {
		t.Fatal(err)
	}
	defer es.Close()
	doc := search.Doc{MessageID: "m", GuildID: "g", Content: "durable"}
	if err := es.IndexDurable(t.Context(), doc); err != nil {
		t.Fatal(err)
	}
	if writes.Load() != 1 {
		t.Fatal("write was not sent synchronously")
	}
	fail.Store(true)
	if err := es.IndexDurable(t.Context(), doc); err == nil {
		t.Fatal("index failure acknowledged")
	}
	if err := es.DeleteDurable(t.Context(), "m"); err == nil {
		t.Fatal("delete failure acknowledged")
	}
	fail.Store(false)
	if err := es.DeleteDurable(t.Context(), "m"); err != nil {
		t.Fatal(err)
	}
}
