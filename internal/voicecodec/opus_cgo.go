//go:build opus

package voicecodec

import (
	"fmt"

	"github.com/hraban/opus"
)

type codec struct {
	enc *opus.Encoder
	dec *opus.Decoder
}

// Available reports whether Opus is compiled in.
func Available() bool { return true }

// New creates an Opus encoder/decoder pair for 48 kHz mono 20 ms frames.
func New() (Codec, error) {
	enc, err := opus.NewEncoder(SampleRate, Channels, opus.AppVoIP)
	if err != nil {
		return nil, fmt.Errorf("voicecodec: encoder: %w", err)
	}
	if err := enc.SetBitrate(32000); err != nil {
		return nil, fmt.Errorf("voicecodec: bitrate: %w", err)
	}
	dec, err := opus.NewDecoder(SampleRate, Channels)
	if err != nil {
		return nil, fmt.Errorf("voicecodec: decoder: %w", err)
	}
	return &codec{enc: enc, dec: dec}, nil
}

func (c *codec) Encode(pcm []int16) ([]byte, error) {
	out := make([]byte, 4000)
	n, err := c.enc.Encode(pcm, out)
	if err != nil {
		return nil, err
	}
	return out[:n], nil
}

func (c *codec) Decode(packet []byte) ([]int16, error) {
	pcm := make([]int16, FrameSize)
	n, err := c.dec.Decode(packet, pcm)
	if err != nil {
		return nil, err
	}
	return pcm[:n], nil
}

func (c *codec) Close() {}
