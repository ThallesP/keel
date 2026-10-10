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
	Current   string               `json:"current,omitempty"`
	Instances map[string]*Instance `json:"instances,omitempty"`
	Links     map[string]*Link     `json:"links,omitempty"`

	path string
}

type Instance struct {
	URL     string        `json:"url"`
	Email   string        `json:"email,omitempty"`
	Token   string        `json:"token,omitempty"`
	Pending *PendingLogin `json:"pending,omitempty"`
}

type PendingLogin struct {
	DeviceCode string    `json:"deviceCode"`
	UserCode   string    `json:"userCode"`
	URL        string    `json:"url"`
	ExpiresAt  time.Time `json:"expiresAt"`
	Interval   int       `json:"interval"`
}

func (p *PendingLogin) Expired() bool { return !time.Now().Before(p.ExpiresAt) }

type Link struct {
	Instance string `json:"instance"`
	Project  string `json:"project"`
}

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
