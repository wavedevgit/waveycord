package mls

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/ecdh"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
)

// PathChain is a simplified TreeKEM update path: the committer chooses a fresh
// leaf secret, derives each ancestor by HKDF over the previous node, and encrypts
// the node at each divergence point to every member of the sibling subtree. Each
// member can open exactly one node (their divergence point) and chains upward with
// the same index-bound KDF, so no member receives the whole path.
type PathChain struct {
	CommitterLeaf int              `json:"committer_leaf"`
	Recipients    []PathRecipient  `json:"recipients"`
	Publics       map[int][32]byte `json:"publics"`
}

// PathRecipient carries one node secret encrypted to one member.
type PathRecipient struct {
	Node     int      `json:"node"`
	UserID   string   `json:"user_id"`
	Envelope Envelope `json:"envelope"`
}

// Welcome lets an added member join without the prior tree state.
type Welcome struct {
	Epoch      uint32   `json:"epoch"`
	CommitHash string   `json:"commit_hash"`
	Root       Envelope `json:"root"`
}

// EpochCommit extends Commit with a path update and Welcome messages.
type EpochCommit struct {
	Commit
	Path    *PathChain         `json:"path,omitempty"`
	Welcome map[string]Welcome `json:"welcome,omitempty"`
}

func pathNodeSecret(child []byte, parent int) []byte {
	return hkdfBytes(child, "waveycord-mls-path", fmt.Sprintf("parent:%d", parent), 32)
}

// leafIndex returns the leaf slot for a roster member, or -1.
func leafIndex(roster []Leaf, userID string) int {
	for i, l := range roster {
		if l.UserID == userID {
			return i
		}
	}
	return -1
}

// leavesUnder lists roster users under a node in a tree of the given size.
func leavesUnder(node, size int, roster []Leaf) []string {
	var out []string
	var walk func(int)
	walk = func(n int) {
		if n >= size {
			idx := n - size
			if idx < len(roster) {
				out = append(out, roster[idx].UserID)
			}
			return
		}
		walk(2 * n)
		walk(2*n + 1)
	}
	walk(node)
	return out
}

// NewEpochCommit creates a path-updated, signed epoch with Welcome messages for
// members added by the commit's proposals.
func NewEpochCommit(epoch uint32, roster []Leaf, proposals []Proposal, committerID string, committerSignPriv ed25519.PrivateKey, committerLeafPriv [32]byte) (EpochCommit, *Epoch, error) {
	next, err := ApplyProposals(roster, proposals)
	if err != nil {
		return EpochCommit{}, nil, err
	}
	if len(next) == 0 {
		return EpochCommit{}, nil, fmt.Errorf("mls: empty roster")
	}
	size := TreeSize(len(next))
	if size < 2 {
		size = 2
	}
	ci := leafIndex(next, committerID)
	if ci < 0 {
		return EpochCommit{}, nil, fmt.Errorf("mls: committer not in roster")
	}
	// Fresh secret at the committer's leaf, then a hash chain to the root.
	leafSecret := make([]byte, 32)
	if _, err := rand.Read(leafSecret); err != nil {
		return EpochCommit{}, nil, err
	}
	node := size + ci
	secrets := map[int][]byte{node: leafSecret}
	publics := map[int][32]byte{}
	for node > 1 {
		parent := node / 2
		secrets[parent] = pathNodeSecret(secrets[node], parent)
		node = parent
	}
	for idx, sec := range secrets {
		priv, err := ecdh.X25519().NewPrivateKey(sec)
		if err == nil {
			publics[idx] = clone32(priv.PublicKey().Bytes())
		}
	}
	path := &PathChain{CommitterLeaf: size + ci, Publics: publics}
	// A self-envelope lets the committer replay its own chain uniformly.
	if selfEnv, err := sealRoot(leafSecret, next[ci]); err == nil {
		path.Recipients = append(path.Recipients, PathRecipient{Node: size + ci, UserID: committerID, Envelope: selfEnv})
	}
	// Encrypt each path node's secret to the sibling subtree's members.
	node = size + ci
	for node > 1 {
		parent := node / 2
		sibling := node ^ 1
		secret := secrets[parent]
		for _, uid := range leavesUnder(sibling, size, next) {
			leaf := next[leafIndex(next, uid)]
			env, err := sealRoot(secret, leaf)
			if err != nil {
				return EpochCommit{}, nil, err
			}
			path.Recipients = append(path.Recipients, PathRecipient{Node: parent, UserID: uid, Envelope: env})
		}
		node = parent
	}
	root := secrets[1]
	commit := EpochCommit{Commit: Commit{Epoch: epoch, CipherSuite: SuiteX25519AES256GCM,
		ProposalRefs: ProposalRefs(proposals), Roster: next, Proposals: proposals,
		Envelopes: map[string]Envelope{}, Committer: committerID}}
	commit.Path = path
	commit.Welcome = map[string]Welcome{}
	// Existing members derive the root through the path; added members need a
	// Welcome that binds the root to the commit hash.
	commit.Signature = ed25519.Sign(committerSignPriv, canonical(commit.Commit))
	hash := sha256.Sum256(canonical(commit.Commit))
	for _, p := range proposals {
		if p.Type != "add" {
			continue
		}
		leaf := next[leafIndex(next, p.UserID)]
		env, err := sealRoot(root, leaf)
		if err != nil {
			return EpochCommit{}, nil, err
		}
		commit.Welcome[p.UserID] = Welcome{Epoch: epoch, CommitHash: hex.EncodeToString(hash[:]), Root: env}
	}
	return commit, &Epoch{Epoch: epoch, Root: root, Roster: next}, nil
}

func clone32(b []byte) (out [32]byte) { copy(out[:], b); return out }

// VerifyEpochCommit verifies the commit signature and roster transition, including
// the path chain shape.
func VerifyEpochCommit(c EpochCommit, previous []Leaf, signKeys map[string]ed25519.PublicKey) error {
	if c.Path == nil || len(c.Path.Publics) == 0 {
		return ErrVerification
	}
	if c.Path.CommitterLeaf < 1 {
		return ErrVerification
	}
	return Verify(c.Commit, previous, signKeys)
}

// DerivePathRoot recovers the epoch root for an existing member from the path
// envelopes and the index-bound chain.
func DerivePathRoot(c EpochCommit, userID string, priv [32]byte) ([]byte, error) {
	if c.Path == nil {
		return nil, fmt.Errorf("mls: no path")
	}
	var start []byte
	startNode := 0
	for _, r := range c.Path.Recipients {
		if r.UserID != userID {
			continue
		}
		secret, err := openRoot(r.Envelope, userID, priv)
		if err != nil {
			continue
		}
		start, startNode = secret, r.Node
		break
	}
	if start == nil {
		return nil, fmt.Errorf("mls: no path envelope for %s", userID)
	}
	node := startNode
	cur := start
	for node > 1 {
		parent := node / 2
		cur = pathNodeSecret(cur, parent)
		node = parent
	}
	return cur, nil
}

// DecryptWelcome opens an added member's Welcome and checks it binds the commit.
func DecryptWelcome(c EpochCommit, userID string, priv [32]byte) ([]byte, error) {
	w, ok := c.Welcome[userID]
	if !ok {
		return nil, fmt.Errorf("mls: no welcome for %s", userID)
	}
	hash := sha256.Sum256(canonical(c.Commit))
	if w.CommitHash != hex.EncodeToString(hash[:]) {
		return nil, ErrVerification
	}
	return openRoot(w.Root, userID, priv)
}

func openRoot(env Envelope, userID string, priv [32]byte) ([]byte, error) {
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
	block, err := aesNewGCM(k)
	if err != nil {
		return nil, err
	}
	root, err := block.Open(nil, env.Nonce[:], env.Ciphertext, []byte(userID))
	if err != nil {
		return nil, ErrVerification
	}
	return root, nil
}

// SortedRoster returns roster sorted by user id.
func SortedRoster(leaves []Leaf) []Leaf {
	out := append([]Leaf(nil), leaves...)
	sort.Slice(out, func(i, j int) bool { return out[i].UserID < out[j].UserID })
	return out
}

func aesNewGCM(key []byte) (cipher.AEAD, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}
