package agent

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"time"
)

type State struct {
	path string

	mu    sync.Mutex
	data  stateFile
	dirty bool

	writeMu sync.Mutex
}

type stateFile struct {
	EventsSince string            `json:"eventsSince,omitempty"`
	LogsSince   map[string]string `json:"logsSince"`
}

func LoadState(path string) *State {
	s := &State{path: path, data: stateFile{LogsSince: map[string]string{}}}
	raw, err := os.ReadFile(path)
	if err != nil {
		return s
	}
	var parsed stateFile
	if json.Unmarshal(raw, &parsed) != nil {
		return s
	}
	s.data.EventsSince = parsed.EventsSince
	if parsed.LogsSince != nil {
		s.data.LogsSince = parsed.LogsSince
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

func (s *State) Checkpoint(points []resumePoint) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, p := range points {
		if p.since == "" {
			continue
		}
		s.data.LogsSince[p.container] = p.since
		s.dirty = true
	}
}

type resumePoint struct{ container, since string }

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
	ids := make([]string, 0, len(s.data.LogsSince))
	for id := range s.data.LogsSince {
		ids = append(ids, id)
	}
	return ids
}

func (s *State) Flush() error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	s.mu.Lock()
	if !s.dirty {
		s.mu.Unlock()
		return nil
	}
	s.dirty = false
	raw := marshal(s.data)
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
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		os.Remove(tmp.Name())
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmp.Name())
		return err
	}
	if err := os.Chmod(tmp.Name(), 0o644); err != nil {
		os.Remove(tmp.Name())
		return err
	}
	if err := os.Rename(tmp.Name(), path); err != nil {
		os.Remove(tmp.Name())
		return err
	}
	return nil
}
