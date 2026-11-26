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

// Leaf is one roster member.
type Leaf struct {
	UserID string   `json:"user_id"`
	Public [32]byte `json:"public"`
}

// Proposal is an Add or Remove operation inside a commit.
type Proposal struct {
	Type   string   `json:"type"` // "add" or "remove"
	UserID string   `json:"user_id"`
	Public [32]byte `json:"public,omitempty"`
}

// Envelope is the HPKE-style recipient encryption of the epoch root.
type Envelope struct {
	Ephemeral  [32]byte `json:"ephemeral"`
	Nonce      [12]byte `json:"nonce"`
	Ciphertext []byte   `json:"ciphertext"`
}

// Commit is the signed epoch transition.
type Commit struct {
	Epoch     uint32              `json:"epoch"`
	Roster    []Leaf              `json:"roster"`
	Proposals []Proposal          `json:"proposals"`
	Envelopes map[string]Envelope `json:"envelopes"`
	Committer string              `json:"committer"`
	Signature []byte              `json:"signature"`
}

// Epoch is a committed key schedule.
type Epoch struct {
	Epoch  uint32
	Root   []byte
	Roster []Leaf
}

const (
	labelTree = "waveycord-mls-tree-v1"
	labelHPKE = "waveycord-mls-hpke-v1"
	labelRoot = "waveycord-mls-root-v1"
)

func hkdfBytes(secret []byte, salt, info string, n int) []byte {
	r := hkdf.New(sha256.New, secret, []byte(salt), []byte(info))
	out := make([]byte, n)
	_, _ = io.ReadFull(r, out)
	return out
}

func canonical(c Commit) []byte {
	type canon struct {
		Epoch     uint32              `json:"epoch"`
		Roster    []Leaf              `json:"roster"`
		Proposals []Proposal          `json:"proposals"`
		Envelopes map[string]Envelope `json:"envelopes"`
		Committer string              `json:"committer"`
	}
	raw, _ := json.Marshal(canon{c.Epoch, c.Roster, c.Proposals, c.Envelopes, c.Committer})
	return raw
}

// CommitError marks verification failures.
var ErrVerification = errors.New("mls: commit verification failed")

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
	commit := Commit{Epoch: epoch, Roster: next, Proposals: proposals, Envelopes: map[string]Envelope{}, Committer: committerID}
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
func Verify(c Commit, previous []Leaf, signKeys map[string]ed25519.PublicKey) error {
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
