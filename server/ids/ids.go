// Package ids makes random, prefixed identifiers such as dec_7k3m...
package ids

import (
	"crypto/rand"
	"encoding/base32"
	"strings"
)

var enc = base32.StdEncoding.WithPadding(base32.NoPadding)

// New returns prefix_ followed by 16 random base32 characters (80 bits).
func New(prefix string) string {
	b := make([]byte, 10)
	if _, err := rand.Read(b); err != nil {
		panic(err) // crypto/rand does not fail on supported platforms
	}
	return prefix + "_" + strings.ToLower(enc.EncodeToString(b))
}
