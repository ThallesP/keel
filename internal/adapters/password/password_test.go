package password

import (
	"strings"
	"testing"
)

// Vectors made with node:crypto scrypt exactly as @better-auth/utils 0.4.1 hashes
// (NFKC, salt = the hex string's ASCII bytes, N=16384 r=16 p=1 dkLen=64).
var betterAuthVectors = []struct{ password, hash string }{
	{"correct-horse-battery", "0123456789abcdef0123456789abcdef:68b228eae069737062c56fbe239acdc9517ba0c0ed53447e2697faa281de7aab26e6e87571f29175a258c42d8a9fde39d6cd344c160472b76897c7bcfe0a1f53"},
	// "ﬁancé-Ⅸ-pässword" with a ligature, a roman numeral and a combining diaeresis: NFKC matters.
	{"ﬁancé-Ⅸ-pässword", "a1b2c3d4e5f60718293a4b5c6d7e8f90:d6f43c886f0278532fef79e8f38c9e4cd4e10fdb9dbc7af09e569af41ff565cb4b3e843772829ca1e91aac25d44df66e9d5cec863363cd6c2d89a82fbaeb077c"},
}

// Cheap parameters keep the tests fast; production uses DefaultParams.
func fast() *Hasher { return &Hasher{Params: Params{Memory: 64, Time: 1, Threads: 1, SaltLen: 16, KeyLen: 32}} }

func TestVerifyBetterAuthScrypt(t *testing.T) {
	h := fast()
	for _, v := range betterAuthVectors {
		ok, rehash := h.Verify(v.hash, v.password)
		if !ok || !rehash {
			t.Errorf("%q: ok=%v rehash=%v, want true true", v.password, ok, rehash)
		}
		if ok, _ := h.Verify(v.hash, v.password+"x"); ok {
			t.Errorf("%q: wrong password matched", v.password)
		}
	}
	// The NFKC form of the second password is the same password.
	if ok, _ := h.Verify(betterAuthVectors[1].hash, "fiancé-IX-pässword"); !ok {
		t.Error("NFKC-equivalent password did not match")
	}
}

func TestHashAndVerify(t *testing.T) {
	h := fast()
	hash, err := h.Hash("correct-horse-battery")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(hash, "$argon2id$v=19$m=64,t=1,p=1$") {
		t.Fatalf("hash format: %s", hash)
	}
	if ok, rehash := h.Verify(hash, "correct-horse-battery"); !ok || rehash {
		t.Fatalf("own hash: ok=%v rehash=%v", ok, rehash)
	}
	if ok, _ := h.Verify(hash, "correct-horse-batterY"); ok {
		t.Fatal("wrong password matched")
	}
	again, _ := h.Hash("correct-horse-battery")
	if again == hash {
		t.Fatal("salt is not random")
	}
	// Stronger parameters now: the old hash still verifies and asks for a rehash.
	stronger := &Hasher{Params: Params{Memory: 128, Time: 1, Threads: 1, SaltLen: 16, KeyLen: 32}}
	if ok, rehash := stronger.Verify(hash, "correct-horse-battery"); !ok || !rehash {
		t.Fatalf("param change: ok=%v rehash=%v", ok, rehash)
	}
}

func TestVerifyRejectsMalformed(t *testing.T) {
	h := fast()
	for _, hash := range []string{
		"",
		"plain",
		":",
		"abc:",
		":abcd",
		"0123456789abcdef0123456789abcdef:zz",
		"0123456789abcdef0123456789abcdef:68b2", // short key
		"$argon2id$v=19$m=64,t=1,p=1$c2FsdA$",
		"$argon2id$v=18$m=64,t=1,p=1$c2FsdHNhbHQ$a2V5a2V5",
		"$argon2id$v=19$m=99999999,t=1,p=1$c2FsdHNhbHQ$a2V5a2V5",
		"$argon2id$v=19$m=64,t=0,p=1$c2FsdHNhbHQ$a2V5a2V5",
		"$argon2id$v=19$m=64,t=1,p=1,x=2$c2FsdHNhbHQ$a2V5a2V5",
		"$argon2i$v=19$m=64,t=1,p=1$c2FsdHNhbHQ$a2V5a2V5",
	} {
		if ok, rehash := h.Verify(hash, "correct-horse-battery"); ok || rehash {
			t.Errorf("%q: ok=%v rehash=%v", hash, ok, rehash)
		}
	}
}

func TestZeroValueUsesDefaults(t *testing.T) {
	var h Hasher
	if h.params() != DefaultParams {
		t.Fatalf("zero Hasher params: %+v", h.params())
	}
}
