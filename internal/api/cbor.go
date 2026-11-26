package api

import (
	"encoding/binary"
	"errors"
	"fmt"
	"math"
)

// Minimal CBOR decoder for WebAuthn objects. It supports the subset the
// attestation object needs: maps with text keys, byte strings, text strings,
// integers, arrays and simple values. Indefinite lengths are rejected.
var errCBOR = errors.New("webauthn: malformed cbor")

type cborDecoder struct {
	data []byte
	off  int
}

func cborDecode(data []byte) (any, error) {
	d := &cborDecoder{data: data}
	v, err := d.value()
	if err != nil {
		return nil, err
	}
	if d.off != len(data) {
		return nil, errCBOR
	}
	return v, nil
}

func (d *cborDecoder) read(n int) ([]byte, error) {
	if n < 0 || d.off+n > len(d.data) {
		return nil, errCBOR
	}
	b := d.data[d.off : d.off+n]
	d.off += n
	return b, nil
}

// readArg returns the argument for the current major type.
func (d *cborDecoder) readArg(info byte) (uint64, error) {
	switch {
	case info < 24:
		return uint64(info), nil
	case info == 24:
		b, err := d.read(1)
		if err != nil {
			return 0, err
		}
		return uint64(b[0]), nil
	case info == 25:
		b, err := d.read(2)
		if err != nil {
			return 0, err
		}
		return uint64(binary.BigEndian.Uint16(b)), nil
	case info == 26:
		b, err := d.read(4)
		if err != nil {
			return 0, err
		}
		return uint64(binary.BigEndian.Uint32(b)), nil
	case info == 27:
		b, err := d.read(8)
		if err != nil {
			return 0, err
		}
		return binary.BigEndian.Uint64(b), nil
	default:
		// 31 is indefinite length, which WebAuthn does not use.
		return 0, errCBOR
	}
}

func (d *cborDecoder) value() (any, error) {
	head, err := d.read(1)
	if err != nil {
		return nil, err
	}
	major := head[0] >> 5
	info := head[0] & 0x1f
	arg, err := d.readArg(info)
	if err != nil {
		return nil, err
	}
	switch major {
	case 0: // unsigned
		return arg, nil
	case 1: // negative
		return -1 - int64(arg), nil
	case 2: // byte string
		b, err := d.read(int(arg))
		if err != nil {
			return nil, err
		}
		return append([]byte(nil), b...), nil
	case 3: // text string
		b, err := d.read(int(arg))
		if err != nil {
			return nil, err
		}
		return string(b), nil
	case 4: // array
		out := make([]any, 0, int(arg))
		for i := uint64(0); i < arg; i++ {
			v, err := d.value()
			if err != nil {
				return nil, err
			}
			out = append(out, v)
		}
		return out, nil
	case 5: // map
		out := make(map[string]any, int(arg))
		for i := uint64(0); i < arg; i++ {
			k, err := d.value()
			if err != nil {
				return nil, err
			}
			v, err := d.value()
			if err != nil {
				return nil, err
			}
			ks, ok := k.(string)
			if !ok {
				return nil, fmt.Errorf("webauthn: non-string map key %T", k)
			}
			out[ks] = v
		}
		return out, nil
	case 7: // simple / float
		switch info {
		case 20:
			return false, nil
		case 21:
			return true, nil
		case 22, 23:
			return nil, nil
		case 25:
			return float64(halfToFloat(uint16(arg))), nil
		case 26:
			return float64(math.Float32frombits(uint32(arg))), nil
		case 27:
			return math.Float64frombits(arg), nil
		}
		return nil, errCBOR
	default:
		return nil, errCBOR
	}
}

// halfToFloat converts an IEEE 754 half-precision value.
func halfToFloat(h uint16) float32 {
	sign := uint32(h>>15) << 31
	exp := uint32(h>>10) & 0x1f
	frac := uint32(h) & 0x3ff
	switch exp {
	case 0:
		if frac == 0 {
			return math.Float32frombits(sign)
		}
		return math.Float32frombits(sign | (127-15)<<23 | frac<<13)
	case 31:
		return math.Float32frombits(sign | 0xff<<23 | frac<<13)
	default:
		return math.Float32frombits(sign | (exp+127-15)<<23 | frac<<13)
	}
}
