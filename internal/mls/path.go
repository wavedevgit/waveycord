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
	Tree    *TreeState         `json:"tree,omitempty"`
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
// TreeState is the persisted leaf layout and node keys. Leaf positions are
// indices (0 .. Size-1): removing a member blanks the index instead of compacting,
// so a retained member keeps its slot and any persisted node key above it stays
// valid.
type TreeState struct {
	Size      int              `json:"size"`
	Users     []string         `json:"users"`
	Positions []int            `json:"positions"`
	Publics   map[int][32]byte `json:"publics"`
}

// SlotFor returns the leaf index for a user, or -1.
func (t *TreeState) SlotFor(userID string) int {
	for i, u := range t.Users {
		if u == userID && i < len(t.Positions) {
			return t.Positions[i]
		}
	}
	return -1
}

// SlotUsers maps absolute leaf nodes to member ids.
func (t *TreeState) SlotUsers() map[int]string {
	out := map[int]string{}
	for i, u := range t.Users {
		if i < len(t.Positions) {
			out[t.Size+t.Positions[i]] = u
		}
	}
	return out
}

// NewEpochCommit keeps the dense layout (a fresh tree per commit).
func NewEpochCommit(epoch uint32, roster []Leaf, proposals []Proposal, committerID string, committerSignPriv ed25519.PrivateKey, committerLeafPriv [32]byte) (EpochCommit, *Epoch, error) {
	return NewEpochCommitWithState(epoch, roster, proposals, committerID, committerSignPriv, committerLeafPriv, nil)
}

// NewEpochCommitWithTree adapts a bare node-key map to a dense previous layout.
func NewEpochCommitWithTree(epoch uint32, roster []Leaf, proposals []Proposal, committerID string, committerSignPriv ed25519.PrivateKey, committerLeafPriv [32]byte, previousTree map[int][32]byte) (EpochCommit, *Epoch, error) {
	var prev *TreeState
	if len(previousTree) > 0 {
		size := TreeSize(len(roster))
		if size < 2 {
			size = 2
		}
		prev = &TreeState{Size: size, Publics: previousTree}
		for i, l := range roster {
			prev.Users = append(prev.Users, l.UserID)
			prev.Positions = append(prev.Positions, i)
		}
	}
	return NewEpochCommitWithState(epoch, roster, proposals, committerID, committerSignPriv, committerLeafPriv, prev)
}

// NewEpochCommitWithState creates a path-updated, signed epoch. When previous is
// supplied, retained members keep their leaf slots, removed members leave blanks and
// newcomers fill the lowest free slot, extending the tree if it is full.
func NewEpochCommitWithState(epoch uint32, roster []Leaf, proposals []Proposal, committerID string, committerSignPriv ed25519.PrivateKey, committerLeafPriv [32]byte, previous *TreeState) (EpochCommit, *Epoch, error) {
	next, err := ApplyProposals(roster, proposals)
	if err != nil {
		return EpochCommit{}, nil, err
	}
	if len(next) == 0 {
		return EpochCommit{}, nil, fmt.Errorf("mls: empty roster")
	}
	size := 0
	prevSlot := map[string]int{}
	prevPublics := map[int][32]byte{}
	if previous != nil {
		size = previous.Size
		for i, u := range previous.Users {
			if i < len(previous.Positions) {
				prevSlot[u] = previous.Positions[i]
			}
		}
		for node, pub := range previous.Publics {
			prevPublics[node] = pub
		}
	}
	if need := TreeSize(len(next)); size < need {
		size = need
	}
	if size < 2 {
		size = 2
	}
	// Allocate slots: reuse retained members, give newcomers the lowest free index.
	used := map[int]bool{}
	positions := make([]int, len(next))
	for i, l := range next {
		if idx, ok := prevSlot[l.UserID]; ok && idx >= 0 && idx < size && !used[idx] {
			positions[i] = idx
			used[idx] = true
		} else {
			positions[i] = -1
		}
	}
	for i := range positions {
		if positions[i] >= 0 {
			continue
		}
		idx := -1
		for j := 0; j < size; j++ {
			if !used[j] {
				idx = j
				break
			}
		}
		if idx < 0 {
			// Grow the tree and place the member in the first slot of the new half.
			for j := 0; j < size; j++ {
				used[j] = true
			}
			oldSize := size
			size *= 2
			idx = oldSize
		}
		positions[i] = idx
		used[idx] = true
	}
	tree := &TreeState{Size: size, Publics: prevPublics}
	for i, l := range next {
		tree.Users = append(tree.Users, l.UserID)
		tree.Positions = append(tree.Positions, positions[i])
	}
	ci := leafIndex(next, committerID)
	if ci < 0 {
		return EpochCommit{}, nil, fmt.Errorf("mls: committer not in roster")
	}
	committerNode := size + positions[ci]

	// Fresh secret at the committer's leaf, then a hash chain to the root.
	leafSecret := make([]byte, 32)
	if _, err := rand.Read(leafSecret); err != nil {
		return EpochCommit{}, nil, err
	}
	node := committerNode
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
	treePublics := map[int][32]byte{}
	for n, pub := range prevPublics {
		treePublics[n] = pub
	}
	for n, pub := range publics {
		treePublics[n] = pub
	}
	tree.Publics = treePublics
	path := &PathChain{CommitterLeaf: committerNode, Publics: publics}
	// A self-envelope lets the committer replay its own chain uniformly.
	if selfEnv, err := sealRoot(leafSecret, next[ci]); err == nil {
		path.Recipients = append(path.Recipients, PathRecipient{Node: committerNode, UserID: committerID, Envelope: selfEnv})
	}

	// Presence: occupied leaves plus persisted node keys that are still usable. An
	// internal node is dropped when a removed member sat under it, so a stale key the
	// departed member still holds can never be addressed again, and when it no longer
	// covers any current member.
	removed := map[string]bool{}
	for _, p := range proposals {
		if p.Type == "remove" {
			removed[p.UserID] = true
		}
	}
	prevSlots := map[int]string{}
	// Slot indices only compare when the tree did not grow in this commit.
	if previous != nil && previous.Size == size {
		for node, uid := range previous.SlotUsers() {
			prevSlots[node] = uid
		}
	}
	present := map[int]bool{}
	slotUser := map[int]string{}
	for i, l := range next {
		abs := size + positions[i]
		present[abs] = true
		slotUser[abs] = l.UserID
	}
	rekeyed := map[int]bool{}
	for n := range prevPublics {
		if n >= size {
			continue
		}
		stale := false
		for _, uid := range membersUnder(n, size, prevSlots) {
			if removed[uid] {
				stale = true
				break
			}
		}
		if len(membersUnder(n, size, slotUser)) == 0 {
			delete(treePublics, n)
			continue
		}
		if stale {
			// The branch still has retained members, so reissue it: a fresh node
			// secret, a fresh public key, and the new secret delivered to every
			// retained member under the node. The removed member's old key opens
			// nothing.
			fresh := make([]byte, 32)
			if _, err := rand.Read(fresh); err != nil {
				return EpochCommit{}, nil, err
			}
			secrets[n] = fresh
			priv, err := ecdh.X25519().NewPrivateKey(fresh)
			if err == nil {
				pub := clone32(priv.PublicKey().Bytes())
				publics[n] = pub
				treePublics[n] = pub
			}
			rekeyed[n] = true
			tree.Publics = treePublics
			for _, uid := range membersUnder(n, size, slotUser) {
				i := leafIndex(next, uid)
				env, err := sealRoot(fresh, next[i])
				if err != nil {
					return EpochCommit{}, nil, err
				}
				path.Recipients = append(path.Recipients, PathRecipient{Node: n, UserID: uid, Envelope: env})
			}
		}
		present[n] = true
	}
	node = committerNode
	for node > 1 {
		parent := node / 2
		sibling := node ^ 1
		secret := secrets[parent]
		seen := map[string]bool{}
		for _, resNode := range Resolution(sibling, size, present) {
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
			for _, uid := range membersUnder(resNode, size, slotUser) {
				if seen[uid] {
					continue
				}
				seen[uid] = true
				i := leafIndex(next, uid)
				env, err := sealRoot(secret, next[i])
				if err != nil {
					return EpochCommit{}, nil, err
				}
				path.Recipients = append(path.Recipients, PathRecipient{Node: parent, UserID: uid, Envelope: env})
			}
		}
		node = parent
	}
	root := secrets[1]
	// Members whose branch was rekeyed get the root directly as well: their fresh
	// node secret does not chain into the committer's independently chosen ancestors.
	for n := range rekeyed {
		for _, uid := range membersUnder(n, size, slotUser) {
			i := leafIndex(next, uid)
			env, err := sealRoot(root, next[i])
			if err != nil {
				return EpochCommit{}, nil, err
			}
			path.Recipients = append(path.Recipients, PathRecipient{Node: 1, UserID: uid, Envelope: env})
		}
	}
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
	commit.Tree = tree
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
	return commit, &Epoch{Epoch: epoch, Root: root, Roster: next, TreePublics: treePublics, Tree: tree}, nil
}

// membersUnder lists the members under an absolute node.
func membersUnder(node, size int, slotUser map[int]string) []string {
	var out []string
	var walk func(int)
	walk = func(n int) {
		if n >= size {
			if uid, ok := slotUser[n]; ok {
				out = append(out, uid)
			}
			return
		}
		walk(2 * n)
		walk(2*n + 1)
	}
	walk(node)
	return out
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
	opened := map[int][]byte{}
	for _, r := range c.Path.Recipients {
		if r.UserID == userID {
			if secret, err := openRoot(r.Envelope, userID, priv); err == nil {
				opened[r.Node] = secret
			}
			continue
		}
		if r.TargetNode > 0 {
			if key, ok := nodePrivs[r.TargetNode]; ok {
				if secret, err := openNodeKey(r.Envelope, r.TargetNode, key); err == nil {
					opened[r.Node] = secret
				}
			}
		}
	}
	if len(opened) == 0 {
		return nil, nil, fmt.Errorf("mls: no path envelope for %s", userID)
	}
	// The secret closest to the root wins for the root value; deeper opened secrets
	// are still persisted (they are the member's branch keys).
	closest := 0
	for n := range opened {
		if closest == 0 || n < closest {
			closest = n
		}
	}
	node := closest
	cur := opened[node]
	updated[node] = append([]byte(nil), cur...)
	for node > 1 {
		parent := node / 2
		cur = pathNodeSecret(cur)
		node = parent
		if _, exists := updated[node]; !exists {
			updated[node] = append([]byte(nil), cur...)
		}
	}
	for n, secret := range opened {
		if _, exists := updated[n]; !exists {
			updated[n] = append([]byte(nil), secret...)
		}
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
