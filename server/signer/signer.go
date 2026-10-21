// Package signer holds the server's ed25519 key and signs permissions.
// In v1 this is replaced by passkey (WebAuthn) approval.
package signer

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"

	"github.com/okemwag/warrant/engine"
)

// Signer signs permissions with one key.
type Signer struct {
	priv  ed25519.PrivateKey
	pub   ed25519.PublicKey
	keyID string
}

// FromBase64Seed builds a Signer from a base64 32-byte seed
// (WARRANT_SIGNING_KEY).
func FromBase64Seed(s string) (*Signer, error) {
	if s == "" {
		return nil, errors.New("signer: WARRANT_SIGNING_KEY is empty; run `make keygen` and put the value in .env")
	}
	seed, err := base64.StdEncoding.DecodeString(s)
	if err != nil {
		return nil, fmt.Errorf("signer: WARRANT_SIGNING_KEY is not base64: %w", err)
	}
	if len(seed) != ed25519.SeedSize {
		return nil, fmt.Errorf("signer: WARRANT_SIGNING_KEY must decode to %d bytes, got %d", ed25519.SeedSize, len(seed))
	}
	priv := ed25519.NewKeyFromSeed(seed)
	pub := priv.Public().(ed25519.PublicKey)
	sum := sha256.Sum256(pub)
	return &Signer{priv: priv, pub: pub, keyID: "ed25519:" + hex.EncodeToString(sum[:8])}, nil
}

// NewSeed returns a fresh base64 seed for WARRANT_SIGNING_KEY.
func NewSeed() string {
	seed := make([]byte, ed25519.SeedSize)
	if _, err := rand.Read(seed); err != nil {
		panic(err)
	}
	return base64.StdEncoding.EncodeToString(seed)
}

// Sign signs a permission version.
func (s *Signer) Sign(p engine.Permission) []byte { return engine.Sign(p, s.priv) }

// PublicKey is the key the engine verifies against.
func (s *Signer) PublicKey() ed25519.PublicKey { return s.pub }

// KeyID identifies the key in evidence packs without revealing it.
func (s *Signer) KeyID() string { return s.keyID }
