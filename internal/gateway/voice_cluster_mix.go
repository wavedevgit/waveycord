package gateway

import (
	"encoding/base64"
	"encoding/binary"
	"hash/fnv"
	"time"
)

// Cluster-wide mixing: one instance owns a channel's mixer (rendezvous hash over
// live instance ids). Non-owners decode locally and publish PCM over the bus; the
// owner mixes local and remote PCM and publishes the encoded output, which every
// instance fans out to its own members.

// SetClusterIdentity enables cluster-wide mixing for this instance.
func (v *VoiceServer) SetClusterIdentity(instanceID string, enabled bool) {
	v.mu.Lock()
	v.instanceID = instanceID
	v.clusterMix = enabled
	v.mu.Unlock()
	if enabled && v.store != nil {
		_ = v.store.RegisterInstance(instanceID)
	}
}

func score(instanceID, channelID string) uint64 {
	h := fnv.New64a()
	_, _ = h.Write([]byte(instanceID))
	_, _ = h.Write([]byte{0})
	_, _ = h.Write([]byte(channelID))
	return h.Sum64()
}

// mixerOwner returns the instance that owns the channel mixer.
func (v *VoiceServer) mixerOwner(channelID string) string {
	v.mu.Lock()
	self := v.instanceID
	enabled := v.clusterMix
	v.mu.Unlock()
	if !enabled || v.store == nil {
		return self
	}
	instances, err := v.store.LiveInstances(15 * time.Second)
	if err != nil || len(instances) == 0 {
		return self
	}
	best := instances[0]
	bestScore := score(best, channelID)
	for _, id := range instances[1:] {
		if s := score(id, channelID); s > bestScore {
			best, bestScore = id, s
		}
	}
	return best
}

type mixPCM struct {
	ChannelID string `json:"channel_id"`
	SSRC      uint32 `json:"ssrc"`
	PCM       string `json:"pcm"`
}

func encodePCM(pcm []int16) string {
	raw := make([]byte, len(pcm)*2)
	for i, s := range pcm {
		binary.LittleEndian.PutUint16(raw[i*2:], uint16(s))
	}
	return base64.StdEncoding.EncodeToString(raw)
}

func decodePCM(raw string) []int16 {
	data, err := base64.StdEncoding.DecodeString(raw)
	if err != nil || len(data) < 2 {
		return nil
	}
	out := make([]int16, len(data)/2)
	for i := range out {
		out[i] = int16(binary.LittleEndian.Uint16(data[i*2:]))
	}
	return out
}

// publishPCM sends a decoded frame to the channel mixer owner.
func (v *VoiceServer) publishPCM(channelID string, ssrc uint32, pcm []int16) {
	if v.publish == nil {
		return
	}
	v.publish("VOICE_MIX_PCM", mixPCM{ChannelID: channelID, SSRC: ssrc, PCM: encodePCM(pcm)})
}

// DeliverMixPCM feeds PCM received from another instance into the local mixer.
func (v *VoiceServer) DeliverMixPCM(channelID string, ssrc uint32, pcmB64 string) {
	pcm := decodePCM(pcmB64)
	if len(pcm) == 0 {
		return
	}
	v.feedMixer(channelID, ssrc, pcm)
}

// publishMixed sends an encoded mixed frame to other instances.
func (v *VoiceServer) publishMixed(channelID string, packet []byte) {
	if v.publish == nil {
		return
	}
	v.publish("VOICE_MIX_OUT", map[string]any{
		"channel_id": channelID,
		"packet":     base64.StdEncoding.EncodeToString(packet),
	})
}

// DeliverMixed fans a mixed frame from the owner out to local members.
func (v *VoiceServer) DeliverMixed(channelID string, packet []byte) {
	v.deliverRaw(channelID, packet)
}

// deliverRaw sends a packet to every local connection in a channel.
func (v *VoiceServer) deliverRaw(channelID string, packet []byte) {
	v.mu.Lock()
	targets := make([]*voiceConn, 0, len(v.conns))
	for c := range v.conns {
		if c.channel() == channelID {
			targets = append(targets, c)
		}
	}
	v.mu.Unlock()
	for _, c := range targets {
		if addr := c.udp(); addr != nil {
			_, _ = v.udp.WriteToUDP(packet, addr)
		}
	}
}
