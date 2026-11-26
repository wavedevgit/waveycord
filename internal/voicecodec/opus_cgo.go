//go:build opus

package voicecodec

/*
#cgo pkg-config: opus
#include <opus.h>
#include <stdlib.h>

static int wc_set_bitrate(OpusEncoder *st, opus_int32 bitrate) {
    return opus_encoder_ctl(st, OPUS_SET_BITRATE(bitrate));
}
*/
import "C"

import (
	"fmt"
	"unsafe"
)

type codec struct {
	enc *C.OpusEncoder
	dec *C.OpusDecoder
}

// Available reports whether Opus is compiled in.
func Available() bool { return true }

// New creates an Opus encoder/decoder pair for 48 kHz mono 20 ms frames.
func New() (Codec, error) {
	var encErr, decErr C.int
	enc := C.opus_encoder_create(C.opus_int32(SampleRate), C.int(Channels), C.OPUS_APPLICATION_VOIP, &encErr)
	if enc == nil || encErr != C.OPUS_OK {
		return nil, fmt.Errorf("voicecodec: encoder create: %d", int(encErr))
	}
	if rc := C.wc_set_bitrate(enc, C.opus_int32(32000)); rc != C.OPUS_OK {
		C.opus_encoder_destroy(enc)
		return nil, fmt.Errorf("voicecodec: set bitrate: %d", int(rc))
	}
	dec := C.opus_decoder_create(C.opus_int32(SampleRate), C.int(Channels), &decErr)
	if dec == nil || decErr != C.OPUS_OK {
		C.opus_encoder_destroy(enc)
		return nil, fmt.Errorf("voicecodec: decoder create: %d", int(decErr))
	}
	return &codec{enc: enc, dec: dec}, nil
}

func (c *codec) Encode(pcm []int16) ([]byte, error) {
	if len(pcm) < FrameSize {
		return nil, fmt.Errorf("voicecodec: encode needs %d samples, got %d", FrameSize, len(pcm))
	}
	out := make([]byte, 4000)
	n := C.opus_encode(c.enc, (*C.opus_int16)(unsafe.Pointer(&pcm[0])), C.int(FrameSize),
		(*C.uchar)(unsafe.Pointer(&out[0])), C.opus_int32(len(out)))
	if n < 0 {
		return nil, fmt.Errorf("voicecodec: encode: %d", int(n))
	}
	return out[:n], nil
}

func (c *codec) Decode(packet []byte) ([]int16, error) {
	if len(packet) == 0 {
		return nil, fmt.Errorf("voicecodec: empty packet")
	}
	pcm := make([]int16, FrameSize)
	n := C.opus_decode(c.dec, (*C.uchar)(unsafe.Pointer(&packet[0])), C.opus_int32(len(packet)),
		(*C.opus_int16)(unsafe.Pointer(&pcm[0])), C.int(FrameSize), 0)
	if n < 0 {
		return nil, fmt.Errorf("voicecodec: decode: %d", int(n))
	}
	return pcm[:n], nil
}

// Conceal runs libopus packet-loss concealment on the decoder state.
func (c *codec) Conceal(pcm []int16) (int, error) {
	if len(pcm) < FrameSize {
		return 0, fmt.Errorf("voicecodec: conceal buffer too small")
	}
	n := C.opus_decode(c.dec, nil, 0, (*C.opus_int16)(unsafe.Pointer(&pcm[0])), C.int(FrameSize), 0)
	if n < 0 {
		return 0, fmt.Errorf("voicecodec: conceal: %d", int(n))
	}
	return int(n), nil
}

func (c *codec) Close() {
	if c.enc != nil {
		C.opus_encoder_destroy(c.enc)
		c.enc = nil
	}
	if c.dec != nil {
		C.opus_decoder_destroy(c.dec)
		c.dec = nil
	}
}
