//go:build !opus

package voicecodec

// New reports that Opus is not available in this build.
func New() (Codec, error) { return nil, ErrUnavailable }

type stubCodec struct{}

func (stubCodec) Encode([]int16) ([]byte, error) { return nil, ErrUnavailable }
func (stubCodec) Decode([]byte) ([]int16, error) { return nil, ErrUnavailable }
func (stubCodec) Conceal([]int16) (int, error)   { return 0, ErrUnavailable }
func (stubCodec) Close()                         {}

// Available reports whether Opus is compiled in.
func Available() bool { return false }
