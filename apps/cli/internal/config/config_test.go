package config

import (
	"os"
	"path/filepath"
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
