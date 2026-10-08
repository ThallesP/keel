// Package password hashes and verifies account passwords (app.Passwords).
//
// New hashes are argon2id in the PHC string format. Accounts imported from Better Auth keep
// their scrypt hash ("<saltHex>:<keyHex>", docs/go/spec/auth-orgs.md §4.5) until the next
// successful sign-in, when Verify asks the caller to rehash.
package password

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"runtime"
	"strconv"
	"strings"

	"golang.org/x/crypto/argon2"
	"golang.org/x/crypto/scrypt"
	"golang.org/x/text/unicode/norm"
)

// Params are argon2id's costs. Memory is in KiB.
type Params struct {
	Memory  uint32
	Time    uint32
	Threads uint8
	SaltLen int
	KeyLen  uint32
}

// DefaultParams: OWASP's argon2id minimum (19 MiB, 2 passes, 1 lane). Light enough for a small
// control-plane VM; the sign-in limiter bounds how often anyone can make us pay it.
var DefaultParams = Params{Memory: 19 * 1024, Time: 2, Threads: 1, SaltLen: 16, KeyLen: 32}

// Hasher implements app.Passwords. The zero value uses DefaultParams.
type Hasher struct {
	Params Params
}

// New is a Hasher with DefaultParams.
func New() *Hasher { return &Hasher{Params: DefaultParams} }

func (h *Hasher) params() Params {
	if h == nil || h.Params.Memory == 0 {
		return DefaultParams
	}
	return h.Params
}

// Better Auth's scrypt parameters (@better-auth/utils 0.4.1).
const (
	scryptN      = 16384
	scryptR      = 16
	scryptP      = 1
	scryptKeyLen = 64
)

// Limits on a stored argon2id hash's costs, so a corrupt row cannot make a sign-in allocate
// gigabytes.
const (
	maxMemory  = 1 << 20 // 1 GiB in KiB
	maxTime    = 16
	maxThreads = 16
	maxKeyLen  = 128
)

// normalize: Better Auth hashes password.normalize("NFKC"); new hashes do the same so a password
// typed with a different Unicode composition still matches.
func normalize(password string) []byte { return []byte(norm.NFKC.String(password)) }

// slots bounds how many hashes run at once. Each argon2id hash holds Params.Memory (19 MiB by
// default) and each Better Auth scrypt check 32 MiB while it runs. Sign-in is unauthenticated and
// its limiter counts per client and email, so without a bound a burst of sign-ins with made-up
// emails would make keel serve allocate gigabytes. Callers past the bound wait their turn: hashing
// is CPU-bound, so running more at once would not finish any sooner.
var slots = make(chan struct{}, max(2, runtime.GOMAXPROCS(0)))

// withSlot runs fn once a hashing slot is free.
func withSlot(fn func()) {
	slots <- struct{}{}
	defer func() { <-slots }()
	fn()
}

var b64 = base64.RawStdEncoding

// Hash is a fresh argon2id hash: $argon2id$v=19$m=<KiB>,t=<passes>,p=<lanes>$<salt>$<key>.
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

// Verify reports whether password matches hash and whether the hash should be replaced with a
// fresh Hash (a Better Auth scrypt hash, or argon2id with other costs). An empty or malformed hash
// never matches, but costs about as much as a real check so a missing account is not revealed by
// timing.
func (h *Hasher) Verify(hash, password string) (ok, rehash bool) {
	switch {
	case strings.HasPrefix(hash, "$argon2id$"):
		p, salt, key, err := parseArgon2id(hash)
		if err != nil {
			h.dummy(password)
			return false, false
		}
		var got []byte
		withSlot(func() { got = argon2.IDKey(normalize(password), salt, p.Time, p.Memory, p.Threads, p.KeyLen) })
		if subtle.ConstantTimeCompare(got, key) != 1 {
			return false, false
		}
		cur := h.params()
		return true, p.Memory != cur.Memory || p.Time != cur.Time || p.Threads != cur.Threads ||
			p.KeyLen != cur.KeyLen || len(salt) != cur.SaltLen
	case strings.Contains(hash, ":"):
		ok := verifyScrypt(hash, password)
		return ok, ok // a match is always rehashed to argon2id
	}
	h.dummy(password)
	return false, false
}

// verifyScrypt checks a Better Auth hash. The salt is the 32 hex characters themselves, as
// ASCII bytes (not hex-decoded); the key is compared as lowercase hex.
func verifyScrypt(hash, password string) bool {
	saltHex, keyHex, _ := strings.Cut(hash, ":")
	if saltHex == "" || keyHex == "" {
		return false
	}
	want, err := hex.DecodeString(keyHex)
	if err != nil || len(want) != scryptKeyLen {
		return false
	}
	var got []byte
	withSlot(func() {
		got, err = scrypt.Key(normalize(password), []byte(saltHex), scryptN, scryptR, scryptP, scryptKeyLen)
	})
	if err != nil {
		return false
	}
	return subtle.ConstantTimeCompare(got, want) == 1
}

// dummy spends one hash's worth of time.
func (h *Hasher) dummy(password string) {
	p := h.params()
	withSlot(func() {
		_ = argon2.IDKey(normalize(password), make([]byte, p.SaltLen), p.Time, p.Memory, p.Threads, p.KeyLen)
	})
}

var errMalformed = errors.New("malformed argon2id hash")

func parseArgon2id(hash string) (Params, []byte, []byte, error) {
	// "", "argon2id", "v=19", "m=…,t=…,p=…", salt, key
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
