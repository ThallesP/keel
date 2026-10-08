package domain

import (
	"regexp"
	"strconv"
	"strings"
	"unicode"
)

// Public ingress rules (docs/go/spec/proxy-ingress.md §2.5, §3). Pure functions; the ingress use
// cases (app/ingress.go, app/proxy.go) and the startup migration call them.

const (
	// MaxEndpoints per node, counting the endpoints other than the one being replaced.
	MaxEndpoints = 10
	// FirstSparePort is where tcp/udp endpoints land when their own port is taken.
	FirstSparePort = 20000
	// ZeroSSLCA is the second ACME issuer when only KEEL_ACME_EMAIL is set.
	ZeroSSLCA = "https://acme.zerossl.com/v2/DV90"
)

// Messages of the ingress area (proxy-ingress.md §4.5), verbatim from the Convex code: the
// dashboard shows them and the CLI matches some of them.
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

// IsHTTPPort: the proxy's HTTP listeners (80, 443); tcp endpoints cannot take them.
func IsHTTPPort(p int) bool { return p == 80 || p == 443 }

// ShortHash is 6 base-36 characters of the 32-bit FNV-1a of s (byte-wise: node ids are ASCII,
// so bytes are the UTF-16 code units the TS hashed). Stable, not secret.
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

// DefaultDomain is `<name>-<hash>.<ip with dashes>.sslip.io`: sslip.io resolves it to the IP
// inside, so HTTPS works with no DNS setup. The hash of the node id keeps two projects' `api`
// apart; renaming the node keeps an existing endpoint's domain (it is stored, never recomputed).
func DefaultDomain(nodeID, name, ip string) string {
	return name + "-" + ShortHash(nodeID) + "." + strings.ReplaceAll(ip, ".", "-") + ".sslip.io"
}

var defaultDomainRE = regexp.MustCompile(`^(.+-([0-9a-z]{6}))\.(\d+-\d+-\d+-\d+)\.sslip\.io$`)

// MovedDefaultDomain: a default-pattern domain of this node on another IP → the same
// `<name>-<hash>` label on ip. ok=false for custom domains, other nodes' patterns and domains
// already on ip.
func MovedDefaultDomain(nodeID, domainName, ip string) (string, bool) {
	m := defaultDomainRE.FindStringSubmatch(domainName)
	dashed := strings.ReplaceAll(ip, ".", "-")
	if m == nil || m[2] != ShortHash(nodeID) || m[3] == dashed {
		return "", false
	}
	return m[1] + "." + dashed + ".sslip.io", true
}

var labelRE = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$`)

// ValidDomain normalizes a user-given hostname (trimmed, lower-cased, one trailing dot removed)
// or fails with "Domain must look like app.example.com". Accepts punycode, `*.localhost` and
// dotted IPs (proxy-ingress.md Q9); rejects wildcards, single labels and underscores.
func ValidDomain(raw string) (string, error) {
	d := strings.ToLower(strings.TrimFunc(raw, jsSpace))
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

// jsSpace is what JavaScript's String.prototype.trim removes.
func jsSpace(r rune) bool { return unicode.IsSpace(r) || r == 0xFEFF }

// AllocatePublicPort: port when nothing holds it on that protocol, else the first free port from
// 20000. taken includes 80 and 443 for tcp. Only Keel's own endpoints count (Q8).
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

// CertHint turns Caddy's ACME error into the user's next step. The CA's problem type says whose
// move it is: `connection`/`unauthorized`/`tls` (it could not reach 80/443), `dns` (the name does
// not resolve here), anything else verbatim (rate limits, CAA). ip is KEEL_PUBLIC_IP, "" when
// unknown.
func CertHint(err, ip string) string {
	if err == "" {
		return "Could not get a certificate"
	}
	kind, detail := "", err
	if m := acmeErrorRE.FindStringSubmatch(err); m != nil {
		kind, detail = m[1], m[2]
	}
	detail = replaceFirst(fetchingRE, detail)
	detail = replaceFirst(caSuffixRE, detail)
	detail = replaceFirst(firewallHintRE, detail)
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

func replaceFirst(re *regexp.Regexp, s string) string {
	loc := re.FindStringIndex(s)
	if loc == nil {
		return s
	}
	return s[:loc[0]] + s[loc[1]:]
}

// TruncateRunes keeps the first n runes of s (the TS sliced UTF-16 units; Q12).
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

// CollapseSpace turns every whitespace run into one space and trims (the TS errorText).
func CollapseSpace(s string) string { return strings.Join(strings.Fields(s), " ") }
