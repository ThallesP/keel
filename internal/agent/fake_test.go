package agent

import (
	"context"
	"errors"
	"io"
	"slices"
	"strings"
	"sync"
)

// fakeDocker is an in-memory Docker: containers, scripted log streams, scripted event streams.
type fakeDocker struct {
	mu         sync.Mutex
	info       NodeInfo
	infoErr    error
	containers []Container
	listErr    error
	// afterList runs inside ListSwarmContainers after the answer was taken: what happens on the
	// node while the list is in flight.
	afterList func()
	// logs scripts each ContainerLogs call of a container, in order (the last repeats).
	logs     map[string][]logScript
	logCalls []logCall
	// streams scripts each Events call, in order; when exhausted, Events blocks until ctx ends.
	streams     []eventScript
	eventsSince []string
	openStreams int
}

type logCall struct{ id, since string }

// logScript is one answer to ContainerLogs: data, then EOF (exited) or an error, or hold the
// stream open until the follower is stopped.
type logScript struct {
	err  error  // returned by ContainerLogs itself
	data []byte // read before the end
	end  error  // io.EOF = container exited; nil = stay open until ctx ends
	// chunk is the read size (default 7, to exercise frame reassembly). A TTY stream needs one
	// read: under 8 buffered bytes the parser waits for more, as the worker's did.
	chunk int
}

type eventScript struct {
	err    error // returned by Events itself
	events []Event
	end    error // io.EOF or a stream error; nil = stay open until ctx ends
}

func newFakeDocker() *fakeDocker {
	return &fakeDocker{info: NodeInfo{NodeID: "node-1", Name: "box"}, logs: map[string][]logScript{}}
}

func (d *fakeDocker) Info(context.Context) (NodeInfo, error) { return d.info, d.infoErr }

func (d *fakeDocker) ListSwarmContainers(context.Context) ([]Container, error) {
	d.mu.Lock()
	out, err, hook := slices.Clone(d.containers), d.listErr, d.afterList
	d.mu.Unlock()
	if hook != nil {
		hook() // the daemon answered; the list is on its way back
	}
	return out, err
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
	var s logScript
	if len(scripts) > 0 {
		s = scripts[0]
		if len(scripts) > 1 {
			d.logs[id] = scripts[1:]
		}
	}
	d.mu.Unlock()
	if s.err != nil {
		return nil, s.err
	}
	r := newScriptedReader(ctx, s.data, s.end)
	if s.chunk > 0 {
		r.chunk = s.chunk
	}
	return r, nil
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

func (d *fakeDocker) Events(ctx context.Context, since string) (EventStream, error) {
	d.mu.Lock()
	d.eventsSince = append(d.eventsSince, since)
	var s *eventScript
	if len(d.streams) > 0 {
		s = &d.streams[0]
		d.streams = d.streams[1:]
	}
	d.mu.Unlock()
	if s == nil {
		return &fakeEvents{ctx: ctx}, nil
	}
	if s.err != nil {
		return nil, s.err
	}
	return &fakeEvents{ctx: ctx, events: s.events, end: s.end}, nil
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

// scriptedReader returns data in small reads (to exercise frame reassembly), then end; with a
// nil end it blocks until ctx is done or it is closed.
type scriptedReader struct {
	ctx    context.Context
	data   []byte
	end    error
	chunk  int
	closed chan struct{}
	once   sync.Once
}

func newScriptedReader(ctx context.Context, data []byte, end error) *scriptedReader {
	return &scriptedReader{ctx: ctx, data: data, end: end, chunk: 7, closed: make(chan struct{})}
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

// fakeSink records batches. results scripts Send's answers in order (the last repeats; empty =
// always true). gate, when set, blocks every Send until a value is received.
type fakeSink struct {
	key     string
	mu      sync.Mutex
	batches [][]LogEvent
	results []bool
	gate    chan struct{}
}

func (s *fakeSink) Key() string { return s.key }

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
	ok := true
	if len(s.results) > 0 {
		ok = s.results[0]
		if len(s.results) > 1 {
			s.results = s.results[1:]
		}
	}
	return ok
}

func (s *fakeSink) sent() [][]LogEvent {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.batches)
}

// delivered is every event of every batch the sink accepted or was offered, in order.
func (s *fakeSink) messages() []string {
	var out []string
	for _, b := range s.sent() {
		for _, e := range b {
			out = append(out, e.Message)
		}
	}
	return out
}

// sinkSet is a SinkFactory over fake sinks, one per key, counting builds.
type sinkSet struct {
	mu     sync.Mutex
	sinks  map[string]*fakeSink
	builds int
	setup  func(*fakeSink)
}

func newSinkSet() *sinkSet { return &sinkSet{sinks: map[string]*fakeSink{}} }

func (ss *sinkSet) factory(cfg SinkConfig) (Sink, bool) {
	if cfg.Kind != "axiom" {
		return nil, false
	}
	ss.mu.Lock()
	defer ss.mu.Unlock()
	ss.builds++
	key := sinkKey(cfg)
	s := ss.sinks[key]
	if s == nil {
		s = &fakeSink{key: key}
		if ss.setup != nil {
			ss.setup(s)
		}
		ss.sinks[key] = s
	}
	return s, true
}

func (ss *sinkSet) get(cfg SinkConfig) *fakeSink {
	ss.mu.Lock()
	defer ss.mu.Unlock()
	return ss.sinks[sinkKey(cfg)]
}

// stamped builds stdout frames of "<stamp> <text>\n" lines.
func stamped(lines ...string) []byte {
	var out []byte
	for _, l := range lines {
		out = append(out, frame(1, l+"\n")...)
	}
	return out
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
