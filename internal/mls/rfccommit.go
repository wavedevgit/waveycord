package mls

import (
	"crypto/sha256"
	"encoding/binary"
	"sort"
)

// RFC-shaped commit authentication. The signed content is
//
//	GroupContext || Commit
//
// where GroupContext carries the version, ciphersuite, group id, epoch, tree hash
// and the chained confirmed transcript hash, and the Commit body is serialized with
// TLS presentation-language framing. The state this implementation needs beyond the
// RFC Commit struct (roster, tree, envelopes, welcome, tree publics) travels as one
// custom proposal (type 0xFFFF) inside the commit, so it is covered by the signature.

const proposalTypeCustom = 0xFFFF

// groupContext commits to the previous epoch's tree and the transcript chain.
func groupContextBytesForCommit(c Commit) []byte {
	w := &tlsWriter{}
	w.buf = binary.BigEndian.AppendUint16(w.buf, 1)
	w.buf = binary.BigEndian.AppendUint16(w.buf, uint16(c.CipherSuite))
	_ = w.opaque16(c.GroupID)
	w.buf = binary.BigEndian.AppendUint64(w.buf, uint64(c.Epoch))
	_ = w.opaque16(c.TreeHash[:])
	_ = w.opaque16(c.ConfirmedHash[:])
	return w.buf
}

// encodeCommitProposal writes one proposal in RFC Proposal choice form.
func encodeCommitProposal(w *tlsWriter, p Proposal, leaf int) error {
	switch p.Type {
	case "add":
		w.u8(1)  // proposal by value
		w.u16(1) // Add
		if err := w.opaque16([]byte(p.UserID)); err != nil {
			return err
		}
		return w.opaque16(p.Public[:])
	case "remove":
		w.u8(1)
		w.u16(2) // Remove
		w.u32(uint32(leaf))
		return nil
	case "ciphersuite":
		w.u8(1)
		w.u16(proposalTypeCustom)
		w.u16(1) // custom proposal id: ciphersuite
		w.u32(uint32(p.Suite))
		return nil
	default:
		return errTLS
	}
}

// encodeImplementationState carries the extra group state inside the commit.
func encodeImplementationState(c Commit) ([]byte, error) {
	w := &tlsWriter{}
	if err := w.vector16(len(c.Roster), func(i int) error { return encodeLeaf(w, c.Roster[i]) }); err != nil {
		return nil, err
	}
	if err := encodeTree(w, c.Tree); err != nil {
		return nil, err
	}
	nodes := make([]int, 0, len(c.TreePublics))
	for n := range c.TreePublics {
		nodes = append(nodes, n)
	}
	sort.Ints(nodes)
	if err := w.vector16(len(nodes), func(i int) error {
		w.u32(uint32(nodes[i]))
		pub := c.TreePublics[nodes[i]]
		return w.opaque16(pub[:])
	}); err != nil {
		return nil, err
	}
	users := make([]string, 0, len(c.Envelopes))
	for u := range c.Envelopes {
		users = append(users, u)
	}
	sort.Strings(users)
	if err := w.vector16(len(users), func(i int) error {
		if err := w.opaque16([]byte(users[i])); err != nil {
			return err
		}
		return encodeEnvelope(w, c.Envelopes[users[i]])
	}); err != nil {
		return nil, err
	}
	return w.buf, nil
}

// encodeRFCCommitBody serializes the commit body covered by the signature.
func encodeRFCCommitBody(c Commit, leafOf func(string) int) ([]byte, error) {
	w := &tlsWriter{}
	// proposals: by-value proposals and the implementation-state extension.
	w.buf = binary.BigEndian.AppendUint16(w.buf, 0) // placeholder count
	count := 0
	var entries [][]byte
	for _, p := range c.Proposals {
		pw := &tlsWriter{}
		leaf := 0
		if p.Type == "remove" {
			leaf = leafOf(p.UserID)
		}
		if err := encodeCommitProposal(pw, p, leaf); err != nil {
			return nil, err
		}
		entries = append(entries, pw.buf)
		count++
	}
	state, err := encodeImplementationState(c)
	if err != nil {
		return nil, err
	}
	sw := &tlsWriter{}
	sw.u8(1)
	sw.u16(proposalTypeCustom)
	sw.u16(2) // custom proposal id: implementation state
	if err := sw.opaque16(state); err != nil {
		return nil, err
	}
	entries = append(entries, sw.buf)
	count++
	binary.BigEndian.PutUint16(w.buf, uint16(count))
	for _, e := range entries {
		w.buf = append(w.buf, e...)
	}
	// path optional: present when the commit carries one.
	if c.Path != nil {
		w.u8(1)
		w.u32(uint32(c.Path.CommitterLeaf))
		recips := append([]PathRecipient(nil), c.Path.Recipients...)
		if err := w.vector16(len(recips), func(i int) error {
			rp := recips[i]
			w.u32(uint32(rp.Node))
			if err := w.opaque16([]byte(rp.UserID)); err != nil {
				return err
			}
			w.u32(uint32(rp.TargetNode))
			return encodeEnvelope(w, rp.Envelope)
		}); err != nil {
			return nil, err
		}
		pnodes := make([]int, 0, len(c.Path.Publics))
		for n := range c.Path.Publics {
			pnodes = append(pnodes, n)
		}
		sort.Ints(pnodes)
		if err := w.vector16(len(pnodes), func(i int) error {
			w.u32(uint32(pnodes[i]))
			pub := c.Path.Publics[pnodes[i]]
			return w.opaque16(pub[:])
		}); err != nil {
			return nil, err
		}
	} else {
		w.u8(0)
	}
	return w.buf, nil
}

// authContent is the signed byte string: GroupContext || Commit.
func authContent(c Commit) []byte {
	leafOf := func(userID string) int {
		if c.Tree != nil {
			if slot := c.Tree.SlotFor(userID); slot >= 0 {
				return slot
			}
		}
		return 0
	}
	body, _ := encodeRFCCommitBody(c, leafOf)
	return append(groupContextBytesForCommit(c), body...)
}

// ChainedConfirmed exposes the transcript chain for verifiers: the confirmed hash of
// an epoch must equal this function of the previous confirmed hash, the committed
// previous tree hash and the epoch.
func ChainedConfirmed(prevConfirmed, prevTreeHash [32]byte, epoch uint32) [32]byte {
	return nextContext(prevTreeHash, prevConfirmed, epoch)
}

// nextContext computes the chained context for the next epoch from the previous
// tree hash and confirmed transcript hash.
func nextContext(prevTreeHash, prevConfirmed [32]byte, epoch uint32) [32]byte {
	h := sha256.New()
	h.Write(prevConfirmed[:])
	h.Write(prevTreeHash[:])
	var ep [4]byte
	binary.BigEndian.PutUint32(ep[:], epoch)
	h.Write(ep[:])
	var out [32]byte
	copy(out[:], h.Sum(nil))
	return out
}
