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

type Shipper struct {
	docker  Docker
	state   *State
	log     *Logger
	newSink SinkFactory
	refresh func()

	flushEvery   time.Duration
	flushLines   int
	retryEvery   time.Duration
	maxQueue     int
	followRetry  time.Duration
	refreshEvery time.Duration
	now          func() time.Time

	followCtx     context.Context
	stopFollowing context.CancelFunc
	sendCtx       context.Context
	cancelSends   context.CancelFunc
	followersWG   sync.WaitGroup

	mu             sync.Mutex
	nodeID         string
	routes         map[string]route
	sinkByService  map[string]Sink
	sinceByService map[string]string
	readSince      map[string]string
	followers      map[string]*follower
	finished       map[string]bool
	starts         uint64
	startedAt      map[string]uint64
	queues         map[string]*queue
	flushTimer     *time.Timer
	retryTimer     *time.Timer
	lastRefresh    time.Time
	closed         bool
}

type route struct {
	sink       Sink
	serviceIDs map[string]bool
}

type entry struct {
	event     LogEvent
	container string
	since     string
}

type queue struct {
	sink     Sink
	entries  []entry
	draining chan struct{}
	room     chan struct{}
}

type follower struct{ cancel context.CancelFunc }

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

func (s *Shipper) SetNodeID(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.nodeID = id
}

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
				continue
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
			known[id] = true
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

func (s *Shipper) OnContainerEvent(action, id string, attrs map[string]string) {
	switch action {
	case "start":
		serviceID, ok := serviceIDOf(attrs)
		if !ok {
			return
		}
		s.mu.Lock()
		if _, routed := s.sinkByService[serviceID]; routed {
			s.startLocked(Container{ID: id, Labels: attrs, State: "running"})
			s.mu.Unlock()
			return
		}
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

var errUnrouted = errors.New("unrouted")

func (s *Shipper) follow(ctx context.Context, c Container, serviceID string) {
	for ctx.Err() == nil {
		s.mu.Lock()
		_, routed := s.sinkByService[serviceID]
		since, ok := s.readSince[c.ID]
		if !ok {
			since, ok = s.state.LogsSince(c.ID)
		}
		if !ok {
			since = s.sinceByService[serviceID]
		}
		s.mu.Unlock()
		if !routed {
			return
		}
		err := s.read(ctx, c, serviceID, since)
		if err == nil {
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

func (s *Shipper) read(ctx context.Context, c Container, serviceID, since string) error {
	rc, err := s.docker.ContainerLogs(ctx, c.ID, since)
	if err != nil {
		return err
	}
	defer rc.Close()
	service := c.Labels[labelServiceName]
	s.log.Log("logs", "following "+service+" ("+shortID(c.ID)+")", "since", orDefault(since, "now"))
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

func (s *Shipper) ship(ctx context.Context, containerID, serviceID string, base LogEvent, line Line) bool {
	for {
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
		if s.enqueueLine(ctx, q, containerID, base, line) {
			return true
		}
		if ctx.Err() != nil {
			return false
		}
	}
}

func (s *Shipper) enqueueLine(ctx context.Context, q *queue, containerID string, base LogEvent, line Line) bool {
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
	if ctx.Err() != nil || s.queues[q.sink.Key()] != q {
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

func (s *Shipper) StopFollowing() { s.stopFollowing() }

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
	waitAtMost(&s.followersWG, wait)
}

func serviceIDOf(labels map[string]string) (string, bool) {
	name := labels[labelServiceName]
	if !strings.HasPrefix(name, "svc-") {
		return "", false
	}
	return name[len("svc-"):], true
}

func replicaOf(taskName string) int {
	parts := strings.Split(taskName, ".")
	if len(parts) < 2 {
		return 0
	}
	f, err := strconv.ParseFloat(strings.TrimSpace(parts[1]), 64)
	if err != nil || math.IsNaN(f) || math.IsInf(f, 0) {
		return 0
	}
	return int(f)
}

func shortID(id string) string {
	if len(id) > 12 {
		return id[:12]
	}
	return id
}
