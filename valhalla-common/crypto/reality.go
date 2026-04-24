package crypto

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"

	"golang.org/x/crypto/curve25519"
)

// RealityKeyPair holds an X25519 keypair encoded the way xray-core's Reality
// expects — URL-safe base64 without padding. Both `xray x25519` CLI and
// Reality's config parser produce/consume this format, so it's the one we use
// end-to-end (server config, client config, control-plane DB).
type RealityKeyPair struct {
	PrivateKey string
	PublicKey  string
}

// GenerateRealityKeyPair creates a fresh X25519 keypair in Reality's format.
// The private key is clamped per Curve25519 requirements; the public key is
// derived from the clamped private key via the base point.
func GenerateRealityKeyPair() (*RealityKeyPair, error) {
	var priv [32]byte
	if _, err := rand.Read(priv[:]); err != nil {
		return nil, err
	}
	// Curve25519 clamping — same bit-twiddling as WireGuard.
	priv[0] &= 248
	priv[31] &= 127
	priv[31] |= 64

	var pub [32]byte
	curve25519.ScalarBaseMult(&pub, &priv)

	enc := base64.RawURLEncoding
	return &RealityKeyPair{
		PrivateKey: enc.EncodeToString(priv[:]),
		PublicKey:  enc.EncodeToString(pub[:]),
	}, nil
}

// GenerateRealityShortID returns a random short-id of the given byte length,
// hex-encoded. Reality accepts 0-8 bytes (empty, 2, 4, 6, 8-char hex); 8 bytes
// (16 hex chars) is the common default and gives 64 bits of entropy — enough
// that brute-forcing the ID is a non-starter even without rate limiting.
func GenerateRealityShortID(byteLen int) (string, error) {
	if byteLen < 0 || byteLen > 8 {
		byteLen = 8
	}
	buf := make([]byte, byteLen)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return hex.EncodeToString(buf), nil
}
