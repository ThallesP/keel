package password

import (
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func fast() *Hasher {
	return &Hasher{Params: Params{Memory: 64, Time: 1, Threads: 1, SaltLen: 16, KeyLen: 32}}
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
	if !h.Verify(hash, "correct-horse-battery") {
		t.Fatal("own hash did not match")
	}
	if h.Verify(hash, "correct-horse-batterY") {
		t.Fatal("wrong password matched")
	}
	again, _ := h.Hash("correct-horse-battery")
	if again == hash {
		t.Fatal("salt is not random")
	}
	stronger := &Hasher{Params: Params{Memory: 128, Time: 1, Threads: 1, SaltLen: 16, KeyLen: 32}}
	if !stronger.Verify(hash, "correct-horse-battery") {
		t.Fatal("a hash made with other costs did not match")
	}
}

func TestVerifyNormalizesNFKC(t *testing.T) {
	h := fast()
	hash, err := h.Hash("\ufb01anc\u00e9-\u2168-pa\u0308ssword")
	if err != nil {
		t.Fatal(err)
	}
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
		"$argon2i$v=19$m=64,t=1,p=1$c2FsdHNhbHQ$a2V5a2V5",
	} {
		if h.Verify(hash, "correct-horse-battery") {
			t.Errorf("%q matched", hash)
		}
	}
}

func TestZeroValueUsesDefaults(t *testing.T) {
	var h Hasher
	if h.params() != DefaultParams {
		t.Fatalf("zero Hasher params: %+v", h.params())
	}
}

func TestHashingWaitsForASlot(t *testing.T) {
	h := fast()
	hash, err := h.Hash("correct-horse-battery")
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < cap(slots); i++ {
		slots <- struct{}{}
	}
	done := make(chan string, 3)
	go func() { _, _ = h.Hash("x-password"); done <- "hash" }()
	go func() { h.Verify(hash, "correct-horse-battery"); done <- "verify" }()
	go func() { h.Verify("", "missing-account"); done <- "dummy" }()
	select {
	case what := <-done:
		t.Fatalf("%s ran without a free slot", what)
	case <-time.After(100 * time.Millisecond):
	}
	for i := 0; i < cap(slots); i++ {
		<-slots
	}
	for i := 0; i < 3; i++ {
		select {
		case <-done:
		case <-time.After(10 * time.Second):
			t.Fatal("hashing still blocked after the slots were freed")
		}
	}
}

func TestWithSlotBoundsConcurrency(t *testing.T) {
	var running, peak atomic.Int32
	var wg sync.WaitGroup
	for i := 0; i < 4*cap(slots); i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			withSlot(func() {
				n := running.Add(1)
				for {
					p := peak.Load()
					if n <= p || peak.CompareAndSwap(p, n) {
						break
					}
				}
				time.Sleep(5 * time.Millisecond)
				running.Add(-1)
			})
		}()
	}
	wg.Wait()
	if p := peak.Load(); p > int32(cap(slots)) || p < 1 {
		t.Fatalf("peak %d concurrent hashes, bound %d", p, cap(slots))
	}
}
