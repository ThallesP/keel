package agent

import (
	"context"
	"encoding/json"
	"log/slog"
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
	defer s.mu.Unlock()
	if !s.dirty {
		return nil
	}
	s.dirty = false
	raw, _ := json.Marshal(s.data)
	if err := os.MkdirAll(filepath.Dir(s.path), 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(s.path+".tmp", raw, 0o644); err != nil {
		return err
	}
	return os.Rename(s.path+".tmp", s.path)
}

func (s *State) RunWriter(ctx context.Context, every time.Duration, log *slog.Logger) {
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if err := s.Flush(); err != nil {
				log.Error("writing the agent state failed", "err", err)
			}
		}
	}
}
