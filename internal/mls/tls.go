package mls

import (
	"crypto/ed25519"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"sort"
)

// TLS presentation-language helpers and the RFC-shaped GroupInfo encoding.

var errTLS = errors.New("mls: malformed tls value")

// groupInfoExtension carries the state this implementation needs that the RFC struct
// does not have a field for (roster, tree, root, node keys).
const groupInfoExtension = 0xF000

type tlsWriter struct{ buf []byte }

func (w *tlsWriter) u8(v uint8) { w.buf = append(w.buf, v) }
func (w *tlsWriter) u16(v uint16) {
	w.buf = binary.BigEndian.AppendUint16(w.buf, v)
}
func (w *tlsWriter) u32(v uint32) { w.buf = binary.BigEndian.AppendUint32(w.buf, v) }

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

func (r *tlsReader) u16() (uint16, error) {
	if r.remaining() < 2 {
		return 0, errTLS
	}
	v := binary.BigEndian.Uint16(r.data[r.off:])
	r.off += 2
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

func (r *tlsReader) u64() (uint64, error) {
	if r.remaining() < 8 {
		return 0, errTLS
	}
	v := binary.BigEndian.Uint64(r.data[r.off:])
	r.off += 8
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

func encodeLeaf(w *tlsWriter, l Leaf) error {
	if err := w.opaque16([]byte(l.UserID)); err != nil {
		return err
	}
	if err := w.opaque16(l.Public[:]); err != nil {
		return err
	}
	if err := w.opaque16(l.SignatureKey); err != nil {
		return err
	}
	return w.opaque16(l.KeyPackage)
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
	sigKey, err := r.opaque16()
	if err != nil {
		return l, err
	}
	if len(sigKey) != 0 && len(sigKey) != 32 {
		return l, errTLS
	}
	l.SignatureKey = ed25519.PublicKey(sigKey)
	pkg, err := r.opaque16()
	if err != nil {
		return l, err
	}
	l.KeyPackage = pkg
	return l, nil
}

func encodeTree(w *tlsWriter, t *TreeState) error {
	if t == nil {
		w.u32(0)
		return w.vector16(0, nil)
	}
	w.u32(uint32(t.Size))
	if err := w.opaque16(t.ConfirmedHash[:]); err != nil {
		return err
	}
	if err := w.vector16(len(t.Users), func(i int) error { return w.opaque16([]byte(t.Users[i])) }); err != nil {
		return err
	}
	if err := w.vector16(len(t.Positions), func(i int) error { w.u32(uint32(t.Positions[i])); return nil }); err != nil {
		return err
	}
	nodes := make([]int, 0, len(t.Publics))
	for n := range t.Publics {
		nodes = append(nodes, n)
	}
	sort.Ints(nodes)
	return w.vector16(len(nodes), func(i int) error {
		w.u32(uint32(nodes[i]))
		pub := t.Publics[nodes[i]]
		return w.opaque16(pub[:])
	})
}

func decodeTree(r *tlsReader) (*TreeState, error) {
	size, err := r.u32()
	if err != nil {
		return nil, err
	}
	t := &TreeState{Size: int(size), Publics: map[int][32]byte{}}
	ch, err := r.opaque16()
	if err != nil {
		return nil, err
	}
	if len(ch) != 32 {
		return nil, errTLS
	}
	copy(t.ConfirmedHash[:], ch)
	un, err := r.vector16()
	if err != nil {
		return nil, err
	}
	if un > r.remaining() {
		return nil, errTLS
	}
	for i := 0; i < un; i++ {
		u, err := r.opaque16()
		if err != nil {
			return nil, err
		}
		t.Users = append(t.Users, string(u))
	}
	pn, err := r.vector16()
	if err != nil {
		return nil, err
	}
	if pn > r.remaining() {
		return nil, errTLS
	}
	for i := 0; i < pn; i++ {
		v, err := r.u32()
		if err != nil {
			return nil, err
		}
		if t.Size > 0 && int(v) >= t.Size {
			return nil, errTLS
		}
		t.Positions = append(t.Positions, int(v))
	}
	tn, err := r.vector16()
	if err != nil {
		return nil, err
	}
	if tn > r.remaining() {
		return nil, errTLS
	}
	for i := 0; i < tn; i++ {
		node, err := r.u32()
		if err != nil {
			return nil, err
		}
		pub, err := r.opaque16()
		if err != nil {
			return nil, err
		}
		if len(pub) != 32 {
			return nil, errTLS
		}
		var arr [32]byte
		copy(arr[:], pub)
		t.Publics[int(node)] = arr
	}
	if t.Size == 0 {
		return nil, nil
	}
	if len(t.Users) != len(t.Positions) {
		return nil, errTLS
	}
	return t, nil
}

// groupContextBytes is the RFC GroupContext serialization.
func groupContextBytes(g GroupInfo) ([]byte, error) {
	w := &tlsWriter{}
	w.buf = binary.BigEndian.AppendUint16(w.buf, g.Version)
	w.buf = binary.BigEndian.AppendUint16(w.buf, uint16(g.Suite))
	if err := w.opaque16(g.GroupID); err != nil {
		return nil, err
	}
	w.buf = binary.BigEndian.AppendUint64(w.buf, uint64(g.Epoch))
	if err := w.opaque16(g.TreeHash); err != nil {
		return nil, err
	}
	if err := w.opaque16(g.Confirmed); err != nil {
		return nil, err
	}
	return w.buf, nil
}

// payloadExtension encodes the group state inside the custom extension.
func payloadExtension(g GroupInfo) ([]byte, error) {
	w := &tlsWriter{}
	if err := w.vector16(len(g.Roster), func(i int) error { return encodeLeaf(w, g.Roster[i]) }); err != nil {
		return nil, err
	}
	if err := encodeTree(w, g.Tree); err != nil {
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

func confirmationKey(root []byte) []byte { return ExpandWithLabel(root, "confirm", nil, 32) }

func groupInfoMAC(g GroupInfo, extData []byte) ([]byte, error) {
	gc, err := groupContextBytes(g)
	if err != nil {
		return nil, err
	}
	w := &tlsWriter{}
	w.buf = append(w.buf, gc...)
	w.buf = binary.BigEndian.AppendUint16(w.buf, 1)
	w.buf = binary.BigEndian.AppendUint16(w.buf, groupInfoExtension)
	if err := w.opaque16(extData); err != nil {
		return nil, err
	}
	mac := hmac.New(sha256.New, confirmationKey(g.Root))
	mac.Write(w.buf)
	return mac.Sum(nil), nil
}

// EncodeGroupInfo serializes the group state as an RFC-shaped GroupInfo.
func EncodeGroupInfo(g GroupInfo) ([]byte, error) {
	if g.Version == 0 {
		g.Version = 1
	}
	if g.Suite == 0 {
		g.Suite = SuiteX25519AES256GCM
	}
	if g.GroupID == nil {
		g.GroupID = []byte{}
	}
	if g.Confirmed == nil {
		g.Confirmed = make([]byte, 32)
	}
	if g.TreeHash == nil {
		tw := &tlsWriter{}
		if err := encodeTree(tw, g.Tree); err != nil {
			return nil, err
		}
		sum := sha256.Sum256(tw.buf)
		g.TreeHash = sum[:]
	}
	extData, err := payloadExtension(g)
	if err != nil {
		return nil, err
	}
	tag, err := groupInfoMAC(g, extData)
	if err != nil {
		return nil, err
	}
	w := &tlsWriter{}
	gc, err := groupContextBytes(g)
	if err != nil {
		return nil, err
	}
	w.buf = append(w.buf, gc...)
	w.buf = binary.BigEndian.AppendUint16(w.buf, 1)
	w.buf = binary.BigEndian.AppendUint16(w.buf, groupInfoExtension)
	if err := w.opaque16(extData); err != nil {
		return nil, err
	}
	if err := w.opaque16(tag); err != nil {
		return nil, err
	}
	w.buf = binary.BigEndian.AppendUint32(w.buf, g.Signer)
	return w.buf, nil
}

// DecodeGroupInfo parses and authenticates the RFC-shaped GroupInfo.
func DecodeGroupInfo(raw []byte) (GroupInfo, error) {
	var g GroupInfo
	r := &tlsReader{data: raw}
	version, err := r.u16()
	if err != nil {
		return g, err
	}
	suite, err := r.u16()
	if err != nil {
		return g, err
	}
	g.Version = version
	g.Suite = Ciphersuite(suite)
	if !g.Suite.Supported() {
		return g, errTLS
	}
	groupID, err := r.opaque16()
	if err != nil {
		return g, err
	}
	g.GroupID = groupID
	epoch, err := r.u64()
	if err != nil {
		return g, err
	}
	g.Epoch = uint32(epoch)
	treeHash, err := r.opaque16()
	if err != nil {
		return g, err
	}
	g.TreeHash = treeHash
	confirmed, err := r.opaque16()
	if err != nil {
		return g, err
	}
	g.Confirmed = confirmed
	extCount, err := r.vector16()
	if err != nil {
		return g, err
	}
	var extData []byte
	for i := 0; i < extCount; i++ {
		typ, err := r.u16()
		if err != nil {
			return g, err
		}
		data, err := r.opaque16()
		if err != nil {
			return g, err
		}
		if typ == groupInfoExtension {
			extData = data
		}
	}
	tag, err := r.opaque16()
	if err != nil {
		return g, err
	}
	signer, err := r.u32()
	if err != nil {
		return g, err
	}
	if r.remaining() != 0 || len(extData) == 0 {
		return g, errTLS
	}
	br := &tlsReader{data: extData}
	n, err := br.vector16()
	if err != nil {
		return g, err
	}
	if n > br.remaining() {
		return g, errTLS
	}
	g.Roster = make([]Leaf, 0, n)
	for i := 0; i < n; i++ {
		l, err := decodeLeaf(br)
		if err != nil {
			return g, err
		}
		g.Roster = append(g.Roster, l)
	}
	tree, err := decodeTree(br)
	if err != nil {
		return g, err
	}
	g.Tree = tree
	root, err := br.opaque16()
	if err != nil {
		return g, err
	}
	if len(root) != 32 {
		return g, errTLS
	}
	g.Root = root
	kn, err := br.vector16()
	if err != nil {
		return g, err
	}
	if kn > br.remaining() {
		return g, errTLS
	}
	g.NodeKeys = map[int][]byte{}
	for i := 0; i < kn; i++ {
		node, err := br.u32()
		if err != nil {
			return g, err
		}
		key, err := br.opaque16()
		if err != nil {
			return g, err
		}
		g.NodeKeys[int(node)] = key
	}
	if br.remaining() != 0 {
		return g, errTLS
	}
	tw := &tlsWriter{}
	if err := encodeTree(tw, g.Tree); err != nil {
		return g, err
	}
	sum := sha256.Sum256(tw.buf)
	if !hmac.Equal(sum[:], g.TreeHash) {
		return g, errTLS
	}
	expect, err := groupInfoMAC(g, extData)
	if err != nil {
		return g, err
	}
	if !hmac.Equal(expect, tag) {
		return g, errTLS
	}
	g.Tag = tag
	g.Signer = signer
	return g, nil
}

var _ = fmt.Sprintf
