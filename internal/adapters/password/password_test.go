package password

import (
	"strings"
	"testing"
	"time"
)

func fast() *Hasher { return &Hasher{Memory: 64, Time: 1, Threads: 1} }

func TestHashAndVerify(t *testing.T) {
	h := fast()
	hash := h.Hash("correct-horse-battery")
	if !strings.HasPrefix(hash, "$argon2id$v=19$m=64,t=1,p=1$") {
		t.Fatalf("hash format: %s", hash)
	}
	if !h.Verify(hash, "correct-horse-battery") {
		t.Fatal("own hash did not match")
	}
	if h.Verify(hash, "correct-horse-batterY") {
		t.Fatal("wrong password matched")
	}
	if h.Hash("correct-horse-battery") == hash {
		t.Fatal("salt is not random")
	}
	stronger := &Hasher{Memory: 128, Time: 1, Threads: 1}
	if !stronger.Verify(hash, "correct-horse-battery") {
		t.Fatal("a hash made with other costs did not match")
	}
}

func TestVerifyNormalizesNFKC(t *testing.T) {
	h := fast()
	hash := h.Hash("\ufb01anc\u00e9-\u2168-pa\u0308ssword")
	if !h.Verify(hash, "fianc\u00e9-IX-p\u00e4ssword") {
		t.Fatal("NFKC-equivalent password did not match")
	}
}

func TestVerifyRejectsMalformed(t *testing.T) {
	h := fast()
	for _, hash := range []string{
		"",
		"plain",
		"$argon2id$v=19$m=64,t=1,p=1$c2FsdA$",
		"$argon2id$v=18$m=64,t=1,p=1$c2FsdHNhbHQ$a2V5a2V5",
		"$argon2id$v=19$m=99999999,t=1,p=1$c2FsdHNhbHQ$a2V5a2V5",
		"$argon2id$v=19$m=64,t=0,p=1$c2FsdHNhbHQ$a2V5a2V5",
		"$argon2id$v=19$m=64,t=1,p=1,x=2$c2FsdHNhbHQ$a2V5a2V5",
		"$argon2id$v=19$m=64,t=1,p=257$c2FsdHNhbHQ$a2V5a2V5",
		"$argon2i$v=19$m=64,t=1,p=1$c2FsdHNhbHQ$a2V5a2V5",
	} {
		if h.Verify(hash, "correct-horse-battery") {
			t.Errorf("%q matched", hash)
		}
	}
}

func TestHashingWaitsForASlot(t *testing.T) {
	h := fast()
	hash := h.Hash("correct-horse-battery")
	for range cap(slots) {
		slots <- struct{}{}
	}
	done := make(chan string, 3)
	go func() { h.Hash("x-password"); done <- "hash" }()
	go func() { h.Verify(hash, "correct-horse-battery"); done <- "verify" }()
	go func() { h.Verify("", "missing-account"); done <- "dummy" }()
	select {
	case what := <-done:
		t.Fatalf("%s ran without a free slot", what)
	case <-time.After(100 * time.Millisecond):
	}
	for range cap(slots) {
		<-slots
	}
	for range 3 {
		select {
		case <-done:
		case <-time.After(10 * time.Second):
			t.Fatal("hashing still blocked after the slots were freed")
		}
	}
}
