// Package hashring implements consistent hashing with virtual nodes, used to
// route channel-scoped reads to the same instance so request coalescing is
// effective (the same design the published message-storage post describes for
// data services).
package hashring

import (
	"hash/fnv"
	"sort"
	"sync"
)

// Ring maps arbitrary keys onto a fixed set of members.
type Ring struct {
	mu       sync.RWMutex
	replicas int
	keys     []uint64
	owners   map[uint64]string
	members  map[string]bool
}

// New builds an empty ring. replicas is the number of virtual nodes per member.
func New(replicas int) *Ring {
	if replicas <= 0 {
		replicas = 64
	}
	return &Ring{replicas: replicas, owners: map[uint64]string{}, members: map[string]bool{}}
}

// Add inserts a member.
func (r *Ring) Add(member string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if member == "" || r.members[member] {
		return
	}
	r.members[member] = true
	for i := 0; i < r.replicas; i++ {
		h := hashString(member + "#" + itoa(i))
		for r.owners[h] != "" {
			h++ // collision: probe forward
		}
		r.owners[h] = member
		r.keys = append(r.keys, h)
	}
	sort.Slice(r.keys, func(i, j int) bool { return r.keys[i] < r.keys[j] })
}

// Remove deletes a member and its virtual nodes.
func (r *Ring) Remove(member string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if !r.members[member] {
		return
	}
	delete(r.members, member)
	keys := r.keys[:0]
	for _, k := range r.keys {
		if r.owners[k] == member {
			delete(r.owners, k)
			continue
		}
		keys = append(keys, k)
	}
	r.keys = keys
}

// Get returns the member owning key.
func (r *Ring) Get(key string) (string, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if len(r.keys) == 0 {
		return "", false
	}
	h := hashString(key)
	idx := sort.Search(len(r.keys), func(i int) bool { return r.keys[i] >= h })
	if idx == len(r.keys) {
		idx = 0
	}
	return r.owners[r.keys[idx]], true
}

// Members lists the current members.
func (r *Ring) Members() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]string, 0, len(r.members))
	for m := range r.members {
		out = append(out, m)
	}
	sort.Strings(out)
	return out
}

// Len reports the member count.
func (r *Ring) Len() int {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return len(r.members)
}

// hashString runs FNV-1a through a splitmix64 finalizer. FNV alone leaves
// sequential keys clustered on the ring, which shows up as skewed assignment for
// ids that differ in their low decimal digits.
func hashString(s string) uint64 {
	h := fnv.New64a()
	_, _ = h.Write([]byte(s))
	v := h.Sum64()
	v ^= v >> 30
	v *= 0xbf58476d1ce4e5b9
	v ^= v >> 27
	v *= 0x94d049bb133111eb
	v ^= v >> 31
	return v
}

func itoa(n int) string {
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
