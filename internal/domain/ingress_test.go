package domain

import (
	"strings"
	"testing"
)

func TestShortHash(t *testing.T) {
	cases := map[string]string{
		"":                                 "ztntfp",
		"a":                                "r9wi7g",
		"j57a8x2kq3n4m5p6r7s8t9v0w1x2y3z4": "16w41g",
		"k17cs4z0mn2mr6yr7gx8tjqe1n7rw9qj": "i8r0ew",
		"jd7f9g6h5k4m3n2p1q0r9s8t7v6w5x4y": "vrfoi4",
	}
	for in, want := range cases {
		if got := shortHash(in); got != want {
			t.Errorf("shortHash(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestDefaultDomain(t *testing.T) {
	got := DefaultDomain("j57a8x2kq3n4m5p6r7s8t9v0w1x2y3z4", "api", "203.0.113.7")
	if want := "api-16w41g.203-0-113-7.sslip.io"; got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestMovedDefaultDomain(t *testing.T) {
	const id = "j57a8x2kq3n4m5p6r7s8t9v0w1x2y3z4"
	cases := []struct {
		name, domain, ip, want string
		ok                     bool
	}{
		{"moves", "api-16w41g.203-0-113-7.sslip.io", "198.51.100.2", "api-16w41g.198-51-100-2.sslip.io", true},
		{"name with dashes keeps its label", "my-api-16w41g.203-0-113-7.sslip.io", "198.51.100.2", "my-api-16w41g.198-51-100-2.sslip.io", true},
		{"already current", "api-16w41g.203-0-113-7.sslip.io", "203.0.113.7", "", false},
		{"another node's hash", "api-i8r0ew.203-0-113-7.sslip.io", "198.51.100.2", "", false},
		{"custom domain", "app.example.com", "198.51.100.2", "", false},
		{"not sslip", "api-16w41g.203-0-113-7.nip.io", "198.51.100.2", "", false},
	}
	for _, c := range cases {
		got, ok := MovedDefaultDomain(id, c.domain, c.ip)
		if got != c.want || ok != c.ok {
			t.Errorf("%s: got %q %v, want %q %v", c.name, got, ok, c.want, c.ok)
		}
	}
}

func TestValidDomain(t *testing.T) {
	l63 := strings.Repeat("a", 63)
	max := l63 + "." + l63 + "." + l63 + "." + strings.Repeat("b", 61)
	ok := map[string]string{
		"app.example.com":       "app.example.com",
		"  App.Example.COM.  ":  "app.example.com",
		"xn--bcher-kva.example": "xn--bcher-kva.example",
		"app.localhost":         "app.localhost",
		"203.0.113.7":           "203.0.113.7",
		"a-b.c":                 "a-b.c",
		max:                     max,
	}
	for in, want := range ok {
		got, err := ValidDomain(in)
		if err != nil || got != want {
			t.Errorf("ValidDomain(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	bad := []string{
		"", "localhost", "*.example.com", "app_1.example.com", "-app.example.com", "app-.example.com",
		"app..example.com", "app.example.com..", strings.Repeat("a", 64) + ".com", "ex ample.com",
		"bücher.example", max + "b",
	}
	for _, in := range bad {
		if got, err := ValidDomain(in); err == nil || err.Error() != "Domain must look like app.example.com" || CodeOf(err) != CodeInvalidInput {
			t.Errorf("ValidDomain(%q) = %q, %v", in, got, err)
		}
	}
}

func TestAllocatePublicPort(t *testing.T) {
	cases := []struct {
		name  string
		port  int
		taken []int
		want  int
	}{
		{"free", 5432, nil, 5432},
		{"taken goes to 20000", 5432, []int{5432}, 20000},
		{"skips taken spares", 5432, []int{5432, 20000, 20001}, 20002},
		{"tcp on 443 with the http ports taken", 443, []int{80, 443}, 20000},
	}
	for _, c := range cases {
		taken := map[int]bool{}
		for _, p := range c.taken {
			taken[p] = true
		}
		got, err := AllocatePublicPort(c.port, taken)
		if err != nil || got != c.want {
			t.Errorf("%s: got %d, %v; want %d", c.name, got, err, c.want)
		}
	}
	full := map[int]bool{5432: true}
	for p := 20000; p <= 65535; p++ {
		full[p] = true
	}
	if _, err := AllocatePublicPort(5432, full); err == nil || err.Error() != "No free public port left" {
		t.Errorf("full: %v", err)
	}
}

func TestCertHint(t *testing.T) {
	cases := []struct{ in, ip, want string }{
		{
			"obtaining certificate: [api-16w41g.203-0-113-7.sslip.io] Obtain: [api-16w41g.203-0-113-7.sslip.io] solving challenge: api-16w41g.203-0-113-7.sslip.io: [api-16w41g.203-0-113-7.sslip.io] authorization failed: HTTP 400 urn:ietf:params:acme:error:connection - 203.0.113.7: Fetching http://api-16w41g.203-0-113-7.sslip.io/.well-known/acme-challenge/tok: Timeout during connect (likely firewall problem) (ca=https://acme-staging-v02.api.letsencrypt.org/directory)",
			"203.0.113.7",
			"Open ports 80 and 443 on the control plane to the internet: the certificate authority could not connect (203.0.113.7: Timeout during connect)",
		},
		{
			"HTTP 400 urn:ietf:params:acme:error:dns - DNS problem: NXDOMAIN looking up A for app.example.com - check that a DNS record exists for this domain (ca=https://acme-v02.api.letsencrypt.org/directory)",
			"203.0.113.7",
			"DNS problem: NXDOMAIN looking up A for app.example.com - check that a DNS record exists for this domain. Point the domain at 203.0.113.7 with an A record.",
		},
		{
			"HTTP 400 urn:ietf:params:acme:error:dns - DNS problem: SERVFAIL",
			"",
			"DNS problem: SERVFAIL. Point the domain at the control plane's public IP with an A record.",
		},
		{
			`HTTP 429 urn:ietf:params:acme:error:rateLimited - too many certificates (50) already issued for "sslip.io" in the last 168h0m0s`,
			"203.0.113.7",
			`Could not get a certificate: too many certificates (50) already issued for "sslip.io" in the last 168h0m0s`,
		},
		{
			"HTTP 403 urn:ietf:params:acme:error:unauthorized - 203.0.113.7: Invalid response from http://x/.well-known/acme-challenge/t: 404",
			"203.0.113.7",
			"Open ports 80 and 443 on the control plane to the internet: the certificate authority could not connect (203.0.113.7: Invalid response from http://x/.well-known/acme-challenge/t: 404)",
		},
		{"something else entirely", "203.0.113.7", "Could not get a certificate: something else entirely"},
		{"", "203.0.113.7", "Could not get a certificate"},
		{strings.Repeat("é", 250), "", "Could not get a certificate: " + strings.Repeat("é", 200)},
	}
	for _, c := range cases {
		if got := CertHint(c.in, c.ip); got != c.want {
			t.Errorf("CertHint(%.60q)\n got %q\nwant %q", c.in, got, c.want)
		}
	}
}

func TestEndpointKeyAndAddress(t *testing.T) {
	pub := 5432
	tcp := Endpoint{Protocol: ProtocolTCP, Port: 5432, PublicPort: &pub}
	web := Endpoint{Protocol: ProtocolHTTP, Port: 8080, Domain: "api-16w41g.203-0-113-7.sslip.io"}
	if tcp.Key() != "tcp:5432" || web.Key() != "http:api-16w41g.203-0-113-7.sslip.io" {
		t.Errorf("keys %q %q", tcp.Key(), web.Key())
	}
	if tcp.Address("203.0.113.7") != "203.0.113.7:5432" || tcp.Address("") != "<public IP>:5432" {
		t.Errorf("tcp address %q %q", tcp.Address("203.0.113.7"), tcp.Address(""))
	}
	if web.Address("") != "https://api-16w41g.203-0-113-7.sslip.io" {
		t.Errorf("http address %q", web.Address(""))
	}
}

func TestTruncateRunes(t *testing.T) {
	if got := TruncateRunes("héllo", 2); got != "hé" {
		t.Errorf("TruncateRunes = %q", got)
	}
	if got := TruncateRunes("hi", 5); got != "hi" {
		t.Errorf("TruncateRunes short = %q", got)
	}
}
