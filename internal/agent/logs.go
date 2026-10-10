package agent

import (
	"cmp"
	"context"
	"errors"
	"io"
	"maps"
	"slices"
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

	flushEvery  time.Duration
	flushLines  int
	retryEvery  time.Duration
	maxQueue    int
	followRetry time.Duration
	now         func() time.Time

	followCtx     context.Context
	stopFollowing context.CancelFunc
	sendCtx       context.Context
	cancelSends   context.CancelFunc
	followersWG   sync.WaitGroup

	mu             sync.Mutex
	nodeID         string
	queues         map[SinkConfig]*queue
	queueByService map[string]*queue
	sinceByService map[string]string
	readSince      map[string]string
	followers      map[string]*follower
	finished       map[string]bool
	starts         uint64
	startedAt      map[string]uint64
	flushTimer     *time.Timer
	retryTimer     *time.Timer
	lastRefresh    time.Time
	closed         bool
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
		followRetry: 3 * time.Second, now: time.Now,
		readSince: map[string]string{}, followers: map[string]*follower{}, finished: map[string]bool{},
		startedAt: map[string]uint64{},
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

func (s *Shipper) ApplyConfig(routes []SinkRoute) {
	s.mu.Lock()
	defer s.mu.Unlock()
	queues := map[SinkConfig]*queue{}
	byService := map[string]*queue{}
	since := map[string]string{}
	for _, r := range routes {
		q := cmp.Or(queues[r.Sink], s.queues[r.Sink])
		if q == nil {
			sink, ok := s.newSink(r.Sink)
			if !ok {
				continue
			}
			q = &queue{sink: sink}
		}
		queues[r.Sink] = q
		for _, id := range r.ServiceIDs {
			byService[id] = q
			if r.Since > 0 {
				since[id] = dockerTime(time.UnixMilli(r.Since))
			}
		}
	}
	changed := !maps.Equal(queues, s.queues) || !maps.Equal(byService, s.queueByService)
	for cfg, q := range s.queues {
		if queues[cfg] != nil {
			continue
		}
		if len(q.entries) > 0 {
			s.log.Logf("logs", "dropping %d queued lines for a removed sink", len(q.entries))
		}
		q.entries = nil
		wakeRoom(q)
	}
	s.queues, s.queueByService, s.sinceByService = queues, byService, since
	if changed {
		s.log.Logf("logs", "config applied: sinks=%d services=%d", len(queues), len(byService))
	}
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
		_, resumable := s.state.LogsSince(c.ID)
		pending := resumable && !s.finished[c.ID]
		if ok && s.queueByService[serviceID] != nil && (c.State == "running" || pending) {
			s.startLocked(c, serviceID)
			continue
		}
		s.stopLocked(c.ID, false)
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
	maps.DeleteFunc(s.finished, func(id string, _ bool) bool { return !known[id] })
	maps.DeleteFunc(s.startedAt, func(id string, _ uint64) bool { return !known[id] })
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
		if s.queueByService[serviceID] != nil {
			s.startLocked(Container{ID: id, Labels: attrs}, serviceID)
			s.mu.Unlock()
			return
		}
		now := s.now()
		if now.Sub(s.lastRefresh) <= 5*time.Second {
			s.mu.Unlock()
			return
		}
		s.lastRefresh = now
		s.mu.Unlock()
		s.refresh()
	case "destroy":
		s.mu.Lock()
		s.stopLocked(id, true)
		s.mu.Unlock()
	}
}

func (s *Shipper) startLocked(c Container, serviceID string) {
	if _, ok := s.followers[c.ID]; ok || s.closed {
		return
	}
	ctx, cancel := context.WithCancel(s.followCtx)
	f := &follower{cancel: cancel}
	s.followers[c.ID] = f
	s.starts++
	s.startedAt[c.ID] = s.starts
	s.followersWG.Go(func() {
		defer cancel()
		s.follow(ctx, c, serviceID)
		s.mu.Lock()
		if s.followers[c.ID] == f {
			delete(s.followers, c.ID)
		}
		s.mu.Unlock()
	})
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
		routed := s.queueByService[serviceID] != nil
		persisted, _ := s.state.LogsSince(c.ID)
		since := cmp.Or(s.readSince[c.ID], persisted, s.sinceByService[serviceID])
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
		s.log.Logf("logs", "follow %s failed (%s), retry in 3s", shortID(c.ID), errorText(err))
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
	s.log.Logf("logs", "following %s (%s) since %s", service, shortID(c.ID), cmp.Or(since, "now"))
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

func (s *Shipper) ship(ctx context.Context, containerID, serviceID string, ev LogEvent, line Line) bool {
	ev.Message, ev.Stream, ev.Time = line.Text, line.Stream, cmp.Or(line.Time, s.now()).UTC()
	since := ""
	if !line.Time.IsZero() {
		since = dockerTime(line.Time.Add(time.Nanosecond))
	}
	for {
		s.mu.Lock()
		q := s.queueByService[serviceID]
		if q == nil || ctx.Err() != nil {
			s.mu.Unlock()
			return false
		}
		if len(q.entries) < s.maxQueue {
			ev.Node = s.nodeID
			if since != "" {
				s.readSince[containerID] = since
			}
			s.enqueueLocked(q, entry{event: ev, container: containerID, since: since})
			s.mu.Unlock()
			return true
		}
		if q.room == nil {
			q.room = make(chan struct{})
		}
		room := q.room
		s.mu.Unlock()
		select {
		case <-room:
		case <-ctx.Done():
			return false
		}
	}
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
		return
	}
	if s.flushTimer == nil && !s.closed {
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
		if len(q.entries) == 0 {
			q.draining = nil
			s.mu.Unlock()
			return
		}
		n := min(s.flushLines, len(q.entries))
		batch := slices.Clone(q.entries[:n])
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
		for _, e := range batch {
			s.state.Checkpoint(e.container, e.since)
		}
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
	}
	if s.retryTimer != nil {
		s.retryTimer.Stop()
	}
	s.mu.Unlock()
	waitAtMost(&s.followersWG, wait)
}

func serviceIDOf(labels map[string]string) (string, bool) {
	return strings.CutPrefix(labels[labelServiceName], "svc-")
}

func replicaOf(taskName string) int {
	_, rest, _ := strings.Cut(taskName, ".")
	slot, _, _ := strings.Cut(rest, ".")
	n, _ := strconv.Atoi(slot)
	return n
}

func shortID(id string) string { return id[:min(len(id), 12)] }
