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

// PathRecipient carries one path node's secret. Either it is sealed to one member's
// leaf (UserID set), or, when the sibling resolution is a present internal node, to
// that node's public key (TargetNode set) so members holding the node's private key
// can open it.
type PathRecipient struct {
	Node       int      `json:"node"`
	UserID     string   `json:"user_id,omitempty"`
	TargetNode int      `json:"target_node,omitempty"`
	Envelope   Envelope `json:"envelope"`
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

// Resolution returns the TreeKEM resolution of a node: the highest non-blank
// descendants. A blank node under a present node is replaced by its children's
// resolutions; a padding (blank) subtree contributes nothing. Presence is leaf
// membership plus any node key persisted from an earlier epoch.
func Resolution(node, size int, present map[int]bool) []int {
	if present[node] {
		return []int{node}
	}
	if node >= size {
		return nil
	}
	left := Resolution(2*node, size, present)
	right := Resolution(2*node+1, size, present)
	return append(left, right...)
}

// pathNodeSecret derives an ancestor secret from its child, using the RFC 9420
// "path" label. Every ancestor in the chain is one labeled expansion of the child,
// and only members that learn a node secret through a DH envelope can walk up.
func pathNodeSecret(child []byte) []byte {
	return ExpandWithLabel(child, "path", nil, 32)
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

// resolutionUnder lists the roster users whose leaves are in a node's subtree. This
// is the DH resolution of the sibling subtree: the committer encrypts one envelope
// per resolved member, and only those members can open that node. The tree carries
// no blank leaves between epochs, so the resolution of a node is exactly the members
// beneath it.
func resolutionUnder(node, size int, roster []Leaf) []string {
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
	return NewEpochCommitWithTree(epoch, roster, proposals, committerID, committerSignPriv, committerLeafPriv, nil)
}

// NewEpochCommitWithTree carries the previous epoch's node public keys so nodes
// outside this update's path keep their keys, which is what lets a later commit
// address an ancestor node instead of every leaf beneath it.
func NewEpochCommitWithTree(epoch uint32, roster []Leaf, proposals []Proposal, committerID string, committerSignPriv ed25519.PrivateKey, committerLeafPriv [32]byte, previousTree map[int][32]byte) (EpochCommit, *Epoch, error) {
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
		secrets[parent] = pathNodeSecret(secrets[node])
		node = parent
	}
	for idx, sec := range secrets {
		priv, err := ecdh.X25519().NewPrivateKey(sec)
		if err == nil {
			publics[idx] = clone32(priv.PublicKey().Bytes())
		}
	}
	// Persisted node keys from earlier epochs survive this update off-path.
	treePublics := map[int][32]byte{}
	for node, pub := range previousTree {
		treePublics[node] = pub
	}
	for node, pub := range publics {
		treePublics[node] = pub
	}
	path := &PathChain{CommitterLeaf: size + ci, Publics: publics}
	// A self-envelope lets the committer replay its own chain uniformly.
	if selfEnv, err := sealRoot(leafSecret, next[ci]); err == nil {
		path.Recipients = append(path.Recipients, PathRecipient{Node: size + ci, UserID: committerID, Envelope: selfEnv})
	}
	// Encrypt each path node's secret to the resolution of its sibling. Presence is
	// the member leaves plus persisted node keys; a resolved internal node is
	// expanded to the members beneath it because client-side node private keys are
	// not persisted by this implementation.
	present := map[int]bool{}
	for i := range next {
		present[size+i] = true
	}
	for node := range previousTree {
		present[node] = true
	}
	node = size + ci
	for node > 1 {
		parent := node / 2
		sibling := node ^ 1
		secret := secrets[parent]
		seen := map[string]bool{}
		for _, resNode := range Resolution(sibling, size, present) {
			// A present internal node is addressed directly when its public key is
			// known; members beneath it persist the matching private key.
			if resNode < size {
				if pub, ok := treePublics[resNode]; ok {
					env, err := sealNodeKey(secret, resNode, pub)
					if err != nil {
						return EpochCommit{}, nil, err
					}
					path.Recipients = append(path.Recipients, PathRecipient{Node: parent, TargetNode: resNode, Envelope: env})
					continue
				}
			}
			for _, uid := range resolutionUnder(resNode, size, next) {
				if seen[uid] {
					continue
				}
				seen[uid] = true
				leaf := next[leafIndex(next, uid)]
				env, err := sealRoot(secret, leaf)
				if err != nil {
					return EpochCommit{}, nil, err
				}
				path.Recipients = append(path.Recipients, PathRecipient{Node: parent, UserID: uid, Envelope: env})
			}
		}
		node = parent
	}
	root := secrets[1]
	messages := make([]ProposalMessage, 0, len(proposals))
	for _, p := range proposals {
		messages = append(messages, SignProposalMessage(p, committerID, committerSignPriv))
	}
	commit := EpochCommit{Commit: Commit{Epoch: epoch, CipherSuite: SuiteX25519AES256GCM,
		ProposalRefs: ProposalRefsFor(messages), ProposalMessages: messages,
		Roster: next, Proposals: proposals,
		Envelopes: map[string]Envelope{}, Committer: committerID}}
	commit.Path = path
	commit.Welcome = map[string]Welcome{}
	commit.TreePublics = treePublics
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
	return commit, &Epoch{Epoch: epoch, Root: root, Roster: next, TreePublics: treePublics}, nil
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

// sealNodeKey seals a path secret to one internal node's public key.
func sealNodeKey(secret []byte, node int, pub [32]byte) (Envelope, error) {
	pk, err := ecdh.X25519().NewPublicKey(pub[:])
	if err != nil {
		return Envelope{}, err
	}
	eph, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		return Envelope{}, err
	}
	shared, err := eph.ECDH(pk)
	if err != nil {
		return Envelope{}, err
	}
	label := fmt.Sprintf("node:%d", node)
	k := hkdfBytes(shared, labelHPKE, label+string(eph.PublicKey().Bytes()), 32)
	block, err := aesNewGCM(k)
	if err != nil {
		return Envelope{}, err
	}
	var env Envelope
	copy(env.Ephemeral[:], eph.PublicKey().Bytes())
	if _, err := rand.Read(env.Nonce[:]); err != nil {
		return Envelope{}, err
	}
	env.Ciphertext = block.Seal(nil, env.Nonce[:], secret, []byte(label))
	return env, nil
}

// openNodeKey opens a node-addressed envelope with a persisted node private key.
func openNodeKey(env Envelope, node int, priv []byte) ([]byte, error) {
	pub, err := ecdh.X25519().NewPublicKey(env.Ephemeral[:])
	if err != nil {
		return nil, err
	}
	key, err := ecdh.X25519().NewPrivateKey(priv)
	if err != nil {
		return nil, err
	}
	shared, err := key.ECDH(pub)
	if err != nil {
		return nil, err
	}
	label := fmt.Sprintf("node:%d", node)
	k := hkdfBytes(shared, labelHPKE, label+string(env.Ephemeral[:]), 32)
	block, err := aesNewGCM(k)
	if err != nil {
		return nil, err
	}
	secret, err := block.Open(nil, env.Nonce[:], env.Ciphertext, []byte(label))
	if err != nil {
		return nil, ErrVerification
	}
	return secret, nil
}

// DerivePathRoot recovers the epoch root for an existing member from the path
// envelopes and the index-bound chain.
func DerivePathRoot(c EpochCommit, userID string, priv [32]byte) ([]byte, error) {
	root, _, err := DerivePathRootWithKeys(c, userID, priv, nil)
	return root, err
}

// DerivePathRootWithKeys additionally accepts the node private keys a member
// persisted from earlier epochs, so a path secret addressed to a resolved internal
// node can be opened. It returns the root and the member's updated node keys: the
// secrets learned from the divergence point up, merged over the persisted ones.
func DerivePathRootWithKeys(c EpochCommit, userID string, priv [32]byte, nodePrivs map[int][]byte) ([]byte, map[int][]byte, error) {
	if c.Path == nil {
		return nil, nil, fmt.Errorf("mls: no path")
	}
	updated := map[int][]byte{}
	for node, key := range nodePrivs {
		updated[node] = key
	}
	var start []byte
	startNode := 0
	for _, r := range c.Path.Recipients {
		if r.UserID == userID {
			if secret, err := openRoot(r.Envelope, userID, priv); err == nil {
				start, startNode = secret, r.Node
				break
			}
			continue
		}
		if r.TargetNode > 0 {
			if key, ok := nodePrivs[r.TargetNode]; ok {
				if secret, err := openNodeKey(r.Envelope, r.TargetNode, key); err == nil {
					start, startNode = secret, r.Node
					break
				}
			}
		}
	}
	if start == nil {
		return nil, nil, fmt.Errorf("mls: no path envelope for %s", userID)
	}
	node := startNode
	cur := start
	updated[node] = append([]byte(nil), cur...)
	for node > 1 {
		parent := node / 2
		cur = pathNodeSecret(cur)
		node = parent
		updated[node] = append([]byte(nil), cur...)
	}
	return cur, updated, nil
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
