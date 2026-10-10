package domain

import (
	"regexp"
	"strconv"
	"strings"
	"unicode"
)

const (
	MaxEndpoints   = 10
	FirstSparePort = 20000
	ZeroSSLCA      = "https://acme.zerossl.com/v2/DV90"
)

const (
	MsgOnlyExposable    = "Only services, databases and caches can be exposed"
	MsgShipRedisFirst   = "Ship this Redis first: its password takes effect on the next Ship"
	MsgPortRange        = "Port must be 1–65535"
	MsgSetPortFirst     = "Set the service's port first"
	MsgNoPublicIP       = "Keel does not know this server's public IP yet: re-run install.sh, or set KEEL_PUBLIC_IP"
	MsgHTTPPorts        = "HTTP is always served on 80 and 443"
	MsgBadDomain        = "Domain must look like app.example.com"
	MsgOnlyHTTPDomain   = "Only HTTP endpoints have a domain"
	MsgNoFreePublicPort = "No free public port left"
	MsgTCPOnHTTPPort    = "80 and 443 serve HTTP; pick another public port"
	MsgTooManyEndpoints = "At most 10 endpoints per node"
	MsgNameTheEndpoint  = "Name the endpoint: protocol and domain (http) or public port"
)

func IsHTTPPort(p int) bool { return p == 80 || p == 443 }

func ShortHash(s string) string {
	h := uint32(0x811c9dc5)
	for i := 0; i < len(s); i++ {
		h ^= uint32(s[i])
		h *= 0x01000193
	}
	b := strconv.FormatUint(uint64(h), 36)
	for len(b) < 6 {
		b = "0" + b
	}
	return b[len(b)-6:]
}

func DefaultDomain(nodeID, name, ip string) string {
	return name + "-" + ShortHash(nodeID) + "." + strings.ReplaceAll(ip, ".", "-") + ".sslip.io"
}

var defaultDomainRE = regexp.MustCompile(`^(.+-([0-9a-z]{6}))\.(\d+-\d+-\d+-\d+)\.sslip\.io$`)

func MovedDefaultDomain(nodeID, domainName, ip string) (string, bool) {
	m := defaultDomainRE.FindStringSubmatch(domainName)
	dashed := strings.ReplaceAll(ip, ".", "-")
	if m == nil || m[2] != ShortHash(nodeID) || m[3] == dashed {
		return "", false
	}
	return m[1] + "." + dashed + ".sslip.io", true
}

var labelRE = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$`)

func ValidDomain(raw string) (string, error) {
	d := strings.ToLower(strings.TrimFunc(raw, isJSTrimSpace))
	d = strings.TrimSuffix(d, ".")
	labels := strings.Split(d, ".")
	ok := len(d) <= 253 && len(labels) >= 2
	for _, l := range labels {
		ok = ok && labelRE.MatchString(l)
	}
	if !ok {
		return "", Invalid(MsgBadDomain)
	}
	return d, nil
}

func isJSTrimSpace(r rune) bool { return unicode.IsSpace(r) || r == 0xFEFF }

func AllocatePublicPort(port int, taken map[int]bool) (int, error) {
	if !taken[port] {
		return port, nil
	}
	for p := FirstSparePort; p <= 65535; p++ {
		if !taken[p] {
			return p, nil
		}
	}
	return 0, Invalid(MsgNoFreePublicPort)
}

var (
	acmeErrorRE    = regexp.MustCompile(`urn:ietf:params:acme:error:(\w+) - (.+)$`)
	fetchingRE     = regexp.MustCompile(`Fetching \S+: `)
	caSuffixRE     = regexp.MustCompile(`\s*\(ca=[^)]*\)\s*$`)
	firewallHintRE = regexp.MustCompile(`\s*\(likely firewall problem\)`)
)

func CertHint(err, ip string) string {
	if err == "" {
		return "Could not get a certificate"
	}
	kind, detail := "", err
	if m := acmeErrorRE.FindStringSubmatch(err); m != nil {
		kind, detail = m[1], m[2]
	}
	detail = removeFirstMatch(fetchingRE, detail)
	detail = removeFirstMatch(caSuffixRE, detail)
	detail = removeFirstMatch(firewallHintRE, detail)
	detail = TruncateRunes(detail, 200)
	switch kind {
	case "connection", "unauthorized", "tls":
		return "Open ports 80 and 443 on the control plane to the internet: the certificate authority could not connect (" + detail + ")"
	case "dns":
		if ip == "" {
			ip = "the control plane's public IP"
		}
		return detail + ". Point the domain at " + ip + " with an A record."
	}
	return "Could not get a certificate: " + detail
}

func removeFirstMatch(re *regexp.Regexp, s string) string {
	loc := re.FindStringIndex(s)
	if loc == nil {
		return s
	}
	return s[:loc[0]] + s[loc[1]:]
}

func TruncateRunes(s string, n int) string {
	i := 0
	for j := range s {
		if i == n {
			return s[:j]
		}
		i++
	}
	return s
}

func CollapseSpace(s string) string { return strings.Join(strings.Fields(s), " ") }
