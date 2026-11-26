package mls

import (
	"encoding/base64"
	"sort"
)

// MLSMessage framing. The op 38 commit travels as an RFC-style MLSMessage: a
// version and wire-format prefix followed by a length-prefixed TLS presentation
// language encoded EpochCommit. Base64 is only the transport encoding inside JSON.

const (
	mlsProtocolVersion    uint16 = 1
	mlsWireFormatCommit   uint16 = 1
	mlsWireFormatProposal uint16 = 2
)

// Envelope TLS encoding (also used by Commit/Path/Welcome structures).
func encodeEnvelope(w *tlsWriter, e Envelope) error {
	if err := w.opaque16(e.Ephemeral[:]); err != nil {
		return err
	}
	if err := w.opaque16(e.Nonce[:]); err != nil {
		return err
	}
	return w.opaque16(e.Ciphertext)
}

func decodeEnvelope(r *tlsReader) (Envelope, error) {
	var e Envelope
	eph, err := r.opaque16()
	if err != nil {
		return e, err
	}
	nonce, err := r.opaque16()
	if err != nil {
		return e, err
	}
	ct, err := r.opaque16()
	if err != nil {
		return e, err
	}
	if len(eph) != 32 || len(nonce) != 12 {
		return e, errTLS
	}
	copy(e.Ephemeral[:], eph)
	copy(e.Nonce[:], nonce)
	e.Ciphertext = ct
	return e, nil
}

func encodeProposal(w *tlsWriter, p Proposal) error {
	switch p.Type {
	case "add":
		w.u8(0)
	case "remove":
		w.u8(1)
	case "ciphersuite":
		w.u8(2)
	default:
		return errTLS
	}
	if err := w.opaque16([]byte(p.UserID)); err != nil {
		return err
	}
	if err := w.opaque16(p.Public[:]); err != nil {
		return err
	}
	if p.Type == "ciphersuite" {
		w.u32(uint32(p.Suite))
	}
	return nil
}

func decodeProposal(r *tlsReader) (Proposal, error) {
	var p Proposal
	t, err := r.u8()
	if err != nil {
		return p, err
	}
	uid, err := r.opaque16()
	if err != nil {
		return p, err
	}
	pub, err := r.opaque16()
	if err != nil {
		return p, err
	}
	switch t {
	case 0:
		p.Type = "add"
	case 1:
		p.Type = "remove"
	case 2:
		p.Type = "ciphersuite"
		suite, err := r.u32()
		if err != nil {
			return p, err
		}
		p.Suite = Ciphersuite(suite)
	default:
		return p, errTLS
	}
	p.UserID = string(uid)
	if len(pub) != 32 {
		return p, errTLS
	}
	copy(p.Public[:], pub)
	return p, nil
}

// encodeEpochCommitBody serializes the whole commit as TLS values.
func encodeEpochCommitBody(c EpochCommit) ([]byte, error) {
	if !c.CipherSuite.Supported() {
		return nil, errTLS
	}
	w := &tlsWriter{}
	w.u32(c.Epoch)
	w.u32(uint32(c.CipherSuite))
	if err := w.opaque16(c.GroupID); err != nil {
		return nil, err
	}
	if err := w.opaque16(c.TreeHash[:]); err != nil {
		return nil, err
	}
	if err := w.opaque16(c.ConfirmedHash[:]); err != nil {
		return nil, err
	}
	if err := w.vector16(len(c.ProposalRefs), func(i int) error { return w.opaque16([]byte(c.ProposalRefs[i])) }); err != nil {
		return nil, err
	}
	if err := w.vector16(len(c.ProposalMessages), func(i int) error {
		m := c.ProposalMessages[i]
		if err := encodeProposal(w, m.Proposal); err != nil {
			return err
		}
		if err := w.opaque16([]byte(m.Proposer)); err != nil {
			return err
		}
		if err := w.opaque16([]byte(m.Ref)); err != nil {
			return err
		}
		return w.opaque16(m.Signature)
	}); err != nil {
		return nil, err
	}
	if err := w.vector16(len(c.Proposals), func(i int) error { return encodeProposal(w, c.Proposals[i]) }); err != nil {
		return nil, err
	}
	if err := encodeTree(w, c.Tree); err != nil {
		return nil, err
	}
	if err := w.vector16(len(c.Roster), func(i int) error { return encodeLeaf(w, c.Roster[i]) }); err != nil {
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
	envUsers := make([]string, 0, len(c.Envelopes))
	for u := range c.Envelopes {
		envUsers = append(envUsers, u)
	}
	sort.Strings(envUsers)
	if err := w.vector16(len(envUsers), func(i int) error {
		uid := envUsers[i]
		if err := w.opaque16([]byte(uid)); err != nil {
			return err
		}
		return encodeEnvelope(w, c.Envelopes[uid])
	}); err != nil {
		return nil, err
	}
	if err := w.opaque16([]byte(c.Committer)); err != nil {
		return nil, err
	}
	if err := w.opaque16(c.Signature); err != nil {
		return nil, err
	}
	if c.Path == nil {
		w.u8(0)
	} else {
		w.u8(1)
		w.u32(uint32(c.Path.CommitterLeaf))
		if err := w.vector16(len(c.Path.Recipients), func(i int) error {
			rp := c.Path.Recipients[i]
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
	}
	welcomeUsers := make([]string, 0, len(c.Welcome))
	for u := range c.Welcome {
		welcomeUsers = append(welcomeUsers, u)
	}
	sort.Strings(welcomeUsers)
	if err := w.vector16(len(welcomeUsers), func(i int) error {
		uid := welcomeUsers[i]
		if err := w.opaque16([]byte(uid)); err != nil {
			return err
		}
		wl := c.Welcome[uid]
		w.u32(wl.Epoch)
		if err := w.opaque16([]byte(wl.CommitHash)); err != nil {
			return err
		}
		return encodeEnvelope(w, wl.GroupInfo)
	}); err != nil {
		return nil, err
	}
	return w.buf, nil
}

func decodeEpochCommitBody(body []byte) (EpochCommit, error) {
	var c EpochCommit
	r := &tlsReader{data: body}
	epoch, err := r.u32()
	if err != nil {
		return c, err
	}
	suite, err := r.u32()
	if err != nil {
		return c, err
	}
	c.Epoch = epoch
	c.CipherSuite = Ciphersuite(suite)
	if !c.CipherSuite.Supported() {
		return c, errTLS
	}
	groupID, err := r.opaque16()
	if err != nil {
		return c, err
	}
	c.GroupID = groupID
	treeHash, err := r.opaque16()
	if err != nil {
		return c, err
	}
	confirmed, err := r.opaque16()
	if err != nil {
		return c, err
	}
	if len(treeHash) != 32 || len(confirmed) != 32 {
		return c, errTLS
	}
	copy(c.TreeHash[:], treeHash)
	copy(c.ConfirmedHash[:], confirmed)
	n, err := r.vector16()
	if err != nil {
		return c, err
	}
	if n > r.remaining() {
		return c, errTLS
	}
	c.ProposalRefs = make([]string, 0, n)
	for i := 0; i < n; i++ {
		ref, err := r.opaque16()
		if err != nil {
			return c, err
		}
		c.ProposalRefs = append(c.ProposalRefs, string(ref))
	}
	mn, err := r.vector16()
	if err != nil {
		return c, err
	}
	if mn > r.remaining() {
		return c, errTLS
	}
	c.ProposalMessages = make([]ProposalMessage, 0, mn)
	for i := 0; i < mn; i++ {
		p, err := decodeProposal(r)
		if err != nil {
			return c, err
		}
		prop, err := r.opaque16()
		if err != nil {
			return c, err
		}
		ref, err := r.opaque16()
		if err != nil {
			return c, err
		}
		sig, err := r.opaque16()
		if err != nil {
			return c, err
		}
		c.ProposalMessages = append(c.ProposalMessages, ProposalMessage{
			Proposal: p, Proposer: string(prop), Ref: string(ref), Signature: sig,
		})
	}
	pn, err := r.vector16()
	if err != nil {
		return c, err
	}
	if pn > r.remaining() {
		return c, errTLS
	}
	c.Proposals = make([]Proposal, 0, pn)
	for i := 0; i < pn; i++ {
		p, err := decodeProposal(r)
		if err != nil {
			return c, err
		}
		c.Proposals = append(c.Proposals, p)
	}
	tree, err := decodeTree(r)
	if err != nil {
		return c, err
	}
	c.Tree = tree
	rn, err := r.vector16()
	if err != nil {
		return c, err
	}
	if rn > r.remaining() {
		return c, errTLS
	}
	c.Roster = make([]Leaf, 0, rn)
	for i := 0; i < rn; i++ {
		l, err := decodeLeaf(r)
		if err != nil {
			return c, err
		}
		c.Roster = append(c.Roster, l)
	}
	tn, err := r.vector16()
	if err != nil {
		return c, err
	}
	if tn > r.remaining() {
		return c, errTLS
	}
	c.TreePublics = map[int][32]byte{}
	for i := 0; i < tn; i++ {
		node, err := r.u32()
		if err != nil {
			return c, err
		}
		pub, err := r.opaque16()
		if err != nil {
			return c, err
		}
		if len(pub) != 32 {
			return c, errTLS
		}
		var arr [32]byte
		copy(arr[:], pub)
		c.TreePublics[int(node)] = arr
	}
	en, err := r.vector16()
	if err != nil {
		return c, err
	}
	if en > r.remaining() {
		return c, errTLS
	}
	c.Envelopes = map[string]Envelope{}
	for i := 0; i < en; i++ {
		uid, err := r.opaque16()
		if err != nil {
			return c, err
		}
		env, err := decodeEnvelope(r)
		if err != nil {
			return c, err
		}
		c.Envelopes[string(uid)] = env
	}
	committer, err := r.opaque16()
	if err != nil {
		return c, err
	}
	c.Committer = string(committer)
	sig, err := r.opaque16()
	if err != nil {
		return c, err
	}
	c.Signature = sig
	hasPath, err := r.u8()
	if err != nil {
		return c, err
	}
	if hasPath == 1 {
		leaf, err := r.u32()
		if err != nil {
			return c, err
		}
		path := &PathChain{CommitterLeaf: int(leaf), Publics: map[int][32]byte{}}
		rc, err := r.vector16()
		if err != nil {
			return c, err
		}
		if rc > r.remaining() {
			return c, errTLS
		}
		for i := 0; i < rc; i++ {
			node, err := r.u32()
			if err != nil {
				return c, err
			}
			uid, err := r.opaque16()
			if err != nil {
				return c, err
			}
			target, err := r.u32()
			if err != nil {
				return c, err
			}
			env, err := decodeEnvelope(r)
			if err != nil {
				return c, err
			}
			path.Recipients = append(path.Recipients, PathRecipient{
				Node: int(node), UserID: string(uid), TargetNode: int(target), Envelope: env,
			})
		}
		pc, err := r.vector16()
		if err != nil {
			return c, err
		}
		if pc > r.remaining() {
			return c, errTLS
		}
		for i := 0; i < pc; i++ {
			node, err := r.u32()
			if err != nil {
				return c, err
			}
			pub, err := r.opaque16()
			if err != nil {
				return c, err
			}
			if len(pub) != 32 {
				return c, errTLS
			}
			var arr [32]byte
			copy(arr[:], pub)
			path.Publics[int(node)] = arr
		}
		c.Path = path
	} else if hasPath != 0 {
		return c, errTLS
	}
	wn, err := r.vector16()
	if err != nil {
		return c, err
	}
	if wn > r.remaining() {
		return c, errTLS
	}
	c.Welcome = map[string]Welcome{}
	for i := 0; i < wn; i++ {
		uid, err := r.opaque16()
		if err != nil {
			return c, err
		}
		wEpoch, err := r.u32()
		if err != nil {
			return c, err
		}
		hash, err := r.opaque16()
		if err != nil {
			return c, err
		}
		gi, err := decodeEnvelope(r)
		if err != nil {
			return c, err
		}
		c.Welcome[string(uid)] = Welcome{Epoch: wEpoch, CommitHash: string(hash), GroupInfo: gi}
	}
	if r.remaining() != 0 {
		return c, errTLS
	}
	return c, nil
}

// MarshalMLSMessage encodes an EpochCommit into a base64 MLSMessage.
func MarshalMLSMessage(c EpochCommit) (string, error) {
	body, err := encodeEpochCommitBody(c)
	if err != nil {
		return "", err
	}
	w := &tlsWriter{}
	w.buf = append(w.buf, byte(mlsProtocolVersion>>8), byte(mlsProtocolVersion))
	w.buf = append(w.buf, byte(mlsWireFormatCommit>>8), byte(mlsWireFormatCommit))
	if err := w.opaque16(body); err != nil {
		return "", err
	}
	return base64.StdEncoding.EncodeToString(w.buf), nil
}

// MarshalProposalMessage frames one proposal message for relaying before a commit.
func MarshalProposalMessage(m ProposalMessage) (string, error) {
	w := &tlsWriter{}
	w.buf = append(w.buf, byte(mlsProtocolVersion>>8), byte(mlsProtocolVersion))
	w.buf = append(w.buf, byte(mlsWireFormatProposal>>8), byte(mlsWireFormatProposal))
	body := &tlsWriter{}
	if err := encodeProposal(body, m.Proposal); err != nil {
		return "", err
	}
	if err := body.opaque16([]byte(m.Proposer)); err != nil {
		return "", err
	}
	if err := body.opaque16([]byte(m.Ref)); err != nil {
		return "", err
	}
	if err := body.opaque16(m.Signature); err != nil {
		return "", err
	}
	if err := w.opaque16(body.buf); err != nil {
		return "", err
	}
	return base64.StdEncoding.EncodeToString(w.buf), nil
}

// UnmarshalProposalMessage decodes a framed proposal message.
func UnmarshalProposalMessage(encoded string) (ProposalMessage, error) {
	var m ProposalMessage
	raw, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil || len(raw) < 4 {
		return m, errTLS
	}
	if uint16(raw[0])<<8|uint16(raw[1]) != mlsProtocolVersion ||
		uint16(raw[2])<<8|uint16(raw[3]) != mlsWireFormatProposal {
		return m, errTLS
	}
	r := &tlsReader{data: raw, off: 4}
	body, err := r.opaque16()
	if err != nil || r.remaining() != 0 {
		return m, errTLS
	}
	br := &tlsReader{data: body}
	p, err := decodeProposal(br)
	if err != nil {
		return m, err
	}
	prop, err := br.opaque16()
	if err != nil {
		return m, err
	}
	ref, err := br.opaque16()
	if err != nil {
		return m, err
	}
	sig, err := br.opaque16()
	if err != nil {
		return m, err
	}
	if br.remaining() != 0 {
		return m, errTLS
	}
	return ProposalMessage{Proposal: p, Proposer: string(prop), Ref: string(ref), Signature: sig}, nil
}

// MLSMessageFormat returns the wire format of a framed message.
func MLSMessageFormat(encoded string) (uint16, error) {
	raw, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil || len(raw) < 4 {
		return 0, errTLS
	}
	if uint16(raw[0])<<8|uint16(raw[1]) != mlsProtocolVersion {
		return 0, errTLS
	}
	return uint16(raw[2])<<8 | uint16(raw[3]), nil
}

// UnmarshalMLSMessage decodes a base64 MLSMessage into an EpochCommit.
func UnmarshalMLSMessage(encoded string) (EpochCommit, error) {
	var c EpochCommit
	raw, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return c, errTLS
	}
	r := &tlsReader{data: raw}
	if r.remaining() < 4 {
		return c, errTLS
	}
	version := uint16(raw[0])<<8 | uint16(raw[1])
	format := uint16(raw[2])<<8 | uint16(raw[3])
	r.off = 4
	if version != mlsProtocolVersion || format != mlsWireFormatCommit {
		return c, errTLS
	}
	body, err := r.opaque16()
	if err != nil {
		return c, err
	}
	if r.remaining() != 0 {
		return c, errTLS
	}
	return decodeEpochCommitBody(body)
}
