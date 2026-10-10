package agent

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"
	"sync"
)

type fakeDocker struct {
	mu          sync.Mutex
	info        NodeInfo
	infoErr     error
	containers  []Container
	afterList   func()
	logs        map[string][]logScript
	logCalls    []logCall
	streams     []fakeEvents
	eventsSince []string
}

type logCall struct{ id, since string }

type logScript struct {
	data  []byte
	end   error
	chunk int
}

func newFakeDocker() *fakeDocker {
	return &fakeDocker{info: NodeInfo{NodeID: "node-1", Name: "box"}, logs: map[string][]logScript{}}
}

func (d *fakeDocker) Info(context.Context) (NodeInfo, error) { return d.info, d.infoErr }

func (d *fakeDocker) ListSwarmContainers(context.Context) ([]Container, error) {
	d.mu.Lock()
	out, hook := slices.Clone(d.containers), d.afterList
	d.mu.Unlock()
	if hook != nil {
		hook()
	}
	return out, nil
}

func (d *fakeDocker) setContainers(cs ...Container) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.containers = cs
}

func (d *fakeDocker) script(id string, s ...logScript) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.logs[id] = s
}

func (d *fakeDocker) ContainerLogs(ctx context.Context, id, since string) (io.ReadCloser, error) {
	d.mu.Lock()
	d.logCalls = append(d.logCalls, logCall{id, since})
	scripts := d.logs[id]
	s := next(&scripts, logScript{})
	d.logs[id] = scripts
	d.mu.Unlock()
	return &scriptedReader{ctx: ctx, data: s.data, end: s.end, chunk: cmp.Or(s.chunk, 7), closed: make(chan struct{})}, nil
}

func (d *fakeDocker) calls() []logCall {
	d.mu.Lock()
	defer d.mu.Unlock()
	return slices.Clone(d.logCalls)
}

func (d *fakeDocker) callsFor(id string) []string {
	var out []string
	for _, c := range d.calls() {
		if c.id == id {
			out = append(out, c.since)
		}
	}
	return out
}

func (d *fakeDocker) Events(ctx context.Context, since string) EventStream {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.eventsSince = append(d.eventsSince, since)
	var s fakeEvents
	if len(d.streams) > 0 {
		s, d.streams = d.streams[0], d.streams[1:]
	}
	s.ctx = ctx
	return &s
}

func (d *fakeDocker) sinces() []string {
	d.mu.Lock()
	defer d.mu.Unlock()
	return slices.Clone(d.eventsSince)
}

type fakeEvents struct {
	ctx    context.Context
	events []Event
	end    error
}

func (s *fakeEvents) Next() (Event, error) {
	if len(s.events) > 0 {
		e := s.events[0]
		s.events = s.events[1:]
		return e, nil
	}
	if s.end != nil {
		return Event{}, s.end
	}
	<-s.ctx.Done()
	return Event{}, s.ctx.Err()
}

func (s *fakeEvents) Close() error { return nil }

type scriptedReader struct {
	ctx    context.Context
	data   []byte
	end    error
	chunk  int
	closed chan struct{}
	once   sync.Once
}

func (r *scriptedReader) Read(p []byte) (int, error) {
	if len(r.data) > 0 {
		n := min(len(p), len(r.data), r.chunk)
		copy(p, r.data[:n])
		r.data = r.data[n:]
		return n, nil
	}
	if r.end != nil {
		return 0, r.end
	}
	select {
	case <-r.ctx.Done():
		return 0, r.ctx.Err()
	case <-r.closed:
		return 0, errors.New("read on closed body")
	}
}

func (r *scriptedReader) Close() error {
	r.once.Do(func() { close(r.closed) })
	return nil
}

type fakeSink struct {
	mu      sync.Mutex
	batches [][]LogEvent
	results []bool
	gate    chan struct{}
}

func (s *fakeSink) Send(ctx context.Context, events []LogEvent) bool {
	if s.gate != nil {
		select {
		case <-s.gate:
		case <-ctx.Done():
			return false
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.batches = append(s.batches, slices.Clone(events))
	return next(&s.results, true)
}

func (s *fakeSink) sent() [][]LogEvent {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.batches)
}

func (s *fakeSink) messages() []string {
	var out []string
	for _, b := range s.sent() {
		for _, e := range b {
			out = append(out, e.Message)
		}
	}
	return out
}

type sinkSet struct {
	mu     sync.Mutex
	sinks  map[SinkConfig]*fakeSink
	builds int
	setup  func(*fakeSink)
}

func newSinkSet() *sinkSet { return &sinkSet{sinks: map[SinkConfig]*fakeSink{}} }

func (ss *sinkSet) factory(cfg SinkConfig) (Sink, bool) {
	if cfg.Kind != "axiom" {
		return nil, false
	}
	ss.mu.Lock()
	defer ss.mu.Unlock()
	ss.builds++
	s := &fakeSink{}
	if ss.setup != nil {
		ss.setup(s)
	}
	ss.sinks[cfg] = s
	return s, true
}

func (ss *sinkSet) get(cfg SinkConfig) *fakeSink {
	ss.mu.Lock()
	defer ss.mu.Unlock()
	return ss.sinks[cfg]
}

func next[T any](script *[]T, whenEmpty T) T {
	if len(*script) == 0 {
		return whenEmpty
	}
	v := (*script)[0]
	if len(*script) > 1 {
		*script = (*script)[1:]
	}
	return v
}

func stamped(lines ...string) []byte {
	var out []byte
	for _, l := range lines {
		out = append(out, frame(1, l+"\n")...)
	}
	return out
}

func numbered(n int) []byte {
	var lines []string
	for i := range n {
		lines = append(lines, fmt.Sprintf("2024-01-01T00:00:%02dZ line %d", i, i))
	}
	return stamped(lines...)
}

func containerID(c byte) string { return strings.Repeat(string(c), 64) }

func task(id byte, serviceID string, slot string, state string) Container {
	return Container{
		ID:    containerID(id),
		State: state,
		Labels: map[string]string{
			labelServiceName: "svc-" + serviceID,
			labelTaskID:      "task-" + string(id),
			labelTaskName:    "svc-" + serviceID + "." + slot + ".task-" + string(id),
		},
	}
}
