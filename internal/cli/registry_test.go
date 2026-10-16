package cli

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ThallesP/keel/internal/api"
	"github.com/ThallesP/keel/internal/cli/output"
)

func fakeDocker(t *testing.T, system string) string {
	t.Helper()
	bin, calls := t.TempDir(), filepath.Join(t.TempDir(), "calls")
	script := `#!/bin/sh
if [ "$1" = info ]; then echo "` + system + `"; exit 0; fi
echo "$* stdin=$(cat)" >> ` + calls + `
`
	if err := os.WriteFile(filepath.Join(bin, "docker"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	return calls
}

var testRegistry = api.Registry{Host: "100.64.0.7:5000", Username: "keel", Password: "pw", CA: "-----BEGIN CERTIFICATE-----\n"}

func TestRegistryLoginDockerDesktop(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	calls := fakeDocker(t, "Docker Desktop")

	path, err := trustRegistry(context.Background(), testRegistry)
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(home, ".docker", "certs.d", "100.64.0.7:5000", "ca.crt"); path != want {
		t.Fatalf("CA at %s, want %s", path, want)
	}
	if ca, _ := os.ReadFile(path); string(ca) != testRegistry.CA {
		t.Fatalf("CA file holds %q", ca)
	}

	if err := dockerLogin(context.Background(), testRegistry); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(calls)
	if want := "login 100.64.0.7:5000 --username keel --password-stdin stdin=pw\n"; string(got) != want {
		t.Fatalf("docker was called with %q, want %q", got, want)
	}
}

func TestRegistryLoginCannotWriteDockersCAs(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root writes anywhere")
	}
	home, keelDir := t.TempDir(), t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("KEEL_CONFIG_DIR", keelDir)
	fakeDocker(t, "Docker Desktop")
	locked := filepath.Join(home, ".docker")
	if err := os.Mkdir(locked, 0o500); err != nil {
		t.Fatal(err)
	}

	_, err := trustRegistry(context.Background(), testRegistry)

	var oe *output.Error
	if !errors.As(err, &oe) || oe.Code != output.CodeDocker {
		t.Fatalf("got %v, want %s", err, output.CodeDocker)
	}
	saved := filepath.Join(keelDir, "registry", "100.64.0.7:5000", "ca.crt")
	if ca, _ := os.ReadFile(saved); string(ca) != testRegistry.CA {
		t.Fatalf("CA not saved for the sudo fix: %q", ca)
	}
	if !strings.HasPrefix(oe.Fix, "sudo install -D -m 644 "+saved+" "+filepath.Join(locked, "certs.d")) {
		t.Fatalf("fix %q", oe.Fix)
	}
}

func TestRegistryLoginKeepsATrustedCA(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	fakeDocker(t, "Docker Desktop")
	dir := filepath.Join(home, ".docker", "certs.d", "100.64.0.7:5000")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "ca.crt"), []byte(testRegistry.CA), 0o444); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o755) })

	if _, err := trustRegistry(context.Background(), testRegistry); err != nil {
		t.Fatalf("an already trusted CA needs no write: %v", err)
	}
}
