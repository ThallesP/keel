package registry

import (
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/bcrypt"
)

func files(t *testing.T, dir, host string, now time.Time) []byte {
	t.Helper()
	ca, err := Prepare(Files{Dir: dir, Host: host, Password: "s3cret", Now: now})
	if err != nil {
		t.Fatal(err)
	}
	return ca
}

func parse(t *testing.T, path string) *x509.Certificate {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	block, _ := pem.Decode(b)
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		t.Fatal(err)
	}
	return cert
}

func TestPrepareWritesEverythingZotNeeds(t *testing.T) {
	dir := t.TempDir()
	now := time.Now()
	caPEM := files(t, dir, "100.64.0.7:5000", now)

	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(caPEM) {
		t.Fatal("Prepare returned no CA certificate")
	}
	leaf := parse(t, filepath.Join(dir, "tls.crt"))
	if _, err := leaf.Verify(x509.VerifyOptions{Roots: pool, DNSName: "100.64.0.7", CurrentTime: now}); err != nil {
		t.Fatalf("leaf does not verify for the registry IP: %v", err)
	}

	htpasswd, err := os.ReadFile(filepath.Join(dir, "htpasswd"))
	if err != nil {
		t.Fatal(err)
	}
	hash, ok := strings.CutPrefix(strings.TrimSpace(string(htpasswd)), Username+":")
	if !ok || bcrypt.CompareHashAndPassword([]byte(hash), []byte("s3cret")) != nil {
		t.Fatalf("htpasswd does not hold the password for %s: %q", Username, htpasswd)
	}

	var cfg struct {
		HTTP struct {
			Port   string   `json:"port"`
			Compat []string `json:"compat"`
			TLS    struct{ Cert, Key string }
			Auth   struct {
				Htpasswd struct{ Path string } `json:"htpasswd"`
			}
		} `json:"http"`
		Storage struct {
			Retention struct {
				Policies []struct {
					KeepTags []struct {
						MostRecentlyPushedCount int `json:"mostRecentlyPushedCount"`
					} `json:"keepTags"`
				} `json:"policies"`
			} `json:"retention"`
		} `json:"storage"`
	}
	b, err := os.ReadFile(filepath.Join(dir, "config.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(b, &cfg); err != nil {
		t.Fatal(err)
	}
	if cfg.HTTP.Port != Port || cfg.HTTP.TLS.Cert != "/etc/zot/tls.crt" || cfg.HTTP.Auth.Htpasswd.Path != "/etc/zot/htpasswd" {
		t.Fatalf("config.json does not point zot at the files: %+v", cfg.HTTP)
	}
	if len(cfg.HTTP.Compat) != 1 || cfg.HTTP.Compat[0] != "docker2s2" {
		t.Fatalf("zot would refuse Docker's own manifests (docker push from the classic image store): compat %v", cfg.HTTP.Compat)
	}
	if got := cfg.Storage.Retention.Policies[0].KeepTags[0].MostRecentlyPushedCount; got != KeepTagsPerRepository {
		t.Fatalf("retention keeps %d tags, want %d", got, KeepTagsPerRepository)
	}

	for name, want := range map[string]os.FileMode{"ca.key": 0o600, "tls.key": 0o600, "htpasswd": 0o600} {
		info, err := os.Stat(filepath.Join(dir, name))
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != want {
			t.Errorf("%s mode %v, want %v", name, info.Mode().Perm(), want)
		}
	}
}

func TestPrepareIsStableAcrossRestarts(t *testing.T) {
	dir := t.TempDir()
	now := time.Now()
	first := files(t, dir, "100.64.0.7:5000", now)
	leaf := parse(t, filepath.Join(dir, "tls.crt"))
	htpasswd, _ := os.ReadFile(filepath.Join(dir, "htpasswd"))

	second := files(t, dir, "100.64.0.7:5000", now.Add(time.Hour))

	if string(first) != string(second) {
		t.Fatal("the CA changed on a restart; every node would stop trusting the registry")
	}
	if !parse(t, filepath.Join(dir, "tls.crt")).Equal(leaf) {
		t.Fatal("the certificate was reissued although it was still good")
	}
	if again, _ := os.ReadFile(filepath.Join(dir, "htpasswd")); string(again) != string(htpasswd) {
		t.Fatal("htpasswd was rewritten for an unchanged password")
	}
}

func TestPrepareReissuesTheCertificate(t *testing.T) {
	now := time.Now()
	cases := map[string]struct {
		host string
		at   time.Time
	}{
		"address changed": {"100.64.0.9:5000", now},
		"close to expiry": {"100.64.0.7:5000", now.Add(CertificateLifetime - RenewBefore + time.Hour)},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			ca := files(t, dir, "100.64.0.7:5000", now)
			leaf := parse(t, filepath.Join(dir, "tls.crt"))

			if again := files(t, dir, c.host, c.at); string(again) != string(ca) {
				t.Fatal("the CA changed")
			}
			if parse(t, filepath.Join(dir, "tls.crt")).Equal(leaf) {
				t.Fatal("the certificate was not reissued")
			}
		})
	}
}

func TestPrepareRehashesAChangedPassword(t *testing.T) {
	dir := t.TempDir()
	files(t, dir, "100.64.0.7:5000", time.Now())
	if _, err := Prepare(Files{Dir: dir, Host: "100.64.0.7:5000", Password: "rotated", Now: time.Now()}); err != nil {
		t.Fatal(err)
	}
	htpasswd, _ := os.ReadFile(filepath.Join(dir, "htpasswd"))
	hash, _ := strings.CutPrefix(strings.TrimSpace(string(htpasswd)), Username+":")
	if bcrypt.CompareHashAndPassword([]byte(hash), []byte("rotated")) != nil {
		t.Fatal("htpasswd still holds the old password")
	}
}

func TestPrepareRejectsBadInput(t *testing.T) {
	for name, f := range map[string]Files{
		"no password": {Host: "100.64.0.7:5000"},
		"no port":     {Host: "100.64.0.7", Password: "x"},
	} {
		t.Run(name, func(t *testing.T) {
			f.Dir, f.Now = t.TempDir(), time.Now()
			if _, err := Prepare(f); err == nil {
				t.Fatal("Prepare accepted it")
			}
		})
	}
}
