package api_test

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"io"
	"net"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	"golang.org/x/crypto/hkdf"

	"github.com/wavedevgit/waveycord/internal/mls"
)

type mlsCommit struct {
	Epoch     uint32   `json:"epoch"`
	Roster    []string `json:"roster"`
	Nonce     string   `json:"nonce"`
	Committer string   `json:"committer"`
	Signature string   `json:"signature"`
}

func commitCanonical(c mlsCommit) string {
	roster := append([]string(nil), c.Roster...)
	sort.Strings(roster)
	return strconv.FormatUint(uint64(c.Epoch), 10) + "|" + strings.Join(roster, ",") + "|" + c.Nonce + "|" + c.Committer
}

func signCommit(priv ed25519.PrivateKey, c mlsCommit) mlsCommit {
	c.Signature = hex.EncodeToString(ed25519.Sign(priv, []byte(commitCanonical(c))))
	return c
}

// verifyCommit checks roster authentication: the roster must match the server's
// member list, the committer must be in it, and the signature must verify against
// the committer's published signing key.
func verifyCommit(c mlsCommit, expectedRoster []string, signKeys map[string]ed25519.PublicKey) bool {
	roster := append([]string(nil), c.Roster...)
	sort.Strings(roster)
	expected := append([]string(nil), expectedRoster...)
	sort.Strings(expected)
	if strings.Join(roster, ",") != strings.Join(expected, ",") {
		return false
	}
	committerInRoster := false
	for _, id := range roster {
		if id == c.Committer {
			committerInRoster = true
		}
	}
	if !committerInRoster {
		return false
	}
	key, ok := signKeys[c.Committer]
	if !ok {
		return false
	}
	sig, err := hex.DecodeString(c.Signature)
	if err != nil {
		return false
	}
	return ed25519.Verify(key, []byte(commitCanonical(c)), sig)
}

// treeRoot derives the epoch root from per-member leaf contributions through a
// binary hash tree.
func treeRoot(leaves map[string][]byte, roster []string) []byte {
	ids := append([]string(nil), roster...)
	sort.Strings(ids)
	level := make([][]byte, 0, len(ids))
	for _, id := range ids {
		leaf := sha256.Sum256([]byte("leaf|" + id + "|" + hex.EncodeToString(leaves[id])))
		level = append(level, leaf[:])
	}
	for len(level) > 1 {
		next := make([][]byte, 0, (len(level)+1)/2)
		for i := 0; i < len(level); i += 2 {
			if i+1 >= len(level) {
				next = append(next, level[i])
				continue
			}
			h := sha256.Sum256(append(append([]byte(nil), level[i]...), level[i+1]...))
			next = append(next, h[:])
		}
		level = next
	}
	return level[0]
}

func treeMediaKey(root []byte, epoch uint32) []byte {
	r := hkdfReader(root, "waveycord-mls-tree", "media:"+strconv.FormatUint(uint64(epoch), 10))
	key := make([]byte, 32)
	_, _ = r.Read(key)
	return key
}

func gcmFromKey(t *testing.T, key []byte) interface {
	Seal(dst, nonce, plaintext, additionalData []byte) []byte
	Open(dst, nonce, ciphertext, additionalData []byte) ([]byte, error)
} {
	t.Helper()
	g, err := newGCMFromKey(key)
	if err != nil {
		t.Fatalf("gcm: %v", err)
	}
	return g
}

// sendMLSCommit sends an op 38 commit in MLSMessage framing.
func sendMLSCommit(t *testing.T, conn *websocket.Conn, commit mls.EpochCommit) {
	t.Helper()
	encoded, err := mls.MarshalMLSMessage(commit)
	if err != nil {
		t.Fatalf("marshal mls message: %v", err)
	}
	writeVoice(t, conn, map[string]any{"op": 38, "d": map[string]any{"mls_message": encoded}})
}

// readMLSCommit reads the next op 38 frame and decodes its MLSMessage.
func readMLSCommit(t *testing.T, conn *websocket.Conn, deadline time.Time) mls.EpochCommit {
	t.Helper()
	f := readVoiceOp(t, conn, 38, deadline)
	encoded, _ := f["d"].(map[string]any)["mls_message"].(string)
	if encoded == "" {
		t.Fatalf("op 38 without mls_message: %+v", f)
	}
	commit, err := mls.UnmarshalMLSMessage(encoded)
	if err != nil {
		t.Fatalf("decode mls message: %v", err)
	}
	return commit
}

func TestVoiceMLSSignedRosterCommit(t *testing.T) {
	a, b := newPGCluster(t)
	alice := a.register("signeda")
	bob := a.register("signedb")
	charlie := a.register("signedc")
	var guild struct{ ID string }
	a.decode("POST", "/guilds", alice.Token, map[string]any{"name": "Signed MLS"}, &guild)
	var text, voice struct{ ID string }
	a.decode("POST", "/guilds/"+guild.ID+"/channels", alice.Token, map[string]any{"name": "text"}, &text)
	a.decode("POST", "/guilds/"+guild.ID+"/channels", alice.Token, map[string]any{"name": "voice", "type": 2}, &voice)
	var invite struct{ Code string }
	a.decode("POST", "/channels/"+text.ID+"/invites", alice.Token, map[string]any{}, &invite)
	for _, acc := range []account{bob, charlie} {
		a.request("POST", "/invites/"+invite.Code, acc.Token, nil)
	}

	_, aliceToken := a.joinVoiceConn(alice, guild.ID, voice.ID)
	_, bobToken := b.joinVoiceConn(bob, guild.ID, voice.ID)
	aliceVoice, aliceReady := voiceEndpoint(t, a, aliceToken, alice.ID)
	bobVoice, bobReady := voiceEndpoint(t, b, bobToken, bob.ID)
	// Charlie joins after the two voice websockets exist so both see the roster.
	charlieMain, charlieToken := a.joinVoiceConn(charlie, guild.ID, voice.ID)
	roster := func(f map[string]any) []string {
		raw := f["d"].(map[string]any)["members"].([]any)
		out := make([]string, 0, len(raw))
		for _, v := range raw {
			out = append(out, v.(string))
		}
		return out
	}
	// Every join emits a rekey, so Bob's earlier join can still be in flight; wait
	// for the roster that names all three members.
	waitRoster := func(conn *websocket.Conn, want int) map[string]any {
		t.Helper()
		deadline := time.Now().Add(20 * time.Second)
		for {
			f := readVoiceOp(t, conn, 36, deadline)
			raw, _ := f["d"].(map[string]any)["members"].([]any)
			if len(raw) == want {
				return f
			}
		}
	}
	aliceRekey := waitRoster(aliceVoice, 3)
	bobRekey := waitRoster(bobVoice, 3)
	_ = aliceRekey
	_ = bobRekey
	charlieVoice, _ := voiceEndpoint(t, a, charlieToken, charlie.ID)
	aliceRecv := udpListener(t)
	bobRecv := udpListener(t)
	charlieRecv := udpListener(t)
	selectProtocol(t, aliceVoice, aliceRecv)
	selectProtocol(t, bobVoice, bobRecv)
	selectProtocol(t, charlieVoice, charlieRecv)

	alicePriv, alicePub := clientKeypair(t)
	bobPriv, bobPub := clientKeypair(t)
	charliePriv, charliePub := clientKeypair(t)
	aliceSignPub, aliceSignPriv, _ := ed25519.GenerateKey(rand.Reader)
	bobSignPub, bobSignPriv, _ := ed25519.GenerateKey(rand.Reader)
	charlieSignPub, charlieSignPriv, _ := ed25519.GenerateKey(rand.Reader)
	signKeys := map[string]ed25519.PublicKey{alice.ID: aliceSignPub, bob.ID: bobSignPub, charlie.ID: charlieSignPub}
	privs := map[string][32]byte{alice.ID: alicePriv, bob.ID: bobPriv, charlie.ID: charliePriv}
	pubs := map[string][32]byte{alice.ID: alicePub, bob.ID: bobPub, charlie.ID: charliePub}

	publish := func(conn *websocket.Conn, id string, pub [32]byte, sign ed25519.PublicKey) {
		writeVoice(t, conn, map[string]any{"op": 34, "d": map[string]any{
			"public_key": encodeB64(pub), "signing_key": hex.EncodeToString(sign),
		}})
	}
	publish(aliceVoice, alice.ID, alicePub, aliceSignPub)
	publish(bobVoice, bob.ID, bobPub, bobSignPub)
	publish(charlieVoice, charlie.ID, charliePub, charlieSignPub)
	learnKeys(t, aliceVoice, []string{bob.ID, charlie.ID})
	learnKeys(t, bobVoice, []string{alice.ID, charlie.ID})
	learnKeys(t, charlieVoice, []string{alice.ID, bob.ID})

	sendContrib := func(from string, to []string, epoch uint32) {
		t.Helper()
		contrib := mlsContribution(privs[from], epoch)
		for _, peer := range to {
			writeVoice(t, voiceConnFor(aliceVoice, bobVoice, charlieVoice, alice.ID, bob.ID, charlie.ID, from), map[string]any{"op": 35, "d": map[string]any{
				"recipient_user_id": peer, "key_id": epoch, "kind": "contrib",
				"wrapped": wrapGroupKey(t, e2eePairwise(t, privs[from], pubs[peer]), contrib),
			}})
		}
	}
	collect := func(conn *websocket.Conn, self string, epoch uint32, peers []string) map[string][]byte {
		t.Helper()
		leaves := map[string][]byte{self: mlsContribution(privs[self], epoch)}
		need := map[string]bool{}
		for _, p := range peers {
			need[p] = true
		}
		until := time.Now().Add(20 * time.Second)
		for len(need) > 0 && time.Now().Before(until) {
			f := readVoice(t, conn, until)
			if f["op"].(float64) != 35 {
				continue
			}
			d := f["d"].(map[string]any)
			if d["kind"] != "contrib" || uint32(d["key_id"].(float64)) != epoch {
				continue
			}
			from := d["from_user_id"].(string)
			if !need[from] {
				continue
			}
			raw, err := base64.StdEncoding.DecodeString(d["wrapped"].(string))
			if err != nil || len(raw) < 28 {
				t.Fatalf("bad wrapped contribution")
			}
			plain, err := e2eePairwise(t, privs[self], pubs[from]).Open(nil, raw[:12], raw[12:], []byte("group"))
			if err != nil {
				t.Fatalf("unwrap: %v", err)
			}
			leaves[from] = plain
			delete(need, from)
		}
		if len(need) > 0 {
			t.Fatalf("missing contributions %v", need)
		}
		return leaves
	}

	// Epoch 1: a fully signed MLS commit with the three-member roster. The server
	// validates it (trust-on-first-use) before relaying.
	leaves1 := []mls.Leaf{
		{UserID: alice.ID, Public: alicePub},
		{UserID: bob.ID, Public: bobPub},
		{UserID: charlie.ID, Public: charliePub},
	}
	signKeyMap := map[string]ed25519.PublicKey{
		alice.ID: aliceSignPub, bob.ID: bobSignPub, charlie.ID: charlieSignPub,
	}
	commit1, _, err := mls.NewEpochCommit(1, leaves1, nil, alice.ID, aliceSignPriv, alicePriv)
	if err != nil {
		t.Fatalf("epoch1 commit: %v", err)
	}
	if err := mls.VerifyEpochCommit(commit1, leaves1, signKeyMap); err != nil {
		t.Fatalf("epoch1 verify: %v", err)
	}
	sendMLSCommit(t, aliceVoice, commit1)
	for _, conn := range []*websocket.Conn{bobVoice, charlieVoice} {
		got := readMLSCommit(t, conn, time.Now().Add(20*time.Second))
		if err := mls.VerifyEpochCommit(got, leaves1, signKeyMap); err != nil {
			t.Fatalf("relayed commit failed verification: %v", err)
		}
	}

	roster1 := roster(aliceRekey)
	sendContrib(alice.ID, []string{bob.ID, charlie.ID}, 1)
	sendContrib(bob.ID, []string{alice.ID, charlie.ID}, 1)
	sendContrib(charlie.ID, []string{alice.ID, bob.ID}, 1)
	aliceLeaves1 := collect(aliceVoice, alice.ID, 1, []string{bob.ID, charlie.ID})
	bobLeaves1 := collect(bobVoice, bob.ID, 1, []string{alice.ID, charlie.ID})
	charlieLeaves1 := collect(charlieVoice, charlie.ID, 1, []string{alice.ID, bob.ID})
	aliceKey1 := gcmFromKey(t, treeMediaKey(treeRoot(aliceLeaves1, roster1), 1))
	bobKey1 := gcmFromKey(t, treeMediaKey(treeRoot(bobLeaves1, roster1), 1))
	charlieKey1 := gcmFromKey(t, treeMediaKey(treeRoot(charlieLeaves1, roster1), 1))

	ssrc := uint32(aliceReady["ssrc"].(float64))
	dst, err := net.ResolveUDPAddr("udp", net.JoinHostPort(aliceReady["ip"].(string),
		strconv.Itoa(int(aliceReady["port"].(float64)))))
	if err != nil {
		t.Fatal(err)
	}
	sender, err := net.DialUDP("udp", nil, dst)
	if err != nil {
		t.Fatal(err)
	}
	defer sender.Close()
	sendMedia := func(key interface {
		Seal(dst, nonce, plaintext, additionalData []byte) []byte
	}, keyID uint32, counter uint64, payload string) []byte {
		t.Helper()
		inner := rtpPacket(ssrc, payload)
		header := make([]byte, 20)
		copy(header[0:4], "WCE2")
		binary.BigEndian.PutUint32(header[4:8], keyID)
		binary.BigEndian.PutUint64(header[8:16], counter)
		binary.BigEndian.PutUint32(header[16:20], ssrc)
		packet := append(header, key.Seal(nil, clientNonce(counter), inner, header)...)
		if _, err := sender.Write(packet); err != nil {
			t.Fatal(err)
		}
		return packet
	}
	openMedia := func(udp *net.UDPConn, key interface {
		Open(dst, nonce, ciphertext, additionalData []byte) ([]byte, error)
	}, want string) {
		t.Helper()
		_ = udp.SetReadDeadline(time.Now().Add(20 * time.Second))
		buf := make([]byte, 1500)
		n, _, err := udp.ReadFromUDP(buf)
		if err != nil {
			t.Fatalf("media receive: %v", err)
		}
		counter := binary.BigEndian.Uint64(buf[8:16])
		plain, err := key.Open(nil, clientNonce(counter), buf[20:n], buf[0:20])
		if err != nil {
			t.Fatalf("media decrypt: %v", err)
		}
		if !bytes.Contains(plain, []byte(want)) {
			t.Fatalf("payload mismatch")
		}
	}
	sendMedia(aliceKey1, 1, 1, "SIGNED-EPOCH1")
	openMedia(bobRecv, bobKey1, "SIGNED-EPOCH1")
	openMedia(charlieRecv, charlieKey1, "SIGNED-EPOCH1")

	// Charlie leaves; Alice commits epoch 2 with an MLS Remove proposal using the
	// internal/mls key schedule, relayed to the other instance.
	writeFrame(t, charlieMain, map[string]any{"op": 4, "d": map[string]any{"guild_id": guild.ID, "channel_id": nil}})
	left := readVoiceOp(t, bobVoice, 36, time.Now().Add(20*time.Second))
	roster2 := roster(left)
	if len(roster2) != 2 {
		t.Fatalf("epoch2 roster = %v", roster2)
	}
	// A prior path update by Charlie establishes the internal node above his slot and
	// publishes a stable leaf layout; carrying it forward keeps Charlie's slot blank
	// instead of compacting the tree.
	_, priorEpoch, err := mls.NewEpochCommit(1, leaves1, nil, charlie.ID, charlieSignPriv, charliePriv)
	if err != nil {
		t.Fatalf("prior epoch: %v", err)
	}
	// The removal proposal is authored and signed by Bob, not by the committer.
	removeProposal := mls.Proposal{Type: "remove", UserID: charlie.ID}
	bobSigned := mls.SignProposalMessage(removeProposal, bob.ID, bobSignPriv)
	commit2, epoch2State, err := mls.NewEpochCommitWithMessages(2, leaves1,
		[]mls.Proposal{removeProposal}, []mls.ProposalMessage{bobSigned},
		alice.ID, aliceSignPriv, alicePriv, priorEpoch.Tree)
	if err != nil {
		t.Fatalf("epoch2 commit: %v", err)
	}
	if err := mls.VerifyEpochCommit(commit2, leaves1, signKeyMap); err != nil {
		t.Fatalf("epoch2 verify: %v", err)
	}
	{
		raw, _ := json.Marshal(commit2)
		var rt mls.EpochCommit
		if err := json.Unmarshal(raw, &rt); err != nil {
			t.Fatalf("roundtrip decode: %v", err)
		}
		if err := mls.VerifyEpochCommit(rt, leaves1, signKeyMap); err != nil {
			t.Fatalf("roundtrip verify: %v", err)
		}
	}
	sendMLSCommit(t, aliceVoice, commit2)
	relayed := readMLSCommit(t, bobVoice, time.Now().Add(20*time.Second))
	if err := mls.VerifyEpochCommit(relayed, leaves1, signKeyMap); err != nil {
		t.Fatalf("relayed epoch2 verify: %v", err)
	}
	if len(relayed.ProposalMessages) != 1 || relayed.ProposalMessages[0].Proposer != bob.ID {
		t.Fatalf("relayed proposer: %+v", relayed.ProposalMessages)
	}
	// The committer signing the same proposal on Bob's behalf must fail verification.
	forgedMsg := mls.SignProposalMessage(removeProposal, bob.ID, aliceSignPriv)
	forgedCommit, _, err := mls.NewEpochCommitWithMessages(2, leaves1,
		[]mls.Proposal{removeProposal}, []mls.ProposalMessage{forgedMsg},
		alice.ID, aliceSignPriv, alicePriv, priorEpoch.Tree)
	if err != nil {
		t.Fatalf("forged commit build: %v", err)
	}
	if err := mls.VerifyEpochCommit(forgedCommit, leaves1, signKeyMap); err == nil {
		t.Fatal("forged proposer signature accepted")
	}
	// The proposal travels as a signed wire message, not only as a hash reference.
	if relayed.Tree == nil || relayed.Tree.Size != 4 {
		t.Fatalf("relayed tree layout: %+v", relayed.Tree)
	}
	if slot := relayed.Tree.SlotFor(charlie.ID); slot != -1 {
		t.Fatalf("removed member retained slot %d", slot)
	}
	// The removed member's branch may not be addressed by its old node key.
	for _, r := range relayed.Path.Recipients {
		if r.TargetNode == 3 {
			t.Fatalf("relayed commit addressed the removed member's stale node: %+v", r)
		}
	}
	if _, stale := relayed.Tree.Publics[3]; stale {
		t.Fatal("relayed tree kept the removed member's node key")
	}
	if len(relayed.TreePublics) == 0 {
		t.Fatal("relayed epoch2 commit lost its tree keys")
	}
	if len(relayed.ProposalMessages) != 1 {
		t.Fatalf("epoch2 proposal messages = %d", len(relayed.ProposalMessages))
	}
	pm := relayed.ProposalMessages[0]
	if pm.Proposal.Type != "remove" || pm.Proposal.UserID != charlie.ID || pm.Proposer != bob.ID {
		t.Fatalf("epoch2 proposal message: %+v", pm)
	}
	if err := mls.VerifyProposalMessage(pm, signKeyMap); err != nil {
		t.Fatalf("epoch2 proposal signature: %v", err)
	}
	if refs := mls.ProposalRefsFor(relayed.ProposalMessages); len(refs) != 1 || refs[0] != relayed.ProposalRefs[0] {
		t.Fatalf("commit refs %v do not bind the wire message %v", relayed.ProposalRefs, refs)
	}
	tamperedMsg := relayed
	tamperedMsg.ProposalMessages = []mls.ProposalMessage{pm}
	tamperedMsg.ProposalMessages[0].Proposal.UserID = bob.ID
	if err := mls.VerifyEpochCommit(tamperedMsg, leaves1, signKeyMap); err == nil {
		t.Fatal("tampered proposal message verified")
	}
	bobRoot, err := mls.DerivePathRoot(relayed, bob.ID, bobPriv)
	if err != nil {
		t.Fatalf("bob decrypt epoch2 root: %v", err)
	}
	bobKey2 := gcmFromKey(t, mls.EpochSecret(bobRoot, relayed.Roster))
	aliceKey2 := gcmFromKey(t, mls.EpochSecret(epoch2State.Root, commit2.Roster))
	sendMedia(aliceKey2, 2, 2, "SIGNED-EPOCH2")
	openMedia(bobRecv, bobKey2, "SIGNED-EPOCH2")

	// Charlie receives no epoch-2 commit or contribution.
	deadline := time.Now().Add(1200 * time.Millisecond)
	for time.Now().Before(deadline) {
		f, ok := tryReadVoice(charlieVoice, time.Until(deadline))
		if !ok {
			break
		}
		if f["op"].(float64) == 38 {
			t.Fatal("departed member received an epoch-2 commit")
		}
		if f["op"].(float64) == 35 {
			if uint32(f["d"].(map[string]any)["key_id"].(float64)) == 2 {
				t.Fatal("departed member received an epoch-2 contribution")
			}
		}
	}

	// Epoch 3 adds Dave, who joins from the Welcome and can decrypt media.
	dave := "user-dave"
	davePriv, davePub := clientKeypair(t)
	daveSignPub, _, _ := ed25519.GenerateKey(rand.Reader)
	signKeyMap[dave] = daveSignPub
	// Epoch 3 negotiates the AES-128-GCM suite.
	prevTree := epoch2State.Tree
	commit3, epoch3State, err := mls.NewEpochCommitNegotiating(3, commit2.Roster,
		[]mls.Proposal{{Type: "add", UserID: dave, Public: davePub}},
		mls.SuiteX25519AES128GCM, alice.ID, aliceSignPriv, alicePriv, prevTree)
	if err != nil {
		t.Fatalf("epoch3 commit: %v", err)
	}
	if err := mls.VerifyEpochCommit(commit3, commit2.Roster, signKeyMap); err != nil {
		t.Fatalf("epoch3 verify: %v", err)
	}
	if commit3.CipherSuite != mls.SuiteX25519AES128GCM {
		t.Fatalf("epoch3 suite = %#x", commit3.CipherSuite)
	}
	// Relay it through the server, which must accept the negotiated suite.
	sendMLSCommit(t, aliceVoice, commit3)
	relayed3 := readMLSCommit(t, bobVoice, time.Now().Add(20*time.Second))
	if relayed3.CipherSuite != mls.SuiteX25519AES128GCM {
		t.Fatalf("relayed suite = %#x", relayed3.CipherSuite)
	}
	if err := mls.VerifyEpochCommit(relayed3, commit2.Roster, signKeyMap); err != nil {
		t.Fatalf("relayed epoch3 verify: %v", err)
	}
	if len(commit3.ProposalMessages) != 2 {
		t.Fatalf("epoch3 proposal messages: %+v", commit3.ProposalMessages)
	}
	sawAdd, sawSuite := false, false
	for _, m := range commit3.ProposalMessages {
		switch m.Proposal.Type {
		case "add":
			sawAdd = m.Proposal.UserID == dave
		case "ciphersuite":
			sawSuite = m.Proposal.Suite == mls.SuiteX25519AES128GCM
		}
	}
	if !sawAdd || !sawSuite {
		t.Fatalf("epoch3 proposals: %+v", commit3.ProposalMessages)
	}
	_ = sawAdd
	for _, m := range commit3.ProposalMessages {
		if err := mls.VerifyProposalMessage(m, signKeyMap); err != nil {
			t.Fatalf("epoch3 proposal signature (%s): %v", m.Proposal.Type, err)
		}
	}
	if len(commit3.TreePublics) == 0 || len(epoch3State.TreePublics) == 0 {
		t.Fatal("epoch3 did not publish persisted tree keys")
	}
	carried := 0
	for node := range epoch2State.TreePublics {
		if _, ok := commit3.TreePublics[node]; ok {
			carried++
		}
	}
	if carried == 0 {
		t.Fatal("epoch3 dropped every previous tree key")
	}
	badAdd := commit3
	badAdd.ProposalMessages = append([]mls.ProposalMessage(nil), commit3.ProposalMessages...)
	badAdd.ProposalMessages[0].Signature = []byte{9, 9, 9}
	if err := mls.VerifyEpochCommit(badAdd, commit2.Roster, signKeyMap); err == nil {
		t.Fatal("tampered add proposal signature verified")
	}
	daveRoot, daveNodeKeys, err := mls.DecryptWelcomeTree(commit3, dave, davePriv)
	if err != nil {
		t.Fatalf("dave welcome: %v", err)
	}
	if !bytes.Equal(daveRoot, epoch3State.Root) {
		t.Fatal("dave welcome root mismatch")
	}
	welcomeJSON, _ := json.Marshal(commit3.Welcome[dave])
	if !strings.Contains(string(welcomeJSON), "group_info") {
		t.Fatalf("welcome is not encrypted group info: %s", welcomeJSON)
	}
	if strings.Contains(string(welcomeJSON), `"root"`) || strings.Contains(string(welcomeJSON), `"node_keys"`) {
		t.Fatalf("welcome still carries separate envelopes: %s", welcomeJSON)
	}
	if len(commit3.Welcome[dave].GroupInfo.Ciphertext) == 0 {
		t.Fatal("welcome group info is empty")
	}
	nodeAddressed := 0
	for _, r := range commit3.Path.Recipients {
		if r.TargetNode > 0 {
			nodeAddressed++
		}
	}
	if nodeAddressed == 0 {
		t.Fatalf("epoch3 has no node-addressed envelope: %+v", commit3.Path.Recipients)
	}
	if len(daveNodeKeys) == 0 {
		t.Fatal("welcome node keys did not decrypt for dave")
	}
	// The newcomer opens the node-addressed path secret with its Welcome keys.
	derived, _, err := mls.DerivePathRootWithKeys(commit3, dave, davePriv, daveNodeKeys)
	if err != nil || !bytes.Equal(derived, epoch3State.Root) {
		t.Fatalf("dave node-addressed derivation: %v", err)
	}
	if _, _, err := mls.DerivePathRootWithKeys(commit3, dave, davePriv, nil); err == nil {
		t.Fatal("dave opened a node-addressed envelope without Welcome keys")
	}
	// Media for epoch 3 uses the per-application key schedule, not the epoch root
	// directly: the newcomer derives the same key, nonce and exporter from the root
	// it received in the Welcome.
	daveEpochSecret := mls.EpochSecret(daveRoot, commit3.Roster)
	aliceEpochSecret := mls.EpochSecret(epoch3State.Root, commit3.Roster)
	if !bytes.Equal(daveEpochSecret, aliceEpochSecret) {
		t.Fatal("newcomer epoch secret differs from the committer's")
	}
	daveApp := mls.DeriveApplicationKeysFor(daveEpochSecret, 3, mls.SuiteX25519AES128GCM)
	aliceApp := mls.DeriveApplicationKeysFor(aliceEpochSecret, 3, mls.SuiteX25519AES128GCM)
	if len(daveApp.Key) != 16 {
		t.Fatalf("AES-128 key size %d", len(daveApp.Key))
	}
	if !bytes.Equal(daveApp.Key, aliceApp.Key) || !bytes.Equal(daveApp.BaseNonce, aliceApp.BaseNonce) ||
		!bytes.Equal(daveApp.Exporter, aliceApp.Exporter) {
		t.Fatal("application key schedule disagrees between members")
	}
	if bytes.Equal(daveApp.Key, daveApp.Exporter) {
		t.Fatal("key and exporter are not separated")
	}
	aliceGCM3 := gcmFromKey(t, aliceApp.Key)
	daveGCM3 := gcmFromKey(t, daveApp.Key)
	inner3 := rtpPacket(ssrc, "WELCOME-EPOCH3")
	header3 := make([]byte, 20)
	copy(header3[0:4], "WCE2")
	binary.BigEndian.PutUint32(header3[4:8], 3)
	binary.BigEndian.PutUint64(header3[8:16], 5)
	binary.BigEndian.PutUint32(header3[16:20], ssrc)
	packet3 := append(header3, aliceGCM3.Seal(nil, mls.NonceFor(aliceApp.BaseNonce, 5), inner3, header3)...)
	plain3, err := daveGCM3.Open(nil, mls.NonceFor(daveApp.BaseNonce, 5), packet3[20:], packet3[0:20])
	if err != nil || !bytes.Contains(plain3, []byte("WELCOME-EPOCH3")) {
		t.Fatalf("dave could not decrypt epoch-3 media: %v", err)
	}
	if _, err := daveGCM3.Open(nil, mls.NonceFor(daveApp.BaseNonce, 6), packet3[20:], packet3[0:20]); err == nil {
		t.Fatal("wrong application nonce decrypted epoch-3 media")
	}

	// The removed member has no path envelope and cannot derive the epoch-2 root.
	if _, err := mls.DerivePathRoot(relayed, charlie.ID, charliePriv); err == nil {
		t.Fatal("removed member derived the path root")
	}
	if _, err := mls.DecryptWelcome(relayed, charlie.ID, charliePriv); err == nil {
		t.Fatal("removed member had a Welcome")
	}

	// Forged commits fail roster authentication.
	forgedRoster := signCommit(aliceSignPriv, mlsCommit{
		Epoch: 3, Roster: append(append([]string(nil), roster2...), charlie.ID), Nonce: "x", Committer: alice.ID,
	})
	if verifyCommit(forgedRoster, roster2, signKeys) {
		t.Fatal("commit with an unauthenticated extra member verified")
	}
	outsider := signCommit(charlieSignPriv, mlsCommit{
		Epoch: 3, Roster: roster2, Nonce: "y", Committer: charlie.ID,
	})
	if verifyCommit(outsider, roster2, signKeys) {
		t.Fatal("commit signed by a non-member verified")
	}

	// Charlie cannot derive the epoch-2 root: he only has his own leaf.
	charlieSolo := treeMediaKey(treeRoot(map[string][]byte{
		charlie.ID: mlsContribution(charliePriv, 2),
	}, []string{charlie.ID}), 2)
	charlieSoloGCM := gcmFromKey(t, charlieSolo)
	packet2 := sendMedia(aliceKey2, 2, 3, "SIGNED-EPOCH2")
	counter2 := binary.BigEndian.Uint64(packet2[8:16])
	if _, err := charlieSoloGCM.Open(nil, clientNonce(counter2), packet2[20:], packet2[0:20]); err == nil {
		t.Fatal("departed member derived the epoch-2 tree secret")
	}

	_ = bobSignPriv
	_ = bobReady
	_ = aliceRecv
	_ = charlieRecv
}

// small local helpers to avoid extra imports in the test
func jsonMarshal(v any) ([]byte, error) { return marshalJSON(v) }

func jsonUnmarshalBytes(data []byte, v any) error { return unmarshalJSON(data, v) }

func voiceConnFor(aliceC, bobC, charlieC *websocket.Conn, aliceID, bobID, charlieID, who string) *websocket.Conn {
	switch who {
	case aliceID:
		return aliceC
	case bobID:
		return bobC
	case charlieID:
		return charlieC
	}
	return nil
}

func hkdfReader(secret []byte, salt, info string) io.Reader {
	return hkdf.New(sha256.New, secret, []byte(salt), []byte(info))
}

func newGCMFromKey(key []byte) (cipher.AEAD, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}

func marshalJSON(v any) ([]byte, error) { return json.Marshal(v) }

func unmarshalJSON(data []byte, v any) error { return json.Unmarshal(data, v) }
