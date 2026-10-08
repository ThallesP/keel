// Package domain holds Keel's entities, value objects and pure rules. It imports nothing from
// this repository and does no I/O. See docs/go/ARCHITECTURE.md.
package domain

import (
	"crypto/rand"
	"encoding/base32"
	"strings"
)

var idEncoding = base32.NewEncoding("abcdefghijklmnopqrstuvwxyz234567").WithPadding(base32.NoPadding)

// NewID is a 20-character random lowercase id (100 bits). Every row id is one.
func NewID() string {
	b := make([]byte, 13)
	if _, err := rand.Read(b); err != nil {
		panic(err) // crypto/rand never fails on supported platforms
	}
	return idEncoding.EncodeToString(b)[:20]
}

// NewSecret is a URL-safe random token with n bytes of entropy (session tokens, ingest keys).
func NewSecret(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return strings.ToLower(idEncoding.EncodeToString(b))
}
