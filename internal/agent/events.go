package agent

import (
	"context"
	"errors"
	"io"
	"strings"
	"time"
)

// Forwarder sends `docker events` to the control plane, which turns each one into a targeted
// Swarm observation. Only what the control plane can act on leaves the node: container and
// service events of `svc-*` services, and node events; exec_* and health_status chatter never
// does (docs/go/spec/swarm-worker.md §13.4).
//
// After every (re)connect an empty batch carries X-Keel-Resync: 1 so the control plane sweeps
// once: Docker replays only a small buffer, so a gap may have been missed. The stream resumes with
// `since` so nothing in the buffer is skipped either. Each relevant event is its own POST (body =
// the event JSON), in order: the stream is not read while a POST retries.
type Forwarder struct {
	Docker Docker
	Poster interface {
		PostEvents(ctx context.Context, body []byte, resync bool) bool
	}
	State *State
	Log   *Logger
	// OnContainer sees every container event (before the relevance filter); the log shipper
	// uses it to start and forget followers.
	OnContainer func(action, containerID string, attrs map[string]string)

	reconnect time.Duration // 2 s
	sleep     func(context.Context, time.Duration) error
}

// Run forwards until ctx is done.
func (f *Forwarder) Run(ctx context.Context) {
	reconnect := f.reconnect
	if reconnect == 0 {
		reconnect = 2 * time.Second
	}
	sleep := f.sleep
	if sleep == nil {
		sleep = sleepCtx
	}
	for ctx.Err() == nil {
		f.stream(ctx)
		if ctx.Err() != nil {
			return
		}
		if sleep(ctx, reconnect) != nil {
			return
		}
	}
}

// stream runs one connection to the Docker event stream, until it ends.
func (f *Forwarder) stream(ctx context.Context) {
	resync := true
	since := f.State.EventsSince()
	s, err := f.Docker.Events(ctx, since)
	if err != nil {
		if ctx.Err() == nil {
			f.Log.Log("events", "stream error: "+err.Error()+", reconnecting in 2s")
		}
		return
	}
	defer s.Close()
	if since != "" {
		f.Log.Log("events", "streaming docker events since "+since)
	} else {
		f.Log.Log("events", "streaming docker events")
	}
	// (Re)connect: ask for a full sweep before streaming anything.
	if f.Poster.PostEvents(ctx, []byte("[]"), true) {
		resync = false
	}
	for {
		e, err := s.Next()
		if err != nil {
			switch {
			case ctx.Err() != nil:
			case errors.Is(err, io.EOF):
				f.Log.Log("events", "docker events stream ended, reconnecting in 2s")
			default:
				f.Log.Log("events", "stream error: "+err.Error()+", reconnecting in 2s")
			}
			return
		}
		if e.Type == "container" && e.ActorID != "" && f.OnContainer != nil {
			attrs := e.Attributes
			if attrs == nil {
				attrs = map[string]string{}
			}
			f.OnContainer(e.Action, e.ActorID, attrs)
		}
		if relevant(e) {
			resync = !f.Poster.PostEvents(ctx, e.Raw, resync)
		}
		if e.TimeNano != 0 {
			f.State.SetEventsSince(eventsSinceAfter(e.TimeNano))
		}
	}
}

// relevant: what the control plane can act on.
func relevant(e Event) bool {
	if strings.HasPrefix(e.Action, "exec_") || strings.HasPrefix(e.Action, "health_status") {
		return false
	}
	switch e.Type {
	case "container":
		return strings.HasPrefix(e.Attributes[labelServiceName], "svc-")
	case "service":
		return strings.HasPrefix(e.Attributes["name"], "svc-")
	case "node":
		return true
	}
	return false
}
