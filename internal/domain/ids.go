package domain

import (
	"crypto/rand"
	"encoding/base32"
)

var (
	idEncoding       = base32.NewEncoding("abcdefghijklmnopqrstuvwxyz234567").WithPadding(base32.NoPadding)
	userCodeEncoding = base32.NewEncoding("ABCDEFGHJKLMNPQRSTUVWXYZ23456789").WithPadding(base32.NoPadding)
)

func NewID() string { return NewSecret(13)[:20] }

func NewSecret(n int) string {
	b := make([]byte, n)
	rand.Read(b)
	return idEncoding.EncodeToString(b)
}

func NewUserCode() string {
	b := make([]byte, 5)
	rand.Read(b)
	return userCodeEncoding.EncodeToString(b)
}
