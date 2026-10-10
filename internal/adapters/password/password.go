package password

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"fmt"
	"runtime"
	"strings"

	"golang.org/x/crypto/argon2"
	"golang.org/x/text/unicode/norm"
)

type Hasher struct {
	Memory  uint32
	Time    uint32
	Threads uint8
}

func New() *Hasher { return &Hasher{Memory: 19 * 1024, Time: 2, Threads: 1} }

var slots = make(chan struct{}, max(2, runtime.GOMAXPROCS(0)))

var b64 = base64.RawStdEncoding

func (h *Hasher) Hash(password string) string {
	salt := make([]byte, 16)
	rand.Read(salt)
	return fmt.Sprintf("$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s",
		argon2.Version, h.Memory, h.Time, h.Threads, b64.EncodeToString(salt), b64.EncodeToString(h.key(password, salt, 32)))
}

func (h *Hasher) Verify(hash, password string) bool {
	made, salt, key, ok := parse(hash)
	if !ok {
		h.key(password, make([]byte, 16), 32)
		return false
	}
	return subtle.ConstantTimeCompare(made.key(password, salt, uint32(len(key))), key) == 1
}

func (h *Hasher) key(password string, salt []byte, keyLen uint32) []byte {
	slots <- struct{}{}
	defer func() { <-slots }()
	return argon2.IDKey([]byte(norm.NFKC.String(password)), salt, h.Time, h.Memory, h.Threads, keyLen)
}

func parse(hash string) (Hasher, []byte, []byte, bool) {
	var (
		version int
		made    Hasher
		rest    string
	)
	_, err := fmt.Sscanf(hash, "$argon2id$v=%d$m=%d,t=%d,p=%d$%s", &version, &made.Memory, &made.Time, &made.Threads, &rest)
	if err != nil || version != argon2.Version || made.Memory == 0 || made.Memory > 1<<20 ||
		made.Time == 0 || made.Time > 16 || made.Threads == 0 || made.Threads > 16 {
		return Hasher{}, nil, nil, false
	}
	saltB64, keyB64, _ := strings.Cut(rest, "$")
	salt, err := b64.DecodeString(saltB64)
	if err != nil || len(salt) == 0 {
		return Hasher{}, nil, nil, false
	}
	key, err := b64.DecodeString(keyB64)
	if err != nil || len(key) == 0 || len(key) > 128 {
		return Hasher{}, nil, nil, false
	}
	return made, salt, key, true
}
