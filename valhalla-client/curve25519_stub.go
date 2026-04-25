package main

import "golang.org/x/crypto/curve25519"

func init() {
	curve25519ScalarBaseMult = curve25519.ScalarBaseMult
}
