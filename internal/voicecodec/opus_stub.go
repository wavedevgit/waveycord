//go:build !opus

package voicecodec

// New reports that Opus is not available in this build.
func New() (Codec, error) { return nil, ErrUnavailable }

// Available reports whether Opus is compiled in.
func Available() bool { return false }
