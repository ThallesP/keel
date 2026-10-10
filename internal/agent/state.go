package agent

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// State holds the resume points, persisted to a per-node volume so a restart neither replays nor
// skips: eventsSince for the Docker event stream, logsSince[containerID] for each container tail
// (delivery-confirmed: only advanced once the sink accepted the line). Same file and format as
// the Bun worker's state.json, so an upgrade continues where it stopped
// (docs/go/spec/swarm-worker.md §13.7):
//
//	{"eventsSince":"1727600000.123456790","logsSince":{"<full container id>":"1727600000.123456790"}}
//
// Losing the file costs one full observe sweep and, for logs, re-reading every container from its
// sink's connect time (duplicates in the sink, never a gap).
type State struct {
	path string

	mu    sync.Mutex
	data  stateFile
	dirty bool

	writeMu sync.Mutex // one file write at a time (the 1 s writer and the final write at shutdown)
}

type stateFile struct {
	EventsSince string            `json:"eventsSince,omitempty"`
	LogsSince   map[string]string `json:"logsSince"`
}

// LoadState reads path. A missing or unreadable file is an empty state, as in the worker.
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

// EventsSince is the Docker events resume point ("" = none).
func (s *State) EventsSince() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.data.EventsSince
}

// SetEventsSince records the events resume point.
func (s *State) SetEventsSince(v string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.data.EventsSince = v
	s.dirty = true
}

// LogsSince is the delivery-confirmed resume point of a container.
func (s *State) LogsSince(container string) (string, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	v, ok := s.data.LogsSince[container]
	return v, ok
}

// Checkpoint records resume points of delivered lines, in order (later entries win).
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

// Forget drops a container's resume point (the container and its log are gone).
func (s *State) Forget(container string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.data.LogsSince[container]; ok {
		delete(s.data.LogsSince, container)
		s.dirty = true
	}
}

// LogsSinceIDs lists the containers that have a resume point.
func (s *State) LogsSinceIDs() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	ids := make([]string, 0, len(s.data.LogsSince))
	for id := range s.data.LogsSince {
		ids = append(ids, id)
	}
	return ids
}

// Flush writes the file when something changed since the last write. As in the worker, the dirty
// flag is cleared before writing: a failed write is retried at the next change.
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

// RunWriter flushes every interval until ctx is done ("written at most once a second").
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

// writeFileAtomic writes through a temporary file in the same directory and renames it, so a
// crash mid-write never leaves a truncated state file.
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
