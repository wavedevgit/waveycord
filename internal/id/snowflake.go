// Package id implements Discord-compatible snowflakes: 64-bit ids laid out as
// 42-bit millisecond timestamp | 5-bit process id | 5-bit worker id | 12-bit sequence.
package id

import (
	"errors"
	"fmt"
	"os"
	"strconv"
	"sync"
	"time"
)

const (
	epoch      int64 = 1420070400000 // 2015-01-01T00:00:00Z, Discord's epoch
	workerBits       = 5
	seqBits          = 12
	maxWorker        = 1<<workerBits - 1
	maxSeq           = 1<<seqBits - 1
)

// ErrExhausted is returned when the sequence overflows inside one millisecond.
var ErrExhausted = errors.New("id: sequence exhausted for this millisecond")

// Generator is safe for concurrent use.
type Generator struct {
	mu       sync.Mutex
	worker   int64
	lastMs   int64
	sequence int64
}

// New returns a generator whose worker bits come from WAVEYCORD_WORKER_ID when
// set, otherwise from the low bits of the process id.
func New() *Generator {
	worker := int64(os.Getpid()) & maxWorker
	if raw := os.Getenv("WAVEYCORD_WORKER_ID"); raw != "" {
		if n, err := strconv.ParseInt(raw, 10, 64); err == nil && n >= 0 && n <= maxWorker {
			worker = n
		}
	}
	return &Generator{worker: worker}
}

// Next returns the next snowflake.
func (g *Generator) Next() (string, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	now := time.Now().UnixMilli()
	if now < g.lastMs {
		// Clock stepped backwards: keep monotonicity by pinning to the last stamp.
		now = g.lastMs
	}
	if now == g.lastMs {
		g.sequence++
		if g.sequence > maxSeq {
			return "", ErrExhausted
		}
	} else {
		g.lastMs = now
		g.sequence = 0
	}
	v := (now-epoch)<<(workerBits+seqBits) | g.worker<<seqBits | g.sequence
	return strconv.FormatInt(v, 10), nil
}

// MustNext panics on exhaustion; only for bootstrapping fixed rows.
func (g *Generator) MustNext() string {
	s, err := g.Next()
	if err != nil {
		panic(err)
	}
	return s
}

// Valid reports whether s is a plausible snowflake.
func Valid(s string) bool {
	if s == "" || len(s) > 20 {
		return false
	}
	n, err := strconv.ParseInt(s, 10, 64)
	return err == nil && n > 0
}

// Timestamp returns the creation time encoded in a snowflake.
func Timestamp(s string) (time.Time, error) {
	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		return time.Time{}, fmt.Errorf("id: invalid snowflake %q", s)
	}
	return time.UnixMilli((n >> (workerBits + seqBits)) + epoch).UTC(), nil
}

// Compare returns -1, 0 or 1 ordering two snowflakes numerically.
func Compare(a, b string) int {
	x, _ := strconv.ParseInt(a, 10, 64)
	y, _ := strconv.ParseInt(b, 10, 64)
	switch {
	case x < y:
		return -1
	case x > y:
		return 1
	default:
		return 0
	}
}
