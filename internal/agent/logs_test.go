package agent

import (
	"context"
	"errors"
	"io"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

var (
	sinkA = SinkConfig{Kind: "axiom", Domain: "api.axiom.co", Dataset: "org-a", Token: "xaat-aaaaaa"}
	sinkB = SinkConfig{Kind: "axiom", Domain: "api.axiom.co", Dataset: "org-b", Token: "xaat-bbbbbb"}
)

func ms(v float64) *float64 { return &v }

func newTestShipper(t *testing.T, d *fakeDocker, ss *sinkSet) (*Shipper, *State, *syncBuffer) {
	t.Helper()
	state := LoadState(t.TempDir() + "/state.json")
	buf := &syncBuffer{}
	s := NewShipper(d, state, NewLogger(buf), ss.factory, nil)
	s.flushEvery = 5 * time.Millisecond
	s.retryEvery = 5 * time.Millisecond
	s.followRetry = 5 * time.Millisecond
	s.SetNodeID("node-1")
	t.Cleanup(func() { s.Close(time.Second) })
	return s, state, buf
}

func (s *Shipper) following() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	var ids []string
	for id := range s.followers {
		ids = append(ids, id)
	}
	slices.Sort(ids)
	return ids
}

func (s *Shipper) isFinished(id string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.finished[id]
}

func (s *Shipper) lastRead(id string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.readSince[id]
}

func (s *Shipper) queued() map[string]int {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := map[string]int{}
	for k, q := range s.queues {
		out[k] = len(q.entries)
		if q.draining != nil {
			out[k+" draining"] = 1
		}
	}
	return out
}

func reconcile(t *testing.T, s *Shipper) {
	t.Helper()
	if err := s.ReconcileFollowers(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestShipperShipsAndCheckpointsAfterDelivery(t *testing.T) {
	d := newFakeDocker()
	c := task('a', "n1", "2", "running")
	d.setContainers(c)
	d.script(c.ID, logScript{data: append(
		stamped("2024-01-01T00:00:01.000000001Z hello"),
		frame(2, "2024-01-01T00:00:02.5Z oops\n")...)})
	ss := newSinkSet()
	gate := make(chan struct{})
	ss.setup = func(f *fakeSink) { f.gate = gate }
	s, state, logs := newTestShipper(t, d, ss)

	if !s.ApplyConfig([]SinkRoute{{ProjectID: "p1", ServiceIDs: []string{"n1"}, Sink: sinkA, Since: ms(1704067200000)}}) {
		t.Fatal("first config reported unchanged")
	}
	reconcile(t, s)
	waitFor(t, func() bool { return s.lastRead(c.ID) == "1704067202.500000001" })
	if got := d.callsFor(c.ID); !slices.Equal(got, []string{"1704067200.000000000"}) {
		t.Fatalf("logs since = %v, want the sink's connect time", got)
	}
	if v, ok := state.LogsSince(c.ID); ok {
		t.Fatalf("resume point %q saved before the sink had the lines", v)
	}

	close(gate)
	sink := ss.get(sinkA)
	waitFor(t, func() bool { return len(sink.messages()) == 2 })
	waitFor(t, func() bool { v, _ := state.LogsSince(c.ID); return v == "1704067202.500000001" })

	var events []LogEvent
	for _, b := range sink.sent() {
		events = append(events, b...)
	}
	want := []LogEvent{
		{Time: "2024-01-01T00:00:01.000000001Z", Message: "hello", Stream: "stdout", ServiceID: "n1", Service: "svc-n1", Task: "task-a", Replica: 2, Node: "node-1", Container: "aaaaaaaaaaaa"},
		{Time: "2024-01-01T00:00:02.5Z", Message: "oops", Stream: "stderr", ServiceID: "n1", Service: "svc-n1", Task: "task-a", Replica: 2, Node: "node-1", Container: "aaaaaaaaaaaa"},
	}
	if !slices.Equal(events, want) {
		t.Fatalf("events = %+v\nwant %+v", events, want)
	}
	out := logs.String()
	for _, w := range []string{
		`[logs] config applied {"sinks":1,"services":1}`,
		`[logs] following svc-n1 (aaaaaaaaaaaa) {"since":"1704067200.000000000"}`,
	} {
		if !strings.Contains(out, w) {
			t.Errorf("log lacks %q:\n%s", w, out)
		}
	}
}

func TestShipperResumePointPrecedence(t *testing.T) {
	d := newFakeDocker()
	persisted := task('p', "n1", "1", "running")
	fresh := task('f', "n2", "1", "running")
	flaky := task('r', "n1", "1", "running")
	d.setContainers(persisted, fresh, flaky)
	d.script(flaky.ID,
		logScript{data: stamped("2024-01-01T00:00:05.000000007Z one"), end: errors.New("unexpected EOF")},
		logScript{},
	)
	ss := newSinkSet()
	s, state, logs := newTestShipper(t, d, ss)
	state.Checkpoint([]resumePoint{{persisted.ID, "1704067300.000000001"}})
	s.ApplyConfig([]SinkRoute{
		{ProjectID: "p1", ServiceIDs: []string{"n1"}, Sink: sinkA, Since: ms(1704067200000)},
		{ProjectID: "p2", ServiceIDs: []string{"n2"}, Sink: sinkB},
	})
	reconcile(t, s)
	waitFor(t, func() bool { return len(d.callsFor(flaky.ID)) == 2 })

	if got := d.callsFor(persisted.ID); !slices.Equal(got, []string{"1704067300.000000001"}) {
		t.Errorf("persisted container since = %v", got)
	}
	if got := d.callsFor(fresh.ID); !slices.Equal(got, []string{""}) {
		t.Errorf("fresh container since = %v, want tail=0", got)
	}
	if got := d.callsFor(flaky.ID); !slices.Equal(got, []string{"1704067200.000000000", "1704067205.000000008"}) {
		t.Errorf("re-opened tail since = %v, want the line read in this process", got)
	}
	if !strings.Contains(logs.String(), "[logs] follow rrrrrrrrrrrr failed (unexpected EOF), retry in 3s") {
		t.Errorf("log = %s", logs.String())
	}
	if !strings.Contains(logs.String(), `[logs] following svc-n2 (ffffffffffff) {"since":"now"}`) {
		t.Errorf("log = %s", logs.String())
	}
}

func TestShipperRetryKeepsBatchAndResumePoint(t *testing.T) {
	d := newFakeDocker()
	c := task('a', "n1", "1", "running")
	d.setContainers(c)
	d.script(c.ID, logScript{data: stamped("2024-01-01T00:00:01Z one", "2024-01-01T00:00:02Z two")})
	ss := newSinkSet()
	gate := make(chan struct{})
	ss.setup = func(f *fakeSink) { f.results = []bool{false, true}; f.gate = gate }
	s, state, _ := newTestShipper(t, d, ss)
	s.ApplyConfig([]SinkRoute{{ProjectID: "p1", ServiceIDs: []string{"n1"}, Sink: sinkA}})
	reconcile(t, s)
	waitFor(t, func() bool { return s.lastRead(c.ID) == "1704067202.000000001" })
	waitFor(t, func() bool { return s.queued()[sinkKey(sinkA)+" draining"] == 1 })

	gate <- struct{}{}
	waitFor(t, func() bool { return len(ss.get(sinkA).sent()) == 1 })
	if _, ok := state.LogsSince(c.ID); ok {
		t.Fatal("a failed batch moved the resume point")
	}
	close(gate)
	waitFor(t, func() bool { v, _ := state.LogsSince(c.ID); return v == "1704067202.000000001" })
	msgs := ss.get(sinkA).messages()
	n := len(ss.get(sinkA).sent()[0])
	if !slices.Equal(msgs[:n], msgs[n:2*n]) || msgs[len(msgs)-1] != "two" {
		t.Fatalf("retried batch = %v, want the failed batch again, in order", msgs)
	}
}

func TestShipperBatchesOf500(t *testing.T) {
	d := newFakeDocker()
	c := task('a', "n1", "1", "exited")
	var lines []string
	for i := range 1200 {
		lines = append(lines, "2024-01-01T00:00:01."+strconv.Itoa(100000000+i)+"Z line "+strconv.Itoa(i))
	}
	d.setContainers(c)
	d.script(c.ID, logScript{data: stamped(lines...), end: io.EOF})
	ss := newSinkSet()
	gate := make(chan struct{})
	ss.setup = func(f *fakeSink) { f.gate = gate }
	s, state, _ := newTestShipper(t, d, ss)
	s.flushEvery = time.Hour
	state.Checkpoint([]resumePoint{{c.ID, "1704067200.000000001"}})
	s.ApplyConfig([]SinkRoute{{ProjectID: "p1", ServiceIDs: []string{"n1"}, Sink: sinkA}})
	reconcile(t, s)
	waitFor(t, func() bool { return s.isFinished(c.ID) })
	close(gate)
	s.Flush(context.Background())
	sink := ss.get(sinkA)
	waitFor(t, func() bool { return len(sink.messages()) == 1200 })
	var sizes []int
	for _, b := range sink.sent() {
		sizes = append(sizes, len(b))
	}
	if !slices.Equal(sizes, []int{500, 500, 200}) {
		t.Fatalf("batch sizes = %v, want [500 500 200]", sizes)
	}
	if msgs := sink.messages(); msgs[0] != "line 0" || msgs[1199] != "line 1199" {
		t.Fatalf("order broken: %s … %s", msgs[0], msgs[1199])
	}
	waitFor(t, func() bool { v, _ := state.LogsSince(c.ID); return v == "1704067201.100001200" })
}

func TestShipperBackPressure(t *testing.T) {
	d := newFakeDocker()
	c := task('a', "n1", "1", "running")
	var lines []string
	for i := range 10 {
		lines = append(lines, "2024-01-01T00:00:0"+strconv.Itoa(i)+"Z line "+strconv.Itoa(i))
	}
	d.setContainers(c)
	d.script(c.ID, logScript{data: stamped(lines...)})
	ss := newSinkSet()
	gate := make(chan struct{})
	ss.setup = func(f *fakeSink) { f.gate = gate }
	s, _, _ := newTestShipper(t, d, ss)
	s.maxQueue = 4
	s.ApplyConfig([]SinkRoute{{ProjectID: "p1", ServiceIDs: []string{"n1"}, Sink: sinkA}})
	reconcile(t, s)
	waitFor(t, func() bool { return s.lastRead(c.ID) == "1704067203.000000001" })
	time.Sleep(30 * time.Millisecond)
	if got := s.lastRead(c.ID); got != "1704067203.000000001" {
		t.Fatalf("the follower read on past a full queue: %s", got)
	}
	close(gate)
	sink := ss.get(sinkA)
	waitFor(t, func() bool { return len(sink.messages()) == 10 })
	want := make([]string, 10)
	for i := range want {
		want[i] = "line " + strconv.Itoa(i)
	}
	if got := sink.messages(); !slices.Equal(got, want) {
		t.Fatalf("messages = %v", got)
	}
}

func TestShipperRemovedSinkDropsItsQueue(t *testing.T) {
	d := newFakeDocker()
	c := task('a', "n1", "1", "running")
	d.setContainers(c)
	d.script(c.ID, logScript{data: stamped("2024-01-01T00:00:01Z a", "2024-01-01T00:00:02Z b", "2024-01-01T00:00:03Z c")})
	ss := newSinkSet()
	ss.setup = func(f *fakeSink) { f.results = []bool{false} }
	s, _, logs := newTestShipper(t, d, ss)
	s.ApplyConfig([]SinkRoute{{ProjectID: "p1", ServiceIDs: []string{"n1"}, Sink: sinkA}})
	reconcile(t, s)
	key := sinkKey(sinkA)
	waitFor(t, func() bool {
		q := s.queued()
		return q[key] == 3 && q[key+" draining"] == 0 && len(ss.get(sinkA).sent()) > 0
	})

	s.ApplyConfig(nil)
	if q := s.queued(); len(q) != 0 {
		t.Fatalf("queues = %v, want the removed sink's dropped", q)
	}
	if !strings.Contains(logs.String(), "[logs] dropping 3 queued lines for a removed sink") {
		t.Fatalf("log = %s", logs.String())
	}
	reconcile(t, s)
	if f := s.following(); len(f) != 0 {
		t.Fatalf("still following %v after the sink was removed", f)
	}
}

func TestShipperRemovedSinkReleasesWaiters(t *testing.T) {
	d := newFakeDocker()
	c := task('a', "n1", "1", "running")
	d.setContainers(c)
	var lines []string
	for i := range 5 {
		lines = append(lines, "2024-01-01T00:00:0"+strconv.Itoa(i)+"Z line "+strconv.Itoa(i))
	}
	d.script(c.ID, logScript{data: stamped(lines...)})
	rotated := sinkA
	rotated.Token = "xaat-rotated"
	ss := newSinkSet()
	s, state, logs := newTestShipper(t, d, ss)
	s.maxQueue = 2
	s.flushEvery = time.Hour
	s.ApplyConfig([]SinkRoute{{ProjectID: "p1", ServiceIDs: []string{"n1"}, Sink: sinkA}})
	reconcile(t, s)
	waitFor(t, func() bool { return s.lastRead(c.ID) == "1704067201.000000001" })
	time.Sleep(20 * time.Millisecond)
	if s.lastRead(c.ID) != "1704067201.000000001" {
		t.Fatal("read past a full queue")
	}

	s.ApplyConfig([]SinkRoute{{ProjectID: "p1", ServiceIDs: []string{"n1"}, Sink: rotated}})
	waitFor(t, func() bool {
		r := s.lastRead(c.ID)
		return r == "1704067203.000000001" || r == "1704067204.000000001"
	})
	s.Flush(context.Background())
	waitFor(t, func() bool { return s.lastRead(c.ID) == "1704067204.000000001" })
	s.Flush(context.Background())
	if got := ss.get(rotated).messages(); !slices.Equal(got, []string{"line 2", "line 3", "line 4"}) {
		t.Fatalf("new sink got %v, want the held line 2 and the rest", got)
	}
	if got := ss.get(sinkA).messages(); len(got) != 0 {
		t.Fatalf("the removed sink was sent %v", got)
	}
	if !strings.Contains(logs.String(), "[logs] dropping 2 queued lines for a removed sink") {
		t.Fatalf("log = %s", logs.String())
	}
	waitFor(t, func() bool { v, _ := state.LogsSince(c.ID); return v == "1704067204.000000001" })
}

func TestShipperBackPressureReleasesBelowHalf(t *testing.T) {
	d := newFakeDocker()
	c := task('a', "n1", "1", "running")
	var lines []string
	for i := range 6 {
		lines = append(lines, "2024-01-01T00:00:0"+strconv.Itoa(i)+"Z line "+strconv.Itoa(i))
	}
	d.setContainers(c)
	d.script(c.ID, logScript{data: stamped(lines...)})
	ss := newSinkSet()
	gate := make(chan struct{})
	ss.setup = func(f *fakeSink) { f.gate = gate }
	s, _, _ := newTestShipper(t, d, ss)
	s.maxQueue = 4
	s.flushEvery = time.Hour
	s.ApplyConfig([]SinkRoute{{ProjectID: "p1", ServiceIDs: []string{"n1"}, Sink: sinkA}})
	reconcile(t, s)
	waitFor(t, func() bool { return s.lastRead(c.ID) == "1704067203.000000001" })
	sink := ss.get(sinkA)
	held := func() bool {
		time.Sleep(20 * time.Millisecond)
		return s.lastRead(c.ID) == "1704067203.000000001"
	}
	if !held() {
		t.Fatal("read past a full queue")
	}
	s.mu.Lock()
	s.flushLines = 1
	s.mu.Unlock()
	go s.Flush(context.Background())
	gate <- struct{}{}
	waitFor(t, func() bool { return len(sink.messages()) == 1 })
	if !held() {
		t.Fatal("room came back at 3 of 4 queued")
	}
	gate <- struct{}{}
	waitFor(t, func() bool { return len(sink.messages()) == 2 })
	if !held() {
		t.Fatal("room came back at 2 of 4 queued")
	}
	gate <- struct{}{}
	waitFor(t, func() bool { return s.lastRead(c.ID) == "1704067205.000000001" })
	close(gate)
	waitFor(t, func() bool { return len(sink.messages()) == 6 })
}

func TestShipperReconcileFollowers(t *testing.T) {
	d := newFakeDocker()
	running := task('1', "n1", "1", "running")
	pending := task('2', "n1", "2", "exited")
	done := task('3', "n1", "3", "exited")
	foreign := task('4', "n9", "1", "running")
	other := Container{ID: containerID('5'), State: "running", Labels: map[string]string{labelServiceName: "keel-agent"}}
	d.setContainers(running, pending, done, foreign, other)
	d.script(pending.ID, logScript{data: stamped("2024-01-01T00:00:09Z last words"), end: io.EOF})
	ss := newSinkSet()
	s, state, _ := newTestShipper(t, d, ss)
	gone := containerID('9')
	state.Checkpoint([]resumePoint{{pending.ID, "1704067200.000000001"}, {gone, "1.000000001"}})
	s.ApplyConfig([]SinkRoute{{ProjectID: "p1", ServiceIDs: []string{"n1"}, Sink: sinkA}})
	reconcile(t, s)

	waitFor(t, func() bool { return s.isFinished(pending.ID) })
	if got := d.calls(); len(got) != 2 {
		t.Fatalf("followed %v, want the running and the pending containers only", got)
	}
	if _, ok := state.LogsSince(gone); ok {
		t.Error("the resume point of a removed container was kept")
	}
	if f := s.following(); !slices.Equal(f, []string{running.ID}) {
		t.Errorf("following %v", f)
	}
	waitFor(t, func() bool { v, _ := state.LogsSince(pending.ID); return v == "1704067209.000000001" })

	reconcile(t, s)
	if got := d.callsFor(pending.ID); len(got) != 1 {
		t.Errorf("a finished container was read again: %v", got)
	}

	d.setContainers(running)
	reconcile(t, s)
	if s.isFinished(pending.ID) {
		t.Error("finished kept for a removed container")
	}
	if _, ok := state.LogsSince(pending.ID); ok {
		t.Error("resume point kept for a removed container")
	}

	state.Checkpoint([]resumePoint{{running.ID, "5.000000001"}})
	s.ApplyConfig(nil)
	reconcile(t, s)
	if f := s.following(); len(f) != 0 {
		t.Errorf("following %v without a sink", f)
	}
	if v, _ := state.LogsSince(running.ID); v != "5.000000001" {
		t.Error("stopping an unrouted container dropped its resume point")
	}
}

func TestShipperReconcileKeepsContainersStartedDuringTheList(t *testing.T) {
	d := newFakeDocker()
	old := task('o', "n1", "1", "running")
	young := task('y', "n1", "2", "running")
	gone := task('g', "n1", "3", "exited")
	d.setContainers(old)
	d.script(young.ID, logScript{data: stamped("2024-01-01T00:00:07Z first line")})
	ss := newSinkSet()
	s, state, _ := newTestShipper(t, d, ss)
	state.Checkpoint([]resumePoint{{gone.ID, "1.000000001"}})
	s.ApplyConfig([]SinkRoute{{ProjectID: "p1", ServiceIDs: []string{"n1"}, Sink: sinkA, Since: ms(1704067200000)}})
	d.afterList = func() {
		d.mu.Lock()
		d.afterList = nil
		d.mu.Unlock()
		s.OnContainerEvent("start", young.ID, young.Labels)
		waitFor(t, func() bool { v, _ := state.LogsSince(young.ID); return v == "1704067207.000000001" })
	}
	reconcile(t, s)

	if f := s.following(); !slices.Equal(f, []string{old.ID, young.ID}) {
		t.Fatalf("following %v, want the container that started during the list kept", f)
	}
	if v, _ := state.LogsSince(young.ID); v != "1704067207.000000001" {
		t.Fatalf("its delivered resume point was forgotten: %q", v)
	}
	if s.lastRead(young.ID) != "1704067207.000000001" {
		t.Fatal("its in-process resume point was forgotten")
	}
	if _, ok := state.LogsSince(gone.ID); ok {
		t.Fatal("a container that was gone before the list kept its resume point")
	}

	d.setContainers(old, young)
	reconcile(t, s)
	if got := d.callsFor(young.ID); len(got) != 1 {
		t.Fatalf("re-read %v", got)
	}
	d.setContainers(old)
	reconcile(t, s)
	if _, ok := state.LogsSince(young.ID); ok || slices.Contains(s.following(), young.ID) {
		t.Fatal("a removed container was kept")
	}
}

func TestShipperContainerEvents(t *testing.T) {
	d := newFakeDocker()
	ss := newSinkSet()
	s, state, _ := newTestShipper(t, d, ss)
	var mu sync.Mutex
	refreshes := 0
	s.refresh = func() { mu.Lock(); refreshes++; mu.Unlock() }
	count := func() int { mu.Lock(); defer mu.Unlock(); return refreshes }
	now := time.Date(2026, 10, 8, 12, 0, 0, 0, time.UTC)
	s.now = func() time.Time { return now }
	s.ApplyConfig([]SinkRoute{{ProjectID: "p1", ServiceIDs: []string{"n1"}, Sink: sinkA}})

	routed := task('a', "n1", "1", "running")
	s.OnContainerEvent("start", routed.ID, routed.Labels)
	waitFor(t, func() bool { return len(d.callsFor(routed.ID)) == 1 })

	unrouted := task('b', "n7", "1", "running")
	s.OnContainerEvent("start", unrouted.ID, unrouted.Labels)
	s.OnContainerEvent("start", unrouted.ID, unrouted.Labels)
	if count() != 1 {
		t.Fatalf("early polls = %d, want 1 (throttled to one per 5 s)", count())
	}
	now = now.Add(6 * time.Second)
	s.OnContainerEvent("start", unrouted.ID, unrouted.Labels)
	if count() != 2 {
		t.Fatalf("early polls = %d, want 2", count())
	}
	s.OnContainerEvent("start", "zzz", map[string]string{labelServiceName: "keel-agent"})
	s.OnContainerEvent("die", routed.ID, routed.Labels)
	if count() != 2 || len(d.calls()) != 1 {
		t.Fatal("a non-svc start or a die did something")
	}

	state.Checkpoint([]resumePoint{{routed.ID, "1.000000001"}})
	s.OnContainerEvent("destroy", routed.ID, nil)
	if f := s.following(); len(f) != 0 {
		t.Errorf("still following %v after destroy", f)
	}
	if _, ok := state.LogsSince(routed.ID); ok {
		t.Error("destroy kept the resume point")
	}
}

func TestShipperOrganizationIsolation(t *testing.T) {
	d := newFakeDocker()
	a := task('a', "n1", "1", "running")
	b := task('b', "n2", "1", "running")
	x := task('x', "n3", "1", "running")
	d.setContainers(a, b, x)
	d.script(a.ID, logScript{data: stamped("2024-01-01T00:00:01Z from org A")})
	d.script(b.ID, logScript{data: stamped("2024-01-01T00:00:01Z from org B")})
	d.script(x.ID, logScript{data: stamped("2024-01-01T00:00:01Z secret")})
	ss := newSinkSet()
	s, _, _ := newTestShipper(t, d, ss)
	s.ApplyConfig([]SinkRoute{
		{ProjectID: "p1", ServiceIDs: []string{"n1"}, Sink: sinkA},
		{ProjectID: "p2", ServiceIDs: []string{"n2"}, Sink: sinkB},
	})
	reconcile(t, s)
	waitFor(t, func() bool { return len(ss.get(sinkA).messages()) == 1 && len(ss.get(sinkB).messages()) == 1 })

	for sink, service := range map[*fakeSink]string{ss.get(sinkA): "n1", ss.get(sinkB): "n2"} {
		for _, batch := range sink.sent() {
			for _, e := range batch {
				if e.ServiceID != service {
					t.Errorf("sink %s got a line of %s", sink.key, e.ServiceID)
				}
			}
		}
	}
	if got := d.callsFor(x.ID); len(got) != 0 {
		t.Fatalf("an unrouted service was read: %v", got)
	}

	s.ApplyConfig([]SinkRoute{{ProjectID: "p1", ServiceIDs: []string{"n1"}, Sink: sinkA}})
	reconcile(t, s)
	s.OnContainerEvent("start", b.ID, b.Labels)
	if f := s.following(); !slices.Equal(f, []string{a.ID}) {
		t.Fatalf("following %v, want only org A's container", f)
	}
	for _, m := range ss.get(sinkA).messages() {
		if m != "from org A" {
			t.Fatalf("org A's sink got %q", m)
		}
	}
}

func TestShipperSharedSinkQueue(t *testing.T) {
	d := newFakeDocker()
	a := task('a', "n1", "1", "running")
	c := task('c', "n3", "1", "running")
	d.setContainers(a, c)
	d.script(a.ID, logScript{data: stamped("2024-01-01T00:00:01Z one")})
	d.script(c.ID, logScript{data: stamped("2024-01-01T00:00:01Z three")})
	ss := newSinkSet()
	s, _, _ := newTestShipper(t, d, ss)
	s.ApplyConfig([]SinkRoute{
		{ProjectID: "p1", ServiceIDs: []string{"n1"}, Sink: sinkA},
		{ProjectID: "p3", ServiceIDs: []string{"n3"}, Sink: sinkA},
	})
	reconcile(t, s)
	waitFor(t, func() bool { return len(ss.get(sinkA).messages()) == 2 })
	if ss.builds != 1 {
		t.Errorf("sink built %d times, want once", ss.builds)
	}
	if q := s.queued(); len(q) != 1 {
		t.Errorf("queues = %v, want one", q)
	}
}

func TestApplyConfigChanged(t *testing.T) {
	ss := newSinkSet()
	s, _, logs := newTestShipper(t, newFakeDocker(), ss)
	cfg := []SinkRoute{{ProjectID: "p1", ServiceIDs: []string{"n1"}, Sink: sinkA, Since: ms(1)}}
	if !s.ApplyConfig(cfg) {
		t.Fatal("first apply unchanged")
	}
	if s.ApplyConfig(cfg) {
		t.Fatal("same config reported changed")
	}
	if ss.builds != 1 {
		t.Fatalf("builds = %d, want the sink reused", ss.builds)
	}
	if n := strings.Count(logs.String(), "config applied"); n != 1 {
		t.Fatalf("config applied logged %d times", n)
	}
	more := []SinkRoute{{ProjectID: "p1", ServiceIDs: []string{"n1", "n2"}, Sink: sinkA}}
	if !s.ApplyConfig(more) {
		t.Fatal("a new service was not a change")
	}
	rotated := sinkA
	rotated.Token = "xaat-rotated"
	if !s.ApplyConfig([]SinkRoute{{ProjectID: "p1", ServiceIDs: []string{"n1", "n2"}, Sink: rotated}}) {
		t.Fatal("a new token was not a change")
	}
	if ss.builds != 2 {
		t.Fatalf("builds = %d, want a new sink for the new token", ss.builds)
	}
	if s.ApplyConfig([]SinkRoute{{ProjectID: "p9", ServiceIDs: []string{"n9"}, Sink: SinkConfig{Kind: "clickhouse"}}}); len(s.routes) != 0 {
		t.Fatalf("an unknown sink kind was routed: %v", s.routes)
	}
	if !strings.Contains(logs.String(), `config applied {"sinks":1,"services":2}`) {
		t.Errorf("log = %s", logs.String())
	}
}

func TestShipperUnstampedLine(t *testing.T) {
	d := newFakeDocker()
	c := task('a', "n1", "1", "running")
	d.setContainers(c)
	d.script(c.ID, logScript{data: []byte("hello from a tty\n"), chunk: 64})
	ss := newSinkSet()
	s, state, _ := newTestShipper(t, d, ss)
	s.now = func() time.Time { return time.Date(2026, 10, 8, 12, 0, 0, 123_456_789, time.UTC) }
	s.ApplyConfig([]SinkRoute{{ProjectID: "p1", ServiceIDs: []string{"n1"}, Sink: sinkA}})
	reconcile(t, s)
	waitFor(t, func() bool { return len(ss.get(sinkA).messages()) == 1 })
	e := ss.get(sinkA).sent()[0][0]
	if e.Time != "2026-10-08T12:00:00.123Z" || e.Message != "hello from a tty" || e.Stream != "stdout" {
		t.Fatalf("event = %+v", e)
	}
	if _, ok := state.LogsSince(c.ID); ok || s.lastRead(c.ID) != "" {
		t.Fatal("an unstamped line moved a resume point")
	}
}

func TestShipperFlushAndClose(t *testing.T) {
	d := newFakeDocker()
	c := task('a', "n1", "1", "running")
	d.setContainers(c)
	d.script(c.ID, logScript{data: stamped("2024-01-01T00:00:01Z one")})
	ss := newSinkSet()
	s, state, _ := newTestShipper(t, d, ss)
	s.flushEvery = time.Hour
	s.ApplyConfig([]SinkRoute{{ProjectID: "p1", ServiceIDs: []string{"n1"}, Sink: sinkA}})
	reconcile(t, s)
	waitFor(t, func() bool { return s.queued()[sinkKey(sinkA)] == 1 })
	s.StopFollowing()
	s.Flush(context.Background())
	if got := ss.get(sinkA).messages(); !slices.Equal(got, []string{"one"}) {
		t.Fatalf("flushed %v", got)
	}
	if v, _ := state.LogsSince(c.ID); v != "1704067201.000000001" {
		t.Fatalf("resume point after flush = %q", v)
	}
	s.Close(time.Second)
	if f := s.following(); len(f) != 0 {
		t.Fatalf("still following %v after Close", f)
	}
}
