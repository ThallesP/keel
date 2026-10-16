package registry

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"slices"
	"time"

	"golang.org/x/crypto/bcrypt"
)

const (
	Username = "keel"
	Port     = "5000"

	KeepTagsPerRepository = 10
	CertificateLifetime   = 5 * 365 * 24 * time.Hour
	RenewBefore           = 365 * 24 * time.Hour
)

type Files struct {
	Dir      string
	Host     string
	Password string
	Now      time.Time
}

func Prepare(f Files) (caPEM []byte, err error) {
	if f.Password == "" {
		return nil, errors.New("registry: KEEL_REGISTRY_PASSWORD is empty")
	}
	host, _, err := net.SplitHostPort(f.Host)
	if err != nil {
		return nil, fmt.Errorf("registry: %q is not host:port: %w", f.Host, err)
	}
	if err := os.MkdirAll(f.Dir, 0o700); err != nil {
		return nil, err
	}
	ca, caKey, err := f.authority()
	if err != nil {
		return nil, err
	}
	if err := f.certificate(ca, caKey, host); err != nil {
		return nil, err
	}
	if err := f.htpasswd(); err != nil {
		return nil, err
	}
	if err := writeIfChanged(f.path("config.json"), zotConfig(), 0o644); err != nil {
		return nil, err
	}
	return pemBlock("CERTIFICATE", ca.Raw), nil
}

func (f Files) path(name string) string { return filepath.Join(f.Dir, name) }

func (f Files) authority() (*x509.Certificate, *ecdsa.PrivateKey, error) {
	cert, key, err := load(f.path("ca.crt"), f.path("ca.key"))
	if err == nil {
		return cert, key, nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return nil, nil, err
	}
	key, err = ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, nil, err
	}
	tmpl := &x509.Certificate{
		SerialNumber:          serial(),
		Subject:               pkix.Name{CommonName: "Keel registry CA"},
		NotBefore:             f.Now.Add(-time.Hour),
		NotAfter:              f.Now.Add(20 * 365 * 24 * time.Hour),
		IsCA:                  true,
		BasicConstraintsValid: true,
		MaxPathLenZero:        true,
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		return nil, nil, err
	}
	if err := writeKeyPair(f.path("ca.crt"), f.path("ca.key"), der, key); err != nil {
		return nil, nil, err
	}
	cert, err = x509.ParseCertificate(der)
	return cert, key, err
}

func (f Files) certificate(ca *x509.Certificate, caKey *ecdsa.PrivateKey, host string) error {
	cert, _, err := load(f.path("tls.crt"), f.path("tls.key"))
	if err == nil && f.stillGood(cert, ca, host) {
		return nil
	}
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return err
	}
	tmpl := &x509.Certificate{
		SerialNumber: serial(),
		Subject:      pkix.Name{CommonName: host},
		NotBefore:    f.Now.Add(-time.Hour),
		NotAfter:     f.Now.Add(CertificateLifetime),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	if ip := net.ParseIP(host); ip != nil {
		tmpl.IPAddresses = []net.IP{ip}
	} else {
		tmpl.DNSNames = []string{host}
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, ca, &key.PublicKey, caKey)
	if err != nil {
		return err
	}
	return writeKeyPair(f.path("tls.crt"), f.path("tls.key"), der, key)
}

func (f Files) stillGood(cert, ca *x509.Certificate, host string) bool {
	if cert.CheckSignatureFrom(ca) != nil || cert.VerifyHostname(host) != nil {
		return false
	}
	return cert.NotAfter.Sub(f.Now) > RenewBefore
}

func (f Files) htpasswd() error {
	path := f.path("htpasswd")
	current, err := os.ReadFile(path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if hash, ok := bytes.CutPrefix(bytes.TrimSpace(current), []byte(Username+":")); ok &&
		bcrypt.CompareHashAndPassword(hash, []byte(f.Password)) == nil {
		return nil
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(f.Password), bcrypt.MinCost)
	if err != nil {
		return err
	}
	return writeFile(path, fmt.Appendf(nil, "%s:%s\n", Username, hash), 0o600)
}

func zotConfig() []byte {
	type tags struct {
		MostRecentlyPushedCount int `json:"mostRecentlyPushedCount"`
	}
	type policy struct {
		Repositories    []string `json:"repositories"`
		DeleteReferrers bool     `json:"deleteReferrers"`
		DeleteUntagged  bool     `json:"deleteUntagged"`
		KeepTags        []tags   `json:"keepTags"`
	}
	cfg := map[string]any{
		"distSpecVersion": "1.1.1",
		"storage": map[string]any{
			"rootDirectory": "/var/lib/registry",
			"gc":            true,
			"gcDelay":       "1h",
			"gcInterval":    "24h",
			"retention": map[string]any{
				"delay": "24h",
				"policies": []policy{{
					Repositories: []string{"**"}, DeleteReferrers: true, DeleteUntagged: true,
					KeepTags: []tags{{MostRecentlyPushedCount: KeepTagsPerRepository}},
				}},
			},
		},
		"http": map[string]any{
			"address": "0.0.0.0",
			"port":    Port,
			"compat":  []string{"docker2s2"},
			"tls":     map[string]string{"cert": "/etc/zot/tls.crt", "key": "/etc/zot/tls.key"},
			"auth":    map[string]any{"htpasswd": map[string]string{"path": "/etc/zot/htpasswd"}, "failDelay": 1},
		},
		"log": map[string]string{"level": "info"},
	}
	b, _ := json.MarshalIndent(cfg, "", "  ")
	return append(b, '\n')
}

func load(certPath, keyPath string) (*x509.Certificate, *ecdsa.PrivateKey, error) {
	certPEM, err := os.ReadFile(certPath)
	if err != nil {
		return nil, nil, err
	}
	keyPEM, err := os.ReadFile(keyPath)
	if err != nil {
		return nil, nil, err
	}
	certBlock, _ := pem.Decode(certPEM)
	keyBlock, _ := pem.Decode(keyPEM)
	if certBlock == nil || keyBlock == nil {
		return nil, nil, fmt.Errorf("registry: %s or %s is not PEM", certPath, keyPath)
	}
	cert, err := x509.ParseCertificate(certBlock.Bytes)
	if err != nil {
		return nil, nil, err
	}
	key, err := x509.ParseECPrivateKey(keyBlock.Bytes)
	if err != nil {
		return nil, nil, err
	}
	return cert, key, nil
}

func writeKeyPair(certPath, keyPath string, der []byte, key *ecdsa.PrivateKey) error {
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return err
	}
	if err := writeFile(keyPath, pemBlock("EC PRIVATE KEY", keyDER), 0o600); err != nil {
		return err
	}
	return writeFile(certPath, pemBlock("CERTIFICATE", der), 0o644)
}

func writeIfChanged(path string, data []byte, mode os.FileMode) error {
	current, err := os.ReadFile(path)
	if err == nil && slices.Equal(current, data) {
		return nil
	}
	return writeFile(path, data, mode)
}

func writeFile(path string, data []byte, mode os.FileMode) error {
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, mode); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func pemBlock(kind string, der []byte) []byte {
	return pem.EncodeToMemory(&pem.Block{Type: kind, Bytes: der})
}

func serial() *big.Int {
	n, _ := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 127))
	return n
}
