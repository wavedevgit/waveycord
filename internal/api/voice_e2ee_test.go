package api_test

import (
	"encoding/base64"
	"net"
	"strconv"
	"testing"
	"time"
)

// TestVoiceE2EEEnvelope proves the media crypto boundary: an AEAD frame sealed by
// one client is re-wrapped and decrypts for a same-channel peer, while tampered,
// replayed and post-rotation old-key frames are dropped.
func TestVoiceE2EEEnvelope(t *testing.T) {
	h := newHarness(t)
	alice := h.register("davealice")
	bob := h.register("davebob")
	var guild struct{ ID string }
	h.decode("POST", "/guilds", alice.Token, map[string]any{"name": "DAVE Guild"}, &guild)
	var text, voice struct{ ID string }
	h.decode("POST", "/guilds/"+guild.ID+"/channels", alice.Token, map[string]any{"name": "text"}, &text)
	h.decode("POST", "/guilds/"+guild.ID+"/channels", alice.Token, map[string]any{"name": "voice", "type": 2}, &voice)
	var invite struct{ Code string }
	h.decode("POST", "/channels/"+text.ID+"/invites", alice.Token, map[string]any{}, &invite)
	h.request("POST", "/invites/"+invite.Code, bob.Token, nil)

	aliceToken := h.joinVoice(alice, guild.ID, voice.ID)
	bobToken := h.joinVoice(bob, guild.ID, voice.ID)
	aliceVoice, aliceReady := voiceEndpoint(t, h, aliceToken, alice.ID)
	bobVoice, bobReady := voiceEndpoint(t, h, bobToken, bob.ID)
	aliceRecv := udpListener(t)
	bobRecv := udpListener(t)
	selectProtocol(t, aliceVoice, aliceRecv)
	selectProtocol(t, bobVoice, bobRecv)

	deadline := time.Now().Add(10 * time.Second)
	alicePriv, alicePub := clientKeypair(t)
	writeVoice(t, aliceVoice, map[string]any{"op": 30, "d": map[string]any{"public_key": encodeB64(alicePub)}})
	aliceKeyID, aliceServerPub := parseServerKey(t, readVoice(t, aliceVoice, deadline))
	aliceGCM := clientAEAD(t, alicePriv, aliceServerPub)

	bobPriv, bobPub := clientKeypair(t)
	writeVoice(t, bobVoice, map[string]any{"op": 30, "d": map[string]any{"public_key": encodeB64(bobPub)}})
	_, bobServerPub := parseServerKey(t, readVoice(t, bobVoice, deadline))
	bobGCM := clientAEAD(t, bobPriv, bobServerPub)

	serverAddr, err := net.ResolveUDPAddr("udp", net.JoinHostPort(aliceReady["ip"].(string),
		strconv.Itoa(int(aliceReady["port"].(float64)))))
	if err != nil {
		t.Fatal(err)
	}
	sender, err := net.DialUDP("udp", nil, serverAddr)
	if err != nil {
		t.Fatal(err)
	}
	defer sender.Close()

	aliceSSRC := uint32(aliceReady["ssrc"].(float64))
	inner := rtpPacket(aliceSSRC, "E2EE-PAYLOAD")
	sealed := clientSeal(aliceGCM, aliceKeyID, 1, aliceSSRC, inner)
	if _, err := sender.Write(sealed); err != nil {
		t.Fatalf("send sealed: %v", err)
	}

	// Bob decrypts the re-wrapped frame.
	_ = bobRecv.SetReadDeadline(time.Now().Add(5 * time.Second))
	buf := make([]byte, 1500)
	n, _, err := bobRecv.ReadFromUDP(buf)
	if err != nil {
		t.Fatalf("bob receive: %v", err)
	}
	got := clientOpen(t, bobGCM, buf[:n])
	compareMedia(t, inner, got)

	// Tampered ciphertext is dropped.
	tampered := clientSeal(aliceGCM, aliceKeyID, 2, aliceSSRC, inner)
	tampered[len(tampered)-1] ^= 0xff
	if _, err := sender.Write(tampered); err != nil {
		t.Fatal(err)
	}
	_ = bobRecv.SetReadDeadline(time.Now().Add(400 * time.Millisecond))
	if _, _, err := bobRecv.ReadFromUDP(buf); err == nil {
		t.Fatal("tampered frame was forwarded")
	}

	// Replayed counter is dropped.
	if _, err := sender.Write(clientSeal(aliceGCM, aliceKeyID, 2, aliceSSRC, inner)); err != nil {
		t.Fatal(err)
	}
	if _, err := sender.Write(clientSeal(aliceGCM, aliceKeyID, 2, aliceSSRC, inner)); err != nil {
		t.Fatal(err)
	}
	// First counter-2 frame is valid, the immediate duplicate is not.
	_ = bobRecv.SetReadDeadline(time.Now().Add(2 * time.Second))
	n, _, err = bobRecv.ReadFromUDP(buf)
	if err != nil {
		t.Fatalf("counter-2 frame missing: %v", err)
	}
	_ = clientOpen(t, bobGCM, buf[:n])
	_ = bobRecv.SetReadDeadline(time.Now().Add(400 * time.Millisecond))
	if _, _, err := bobRecv.ReadFromUDP(buf); err == nil {
		t.Fatal("replayed frame was forwarded")
	}

	// Key rotation: new key id, fresh key, traffic still decrypts for the peer.
	writeVoice(t, aliceVoice, map[string]any{"op": 33, "d": map[string]any{}})
	newKeyID, newServerPub := parseServerKey(t, readVoice(t, aliceVoice, deadline))
	if newKeyID == aliceKeyID {
		t.Fatal("key id did not rotate")
	}
	newGCM := clientAEAD(t, alicePriv, newServerPub)
	if _, err := sender.Write(clientSeal(newGCM, newKeyID, 3, aliceSSRC, inner)); err != nil {
		t.Fatal(err)
	}
	_ = bobRecv.SetReadDeadline(time.Now().Add(5 * time.Second))
	n, _, err = bobRecv.ReadFromUDP(buf)
	if err != nil {
		t.Fatalf("post-rotation frame missing: %v", err)
	}
	compareMedia(t, inner, clientOpen(t, bobGCM, buf[:n]))

	// Frames sealed with the retired key are rejected.
	if _, err := sender.Write(clientSeal(aliceGCM, aliceKeyID, 4, aliceSSRC, inner)); err != nil {
		t.Fatal(err)
	}
	_ = bobRecv.SetReadDeadline(time.Now().Add(400 * time.Millisecond))
	if _, _, err := bobRecv.ReadFromUDP(buf); err == nil {
		t.Fatal("old-key frame was forwarded after rotation")
	}
	_ = bobReady
	_ = aliceRecv
}

func encodeB64(pub [32]byte) string {
	return base64.StdEncoding.EncodeToString(pub[:])
}
