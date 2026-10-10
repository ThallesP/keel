package domain

import (
	"cmp"
	"fmt"
	"hash/fnv"
	"regexp"
	"strconv"
	"strings"
)

const MsgPortRange = "Port must be 1–65535"

func IsHTTPPort(p int) bool { return p == 80 || p == 443 }

func shortHash(s string) string {
	h := fnv.New32a()
	h.Write([]byte(s))
	b := fmt.Sprintf("%06s", strconv.FormatUint(uint64(h.Sum32()), 36))
	return b[len(b)-6:]
}

func DefaultDomain(nodeID, name, ip string) string {
	return name + "-" + shortHash(nodeID) + "." + strings.ReplaceAll(ip, ".", "-") + ".sslip.io"
}

var defaultDomainRE = regexp.MustCompile(`^(.+-([0-9a-z]{6}))\.(\d+-\d+-\d+-\d+)\.sslip\.io$`)

func MovedDefaultDomain(nodeID, domainName, ip string) (string, bool) {
	m := defaultDomainRE.FindStringSubmatch(domainName)
	dashed := strings.ReplaceAll(ip, ".", "-")
	if m == nil || m[2] != shortHash(nodeID) || m[3] == dashed {
		return "", false
	}
	return m[1] + "." + dashed + ".sslip.io", true
}

var domainRE = regexp.MustCompile(`^([a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?\.)+[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$`)

func ValidDomain(raw string) (string, error) {
	d := strings.TrimSuffix(strings.ToLower(strings.TrimSpace(raw)), ".")
	if len(d) > 253 || !domainRE.MatchString(d) {
		return "", Invalid("Domain must look like app.example.com")
	}
	return d, nil
}

func AllocatePublicPort(port int, taken map[int]bool) (int, error) {
	if !taken[port] {
		return port, nil
	}
	for p := 20000; p <= 65535; p++ {
		if !taken[p] {
			return p, nil
		}
	}
	return 0, Invalid("No free public port left")
}

var (
	acmeErrorRE = regexp.MustCompile(`urn:ietf:params:acme:error:(\w+) - (.+)$`)
	certNoiseRE = regexp.MustCompile(`Fetching \S+: |\s*\(likely firewall problem\)|\s*\(ca=[^)]*\)\s*$`)
)

func CertHint(err, ip string) string {
	if err == "" {
		return "Could not get a certificate"
	}
	kind, detail := "", err
	if m := acmeErrorRE.FindStringSubmatch(err); m != nil {
		kind, detail = m[1], m[2]
	}
	detail = TruncateRunes(certNoiseRE.ReplaceAllString(detail, ""), 200)
	switch kind {
	case "connection", "unauthorized", "tls":
		return "Open ports 80 and 443 on the control plane to the internet: the certificate authority could not connect (" + detail + ")"
	case "dns":
		return detail + ". Point the domain at " + cmp.Or(ip, "the control plane's public IP") + " with an A record."
	}
	return "Could not get a certificate: " + detail
}

func TruncateRunes(s string, n int) string {
	for i := range s {
		if n == 0 {
			return s[:i]
		}
		n--
	}
	return s
}
