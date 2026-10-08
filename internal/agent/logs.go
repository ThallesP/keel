package agent

import (
	"context"
	"errors"
	"io"
	"math"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Shipper streams every `svc-*` container's stdout/stderr on this node to the sink of the project
// it belongs to (docs/go/spec/observability.md §11.4–§11.7). `docker logs --follow` per container:
// Docker has no per-node "all containers" stream, and a logging driver would put the sink in every
// service spec and restart every task whenever it changes. One follower per container, started on
// `container start` events and on every config poll, stopped when the container is removed or the
// project loses its sink.
//
// Delivery: a line's resume point (State.logsSince) is saved only once the sink accepted the batch
// it was in, so a restart re-reads anything not delivered yet from Docker's own log file instead
// of skipping it. A sink that stays down backs up its queue; at maxQueue the followers feeding it
// stop reading until it drains, and Docker's file is the buffer. Nothing is dropped on this side;
// only a sink that rejects a batch as malformed (4xx) discards it, and a removed sink drops what
// was still queued for it.
type Shipper struct {
	docker  Docker
	state   *State
	log     *Logger
	newSink SinkFactory
	refresh func() // asks the config loop for an early poll

	flushEvery   time.Duration // FLUSH_MS 1 s
	flushLines   int           // FLUSH_LINES 500
	retryEvery   time.Duration // RETRY_MS 5 s, between attempts at a batch the sink could not take
	maxQueue     int           // MAX_QUEUE 20 000 per sink; beyond this its followers wait
	followRetry  time.Duration // 3 s after a failed follow
	refreshEvery time.Duration // 5 s between early config polls
	now          func() time.Time

	followCtx     context.Context // parent of every follower; cancelled by StopFollowing
	stopFollowing context.CancelFunc
	sendCtx       context.Context // sink sends; outlives the followers so the shutdown flush delivers
	cancelSends   context.CancelFunc
	followersWG   sync.WaitGroup

	mu     sync.Mutex
	nodeID string
	// projectID → route. Rebuilt on every config poll; sinks are reused when their key matches.
	routes        map[string]route
	sinkByService map[string]Sink
	// serviceID → Docker `since` of the moment its project's sink was connected (config since).
	sinceByService map[string]string
	// Per container, the line last read in this process. A tail that is re-opened (the socket
	// idles out, Docker hiccups) resumes here: what was read is queued or delivered already.
	// Across process restarts the persisted, delivery-confirmed State.logsSince is used instead.
	readSince map[string]string
	followers map[string]*follower
	// Containers read to EOF after they exited; nothing left to fetch until they are removed.
	finished map[string]bool
	// starts counts follower starts; startedAt is the count at a container's last start, so a
	// reconcile can tell containers that started while its list was in flight.
	starts    uint64
	startedAt map[string]uint64
	// One queue per sink key, drained in order so a slow sink never fans out into parallel
	// retries; equal sinks of several projects share one.
	queues      map[string]*queue
	flushTimer  *time.Timer
	retryTimer  *time.Timer
	lastRefresh time.Time
	closed      bool
}

type route struct {
	sink       Sink
	serviceIDs map[string]bool
}

type entry struct {
	event     LogEvent
	container string // full container id
	since     string // resume point after this line ("" when the line had no stamp)
}

type queue struct {
	sink     Sink
	entries  []entry
	draining chan struct{} // non-nil while a drain runs; closed when it returns
	room     chan struct{} // closed to release the followers waiting for room
}

type follower struct{ cancel context.CancelFunc }

// NewShipper builds a shipper. refresh may be nil.
func NewShipper(docker Docker, state *State, log *Logger, newSink SinkFactory, refresh func()) *Shipper {
	s := &Shipper{
		docker: docker, state: state, log: log, newSink: newSink, refresh: refresh,
		flushEvery: time.Second, flushLines: 500, retryEvery: 5 * time.Second, maxQueue: 20_000,
		followRetry: 3 * time.Second, refreshEvery: 5 * time.Second, now: time.Now,
		routes: map[string]route{}, sinkByService: map[string]Sink{}, sinceByService: map[string]string{},
		readSince: map[string]string{}, followers: map[string]*follower{}, finished: map[string]bool{},
		startedAt: map[string]uint64{}, queues: map[string]*queue{},
	}
	s.followCtx, s.stopFollowing = context.WithCancel(context.Background())
	s.sendCtx, s.cancelSends = context.WithCancel(context.Background())
	return s
}

// SetNodeID sets the Swarm node id every event carries.
func (s *Shipper) SetNodeID(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.nodeID = id
}

// ApplyConfig rebuilds the routing from a config poll and reports whether it changed. A sink
// nobody routes to any more (disconnected, or its token changed) cannot take what is still queued
// for it: the lines go with it, and followers waiting on it move on.
func (s *Shipper) ApplyConfig(sinks []SinkRoute) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	next := map[string]route{}
	nextByService := map[string]Sink{}
	nextSince := map[string]string{}
	existing := map[string]Sink{}
	for _, r := range s.routes {
		existing[r.sink.Key()] = r.sink
	}
	for _, sr := range sinks {
		key := sinkKey(sr.Sink)
		sink, ok := existing[key]
		if !ok {
			built, known := s.newSink(sr.Sink)
			if !known {
				continue // a kind this agent does not ship to
			}
			sink = built
			existing[key] = sink
		}
		ids := make(map[string]bool, len(sr.ServiceIDs))
		for _, id := range sr.ServiceIDs {
			ids[id] = true
			nextByService[id] = sink
			if sr.Since != nil {
				nextSince[id] = dockerSince(*sr.Since)
			}
		}
		next[sr.ProjectID] = route{sink: sink, serviceIDs: ids}
	}
	changed := len(next) != len(s.routes) || len(nextByService) != len(s.sinkByService)
	for p, r := range next {
		if old, ok := s.routes[p]; !ok || old.sink.Key() != r.sink.Key() {
			changed = true
		}
	}
	for id, sink := range nextByService {
		if old, ok := s.sinkByService[id]; !ok || old.Key() != sink.Key() {
			changed = true
		}
	}
	s.routes, s.sinkByService, s.sinceByService = next, nextByService, nextSince
	used := map[string]bool{}
	for _, r := range next {
		used[r.sink.Key()] = true
	}
	for key, q := range s.queues {
		if used[key] {
			continue
		}
		delete(s.queues, key)
		if len(q.entries) > 0 {
			s.log.Log("logs", "dropping "+strconv.Itoa(len(q.entries))+" queued lines for a removed sink")
		}
		wakeRoom(q)
	}
	if changed {
		s.log.Log("logs", "config applied", "sinks", len(next), "services", len(nextByService))
	}
	return changed
}

// ReconcileFollowers re-scans the containers: follows running ones routed to a sink, finishes
// reading exited ones a restart left undelivered (they have a resume point and were not read to
// EOF yet), stops the rest, and forgets the resume points of containers Docker no longer has.
//
// A container that a `start` event began following while the list was in flight is not in the
// list: it is left alone until the next reconcile (the worker stopped it and forgot its resume
// points, so it was not read until the next poll and then re-read from the sink's connect time).
func (s *Shipper) ReconcileFollowers(ctx context.Context) error {
	s.mu.Lock()
	listed := s.starts
	s.mu.Unlock()
	containers, err := s.docker.ListSwarmContainers(ctx)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	known := make(map[string]bool, len(containers))
	for id, seq := range s.startedAt {
		if seq > listed {
			known[id] = true // younger than the list
		}
	}
	for _, c := range containers {
		known[c.ID] = true
		serviceID, ok := serviceIDOf(c.Labels)
		_, routed := s.sinkByService[serviceID]
		routed = ok && routed
		_, resumable := s.state.LogsSince(c.ID)
		pending := resumable && !s.finished[c.ID]
		if routed && (c.State == "running" || pending) {
			s.startLocked(c)
		} else {
			// Unrouted (the project dropped its sink): a follower returns on its own at its
			// next line; stop it now so an idle container does not keep a socket open.
			s.stopLocked(c.ID, false)
		}
	}
	for id := range s.followers {
		if !known[id] {
			s.stopLocked(id, true)
		}
	}
	for _, id := range s.state.LogsSinceIDs() {
		if !known[id] {
			s.stopLocked(id, true)
		}
	}
	for id := range s.finished {
		if !known[id] {
			delete(s.finished, id)
		}
	}
	for id := range s.startedAt {
		if !known[id] {
			delete(s.startedAt, id)
		}
	}
	return nil
}

// OnContainerEvent is the event forwarder's hook: a new svc-* container is followed, a removed
// one forgotten. A container that dies ends its own stream (EOF), and what it wrote stays queued
// until delivered.
func (s *Shipper) OnContainerEvent(action, id string, attrs map[string]string) {
	switch action {
	case "start":
		serviceID, ok := serviceIDOf(attrs)
		if !ok {
			return
		}
		s.mu.Lock()
		if _, routed := s.sinkByService[serviceID]; routed {
			// Labels on the event are the container's; enough to route without an inspect.
			s.startLocked(Container{ID: id, Labels: attrs, State: "running"})
			s.mu.Unlock()
			return
		}
		// Unrouted: its project may have connected a sink since the last poll. Ask for the config
		// now so the container's first lines are not a poll interval behind; the tail then starts
		// from the connect time, before the container did, so nothing is missed meanwhile.
		now := s.now()
		early := now.Sub(s.lastRefresh) > s.refreshEvery
		if early {
			s.lastRefresh = now
		}
		s.mu.Unlock()
		if early && s.refresh != nil {
			s.refresh()
		}
	case "destroy":
		s.mu.Lock()
		s.stopLocked(id, true)
		s.mu.Unlock()
	}
}

// startLocked follows a container unless it is followed already or not routed.
func (s *Shipper) startLocked(c Container) {
	if _, ok := s.followers[c.ID]; ok || s.closed {
		return
	}
	serviceID, ok := serviceIDOf(c.Labels)
	if !ok {
		return
	}
	if _, routed := s.sinkByService[serviceID]; !routed {
		return
	}
	ctx, cancel := context.WithCancel(s.followCtx)
	f := &follower{cancel: cancel}
	s.followers[c.ID] = f
	s.starts++
	s.startedAt[c.ID] = s.starts
	s.followersWG.Add(1)
	go func() {
		defer s.followersWG.Done()
		defer cancel()
		s.follow(ctx, c, serviceID)
		s.mu.Lock()
		if s.followers[c.ID] == f {
			delete(s.followers, c.ID)
		}
		s.mu.Unlock()
	}()
}

// stopLocked stops following. The resume point stays (lines may still be queued, and an exited
// container can be read again) unless forget: the container is gone and so is its log.
func (s *Shipper) stopLocked(id string, forget bool) {
	if f := s.followers[id]; f != nil {
		f.cancel()
	}
	delete(s.followers, id)
	if !forget {
		return
	}
	delete(s.finished, id)
	delete(s.readSince, id)
	delete(s.startedAt, id)
	s.state.Forget(id)
}

// errUnrouted ends a follower whose service lost its sink, or that was stopped.
var errUnrouted = errors.New("unrouted")

func (s *Shipper) follow(ctx context.Context, c Container, serviceID string) {
	for ctx.Err() == nil {
		s.mu.Lock()
		_, routed := s.sinkByService[serviceID]
		// In this process → delivered before a restart → the sink's connect time → new lines only.
		since, ok := s.readSince[c.ID]
		if !ok {
			since, ok = s.state.LogsSince(c.ID)
		}
		if !ok {
			since = s.sinceByService[serviceID]
		}
		s.mu.Unlock()
		if !routed {
			return // the project lost its sink between the check and here
		}
		err := s.read(ctx, c, serviceID, since)
		if err == nil {
			// EOF: the container exited. Its lines are queued; the resume point catches up as
			// they are delivered and goes when the container is removed.
			s.mu.Lock()
			s.finished[c.ID] = true
			s.mu.Unlock()
			return
		}
		if errors.Is(err, errUnrouted) || ctx.Err() != nil {
			return
		}
		s.log.Log("logs", "follow "+shortID(c.ID)+" failed ("+errorText(err)+"), retry in 3s")
		if sleepCtx(ctx, s.followRetry) != nil {
			return
		}
	}
}

// read tails one container from since until EOF (nil), an error, or errUnrouted.
func (s *Shipper) read(ctx context.Context, c Container, serviceID, since string) error {
	rc, err := s.docker.ContainerLogs(ctx, c.ID, since)
	if err != nil {
		return err
	}
	defer rc.Close()
	service := c.Labels[labelServiceName]
	logged := since
	if logged == "" {
		logged = "now"
	}
	s.log.Log("logs", "following "+service+" ("+shortID(c.ID)+")", "since", logged)
	base := LogEvent{
		ServiceID: serviceID,
		Service:   service,
		Task:      c.Labels[labelTaskID],
		Replica:   replicaOf(c.Labels[labelTaskName]),
		Container: shortID(c.ID),
	}
	var frames FrameParser
	var lines LineSplitter
	buf := make([]byte, 32<<10)
	for {
		n, rerr := rc.Read(buf)
		for _, frame := range frames.Push(buf[:n]) {
			for _, line := range lines.Push(frame) {
				if !s.ship(ctx, c.ID, serviceID, base, line) {
					return errUnrouted
				}
			}
		}
		if errors.Is(rerr, io.EOF) {
			return nil
		}
		if rerr != nil {
			return rerr
		}
	}
}

// ship queues one line for its service's current sink, waiting for room first. false when the
// service lost its sink or the follower was stopped.
func (s *Shipper) ship(ctx context.Context, containerID, serviceID string, base LogEvent, line Line) bool {
	s.mu.Lock()
	sink := s.sinkByService[serviceID]
	if sink == nil {
		s.mu.Unlock()
		return false
	}
	q := s.queueForLocked(sink)
	s.mu.Unlock()
	if !s.waitForRoom(ctx, q) {
		return false
	}
	ev := base
	ev.Message = line.Text
	ev.Stream = line.Stream
	ev.Time = line.Time
	if ev.Time == "" {
		ev.Time = isoMillis(s.now())
	}
	after := ""
	if line.Time != "" {
		after = sinceAfter(line.Time)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if ctx.Err() != nil {
		return false
	}
	ev.Node = s.nodeID
	if after != "" {
		s.readSince[containerID] = after
	}
	s.enqueueLocked(q, entry{event: ev, container: containerID, since: after})
	return true
}

func (s *Shipper) queueForLocked(sink Sink) *queue {
	q := s.queues[sink.Key()]
	if q == nil {
		q = &queue{sink: sink}
		s.queues[sink.Key()] = q
	}
	return q
}

// waitForRoom returns once the queue has room, or false when the follower is stopped meanwhile.
// Like the worker, a woken follower does not re-check: the drain woke it below half capacity, or
// the queue was removed (its next line goes to whatever sink the service has then).
func (s *Shipper) waitForRoom(ctx context.Context, q *queue) bool {
	s.mu.Lock()
	if len(q.entries) < s.maxQueue || ctx.Err() != nil {
		s.mu.Unlock()
		return ctx.Err() == nil
	}
	if q.room == nil {
		q.room = make(chan struct{})
	}
	room := q.room
	s.mu.Unlock()
	select {
	case <-room:
	case <-ctx.Done():
	}
	return ctx.Err() == nil
}

func wakeRoom(q *queue) {
	if q.room != nil {
		close(q.room)
		q.room = nil
	}
}

func (s *Shipper) enqueueLocked(q *queue, e entry) {
	q.entries = append(q.entries, e)
	if len(q.entries) >= s.flushLines {
		s.flushLocked()
	} else if s.flushTimer == nil && !s.closed {
		s.flushTimer = time.AfterFunc(s.flushEvery, s.flush)
	}
}

func (s *Shipper) flush() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.flushLocked()
}

// flushLocked starts a drain for every queue with entries and none in progress.
func (s *Shipper) flushLocked() {
	if s.flushTimer != nil {
		s.flushTimer.Stop()
		s.flushTimer = nil
	}
	if s.closed {
		return
	}
	for _, q := range s.queues {
		if len(q.entries) == 0 || q.draining != nil {
			continue
		}
		done := make(chan struct{})
		q.draining = done
		go s.drain(q, done)
	}
}

// drain sends the queue in order, flushLines per request. A batch the sink could not take goes
// back to the front and is retried after retryEvery; its resume points stay where they were.
func (s *Shipper) drain(q *queue, done chan struct{}) {
	defer close(done)
	for {
		s.mu.Lock()
		if len(q.entries) == 0 || s.queues[q.sink.Key()] != q {
			q.draining = nil
			s.mu.Unlock()
			return
		}
		n := min(s.flushLines, len(q.entries))
		batch := append([]entry(nil), q.entries[:n]...)
		q.entries = q.entries[n:]
		s.mu.Unlock()

		events := make([]LogEvent, len(batch))
		for i, e := range batch {
			events[i] = e.event
		}
		ok := q.sink.Send(s.sendCtx, events)

		s.mu.Lock()
		if !ok {
			q.entries = append(batch, q.entries...)
			if s.retryTimer == nil && !s.closed {
				s.retryTimer = time.AfterFunc(s.retryEvery, func() {
					s.mu.Lock()
					defer s.mu.Unlock()
					s.retryTimer = nil
					s.flushLocked()
				})
			}
			q.draining = nil
			s.mu.Unlock()
			return
		}
		// The sink has these lines: per container, where a restart resumes from.
		points := make([]resumePoint, 0, len(batch))
		for _, e := range batch {
			points = append(points, resumePoint{container: e.container, since: e.since})
		}
		s.state.Checkpoint(points)
		if len(q.entries) < s.maxQueue/2 {
			wakeRoom(q)
		}
		s.mu.Unlock()
	}
}

// StopFollowing stops every follower (shutdown: nothing new is read while the queues flush).
func (s *Shipper) StopFollowing() { s.stopFollowing() }

// Flush sends whatever is queued and waits for the drains in progress, or until ctx is done.
func (s *Shipper) Flush(ctx context.Context) {
	s.mu.Lock()
	s.flushLocked()
	var waits []chan struct{}
	for _, q := range s.queues {
		if q.draining != nil {
			waits = append(waits, q.draining)
		}
	}
	s.mu.Unlock()
	for _, w := range waits {
		select {
		case <-w:
		case <-ctx.Done():
			return
		}
	}
}

// Close aborts in-flight sends and timers and waits (at most wait) for the followers to return.
func (s *Shipper) Close(wait time.Duration) {
	s.stopFollowing()
	s.cancelSends()
	s.mu.Lock()
	s.closed = true
	if s.flushTimer != nil {
		s.flushTimer.Stop()
		s.flushTimer = nil
	}
	if s.retryTimer != nil {
		s.retryTimer.Stop()
		s.retryTimer = nil
	}
	s.mu.Unlock()
	done := make(chan struct{})
	go func() {
		s.followersWG.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(wait):
	}
}

// serviceIDOf is the node id of a Swarm task container: its service name minus "svc-".
func serviceIDOf(labels map[string]string) (string, bool) {
	name := labels[labelServiceName]
	if !strings.HasPrefix(name, "svc-") {
		return "", false
	}
	return name[len("svc-"):], true
}

// replicaOf is the Swarm slot from a task name svc-<id>.<slot>.<taskId>, 0 when there is none
// (JavaScript: Number(name.split(".")[1] ?? 0) || 0).
func replicaOf(taskName string) int {
	parts := strings.Split(taskName, ".")
	if len(parts) < 2 {
		return 0
	}
	v := strings.TrimSpace(parts[1])
	if v == "" {
		return 0
	}
	f, err := strconv.ParseFloat(v, 64)
	if err != nil || math.IsNaN(f) || math.IsInf(f, 0) {
		return 0
	}
	return int(f)
}

// shortID is the 12-character container id.
func shortID(id string) string {
	if len(id) > 12 {
		return id[:12]
	}
	return id
}
