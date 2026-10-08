package agent

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestLoadStateMissingOrGarbage(t *testing.T) {
	dir := t.TempDir()
	for name, content := range map[string]string{"garbage": "{not json", "null": "null", "wrong types": `{"eventsSince":1}`} {
		path := filepath.Join(dir, name+".json")
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
		s := LoadState(path)
		if s.EventsSince() != "" || len(s.LogsSinceIDs()) != 0 {
			t.Errorf("%s: state = %+v, want empty", name, s.data)
		}
	}
	s := LoadState(filepath.Join(dir, "missing", "state.json"))
	if s.EventsSince() != "" || len(s.LogsSinceIDs()) != 0 {
		t.Errorf("missing: state = %+v, want empty", s.data)
	}
}

// The Bun worker's state.json is read as is, so an upgrade neither replays nor skips.
func TestLoadStateFromBunWorker(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	bun := `{"eventsSince":"1727600000.123456790","logsSince":{"f00dbabe0000111122223333444455556666777788889999aaaabbbbccccdddd":"1727600000.123456790"}}`
	if err := os.WriteFile(path, []byte(bun), 0o644); err != nil {
		t.Fatal(err)
	}
	s := LoadState(path)
	if s.EventsSince() != "1727600000.123456790" {
		t.Errorf("eventsSince = %q", s.EventsSince())
	}
	if v, ok := s.LogsSince("f00dbabe0000111122223333444455556666777788889999aaaabbbbccccdddd"); !ok || v != "1727600000.123456790" {
		t.Errorf("logsSince = %q, %v", v, ok)
	}
}

func TestStateFlushFormat(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sub", "state.json")
	s := LoadState(path)
	if err := s.Flush(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("clean state was written (err %v)", err)
	}
	s.Forget("nope") // nothing to forget: still clean
	if err := s.Flush(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("forgetting nothing wrote the state")
	}

	s.Checkpoint([]resumePoint{{"c1", "1.000000001"}, {"c2", ""}, {"c1", "2.000000001"}})
	if err := s.Flush(); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(path)
	if want := `{"logsSince":{"c1":"2.000000001"}}`; string(raw) != want {
		t.Fatalf("state file = %s, want %s", raw, want)
	}

	s.SetEventsSince("3.000000001")
	s.Forget("c1")
	if err := s.Flush(); err != nil {
		t.Fatal(err)
	}
	raw, _ = os.ReadFile(path)
	if want := `{"eventsSince":"3.000000001","logsSince":{}}`; string(raw) != want {
		t.Fatalf("state file = %s, want %s", raw, want)
	}
	again := LoadState(path)
	if again.EventsSince() != "3.000000001" || len(again.LogsSinceIDs()) != 0 {
		t.Fatalf("reloaded = %+v", again.data)
	}
	entries, _ := os.ReadDir(filepath.Dir(path))
	if len(entries) != 1 {
		t.Fatalf("temporary files left behind: %v", entries)
	}
}

func TestStateWriterLogsFailures(t *testing.T) {
	dir := t.TempDir()
	blocker := filepath.Join(dir, "file")
	if err := os.WriteFile(blocker, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	s := LoadState(filepath.Join(blocker, "state.json")) // parent is a file: writes fail
	var buf syncBuffer
	log := NewLogger(&buf)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		s.RunWriter(ctx, 5*time.Millisecond, log)
		close(done)
	}()
	s.SetEventsSince("1.000000001")
	waitFor(t, func() bool { return strings.Contains(buf.String(), "[state] write failed: ") })
	cancel()
	<-done
	if n := strings.Count(buf.String(), "write failed"); n != 1 {
		t.Fatalf("write failed logged %d times, want once per change", n)
	}
	if !slices.Equal(s.LogsSinceIDs(), []string{}) {
		t.Fatal("unexpected resume points")
	}
}

// syncBuffer is a bytes.Buffer safe for the logger's concurrent writers and the test's reads.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// waitFor polls cond for up to 5 s.
func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatal("condition not met within 5s")
		}
		time.Sleep(2 * time.Millisecond)
	}
}
