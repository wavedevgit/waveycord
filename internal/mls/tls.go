package mls

import (
	"encoding/binary"
	"errors"
	"fmt"
	"sort"
)

// TLS presentation-language encoding for the encrypted GroupInfo payload. Values are
// length-prefixed as RFC 8446-style uint16 opaque/vector lengths; integers are
// network byte order. Only the fixed shape this package emits is supported, with
// strict length checks on decode.

var errTLS = errors.New("mls: malformed tls value")

type tlsWriter struct{ buf []byte }

func (w *tlsWriter) u8(v uint8)   { w.buf = append(w.buf, v) }
func (w *tlsWriter) u32(v uint32) { w.buf = binary.BigEndian.AppendUint32(w.buf, v) }

// opaque16 writes a uint16-length-prefixed byte string.
func (w *tlsWriter) opaque16(b []byte) error {
	if len(b) > 0xffff {
		return errTLS
	}
	w.buf = binary.BigEndian.AppendUint16(w.buf, uint16(len(b)))
	w.buf = append(w.buf, b...)
	return nil
}

func (w *tlsWriter) vector16(items int, each func(i int) error) error {
	if items > 0xffff {
		return errTLS
	}
	w.buf = binary.BigEndian.AppendUint16(w.buf, uint16(items))
	for i := 0; i < items; i++ {
		if err := each(i); err != nil {
			return err
		}
	}
	return nil
}

type tlsReader struct {
	data []byte
	off  int
}

func (r *tlsReader) remaining() int { return len(r.data) - r.off }

func (r *tlsReader) u8() (uint8, error) {
	if r.remaining() < 1 {
		return 0, errTLS
	}
	v := r.data[r.off]
	r.off++
	return v, nil
}

func (r *tlsReader) u32() (uint32, error) {
	if r.remaining() < 4 {
		return 0, errTLS
	}
	v := binary.BigEndian.Uint32(r.data[r.off:])
	r.off += 4
	return v, nil
}

func (r *tlsReader) opaque16() ([]byte, error) {
	if r.remaining() < 2 {
		return nil, errTLS
	}
	n := int(binary.BigEndian.Uint16(r.data[r.off:]))
	r.off += 2
	if r.remaining() < n {
		return nil, errTLS
	}
	out := append([]byte(nil), r.data[r.off:r.off+n]...)
	r.off += n
	return out, nil
}

func (r *tlsReader) vector16() (int, error) {
	if r.remaining() < 2 {
		return 0, errTLS
	}
	n := int(binary.BigEndian.Uint16(r.data[r.off:]))
	r.off += 2
	return n, nil
}

// encodeLeaf encodes one roster entry.
func encodeLeaf(w *tlsWriter, l Leaf) error {
	if err := w.opaque16([]byte(l.UserID)); err != nil {
		return err
	}
	return w.opaque16(l.Public[:])
}

func decodeLeaf(r *tlsReader) (Leaf, error) {
	var l Leaf
	uid, err := r.opaque16()
	if err != nil {
		return l, err
	}
	pub, err := r.opaque16()
	if err != nil {
		return l, err
	}
	if len(pub) != 32 {
		return l, errTLS
	}
	l.UserID = string(uid)
	copy(l.Public[:], pub)
	return l, nil
}

// EncodeGroupInfo serializes the group state with TLS presentation language.
func EncodeGroupInfo(g GroupInfo) ([]byte, error) {
	w := &tlsWriter{}
	w.u32(g.Epoch)
	if err := w.vector16(len(g.Roster), func(i int) error { return encodeLeaf(w, g.Roster[i]) }); err != nil {
		return nil, err
	}
	// TreeState: size, users, positions, node keys.
	size := 0
	var users []string
	var positions []int
	pubs := map[int][32]byte{}
	if g.Tree != nil {
		size = g.Tree.Size
		users = g.Tree.Users
		positions = g.Tree.Positions
		pubs = g.Tree.Publics
	}
	w.u32(uint32(size))
	if err := w.vector16(len(users), func(i int) error { return w.opaque16([]byte(users[i])) }); err != nil {
		return nil, err
	}
	if err := w.vector16(len(positions), func(i int) error { w.u32(uint32(positions[i])); return nil }); err != nil {
		return nil, err
	}
	nodes := make([]int, 0, len(pubs))
	for n := range pubs {
		nodes = append(nodes, n)
	}
	sort.Ints(nodes)
	if err := w.vector16(len(nodes), func(i int) error {
		w.u32(uint32(nodes[i]))
		pub := pubs[nodes[i]]
		return w.opaque16(pub[:])
	}); err != nil {
		return nil, err
	}
	if err := w.opaque16(g.Root); err != nil {
		return nil, err
	}
	keys := make([]int, 0, len(g.NodeKeys))
	for n := range g.NodeKeys {
		keys = append(keys, n)
	}
	sort.Ints(keys)
	if err := w.vector16(len(keys), func(i int) error {
		w.u32(uint32(keys[i]))
		return w.opaque16(g.NodeKeys[keys[i]])
	}); err != nil {
		return nil, err
	}
	return w.buf, nil
}

// DecodeGroupInfo parses the TLS presentation-language group state.
func DecodeGroupInfo(raw []byte) (GroupInfo, error) {
	var g GroupInfo
	r := &tlsReader{data: raw}
	epoch, err := r.u32()
	if err != nil {
		return g, err
	}
	g.Epoch = epoch
	n, err := r.vector16()
	if err != nil {
		return g, err
	}
	if n > r.remaining() {
		return g, errTLS
	}
	g.Roster = make([]Leaf, 0, n)
	for i := 0; i < n; i++ {
		l, err := decodeLeaf(r)
		if err != nil {
			return g, err
		}
		g.Roster = append(g.Roster, l)
	}
	size, err := r.u32()
	if err != nil {
		return g, err
	}
	tree := &TreeState{Size: int(size), Publics: map[int][32]byte{}}
	un, err := r.vector16()
	if err != nil {
		return g, err
	}
	if un > r.remaining() {
		return g, errTLS
	}
	for i := 0; i < un; i++ {
		u, err := r.opaque16()
		if err != nil {
			return g, err
		}
		tree.Users = append(tree.Users, string(u))
	}
	pn, err := r.vector16()
	if err != nil {
		return g, err
	}
	if pn > r.remaining() {
		return g, errTLS
	}
	for i := 0; i < pn; i++ {
		v, err := r.u32()
		if err != nil {
			return g, err
		}
		if int(v) >= tree.Size && tree.Size > 0 {
			return g, errTLS
		}
		tree.Positions = append(tree.Positions, int(v))
	}
	tn, err := r.vector16()
	if err != nil {
		return g, err
	}
	if tn > r.remaining() {
		return g, errTLS
	}
	for i := 0; i < tn; i++ {
		node, err := r.u32()
		if err != nil {
			return g, err
		}
		pub, err := r.opaque16()
		if err != nil {
			return g, err
		}
		if len(pub) != 32 {
			return g, errTLS
		}
		var arr [32]byte
		copy(arr[:], pub)
		tree.Publics[int(node)] = arr
	}
	root, err := r.opaque16()
	if err != nil {
		return g, err
	}
	if len(root) != 32 {
		return g, errTLS
	}
	g.Root = root
	kn, err := r.vector16()
	if err != nil {
		return g, err
	}
	if kn > r.remaining() {
		return g, errTLS
	}
	g.NodeKeys = map[int][]byte{}
	for i := 0; i < kn; i++ {
		node, err := r.u32()
		if err != nil {
			return g, err
		}
		key, err := r.opaque16()
		if err != nil {
			return g, err
		}
		g.NodeKeys[int(node)] = key
	}
	if r.remaining() != 0 {
		return g, fmt.Errorf("%w: %d trailing bytes", errTLS, r.remaining())
	}
	if len(tree.Users) != len(tree.Positions) {
		return g, errTLS
	}
	g.Tree = tree
	return g, nil
}
