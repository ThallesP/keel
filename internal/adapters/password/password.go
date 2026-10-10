package password

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"runtime"
	"strconv"
	"strings"

	"golang.org/x/crypto/argon2"
	"golang.org/x/text/unicode/norm"
)

type Params struct {
	Memory  uint32
	Time    uint32
	Threads uint8
	SaltLen int
	KeyLen  uint32
}

var DefaultParams = Params{Memory: 19 * 1024, Time: 2, Threads: 1, SaltLen: 16, KeyLen: 32}

type Hasher struct {
	Params Params
}

func New() *Hasher { return &Hasher{Params: DefaultParams} }

func (h *Hasher) params() Params {
	if h == nil || h.Params.Memory == 0 {
		return DefaultParams
	}
	return h.Params
}

const (
	maxMemory  = 1 << 20
	maxTime    = 16
	maxThreads = 16
	maxKeyLen  = 128
)

func normalize(password string) []byte { return []byte(norm.NFKC.String(password)) }

var slots = make(chan struct{}, max(2, runtime.GOMAXPROCS(0)))

func withSlot(fn func()) {
	slots <- struct{}{}
	defer func() { <-slots }()
	fn()
}

var b64 = base64.RawStdEncoding

func (h *Hasher) Hash(password string) (string, error) {
	p := h.params()
	salt := make([]byte, p.SaltLen)
	if _, err := rand.Read(salt); err != nil {
		return "", err
	}
	var key []byte
	withSlot(func() { key = argon2.IDKey(normalize(password), salt, p.Time, p.Memory, p.Threads, p.KeyLen) })
	return fmt.Sprintf("$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s",
		argon2.Version, p.Memory, p.Time, p.Threads, b64.EncodeToString(salt), b64.EncodeToString(key)), nil
}

func (h *Hasher) Verify(hash, password string) bool {
	p, salt, key, err := parseArgon2id(hash)
	if err != nil {
		h.dummy(password)
		return false
	}
	var got []byte
	withSlot(func() { got = argon2.IDKey(normalize(password), salt, p.Time, p.Memory, p.Threads, p.KeyLen) })
	return subtle.ConstantTimeCompare(got, key) == 1
}

func (h *Hasher) dummy(password string) {
	p := h.params()
	withSlot(func() {
		_ = argon2.IDKey(normalize(password), make([]byte, p.SaltLen), p.Time, p.Memory, p.Threads, p.KeyLen)
	})
}

var errMalformed = errors.New("malformed argon2id hash")

func parseArgon2id(hash string) (Params, []byte, []byte, error) {
	parts := strings.Split(hash, "$")
	if len(parts) != 6 || parts[1] != "argon2id" || parts[2] != "v="+strconv.Itoa(argon2.Version) {
		return Params{}, nil, nil, errMalformed
	}
	var p Params
	for _, kv := range strings.Split(parts[3], ",") {
		k, v, _ := strings.Cut(kv, "=")
		n, err := strconv.ParseUint(v, 10, 32)
		if err != nil {
			return Params{}, nil, nil, errMalformed
		}
		switch k {
		case "m":
			p.Memory = uint32(n)
		case "t":
			p.Time = uint32(n)
		case "p":
			if n > maxThreads {
				return Params{}, nil, nil, errMalformed
			}
			p.Threads = uint8(n)
		default:
			return Params{}, nil, nil, errMalformed
		}
	}
	if p.Memory == 0 || p.Memory > maxMemory || p.Time == 0 || p.Time > maxTime || p.Threads == 0 {
		return Params{}, nil, nil, errMalformed
	}
	salt, err := b64.DecodeString(parts[4])
	if err != nil || len(salt) == 0 {
		return Params{}, nil, nil, errMalformed
	}
	key, err := b64.DecodeString(parts[5])
	if err != nil || len(key) == 0 || len(key) > maxKeyLen {
		return Params{}, nil, nil, errMalformed
	}
	p.SaltLen, p.KeyLen = len(salt), uint32(len(key))
	return p, salt, key, nil
}
