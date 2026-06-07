package util

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"regexp"

	"github.com/fernjager/qvole/spake2"
)

// GeneratedCodeRe matches auto-generated codes of the form "1234-word-word-word".
var GeneratedCodeRe = regexp.MustCompile(`^\d{4}-`)

// GenerateCode creates a new random connection code in the format "0000-word-word-word".
func GenerateCode() (string, error) {
	maxes := []int{10000, len(spake2.CodeWords), len(spake2.CodeWords), len(spake2.CodeWords)}
	vals := make([]int, 4)
	for i, m := range maxes {
		v, err := randInt(m)
		if err != nil {
			return "", fmt.Errorf("generate code: %w", err)
		}
		vals[i] = v
	}
	return fmt.Sprintf("%04d-%s-%s-%s", vals[0], spake2.CodeWords[vals[1]], spake2.CodeWords[vals[2]], spake2.CodeWords[vals[3]]), nil
}

// randInt returns a uniform random int in [0, max) using rejection sampling
// to eliminate modulo bias. max must be <= 65536.
func randInt(max int) (int, error) {
	if max <= 0 || max > 65536 {
		return 0, fmt.Errorf("randInt: max out of range: %d", max)
	}
	// Largest multiple of max that fits in uint16
	limit := (65536 / max) * max
	var b [2]byte
	for {
		if _, err := rand.Read(b[:]); err != nil {
			return 0, err
		}
		v := int(binary.BigEndian.Uint16(b[:]))
		if v < limit {
			return v % max, nil
		}
	}
}

// Nameplate derives a 4-byte room identifier from a code. For auto-generated codes
// it uses the numeric prefix; otherwise it returns a truncated SHA-256 hash.
func Nameplate(code string) string {
	if GeneratedCodeRe.MatchString(code) {
		return code[:4]
	}
	h := sha256.Sum256([]byte("qvole-nameplate:" + code))
	return hex.EncodeToString(h[:4])
}
