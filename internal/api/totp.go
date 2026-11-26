package api

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha1"
	"crypto/subtle"
	"encoding/base32"
	"encoding/binary"
	"fmt"
	"strings"
	"time"
)

// TOTP parameters follow RFC 6238 defaults: HMAC-SHA1, 30-second step, 6 digits.
const (
	totpStep   = 30
	totpDigits = 6
)

var b32NoPad = base32.StdEncoding.WithPadding(base32.NoPadding)

// normalizeTOTPSecret accepts lowercase, spaced or padded base32 input.
func normalizeTOTPSecret(secret string) string {
	s := strings.ToUpper(strings.ReplaceAll(strings.TrimSpace(secret), " ", ""))
	s = strings.TrimRight(s, "=")
	return s
}

// totpCode derives the code for a counter value.
func totpCode(key []byte, counter uint64) string {
	var buf [8]byte
	binary.BigEndian.PutUint64(buf[:], counter)
	mac := hmac.New(sha1.New, key)
	mac.Write(buf[:])
	sum := mac.Sum(nil)
	offset := sum[len(sum)-1] & 0x0f
	value := (uint32(sum[offset])&0x7f)<<24 |
		uint32(sum[offset+1])<<16 |
		uint32(sum[offset+2])<<8 |
		uint32(sum[offset+3])
	mod := uint32(1)
	for i := 0; i < totpDigits; i++ {
		mod *= 10
	}
	return fmt.Sprintf("%0*d", totpDigits, value%mod)
}

// verifyTOTP accepts the current step and one step either side, which covers
// clock skew without widening the window further.
func verifyTOTP(secret, code string) bool {
	key, err := b32NoPad.DecodeString(normalizeTOTPSecret(secret))
	if err != nil || len(key) == 0 {
		return false
	}
	code = strings.TrimSpace(code)
	if len(code) != totpDigits {
		return false
	}
	step := uint64(time.Now().Unix() / totpStep)
	for _, delta := range []int64{0, -1, 1} {
		counter := uint64(int64(step) + delta)
		if subtle.ConstantTimeCompare([]byte(totpCode(key, counter)), []byte(code)) == 1 {
			return true
		}
	}
	return false
}

// newTOTPSecret returns a 160-bit base32 secret (the RFC 4226 recommendation).
func newTOTPSecret() (string, error) {
	raw := make([]byte, 20)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	return b32NoPad.EncodeToString(raw), nil
}

// newBackupCodes returns fresh recovery codes, formatted as two groups of four.
func newBackupCodes(n int) ([]string, error) {
	codes := make([]string, 0, n)
	for i := 0; i < n; i++ {
		raw := make([]byte, 5)
		if _, err := rand.Read(raw); err != nil {
			return nil, err
		}
		s := b32NoPad.EncodeToString(raw)
		codes = append(codes, s[:4]+"-"+s[4:8])
	}
	return codes, nil
}
