package crypto

import (
	"crypto/rand"
	"encoding/base64"

	"golang.org/x/crypto/curve25519"
)

// WireGuardKeyPair holds a WireGuard private and public key.
type WireGuardKeyPair struct {
	PrivateKey string
	PublicKey  string
}

// GenerateKeyPair generates a new WireGuard keypair.
func GenerateKeyPair() (*WireGuardKeyPair, error) {
	var privateKey [32]byte
	if _, err := rand.Read(privateKey[:]); err != nil {
		return nil, err
	}

	// Clamp the private key as per Curve25519 requirements
	privateKey[0] &= 248
	privateKey[31] &= 127
	privateKey[31] |= 64

	var publicKey [32]byte
	curve25519.ScalarBaseMult(&publicKey, &privateKey)

	return &WireGuardKeyPair{
		PrivateKey: base64.StdEncoding.EncodeToString(privateKey[:]),
		PublicKey:  base64.StdEncoding.EncodeToString(publicKey[:]),
	}, nil
}

// PublicKeyFromPrivate derives a WireGuard public key from a base64-encoded private key.
func PublicKeyFromPrivate(privKeyB64 string) (string, error) {
	privBytes, err := base64.StdEncoding.DecodeString(privKeyB64)
	if err != nil {
		return "", err
	}

	var privateKey, publicKey [32]byte
	copy(privateKey[:], privBytes)
	curve25519.ScalarBaseMult(&publicKey, &privateKey)

	return base64.StdEncoding.EncodeToString(publicKey[:]), nil
}

// GeneratePreSharedKey generates a random 256-bit pre-shared key.
func GeneratePreSharedKey() (string, error) {
	var psk [32]byte
	if _, err := rand.Read(psk[:]); err != nil {
		return "", err
	}
	return base64.StdEncoding.EncodeToString(psk[:]), nil
}
