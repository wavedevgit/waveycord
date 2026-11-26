package api_test

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"encoding/binary"
	"net"
	"strconv"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/wavedevgit/waveycord/internal/gateway"
)

func escrowSealer(t *testing.T, ws *websocket.Conn, ready map[string]any, token string) func([]byte) []byte {
	t.Helper()
	raw, err := base64.StdEncoding.DecodeString(ready["mix_public_key"].(string))
	if err != nil || len(raw) != 32 {
		t.Fatalf("mix public key: %v", err)
	}
	var pub [32]byte
	copy(pub[:], raw)
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		t.Fatal(err)
	}
	wrap, err := gateway.WrapEscrowForMixer(pub, key, 1, token)
	if err != nil {
		t.Fatal(err)
	}
	_, media := clientKeypair(t)
	writeVoice(t, ws, map[string]any{"op": 34, "d": map[string]any{
		"public_key": encodeB64(media), "escrow_key": base64.StdEncoding.EncodeToString(wrap), "escrow_key_id": 1,
	}})
	block, err := aes.NewCipher(key)
	if err != nil {
		t.Fatal(err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		t.Fatal(err)
	}
	var counter uint64
	time.Sleep(150 * time.Millisecond)
	return func(inner []byte) []byte {
		counter++
		header := make([]byte, 20)
		copy(header, "WCE2")
		binary.BigEndian.PutUint32(header[4:], 1)
		binary.BigEndian.PutUint64(header[8:], counter)
		binary.BigEndian.PutUint32(header[16:], uint32(ready["ssrc"].(float64)))
		nonce := make([]byte, 12)
		binary.BigEndian.PutUint32(nonce, uint32(ready["ssrc"].(float64)))
		binary.BigEndian.PutUint64(nonce[4:], counter)
		return append(header, gcm.Seal(nil, nonce, inner, header)...)
	}
}

func TestEscrowRejectsMalformedPublication(t *testing.T) {
	h := newHarness(t)
	alice := h.register("escrowinvalid")
	var guild struct{ ID string }
	h.decode("POST", "/guilds", alice.Token, map[string]any{"name": "Invalid Escrow"}, &guild)
	var voice struct{ ID string }
	h.decode("POST", "/guilds/"+guild.ID+"/channels", alice.Token, map[string]any{"name": "voice", "type": 2}, &voice)
	token := h.joinVoice(alice, guild.ID, voice.ID)
	ws, _ := voiceEndpoint(t, h, token, alice.ID)
	_, media := clientKeypair(t)
	writeVoice(t, ws, map[string]any{"op": 34, "d": map[string]any{
		"public_key": encodeB64(media), "escrow_key": "not-base64", "escrow_key_id": 1,
	}})
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	for {
		_, _, err := ws.Read(ctx)
		if err != nil {
			if websocket.CloseStatus(err) != 4001 {
				t.Fatalf("expected protocol close, got %v", err)
			}
			return
		}
	}
}

// TestEscrowedGroupMediaForwarding proves the opt-in escrow path: a sender that wraps its
// group key for the mixer has its WCE2 frames decrypted and fed to the normal RTP path
// while a frame under an unescrowed key stays opaque.
func TestEscrowedGroupMediaForwarding(t *testing.T) {
	h := newHarness(t)
	alice := h.register("escrowalice")
	bob := h.register("escrowbob")
	var guild struct{ ID string }
	h.decode("POST", "/guilds", alice.Token, map[string]any{"name": "Escrow Guild"}, &guild)
	var text, voice struct{ ID string }
	h.decode("POST", "/guilds/"+guild.ID+"/channels", alice.Token, map[string]any{"name": "text"}, &text)
	h.decode("POST", "/guilds/"+guild.ID+"/channels", alice.Token, map[string]any{"name": "voice", "type": 2}, &voice)
	var invite struct{ Code string }
	h.decode("POST", "/channels/"+text.ID+"/invites", alice.Token, map[string]any{}, &invite)
	h.request("POST", "/invites/"+invite.Code, bob.Token, nil)

	aliceToken := h.joinVoice(alice, guild.ID, voice.ID)
	bobToken := h.joinVoice(bob, guild.ID, voice.ID)
	aliceVoice, aliceReady := voiceEndpoint(t, h, aliceToken, alice.ID)
	bobVoice, _ := voiceEndpoint(t, h, bobToken, bob.ID)
	bobRecv := udpListener(t)
	selectProtocol(t, bobVoice, bobRecv)

	mixPubB64, _ := aliceReady["mix_public_key"].(string)
	if mixPubB64 == "" {
		t.Fatal("READY did not advertise a mixer public key")
	}
	rawPub, err := base64.StdEncoding.DecodeString(mixPubB64)
	if err != nil || len(rawPub) != 32 {
		t.Fatalf("mixer key: %v len=%d", err, len(rawPub))
	}
	var serverPub [32]byte
	copy(serverPub[:], rawPub)

	groupKey := make([]byte, 32)
	if _, err := rand.Read(groupKey); err != nil {
		t.Fatal(err)
	}
	const keyID = uint32(7)
	wrap, err := gateway.WrapEscrowForMixer(serverPub, groupKey, keyID, aliceToken)
	if err != nil {
		t.Fatalf("wrap escrow: %v", err)
	}
	_, aliceMedia := clientKeypair(t)
	writeVoice(t, aliceVoice, map[string]any{"op": 34, "d": map[string]any{
		"public_key":    encodeB64(aliceMedia),
		"escrow_key":    base64.StdEncoding.EncodeToString(wrap),
		"escrow_key_id": keyID,
	}})
	time.Sleep(150 * time.Millisecond)

	aliceSSRC := uint32(aliceReady["ssrc"].(float64))
	block, err := aes.NewCipher(groupKey)
	if err != nil {
		t.Fatal(err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		t.Fatal(err)
	}
	sealFrame := func(counter uint64, kid uint32) []byte {
		inner := make([]byte, 12+16)
		inner[0] = 0x80
		binary.BigEndian.PutUint16(inner[2:4], uint16(counter))
		binary.BigEndian.PutUint32(inner[4:8], uint32(counter)*960)
		binary.BigEndian.PutUint32(inner[8:12], aliceSSRC)
		header := make([]byte, 20)
		copy(header[0:4], "WCE2")
		binary.BigEndian.PutUint32(header[4:8], kid)
		binary.BigEndian.PutUint64(header[8:16], counter)
		binary.BigEndian.PutUint32(header[16:20], aliceSSRC)
		nonce := make([]byte, 12)
		binary.BigEndian.PutUint32(nonce, aliceSSRC)
		binary.BigEndian.PutUint64(nonce[4:], counter)
		return append(header, gcm.Seal(nil, nonce, inner, header)...)
	}

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

	// Escrowed frame: the peer must receive a plaintext RTP packet, not WCE2.
	if _, err := sender.Write(sealFrame(1, keyID)); err != nil {
		t.Fatal(err)
	}
	_ = bobRecv.SetReadDeadline(time.Now().Add(10 * time.Second))
	buf := make([]byte, 1500)
	n, _, err := bobRecv.ReadFromUDP(buf)
	if err != nil {
		t.Fatalf("listener: %v", err)
	}
	if n < 12 {
		t.Fatalf("short RTP packet: %x", buf[:n])
	}
	if string(buf[0:4]) == "WCE2" {
		t.Fatal("escrowed frame was forwarded opaque instead of decoded")
	}
	if buf[0]>>6 != 2 || binary.BigEndian.Uint32(buf[8:12]) != aliceSSRC {
		t.Fatalf("expected plaintext RTP: %x", buf[:12])
	}

	// Unescrowed key id: frames stay opaque.
	if _, err := sender.Write(sealFrame(2, keyID+1)); err != nil {
		t.Fatal(err)
	}
	_ = bobRecv.SetReadDeadline(time.Now().Add(10 * time.Second))
	n, _, err = bobRecv.ReadFromUDP(buf)
	if err != nil {
		t.Fatalf("listener opaque: %v", err)
	}
	if string(buf[0:4]) != "WCE2" {
		t.Fatalf("unexpected decode for an unescrowed key id: %x", buf[:4])
	}
	if n < 20 {
		t.Fatalf("short opaque packet: %x", buf[:n])
	}

	// Matching-key authentication failure must not downgrade to opaque relay.
	bad := sealFrame(3, keyID)
	bad[len(bad)-1] ^= 1
	if _, err := sender.Write(bad); err != nil {
		t.Fatal(err)
	}
	_ = bobRecv.SetReadDeadline(time.Now().Add(150 * time.Millisecond))
	if n, _, err := bobRecv.ReadFromUDP(buf); err == nil {
		t.Fatalf("invalid escrow frame was relayed: %x", buf[:n])
	}

	// A key publication without escrow revokes mixer access.
	writeVoice(t, aliceVoice, map[string]any{"op": 34, "d": map[string]any{"public_key": encodeB64(aliceMedia)}})
	time.Sleep(150 * time.Millisecond)
	if _, err := sender.Write(sealFrame(4, keyID)); err != nil {
		t.Fatal(err)
	}
	_ = bobRecv.SetReadDeadline(time.Now().Add(5 * time.Second))
	n, _, err = bobRecv.ReadFromUDP(buf)
	if err != nil {
		t.Fatal(err)
	}
	if n < 20 || string(buf[:4]) != "WCE2" {
		t.Fatalf("revoked escrow still decrypted: %x", buf[:n])
	}
}
