// Package mls is a compact, testable reference for the voice E2EE key schedule:
// leaf X25519 keypairs, an epoch commit signed with an Ed25519 identity key,
// Add/Remove proposals, HPKE-style recipient encryption of the epoch root, and a
// deterministic binary-tree key schedule with blanked nodes.
//
// It is TreeKEM-*style*, not RFC 9420: there are no path secrets, no proposal
// references, no Welcome messages and no ciphersuite negotiation. Those gaps are
// recorded in docs/COMPATIBILITY.md rather than claimed as MLS conformance.
package mls

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/ecdh"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"

	"golang.org/x/crypto/hkdf"
)

// Ciphersuite identifies the negotiated primitives. Only one suite is implemented;
// unknown values are rejected by Verify.
type Ciphersuite uint16

// SuiteX25519AES256GCM is X25519 for HPKE, HKDF-SHA256 for derivation and
// AES-256-GCM for AEAD.
const SuiteX25519AES256GCM Ciphersuite = 0x0001

// SuiteX25519AES128GCM is the same HPKE/KDF with a 128-bit application AEAD key.
const SuiteX25519AES128GCM Ciphersuite = 0x0002

// Supported reports whether the suite is implemented.
func (c Ciphersuite) Supported() bool {
	return c == SuiteX25519AES256GCM || c == SuiteX25519AES128GCM
}

// KeySize is the application AEAD key length for the suite.
func (c Ciphersuite) KeySize() int {
	if c == SuiteX25519AES128GCM {
		return 16
	}
	return 32
}

// Leaf is one roster member.
type Leaf struct {
	UserID string   `json:"user_id"`
	Public [32]byte `json:"public"`
}

// Proposal is an Add or Remove operation inside a commit.
type Proposal struct {
	Type   string      `json:"type"` // "add", "remove" or "ciphersuite"
	UserID string      `json:"user_id,omitempty"`
	Public [32]byte    `json:"public,omitempty"`
	Suite  Ciphersuite `json:"cipher_suite,omitempty"`
}

// Envelope is the HPKE-style recipient encryption of the epoch root.
type Envelope struct {
	Ephemeral  [32]byte `json:"ephemeral"`
	Nonce      [12]byte `json:"nonce"`
	Ciphertext []byte   `json:"ciphertext"`
}

// Commit is the signed epoch transition.
type Commit struct {
	Epoch            uint32              `json:"epoch"`
	CipherSuite      Ciphersuite         `json:"cipher_suite"`
	ProposalRefs     []string            `json:"proposal_refs"`
	ProposalMessages []ProposalMessage   `json:"proposal_messages,omitempty"`
	TreePublics      map[int][32]byte    `json:"tree_publics,omitempty"`
	Tree             *TreeState          `json:"tree,omitempty"`
	Roster           []Leaf              `json:"roster"`
	Proposals        []Proposal          `json:"proposals"`
	Envelopes        map[string]Envelope `json:"envelopes"`
	Committer        string              `json:"committer"`
	Signature        []byte              `json:"signature"`
}

// Epoch is a committed key schedule.
type Epoch struct {
	Epoch  uint32
	Root   []byte
	Roster []Leaf
	// TreePublics carries node public keys across epochs so later path updates can
	// address a node resolution instead of re-deriving every key.
	TreePublics map[int][32]byte
	// Tree is the persisted leaf layout for this epoch.
	Tree *TreeState
}

const (
	labelTree = "waveycord-mls-tree-v1"
	labelHPKE = "waveycord-mls-hpke-v1"
	labelRoot = "waveycord-mls-root-v1"
)

// ExpandWithLabel is RFC 9420's labeled KDF expansion: the label and context are
// length-prefixed and fed to HKDF-Expand, so two derivations with different labels
// never collide.
func ExpandWithLabel(secret []byte, label string, context []byte, n int) []byte {
	info := make([]byte, 0, 4+len(label)+len(context))
	info = append(info, byte(len(label)>>8), byte(len(label)))
	info = append(info, label...)
	info = append(info, byte(len(context)>>8), byte(len(context)))
	info = append(info, context...)
	reader := hkdf.New(sha256.New, secret, nil, info)
	out := make([]byte, n)
	_, _ = io.ReadFull(reader, out)
	return out
}

// ApplicationKeys is the per-epoch application key schedule: an AEAD key, a base
// nonce and an exporter secret, each derived under its own label.
type ApplicationKeys struct {
	Key       []byte
	BaseNonce []byte
	Exporter  []byte
}

// DeriveApplicationKeys runs the application key schedule for the default suite.
func DeriveApplicationKeys(epochSecret []byte, epoch uint32) ApplicationKeys {
	return DeriveApplicationKeysFor(epochSecret, epoch, SuiteX25519AES256GCM)
}

// DeriveApplicationKeysFor runs the application key schedule for a negotiated suite,
// so the AEAD key length follows the ciphersuite.
func DeriveApplicationKeysFor(epochSecret []byte, epoch uint32, suite Ciphersuite) ApplicationKeys {
	var ctx [4]byte
	binary.BigEndian.PutUint32(ctx[:], epoch)
	es := ExpandWithLabel(epochSecret, "epoch", ctx[:], suite.KeySize())
	return ApplicationKeys{
		Key:       ExpandWithLabel(es, "key", nil, suite.KeySize()),
		BaseNonce: ExpandWithLabel(es, "nonce", nil, 12),
		Exporter:  ExpandWithLabel(es, "exporter", nil, suite.KeySize()),
	}
}

// NonceFor XORs a 64-bit counter into an application base nonce, the RFC 9420
// per-record construction.
func NonceFor(baseNonce []byte, counter uint64) []byte {
	nonce := append([]byte(nil), baseNonce...)
	var c [8]byte
	binary.BigEndian.PutUint64(c[:], counter)
	for i := 0; i < 8 && i < len(nonce); i++ {
		nonce[len(nonce)-8+i] ^= c[i]
	}
	return nonce
}

func hkdfBytes(secret []byte, salt, info string, n int) []byte {
	r := hkdf.New(sha256.New, secret, []byte(salt), []byte(info))
	out := make([]byte, n)
	_, _ = io.ReadFull(r, out)
	return out
}

// normSlice maps an empty slice to nil so nil and empty encode identically.
func normSlice[T any](v []T) []T {
	if len(v) == 0 {
		return nil
	}
	return v
}

func canonical(c Commit) []byte {
	type canon struct {
		Epoch            uint32              `json:"epoch"`
		CipherSuite      Ciphersuite         `json:"cipher_suite"`
		ProposalRefs     []string            `json:"proposal_refs"`
		ProposalMessages []ProposalMessage   `json:"proposal_messages,omitempty"`
		TreePublics      map[int][32]byte    `json:"tree_publics,omitempty"`
		Tree             *TreeState          `json:"tree,omitempty"`
		Roster           []Leaf              `json:"roster"`
		Proposals        []Proposal          `json:"proposals"`
		Envelopes        map[string]Envelope `json:"envelopes"`
		Committer        string              `json:"committer"`
	}
	// Normalize empty collections so a nil and an empty slice or map produce the same
	// signed bytes. Without this a TLS round trip (which allocates empty collections)
	// would change the canonical form and break the signature.
	tree := c.Tree
	if tree != nil {
		cp := *tree
		cp.Users = normSlice(cp.Users)
		cp.Positions = normSlice(cp.Positions)
		if len(cp.Publics) == 0 {
			cp.Publics = nil
		}
		tree = &cp
	}
	treePublics := c.TreePublics
	if len(treePublics) == 0 {
		treePublics = nil
	}
	envelopes := c.Envelopes
	if len(envelopes) == 0 {
		envelopes = nil
	}
	raw, _ := json.Marshal(canon{c.Epoch, c.CipherSuite, normSlice(c.ProposalRefs), normSlice(c.ProposalMessages),
		treePublics, tree, normSlice(c.Roster), normSlice(c.Proposals), envelopes, c.Committer})
	return raw
}

// CommitError marks verification failures.
var ErrVerification = errors.New("mls: commit verification failed")

// ProposalMessage is the signed wire framing for one proposal. The RFC carries each
// proposal as its own authenticated message; here the message is signed by the
// committer that included it, which is the subset this implementation supports.
type ProposalMessage struct {
	Proposal Proposal `json:"proposal"`
	Proposer string   `json:"proposer"`
	// Ref is the proposal's own reference, derived from the body and proposer, and is
	// what the commit's reference list binds. The signature covers it.
	Ref       string `json:"ref"`
	Signature []byte `json:"signature"`
}

// proposalBodyCanonical is the reference preimage: body and proposer only, so a
// message's own Ref can be derived before signing.
func proposalBodyCanonical(m ProposalMessage) []byte {
	raw, _ := json.Marshal(struct {
		Proposal Proposal `json:"proposal"`
		Proposer string   `json:"proposer"`
	}{m.Proposal, m.Proposer})
	return append([]byte("proposal-body|"), raw...)
}

// proposalMessageCanonical is the signed bytes, including the message's Ref.
func proposalMessageCanonical(m ProposalMessage) []byte {
	raw, _ := json.Marshal(struct {
		Proposal Proposal `json:"proposal"`
		Proposer string   `json:"proposer"`
		Ref      string   `json:"ref"`
	}{m.Proposal, m.Proposer, m.Ref})
	return append([]byte("proposal-message|"), raw...)
}

// SignProposalMessage derives the message reference and signs body, proposer and ref.
func SignProposalMessage(p Proposal, proposer string, priv ed25519.PrivateKey) ProposalMessage {
	m := ProposalMessage{Proposal: p, Proposer: proposer}
	sum := sha256.Sum256(proposalBodyCanonical(m))
	m.Ref = fmt.Sprintf("%x", sum[:16])
	m.Signature = ed25519.Sign(priv, proposalMessageCanonical(m))
	return m
}

// VerifyProposalMessage checks the reference derivation and the proposer's signature.
func VerifyProposalMessage(m ProposalMessage, signKeys map[string]ed25519.PublicKey) error {
	key, ok := signKeys[m.Proposer]
	if !ok || len(m.Signature) == 0 {
		return ErrVerification
	}
	sum := sha256.Sum256(proposalBodyCanonical(m))
	if m.Ref == "" || m.Ref != fmt.Sprintf("%x", sum[:16]) {
		return ErrVerification
	}
	if !ed25519.Verify(key, proposalMessageCanonical(m), m.Signature) {
		return ErrVerification
	}
	return nil
}

// ProposalMessageRef returns the message's bound reference, recomputing it so a
// forged Ref cannot be used.
func ProposalMessageRef(m ProposalMessage) string {
	sum := sha256.Sum256(proposalBodyCanonical(m))
	return fmt.Sprintf("%x", sum[:16])
}

// ProposalRefsFor computes the reference list for signed messages.
func ProposalRefsFor(messages []ProposalMessage) []string {
	out := make([]string, 0, len(messages))
	for _, m := range messages {
		out = append(out, ProposalMessageRef(m))
	}
	return out
}

// ProposalRef is the commit reference for one proposal.
func ProposalRef(p Proposal) string {
	raw, _ := json.Marshal(p)
	sum := sha256.Sum256(append([]byte("proposal|"), raw...))
	return fmt.Sprintf("%x", sum[:16])
}

// ProposalRefs computes the reference list for a proposal slice.
func ProposalRefs(proposals []Proposal) []string {
	out := make([]string, 0, len(proposals))
	for _, p := range proposals {
		out = append(out, ProposalRef(p))
	}
	return out
}

// ApplyProposals validates and applies Add/Remove proposals to a roster.
func ApplyProposals(roster []Leaf, proposals []Proposal) ([]Leaf, error) {
	byID := map[string]Leaf{}
	for _, l := range roster {
		byID[l.UserID] = l
	}
	for _, p := range proposals {
		switch p.Type {
		case "add":
			if p.UserID == "" || p.Public == ([32]byte{}) {
				return nil, fmt.Errorf("mls: add proposal missing user or key")
			}
			if _, exists := byID[p.UserID]; exists {
				return nil, fmt.Errorf("mls: cannot add existing member %s", p.UserID)
			}
			byID[p.UserID] = Leaf{UserID: p.UserID, Public: p.Public}
		case "remove":
			if _, exists := byID[p.UserID]; !exists {
				return nil, fmt.Errorf("mls: cannot remove unknown member %s", p.UserID)
			}
			delete(byID, p.UserID)
		case "ciphersuite":
			if !p.Suite.Supported() {
				return nil, fmt.Errorf("mls: unsupported ciphersuite proposal %#x", p.Suite)
			}
		default:
			return nil, fmt.Errorf("mls: unknown proposal %q", p.Type)
		}
	}
	out := make([]Leaf, 0, len(byID))
	for _, l := range byID {
		out = append(out, l)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].UserID < out[j].UserID })
	return out, nil
}

// NewCommit creates a fresh epoch: random root, HPKE envelopes for every member
// of the resulting roster, tree-derived node keys, signed by committerPriv.
func NewCommit(epoch uint32, roster []Leaf, proposals []Proposal, committerID string, committerPriv ed25519.PrivateKey) (Commit, *Epoch, error) {
	next, err := ApplyProposals(roster, proposals)
	if err != nil {
		return Commit{}, nil, err
	}
	if len(next) == 0 {
		return Commit{}, nil, fmt.Errorf("mls: empty roster")
	}
	if committerPriv == nil {
		return Commit{}, nil, fmt.Errorf("mls: committer key required")
	}
	root := make([]byte, 32)
	if _, err := rand.Read(root); err != nil {
		return Commit{}, nil, err
	}
	messages := make([]ProposalMessage, 0, len(proposals))
	for _, p := range proposals {
		messages = append(messages, SignProposalMessage(p, committerID, committerPriv))
	}
	commit := Commit{Epoch: epoch, CipherSuite: SuiteX25519AES256GCM,
		ProposalRefs: ProposalRefsFor(messages), ProposalMessages: messages,
		Roster: next, Proposals: proposals,
		Envelopes: map[string]Envelope{}, Committer: committerID}
	for _, leaf := range next {
		env, err := sealRoot(root, leaf)
		if err != nil {
			return Commit{}, nil, err
		}
		commit.Envelopes[leaf.UserID] = env
	}
	commit.Signature = ed25519.Sign(committerPriv, canonical(commit))
	return commit, &Epoch{Epoch: epoch, Root: root, Roster: next}, nil
}

func sealRoot(root []byte, leaf Leaf) (Envelope, error) {
	pub, err := ecdh.X25519().NewPublicKey(leaf.Public[:])
	if err != nil {
		return Envelope{}, err
	}
	eph, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		return Envelope{}, err
	}
	shared, err := eph.ECDH(pub)
	if err != nil {
		return Envelope{}, err
	}
	var env Envelope
	copy(env.Ephemeral[:], eph.PublicKey().Bytes())
	info := append(eph.PublicKey().Bytes(), []byte(leaf.UserID)...)
	key := hkdfBytes(shared, labelHPKE, string(info), 32)
	block, err := aes.NewCipher(key)
	if err != nil {
		return env, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return env, err
	}
	if _, err := rand.Read(env.Nonce[:]); err != nil {
		return env, err
	}
	env.Ciphertext = gcm.Seal(nil, env.Nonce[:], root, []byte(leaf.UserID))
	return env, nil
}

// Verify checks the signature, roster transition and proposer membership.
// Verify is VerifyWithSuite without a previous-suite constraint.
func Verify(c Commit, previous []Leaf, signKeys map[string]ed25519.PublicKey) error {
	return VerifyWithSuite(c, previous, 0, signKeys)
}

// VerifyWithSuite additionally enforces ciphersuite negotiation: a committed suite
// may only change through a signed ciphersuite proposal, and an unchanged suite must
// match the previous epoch's.
func VerifyWithSuite(c Commit, previous []Leaf, previousSuite Ciphersuite, signKeys map[string]ed25519.PublicKey) error {
	return VerifyWithProposals(c, previous, previousSuite, nil, signKeys)
}

// VerifyWithProposals resolves proposal references that are not carried inside the
// commit from a cache of previously relayed proposal messages.
func VerifyWithProposals(c Commit, previous []Leaf, previousSuite Ciphersuite, external map[string]ProposalMessage, signKeys map[string]ed25519.PublicKey) error {
	if !c.CipherSuite.Supported() {
		return ErrVerification
	}
	selected := Ciphersuite(0)
	for _, p := range c.Proposals {
		if p.Type != "ciphersuite" {
			continue
		}
		if selected != 0 {
			return ErrVerification
		}
		selected = p.Suite
	}
	if selected != 0 {
		if selected != c.CipherSuite || !selected.Supported() {
			return ErrVerification
		}
	} else if previousSuite != 0 && c.CipherSuite != previousSuite {
		return ErrVerification
	}
	// Proposals are either carried by value (with their signed message) or referenced
	// by ref from a previously relayed proposal; the signed ref list orders them.
	if len(c.ProposalMessages) > len(c.Proposals) || len(c.ProposalRefs) != len(c.Proposals) {
		return ErrVerification
	}
	bodyMatches := func(m ProposalMessage, i int) bool {
		body, _ := json.Marshal(m.Proposal)
		want, _ := json.Marshal(c.Proposals[i])
		return string(body) == string(want)
	}
	proposerOK := func(m ProposalMessage) bool {
		return leafIndex(previous, m.Proposer) >= 0
	}
	seenRef := map[string]bool{}
	for i := range c.Proposals {
		ref := c.ProposalRefs[i]
		if ref == "" || seenRef[ref] {
			return ErrVerification
		}
		seenRef[ref] = true
		var m ProposalMessage
		if i < len(c.ProposalMessages) {
			m = c.ProposalMessages[i]
			if ProposalMessageRef(m) != ref || m.Ref != ref {
				return ErrVerification
			}
		} else {
			ext, ok := external[ref]
			if !ok {
				return ErrVerification
			}
			m = ext
			if ProposalMessageRef(m) != ref || m.Ref != ref {
				return ErrVerification
			}
		}
		if !bodyMatches(m, i) || !proposerOK(m) {
			return ErrVerification
		}
		if err := VerifyProposalMessage(m, signKeys); err != nil {
			return err
		}
	}
	expected, err := ApplyProposals(previous, c.Proposals)
	if err != nil {
		return err
	}
	if len(expected) != len(c.Roster) {
		return ErrVerification
	}
	for i := range expected {
		if expected[i].UserID != c.Roster[i].UserID || expected[i].Public != c.Roster[i].Public {
			return ErrVerification
		}
	}
	inRoster := false
	for _, l := range c.Roster {
		if l.UserID == c.Committer {
			inRoster = true
		}
	}
	if !inRoster {
		return ErrVerification
	}
	key, ok := signKeys[c.Committer]
	if !ok || !ed25519.Verify(key, canonical(c), c.Signature) {
		return ErrVerification
	}
	return nil
}

// DecryptRoot opens the caller's envelope and derives the epoch root.
func DecryptRoot(c Commit, userID string, priv [32]byte) ([]byte, error) {
	env, ok := c.Envelopes[userID]
	if !ok {
		return nil, fmt.Errorf("mls: no envelope for %s", userID)
	}
	pub, err := ecdh.X25519().NewPublicKey(env.Ephemeral[:])
	if err != nil {
		return nil, err
	}
	key, err := ecdh.X25519().NewPrivateKey(priv[:])
	if err != nil {
		return nil, err
	}
	shared, err := key.ECDH(pub)
	if err != nil {
		return nil, err
	}
	info := append(env.Ephemeral[:], []byte(userID)...)
	k := hkdfBytes(shared, labelHPKE, string(info), 32)
	block, err := aes.NewCipher(k)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	root, err := gcm.Open(nil, env.Nonce[:], env.Ciphertext, []byte(userID))
	if err != nil {
		return nil, ErrVerification
	}
	return root, nil
}

// NodeKey is one node in the deterministic binary tree key schedule.
type NodeKey struct {
	Index  int
	Blank  bool
	Secret []byte
	Public [32]byte
}

// TreeSize returns the next power of two >= n.
func TreeSize(n int) int {
	size := 1
	for size < n {
		size <<= 1
	}
	return size
}

// DeriveTree builds the epoch node keys. Leaves are assigned by roster order;
// unused leaves are blank, and all internal nodes derive from the root by HKDF.
func DeriveTree(root []byte, roster []Leaf) []NodeKey {
	size := TreeSize(len(roster))
	if size < 2 {
		size = 2
	}
	nodes := make([]NodeKey, 2*size)
	for i := 0; i < size; i++ {
		blank := i >= len(roster)
		var secret []byte
		if !blank {
			secret = hkdfBytes(root, labelTree, fmt.Sprintf("leaf:%d", i), 32)
		}
		nodes[size+i] = NodeKey{Index: size + i, Blank: blank, Secret: secret}
	}
	for i := size - 1; i >= 1; i-- {
		left, right := nodes[2*i], nodes[2*i+1]
		blank := left.Blank && right.Blank
		secret := hkdfBytes(root, labelTree, fmt.Sprintf("node:%d", i), 32)
		nodes[i] = NodeKey{Index: i, Blank: blank, Secret: secret}
	}
	// Public keys for non-blank nodes.
	for i := range nodes {
		if nodes[i].Blank || len(nodes[i].Secret) == 0 {
			continue
		}
		priv, err := ecdh.X25519().NewPrivateKey(nodes[i].Secret)
		if err != nil {
			continue
		}
		copy(nodes[i].Public[:], priv.PublicKey().Bytes())
	}
	return nodes
}

// EpochSecret derives the media key from an epoch root and roster.
func EpochSecret(root []byte, roster []Leaf) []byte {
	h := sha256.New()
	for _, l := range roster {
		h.Write([]byte(l.UserID))
		h.Write(l.Public[:])
	}
	return hkdfBytes(root, "waveycord-mls-epoch-secret", string(h.Sum(nil)), 32)
}

var _ = binary.BigEndian
