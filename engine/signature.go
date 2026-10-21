package engine

import "crypto/ed25519"

// SignedPermission is a permission version plus its signature and status.
type SignedPermission struct {
	Permission Permission
	Status     Status
	Signature  []byte
}

// Sign signs the permission's hash with key.
func Sign(p Permission, key ed25519.PrivateKey) []byte {
	h := p.Hash()
	return ed25519.Sign(key, h[:])
}

// Verify reports whether sig is a valid signature of p by pub.
func Verify(p Permission, sig []byte, pub ed25519.PublicKey) bool {
	if len(pub) != ed25519.PublicKeySize || len(sig) != ed25519.SignatureSize {
		return false
	}
	h := p.Hash()
	return ed25519.Verify(pub, h[:], sig)
}
