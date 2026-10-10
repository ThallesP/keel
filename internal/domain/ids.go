package domain

import (
	"crypto/rand"
	"encoding/base32"
)

var idEncoding = base32.NewEncoding("abcdefghijklmnopqrstuvwxyz234567").WithPadding(base32.NoPadding)

func NewID() string {
	b := make([]byte, 13)
	rand.Read(b)
	return idEncoding.EncodeToString(b)[:20]
}

func NewSecret(n int) string {
	b := make([]byte, n)
	rand.Read(b)
	return idEncoding.EncodeToString(b)
}
