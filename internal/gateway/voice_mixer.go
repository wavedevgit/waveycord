package gateway

import (
	"encoding/binary"
	"math"
	"sync"
	"time"

	"github.com/wavedevgit/waveycord/internal/voicecodec"
)

// Channel mixing for the plaintext SFU path. When two or more senders deliver
// decodable Opus in one channel, the server decodes each stream, mixes 20 ms PCM
// frames (padding short frames such as 10 ms and trimming long ones), encodes the
// result once and sends one mixed RTP stream to every member. Single-sender
// channels keep the per-packet forwarding path (and its jitter buffer).
type channelMixer struct {
	mu       sync.Mutex
	streams  map[uint32]*pcmStream
	seq      uint16
	ts       uint32
	ssrc     uint32
	codec    voicecodec.Codec
	lastEmit time.Time
}

type pcmStream struct {
	pcm      []int16
	lastSeen time.Time
}

func (v *VoiceServer) mixer(channelID string) *channelMixer {
	v.mixMu.Lock()
	defer v.mixMu.Unlock()
	select {
	case <-v.done:
		return nil
	default:
	}
	if v.mixers == nil {
		v.mixers = map[string]*channelMixer{}
	}
	m := v.mixers[channelID]
	if m == nil {
		m = &channelMixer{streams: map[uint32]*pcmStream{}, ssrc: randomUint32(), codec: newVoiceCodec()}
		v.mixers[channelID] = m
	}
	return m
}

func newVoiceCodec() voicecodec.Codec {
	if !voicecodec.Available() {
		return nil
	}
	c, err := voicecodec.New()
	if err != nil {
		return nil
	}
	return c
}

// feedMixer buffers decoded PCM. It reports true when the channel currently has at
// least two recent senders, in which case the caller must not forward the packet
// separately.
func (v *VoiceServer) feedMixer(channelID string, ssrc uint32, pcm []int16) bool {
	if channelID == "" || !v.codecReady {
		return false
	}
	m := v.mixer(channelID)
	if m == nil {
		return false
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	pending := append(m.streams[ssrc].pcmOrNil(), pcm...)
	const maxPending = voicecodec.FrameSize * 10
	if len(pending) > maxPending {
		pending = pending[len(pending)-maxPending:]
	}
	m.streams[ssrc] = &pcmStream{pcm: pending, lastSeen: time.Now()}
	return m.recentSenders() >= 2
}

func (s *pcmStream) pcmOrNil() []int16 {
	if s == nil {
		return nil
	}
	return s.pcm
}

func (m *channelMixer) recentSenders() int {
	cutoff := time.Now().Add(-1 * time.Second)
	n := 0
	for _, s := range m.streams {
		if s.lastSeen.After(cutoff) {
			n++
		}
	}
	return n
}

// mixOnce emits one 20 ms mixed frame when at least two senders are active.
func (v *VoiceServer) mixOnce(channelID string, m *channelMixer) ([]byte, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for ssrc, stream := range m.streams {
		if time.Since(stream.lastSeen) > time.Second {
			delete(m.streams, ssrc)
		}
	}
	if m.codec == nil || m.recentSenders() < 2 {
		return nil, false
	}
	const frame = voicecodec.FrameSize
	mix := make([]int32, frame)
	active := 0
	for _, s := range m.streams {
		if !s.lastSeen.After(time.Now().Add(-1 * time.Second)) {
			continue
		}
		active++
		for i := 0; i < frame; i++ {
			if i < len(s.pcm) {
				mix[i] += int32(s.pcm[i])
			}
		}
		// Consume one frame worth from the front (short frames leave zeros behind).
		if len(s.pcm) > frame {
			s.pcm = s.pcm[frame:]
		} else {
			s.pcm = nil
		}
	}
	if active < 2 {
		return nil, false
	}
	pcm := make([]int16, frame)
	for i, sample := range mix {
		if sample > math.MaxInt16 {
			sample = math.MaxInt16
		}
		if sample < math.MinInt16 {
			sample = math.MinInt16
		}
		pcm[i] = int16(sample)
	}
	encoded, err := m.codec.Encode(pcm)
	if err != nil {
		return nil, false
	}
	m.seq++
	m.ts += frame
	header := make([]byte, 12)
	header[0] = 0x80
	header[1] = 0x78
	binary.BigEndian.PutUint16(header[2:4], m.seq)
	binary.BigEndian.PutUint32(header[4:8], m.ts)
	binary.BigEndian.PutUint32(header[8:12], m.ssrc)
	m.lastEmit = time.Now()
	v.mixes.Add(1)
	return append(header, encoded...), true
}

// mixLoop runs the 20 ms mixing tick and fanout.
func (v *VoiceServer) mixLoop() {
	ticker := time.NewTicker(20 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-v.done:
			return
		case <-ticker.C:
			v.mixMu.Lock()
			mixers := make(map[string]*channelMixer, len(v.mixers))
			for ch, m := range v.mixers {
				mixers[ch] = m
			}
			v.mixMu.Unlock()
			for channelID, m := range mixers {
				v.mu.Lock()
				cluster, self := v.clusterMix, v.instanceID
				v.mu.Unlock()
				if cluster && v.mixerOwner(channelID) != self {
					continue
				}
				packet, ok := v.mixOnce(channelID, m)
				if !ok {
					continue
				}
				if cluster {
					v.publishMixed(channelID, packet)
				}
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
		}
	}
}
