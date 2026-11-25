package lb

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func backendServer(t *testing.T, name string) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"status":"ok"}`))
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Test-Name", name)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(name))
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func front(t *testing.T, b *Balancer) *httptest.Server {
	t.Helper()
	b.SetHealthInterval(50 * time.Millisecond)
	ctx, cancel := context.WithCancel(context.Background())
	b.Start(ctx)
	t.Cleanup(func() { cancel(); b.Close() })
	front := httptest.NewServer(b.Handler())
	t.Cleanup(front.Close)
	return front
}

func TestConsistentHashRouting(t *testing.T) {
	a := backendServer(t, "a")
	c := backendServer(t, "c")
	b, err := New([]string{a.URL, c.URL}, 128, nil)
	if err != nil {
		t.Fatal(err)
	}
	f := front(t, b)

	// One channel id must always reach the same backend.
	var first string
	for i := 0; i < 40; i++ {
		resp, err := http.Get(f.URL + "/api/v10/channels/123456789/messages")
		if err != nil {
			t.Fatal(err)
		}
		got := resp.Header.Get("X-Waveycord-Backend")
		resp.Body.Close()
		if got == "" {
			t.Fatal("missing backend header")
		}
		if first == "" {
			first = got
		} else if got != first {
			t.Fatalf("channel routed to %s then %s", first, got)
		}
	}

	// Distinct channels should use both backends.
	owners := map[string]bool{}
	for i := 0; i < 100; i++ {
		resp, err := http.Get(f.URL + "/api/v10/channels/" + itoaTest(i) + "/messages")
		if err != nil {
			t.Fatal(err)
		}
		owners[resp.Header.Get("X-Waveycord-Backend")] = true
		resp.Body.Close()
	}
	if len(owners) != 2 {
		t.Fatalf("expected both backends to own channels, got %v", owners)
	}
}

func TestDeadBackendRemoved(t *testing.T) {
	a := backendServer(t, "a")
	dead := backendServer(t, "dead")
	b, err := New([]string{a.URL, dead.URL}, 128, nil)
	if err != nil {
		t.Fatal(err)
	}
	f := front(t, b)
	dead.Close()

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		failures := 0
		for i := 0; i < 20; i++ {
			resp, err := http.Get(f.URL + "/api/v10/guilds/" + itoaTest(i) + "/channels")
			if err != nil {
				t.Fatal(err)
			}
			if resp.StatusCode == http.StatusBadGateway {
				failures++
			}
			resp.Body.Close()
		}
		if failures == 0 {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatal("dead backend still receiving traffic")
}

func itoaTest(n int) string {
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
