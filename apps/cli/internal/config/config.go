// Package config is the CLI's state file, ~/.config/keel/config.json: the Keel installs you are
// logged in to and the directories linked to a project. It holds session tokens, so it is 0600
// in a 0700 directory and written atomically.
package config

import (
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"time"
)

type Config struct {
	// Name of the instance commands use when nothing else picks one.
	Current   string               `json:"current,omitempty"`
	Instances map[string]*Instance `json:"instances,omitempty"`
	// Absolute directory → project, resolved from the working directory upwards.
	Links map[string]*Link `json:"links,omitempty"`

	path string
}

// Instance is one Keel install.
type Instance struct {
	// Dashboard URL. Also the Origin better-auth trusts for sign-in.
	URL           string `json:"url"`
	ConvexURL     string `json:"convexUrl"`
	ConvexSiteURL string `json:"convexSiteUrl"`
	Email         string `json:"email,omitempty"`
	// Better-auth session token. Exchanged for a short-lived Convex JWT on every run.
	Token string `json:"token,omitempty"`
	// A keel login waiting for someone to approve it in the dashboard. The first run that finds
	// it approved swaps it for Token.
	Pending *PendingLogin `json:"pending,omitempty"`
}

// PendingLogin is a device authorization (RFC 8628) that keel login started.
type PendingLogin struct {
	// Secret: whoever holds it gets the session once the login is approved.
	DeviceCode string `json:"deviceCode"`
	UserCode   string `json:"userCode"`
	// The dashboard page that approves it, with the user code filled in.
	URL       string    `json:"url"`
	ExpiresAt time.Time `json:"expiresAt"`
	// Seconds to wait between polls.
	Interval int `json:"interval"`
}

func (p *PendingLogin) Expired() bool { return !time.Now().Before(p.ExpiresAt) }

type Link struct {
	Instance string `json:"instance"`
	Project  string `json:"project"`
}

// Dir is $KEEL_CONFIG_DIR, else $XDG_CONFIG_HOME/keel, else ~/.config/keel, on every OS.
func Dir() (string, error) {
	if dir := os.Getenv("KEEL_CONFIG_DIR"); dir != "" {
		return dir, nil
	}
	if dir := os.Getenv("XDG_CONFIG_HOME"); dir != "" {
		return filepath.Join(dir, "keel"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".config", "keel"), nil
}

// Load reads the config file; a missing file is an empty config.
func Load() (*Config, error) {
	dir, err := Dir()
	if err != nil {
		return nil, err
	}
	c := &Config{path: filepath.Join(dir, "config.json")}
	data, err := os.ReadFile(c.path)
	if errors.Is(err, fs.ErrNotExist) {
		return c, nil
	}
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(data, c); err != nil {
		return nil, err
	}
	return c, nil
}

func (c *Config) Path() string { return c.path }

// Save writes through a temp file and a rename, so a concurrent run never reads half a file.
func (c *Config) Save() error {
	if err := os.MkdirAll(filepath.Dir(c.path), 0o700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(c.path), ".config-*.json")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(append(data, '\n')); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), c.path)
}

// LinkFor returns the link of dir or its closest linked parent, with the directory it is on.
func (c *Config) LinkFor(dir string) (string, *Link) {
	for {
		if l, ok := c.Links[dir]; ok {
			return dir, l
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", nil
		}
		dir = parent
	}
}

func (c *Config) SetLink(dir string, l *Link) {
	if c.Links == nil {
		c.Links = map[string]*Link{}
	}
	c.Links[dir] = l
}

func (c *Config) SetInstance(name string, inst *Instance) {
	if c.Instances == nil {
		c.Instances = map[string]*Instance{}
	}
	c.Instances[name] = inst
}

// RemoveInstance drops the instance and every link to it.
func (c *Config) RemoveInstance(name string) {
	delete(c.Instances, name)
	for dir, l := range c.Links {
		if l.Instance == name {
			delete(c.Links, dir)
		}
	}
	if c.Current == name {
		c.Current = ""
	}
}
