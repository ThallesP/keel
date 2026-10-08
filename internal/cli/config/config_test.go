package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLinkForWalksUp(t *testing.T) {
	c := &Config{}
	c.SetLink("/work/app", &Link{Instance: "dev", Project: "api"})
	if dir, l := c.LinkFor("/work/app/src/deep"); dir != "/work/app" || l.Project != "api" {
		t.Errorf("LinkFor(child) = %q, %v", dir, l)
	}
	if _, l := c.LinkFor("/work/other"); l != nil {
		t.Errorf("LinkFor(unrelated) = %v, want nil", l)
	}
}

func TestSaveIsPrivateAndRoundTrips(t *testing.T) {
	t.Setenv("KEEL_CONFIG_DIR", filepath.Join(t.TempDir(), "keel"))
	c, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	c.SetInstance("dev", &Instance{URL: "https://keel.test", Token: "secret"})
	c.SetLink("/work/app", &Link{Instance: "dev", Project: "api"})
	c.Current = "dev"
	if err := c.Save(); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(c.Path())
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Errorf("config mode = %v, want 0600", info.Mode().Perm())
	}
	again, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if again.Instances["dev"].Token != "secret" || again.Links["/work/app"].Project != "api" {
		t.Errorf("round trip lost data: %+v", again)
	}
	again.RemoveInstance("dev")
	if len(again.Links) != 0 || again.Current != "" {
		t.Errorf("RemoveInstance kept links or current: %+v", again)
	}
}

// A config file written by the Convex-era CLI keeps working: its instances, tokens, pending
// logins and links load; the Convex URLs are ignored and dropped on the next save.
func TestConvexEraFileLoads(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("KEEL_CONFIG_DIR", dir)
	old := `{
  "current": "keel.example.ts.net",
  "instances": {
    "keel.example.ts.net": {
      "url": "https://keel.example.ts.net",
      "convexUrl": "http://100.64.0.1:3210",
      "convexSiteUrl": "http://100.64.0.1:3211",
      "email": "me@example.com",
      "token": "tok",
      "pending": {
        "deviceCode": "dev",
        "userCode": "ABCDEFGH",
        "url": "https://keel.example.ts.net/device?user_code=ABCDEFGH",
        "expiresAt": "2026-10-08T12:30:00Z",
        "interval": 5
      }
    }
  },
  "links": { "/home/me/acme": { "instance": "keel.example.ts.net", "project": "acme-api" } }
}
`
	if err := os.WriteFile(filepath.Join(dir, "config.json"), []byte(old), 0o600); err != nil {
		t.Fatal(err)
	}
	c, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	inst := c.Instances["keel.example.ts.net"]
	if c.Current != "keel.example.ts.net" || inst == nil || inst.URL != "https://keel.example.ts.net" ||
		inst.Token != "tok" || inst.Email != "me@example.com" || inst.Pending == nil ||
		inst.Pending.DeviceCode != "dev" || inst.Pending.Interval != 5 || c.Links["/home/me/acme"].Project != "acme-api" {
		t.Fatalf("loaded %+v / %+v", c, inst)
	}
	if err := c.Save(); err != nil {
		t.Fatal(err)
	}
	saved, _ := os.ReadFile(filepath.Join(dir, "config.json"))
	if strings.Contains(string(saved), "convex") {
		t.Errorf("saved file still names Convex URLs:\n%s", saved)
	}
}
