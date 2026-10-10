package agent

import (
	"context"
	"encoding/json"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"time"
)

type State struct {
	path string

	mu    sync.Mutex
	data  stateFile
	dirty bool
}

type stateFile struct {
	EventsSince string            `json:"eventsSince,omitempty"`
	LogsSince   map[string]string `json:"logsSince"`
}

func LoadState(path string) *State {
	s := &State{path: path}
	raw, _ := os.ReadFile(path)
	if json.Unmarshal(raw, &s.data) != nil {
		s.data = stateFile{}
	}
	if s.data.LogsSince == nil {
		s.data.LogsSince = map[string]string{}
	}
	return s
}

func (s *State) EventsSince() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.data.EventsSince
}

func (s *State) SetEventsSince(v string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.data.EventsSince = v
	s.dirty = true
}

func (s *State) LogsSince(container string) (string, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	v, ok := s.data.LogsSince[container]
	return v, ok
}

func (s *State) Checkpoint(container, since string) {
	if since == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.data.LogsSince[container] = since
	s.dirty = true
}

func (s *State) Forget(container string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.data.LogsSince[container]; ok {
		delete(s.data.LogsSince, container)
		s.dirty = true
	}
}

func (s *State) LogsSinceIDs() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Collect(maps.Keys(s.data.LogsSince))
}

func (s *State) Flush() error {
	s.mu.Lock()
	if !s.dirty {
		s.mu.Unlock()
		return nil
	}
	s.dirty = false
	raw, _ := json.Marshal(s.data)
	s.mu.Unlock()
	return writeFileAtomic(s.path, raw)
}

func (s *State) RunWriter(ctx context.Context, every time.Duration, log *Logger) {
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if err := s.Flush(); err != nil {
				log.Log("state", "write failed: "+err.Error())
			}
		}
	}
}

func writeFileAtomic(path string, data []byte) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".state-*.json")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmp.Name(), 0o644); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}
