package hashring

import "testing"

func TestStableAssignment(t *testing.T) {
	r := New(64)
	for _, m := range []string{"a:8080", "b:8080", "c:8080"} {
		r.Add(m)
	}
	first := map[string]string{}
	for i := 0; i < 2000; i++ {
		key := "channel:" + itoa(i)
		owner, ok := r.Get(key)
		if !ok {
			t.Fatal("no owner")
		}
		first[key] = owner
	}
	for key, want := range first {
		if got, _ := r.Get(key); got != want {
			t.Fatalf("key %s moved from %s to %s without membership change", key, want, got)
		}
	}
}

func TestRemovalMovesOnlyItsKeys(t *testing.T) {
	r := New(64)
	for _, m := range []string{"a", "b", "c", "d"} {
		r.Add(m)
	}
	before := map[string]string{}
	for i := 0; i < 4000; i++ {
		key := "guild:" + itoa(i)
		owner, _ := r.Get(key)
		before[key] = owner
	}
	r.Remove("b")
	moved, total := 0, 0
	for key, owner := range before {
		total++
		if owner != "b" {
			if now, _ := r.Get(key); now != owner {
				moved++
			}
		}
	}
	if total == 0 {
		t.Fatal("no keys")
	}
	// Keys not owned by the removed member must not move.
	if moved != 0 {
		t.Fatalf("%d/%d keys moved despite owning another member", moved, total)
	}
	r.Add("b")
	for key, owner := range before {
		if now, _ := r.Get(key); now != owner {
			t.Fatalf("key %s not restored after re-add: %s != %s", key, now, owner)
		}
	}
}

func TestDistribution(t *testing.T) {
	r := New(128)
	members := []string{"a", "b", "c"}
	for _, m := range members {
		r.Add(m)
	}
	counts := map[string]int{}
	for i := 0; i < 30000; i++ {
		owner, _ := r.Get("ch:" + itoa(i))
		counts[owner]++
	}
	for _, m := range members {
		if counts[m] < 5000 {
			t.Fatalf("member %s got %d keys, distribution too skewed: %v", m, counts[m], counts)
		}
	}
}
