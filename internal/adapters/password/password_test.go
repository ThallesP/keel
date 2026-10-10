package password

import (
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

var betterAuthVectors = []struct{ password, hash string }{
	{"correct-horse-battery", "0123456789abcdef0123456789abcdef:68b228eae069737062c56fbe239acdc9517ba0c0ed53447e2697faa281de7aab26e6e87571f29175a258c42d8a9fde39d6cd344c160472b76897c7bcfe0a1f53"},
	{"\ufb01anc\u00e9-\u2168-pa\u0308ssword", "a1b2c3d4e5f60718293a4b5c6d7e8f90:d6f43c886f0278532fef79e8f38c9e4cd4e10fdb9dbc7af09e569af41ff565cb4b3e843772829ca1e91aac25d44df66e9d5cec863363cd6c2d89a82fbaeb077c"},
}

func fast() *Hasher {
	return &Hasher{Params: Params{Memory: 64, Time: 1, Threads: 1, SaltLen: 16, KeyLen: 32}}
}

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
	if ok, _ := h.Verify(betterAuthVectors[1].hash, "fianc\u00e9-IX-p\u00e4ssword"); !ok {
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
		"0123456789abcdef0123456789abcdef:68b2",
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

func TestHashingWaitsForASlot(t *testing.T) {
	h := fast()
	hash, err := h.Hash("correct-horse-battery")
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < cap(slots); i++ {
		slots <- struct{}{}
	}
	done := make(chan string, 4)
	go func() { _, _ = h.Hash("x-password"); done <- "hash" }()
	go func() { h.Verify(hash, "correct-horse-battery"); done <- "argon2id" }()
	go func() { h.Verify(betterAuthVectors[0].hash, betterAuthVectors[0].password); done <- "scrypt" }()
	go func() { h.Verify("", "missing-account"); done <- "dummy" }()
	select {
	case what := <-done:
		t.Fatalf("%s ran without a free slot", what)
	case <-time.After(100 * time.Millisecond):
	}
	for i := 0; i < cap(slots); i++ {
		<-slots
	}
	for i := 0; i < 4; i++ {
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
