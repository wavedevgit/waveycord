// Package voicecodec is the Opus boundary for the voice media path. The real
// implementation is compiled with -tags opus and links libopus; the default build
// exposes an unavailable stub so the server builds without C dependencies.
package voicecodec

import "errors"

// ErrUnavailable means this build has no Opus support.
var ErrUnavailable = errors.New("voicecodec: opus support not compiled in (build with -tags opus)")

// Codec encodes and decodes 20 ms frames.
type Codec interface {
	Encode(pcm []int16) ([]byte, error)
	Decode(packet []byte) ([]int16, error)
	// Conceal fills pcm with libopus packet-loss concealment output.
	Conceal(pcm []int16) (int, error)
	Close()
}

const (
	SampleRate = 48000
	Channels   = 1
	FrameSize  = 960 // 20 ms at 48 kHz mono
)
